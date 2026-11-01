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
	{name: "sed interval above the cap", script: `echo x | sed 's/x\{1001\}//'`, want: "sed: -e expression #1, char 13: Regular expression too big\n", status: 1},
}

func TestReviewFixes(t *testing.T) {
	runScriptCases(t, reviewCases, nil)
}
