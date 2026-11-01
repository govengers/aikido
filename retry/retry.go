// Package retry runs an operation again after transient failures, with
// exponential backoff, jitter and a cap on every wait. The provider clients
// (llm/openrouter, llm/codex) use it for their HTTP calls, and callers wrap
// higher-level operations such as llm.CollectWithRetry in the same Policy
// (ADR-027).
//
// Do never retries a failure caused by a cancelled context or an expired
// deadline, never waits past the context's deadline, and honors a server's
// Retry-After hint carried by AfterError.
package retry

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

// Defaults Do applies to a Policy field left at its zero value.
const (
	defaultMaximumDelay = 30 * time.Second
	defaultMultiplier   = 2.0
)

// Policy configures Do. The zero value calls the operation once.
type Policy struct {
	// MaximumAttempts is the total number of attempts, the first included.
	// Values below 1 mean 1: call once, never retry.
	MaximumAttempts int

	// BaseDelay is the wait before the second attempt. Zero or negative means
	// the attempts follow each other without waiting.
	BaseDelay time.Duration

	// MaximumDelay caps every single wait, also one requested by AfterError.
	// Zero or negative means 30 seconds.
	MaximumDelay time.Duration

	// Multiplier is the growth factor of the wait from one attempt to the
	// next. Values below 1 mean 2.
	Multiplier float64

	// Jitter randomizes every computed wait by up to this fraction in both
	// directions: 0.2 waits uniformly between 80% and 120% of the computed
	// wait. 0 or negative means no jitter, values above 1 mean 1. A wait
	// requested by AfterError is never jittered.
	Jitter float64

	// ShouldRetry reports whether a failed attempt is worth another one. Nil
	// retries every error. Do never retries context.Canceled or
	// context.DeadlineExceeded, whatever ShouldRetry answers.
	ShouldRetry func(err error) bool
}

// DefaultPolicy returns the project-wide default: 3 attempts, 500 ms base
// delay, 30 s cap, 2x multiplier, 20% jitter. Its ShouldRetry retries every
// error; callers that know their failures override it with a predicate on
// their typed errors, as the provider clients do with llm.ErrRateLimited and
// llm.ErrServerError.
func DefaultPolicy() Policy {
	return Policy{
		MaximumAttempts: 3,
		BaseDelay:       500 * time.Millisecond,
		MaximumDelay:    defaultMaximumDelay,
		Multiplier:      defaultMultiplier,
		Jitter:          0.2,
		ShouldRetry:     retryEveryError,
	}
}

// AfterError overrides the computed backoff with the wait a server asked for.
// Provider clients wrap a 429 response with a Retry-After header in it. Do
// waits After (capped by Policy.MaximumDelay) before the next attempt; whether
// there is a next attempt is still up to Policy.ShouldRetry, which sees the
// AfterError itself and matches Cause through errors.Is.
type AfterError struct {
	// Cause is the failure being retried. Error and Unwrap return it.
	Cause error
	// After is the requested wait. Zero or negative falls back to the
	// policy's backoff.
	After time.Duration
}

// Error returns the message of Cause, or "retry after" without one.
func (e *AfterError) Error() string {
	if e.Cause == nil {
		return "retry after"
	}
	return e.Cause.Error()
}

// Unwrap returns Cause.
func (e *AfterError) Unwrap() error { return e.Cause }

// Do calls operation until it succeeds, ShouldRetry or a context error rules
// out another attempt, or MaximumAttempts is reached. The attempt passed to
// operation counts from 1.
//
// Do returns nil on success, otherwise the error of the last attempt
// unchanged. It returns ctx.Err() when ctx ends before the first attempt or
// during a wait, and it returns the last attempt's error without waiting when
// the wait would end after ctx's deadline (RES-006).
func Do(ctx context.Context, policy Policy, operation func(attempt int) error) error {
	policy = policy.withDefaults()
	var lastError error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return errorBeforeAttempt(lastError, err)
		}
		err := operation(attempt)
		if err == nil {
			return nil
		}
		if attempt >= policy.MaximumAttempts || !policy.retryable(err) {
			return err
		}
		wait := policy.delay(attempt, err, rand.Float64()) //nolint:gosec // G404: backoff jitter needs no cryptographic randomness
		if endsAfterDeadline(ctx, wait) {
			return err
		}
		if waitError := sleep(ctx, wait); waitError != nil {
			return waitError
		}
		lastError = err
	}
}

// errorBeforeAttempt is what Do returns when ctx ended right before an
// attempt: the previous attempt's failure says more than the context error.
func errorBeforeAttempt(lastError, contextError error) error {
	if lastError != nil {
		return lastError
	}
	return contextError
}

func retryEveryError(error) bool { return true }

func (p Policy) withDefaults() Policy {
	if p.MaximumAttempts < 1 {
		p.MaximumAttempts = 1
	}
	if p.MaximumDelay <= 0 {
		p.MaximumDelay = defaultMaximumDelay
	}
	if p.Multiplier < 1 {
		p.Multiplier = defaultMultiplier
	}
	if p.ShouldRetry == nil {
		p.ShouldRetry = retryEveryError
	}
	return p
}

// retryable applies RES-002: a cancelled or expired context is final, the rest
// is the policy's call.
func (p Policy) retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return p.ShouldRetry(err)
}

// delay is the wait after the failed attempt, at most MaximumDelay. random is
// a sample from [0, 1) that drives the jitter.
func (p Policy) delay(attempt int, err error, random float64) time.Duration {
	if afterError, ok := errors.AsType[*AfterError](err); ok && afterError.After > 0 {
		return min(afterError.After, p.MaximumDelay)
	}
	return p.backoff(attempt, random)
}

// backoff is BaseDelay * Multiplier^(attempt-1), jittered by random and
// capped at MaximumDelay. The float arithmetic saturates at the cap instead of
// overflowing for any attempt count.
func (p Policy) backoff(attempt int, random float64) time.Duration {
	if p.BaseDelay <= 0 {
		return 0
	}
	wait := float64(p.BaseDelay) * math.Pow(p.Multiplier, float64(attempt-1))
	jitter := min(max(p.Jitter, 0), 1)
	wait *= 1 + jitter*(2*random-1)
	if wait >= float64(p.MaximumDelay) {
		return p.MaximumDelay
	}
	return time.Duration(wait)
}

func endsAfterDeadline(ctx context.Context, wait time.Duration) bool {
	deadline, hasDeadline := ctx.Deadline()
	return hasDeadline && time.Now().Add(wait).After(deadline)
}

// sleep waits for duration or until ctx ends, whichever comes first.
func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
