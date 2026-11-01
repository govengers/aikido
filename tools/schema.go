package tools

import "encoding/json"

// Object builds a JSON Schema "object" from a property map and a list of
// required property names. Output is deterministic (properties are emitted in
// the order Go's encoding/json sorts map keys, which is alphabetical). Nil
// properties produce an object without properties.
//
// Object panics if properties hold a value encoding/json cannot encode (a
// channel, a function): schemas are literals written at registration time,
// so that is a programmer error (ERR-001).
func Object(properties map[string]any, required ...string) json.RawMessage {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic("aikido/tools: Object marshal failed: " + err.Error())
	}
	return encoded
}

// String builds a string-typed property schema.
func String(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}

// Integer builds an integer-typed property schema.
func Integer(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
	}
}

// Number builds a number-typed property schema.
func Number(description string) map[string]any {
	return map[string]any{
		"type":        "number",
		"description": description,
	}
}

// Boolean builds a boolean-typed property schema.
func Boolean(description string) map[string]any {
	return map[string]any{
		"type":        "boolean",
		"description": description,
	}
}

// Enum builds a string-typed enum property schema.
func Enum(description string, values ...string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
		"enum":        values,
	}
}

// Array builds an array-typed property schema.
func Array(items any, description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       items,
	}
}
