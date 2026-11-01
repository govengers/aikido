package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/govengers/aikido/benchmark/internal/fakestate"
)

// found is the result of one target: the items that matched.
type found struct {
	kind  kind
	items []namespacedItem
}

func get(parsed *invocation, state *fakestate.State, stdout, stderr io.Writer) int {
	targets, err := resolveTargets(parsed.positional[1:])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	exitCode := 0
	results := make([]found, 0, len(targets))
	for _, wanted := range targets {
		items, missing := selectItems(state, parsed, wanted, false)
		for _, name := range missing {
			fmt.Fprintf(stderr, "Error from server (NotFound): %s %q not found\n", wanted.kind.resource(), name)
			exitCode = 1
		}
		results = append(results, found{kind: wanted.kind, items: items})
	}
	single := len(targets) == 1 && len(targets[0].names) == 1
	switch output := parsed.output; output {
	case "json", "yaml":
		writeObjects(stdout, output, results, single)
	case "name":
		for _, result := range results {
			for _, entry := range result.items {
				fmt.Fprintf(stdout, "%s/%s\n", result.kind.prefix(), objectName(entry.item))
			}
		}
	case "", "wide":
		if countItems(results) == 0 && exitCode == 0 {
			fmt.Fprintln(stderr, noResources(parsed, targets[0].kind))
			return 0
		}
		writeTables(stdout, parsed, results, len(results) > 1)
	default:
		fmt.Fprintf(stderr, "error: unable to match a printer suitable for the output format %q, allowed formats are: json,name,wide,yaml\n", output)
		return 1
	}
	return exitCode
}

// selectItems applies the names (exact, or as prefix for describe), -l and
// --field-selector, and returns the names that matched nothing.
func selectItems(state *fakestate.State, parsed *invocation, wanted target, prefixMatch bool) (items []namespacedItem, missing []string) {
	var candidates []namespacedItem
	for _, entry := range lookup(state, wanted.kind, parsed.namespace, parsed.allNamespaces) {
		if matches(entry.item, parsed.selector, parsed.fieldSelector) {
			candidates = append(candidates, entry)
		}
	}
	if len(wanted.names) == 0 {
		return candidates, nil
	}
	for _, name := range wanted.names {
		matched := byName(candidates, name, false)
		if len(matched) == 0 && prefixMatch {
			matched = byName(candidates, name, true)
		}
		if len(matched) == 0 {
			missing = append(missing, name)
		}
		items = append(items, matched...)
	}
	return items, missing
}

func byName(candidates []namespacedItem, name string, prefix bool) []namespacedItem {
	var matched []namespacedItem
	for _, entry := range candidates {
		itemName := objectName(entry.item)
		if itemName == name || prefix && strings.HasPrefix(itemName, name) {
			matched = append(matched, entry)
		}
	}
	return matched
}

func countItems(results []found) int {
	total := 0
	for _, result := range results {
		total += len(result.items)
	}
	return total
}

func noResources(parsed *invocation, resource kind) string {
	if parsed.allNamespaces || resource.clusterScoped {
		return "No resources found"
	}
	return fmt.Sprintf("No resources found in %s namespace.", parsed.namespace)
}

func writeObjects(stdout io.Writer, format string, results []found, single bool) {
	var objects []any
	for _, result := range results {
		for _, entry := range result.items {
			objects = append(objects, entry.item.Object)
		}
	}
	var document any = map[string]any{"apiVersion": "v1", "kind": "List", "items": append([]any{}, objects...), "metadata": map[string]any{"resourceVersion": ""}}
	if single && len(objects) == 1 {
		document = objects[0]
	}
	if format == "yaml" {
		fmt.Fprint(stdout, toYAML(document))
		return
	}
	encoded, _ := json.MarshalIndent(document, "", "    ")
	fmt.Fprintln(stdout, string(encoded))
}

// writeTables prints one table per kind; mixed lists prefix names with the
// kind and separate the tables with a blank line, like kubectl.
func writeTables(stdout io.Writer, parsed *invocation, results []found, mixed bool) {
	first := true
	for _, result := range results {
		if len(result.items) == 0 {
			continue
		}
		if !first {
			fmt.Fprintln(stdout)
		}
		first = false
		writeTable(stdout, parsed, result, mixed)
	}
}

func writeTable(stdout io.Writer, parsed *invocation, result found, mixed bool) {
	columns := result.kind.columns
	if parsed.output == "wide" {
		columns = append(append([]string{}, columns...), result.kind.wide...)
	}
	table := tabwriter.NewWriter(stdout, 0, 8, 3, ' ', 0)
	defer table.Flush()
	if !parsed.noHeaders {
		headers := columns
		if parsed.allNamespaces && !result.kind.clusterScoped {
			headers = append([]string{"NAMESPACE"}, headers...)
		}
		if parsed.showLabels {
			headers = append(append([]string{}, headers...), "LABELS")
		}
		fmt.Fprintln(table, strings.Join(headers, "\t"))
	}
	for _, entry := range result.items {
		fmt.Fprintln(table, strings.Join(rowCells(parsed, result.kind, entry, columns, mixed), "\t"))
	}
}

func rowCells(parsed *invocation, resource kind, entry namespacedItem, columns []string, mixed bool) []string {
	var cells []string
	if parsed.allNamespaces && !resource.clusterScoped {
		cells = append(cells, entry.namespace)
	}
	for _, column := range columns {
		cell, ok := entry.item.Row[column]
		if !ok {
			cell = "<none>"
		}
		if column == "NAME" && mixed {
			cell = resource.prefix() + "/" + cell
		}
		cells = append(cells, cell)
	}
	if parsed.showLabels {
		cells = append(cells, labelList(entry.item))
	}
	return cells
}

func labelList(item fakestate.Item) string {
	labels, _ := field(item.Object, "metadata.labels").(map[string]any)
	if len(labels) == 0 {
		return "<none>"
	}
	pairs := make([]string, 0, len(labels))
	for key, value := range labels {
		pairs = append(pairs, fmt.Sprintf("%s=%v", key, value))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func describe(parsed *invocation, state *fakestate.State, stdout, stderr io.Writer) int {
	targets, err := resolveTargets(parsed.positional[1:])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	exitCode := 0
	var texts []string
	for _, wanted := range targets {
		items, missing := selectItems(state, parsed, wanted, true)
		for _, name := range missing {
			fmt.Fprintf(stderr, "Error from server (NotFound): %s %q not found\n", wanted.kind.resource(), name)
			exitCode = 1
		}
		for _, entry := range items {
			texts = append(texts, describeText(entry))
		}
	}
	if len(texts) == 0 && exitCode == 0 {
		fmt.Fprintln(stderr, noResources(parsed, targets[0].kind))
	}
	fmt.Fprint(stdout, strings.Join(texts, "\n\n"))
	return exitCode
}

func describeText(entry namespacedItem) string {
	if entry.item.Describe != "" {
		return strings.TrimRight(entry.item.Describe, "\n") + "\n"
	}
	return fmt.Sprintf("Name:         %s\nNamespace:    %s\nLabels:       %s\nEvents:       <none>\n",
		objectName(entry.item), entry.namespace, strings.ReplaceAll(labelList(entry.item), ",", "\n              "))
}
