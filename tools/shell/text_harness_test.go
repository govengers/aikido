package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/govengers/aikido/vfs/memory"
)

// textCase is one differential case: a script over files, with the output
// and exit status GNU produces. The expectations are literal so the tests
// run without GNU tools; TestTextCasesAgainstGNU re-checks them.
type textCase struct {
	name string
	// files are the workspace files, path to content.
	files map[string]string
	// stdin feeds the first command of the pipeline.
	stdin string
	// script is one pipeline of text commands with shell quoting, an
	// optional "< file" on the first command.
	script string
	stdout string
	stderr string
	exit   int
	// wantFiles, when set, are files that must hold this content afterwards.
	wantFiles map[string]string
	// gnuSkip names why the case is not compared against GNU (a documented
	// deviation, or a host tool other than GNU's is the reference).
	gnuSkip string
	// reference is the host binary to compare against instead of Debian's
	// GNU tools, e.g. /opt/homebrew/bin/jq.
	reference string
}

// allTextCases gathers the case tables of every text command.
func allTextCases() []textCase {
	return slices.Concat(headCases, tailCases, wcCases, sortCases, uniqCases, cutCases, trCases, grepCases, sedCases, diffCases, jqCases, awkCases)
}

func TestTextCases(t *testing.T) {
	seen := map[string]bool{}
	for _, testCase := range allTextCases() {
		if seen[testCase.name] {
			t.Fatalf("duplicate case name %q", testCase.name)
		}
		seen[testCase.name] = true
		t.Run(testCase.name, func(t *testing.T) {
			result := runTextCase(t, testCase)
			if result.stdout != testCase.stdout {
				t.Errorf("stdout:\n got %q\nwant %q", result.stdout, testCase.stdout)
			}
			if result.stderr != testCase.stderr {
				t.Errorf("stderr:\n got %q\nwant %q", result.stderr, testCase.stderr)
			}
			if result.exit != testCase.exit {
				t.Errorf("exit = %d, want %d", result.exit, testCase.exit)
			}
			for name, want := range testCase.wantFiles {
				content, _, err := result.storage.ReadFile(context.Background(), name)
				if err != nil || string(content) != want {
					t.Errorf("file %s = %q (%v), want %q", name, content, err, want)
				}
			}
		})
	}
}

// textResult is what a case's pipeline produced.
type textResult struct {
	storage        *memory.Storage
	stdout, stderr string
	exit           int
}

