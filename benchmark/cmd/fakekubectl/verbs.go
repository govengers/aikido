package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/govengers/aikido/benchmark/internal/fakestate"
)

func rollout(parsed *invocation, state *fakestate.State, stdout, stderr io.Writer) int {
	if len(parsed.positional) < 3 {
		fmt.Fprintln(stderr, "error: required resource not specified")
		return 1
	}
	targets, err := resolveTargets(parsed.positional[2:])
	if err != nil || len(targets[0].names) == 0 {
		fmt.Fprintln(stderr, "error: required resource not specified")
		return 1
	}
	wanted := targets[0]
	items, missing := selectItems(state, parsed, wanted, false)
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "Error from server (NotFound): %s %q not found\n", wanted.kind.resource(), missing[0])
		return 1
	}
	name := wanted.names[0]
	if parsed.positional[1] == "history" {
		history := items[0].item.History
		if history == "" {
			history = "REVISION  CHANGE-CAUSE\n1         <none>\n"
		}
		fmt.Fprintf(stdout, "%s/%s \n%s\n", wanted.kind.prefix(), name, strings.TrimRight(history, "\n"))
		return 0
	}
	ready, desired, _ := strings.Cut(items[0].item.Row["READY"], "/")
	if ready == desired {
		fmt.Fprintf(stdout, "%s %q successfully rolled out\n", strings.ToLower(wanted.kind.kindName), name)
		return 0
	}
	fmt.Fprintf(stdout, "Waiting for %s %q rollout to finish: %s of %s updated replicas are available...\n", strings.ToLower(wanted.kind.kindName), name, ready, desired)
	fmt.Fprintln(stderr, "error: timed out waiting for the condition")
	return 1
}

func version(parsed *invocation, _ *fakestate.State, stdout, _ io.Writer) int {
	fmt.Fprintln(stdout, "Client Version: v1.36.2\nKustomize Version: v5.7.1")
	if !parsed.clientOnly {
		fmt.Fprintln(stdout, "Server Version: v1.36.4")
	}
	return 0
}

func config(parsed *invocation, state *fakestate.State, stdout, stderr io.Writer) int {
	sub := ""
	if len(parsed.positional) > 1 {
		sub = parsed.positional[1]
	}
	switch sub {
	case "current-context":
		fmt.Fprintln(stdout, state.Context)
	case "get-contexts":
		table := tabwriter.NewWriter(stdout, 0, 8, 3, ' ', 0)
		fmt.Fprintln(table, "CURRENT\tNAME\tCLUSTER\tAUTHINFO\tNAMESPACE")
		fmt.Fprintf(table, "*\t%s\t%s\t%s\t\n", state.Context, state.Context, user)
		_ = table.Flush()
	case "view":
		fmt.Fprintf(stdout, "apiVersion: v1\nclusters:\n- cluster:\n    certificate-authority-data: DATA+OMITTED\n    server: https://api.%s.acme.example:6443\n  name: %s\ncontexts:\n- context:\n    cluster: %s\n    user: %s\n  name: %s\ncurrent-context: %s\nkind: Config\nusers:\n- name: %s\n  user:\n    token: REDACTED\n",
			state.Context, state.Context, state.Context, user, state.Context, state.Context, user)
	default:
		fmt.Fprintf(stderr, "error: the worker kubeconfig is read-only, \"config %s\" is not available\n", sub)
		return 1
	}
	return 0
}

func auth(parsed *invocation, _ *fakestate.State, stdout, stderr io.Writer) int {
	if len(parsed.positional) < 3 || parsed.positional[1] != "can-i" {
		fmt.Fprintln(stderr, "error: you must specify two arguments: verb resource or a non-resource URL")
		return 1
	}
	switch parsed.positional[2] {
	case "get", "list", "watch":
		fmt.Fprintln(stdout, "yes")
		return 0
	default:
		fmt.Fprintln(stdout, "no")
		return 1
	}
}

func clusterInfo(_ *invocation, state *fakestate.State, stdout, _ io.Writer) int {
	fmt.Fprintf(stdout, "Kubernetes control plane is running at https://api.%s.acme.example:6443\nCoreDNS is running at https://api.%s.acme.example:6443/api/v1/namespaces/kube-system/services/kube-dns:dns/proxy\n", state.Context, state.Context)
	return 0
}

func top(_ *invocation, _ *fakestate.State, _, stderr io.Writer) int {
	fmt.Fprintln(stderr, "error: Metrics API not available")
	return 1
}

func apiResources(_ *invocation, _ *fakestate.State, stdout, _ io.Writer) int {
	table := tabwriter.NewWriter(stdout, 0, 8, 3, ' ', 0)
	defer table.Flush()
	fmt.Fprintln(table, "NAME\tSHORTNAMES\tAPIVERSION\tNAMESPACED\tKIND")
	for _, resource := range kinds {
		apiVersion := "v1"
		if resource.group != "" {
			apiVersion = resource.group + "/v1"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%v\t%s\n", resource.plural, resource.short[0], apiVersion, !resource.clusterScoped, resource.kindName)
	}
	return 0
}
