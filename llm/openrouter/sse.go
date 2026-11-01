package openrouter

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
)

// sseDoneToken is the OpenRouter terminator. Once seen, no more useful events
// arrive.
const sseDoneToken = "[DONE]"

const (
	finishReasonError         = "error"
	finishReasonContentFilter = "content_filter"
)

// Provider tool-call payloads can be large, so a line may grow to 1 MiB.
// Anything longer fails the stream with ErrServerError; Complete is the
// path for multi-MiB image payloads.
const (
	initialLineBufferSize = 64 * 1024
	maximumLineSize       = 1024 * 1024
)

// processStream reads the SSE body and emits llm.Events onto out. It is
// designed to be called from a producer goroutine; it always emits exactly
// one EventEnd as the final event before returning. Channel close is the
// caller's responsibility (the main client wraps this in a goroutine that
// closes the channel after processStream returns).
//
// processStream respects ctx by checking on every send. On ctx.Done it
// returns silently; the caller-side goroutine will still close the channel.
//
// Mid-stream errors (provider drops the connection mid-SSE, or the chunk
// includes a top-level `error` envelope) are emitted as EventError followed
// by EventEnd. They are NOT retried (per ADR-006 / ADR-022, RES-005).
//
// Content-filter signals come in two forms (per ADR-028):
//  1. finish_reason="content_filter" on a regular chunk, emitted as
//     EventError{Err: ErrContentFiltered} so callers can branch on it.
//  2. error envelope with code/message indicating content filtering, same.
//
// The closing EventEnd carries FinishReason populated from the provider's
// last finish_reason (or "content_filter"/"error" when surfaced as an error).
//
// start is when the call began; the usage event carries the provider and the
// call's timing (llm.Usage.SetTiming) measured from it.
//
// A panic while decoding (CON-001) surfaces as EventError before the
// EventEnd instead of crashing the consumer's process.
func processStream(ctx context.Context, body io.Reader, out chan<- llm.Event, start time.Time) {
	decoder := &streamDecoder{
		send:      func(event llm.Event) { emit(ctx, out, event) },
		assembler: newToolCallAssembler(),
		start:     start,
	}
	defer decoder.end()
	defer decoder.recoverPanic()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, initialLineBufferSize), maximumLineSize)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		payload, ok := ssePayload(scanner.Text())
		if !ok {
			continue
		}
		if payload == sseDoneToken {
			break
		}
		decoder.handlePayload(payload)
	}
	if ctx.Err() != nil {
		return
	}
	decoder.handleReadError(scanner.Err())
}

// ssePayload returns the payload of a `data:` line. Blank lines, comment
// lines (OpenRouter sends `: OPENROUTER PROCESSING` as a keepalive), other
// SSE fields and empty payloads are not payloads.
func ssePayload(line string) (string, bool) {
	data, isData := strings.CutPrefix(strings.TrimRight(line, "\r"), "data:")
	if !isData {
		return "", false
	}
	payload := strings.TrimSpace(data)
	return payload, payload != ""
}

// emit sends event unless ctx is done. It does NOT close out.
func emit(ctx context.Context, out chan<- llm.Event, event llm.Event) {
	select {
	case <-ctx.Done():
	case out <- event:
	}
}

// streamDecoder turns decoded SSE chunks into llm.Events. It is used by one
// goroutine only.
type streamDecoder struct {
	send      func(llm.Event)
	assembler *toolCallAssembler
	start     time.Time

	firstOutput time.Time
	usageSent   bool
	// failed is set once an EventError went out; afterwards only a trailing
	// usage block is still emitted.
	failed       bool
	finishReason string
}

func (d *streamDecoder) handlePayload(payload string) {
	var chunk streamChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		// Permissive: a malformed line is skipped, not fatal for the stream.
		return
	}
	switch {
	case d.failed:
		// A priced turn is worth exactly as much after an error as before
		// one: the provider bills a filtered or failed turn and reports that
		// on a later chunk.
		d.emitUsage(&chunk)
	case chunk.Error != nil:
		d.handleErrorEnvelope(&chunk)
	default:
		for _, choice := range chunk.Choices {
			d.handleChoice(choice)
		}
		// Usage may arrive on any chunk (typically the final content chunk
		// or a dedicated trailing chunk with empty `choices`).
		d.emitUsage(&chunk)
	}
}

