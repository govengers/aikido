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
}

func TestReviewFixes(t *testing.T) {
	runScriptCases(t, reviewCases, nil)
}
