//go:build e2e

package e2e

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	agentModel   = "e2e/agent-model"
	draftNotes   = "# Notes\n\nstatus: draft\n"
	finalNotes   = "# Notes\n\nstatus: final\n"
	readCallID   = "call_read"
	editCallID   = "call_edit"
	finishedText = "Marked the notes final."
)

// queueNotesEdit scripts the model: read notes.md, replace draft by final,
// then answer.
func queueNotesEdit(fake *fakeOpenRouter) {
	fake.queue(
		streamedToolCalls(scriptedToolCall{id: readCallID, name: "read_file", arguments: `{"path":"notes.md"}`}),
		streamedToolCalls(scriptedToolCall{
			id: editCallID, name: "edit_file",
			arguments: `{"path":"notes.md","old_string":"status: draft","new_string":"status: final"}`,
		}),
		streamedText(finishedText),
	)
}

func TestAgentCLIAppliesTheModelsEditsToTheDirectory(t *testing.T) {
	fake := newFakeOpenRouter(t)
	queueNotesEdit(fake)
	directory := t.TempDir()
	writeFiles(t, directory, map[string]string{"notes.md": draftNotes})

	result := runCLI(t, fake, "agent", "--model", agentModel, "--dir", directory, "--apply", "Mark the notes final.")

	result.requireSuccess(t)
	if content := readFile(t, filepath.Join(directory, "notes.md")); content != finalNotes {
		t.Errorf("notes.md = %q; want %q", content, finalNotes)
	}
	for _, want := range []string{
		"[tool-result] read_file ok", "[tool-result] edit_file ok", finishedText,
		"--- changes ---", "modify notes.md", "--- diff ---", "-status: draft", "+status: final",
		"applied the changes",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, result.stdout)
		}
	}
	assertNotesEditRequests(t, fake)
}

func TestAgentCLIWithoutApplyLeavesTheDirectoryUntouched(t *testing.T) {
	fake := newFakeOpenRouter(t)
	queueNotesEdit(fake)
	directory := t.TempDir()
	writeFiles(t, directory, map[string]string{"notes.md": draftNotes})

	result := runCLI(t, fake, "agent", "--model", agentModel, "--dir", directory, "Mark the notes final.")

	result.requireSuccess(t)
	if content := readFile(t, filepath.Join(directory, "notes.md")); content != draftNotes {
		t.Errorf("notes.md = %q; want it untouched", content)
	}
	for _, want := range []string{"modify notes.md", "+status: final", "dry run: nothing written"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, result.stdout)
		}
	}
	assertNotesEditRequests(t, fake)
}

// assertNotesEditRequests checks what the CLI sent while editing notes.md:
// the model, the key, the tools, and each tool result in the next request.
func assertNotesEditRequests(t *testing.T, fake *fakeOpenRouter) {
	t.Helper()
	for _, request := range fake.received() {
		if request.authorization != "Bearer "+e2eAPIKey {
			t.Errorf("Authorization = %q; want the key from OPENROUTER_API_KEY", request.authorization)
		}
	}
	requests := fake.chatRequests()
	if len(requests) != 3 {
		t.Fatalf("chat requests = %d; want 3 (read, edit, answer)", len(requests))
	}
	first := requests[0]
	if first.Model != agentModel || !first.Stream {
		t.Errorf("first request model %q stream %v; want %q streaming", first.Model, first.Stream, agentModel)
	}
	if first.lastUserText() != "Mark the notes final." {
		t.Errorf("user message = %q; want the prompt", first.lastUserText())
	}
	for _, tool := range []string{"read_file", "write_file", "edit_file", "list_files", "delete_file"} {
		if !slices.Contains(first.toolNames(), tool) {
			t.Errorf("tools offered = %v; want %s among them", first.toolNames(), tool)
		}
	}
	if read, ok := requests[1].toolResult(readCallID); !ok || !strings.Contains(read, "status: draft") {
		t.Errorf("read_file result in the second request = %q (found %v); want the file content", read, ok)
	}
	if edit, ok := requests[2].toolResult(editCallID); !ok || !strings.Contains(edit, `"replacements":1`) {
		t.Errorf("edit_file result in the third request = %q (found %v); want one replacement", edit, ok)
	}
}

func TestAgentCLIAppliesAMultiEditAndAMoveToTheDirectory(t *testing.T) {
	fake := newFakeOpenRouter(t)
	fake.queue(
		streamedToolCalls(scriptedToolCall{
			id: "call_multi", name: "multi_edit",
			arguments: `{"path":"draft.md","edits":[{"old_string":"# Draft","new_string":"# Release"},{"old_string":"status: draft","new_string":"status: final"}]}`,
		}),
		streamedToolCalls(scriptedToolCall{
			id: "call_move", name: "move_file",
			arguments: `{"source":"draft.md","destination":"release.md"}`,
		}),
		streamedText("Released."),
	)
	directory := t.TempDir()
	writeFiles(t, directory, map[string]string{"draft.md": "# Draft\n\nstatus: draft\n"})

	result := runCLI(t, fake, "agent", "--model", agentModel, "--dir", directory, "--apply", "Release the draft.")

	result.requireSuccess(t)
	if content := readFile(t, filepath.Join(directory, "release.md")); content != "# Release\n\nstatus: final\n" {
		t.Errorf("release.md = %q; want both edits applied", content)
	}
	if _, err := os.Stat(filepath.Join(directory, "draft.md")); !os.IsNotExist(err) {
		t.Errorf("draft.md after the move: %v; want it gone", err)
	}
	for _, want := range []string{
		"[tool-result] multi_edit ok", "[tool-result] move_file ok",
		"create release.md", "delete draft.md", "applied the changes",
	} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, result.stdout)
		}
	}
	requests := fake.chatRequests()
	if len(requests) != 3 {
		t.Fatalf("chat requests = %d; want 3 (multi_edit, move_file, answer)", len(requests))
	}
	for _, tool := range []string{"multi_edit", "move_file"} {
		if !slices.Contains(requests[0].toolNames(), tool) {
			t.Errorf("tools offered = %v; want %s among them", requests[0].toolNames(), tool)
		}
	}
	if edit, ok := requests[1].toolResult("call_multi"); !ok || !strings.Contains(edit, `"replacements":2`) {
		t.Errorf("multi_edit result sent to the model = %q (found %v); want two replacements", edit, ok)
	}
}

