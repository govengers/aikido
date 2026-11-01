// Package llmtest provides public test helpers for the llm package.
//
// StubClient is a scriptable llm.Client. Each Stream call consumes one
// TurnScript; the agent loop tests script multi-turn conversations as a slice
// of TurnScripts.
package llmtest

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/govengers/aikido/llm"
)

// ErrStubExhausted is returned by every StubClient call whose scripts are
// used up. The exhausted call is not recorded.
var ErrStubExhausted = errors.New("aikido/llmtest: stub client: script exhausted")

// TurnScript is the events the stub emits for one Stream call.
// The same script drives Stream and Complete.
type TurnScript struct {
	// Events are emitted in order. The stub does not add an EventEnd.
	Events []llm.Event
	// Block, if non-nil, holds the turn until the channel is closed (or
	// receives) before any event: Stream's producer emits nothing, Complete
	// does not return. Context cancellation ends the wait; Stream then closes
	// its channel without events and Complete returns ctx.Err(). Useful for
	// timeout tests.
	Block <-chan struct{}
}

// ImageScript is one scripted GenerateImage outcome. Err short-circuits the
// call; otherwise Response is returned verbatim.
type ImageScript struct {
	Response llm.ImageResponse
	Err      error // returned as is, together with Response
}

// SpeechScript is one scripted Speak outcome. Err short-circuits the call;
// otherwise Response is returned verbatim.
type SpeechScript struct {
	Response llm.SpeechResponse
	Err      error // returned as is, together with Response
}

// VideoScript is one scripted GenerateVideo outcome. Err short-circuits the
// call; otherwise Response is returned verbatim.
type VideoScript struct {
	Response llm.VideoResponse
	Err      error // returned as is, together with Response
}

// DecisionScript is one scripted Decide outcome. Err short-circuits the
// call; otherwise Response is returned verbatim.
type DecisionScript struct {
	Response llm.DecisionResponse
	Err      error // returned as is, together with Response
}

// StubClient is a scriptable llm.Client that also implements the optional
// llm.ImageGenerator, llm.SpeechGenerator, llm.VideoGenerator and llm.Decider
// capabilities. Each capability plays its own scripts in order and records
// the requests it was called with. It is safe for concurrent use.
type StubClient struct {
	mutex            sync.Mutex
	turns            []TurnScript
	turnCursor       int
	requests         []llm.Request
	images           []ImageScript
	imageCursor      int
	imageRequests    []llm.ImageRequest
	speech           []SpeechScript
	speechCursor     int
	speechRequests   []llm.SpeechRequest
	videos           []VideoScript
	videoCursor      int
	videoRequests    []llm.VideoRequest
	decisions        []DecisionScript
	decisionCursor   int
	decisionRequests []llm.DecisionRequest
}

var (
	_ llm.Client          = (*StubClient)(nil)
	_ llm.ImageGenerator  = (*StubClient)(nil)
	_ llm.SpeechGenerator = (*StubClient)(nil)
	_ llm.VideoGenerator  = (*StubClient)(nil)
	_ llm.Decider         = (*StubClient)(nil)
)

// NewStubClient returns a StubClient that plays the given turn scripts in order.
func NewStubClient(turns ...TurnScript) *StubClient {
	return &StubClient{turns: turns}
}

// Requests returns a copy of every llm.Request Stream and Complete have been
// called with, in order.
func (s *StubClient) Requests() []llm.Request {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return slices.Clone(s.requests)
}

// Stream consumes one TurnScript and emits its events from a goroutine that
// closes the channel after the last event, or as soon as ctx ends.
func (s *StubClient) Stream(ctx context.Context, request llm.Request) (<-chan llm.Event, error) {
	turn, err := s.nextTurn(request)
	if err != nil {
		return nil, err
	}
	events := make(chan llm.Event, len(turn.Events)+1)
	go func() {
		defer close(events)
		if waitForRelease(ctx, turn.Block) != nil {
			return
		}
		for _, event := range turn.Events {
			select {
			case <-ctx.Done():
				return
			case events <- event:
			}
		}
	}()
	return events, nil
}

// Complete consumes one TurnScript and folds its events into a Response the
// way llm.Collect does. EventError short-circuits with that error and the
// Response gathered so far (matching live-client semantics).
func (s *StubClient) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	turn, err := s.nextTurn(request)
	if err != nil {
		return llm.Response{}, err
	}
	if err := waitForRelease(ctx, turn.Block); err != nil {
		return llm.Response{}, err
	}
	var response llm.Response
	for _, event := range turn.Events {
		if event.Kind == llm.EventError {
			return response, event.Err
		}
		foldEvent(&response, event)
	}
	return response, nil
}

