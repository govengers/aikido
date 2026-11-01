package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/govengers/aikido/benchmark/internal/fakestate"
)

// streams is every Loki stream: the state's own plus one per pod container,
// built from its previous and current log lines that start with an RFC 3339
// timestamp.
func streams(state *fakestate.State) []fakestate.Stream {
	all := append([]fakestate.Stream{}, state.Loki.Streams...)
	for namespace, resources := range state.Namespaces {
		for _, pod := range resources["pods"] {
			metadata, _ := pod.Object["metadata"].(map[string]any)
			labels, _ := metadata["labels"].(map[string]any)
			for container, current := range pod.Logs {
				stream := fakestate.Stream{Labels: map[string]string{
					"namespace": namespace, "pod": fmt.Sprint(metadata["name"]), "container": container, "cluster": state.Context,
				}}
				if app, ok := labels["app"].(string); ok {
					stream.Labels["app"] = app
				}
				stream.Entries = append(timestamped(pod.Previous[container]), timestamped(current)...)
				all = append(all, stream)
			}
		}
	}
	return all
}

func timestamped(text string) []fakestate.Entry {
	var entries []fakestate.Entry
	for _, line := range strings.Split(text, "\n") {
		stamp, _, _ := strings.Cut(line, " ")
		if when, err := time.Parse(time.RFC3339, stamp); err == nil {
			entries = append(entries, fakestate.Entry{Time: when, Line: line})
		}
	}
	return entries
}

// result is one matching line with its stream's labels.
type result struct {
	entry  fakestate.Entry
	labels map[string]string
}

func runQuery(parsed *query, state *fakestate.State, stdout, stderr io.Writer) error {
	if len(parsed.positional) < 2 {
		return failure("logcli: error: required argument 'query' not provided")
	}
	selector, filters, err := parseLogQL(parsed.positional[1])
	if err != nil {
		return err
	}
	end := state.Now
	if !parsed.to.IsZero() {
		end = parsed.to
	}
	start := end.Add(-parsed.since)
	if !parsed.from.IsZero() {
		start = parsed.from
	}
	var results []result
	for _, stream := range streams(state) {
		if !selector.matches(stream.Labels) {
			continue
		}
		for _, entry := range stream.Entries {
			if !entry.Time.Before(start) && !entry.Time.After(end) && filters.keep(entry.Line) {
				results = append(results, result{entry: entry, labels: stream.Labels})
			}
		}
	}
	sort.SliceStable(results, func(left, right int) bool {
		if parsed.forward {
			return results[left].entry.Time.Before(results[right].entry.Time)
		}
		return results[left].entry.Time.After(results[right].entry.Time)
	})
	if parsed.limit > 0 && len(results) > parsed.limit {
		results = results[:parsed.limit]
	}
	common := commonLabels(results)
	if !parsed.quiet {
		direction := map[bool]string{false: "BACKWARD", true: "FORWARD"}[parsed.forward]
		clock := state.Now.Format("2006/01/02 15:04:05")
		fmt.Fprintf(stderr, "%s %s/loki/api/v1/query_range?direction=%s&end=%d&limit=%d&query=%s&start=%d\n",
			clock, lokiURL(state), direction, end.UnixNano(), parsed.limit, url.QueryEscape(parsed.positional[1]), start.UnixNano())
		fmt.Fprintf(stderr, "%s Common labels: %s\n", clock, renderLabels(common, nil))
	}
	for _, found := range results {
		writeResult(stdout, parsed.output, found, common)
	}
	return nil
}

func writeResult(stdout io.Writer, output string, found result, common map[string]string) {
	stamp := found.entry.Time.UTC().Format(time.RFC3339)
	switch output {
	case "raw":
		fmt.Fprintln(stdout, found.entry.Line)
	case "jsonl":
		encoded, _ := json.Marshal(map[string]any{"labels": found.labels, "line": found.entry.Line, "timestamp": stamp})
		fmt.Fprintln(stdout, string(encoded))
	default:
		fmt.Fprintf(stdout, "%s %s %s\n", stamp, renderLabels(found.labels, common), found.entry.Line)
	}
}

func lokiURL(state *fakestate.State) string {
	if state.Loki.URL != "" {
		return state.Loki.URL
	}
	return "http://loki.acme.example"
}

func commonLabels(results []result) map[string]string {
	if len(results) == 0 {
		return map[string]string{}
	}
	common := map[string]string{}
	for key, value := range results[0].labels {
		common[key] = value
	}
	for _, found := range results[1:] {
		for key, value := range common {
			if found.labels[key] != value {
				delete(common, key)
			}
		}
	}
	return common
}

// renderLabels prints {k="v", ...} sorted, leaving out the keys in omit.
func renderLabels(labels, omit map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		if _, skip := omit[key]; !skip {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%q", key, labels[key]))
	}
	return "{" + strings.Join(pairs, ", ") + "}"
}

func runLabels(parsed *query, all []fakestate.Stream, stdout io.Writer) {
	values := map[string]bool{}
	for _, stream := range all {
		for key, value := range stream.Labels {
			if len(parsed.positional) < 2 {
				values[key] = true
			} else if key == parsed.positional[1] {
				values[value] = true
			}
		}
	}
	sorted := make([]string, 0, len(values))
	for value := range values {
		sorted = append(sorted, value)
	}
	sort.Strings(sorted)
	for _, value := range sorted {
		fmt.Fprintln(stdout, value)
	}
}

func runSeries(parsed *query, all []fakestate.Stream, stdout io.Writer) error {
	if len(parsed.positional) < 2 {
		return failure("logcli: error: required argument 'matcher' not provided")
	}
	selector, _, err := parseLogQL(parsed.positional[1])
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var lines []string
	for _, stream := range all {
		if line := renderLabels(stream.Labels, nil); selector.matches(stream.Labels) && !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	sort.Strings(lines)
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
	return nil
}
