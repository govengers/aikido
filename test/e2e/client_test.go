//go:build e2e

package e2e

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
)

// retryAfterTolerance absorbs timer granularity when checking that the
// client waited the Retry-After hint.
const retryAfterTolerance = 50 * time.Millisecond

func chatRequest() llm.Request {
	return llm.Request{
		Model:    "e2e/client-model",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "Say hello."}},
	}
}

func TestClientRetriesARateLimitAfterItsRetryAfterAndAServerError(t *testing.T) {
	fake := newFakeOpenRouter(t)
	fake.queue(
		failedChat(http.StatusTooManyRequests, "1", "Rate limit exceeded"),
		failedChat(http.StatusInternalServerError, "", "Internal Server Error"),
		streamedText("Hello."),
	)

	response, err := llm.Collect(t.Context(), newClient(t, fake), chatRequest())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if response.Text != "Hello." {
		t.Errorf("text = %q; want Hello.", response.Text)
	}
	if response.Usage == nil || response.Usage.CostUSD != scriptedCost {
		t.Errorf("usage = %+v; want cost %v", response.Usage, scriptedCost)
	}
	requests := fake.received()
	if len(requests) != 3 {
		t.Fatalf("requests = %d; want 3 (429, 500, success)", len(requests))
	}
	if waited := requests[1].receivedAt.Sub(requests[0].receivedAt); waited < time.Second-retryAfterTolerance {
		t.Errorf("retry after the 429 came %v later; want the Retry-After of 1s", waited)
	}
}

func TestClientSurfacesAnAuthenticationFailureWithoutRetry(t *testing.T) {
	fake := newFakeOpenRouter(t)
	fake.queue(failedChat(http.StatusUnauthorized, "", "No auth credentials found"))

	_, err := llm.Collect(t.Context(), newClient(t, fake), chatRequest())

	if !errors.Is(err, llm.ErrAuthentication) {
		t.Errorf("err = %v; want llm.ErrAuthentication", err)
	}
	apiError, ok := errors.AsType[*openrouter.APIError](err)
	if !ok {
		t.Fatalf("err = %v; want an *openrouter.APIError", err)
	}
	if apiError.StatusCode != http.StatusUnauthorized || apiError.Message != "No auth credentials found" {
		t.Errorf("APIError = %+v; want status 401 and the envelope's message", apiError)
	}
	if requests := fake.received(); len(requests) != 1 {
		t.Errorf("requests = %d; want 1, a 401 is not retried", len(requests))
	}
}
