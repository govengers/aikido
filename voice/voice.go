// Package voice wraps one agent turn with speech in and speech out.
//
// A Pipeline takes text or recorded audio, transcribes audio with
// llm.TranscribeDetailed, runs the turn on an agent session, and, when the
// output is speech, synthesizes the reply sentence by sentence while the model
// is still writing, so playback starts before the answer is complete. All four
// combinations of text or voice in and text or voice out run through the same
// Run call and the same typed event stream.
//
// The package has no web framework dependency. Handler serves a Pipeline as
// NDJSON over plain net/http; anything that mounts an http.Handler (Gin via
// gin.WrapH, chi, the standard mux) can use it.
package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
)

// DefaultSpeechModel, DefaultVoice and DefaultFormat are used when Options
// name none.
const (
	DefaultSpeechModel = "elevenlabs/eleven-v4-turbo"
	DefaultVoice       = "sarah"
	DefaultFormat      = "mp3"
)

// DefaultSpokenStyle is a reply-style instruction for answers that are read
// aloud. Set it as Options.SpokenStyle to use it.
const DefaultSpokenStyle = `Your reply will be read aloud by a text-to-speech voice. Answer in the user's language, in one to three short, plain sentences. No markdown, lists, tables, code, emojis or URLs. Write numbers, units and abbreviations the way a person says them.`

// EndReasonNoSpeech ends a turn whose audio held no intelligible speech. The
// agent does not run.
const EndReasonNoSpeech agent.EndReason = "no_speech"

// Output selects what a turn produces.
type Output string

const (
	// OutputText streams the reply as text only.
	OutputText Output = "text"
	// OutputSpeech streams the reply as text and speech chunks.
	OutputSpeech Output = "speech"
)

const (
	defaultSpeechConcurrency = 2
	eventBufferSize          = 32
	// speechQueueSize bounds the sentences waiting for synthesis before the
	// turn waits for the speaker.
	speechQueueSize = 256
	// terminalGrace bounds how long Run waits to hand over the final EventDone
	// once ctx is over and the consumer's buffer is full.
	terminalGrace = 5 * time.Second
)

// Runner runs one agent turn. *agent.Session implements it.
type Runner interface {
	RunWithOptions(ctx context.Context, userText string, options *agent.RunOptions) (<-chan agent.Event, error)
}

// Options configure a Pipeline. Transcriber is required for audio input,
// Speech for speech output; a Pipeline without one rejects the matching
// Input with llm.ErrInvalidRequest.
type Options struct {
	// Transcriber is the client llm.TranscribeDetailed runs on.
	Transcriber     llm.Client
	TranscribeModel string // default llm.DefaultTranscribeModel
	// Vocabulary biases and corrects every transcript.
	Vocabulary *llm.Vocabulary

	Speech      llm.SpeechGenerator
	SpeechModel string   // default DefaultSpeechModel
	Voice       string   // default DefaultVoice
	Format      string   // "mp3" (default) or "pcm"
	Speed       *float32 // nil leaves the provider default
	// SpeechUSDPerCharacter prices synthesized characters for Usage.SpeechCostUSD.
	// OpenRouter reports no cost on speech responses, so this is the only
	// way to a per-turn price. eleven-v4-turbo lists 0.00002 (10.2026).
	SpeechUSDPerCharacter float64
	// SpeechConcurrency bounds parallel synthesis calls. Default 2.
	SpeechConcurrency int

	// SpokenStyle is appended to the system prompt for turns with
	// OutputSpeech, e.g. DefaultSpokenStyle. Empty adds nothing.
	SpokenStyle string
}

// Pipeline runs voice turns. It is safe for concurrent use.
type Pipeline struct {
	options       Options
	terminalGrace time.Duration
}

