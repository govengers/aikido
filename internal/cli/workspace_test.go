package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

// editingStub scripts one turn of tool calls followed by a closing text turn.
func editingStub(calls ...llm.ToolCall) *llmtest.StubClient {
	var events []llm.Event
	for index := range calls {
		events = append(events, llm.Event{Kind: llm.EventToolCall, Tool: &calls[index]})
	}
	events = append(events, llm.Event{Kind: llm.EventEnd})
	return llmtest.NewStubClient(
		llmtest.TurnScript{Events: events},
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventTextDelta, Text: "done"}, {Kind: llm.EventEnd}}},
	)
}

func directoryEdits() []llm.ToolCall {
	return []llm.ToolCall{
		{ID: "c1", Name: "write_file", Arguments: `{"path":"notes.md","content":"new notes\n"}`},
		{ID: "c2", Name: "edit_file", Arguments: `{"path":"readme.md","old_string":"old line","new_string":"new line"}`},
		{ID: "c3", Name: "delete_file", Arguments: `{"path":"obsolete.md"}`},
	}
}

func newProjectDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	writeDiskFile(t, directory, "readme.md", "# Project\nold line\n")
	writeDiskFile(t, directory, "obsolete.md", "remove me\n")
	return directory
}

func TestRunAgent_DirectoryDryRunWritesNothing(t *testing.T) {
	directory := newProjectDirectory(t)
	var out bytes.Buffer
	options := agentTestOptions()
	options.directory = directory
	if err := runAgent(context.Background(), options, &out, editingStub(directoryEdits()...)); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"--- changes ---\ncreate notes.md\ndelete obsolete.md\nmodify readme.md\n",
		"--- /dev/null\n+++ b/notes.md\n@@ -0,0 +1,1 @@\n+new notes\n",
		"--- a/readme.md\n+++ b/readme.md\n@@ -1,2 +1,2 @@\n # Project\n-old line\n+new line\n",
		"--- a/obsolete.md\n+++ /dev/null\n",
		"dry run: nothing written",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "final VFS state") {
		t.Errorf("a directory run printed the in-memory state:\n%s", got)
	}
	if content := readDiskFile(t, directory, "readme.md"); content != "# Project\nold line\n" {
		t.Errorf("dry run changed readme.md: %q", content)
	}
	if _, err := os.Stat(filepath.Join(directory, "notes.md")); !os.IsNotExist(err) {
		t.Errorf("dry run created notes.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "obsolete.md")); err != nil {
		t.Errorf("dry run deleted obsolete.md: %v", err)
	}
}

func TestRunAgent_DirectoryApplyWrites(t *testing.T) {
	directory := newProjectDirectory(t)
	var out bytes.Buffer
	options := agentTestOptions()
	options.directory, options.apply = directory, true
	if err := runAgent(context.Background(), options, &out, editingStub(directoryEdits()...)); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if !strings.Contains(out.String(), "applied the changes") {
		t.Errorf("output lacks the apply line:\n%s", out.String())
	}
	if content := readDiskFile(t, directory, "readme.md"); content != "# Project\nnew line\n" {
		t.Errorf("readme.md = %q", content)
	}
	if content := readDiskFile(t, directory, "notes.md"); content != "new notes\n" {
		t.Errorf("notes.md = %q", content)
	}
	if _, err := os.Stat(filepath.Join(directory, "obsolete.md")); !os.IsNotExist(err) {
		t.Errorf("obsolete.md still exists: %v", err)
	}
}

func TestRunAgent_ApplyIsRefusedWhenTheRunDidNotFinish(t *testing.T) {
	directory := newProjectDirectory(t)
	var out bytes.Buffer
	options := agentTestOptions()
	options.directory, options.apply = directory, true
	write := llm.ToolCall{ID: "c1", Name: "write_file", Arguments: `{"path":"notes.md","content":"half done\n"}`}
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventToolCall, Tool: &write}, {Kind: llm.EventEnd}}},
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrServerError}, {Kind: llm.EventEnd}}},
	)
	err := runAgent(context.Background(), options, &out, stub)
	if err == nil || !strings.Contains(err.Error(), "--apply refused") {
		t.Fatalf("runAgent error = %v; want the apply refusal", err)
	}
	if !strings.Contains(out.String(), "not applied: the run ended with error") {
		t.Errorf("output lacks the refusal line:\n%s", out.String())
	}
	if _, statError := os.Stat(filepath.Join(directory, "notes.md")); !os.IsNotExist(statError) {
		t.Errorf("a failed run wrote notes.md: %v", statError)
	}
}

