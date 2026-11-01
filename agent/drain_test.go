package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/tools"
)

func TestDrain_HappyPath_SingleTurn(t *testing.T) {
	events := make(chan Event, 8)
	events <- Event{Kind: EventText, Text: "Hello "}
	events <- Event{Kind: EventText, Text: "world"}
	events <- Event{Kind: EventEnd, EndReason: EndReasonStop}
	close(events)

	messages, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d; want 1", len(messages))
	}
	if messages[0].Role != llm.RoleAssistant || messages[0].Content != "Hello world" {
		t.Errorf("messages[0] = %+v", messages[0])
	}
}

func TestDrain_WithToolCallsAndResults(t *testing.T) {
	events := make(chan Event, 16)
	events <- Event{Kind: EventText, Text: "calling tools"}
	events <- Event{Kind: EventToolCall, ToolCall: &llm.ToolCall{ID: "c1", Name: "x", Arguments: `{}`}}
	events <- Event{Kind: EventToolCall, ToolCall: &llm.ToolCall{ID: "c2", Name: "y", Arguments: `{}`}}
	events <- Event{Kind: EventToolResult, ToolResult: &ToolResult{CallID: "c1", Name: "x", OK: true, Content: map[string]any{"r": 1}}}
	events <- Event{Kind: EventToolResult, ToolResult: &ToolResult{CallID: "c2", Name: "y", OK: false, Error: "oops"}}
	events <- Event{Kind: EventText, Text: "done"}
	events <- Event{Kind: EventEnd, EndReason: EndReasonStop}
	close(events)

	messages, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Expect: assistant1(text+2 calls), tool(c1), tool(c2), assistant2(done)
	if len(messages) != 4 {
		t.Fatalf("len(messages) = %d; want 4. messages=%+v", len(messages), messages)
	}
	if messages[0].Role != llm.RoleAssistant || messages[0].Content != "calling tools" || len(messages[0].ToolCalls) != 2 {
		t.Errorf("messages[0] = %+v", messages[0])
	}
	if messages[1].Role != llm.RoleTool || messages[1].ToolCallID != "c1" {
		t.Errorf("messages[1] = %+v", messages[1])
	}
	if messages[2].Role != llm.RoleTool || messages[2].ToolCallID != "c2" || !strings.Contains(messages[2].Content, "oops") {
		t.Errorf("messages[2] = %+v", messages[2])
	}
	if messages[3].Role != llm.RoleAssistant || messages[3].Content != "done" {
		t.Errorf("messages[3] = %+v", messages[3])
	}
}

func TestDrain_FirstError(t *testing.T) {
	events := make(chan Event, 4)
	first := errors.New("first")
	second := errors.New("second")
	events <- Event{Kind: EventError, Err: first}
	events <- Event{Kind: EventError, Err: second}
	events <- Event{Kind: EventEnd, EndReason: EndReasonError}
	close(events)
	_, err := Drain(events)
	if !errors.Is(err, first) {
		t.Errorf("err = %v; want %v", err, first)
	}
}

func TestDrain_AssistantImagesAccumulated(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}
	events := make(chan Event, 8)
	events <- Event{Kind: EventText, Text: "here it is"}
	events <- Event{Kind: EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: pngBytes}}
	events <- Event{Kind: EventImage, Image: &llm.ImagePart{URL: "https://example.com/x.png"}}
	events <- Event{Kind: EventEnd, EndReason: EndReasonStop}
	close(events)

	messages, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	got := messages[0]
	if got.Role != llm.RoleAssistant || got.Content != "here it is" {
		t.Errorf("message = %+v", got)
	}
	if len(got.Images) != 2 {
		t.Fatalf("Images = %d, want 2", len(got.Images))
	}
	if string(got.Images[0].Data) != string(pngBytes) || got.Images[0].ContentType != "image/png" {
		t.Errorf("Images[0] = %+v", got.Images[0])
	}
	if got.Images[1].URL != "https://example.com/x.png" {
		t.Errorf("Images[1] = %+v", got.Images[1])
	}
}

func TestRunWithMessages_ImagesPropagateThroughAgent(t *testing.T) {
	pngBytes := []byte{0x89, 0x50, 0x4e, 0x47}
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "drawing"},
		{Kind: llm.EventImage, Image: &llm.ImagePart{ContentType: "image/png", Data: pngBytes}},
		{Kind: llm.EventEnd},
	}})
	session, err := NewLocalSession(&SessionOptions{
		ID: "img", Client: stub, Model: "image-model",
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	events, _ := session.RunWithMessages(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "draw a koan"},
	})
	produced, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(produced) != 1 {
		t.Fatalf("produced = %d, want 1", len(produced))
	}
	assistant := produced[0]
	if assistant.Role != llm.RoleAssistant || assistant.Content != "drawing" {
		t.Errorf("assistant = %+v", assistant)
	}
	if len(assistant.Images) != 1 || string(assistant.Images[0].Data) != string(pngBytes) {
		t.Errorf("assistant.Images = %+v", assistant.Images)
	}
}

func TestRunWithMessages_DrainEndToEnd(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "answer"},
		{Kind: llm.EventEnd},
	}})
	session, err := NewLocalSession(&SessionOptions{
		ID: "rwm", Client: stub, Model: "m", SystemPrompt: "be brief",
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	history := []llm.Message{{Role: llm.RoleUser, Content: "what is 2+2?"}}
	events, _ := session.RunWithMessages(context.Background(), history)
	produced, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(produced) != 1 || produced[0].Role != llm.RoleAssistant || produced[0].Content != "answer" {
		t.Errorf("produced = %+v", produced)
	}
	// RunWithMessages must not write to History.
	stored := storedMessages(t, session.options.History, "rwm")
	if len(stored) != 0 {
		t.Errorf("History should not be touched by RunWithMessages, got %+v", stored)
	}
}

func TestRunWithMessages_ToolDispatchOnPassedHistory(t *testing.T) {
	registry := tools.NewRegistry()
	mustRegister(t, registry, "ping", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		return tools.Result{Content: "pong"}, nil
	})
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c1", Name: "ping"}},
			{Kind: llm.EventEnd},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "pong received"},
			{Kind: llm.EventEnd},
		}},
	)
	session, err := NewLocalSession(&SessionOptions{
		ID: "x", Client: stub, Model: "m", Tools: registry,
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	events, _ := session.RunWithMessages(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "ping it"},
	})
	messages, err := Drain(events)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Expected: asst1 (with toolCall), tool(c1), asst2 (text)
	if len(messages) != 3 {
		t.Fatalf("len = %d; want 3. messages=%+v", len(messages), messages)
	}
}
