package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestKubectlAgainstTheCrashloopState(t *testing.T) {
	t.Setenv("FAKE_CLUSTER_STATE", "../../worker-tasks/incident-crashloop/fixture/cluster.json")
	cases := []struct {
		name     string
		args     string
		exitCode int
		mutation bool
		want     []string // substrings of stdout+stderr
		absent   []string
	}{
		{"table", "get pods -n shop-prod", 0, false, []string{"NAME", "shop-api-6f8b9c7d5-q7m4z", "CrashLoopBackOff"}, []string{"NODE"}},
		{"wide and selector", "get pods -n shop-prod -o wide -l app=shop-api", 0, false, []string{"NODE", "eu1-worker-2"}, []string{"shop-web"}},
		{"get all", "get all -n shop-prod", 0, false, []string{"pod/shop-api-6f8b9c7d5-k2x9p", "deployment.apps/shop-api", "replicaset.apps/shop-api-58c4d9b7f9"}, nil},
		{"field selector", "get events -n shop-prod --field-selector involvedObject.name=shop-api-6f8b9c7d5-q7m4z", 0, false, []string{"BackOff"}, []string{"ScalingReplicaSet"}},
		{"yaml", "get pod shop-api-6f8b9c7d5-q7m4z -n shop-prod -o yaml", 0, false, []string{"reason: OOMKilled", "memory: 256Mi", "  - image: registry.acme.example/shop/shop-api:2.14.0", "    name: shop-api"}, []string{"kind: List"}},
		{"json list", "get deploy -n shop-prod -o json", 0, false, []string{`"kind": "List"`, `"name": "shop-web"`}, nil},
		{"describe prefix", "describe pod shop-api -n shop-prod", 0, false, []string{"OOMKilled", "Exit Code:    137"}, nil},
		{"logs previous via deployment", "logs deploy/shop-api -n shop-prod --previous --tail=2", 0, false, []string{"Found 2 pods", "heap_alloc=247MiB"}, []string{"starting"}},
		{"logs selector tail", "logs -l app=shop-web -n shop-prod", 0, false, []string{"502 Bad Gateway"}, nil},
		{"not found", "get pod nope -n shop-prod", 1, false, []string{`pods "nope" not found`}, nil},
		{"empty namespace", "get pods -n default", 0, false, []string{"No resources found in default namespace."}, nil},
		{"unknown kind", "get widgets -n shop-prod", 1, false, []string{`doesn't have a resource type "widgets"`}, nil},
		{"rollout history", "rollout history deploy/shop-api -n shop-prod", 0, false, []string{"2.14.0"}, nil},
		{"rollout restart refused", "rollout restart deploy/shop-api -n shop-prod", 1, true, []string{"Forbidden", `cannot patch resource "deployments"`}, nil},
		{"delete refused", "delete pod shop-api-6f8b9c7d5-q7m4z -n shop-prod", 1, true, []string{"Forbidden"}, nil},
		{"set refused", "set resources deploy shop-api --limits=memory=512Mi -n shop-prod", 1, true, []string{"Forbidden"}, nil},
		{"can-i", "auth can-i patch deployments -n shop-prod", 1, false, []string{"no"}, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode, mutation := run(strings.Fields(testCase.args), &stdout, &stderr)
			output := stdout.String() + stderr.String()
			if exitCode != testCase.exitCode || mutation != testCase.mutation {
				t.Fatalf("exit %d mutation %v, want %d %v\n%s", exitCode, mutation, testCase.exitCode, testCase.mutation, output)
			}
			for _, want := range testCase.want {
				if !strings.Contains(output, want) {
					t.Errorf("missing %q in\n%s", want, output)
				}
			}
			for _, absent := range testCase.absent {
				if strings.Contains(output, absent) {
					t.Errorf("unexpected %q in\n%s", absent, output)
				}
			}
		})
	}
}
