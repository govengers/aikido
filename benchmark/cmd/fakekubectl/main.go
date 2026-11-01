// Command fakekubectl is the worker suite's kubectl: it answers read-only
// verbs (get, describe, logs, rollout status|history, auth can-i, config,
// version) from the canned cluster state in $FAKE_CLUSTER_STATE, refuses every
// mutating verb with an RBAC error, and records each invocation in $FAKE_LOG.
// See benchmark/README.md, worker suite.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/govengers/aikido/benchmark/internal/fakestate"
)

const user = "acme-worker-readonly"

func main() {
	arguments := os.Args[1:]
	exitCode, mutation := run(arguments, os.Stdout, os.Stderr)
	fakestate.Record("kubectl", arguments, mutation, exitCode)
	os.Exit(exitCode)
}

// invocation is a parsed command line. kubectl accepts flags anywhere, so
// positional holds the verb and its operands in order.
type invocation struct {
	positional    []string
	namespace     string
	allNamespaces bool
	output        string
	selector      string
	fieldSelector string
	container     string
	tail          int // -1: all lines
	tailSet       bool
	previous      bool
	showLabels    bool
	noHeaders     bool
	clientOnly    bool
}

// valueFlags take the next argument (or =value); unknown value flags are
// listed so their value is not mistaken for an operand.
var valueFlags = map[string]string{
	"-n": "namespace", "--namespace": "namespace", "-o": "output", "--output": "output",
	"-l": "selector", "--selector": "selector", "-c": "container", "--container": "container",
	"--tail": "tail", "--field-selector": "field-selector",
	"--since": "", "--since-time": "", "--context": "", "--kubeconfig": "", "--request-timeout": "",
	"--sort-by": "", "--limit-bytes": "", "--as": "", "--cluster": "", "--user": "", "--replicas": "",
	"--type": "", "--filename": "", "--timeout": "", "--max-log-requests": "", "--chunk-size": "", "--template": "",
}

// parse reads the command line. ponytail: -f and -p are booleans (logs
// --follow / --previous); for apply and patch they carry a value, but those
// verbs are refused before the value matters.
func parse(arguments []string) invocation {
	parsed := invocation{namespace: "default", tail: -1}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if !strings.HasPrefix(argument, "-") || argument == "-" {
			parsed.positional = append(parsed.positional, argument)
			continue
		}
		name, value, hasValue := strings.Cut(argument, "=")
		field, takesValue := valueFlags[name]
		if !takesValue {
			parsed.setBoolean(name)
			continue
		}
		if !hasValue && index+1 < len(arguments) {
			index++
			value = arguments[index]
		}
		parsed.setValue(field, value)
	}
	return parsed
}

func (parsed *invocation) setBoolean(name string) {
	switch name {
	case "-A", "--all-namespaces":
		parsed.allNamespaces = true
	case "-p", "--previous":
		parsed.previous = true
	case "--show-labels":
		parsed.showLabels = true
	case "--no-headers":
		parsed.noHeaders = true
	case "--client":
		parsed.clientOnly = true
	}
}

func (parsed *invocation) setValue(field, value string) {
	switch field {
	case "namespace":
		parsed.namespace = value
	case "output":
		parsed.output = value
	case "selector":
		parsed.selector = value
	case "field-selector":
		parsed.fieldSelector = value
	case "container":
		parsed.container = value
	case "tail":
		if lines, err := strconv.Atoi(value); err == nil {
			parsed.tail, parsed.tailSet = lines, true
		}
	}
}

// mutatingVerbs change the cluster (or open a shell in it); the worker's
// kubeconfig is read-only, so each is refused and recorded as a mutation.
var mutatingVerbs = map[string]string{
	"apply": "patch", "create": "create", "delete": "delete", "patch": "patch", "edit": "patch",
	"scale": "patch", "replace": "update", "set": "patch", "label": "patch", "annotate": "patch",
	"cordon": "patch", "uncordon": "patch", "drain": "patch", "taint": "patch", "autoscale": "create",
	"expose": "create", "run": "create", "exec": "create", "cp": "create", "attach": "create",
	"debug": "patch", "port-forward": "create",
}

// run executes one kubectl call and returns its exit code and whether it was
// a (refused) mutation.
func run(arguments []string, stdout, stderr io.Writer) (exitCode int, mutation bool) {
	parsed := parse(arguments)
	if len(parsed.positional) == 0 {
		fmt.Fprint(stdout, usage)
		return 0, false
	}
	verb := parsed.positional[0]
	if rbacVerb, ok := mutatingVerbs[verb]; ok {
		fmt.Fprintln(stderr, forbidden(&parsed, rbacVerb))
		return 1, true
	}
	if verb == "rollout" && len(parsed.positional) > 1 && parsed.positional[1] != "status" && parsed.positional[1] != "history" {
		fmt.Fprintln(stderr, forbidden(&invocation{positional: parsed.positional[1:], namespace: parsed.namespace}, "patch"))
		return 1, true
	}
	if handler, ok := readOnlyVerbs[verb]; ok {
		state, err := fakestate.Load()
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1, false
		}
		return handler(&parsed, state, stdout, stderr), false
	}
	fmt.Fprintf(stderr, "error: unknown command %q for \"kubectl\"\nRun 'kubectl --help' for usage.\n", verb)
	return 1, false
}

type verbHandler func(parsed *invocation, state *fakestate.State, stdout, stderr io.Writer) int

var readOnlyVerbs = map[string]verbHandler{
	"get":           get,
	"describe":      describe,
	"logs":          logs,
	"rollout":       rollout,
	"version":       version,
	"config":        config,
	"auth":          auth,
	"cluster-info":  clusterInfo,
	"top":           top,
	"api-resources": apiResources,
}

// forbidden renders the RBAC error the API server sends for a write.
func forbidden(parsed *invocation, rbacVerb string) string {
	if len(parsed.positional) > 1 {
		if targets, err := resolveTargets(parsed.positional[1:]); err == nil && len(targets[0].names) > 0 {
			resource := targets[0].kind
			return fmt.Sprintf("Error from server (Forbidden): %s %q is forbidden: User %q cannot %s resource %q in API group %q in the namespace %q",
				resource.resource(), targets[0].names[0], user, rbacVerb, resource.plural, resource.group, parsed.namespace)
		}
	}
	return fmt.Sprintf("Error from server (Forbidden): User %q cannot %s resources in the namespace %q", user, rbacVerb, parsed.namespace)
}

// messageError carries kubectl's own message verbatim (capitals included).
type messageError string

func (message messageError) Error() string { return string(message) }

func failure(format string, arguments ...any) error {
	return messageError(fmt.Sprintf(format, arguments...))
}

const usage = `kubectl controls the Kubernetes cluster manager.

Basic Commands:
  get           Display one or many resources
  describe      Show details of a specific resource or group of resources
  logs          Print the logs for a container in a pod
  rollout       Manage the rollout of a resource (status, history)
  top           Display resource (CPU/memory) usage
  auth          Inspect authorization (can-i)
  config        Modify kubeconfig files (current-context, get-contexts, view)
  api-resources Print the supported API resources on the server
  version       Print the client and server version information

Usage:
  kubectl [flags] [options]
`
