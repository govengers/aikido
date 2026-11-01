package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Claude Code isolation (worker suite): claude keeps the user's real HOME,
// because on macOS its login is found only there (an empty per-run HOME
// answers "Not logged in", checked 01.11.2026 with 2.1.316). The user layer is
// switched off by flags instead: --setting-sources project (no user settings,
// hooks, plugins or user CLAUDE.md; the worker repo's CLAUDE.md still loads),
// --disable-slash-commands (no skills), --strict-mcp-config with an empty
// --mcp-config (no configured or claude.ai MCP servers),
// --no-session-persistence and CLAUDE_CODE_DISABLE_AUTO_MEMORY (nothing is
// written under ~/.claude).
var claudeIsolation = []string{
	// --mcp-config is variadic: an option must follow it, or it eats the prompt.
	"--mcp-config", `{"mcpServers":{}}`, "--strict-mcp-config",
	"--setting-sources", "project", "--disable-slash-commands", "--no-session-persistence",
}

func prepareClaudeCode(_ context.Context, _ string, template *job) error {
	binary, err := exec.LookPath(template.ClaudeBinary)
	template.ClaudeBinary = binary
	return err
}

// claudeModel maps an OpenRouter Anthropic id to Claude Code's:
// anthropic/claude-haiku-5.5 -> claude-haiku-5-5.
func claudeModel(model string) (string, error) {
	name, ok := strings.CutPrefix(model, "anthropic/")
	if !ok {
		return "", fmt.Errorf("claude-code runs Anthropic models only, not %s", model)
	}
	return strings.ReplaceAll(name, ".", "-"), nil
}

// runClaudeCode runs `claude -p` in the worker clone and reads its
// stream-json events: tool calls per tool_use block, LLM calls per distinct
// assistant message, tokens and cost from the final result event.
func runClaudeCode(ctx context.Context, run *job, observed *metrics) error {
	model, err := claudeModel(run.Model)
	if err != nil {
		return err
	}
	arguments := append([]string{"-p"}, claudeIsolation...)
	arguments = append(arguments, "--model", model, "--output-format", "stream-json", "--verbose",
		"--permission-mode", "bypassPermissions", run.Task.Prompt)
	//nolint:gosec // G204: the operator names the claude binary (-claude flag); the prompt is the task's task.md
	command := exec.CommandContext(ctx, run.ClaudeBinary, arguments...)
	command.Dir = run.WorkDirectory
	command.Env = claudeEnvironment(run)
	killGroup(command)
	return runStreaming(ctx, run, command, streamParser{transcript: "transcript.jsonl", parse: func(stream io.Reader) error {
		return parseClaudeEvents(stream, observed)
	}}, observed)
}

// claudeEnvironment is childEnv plus the worker environment, without the
// parent Claude Code session's markers (a nested claude must not think it is
// a child session of the one running the benchmark) and with auto memory off.
func claudeEnvironment(run *job) []string {
	var environment []string
	for _, variable := range childEnv(run.WorkDirectory) {
		name, _, _ := strings.Cut(variable, "=")
		session := name == "CLAUDECODE" || name == "CLAUDE_PID" || name == "CLAUDE_EFFORT" || name == "CLAUDE_CONFIG_DIR" ||
			name == "CLAUDE_PROJECT_DIR" || name == "CLAUDE_PLUGIN_DATA" ||
			strings.HasPrefix(name, "CLAUDE_CODE_") && name != "CLAUDE_CODE_OAUTH_TOKEN"
		if !session {
			environment = append(environment, variable)
		}
	}
	return append(append(environment, run.Env...), "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type claudeEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		ID      string `json:"id"`
		Content []struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			ToolUseID string `json:"tool_use_id"`
			IsError   bool   `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
	IsError      bool        `json:"is_error"`
	Result       string      `json:"result"`
	TotalCostUSD float64     `json:"total_cost_usd"`
	Usage        claudeUsage `json:"usage"`
}

// parseClaudeEvents reads the stream-json events. Tokens are normalized to
// OpenRouter's shape (input includes cache reads and writes).
func parseClaudeEvents(stream io.Reader, observed *metrics) error {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 1<<20), 1<<26)
	tally := claudeTally{messages: map[string]bool{}, toolNames: map[string]string{}, observed: observed}
	var resultError error
	for scanner.Scan() {
		var event claudeEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "result" {
			resultError = recordClaudeResult(&event, observed)
			continue
		}
		tally.record(&event)
	}
	observed.LLMCalls = len(tally.messages)
	if err := scanner.Err(); err != nil {
		return err
	}
	if observed.EndReason == "" {
		return errors.New("claude: no result event")
	}
	return resultError
}

// claudeTally counts distinct assistant messages (one per LLM call, streamed
// as several events) and pairs tool results with their tool_use names.
type claudeTally struct {
	messages  map[string]bool
	toolNames map[string]string
	observed  *metrics
}

func (tally *claudeTally) record(event *claudeEvent) {
	for _, block := range event.Message.Content {
		switch {
		case event.Type == "assistant" && block.Type == "tool_use":
			tally.toolNames[block.ID] = block.Name
		case event.Type == "user" && block.Type == "tool_result":
			tally.observed.tool(tally.toolNames[block.ToolUseID], block.IsError)
		}
	}
	if event.Type == "assistant" {
		tally.messages[event.Message.ID] = true
	}
}

func recordClaudeResult(event *claudeEvent, observed *metrics) error {
	usage := event.Usage
	observed.InputTokens = usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	observed.OutputTokens = usage.OutputTokens
	observed.CacheReadTokens = usage.CacheReadInputTokens
	observed.CacheWriteTokens = usage.CacheCreationInputTokens
	observed.CostUSD = event.TotalCostUSD
	observed.EndReason = "stop"
	if event.Subtype != "success" || event.IsError {
		observed.EndReason = event.Subtype
		return fmt.Errorf("claude result %s: %s", event.Subtype, tail(event.Result, 300))
	}
	return nil
}
