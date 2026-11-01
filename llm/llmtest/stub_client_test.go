package llmtest

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/goleak"

	"github.com/govengers/aikido/llm"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestStubClient_Replay(t *testing.T) {
	stub := NewStubClient(TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "hi"},
		{Kind: llm.EventEnd},
	}})
	events, err := stub.Stream(t.Context(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got []llm.EventKind
	for event := range events {
		got = append(got, event.Kind)
	}
	if len(got) != 2 || got[0] != llm.EventTextDelta || got[1] != llm.EventEnd {
		t.Errorf("got %v", got)
	}
}

func TestStubClient_Exhausted(t *testing.T) {
	stub := NewStubClient(TurnScript{Events: []llm.Event{{Kind: llm.EventEnd}}})
	if _, err := stub.Stream(t.Context(), llm.Request{}); err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	_, err := stub.Stream(t.Context(), llm.Request{})
	if !errors.Is(err, ErrStubExhausted) {
		t.Errorf("err = %v; want ErrStubExhausted", err)
	}
}

func TestStubClient_RequestsCaptured(t *testing.T) {
	stub := NewStubClient(
		TurnScript{Events: []llm.Event{{Kind: llm.EventEnd}}},
		TurnScript{Events: []llm.Event{{Kind: llm.EventEnd}}},
	)
	_, _ = stub.Stream(t.Context(), llm.Request{Model: "m1"})
	_, _ = stub.Stream(t.Context(), llm.Request{Model: "m2"})
	requests := stub.Requests()
	if len(requests) != 2 || requests[0].Model != "m1" || requests[1].Model != "m2" {
		t.Errorf("requests = %+v", requests)
	}
}

func TestStubClient_GenerateImage_ScriptsAndRecords(t *testing.T) {
	stub := NewStubClient()
	want := llm.ImageResponse{
		Images: []llm.ImagePart{{ContentType: "image/png", Data: []byte("png-bytes")}},
		Usage:  &llm.Usage{CostUSD: 0.0053},
	}
	boom := errors.New("provider blew up")
	stub.ScriptImages(
		ImageScript{Response: want},
		ImageScript{Err: boom},
	)

	got, err := stub.GenerateImage(t.Context(), llm.ImageRequest{Model: "openai/gpt-image-2.5-flare", Prompt: "a red cube"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if len(got.Images) != 1 || string(got.Images[0].Data) != "png-bytes" {
		t.Errorf("images = %+v", got.Images)
	}
	if got.Usage == nil || got.Usage.CostUSD != 0.0053 {
		t.Errorf("usage = %+v", got.Usage)
	}

	if _, err := stub.GenerateImage(t.Context(), llm.ImageRequest{Model: "m", Prompt: "p"}); !errors.Is(err, boom) {
		t.Errorf("second call err = %v, want the scripted error", err)
	}
	if _, err := stub.GenerateImage(t.Context(), llm.ImageRequest{Model: "m", Prompt: "p"}); !errors.Is(err, ErrStubExhausted) {
		t.Errorf("third call err = %v, want ErrStubExhausted", err)
	}

	// The exhausted call is not recorded, matching Stream.
	requests := stub.ImageRequests()
	if len(requests) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(requests))
	}
	if requests[0].Model != "openai/gpt-image-2.5-flare" || requests[0].Prompt != "a red cube" {
		t.Errorf("first request = %+v", requests[0])
	}
}

func TestStubClient_Speak_ScriptsAndRecords(t *testing.T) {
	stub := NewStubClient()
	boom := errors.New("provider blew up")
	stub.ScriptSpeech(
		SpeechScript{Response: llm.SpeechResponse{Audio: []byte("mp3"), ContentType: "audio/mpeg"}},
		SpeechScript{Err: boom},
	)

	got, err := stub.Speak(t.Context(), llm.SpeechRequest{Model: "elevenlabs/eleven-v4-turbo", Input: "Hallo"})
	if err != nil || string(got.Audio) != "mp3" || got.ContentType != "audio/mpeg" {
		t.Fatalf("Speak = %+v, %v", got, err)
	}
	if _, err := stub.Speak(t.Context(), llm.SpeechRequest{}); !errors.Is(err, boom) {
		t.Errorf("second call err = %v, want the scripted error", err)
	}
	if _, err := stub.Speak(t.Context(), llm.SpeechRequest{}); !errors.Is(err, ErrStubExhausted) {
		t.Errorf("third call err = %v, want ErrStubExhausted", err)
	}
	if requests := stub.SpeechRequests(); len(requests) != 2 || requests[0].Input != "Hallo" {
		t.Errorf("requests = %+v", requests)
	}
}

func TestStubClient_Complete_FoldsEvents(t *testing.T) {
	stub := NewStubClient(TurnScript{Events: []llm.Event{
		{Kind: llm.EventThinking, Text: "hmm"},
		{Kind: llm.EventTextDelta, Text: "Hello, "},
		{Kind: llm.EventTextDelta, Text: "world."},
		{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "call-1", Name: "lookup"}},
		{Kind: llm.EventToolCall},
		{Kind: llm.EventImage, Image: &llm.ImagePart{URL: "https://example.com/cube.png"}},
		{Kind: llm.EventImage},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 5}},
		{Kind: llm.EventEnd, FinishReason: "tool_calls"},
	}})
	response, err := stub.Complete(t.Context(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "Hello, world." || len(response.ToolCalls) != 1 || len(response.Images) != 1 ||
		response.Usage == nil || response.Usage.PromptTokens != 5 || response.FinishReason != "tool_calls" {
		t.Errorf("response = %+v", response)
	}
	if requests := stub.Requests(); len(requests) != 1 || requests[0].Model != "m" {
		t.Errorf("requests = %+v", requests)
	}
}

func TestStubClient_Complete_ErrorKeepsPartialResponse(t *testing.T) {
	stub := NewStubClient(TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "partial"},
		{Kind: llm.EventError, Err: llm.ErrServerError},
		{Kind: llm.EventTextDelta, Text: " never"},
	}})
	response, err := stub.Complete(t.Context(), llm.Request{})
	if !errors.Is(err, llm.ErrServerError) || response.Text != "partial" {
		t.Errorf("Complete = %+v, %v; want the partial text and ErrServerError", response, err)
	}
	if _, err := stub.Complete(t.Context(), llm.Request{}); !errors.Is(err, ErrStubExhausted) {
		t.Errorf("exhausted Complete err = %v, want ErrStubExhausted", err)
	}
}

