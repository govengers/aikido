package llm

import "time"

// Role is the author of a Message, written on the wire as is.
type Role string

// The roles of a conversation.
const (
	// RoleSystem is the system prompt, the first message of a conversation.
	RoleSystem Role = "system"
	// RoleUser is the human or calling application.
	RoleUser Role = "user"
	// RoleAssistant is the model; its messages carry ToolCalls.
	RoleAssistant Role = "assistant"
	// RoleTool is a tool's result; its messages carry ToolCallID.
	RoleTool Role = "tool"
)

// ImagePart is one image attached to a message or returned by an image-capable
// model. URL is set when the provider returned a remote URL or when the caller
// supplied a data URI / remote URL on input. Data is set when the provider
// returned inline bytes (decoded from a data: URI) or when the caller wants to
// inline an image on output. ContentType is the MIME type when known
// ("image/png", "image/jpeg", ...), empty when not provided by the wire.
type ImagePart struct {
	URL         string
	ContentType string
	Data        []byte
}

// AudioPart is one audio clip attached to a message. Data is the encoded clip
// as recorded; Format names its container the way OpenRouter's input_audio
// expects it ("wav", "mp3", "m4a", "webm", "ogg", "flac", "aiff"). AudioFormat
// derives Format from a MIME type.
type AudioPart struct {
	Data   []byte
	Format string
}

// ToolCall is one call the model made to a tool of Request.Tools.
type ToolCall struct {
	// ID is the provider's call id; the tool result answers it in
	// Message.ToolCallID.
	ID string
	// Name is the ToolDefinition.Name of the called tool.
	Name string
	// Arguments is the JSON object the model passed, as raw text. Models can
	// emit invalid JSON; the caller validates it.
	Arguments string
}

// Usage is the accounting and performance record of one model call. Fields
// the provider did not report stay zero.
type Usage struct {
	// PromptTokens is the input tokens, cached ones included.
	PromptTokens int
	// CompletionTokens is the output tokens, reasoning included.
	CompletionTokens int
	// CacheReadTokens is the part of PromptTokens served from the prompt cache.
	CacheReadTokens int
	// CacheWriteTokens is the part of PromptTokens written to the prompt cache.
	CacheWriteTokens int
	// CostUSD is the provider's billed cost of the call in US dollars.
	CostUSD float64

	// Provider is the upstream provider that served the call (OpenRouter's
	// "Anthropic", "Azure", ...). Empty when the client cannot tell.
	Provider string
	// Latency is the time from the call to its first output: the first
	// streamed token (text, reasoning or tool-call fragment) on Stream, the
	// whole response on Complete. Measured client-side, retries included.
	Latency time.Duration
	// Duration is the time from the call to its usage report (end of the
	// generation).
	Duration time.Duration
	// TokensPerSecond is CompletionTokens over the generation time: Duration
	// minus Latency on Stream, Duration on Complete. 0 when unmeasurable
	// (no completion tokens, or under 250 ms of generation).
	TokensPerSecond float64
}

// minimumRateWindow is the shortest generation time SetTiming derives a
// TokensPerSecond from.
const minimumRateWindow = 250 * time.Millisecond

// SetTiming fills Latency, Duration and TokensPerSecond from a call that
// started at start, produced its first output at firstOutput and ended at end.
// A zero firstOutput (non-streaming call, or no output seen) counts the whole
// call as latency and computes the rate over Duration.
func (u *Usage) SetTiming(start, firstOutput, end time.Time) {
	u.Duration = end.Sub(start)
	generation := end.Sub(firstOutput)
	if firstOutput.IsZero() {
		u.Latency, generation = u.Duration, u.Duration
	} else {
		u.Latency = firstOutput.Sub(start)
	}
	u.TokensPerSecond = 0
	// Providers that buffer (Bedrock) deliver short replies in a few large
	// chunks, so a short generation window measures chunking, not the model:
	// 83 tokens over 62 ms read as 1300 tok/s. Below 250 ms there is no rate.
	if generation >= minimumRateWindow && u.CompletionTokens > 0 {
		u.TokensPerSecond = float64(u.CompletionTokens) / generation.Seconds()
	}
}

// CacheTTL is how long a prompt-cache entry lives at the provider.
type CacheTTL string

// The cache lifetimes providers offer.
const (
	// CacheTTLFiveMinutes is the providers' default lifetime.
	CacheTTLFiveMinutes CacheTTL = "5m"
	// CacheTTLOneHour costs more to write and pays off for prompts reused
	// over a longer session.
	CacheTTLOneHour CacheTTL = "1h"
)

// CacheBreakpoint marks a message as a cache breakpoint on providers that
// support it. nil = no breakpoint. &CacheBreakpoint{} = default 5m TTL.
type CacheBreakpoint struct {
	// TTL is the entry's lifetime; empty means CacheTTLFiveMinutes.
	TTL CacheTTL
}

// Message is one turn of a conversation. Which fields are set depends on
// Role: Content (and Images, Audio) for system and user, Content and
// ToolCalls for assistant, Content and ToolCallID for tool.
type Message struct {
	Role    Role
	Content string
	// Images are attached images; only user messages carry them on most
	// providers.
	Images []ImagePart
	// Audio are attached audio clips (user messages).
	Audio []AudioPart
	// ToolCalls are the calls an assistant message made.
	ToolCalls []ToolCall
	// ToolCallID is the ToolCall.ID a tool message answers.
	ToolCallID string
	// Cache marks the message as a prompt-cache breakpoint; nil means none.
	Cache *CacheBreakpoint
}
