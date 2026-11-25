package agent

import (
	"errors"
	"fmt"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
)

// Defaults NewSession applies to unset SessionOptions fields.
const (
	defaultMaximumTurns           = 20
	defaultRunTimeout             = 10 * time.Minute
	defaultLLMCallTimeout         = 180 * time.Second
	defaultMaximumTokens          = 16384
	defaultToolCallTimeout        = 2 * time.Minute
	defaultMaximumToolResultBytes = 64 << 10
	defaultRepeatedToolCallLimit  = 3
	// DefaultWrapUpPrompt is the instruction of the wrap-up call a run makes
	// when it ran out of model calls; %d is SessionOptions.MaximumTurns.
	DefaultWrapUpPrompt = "You have used all %d model calls of this turn, so no tool will run any more. Do not call tools. " +
		"Answer now with what you found so far, and say plainly what is left undone."
)

// SessionOptions configure a Session. NewSession copies them, including the
// values Temperature and Thinking point to, so changing them afterwards does
// not affect the Session.
type SessionOptions struct {
	// ID keys the session's History and Locker entries. Required.
	ID string
	// Client is the model client every turn streams from. Required.
	Client llm.Client
	// Tools are offered to the model and dispatched when it calls them. Nil
	// offers no tools; a tool call then fails with tools.ErrUnknownTool.
	Tools *tools.Registry
	// History stores the conversation Run continues. Required.
	History History
	// Locker serializes runs of the same session ID. Required.
	Locker Locker
	// Model is the provider's model ID. Required.
	Model string
	// SystemPrompt is sent as the first message of every model call. Empty
	// sends none. It is never written to History.
	SystemPrompt string
	// Instructions open the first user message of a conversation (a Run
	// whose History is still empty) and are stored with it: the project's
	// own instructions, e.g. a directory's CLAUDE.md or AGENTS.md read with
	// ReadProjectInstructions. Unlike SystemPrompt they stay out of the
	// system prompt, so one cached system prompt serves every directory.
	// RunWithMessages ignores them; the caller owns those messages.
	Instructions string

	// MaximumTurns caps the model calls of one run; zero or negative means 20.
	MaximumTurns int
	// RunTimeout caps one whole run; zero means 10 minutes, negative is an
	// error.
	RunTimeout time.Duration
	// LLMCallTimeout caps one model call; zero means 180 seconds, negative is
	// an error.
	LLMCallTimeout time.Duration
	// MaximumTokens caps the tokens of one model reply; zero means 16384.
	MaximumTokens int
	// Temperature is the sampling temperature; nil leaves the model default.
	Temperature *float32
	// Thinking sets the reasoning effort or budget of every model call, e.g.
	// llm.ThinkingByEffort(llm.ThinkingEffortLow). Nil leaves the model default.
	Thinking *llm.ThinkingConfiguration

	// ToolCallTimeout caps one tool call; zero means 2 minutes, negative
	// means no cap. A call that fails after its deadline passed is reported
	// to the model as timed out and the run continues. The cap reaches the
	// handler as its context's deadline, so it binds only handlers that
	// honour ctx.
	ToolCallTimeout time.Duration
	// MaximumToolResultBytes caps the JSON of one tool result the model sees
	// (and History stores); zero means 64 KiB, negative means no cap. A
	// longer result is replaced by {"truncated":true,"total_bytes":N,
	// "notice":"...","content":"<prefix of the JSON>"}, itself within the cap
	// unless the cap is too small for the notice. EventToolResult always
	// carries the full result.
	MaximumToolResultBytes int
	// DisablePromptCaching stops the run from setting prompt-cache
	// breakpoints. By default every model request marks the system prompt
	// and its last message (if that has content) with llm.CacheBreakpoint
	// (5 minute TTL), at most 2 of the 4 breakpoints Anthropic allows. A
	// request whose messages already carry a breakpoint is sent unchanged,
	// so a History that sets its own keeps control. Breakpoints are added to
	// the request only, never to History. Providers that cache on their own
	// or not at all (OpenAI, most open models) ignore them; the Anthropic
	// and Gemini models on OpenRouter honour them.
	DisablePromptCaching bool

	// RepeatedToolCallLimit is how often one tool call (same name, same
	// arguments), or a cycle of up to 4 calls (A, B, A, B), may return the
	// same results back to back. The next call that would continue the cycle
	// is not run: the model gets an error telling it so. After
	// RepeatedToolCallLimit refusals the run makes the wrap-up call and ends
	// with EndReasonRepeatedToolCalls. A call whose result changes (polling a
	// process, a test after an edit) breaks a cycle. Zero means 3, negative
	// means no limit.
	RepeatedToolCallLimit int
	// DisableWrapUp ends a run that reached MaximumTurns (or
	// RepeatedToolCallLimit) right away. By default the run makes one more
	// model call that asks for an answer without tools, so a cut run still
	// ends with an answer; tool calls in that reply are dropped. The end
	// reason stays EndReasonMaximumTurns (EndReasonRepeatedToolCalls).
	DisableWrapUp bool
	// WrapUpPrompt is the user message of the wrap-up call; a %d in it is
	// replaced by MaximumTurns. Empty means DefaultWrapUpPrompt.
	WrapUpPrompt string
}

