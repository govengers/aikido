// Package openrouter implements llm.Client against OpenRouter
// (https://openrouter.ai). Streaming-first; tool-call assembly; 429/5xx
// retry at stream-start. The SSE parser is inlined here per ADR-022.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/retry"
)

// DefaultBaseURL is OpenRouter's API root.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

const (
	defaultVideoPollInterval = 3 * time.Second

	// defaultResponseHeaderTimeout bounds how long the default HTTP client
	// waits for a response's headers (RES-001). It is far above any
	// legitimate wait, a non-streaming reasoning turn included, and only
	// turns a hung connection into an error instead of a goroutine stuck forever.
	defaultResponseHeaderTimeout = 10 * time.Minute

	// eventBufferSize lets the stream producer run a few events ahead of a
	// slow consumer.
	eventBufferSize = 16

	// maximumResponseBytes caps a non-streaming answer read into memory: a
	// Complete with several inline base64 images, a model list, audio. Far
	// above any legitimate answer; a body above it is llm.ErrInvalidResponse.
	maximumResponseBytes = 64 << 20

	// maximumVideoBytes caps a downloaded clip, which is read into memory too.
	maximumVideoBytes = 512 << 20
)

// Options configure the OpenRouter Client.
type Options struct {
	// APIKey is required.
	APIKey string

	// BaseURL overrides DefaultBaseURL. Useful for tests via httptest.Server.
	BaseURL string

	// HTTPClient overrides the default client, which waits at most 10 minutes
	// for response headers but sets no overall Timeout: streams may be
	// long-lived, so callers wanting a wall-clock cap pass a context with a
	// deadline rather than configuring a client-level timeout.
	HTTPClient *http.Client

	// HTTPReferer is the optional OpenRouter ranking-attribution header.
	HTTPReferer string

	// XTitle is the optional OpenRouter ranking-attribution header.
	XTitle string

	// Provider is the default provider-routing block for chat calls; a
	// context from WithProvider overrides it per call. NewClient keeps a copy.
	Provider *ProviderPreferences

	// ProviderOrder is shorthand for Provider when only the order matters:
	// a single entry locks to one provider (allow_fallbacks=false), multiple
	// entries form a fallback chain (allow_fallbacks=true). Ignored when
	// Provider is set.
	ProviderOrder []string

	// MaximumAttempts is the total number of HTTP attempts per call on 429, 5xx
	// and network errors, on every path (Stream, Complete, transcription,
	// images, speech, decisions, models, each video submit/poll/download
	// request); the video submit retries on 429 only. Only failures before a
	// response status arrived, 429 and 5xx are retried: once a 2xx arrived the
	// request is never sent again, so a body cut off mid-read (or a stream
	// broken mid-way) fails the call with llm.ErrServerError instead of
	// billing the work twice. 0 keeps the default of 3; 1 disables retry, for
	// callers that run their own job-level retry. Negative is treated as 1.
	MaximumAttempts int

	// VideoPollInterval is how often GenerateVideo polls a running job.
	// 0 keeps the default of 3 s.
	VideoPollInterval time.Duration
}

// Client is an OpenRouter implementation of llm.Client. It is safe for
// concurrent use; its configuration is fixed at construction. Every call
// that gets an answer with an unexpected HTTP status fails with an
// *APIError, which also matches the llm sentinel for that status.
type Client struct {
	apiKey            string
	baseURL           string
	httpClient        *http.Client
	httpReferer       string
	xTitle            string
	provider          *ProviderPreferences
	maximumAttempts   int
	videoPollInterval time.Duration
}

var _ llm.Client = (*Client)(nil)

// NewClient builds a Client. It returns an error if options is nil or
// options.APIKey is empty; every other field has a default.
func NewClient(options *Options) (*Client, error) {
	if options == nil || options.APIKey == "" {
		return nil, errors.New("openrouter: APIKey is required")
	}
	client := &Client{
		apiKey:            options.APIKey,
		baseURL:           options.BaseURL,
		httpClient:        options.HTTPClient,
		httpReferer:       options.HTTPReferer,
		xTitle:            options.XTitle,
		provider:          options.Provider.clone(),
		maximumAttempts:   options.MaximumAttempts,
		videoPollInterval: options.VideoPollInterval,
	}
	if client.provider == nil {
		client.provider = preferencesFromOrder(options.ProviderOrder)
	}
	if client.baseURL == "" {
		client.baseURL = DefaultBaseURL
	}
	if client.httpClient == nil {
		client.httpClient = defaultHTTPClient()
	}
	if client.videoPollInterval <= 0 {
		client.videoPollInterval = defaultVideoPollInterval
	}
	return client, nil
}

