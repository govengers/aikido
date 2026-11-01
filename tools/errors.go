package tools

import "errors"

var (
	// ErrDuplicateTool is returned by Registry.Register when the tool name
	// is already registered.
	ErrDuplicateTool = errors.New("aikido/tools: duplicate tool registration")
	// ErrUnknownTool is returned by Registry.Dispatch when no tool of the
	// called name is registered.
	ErrUnknownTool = errors.New("aikido/tools: unknown tool")
)
