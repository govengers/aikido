package shell

import "testing"

var sortFiles = map[string]string{
	"s.txt": "b 2\na 10\nB 1\nc 2\na 10\n",
	"n.txt": "10\n-2\n3.5\nabc\n\n-0\n0\n 7\n1e3\n",
	"t.txt": "x:3:b\ny:1:a\nz:2:c\nw:1:b\n",
	"d/f":   "f\n",
}

var sortCases = []textCase{
	{name: "sort bytes", files: sortFiles, script: "sort s.txt", stdout: "B 1\na 10\na 10\nb 2\nc 2\n"},
	{name: "sort -k 2", files: sortFiles, script: "sort -k 2 s.txt", stdout: "B 1\na 10\na 10\nb 2\nc 2\n"},
	{name: "sort -k 2,2n", files: sortFiles, script: "sort -k 2,2n s.txt", stdout: "B 1\nb 2\nc 2\na 10\na 10\n"},
	{name: "sort -k2n -k1,1", files: sortFiles, script: "sort -k2n -k1,1 s.txt", stdout: "B 1\nb 2\nc 2\na 10\na 10\n"},
	{name: "sort -t space -k2,2nr", files: sortFiles, script: "sort -t ' ' -k2,2nr s.txt", stdout: "a 10\na 10\nb 2\nc 2\nB 1\n"},
	{name: "sort -u", files: sortFiles, script: "sort -u s.txt", stdout: "B 1\na 10\nb 2\nc 2\n"},
	{name: "sort -f", files: sortFiles, script: "sort -f s.txt", stdout: "a 10\na 10\nB 1\nb 2\nc 2\n"},
	{name: "sort -fu", files: sortFiles, script: "sort -fu s.txt", stdout: "a 10\nB 1\nb 2\nc 2\n"},
	{name: "sort -n", files: sortFiles, script: "sort -n n.txt", stdout: "-2\n\n-0\n0\nabc\n1e3\n3.5\n 7\n10\n"},
	{name: "sort -rn", files: sortFiles, script: "sort -rn n.txt", stdout: "10\n 7\n3.5\n1e3\nabc\n0\n-0\n\n-2\n"},
	{name: "sort -s -k1,1f", files: sortFiles, script: "sort -s -k1,1f s.txt", stdout: "a 10\na 10\nb 2\nB 1\nc 2\n"},
	{name: "sort -t: -k2,2 -k3", files: sortFiles, script: "sort -t: -k2,2 -k3 t.txt", stdout: "y:1:a\nw:1:b\nz:2:c\nx:3:b\n"},
	{name: "sort -t: -k3.1,3.1r", files: sortFiles, script: "sort -t: -k3.1,3.1r t.txt", stdout: "z:2:c\nw:1:b\nx:3:b\ny:1:a\n"},
	{name: "sort -r", files: sortFiles, script: "sort -r s.txt", stdout: "c 2\nb 2\na 10\na 10\nB 1\n"},
	{name: "sort -b with leading blanks", stdin: "  b\na\n c\n", script: "sort -b", stdout: "a\n  b\n c\n"},
	{name: "sort -k2b field with blanks", stdin: "x   b\ny a\nz  c\n", script: "sort -k2b", stdout: "y a\nx   b\nz  c\n"},
	{name: "sort -k2 counts leading blanks", stdin: "x   b\ny a\nz  c\n", script: "sort -k2", stdout: "x   b\nz  c\ny a\n"},
	{name: "sort two files", files: sortFiles, script: "sort s.txt t.txt", stdout: "B 1\na 10\na 10\nb 2\nc 2\nw:1:b\nx:3:b\ny:1:a\nz:2:c\n"},
	{name: "sort -o file", files: sortFiles, script: "sort -o out.txt s.txt", wantFiles: map[string]string{"out.txt": "B 1\na 10\na 10\nb 2\nc 2\n"}},
	{name: "sort -o same file", files: sortFiles, script: "sort -o s.txt s.txt", wantFiles: map[string]string{"s.txt": "B 1\na 10\na 10\nb 2\nc 2\n"}},
	{name: "sort -c disorder", files: sortFiles, script: "sort -c s.txt", stderr: "sort: s.txt:2: disorder: a 10\n", exit: 1},
	{name: "sort -c sorted", stdin: "a\nb\n", script: "sort -c"},
	{name: "sort no final newline", stdin: "b\na", script: "sort", stdout: "a\nb\n"},
	{name: "sort field zero", files: sortFiles, script: "sort -k 0 s.txt", stderr: "sort: field number is zero: invalid field specification '0'\n", exit: 2},
	{name: "sort char zero", files: sortFiles, script: "sort -k 1.0 s.txt", stderr: "sort: character offset is zero: invalid field specification '1.0'\n", exit: 2},
	{name: "sort end field zero", files: sortFiles, script: "sort -k 1,0 s.txt", stderr: "sort: field number is zero: invalid field specification '1,0'\n", exit: 2},
	{name: "sort bad key", files: sortFiles, script: "sort -k x s.txt", stderr: "sort: invalid number at field start: invalid count at start of 'x'\n", exit: 2},
	{name: "sort bad char", files: sortFiles, script: "sort -k2.x s.txt", stderr: "sort: invalid number after '.': invalid count at start of 'x'\n", exit: 2},
	{name: "sort stray", files: sortFiles, script: "sort -k 1,2,3 s.txt", stderr: "sort: stray character in field spec: invalid field specification '1,2,3'\n", exit: 2},
	{name: "sort multi-char tab", files: sortFiles, script: "sort -t ab s.txt", stderr: "sort: multi-character tab 'ab'\n", exit: 2},
	{name: "sort missing file", script: "sort nope", stderr: "sort: cannot read: nope: No such file or directory\n", exit: 2},
	{name: "sort directory", files: sortFiles, script: "sort d", stderr: "sort: read failed: d: Is a directory\n", exit: 2},
	{name: "sort missing key argument", script: "sort -n -k", stderr: "sort: option requires an argument -- 'k'\nTry 'sort --help' for more information.\n", exit: 2},
	{name: "sort invalid option", script: "sort -Z", stderr: "sort: invalid option -- 'Z'\nTry 'sort --help' for more information.\n", exit: 2},
	{name: "sort uniq pipeline", files: sortFiles, script: "cut -d ' ' -f1 s.txt | sort | uniq -c", stdout: "      1 B\n      2 a\n      1 b\n      1 c\n"},
}

