package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogcliAgainstTheCrashloopState(t *testing.T) {
	t.Setenv("FAKE_CLUSTER_STATE", "../../worker-tasks/incident-crashloop/fixture/cluster.json")
	cases := []struct {
		name     string
		args     []string
		exitCode int
		want     []string
		absent   []string
	}{
		{
			"filter and limit",
			[]string{"query", `{namespace="shop-prod", app="shop-api"} |= "memstats"`, "--since", "2h", "--limit", "3"},
			0,
			[]string{"Common labels:", "heap_alloc=214MiB", "query_range?direction=BACKWARD"},
			[]string{"starting", "heap_alloc=91MiB"},
		},
		{"since bounds", []string{"query", `{app="shop-api", namespace="shop-prod"} |= "heap_alloc=91MiB"`, "--since=30m"}, 0, nil, []string{"heap_alloc=91MiB"}},
		{
			"regex and negative filter",
			[]string{"query", `{app=~"shop-.*"} |~ "502|OOM" != "catalogue"`, "--since", "1h", "-q", "-o", "raw"},
			0,
			[]string{"path=/api/cart"},
			[]string{"catalogue", "Common labels"},
		},
		{"forward order", []string{"query", `{app="shop-web"} |= "502"`, "--since", "2h", "--forward", "--limit", "1", "-q"}, 0, []string{"06:40:11"}, []string{"07:09:58"}},
		{"parser stage ignored", []string{"query", `{app="shop-worker"} | logfmt | level="info"`, "--since", "1h", "-q"}, 0, []string{"212 orders"}, nil},
		{"empty selector refused", []string{"query", `{app=~".*"}`}, 1, []string{"parse error"}, nil},
		{"metric query refused", []string{"query", `rate({app="shop-api"}[5m])`}, 1, []string{"stream selector"}, nil},
		{"labels", []string{"labels", "app"}, 0, []string{"shop-api", "shop-web"}, nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := run(testCase.args, &stdout, &stderr)
			output := stdout.String() + stderr.String()
			if exitCode != testCase.exitCode {
				t.Fatalf("exit %d, want %d\n%s", exitCode, testCase.exitCode, output)
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
