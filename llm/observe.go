package llm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The operations an ObservedClient reports.
const (
	OperationStream   = "stream"
	OperationComplete = "complete"
)

// Call is one model call an ObservedClient makes.
type Call struct {
	Client    string // ObservedClientOptions.Name: "openrouter", "codex"
	Operation string // OperationStream or OperationComplete
	Model     string // Request.Model; empty when the request leaves the client's default
}

// CallOutcome is how a model call ended.
type CallOutcome struct {
	Usage        *Usage // the provider's accounting; nil when it reported none
	FinishReason string
	Err          error         // nil on success; context.Canceled or DeadlineExceeded when the caller gave up
	Duration     time.Duration // from the call to the end of the stream or the response
}

// Observer is told about every model call an ObservedClient makes, for metrics, traces or
// logs; the adapter packages implement it (llm/prometheusadapter, llm/oteladapter,
// llm/wideeventadapter). CallStarted returns the context the call runs in, so a tracing
// observer can put its span there for the provider's own instrumentation; observers that
// keep nothing return ctx. CallFinished gets that context back. Both run on the caller's
// path and must be fast and safe for concurrent use.
type Observer interface {
	CallStarted(ctx context.Context, call *Call) context.Context
	CallFinished(ctx context.Context, call *Call, outcome *CallOutcome)
}

// ObservedClientOptions configures NewObservedClient.
type ObservedClientOptions struct {
	Client    Client     // required: the client whose calls are observed
	Name      string     // required: Call.Client, the provider's name in metrics and traces ("openrouter")
	Observers []Observer // required, at least one; started in order, finished in reverse order
}

// ObservedClient is a Client that reports every Stream and Complete call of the client it
// wraps to its Observers. It implements only Client: the optional capabilities of the
// wrapped client (ImageGenerator, SpeechGenerator, ...) are not observed and not reachable
// through it, so keep the original for those.
type ObservedClient struct {
	client    Client
	name      string
	observers []Observer
}

var _ Client = (*ObservedClient)(nil)

// NewObservedClient wraps options.Client.
func NewObservedClient(options *ObservedClientOptions) (*ObservedClient, error) {
	if options == nil || options.Client == nil || options.Name == "" || len(options.Observers) == 0 {
		return nil, errors.New("llm: ObservedClientOptions needs a Client, a Name and at least one Observer")
	}
	for _, observer := range options.Observers {
		if observer == nil {
			return nil, errors.New("llm: ObservedClientOptions.Observers holds a nil Observer")
		}
	}
	return &ObservedClient{client: options.Client, name: options.Name, observers: append([]Observer(nil), options.Observers...)}, nil
}

// Stream calls the wrapped client's Stream and reports the call when its channel closes,
// with the usage, finish reason and error the events carried. The events pass through
// unchanged.
func (c *ObservedClient) Stream(ctx context.Context, request Request) (<-chan Event, error) {
	call, callContext, start := c.start(ctx, OperationStream, request.Model)
	events, err := c.client.Stream(callContext, request)
	if err != nil {
		c.finish(callContext, call, &CallOutcome{Err: err, Duration: time.Since(start)})
		return nil, err
	}
	observed := make(chan Event)
	go c.forward(callContext, call, start, events, observed)
	return observed, nil
}

// forward hands every event on and reports the call once the stream ended or the caller
// gave up. The caller reads until the channel closes or cancels ctx (the Client contract); on
// cancellation the rest of the wrapped stream is drained in the background, so a producer
// that does not watch ctx cannot block forever. A panic of an observer or of the stream
// handling reaches the caller as an EventError instead of ending the process.
func (c *ObservedClient) forward(ctx context.Context, call *Call, start time.Time, events <-chan Event, observed chan<- Event) {
	defer close(observed)
	defer func() {
		if recovered := recover(); recovered != nil {
			select {
			case observed <- Event{Kind: EventError, Err: fmt.Errorf("%w: observing the stream panicked: %v", ErrInvalidResponse, recovered)}:
			case <-ctx.Done():
			}
		}
	}()
	outcome := &CallOutcome{}
	defer func() {
		outcome.Duration = time.Since(start)
		c.finish(ctx, call, outcome)
	}()
	ended := false
	for {
		select {
		case event, open := <-events:
			if !open {
				if !ended && outcome.Err == nil && ctx.Err() != nil {
					outcome.Err = ctx.Err() // closed early because the caller gave up
				}
				return
			}
			record(outcome, event)
			ended = ended || event.Kind == EventEnd
			select {
			case observed <- event:
			case <-ctx.Done():
				abandon(ctx, outcome, ended, events)
				return
			}
		case <-ctx.Done():
			abandon(ctx, outcome, ended, events)
			return
		}
	}
}