var uniqFiles = map[string]string{"u.txt": "a\na\nb\nA\na\n", "f.txt": "x a\ny a\nz b\n", "d/f": "f\n"}

var uniqCases = []textCase{
	{name: "uniq plain", files: uniqFiles, script: "uniq u.txt", stdout: "a\nb\nA\na\n"},
	{name: "uniq -c", files: uniqFiles, script: "uniq -c u.txt", stdout: "      2 a\n      1 b\n      1 A\n      1 a\n"},
	{name: "uniq -d", files: uniqFiles, script: "uniq -d u.txt", stdout: "a\n"},
	{name: "uniq -u", files: uniqFiles, script: "uniq -u u.txt", stdout: "b\nA\na\n"},
	{name: "uniq -ic", files: uniqFiles, script: "uniq -ic u.txt", stdout: "      2 a\n      1 b\n      2 A\n"},
	{name: "uniq -f1", files: uniqFiles, script: "uniq -f1 f.txt", stdout: "x a\nz b\n"},
	{name: "uniq -s1", files: uniqFiles, script: "uniq -s1 f.txt", stdout: "x a\nz b\n"},
	{name: "uniq -w1", files: uniqFiles, script: "uniq -w1 f.txt", stdout: "x a\ny a\nz b\n"},
	{name: "uniq -D", files: uniqFiles, script: "uniq -D u.txt", stdout: "a\na\n"},
	{name: "uniq output file", files: uniqFiles, script: "uniq u.txt o.txt", wantFiles: map[string]string{"o.txt": "a\nb\nA\na\n"}},
	{name: "uniq stdin no final newline", stdin: "x\nx", script: "uniq", stdout: "x\n"},
	{name: "uniq bad field count", files: uniqFiles, script: "uniq -f x f.txt", stderr: "uniq: x: invalid number of fields to skip\n", exit: 1},
	{name: "uniq extra operand", script: "uniq a b c", stderr: "uniq: extra operand 'c'\nTry 'uniq --help' for more information.\n", exit: 1},
	{name: "uniq missing file", script: "uniq nope", stderr: "uniq: nope: No such file or directory\n", exit: 1},
	{name: "uniq directory", files: uniqFiles, script: "uniq d", stderr: "uniq: error reading 'd'\n", exit: 1},
}

