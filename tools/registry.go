package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/govengers/aikido/llm"
)

// Registry holds tool definitions and their handlers. It is safe for
// concurrent use; the zero value is not usable, use NewRegistry.
type Registry struct {
	mutex sync.RWMutex
	order []string
	tools map[string]registeredTool
}

type registeredTool struct {
	definition llm.ToolDefinition
	handler    Handler
	required   []string // top-level required fields of definition.Parameters
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]registeredTool)}
}

// Register adds a tool. It returns an error for an empty name or a nil
// handler, and an error matching ErrDuplicateTool if the name is taken.
//
// Dispatch enforces the "required" list of definition.Parameters; a
// Parameters value that is not a JSON object schema enforces nothing.
func (r *Registry) Register(definition llm.ToolDefinition, handler Handler) error {
	if definition.Name == "" {
		return errors.New("aikido/tools: tool def has empty name")
	}
	if handler == nil {
		return fmt.Errorf("aikido/tools: handler for %q is nil", definition.Name)
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if _, exists := r.tools[definition.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, definition.Name)
	}
	r.tools[definition.Name] = registeredTool{
		definition: definition,
		handler:    handler,
		required:   requiredFields(definition.Parameters),
	}
	r.order = append(r.order, definition.Name)
	return nil
}

// Definitions returns all registered tool definitions in registration order,
// as a new slice.
func (r *Registry) Definitions() []llm.ToolDefinition {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	definitions := make([]llm.ToolDefinition, 0, len(r.order))
	for _, name := range r.order {
		definitions = append(definitions, r.tools[name].definition)
	}
	return definitions
}

// Has reports whether a tool is registered.
func (r *Registry) Has(name string) bool {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// Dispatch routes a ToolCall to its handler and returns the handler's result
// and error unchanged.
//
// Before the handler runs, Dispatch checks the arguments: empty arguments
// count as {}, anything else must be a JSON object holding every required
// field of the tool's schema (a JSON null counts as missing). The handler
// receives the arguments verbatim ({} for empty ones) and still parses them.
//
// It returns an error matching ErrUnknownTool if the name is not registered,
// ErrInvalidArguments if the argument check fails, and ErrToolPanicked if the
// handler panicked; the error messages are written for the model, so it can
// correct the call.
func (r *Registry) Dispatch(ctx context.Context, call llm.ToolCall, environment Environment) (Result, error) {
	r.mutex.RLock()
	tool, ok := r.tools[call.Name]
	r.mutex.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)
	}
	arguments, err := checkArguments(call.Arguments, tool.required)
	if err != nil {
		return Result{}, fmt.Errorf("%w for %s: %w", ErrInvalidArguments, call.Name, err)
	}
	return callHandler(ctx, call.Name, tool.handler, arguments, environment)
}

// callHandler runs handler and turns its panic into an ErrToolPanicked error,
// so one broken tool does not end the caller's agent run.
func callHandler(ctx context.Context, name string, handler Handler, arguments json.RawMessage, environment Environment) (result Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result, err = Result{}, fmt.Errorf("%w: %s: %v", ErrToolPanicked, name, recovered)
		}
	}()
	return handler(ctx, arguments, environment)
}

// checkArguments validates the model's raw arguments against the required
// fields and returns them as the handler receives them.
func checkArguments(raw string, required []string) (json.RawMessage, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("arguments must be one JSON object (%w)", err)
	}
	for _, field := range required {
		if value, present := fields[field]; !present || string(value) == "null" {
			return nil, fmt.Errorf("missing required argument %q", field)
		}
	}
	return json.RawMessage(raw), nil
}

// requiredFields reads the top-level "required" list of a JSON schema. A
// schema that does not decode has none.
func requiredFields(parameters json.RawMessage) []string {
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(parameters, &schema); err != nil {
		return nil
	}
	return schema.Required
}
