package shell

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOptionSetParse(t *testing.T) {
	set := optionSet{flags: "rf", valued: "n", long: map[string]longOption{
		"recursive": {name: "r"}, "lines": {name: "n", valued: true},
	}}
	testCases := []struct {
		name         string
		arguments    []string
		wantOptions  string
		wantOperands []string
		wantError    string
	}{
		{"cluster and permutation", []string{"a", "-rf", "b"}, "r f", []string{"a", "b"}, ""},
		{"attached value", []string{"-n5", "x"}, "n=5", []string{"x"}, ""},
		{"separate value", []string{"-rn", "5"}, "r n=5", []string{}, ""},
		{"long forms", []string{"--recursive", "--lines=3", "--lines", "4"}, "r n=3 n=4", []string{}, ""},
		{"double dash ends options", []string{"--", "-r"}, "", []string{"-r"}, ""},
		{"dash is an operand", []string{"-"}, "", []string{"-"}, ""},
		{"unknown short", []string{"-z"}, "", nil, "invalid option -- 'z'"},
		{"missing short value", []string{"-n"}, "", nil, "option requires an argument -- 'n'"},
		{"unknown long", []string{"--bogus"}, "", nil, "unrecognized option '--bogus'"},
		{"long flag with value", []string{"--recursive=yes"}, "", nil, "option '--recursive' doesn't allow an argument"},
		{"missing long value", []string{"--lines"}, "", nil, "option '--lines' requires an argument"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, operands, err := set.parse(testCase.arguments)
			if testCase.wantError != "" {
				var usageErr *usageError
				if !errors.As(err, &usageErr) || err.Error() != testCase.wantError {
					t.Fatalf("error = %v; want %q", err, testCase.wantError)
				}
				return
			}
			rendered := renderOptions(parsed)
			if err != nil || rendered != testCase.wantOptions || !slices.Equal(operands, testCase.wantOperands) {
				t.Fatalf("parse = %q, %q, %v; want %q, %q", rendered, operands, err, testCase.wantOptions, testCase.wantOperands)
			}
		})
	}
	inOrder := optionSet{flags: "i", inOrder: true}
	if _, operands, _ := inOrder.parse([]string{"-i", "cmd", "-i"}); !slices.Equal(operands, []string{"cmd", "-i"}) {
		t.Fatalf("in-order operands = %q", operands)
	}
}

// renderOptions writes parsed options as "name" or "name=value", space
// separated.
func renderOptions(parsed options) string {
	rendered := []string{}
	for _, given := range parsed {
		if given.value != "" {
			rendered = append(rendered, given.name+"="+given.value)
		} else {
			rendered = append(rendered, given.name)
		}
	}
	return strings.Join(rendered, " ")
}

func TestBasenameAndDirname(t *testing.T) {
	testCases := []struct {
		name, suffix, base, directory string
	}{
		{"/a/b/c.txt", ".txt", "c", "/a/b"},
		{"/a/b/", "", "b", "/a"},
		{"/", "", "/", "/"},
		{"//", "", "/", "/"},
		{"c", "", "c", "."},
		{"a//b", "", "b", "a"},
		{".txt", ".txt", ".txt", "."},
		{"", "", "", "."},
		{"/a", "", "a", "/"},
	}
	for _, testCase := range testCases {
		if got := basename(testCase.name, testCase.suffix); got != testCase.base {
			t.Errorf("basename(%q, %q) = %q; want %q", testCase.name, testCase.suffix, got, testCase.base)
		}
		if got := dirname(testCase.name); got != testCase.directory {
			t.Errorf("dirname(%q) = %q; want %q", testCase.name, got, testCase.directory)
		}
	}
}

func TestRelativePath(t *testing.T) {
	testCases := []struct{ base, target, want string }{
		{"", "a/b", "a/b"},
		{"a", "a/b", "b"},
		{"a/b", "a", ".."},
		{"a/x", "a/b/c", "../b/c"},
		{"a", "a", "."},
		{"x/y", "", "../.."},
	}
	for _, testCase := range testCases {
		if got := relativePath(testCase.base, testCase.target); got != testCase.want {
			t.Errorf("relativePath(%q, %q) = %q; want %q", testCase.base, testCase.target, got, testCase.want)
		}
	}
}

func TestGlobRegexp(t *testing.T) {
	testCases := []struct {
		pattern    string
		ignoreCase bool
		subject    string
		want       bool
	}{
		{"*.go", false, "main.go", true},
		{"*.go", false, "./src/main.go", true},
		{"*.go", false, "main.GO", false},
		{"*.go", true, "main.GO", true},
		{"a?c", false, "abc", true},
		{"[!a]*", false, "abc", false},
		{"[^a]*", false, "bcd", true},
		{"[]x]", false, "]", true},
		{"a\\*", false, "a*", true},
		{"a\\*", false, "ab", false},
		{"[unclosed", false, "[unclosed", true},
		{"trailing\\", false, "trailing\\", true},
		{"a.b", false, "axb", false},
	}
	for _, testCase := range testCases {
		matcher, err := globRegexp(testCase.pattern, testCase.ignoreCase)
		if err != nil {
			t.Fatalf("globRegexp(%q): %v", testCase.pattern, err)
		}
		if got := matcher.MatchString(testCase.subject); got != testCase.want {
			t.Errorf("%q matches %q = %v; want %v", testCase.pattern, testCase.subject, got, testCase.want)
		}
	}
}

