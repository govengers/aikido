package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/osfs"
	"github.com/govengers/aikido/vfs/overlay"
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
		{"growth to the limit", "aa", []textEdit{{OldString: "a", NewString: "aaaaa", ReplaceAll: true}}, strings.Repeat("a", 10), 2, ""},
		{"growth beyond the limit", "aa", []textEdit{{OldString: "a", NewString: "aaaaaa", ReplaceAll: true}}, "", 0, "would grow the file beyond 10 bytes"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			updated, replacements, err := applyEdits(context.Background(), "f.md", []byte(testCase.content), testCase.edits, 10)
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

func TestApplyEdits_ReplaceAllChainStopsAtTheLimit(t *testing.T) {
	doubling := make([]textEdit, 26)
	for index := range doubling {
		doubling[index] = textEdit{OldString: "x", NewString: "xx", ReplaceAll: true}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, _, err := applyEdits(context.Background(), "f.md", []byte("x"), doubling, 1<<10)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, vfs.ErrFileTooLarge) {
		t.Fatalf("err = %v; want ErrFileTooLarge", err)
	}
	// Without the check before each edit the chain allocates about 2^27 bytes.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
		t.Errorf("applyEdits allocated %d bytes for a 1 KiB limit", allocated)
	}
}

func TestApplyEdits_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := applyEdits(ctx, "f.md", []byte("a"), []textEdit{{OldString: "a", NewString: "b"}}, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled", err)
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

// faultyStorage fails DeleteFile, and ReadFile and WriteFile of one path
// each, on top of a working Storage.
type faultyStorage struct {
	vfs.Storage
	deleteError    error
	readErrorPath  string
	readError      error
	writeErrorPath string
	writeError     error
}

func (s faultyStorage) WriteFile(ctx context.Context, filePath string, content []byte, contentType string) error {
	if filePath == s.writeErrorPath {
		return s.writeError
	}
	return s.Storage.WriteFile(ctx, filePath, content, contentType)
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

// caseInsensitiveDirectory returns a temporary directory on a filesystem
// that ignores case (the macOS and Windows defaults) and skips the test
// elsewhere.
func caseInsensitiveDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "probe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "PROBE")); err != nil {
		t.Skip("the temporary directory is case-sensitive")
	}
	if err := os.Remove(filepath.Join(directory, "probe")); err != nil {
		t.Fatal(err)
	}
	return directory
}

