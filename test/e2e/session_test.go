//go:build e2e

package e2e

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/agent"
	historymemory "github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
	vfsmemory "github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/osfs"
	"github.com/govengers/aikido/vfs/overlay"

	"go.uber.org/goleak"
)

const (
	sessionID    = "e2e-session"
	sessionModel = "e2e/session-model"
	// runTimeout bounds every run, so a run that never ends fails its test.
	runTimeout = 20 * time.Second
)

// newClient is the real OpenRouter client pointed at the fake.
func newClient(t *testing.T, fake *fakeOpenRouter) *openrouter.Client {
	t.Helper()
	client, err := openrouter.NewClient(&openrouter.Options{APIKey: e2eAPIKey, BaseURL: fake.baseURL()})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// newSession is a local session with the VFS tools over storage.
func newSession(t *testing.T, fake *fakeOpenRouter, storage vfs.Storage, history agent.History) *agent.Session {
	t.Helper()
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage}); err != nil {
		t.Fatal(err)
	}
	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID:           sessionID,
		Client:       newClient(t, fake),
		Tools:        registry,
		History:      history,
		Model:        sessionModel,
		SystemPrompt: "You edit files.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// runToEnd runs one turn and returns all its events; the last is EventEnd.
func runToEnd(t *testing.T, session *agent.Session, prompt string) []agent.Event {
	t.Helper()
	runContext, cancel := context.WithTimeout(t.Context(), runTimeout)
	defer cancel()
	events, err := session.Run(runContext, prompt)
	if err != nil {
		t.Fatal(err)
	}
	var collected []agent.Event
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

func endReason(events []agent.Event) agent.EndReason {
	if len(events) == 0 || events[len(events)-1].Kind != agent.EventEnd {
		return ""
	}
	return events[len(events)-1].EndReason
}

func TestSessionOverAnOverlayStagesEveryEditUntilCommit(t *testing.T) {
	fake := newFakeOpenRouter(t)
	fake.queue(
		streamedToolCalls(scriptedToolCall{id: "call_read", name: "read_file", arguments: `{"path":"a.md"}`}),
		streamedToolCalls(
			scriptedToolCall{id: "call_edit", name: "edit_file", arguments: `{"path":"a.md","old_string":"alpha","new_string":"ALPHA"}`},
			scriptedToolCall{id: "call_write", name: "write_file", arguments: `{"path":"c.md","content":"gamma\n"}`},
		),
		streamedToolCalls(scriptedToolCall{id: "call_delete", name: "delete_file", arguments: `{"path":"b.md"}`}),
		streamedText("Done."),
	)
	directory := t.TempDir()
	writeFiles(t, directory, map[string]string{"a.md": "alpha\n", "b.md": "beta\n"})
	disk, err := osfs.New(&osfs.Options{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	staged, err := overlay.New(&overlay.Options{Base: disk})
	if err != nil {
		t.Fatal(err)
	}
	history := historymemory.New()
	session := newSession(t, fake, staged, history)

	events := runToEnd(t, session, "Uppercase alpha, add c.md, drop b.md.")

	if reason := endReason(events); reason != agent.EndReasonStop {
		t.Fatalf("end reason = %q; want stop. events: %+v", reason, events)
	}
	assertDiskUnchanged(t, directory)
	assertStagedChanges(t, staged)
	if err := staged.Commit(t.Context()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	assertCommitted(t, directory)
	assertTurnInHistory(t, history)
	if result, ok := fake.chatRequests()[2].toolResult("call_write"); !ok || !strings.Contains(result, `"path":"c.md"`) {
		t.Errorf("write_file result sent to the model = %q (found %v)", result, ok)
	}
}

func assertDiskUnchanged(t *testing.T, directory string) {
	t.Helper()
	if content := readFile(t, filepath.Join(directory, "a.md")); content != "alpha\n" {
		t.Errorf("a.md on disk before Commit = %q; want it unchanged", content)
	}
	if matches, _ := filepath.Glob(filepath.Join(directory, "c.md")); len(matches) != 0 {
		t.Error("c.md exists on disk before Commit")
	}
}

func assertStagedChanges(t *testing.T, staged *overlay.Storage) {
	t.Helper()
	changes, err := staged.Changes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	type pathChange struct {
		path    string
		kind    overlay.ChangeKind
		content string
	}
	listed := make([]pathChange, 0, len(changes))
	for _, change := range changes {
		listed = append(listed, pathChange{path: change.Path, kind: change.Kind, content: string(change.Content)})
	}
	want := []pathChange{
		{path: "a.md", kind: overlay.ChangeModify, content: "ALPHA\n"},
		{path: "b.md", kind: overlay.ChangeDelete},
		{path: "c.md", kind: overlay.ChangeCreate, content: "gamma\n"},
	}
	if !slices.Equal(listed, want) {
		t.Errorf("Changes() = %+v; want %+v", listed, want)
	}
}

func assertCommitted(t *testing.T, directory string) {
	t.Helper()
	if content := readFile(t, filepath.Join(directory, "a.md")); content != "ALPHA\n" {
		t.Errorf("a.md after Commit = %q", content)
	}
	if content := readFile(t, filepath.Join(directory, "c.md")); content != "gamma\n" {
		t.Errorf("c.md after Commit = %q", content)
	}
	if matches, _ := filepath.Glob(filepath.Join(directory, "b.md")); len(matches) != 0 {
		t.Error("b.md still exists after Commit")
	}
}

// assertTurnInHistory checks that History holds the whole turn: the user
// message, every assistant reply with its tool calls, every tool result in
// order, and the final answer.
func assertTurnInHistory(t *testing.T, history agent.History) {
	t.Helper()
	messages, err := history.Read(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	type turnStep struct {
		role   llm.Role
		detail string // tool call ids of an assistant message, the answered id of a tool message, else the content
	}
	steps := make([]turnStep, 0, len(messages))
	for _, message := range messages {
		step := turnStep{role: message.Role, detail: message.Content}
		switch {
		case len(message.ToolCalls) > 0:
			callIDs := make([]string, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				callIDs = append(callIDs, call.ID)
			}
			step.detail = strings.Join(callIDs, ",")
		case message.Role == llm.RoleTool:
			step.detail = message.ToolCallID
		}
		steps = append(steps, step)
	}
	want := []turnStep{
		{llm.RoleUser, "Uppercase alpha, add c.md, drop b.md."},
		{llm.RoleAssistant, "call_read"},
		{llm.RoleTool, "call_read"},
		{llm.RoleAssistant, "call_edit,call_write"},
		{llm.RoleTool, "call_edit"},
		{llm.RoleTool, "call_write"},
		{llm.RoleAssistant, "call_delete"},
		{llm.RoleTool, "call_delete"},
		{llm.RoleAssistant, "Done."},
	}
	if !slices.Equal(steps, want) {
		t.Errorf("History = %+v;\nwant %+v", steps, want)
	}
}

func TestCancellingARunMidStreamReleasesTheSession(t *testing.T) {
	ignoreRunning := goleak.IgnoreCurrent()
	// Registered first, so it runs last: after the fake has shut down.
	t.Cleanup(func() { goleak.VerifyNone(t, ignoreRunning) })
	fake := newFakeOpenRouter(t)
	fake.queue(stalledText("Let me think"), streamedText("Second answer."))
	session := newSession(t, fake, vfsmemory.New(), historymemory.New())

	cancelled := cancelAfterFirstText(t, session)

	if reason := endReason(cancelled); reason != agent.EndReasonCancelled {
		t.Fatalf("cancelled run end reason = %q; want cancelled. events: %+v", reason, cancelled)
	}
	second := runToEnd(t, session, "Try again.")
	if reason := endReason(second); reason != agent.EndReasonStop {
		t.Fatalf("second run end reason = %q; want stop (the lock was released). events: %+v", reason, second)
	}
	if text := runText(second); text != "Second answer." {
		t.Errorf("second run text = %q", text)
	}
}

// cancelAfterFirstText starts a run, cancels its context as soon as the
// first text arrives and returns every event the run still delivered.
func cancelAfterFirstText(t *testing.T, session *agent.Session) []agent.Event {
	t.Helper()
	runContext, cancel := context.WithTimeout(t.Context(), runTimeout)
	defer cancel()
	events, err := session.Run(runContext, "Think out loud.")
	if err != nil {
		t.Fatal(err)
	}
	var collected []agent.Event
	for event := range events {
		collected = append(collected, event)
		if event.Kind == agent.EventText {
			cancel()
		}
	}
	return collected
}

func runText(events []agent.Event) string {
	var text strings.Builder
	for _, event := range events {
		if event.Kind == agent.EventText {
			text.WriteString(event.Text)
		}
	}
	return text.String()
}
