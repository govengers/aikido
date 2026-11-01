// Package fakestate is the canned cluster state the worker suite's fake
// kubectl and logcli answer from, and the invocation log both write. The state
// file ($FAKE_CLUSTER_STATE) and the log ($FAKE_LOG) live outside the agent's
// workspace; check.sh reads the log to prove what the agent ran.
package fakestate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// State is one task's cluster as the fake tools see it.
type State struct {
	Now        time.Time                    `json:"now"`        // the cluster's clock: ages and --since are relative to it
	Context    string                       `json:"context"`    // kubeconfig context name
	Cluster    map[string][]Item            `json:"cluster"`    // cluster-scoped kinds (nodes)
	Namespaces map[string]map[string][]Item `json:"namespaces"` // namespace -> kind (plural) -> items
	Loki       Loki                         `json:"loki"`
}

// Item is one object: its table row, its manifest and canned texts.
type Item struct {
	Row      map[string]string `json:"row"`      // column header -> cell, normal and wide columns
	Object   map[string]any    `json:"object"`   // the manifest for -o json|yaml and -l
	Describe string            `json:"describe"` // kubectl describe output
	Logs     map[string]string `json:"logs"`     // container -> current log
	Previous map[string]string `json:"previous"` // container -> log of the previous (crashed) container
	History  string            `json:"history"`  // kubectl rollout history output
}

// Loki holds the log streams logcli serves besides the pod logs.
type Loki struct {
	URL     string   `json:"url"`
	Streams []Stream `json:"streams"`
}

// Stream is one label set with its entries.
type Stream struct {
	Labels  map[string]string `json:"labels"`
	Entries []Entry           `json:"entries"`
}

// Entry is one log line.
type Entry struct {
	Time time.Time `json:"ts"`
	Line string    `json:"line"`
}

// Load reads the state named by $FAKE_CLUSTER_STATE.
func Load() (*State, error) {
	path := os.Getenv("FAKE_CLUSTER_STATE")
	if path == "" {
		return nil, errors.New("FAKE_CLUSTER_STATE is not set")
	}
	file, err := os.Open(path) //nolint:gosec // G304: the benchmark runner names the state file
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &state, nil
}

// Record appends one invocation to $FAKE_LOG as a JSON line. A missing log
// variable records nothing; a failing write is reported on stderr only, so the
// tool's answer stays what the agent sees.
func Record(tool string, arguments []string, mutation bool, exitCode int) {
	path := os.Getenv("FAKE_LOG")
	if path == "" {
		return
	}
	line, _ := json.Marshal(map[string]any{"tool": tool, "args": arguments, "mutation": mutation, "exit": exitCode})
	//nolint:gosec // G302,G304: the runner's own log file outside the workspace
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake log:", err)
		return
	}
	defer file.Close()
	if _, err := file.Write(append(line, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, "fake log:", err)
	}
}
