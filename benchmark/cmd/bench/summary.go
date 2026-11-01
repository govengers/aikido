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

// writeSummary renders summary.md from results.jsonl in runDirectory.
func writeSummary(runDirectory string) error {
	resultsFile, err := os.DirFS(runDirectory).Open("results.jsonl")
	if err != nil {
		return err
	}
	defer resultsFile.Close()
	var results []Result
	scanner := bufio.NewScanner(resultsFile)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		var result Result
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			return fmt.Errorf("results.jsonl: %w", err)
		}
		results = append(results, result)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDirectory, "summary.md"), []byte(renderSummary(filepath.Base(runDirectory), results)), filePermissions)
}

type group struct {
	key     string
	results []Result
}

func groupBy(results []Result, key func(Result) string) []group {
	indexByKey := map[string]int{}
	var groups []group
	for _, result := range results {
		groupKey := key(result)
		index, ok := indexByKey[groupKey]
		if !ok {
			index = len(groups)
			indexByKey[groupKey] = index
			groups = append(groups, group{key: groupKey})
		}
		groups[index].results = append(groups[index].results, result)
	}
	sort.Slice(groups, func(left, right int) bool { return groups[left].key < groups[right].key })
	return groups
}

func setup(result Result) string { return result.Harness + " / " + result.Model }

func renderSummary(runID string, results []Result) string {
	var builder strings.Builder
	setups := groupBy(results, setup)
	fmt.Fprintf(&builder, "# Benchmark run %s\n\n%d runs. Pass means check.sh accepted the files; cost and tokens are what each harness reports (OpenRouter usage).\n\n", runID, len(results))

	writeOverall(&builder, setups)

	builder.WriteString("\n## Pass rate by category\n\n")
	matrix(&builder, "category", groupBy(results, func(result Result) string { return result.Category }), setups, passRate)

	builder.WriteString("\n## Per task (passes/runs, median wall s, total USD)\n\n")
	matrix(&builder, "task", groupBy(results, func(result Result) string { return result.Task + " (" + result.Category + ")" }), setups, taskCell)

	builder.WriteString("\n## Tool-call mix\n\n")
	for _, setupGroup := range setups {
		writeToolMix(&builder, setupGroup)
	}

	writeFailures(&builder, results)
	return builder.String()
}

func writeOverall(builder *strings.Builder, setups []group) {
	builder.WriteString("## Overall\n\n| harness / model | pass | median wall s | median LLM calls | median tool calls | input tok | output tok | cache read tok | cost USD | runs with error |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, setupGroup := range setups {
		var wall, calls, toolCalls []float64
		var input, output, cacheRead, runsWithError int
		var cost float64
		for _, result := range setupGroup.results {
			wall = append(wall, result.WallSeconds)
			calls = append(calls, float64(result.LLMCalls))
			toolCalls = append(toolCalls, float64(sumTools(result.ToolCalls)))
			input, output, cacheRead, cost = input+result.InputTokens, output+result.OutputTokens, cacheRead+result.CacheReadTokens, cost+result.CostUSD
			if result.Error != "" {
				runsWithError++
			}
		}
		fmt.Fprintf(builder, "| %s | %s | %.1f | %.0f | %.0f | %d | %d | %d | %.4f | %d |\n",
			setupGroup.key, passRate(setupGroup.results), median(wall), median(calls), median(toolCalls), input, output, cacheRead, cost, runsWithError)
	}
}

func taskCell(results []Result) string {
	var wall []float64
	var cost float64
	for _, result := range results {
		wall = append(wall, result.WallSeconds)
		cost += result.CostUSD
	}
	return fmt.Sprintf("%s, %.0fs, $%.3f", passCount(results), median(wall), cost)
}

func writeToolMix(builder *strings.Builder, setupGroup group) {
	counts := map[string]int{}
	toolErrors := 0
	for _, result := range setupGroup.results {
		for name, count := range result.ToolCalls {
			counts[name] += count
		}
		toolErrors += result.ToolErrors
	}
	total := sumTools(counts)
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(left, right int) bool {
		return counts[names[left]] > counts[names[right]] || counts[names[left]] == counts[names[right]] && names[left] < names[right]
	})
	var parts []string
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %d (%.0f%%)", name, counts[name], 100*float64(counts[name])/float64(max(total, 1))))
	}
	fmt.Fprintf(builder, "- **%s**: %d calls, %d failed. %s\n", setupGroup.key, total, toolErrors, strings.Join(parts, ", "))
}

func writeFailures(builder *strings.Builder, results []Result) {
	var failures []Result
	for _, result := range results {
		if !result.Pass {
			failures = append(failures, result)
		}
	}
	if len(failures) == 0 {
		return
	}
	builder.WriteString("\n## Failures\n\n")
	sort.Slice(failures, func(left, right int) bool {
		return setup(failures[left])+failures[left].Task < setup(failures[right])+failures[right].Task
	})
	for _, failure := range failures {
		reason := failure.Error
		if reason == "" {
			reason = firstFailLine(failure.Check)
		}
		fmt.Fprintf(builder, "- %s, %s rep %d: %s\n", setup(failure), failure.Task, failure.Repetition, oneLine(reason))
	}
}

func matrix(builder *strings.Builder, label string, rows, columns []group, cell func([]Result) string) {
	fmt.Fprintf(builder, "| %s |", label)
	for _, column := range columns {
		fmt.Fprintf(builder, " %s |", column.key)
	}
	builder.WriteString("\n|---|" + strings.Repeat("---|", len(columns)) + "\n")
	for _, row := range rows {
		fmt.Fprintf(builder, "| %s |", row.key)
		for _, column := range columns {
			var cellResults []Result
			for _, result := range row.results {
				if setup(result) == column.key {
					cellResults = append(cellResults, result)
				}
			}
			if len(cellResults) == 0 {
				builder.WriteString(" - |")
				continue
			}
			fmt.Fprintf(builder, " %s |", cell(cellResults))
		}
		builder.WriteString("\n")
	}
}

func passCount(results []Result) string {
	return fmt.Sprintf("%d/%d", passes(results), len(results))
}

func passRate(results []Result) string {
	passed := passes(results)
	return fmt.Sprintf("%d/%d (%.0f%%)", passed, len(results), 100*float64(passed)/float64(max(len(results), 1)))
}

func passes(results []Result) int {
	passed := 0
	for _, result := range results {
		if result.Pass {
			passed++
		}
	}
	return passed
}

func sumTools(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if len(sorted)%2 == 1 {
		return sorted[len(sorted)/2]
	}
	return (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
}

func firstFailLine(check string) string {
	for _, line := range strings.Split(check, "\n") {
		if strings.HasPrefix(line, "FAIL:") {
			return line
		}
	}
	return tail(check, 200)
}

func oneLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 240 {
		text = text[:240] + "..."
	}
	return strings.ReplaceAll(text, "|", "\\|")
}