// New builds a Pipeline. It returns an error if options is nil or has neither
// a Transcriber nor a Speech generator. options is copied; later changes do
// not reach the Pipeline.
func New(options *Options) (*Pipeline, error) {
	if options == nil {
		return nil, errors.New("aikido/voice: nil Options")
	}
	if options.Transcriber == nil && options.Speech == nil {
		return nil, errors.New("aikido/voice: Options need a Transcriber, a Speech generator, or both")
	}
	configured := *options
	if configured.SpeechModel == "" {
		configured.SpeechModel = DefaultSpeechModel
	}
	if configured.Voice == "" {
		configured.Voice = DefaultVoice
	}
	if configured.Format == "" {
		configured.Format = DefaultFormat
	}
	if configured.SpeechConcurrency <= 0 {
		configured.SpeechConcurrency = defaultSpeechConcurrency
	}
	return &Pipeline{options: configured, terminalGrace: terminalGrace}, nil
}

// Input is one user turn: Text, or Audio with its ContentType (a MIME type
// as MediaRecorder reports it). Audio wins when both are set.
type Input struct {
	Text        string
	Audio       []byte
	ContentType string
	// Language is a hint for transcription, a code or name ("de", "German").
	Language string
	// Output defaults to OutputText.
	Output Output
}

// EventKind identifies a voice Event.
type EventKind string

// The kinds of Event; Event lists the fields each one sets.
const (
	EventTranscript EventKind = "transcript"
	EventText       EventKind = "text"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventSpeech     EventKind = "speech"
	EventError      EventKind = "error"
	EventDone       EventKind = "done"
)

// Event is one item of a turn's stream. The channel closes after EventDone.
//
//   - EventTranscript: Text is the corrected transcript of the user's audio,
//     Raw the model's version. Sent once, before anything else; Text "" is
//     followed by EventDone with EndReasonNoSpeech.
//   - EventText: a delta of the agent's reply.
//   - EventToolCall, EventToolResult: passed through from agent.Session.
//   - EventSpeech: Speech holds one synthesized sentence. Chunks arrive in
//     Sequence order (0, 1, 2, ...).
//   - EventError: Err is set. With Speech set, only that chunk failed (no
//     Audio) and the turn goes on; otherwise EventDone follows.
//   - EventDone: EndReason (agent.EndReason* or EndReasonNoSpeech) and Usage.
//     Always the last event, exactly once per Run, also when ctx is over.
type Event struct {
	Kind       EventKind
	Text       string
	Raw        string
	ToolCall   *llm.ToolCall
	ToolResult *agent.ToolResult
	Speech     *SpeechChunk
	Err        error
	EndReason  agent.EndReason
	Usage      *Usage
}

// SpeechChunk is one synthesized sentence. Text is what was spoken, after
// Speakable.
type SpeechChunk struct {
	Sequence     int
	Text         string
	Audio        []byte
	ContentType  string
	GenerationID string
}

// Usage aggregates one turn over transcription, the agent's model calls and
// speech. CostUSD is the sum; SpeechCostUSD is SpeechCharacters times
// Options.SpeechUSDPerCharacter. LLM sums the token and cost fields of
// LLMCalls; the per-call provider, latency and throughput live in LLMCalls
// only.
type Usage struct {
	Transcription    *llm.Usage
	LLM              llm.Usage
	LLMCalls         []llm.Usage
	SpeechCharacters int
	SpeechCostUSD    float64
	CostUSD          float64
}

func (u *Usage) addLLMCall(call llm.Usage) {
	u.LLM.PromptTokens += call.PromptTokens
	u.LLM.CompletionTokens += call.CompletionTokens
	u.LLM.CacheReadTokens += call.CacheReadTokens
	u.LLM.CacheWriteTokens += call.CacheWriteTokens
	u.LLM.CostUSD += call.CostUSD
	u.LLMCalls = append(u.LLMCalls, call)
}

func (u *Usage) totalCostUSD() float64 {
	total := u.LLM.CostUSD + u.SpeechCostUSD
	if u.Transcription != nil {
		total += u.Transcription.CostUSD
	}
	return total
}

