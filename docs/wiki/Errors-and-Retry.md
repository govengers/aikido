# Errors and retry

Errors are matched with `errors.Is` against sentinels and read with `errors.As`; the message around them is not API. Retry happens at well-defined places only, never in the middle of a stream.

## Sentinels

| Package | Error | Meaning |
|---|---|---|
| `llm` | `ErrAuthentication` | rejected or missing credential, or an account that cannot pay (401, 402, 403); retrying does not help |
| `llm` | `ErrRateLimited` | 429, after the client's own attempts are used up |
| `llm` | `ErrServerError` | 5xx, 408, a broken stream, an unexpected status; may pass on a retry |
| `llm` | `ErrInvalidRequest` | other 4xx, or refused before sending (missing model, unsupported feature) |
| `llm` | `ErrContentFiltered` | the provider's safety policy aborted the generation; do not retry the same prompt |
| `llm/openrouter` | `*APIError{StatusCode, Code, Message}` | any unexpected HTTP status; `Unwrap` returns the `llm` sentinel above. A 429 with `Retry-After` arrives wrapped in a `*retry.AfterError` |
| `llm/codex` | `ErrReauthenticationRequired`, `ErrDeviceLoginDisabled` | the login is gone for good; the issuer has no device login |
| `tools` | `ErrUnknownTool`, `ErrInvalidArguments`, `ErrToolPanicked`, `ErrDuplicateTool` | [Tools](Tools.md#what-dispatch-checks) |
| `vfs` | `ErrPathInvalid`, `ErrFileNotFound`, `ErrFileTooLarge`, `ErrReadOnly` | [Files](Files.md#the-storage-contract) |
| `agent/history/memory` | `ErrEmptySessionID` | |
| `llm/llmtest` | `ErrStubExhausted` | a scripted client ran out of scripts |

A cancelled or expired `ctx` surfaces as `context.Canceled` or `context.DeadlineExceeded`, never as `ErrServerError`, so it is neither reported nor retried as a provider failure.

`llm.IsTransientServerError(err)` is true for `ErrServerError` only (not for rate limits, which the clients retry themselves). `llm.IsContentFilter(err)` detects a content filter, also from a structured error envelope. Providers sometimes drop a stream without any signal; that is an `ErrServerError` and indistinguishable from a flake.

Agent runs do not return errors: they end with `EventError` plus an `EndReason` ([Agent](Agent.md#end-reasons)). Tool errors never end a run.

## Retry per call path

| Path | Retried | Not retried |
|---|---|---|
| `openrouter` `Stream` | 429, 5xx and network errors before the first SSE byte, `Options.MaximumAttempts` in total (default 3), `Retry-After` honoured | anything after the stream started: it ends with `EventError` |
| `openrouter` `Complete`, images, speech, decisions, each video submit, poll and download | the same rule per HTTP request | authentication, invalid request, content filter, context errors |
| `codex` `Stream` | 429 and 5xx at stream start (3 attempts) | mid-stream errors |
| `llm.CollectWithRetry` | restarts the whole stream after a failure `policy.ShouldRetry` accepts (default `llm.IsTransientServerError`); every attempt is billed | |
| `llm.CompleteWithRetry` | a fresh `Complete` per attempt, same default predicate | |
| `agent.Session` | nothing beyond the client's own attempts; a failed model call ends the run and leaves History as it was, so the caller can run again | |

`llm.DefaultStreamingRetryPolicy()` (5 attempts, 2 s base, 30 s cap, 20 % jitter, transient errors only) suits flaky image previews. From [llm/example_test.go](../../llm/example_test.go) (`ExampleCompleteWithRetry`):

```go
	policy := retry.Policy{MaximumAttempts: 3, BaseDelay: time.Millisecond}

	// ShouldRetry is nil, so only transient server errors are retried: a content filter is final.
	_, err := llm.CompleteWithRetry(context.Background(), client, llm.Request{Model: "m"}, policy)
	fmt.Println(llm.IsContentFilter(err), llm.IsTransientServerError(err), len(client.Requests()))
```

## retry.Do

`retry.Do(ctx, policy, func(attempt int) error)` calls the operation until it succeeds, `ShouldRetry` refuses, or `MaximumAttempts` is reached, and returns the last error unchanged. From [retry/example_test.go](../../retry/example_test.go) (`ExampleDo`):

```go
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
```

| `retry.Policy` | Zero or out-of-range value means |
|---|---|
| `MaximumAttempts` | below 1: one call, no retry |
| `BaseDelay` | zero or negative: no wait between attempts |
| `MaximumDelay` | zero or negative: 30 s; caps every wait, also a `Retry-After` |
| `Multiplier` | below 1: 2 |
| `Jitter` | 0 or negative: none, above 1: 1; `Retry-After` waits are never jittered |
| `ShouldRetry` | nil: every error |

`retry.DefaultPolicy()` is 3 attempts, 500 ms base, 30 s cap, 2x, 20 % jitter. `Do` never retries `context.Canceled` or `context.DeadlineExceeded`, whatever `ShouldRetry` says, and returns the last error without waiting when the wait would end after the context's deadline. An operation that knows the server's wish returns `&retry.AfterError{Cause: err, After: d}`; `ShouldRetry` still decides whether there is a next attempt.
