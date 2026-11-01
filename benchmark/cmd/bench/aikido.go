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
	"github.com/govengers/aikido/vfs/osfs"
)

// systemPrompt is the only instruction aikido gets besides task.md. opencode
// brings its own (much longer) system prompt; see README "Fairness".
const systemPrompt = `You are a coding agent working in a software project. The project files are your workspace and every path is relative to the project root. Use the tools to inspect and change the files until the user's task is complete. Make the changes in the files themselves instead of describing them, and check your work where you can. When you are done, reply with a short summary of what you changed.`

const aikidoMaximumTurns = 60

// runAikido drives the library in-process: openrouter client, osfs storage on
// the work dir, all VFS tools, and optionally the prototype bash tool.
func runAikido(withBash bool) harnessFunc {
	return func(ctx context.Context, j *job, m *metrics) error {
		client, err := openrouter.NewClient(&openrouter.Options{APIKey: j.APIKey, XTitle: "aikido-bench"})
		if err != nil {
			return err
		}
		storage, err := osfs.New(&osfs.Options{Directory: j.WorkDir})
		if err != nil {
			return err
		}
		defer storage.Close()
		registry := tools.NewRegistry()
		if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage}); err != nil {
			return err
		}
		if withBash {
			if err := registry.Register(bashToolDefinition(), bashHandler(j.WorkDir)); err != nil {
				return err
			}
		}
		session, err := agent.NewLocalSession(&agent.SessionOptions{
			ID:           j.id(),
			Client:       client,
			Tools:        registry,
			Model:        j.Model,
			SystemPrompt: systemPrompt,
			MaximumTurns: aikidoMaximumTurns,
			RunTimeout:   j.Timeout,
		})
		if err != nil {
			return err
		}
		transcript, err := os.Create(filepath.Join(j.LogDir, "transcript.jsonl"))
		if err != nil {
			return err
		}
		defer transcript.Close()
		enc := json.NewEncoder(transcript)

		events, err := session.Run(ctx, j.Task.Prompt)
		if err != nil {
			return err
		}
		var (
			runErr error
			text   strings.Builder
		)
		flush := func() {
			if text.Len() > 0 {
				_ = enc.Encode(map[string]any{"kind": "text", "text": text.String()})
				text.Reset()
			}
		}
		for ev := range events {
			if ev.Kind == agent.EventText {
				text.WriteString(ev.Text)
				continue
			}
			flush()
			switch ev.Kind {
			case agent.EventUsage:
				u := ev.Usage
				m.LLMCalls++
				m.InputTokens += u.PromptTokens
				m.OutputTokens += u.CompletionTokens
				m.CacheReadTokens += u.CacheReadTokens
				m.CacheWriteTokens += u.CacheWriteTokens
				m.CostUSD += u.CostUSD
				_ = enc.Encode(map[string]any{"kind": "usage", "usage": u})
			case agent.EventToolCall:
				_ = enc.Encode(map[string]any{"kind": "tool_call", "name": ev.ToolCall.Name, "arguments": tail(ev.ToolCall.Arguments, 4000)})
			case agent.EventToolResult:
				tr := ev.ToolResult
				m.tool(tr.Name, !tr.OK)
				content, _ := json.Marshal(tr.Content)
				_ = enc.Encode(map[string]any{"kind": "tool_result", "name": tr.Name, "ok": tr.OK, "error": tr.Error, "content": tail(string(content), 2000)})
			case agent.EventError:
				runErr = ev.Err
				_ = enc.Encode(map[string]any{"kind": "error", "error": ev.Err.Error()})
			case agent.EventEnd:
				m.EndReason = string(ev.EndReason)
				_ = enc.Encode(map[string]any{"kind": "end", "reason": ev.EndReason})
			}
		}
		flush()
		if runErr != nil {
			return runErr
		}
		if m.EndReason != string(agent.EndReasonStop) {
			return fmt.Errorf("run ended with %s", m.EndReason)
		}
		if ctx.Err() != nil {
			return errors.New("timeout")
		}
		return nil
	}
}
