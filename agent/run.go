package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/google/uuid"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
)

// eventBufferSize is how many events a run queues before it waits for the
// caller to read.
const eventBufferSize = 16

// RunOptions tune one Run without changing the Session.
type RunOptions struct {
	// SystemSuffix is appended to SessionOptions.SystemPrompt for this run
	// only, e.g. a reply-style instruction for a spoken answer. It is never
	// written to History.
	SystemSuffix string
	// Thinking replaces SessionOptions.Thinking for this run's model calls.
	// Nil keeps the session's setting.
	Thinking *llm.ThinkingConfiguration
}

// Run executes one agent turn from a single user message.
//
// The user message and the assistant's response (including any tool calls and
// tool results produced this turn) are accumulated for one variadic
// History.Append at end of turn. The returned channel always ends with
// EventEnd and is then closed; the returned error is always nil.
//
// On EndReasonError (including History I/O failure, Lock-acquire timeout or a
// panic inside the run) the History is NOT updated for this turn: the caller
// knows to retry, and the durable transcript stays consistent.
func (s *Session) Run(ctx context.Context, userText string) (<-chan Event, error) {
	return s.RunWithOptions(ctx, userText, nil)
}

// RunWithOptions is Run with per-run options. Nil options behave like Run.
func (s *Session) RunWithOptions(ctx context.Context, userText string, options *RunOptions) (<-chan Event, error) {
	plan := runPlan{userText: userText, persistToHistory: true}
	if options != nil {
		plan.options = *options
	}
	events := make(chan Event, eventBufferSize)
	go s.runLoop(ctx, events, plan)
	return events, nil
}

// RunWithMessages is the lower-level escape hatch for callers that maintain
// their own history shape (trimming, branched conversations, agent-as-subroutine).
//
// The history slice is the conversation so far, excluding the system prompt
// (which Run prepends from SessionOptions.SystemPrompt). RunWithMessages does
// not append to History; the caller owns the message log here and uses Drain
// to assemble the produced messages. The channel follows the same contract as
// Run's; the returned error is always nil.
func (s *Session) RunWithMessages(ctx context.Context, history []llm.Message) (<-chan Event, error) {
	events := make(chan Event, eventBufferSize)
	go s.runLoop(ctx, events, runPlan{callerHistory: history})
	return events, nil
}

// runPlan is what one run starts from. A run either reads and appends the
// session's History (Run) or works on messages the caller owns
// (RunWithMessages).
type runPlan struct {
	userText         string
	callerHistory    []llm.Message
	persistToHistory bool
	options          RunOptions
}

// runState is the conversation of one run as it grows turn by turn.
type runState struct {
	messages        []llm.Message // everything the model sees, system prompt first
	produced        []llm.Message // this run's new messages, flushed to History in one Append
	toolDefinitions []llm.ToolDefinition
	thinking        *llm.ThinkingConfiguration
}

func (state *runState) record(message llm.Message) {
	state.messages = append(state.messages, message)
	state.produced = append(state.produced, message)
}

// runLoop owns the run goroutine: it closes events when the run is over and
// turns a panic into EventError plus EventEnd (CON-001).
func (s *Session) runLoop(userContext context.Context, events chan<- Event, plan runPlan) {
	defer close(events)
	sink := eventSink{events: events}
	defer recoverRun(sink)

	runContext, cancelRun := context.WithTimeout(userContext, s.options.RunTimeout)
	defer cancelRun()

	endReason, err := s.execute(userContext, runContext, sink, plan)
	if err != nil {
		sink.send(Event{Kind: EventError, Err: err})
	}
	sink.send(Event{Kind: EventEnd, EndReason: endReason})
}

func recoverRun(sink eventSink) {
	recovered := recover()
	if recovered == nil {
		return
	}
	sink.send(Event{Kind: EventError, Err: fmt.Errorf("aikido/agent: run panicked: %v\n%s", recovered, debug.Stack())})
	sink.send(Event{Kind: EventEnd, EndReason: EndReasonError})
}

// execute holds the session lock for the whole run and alternates model
// calls and tool calls until the model stops asking for tools or the turn
// limit is reached.
func (s *Session) execute(userContext, runContext context.Context, sink eventSink, plan runPlan) (EndReason, error) {
	unlock, err := s.options.Locker.Lock(runContext, s.options.ID)
	if err != nil {
		return failedRun(userContext, runContext, nil, fmt.Errorf("aikido/agent: locker.Lock: %w", err))
	}
	defer unlock()

	state, err := s.startRun(runContext, plan)
	if err != nil {
		return failedRun(userContext, runContext, nil, err)
	}
	for range s.options.MaximumTurns {
		reply, callContextError, err := s.callModel(runContext, sink, state)
		if err != nil {
			return failedRun(userContext, runContext, callContextError, err)
		}
		state.record(reply)
		if len(reply.ToolCalls) == 0 {
			return s.finishRun(userContext, runContext, state, plan, EndReasonStop)
		}
		s.runToolCalls(runContext, sink, state, reply.ToolCalls)
	}
	return s.finishRun(userContext, runContext, state, plan, EndReasonMaximumTurns)
}