// Session bundles a session ID, a model + system prompt, a tool registry, a
// History plug-in, and a Locker plug-in. Multiple Run calls share the
// session's history, serialized by the Locker. A Session is safe for
// concurrent use.
type Session struct {
	options SessionOptions
}

// NewSession constructs a Session. It returns an error if options is nil, a
// required field (ID, Client, Model, History, Locker) is empty, or a timeout
// is negative.
func NewSession(options *SessionOptions) (*Session, error) {
	if err := validateSessionOptions(options); err != nil {
		return nil, err
	}
	own := withSessionDefaults(*options)
	own.Temperature = copyOf(own.Temperature)
	own.Thinking = copyOf(own.Thinking)
	return &Session{options: own}, nil
}

// copyOf returns a pointer to a copy of *value, nil for nil, so a Session
// never shares configuration the caller can still change.
func copyOf[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}

func validateSessionOptions(options *SessionOptions) error {
	switch {
	case options == nil:
		return errors.New("aikido/agent: nil SessionOptions")
	case options.ID == "":
		return errors.New("aikido/agent: SessionOptions.ID is required")
	case options.Client == nil:
		return errors.New("aikido/agent: SessionOptions.Client is required")
	case options.Model == "":
		return errors.New("aikido/agent: SessionOptions.Model is required")
	case options.History == nil:
		return errors.New("aikido/agent: SessionOptions.History is required (use agent/history/memory.New or NewLocalSession)")
	case options.Locker == nil:
		return errors.New("aikido/agent: SessionOptions.Locker is required (use agent.NewLocalLocker or NewLocalSession)")
	case options.RunTimeout < 0:
		return fmt.Errorf("aikido/agent: SessionOptions.RunTimeout must be >= 0, got %v", options.RunTimeout)
	case options.LLMCallTimeout < 0:
		return fmt.Errorf("aikido/agent: SessionOptions.LLMCallTimeout must be >= 0, got %v", options.LLMCallTimeout)
	}
	return nil
}

func withSessionDefaults(options SessionOptions) SessionOptions {
	if options.MaximumTurns <= 0 {
		options.MaximumTurns = defaultMaximumTurns
	}
	if options.RepeatedToolCallLimit == 0 {
		options.RepeatedToolCallLimit = defaultRepeatedToolCallLimit
	}
	if options.WrapUpPrompt == "" {
		options.WrapUpPrompt = DefaultWrapUpPrompt
	}
	if options.RunTimeout == 0 {
		options.RunTimeout = defaultRunTimeout
	}
	if options.LLMCallTimeout == 0 {
		options.LLMCallTimeout = defaultLLMCallTimeout
	}
	if options.MaximumTokens == 0 {
		options.MaximumTokens = defaultMaximumTokens
	}
	if options.ToolCallTimeout == 0 {
		options.ToolCallTimeout = defaultToolCallTimeout
	}
	if options.MaximumToolResultBytes == 0 {
		options.MaximumToolResultBytes = defaultMaximumToolResultBytes
	}
	return options
}
