package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
)

const (
	defaultMaximumAudioBytes = 8 << 20
	defaultTurnTimeout       = 5 * time.Minute
	defaultWriteTimeout      = 30 * time.Second
)

// HandlerOptions configure a Handler.
type HandlerOptions struct {
	Pipeline *Pipeline
	// Session resolves the agent session for a request (auth, tenant,
	// conversation id). An error answers 400 with its message; check auth
	// in a middleware in front for a 401. Extra request fields are there
	// to read: a JSON body can be decoded again from request.Body (the
	// handler restores it), multipart fields come from request.FormValue,
	// query parameters from request.URL.
	Session func(request *http.Request) (Runner, error)
	// ToolResult, when set, picks what of a tool result reaches the client:
	// a non-nil return is sent as the "content" of the tool_result line.
	// Nil (the default, and the return for results to keep private) sends
	// no content.
	ToolResult func(result agent.ToolResult) any
	// MaximumAudioBytes caps the upload. Default 8 MiB; larger answers 413.
	MaximumAudioBytes int64
	// Timeout caps the turn, which runs detached from the request context so
	// a client that goes away does not lose it. Default 5 minutes.
	Timeout time.Duration
	// WriteTimeout caps writing and flushing one NDJSON line. A client that
	// does not take a line within it counts as gone: the handler stops
	// writing and drains the turn to its end. It is applied through
	// http.ResponseController.SetWriteDeadline, so a ResponseWriter without
	// deadline support writes without a cap. The deadline is cleared between
	// lines, so a turn may wait longer than WriteTimeout for its next event
	// (a long tool call). A writer without Flush gets every line unflushed.
	// Default 30 seconds.
	WriteTimeout time.Duration
}

// Handler serves one voice turn per POST and streams its events as NDJSON
// (application/x-ndjson, one JSON object per line, flushed per event). It is
// safe for concurrent use.
//
// The request is one of:
//   - application/json: {"text": "...", "output": "text"|"speech", "language": "de"}
//   - multipart/form-data: an "audio" file part (its Content-Type is the
//     audio type) or a "text" field, plus "output" and "language" fields
//   - a raw audio/* body, with ?output= and ?language=
//
// Each line has a "type" (the EventKind) and the fields of that event:
//
//	{"type":"transcript","text":"...","raw":"..."}
//	{"type":"text","text":"..."}
//	{"type":"tool_call","tool":{"id":"...","name":"...","arguments":"..."}}
//	{"type":"tool_result","tool":{"id":"...","name":"...","ok":true,"error":"","content":...}}
//	{"type":"speech","seq":0,"text":"...","content_type":"audio/mpeg","audio":"<base64>"}
//	{"type":"error","error":"...","seq":2}
//	{"type":"done","end_reason":"stop","usage":{"cost_usd":0.0031,...,"llm_calls":[{"provider":"Anthropic","latency_ms":412,"duration_ms":1630,"tokens_per_second":84.2,...}]}}
//
// usage.llm_calls has one entry per model call of the turn (tool loops make
// several) and usage.transcription the transcription call, each with the
// provider that served it, latency to the first token, total duration and
// output tokens per second.
//
// The done line is always the last one, also for a turn that hit Timeout
// (end_reason "timeout"), as long as the client is still connected.
//
// Input errors answer 4xx with a JSON {"error": "..."} body before any line.
type Handler struct {
	options HandlerOptions
}

// NewHandler builds a Handler. It returns an error if options is nil or lacks
// Pipeline or Session. options is copied; later changes do not reach the
// Handler.
func NewHandler(options *HandlerOptions) (*Handler, error) {
	if options == nil || options.Pipeline == nil || options.Session == nil {
		return nil, errors.New("aikido/voice: HandlerOptions need Pipeline and Session")
	}
	configured := *options
	if configured.MaximumAudioBytes <= 0 {
		configured.MaximumAudioBytes = defaultMaximumAudioBytes
	}
	if configured.Timeout <= 0 {
		configured.Timeout = defaultTurnTimeout
	}
	if configured.WriteTimeout <= 0 {
		configured.WriteTimeout = defaultWriteTimeout
	}
	return &Handler{options: configured}, nil
}

// ServeHTTP runs one turn; Handler describes the request and response format.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, http.StatusMethodNotAllowed, "POST only")
		return
	}
	input, status, err := h.readInput(writer, request)
	if err != nil {
		writeError(writer, status, err.Error())
		return
	}
	runner, err := h.options.Session(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}

	// Detached from the request: a closed tab must not cancel the turn
	// half-way, or the agent's History never gets the reply.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), h.options.Timeout)
	defer cancel()
	events, err := h.options.Pipeline.Run(ctx, runner, input)
	if err != nil {
		writeError(writer, runErrorStatus(err), err.Error())
		return
	}
	h.writeEvents(writer, events)
}

