package shell

import (
	"errors"
	"testing"

	"github.com/govengers/aikido/vfs"
)

func TestResolve(t *testing.T) {
	testCases := []struct {
		name      string
		directory string
		path      string
		want      string
		wantError bool
	}{
		{"relative at the root", "", "a.md", "a.md", false},
		{"relative in a directory", "docs", "a.md", "docs/a.md", false},
		{"absolute means the workspace root", "docs", "/b/c.md", "b/c.md", false},
		{"dot segments", "docs", "./x/../y.md", "docs/y.md", false},
		{"parent inside the workspace", "docs/api", "../index.md", "docs/index.md", false},
		{"the root itself", "docs", "..", "", false},
		{"slash is the root", "docs", "/", "", false},
		{"climbing above the root", "docs", "../../etc/passwd", "", true},
		{"climbing above from the root", "", "../x", "", true},
		{"empty path", "", "", "", true},
		{"backslash stays invalid", "", `a\b`, "", true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := Resolve(testCase.directory, testCase.path)
			if testCase.wantError {
				if !errors.Is(err, vfs.ErrPathInvalid) {
					t.Fatalf("Resolve(%q, %q) error = %v; want ErrPathInvalid", testCase.directory, testCase.path, err)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("Resolve(%q, %q) = %q, %v; want %q", testCase.directory, testCase.path, got, err, testCase.want)
			}
		})
	}
}
