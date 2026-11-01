package llm

import "errors"

// Errors providers wrap with %w when mapping a failure to a typed cause.
// Match them with errors.Is; the message around them is not part of the API.
var (
	// ErrAuthentication is a rejected or missing credential, or an account
	// that cannot pay (HTTP 401, 402, 403). Retrying does not help.
	ErrAuthentication = errors.New("aikido/llm: authentication failed")
	// ErrRateLimited is HTTP 429. The provider clients in this module retry it
	// themselves (llm/openrouter honoring Retry-After), so it surfaces once
	// their attempts are exhausted.
	ErrRateLimited = errors.New("aikido/llm: rate limited")
	// ErrServerError is a provider-side failure (HTTP 5xx, 408, a broken
	// stream) that may pass on a retry; see IsTransientServerError.
	ErrServerError = errors.New("aikido/llm: provider server error")
	// ErrInvalidRequest is a request the provider refused (other HTTP 4xx) or
	// that a client or helper rejected before sending. Retrying does not help.
	ErrInvalidRequest = errors.New("aikido/llm: invalid request")

	// ErrContentFiltered is wrapped when the provider's safety policy aborts a
	// generation. Detected from finish_reason="content_filter" on a streamed
	// chunk OR a structured error envelope whose code/type signals content
	// filtering. Callers should NOT retry: the same prompt will trip the
	// classifier deterministically.
	//
	// Note: providers sometimes mid-stream RST without sending a structured
	// signal. Those failures still surface as ErrServerError; callers cannot
	// distinguish silent content aborts from genuine transient flake at the
	// wire level.
	ErrContentFiltered = errors.New("aikido/llm: content filtered by provider safety policy")
)