func TestAgentCLIRefusesWritesIntoAReadOnlyMount(t *testing.T) {
	const guide = "# Guide\n\nDo not touch.\n"
	fake := newFakeOpenRouter(t)
	fake.queue(
		streamedToolCalls(scriptedToolCall{
			id: "call_write", name: "write_file",
			arguments: `{"path":"docs/guide.md","content":"overwritten"}`,
		}),
		streamedText("The docs are read-only."),
	)
	workspace, docs := t.TempDir(), t.TempDir()
	writeFiles(t, docs, map[string]string{"guide.md": guide})

	result := runCLI(t, fake, "agent", "--model", agentModel, "--dir", workspace,
		"--mount", "docs="+docs+":ro", "--apply", "Rewrite the guide.")

	result.requireSuccess(t)
	if content := readFile(t, filepath.Join(docs, "guide.md")); content != guide {
		t.Errorf("docs/guide.md = %q; want it untouched", content)
	}
	if entries, err := os.ReadDir(workspace); err != nil || len(entries) != 0 {
		t.Errorf("workspace entries = %v (%v); want none", entries, err)
	}
	for _, want := range []string{"[tool-result] write_file fail:", "--- no changes ---"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, result.stdout)
		}
	}
	requests := fake.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d; want 2", len(requests))
	}
	if refusal, ok := requests[1].toolResult("call_write"); !ok || !strings.Contains(refusal, "read-only") {
		t.Errorf("write_file result sent to the model = %q (found %v); want the read-only error", refusal, ok)
	}
}

func TestChatCLIPrintsTheStreamedAnswerAndUsage(t *testing.T) {
	const answer = "Go was announced in November 2009."
	fake := newFakeOpenRouter(t)
	fake.queue(streamedText(answer))

	result := runCLI(t, fake, "chat", "--model", "e2e/chat-model", "When was Go announced?")

	result.requireSuccess(t)
	for _, want := range []string{answer, "[usage] prompt=12 completion=7 cost=$0.001200"} {
		if !strings.Contains(result.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, result.stdout)
		}
	}
	requests := fake.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d; want 1", len(requests))
	}
	if requests[0].Model != "e2e/chat-model" || !requests[0].Stream || requests[0].lastUserText() != "When was Go announced?" {
		t.Errorf("request = %+v; want the model, streaming and the prompt", requests[0])
	}
}

func TestImageCLIWritesThePNGFromTheImagesEndpoint(t *testing.T) {
	picture := pngImage(t)
	fake := newFakeOpenRouter(t)
	fake.queue(generatedImage(picture))
	output := t.TempDir()

	result := runCLI(t, fake, "image", "--out", output, "A pixel-art fox.")

	result.requireSuccess(t)
	assertSingleImageWritten(t, output, picture)
	requests := fake.received()
	if len(requests) != 1 || requests[0].path != imagesPath {
		t.Fatalf("requests = %+v; want one to %s", requests, imagesPath)
	}
	for _, want := range []string{`"model":"openai/gpt-image-2.5-flare"`, `"prompt":"A pixel-art fox."`} {
		if !strings.Contains(string(requests[0].body), want) {
			t.Errorf("images request %s lacks %s", requests[0].body, want)
		}
	}
}

func TestImageCLIWritesThePNGFromANonStreamingChatCompletion(t *testing.T) {
	const chatImageModel = "google/gemini-3.1-flash-image-preview"
	picture := pngImage(t)
	fake := newFakeOpenRouter(t)
	fake.queue(completedWithImage("Here is the fox.", picture))
	output := t.TempDir()

	result := runCLI(t, fake, "image", "--model", chatImageModel, "--out", output, "A pixel-art fox.")

	result.requireSuccess(t)
	assertSingleImageWritten(t, output, picture)
	if !strings.Contains(result.stdout, "Here is the fox.") {
		t.Errorf("stdout lacks the model's text:\n%s", result.stdout)
	}
	requests := fake.chatRequests()
	if len(requests) != 1 {
		t.Fatalf("chat requests = %d; want 1", len(requests))
	}
	if request := requests[0]; request.Model != chatImageModel || request.Stream || !slices.Equal(request.Modalities, []string{"image", "text"}) {
		t.Errorf("request model %q stream %v modalities %v; want %s, not streaming, image+text",
			request.Model, request.Stream, request.Modalities, chatImageModel)
	}
}

// pngImage encodes a small PNG, the bytes the fake model returns.
func pngImage(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(1, 1, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

// assertSingleImageWritten checks that directory holds exactly one .png with
// the given bytes.
func assertSingleImageWritten(t *testing.T, directory string, want []byte) {
	t.Helper()
	written, err := filepath.Glob(filepath.Join(directory, "*.png"))
	if err != nil || len(written) != 1 {
		t.Fatalf("PNG files in the output directory = %v (%v); want one", written, err)
	}
	if content := readFile(t, written[0]); content != string(want) {
		t.Errorf("%s holds %d bytes that differ from the %d the model returned", written[0], len(content), len(want))
	}
}
