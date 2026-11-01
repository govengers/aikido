package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/govengers/aikido/llm"
)

func TestRegistry_RegisterAndDispatch(t *testing.T) {
	registry := NewRegistry()

	type echoArguments struct {
		Message string `json:"msg"`
	}
	var seenEnvironment Environment
	var seenArguments echoArguments

	definition := llm.ToolDefinition{Name: "echo", Description: "echo", Parameters: Object(map[string]any{
		"msg": String("text"),
	}, "msg")}
	if err := registry.Register(definition, func(ctx context.Context, arguments json.RawMessage, environment Environment) (Result, error) {
		seenEnvironment = environment
		if err := json.Unmarshal(arguments, &seenArguments); err != nil {
			return Result{}, err
		}
		return Result{Content: seenArguments.Message}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Second tool, different name.
	if err := registry.Register(llm.ToolDefinition{Name: "noop"}, func(ctx context.Context, arguments json.RawMessage, environment Environment) (Result, error) {
		return Result{Content: "ok"}, nil
	}); err != nil {
		t.Fatalf("register noop: %v", err)
	}

	if !registry.Has("echo") {
		t.Error("Has(echo) = false; want true")
	}
	if !registry.Has("noop") {
		t.Error("Has(noop) = false; want true")
	}
	if registry.Has("nope") {
		t.Error("Has(nope) = true; want false")
	}

	definitions := registry.Definitions()
	if len(definitions) != 2 {
		t.Fatalf("len(defs) = %d, want 2", len(definitions))
	}
	if definitions[0].Name != "echo" || definitions[1].Name != "noop" {
		t.Errorf("defs order = [%s, %s], want [echo, noop] (registration order)", definitions[0].Name, definitions[1].Name)
	}

	turnID := uuid.New()
	result, err := registry.Dispatch(context.Background(), llm.ToolCall{
		ID:        "c1",
		Name:      "echo",
		Arguments: `{"msg":"hello"}`,
	}, Environment{SessionID: "s-1", TurnID: turnID})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if seenArguments.Message != "hello" {
		t.Errorf("seenArguments.Message = %q", seenArguments.Message)
	}
	if seenEnvironment.SessionID != "s-1" || seenEnvironment.TurnID != turnID {
		t.Errorf("seenEnv = %+v", seenEnvironment)
	}
	if result.Content != "hello" {
		t.Errorf("res = %+v", result)
	}
}

func TestRegistry_DuplicateTool(t *testing.T) {
	registry := NewRegistry()
	noopHandler := func(ctx context.Context, arguments json.RawMessage, environment Environment) (Result, error) {
		return Result{}, nil
	}
	if err := registry.Register(llm.ToolDefinition{Name: "x"}, noopHandler); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := registry.Register(llm.ToolDefinition{Name: "x"}, noopHandler)
	if !errors.Is(err, ErrDuplicateTool) {
		t.Errorf("err = %v, want ErrDuplicateTool", err)
	}
}

func TestRegistry_DispatchUnknown(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Dispatch(context.Background(), llm.ToolCall{Name: "ghost"}, Environment{})
	if !errors.Is(err, ErrUnknownTool) {
		t.Errorf("err = %v, want ErrUnknownTool", err)
	}
}

func TestRegistry_RegisterEmptyName(t *testing.T) {
	registry := NewRegistry()
	noopHandler := func(ctx context.Context, arguments json.RawMessage, environment Environment) (Result, error) {
		return Result{}, nil
	}
	if err := registry.Register(llm.ToolDefinition{Name: ""}, noopHandler); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestRegistry_RegisterNilHandler(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(llm.ToolDefinition{Name: "x"}, nil); err == nil {
		t.Error("expected error for nil handler")
	}
}

func TestRegistry_DispatchHandlerError(t *testing.T) {
	registry := NewRegistry()
	sentinel := errors.New("boom")
	if err := registry.Register(llm.ToolDefinition{Name: "fail"}, func(ctx context.Context, arguments json.RawMessage, environment Environment) (Result, error) {
		return Result{}, sentinel
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := registry.Dispatch(context.Background(), llm.ToolCall{Name: "fail"}, Environment{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want sentinel", err)
	}
}
