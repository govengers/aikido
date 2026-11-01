package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

func TestNewSession_RequiredFields(t *testing.T) {
	stubClient := llmtest.NewStubClient()
	testCases := []struct {
		name    string
		options *SessionOptions
	}{
		{"nil options", nil},
		{"missing ID", &SessionOptions{Client: stubClient, Model: "m", History: noopHistory{}, Locker: NewLocalLocker()}},
		{"missing Client", &SessionOptions{ID: "s", Model: "m", History: noopHistory{}, Locker: NewLocalLocker()}},
		{"missing Model", &SessionOptions{ID: "s", Client: stubClient, History: noopHistory{}, Locker: NewLocalLocker()}},
		{"missing History", &SessionOptions{ID: "s", Client: stubClient, Model: "m", Locker: NewLocalLocker()}},
		{"missing Locker", &SessionOptions{ID: "s", Client: stubClient, Model: "m", History: noopHistory{}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewSession(testCase.options); err == nil {
				t.Errorf("expected error for %s", testCase.name)
			}
		})
	}
}

func TestNewSession_DefaultsApplied(t *testing.T) {
	session, err := NewSession(&SessionOptions{
		ID:      "s",
		Client:  llmtest.NewStubClient(),
		Model:   "m",
		History: noopHistory{},
		Locker:  NewLocalLocker(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if session.options.MaximumTurns != 20 {
		t.Errorf("MaximumTurns default = %d; want 20", session.options.MaximumTurns)
	}
	if session.options.RunTimeout != 10*time.Minute {
		t.Errorf("RunTimeout default = %v; want 10m", session.options.RunTimeout)
	}
	if session.options.LLMCallTimeout != 180*time.Second {
		t.Errorf("LLMCallTimeout default = %v; want 180s", session.options.LLMCallTimeout)
	}
	if session.options.MaximumTokens != 16384 {
		t.Errorf("MaximumTokens default = %d; want 16384", session.options.MaximumTokens)
	}
}

func TestNewSession_NegativeTimeoutErrors(t *testing.T) {
	_, err := NewSession(&SessionOptions{
		ID: "s", Client: llmtest.NewStubClient(), Model: "m",
		History: noopHistory{}, Locker: NewLocalLocker(),
		RunTimeout: -1 * time.Second,
	})
	if err == nil {
		t.Error("expected error for negative RunTimeout")
	}
	_, err = NewSession(&SessionOptions{
		ID: "s", Client: llmtest.NewStubClient(), Model: "m",
		History: noopHistory{}, Locker: NewLocalLocker(),
		LLMCallTimeout: -1 * time.Second,
	})
	if err == nil {
		t.Error("expected error for negative LLMCallTimeout")
	}
}

func TestLocalLocker(t *testing.T) {
	locker := NewLocalLocker()
	unlock, err := locker.Lock(context.Background(), "s")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	expired, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	if _, err := locker.Lock(expired, "s"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock on a held ID with an expired context = %v; want DeadlineExceeded", err)
	}
	otherUnlock, err := locker.Lock(context.Background(), "other")
	if err != nil {
		t.Fatalf("Lock on another ID: %v", err)
	}
	otherUnlock()

	unlock()
	unlock() // idempotent: a second call must not release a later holder
	relock, err := locker.Lock(context.Background(), "s")
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	if _, err := locker.Lock(expired, "s"); err == nil {
		t.Error("the repeated unlock released the new holder's lock")
	}
	relock()

	locker.Forget("s")
	afterForget, err := locker.Lock(context.Background(), "s")
	if err != nil {
		t.Fatalf("Lock after Forget: %v", err)
	}
	afterForget()
}

// noopHistory is a History that never errors but stores nothing.
type noopHistory struct{}

func (noopHistory) Append(context.Context, string, ...llm.Message) error { return nil }

func (noopHistory) Read(context.Context, string) ([]llm.Message, error) { return nil, nil }

// errorHistory fails Append and Read with the configured errors.
type errorHistory struct {
	appendError error
	readError   error
}

func (h errorHistory) Append(context.Context, string, ...llm.Message) error {
	return h.appendError
}

func (h errorHistory) Read(context.Context, string) ([]llm.Message, error) {
	return nil, h.readError
}

// errorLocker always returns an error.
type errorLocker struct{ err error }

func (l errorLocker) Lock(context.Context, string) (func(), error) { return nil, l.err }

// blockingClient streams nothing until ctx is done. A non-nil streaming
// channel is closed when Stream is called, so use it for one call only.
type blockingClient struct {
	streaming chan struct{}
}

func (c blockingClient) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	if c.streaming != nil {
		close(c.streaming)
	}
	events := make(chan llm.Event)
	go func() {
		<-ctx.Done()
		close(events)
	}()
	return events, nil
}

func (blockingClient) Complete(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

// gatedClient reports each Stream call on streaming, then replies "x" once
// release is closed.
type gatedClient struct {
	streaming chan<- struct{}
	release   <-chan struct{}
}

func (c gatedClient) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	c.streaming <- struct{}{}
	events := make(chan llm.Event, 2)
	go func() {
		defer close(events)
		select {
		case <-c.release:
			events <- llm.Event{Kind: llm.EventTextDelta, Text: "x"}
			events <- llm.Event{Kind: llm.EventEnd}
		case <-ctx.Done():
		}
	}()
	return events, nil
}

func (gatedClient) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("gatedClient: Complete is not scripted")
}

// dialErrorClient fails Stream before any event.
type dialErrorClient struct{ err error }

func (c dialErrorClient) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	return nil, c.err
}

func (c dialErrorClient) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, c.err
}
