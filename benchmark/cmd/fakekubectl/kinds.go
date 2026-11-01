package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/govengers/aikido/benchmark/internal/fakestate"
)

// kind is one resource type: how it is named on the command line and how
// its table looks.
type kind struct {
	plural        string // key in the state file
	kindName      string // Kind in manifests
	short         []string
	group         string // API group, "" for core
	columns, wide []string
	clusterScoped bool
}

func (resource kind) resource() string {
	if resource.group == "" {
		return resource.plural
	}
	return resource.plural + "." + resource.group
}

// prefix is the resource/name prefix kubectl prints for -o name and mixed lists.
func (resource kind) prefix() string {
	singular := strings.ToLower(resource.kindName)
	if resource.group == "" {
		return singular
	}
	return singular + "." + resource.group
}

var workloadWide = []string{"CONTAINERS", "IMAGES", "SELECTOR"}

var kinds = []kind{
	{plural: "pods", kindName: "Pod", short: []string{"po", "pod"}, columns: []string{"NAME", "READY", "STATUS", "RESTARTS", "AGE"}, wide: []string{"IP", "NODE"}},
	{plural: "services", kindName: "Service", short: []string{"svc", "service"}, columns: []string{"NAME", "TYPE", "CLUSTER-IP", "EXTERNAL-IP", "PORT(S)", "AGE"}, wide: []string{"SELECTOR"}},
	{plural: "deployments", kindName: "Deployment", short: []string{"deploy", "deployment"}, group: "apps", columns: []string{"NAME", "READY", "UP-TO-DATE", "AVAILABLE", "AGE"}, wide: workloadWide},
	{plural: "replicasets", kindName: "ReplicaSet", short: []string{"rs", "replicaset"}, group: "apps", columns: []string{"NAME", "DESIRED", "CURRENT", "READY", "AGE"}, wide: workloadWide},
	{plural: "jobs", kindName: "Job", short: []string{"job"}, group: "batch", columns: []string{"NAME", "STATUS", "COMPLETIONS", "DURATION", "AGE"}, wide: workloadWide},
	{plural: "cronjobs", kindName: "CronJob", short: []string{"cj", "cronjob"}, group: "batch", columns: []string{"NAME", "SCHEDULE", "TIMEZONE", "SUSPEND", "ACTIVE", "LAST SCHEDULE", "AGE"}, wide: workloadWide},
	{plural: "events", kindName: "Event", short: []string{"ev", "event"}, columns: []string{"LAST SEEN", "TYPE", "REASON", "OBJECT", "MESSAGE"}},
	{plural: "endpoints", kindName: "Endpoints", short: []string{"ep"}, columns: []string{"NAME", "ENDPOINTS", "AGE"}},
	{plural: "configmaps", kindName: "ConfigMap", short: []string{"cm", "configmap"}, columns: []string{"NAME", "DATA", "AGE"}},
	{plural: "ingresses", kindName: "Ingress", short: []string{"ing", "ingress"}, group: "networking.k8s.io", columns: []string{"NAME", "CLASS", "HOSTS", "ADDRESS", "PORTS", "AGE"}},
	{plural: "horizontalpodautoscalers", kindName: "HorizontalPodAutoscaler", short: []string{"hpa"}, group: "autoscaling", columns: []string{"NAME", "REFERENCE", "TARGETS", "MINPODS", "MAXPODS", "REPLICAS", "AGE"}},
	{plural: "applications", kindName: "Application", short: []string{"app", "application", "apps"}, group: "argoproj.io", columns: []string{"NAME", "SYNC STATUS", "HEALTH STATUS"}},
	{plural: "nodes", kindName: "Node", short: []string{"no", "node"}, columns: []string{"NAME", "STATUS", "ROLES", "AGE", "VERSION"}, wide: []string{"INTERNAL-IP", "OS-IMAGE", "KERNEL-VERSION", "CONTAINER-RUNTIME"}, clusterScoped: true},
	{plural: "namespaces", kindName: "Namespace", short: []string{"ns", "namespace"}, columns: []string{"NAME", "STATUS", "AGE"}, clusterScoped: true},
}

// allKinds is what `kubectl get all` lists.
var allKinds = []string{"pods", "services", "deployments", "replicasets", "jobs", "cronjobs"}

func findKind(name string) (kind, bool) {
	name = strings.ToLower(name)
	for _, candidate := range kinds {
		if name == candidate.plural || name == candidate.resource() || slices.Contains(candidate.short, name) || name == candidate.prefix() {
			return candidate, true
		}
	}
	return kind{}, false
}

// target is one resource type with the names asked for (none: all).
type target struct {
	kind  kind
	names []string
}

