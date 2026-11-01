package shell

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/mount"
	"github.com/govengers/aikido/vfs/overlay"
)

// newTestShell returns a Shell over a memory Storage holding files.
func newTestShell(t *testing.T, files map[string]string, options *Options) (*Shell, *memory.Storage) {
	t.Helper()
	storage := memory.New()
	for name, content := range files {
		if err := storage.WriteFile(context.Background(), name, []byte(content), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	configured := Options{}
	if options != nil {
		configured = *options
	}
	configured.Storage = storage
	shell, err := New(&configured)
	if err != nil {
		t.Fatal(err)
	}
	return shell, storage
}

// mustRun runs script and fails the test on a Run error.
func mustRun(t *testing.T, shell *Shell, script string) Result {
	t.Helper()
	result, err := shell.Run(context.Background(), script)
	if err != nil {
		t.Fatalf("Run(%q): %v", script, err)
	}
	return result
}

func readStorage(t *testing.T, storage vfs.Storage, name string) string {
	t.Helper()
	content, _, err := storage.ReadFile(context.Background(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}

func TestNewValidates(t *testing.T) {
	run := func(context.Context, *Invocation) error { return nil }
	testCases := []struct {
		name    string
		options *Options
	}{
		{"nil options", nil},
		{"no storage", &Options{}},
		{"environment without equals", &Options{Storage: memory.New(), Environment: []string{"PATH"}}},
		{"command without name", &Options{Storage: memory.New(), Commands: []Command{{Summary: "x", Run: run}}}},
		{"command name with slash", &Options{Storage: memory.New(), Commands: []Command{{Name: "a/b", Summary: "x", Run: run}}}},
		{"shell builtin name", &Options{Storage: memory.New(), Commands: []Command{{Name: "echo", Summary: "x", Run: run}}}},
		{"command without summary", &Options{Storage: memory.New(), Commands: []Command{{Name: "x", Run: run}}}},
		{"command without run", &Options{Storage: memory.New(), Commands: []Command{{Name: "x", Summary: "x"}}}},
		{"command twice", &Options{Storage: memory.New(), Commands: []Command{{Name: "x", Summary: "x", Run: run}, {Name: "x", Summary: "y", Run: run}}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(testCase.options); err == nil {
				t.Fatal("New succeeded; want an error")
			}
		})
	}
}

func TestNewCopiesOptions(t *testing.T) {
	environment := []string{"A=1"}
	options := &Options{Storage: memory.New(), Environment: environment}
	shell, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	environment[0] = "A=2"
	if got := mustRun(t, shell, "echo $A").Output; got != "1\n" {
		t.Fatalf("output = %q; want the environment at New", got)
	}
	if options.Timeout != 0 || options.ToolName != "" {
		t.Fatal("New wrote defaults into the caller's options")
	}
}

func TestRunPersistsTheWorkingDirectoryNotVariables(t *testing.T) {
	shell, _ := newTestShell(t, map[string]string{"docs/a.md": "a\n"}, nil)
	mustRun(t, shell, "cd docs; x=1; export Y=2; greet() { echo hi; }")
	result := mustRun(t, shell, `pwd; cat a.md; echo "[$x][$Y]"; greet`)
	want := "/docs\na\n[][]\nbash: line 1: greet: command not found\n"
	if result.Output != want || result.ExitCode != 127 {
		t.Fatalf("second call = %q, exit %d; want %q, exit 127", result.Output, result.ExitCode, want)
	}
}

func TestMkdirDirectoriesPersistAcrossCalls(t *testing.T) {
	shell, storage := newTestShell(t, nil, nil)
	mustRun(t, shell, "mkdir -p build/out")
	result := mustRun(t, shell, "cd build/out && echo x > f.txt && ls -R /build")
	if result.Output != "/build:\nout\n\n/build/out:\nf.txt\n" {
		t.Fatalf("output = %q", result.Output)
	}
	if got := readStorage(t, storage, "build/out/f.txt"); got != "x\n" {
		t.Fatalf("f.txt = %q", got)
	}
}

func TestWorkingDirectoryRemovedBetweenCalls(t *testing.T) {
	shell, storage := newTestShell(t, map[string]string{"docs/a.md": "a\n"}, nil)
	mustRun(t, shell, "cd docs")
	if err := storage.DeleteFile(context.Background(), "docs/a.md"); err != nil {
		t.Fatal(err)
	}
	result := mustRun(t, shell, "pwd")
	if result.Output != "bash: the working directory /docs no longer exists; now in /\n/\n" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestTmpIsDiscardedAfterEachCall(t *testing.T) {
	shell, storage := newTestShell(t, nil, nil)
	if result := mustRun(t, shell, "echo x > /tmp/scratch && cat /tmp/scratch && cd /tmp && pwd"); result.Output != "x\n/tmp\n" {
		t.Fatalf("first call = %q", result.Output)
	}
	result := mustRun(t, shell, "pwd; cat /tmp/scratch")
	if result.Output != "/tmp\ncat: /tmp/scratch: No such file or directory\n" || result.ExitCode != 1 {
		t.Fatalf("second call = %q, exit %d", result.Output, result.ExitCode)
	}
	files, err := storage.ListFiles(context.Background())
	if err != nil || len(files) != 0 {
		t.Fatalf("workspace files = %v, %v; want none", files, err)
	}
}

func TestRunTimeout(t *testing.T) {
	shell, _ := newTestShell(t, nil, &Options{Timeout: 50 * time.Millisecond})
	started := time.Now()
	result := mustRun(t, shell, "echo started; sleep 10; echo never")
	if result.ExitCode != statusTimedOut || !strings.HasPrefix(result.Output, "started\nbash: timed out after 50ms") {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Run took %v after the timeout", elapsed)
	}
}

func TestRunTimeoutStopsALoop(t *testing.T) {
	shell, _ := newTestShell(t, nil, &Options{Timeout: 50 * time.Millisecond, MaximumOutputBytes: 100})
	result := mustRun(t, shell, "while true; do echo y; done")
	if result.ExitCode != statusTimedOut || !result.Truncated {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunReturnsTheCallersCancellation(t *testing.T) {
	shell, _ := newTestShell(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := shell.Run(ctx, "sleep 10"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v; want context.Canceled", err)
	}
}

func TestRunBoundsOutput(t *testing.T) {
	shell, _ := newTestShell(t, nil, &Options{MaximumOutputBytes: 40})
	result := mustRun(t, shell, "seq 1000")
	if !result.Truncated || !strings.HasPrefix(result.Output, "1\n2\n3\n") || !strings.HasSuffix(result.Output, "999\n1000\n") {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Output, "bytes of output cut") {
		t.Fatalf("output %q has no cut marker", result.Output)
	}
	unbounded, _ := newTestShell(t, nil, &Options{MaximumOutputBytes: -1})
	if result := mustRun(t, unbounded, "seq 10000"); result.Truncated || len(result.Output) != 48894 {
		t.Fatalf("unbounded output: %d bytes, truncated %v", len(result.Output), result.Truncated)
	}
}

func TestRunSyntaxErrorAndUnsupported(t *testing.T) {
	shell, _ := newTestShell(t, nil, nil)
	testCases := []struct {
		script string
		want   string
	}{
		{"echo (", "bash: 1:1: "},
		{"diff <(echo a) <(echo b)", "process substitution is not supported"},
		{"coproc cat", "coproc is not supported"},
	}
	for _, testCase := range testCases {
		result := mustRun(t, shell, testCase.script)
		if result.ExitCode != 2 || !strings.Contains(result.Output, testCase.want) {
			t.Errorf("Run(%q) = %q, exit %d; want exit 2 with %q", testCase.script, result.Output, result.ExitCode, testCase.want)
		}
	}
}

func TestRunIsSafeForConcurrentUse(t *testing.T) {
	shell, storage := newTestShell(t, nil, nil)
	var group sync.WaitGroup
	for index := range 8 {
		group.Go(func() {
			if _, err := shell.Run(context.Background(), "echo "+strings.Repeat("x", index)+" >> log.txt"); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if lines := strings.Count(readStorage(t, storage, "log.txt"), "\n"); lines != 8 {
		t.Fatalf("log.txt has %d lines; want 8", lines)
	}
}

func TestCustomCommand(t *testing.T) {
	upper := Command{
		Name:    "upper",
		Summary: "print the arguments in upper case",
		Run: func(_ context.Context, invocation *Invocation) error {
			_, err := invocation.Stdout.Write([]byte(strings.ToUpper(strings.Join(invocation.Arguments, " ")) + "\n"))
			if len(invocation.Arguments) == 0 {
				return ExitError(3)
			}
			return err
		},
	}
	failing := Command{Name: "fail", Summary: "fail", Run: func(context.Context, *Invocation) error { return errors.New("broken") }}
	panicking := Command{Name: "boom", Summary: "panic", Run: func(context.Context, *Invocation) error { panic("kaboom") }}
	replacedCat := Command{Name: "cat", Summary: "not cat", Run: func(_ context.Context, invocation *Invocation) error {
		_, err := invocation.Stdout.Write([]byte("replaced\n"))
		return err
	}}
	shell, _ := newTestShell(t, nil, &Options{Commands: []Command{upper, failing, panicking, replacedCat}})
	result := mustRun(t, shell, "upper a b | cat; upper; echo $?; fail; echo $?; boom; upper x | cat; echo $?")
	want := "replaced\n\n3\nfail: broken\n1\nboom: internal error: kaboom\nreplaced\n0\n"
	if result.Output != want {
		t.Fatalf("output = %q; want %q", result.Output, want)
	}
}

func TestCommandSeesItsInvocation(t *testing.T) {
	var seen Invocation
	probe := Command{Name: "probe", Summary: "record", Run: func(_ context.Context, invocation *Invocation) error {
		seen = *invocation
		return nil
	}}
	shell, _ := newTestShell(t, map[string]string{"d/x": ""}, &Options{Commands: []Command{probe}, Environment: []string{"TOKEN=t"}})
	mustRun(t, shell, "cd d; LOCAL=1; export SHARED=2; probe one 'two words' *")
	environment := strings.Join(seen.Environment, ",")
	switch {
	case strings.Join(seen.Arguments, "|") != "one|two words|x":
		t.Errorf("arguments = %q", seen.Arguments)
	case seen.Directory != "d":
		t.Errorf("directory = %q", seen.Directory)
	case !strings.Contains(environment, "TOKEN=t") || !strings.Contains(environment, "SHARED=2") || strings.Contains(environment, "LOCAL"):
		t.Errorf("environment = %q", seen.Environment)
	}
}

func TestRegister(t *testing.T) {
	registry := tools.NewRegistry()
	if err := Register(registry, &Options{Storage: memory.New(), ToolName: "shell"}); err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 1 || definitions[0].Name != "shell" {
		t.Fatalf("definitions = %+v", definitions)
	}
	for _, want := range []string{"Runs bash over the project workspace", "cat: print files", "xargs: ", "No network"} {
		if !strings.Contains(definitions[0].Description, want) {
			t.Errorf("description lacks %q:\n%s", want, definitions[0].Description)
		}
	}
	result, err := registry.Dispatch(context.Background(), llm.ToolCall{Name: "shell", Arguments: `{"command":"cd /tmp; echo hi; nope","description":"say hi"}`}, tools.Environment{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"output":"hi\nbash: line 1: nope: command not found\n","exit_code":127,"truncated":false}`
	if string(encoded) != want {
		t.Fatalf("content = %s; want %s", encoded, want)
	}
	if _, err := registry.Dispatch(context.Background(), llm.ToolCall{Name: "shell", Arguments: `{"command":5}`}, tools.Environment{}); !errors.Is(err, tools.ErrInvalidArguments) {
		t.Fatalf("number command: error %v; want ErrInvalidArguments", err)
	}
	if err := Register(registry, &Options{Storage: memory.New(), ToolName: "shell"}); !errors.Is(err, tools.ErrDuplicateTool) {
		t.Fatalf("second Register: %v; want ErrDuplicateTool", err)
	}
	if err := Register(nil, &Options{Storage: memory.New()}); err == nil {
		t.Fatal("Register(nil) succeeded")
	}
	if err := Register(registry, nil); err == nil {
		t.Fatal("Register without options succeeded")
	}
}

func TestOverOverlayAndReadOnlyMount(t *testing.T) {
	base := memory.New()
	if err := base.WriteFile(context.Background(), "guide.md", []byte("guide\n"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	reference, err := mount.New(&mount.Options{Mounts: []mount.Mount{
		{Prefix: "", Storage: memory.New()},
		{Prefix: "ref", Storage: base, ReadOnly: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	staged, err := overlay.New(&overlay.Options{Base: reference})
	if err != nil {
		t.Fatal(err)
	}
	shell, err := New(&Options{Storage: staged})
	if err != nil {
		t.Fatal(err)
	}
	result := mustRun(t, shell, "cat ref/guide.md > copy.md; ls")
	if result.Output != "copy.md\nref\n" {
		t.Fatalf("output = %q", result.Output)
	}
	changes, err := staged.Changes(context.Background())
	if err != nil || len(changes) != 1 || changes[0].Path != "copy.md" {
		t.Fatalf("changes = %+v, %v", changes, err)
	}
	if err := staged.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	direct, err := New(&Options{Storage: reference})
	if err != nil {
		t.Fatal(err)
	}
	if result := mustRun(t, direct, "echo x > ref/new.md; rm ref/guide.md"); !strings.Contains(result.Output, "bash: ref/new.md: Read-only file system") || !strings.Contains(result.Output, "rm: cannot remove 'ref/guide.md': Read-only file system") {
		t.Fatalf("read-only mount output = %q", result.Output)
	}
}