// Run starts one turn on runner and returns its events. Invalid input
// (nothing to say, audio without a Transcriber, speech without a Speech
// generator, an unknown audio type, a nil runner) is rejected up front with
// llm.ErrInvalidRequest.
//
// The caller must drain the channel. Cancelling ctx cancels the turn; a
// server that wants the turn to finish after its client went away passes
// context.WithoutCancel, as Handler does.
//
// Every Run ends with exactly one EventDone carrying the usage accrued so far,
// then the channel closes. Once ctx is over, other events are dropped, but
// EventDone is still delivered, with EndReason agent.EndReasonCancelled or
// agent.EndReasonTimeout in place of "stop" or "error". A
// consumer that stopped reading does not block Run forever: with a full
// buffer and ctx over, EventDone is given up after a few seconds. A panic in
// the runner, the Transcriber or the Speech generator ends the turn with
// EventError and EventDone instead of crashing the process.
func (p *Pipeline) Run(ctx context.Context, runner Runner, input Input) (<-chan Event, error) {
	input, format, err := p.validate(input)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		return nil, fmt.Errorf("aikido/voice: nil Runner: %w", llm.ErrInvalidRequest)
	}
	events := make(chan Event, eventBufferSize)
	go p.run(ctx, runner, input, format, events)
	return events, nil
}

// validate checks input against what the Pipeline can do and returns it with
// Output defaulted, and the audio format for transcription.
func (p *Pipeline) validate(input Input) (Input, string, error) {
	var format string
	switch {
	case len(input.Audio) > 0:
		if p.options.Transcriber == nil {
			return Input{}, "", fmt.Errorf("aikido/voice: audio input needs Options.Transcriber: %w", llm.ErrInvalidRequest)
		}
		audioFormat, err := llm.AudioFormat(input.ContentType)
		if err != nil {
			return Input{}, "", fmt.Errorf("%w: %w", err, llm.ErrInvalidRequest)
		}
		format = audioFormat
	case strings.TrimSpace(input.Text) == "":
		return Input{}, "", fmt.Errorf("aikido/voice: input has neither text nor audio: %w", llm.ErrInvalidRequest)
	}
	switch input.Output {
	case "", OutputText:
		input.Output = OutputText
	case OutputSpeech:
		if p.options.Speech == nil {
			return Input{}, "", fmt.Errorf("aikido/voice: speech output needs Options.Speech: %w", llm.ErrInvalidRequest)
		}
	default:
		return Input{}, "", fmt.Errorf("aikido/voice: unknown output %q: %w", input.Output, llm.ErrInvalidRequest)
	}
	return input, format, nil
}

func (p *Pipeline) run(ctx context.Context, runner Runner, input Input, format string, events chan<- Event) {
	defer close(events)
	currentTurn := &turn{options: &p.options, events: events, terminalGrace: p.terminalGrace}
	defer currentTurn.endAfterPanic(ctx)
	text, proceed := currentTurn.userText(ctx, input, format)
	if !proceed {
		return
	}
	currentTurn.reply(ctx, runner, text, input.Output)
}

// turn is the state of one Run, owned by its goroutine.
type turn struct {
	options       *Options
	events        chan<- Event
	terminalGrace time.Duration
	usage         Usage
	speaker       *speaker
	ended         bool
}

// endAfterPanic recovers a panic of the turn's goroutine, which would
// otherwise crash the consumer's process (CON-001), and ends the turn with
// EventError and EventDone.
func (t *turn) endAfterPanic(ctx context.Context) {
	recovered := recover()
	if recovered == nil {
		return
	}
	t.stopSpeaking(ctx)
	if !t.ended {
		t.fail(ctx, fmt.Errorf("aikido/voice: turn panicked: %v", recovered))
	}
}

// userText is the text the agent runs on: the input's text, or the transcript
// of its audio. It reports false when the turn already ended (failed
// transcription, no speech).
func (t *turn) userText(ctx context.Context, input Input, format string) (string, bool) {
	if len(input.Audio) == 0 {
		return input.Text, true
	}
	transcription, err := llm.TranscribeDetailed(ctx, t.options.Transcriber, transcribeRequest(t.options, input, format))
	t.usage.Transcription = transcription.Usage
	if err != nil {
		t.fail(ctx, fmt.Errorf("aikido/voice: transcribe: %w", err))
		return "", false
	}
	t.emit(ctx, Event{Kind: EventTranscript, Text: transcription.Text, Raw: transcription.Raw})
	if transcription.Text == "" {
		t.end(ctx, EndReasonNoSpeech)
		return "", false
	}
	return transcription.Text, true
}

