package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/govengers/aikido/llm"
)

// Registry holds tool definitions and their handlers. It is safe for
// concurrent use; the zero value is not usable, use NewRegistry.
type Registry struct {
	mutex       sync.RWMutex
	order       []string
	definitions map[string]llm.ToolDefinition
	handlers    map[string]Handler
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		definitions: make(map[string]llm.ToolDefinition),
		handlers:    make(map[string]Handler),
	}
}

// Register adds a tool. It returns an error for an empty name or a nil
// handler, and an error matching ErrDuplicateTool if the name is taken.
func (r *Registry) Register(definition llm.ToolDefinition, handler Handler) error {
	if definition.Name == "" {
		return errors.New("aikido/tools: tool def has empty name")
	}
	if handler == nil {
		return fmt.Errorf("aikido/tools: handler for %q is nil", definition.Name)
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if _, exists := r.definitions[definition.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, definition.Name)
	}
	r.definitions[definition.Name] = definition
	r.handlers[definition.Name] = handler
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
		definitions = append(definitions, r.definitions[name])
	}
	return definitions
}

// Has reports whether a tool is registered.
func (r *Registry) Has(name string) bool {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	_, ok := r.definitions[name]
	return ok
}

// Dispatch routes a ToolCall to its handler and returns the handler's result
// and error unchanged.
//
// It returns an error matching ErrUnknownTool if the name is not registered.
// Argument bytes are passed verbatim; handlers parse them. A panicking
// handler is not recovered.
func (r *Registry) Dispatch(ctx context.Context, call llm.ToolCall, environment Environment) (Result, error) {
	r.mutex.RLock()
	handler, ok := r.handlers[call.Name]
	r.mutex.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)
	}
	return handler(ctx, json.RawMessage(call.Arguments), environment)
}
