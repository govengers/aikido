package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	vfsmem "github.com/govengers/aikido/vfs/memory"
)

func TestReadProjectInstructions(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "AGENTS.md", "Read CLAUDE.md.\n")
	mustWriteFile(t, storage, "CLAUDE.md", "# Routing\n\n| incident | workbooks/incident |\n")

	text, err := ReadProjectInstructions(context.Background(), storage, nil)
	if err != nil {
		t.Fatalf("ReadProjectInstructions: %v", err)
	}
	want := "Project instructions from AGENTS.md:\n\nRead CLAUDE.md.\n\nProject instructions from CLAUDE.md:\n\n# Routing\n\n| incident | workbooks/incident |"
	if text != want {
		t.Errorf("text = %q; want %q", text, want)
	}
	only, err := ReadProjectInstructions(context.Background(), storage, &ProjectInstructionsOptions{Names: []string{"MISSING.md", "CLAUDE.md"}, MaximumBytes: 140})
	if err != nil {
		t.Fatalf("ReadProjectInstructions: %v", err)
	}
	if len(only) > 140 || !strings.HasPrefix(only, "Project instructions from CLAUDE.md") {
		t.Errorf("text = %q; want CLAUDE.md only, cut to 140 bytes", only)
	}
	none, err := ReadProjectInstructions(context.Background(), vfsmem.New(), nil)
	if err != nil || none != "" {
		t.Errorf("empty storage = %q, %v; want empty", none, err)
	}
	if _, err := ReadProjectInstructions(context.Background(), nil, nil); err == nil {
		t.Error("nil storage: want an error")
	}
}

func TestReadProjectInstructions_MissingIsErrorForNamedFiles(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "CLAUDE.md", "rules")
	if _, err := ReadProjectInstructions(context.Background(), storage, &ProjectInstructionsOptions{Names: []string{"CLAUDE.md", "docs/AGENTS.md"}, MissingIsError: true}); err == nil {
		t.Error("a missing named file: want an error")
	}
	text, err := ReadProjectInstructions(context.Background(), storage, &ProjectInstructionsOptions{Names: []string{"docs/AGENTS.md", "CLAUDE.md"}})
	if err != nil || text != "Project instructions from CLAUDE.md:\n\nrules" {
		t.Errorf("text = %q, %v; want CLAUDE.md, the missing one skipped", text, err)
	}
}

func TestRun_InstructionsOpenOnlyTheFirstMessage(t *testing.T) {
	stub := llmtest.NewStubClient(textTurn("one"), textTurn("two"))
	history := memory.New()
	session := newTestSession(t, stub, nil, history, func(options *SessionOptions) { options.Instructions = "Read CLAUDE.md first." })

	runToEnd(t, session, "first task")
	runToEnd(t, session, "second task")

	stored := storedMessages(t, history, "s-test")
	if stored[0].Content != "Read CLAUDE.md first.\n\nfirst task" {
		t.Errorf("first message = %q; want the instructions before the text", stored[0].Content)
	}
	if stored[2].Content != "second task" {
		t.Errorf("second message = %q; want the text alone", stored[2].Content)
	}
	if system := stub.Requests()[0].Messages[0]; system.Role != llm.RoleSystem || strings.Contains(system.Content, "CLAUDE.md") {
		t.Errorf("system prompt = %q; want it without the instructions", system.Content)
	}
}