func transcribeRequest(options *Options, input Input, format string) llm.TranscribeRequest {
	request := llm.TranscribeRequest{
		Model:      options.TranscribeModel,
		Audio:      input.Audio,
		Format:     format,
		Vocabulary: options.Vocabulary,
	}
	if input.Language != "" {
		request.Hint = "Expected language: " + input.Language + "."
	}
	return request
}

// reply runs the agent on text, forwards its events and, for speech output,
// speaks the reply, then ends the turn.
func (t *turn) reply(ctx context.Context, runner Runner, text string, output Output) {
	var runOptions *agent.RunOptions
	if output == OutputSpeech {
		if t.options.SpokenStyle != "" {
			runOptions = &agent.RunOptions{SystemSuffix: t.options.SpokenStyle}
		}
		t.speaker = newSpeaker(t.options, func(event Event) { t.emit(ctx, event) })
	}
	agentEvents, err := runner.RunWithOptions(ctx, text, runOptions)
	if err != nil {
		t.stopSpeaking(ctx)
		t.fail(ctx, fmt.Errorf("aikido/voice: run: %w", err))
		return
	}
	reason := t.forward(ctx, agentEvents)
	t.stopSpeaking(ctx)
	t.end(ctx, reason)
}

// forward passes the agent's events on, feeds its text to the speaker and
// returns the agent's end reason.
func (t *turn) forward(ctx context.Context, agentEvents <-chan agent.Event) agent.EndReason {
	reason := agent.EndReasonError
	for event := range agentEvents {
		switch event.Kind {
		case agent.EventText:
			t.emit(ctx, Event{Kind: EventText, Text: event.Text})
			t.speaker.feed(ctx, event.Text)
		case agent.EventToolCall:
			// Text before a tool call is a sentence of its own ("Let me check.").
			t.speaker.flush(ctx)
			t.emit(ctx, Event{Kind: EventToolCall, ToolCall: event.ToolCall})
		case agent.EventToolResult:
			t.emit(ctx, Event{Kind: EventToolResult, ToolResult: event.ToolResult})
		case agent.EventUsage:
			if event.Usage != nil {
				t.usage.addLLMCall(*event.Usage)
			}
		case agent.EventError:
			t.emit(ctx, Event{Kind: EventError, Err: event.Err})
		case agent.EventEnd:
			reason = event.EndReason
		case agent.EventThinking, agent.EventImage:
			// a voice turn has no channel for thinking or images
		}
	}
	return reason
}

// stopSpeaking waits for every pending chunk and books the characters. It is
// safe to call more than once.
func (t *turn) stopSpeaking(ctx context.Context) {
	speaker := t.speaker
	t.speaker = nil
	speaker.close(ctx, &t.usage)
}

func (t *turn) emit(ctx context.Context, event Event) {
	select {
	case t.events <- event:
	case <-ctx.Done():
	}
}

func (t *turn) fail(ctx context.Context, err error) {
	t.emit(ctx, Event{Kind: EventError, Err: err})
	t.end(ctx, agent.EndReasonError)
}

// end hands over EventDone. Once ctx is over it still waits terminalGrace for
// room in the buffer, so a consumer that keeps reading gets it.
func (t *turn) end(ctx context.Context, reason agent.EndReason) {
	t.ended = true
	t.usage.CostUSD = t.usage.totalCostUSD()
	done := Event{Kind: EventDone, EndReason: endReason(reason, ctx.Err()), Usage: &t.usage}
	select {
	case t.events <- done:
		return
	case <-ctx.Done():
	}
	timer := time.NewTimer(t.terminalGrace)
	defer timer.Stop()
	select {
	case t.events <- done:
	case <-timer.C:
	}
}

// endReason reports a turn cut short by its context as cancelled or timed
// out, also when the agent finished but speech did not.
func endReason(reason agent.EndReason, contextError error) agent.EndReason {
	if reason != agent.EndReasonError && reason != agent.EndReasonStop {
		return reason
	}
	switch {
	case errors.Is(contextError, context.Canceled):
		return agent.EndReasonCancelled
	case errors.Is(contextError, context.DeadlineExceeded):
		return agent.EndReasonTimeout
	}
	return reason
}

