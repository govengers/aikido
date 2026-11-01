package memory_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/history/historytest"
	"github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
)

var _ agent.History = (*memory.History)(nil)

func TestConformance(t *testing.T) {
	historytest.RunConformance(t, func() agent.History { return memory.New() })
}

func TestHistory_RejectsEmptySessionID(t *testing.T) {
	history := memory.New()
	if err := history.Append(context.Background(), "", llm.Message{Content: "x"}); !errors.Is(err, memory.ErrEmptySessionID) {
		t.Errorf("Append = %v; want ErrEmptySessionID", err)
	}
	if _, err := history.Read(context.Background(), ""); !errors.Is(err, memory.ErrEmptySessionID) {
		t.Errorf("Read = %v; want ErrEmptySessionID", err)
	}
}

func TestHistory_HonoursCancelledContext(t *testing.T) {
	history := memory.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := history.Append(ctx, "s", llm.Message{Content: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Append = %v; want context.Canceled", err)
	}
	if _, err := history.Read(ctx, "s"); !errors.Is(err, context.Canceled) {
		t.Errorf("Read = %v; want context.Canceled", err)
	}
}

func TestHistory_SharesNoMemoryWithCallers(t *testing.T) {
	ctx := context.Background()
	original := func() llm.Message {
		return llm.Message{
			Role:      llm.RoleUser,
			Images:    []llm.ImagePart{{URL: "u", Data: []byte("image")}},
			Audio:     []llm.AudioPart{{Data: []byte("audio"), Format: "wav"}},
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "tool"}},
			Cache:     &llm.CacheBreakpoint{TTL: llm.CacheTTLFiveMinutes},
		}
	}
	mutate := func(message *llm.Message) {
		message.Images[0].Data[0] = 'X'
		message.Images[0].URL = "changed"
		message.Audio[0].Data[0] = 'X'
		message.ToolCalls[0].ID = "changed"
		message.Cache.TTL = "changed"
	}
	history := memory.New()
	appended := original()
	if err := history.Append(ctx, "s", appended); err != nil {
		t.Fatal(err)
	}
	mutate(&appended)
	read, err := history.Read(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	mutate(&read[0])

	again, err := history.Read(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again[0], original()) {
		t.Errorf("stored message = %+v; want it untouched by the callers' changes", again[0])
	}
}

func ExampleHistory() {
	history := memory.New()
	ctx := context.Background()
	err := history.Append(ctx, "conversation-1",
		llm.Message{Role: llm.RoleUser, Content: "Hi"},
		llm.Message{Role: llm.RoleAssistant, Content: "Hello!"},
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	messages, err := history.Read(ctx, "conversation-1")
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, message := range messages {
		fmt.Printf("%s: %s\n", message.Role, message.Content)
	}
	// Output:
	// user: Hi
	// assistant: Hello!
}
