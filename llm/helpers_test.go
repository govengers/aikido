package llm

import (
	"context"
	"errors"
	"testing"
)

// scriptedClient is a tiny in-package Client for testing the helpers; the
// llmtest stub cannot be imported here without an import cycle.
type scriptedClient struct {
	events []Event
	err    error
}

func (c *scriptedClient) Stream(context.Context, Request) (<-chan Event, error) {
	if c.err != nil {
		return nil, c.err
	}
	events := make(chan Event, len(c.events))
	for _, event := range c.events {
		events <- event
	}
	close(events)
	return events, nil
}

func (c *scriptedClient) Complete(ctx context.Context, request Request) (Response, error) {
	return Collect(ctx, c, request)
}

func TestCollect_HappyPath(t *testing.T) {
	client := &scriptedClient{events: []Event{
		{Kind: EventTextDelta, Text: "Hello, "},
		{Kind: EventTextDelta, Text: "world."},
		{Kind: EventToolCall, Tool: &ToolCall{ID: "c1", Name: "fn", Arguments: `{"a":1}`}},
		{Kind: EventUsage, Usage: &Usage{PromptTokens: 4, CompletionTokens: 7}},
		{Kind: EventEnd, FinishReason: "tool_calls"},
	}}
	response, err := Collect(t.Context(), client, Request{})
	text, calls, images, usage := response.Text, response.ToolCalls, response.Images, response.Usage
	if response.FinishReason != "tool_calls" {
		t.Errorf("finish reason = %q, want tool_calls", response.FinishReason)
	}
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if text != "Hello, world." {
		t.Errorf("text = %q, want %q", text, "Hello, world.")
	}
	if len(calls) != 1 || calls[0].ID != "c1" || calls[0].Arguments != `{"a":1}` {
		t.Errorf("calls = %+v", calls)
	}
	if len(images) != 0 {
		t.Errorf("images = %+v, want none", images)
	}
	if usage == nil || usage.PromptTokens != 4 || usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestCollect_Images(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	client := &scriptedClient{events: []Event{
		{Kind: EventTextDelta, Text: "here it is"},
		{Kind: EventImage, Image: &ImagePart{ContentType: "image/png", Data: pngBytes}},
		{Kind: EventImage, Image: &ImagePart{URL: "https://example.com/x.png", ContentType: "image/png"}},
		{Kind: EventEnd},
	}}
	response, err := Collect(t.Context(), client, Request{})
	text, images := response.Text, response.Images
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if text != "here it is" {
		t.Errorf("text = %q", text)
	}
	if len(images) != 2 {
		t.Fatalf("images len = %d, want 2", len(images))
	}
	if string(images[0].Data) != string(pngBytes) || images[0].ContentType != "image/png" {
		t.Errorf("images[0] = %+v", images[0])
	}
	if images[1].URL != "https://example.com/x.png" || len(images[1].Data) != 0 {
		t.Errorf("images[1] = %+v", images[1])
	}
}

func TestCollect_ThinkingExcludedFromText(t *testing.T) {
	client := &scriptedClient{events: []Event{
		{Kind: EventThinking, Text: "let me think about this"},
		{Kind: EventTextDelta, Text: "answer"},
		{Kind: EventThinking, Text: "more thinking"},
		{Kind: EventEnd},
	}}
	response, err := Collect(t.Context(), client, Request{})
	text := response.Text
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if text != "answer" {
		t.Errorf("text = %q, want %q (thinking must not be included)", text, "answer")
	}
}

func TestCollect_PropagatesEventError(t *testing.T) {
	sentinel := errors.New("provider blew up")
	client := &scriptedClient{events: []Event{
		{Kind: EventTextDelta, Text: "partial"},
		{Kind: EventError, Err: sentinel},
		{Kind: EventEnd},
	}}
	response, err := Collect(t.Context(), client, Request{})
	text := response.Text
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
	if text != "partial" {
		t.Errorf("text = %q, want %q", text, "partial")
	}
}

func TestCollect_PropagatesStreamError(t *testing.T) {
	sentinel := errors.New("dial failure")
	client := &scriptedClient{err: sentinel}
	_, err := Collect(t.Context(), client, Request{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want %v", err, sentinel)
	}
}

func TestCollect_ContextCancellation(t *testing.T) {
	client := &silentClient{events: make(chan Event)} // never closed; the producer never sends
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Collect(ctx, client, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

type silentClient struct{ events chan Event }

func (c *silentClient) Stream(context.Context, Request) (<-chan Event, error) {
	return c.events, nil
}

func (c *silentClient) Complete(ctx context.Context, _ Request) (Response, error) {
	<-ctx.Done()
	return Response{}, ctx.Err()
}