// speaker turns streamed text into ordered speech chunks. Synthesis runs in
// parallel up to SpeechConcurrency; one emitter goroutine forwards results in
// Sequence order. A nil *speaker is a no-op, which is the text-output case.
// It is used by its turn's goroutine only; close waits for all its goroutines.
type speaker struct {
	options     *Options
	pending     string
	sequence    int
	queue       chan chan Event
	slots       chan struct{}
	emitterDone chan struct{}

	mutex      sync.Mutex
	characters int
}

func newSpeaker(options *Options, emit func(Event)) *speaker {
	created := &speaker{
		options:     options,
		queue:       make(chan chan Event, speechQueueSize),
		slots:       make(chan struct{}, options.SpeechConcurrency),
		emitterDone: make(chan struct{}),
	}
	// No recover: the emitter only moves values between channels, nothing in
	// it can panic (CON-001). close owns it.
	go func() {
		defer close(created.emitterDone)
		for result := range created.queue {
			emit(<-result)
		}
	}()
	return created
}

func (s *speaker) feed(ctx context.Context, delta string) {
	if s == nil {
		return
	}
	var sentences []string
	sentences, s.pending = splitSentences(s.pending + delta)
	for _, sentence := range sentences {
		s.speak(ctx, sentence)
	}
}

func (s *speaker) flush(ctx context.Context) {
	if s == nil {
		return
	}
	s.speak(ctx, s.pending)
	s.pending = ""
}

// close speaks what is left, waits for every chunk and books the characters
// on usage.
func (s *speaker) close(ctx context.Context, usage *Usage) {
	if s == nil {
		return
	}
	s.flush(ctx)
	close(s.queue)
	<-s.emitterDone
	usage.SpeechCharacters = s.characters
	usage.SpeechCostUSD = float64(s.characters) * s.options.SpeechUSDPerCharacter
}

// speak queues text for synthesis in Sequence order. Once ctx is over, it
// queues nothing: the emitter would drop the chunk anyway.
func (s *speaker) speak(ctx context.Context, text string) {
	text = Speakable(text)
	if text == "" {
		return
	}
	chunk := SpeechChunk{Sequence: s.sequence, Text: text}
	s.sequence++
	result := make(chan Event, 1)
	select {
	case s.queue <- result:
	case <-ctx.Done():
		return
	}
	go s.synthesize(ctx, chunk, result)
}

// synthesize speaks one chunk and sends exactly one event to result, which
// has room for it.
func (s *speaker) synthesize(ctx context.Context, chunk SpeechChunk, result chan<- Event) {
	defer func() {
		// CON-001: a panicking Speech generator fails this chunk, not the process.
		if recovered := recover(); recovered != nil {
			result <- chunkError(chunk, fmt.Errorf("aikido/voice: speak chunk %d panicked: %v", chunk.Sequence, recovered))
		}
	}()
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		result <- chunkError(chunk, fmt.Errorf("aikido/voice: speak chunk %d: %w", chunk.Sequence, ctx.Err()))
		return
	}
	defer func() { <-s.slots }()
	response, err := s.options.Speech.Speak(ctx, llm.SpeechRequest{
		Model:  s.options.SpeechModel,
		Input:  chunk.Text,
		Voice:  s.options.Voice,
		Format: s.options.Format,
		Speed:  s.options.Speed,
	})
	if err != nil {
		result <- chunkError(chunk, fmt.Errorf("aikido/voice: speak chunk %d: %w", chunk.Sequence, err))
		return
	}
	s.mutex.Lock()
	s.characters += len([]rune(chunk.Text))
	s.mutex.Unlock()
	chunk.Audio, chunk.ContentType, chunk.GenerationID = response.Audio, response.ContentType, response.GenerationID
	result <- Event{Kind: EventSpeech, Speech: &chunk}
}

func chunkError(chunk SpeechChunk, err error) Event {
	return Event{Kind: EventError, Speech: &chunk, Err: err}
}
