package shell

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/govengers/aikido/vfs/memory"
)

// differentialCase is one script that must behave the same in this shell
// (over a memory Storage) and in GNU bash with the GNU tools (over a
// directory with the same files): same stdout, stderr, exit status and
// resulting files.
type differentialCase struct {
	name   string
	files  map[string]string // workspace files before the run, path to content
	script string
	// ignoreStderr skips the stderr comparison where the interpreter's own
	// wording differs from bash's; stdout, status and files still count.
	ignoreStderr bool
	// requires names commands the script needs besides the shell's; the
	// case is skipped while one is not built in (sort, grep, wc, ...).
	requires []string
}

// differentialResult is what one side produced.
type differentialResult struct {
	stdout, stderr string
	status         int
	files          map[string]string
}

// differentialBackend runs scripts in GNU bash: the host bash when the host
// has GNU tools (Linux CI), else a container.
type differentialBackend struct {
	kind  backendKind
	image string // for backendDocker
	bash  string // for backendHost
}

type backendKind int

const (
	backendNone backendKind = iota
	backendHost
	backendDocker
)

func (b differentialBackend) String() string {
	switch b.kind {
	case backendHost:
		return "host " + b.bash
	case backendDocker:
		return "docker " + b.image
	case backendNone:
	}
	return "none (skipped)"
}

// differentialImages have GNU bash, coreutils, findutils, grep, sed and
// diffutils; the first that runs is used. golang:1.26 comes first because it
// is native on arm64 hosts, where debian:12 may only exist as amd64 and run
// emulated.
var differentialImages = []string{"golang:1.26", "debian:12"}

// dockerProbeTimeout bounds the check that docker can start a container at
// all; a wedged daemon accepts the request and never starts it.
const dockerProbeTimeout = 15 * time.Second

// referenceTimeout bounds one batch of reference runs.
const referenceTimeout = 5 * time.Minute

// dockerReferenceVariable opts into the GNU reference in Docker when the host
// has no GNU tools (macOS). Off by default: a wedged Docker daemon would stall
// every test run, and CI on Linux has GNU tools natively.
const dockerReferenceVariable = "AIKIDO_GNU_DOCKER"

// differentialBashVariable names a GNU bash to use as the host reference
// even without GNU tools (macOS with Homebrew bash): only meaningful for the
// shell-semantics cases, the file commands are then the host's BSD ones.
const differentialBashVariable = "AIKIDO_DIFFERENTIAL_BASH"

var detectBackend = sync.OnceValue(func() differentialBackend {
	if bash := os.Getenv(differentialBashVariable); bash != "" {
		return differentialBackend{kind: backendHost, bash: bash}
	}
	if output, err := exec.Command("sed", "--version").CombinedOutput(); err == nil && bytes.Contains(output, []byte("GNU")) {
		if bash, err := exec.LookPath("bash"); err == nil {
			return differentialBackend{kind: backendHost, bash: bash}
		}
	}
	if os.Getenv(dockerReferenceVariable) == "" {
		return differentialBackend{kind: backendNone}
	}
	for _, image := range differentialImages {
		if exec.Command("docker", "image", "inspect", image).Run() != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), dockerProbeTimeout)
		err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", image, "true").Run()
		wedged := ctx.Err() != nil
		cancel()
		switch {
		case err == nil:
			return differentialBackend{kind: backendDocker, image: image}
		case wedged:
			// The daemon accepted the container and never started it; the
			// next image would wait just as long.
			return differentialBackend{kind: backendNone}
		}
	}
	return differentialBackend{kind: backendNone}
})

// runDifferential runs every case in both shells and reports each
// difference. It skips when there is no GNU reference: no GNU sed on the
// host and no docker that starts one of differentialImages.
func runDifferential(t *testing.T, cases []differentialCase) {
	t.Helper()
	backend := detectBackend()
	if backend.kind == backendNone {
		t.Skipf("no GNU reference: needs GNU sed on the host or docker running one of %v", differentialImages)
	}
	root := t.TempDir()
	for index, testCase := range cases {
		writeCaseFiles(t, filepath.Join(root, strconv.Itoa(index)), testCase)
	}
	runReference(t, backend, root, len(cases))
	builtins := map[string]bool{}
	for _, command := range Builtins() {
		builtins[command.Name] = true
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, name := range testCase.requires {
				if !builtins[name] {
					t.Skipf("needs %s, which is not built in yet", name)
				}
			}
			caseDirectory := filepath.Join(root, strconv.Itoa(index))
			want := readReference(t, caseDirectory, backend)
			got := runOurs(t, testCase)
			compareResults(t, testCase, got, want)
		})
	}
}