// resolveTargets reads `pods`, `pods,deploy`, `pod x y`, `pod/x deploy/y` and `all`.
func resolveTargets(operands []string) ([]target, error) {
	if len(operands) == 0 {
		return nil, failure(`You must specify the type of resource to get. Use "kubectl api-resources" for a complete list of supported resources.`)
	}
	if strings.Contains(operands[0], "/") {
		return resolveSlashTargets(operands)
	}
	var targets []target
	for _, name := range strings.Split(operands[0], ",") {
		if name == "all" {
			for _, plural := range allKinds {
				resource, _ := findKind(plural)
				targets = append(targets, target{kind: resource})
			}
			continue
		}
		resource, ok := findKind(name)
		if !ok {
			return nil, failure("error: the server doesn't have a resource type %q", name)
		}
		targets = append(targets, target{kind: resource})
	}
	if len(operands) > 1 {
		if len(targets) != 1 {
			return nil, failure("error: arguments in resource/name form must have a single resource and name")
		}
		targets[0].names = operands[1:]
	}
	return targets, nil
}

func resolveSlashTargets(operands []string) ([]target, error) {
	var targets []target
	for _, operand := range operands {
		kindName, name, ok := strings.Cut(operand, "/")
		if !ok {
			return nil, failure("error: there is no need to specify a resource type as a separate argument when passing arguments in resource/name form (e.g. 'kubectl get resource/<resource_name>' instead of 'kubectl get resource resource/<resource_name>'): %s", operand)
		}
		resource, found := findKind(kindName)
		if !found {
			return nil, failure("error: the server doesn't have a resource type %q", kindName)
		}
		targets = append(targets, target{kind: resource, names: []string{name}})
	}
	return targets, nil
}

// namespacedItem is an item with the namespace it was found in.
type namespacedItem struct {
	namespace string
	item      fakestate.Item
}

// lookup returns every item of a kind in the namespace (all namespaces with
// allNamespaces), sorted by namespace, then name.
func lookup(state *fakestate.State, resource kind, namespace string, allNamespaces bool) []namespacedItem {
	if resource.plural == "namespaces" && len(state.Cluster["namespaces"]) == 0 {
		return derivedNamespaces(state)
	}
	if resource.clusterScoped {
		return withNamespace("", state.Cluster[resource.plural])
	}
	if !allNamespaces {
		return withNamespace(namespace, state.Namespaces[namespace][resource.plural])
	}
	namespaces := make([]string, 0, len(state.Namespaces))
	for name := range state.Namespaces {
		namespaces = append(namespaces, name)
	}
	sort.Strings(namespaces)
	var found []namespacedItem
	for _, name := range namespaces {
		found = append(found, withNamespace(name, state.Namespaces[name][resource.plural])...)
	}
	return found
}

func withNamespace(namespace string, items []fakestate.Item) []namespacedItem {
	found := make([]namespacedItem, 0, len(items))
	for _, item := range items {
		found = append(found, namespacedItem{namespace: namespace, item: item})
	}
	return found
}

func derivedNamespaces(state *fakestate.State) []namespacedItem {
	names := []string{"default", "kube-system"}
	for name := range state.Namespaces {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	found := make([]namespacedItem, 0, len(names))
	for _, name := range names {
		object := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name}, "status": map[string]any{"phase": "Active"}}
		found = append(found, namespacedItem{item: fakestate.Item{Row: map[string]string{"NAME": name, "STATUS": "Active", "AGE": "412d"}, Object: object}})
	}
	return found
}

// objectName is metadata.name, or the NAME cell for items without a manifest.
func objectName(item fakestate.Item) string {
	if name, ok := field(item.Object, "metadata.name").(string); ok {
		return name
	}
	return item.Row["NAME"]
}

// field walks a dotted path through a manifest.
func field(object map[string]any, path string) any {
	var current any = object
	for _, key := range strings.Split(path, ".") {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = asMap[key]
	}
	return current
}

// matches applies -l (equality and inequality on metadata.labels) and
// --field-selector (equality on dotted manifest paths).
func matches(item fakestate.Item, selector, fieldSelector string) bool {
	labels, _ := field(item.Object, "metadata.labels").(map[string]any)
	for _, requirement := range splitSelector(selector) {
		if !strings.Contains(requirement, "=") { // existence: -l app
			if labels[strings.TrimSpace(requirement)] == nil {
				return false
			}
			continue
		}
		key, value, negate := parseRequirement(requirement)
		if (fmt.Sprint(labels[key]) == value) == negate {
			return false
		}
	}
	for _, requirement := range splitSelector(fieldSelector) {
		key, value, negate := parseRequirement(requirement)
		if (fmt.Sprint(field(item.Object, key)) == value) == negate {
			return false
		}
	}
	return true
}

func splitSelector(selector string) []string {
	var requirements []string
	for _, requirement := range strings.Split(selector, ",") {
		if requirement = strings.TrimSpace(requirement); requirement != "" {
			requirements = append(requirements, requirement)
		}
	}
	return requirements
}

func parseRequirement(requirement string) (key, value string, negate bool) {
	if key, value, ok := strings.Cut(requirement, "!="); ok {
		return strings.TrimSpace(key), strings.TrimSpace(value), true
	}
	key, value, _ = strings.Cut(strings.Replace(requirement, "==", "=", 1), "=")
	return strings.TrimSpace(key), strings.TrimSpace(value), false
}
