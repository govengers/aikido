package agent

import (
	"slices"

	"github.com/govengers/aikido/llm"
)

// withCacheBreakpoints returns the messages of one model request with
// prompt-cache breakpoints on the system prompt and on the last message, so
// the next request of the run reads the whole earlier conversation from the
// cache. It returns messages unchanged when any of them already carries a
// breakpoint (the caller manages caching) and never modifies messages.
//
// A last message without content is skipped: an assistant turn of only tool
// calls would carry the breakpoint on an empty text part, which Anthropic
// rejects.
func withCacheBreakpoints(messages []llm.Message) []llm.Message {
	if len(messages) == 0 || slices.ContainsFunc(messages, func(message llm.Message) bool { return message.Cache != nil }) {
		return messages
	}
	marked := slices.Clone(messages)
	if marked[0].Role == llm.RoleSystem {
		marked[0].Cache = &llm.CacheBreakpoint{}
	}
	if last := &marked[len(marked)-1]; hasContent(*last) {
		last.Cache = &llm.CacheBreakpoint{}
	}
	return marked
}

func hasContent(message llm.Message) bool {
	return message.Content != "" || len(message.Images) > 0 || len(message.Audio) > 0
}
