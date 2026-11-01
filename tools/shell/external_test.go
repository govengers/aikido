package shell

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// hostProgram returns the path of a host binary or skips the test.
func hostProgram(t *testing.T, candidates ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("host program tests need a Unix host")
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	t.Skipf("none of %v exists", candidates)
	return ""
}

// externalShell returns a Shell with the given host programs registered.
func externalShell(t *testing.T, options ...*ExternalOptions) *Shell {
	t.Helper()
	commands := []Command{}
	for _, external := range options {
		command, err := External(external)
		if err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	shell, _ := newTestShell(t, map[string]string{"input.txt": "from the workspace\n"}, &Options{Commands: commands})
	return shell
}

func TestExternalRunsAHostProgramInPipes(t *testing.T) {
	echo := hostProgram(t, "/bin/echo", "/usr/bin/echo")
	translate := hostProgram(t, "/usr/bin/tr", "/bin/tr")
	directory := t.TempDir()
	shell := externalShell(t,
		&ExternalOptions{Name: "hostecho", Path: echo, Directory: directory},
		&ExternalOptions{Name: "upper", Path: translate, Directory: directory, Summary: "upper-case stdin"},
	)
	result := mustRun(t, shell, "hostecho one two | upper a-z A-Z; cat input.txt | upper a-z A-Z > shout.txt; cat shout.txt")
	if result.Output != "ONE TWO\nFROM THE WORKSPACE\n" || result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(shell.description(), "upper: upper-case stdin") || !strings.Contains(shell.description(), "hostecho: host program "+echo) {
		t.Fatalf("description lacks the external summaries:\n%s", shell.description())
	}
}

func TestExternalEnvironmentIsIsolated(t *testing.T) {
	env := hostProgram(t, "/usr/bin/env")
	t.Setenv("AIKIDO_HOST_SECRET", "leaked")
	shell := externalShell(t, &ExternalOptions{Name: "hostenv", Path: env, Directory: t.TempDir(), Environment: []string{"ONLY=this"}})
	result := mustRun(t, shell, "export SCRIPT_VARIABLE=1; hostenv")
	if result.Output != "ONLY=this\n" {
		t.Fatalf("host env printed %q; want only the configured environment", result.Output)
	}
	empty := externalShell(t, &ExternalOptions{Name: "hostenv", Path: env, Directory: t.TempDir()})
	if result := mustRun(t, empty, "hostenv"); result.Output != "" {
		t.Fatalf("host env without Environment printed %q; want nothing", result.Output)
	}
}

func TestExternalDirectory(t *testing.T) {
	pwd := hostProgram(t, "/bin/pwd", "/usr/bin/pwd")
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	shell := externalShell(t, &ExternalOptions{Name: "hostpwd", Path: pwd, Directory: directory})
	if result := mustRun(t, shell, "cd /tmp && hostpwd -P"); result.Output != directory+"\n" {
		t.Fatalf("host pwd = %q; want %q", result.Output, directory)
	}
}

func TestExternalGitInATempDirectory(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	directory := t.TempDir()
	shell := externalShell(t, &ExternalOptions{
		Name: "git", Path: gitPath, Directory: directory,
		Environment: []string{"HOME=" + directory, "GIT_CONFIG_NOSYSTEM=1"},
		Allow: func(arguments []string) error {
			if len(arguments) > 0 && arguments[0] == "push" {
				return errors.New("push is not allowed")
			}
			return nil
		},
	})
	result := mustRun(t, shell, "git init -q . && git status --porcelain; git push origin main; echo $?")
	if !strings.Contains(result.Output, "git: push is not allowed\n1\n") {
		t.Fatalf("output = %q", result.Output)
	}
	if _, err := os.Stat(filepath.Join(directory, ".git")); err != nil {
		t.Fatalf("git init did not run in Directory: %v", err)
	}
}

func TestExternalAllowRefuses(t *testing.T) {
	echo := hostProgram(t, "/bin/echo", "/usr/bin/echo")
	var asked []string
	shell := externalShell(t, &ExternalOptions{
		Name: "kubectl", Path: echo, Directory: t.TempDir(),
		Allow: func(arguments []string) error {
			asked = arguments
			if len(arguments) > 0 && arguments[0] == "apply" {
				return errors.New("apply is not allowed, use diff")
			}
			return nil
		},
	})
	result := mustRun(t, shell, "kubectl apply -f x.yaml; echo $?; kubectl get pods")
	if result.Output != "kubectl: apply is not allowed, use diff\n1\nget pods\n" {
		t.Fatalf("output = %q", result.Output)
	}
	if strings.Join(asked, " ") != "get pods" {
		t.Fatalf("Allow saw %q", asked)
	}
}

func TestExternalExitStatus(t *testing.T) {
	sh := hostProgram(t, "/bin/sh")
	shell := externalShell(t, &ExternalOptions{Name: "hostsh", Path: sh, Directory: t.TempDir()})
	result := mustRun(t, shell, "hostsh -c 'echo out; echo err >&2; exit 5'; echo status $?; hostsh -c 'kill -TERM $$'; echo signal $?")
	if result.Output != "out\nerr\nstatus 5\nsignal 143\n" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestExternalTimeoutKillsTheProcessGroup(t *testing.T) {
	sh := hostProgram(t, "/bin/sh")
	shell := externalShell(t, &ExternalOptions{Name: "slow", Path: sh, Directory: t.TempDir(), Timeout: 100 * time.Millisecond})
	started := time.Now()
	// The child sleep holds stdout; only killing the group ends the run.
	result := mustRun(t, shell, "slow -c 'sleep 30 & sleep 30; echo never'; echo status $?")
	if result.Output != "slow: killed after the 100ms timeout\nstatus 124\n" {
		t.Fatalf("output = %q", result.Output)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("run took %v", elapsed)
	}
}

func TestExternalStopsWithTheShellTimeout(t *testing.T) {
	sleep := hostProgram(t, "/bin/sleep", "/usr/bin/sleep")
	command, err := External(&ExternalOptions{Name: "hostsleep", Path: sleep, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	shell, _ := newTestShell(t, nil, &Options{Commands: []Command{command}, Timeout: 100 * time.Millisecond})
	started := time.Now()
	if result := mustRun(t, shell, "hostsleep 30"); result.ExitCode != statusTimedOut {
		t.Fatalf("result = %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("run took %v", elapsed)
	}
}

func TestExternalValidates(t *testing.T) {
	echo := hostProgram(t, "/bin/echo", "/usr/bin/echo")
	directory := t.TempDir()
	testCases := []struct {
		name    string
		options *ExternalOptions
	}{
		{"nil", nil},
		{"no name", &ExternalOptions{Path: echo, Directory: directory}},
		{"name with space", &ExternalOptions{Name: "a b", Path: echo, Directory: directory}},
		{"relative path", &ExternalOptions{Name: "x", Path: "echo", Directory: directory}},
		{"missing path", &ExternalOptions{Name: "x", Path: "/nonexistent/echo", Directory: directory}},
		{"path is a directory", &ExternalOptions{Name: "x", Path: directory, Directory: directory}},
		{"no directory", &ExternalOptions{Name: "x", Path: echo}},
		{"directory is a file", &ExternalOptions{Name: "x", Path: echo, Directory: echo}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := External(testCase.options); err == nil {
				t.Fatal("External succeeded; want an error")
			}
		})
	}
}
