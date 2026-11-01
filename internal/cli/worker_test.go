package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

func TestValidateWorkerOptions(t *testing.T) {
	testCases := []struct {
		name    string
		options agentOptions
		want    string // substring of the error; empty means valid
	}{
		{"in-memory shell", agentOptions{worker: workerOptions{shell: true}}, ""},
		{"direct worker", agentOptions{directory: "d", worker: workerOptions{shell: true, direct: true, allowed: []string{"git"}, environment: []string{"HOME"}}}, ""},
		{"direct without dir", agentOptions{worker: workerOptions{direct: true}}, "--direct needs --dir"},
		{"direct with apply", agentOptions{directory: "d", apply: true, worker: workerOptions{direct: true}}, "--apply has nothing left"},
		{"allow without shell", agentOptions{directory: "d", worker: workerOptions{direct: true, allowed: []string{"git"}}}, "--allow needs --shell"},
		{"allow through an overlay", agentOptions{directory: "d", worker: workerOptions{shell: true, allowed: []string{"git"}}}, "--allow needs --direct"},
		{"env without allow", agentOptions{worker: workerOptions{shell: true, environment: []string{"HOME"}}}, "--env needs --allow"},
		// The review's S10: cd /dep ran the program in --dir/dep on disk.
		{"allow with a mount", agentOptions{directory: "d", mounts: []mountSpec{{}}, worker: workerOptions{shell: true, direct: true, allowed: []string{"git"}}}, "--allow cannot be combined with --mount"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateWorkerOptions(testCase.options)
			switch {
			case testCase.want == "" && err != nil:
				t.Fatalf("validateWorkerOptions = %v; want nil", err)
			case testCase.want != "" && (err == nil || !strings.Contains(err.Error(), testCase.want)):
				t.Fatalf("validateWorkerOptions = %v; want an error containing %q", err, testCase.want)
			}
		})
	}
}

// bashStub scripts one bash tool call, then a closing answer.
func bashStub(command string) *llmtest.StubClient {
	arguments, _ := json.Marshal(map[string]string{"command": command}) // a map of strings always encodes
	call := llm.ToolCall{ID: "c1", Name: "bash", Arguments: string(arguments)}
	return llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventToolCall, Tool: &call}, {Kind: llm.EventEnd}}},
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventTextDelta, Text: "done"}, {Kind: llm.EventEnd}}},
	)
}

func TestRunAgent_DirectWorkerRunsAllowedHostProgramsWithOnlyTheirEnvironment(t *testing.T) {
	printenv := "/usr/bin/printenv"
	if _, err := os.Stat(printenv); err != nil {
		t.Skip("no /usr/bin/printenv on this host")
	}
	t.Setenv("AIKIDO_TEST_SECRET", "must-not-leak")
	directory := t.TempDir()
	options := agentTestOptions()
	options.directory = directory
	options.worker = workerOptions{shell: true, direct: true, allowed: []string{"printenv=" + printenv}, environment: []string{"STAGE=staging"}}
	stub := bashStub(`printenv STAGE > stage.txt; printenv AIKIDO_TEST_SECRET > leak.txt; echo "exit $?" >> stage.txt`)
	var out bytes.Buffer
	if err := runAgent(context.Background(), options, &out, stub); err != nil {
		t.Fatalf("runAgent: %v\n%s", err, out.String())
	}
	if got := readDiskFile(t, directory, "stage.txt"); got != "staging\nexit 1\n" {
		t.Errorf("stage.txt = %q; want the --env value and printenv's failure for the unpassed variable", got)
	}
	if got := readDiskFile(t, directory, "leak.txt"); got != "" {
		t.Errorf("leak.txt = %q; a host variable reached the program", got)
	}
	if strings.Contains(out.String(), "dry run") {
		t.Errorf("a direct run printed a dry run line:\n%s", out.String())
	}
}

// TestRunAgent_DirectShellShowsTheDirectoryAtItsHostPath covers the worker
// benchmark's `git -C /repos/x` failure: with --direct the shell's paths are
// the host paths, so an absolute path from pwd works for a host program.
func TestRunAgent_DirectShellShowsTheDirectoryAtItsHostPath(t *testing.T) {
	list := "/bin/ls"
	if _, err := os.Stat(list); err != nil {
		t.Skip("no /bin/ls on this host")
	}
	directory := t.TempDir()
	options := agentTestOptions()
	options.directory = directory
	options.worker = workerOptions{shell: true, direct: true, allowed: []string{"hostls=" + list}}
	stub := bashStub(`echo hi > a.txt; pwd > where.txt; hostls "$PWD/a.txt" > listed.txt`)
	var out bytes.Buffer
	if err := runAgent(context.Background(), options, &out, stub); err != nil {
		t.Fatalf("runAgent: %v\n%s", err, out.String())
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := readDiskFile(t, directory, "where.txt"); got != absolute+"\n" {
		t.Errorf("pwd = %q; want the host path %q", got, absolute)
	}
	if got := readDiskFile(t, directory, "listed.txt"); got != absolute+"/a.txt\n" {
		t.Errorf("host ls of $PWD/a.txt = %q", got)
	}
}

func TestRunAgent_ShellOverAnOverlayStaysADryRun(t *testing.T) {
	directory := t.TempDir()
	writeDiskFile(t, directory, "hosts.txt", "api.old.example\n")
	options := agentTestOptions()
	options.directory = directory
	options.worker = workerOptions{shell: true}
	var out bytes.Buffer
	if err := runAgent(context.Background(), options, &out, bashStub(`echo api.new.example > hosts.txt`)); err != nil {
		t.Fatalf("runAgent: %v\n%s", err, out.String())
	}
	if got := readDiskFile(t, directory, "hosts.txt"); got != "api.old.example\n" {
		t.Errorf("the dry run changed hosts.txt: %q", got)
	}
	if !strings.Contains(out.String(), "modify hosts.txt") {
		t.Errorf("the change list lacks the shell's edit:\n%s", out.String())
	}
}

func TestSystemPrompt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "CLAUDE.md")
	writeDiskFile(t, filepath.Dir(file), "CLAUDE.md", "# worker rules\n")
	got, err := systemPrompt("preamble", file)
	if err != nil || got != "preamble\n\n# worker rules\n" {
		t.Fatalf("systemPrompt = %q, %v", got, err)
	}
	if got, err := systemPrompt("only flag", ""); err != nil || got != "only flag" {
		t.Fatalf("systemPrompt without a file = %q, %v", got, err)
	}
	if _, err := systemPrompt("x", filepath.Join(t.TempDir(), "missing.md")); err == nil {
		t.Fatal("a missing --system-file is no error")
	}
}
