package agent

import (
	"strings"
	"testing"

	"github.com/govengers/aikido/vfs"
	vfsmem "github.com/govengers/aikido/vfs/memory"
)

func TestReadFile_TextIsTheNumberedLinesAndWhereToContinue(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "a.go", "package a\n\nfunc A() {}\n")
	mustWriteFile(t, storage, "empty.md", "")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})

	whole, err := dispatch(t, registry, "read_file", `{"path":"a.go"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if whole.Text != "1\tpackage a\n2\t\n3\tfunc A() {}\n" {
		t.Errorf("text = %q; want the numbered lines only", whole.Text)
	}
	part, err := dispatch(t, registry, "read_file", `{"path":"a.go","limit":1}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if part.Text != "1\tpackage a\n[lines 1-1 of 3; continue with offset 2]" {
		t.Errorf("text = %q; want the line and where to continue", part.Text)
	}
	empty, err := dispatch(t, registry, "read_file", `{"path":"empty.md"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if empty.Text != "(empty file)" {
		t.Errorf("text = %q; want (empty file)", empty.Text)
	}
}

func TestGrep_TextIsOneLinePerHit(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "a.go", "alpha\nbeta\nalpha again\n")
	mustWriteFile(t, storage, "b.go", "gamma\n")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})

	for _, testCase := range []struct{ name, arguments, want string }{
		{"content", `{"pattern":"alpha"}`, "a.go:1:alpha\na.go:3:alpha again"},
		{"files", `{"pattern":"alpha","output_mode":"files_with_matches"}`, "a.go"},
		{"count", `{"pattern":"a","output_mode":"count"}`, "a.go: 3\nb.go: 1"},
		{"no match", `{"pattern":"delta"}`, "(no match)"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := dispatch(t, registry, "grep", testCase.arguments)
			if err != nil {
				t.Fatalf("grep: %v", err)
			}
			if result.Text != testCase.want {
				t.Errorf("text = %q; want %q", result.Text, testCase.want)
			}
		})
	}
}

func TestGlob_TextListsThePaths(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "docs/a.md", "a")
	mustWriteFile(t, storage, "docs/b.md", "b")
	registry := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage})

	result, err := dispatch(t, registry, "glob", `{"pattern":"docs/*.md"}`)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if !strings.Contains(result.Text, "docs/a.md\ndocs/b.md") {
		t.Errorf("text = %q; want one path per line", result.Text)
	}
}

func TestHiddenPathRuleIsConfigurable(t *testing.T) {
	storage := vfsmem.New()
	mustWriteFile(t, storage, "workbooks/_shared/learn/CONTEXT.md", "learn")
	mustWriteFile(t, storage, ".env", "secret")
	strict := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage, HideHiddenPaths: true})
	if _, err := dispatch(t, strict, "read_file", `{"path":"workbooks/_shared/learn/CONTEXT.md"}`); err == nil {
		t.Error("default rule: want _shared hidden")
	}
	dotOnly := newRegistryWithVFS(t, &VFSToolOptions{Storage: storage, HideHiddenPaths: true, HiddenPath: vfs.IsDotPath})
	if result, err := dispatch(t, dotOnly, "read_file", `{"path":"workbooks/_shared/learn/CONTEXT.md"}`); err != nil || !strings.Contains(result.Text, "learn") {
		t.Errorf("dot rule: read = %+v, %v; want the file", result, err)
	}
	if _, err := dispatch(t, dotOnly, "read_file", `{"path":".env"}`); err == nil {
		t.Error("dot rule: want .env hidden")
	}
}