// defaultHTTPClient clones http.DefaultTransport (dial and TLS timeouts) and
// adds the response-header timeout. An application that replaced
// http.DefaultTransport with its own RoundTripper keeps it, unchanged.
func defaultHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Transport: http.DefaultTransport}
	}
	withHeaderTimeout := transport.Clone()
	withHeaderTimeout.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	return &http.Client{Transport: withHeaderTimeout}
}

// Stream sends one chat-completions request and returns a channel of events.
// EventEnd is always the last event; the channel closes after EventEnd. The
// caller must drain the channel or cancel ctx, otherwise the producer
// goroutine blocks on a full channel.
//
// Retry happens only at stream-start (RES-005): if the HTTP response is 429
// or 5xx before any SSE bytes have been read, aikido closes the body and
// retries per Options.MaximumAttempts. Mid-stream errors do NOT retry; they
// propagate as EventError. A cancelled ctx surfaces as an error matching
// context.Canceled or context.DeadlineExceeded, never as llm.ErrServerError.
func (c *Client) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	start := time.Now()
	body, err := c.buildBody(ctx, request, true)
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}
	response, err := c.openStream(ctx, body) //nolint:bodyclose // closed by the stream goroutine below
	if err != nil {
		return nil, err
	}
	events := make(chan llm.Event, eventBufferSize)
	go func() {
		defer close(events)
		defer response.Body.Close()
		processStream(ctx, response.Body, events, start)
	}()
	return events, nil
}