// startRun builds the first model request's conversation: system prompt,
// earlier messages, and for History runs the new user message.
func (s *Session) startRun(ctx context.Context, plan runPlan) (*runState, error) {
	earlier := plan.callerHistory
	if plan.persistToHistory {
		stored, err := s.options.History.Read(ctx, s.options.ID)
		if err != nil {
			return nil, fmt.Errorf("aikido/agent: history.Read: %w", err)
		}
		earlier = stored
	}

	state := &runState{
		messages: make([]llm.Message, 0, len(earlier)+2),
		thinking: s.options.Thinking,
	}
	if plan.options.Thinking != nil {
		state.thinking = plan.options.Thinking
	}
	if system := systemPrompt(s.options.SystemPrompt, plan.options.SystemSuffix); system != "" {
		state.messages = append(state.messages, llm.Message{Role: llm.RoleSystem, Content: system})
	}
	state.messages = append(state.messages, earlier...)
	if plan.persistToHistory {
		state.record(llm.Message{Role: llm.RoleUser, Content: plan.userText})
	}
	if s.options.Tools != nil {
		state.toolDefinitions = s.options.Tools.Definitions()
	}
	return state, nil
}

// systemPrompt joins the session prompt and a run's suffix with a blank line.
func systemPrompt(prompt, suffix string) string {
	if suffix == "" {
		return prompt
	}
	if prompt == "" {
		return suffix
	}
	return prompt + "\n\n" + suffix
}

// callModel streams one model reply under LLMCallTimeout, forwarding text,
// thinking and images to the caller. On failure it also returns the call
// context's error, which tells an LLMCallTimeout apart from other failures.
func (s *Session) callModel(runContext context.Context, sink eventSink, state *runState) (reply llm.Message, callContextError, err error) {
	callContext, cancelCall := context.WithTimeout(runContext, s.options.LLMCallTimeout)
	defer cancelCall()

	providerEvents, err := s.options.Client.Stream(callContext, llm.Request{
		Model:         s.options.Model,
		Messages:      state.messages,
		Tools:         state.toolDefinitions,
		MaximumTokens: s.options.MaximumTokens,
		Temperature:   s.options.Temperature,
		Thinking:      state.thinking,
	})
	if err != nil {
		return llm.Message{}, callContext.Err(), fmt.Errorf("aikido/agent: client.Stream: %w", err)
	}
	builder, err := receiveReply(callContext, providerEvents, sink)
	if err != nil {
		return llm.Message{}, callContext.Err(), err
	}
	if builder.usage != nil {
		sink.send(Event{Kind: EventUsage, Usage: builder.usage})
	}
	return builder.message(), nil, nil
}

// receiveReply consumes the provider's stream until it closes or ctx ends.
// The first EventError of the stream becomes the returned error.
func receiveReply(ctx context.Context, providerEvents <-chan llm.Event, sink eventSink) (*replyBuilder, error) {
	builder := &replyBuilder{}
	for {
		select {
		case <-ctx.Done():
			return builder, ctx.Err()
		case event, open := <-providerEvents:
			if !open {
				return builder, builder.streamError
			}
			builder.add(event, sink)
		}
	}
}

// replyBuilder accumulates one streamed model reply.
type replyBuilder struct {
	text        strings.Builder
	toolCalls   []llm.ToolCall
	images      []llm.ImagePart
	usage       *llm.Usage
	streamError error
}

func (b *replyBuilder) add(event llm.Event, sink eventSink) {
	switch event.Kind {
	case llm.EventTextDelta:
		b.text.WriteString(event.Text)
		sink.send(Event{Kind: EventText, Text: event.Text})
	case llm.EventThinking:
		sink.send(Event{Kind: EventThinking, Text: event.Text})
	case llm.EventToolCall:
		if event.Tool != nil {
			b.toolCalls = append(b.toolCalls, *event.Tool)
		}
	case llm.EventImage:
		if event.Image != nil {
			b.images = append(b.images, *event.Image)
			image := *event.Image
			sink.send(Event{Kind: EventImage, Image: &image})
		}
	case llm.EventUsage:
		b.usage = event.Usage
	case llm.EventError:
		if b.streamError == nil {
			b.streamError = event.Err
		}
	case llm.EventEnd:
		// The provider closes the channel right after EventEnd; receiveReply ends there.
	}
}