var cutFiles = map[string]string{"c.txt": "a:b:c\nd:e\nnodelim\n", "tab.txt": "a\tb\tc\n", "d/f": "f\n"}

var cutCases = []textCase{
	{name: "cut -f2", files: cutFiles, script: "cut -d: -f2 c.txt", stdout: "b\ne\nnodelim\n"},
	{name: "cut -f2-", files: cutFiles, script: "cut -d: -f2- c.txt", stdout: "b:c\ne\nnodelim\n"},
	{name: "cut -f1,3", files: cutFiles, script: "cut -d: -f1,3 c.txt", stdout: "a:c\nd\nnodelim\n"},
	{name: "cut -s", files: cutFiles, script: "cut -d: -s -f2 c.txt", stdout: "b\ne\n"},
	{name: "cut --complement", files: cutFiles, script: "cut -d: --complement -f2 c.txt", stdout: "a:c\nd\nnodelim\n"},
	{name: "cut --output-delimiter fields", files: cutFiles, script: "cut -d: -f1,3 --output-delimiter=- c.txt", stdout: "a-c\nd\nnodelim\n"},
	{name: "cut -c2-3", files: cutFiles, script: "cut -c2-3 c.txt", stdout: ":b\n:e\nod\n"},
	{name: "cut -b -2", files: cutFiles, script: "cut -b -2 c.txt", stdout: "a:\nd:\nno\n"},
	{name: "cut -c 3,1 in order", files: cutFiles, script: "cut -c 3,1 c.txt", stdout: "ab\nde\nnd\n"},
	{name: "cut default tab", files: cutFiles, script: "cut -f2 tab.txt", stdout: "b\n"},
	{name: "cut empty delimiter", files: cutFiles, script: "cut -f 1- -d '' c.txt", stdout: "a:b:c\nd:e\nnodelim\n"},
	{name: "cut -n ignored", files: cutFiles, script: "cut -d: -f2 -n c.txt", stdout: "b\ne\nnodelim\n"},
	{name: "cut contiguous output delimiter", files: cutFiles, script: "cut -c2-3 --output-delimiter=- c.txt", stdout: ":b\n:e\nod\n"},
	{name: "cut split output delimiter", files: cutFiles, script: "cut -c1,3 --output-delimiter=- c.txt", stdout: "a-b\nd-e\nn-d\n"},
	{name: "cut stdin no final newline", stdin: "1,2", script: "cut -d, -f2", stdout: "2\n"},
	{name: "cut decreasing", files: cutFiles, script: "cut -f 3-1 c.txt", stderr: "cut: invalid decreasing range\nTry 'cut --help' for more information.\n", exit: 1},
	{name: "cut bad field", files: cutFiles, script: "cut -f x- c.txt", stderr: "cut: invalid field value 'x-'\nTry 'cut --help' for more information.\n", exit: 1},
	{name: "cut byte zero", files: cutFiles, script: "cut -b 0 c.txt", stderr: "cut: byte/character positions are numbered from 1\nTry 'cut --help' for more information.\n", exit: 1},
	{name: "cut field zero", files: cutFiles, script: "cut -f 0 c.txt", stderr: "cut: fields are numbered from 1\nTry 'cut --help' for more information.\n", exit: 1},
	{name: "cut no list", script: "cut", stderr: "cut: you must specify a list of bytes, characters, or fields\nTry 'cut --help' for more information.\n", exit: 1},
	{name: "cut two lists", files: cutFiles, script: "cut -c1 -f1 c.txt", stderr: "cut: only one list may be specified\nTry 'cut --help' for more information.\n", exit: 1},
	{name: "cut long delimiter", files: cutFiles, script: "cut -d ab -f1 c.txt", stderr: "cut: the delimiter must be a single character\nTry 'cut --help' for more information.\n", exit: 1},
	{name: "cut missing file", files: cutFiles, script: "cut -f1 nope c.txt", stdout: "a:b:c\nd:e\nnodelim\n", stderr: "cut: nope: No such file or directory\n", exit: 1},
	{name: "cut directory", files: cutFiles, script: "cut -f1 d", stderr: "cut: d: Is a directory\n", exit: 1},
}