func TestStubClient_Block(t *testing.T) {
	t.Run("Stream waits for the release", func(t *testing.T) {
		release := make(chan struct{})
		stub := NewStubClient(TurnScript{Block: release, Events: []llm.Event{{Kind: llm.EventEnd}}})
		events, err := stub.Stream(t.Context(), llm.Request{})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		select {
		case event := <-events:
			t.Fatalf("event %+v before the release", event)
		default:
		}
		close(release)
		if event := <-events; event.Kind != llm.EventEnd {
			t.Errorf("event = %+v, want EventEnd", event)
		}
	})
	t.Run("Stream closes without events on cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		stub := NewStubClient(TurnScript{Block: make(chan struct{}), Events: []llm.Event{{Kind: llm.EventEnd}}})
		events, err := stub.Stream(ctx, llm.Request{})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		cancel()
		if event, open := <-events; open {
			t.Errorf("got %+v after cancel, want a closed channel", event)
		}
	})
	t.Run("Complete returns the context error on cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		stub := NewStubClient(TurnScript{Block: make(chan struct{})})
		if _, err := stub.Complete(ctx, llm.Request{}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

func TestStubClient_GenerateVideo_ScriptsAndRecords(t *testing.T) {
	stub := NewStubClient()
	stub.ScriptVideos(VideoScript{Response: llm.VideoResponse{Video: []byte("mp4"), JobID: "job-1"}})

	got, err := stub.GenerateVideo(t.Context(), llm.VideoRequest{Model: "m", Prompt: "a wave"})
	if err != nil || got.JobID != "job-1" {
		t.Fatalf("GenerateVideo = %+v, %v", got, err)
	}
	if _, err := stub.GenerateVideo(t.Context(), llm.VideoRequest{}); !errors.Is(err, ErrStubExhausted) {
		t.Errorf("second call err = %v, want ErrStubExhausted", err)
	}
	if requests := stub.VideoRequests(); len(requests) != 1 || requests[0].Prompt != "a wave" {
		t.Errorf("requests = %+v", requests)
	}
}

func TestStubClient_Decide_ScriptsAndRecords(t *testing.T) {
	stub := NewStubClient()
	refused := errors.New("refused")
	stub.ScriptDecisions(DecisionScript{Err: refused})

	if _, err := stub.Decide(t.Context(), llm.DecisionRequest{Model: "jev"}); !errors.Is(err, refused) {
		t.Errorf("first call err = %v, want the scripted error", err)
	}
	if _, err := stub.Decide(t.Context(), llm.DecisionRequest{}); !errors.Is(err, ErrStubExhausted) {
		t.Errorf("second call err = %v, want ErrStubExhausted", err)
	}
	if requests := stub.DecisionRequests(); len(requests) != 1 || requests[0].Model != "jev" {
		t.Errorf("requests = %+v", requests)
	}
}

func TestStubClient_Speak_Exhausted(t *testing.T) {
	stub := NewStubClient()
	if _, err := stub.Speak(t.Context(), llm.SpeechRequest{}); !errors.Is(err, ErrStubExhausted) {
		t.Errorf("err = %v, want ErrStubExhausted", err)
	}
	if requests := stub.SpeechRequests(); len(requests) != 0 {
		t.Errorf("exhausted call recorded: %+v", requests)
	}
}
