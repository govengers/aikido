package shell

import "testing"

// reviewCases reproduce the scripts of the review findings on the text
// commands through Shell.Run, with GNU's output and status.
var reviewCases = []scriptCase{
	{name: "grep multibyte literal", script: `printf 'é\n' | grep 'é'`, want: "é\n"},
	{name: "sed multibyte literal", script: `echo café | sed 's/é/e/'`, want: "cafe\n"},
	{name: "sed escaped multibyte literal", script: `echo café | sed 's/\é/e/'`, want: "cafe\n"},
	{name: "sed multibyte starred", script: `echo 'aééb' | sed 's/é*b/X/'`, want: "aX\n"},
	{name: "grep multibyte collating element", script: `printf 'é\n' | grep '[[=é=]]'`, want: "é\n"},
	{name: "find multibyte name", files: map[string]string{"café.txt": ""}, script: `find . -name 'caf\é*'; find . -name 'café*'`, want: "./café.txt\n./café.txt\n"},
	{name: "tr repeat of a high byte", script: `printf ab | tr ab '[\351*2]' | base64`, want: "6ek=\n"},
	{name: "grep non-UTF-8 pattern", script: `echo x | grep -F $'\xe9'`, want: "grep: pattern is not valid UTF-8\n", status: 2},
	{name: "grep interval above 255", script: `printf 'xxx\n' | grep -cE 'x{300}'; printf '%0300d\n' 0 | tr 0 x | grep -cE '^x{300}$'`, want: "0\n1\n", status: 0},
	{name: "sed -E leading star", script: `echo a | sed -E 's/*a/x/'`, want: "sed: -e expression #1, char 7: Invalid preceding regular expression\n", status: 1},
	{name: "sed -E leading plus in group", script: `echo a | sed -E 's/(+a)/x/'`, want: "sed: -e expression #1, char 9: Invalid preceding regular expression\n", status: 1},
	{name: "grep -E leading star repeats nothing", script: `echo a | grep -E '*a'`, want: "a\n"},
	{name: "sed hex escape in replacement", script: `echo a | sed 's/a/\x41/'`, want: "A\n"},
	{name: "sed hex escape in regex", script: `echo 'x61 a' | sed 's/\x61/b/'`, want: "x61 b\n"},
	{name: "sed decimal octal control escapes", script: `echo abc | sed 's/a/\d066/; s/b/\o103/; s/c/[\cI]/' | tr '\t' T`, want: "BC[T]\n"},
	{name: "sed escape yields a literal", script: `echo 'a*b aab' | sed 's/a\x2a/X/'`, want: "Xb aab\n"},
	{name: "sed y with hex escape", script: `echo abc | sed 'y/\x61b/xy/'`, want: "xyc\n"},
	{name: "sed newline in a bracket", script: `printf 'line1\nline2\n' | sed 'N;s/[^\n]*\n//'`, want: "line2\n"},
	{name: "sed tab in a bracket", script: `printf 'a\tb\n' | sed 's/[\t]/T/'`, want: "aTb\n"},
	{name: "sed other backslash in a bracket", script: `printf 'a\\b\n' | sed 's/[\\]/S/'`, want: "aSb\n"},
	{name: "grep backslash n in a bracket", script: `printf 'n\n' | grep '[\n]'`, want: "n\n"},
	{name: "sed M flag dot stops at newline", script: `printf 'a\nb\n' | sed 'N;s/a.*/X/M'`, want: "X\nb\n"},
	{name: "sed M flag negated bracket", script: `printf 'a\nb\n' | sed 'N;s/a[^x]*/X/M'`, want: "X\nb\n"},
	{name: "sed M flag anchors", script: `printf 'a\nb\n' | sed 'N;s/^b$/B/M'`, want: "a\nB\n"},
	{name: "sed without M dot spans newline", script: `printf 'a\nb\n' | sed 'N;s/a.*/X/'`, want: "X\n"},
	{name: "sed word start", script: `echo foo | sed 's/\</X/g'`, want: "Xfoo\n"},
	{name: "sed word end", script: `echo foo | sed 's/\>/X/g'`, want: "fooX\n"},
	{name: "sed word edges", script: `echo 'foo bar' | sed 's/\</[/g; s/\>/]/g'`, want: "[foo] [bar]\n"},
	{name: "sed word end shortens the match", script: `echo 'ab cd-' | sed 's/\<a.*\>/X/'`, want: "X-\n"},
	{name: "sed anchored word start", script: `echo 'a a' | sed 's/^\<a/X/g'`, want: "X a\n"},
	{name: "sed word start group numbering", script: `echo 'ab' | sed 's/\<\(a\)\(b\)/\2\1/'`, want: "ba\n"},
	{name: "grep word start", script: `echo 'xfoo foo' | grep -o '\<foo'`, want: "foo\n"},
	{name: "grep word end", script: `printf 'foox\nfoo\n' | grep 'foo\>'`, want: "foo\n"},
	{name: "grep -w keeps the line start", script: `printf 'ab c\n' | grep -w '^.'`, want: "", status: 1},
	{name: "grep -w keeps the word boundary", script: `printf 'xa a\n' | grep -ow '\ba'`, want: "a\n"},
	{name: "grep -w later word", script: `printf 'foobar foo\n' | grep -w foo`, want: "foobar foo\n"},
	{name: "grep include after exclude wins", files: map[string]string{"a.go": "x\n", "b.txt": "x\n"}, script: `grep -r --exclude='*' --include='*.go' x .`, want: "./a.go:x\n"},
	{name: "grep exclude after include wins", files: map[string]string{"a.go": "x\n", "b.txt": "x\n"}, script: `grep -rl --include='*.go' --exclude='a*' x .; echo $?`, want: "1\n"},
	{name: "grep unmatched file with leading exclude", files: map[string]string{"a.go": "x\n", "b.txt": "x\n"}, script: `grep -rl --exclude='*.go' --include='*.md' x .`, want: "./b.txt\n"},
	{name: "sed interval above the cap", script: `echo x | sed 's/x\{1001\}//'`, want: "sed: -e expression #1, char 13: Regular expression too big\n", status: 1},
}

func TestReviewFixes(t *testing.T) {
	runScriptCases(t, reviewCases, nil)
}
