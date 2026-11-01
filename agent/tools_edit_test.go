package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/govengers/aikido/vfs"
)

func TestApplyEdits(t *testing.T) {
	testCases := []struct {
		name             string
		content          string
		edits            []textEdit
		wantUpdated      string
		wantReplacements int
		wantError        string // empty: success
	}{
		{"unique match", "a b c", []textEdit{{OldString: "b", NewString: "x"}}, "a x c", 1, ""},
		{"replace all", "b b", []textEdit{{OldString: "b", NewString: "x", ReplaceAll: true}}, "x x", 2, ""},
		{"edits chain", "a b", []textEdit{{OldString: "a", NewString: "c"}, {OldString: "c b", NewString: "done"}}, "done", 2, ""},
		{"ambiguous", "b b", []textEdit{{OldString: "b", NewString: "x"}}, "", 0, "old_string matches 2 times in f.md"},
		{"missing", "a", []textEdit{{OldString: "b", NewString: "x"}}, "", 0, "old_string not found in f.md"},
		{"empty old_string", "a", []textEdit{{NewString: "x"}}, "", 0, "old_string is empty"},
		{"identical", "a", []textEdit{{OldString: "a", NewString: "a"}}, "", 0, "identical"},
		{"binary", "a\x00b", []textEdit{{OldString: "a", NewString: "x"}}, "", 0, "f.md is not a UTF-8 text file"},
		{"second of two fails", "a", []textEdit{{OldString: "a", NewString: "b"}, {OldString: "a", NewString: "c"}}, "", 0, "edit 2: old_string not found"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			updated, replacements, err := applyEdits("f.md", []byte(testCase.content), testCase.edits)
			if testCase.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("err = %v; want %q", err, testCase.wantError)
				}
				return
			}
			if err != nil || updated != testCase.wantUpdated || replacements != testCase.wantReplacements {
				t.Errorf("applyEdits = %q, %d, %v; want %q, %d", updated, replacements, err, testCase.wantUpdated, testCase.wantReplacements)
			}
		})
	}
}

func readBack(t *testing.T, storage vfs.Storage, filePath string) string {
	t.Helper()
	content, _, err := storage.ReadFile(context.Background(), filePath)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", filePath, err)
	}
	return string(content)
}

func TestMultiEdit(t *testing.T) {
	options := seeded(t, VFSToolOptions{MaximumFileBytes: 20}, map[string]string{"a.md": "one two three\n"})
	registry := newRegistryWithVFS(t, options)

	result := mustDispatch(t, registry, "multi_edit", `{"path":"a.md","edits":[{"old_string":"one","new_string":"1"},{"old_string":"three","new_string":"3"}]}`)
	if result["replacements"] != 2 || readBack(t, options.Storage, "a.md") != "1 two 3\n" {
		t.Errorf("result = %v, file = %q", result, readBack(t, options.Storage, "a.md"))
	}

	failures := []struct{ name, arguments, wantError string }{
		{"failing edit leaves the file unchanged", `{"path":"a.md","edits":[{"old_string":"two","new_string":"2"},{"old_string":"nope","new_string":"x"}]}`, "multi_edit: edit 2: old_string not found in a.md; the file is unchanged"},
		{"empty edits", `{"path":"a.md","edits":[]}`, "edits is empty"},
		{"missing new_string", `{"path":"a.md","edits":[{"old_string":"two"}]}`, "invalid tool arguments for multi_edit: edit 1 is missing new_string"},
		{"null new_string", `{"path":"a.md","edits":[{"old_string":"one","new_string":"x"},{"old_string":"two","new_string":null}]}`, "edit 2 is missing new_string"},
		{"missing old_string", `{"path":"a.md","edits":[{"new_string":"x"}]}`, "edit 1 is missing old_string"},
		{"null edit", `{"path":"a.md","edits":[null]}`, "edit 1 is missing old_string"},
		{"result over MaximumFileBytes", `{"path":"a.md","edits":[{"old_string":"two","new_string":"two two two two two"}]}`, "MaximumFileBytes"},
		{"missing file", `{"path":"b.md","edits":[{"old_string":"a","new_string":"b"}]}`, "file not found"},
		{"invalid path", `{"path":"../a.md","edits":[{"old_string":"a","new_string":"b"}]}`, "invalid path"},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			if _, err := dispatch(t, registry, "multi_edit", failure.arguments); err == nil || !strings.Contains(err.Error(), failure.wantError) {
				t.Errorf("err = %v; want %q", err, failure.wantError)
			}
			if got := readBack(t, options.Storage, "a.md"); got != "1 two 3\n" {
				t.Errorf("file = %q; want it unchanged", got)
			}
		})
	}

	mustDispatch(t, registry, "multi_edit", `{"path":"a.md","edits":[{"old_string":" two","new_string":""}]}`)
	if got := readBack(t, options.Storage, "a.md"); got != "1 3\n" {
		t.Errorf("file = %q; an explicit empty new_string deletes", got)
	}
}

// faultyStorage fails DeleteFile, and ReadFile of one path, on top of a
// working Storage.
type faultyStorage struct {
	vfs.Storage
	deleteError   error
	readErrorPath string
	readError     error
}

func (s faultyStorage) DeleteFile(ctx context.Context, filePath string) error {
	if s.deleteError != nil {
		return s.deleteError
	}
	return s.Storage.DeleteFile(ctx, filePath)
}

func (s faultyStorage) ReadFile(ctx context.Context, filePath string) ([]byte, vfs.FileMetadata, error) {
	if filePath == s.readErrorPath {
		return nil, vfs.FileMetadata{}, s.readError
	}
	return s.Storage.ReadFile(ctx, filePath)
}

