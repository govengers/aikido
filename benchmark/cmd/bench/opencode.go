package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

var goEnvOnce = sync.OnceValue(func() []string {
	out, err := exec.Command("go", "env", "GOPATH", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		return nil
	}
	v := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(v) != 3 {
		return nil
	}
	return []string{"GOPATH=" + v[0], "GOCACHE=" + v[1], "GOMODCACHE=" + v[2]}
})

// runOpencode runs `opencode run` non-interactively in the work dir with an
// isolated HOME and XDG tree under j.Root, so its session database, config
// and skills are per run, and parses its JSON event stream.
func runOpencode(ctx context.Context, j *job, m *metrics) error {
	home := filepath.Join(j.Root, "home")
	cfgDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "opencode.json"), []byte(opencodeConfig), 0o644); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, j.Opencode, "run", "--standalone", "--auto", "--format", "json",
		"-m", "openrouter/"+j.Model, j.Task.Prompt)
	cmd.Dir = j.WorkDir
	var env []string
	for _, kv := range childEnv(j.WorkDir) {
		name := strings.SplitN(kv, "=", 2)[0]
		if name == "HOME" || strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "OPENCODE") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, goEnvOnce()...)
	cmd.Env = append(cmd.Env,
		"OPENROUTER_API_KEY="+j.APIKey,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"OPENCODE_DISABLE_CLAUDE_CODE=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1",
	)
	killGroup(cmd)

	stderr, err := os.Create(filepath.Join(j.LogDir, "stderr.log"))
	if err != nil {
		return err
	}
	defer stderr.Close()
	transcript, err := os.Create(filepath.Join(j.LogDir, "transcript.jsonl"))
	if err != nil {
		return err
	}
	defer transcript.Close()
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	eventErr := parseOpencodeEvents(io.TeeReader(stdout, transcript), m)
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		m.EndReason = "timeout"
		return fmt.Errorf("timeout after %s", j.Timeout)
	case waitErr != nil:
		m.EndReason = "error"
		return fmt.Errorf("opencode: %w: %s", waitErr, tail(readFile(stderr.Name()), 300))
	case eventErr != nil:
		m.EndReason = "error"
		return eventErr
	}
	m.EndReason = "stop"
	return nil
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
func parseOpencodeEvents(r io.Reader, m *metrics) error {
	var firstErr error
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var ev opencodeEvent
			if json.Unmarshal(line, &ev) == nil {
				switch ev.Type {
				case "tool_use":
					m.tool(ev.Part.Tool, ev.Part.State.Status == "error")
				case "step_finish":
					t := ev.Part.Tokens
					m.LLMCalls++
					m.InputTokens += t.Input + t.Cache.Read + t.Cache.Write
					m.OutputTokens += t.Output + t.Reasoning
					m.CacheReadTokens += t.Cache.Read
					m.CacheWriteTokens += t.Cache.Write
					m.CostUSD += ev.Part.Cost
				case "error":
					if firstErr == nil {
						b, _ := json.Marshal(ev.Error)
						firstErr = errors.New("opencode error event: " + tail(string(b), 300))
					}
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return firstErr
			}
			return err
		}
	}
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}