func writeCaseFiles(t *testing.T, directory string, testCase differentialCase) {
	t.Helper()
	files := filepath.Join(directory, "files")
	if err := os.MkdirAll(files, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range testCase.files {
		target := filepath.Join(files, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "script.sh"), []byte(testCase.script), 0o600); err != nil {
		t.Fatal(err)
	}
}

// referenceDriver runs case directories 0..count-1 under root: each script
// in its files directory, recording stdout, stderr and the status next to
// it. Scripts name their /tmp files /tmp/differential-*, which the driver
// removes before each case.
func referenceDriver(root string, count int) string {
	return fmt.Sprintf(`export LC_ALL=C HOME=/ PATH="$(dirname "$BASH"):$PATH"
for index in $(seq 0 %d); do
  rm -rf /tmp/differential-*
  cd "%s/$index/files" || exit 1
  bash -c "$(cat ../script.sh)" > ../stdout 2> ../stderr < /dev/null
  echo $? > ../status
done
`, count-1, root)
}

func runReference(t *testing.T, backend differentialBackend, root string, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), referenceTimeout)
	defer cancel()
	var command *exec.Cmd
	switch backend.kind {
	case backendHost:
		command = exec.CommandContext(ctx, backend.bash, "-c", referenceDriver(root, count))
	case backendDocker:
		user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
		command = exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--user", user,
			"-v", root+":/w", backend.image, "bash", "-c", referenceDriver("/w", count))
	case backendNone:
		t.Fatal("no backend")
	}
	output, err := command.CombinedOutput()
	switch {
	case ctx.Err() != nil && backend.kind == backendDocker:
		t.Skipf("docker did not finish the reference run within %v", referenceTimeout)
	case err != nil:
		t.Fatalf("reference run failed: %v\n%s", err, output)
	}
}

func readReference(t *testing.T, directory string, backend differentialBackend) differentialResult {
	t.Helper()
	read := func(name string) string {
		content, err := os.ReadFile(filepath.Clean(filepath.Join(directory, name)))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	status, err := strconv.Atoi(strings.TrimSpace(read("status")))
	if err != nil {
		t.Fatal(err)
	}
	// The reference ran in a real directory; pwd and realpath print it.
	// Map it back to the workspace root the virtual shell calls "/".
	filesDirectory := filepath.Join(directory, "files")
	if backend.kind == backendDocker {
		filesDirectory = "/w/" + filepath.Base(directory) + "/files"
	}
	normalize := func(text string) string {
		text = strings.ReplaceAll(text, filesDirectory+"/", "/")
		return strings.ReplaceAll(text, filesDirectory, "/")
	}
	return differentialResult{
		stdout: normalize(read("stdout")),
		stderr: normalize(read("stderr")),
		status: status,
		files:  readTree(t, filepath.Join(directory, "files")),
	}
}

func readTree(t *testing.T, directory string) map[string]string {
	t.Helper()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files := map[string]string{}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := fs.ReadFile(root.FS(), name)
		files[name] = string(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func runOurs(t *testing.T, testCase differentialCase) differentialResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	storage := memory.New()
	for name, content := range testCase.files {
		if err := storage.WriteFile(ctx, name, []byte(content), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	shell, err := New(&Options{Storage: storage})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	status, err := shell.run(ctx, testCase.script, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	files := map[string]string{}
	listed, err := storage.ListFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range listed {
		content, _, err := storage.ReadFile(ctx, file.Path)
		if err != nil {
			t.Fatal(err)
		}
		files[file.Path] = string(content)
	}
	return differentialResult{stdout: stdout.String(), stderr: stderr.String(), status: status, files: files}
}

func compareResults(t *testing.T, testCase differentialCase, got, want differentialResult) {
	t.Helper()
	if got.stdout != want.stdout {
		t.Errorf("stdout differs for %q\n--- ours\n%s--- GNU\n%s", testCase.script, got.stdout, want.stdout)
	}
	if !testCase.ignoreStderr && got.stderr != want.stderr {
		t.Errorf("stderr differs for %q\n--- ours\n%s--- GNU\n%s", testCase.script, got.stderr, want.stderr)
	}
	if got.status != want.status {
		t.Errorf("exit status for %q = %d, GNU %d (stderr ours %q, GNU %q)", testCase.script, got.status, want.status, got.stderr, want.stderr)
	}
	names := []string{}
	for name := range got.files {
		names = append(names, name)
	}
	for name := range want.files {
		if _, ok := got.files[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		gotContent, inOurs := got.files[name]
		wantContent, inGNU := want.files[name]
		if inOurs != inGNU || gotContent != wantContent {
			t.Errorf("file %s for %q: ours (exists %v) %q, GNU (exists %v) %q", name, testCase.script, inOurs, gotContent, inGNU, wantContent)
		}
	}
}

// TestDifferentialBackend reports which reference the differential tests
// use, so a skipped comparison is visible in the test log.
func TestDifferentialBackend(t *testing.T) {
	t.Logf("differential reference: %v on %s", detectBackend(), runtime.GOOS)
}
