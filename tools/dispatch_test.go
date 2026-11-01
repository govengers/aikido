package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/govengers/aikido/llm"
)

func TestRegistry_DispatchRecoversPanic(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(llm.ToolDefinition{Name: "explode"}, func(context.Context, json.RawMessage, Environment) (Result, error) {
		panic("tool bug")
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	result, err := registry.Dispatch(context.Background(), llm.ToolCall{Name: "explode"}, Environment{})
	if !errors.Is(err, ErrToolPanicked) {
		t.Fatalf("err = %v, want ErrToolPanicked", err)
	}
	if want := "aikido/tools: tool panicked: explode: tool bug"; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if result.Content != nil {
		t.Errorf("result = %+v, want zero", result)
	}
}

func TestRegistry_DispatchChecksArguments(t *testing.T) {
	registry := NewRegistry()
	var received string
	recordArguments := func(_ context.Context, arguments json.RawMessage, _ Environment) (Result, error) {
		received = string(arguments)
		return Result{Content: "ok"}, nil
	}
	definition := llm.ToolDefinition{Name: "read", Parameters: Object(map[string]any{
		"path":  String("file"),
		"limit": Integer("lines"),
	}, "path")}
	if err := registry.Register(definition, recordArguments); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register(llm.ToolDefinition{Name: "free"}, recordArguments); err != nil {
		t.Fatalf("register: %v", err)
	}

	testCases := []struct {
		name, tool, arguments string
		wantError             string // empty: the call succeeds
		wantReceived          string
	}{
		{"valid", "read", `{"path":"a.md"}`, "", `{"path":"a.md"}`},
		{"missing required", "read", `{"limit":3}`, `aikido/tools: invalid tool arguments for read: missing required argument "path"`, ""},
		{"null required", "read", `{"path":null}`, `missing required argument "path"`, ""},
		{"empty counts as object", "read", ``, `missing required argument "path"`, ""},
		{"malformed", "read", `{"path":`, "arguments must be one JSON object (unexpected end of JSON input)", ""},
		{"not an object", "read", `["a.md"]`, "arguments must be one JSON object", ""},
		{"null without schema", "free", `null`, "arguments must be one JSON object, got null", ""},
		{"empty without schema", "free", "  ", "", "{}"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			received = ""
			_, err := registry.Dispatch(context.Background(), llm.ToolCall{Name: testCase.tool, Arguments: testCase.arguments}, Environment{})
			if testCase.wantError == "" {
				if err != nil || received != testCase.wantReceived {
					t.Fatalf("err = %v, received %q; want success with %q", err, received, testCase.wantReceived)
				}
				return
			}
			if !errors.Is(err, ErrInvalidArguments) || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("err = %v; want ErrInvalidArguments containing %q", err, testCase.wantError)
			}
			if received != "" {
				t.Error("the handler must not run on invalid arguments")
			}
		})
	}
}

func TestRequiredFields(t *testing.T) {
	testCases := []struct {
		name       string
		parameters json.RawMessage
		want       []string
	}{
		{"nil schema", nil, nil},
		{"not json", json.RawMessage("nope"), nil},
		{"no required", Object(map[string]any{"a": String("a")}), nil},
		{"required", Object(map[string]any{"a": String("a"), "b": String("b")}, "a", "b"), []string{"a", "b"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := requiredFields(testCase.parameters); !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("requiredFields = %v, want %v", got, testCase.want)
			}
		})
	}
}