func runErrorStatus(err error) int {
	if errors.Is(err, llm.ErrInvalidRequest) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// writeEvents streams events as NDJSON. After the client is gone (a failed
// or timed out write) it keeps draining, so the turn completes.
func (h *Handler) writeEvents(writer http.ResponseWriter, events <-chan Event) {
	writer.Header().Set("Content-Type", "application/x-ndjson")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(writer)
	encoder := json.NewEncoder(writer)
	connected := true
	for event := range events {
		if connected {
			connected = h.writeLine(controller, encoder, event)
		}
	}
}

// writeLine writes and flushes one event under the write deadline and
// reports whether the client is still connected. The deadline is cleared
// after the line: armed while the turn waits (a long tool call), it would
// reset a healthy HTTP/2 stream. A writer without deadlines or Flush (a
// middleware wrapper) is written to as it is.
func (h *Handler) writeLine(controller *http.ResponseController, encoder *json.Encoder, event Event) bool {
	return stillConnected(controller.SetWriteDeadline(time.Now().Add(h.options.WriteTimeout))) &&
		encoder.Encode(h.toWire(event)) == nil &&
		stillConnected(controller.Flush()) &&
		stillConnected(controller.SetWriteDeadline(time.Time{}))
}

// stillConnected reports whether a ResponseController call left the client
// connected: it succeeded, or the writer does not support it.
func stillConnected(err error) bool {
	return err == nil || errors.Is(err, http.ErrNotSupported)
}

// writeError answers with a JSON error body. The status is already sent when
// encoding fails, so that error carries nothing to act on.
func writeError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": message})
}

// readInput parses the three request shapes into an Input and returns the
// status to answer with when it fails. A JSON body is put back on
// request.Body for Session to decode again.
func (h *Handler) readInput(writer http.ResponseWriter, request *http.Request) (Input, int, error) {
	// An unparsable Content-Type leaves mediaType empty, which answers 415 below.
	mediaType, _, _ := mime.ParseMediaType(request.Header.Get("Content-Type"))
	body := http.MaxBytesReader(writer, request.Body, h.options.MaximumAudioBytes)
	var (
		input Input
		err   error
	)
	switch {
	case mediaType == "application/json":
		input, err = readJSONInput(request, body)
	case mediaType == "multipart/form-data":
		input, err = h.readMultipartInput(writer, request)
	case strings.HasPrefix(mediaType, "audio/"):
		input, err = readAudioInput(request, body)
	default:
		return Input{}, http.StatusUnsupportedMediaType, fmt.Errorf("unsupported content type %q", mediaType)
	}
	if err != nil {
		status, bodyError := h.bodyError(err)
		return Input{}, status, bodyError
	}
	if len(input.Audio) > 0 {
		if _, err := llm.AudioFormat(input.ContentType); err != nil {
			return Input{}, http.StatusUnsupportedMediaType, err
		}
	}
	return input, 0, nil
}

// bodyError maps a failure to read the request to its status and message.
func (h *Handler) bodyError(err error) (int, error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge, fmt.Errorf("body exceeds %d bytes", h.options.MaximumAudioBytes)
	}
	return http.StatusBadRequest, err
}

