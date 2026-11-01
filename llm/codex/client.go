// Package codex implements llm.Client against the Codex backend of a ChatGPT
// subscription (https://chatgpt.com/backend-api/codex/responses), the way the
// Codex CLI uses it. It bills the subscription instead of an API key.
//
// The backend speaks the OpenAI Responses API with a few rules: it only
// streams, never stores, and rejects max_output_tokens. Supported: system
// messages (as instructions), user and assistant text, image input and
// structured output (llm.Request.ResponseFormat). Tools, tool results, audio
// input and Thinking are not supported and fail with llm.ErrInvalidRequest;
// MaximumTokens, Temperature, StopSequences, Modalities and
// ImageConfiguration are ignored.
//
// Log in with StartDeviceLogin + PollDeviceLogin (or ParseLogin), wrap the
// login in a TokenSource that persists every refresh, and pass it in Options.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/retry"
)

const (
	// DefaultBaseURL is the Codex backend root.
	DefaultBaseURL = "https://chatgpt.com/backend-api/codex"
	// clientVersion gates the models list; older versions hide newer models.
	clientVersion = "0.155.0"
	// maximumErrorDetailBytes is how much of a failed response ends up in the error.
	maximumErrorDetailBytes = 512
	streamBufferSize        = 16
	// maximumStreamLineBytes fits a large structured-output delta in one SSE line.
	maximumStreamLineBytes = 4 << 20
	initialLineBufferBytes = 64 << 10
)

// Options configure the Client.
type Options struct {
	// Tokens is required.
	Tokens *TokenSource
	// Originator is required: OpenAI asks third-party clients to identify
	// themselves (e.g. "casa-mapa").
	Originator string
	// UserAgent defaults to Originator + "/aikido".
	UserAgent string
	// BaseURL overrides DefaultBaseURL (tests).
	BaseURL string
	// HTTPClient overrides a client without timeout; pass a ctx deadline.
	HTTPClient *http.Client
}

// Client is a Codex-backend implementation of llm.Client. It is safe for
// concurrent use.
type Client struct {
	options Options
}

var _ llm.Client = (*Client)(nil)

// NewClient builds a Client. It returns an error if options is nil or lacks
// Tokens or Originator. options is copied; later changes do not reach the
// Client.
func NewClient(options *Options) (*Client, error) {
	if options == nil || options.Tokens == nil || options.Originator == "" {
		return nil, errors.New("codex: Tokens and Originator are required")
	}
	client := &Client{options: *options}
	if client.options.UserAgent == "" {
		client.options.UserAgent = options.Originator + "/aikido"
	}
	if client.options.BaseURL == "" {
		client.options.BaseURL = DefaultBaseURL
	}
	client.options.BaseURL = strings.TrimRight(client.options.BaseURL, "/")
	if client.options.HTTPClient == nil {
		client.options.HTTPClient = &http.Client{}
	}
	return client, nil
}

// Stream sends one request and returns its events; EventEnd comes last, then
// the channel closes. The caller must drain the channel or cancel ctx.
// 429 and 5xx at stream start are retried, mid-stream errors are not. Errors
// wrap the llm sentinels (llm.ErrInvalidRequest for unsupported request
// features).
func (c *Client) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	start := time.Now()
	body, err := buildBody(request)
	if err != nil {
		return nil, err
	}
	var response *http.Response
	err = retry.Do(ctx, retryPolicy(), func(int) error {
		var attemptError error
		response, attemptError = c.do(ctx, http.MethodPost, "/responses", body, "text/event-stream") //nolint:bodyclose // closed by the stream goroutine
		return attemptError
	})
	if err != nil {
		return nil, err
	}
	events := make(chan llm.Event, streamBufferSize)
	// No recover here: processStream recovers its own panics (CON-001). The
	// goroutine ends with the body or with ctx.
	go func() {
		defer close(events)
		defer response.Body.Close()
		processStream(ctx, response.Body, events, start)
	}()
	return events, nil
}

