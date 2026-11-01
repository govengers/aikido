package vfs

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePath_Accepts(t *testing.T) {
	validPaths := []string{
		"a.md",
		"notes/today.md",
		"deep/nested/file.txt",
		"_internal/x.md", // hidden filtering is a caller-policy concern, not structural
		"a/b/c.bin",
	}
	for _, path := range validPaths {
		if err := ValidatePath(path); err != nil {
			t.Errorf("ValidatePath(%q) = %v; want nil", path, err)
		}
	}
}

func TestValidatePath_Rejects(t *testing.T) {
	testCases := []struct {
		name string
		path string
		want string
	}{
		{"empty", "", "empty"},
		{"absolute", "/abs/path.md", "absolute"},
		{"trailing slash", "foo/", "trailing"},
		{"double slash", "foo//bar", "double"},
		{"parent traversal", "../etc/passwd", "parent"},
		{"parent traversal mid", "a/../b", "parent"},
		{"null byte", "a\x00b", "null"},
		{"oversized", strings.Repeat("a", MaximumPathBytes+1), "exceeds"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidatePath(testCase.path)
			if !errors.Is(err, ErrPathInvalid) {
				t.Fatalf("ValidatePath(%q) = %v; want ErrPathInvalid", testCase.path, err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("err = %q; want substring %q", err.Error(), testCase.want)
			}
		})
	}
}

func TestValidatePath_BoundaryLength(t *testing.T) {
	exact := strings.Repeat("a", MaximumPathBytes)
	if err := ValidatePath(exact); err != nil {
		t.Errorf("path of exactly %d bytes should be accepted, got %v", MaximumPathBytes, err)
	}
}

func TestContentTypeFor(t *testing.T) {
	testCases := map[string]string{
		"notes.md":        "text/markdown",
		"README.MARKDOWN": "text/markdown",
		"a/b.txt":         "text/plain",
		"data.json":       "application/json",
		"c.yaml":          "application/yaml",
		"c.yml":           "application/yaml",
		"page.htm":        "text/html",
		"page.html":       "text/html",
		"table.csv":       "text/csv",
		"logo.png":        "",
		"Makefile":        "",
	}
	for path, want := range testCases {
		if got := ContentTypeFor(path); got != want {
			t.Errorf("ContentTypeFor(%q) = %q; want %q", path, got, want)
		}
	}
}
