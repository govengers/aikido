package openrouter

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/retry"
)

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
