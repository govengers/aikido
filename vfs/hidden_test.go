package vfs

import "testing"

func TestHiddenPathRules(t *testing.T) {
	for _, testCase := range []struct {
		path        string
		hidden, dot bool
	}{
		{"README.md", false, false},
		{".env", true, true},
		{"a/.git/config", true, true},
		{"workbooks/_shared/learn/CONTEXT.md", true, false},
		{"under_score.md", false, false},
	} {
		if got := IsHiddenPath(testCase.path); got != testCase.hidden {
			t.Errorf("IsHiddenPath(%q) = %v; want %v", testCase.path, got, testCase.hidden)
		}
		if got := IsDotPath(testCase.path); got != testCase.dot {
			t.Errorf("IsDotPath(%q) = %v; want %v", testCase.path, got, testCase.dot)
		}
	}
}