var trCases = []textCase{
	{name: "tr upper", stdin: "hello World\n", script: "tr a-z A-Z", stdout: "HELLO WORLD\n"},
	{name: "tr classes", stdin: "hello World\n", script: "tr '[:lower:]' '[:upper:]'", stdout: "HELLO WORLD\n"},
	{name: "tr pad second set", stdin: "abcdef\n", script: "tr a-f xy", stdout: "xyyyyy\n"},
	{name: "tr -t truncates", stdin: "abcdef\n", script: "tr -t a-f xy", stdout: "xycdef\n"},
	{name: "tr -d", stdin: "a1b2c3\n", script: "tr -d 0-9", stdout: "abc\n"},
	{name: "tr -d class", stdin: "a1 b2\tc3\n", script: "tr -d '[:space:]'", stdout: "a1b2c3"},
	{name: "tr -s", stdin: "aaabbbccc  d\n", script: "tr -s 'a '", stdout: "abbbccc d\n"},
	{name: "tr -s translate", stdin: "a  b   c\n", script: "tr -s ' ' '\\n'", stdout: "a\nb\nc\n"},
	{name: "tr -c", stdin: "abc-123\n", script: "tr -c 'a-z\\n' X", stdout: "abcXXXX\n"},
	{name: "tr -cd", stdin: "abc-123\n", script: "tr -cd '0-9'", stdout: "123"},
	{name: "tr -ds", stdin: "aa11bb22\n", script: "tr -ds 0-9 a-z", stdout: "ab\n"},
	{name: "tr escapes", stdin: "a\tb\n", script: "tr '\\t' ,", stdout: "a,b\n"},
	{name: "tr octal", stdin: "a b\n", script: "tr '\\040' _", stdout: "a_b\n"},
	{name: "tr repeat", stdin: "abcd\n", script: "tr abcd '[x*2]yz'", stdout: "xxyz\n"},
	{name: "tr fill", stdin: "abcd\n", script: "tr abcd 'y[x*]'", stdout: "yxxx\n"},
	{name: "tr missing operand", script: "tr", stderr: "tr: missing operand\nTry 'tr --help' for more information.\n", exit: 1},
	{name: "tr one set translating", script: "tr a", stderr: "tr: missing operand after 'a'\nTwo strings must be given when translating.\nTry 'tr --help' for more information.\n", exit: 1},
	{name: "tr extra operand", script: "tr a b c", stderr: "tr: extra operand 'c'\nTry 'tr --help' for more information.\n", exit: 1},
	{name: "tr -d extra operand", script: "tr -d a b", stderr: "tr: extra operand 'b'\nOnly one string may be given when deleting without squeezing repeats.\nTry 'tr --help' for more information.\n", exit: 1},
	{name: "tr reverse range", script: "tr z-a x", stderr: "tr: range-endpoints of 'z-a' are in reverse collating sequence order\n", exit: 1},
	{name: "tr invalid option", script: "tr -z a b", stderr: "tr: invalid option -- 'z'\nTry 'tr --help' for more information.\n", exit: 1},
}

func TestParseCutList(t *testing.T) {
	for _, testCase := range []struct {
		list    string
		want    []cutRange
		message string
	}{
		{list: "1,3-4", want: []cutRange{{1, 1}, {3, 4}}},
		{list: "3,1-2", want: []cutRange{{1, 3}}},
		{list: "-2,5-", want: []cutRange{{1, 2}, {5, 1<<63 - 1}}},
		{list: "2-,4", want: []cutRange{{2, 1<<63 - 1}}},
		{list: "-", message: "invalid range with no endpoint: -"},
	} {
		got, message := parseCutList(testCase.list, true)
		if message != testCase.message || len(got) != len(testCase.want) {
			t.Errorf("parseCutList(%q) = %v, %q; want %v, %q", testCase.list, got, message, testCase.want, testCase.message)
			continue
		}
		for index := range got {
			if got[index] != testCase.want[index] {
				t.Errorf("parseCutList(%q)[%d] = %v, want %v", testCase.list, index, got[index], testCase.want[index])
			}
		}
	}
}

func TestCompareNumeric(t *testing.T) {
	for _, testCase := range []struct {
		left, right string
		want        int
	}{
		{"10", "9", 1},
		{"-1", "1", -1},
		{"-0", "0", 0},
		{"", "0", 0},
		{"1.50", "1.5", 0},
		{"-2", "-10", 1},
		{" 7", "7", 0},
		{"0.1", ".1", 0},
		{"abc", "-0.0", 0},
	} {
		if got := compareNumeric(testCase.left, testCase.right); got != testCase.want {
			t.Errorf("compareNumeric(%q, %q) = %d, want %d", testCase.left, testCase.right, got, testCase.want)
		}
	}
}
