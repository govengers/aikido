package tools

import "errors"

var (
	// ErrDuplicateTool is returned by Registry.Register when the tool name
	// is already registered.
	ErrDuplicateTool = errors.New("aikido/tools: duplicate tool registration")
	// ErrUnknownTool is returned by Registry.Dispatch when no tool of the
	// called name is registered.
	ErrUnknownTool = errors.New("aikido/tools: unknown tool")
	// ErrInvalidArguments is returned by Registry.Dispatch when the call's
	// arguments are not a JSON object or lack a required field of the tool's
	// schema. The message names the tool and the field.
	ErrInvalidArguments = errors.New("aikido/tools: invalid tool arguments")
	// ErrToolPanicked is returned by Registry.Dispatch when the handler
	// panicked. The message names the tool and the panic value.
	ErrToolPanicked = errors.New("aikido/tools: tool panicked")
)
