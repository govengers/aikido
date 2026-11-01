package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// opencodeConfig is the per-run global config: nothing of the user's
// ~/.config/opencode (plugins, AGENTS.md, providers) leaks into a run.
const opencodeConfig = `{
  "$schema": "https://opencode.ai/config.json",
  "autoupdate": false,
  "share": "disabled"
}
`

var goEnvironmentOnce = sync.OnceValue(func() []string {
	output, err := exec.Command("go", "env", "GOPATH", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		return nil
	}
	values := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(values) != 3 {
		return nil
	}
	return []string{"GOPATH=" + values[0], "GOCACHE=" + values[1], "GOMODCACHE=" + values[2]}
})

// runOpencode runs `opencode run` non-interactively in the work directory with
// an isolated HOME and XDG tree under run.Root, so its session database,
// config and skills are per run, and parses its JSON event stream.
func runOpencode(ctx context.Context, run *job, observed *metrics) error {
	home := filepath.Join(run.Root, "home")
	configDirectory := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(configDirectory, directoryPermissions); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "opencode.json"), []byte(opencodeConfig), filePermissions); err != nil {
		return err
	}

	//nolint:gosec // G204: the operator names the opencode binary (-opencode flag); the prompt is the task's task.md
	command := exec.CommandContext(ctx, run.OpencodeBinary, "run", "--standalone", "--auto", "--format", "json",
		"-m", "openrouter/"+run.Model, run.Task.Prompt)
	command.Dir = run.WorkDirectory
	command.Env = opencodeEnvironment(run, home)
	killGroup(command)
	return runStreaming(ctx, run, command, streamParser{transcript: "transcript.jsonl", parse: func(stream io.Reader) error { return parseOpencodeEvents(stream, observed) }}, observed)
}

// opencodeEnvironment is childEnv without the user's HOME, XDG and OPENCODE
// variables, plus the Go caches, the API key, the run's own home tree and the
// worker suite's environment. Coding runs disable opencode's Claude Code
// compatibility; worker runs keep it, because the worker repo's instructions
// are its CLAUDE.md (the isolated HOME has no ~/.claude to leak in).
func opencodeEnvironment(run *job, home string) []string {
	var environment []string
	for _, variable := range childEnv(run.WorkDirectory) {
		name, _, _ := strings.Cut(variable, "=")
		if name == "HOME" || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "OPENCODE") {
			continue
		}
		environment = append(environment, variable)
	}
	environment = append(environment, goEnvironmentOnce()...)
	environment = append(environment, run.Env...)
	if !run.Task.Worker {
		environment = append(environment, "OPENCODE_DISABLE_CLAUDE_CODE=1")
	}
	return append(environment,
		"OPENROUTER_API_KEY="+run.APIKey,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"OPENCODE_DISABLE_AUTOUPDATE=1",
	)
}

type opencodeEvent struct {
	Type  string `json:"type"`
	Error any    `json:"error"`
	Part  struct {
		Tool  string `json:"tool"`
		State struct {
			Status string `json:"status"`
		} `json:"state"`
		Cost   float64 `json:"cost"`
		Tokens struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
			Cache     struct {
				Read  int `json:"read"`
				Write int `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	} `json:"part"`
}

// parseOpencodeEvents reads the `--format json` stream. Tokens are normalized
// to OpenRouter's shape: input includes cache reads and writes, output
// includes reasoning. It returns the first error event.
func parseOpencodeEvents(stream io.Reader, observed *metrics) error {
	var firstError error
	reader := bufio.NewReader(stream)
	for {
		line, err := reader.ReadBytes('\n')
		var event opencodeEvent
		if len(strings.TrimSpace(string(line))) > 0 && json.Unmarshal(line, &event) == nil {
			eventError := recordOpencodeEvent(&event, observed)
			if firstError == nil {
				firstError = eventError
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return firstError
			}
			return err
		}
	}
}

// recordOpencodeEvent adds a tool_use or step_finish event to observed and
// turns an error event into an error.
func recordOpencodeEvent(event *opencodeEvent, observed *metrics) error {
	switch event.Type {
	case "tool_use":
		observed.tool(event.Part.Tool, event.Part.State.Status == "error")
	case "step_finish":
		tokens := event.Part.Tokens
		observed.LLMCalls++
		observed.InputTokens += tokens.Input + tokens.Cache.Read + tokens.Cache.Write
		observed.OutputTokens += tokens.Output + tokens.Reasoning
		observed.CacheReadTokens += tokens.Cache.Read
		observed.CacheWriteTokens += tokens.Cache.Write
		observed.CostUSD += event.Part.Cost
	case "error":
		encoded, _ := json.Marshal(event.Error)
		return errors.New("opencode error event: " + tail(string(encoded), 300))
	}
	return nil
}