func listedPaths(t *testing.T, storage vfs.Storage) []string {
	t.Helper()
	files, err := storage.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

// caseInsensitiveStorage returns an osfs storage holding Readme.md on a
// case-insensitive temporary directory, optionally behind an overlay.
func caseInsensitiveStorage(t *testing.T, throughOverlay bool) vfs.Storage {
	t.Helper()
	disk, err := osfs.New(&osfs.Options{Directory: caseInsensitiveDirectory(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	mustWriteFile(t, disk, "Readme.md", "keep me")
	if !throughOverlay {
		return disk
	}
	staged, err := overlay.New(&overlay.Options{Base: disk})
	if err != nil {
		t.Fatal(err)
	}
	return staged
}

func TestMoveFile_CaseOnlyRenameOnCaseInsensitiveDisk(t *testing.T) {
	testCases := []struct{ overwrite, throughOverlay bool }{{false, false}, {true, false}, {false, true}, {true, true}}
	for _, testCase := range testCases {
		t.Run(fmt.Sprintf("overwrite=%v overlay=%v", testCase.overwrite, testCase.throughOverlay), func(t *testing.T) {
			storage := caseInsensitiveStorage(t, testCase.throughOverlay)
			registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})

			mustDispatch(t, registry, "move_file", fmt.Sprintf(`{"source":"Readme.md","destination":"README.md","overwrite":%v}`, testCase.overwrite))
			if got := readBack(t, storage, "README.md"); got != "keep me" {
				t.Errorf("README.md = %q; want the moved content", got)
			}
			if paths := listedPaths(t, storage); !slices.Equal(paths, []string{"README.md"}) {
				t.Errorf("files = %v; want only README.md", paths)
			}
		})
	}
}

func TestMoveFile_CaseOnlyRenameThroughOverlayCommit(t *testing.T) {
	storage := caseInsensitiveStorage(t, true)
	staged, ok := storage.(*overlay.Storage)
	if !ok {
		t.Fatalf("storage is %T; want *overlay.Storage", storage)
	}
	mustDispatch(t, newRegistryWithVFS(t, &VFSToolOptions{Storage: staged}), "move_file", `{"source":"Readme.md","destination":"README.md"}`)
	if err := staged.Commit(context.Background()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	staged.Discard()
	if got := readBack(t, staged, "README.md"); got != "keep me" {
		t.Errorf("README.md on disk = %q; want the moved content", got)
	}
	if paths := listedPaths(t, staged); !slices.Equal(paths, []string{"README.md"}) {
		t.Errorf("files on disk = %v; want only README.md", paths)
	}
}

func TestMoveFile_CaseOnlyRenameOnCaseSensitiveStorage(t *testing.T) {
	options := seeded(t, VFSToolOptions{}, map[string]string{"Readme.md": "source", "README.md": "existing"})
	registry := newRegistryWithVFS(t, options)

	if _, err := dispatch(t, registry, "move_file", `{"source":"Readme.md","destination":"README.md"}`); err == nil || !strings.Contains(err.Error(), "destination README.md exists") {
		t.Fatalf("err = %v; want the existing destination refused", err)
	}
	mustDispatch(t, registry, "move_file", `{"source":"Readme.md","destination":"README.md","overwrite":true}`)
	if got := readBack(t, options.Storage, "README.md"); got != "source" {
		t.Errorf("README.md = %q; want source", got)
	}
	if paths := listedPaths(t, options.Storage); !slices.Equal(paths, []string{"README.md"}) {
		t.Errorf("files = %v; want only README.md, no staging file", paths)
	}
}

func TestMoveFile_CaseOnlyRenameRestoresTheSourceOnFailure(t *testing.T) {
	base := seeded(t, VFSToolOptions{}, map[string]string{"a.md": "alpha"}).Storage
	options := &VFSToolOptions{Storage: faultyStorage{Storage: base, writeErrorPath: "A.md", writeError: errors.New("disk full")}}
	registry := newRegistryWithVFS(t, options)

	if _, err := dispatch(t, registry, "move_file", `{"source":"a.md","destination":"A.md"}`); err == nil || !strings.Contains(err.Error(), "write A.md: disk full") {
		t.Fatalf("err = %v; want the failed destination write", err)
	}
	if got := readBack(t, base, "a.md"); got != "alpha" {
		t.Errorf("a.md = %q; want it restored", got)
	}
	if paths := listedPaths(t, base); !slices.Equal(paths, []string{"a.md"}) {
		t.Errorf("files = %v; want only a.md, no staging file", paths)
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
		skip         int
		wantNumbered string
		wantNext     int
	}{
		{"width follows the last line number", ten, 8, 10, 0, " 9\tx\n10\tx\n", 10},
		{"empty window", nil, 0, 0, 0, "", 0},
		{"stops at the byte budget", []string{"a\n", long}, 0, 2, 0, "1\ta\n", 1},
		{"skips into the first line", []string{"abcdef\n", "z\n"}, 0, 2, 3, "1\tdef\n2\tz\n", 2},
		{"budget counts JSON escapes", []string{"a\n", strings.Repeat("<", readMaximumBytes/2)}, 0, 2, 0, "1\ta\n", 1},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			numbered, next := numberLines(testCase.lines, testCase.start, testCase.end, testCase.skip)
			if numbered != testCase.wantNumbered || next != testCase.wantNext {
				t.Errorf("numberLines = %.40q, %d; want %.40q, %d", numbered, next, testCase.wantNumbered, testCase.wantNext)
			}
		})
	}
}

// readFileResult dispatches read_file and checks that the model would get
// the result whole, under the default tool result cap.
func readFileResult(t *testing.T, registry *tools.Registry, arguments string) map[string]any {
	t.Helper()
	content := mustDispatch(t, registry, "read_file", arguments)
	if encoded := toolResultJSON(ToolResult{OK: true, Content: content}); len(encoded) > defaultMaximumToolResultBytes {
		t.Fatalf("read_file %s: %d bytes of JSON; the model would get a cut payload", arguments, len(encoded))
	}
	return content
}

// Regression: the window was budgeted on raw bytes, so escape-heavy content
// passed the tool result cap once encoded and reached the model cut.
func TestReadFile_BudgetsTheEncodedSize(t *testing.T) {
	line := strings.Repeat(`"<\`, 30) + "\n"
	options := seeded(t, VFSToolOptions{}, map[string]string{"quotes.md": strings.Repeat(line, 400)})
	registry := newRegistryWithVFS(t, options)

	content := readFileResult(t, registry, `{"path":"quotes.md"}`)
	read := strings.Count(content["content"].(string), "\n")
	if content["truncated"] != true || read == 0 || read >= 400 {
		t.Fatalf("read %d lines, truncated %v; want a partial window", read, content["truncated"])
	}
	next := readFileResult(t, registry, fmt.Sprintf(`{"path":"quotes.md","offset":%d}`, read+1))
	if !strings.HasPrefix(next["content"].(string), fmt.Sprintf("%d\t", read+1)) {
		t.Errorf("continuation starts with %.20q; want line %d", next["content"], read+1)
	}
}

// Regression: a line longer than the window was returned whole and then cut
// by the tool result cap, with no way to read the rest.
func TestReadFile_LongLineContinuesByColumn(t *testing.T) {
	line := strings.Repeat(`é"x`, 30000)
	options := seeded(t, VFSToolOptions{}, map[string]string{"long.md": line + "\nnext\n"})
	registry := newRegistryWithVFS(t, options)
	cutNotice := regexp.MustCompile(`\n\[line 1 is cut here; read on with offset 1 and column (\d+)\]$`)

	var rebuilt strings.Builder
	arguments := `{"path":"long.md"}`
	for range 20 {
		numbered := readFileResult(t, registry, arguments)["content"].(string)
		piece := strings.TrimPrefix(numbered, "1\t")
		match := cutNotice.FindStringSubmatch(piece)
		if match == nil {
			if !strings.HasSuffix(piece, "\n2\tnext\n") {
				t.Fatalf("last window = %.40q...; want the rest of line 1 and line 2", piece)
			}
			rebuilt.WriteString(strings.TrimSuffix(piece, "\n2\tnext\n"))
			if rebuilt.String() != line {
				t.Fatalf("rebuilt line has %d bytes; want the %d of line 1", rebuilt.Len(), len(line))
			}
			return
		}
		rebuilt.WriteString(strings.TrimSuffix(piece, match[0]))
		if column, _ := strconv.Atoi(match[1]); column != rebuilt.Len()+1 {
			t.Fatalf("notice column %d; want %d", column, rebuilt.Len()+1)
		}
		arguments = fmt.Sprintf(`{"path":"long.md","column":%s}`, match[1])
	}
	t.Fatal("line 1 did not end within 20 reads")
}

func TestColumnBytes(t *testing.T) {
	lines := []string{"aé\n", "b\n"}
	testCases := []struct {
		offset, column, want int
		wantError            bool
	}{
		{1, 0, 0, false},
		{1, 1, 0, false},
		{1, 2, 1, false},
		{1, 3, 1, false}, // inside é: back to its start
		{1, 4, 3, false},
		{1, 5, 0, true},
		{3, 2, 0, true},
	}
	for _, testCase := range testCases {
		got, err := columnBytes(lines, testCase.offset, testCase.column)
		if got != testCase.want || (err != nil) != testCase.wantError {
			t.Errorf("columnBytes(%d, %d) = %d, %v; want %d, error %v", testCase.offset, testCase.column, got, err, testCase.want, testCase.wantError)
		}
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