// Complete drains Stream: the backend has no non-streaming mode. On a stream
// error the Response holds what arrived before it.
func (c *Client) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	events, err := c.Stream(ctx, request)
	if err != nil {
		return llm.Response{}, err
	}
	var (
		response llm.Response
		text     strings.Builder
	)
	for event := range events {
		switch event.Kind {
		case llm.EventTextDelta:
			text.WriteString(event.Text)
		case llm.EventUsage:
			response.Usage = event.Usage
		case llm.EventError:
			err = event.Err
		case llm.EventEnd:
			response.FinishReason = event.FinishReason
		case llm.EventToolCall, llm.EventThinking, llm.EventImage:
			// processStream emits none of these
		}
	}
	response.Text = text.String()
	if err == nil {
		err = ctx.Err()
	}
	return response, err
}

// Models lists the model ids the subscription offers, in the backend's order,
// hidden ones left out. Errors wrap the llm sentinels.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	response, err := c.do(ctx, http.MethodGet, "/models?client_version="+clientVersion, nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var catalog struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("codex: models: %w", err)
	}
	var ids []string
	for _, model := range catalog.Models {
		if model.Slug != "" && model.Visibility != "hide" && model.Visibility != "hidden" {
			ids = append(ids, model.Slug)
		}
	}
	return ids, nil
}

// do sends one authenticated request and returns a 200 response, or an error
// wrapping an llm sentinel or ctx.Err() (closing the body).
func (c *Client) do(ctx context.Context, method, path string, body []byte, accept string) (*http.Response, error) {
	tokens, err := c.options.Tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("codex: access token: %w", err)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.options.BaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("codex: build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	request.Header.Set("ChatGPT-Account-ID", tokens.AccountID)
	request.Header.Set("originator", c.options.Originator)
	request.Header.Set("User-Agent", c.options.UserAgent)
	request.Header.Set("Accept", accept)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.options.HTTPClient.Do(request)
	if err != nil {
		return nil, transportError(ctx, "codex: http error", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, statusError(response)
	}
	return response, nil
}

// transportError wraps ctx.Err() when the request failed because ctx ended,
// so a cancelled call is neither reported nor retried as a server error, and
// llm.ErrServerError otherwise. LIB-010: the transport error itself is not
// part of the API, only its text is kept.
func transportError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	}
	return fmt.Errorf("%s: %s: %w", operation, err.Error(), llm.ErrServerError)
}

// statusError turns a non-200 response into an error and closes its body.
// The status alone picks the sentinel; the body only adds detail, so its read
// and close errors carry nothing worth returning.
func statusError(response *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(response.Body, maximumErrorDetailBytes))
	_ = response.Body.Close()
	return fmt.Errorf("codex: status %d: %s: %w", response.StatusCode, strings.TrimSpace(string(detail)), statusSentinel(response.StatusCode))
}

func statusSentinel(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired:
		return llm.ErrAuthentication
	case status == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case status >= http.StatusInternalServerError || status == http.StatusRequestTimeout:
		return llm.ErrServerError
	default:
		return llm.ErrInvalidRequest
	}
}

