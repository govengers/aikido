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
		toolResult.Text = result.Text
	}
	return toolResult
}

// errorPrefix starts the model's copy of a failed tool call.
const errorPrefix = "error: "

// toolResultContent is a tool result as the model's tool message: the
// tool's text, else its content as JSON; a failure is its message after
// errorPrefix.
func toolResultContent(toolResult ToolResult) string {
	if !toolResult.OK {
		return errorPrefix + toolResult.Error
	}
	if toolResult.Text != "" {
		return toolResult.Text
	}
	return toolResultJSON(toolResult)
}

// toolResultJSON serializes a successful tool result's content.
func toolResultJSON(toolResult ToolResult) string {
	if toolResult.Content == nil {
		return "null"
	}
	content, err := json.Marshal(toolResult.Content)
	if err != nil {
		return errorPrefix + "content marshal failed: " + err.Error()
	}
	return string(content)
}

// boundedContent cuts a tool message to maximumBytes: JSON into a
// truncatedResult (boundedJSON), anything else as text with a notice at its
// end, the notice included in the bytes.
func boundedContent(content string, maximumBytes int) string {
	if maximumBytes < 0 || len(content) <= maximumBytes {
		return content
	}
	if json.Valid([]byte(content)) {
		return boundedJSON(content, maximumBytes)
	}
	notice := func(prefixBytes int) string {
		return fmt.Sprintf("\n[result cut to its first %d of %d bytes; request less, e.g. a smaller range or a narrower pattern]", prefixBytes, len(content))
	}
	prefix := utf8Prefix(content, max(maximumBytes-len(notice(maximumBytes)), 0))
	return prefix + notice(len(prefix))
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
