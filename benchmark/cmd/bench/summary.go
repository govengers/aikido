package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// writeSummary renders summary.md from results.jsonl in runDir.
func writeSummary(runDir string) error {
	f, err := os.Open(filepath.Join(runDir, "results.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	var results []Result
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r Result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return fmt.Errorf("results.jsonl: %w", err)
		}
		results = append(results, r)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "summary.md"), []byte(renderSummary(filepath.Base(runDir), results)), 0o644)
}

type group struct {
	key     string
	results []Result
}

func groupBy(results []Result, key func(Result) string) []group {
	idx := map[string]int{}
	var gs []group
	for _, r := range results {
		k := key(r)
		i, ok := idx[k]
		if !ok {
			i = len(gs)
			idx[k] = i
			gs = append(gs, group{key: k})
		}
		gs[i].results = append(gs[i].results, r)
	}
	sort.Slice(gs, func(a, b int) bool { return gs[a].key < gs[b].key })
	return gs
}

func setup(r Result) string { return r.Harness + " / " + r.Model }

func renderSummary(runID string, results []Result) string {
	var b strings.Builder
	setups := groupBy(results, setup)
	fmt.Fprintf(&b, "# Benchmark run %s\n\n%d runs. Pass means check.sh accepted the files; cost and tokens are what each harness reports (OpenRouter usage).\n\n", runID, len(results))

	b.WriteString("## Overall\n\n| harness / model | pass | median wall s | median LLM calls | median tool calls | input tok | output tok | cache read tok | cost USD | runs with error |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, g := range setups {
		var wall, calls, toolN []float64
		var in, out, cache, errs int
		var cost float64
		for _, r := range g.results {
			wall = append(wall, r.WallSeconds)
			calls = append(calls, float64(r.LLMCalls))
			toolN = append(toolN, float64(sumTools(r.ToolCalls)))
			in, out, cache, cost = in+r.InputTokens, out+r.OutputTokens, cache+r.CacheReadTokens, cost+r.CostUSD
			if r.Error != "" {
				errs++
			}
		}
		fmt.Fprintf(&b, "| %s | %s | %.1f | %.0f | %.0f | %d | %d | %d | %.4f | %d |\n",
			g.key, passRate(g.results), median(wall), median(calls), median(toolN), in, out, cache, cost, errs)
	}

	b.WriteString("\n## Pass rate by category\n\n")
	matrix(&b, "category", groupBy(results, func(r Result) string { return r.Category }), setups, func(rs []Result) string { return passRate(rs) })

	b.WriteString("\n## Per task (passes/runs, median wall s, total USD)\n\n")
	matrix(&b, "task", groupBy(results, func(r Result) string { return r.Task + " (" + r.Category + ")" }), setups, func(rs []Result) string {
		var wall []float64
		var cost float64
		for _, r := range rs {
			wall = append(wall, r.WallSeconds)
			cost += r.CostUSD
		}
		return fmt.Sprintf("%s, %.0fs, $%.3f", passCount(rs), median(wall), cost)
	})

	b.WriteString("\n## Tool-call mix\n\n")
	for _, g := range setups {
		counts := map[string]int{}
		toolErrs := 0
		for _, r := range g.results {
			for n, c := range r.ToolCalls {
				counts[n] += c
			}
			toolErrs += r.ToolErrors
		}
		total := sumTools(counts)
		names := make([]string, 0, len(counts))
		for n := range counts {
			names = append(names, n)
		}
		sort.Slice(names, func(a, c int) bool {
			return counts[names[a]] > counts[names[c]] || counts[names[a]] == counts[names[c]] && names[a] < names[c]
		})
		var parts []string
		for _, n := range names {
			parts = append(parts, fmt.Sprintf("%s %d (%.0f%%)", n, counts[n], 100*float64(counts[n])/float64(max(total, 1))))
		}
		fmt.Fprintf(&b, "- **%s**: %d calls, %d failed. %s\n", g.key, total, toolErrs, strings.Join(parts, ", "))
	}

	var failures []Result
	for _, r := range results {
		if !r.Pass {
			failures = append(failures, r)
		}
	}
	if len(failures) > 0 {
		b.WriteString("\n## Failures\n\n")
		sort.Slice(failures, func(a, c int) bool { return setup(failures[a])+failures[a].Task < setup(failures[c])+failures[c].Task })
		for _, r := range failures {
			reason := r.Error
			if reason == "" {
				reason = firstFailLine(r.Check)
			}
			fmt.Fprintf(&b, "- %s, %s rep %d: %s\n", setup(r), r.Task, r.Rep, oneLine(reason))
		}
	}
	return b.String()
}

func matrix(b *strings.Builder, label string, rows, cols []group, cell func([]Result) string) {
	fmt.Fprintf(b, "| %s |", label)
	for _, c := range cols {
		fmt.Fprintf(b, " %s |", c.key)
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(cols)) + "\n")
	for _, row := range rows {
		fmt.Fprintf(b, "| %s |", row.key)
		for _, c := range cols {
			var rs []Result
			for _, r := range row.results {
				if setup(r) == c.key {
					rs = append(rs, r)
				}
			}
			if len(rs) == 0 {
				b.WriteString(" - |")
				continue
			}
			fmt.Fprintf(b, " %s |", cell(rs))
		}
		b.WriteString("\n")
	}
}

func passCount(rs []Result) string {
	n := 0
	for _, r := range rs {
		if r.Pass {
			n++
		}
	}
	return fmt.Sprintf("%d/%d", n, len(rs))
}

func passRate(rs []Result) string {
	n := 0
	for _, r := range rs {
		if r.Pass {
			n++
		}
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", n, len(rs), 100*float64(n)/float64(max(len(rs), 1)))
}

func sumTools(m map[string]int) int {
	n := 0
	for _, c := range m {
		n += c
	}
	return n
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func firstFailLine(check string) string {
	for _, l := range strings.Split(check, "\n") {
		if strings.HasPrefix(l, "FAIL:") {
			return l
		}
	}
	return tail(check, 200)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	return strings.ReplaceAll(s, "|", "\\|")
}
