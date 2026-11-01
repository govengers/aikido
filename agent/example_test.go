package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/tools"
	vfsmemory "github.com/govengers/aikido/vfs/memory"
)

func ExampleSession_Run() {
	client := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "Hello, "},
		{Kind: llm.EventTextDelta, Text: "world."},
		{Kind: llm.EventEnd},
	}})
	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID:     "conversation-1",
		Client: client,
		Model:  "openai/gpt-5.4",
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	events, _ := session.Run(context.Background(), "Say hello.")
	var reply strings.Builder
	for event := range events {
		switch event.Kind {
		case agent.EventText:
			reply.WriteString(event.Text)
		case agent.EventEnd:
			fmt.Printf("%s (%s)\n", reply.String(), event.EndReason)
		default:
		}
	}
	// Output: Hello, world. (stop)
}

func ExampleSession_RunWithMessages() {
	registry := tools.NewRegistry()
	err := registry.Register(llm.ToolDefinition{Name: "clock"}, func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		return tools.Result{Content: "12:00"}, nil
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	client := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "call-1", Name: "clock", Arguments: "{}"}},
			{Kind: llm.EventEnd},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "It is noon."},
			{Kind: llm.EventEnd},
		}},
	)
	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID: "conversation-2", Client: client, Model: "openai/gpt-5.4", Tools: registry,
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	events, _ := session.RunWithMessages(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "What time is it?"},
	})
	produced, err := agent.Drain(events)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, message := range produced {
		fmt.Printf("%s %q, %d tool calls\n", message.Role, message.Content, len(message.ToolCalls))
	}
	// Output:
	// assistant "", 1 tool calls
	// tool "\"12:00\"", 0 tool calls
	// assistant "It is noon.", 0 tool calls
}

func ExampleRegisterVFSTools() {
	registry := tools.NewRegistry()
	err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:  vfsmemory.NewStorage(),
		ReadOnly: true,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, definition := range registry.Definitions() {
		fmt.Println(definition.Name)
	}
	// Output:
	// read_file
	// list_files
	// grep
	// glob
	// search
}
