package agent

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/govengers/aikido/tools"
)

func TestRegisterPlanTool(t *testing.T) {
	if err := RegisterPlanTool(nil); err == nil {
		t.Error("nil registry: want an error")
	}
	registry := tools.NewRegistry()
	if err := RegisterPlanTool(registry); err != nil {
		t.Fatalf("RegisterPlanTool: %v", err)
	}
	if err := RegisterPlanTool(registry); !errors.Is(err, tools.ErrDuplicateTool) {
		t.Errorf("second registration err = %v; want ErrDuplicateTool", err)
	}
}

func TestTodoWrite(t *testing.T) {
	registry := tools.NewRegistry()
	if err := RegisterPlanTool(registry); err != nil {
		t.Fatalf("RegisterPlanTool: %v", err)
	}

	content := mustDispatch(t, registry, "todo_write", `{"todos":[{"content":"read","status":"completed"},{"content":"edit","status":"in_progress"}]}`)
	want := []todo{{Content: "read", Status: todoCompleted}, {Content: "edit", Status: todoInProgress}}
	if !reflect.DeepEqual(content["todos"], want) {
		t.Errorf("todos = %v; want %v", content["todos"], want)
	}
	if content := mustDispatch(t, registry, "todo_write", `{"todos":[]}`); !reflect.DeepEqual(content["todos"], []todo{}) {
		t.Errorf("cleared list = %v; want an empty list", content["todos"])
	}

	failures := []struct{ name, arguments, wantError string }{
		{"unknown status", `{"todos":[{"content":"a","status":"done"}]}`, `task 1: status "done" is not one of pending, in_progress, completed`},
		{"empty content", `{"todos":[{"content":"a","status":"pending"},{"content":"","status":"pending"}]}`, "task 2: content is empty"},
		{"wrong type", `{"todos":"a, b"}`, `argument "todos" must be an array`},
		{"missing list", `{}`, `missing required argument "todos"`},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			if _, err := dispatch(t, registry, "todo_write", failure.arguments); err == nil || !strings.Contains(err.Error(), failure.wantError) {
				t.Errorf("err = %v; want %q", err, failure.wantError)
			}
		})
	}
}
