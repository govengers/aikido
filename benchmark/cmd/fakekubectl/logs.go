package main

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/govengers/aikido/benchmark/internal/fakestate"
)

// selectorTail is kubectl's default --tail when logs are selected with -l.
const selectorTail = 10

func logs(parsed *invocation, state *fakestate.State, stdout, stderr io.Writer) int {
	pods, err := logPods(parsed, state, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(pods) == 0 {
		fmt.Fprintln(stderr, noResources(parsed, kind{}))
		return 0
	}
	tail := parsed.tail
	if !parsed.tailSet && parsed.selector != "" {
		tail = selectorTail
	}
	container := parsed.container
	if container == "" && parsed.selector == "" && len(parsed.positional) > 2 {
		container = parsed.positional[2]
	}
	for _, pod := range pods {
		text, err := podLog(pod.item, container, parsed.previous)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprint(stdout, lastLines(text, tail))
	}
	return 0
}

// logPods resolves `logs POD`, `logs TYPE/NAME` (first pod named NAME-...) and
// `logs -l SELECTOR`.
func logPods(parsed *invocation, state *fakestate.State, stderr io.Writer) ([]namespacedItem, error) {
	podKind, _ := findKind("pods")
	all := lookup(state, podKind, parsed.namespace, false)
	if parsed.selector != "" {
		var selected []namespacedItem
		for _, entry := range all {
			if matches(entry.item, parsed.selector, "") {
				selected = append(selected, entry)
			}
		}
		return selected, nil
	}
	if len(parsed.positional) < 2 {
		return nil, failure("error: expected 'logs [-f] [-p] (POD | TYPE/NAME) [-c CONTAINER]'.\nPOD or TYPE/NAME is a required argument for the logs command")
	}
	operand := parsed.positional[1]
	kindName, name, typed := strings.Cut(operand, "/")
	if !typed {
		name = operand
	}
	owner, known := findKind(kindName)
	if !typed || owner.plural == "pods" {
		if matched := byName(all, name, false); len(matched) > 0 {
			return matched[:1], nil
		}
		return nil, failure("Error from server (NotFound): pods %q not found", name)
	}
	if !known {
		return nil, failure("error: the server doesn't have a resource type %q", kindName)
	}
	owned := byName(all, name+"-", true)
	if len(owned) == 0 {
		return nil, failure("Error from server (NotFound): %s %q not found", owner.resource(), name)
	}
	if len(owned) > 1 {
		fmt.Fprintf(stderr, "Found %d pods, using pod/%s\n", len(owned), objectName(owned[0].item))
	}
	return owned[:1], nil
}

func podLog(pod fakestate.Item, container string, previous bool) (string, error) {
	name := objectName(pod)
	var containers []string
	for key := range pod.Logs {
		containers = append(containers, key)
	}
	sort.Strings(containers)
	switch {
	case container == "" && len(containers) == 1:
		container = containers[0]
	case container == "":
		return "", failure("error: a container name must be specified for pod %s, choose one of: [%s]", name, strings.Join(containers, " "))
	case !slices.Contains(containers, container):
		return "", failure("error: container %s is not valid for pod %s", container, name)
	}
	if !previous {
		return pod.Logs[container], nil
	}
	text, ok := pod.Previous[container]
	if !ok {
		return "", failure("Error from server (BadRequest): previous terminated container %q in pod %q not found", container, name)
	}
	return text, nil
}

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if text == "" {
		return ""
	}
	if count >= 0 && count < len(lines) {
		lines = lines[len(lines)-count:]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
