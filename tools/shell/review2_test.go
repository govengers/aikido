package shell

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/osfs"
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

// TestHandlerPanicsDoNotCrash checks that a panic in a handler (here a buggy
// Storage read by a redirect in a background job and in a pipeline) stops
// that job or the script instead of ending the host process. The
// interpreter drops a background job's error, so only the pipeline reports.
func TestHandlerPanicsDoNotCrash(t *testing.T) {
	if result := runIsolated(t, "{ read x <panic; } & wait; echo after", isolatedPanicVariable+"=1"); result.Output != "after\n" {
		t.Errorf("background job: %+v", result)
	}
	result := runIsolated(t, "cat <panic | cat", isolatedPanicVariable+"=1")
	if result.ExitCode == 0 || !strings.Contains(result.Output, "internal error: storage bug") {
		t.Errorf("pipeline: %+v", result)
	}
}

// TestHiddenTargetsOfLinks replays the review's "public -> .env": with
// HideHiddenPaths over an osfs that follows links, cat public read .env and a
// link into .git took writes. RefuseSymlinks closes both.
func TestHiddenTargetsOfLinks(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, ".git", "hooks"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", filepath.Join(directory, "public")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(".git", filepath.Join(directory, "pub")); err != nil {
		t.Fatal(err)
	}
	storage, err := osfs.New(&osfs.Options{Directory: directory, RefuseSymlinks: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	shell, err := New(&Options{Storage: storage, HideHiddenPaths: true})
	if err != nil {
		t.Fatal(err)
	}
	result := mustRun(t, shell, "cat public; echo hook > pub/hooks/pre-commit; ls")
	want := "cat: public: No such file or directory\nbash: line 1: pub/hooks/pre-commit: No such file or directory\n"
	if result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
	}
	if _, err := os.Stat(filepath.Join(directory, ".git", "hooks", "pre-commit")); err == nil {
		t.Fatal("the script wrote .git/hooks/pre-commit through pub")
	}
}

// TestRedirectErrorsCarryTheLine checks bash's "bash: line N: " in front of
// a failed redirect (CI against GNU bash: "bash: line 1: r/: Is a
// directory"), on the redirect's own line, and that a leading ~ still
// expands.
func TestRedirectErrorsCarryTheLine(t *testing.T) {
	shell, _ := newTestShell(t, nil, nil)
	script := "echo x > r/\nf=out\n{\n  cat <missing\n} 2>/dev/null; cat <missing\nbash -c 'echo y >/nope/x'\necho z > ~/f; cat ~/f"
	want := "bash: line 1: r/: Is a directory\nbash: line 5: missing: No such file or directory\n" +
		"bash: line 1: /nope/x: No such file or directory\nz\n"
	if result := mustRun(t, shell, script); result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
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
