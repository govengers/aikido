package llm

import "encoding/json"

// ToolDefinition is one tool the model may call. Parameters is a JSON Schema.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}
