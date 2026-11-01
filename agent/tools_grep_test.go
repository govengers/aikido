package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
	vfsmem "github.com/govengers/aikido/vfs/memory"
)

func seeded(t *testing.T, options VFSToolOptions, files map[string]string) *VFSToolOptions {
	t.Helper()
	storage := vfsmem.New()
	for filePath, content := range files {
		if err := storage.WriteFile(context.Background(), filePath, []byte(content), ""); err != nil {
			t.Fatal(err)
		}
	}
	options.Storage = storage
	return &options
}

func TestGlobToRegexp(t *testing.T) {
	testCases := []struct {
		glob, path string
		want       bool
	}{
		{"*.md", "a.md", true},
		{"*.md", "dir/a.md", false},
		{"**/*.md", "a.md", true},
		{"**/*.md", "x/y/a.md", true},
		{"docs/**", "docs/a/b.txt", true},
		{"docs/**", "other/a.txt", false},
		{"a?.md", "ab.md", true},
		{"a?.md", "a/.md", false},
		{"*.{md,txt}", "x.txt", true},
		{"*.{md,txt}", "x.json", false},
		{"a.md", "aXmd", false},
		{"café/*.md", "café/a.md", true},
		{"caf?/*.md", "café/a.md", true},
		{"{über,unter}/*.md", "über/a.md", true},
	}
	for _, testCase := range testCases {
		pattern, err := globToRegexp(testCase.glob)
		if err != nil {
			t.Fatalf("%q: %v", testCase.glob, err)
		}
		if got := pattern.MatchString(testCase.path); got != testCase.want {
			t.Errorf("glob %q on %q = %v; want %v", testCase.glob, testCase.path, got, testCase.want)
		}
	}
	for _, invalid := range []string{"{a,b", ""} {
		if _, err := globToRegexp(invalid); err == nil {
			t.Errorf("glob %q should error", invalid)
		}
	}
}

func TestRegisterVFSTools_Extended(t *testing.T) {
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: vfsmem.New()})
	for _, name := range []string{"grep", "glob", "edit_file", "multi_edit", "move_file"} {
		if !registry.Has(name) {
			t.Errorf("%s should be registered by default", name)
		}
	}
	registry = newRegistryWithVFS(t, &VFSToolOptions{Storage: vfsmem.New(), ReadOnly: true})
	if !registry.Has("grep") || !registry.Has("glob") || registry.Has("edit_file") || registry.Has("multi_edit") || registry.Has("move_file") {
		t.Error("read-only: want grep and glob, no edit_file, multi_edit or move_file")
	}
	registry = newRegistryWithVFS(t, &VFSToolOptions{Storage: vfsmem.New(), DisableExtended: true})
	for _, name := range []string{"grep", "glob", "edit_file", "multi_edit", "move_file"} {
		if registry.Has(name) {
			t.Errorf("%s must not be registered with DisableExtended", name)
		}
	}
}

