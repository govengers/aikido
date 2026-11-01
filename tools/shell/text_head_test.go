package shell

import (
	"math"
	"testing"
)

var sliceFiles3 = map[string]string{"a.txt": "a\nb\nc\n", "b.txt": "x\ny", "d/f.txt": "f\n"}

var headCases = []textCase{
	{name: "head default", files: map[string]string{"n.txt": "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"}, script: "head n.txt", stdout: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"},
	{
		name: "head -n2 headers and missing file", files: sliceFiles3, script: "head -n2 a.txt nope b.txt",
		stdout: "==> a.txt <==\na\nb\n\n==> b.txt <==\nx\ny",
		stderr: "head: cannot open 'nope' for reading: No such file or directory\n", exit: 1,
	},
	{name: "head obsolete -2", files: sliceFiles3, script: "head -2 a.txt", stdout: "a\nb\n"},
	{name: "head -n -1 keeps all but last", files: sliceFiles3, script: "head -n -1 b.txt", stdout: "x\n"},
	{name: "head -c 3", files: sliceFiles3, script: "head -c 3 a.txt", stdout: "a\nb"},
	{name: "head -c -2", files: sliceFiles3, script: "head -c -2 a.txt", stdout: "a\nb\n"},
	{name: "head -n 2K suffix", files: sliceFiles3, script: "head -n 2K a.txt", stdout: "a\nb\nc\n"},
	{name: "head invalid count", files: sliceFiles3, script: "head -n 1x a.txt", stderr: "head: invalid number of lines: '1x'\n", exit: 1},
	{
		name: "head directory", files: sliceFiles3, script: "head d a.txt",
		stdout: "==> d <==\n\n==> a.txt <==\na\nb\nc\n", stderr: "head: error reading 'd': Is a directory\n", exit: 1,
	},
	{name: "head stdin -q -v", stdin: "s1\ns2\n", script: "head -v -n1", stdout: "==> standard input <==\ns1\n"},
	{name: "head -q", files: sliceFiles3, script: "head -q -n1 a.txt b.txt", stdout: "a\nx\n"},
	{name: "head invalid option", script: "head -Q", stderr: "head: invalid option -- 'Q'\nTry 'head --help' for more information.\n", exit: 1},
	{name: "head pipeline", files: sliceFiles3, script: "head -n 3 a.txt | head -n 1", stdout: "a\n"},
}

var tailCases = []textCase{
	{name: "tail -n 1 without newline", files: sliceFiles3, script: "tail -n 1 b.txt", stdout: "y"},
	{name: "tail obsolete +2", files: sliceFiles3, script: "tail +2 a.txt", stdout: "b\nc\n"},
	{name: "tail obsolete -2", files: sliceFiles3, script: "tail -2 a.txt", stdout: "b\nc\n"},
	{name: "tail -n +2 headers", files: sliceFiles3, script: "tail -n +2 a.txt b.txt", stdout: "==> a.txt <==\nb\nc\n\n==> b.txt <==\ny"},
	{name: "tail -c 2", files: sliceFiles3, script: "tail -c 2 b.txt", stdout: "\ny"},
	{name: "tail -c +3", files: sliceFiles3, script: "tail -c +3 a.txt", stdout: "b\nc\n"},
	{name: "tail -n 0", files: sliceFiles3, script: "tail -n 0 a.txt", stdout: ""},
	{name: "tail more than there is", files: sliceFiles3, script: "tail -n 99 a.txt", stdout: "a\nb\nc\n"},
	{name: "tail stdin", stdin: "1\n2\n3\n", script: "tail -n2", stdout: "2\n3\n"},
	{name: "tail missing file", script: "tail nope", stderr: "tail: cannot open 'nope' for reading: No such file or directory\n", exit: 1},
	{name: "tail invalid count", files: sliceFiles3, script: "tail -n x a.txt", stderr: "tail: invalid number of lines: 'x'\n", exit: 1},
	{
		name: "tail unsupported -f", files: sliceFiles3, script: "tail -f a.txt",
		stderr: "tail: invalid option -- 'f'\nTry 'tail --help' for more information.\n", exit: 1, gnuSkip: "tail -f follows; not supported here",
	},
}

var wcFiles = map[string]string{"a.txt": "a\nb\nc\n", "b.txt": "x\ny", "w.txt": "one two\n  three\tfour  \n\nfive", "d/f.txt": "f\n"}

var wcCases = []textCase{
	{name: "wc one file", files: wcFiles, script: "wc a.txt", stdout: "3 3 6 a.txt\n"},
	{name: "wc -l one file", files: wcFiles, script: "wc -l a.txt", stdout: "3 a.txt\n"},
	{name: "wc two files total", files: wcFiles, script: "wc w.txt a.txt", stdout: " 3  5 28 w.txt\n 3  3  6 a.txt\n 6  8 34 total\n"},
	{name: "wc -lc", files: wcFiles, script: "wc -cl w.txt", stdout: " 3 28 w.txt\n"},
	{name: "wc -m", files: wcFiles, script: "wc -m w.txt", stdout: "28 w.txt\n"},
	{name: "wc -L", files: wcFiles, script: "wc -L w.txt", stdout: "14 w.txt\n"},
	{
		name: "wc directory", files: wcFiles, script: "wc d a.txt",
		stdout: "      0       0       0 d\n      3       3       6 a.txt\n      3       3       6 total\n", stderr: "wc: d: Is a directory\n", exit: 1,
	},
	{name: "wc missing file", files: wcFiles, script: "wc -l nope a.txt", stdout: "3 a.txt\n3 total\n", stderr: "wc: nope: No such file or directory\n", exit: 1},
	{name: "wc -w three files", files: wcFiles, script: "wc -w a.txt b.txt w.txt", stdout: " 3 a.txt\n 2 b.txt\n 5 w.txt\n10 total\n"},
	{name: "wc stdin", stdin: "hi\n", script: "wc", stdout: "      1       1       3\n"},
	{name: "wc -l stdin", stdin: "hi\n", script: "wc -l", stdout: "1\n"},
	{name: "wc -l dash", stdin: "hi\n", script: "wc -l -", stdout: "1 -\n"},
	{name: "wc dash and file", files: wcFiles, stdin: "hi\n", script: "wc - a.txt", stdout: "      1       1       3 -\n      3       3       6 a.txt\n      4       4       9 total\n"},
	{name: "wc dev null", script: "wc /dev/null", stdout: "      0       0       0 /dev/null\n"},
	{name: "wc invalid option", script: "wc -z", stderr: "wc: invalid option -- 'z'\nTry 'wc --help' for more information.\n", exit: 1},
	{name: "wc pipeline", files: wcFiles, script: "head -n 2 a.txt | wc -l", stdout: "2\n"},
}

func TestParseCount(t *testing.T) {
	for _, testCase := range []struct {
		text string
		want int
		ok   bool
	}{
		{"10", 10, true},
		{"2K", 2048, true},
		{"1kB", 1000, true},
		{"3b", 1536, true},
		{"", 0, false},
		{"x", 0, false},
		{"1x", 0, false},
		{"99999999999999999999999", math.MaxInt, true},
	} {
		got, ok := parseCount(testCase.text)
		if got != testCase.want || ok != testCase.ok {
			t.Errorf("parseCount(%q) = %d, %v; want %d, %v", testCase.text, got, ok, testCase.want, testCase.ok)
		}
	}
}