// Regression: a refused --apply returned success when the run left no
// changes.
func TestRunAgent_ApplyIsRefusedWithoutChanges(t *testing.T) {
	options := agentTestOptions()
	options.directory, options.apply = newProjectDirectory(t), true
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrServerError}, {Kind: llm.EventEnd}}})
	var out bytes.Buffer
	err := runAgent(context.Background(), options, &out, stub)
	if err == nil || !strings.Contains(err.Error(), "--apply refused") || !errors.Is(err, llm.ErrServerError) {
		t.Fatalf("runAgent error = %v; want the apply refusal and the run's error", err)
	}
	if got := out.String(); !strings.Contains(got, "--- no changes ---") || !strings.Contains(got, "not applied") {
		t.Errorf("output:\n%s", got)
	}
}

// Regression: a dry run that ended with an error exited zero.
func TestRunAgent_FailedDryRunIsAnError(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrServerError}, {Kind: llm.EventEnd}}})
	err := runAgent(context.Background(), agentTestOptions(), &bytes.Buffer{}, stub)
	if !errors.Is(err, llm.ErrServerError) || !strings.Contains(err.Error(), "the run ended with error") {
		t.Fatalf("runAgent error = %v; want the run's error", err)
	}
}

func TestRunFailure(t *testing.T) {
	cause := errors.New("boom")
	testCases := []struct {
		endReason agent.EndReason
		cause     error
		want      string // empty: no failure
	}{
		{agent.EndReasonStop, nil, ""},
		{agent.EndReasonMaximumTurns, nil, ""},
		{agent.EndReasonError, cause, "the run ended with error: boom"},
		{agent.EndReasonTimeout, nil, "the run ended with timeout"},
		{agent.EndReasonCancelled, context.Canceled, "the run ended with cancelled: context canceled"},
	}
	for _, testCase := range testCases {
		t.Run(string(testCase.endReason), func(t *testing.T) {
			err := runFailure(testCase.endReason, testCase.cause)
			switch {
			case testCase.want == "" && err != nil:
				t.Errorf("runFailure = %v; want nil", err)
			case testCase.want != "" && (err == nil || err.Error() != testCase.want):
				t.Errorf("runFailure = %v; want %q", err, testCase.want)
			case testCase.cause != nil && !errors.Is(err, testCase.cause):
				t.Errorf("runFailure = %v; want it to wrap %v", err, testCase.cause)
			}
		})
	}
}

func TestApplyDecision(t *testing.T) {
	testCases := []struct {
		name           string
		applyRequested bool
		endReason      agent.EndReason
		want           commitDecision
	}{
		{"dry run", false, agent.EndReasonError, commitDecision{}},
		{"finished run applies", true, agent.EndReasonStop, commitDecision{apply: true}},
		{"turn cap refuses", true, agent.EndReasonMaximumTurns, commitDecision{refusedReason: "max_turns"}},
		{"timeout refuses", true, agent.EndReasonTimeout, commitDecision{refusedReason: "timeout"}},
		{"cancel refuses", true, agent.EndReasonCancelled, commitDecision{refusedReason: "cancelled"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := applyDecision(testCase.applyRequested, testCase.endReason); got != testCase.want {
				t.Errorf("applyDecision = %+v; want %+v", got, testCase.want)
			}
		})
	}
}

func TestRunAgent_DirectoryWithoutChanges(t *testing.T) {
	directory := newProjectDirectory(t)
	var out bytes.Buffer
	options := agentTestOptions()
	options.directory, options.apply = directory, true
	stub := editingStub(llm.ToolCall{ID: "c1", Name: "read_file", Arguments: `{"path":"readme.md"}`})
	if err := runAgent(context.Background(), options, &out, stub); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "--- no changes ---") || strings.Contains(got, "applied") {
		t.Errorf("output:\n%s", got)
	}
}

