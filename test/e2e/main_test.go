//go:build e2e

// Package e2e is aikido's end-to-end suite (TST-011): the real aikido CLI
// binary and the public library API against a fake OpenRouter that speaks the
// real wire format. No network, no API key, no Docker.
package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// e2eAPIKey is the key the CLI and the library clients send to the fake.
const e2eAPIKey = "sk-or-e2e"

// cliTimeout bounds one CLI run, so a hanging binary fails its test instead
// of the whole suite.
const cliTimeout = 30 * time.Second

// aikidoBinary is the CLI under test: E2E_BINARY (just e2e-build builds it
// reproducibly, so its hash is part of the skip key of TST-015), else built
// once by TestMain.
var aikidoBinary string

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

// runSuite returns the exit code, so the build directory is removed before
// os.Exit. Goroutine leaks fail the suite (TST-009).
func runSuite(m *testing.M) int {
	binary, cleanup, err := resolveBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer cleanup()
	aikidoBinary = binary

	code := m.Run()
	if code == 0 {
		if err := goleak.Find(); err != nil {
			fmt.Fprintln(os.Stderr, "e2e: leaked goroutines:", err)
			return 1
		}
	}
	return code
}

func resolveBinary() (binary string, cleanup func(), err error) {
	if binary := os.Getenv("E2E_BINARY"); binary != "" {
		if !filepath.IsAbs(binary) {
			return "", nil, fmt.Errorf("E2E_BINARY %q is not an absolute path", binary)
		}
		return binary, func() {}, nil
	}
	directory, err := os.MkdirTemp("", "aikido-e2e")
	if err != nil {
		return "", nil, fmt.Errorf("build directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(directory) } // temporary build output
	binary = filepath.Join(directory, "aikido")
	if err := buildCLI(binary); err != nil {
		cleanup()
		return "", nil, err
	}
	return binary, cleanup, nil
}

func buildCLI(output string) error {
	buildContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(buildContext, "go", "build", "-race", "-trimpath", "-buildvcs=false",
		"-o", output, "github.com/govengers/aikido/cmd/aikido")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("go build ./cmd/aikido: %w", err)
	}
	return nil
}

// cliResult is what one run of the CLI left behind.
type cliResult struct {
	exitCode int
	stdout   string
	stderr   string
}

// runCLI runs the aikido binary in a fresh working directory (no .env) with
// only PATH, HOME, GORACE and the fake's credentials in the environment, so
// nothing from the developer's shell leaks in.
func runCLI(t *testing.T, fake *fakeOpenRouter, arguments ...string) cliResult {
	t.Helper()
	runContext, cancel := context.WithTimeout(t.Context(), cliTimeout)
	defer cancel()
	workingDirectory := t.TempDir()
	command := exec.CommandContext(runContext, aikidoBinary, arguments...)
	command.Dir = workingDirectory
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + workingDirectory,
		"OPENROUTER_API_KEY=" + e2eAPIKey,
		"OPENROUTER_BASE_URL=" + fake.baseURL(),
		// The race runtime sleeps a second before exit by default; a race
		// still fails the run with exit code 66.
		"GORACE=atexit_sleep_ms=0",
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr

	err := command.Run()

	result := cliResult{stdout: stdout.String(), stderr: stderr.String()}
	if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
		result.exitCode = exitError.ExitCode()
		return result
	}
	if err != nil {
		t.Fatalf("run aikido %v: %v", arguments, err)
	}
	return result
}

// requireSuccess fails the test unless the CLI exited 0.
func (result cliResult) requireSuccess(t *testing.T) {
	t.Helper()
	if result.exitCode != 0 {
		t.Fatalf("aikido exited %d\nstdout:\n%s\nstderr:\n%s", result.exitCode, result.stdout, result.stderr)
	}
}

// writeFiles creates each path (relative to directory) with its content.
func writeFiles(t *testing.T, directory string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(directory, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// readFile returns a file's content, or fails the test.
func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // G304: the tests read only files under their own t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