func TestSplitQuoted(t *testing.T) {
	testCases := []struct {
		line      string
		want      []string
		wantError bool
	}{
		{"a b\tc\n", []string{"a", "b", "c"}, false},
		{`'a b' "c d" e\ f`, []string{"a b", "c d", "e f"}, false},
		{`''`, []string{""}, false},
		{`'open`, nil, true},
		{"\"open\n", nil, true},
	}
	for _, testCase := range testCases {
		got, err := splitQuoted(testCase.line, true)
		if (err != nil) != testCase.wantError || !slices.Equal(got, testCase.want) {
			t.Errorf("splitQuoted(%q) = %q, %v; want %q, error %v", testCase.line, got, err, testCase.want, testCase.wantError)
		}
	}
}

func TestParseSleepAndSeq(t *testing.T) {
	durations := map[string]time.Duration{"1": time.Second, "0.5": 500 * time.Millisecond, "2s": 2 * time.Second, "1m": time.Minute, "1h": time.Hour, "1d": 24 * time.Hour}
	for text, want := range durations {
		if got, err := parseSleep(text); err != nil || got != want {
			t.Errorf("parseSleep(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	for _, text := range []string{"x", "-1", "s", "1y"} {
		if _, err := parseSleep(text); err == nil {
			t.Errorf("parseSleep(%q) succeeded", text)
		}
	}
	parsed, err := parseSeq([]string{"-s", ":", "-w", "5", "-1", "1"})
	if err != nil || parsed.first != 5 || parsed.step != -1 || parsed.last != 1 || parsed.separator != ":" || !parsed.equalWidth {
		t.Fatalf("parseSeq = %+v, %v", parsed, err)
	}
	for _, arguments := range [][]string{{"0.5"}, {"1", "2", "3", "4"}, {"-x"}, {"--separator=,"}} {
		if _, err := parseSeq(arguments); err == nil {
			t.Errorf("parseSeq(%q) succeeded", arguments)
		}
	}
}

func TestParseBashArguments(t *testing.T) {
	parsed, err := parseBashArguments([]string{"-e", "+u", "-o", "pipefail", "-c", "echo $1", "name", "one"})
	if err != nil || !parsed.hasScript || parsed.script != "echo $1" || !slices.Equal(parsed.positional, []string{"one"}) || !slices.Equal(parsed.options, []string{"-e", "+u", "-o", "pipefail"}) {
		t.Fatalf("parsed = %+v, %v", parsed, err)
	}
	parsed, err = parseBashArguments([]string{"--", "run.sh", "x"})
	if err != nil || parsed.file != "run.sh" || !slices.Equal(parsed.positional, []string{"x"}) {
		t.Fatalf("parsed = %+v, %v", parsed, err)
	}
}

func TestWrapText(t *testing.T) {
	if got := wrapText("abcdefg", 3); got != "abc\ndef\ng\n" {
		t.Errorf("wrapText = %q", got)
	}
	if got := wrapText("abc", 0); got != "abc\n" {
		t.Errorf("wrapText without wrap = %q", got)
	}
	if got := wrapText("", 76); got != "" {
		t.Errorf("wrapText of nothing = %q", got)
	}
}

func TestBoundedOutputKeepsRunesWhole(t *testing.T) {
	output := newBoundedOutput(46) // 34 bytes of marker, 12 of content
	if _, err := output.Write([]byte("äääää" + strings.Repeat("x", 40) + "üüüüü")); err != nil {
		t.Fatal(err)
	}
	text, truncated := output.result()
	if !truncated || !strings.HasPrefix(text, "ää") || !strings.HasSuffix(text, "üü") || strings.ContainsRune(text, '�') {
		t.Fatalf("result = %q, %v", text, truncated)
	}
	if !strings.Contains(text, "[") || strings.Count(text, "\n") != 2 || len(text) > 46 {
		t.Fatalf("no single cut marker in %q", text)
	}
}

func TestExitErrorText(t *testing.T) {
	if got := ExitError(3).Error(); got != "exit status 3" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestMoreCommandCases(t *testing.T) {
	files := map[string]string{"a.txt": "alpha\n", "n.md": "no newline", "dir/b.txt": "beta\n"}
	runScriptCases(t, []scriptCase{
		{name: "cat -n across files", files: files, script: "cat -n a.txt n.md a.txt", want: "     1\talpha\n     2\tno newlinealpha\n"},
		{name: "cat -n from stdin", files: files, script: "printf 'x\\ny' | cat -n", want: "     1\tx\n     2\ty"},
		{name: "sha256sum", files: files, script: "sha256sum a.txt nope; printf abc | sha256sum", want: "b6a98d9ce9a2d9149288fa3df42d377c3e42737afdcdaf714e33c0a100b51060  a.txt\nsha256sum: nope: No such file or directory\nba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  -\n"},
		{name: "basename and dirname commands", files: files, script: "basename -s .txt a.txt b.txt; basename x y z; dirname a/b c", want: "a\nb\nbasename: extra operand 'z'\nTry 'basename --help' for more information.\na\n.\n"},
		{name: "basename options", files: files, script: "basename --bogus; dirname --bogus", want: "basename: unrecognized option '--bogus'\nTry 'basename --help' for more information.\ndirname: unrecognized option '--bogus'\nTry 'dirname --help' for more information.\n", status: 1},
		{name: "seq output", files: files, script: "seq 3; seq -s, 2 4; seq -w 9 10; seq 3 1", want: "1\n2\n3\n2,3,4\n09\n10\n"},
		{name: "base64 round trip", files: files, script: "base64 a.txt; printf hello | base64 -w 4", want: "YWxwaGEK\naGVs\nbG8=\n"},
		{name: "touch then write", files: files, script: "touch dir/new; ls dir; touch dir; touch /dev/null; echo $?", want: "b.txt\nnew\n0\n"},
		{name: "rmdir errors", files: files, script: "rmdir a.txt; rmdir nope; rmdir", want: "rmdir: failed to remove 'a.txt': Not a directory\nrmdir: failed to remove 'nope': No such file or directory\nrmdir: missing operand\nTry 'rmdir --help' for more information.\n", status: 1},
		{name: "mkdir errors", files: files, script: "mkdir; mkdir a.txt/x; mkdir ../x", want: "mkdir: missing operand\nTry 'mkdir --help' for more information.\nmkdir: cannot create directory 'a.txt/x': Not a directory\nmkdir: cannot create directory '../x': No such file or directory\n", status: 1},
		{name: "cp into a missing directory", files: files, script: "cp a.txt nodir/x; cp -r dir nodir/x; mv dir nodir/x", want: "cp: cannot create regular file 'nodir/x': No such file or directory\ncp: cannot create regular file 'nodir/x': No such file or directory\nmv: cannot move 'dir' to 'nodir/x': No such file or directory\n", status: 1},
		{name: "cp and mv usage", files: files, script: "cp; mv -z", want: "cp: missing file operand\nTry 'cp --help' for more information.\nmv: invalid option -- 'z'\nTry 'mv --help' for more information.\n", status: 1},
		{name: "find with or and not", files: files, script: "find . -name a.txt -o -name n.md; find dir ! -name dir; find . -maxdepth 1 -type f -a -not -name a.txt", want: "./a.txt\n./n.md\ndir/b.txt\n./n.md\n"},
		{name: "find -mindepth and missing values", files: files, script: "find dir -mindepth 1; find . -mindepth; find . -type", want: "dir/b.txt\nfind: missing argument to `-mindepth'\nfind: missing argument to `-type'\n", status: 1},
		{name: "find -empty on a directory and -print0", files: files, script: "mkdir e; find . -empty -type d; find dir -print0", want: "./e\ndir\x00dir/b.txt\x00"},
		{name: "rm usage", files: files, script: "rm -z", want: "rm: invalid option -- 'z'\nTry 'rm --help' for more information.\n", status: 1},
		{name: "tee usage and errors", files: files, script: "echo x | tee nodir/f; tee -z", want: "x\ntee: nodir/f: No such file or directory\ntee: invalid option -- 'z'\nTry 'tee --help' for more information.\n", status: 1},
		{name: "touch usage", files: files, script: "touch; touch -z", want: "touch: missing file operand\nTry 'touch --help' for more information.\ntouch: invalid option -- 'z'\nTry 'touch --help' for more information.\n", status: 1},
		{name: "env usage and which usage", files: files, script: "env -z; echo $?; which -z; echo $?", want: "env: invalid option -- 'z'\nTry 'env --help' for more information.\n2\nwhich: invalid option -- 'z'\nTry 'which --help' for more information.\n2\n"},
		{name: "realpath usage", files: files, script: "realpath; realpath -z", want: "realpath: missing operand\nTry 'realpath --help' for more information.\nrealpath: invalid option -- 'z'\nTry 'realpath --help' for more information.\n", status: 1},
		{name: "sha256sum and base64 usage", files: files, script: "sha256sum -z; base64 -z; base64 nope", want: "sha256sum: invalid option -- 'z'\nTry 'sha256sum --help' for more information.\nbase64: invalid option -- 'z'\nTry 'base64 --help' for more information.\nbase64: nope: No such file or directory\n", status: 1},
		{name: "ls usage", files: files, script: "ls --bogus", want: "ls: unrecognized option '--bogus'\nTry 'ls --help' for more information.\n", status: 2},
		{name: "ls -t", files: files, script: "ls -t dir", want: "b.txt\n"},
		{name: "cat usage", files: files, script: "cat < /dev/null; cat /dev/null", want: ""},
		{name: "date -d forms", files: files, script: "date -d '2026-03-04 05:06' +%R; date -d @x", want: "05:06\ndate: invalid date '@x'\n", status: 1},
	}, nil)
}
