`ProcessSignup` and `ProcessInvite` in `accounts/service.go` contain the same email normalization and validation block. Extract it into one unexported function in package `accounts` with exactly this signature:

```go
func normalizeEmail(raw string) (string, error)
```

It returns the trimmed, lower-cased address, or the same validation error (same messages, same order of checks) the duplicated code returns today. Replace both copies with a call to `normalizeEmail`, so the validation code exists only once. Behavior must stay identical; do not change the tests. `go vet ./...` and `go test ./...` must pass.
