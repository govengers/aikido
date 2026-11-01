package retry_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/govengers/aikido/retry"
)

var errUnavailable = errors.New("service unavailable")

func ExampleDo() {
	policy := retry.DefaultPolicy()
	policy.BaseDelay = time.Millisecond
	policy.ShouldRetry = func(err error) bool { return errors.Is(err, errUnavailable) }

	err := retry.Do(context.Background(), policy, func(attempt int) error {
		fmt.Println("attempt", attempt)
		if attempt < 3 {
			return errUnavailable
		}
		return nil
	})
	fmt.Println("error:", err)
	// Output:
	// attempt 1
	// attempt 2
	// attempt 3
	// error: <nil>
}

func ExampleAfterError() {
	policy := retry.Policy{MaximumAttempts: 2, BaseDelay: time.Hour, ShouldRetry: func(error) bool { return true }}

	err := retry.Do(context.Background(), policy, func(attempt int) error {
		if attempt == 1 {
			// A 429 whose Retry-After header asks for one millisecond replaces the hour of backoff.
			return &retry.AfterError{Cause: errUnavailable, After: time.Millisecond}
		}
		return nil
	})
	fmt.Println("error:", err)
	// Output:
	// error: <nil>
}