// handleErrorEnvelope handles a top-level error envelope, a mid-stream error.
func (d *streamDecoder) handleErrorEnvelope(chunk *streamChunk) {
	d.emitToolCalls()
	// Price first: an error envelope that carries usage was billed.
	d.emitUsage(chunk)
	cause := chunk.Error.cause()
	d.finishReason = finishReasonError
	if errors.Is(cause, llm.ErrContentFiltered) {
		d.finishReason = finishReasonContentFilter
	}
	d.fail(fmt.Errorf("openrouter mid-stream: %s: %w", chunk.Error.message(), cause))
}

func (d *streamDecoder) handleChoice(choice streamChoice) {
	if d.firstOutput.IsZero() && choice.hasOutput() {
		d.firstOutput = time.Now()
	}
	d.emitPayload(choice.Delta)
	// Some image-capable models deliver the generated image as a
	// non-streaming `choices[0].message` payload instead of a `delta`.
	if choice.Message != nil {
		d.emitPayload(*choice.Message)
	}
	if choice.FinishReason != nil {
		d.handleFinishReason(*choice.FinishReason)
	}
}

func (d *streamDecoder) handleFinishReason(reason string) {
	d.finishReason = reason
	switch reason {
	case finishReasonError:
		// Usually an error envelope came first and failed the stream already;
		// without one, surface a generic server error.
		if !d.failed {
			d.fail(fmt.Errorf("openrouter mid-stream: finish_reason=error: %w", llm.ErrServerError))
		}
	case finishReasonContentFilter:
		// The provider's safety policy aborted the generation. Surfaced
		// distinctly so callers can skip retry and show a user-actionable error.
		d.fail(fmt.Errorf("openrouter finish_reason=content_filter: %w", llm.ErrContentFiltered))
	default:
		// stop, tool_calls, length.
		d.emitToolCalls()
	}
}

// emitPayload emits text / image / thinking events from one delta-or-message
// payload and feeds tool-call fragments into the assembler.
func (d *streamDecoder) emitPayload(payload streamDelta) {
	text, images := decodeDeltaContent(payload.Content)
	if text != "" {
		d.send(llm.Event{Kind: llm.EventTextDelta, Text: text})
	}
	for _, image := range images {
		d.send(llm.Event{Kind: llm.EventImage, Image: &image})
	}
	if payload.Reasoning != "" {
		d.send(llm.Event{Kind: llm.EventThinking, Text: payload.Reasoning})
	}
	for _, part := range payload.Images {
		if image, ok := decodeAPIImagePart(part); ok {
			d.send(llm.Event{Kind: llm.EventImage, Image: &image})
		}
	}
	for _, fragment := range payload.ToolCalls {
		d.assembler.feed(fragment)
	}
}

// emitUsage emits the first usage block seen, once.
func (d *streamDecoder) emitUsage(chunk *streamChunk) {
	if d.usageSent {
		return
	}
	usage := chunkUsage(chunk)
	if usage == nil {
		return
	}
	usage.SetTiming(d.start, d.firstOutput, time.Now())
	d.send(llm.Event{Kind: llm.EventUsage, Usage: usage})
	d.usageSent = true
}

// emitToolCalls emits one EventToolCall per assembled tool call, in ascending
// index order.
func (d *streamDecoder) emitToolCalls() {
	for _, call := range d.assembler.flush() {
		d.send(llm.Event{Kind: llm.EventToolCall, Tool: &call})
	}
}

// fail flushes pending tool calls and emits err as the stream's EventError.
func (d *streamDecoder) fail(err error) {
	d.emitToolCalls()
	d.send(llm.Event{Kind: llm.EventError, Err: err})
	d.failed = true
}