func (b *replyBuilder) message() llm.Message {
	return llm.Message{
		Role:      llm.RoleAssistant,
		Content:   b.text.String(),
		Images:    b.images,
		ToolCalls: b.toolCalls,
	}
}

// runToolCalls dispatches the calls of one model reply in order, reports each
// call and result to the caller and adds the results to the conversation.
// A failing tool does not end the run: the model sees the error and can
// self-correct on the next turn.
func (s *Session) runToolCalls(ctx context.Context, sink eventSink, state *runState, calls []llm.ToolCall) {
	environment := tools.Environment{SessionID: s.options.ID, TurnID: uuid.New()}
	for _, call := range calls {
		result, err := s.dispatch(ctx, call, environment)
		toolResult := buildToolResult(call, result, err)
		sink.send(Event{Kind: EventToolCall, ToolCall: &call})
		sink.send(Event{Kind: EventToolResult, ToolResult: &toolResult})
		state.record(llm.Message{
			Role:       llm.RoleTool,
			ToolCallID: call.ID,
			Content:    toolResultJSON(toolResult),
		})
	}
}

// dispatch routes a tool call. Without a registry every tool name is unknown;
// the model self-corrects on the next turn.
func (s *Session) dispatch(ctx context.Context, call llm.ToolCall, environment tools.Environment) (tools.Result, error) {
	if s.options.Tools == nil {
		return tools.Result{}, fmt.Errorf("%w: %s", tools.ErrUnknownTool, call.Name)
	}
	return s.options.Tools.Dispatch(ctx, call, environment)
}

// finishRun flushes the run's messages to History (History runs only, also
// when the turn limit was hit) and ends the run with endReason.
func (s *Session) finishRun(userContext, runContext context.Context, state *runState, plan runPlan, endReason EndReason) (EndReason, error) {
	if !plan.persistToHistory {
		return endReason, nil
	}
	if err := s.options.History.Append(runContext, s.options.ID, state.produced...); err != nil {
		return failedRun(userContext, runContext, nil, fmt.Errorf("aikido/agent: history.Append: %w", err))
	}
	return endReason, nil
}

// failedRun pairs a run's error with the EndReason the contexts explain.
func failedRun(userContext, runContext context.Context, callContextError, err error) (EndReason, error) {
	return resolveEndReason(userContext, runContext, callContextError), err
}

// resolveEndReason maps the context errors of a failed run to its EndReason.
//
// Order matters: caller cancellation always wins, then RunTimeout, then
// LLMCallTimeout, then everything else is EndReasonError.
func resolveEndReason(userContext, runContext context.Context, callContextError error) EndReason {
	if userContext != nil {
		switch {
		case errors.Is(userContext.Err(), context.Canceled):
			return EndReasonCancelled
		case errors.Is(userContext.Err(), context.DeadlineExceeded):
			return EndReasonTimeout
		}
	}
	if runContext != nil && errors.Is(runContext.Err(), context.DeadlineExceeded) {
		return EndReasonTimeout
	}
	if errors.Is(callContextError, context.DeadlineExceeded) {
		return EndReasonTimeout
	}
	if errors.Is(callContextError, context.Canceled) {
		return EndReasonCancelled
	}
	return EndReasonError
}

func buildToolResult(call llm.ToolCall, result tools.Result, err error) ToolResult {
	toolResult := ToolResult{
		CallID: call.ID,
		Name:   call.Name,
		OK:     err == nil,
	}
	if err != nil {
		toolResult.Error = err.Error()
	} else {
		toolResult.Content = result.Content
	}
	return toolResult
}

// toolResultJSON serializes a tool result for the model's tool message.
func toolResultJSON(toolResult ToolResult) string {
	if !toolResult.OK {
		return errorJSON(toolResult.Error)
	}
	if toolResult.Content == nil {
		return "null"
	}
	content, err := json.Marshal(toolResult.Content)
	if err != nil {
		return errorJSON("content marshal failed: " + err.Error())
	}
	return string(content)
}

// errorJSON renders {"error": message}.
func errorJSON(message string) string {
	// Marshalling a struct with one string field cannot fail.
	encoded, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	return string(encoded)
}

// eventSink delivers a run's events to the caller's channel.
type eventSink struct {
	events chan<- Event
}

func (sink eventSink) send(event Event) {
	sink.events <- event
}
