package agent

import (
	"strings"

	"github.com/govengers/aikido/llm"
)

// Drain consumes events from the channel until it closes and returns the
// assembled assistant message followed by tool-result messages, in the
// order they were produced this turn. Returns the first error encountered
// on EventError.
//
// Drain is the recommended way for RunWithMessages callers to obtain the
// turn's output messages for appending to their own history store. Callers
// who also need streaming (e.g., to a UI) fan out the channel themselves
// before passing one branch to Drain.
func Drain(events <-chan Event) ([]llm.Message, error) {
	var transcript drainTranscript
	for event := range events {
		transcript.add(event)
	}
	return transcript.messages, transcript.firstError
}

// drainTranscript rebuilds the message log from a run's events. The pending
// assistant message and its tool results are flushed when the next model
// reply starts or the run ends.
type drainTranscript struct {
	messages    []llm.Message
	text        strings.Builder
	toolCalls   []llm.ToolCall
	images      []llm.ImagePart
	toolResults []llm.Message
	firstError  error
}

func (d *drainTranscript) add(event Event) {
	switch event.Kind {
	case EventText:
		d.startReplyIfToolsAnswered()
		d.text.WriteString(event.Text)
	case EventToolCall:
		if event.ToolCall != nil {
			d.toolCalls = append(d.toolCalls, *event.ToolCall)
		}
	case EventImage:
		if event.Image != nil {
			d.startReplyIfToolsAnswered()
			d.images = append(d.images, *event.Image)
		}
	case EventToolResult:
		if event.ToolResult != nil {
			d.toolResults = append(d.toolResults, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: event.ToolResult.CallID,
				Content:    toolResultJSON(*event.ToolResult),
			})
		}
	case EventError:
		if d.firstError == nil {
			d.firstError = event.Err
		}
	case EventEnd:
		d.flush()
	case EventThinking, EventUsage:
		// Not part of the persisted message log.
	}
}

// startReplyIfToolsAnswered flushes the previous reply once its tool results
// are in: new output then belongs to the next model reply.
func (d *drainTranscript) startReplyIfToolsAnswered() {
	if len(d.toolResults) > 0 {
		d.flush()
	}
}

func (d *drainTranscript) flush() {
	if d.text.Len() > 0 || len(d.toolCalls) > 0 || len(d.images) > 0 {
		d.messages = append(d.messages, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   d.text.String(),
			Images:    append([]llm.ImagePart(nil), d.images...),
			ToolCalls: append([]llm.ToolCall(nil), d.toolCalls...),
		})
	}
	d.messages = append(d.messages, d.toolResults...)
	d.text.Reset()
	d.toolCalls = nil
	d.images = nil
	d.toolResults = nil
}