// handleReadError treats a read error (connection drop, a line above
// maximumLineSize) without a prior EventError as a mid-stream drop.
func (d *streamDecoder) handleReadError(err error) {
	if err == nil || d.failed || errors.Is(err, io.EOF) {
		return
	}
	d.fail(fmt.Errorf("openrouter mid-stream read: %v: %w", err, llm.ErrServerError)) //nolint:errorlint // LIB-010: like transportError, the read error is text, ErrServerError the contract
}

// recoverPanic must be deferred directly: recover only works there.
func (d *streamDecoder) recoverPanic() {
	recovered := recover()
	if recovered == nil {
		return
	}
	d.finishReason = finishReasonError
	d.send(llm.Event{Kind: llm.EventError, Err: fmt.Errorf("openrouter: stream decoder panic: %v\n%s", recovered, debug.Stack())})
}

// end emits the closing EventEnd.
func (d *streamDecoder) end() {
	d.send(llm.Event{Kind: llm.EventEnd, FinishReason: d.finishReason})
}

// toolCallAssembler joins streamed tool-call fragments into complete
// llm.ToolCalls. Fragments continue the call at their index, except that a
// fragment with a new non-empty id starts a new call: some providers send
// several complete calls all at index 0, each with its own id.
type toolCallAssembler struct {
	// parts holds the calls in arrival order.
	parts []*partialToolCall
	// current maps a wire index to the call its fragments continue.
	current map[int]*partialToolCall
}

type partialToolCall struct {
	index     int
	id        string
	name      string
	arguments strings.Builder
}

func newToolCallAssembler() *toolCallAssembler {
	return &toolCallAssembler{current: make(map[int]*partialToolCall)}
}

// feed merges a single fragment into the buffer. A call keeps its first
// non-empty name; argument fragments concatenate.
func (a *toolCallAssembler) feed(fragment toolCallFragment) {
	part, ok := a.current[fragment.Index]
	if !ok || (part.id != "" && fragment.ID != "" && fragment.ID != part.id) {
		part = &partialToolCall{index: fragment.Index}
		a.parts = append(a.parts, part)
		a.current[fragment.Index] = part
	}
	if part.id == "" {
		part.id = fragment.ID
	}
	if part.name == "" {
		part.name = fragment.Function.Name
	}
	part.arguments.WriteString(fragment.Function.Arguments)
}

// flush returns the assembled tool calls in ascending index order (calls
// sharing an index in arrival order) and resets the buffer.
func (a *toolCallAssembler) flush() []llm.ToolCall {
	if len(a.parts) == 0 {
		return nil
	}
	slices.SortStableFunc(a.parts, func(left, right *partialToolCall) int { return cmp.Compare(left.index, right.index) })
	calls := make([]llm.ToolCall, 0, len(a.parts))
	for _, part := range a.parts {
		calls = append(calls, newToolCall(part.id, part.name, part.arguments.String(), part.index))
	}
	a.parts = nil
	clear(a.current)
	return calls
}

// newToolCall fills the gaps providers leave (OPENROUTER-DETAILS edge
// cases): empty arguments become "{}", a missing id "call_<position>".
func newToolCall(id, name, arguments string, position int) llm.ToolCall {
	if arguments == "" {
		arguments = "{}"
	}
	if id == "" {
		id = fmt.Sprintf("call_%d", position)
	}
	return llm.ToolCall{ID: id, Name: name, Arguments: arguments}
}

// decodeDeltaContent extracts plain text and any inline images from a
// streaming `delta.content` (or non-streaming `message.content`) field.
//
// OpenRouter's image-capable models surface generated images in three
// observed shapes:
//
//  1. A plain JSON string, usually descriptive text, but occasionally a raw
//     `data:image/...;base64,...` URI for tiny single-image responses.
//  2. A typed-parts array: `[{type:"text",text:"..."}, {type:"image_url",
//     image_url:{url:"data:..."}}]`.
//  3. The dedicated `images` field on the message/delta envelope (handled
//     separately by the caller).
//
// Empty input or a JSON null returns ("", nil). Unknown shapes return ("", nil)
// rather than erroring: be permissive on the wire.
func decodeDeltaContent(raw json.RawMessage) (string, []llm.ImagePart) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return decodeStringContent(text)
	}
	var parts []apiContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		return decodeContentParts(parts)
	}
	return "", nil
}