func retryPolicy() retry.Policy {
	policy := retry.DefaultPolicy()
	policy.ShouldRetry = func(err error) bool {
		return errors.Is(err, llm.ErrRateLimited) || errors.Is(err, llm.ErrServerError)
	}
	return policy
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type inputItem struct {
	Type    string        `json:"type"` // "message"
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type textFormat struct {
	Type   string          `json:"type"` // "json_schema"
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type textOptions struct {
	Format textFormat `json:"format"`
}

type requestBody struct {
	Model        string       `json:"model"`
	Instructions string       `json:"instructions,omitempty"`
	Input        []inputItem  `json:"input"`
	Store        bool         `json:"store"`
	Stream       bool         `json:"stream"`
	Text         *textOptions `json:"text,omitempty"`
}

func buildBody(request llm.Request) ([]byte, error) {
	if len(request.Tools) > 0 || request.Thinking != nil {
		return nil, fmt.Errorf("codex: tools and thinking are not supported: %w", llm.ErrInvalidRequest)
	}
	body := requestBody{Model: request.Model, Stream: true, Input: []inputItem{}}
	var instructions []string
	for _, message := range request.Messages {
		if message.Role == llm.RoleSystem {
			instructions = append(instructions, message.Content)
			continue
		}
		item, err := toInputItem(message)
		if err != nil {
			return nil, err
		}
		body.Input = append(body.Input, item)
	}
	body.Instructions = strings.Join(instructions, "\n\n")
	body.Text = toTextOptions(request.ResponseFormat)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("codex: encode request: %w", err)
	}
	return raw, nil
}

// toInputItem converts a user or assistant message; every other role and
// every non-text, non-image part is rejected with llm.ErrInvalidRequest.
func toInputItem(message llm.Message) (inputItem, error) {
	textType := "input_text"
	switch message.Role {
	case llm.RoleUser:
	case llm.RoleAssistant:
		textType = "output_text"
	default:
		return inputItem{}, fmt.Errorf("codex: %s messages are not supported: %w", message.Role, llm.ErrInvalidRequest)
	}
	if len(message.ToolCalls) > 0 {
		return inputItem{}, fmt.Errorf("codex: tool calls are not supported: %w", llm.ErrInvalidRequest)
	}
	if len(message.Audio) > 0 {
		return inputItem{}, fmt.Errorf("codex: audio input is not supported: %w", llm.ErrInvalidRequest)
	}
	item := inputItem{Type: "message", Role: string(message.Role)}
	if message.Content != "" {
		item.Content = append(item.Content, contentPart{Type: textType, Text: message.Content})
	}
	for _, image := range message.Images {
		item.Content = append(item.Content, contentPart{Type: "input_image", ImageURL: imageURL(image)})
	}
	return item, nil
}

// imageURL is the image's URL, or its data inlined as a data URL.
func imageURL(image llm.ImagePart) string {
	if image.URL != "" || len(image.Data) == 0 {
		return image.URL
	}
	contentType := image.ContentType
	if contentType == "" {
		contentType = http.DetectContentType(image.Data)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
}

func toTextOptions(schema *llm.JSONSchema) *textOptions {
	if schema == nil {
		return nil
	}
	return &textOptions{Format: textFormat{Type: "json_schema", Name: schema.Name, Strict: schema.Strict, Schema: schema.Schema}}
}

type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Text     string          `json:"text"`
	Message  string          `json:"message"`
	Code     string          `json:"code"`
	Response *streamResponse `json:"response"`
}

type streamResponse struct {
	Usage *responseUsage `json:"usage"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

type responseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// streamReader turns one Responses SSE body into llm events.
type streamReader struct {
	events      chan<- llm.Event
	start       time.Time
	firstOutput time.Time
	sawDelta    bool
	finished    bool
	finish      string
}

// processStream turns the Responses SSE into llm events and always ends with
// exactly one EventEnd, also when reading the body panics. Text arrives as
// output_text deltas; the .done event only counts when no delta came
// (response.completed carries no output).
func processStream(ctx context.Context, body io.Reader, events chan<- llm.Event, start time.Time) {
	reader := &streamReader{events: events, start: start}
	defer reader.end(ctx)
	reader.read(ctx, body)
	if !reader.finished && ctx.Err() == nil {
		reader.fail(ctx, "error", fmt.Errorf("codex: stream ended before completion: %w", llm.ErrServerError))
	}
}

// end emits EventEnd. It recovers a panic of the stream goroutine, which would
// otherwise crash the consumer's process (CON-001), and reports it as an error.
func (r *streamReader) end(ctx context.Context) {
	if recovered := recover(); recovered != nil {
		r.fail(ctx, "error", fmt.Errorf("codex: reading the stream panicked: %v: %w", recovered, llm.ErrServerError))
	}
	emit(ctx, r.events, llm.Event{Kind: llm.EventEnd, FinishReason: r.finish})
}

// read handles data lines until the response finished, the body ended or ctx
// ended.
func (r *streamReader) read(ctx context.Context, body io.Reader) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, initialLineBufferBytes), maximumStreamLineBytes)
	for scanner.Scan() {
		event, ok := parseStreamLine(scanner.Text())
		if !ok {
			continue
		}
		if !r.handle(ctx, event) {
			return
		}
	}
}

// parseStreamLine decodes an SSE data line. "event:" lines repeat the type
// that data carries anyway, and an undecodable line carries nothing usable,
// so both report false.
func parseStreamLine(line string) (streamEvent, bool) {
	data, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return streamEvent{}, false
	}
	var event streamEvent
	if json.Unmarshal([]byte(strings.TrimSpace(data)), &event) != nil {
		return streamEvent{}, false
	}
	return event, true
}

// handle emits what event carries and reports whether reading goes on.
func (r *streamReader) handle(ctx context.Context, event streamEvent) bool {
	switch event.Type {
	case "response.output_text.delta":
		r.sawDelta = true
		if r.firstOutput.IsZero() {
			r.firstOutput = time.Now()
		}
		return emit(ctx, r.events, llm.Event{Kind: llm.EventTextDelta, Text: event.Delta})
	case "response.output_text.done":
		if !r.sawDelta && event.Text != "" {
			emit(ctx, r.events, llm.Event{Kind: llm.EventTextDelta, Text: event.Text})
		}
	case "response.completed", "response.incomplete":
		r.complete(ctx, event.Response)
		return false
	case "response.failed", "error":
		finish, failure := streamFailure(event)
		r.fail(ctx, finish, failure)
		return false
	}
	return true
}

func (r *streamReader) complete(ctx context.Context, response *streamResponse) {
	r.finished = true
	r.finish = finishReason(response)
	if response == nil || response.Usage == nil {
		return
	}
	usage := toUsage(response.Usage)
	usage.SetTiming(r.start, r.firstOutput, time.Now())
	emit(ctx, r.events, llm.Event{Kind: llm.EventUsage, Usage: usage})
}

func (r *streamReader) fail(ctx context.Context, finish string, failure error) {
	r.finished = true
	r.finish = finish
	emit(ctx, r.events, llm.Event{Kind: llm.EventError, Err: failure})
}

// finishReason is "stop", or the reason an incomplete response gives.
func finishReason(response *streamResponse) string {
	if response != nil && response.IncompleteDetails != nil && response.IncompleteDetails.Reason != "" {
		return response.IncompleteDetails.Reason
	}
	return "stop"
}

func toUsage(usage *responseUsage) *llm.Usage {
	converted := &llm.Usage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens}
	if usage.InputTokensDetails != nil {
		converted.CacheReadTokens = usage.InputTokensDetails.CachedTokens
	}
	return converted
}

// streamFailure maps a response.failed or error event to its finish reason
// and an error wrapping llm.ErrContentFiltered for content and safety codes,
// llm.ErrServerError otherwise.
func streamFailure(event streamEvent) (string, error) {
	message, code := event.Message, event.Code
	if event.Response != nil && event.Response.Error != nil {
		message, code = event.Response.Error.Message, event.Response.Error.Code
	}
	if strings.Contains(code, "content") || strings.Contains(code, "safety") {
		return "content_filter", fmt.Errorf("codex: %s %s: %w", code, message, llm.ErrContentFiltered)
	}
	return "error", fmt.Errorf("codex: %s %s: %w", code, message, llm.ErrServerError)
}

// emit sends event unless ctx ends first, and reports whether it was sent.
func emit(ctx context.Context, events chan<- llm.Event, event llm.Event) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
