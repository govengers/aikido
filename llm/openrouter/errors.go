package openrouter

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/retry"
)

// maximumErrorMessageLength trims a provider's error body to what is useful
// inside an error string.
const maximumErrorMessageLength = 256

// APIError is an answer of OpenRouter with an unexpected HTTP status (any
// non-2xx, and a 2xx other than the one the endpoint answers with). Callers
// read its fields with errors.As; errors.Is matches the llm sentinel for the
// status: llm.ErrAuthentication for 401, 402 (no credits) and 403,
// llm.ErrRateLimited for 429, llm.ErrServerError for 408, 5xx and anything
// unexpected, llm.ErrInvalidRequest for every other 4xx. A 429 with a
// Retry-After hint arrives wrapped in a *retry.AfterError.
//
// Errors carried inside a 200 body (a mid-stream error envelope, a failed
// job) are not APIErrors.
type APIError struct {
	// StatusCode is the HTTP status of the answer.
	StatusCode int
	// Code is the error envelope's code as text: a provider symbol
	// ("content_policy_violation") or a mirrored status ("404"). Empty when
	// the body is no envelope or has no code.
	Code string
	// Message is the envelope's message, else the raw body, trimmed to 256
	// bytes. It may be empty.
	Message string
}

// Error reads "openrouter: status <code>: <message>: <sentinel text>".
func (e *APIError) Error() string {
	return fmt.Sprintf("openrouter: status %d: %s: %v", e.StatusCode, e.Message, e.Unwrap())
}

// Unwrap returns the llm sentinel for StatusCode, so errors.Is matches it.
func (e *APIError) Unwrap() error {
	return classifySentinel(e.StatusCode)
}

// classifyHTTPError reads (and closes) the body of an answer with an
// unexpected status and returns it as an *APIError. On 429 with a parseable
// Retry-After header the APIError is wrapped in a *retry.AfterError so the
// retry helper honors the server's hint.
func classifyHTTPError(response *http.Response) error {
	// Best effort: a body that cannot be read only costs the message.
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()

	apiError := newAPIError(response.StatusCode, body)
	if response.StatusCode == http.StatusTooManyRequests {
		if delay, ok := parseRetryAfter(response.Header.Get("Retry-After")); ok {
			return &retry.AfterError{Cause: apiError, After: delay}
		}
	}
	return apiError
}

// newAPIError takes message and code from the OpenRouter error envelope and
// falls back to the raw body as the message.
func newAPIError(statusCode int, body []byte) *APIError {
	apiError := &APIError{StatusCode: statusCode, Message: string(body)}
	if envelope := decodeErrorEnvelope(body); envelope != nil {
		apiError.Message = envelope.Error.Message
		apiError.Code = errorCodeText(envelope.Error.Code)
	}
	if len(apiError.Message) > maximumErrorMessageLength {
		apiError.Message = apiError.Message[:maximumErrorMessageLength] + "..."
	}
	apiError.Message = strings.TrimSpace(apiError.Message)
	return apiError
}

// errorCodeText is a JSON string code as is and any other code (a number) as
// its JSON text; null or absent is empty.
func errorCodeText(raw json.RawMessage) string {
	if code := decodeErrorCodeAsString(raw); code != "" {
		return code
	}
	text := strings.TrimSpace(string(raw))
	if text == "null" {
		return ""
	}
	return text
}

// classifySentinel maps HTTP status onto an llm sentinel. See
// OPENROUTER-DETAILS.md for the rationale on each row.
func classifySentinel(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return llm.ErrAuthentication
	case status == http.StatusPaymentRequired:
		// 402: insufficient credits. Auth-class: the caller must fix something.
		return llm.ErrAuthentication
	case status == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case status >= 500 && status <= 599:
		return llm.ErrServerError
	case status == http.StatusRequestTimeout:
		return llm.ErrServerError
	case status >= 400 && status <= 499:
		return llm.ErrInvalidRequest
	default:
		return llm.ErrServerError
	}
}

// decodeErrorEnvelope parses the OpenRouter error JSON. It returns nil when
// the body is not an envelope with a message, so callers fall back to the raw
// body.
func decodeErrorEnvelope(body []byte) *errorEnvelope {
	var envelope errorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	if envelope.Error.Message == "" {
		return nil
	}
	return &envelope
}