func TestRunAgent_Mounts(t *testing.T) {
	docs, work := t.TempDir(), t.TempDir()
	writeDiskFile(t, docs, "style.md", "short sentences\n")
	var out bytes.Buffer
	options := agentTestOptions()
	options.apply = true
	options.mounts = []mountSpec{{name: "docs", directory: docs, readOnly: true}, {name: "work", directory: work}}
	stub := editingStub(
		llm.ToolCall{ID: "c1", Name: "read_file", Arguments: `{"path":"docs/style.md"}`},
		llm.ToolCall{ID: "c2", Name: "write_file", Arguments: `{"path":"docs/style.md","content":"anything"}`},
		llm.ToolCall{ID: "c3", Name: "write_file", Arguments: `{"path":"work/draft.md","content":"draft\n"}`},
		llm.ToolCall{ID: "c4", Name: "write_file", Arguments: `{"path":"scratch.md","content":"in memory"}`},
	)
	if err := runAgent(context.Background(), options, &out, stub); err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"[tool-result] read_file ok",
		"[tool-result] write_file fail",
		"--- changes ---\ncreate work/draft.md\n",
		"+++ b/work/draft.md",
		"--- final VFS state ---\n- scratch.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if content := readDiskFile(t, work, "draft.md"); content != "draft\n" {
		t.Errorf("work/draft.md = %q", content)
	}
	if content := readDiskFile(t, docs, "style.md"); content != "short sentences\n" {
		t.Errorf("read-only docs/style.md changed: %q", content)
	}
}

func TestRunAgent_ApplyNeedsADirectory(t *testing.T) {
	options := agentTestOptions()
	options.apply = true
	if err := runAgent(context.Background(), options, &bytes.Buffer{}, llmtest.NewStubClient()); err == nil {
		t.Error("runAgent with --apply and no directory: err = nil")
	}
}

func TestRunAgent_BadDirectoryOrMount(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	testCases := []struct {
		name   string
		modify func(options *agentOptions)
	}{
		{"missing --dir", func(options *agentOptions) { options.directory = missing }},
		{"missing --mount path", func(options *agentOptions) { options.mounts = []mountSpec{{name: "docs", directory: missing}} }},
		{"invalid --mount name", func(options *agentOptions) {
			options.mounts = []mountSpec{{name: "../docs", directory: t.TempDir()}}
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			options := agentTestOptions()
			testCase.modify(&options)
			if err := runAgent(context.Background(), options, &bytes.Buffer{}, llmtest.NewStubClient()); err == nil {
				t.Error("err = nil; want an error")
			}
		})
	}
}

func TestNewApp_AgentFlags(t *testing.T) {
	directory, docs := newProjectDirectory(t), t.TempDir()
	stub := editingStub(directoryEdits()...)
	app := NewApp(stubFactory(stub))
	var out bytes.Buffer
	app.Writer, app.ErrWriter = &out, &out
	arguments := []string{"aikido", "agent", "--dir", directory, "--mount", "docs=" + docs + ":ro", "--apply", "tidy up"}
	if err := app.Run(context.Background(), arguments); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if content := readDiskFile(t, directory, "notes.md"); content != "new notes\n" {
		t.Errorf("--apply did not write notes.md: %q", content)
	}
	if err := NewApp(stubFactory(stub)).Run(context.Background(), []string{"aikido", "agent", "--mount", "nopath", "x"}); err == nil {
		t.Error("malformed --mount: err = nil")
	}
}

func TestParseMountFlag(t *testing.T) {
	testCases := []struct {
		value   string
		want    mountSpec
		wantErr bool
	}{
		{value: "docs=../docs", want: mountSpec{name: "docs", directory: "../docs"}},
		{value: "docs=/srv/docs:ro", want: mountSpec{name: "docs", directory: "/srv/docs", readOnly: true}},
		{value: "a/b=dir=with=equals", want: mountSpec{name: "a/b", directory: "dir=with=equals"}},
		{value: "docs", wantErr: true},
		{value: "=dir", wantErr: true},
		{value: "docs=", wantErr: true},
		{value: "docs=:ro", wantErr: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.value, func(t *testing.T) {
			got, err := parseMountFlag(testCase.value)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("err = %v; wantErr %v", err, testCase.wantErr)
			}
			if got != testCase.want {
				t.Errorf("parseMountFlag = %+v; want %+v", got, testCase.want)
			}
		})
	}
}

func agentTestOptions() agentOptions {
	return agentOptions{model: "m", sessionID: "s1", prompt: "edit", maximumTokens: 100, temperature: -1, maximumTurns: 5, hideHidden: true}
}

func writeDiskFile(t *testing.T, directory, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readDiskFile(t *testing.T, directory, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directory, name)) //nolint:gosec // G304: the test reads its own temp files
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}