// decodeStringContent reads a decodable `data:image/` URI as an image and
// anything else as text.
func decodeStringContent(text string) (string, []llm.ImagePart) {
	if strings.HasPrefix(text, "data:image/") {
		if image, ok := decodeDataURI(text); ok {
			return "", []llm.ImagePart{image}
		}
	}
	return text, nil
}

// decodeContentParts concatenates the text parts and decodes the image_url
// parts; other part types are ignored.
func decodeContentParts(parts []apiContentPart) (string, []llm.ImagePart) {
	var (
		text   strings.Builder
		images []llm.ImagePart
	)
	for _, part := range parts {
		switch part.Type {
		case "text":
			text.WriteString(part.Text)
		case "image_url":
			if part.ImageURL == nil {
				continue
			}
			if image, ok := decodeImageURL(part.ImageURL.URL); ok {
				images = append(images, image)
			}
		}
	}
	return text.String(), images
}

// decodeAPIImagePart maps one apiImagePart onto an llm.ImagePart, decoding
// data: URIs into bytes and leaving HTTP URLs as-is. Returns false on missing
// or unrecognized shapes.
func decodeAPIImagePart(part apiImagePart) (llm.ImagePart, bool) {
	if part.ImageURL == nil {
		return llm.ImagePart{}, false
	}
	return decodeImageURL(part.ImageURL.URL)
}

// decodeImageURL converts a URL string into an llm.ImagePart. data: URIs are
// decoded into bytes; other URLs are passed through verbatim. Returns false
// on empty or malformed input.
func decodeImageURL(url string) (llm.ImagePart, bool) {
	if url == "" {
		return llm.ImagePart{}, false
	}
	if strings.HasPrefix(url, "data:") {
		return decodeDataURI(url)
	}
	return llm.ImagePart{URL: url}, true
}

// decodeDataURI parses a `data:<mime>;base64,<payload>` URI into an ImagePart.
// A non-base64 data URI keeps its URL; malformed inputs return false.
func decodeDataURI(uri string) (llm.ImagePart, bool) {
	rest, isDataURI := strings.CutPrefix(uri, "data:")
	if !isDataURI {
		return llm.ImagePart{}, false
	}
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return llm.ImagePart{}, false
	}
	contentType, isBase64 := strings.CutSuffix(meta, ";base64")
	if !isBase64 {
		// Non-base64 data URIs are valid per the HTTP spec but unused by the
		// providers we target; surface only the URL so callers can still see
		// them.
		return llm.ImagePart{URL: uri, ContentType: meta}, true
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return llm.ImagePart{}, false
	}
	return llm.ImagePart{ContentType: contentType, Data: data}, true
}

// isContentFilterErrorEnvelope checks whether a structured error envelope
// indicates content filtering. OpenRouter forwards provider-shaped error codes
// in `error.code` (sometimes a string, sometimes an int HTTP status), and the
// human message in `error.message`. We match the most common signals:
//
//   - code == "content_filter" or "content_policy_violation" or "safety" (string forms)
//   - message contains "content filter", "content policy", "safety policy" or
//     "safety filter" (case-insensitive)
//
// False positives lead to the caller showing "try a different prompt" copy
// instead of "try again later", which is the correct user action either way
// for any policy-related rejection. Conservative match preferred.
func isContentFilterErrorEnvelope(envelope *apiError) bool {
	if envelope == nil {
		return false
	}
	switch strings.ToLower(decodeErrorCodeAsString(envelope.Code)) {
	case "content_filter", "content_policy_violation", "safety":
		return true
	}
	message := strings.ToLower(envelope.Message)
	for _, marker := range []string{"content filter", "content policy", "safety policy", "safety filter"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// decodeErrorCodeAsString returns the JSON-encoded code as a string when it's
// a JSON string. Returns "" for numeric codes (HTTP status mirrors) or empty input.
func decodeErrorCodeAsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var code string
	if err := json.Unmarshal(raw, &code); err == nil {
		return code
	}
	return ""
}
