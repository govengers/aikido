package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm/openrouter"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/tools/shell"
	"github.com/govengers/aikido/vfs/osfs"
)

// systemPrompt is the only instruction aikido gets besides task.md. opencode
// brings its own (much longer) system prompt; see README "Fairness".
const systemPrompt = `You are a coding agent working in a software project. The project files are your workspace and every path is relative to the project root. Use the tools to inspect and change the files until the user's task is complete. Make the changes in the files themselves instead of describing them, and check your work where you can. When you are done, reply with a short summary of what you changed.`

const aikidoMaximumTurns = 60

// runAikido drives the library in-process: openrouter client, osfs storage on
// the work directory, all VFS tools, todo_write, and optionally the prototype
// bash tool. Library defaults stay on (prompt caching, tool result cap, tool
// timeout).
func runAikido(shellMode aikidoShell) harnessRunner {
	return func(ctx context.Context, run *job, observed *metrics) error {
		return runAikidoJob(ctx, run, observed, shellMode)
	}
}

// aikidoShell selects the shell an aikido harness gets next to the file tools.
type aikidoShell int

const (
	shellNone    aikidoShell = iota // file tools only
	shellHost                       // the prototype bash tool on the host (bash.go)
	shellVirtual                    // tools/shell over the work directory's Storage
)

func runAikidoJob(ctx context.Context, run *job, observed *metrics, shellMode aikidoShell) error {
	client, err := openrouter.NewClient(&openrouter.Options{APIKey: run.APIKey, XTitle: "aikido-bench"})
	if err != nil {
		return err
	}
	storage, err := osfs.New(&osfs.Options{Directory: run.WorkDirectory})
	if err != nil {
		return err
	}
	defer storage.Close()
	registry, err := aikidoTools(storage, run.WorkDirectory, shellMode)
	if err != nil {
		return err
	}
	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID:           run.id(),
		Client:       client,
		Tools:        registry,
		Model:        run.Model,
		SystemPrompt: systemPrompt,
		MaximumTurns: aikidoMaximumTurns,
		RunTimeout:   run.Timeout,
	})
	if err != nil {
		return err
	}
	transcript, err := os.Create(filepath.Join(run.LogDirectory, "transcript.jsonl"))
	if err != nil {
		return err
	}
	defer transcript.Close()

	events, err := session.Run(ctx, run.Task.Prompt)
	if err != nil {
		return err
	}
	recorder := &aikidoRecorder{encoder: json.NewEncoder(transcript), observed: observed}
	for event := range events {
		recorder.record(event)
	}
	recorder.flushText()
	if recorder.runError != nil {
		return recorder.runError
	}
	if observed.EndReason != string(agent.EndReasonStop) {
		return fmt.Errorf("run ended with %s", observed.EndReason)
	}
	if ctx.Err() != nil {
		return errors.New("timeout")
	}
	return nil
}

func aikidoTools(storage *osfs.Storage, workDirectory string, shellMode aikidoShell) (*tools.Registry, error) {
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage}); err != nil {
		return nil, err
	}
	if err := agent.RegisterPlanTool(registry); err != nil {
		return nil, err
	}
	switch shellMode {
	case shellHost:
		if err := registry.Register(bashToolDefinition(), bashHandler(workDirectory)); err != nil {
			return nil, err
		}
	case shellVirtual:
		if err := shell.Register(registry, &shell.Options{Storage: storage}); err != nil {
			return nil, err
		}
	case shellNone:
	}
	return registry, nil
}

// aikidoRecorder writes a run's events to transcript.jsonl, merging streamed
// text deltas into one line, and adds usage and tool calls to observed.
type aikidoRecorder struct {
	encoder  *json.Encoder
	text     strings.Builder
	observed *metrics
	runError error
}

func (r *aikidoRecorder) record(event agent.Event) {
	if event.Kind != agent.EventText {
		r.flushText()
	}
	switch event.Kind {
	case agent.EventText:
		r.text.WriteString(event.Text)
	case agent.EventUsage:
		usage := event.Usage
		r.observed.LLMCalls++
		r.observed.InputTokens += usage.PromptTokens
		r.observed.OutputTokens += usage.CompletionTokens
		r.observed.CacheReadTokens += usage.CacheReadTokens
		r.observed.CacheWriteTokens += usage.CacheWriteTokens
		r.observed.CostUSD += usage.CostUSD
		_ = r.encoder.Encode(map[string]any{"kind": "usage", "usage": usage})
	case agent.EventToolCall:
		_ = r.encoder.Encode(map[string]any{"kind": "tool_call", "name": event.ToolCall.Name, "arguments": tail(event.ToolCall.Arguments, 4000)})
	case agent.EventToolResult:
		toolResult := event.ToolResult
		r.observed.tool(toolResult.Name, !toolResult.OK)
		content, _ := json.Marshal(toolResult.Content)
		_ = r.encoder.Encode(map[string]any{"kind": "tool_result", "name": toolResult.Name, "ok": toolResult.OK, "error": toolResult.Error, "content": tail(string(content), 2000)})
	case agent.EventError:
		r.runError = event.Err
		_ = r.encoder.Encode(map[string]any{"kind": "error", "error": event.Err.Error()})
	case agent.EventEnd:
		r.observed.EndReason = string(event.EndReason)
		_ = r.encoder.Encode(map[string]any{"kind": "end", "reason": event.EndReason})
	case agent.EventThinking, agent.EventImage:
		// Not part of the transcript.
	}
}

func (r *aikidoRecorder) flushText() {
	if r.text.Len() > 0 {
		_ = r.encoder.Encode(map[string]any{"kind": "text", "text": r.text.String()})
		r.text.Reset()
	}
}