// openStream retries until a 200 arrives and returns it with its body unread,
// so the stream itself is never retried.
func (c *Client) openStream(ctx context.Context, body []byte) (*http.Response, error) {
	call := httpCall{
		method:     http.MethodPost,
		url:        c.baseURL + "/chat/completions",
		body:       body,
		accept:     "text/event-stream",
		wantStatus: http.StatusOK,
	}
	var response *http.Response
	err := retry.Do(ctx, c.retryPolicy(), func(_ int) error {
		var attemptError error
		response, attemptError = c.send(ctx, call) //nolint:bodyclose // handed to Stream, whose goroutine closes it
		return attemptError
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// buildBody assembles the chat-completions JSON body from llm.Request.
// stream selects SSE-streaming (true) or single-shot JSON (false). With
// stream=false the wire field is omitted via omitempty, which OpenRouter
// treats as non-streaming; Complete relies on that.
func (c *Client) buildBody(ctx context.Context, request llm.Request, stream bool) ([]byte, error) {
	messages, err := buildAPIMessages(request.Messages)
	if err != nil {
		return nil, err
	}
	body := chatRequest{
		Model:              request.Model,
		Messages:           messages,
		Tools:              buildAPITools(request.Tools),
		Stream:             stream,
		MaximumTokens:      request.MaximumTokens,
		Temperature:        request.Temperature,
		Stop:               request.StopSequences,
		Modalities:         request.Modalities,
		ImageConfiguration: toAPIImageConfiguration(request.ImageConfiguration),
		ResponseFormat:     toAPIResponseFormat(request.ResponseFormat),
		Reasoning:          toAPIReasoning(request.Thinking),
		Provider:           c.providerPreferences(ctx),
		// Always ask for usage accounting: cost is the only exact price the
		// caller can get, it costs nothing to request, and a caller that does
		// not want it simply ignores Usage.CostUSD.
		Usage: &apiUsageOptions{Include: true},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode chat request: %w", err)
	}
	return encoded, nil
}

// httpCall is one request to a non-streaming endpoint. Only an answer with
// wantStatus is a result: an unexpected 201 is never parsed as one (ADR-029);
// only the video submit wants 202.
type httpCall struct {
	method     string
	url        string
	body       []byte
	accept     string
	wantStatus int
	// maximumBytes caps the answer's body; 0 means maximumResponseBytes.
	maximumBytes int64
	// shouldRetry narrows the retry predicate for a request that is not
	// idempotent; nil retries every transient error.
	shouldRetry func(error) bool
}

// postJSON sends one JSON body to path (relative to baseURL) and returns the
// raw response bytes. JSON parsing is the caller's job and deliberately sits
// outside the retry loop.
func (c *Client) postJSON(ctx context.Context, path string, body []byte) ([]byte, error) {
	raw, _, err := c.do(ctx, httpCall{
		method:     http.MethodPost,
		url:        c.baseURL + path,
		body:       body,
		accept:     "application/json",
		wantStatus: http.StatusOK,
	})
	return raw, err
}

// do sends call with retry and returns the body and headers of the answer.
// Every non-streaming endpoint shares it, so retry, headers and error
// classification stay in one place. Once an answer with call.wantStatus has
// arrived the request is never sent again (RES-004): the work may already be
// done and billed, so a body that dies mid-read fails the call with
// llm.ErrServerError instead of retrying, and a body above call.maximumBytes
// is llm.ErrInvalidResponse.
func (c *Client) do(ctx context.Context, call httpCall) ([]byte, http.Header, error) {
	var (
		raw      []byte
		header   http.Header
		answered bool
	)
	policy := c.retryPolicy()
	if call.shouldRetry != nil {
		policy.ShouldRetry = call.shouldRetry
	}
	shouldRetry := policy.ShouldRetry
	policy.ShouldRetry = func(err error) bool { return !answered && shouldRetry(err) }
	err := retry.Do(ctx, policy, func(_ int) error {
		response, err := c.send(ctx, call)
		if err != nil {
			return err
		}
		answered = true
		defer response.Body.Close()
		raw, err = readBounded(ctx, response, call.maximumBytes)
		if err != nil {
			return err
		}
		header = response.Header
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return raw, header, nil
}

// readBounded reads the body of response, at most limit bytes (0 means
// maximumResponseBytes). A declared or actual length above limit is
// llm.ErrInvalidResponse; a failed read is a transportError.
func readBounded(ctx context.Context, response *http.Response, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = maximumResponseBytes
	}
	if response.ContentLength > limit {
		return nil, bodyTooLarge(limit)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, transportError(ctx, "read body", err)
	}
	if int64(len(raw)) > limit {
		return nil, bodyTooLarge(limit)
	}
	return raw, nil
}

func bodyTooLarge(limit int64) error {
	return fmt.Errorf("openrouter: response body above %d bytes: %w", limit, llm.ErrInvalidResponse)
}

// send makes one HTTP attempt. It returns the response with its body open
// when the status is call.wantStatus, and a classified error (body closed)
// otherwise.
func (c *Client) send(ctx context.Context, call httpCall) (*http.Response, error) {
	// A fresh *http.Request per attempt: net/http does not allow reusing one.
	request, err := http.NewRequestWithContext(ctx, call.method, call.url, bytes.NewReader(call.body))
	if err != nil {
		return nil, fmt.Errorf("openrouter: build http request: %w", err)
	}
	c.setHeaders(request, call.accept)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, transportError(ctx, "http error", err)
	}
	if response.StatusCode != call.wantStatus {
		return nil, classifyHTTPError(response)
	}
	return response, nil
}

func (c *Client) setHeaders(request *http.Request, accept string) {
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	if c.httpReferer != "" {
		request.Header.Set("HTTP-Referer", c.httpReferer)
	}
	if c.xTitle != "" {
		request.Header.Set("X-Title", c.xTitle)
	}
}

// transportError maps a failed round trip or body read. The caller's own
// cancellation stays a context error, which is never retried (RES-002);
// anything else is a transient network failure, mapped onto ErrServerError.
// The transport's error is kept as text only (LIB-010).
func transportError(ctx context.Context, operation string, err error) error {
	if contextError := ctx.Err(); contextError != nil {
		return fmt.Errorf("openrouter: %s: %w", operation, contextError)
	}
	// A wrapped client timeout would match context.DeadlineExceeded and pass
	// for the caller's own deadline; the sentinel is the contract.
	return fmt.Errorf("openrouter: %s: %v: %w", operation, err, llm.ErrServerError) //nolint:errorlint // LIB-010: the transport error is kept as text on purpose
}
