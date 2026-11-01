package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
)

func buildToolResult(call llm.ToolCall, result tools.Result, err error) ToolResult {
	toolResult := ToolResult{
		CallID: call.ID,
		Name:   call.Name,
		OK:     err == nil,
	}
	if err != nil {
		toolResult.Error = err.Error()
	} else {
		toolResult.Content = result.Content
	}
	return toolResult
}

// toolResultJSON serializes a tool result for the model's tool message.
func toolResultJSON(toolResult ToolResult) string {
	if !toolResult.OK {
		return errorJSON(toolResult.Error)
	}
	if toolResult.Content == nil {
		return "null"
	}
	content, err := json.Marshal(toolResult.Content)
	if err != nil {
		return errorJSON("content marshal failed: " + err.Error())
	}
	return string(content)
}

// errorJSON renders {"error": message}.
func errorJSON(message string) string {
	// Marshalling a struct with one string field cannot fail.
	encoded, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	return string(encoded)
}

// truncatedResult is what the model sees instead of a tool result over
// SessionOptions.MaximumToolResultBytes.
type truncatedResult struct {
	Truncated  bool   `json:"truncated"`
	TotalBytes int    `json:"total_bytes"`
	Notice     string `json:"notice"`
	Content    string `json:"content"`
}

// boundedJSON returns encoded when it fits maximumBytes (negative: no cap),
// otherwise a truncatedResult holding the longest prefix of encoded that
// keeps the whole notice within maximumBytes. A cap too small for the notice
// itself yields the notice with an empty prefix.
func boundedJSON(encoded string, maximumBytes int) string {
	if maximumBytes < 0 || len(encoded) <= maximumBytes {
		return encoded
	}
	notice := func(prefixBytes int) []byte {
		prefix := utf8Prefix(encoded, prefixBytes)
		// Marshalling a struct of strings, an int and a bool cannot fail.
		marshalled, _ := json.Marshal(truncatedResult{
			Truncated:  true,
			TotalBytes: len(encoded),
			Notice:     fmt.Sprintf("result cut to its first %d of %d bytes; request less, e.g. a smaller range or a narrower pattern", len(prefix), len(encoded)),
			Content:    prefix,
		})
		return marshalled
	}
	// The notice grows with the prefix, so the longest fitting prefix is one
	// below the shortest that does not fit.
	tooLong := sort.Search(maximumBytes+1, func(prefixBytes int) bool { return len(notice(prefixBytes)) > maximumBytes })
	return string(notice(max(tooLong-1, 0)))
}

// utf8Prefix returns the longest prefix of text of at most maximumBytes that
// does not split a UTF-8 sequence.
func utf8Prefix(text string, maximumBytes int) string {
	if maximumBytes >= len(text) {
		return text
	}
	end := maximumBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}
