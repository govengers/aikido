package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// unsetForTest removes key for the duration of the test; t.Setenv registers
// the restore.
func unsetForTest(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	contents := []byte("# comment\nFOO=bar\nQUOTED=\"hello world\"\n\nSINGLE='abc'\nALREADY=fromfile\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALREADY", "fromenv")
	for _, key := range []string{"FOO", "QUOTED", "SINGLE"} {
		unsetForTest(t, key)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("FOO = %q; want bar", got)
	}
	if got := os.Getenv("QUOTED"); got != "hello world" {
		t.Errorf("QUOTED = %q; want 'hello world'", got)
	}
	if got := os.Getenv("SINGLE"); got != "abc" {
		t.Errorf("SINGLE = %q; want abc", got)
	}
	if got := os.Getenv("ALREADY"); got != "fromenv" {
		t.Errorf("ALREADY clobbered: got %q; want existing fromenv", got)
	}
}

func TestLoadDotEnv_Missing(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Errorf("missing file should be a no-op, got %v", err)
	}
}

func TestLoadDotEnv_UnreadableIsAnError(t *testing.T) {
	if err := LoadDotEnv(t.TempDir()); err == nil {
		t.Error("reading a directory as .env returned no error")
	}
}

func TestParseDotEnvLine(t *testing.T) {
	testCases := []struct {
		line      string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{"FOO=bar", "FOO", "bar", true},
		{"  FOO = bar  ", "FOO", "bar", true},
		{`QUOTED="a = b"`, "QUOTED", "a = b", true},
		{"SINGLE='x'", "SINGLE", "x", true},
		{`MIXED="x'`, "MIXED", `"x'`, true},
		{"EMPTY=", "EMPTY", "", true},
		{"", "", "", false},
		{"# FOO=bar", "", "", false},
		{"=value", "", "", false},
		{"no separator", "", "", false},
	}
	for _, testCase := range testCases {
		key, value, ok := parseDotEnvLine(testCase.line)
		if key != testCase.wantKey || value != testCase.wantValue || ok != testCase.wantOK {
			t.Errorf("parseDotEnvLine(%q) = %q, %q, %v; want %q, %q, %v",
				testCase.line, key, value, ok, testCase.wantKey, testCase.wantValue, testCase.wantOK)
		}
	}
}