// runTextCase runs a case's pipeline over a memory storage.
func runTextCase(t *testing.T, testCase textCase) textResult {
	t.Helper()
	ctx := context.Background()
	storage := memory.New()
	for name, content := range testCase.files {
		if err := storage.WriteFile(ctx, name, []byte(content), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	stages, err := parsePipeline(testCase.script)
	if err != nil {
		t.Fatalf("parse %q: %v", testCase.script, err)
	}
	commands := map[string]Command{}
	for _, command := range textCommands() {
		commands[command.Name] = command
	}
	input := []byte(testCase.stdin)
	var stderr bytes.Buffer
	exit := 0
	for _, stage := range stages {
		if stage.input != "" {
			content, _, readErr := storage.ReadFile(ctx, stage.input)
			if readErr != nil {
				t.Fatalf("redirect %s: %v", stage.input, readErr)
			}
			input = content
		}
		command, ok := commands[stage.words[0]]
		if !ok {
			t.Fatalf("unknown command %q", stage.words[0])
		}
		var stdout bytes.Buffer
		invocation := &Invocation{
			Arguments: stage.words[1:], Stdin: bytes.NewReader(input), Stdout: &stdout, Stderr: &stderr,
			Storage: storage, Environment: []string{},
		}
		exit = exitCodeOf(&stderr, command.Name, command.Run(ctx, invocation))
		input = stdout.Bytes()
	}
	return textResult{storage: storage, stdout: string(input), stderr: stderr.String(), exit: exit}
}

// exitCodeOf maps Run's result the way the shell does.
func exitCodeOf(stderr *bytes.Buffer, name string, err error) int {
	var status ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &status):
		return int(status)
	default:
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
}

// pipelineStage is one command of a test pipeline.
type pipelineStage struct {
	words []string
	input string
}

// parsePipeline splits a script into commands at unquoted "|" and words with
// shell quoting ('...', "...", backslash); "< file" sets a stage's input.
func parsePipeline(script string) ([]pipelineStage, error) {
	var stages []pipelineStage
	var words []string
	var word strings.Builder
	inWord, redirect := false, false
	var input string
	flush := func() {
		if !inWord {
			return
		}
		if redirect {
			input, redirect = word.String(), false
		} else {
			words = append(words, word.String())
		}
		word.Reset()
		inWord = false
	}
	for index := 0; index < len(script); index++ {
		character := script[index]
		switch character {
		case ' ', '\t', '\n':
			flush()
		case '|':
			flush()
			stages = append(stages, pipelineStage{words: words, input: input})
			words, input = nil, ""
		case '<':
			flush()
			redirect = true
		case '\'':
			end := strings.IndexByte(script[index+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			word.WriteString(script[index+1 : index+1+end])
			index += end + 1
			inWord = true
		case '"':
			consumed, err := readDoubleQuoted(script[index+1:], &word)
			if err != nil {
				return nil, err
			}
			index += consumed + 1
			inWord = true
		case '\\':
			index++
			if index < len(script) {
				word.WriteByte(script[index])
			}
			inWord = true
		default:
			word.WriteByte(character)
			inWord = true
		}
	}
	flush()
	return append(stages, pipelineStage{words: words, input: input}), nil
}

// readDoubleQuoted reads up to the closing quote; a backslash escapes only
// $ ` " \ as in bash.
func readDoubleQuoted(text string, word *strings.Builder) (int, error) {
	for index := 0; index < len(text); index++ {
		switch text[index] {
		case '"':
			return index + 1, nil
		case '\\':
			if index+1 < len(text) && strings.IndexByte("$`\"\\", text[index+1]) >= 0 {
				index++
			}
		}
		word.WriteByte(text[index])
	}
	return 0, errors.New("unterminated double quote")
}

// TestTextCasesAgainstGNU runs every case in real bash with GNU tools
// (Debian 12 in Docker, or the host when its sed is GNU) and checks that the
// literal expectations still match. Skipped with -short or without GNU.
func TestTextCasesAgainstGNU(t *testing.T) {
	if testing.Short() {
		t.Skip("differential run against GNU skipped with -short")
	}
	runner := gnuRunner(t)
	directory := t.TempDir()
	var cases []textCase
	for _, testCase := range allTextCases() {
		if testCase.gnuSkip == "" && testCase.reference == "" {
			cases = append(cases, testCase)
		}
	}
	writeGNUCases(t, directory, cases)
	runner(t, directory)
	compareGNUResults(t, directory, cases)
	t.Logf("compared %d cases against GNU", len(cases))
}

// TestTextCasesAgainstHostReferences checks the cases whose reference is a
// host binary (jq) against it; a missing binary skips its cases.
func TestTextCasesAgainstHostReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("differential run against host references skipped with -short")
	}
	var cases []textCase
	for _, testCase := range allTextCases() {
		if testCase.gnuSkip == "" && testCase.reference != "" {
			cases = append(cases, testCase)
		}
	}
	checkHostReferences(t, cases)
}

// gnuRunner returns how to run the generated script: natively when the
// host's sed is GNU, else in Docker's debian:12; it skips the test otherwise.
func gnuRunner(t *testing.T) func(*testing.T, string) {
	t.Helper()
	if output, err := exec.CommandContext(t.Context(), "sed", "--version").Output(); err == nil && bytes.Contains(output, []byte("GNU")) {
		return func(t *testing.T, directory string) {
			t.Helper()
			runOrFail(t, exec.CommandContext(t.Context(), "bash", filepath.Join(directory, "run.sh")), directory)
		}
	}
	if os.Getenv(dockerReferenceVariable) == "" {
		t.Skipf("no GNU tools on the host; set %s=1 to compare in Docker", dockerReferenceVariable)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no GNU tools on the host and no docker")
	}
	image := gnuImage()
	if err := exec.CommandContext(t.Context(), "docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("docker image %s not present", image)
	}
	return func(t *testing.T, directory string) {
		t.Helper()
		runOrFail(t, exec.CommandContext(t.Context(), "docker", "run", "--rm", "--init", "-v", directory+":/w", "-w", "/w", image, "bash", "/w/run.sh"), directory)
	}
}

// gnuImage is the Docker image with GNU tools: debian:12 unless
// AIKIDO_GNU_IMAGE names another (golang:1.26 runs natively on arm64).
func gnuImage() string {
	if image := os.Getenv("AIKIDO_GNU_IMAGE"); image != "" {
		return image
	}
	return "debian:12"
}

func runOrFail(t *testing.T, command *exec.Cmd, directory string) {
	t.Helper()
	command.Env = append(os.Environ(), "W="+directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("GNU run failed: %v\n%s", err, output)
	}
}

// writeGNUCases lays out one directory per case plus run.sh, which runs
// every case's script in its directory with LC_ALL=C.
func writeGNUCases(t *testing.T, directory string, cases []textCase) {
	t.Helper()
	var script strings.Builder
	script.WriteString("export LC_ALL=C\nroot=$(cd \"$(dirname \"$0\")\" && pwd)\n")
	for index, testCase := range cases {
		caseDirectory := filepath.Join(directory, "cases", strconv.Itoa(index))
		writeFiles(t, caseDirectory, testCase.files)
		writeFiles(t, filepath.Join(directory, "io", strconv.Itoa(index)), map[string]string{
			"stdin": testCase.stdin, "script": testCase.script,
		})
		fmt.Fprintf(&script, "cd \"$root/cases/%d\"; io=\"$root/io/%d\"; cat \"$io/stdin\" | timeout 60 bash \"$io/script\" >\"$io/stdout\" 2>\"$io/stderr\"; echo $? >\"$io/exit\"\n", index, index)
	}
	writeFiles(t, directory, map[string]string{"run.sh": script.String()})
}

func writeFiles(t *testing.T, directory string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func compareGNUResults(t *testing.T, directory string, cases []textCase) {
	t.Helper()
	for index, testCase := range cases {
		results := filepath.Join(directory, "io", strconv.Itoa(index))
		stdout := readTestFile(t, filepath.Join(results, "stdout"))
		stderr := readTestFile(t, filepath.Join(results, "stderr"))
		exit, _ := strconv.Atoi(strings.TrimSpace(readTestFile(t, filepath.Join(results, "exit"))))
		reportDifference(t, testCase, stdout, stderr, exit)
		for name, want := range testCase.wantFiles {
			if got := readTestFile(t, filepath.Join(directory, "cases", strconv.Itoa(index), name)); got != want {
				t.Errorf("%s: GNU file %s = %q, case says %q", testCase.name, name, got, want)
			}
		}
	}
}

// checkHostReferences runs the cases whose reference is a host binary in a
// temporary directory with the binary first on PATH.
func checkHostReferences(t *testing.T, cases []textCase) {
	t.Helper()
	for _, testCase := range cases {
		if _, err := os.Stat(testCase.reference); err != nil {
			t.Logf("%s: reference %s missing, not checked", testCase.name, testCase.reference)
			continue
		}
		directory := t.TempDir()
		writeFiles(t, directory, testCase.files)
		binDirectory := t.TempDir()
		if err := os.Symlink(testCase.reference, filepath.Join(binDirectory, filepath.Base(testCase.reference))); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), "bash", "-c", testCase.script)
		command.Dir = directory
		command.Env = []string{"PATH=" + binDirectory + ":/usr/bin:/bin", "LC_ALL=C"}
		command.Stdin = strings.NewReader(testCase.stdin)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		exit := 0
		var exitError *exec.ExitError
		if err := command.Run(); errors.As(err, &exitError) {
			exit = exitError.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		reportDifference(t, testCase, stdout.String(), stderr.String(), exit)
	}
}

func reportDifference(t *testing.T, testCase textCase, stdout, stderr string, exit int) {
	t.Helper()
	if stdout != testCase.stdout || stderr != testCase.stderr || exit != testCase.exit {
		t.Errorf("%s: GNU differs from the case\n script %s\n stdout %q\n   case %q\n stderr %q\n   case %q\n exit %d, case %d",
			testCase.name, testCase.script, stdout, testCase.stdout, stderr, testCase.stderr, exit, testCase.exit)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // G304: paths are the test's own temp files
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// TestTextCommandsHonourCancelledContext checks that every command stops
// with the context's error instead of running on.
func TestTextCommandsHonourCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	storage := memory.New()
	scripts := map[string][]string{
		"grep": {"x", "a"}, "sed": {"p", "a"}, "head": {"a"}, "tail": {"a"}, "wc": {"a"}, "sort": {"a"},
		"uniq": {"a"}, "cut": {"-f1", "a"}, "diff": {"a", "b"}, "jq": {".", "a"}, "awk": {"{print}", "a"},
	}
	for _, command := range textCommands() {
		arguments, ok := scripts[command.Name]
		if !ok {
			continue
		}
		var output bytes.Buffer
		invocation := &Invocation{Arguments: arguments, Stdin: strings.NewReader(""), Stdout: &output, Stderr: &output, Storage: storage}
		if err := command.Run(ctx, invocation); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a cancelled context = %v (%q), want context.Canceled", command.Name, err, output.String())
		}
	}
}