func TestGrep_ContentContextAndPolicy(t *testing.T) {
	options := seeded(t, VFSToolOptions{HideHiddenPaths: true, AllowedExtensions: []string{".md"}}, map[string]string{
		"a.md":       "one\ntwo needle\nthree\nfour\nfive needle\n",
		"_hidden.md": "needle\n",
		"b.txt":      "needle\n",
		"bin.md":     "needle\x00\n",
	})
	registry := newRegistryWithVFS(t, options)
	result, err := dispatch(t, registry, "grep", `{"pattern":"NEEDLE","case_insensitive":true,"context":1}`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(result.Content.(map[string]any)["matches"].([]string), "|")
	want := "a.md-1-one|a.md:2:two needle|a.md-3-three|a.md-4-four|a.md:5:five needle"
	if got != want {
		t.Errorf("matches = %q; want %q", got, want)
	}
}

func TestGrep_EndAnchorMatchesBeforeLineTerminator(t *testing.T) {
	options := seeded(t, VFSToolOptions{}, map[string]string{
		"a.md": "TODO:\nTODO: later\n\r\nend\r\n",
	})
	registry := newRegistryWithVFS(t, options)
	for _, testCase := range []struct{ pattern, want string }{
		{`TODO:$`, "a.md:1:TODO:"},
		{`^\s*$`, "a.md:3:"},
		{`^end$`, "a.md:4:end"},
	} {
		result, err := dispatch(t, registry, "grep", fmt.Sprintf(`{"pattern":%q}`, testCase.pattern))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(result.Content.(map[string]any)["matches"].([]string), "|"); got != testCase.want {
			t.Errorf("grep %s = %q; want %q", testCase.pattern, got, testCase.want)
		}
	}
}

func TestGrep_ModesAndCaps(t *testing.T) {
	var many strings.Builder
	for line := range grepMaximumLines + 10 {
		fmt.Fprintf(&many, "hit %d %s\n", line, strings.Repeat("x", grepMaximumLineChars))
	}
	options := seeded(t, VFSToolOptions{}, map[string]string{
		"big.md":   many.String(),
		"small.md": "hit\nhit\n",
		"none.md":  "miss\n",
	})
	registry := newRegistryWithVFS(t, options)

	result, err := dispatch(t, registry, "grep", `{"pattern":"hit"}`)
	if err != nil {
		t.Fatal(err)
	}
	content := result.Content.(map[string]any)
	matches := content["matches"].([]string)
	if len(matches) != grepMaximumLines || content["truncated"] != true {
		t.Errorf("len = %d truncated = %v; want %d true", len(matches), content["truncated"], grepMaximumLines)
	}
	if !strings.HasSuffix(matches[0], "…") || len([]rune(matches[0])) > grepMaximumLineChars+20 {
		t.Errorf("long line not clipped: %d runes", len([]rune(matches[0])))
	}

	files := mustDispatch(t, registry, "grep", `{"pattern":"hit","output_mode":"files_with_matches"}`)
	if got := strings.Join(files["files"].([]string), ","); got != "big.md,small.md" {
		t.Errorf("files = %q", got)
	}
	counts := mustDispatch(t, registry, "grep", `{"pattern":"hit","output_mode":"count","path_glob":"small.*"}`)["counts"].([]map[string]any)
	if len(counts) != 1 || counts[0]["count"] != 2 {
		t.Errorf("counts = %v", counts)
	}
	for _, arguments := range []string{`{"pattern":"("}`, `{"pattern":"x","output_mode":"lines"}`, `{"pattern":"x","path_glob":"{a"}`, `not json`} {
		if _, err := dispatch(t, registry, "grep", arguments); err == nil {
			t.Errorf("grep %s should error", arguments)
		}
	}
}

func TestGlob(t *testing.T) {
	options := seeded(t, VFSToolOptions{HideHiddenPaths: true}, map[string]string{
		"a.md": "", "d/b.md": "", "d/c.txt": "", ".x/y.md": "",
	})
	registry := newRegistryWithVFS(t, options)
	result, err := dispatch(t, registry, "glob", `{"pattern":"**/*.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Content.(map[string]any)["paths"].([]string), ","); got != "a.md,d/b.md" {
		t.Errorf("paths = %q", got)
	}
}

func TestEditFile(t *testing.T) {
	options := seeded(t, VFSToolOptions{}, map[string]string{"a.md": "foo bar foo\n", "bin.md": "x\x00"})
	registry := newRegistryWithVFS(t, options)
	ctx := context.Background()

	if _, err := dispatch(t, registry, "edit_file", `{"path":"a.md","old_string":"foo","new_string":"baz"}`); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Errorf("ambiguous match err = %v", err)
	}
	if _, err := dispatch(t, registry, "edit_file", `{"path":"a.md","old_string":"nope","new_string":"x"}`); err == nil {
		t.Error("missing old_string should error")
	}
	if _, err := dispatch(t, registry, "edit_file", `{"path":"a.md","old_string":"","new_string":"x"}`); err == nil {
		t.Error("empty old_string should error")
	}
	if _, err := dispatch(t, registry, "edit_file", `{"path":"bin.md","old_string":"x","new_string":"y"}`); err == nil {
		t.Error("binary file should error")
	}
	if _, err := dispatch(t, registry, "edit_file", `{"path":"a.md","old_string":"bar","new_string":"qux"}`); err != nil {
		t.Fatal(err)
	}
	result, err := dispatch(t, registry, "edit_file", `{"path":"a.md","old_string":"foo","new_string":"baz","replace_all":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content.(map[string]any)["replacements"] != 2 {
		t.Errorf("replacements = %v", result.Content)
	}
	got, _, err := options.Storage.ReadFile(ctx, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "baz qux baz\n" {
		t.Errorf("content = %q", got)
	}
}

func TestReadFile_Ranges(t *testing.T) {
	options := seeded(t, VFSToolOptions{}, map[string]string{"a.md": "1\n2\n3\n4\n5\n"})
	registry := newRegistryWithVFS(t, options)
	result, err := dispatch(t, registry, "read_file", `{"path":"a.md","offset":2,"limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	content := result.Content.(map[string]any)
	if content["content"] != "2\t2\n3\t3\n" || content["total_lines"] != 5 || content["truncated"] != true {
		t.Errorf("content = %v", content)
	}
	if content := mustDispatch(t, registry, "read_file", `{"path":"a.md"}`); content["content"] != "1\t1\n2\t2\n3\t3\n4\t4\n5\t5\n" || content["truncated"] != false {
		t.Errorf("full read = %v", content)
	}
	if _, err := dispatch(t, registry, "read_file", `{"path":"a.md","offset":9}`); err == nil {
		t.Error("offset past end should error")
	}
}

func TestListAndSearch_RespectAllowedExtensions(t *testing.T) {
	options := seeded(t, VFSToolOptions{AllowedExtensions: []string{".md"}}, map[string]string{"a.md": "x", "b.txt": "x"})
	registry := newRegistryWithVFS(t, options)
	if files := mustDispatch(t, registry, "list_files", `{}`)["files"].([]map[string]any); len(files) != 1 || files[0]["path"] != "a.md" {
		t.Errorf("list_files = %v", files)
	}
	if paths := mustDispatch(t, registry, "search", `{"query":"x"}`)["paths"].([]string); len(paths) != 1 || paths[0] != "a.md" {
		t.Errorf("search = %v", paths)
	}
}

func TestParseGrepMode(t *testing.T) {
	testCases := []struct {
		outputMode string
		want       grepMode
		wantError  bool
	}{
		{"", grepModeContent, false},
		{"content", grepModeContent, false},
		{"files_with_matches", grepModeFiles, false},
		{"count", grepModeCount, false},
		{"lines", "", true},
	}
	for _, testCase := range testCases {
		got, err := parseGrepMode(testCase.outputMode)
		if got != testCase.want || (err != nil) != testCase.wantError {
			t.Errorf("parseGrepMode(%q) = %q, %v; want %q, error %v", testCase.outputMode, got, err, testCase.want, testCase.wantError)
		}
	}
}

func TestGrep_ScanBudgetTruncates(t *testing.T) {
	options := seeded(t, VFSToolOptions{MaximumFileBytes: grepScanBudget}, map[string]string{
		"a.md": strings.Repeat("x", grepScanBudget),
		"b.md": "needle\n",
	})
	registry := newRegistryWithVFS(t, options)
	content := mustDispatch(t, registry, "grep", `{"pattern":"needle"}`)
	if matches := content["matches"].([]string); len(matches) != 0 || content["truncated"] != true {
		t.Errorf("content = %v; want no matches and truncated after the budget", content)
	}
}

// inflatedStorage lists one file with a larger size than it holds.
type inflatedStorage struct {
	vfs.Storage
	path string
	size int64
}

func (s inflatedStorage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	files, err := s.Storage.ListFiles(ctx)
	for index := range files {
		if files[index].Path == s.path {
			files[index].Size = s.size
		}
	}
	return files, err
}

func TestGrep_SkipsOversizedFilesUnread(t *testing.T) {
	testCases := []struct {
		name             string
		maximumFileBytes int64
		listedSize       int64
		wantMatches      string
	}{
		{"over MaximumFileBytes is skipped", 16, 17, "small.md:1:needle"},
		{"over the scan budget ends the scan", 2 * grepScanBudget, grepScanBudget + 1, ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			seededOptions := seeded(t, VFSToolOptions{MaximumFileBytes: testCase.maximumFileBytes}, map[string]string{
				"big.md":   "needle\n",
				"small.md": "needle\n",
			})
			seededOptions.Storage = faultyStorage{
				Storage:       inflatedStorage{Storage: seededOptions.Storage, path: "big.md", size: testCase.listedSize},
				readErrorPath: "big.md",
				readError:     errors.New("big.md must not be read"),
			}
			content := mustDispatch(t, newRegistryWithVFS(t, seededOptions), "grep", `{"pattern":"needle"}`)
			if got := strings.Join(content["matches"].([]string), "|"); got != testCase.wantMatches || content["truncated"] != true {
				t.Errorf("content = %v; want matches %q and truncated", content, testCase.wantMatches)
			}
		})
	}
}

func TestRegisterVFSTools_DuplicateName(t *testing.T) {
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: vfsmem.New()})
	err := RegisterVFSTools(registry, &VFSToolOptions{Storage: vfsmem.New()})
	if !errors.Is(err, tools.ErrDuplicateTool) {
		t.Errorf("err = %v; want tools.ErrDuplicateTool", err)
	}
}
