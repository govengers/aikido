package tools_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
)

func ExampleRegistry() {
	registry := tools.NewRegistry()
	err := registry.Register(llm.ToolDefinition{
		Name:        "greet",
		Description: "Greet a person by name.",
		Parameters:  tools.Object(map[string]any{"name": tools.String("Who to greet.")}, "name"),
	}, func(_ context.Context, arguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var parsed struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(arguments, &parsed); err != nil {
			return tools.Result{}, fmt.Errorf("greet arguments: %w", err)
		}
		return tools.Result{Content: "Hello, " + parsed.Name + "!"}, nil
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	result, err := registry.Dispatch(context.Background(), llm.ToolCall{
		ID: "call-1", Name: "greet", Arguments: `{"name":"Ada"}`,
	}, tools.Environment{SessionID: "conversation-1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(result.Content)
	// Output: Hello, Ada!
}

func ExampleObject() {
	schema := tools.Object(map[string]any{
		"path":  tools.String("File to read."),
		"limit": tools.Integer("Maximum lines."),
	}, "path")
	fmt.Println(string(schema))
	// Output: {"properties":{"limit":{"description":"Maximum lines.","type":"integer"},"path":{"description":"File to read.","type":"string"}},"required":["path"],"type":"object"}
}