func readJSONInput(request *http.Request, body io.Reader) (Input, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return Input{}, err
	}
	var fields struct {
		Text     string `json:"text"`
		Output   Output `json:"output"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Input{}, fmt.Errorf("invalid JSON: %w", err)
	}
	request.Body = io.NopCloser(bytes.NewReader(raw))
	return Input{Text: fields.Text, Output: fields.Output, Language: fields.Language}, nil
}

func (h *Handler) readMultipartInput(writer http.ResponseWriter, request *http.Request) (Input, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, h.options.MaximumAudioBytes)
	if err := request.ParseMultipartForm(h.options.MaximumAudioBytes); err != nil { //nolint:gosec // G120: the body is capped by MaxBytesReader above; gosec tracks the request, not its body
		return Input{}, err
	}
	input := Input{
		Text:     request.FormValue("text"),
		Output:   Output(request.FormValue("output")),
		Language: request.FormValue("language"),
	}
	file, header, err := request.FormFile("audio")
	if errors.Is(err, http.ErrMissingFile) {
		return input, nil // a text turn
	}
	if err != nil {
		return Input{}, err
	}
	defer file.Close()
	audio, err := io.ReadAll(file)
	if err != nil {
		return Input{}, err
	}
	input.Audio, input.ContentType = audio, header.Header.Get("Content-Type")
	return input, nil
}

func readAudioInput(request *http.Request, body io.Reader) (Input, error) {
	audio, err := io.ReadAll(body)
	if err != nil {
		return Input{}, err
	}
	query := request.URL.Query()
	return Input{
		Audio:       audio,
		ContentType: request.Header.Get("Content-Type"),
		Output:      Output(query.Get("output")),
		Language:    query.Get("language"),
	}, nil
}

type wireTool struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	OK        *bool  `json:"ok,omitempty"`
	Error     string `json:"error,omitempty"`
	Content   any    `json:"content,omitempty"`
}

type wireUsage struct {
	CostUSD              float64 `json:"cost_usd"`
	TranscriptionCostUSD float64 `json:"transcription_cost_usd"`
	LLMCostUSD           float64 `json:"llm_cost_usd"`
	SpeechCostUSD        float64 `json:"speech_cost_usd"`
	SpeechCharacters     int     `json:"speech_chars"`
	PromptTokens         int     `json:"prompt_tokens"`
	CompletionTokens     int     `json:"completion_tokens"`
	// Transcription and LLMCalls carry each model call's provider and timing.
	Transcription *wireCall  `json:"transcription,omitempty"`
	LLMCalls      []wireCall `json:"llm_calls,omitempty"`
}

// wireCall is one model call's usage and performance on the wire.
type wireCall struct {
	Provider         string  `json:"provider,omitempty"`
	LatencyMS        int64   `json:"latency_ms"`
	DurationMS       int64   `json:"duration_ms"`
	TokensPerSecond  float64 `json:"tokens_per_second"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type wireEvent struct {
	Type        EventKind       `json:"type"`
	Text        string          `json:"text,omitempty"`
	Raw         string          `json:"raw,omitempty"`
	Tool        *wireTool       `json:"tool,omitempty"`
	Sequence    *int            `json:"seq,omitempty"`
	ContentType string          `json:"content_type,omitempty"`
	Audio       []byte          `json:"audio,omitempty"`
	Error       string          `json:"error,omitempty"`
	EndReason   agent.EndReason `json:"end_reason,omitempty"`
	Usage       *wireUsage      `json:"usage,omitempty"`
}

func (h *Handler) toWire(event Event) wireEvent {
	wire := wireEvent{Type: event.Kind, Text: event.Text, Raw: event.Raw, EndReason: event.EndReason}
	if event.Err != nil {
		wire.Error = event.Err.Error()
	}
	if call := event.ToolCall; call != nil {
		wire.Tool = &wireTool{ID: call.ID, Name: call.Name, Arguments: call.Arguments}
	}
	if result := event.ToolResult; result != nil {
		wire.Tool = h.toWireToolResult(*result)
	}
	if chunk := event.Speech; chunk != nil {
		sequence := chunk.Sequence
		wire.Sequence, wire.Text, wire.ContentType, wire.Audio = &sequence, chunk.Text, chunk.ContentType, chunk.Audio
	}
	if event.Usage != nil {
		wire.Usage = toWireUsage(event.Usage)
	}
	return wire
}

func (h *Handler) toWireToolResult(result agent.ToolResult) *wireTool {
	succeeded := result.OK
	tool := &wireTool{ID: result.CallID, Name: result.Name, OK: &succeeded, Error: result.Error}
	if h.options.ToolResult != nil {
		tool.Content = h.options.ToolResult(result)
	}
	return tool
}

// toWireUsage flattens usage; the top-level token counts include the
// transcription call.
func toWireUsage(usage *Usage) *wireUsage {
	wire := &wireUsage{
		CostUSD:          usage.CostUSD,
		LLMCostUSD:       usage.LLM.CostUSD,
		SpeechCostUSD:    usage.SpeechCostUSD,
		SpeechCharacters: usage.SpeechCharacters,
		PromptTokens:     usage.LLM.PromptTokens,
		CompletionTokens: usage.LLM.CompletionTokens,
	}
	for _, call := range usage.LLMCalls {
		wire.LLMCalls = append(wire.LLMCalls, toWireCall(call))
	}
	if transcription := usage.Transcription; transcription != nil {
		call := toWireCall(*transcription)
		wire.Transcription = &call
		wire.TranscriptionCostUSD = transcription.CostUSD
		wire.PromptTokens += transcription.PromptTokens
		wire.CompletionTokens += transcription.CompletionTokens
	}
	return wire
}

func toWireCall(usage llm.Usage) wireCall {
	return wireCall{
		Provider:         usage.Provider,
		LatencyMS:        usage.Latency.Milliseconds(),
		DurationMS:       usage.Duration.Milliseconds(),
		TokensPerSecond:  usage.TokensPerSecond,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CostUSD:          usage.CostUSD,
	}
}
