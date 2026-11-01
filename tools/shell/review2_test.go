package shell

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
)

// isolatedScriptVariable carries the script runIsolated runs in a child
// test process; with isolatedPanicVariable set, the child's Storage panics
// on reading "panic".
const (
	isolatedScriptVariable = "AIKIDO_SHELL_ISOLATED_SCRIPT"
	isolatedPanicVariable  = "AIKIDO_SHELL_ISOLATED_PANIC"
)

// panickingStorage panics when "panic" is read, like a buggy Storage.
type panickingStorage struct {
	vfs.Storage
}

func (p panickingStorage) ReadFile(ctx context.Context, name string) ([]byte, vfs.FileMetadata, error) {
	if name == "panic" {
		panic("storage bug")
	}
	return p.Storage.ReadFile(ctx, name) //nolint:wrapcheck // a test double
}

// runIsolated runs script in a fresh test process: a panic in an
// interpreter goroutine ends that process, not this one, and fails the
// test with the child's output.
func runIsolated(t *testing.T, script string, environment ...string) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	//nolint:gosec // G204: the test binary re-executes itself
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIsolatedScriptHelper$", "-test.count=1")
	command.Env = append(append(os.Environ(), environment...), isolatedScriptVariable+"="+script)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("the child process running %q failed: %v\n%s", script, err, output)
	}
	line, _, _ := strings.Cut(string(output), "\n")
	var result Result
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		t.Fatalf("child output %q: %v", output, err)
	}
	return result
}

// TestIsolatedScriptHelper is the child of runIsolated; without its
// variable it does nothing.
func TestIsolatedScriptHelper(t *testing.T) {
	script, ok := os.LookupEnv(isolatedScriptVariable)
	if !ok {
		t.Skip("runs only as the child of runIsolated")
	}
	options := &Options{Storage: memory.New(), Timeout: 10 * time.Second}
	if os.Getenv(isolatedPanicVariable) != "" {
		options.Storage = panickingStorage{options.Storage}
	}
	shell, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(mustRun(t, shell, script))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
}

// TestWrappedBuiltinsAreRewritten replays the review's wrapper bypasses:
// every way to reach a builtin through command, builtin or exec gets the
// rewrite a direct call gets (pwd -P resolved host symlinks; type and
// command -v searched the host PATH).
func TestWrappedBuiltinsAreRewritten(t *testing.T) {
	testCases := []struct{ script, want string }{
		{"cd /tmp; command -- pwd -P", "/tmp\n"},
		{"cd /tmp; command -p pwd -P", "/tmp\n"},
		{"cd /tmp; builtin pwd -P", "/tmp\n"},
		{"cd /tmp; builtin -- pwd -P", "/tmp\n"},
		{"cd /tmp; command command pwd -P", "/tmp\n"},
		{"cd /tmp; command builtin pwd -P", "/tmp\n"},
		{"cd /tmp; builtin command -p -- pwd -P", "/tmp\n"},
		{"cd /tmp; (exec -- pwd -P); echo $?", "/tmp\n0\n"},
		{"PATH=/usr/bin:/bin; command -vv curl ls; echo $?", "ls\n1\n"},
		{"PATH=/usr/bin:/bin; command -pv sh; command -vV ls; command -v -- ls", "sh\nls is a shell builtin\nls\n"},
		{"command -x ls; echo $?", "bash: line 1: command: -x: invalid option\ncommand: usage: command [-pVv] command [arg ...]\n2\n"},
		{"type -- /bin/sh; echo $?", "bash: line 1: type: /bin/sh: not found\n1\n"},
		{"PATH=/usr/bin:/bin; type -- python3; echo $?", "bash: line 1: type: python3: not found\n1\n"},
		{"type -a -P ls; echo $?; type -tP ls; echo $?; type -t -P ls; echo $?; type -Pt ls", "1\n1\n1\nbuiltin\n"},
		{"f() { :; }; type -t f; type -ft f; echo $?; type -a ls", "function\n1\nls is a shell builtin\n"},
		{"type -x ls; echo $?", "bash: line 1: type: -x: invalid option\ntype: usage: type [-afptP] name [name ...]\n2\n"},
		{"builtin cat x; echo $?", "bash: line 1: builtin: cat: not a shell builtin\n1\n"},
		{"command; builtin; command -p; echo $?", "0\n"},
		{"cat() { echo function; }; command cat </dev/null; builtin echo x", "x\n"},
		{`printf '\0failure\n' | { read -r; command -- "$REPLY" forged 0; echo $?; }`, "bash: line 1: : command not found\n127\n"},
	}
	shell, _ := newTestShell(t, nil, nil)
	for _, testCase := range testCases {
		if result := mustRun(t, shell, testCase.script); result.Output != testCase.want {
			t.Errorf("Run(%q) = %q, want %q", testCase.script, result.Output, testCase.want)
		}
	}
}

// TestWrappedNegativeShiftDoesNotCrash replays the review's "command --
// shift -1 & wait": the interpreter consumed "--" and sliced the parameters
// with -1 in a background goroutine, ending the host process.
func TestWrappedNegativeShiftDoesNotCrash(t *testing.T) {
	script := "set -- a b; command -- shift -1 & wait; builtin -- shift -1 & wait; command command -p shift -1 & wait; (exec shift -1) & wait; echo $1"
	refused := "bash: line 1: shift: -1: shift count out of range\n"
	if result := runIsolated(t, script); result.Output != refused+refused+refused+refused+"a\n" {
		t.Fatalf("output = %q", result.Output)
	}
}
