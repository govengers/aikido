package overlay

import (
	"fmt"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	testCases := []struct {
		name    string
		changes []Change
		want    string
	}{
		{name: "no changes", want: ""},
		{
			name:    "create",
			changes: []Change{{Path: "new.md", Kind: ChangeCreate, Content: []byte("one\ntwo\n")}},
			want:    "--- /dev/null\n+++ b/new.md\n@@ -0,0 +1,2 @@\n+one\n+two\n",
		},
		{
			name:    "delete",
			changes: []Change{{Path: "old.md", Kind: ChangeDelete, Previous: []byte("gone\n")}},
			want:    "--- a/old.md\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-gone\n",
		},
		{
			name:    "modify with context",
			changes: []Change{{Path: "a.md", Kind: ChangeModify, Previous: []byte("1\n2\n3\n4\n5\n"), Content: []byte("1\n2\nthree\n4\n5\n")}},
			want:    "--- a/a.md\n+++ b/a.md\n@@ -1,5 +1,5 @@\n 1\n 2\n-3\n+three\n 4\n 5\n",
		},
		{
			name:    "missing newline at end of file",
			changes: []Change{{Path: "a.txt", Kind: ChangeModify, Previous: []byte("a\nb"), Content: []byte("a\nc")}},
			want:    "--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n",
		},
		{
			name:    "binary",
			changes: []Change{{Path: "logo.png", Kind: ChangeCreate, Content: []byte{0x89, 'P', 0, 1}}},
			want:    "Binary files /dev/null and b/logo.png differ\n",
		},
		{
			name: "files in the given order",
			changes: []Change{
				{Path: "a.md", Kind: ChangeCreate, Content: []byte("a\n")},
				{Path: "b.md", Kind: ChangeDelete, Previous: []byte("b\n")},
			},
			want: "--- /dev/null\n+++ b/a.md\n@@ -0,0 +1,1 @@\n+a\n--- a/b.md\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-b\n",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := UnifiedDiff(testCase.changes); got != testCase.want {
				t.Errorf("UnifiedDiff =\n%s\nwant\n%s", got, testCase.want)
			}
		})
	}
}

func TestLineDiff_SeparateHunks(t *testing.T) {
	var before, after strings.Builder
	for line := 1; line <= 30; line++ {
		fmt.Fprintf(&before, "line %d\n", line)
		switch line {
		case 2:
			after.WriteString("changed 2\n")
		case 25:
			after.WriteString("changed 25\n")
		default:
			fmt.Fprintf(&after, "line %d\n", line)
		}
	}
	got := lineDiff("a/x", []byte(before.String()), "b/x", []byte(after.String()))
	want := "--- a/x\n+++ b/x\n" +
		"@@ -1,5 +1,5 @@\n line 1\n-line 2\n+changed 2\n line 3\n line 4\n line 5\n" +
		"@@ -22,7 +22,7 @@\n line 22\n line 23\n line 24\n-line 25\n+changed 25\n line 26\n line 27\n line 28\n"
	if got != want {
		t.Errorf("lineDiff =\n%s\nwant\n%s", got, want)
	}
}

func TestLineDiff_EqualIsEmpty(t *testing.T) {
	if got := lineDiff("a", []byte("x\n"), "b", []byte("x\n")); got != "" {
		t.Errorf("lineDiff of equal texts = %q; want empty", got)
	}
}

func TestLineDiff_RepeatedLines(t *testing.T) {
	// No line is unique, so the whole file is one hunk.
	got := lineDiff("a/x", []byte("}\n}\n"), "b/x", []byte("}\n}\n}\n"))
	want := "--- a/x\n+++ b/x\n@@ -1,2 +1,3 @@\n }\n }\n+}\n"
	if got != want {
		t.Errorf("lineDiff =\n%s\nwant\n%s", got, want)
	}
}

func TestIncreasingRunLengths(t *testing.T) {
	got := increasingRunLengths([]int{3, 1, 4, 2, 5})
	want := []int{1, 1, 2, 2, 3}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("increasingRunLengths = %v; want %v", got, want)
	}
}
