package openrouter

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/retry"
)

func TestClassifySentinel(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, llm.ErrAuthentication},
		{http.StatusPaymentRequired, llm.ErrAuthentication},
		{http.StatusForbidden, llm.ErrAuthentication},
		{http.StatusTooManyRequests, llm.ErrRateLimited},
		{http.StatusRequestTimeout, llm.ErrServerError},
		{http.StatusBadRequest, llm.ErrInvalidRequest},
		{http.StatusNotFound, llm.ErrInvalidRequest},
		{http.StatusInternalServerError, llm.ErrServerError},
		{http.StatusServiceUnavailable, llm.ErrServerError},
		{http.StatusCreated, llm.ErrServerError},
	}
	for _, testCase := range testCases {
		if got := classifySentinel(testCase.status); !errors.Is(got, testCase.want) {
			t.Errorf("classifySentinel(%d) = %v, want %v", testCase.status, got, testCase.want)
		}
	}
}

// TestAPIError_AsAndIs pins the contract of the exported error: the fields
// through errors.As, the sentinel through errors.Is, the message text as
// before the type existed.
func TestAPIError_AsAndIs(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"error":{"code":404,"message":"model is only available on /api/v1/images"}}`))
	})
	_, err := newTestClient(t, server).Complete(context.Background(), llm.Request{Model: "openai/gpt-image-2.5-flare"})

	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("err = %v, want an *APIError", err)
	}
	if apiError.StatusCode != http.StatusNotFound || apiError.Code != "404" || apiError.Message != "model is only available on /api/v1/images" {
		t.Errorf("APIError = %+v", apiError)
	}
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("err = %v, want errors.Is(ErrInvalidRequest)", err)
	}
	if want := "openrouter: status 404: model is only available on /api/v1/images: aikido/llm: invalid request"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestNewAPIError(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		body        string
		wantCode    string
		wantMessage string
	}{
		{name: "string code", body: `{"error":{"code":"content_policy_violation","message":"blocked"}}`, wantCode: "content_policy_violation", wantMessage: "blocked"},
		{name: "numeric code", body: `{"error":{"code":429,"message":"slow down"}}`, wantCode: "429", wantMessage: "slow down"},
		{name: "no code", body: `{"error":{"message":"upstream down"}}`, wantMessage: "upstream down"},
		{name: "null code", body: `{"error":{"code":null,"message":"x"}}`, wantMessage: "x"},
		{name: "raw body", body: "  Bad Gateway\n", wantMessage: "Bad Gateway"},
		{name: "long raw body", body: strings.Repeat("a", 300), wantMessage: strings.Repeat("a", maximumErrorMessageLength) + "..."},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			apiError := newAPIError(http.StatusBadGateway, []byte(testCase.body))
			if apiError.Code != testCase.wantCode || apiError.Message != testCase.wantMessage {
				t.Errorf("newAPIError(%q) = %+v, want code %q message %q", testCase.body, apiError, testCase.wantCode, testCase.wantMessage)
			}
		})
	}
}

func TestClassifyHTTPError_RetryAfterWrapsAPIError(t *testing.T) {
	t.Parallel()
	response := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"2"}},
		Body:       http.NoBody,
	}
	err := classifyHTTPError(response)
	afterError, ok := errors.AsType[*retry.AfterError](err)
	if !ok || afterError.After != 2*time.Second {
		t.Fatalf("err = %#v, want a *retry.AfterError of 2s", err)
	}
	if _, ok := errors.AsType[*APIError](err); !ok || !errors.Is(err, llm.ErrRateLimited) {
		t.Errorf("err = %v, want an *APIError matching ErrRateLimited", err)
	}
}

// TestClassifyHTTPError_ReadsAtMostTheErrorBodyCap pins that an error page of
// any size costs at most maximumErrorBodyBytes of memory per attempt.
func TestClassifyHTTPError_ReadsAtMostTheErrorBodyCap(t *testing.T) {
	t.Parallel()
	body := &trackedBody{data: bytes.Repeat([]byte("x"), 4*maximumErrorBodyBytes), failAt: -1}
	err := classifyHTTPError(&http.Response{StatusCode: http.StatusBadGateway, Body: body})
	if !errors.Is(err, llm.ErrServerError) {
		t.Errorf("err = %v, want ErrServerError", err)
	}
	if body.position > maximumErrorBodyBytes {
		t.Errorf("read %d bytes, want at most %d", body.position, maximumErrorBodyBytes)
	}
	if !body.closed {
		t.Error("body not closed")
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	testCases := []struct {
		value  string
		wantOK bool
	}{
		{value: "3", wantOK: true},
		{value: " 0 ", wantOK: true},
		{value: "-1"},
		{value: ""},
		{value: "soon"},
		{value: future, wantOK: true},
		{value: past},
	}
	for _, testCase := range testCases {
		if _, ok := parseRetryAfter(testCase.value); ok != testCase.wantOK {
			t.Errorf("parseRetryAfter(%q) ok = %v, want %v", testCase.value, ok, testCase.wantOK)
		}
	}
}
