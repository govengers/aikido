package memory_test

import (
	"context"
	"errors"
	"fmt"
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
