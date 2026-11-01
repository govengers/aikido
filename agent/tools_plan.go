package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
)

// The statuses a todo_write task can have.
const (
	todoPending    = "pending"
	todoInProgress = "in_progress"
	todoCompleted  = "completed"
)

// todo is one task of the todo_write list.
type todo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// RegisterPlanTool registers todo_write, a planning tool the model uses to
// keep a short task list for multi-step work. Each call sends the whole list
// and replaces the previous one; the tool validates it and returns it as
// {"todos": [...]}. The list lives only in the conversation, so it needs no
// storage and History keeps it; callers show progress from EventToolResult.
//
// It returns an error for a nil registry and an error matching
// tools.ErrDuplicateTool if todo_write is already registered.
func RegisterPlanTool(registry *tools.Registry) error {
	if registry == nil {
		return errors.New("aikido/agent: RegisterPlanTool: nil registry")
	}
	if err := registry.Register(todoWriteDefinition(), todoWriteHandler); err != nil {
		return fmt.Errorf("aikido/agent: RegisterPlanTool: %w", err)
	}
	return nil
}

func todoWriteDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name:        "todo_write",
		Description: "Keep a task list for multi-step work. Send the whole list each time; it replaces the previous one. Keep one task in_progress and mark tasks completed as soon as they are done.",
		Parameters: tools.Object(map[string]any{
			"todos": tools.Array(tools.Object(map[string]any{
				"content": tools.String("The task, imperative and short."),
				"status":  tools.Enum("Task state.", todoPending, todoInProgress, todoCompleted),
			}, "content", "status"), "The complete task list in order."),
		}, "todos"),
	}
}

func todoWriteHandler(_ context.Context, rawArguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
	var arguments struct {
		Todos []todo `json:"todos"`
	}
	if err := decodeArguments("todo_write", rawArguments, &arguments); err != nil {
		return tools.Result{}, err
	}
	for index, task := range arguments.Todos {
		if err := validateTodo(task); err != nil {
			return tools.Result{}, fmt.Errorf("aikido/agent: todo_write: task %d: %w", index+1, err)
		}
	}
	return tools.Result{Content: map[string]any{"todos": arguments.Todos}}, nil
}

func validateTodo(task todo) error {
	if task.Content == "" {
		return errors.New("content is empty")
	}
	switch task.Status {
	case todoPending, todoInProgress, todoCompleted:
		return nil
	default:
		return fmt.Errorf("status %q is not one of %s, %s, %s", task.Status, todoPending, todoInProgress, todoCompleted)
	}
}