func (s *StubClient) nextTurn(request llm.Request) (TurnScript, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.turnCursor >= len(s.turns) {
		return TurnScript{}, ErrStubExhausted
	}
	turn := s.turns[s.turnCursor]
	s.turnCursor++
	s.requests = append(s.requests, request)
	return turn, nil
}

// waitForRelease waits until block is released or ctx ends; a nil block
// does not wait.
func waitForRelease(ctx context.Context, block <-chan struct{}) error {
	if block == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-block:
		return nil
	}
}

func foldEvent(response *llm.Response, event llm.Event) {
	switch event.Kind {
	case llm.EventTextDelta:
		response.Text += event.Text
	case llm.EventToolCall:
		if event.Tool != nil {
			response.ToolCalls = append(response.ToolCalls, *event.Tool)
		}
	case llm.EventImage:
		if event.Image != nil {
			response.Images = append(response.Images, *event.Image)
		}
	case llm.EventUsage:
		response.Usage = event.Usage
	case llm.EventEnd:
		response.FinishReason = event.FinishReason
	case llm.EventThinking, llm.EventError:
	}
}

// ScriptImages appends image scripts consumed by GenerateImage in order.
// Separate from the turn scripts: a test can drive both endpoints on one stub.
func (s *StubClient) ScriptImages(scripts ...ImageScript) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.images = append(s.images, scripts...)
}

// ImageRequests returns a copy of every llm.ImageRequest the stub has been
// called with, in order.
func (s *StubClient) ImageRequests() []llm.ImageRequest {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return slices.Clone(s.imageRequests)
}

// GenerateImage consumes one ImageScript. Returns ErrStubExhausted when no
// image scripts remain.
func (s *StubClient) GenerateImage(_ context.Context, request llm.ImageRequest) (llm.ImageResponse, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.imageCursor >= len(s.images) {
		return llm.ImageResponse{}, ErrStubExhausted
	}
	script := s.images[s.imageCursor]
	s.imageCursor++
	s.imageRequests = append(s.imageRequests, request)
	return script.Response, script.Err
}

// ScriptSpeech appends speech scripts consumed by Speak in order.
func (s *StubClient) ScriptSpeech(scripts ...SpeechScript) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.speech = append(s.speech, scripts...)
}

// SpeechRequests returns a copy of every llm.SpeechRequest the stub has
// been called with, in order.
func (s *StubClient) SpeechRequests() []llm.SpeechRequest {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return slices.Clone(s.speechRequests)
}

// Speak consumes one SpeechScript. Returns ErrStubExhausted when no speech
// scripts remain.
func (s *StubClient) Speak(_ context.Context, request llm.SpeechRequest) (llm.SpeechResponse, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.speechCursor >= len(s.speech) {
		return llm.SpeechResponse{}, ErrStubExhausted
	}
	script := s.speech[s.speechCursor]
	s.speechCursor++
	s.speechRequests = append(s.speechRequests, request)
	return script.Response, script.Err
}

// ScriptVideos appends video scripts consumed by GenerateVideo in order.
func (s *StubClient) ScriptVideos(scripts ...VideoScript) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.videos = append(s.videos, scripts...)
}

// VideoRequests returns a copy of every llm.VideoRequest the stub has been
// called with, in order.
func (s *StubClient) VideoRequests() []llm.VideoRequest {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return slices.Clone(s.videoRequests)
}

// GenerateVideo consumes one VideoScript. Returns ErrStubExhausted when no
// video scripts remain.
func (s *StubClient) GenerateVideo(_ context.Context, request llm.VideoRequest) (llm.VideoResponse, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.videoCursor >= len(s.videos) {
		return llm.VideoResponse{}, ErrStubExhausted
	}
	script := s.videos[s.videoCursor]
	s.videoCursor++
	s.videoRequests = append(s.videoRequests, request)
	return script.Response, script.Err
}

// ScriptDecisions appends decision scripts consumed by Decide in order.
func (s *StubClient) ScriptDecisions(scripts ...DecisionScript) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.decisions = append(s.decisions, scripts...)
}

// DecisionRequests returns a copy of every llm.DecisionRequest the stub
// has been called with, in order.
func (s *StubClient) DecisionRequests() []llm.DecisionRequest {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return slices.Clone(s.decisionRequests)
}

// Decide consumes one DecisionScript. Returns ErrStubExhausted when no
// decision scripts remain.
func (s *StubClient) Decide(_ context.Context, request llm.DecisionRequest) (llm.DecisionResponse, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.decisionCursor >= len(s.decisions) {
		return llm.DecisionResponse{}, ErrStubExhausted
	}
	script := s.decisions[s.decisionCursor]
	s.decisionCursor++
	s.decisionRequests = append(s.decisionRequests, request)
	return script.Response, script.Err
}
