package openrouter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/retry"
)

// maximumErrorMessageLength trims a provider's error body to what is useful
// inside an error string.
const maximumErrorMessageLength = 256

// retryPolicy returns the policy every HTTP call path uses. It retries only
// rate-limit and 5xx-class errors (RES-002); auth, bad-request and context
// errors short-circuit. Options.MaximumAttempts overrides the attempt count.
func (c *Client) retryPolicy() retry.Policy {
	policy := retry.DefaultPolicy()
	if c.maximumAttempts != 0 {
		policy.MaximumAttempts = c.maximumAttempts
	}
	policy.ShouldRetry = isTransient
	return policy
}

func isTransient(err error) bool {
	return errors.Is(err, llm.ErrRateLimited) || errors.Is(err, llm.ErrServerError)
}

// classifyHTTPError maps an HTTP non-200 response onto a wrapped llm.Err*.
// It reads (and closes) response.Body, attempts to extract the OpenRouter
// error envelope, and returns an error wrapping the appropriate sentinel.
//
// On 429 with a parseable Retry-After header the returned error is wrapped
// in a *retry.AfterError so the retry helper honors the server's hint.
func classifyHTTPError(response *http.Response) error {
	// Best effort: a body that cannot be read only costs the message.
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()

	wrapped := fmt.Errorf("openrouter: status %d: %s: %w", response.StatusCode, errorBodyMessage(body), classifySentinel(response.StatusCode))
	if response.StatusCode == http.StatusTooManyRequests {
		if delay, ok := parseRetryAfter(response.Header.Get("Retry-After")); ok {
			return &retry.AfterError{Cause: wrapped, After: delay}
		}
	}
	return wrapped
}

// errorBodyMessage is the envelope's message, else the raw body, trimmed to
// maximumErrorMessageLength.
func errorBodyMessage(body []byte) string {
	message := string(body)
	if envelope := decodeErrorEnvelope(body); envelope != nil {
		message = envelope.Error.Message
	}
	if len(message) > maximumErrorMessageLength {
		message = message[:maximumErrorMessageLength] + "..."
	}
	return strings.TrimSpace(message)
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

// parseRetryAfter handles RFC 7231: integer seconds OR HTTP-date.
// It returns ok=true with a non-negative delay on success; a date in the
// past is not a hint.
func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := time.Until(date)
		if delay <= 0 {
			return 0, false
		}
		return delay, true
	}
	return 0, false
}