func TestMoveFile(t *testing.T) {
	options := seeded(t, VFSToolOptions{HideHiddenPaths: true}, map[string]string{"a.md": "alpha", "b.md": "beta"})
	registry := newRegistryWithVFS(t, options)

	result := mustDispatch(t, registry, "move_file", `{"source":"a.md","destination":"docs/a.txt"}`)
	if result["size"] != int64(5) || readBack(t, options.Storage, "docs/a.txt") != "alpha" {
		t.Errorf("result = %v", result)
	}
	if _, _, err := options.Storage.ReadFile(context.Background(), "a.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("source still readable: %v", err)
	}
	if _, metadata, _ := options.Storage.ReadFile(context.Background(), "docs/a.txt"); metadata.ContentType != vfs.ContentTypeFor("x.txt") {
		t.Errorf("content type = %q; want the destination extension's", metadata.ContentType)
	}

	failures := []struct{ name, arguments, wantError string }{
		{"existing destination", `{"source":"b.md","destination":"docs/a.txt"}`, "destination docs/a.txt exists; set overwrite"},
		{"same path", `{"source":"b.md","destination":"b.md"}`, "same path"},
		{"missing source", `{"source":"gone.md","destination":"c.md"}`, "file not found"},
		{"hidden destination", `{"source":"b.md","destination":".secret.md"}`, "hidden path"},
		{"invalid source", `{"source":"/b.md","destination":"c.md"}`, "invalid path"},
		{"missing destination argument", `{"source":"b.md"}`, `missing required argument "destination"`},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			if _, err := dispatch(t, registry, "move_file", failure.arguments); err == nil || !strings.Contains(err.Error(), failure.wantError) {
				t.Errorf("err = %v; want %q", err, failure.wantError)
			}
		})
	}

	mustDispatch(t, registry, "move_file", `{"source":"b.md","destination":"docs/a.txt","overwrite":true}`)
	if got := readBack(t, options.Storage, "docs/a.txt"); got != "beta" {
		t.Errorf("overwritten destination = %q; want beta", got)
	}
}

func TestMoveFile_StorageFailures(t *testing.T) {
	base := seeded(t, VFSToolOptions{}, map[string]string{"a.md": "alpha"}).Storage
	readFailure := errors.New("backend down")
	testCases := []struct {
		name      string
		storage   faultyStorage
		options   VFSToolOptions
		wantError string
	}{
		{"delete fails", faultyStorage{Storage: base, deleteError: errors.New("locked")}, VFSToolOptions{}, "copied a.md to b.md but could not delete the source: locked"},
		{"destination check fails", faultyStorage{Storage: base, readErrorPath: "b.md", readError: readFailure}, VFSToolOptions{}, "backend down"},
		{"source over MaximumFileBytes", faultyStorage{Storage: base}, VFSToolOptions{MaximumFileBytes: 2}, "MaximumFileBytes"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			options := testCase.options
			options.Storage = testCase.storage
			registry := newRegistryWithVFS(t, &options)
			if _, err := dispatch(t, registry, "move_file", `{"source":"a.md","destination":"b.md"}`); err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Errorf("err = %v; want %q", err, testCase.wantError)
			}
		})
	}
}

func TestMovedContentType(t *testing.T) {
	testCases := []struct{ destination, source, want string }{
		{"a.md", "text/plain", vfs.ContentTypeFor("a.md")},
		{"a.unknownext", "text/plain", "text/plain"},
		{"a.unknownext", "", unknownContentType},
	}
	for _, testCase := range testCases {
		if got := movedContentType(testCase.destination, testCase.source); got != testCase.want {
			t.Errorf("movedContentType(%q, %q) = %q; want %q", testCase.destination, testCase.source, got, testCase.want)
		}
	}
}

func TestNumberLines(t *testing.T) {
	ten := splitLines(strings.Repeat("x\n", 10))
	long := strings.Repeat("y", readMaximumBytes) + "\n"
	testCases := []struct {
		name         string
		lines        []string
		start, end   int
		wantNumbered string
		wantRendered int
	}{
		{"width follows the last line number", ten, 8, 10, " 9\tx\n10\tx\n", 10},
		{"empty window", nil, 0, 0, "", 0},
		{"an overlong first line is still returned", []string{long, "z\n"}, 0, 2, "1\t" + long, 1},
		{"stops at the byte budget", []string{"a\n", long}, 0, 2, "1\ta\n", 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			numbered, rendered := numberLines(testCase.lines, testCase.start, testCase.end)
			if numbered != testCase.wantNumbered || rendered != testCase.wantRendered {
				t.Errorf("numberLines = %.40q, %d; want %.40q, %d", numbered, rendered, testCase.wantNumbered, testCase.wantRendered)
			}
		})
	}
}

func TestReadFile_LargeFileIsCutAtTheByteBudget(t *testing.T) {
	line := strings.Repeat("w", 99) + "\n"
	options := seeded(t, VFSToolOptions{}, map[string]string{"big.md": strings.Repeat(line, 1000)})
	registry := newRegistryWithVFS(t, options)

	content := mustDispatch(t, registry, "read_file", `{"path":"big.md"}`)
	numbered, _ := content["content"].(string)
	if len(numbered) > readMaximumBytes || content["truncated"] != true || content["total_lines"] != 1000 {
		t.Errorf("read %d bytes, truncated %v, total %v; want at most %d bytes of 1000 lines, truncated", len(numbered), content["truncated"], content["total_lines"], readMaximumBytes)
	}
}
