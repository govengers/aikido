package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/govengers/aikido/vfs/osfs"
)

// TestScriptsCannotReachHostFiles runs a workspace on a directory next to a
// secret host file and checks that no spelling of a path reads, lists or
// writes outside the workspace.
func TestScriptsCannotReachHostFiles(t *testing.T) {
	host := t.TempDir()
	secret := filepath.Join(host, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(host, "workspace")
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		t.Fatal(err)
	}
	storage, err := osfs.New(&osfs.Options{Directory: workspace})
	if err != nil {
		t.Fatal(err)
	}
	shell, err := New(&Options{Storage: storage})
	if err != nil {
		t.Fatal(err)
	}
	scripts := []string{
		"cat " + secret,
		"cat ../secret.txt",
		"cat /../secret.txt",
		"cd .. && cat secret.txt",
		"cat < " + secret,
		"source " + secret,
		"while read -r line; do echo $line; done < ../secret.txt",
		"echo " + filepath.Dir(secret) + "/*",
		"ls -R / " + host,
		"find / " + host,
		"cp " + secret + " stolen.txt",
		"base64 /etc/passwd; cat /etc/hosts",
		"cd ~ && ls",
		"bash -c 'cat ../secret.txt'",
		"pwd -P",
	}
	for _, script := range scripts {
		result := mustRun(t, shell, script)
		if strings.Contains(result.Output, "TOP SECRET") || strings.Contains(result.Output, "secret.txt\n") || strings.Contains(result.Output, "root:") {
			t.Errorf("Run(%q) reached a host file:\n%s", script, result.Output)
		}
	}
	writes := []string{
		"echo owned > " + filepath.Join(host, "written.txt"),
		"echo owned > ../written.txt",
		"mkdir -p ../written.txt && touch ../written.txt",
		"echo owned | tee ../written.txt",
		"cd / && echo owned > ../written.txt",
	}
	for _, script := range writes {
		mustRun(t, shell, script)
		if _, err := os.Stat(filepath.Join(host, "written.txt")); err == nil {
			t.Fatalf("Run(%q) wrote a host file outside the workspace", script)
		}
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "stolen.txt" {
			if content, _ := os.ReadFile(filepath.Clean(filepath.Join(workspace, "stolen.txt"))); strings.Contains(string(content), "SECRET") {
				t.Fatal("cp copied the host secret into the workspace")
			}
		}
	}
}

// TestHostEnvironmentIsNotInherited checks that a script sees only the
// variables of Options.Environment.
func TestHostEnvironmentIsNotInherited(t *testing.T) {
	t.Setenv("AIKIDO_HOST_SECRET", "leaked")
	shell, _ := newTestShell(t, nil, &Options{Environment: []string{"VISIBLE=yes"}})
	result := mustRun(t, shell, `env; echo "[$AIKIDO_HOST_SECRET][$VISIBLE][$HOME]"`)
	if strings.Contains(result.Output, "leaked") || result.Output != "HOME=/\nVISIBLE=yes\n[][yes][/]\n" {
		t.Fatalf("output = %q", result.Output)
	}
}

// TestInterpreterTempPathsStayVirtual replays the review's FIFO bypass: the
// interpreter opens "<its temp dir>/sh-interp-*" on the host past the open
// handler, and TMPDIR picks that directory.
func TestInterpreterTempPathsStayVirtual(t *testing.T) {
	name := "sh-interp-aikido-" + strings.ReplaceAll(t.Name(), "/", "-")
	hostTemp := os.TempDir()
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join("/tmp", name))
		_ = os.Remove(filepath.Join(hostTemp, name))
	})
	shell, _ := newTestShell(t, nil, nil)
	script := "env TMPDIR=/tmp bash -c 'echo x >/tmp/" + name + "'; export TMPDIR=/tmp; echo y >>/tmp/" + name + "; cat /tmp/" + name +
		"; echo z 2>/dev/null > " + filepath.Join(hostTemp, name) + "; true"
	if result := mustRun(t, shell, script); result.Output != "x\ny\n" {
		t.Fatalf("output = %q", result.Output)
	}
	for _, host := range []string{filepath.Join("/tmp", name), filepath.Join(hostTemp, name)} {
		if _, err := os.Stat(host); !os.IsNotExist(err) {
			t.Fatalf("the script wrote the host file %s (%v)", host, err)
		}
	}
}

// TestInternalCommandsCannotBeForged replays the review's script: read puts
// a NUL byte in $REPLY, and calling it named an internal command that
// indexed missing arguments in a pipeline goroutine.
func TestInternalCommandsCannotBeForged(t *testing.T) {
	shell, _ := newTestShell(t, nil, nil)
	result := mustRun(t, shell, `printf '\0failure\n' | { read -r; "$REPLY"; } | cat; printf '\0lookup\n' | { read -r; "$REPLY" x; echo $?; }`)
	if want := "bash: line 1: : command not found\nbash: line 1: : command not found\n127\n"; result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
	}
}

// TestNegativeShiftIsRefused replays the review's "shift -1 & wait": the
// interpreter sliced the parameters with -1 in a background goroutine and
// the panic ended the host process.
func TestNegativeShiftIsRefused(t *testing.T) {
	shell, _ := newTestShell(t, nil, nil)
	result := mustRun(t, shell, "set -- a b; shift -1 & wait; builtin shift -2; command shift -3; echo $?; shift 1; echo $1")
	want := "bash: line 1: shift: -1: shift count out of range\n" +
		"bash: line 1: shift: -2: shift count out of range\nbash: line 1: shift: -3: shift count out of range\n1\nb\n"
	if result.Output != want {
		t.Fatalf("output = %q, want %q", result.Output, want)
	}
}
