package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiffTrees(t *testing.T) {
	writeTree := func(files map[string]string) string {
		root := t.TempDir()
		for path, content := range files {
			full := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	before := writeTree(map[string]string{"kept.txt": "same", "removed.txt": "gone", "sub/edited.go": "price: 4599,"})
	after := writeTree(map[string]string{"kept.txt": "same", "added.txt": "new!!", "sub/edited.go": "price: 4999,"})

	files, bytes, err := diffTrees(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if files != 3 || bytes != 4+5+1 {
		t.Errorf("diffTrees = %d files, %d bytes, want 3 files, 10 bytes", files, bytes)
	}
}

func TestChangedBytes(t *testing.T) {
	testCases := []struct {
		before, after string
		want          int
	}{
		{"same", "same", 0},
		{"price: 4599,", "price: 4999,", 1},
		{"abc", "abXYc", 2},
		{"abc", "", 3},
		{"aaaa", "aa", 2},
	}
	for _, testCase := range testCases {
		if got := changedBytes([]byte(testCase.before), []byte(testCase.after)); got != testCase.want {
			t.Errorf("changedBytes(%q, %q) = %d, want %d", testCase.before, testCase.after, got, testCase.want)
		}
	}
}