// abandon records a caller that gave up before the stream ended and drains the rest of it.
func abandon(ctx context.Context, outcome *CallOutcome, ended bool, events <-chan Event) {
	if !ended && outcome.Err == nil {
		outcome.Err = ctx.Err()
	}
	go func() {
		for range events { // drain: the producer may still send before it sees ctx
		}
	}()
}

// record folds the event into the outcome.
func record(outcome *CallOutcome, event Event) {
	switch event.Kind {
	case EventUsage:
		outcome.Usage = event.Usage
	case EventError:
		outcome.Err = event.Err
		if event.FinishReason != "" {
			outcome.FinishReason = event.FinishReason
		}
	case EventEnd:
		outcome.FinishReason = event.FinishReason
	default:
	}
}

// Complete calls the wrapped client's Complete and reports the call.
func (c *ObservedClient) Complete(ctx context.Context, request Request) (Response, error) {
	call, callContext, start := c.start(ctx, OperationComplete, request.Model)
	response, err := c.client.Complete(callContext, request)
	c.finish(callContext, call, &CallOutcome{Usage: response.Usage, FinishReason: response.FinishReason, Err: err, Duration: time.Since(start)})
	return response, err
}

func (c *ObservedClient) start(ctx context.Context, operation, model string) (*Call, context.Context, time.Time) {
	call := &Call{Client: c.name, Operation: operation, Model: model}
	for _, observer := range c.observers {
		ctx = observer.CallStarted(ctx, call) //nolint:fatcontext // each observer wraps the previous one's context once, bounded by the observers
	}
	return call, ctx, time.Now()
}

func (c *ObservedClient) finish(ctx context.Context, call *Call, outcome *CallOutcome) {
	for index := len(c.observers) - 1; index >= 0; index-- {
		c.observers[index].CallFinished(ctx, call, outcome)
	}
}

// The values of ErrorClass.
const (
	ErrorClassAuthentication   = "authentication"
	ErrorClassRateLimited      = "rate_limited"
	ErrorClassServerError      = "server_error"
	ErrorClassInvalidRequest   = "invalid_request"
	ErrorClassInvalidResponse  = "invalid_response"
	ErrorClassContentFiltered  = "content_filtered"
	ErrorClassCanceled         = "canceled"
	ErrorClassDeadlineExceeded = "deadline_exceeded"
	ErrorClassOther            = "other"
)

// ErrorClass names the kind of a model call's error from a closed set, for metrics, span
// status and logs: the error's text can carry the provider's raw response, which does not
// belong there. A nil err is "".
func ErrorClass(err error) string {
	for _, class := range []struct {
		target error
		name   string
	}{
		{context.Canceled, ErrorClassCanceled},
		{context.DeadlineExceeded, ErrorClassDeadlineExceeded},
		{ErrAuthentication, ErrorClassAuthentication},
		{ErrRateLimited, ErrorClassRateLimited},
		{ErrServerError, ErrorClassServerError},
		{ErrInvalidRequest, ErrorClassInvalidRequest},
		{ErrInvalidResponse, ErrorClassInvalidResponse},
		{ErrContentFiltered, ErrorClassContentFiltered},
	} {
		if errors.Is(err, class.target) {
			return class.name
		}
	}
	if err == nil {
		return ""
	}
	return ErrorClassOther
}
