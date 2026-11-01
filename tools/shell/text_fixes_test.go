package shell

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/mount"
)

// reviewCases reproduce the scripts of the review findings on the text
// commands through Shell.Run, with GNU's output and status.
var reviewCases = []scriptCase{
	{name: "grep multibyte literal", script: `printf 'é\n' | grep 'é'`, want: "é\n"},
	{name: "sed multibyte literal", script: `echo café | sed 's/é/e/'`, want: "cafe\n"},
	{name: "sed escaped multibyte literal", script: `echo café | sed 's/\é/e/'`, want: "cafe\n"},
	{name: "sed multibyte starred", script: `echo 'aééb' | sed 's/é*b/X/'`, want: "aX\n"},
	{name: "grep multibyte collating element", script: `printf 'é\n' | grep '[[=é=]]'`, want: "é\n"},
	{name: "find multibyte name", files: map[string]string{"café.txt": ""}, script: `find . -name 'caf\é*'; find . -name 'café*'`, want: "./café.txt\n./café.txt\n"},
	{name: "tr lone bracket", script: `echo 'a[' | tr '[' x; echo 'a[' | tr -d '['; echo 'a[b' | tr 'a[' '[x'`, want: "ax\na\n[xb\n"},
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
	{name: "sed escaped delimiter in a negated bracket", script: `echo 'a\b/c' | sed 's/[^\/]*/X/'`, want: "X/c\n"},
	{name: "sed escaped delimiter in a bracket", script: `echo 'a\b/c' | sed 's/[\/]/X/g'; echo 'a|b' | sed 's|[\|]|X|'`, want: "a\\bXc\naXb\n"},
	{name: "sed backslash closes a bracket", script: `echo 'a]b\]' | sed 's/[\]]/X/g'`, want: "a]bX\n"},
	{name: "grep backslash n in a bracket", script: `printf 'n\n' | grep '[\n]'`, want: "n\n"},
	{name: "sed M flag dot stops at newline", script: `printf 'a\nb\n' | sed 'N;s/a.*/X/M'`, want: "X\nb\n"},
	{name: "sed M flag negated bracket", script: `printf 'a\nb\n' | sed 'N;s/a[^x]*/X/M'`, want: "X\nb\n"},
	{name: "sed M flag anchors", script: `printf 'a\nb\n' | sed 'N;s/^b$/B/M'`, want: "a\nB\n"},
	{name: "sed without M dot spans newline", script: `printf 'a\nb\n' | sed 'N;s/a.*/X/'`, want: "X\n"},
	{name: "sed word start", script: `echo foo | sed 's/\</X/g'`, want: "Xfoo\n"},
	{name: "sed word end", script: `echo foo | sed 's/\>/X/g'`, want: "fooX\n"},
	{name: "sed word edges", script: `echo 'foo bar' | sed 's/\</[/g; s/\>/]/g'`, want: "[foo] [bar]\n"},
	{name: "sed word end shortens the match", script: `echo 'ab cd-' | sed 's/\<a.*\>/X/'`, want: "X-\n"},
	{name: "sed word start in a failing alternative", script: `echo ab | sed 's/a\<b\|ab/X/'`, want: "X\n"},
	{name: "sed word start loses to an equal alternative", script: `echo 'a .' | sed 's/a\< \|a /X/'; echo 'A .' | sed 's/a\< \|a /X/I'`, want: "X.\nX.\n"},
	{name: "grep word start loses to an equal alternative", script: `echo 'a b' | grep -o 'a\< b\|a b'`, want: "a b\n"},
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
	{name: "grep -o keeps the group separator", files: map[string]string{"c.txt": "1\n2\nm\n4\n5\n6\n7\nm\n9\n"}, script: `grep -o -A1 m c.txt`, want: "m\n--\nm\n"},
	{name: "grep -o adjacent context needs no separator", files: map[string]string{"c.txt": "m\nx\nm\n"}, script: `grep -o -A1 m c.txt`, want: "m\nm\n"},
	{name: "grep -C0 separates groups", files: map[string]string{"c.txt": "1\n2\nm\n4\n5\n6\n7\nm\n9\n"}, script: `grep -C0 m c.txt`, want: "m\n--\nm\n"},
	{name: "grep -m0 exits before reading", files: map[string]string{"a.txt": "x\n"}, script: `grep -c -m0 x a.txt; echo $?; grep -L -m0 x a.txt nope; echo $?`, want: "1\n1\n"},
	{name: "sed empty regex without a previous one", script: `echo a | sed 's//b/'; echo $?`, want: "sed: -e expression #1, char 0: no previous regular expression\n1\n"},
	{name: "sed empty address without a previous one", script: `echo a | sed -n '//p'; echo $?`, want: "sed: -e expression #1, char 0: no previous regular expression\n1\n"},
	{name: "sed empty regex reuses the last one", script: `echo abc | sed '/b/s//X/'`, want: "aXc\n"},
	{name: "sed Q drops queued text", script: `printf 'x\n' | sed -e '1a foo' -e '1Q'`, want: ""},
	{name: "sed q prints queued text", script: `printf 'x\n' | sed -e '1a foo' -e '1q'`, want: "x\nfoo\n"},
	{name: "sed a backslash continues into the next -e", script: `seq 3 | sed -e '2a\' -e hello`, want: "1\n2\nhello\n3\n"},
	{name: "sed a text continues into the next -e", script: `seq 2 | sed -e '1a foo\' -e bar`, want: "1\nfoo\nbar\n2\n"},
	{name: "sed a backslash at the very end", script: `seq 1 | sed -e '1a\'`, want: "sed: -e expression #1, char 3: expected \\ after `a', `c' or `i'\n", status: 1},
	{name: "sed -i keeps Latin-1 bytes", files: map[string]string{"f": "caf\xe9\n"}, script: `sed -i 's/.*/[&]/' f; base64 f`, want: "W2NhZuldCg==\n"},
	{name: "sed case conversion keeps Latin-1 bytes", script: `printf 'caf\351\n' | sed 's/.*/\U&/' | base64`, want: "Q0FG6Qo=\n"},
	{name: "sed y keeps Latin-1 bytes", script: `printf 'caf\351\n' | sed 'y/a/A/' | base64`, want: "Y0Fm6Qo=\n"},
	{name: "wc -m across chunk boundaries", files: map[string]string{"e": strings.Repeat("é", 50000) + "\n"}, script: `cat e | wc -m -c`, want: "  50001  100001\n"},
	{name: "uniq -D streams groups", script: `printf 'a\na\nb\nc\nc\nc\n' | uniq -D`, want: "a\na\nc\nc\nc\n"},
	{name: "uniq -D -u prints nothing", script: `printf 'a\na\nb\n' | uniq -D -u`, want: ""},
	{name: "sort -o onto a directory", files: map[string]string{"a.txt": "b\na\n", "d/x": ""}, script: `sort -o d a.txt; echo $?; ls d`, want: "sort: open failed: d: Is a directory\n2\nx\n"},
	{name: "sort -o to /dev/stdout", files: map[string]string{"a.txt": "b\na\n"}, script: `sort -o /dev/stdout a.txt; ls /dev/stdout 2>/dev/null; find . -path ./dev`, want: "a\nb\n/dev/stdout\n"},
	{name: "sort -o without parent", files: map[string]string{"a.txt": "b\na\n"}, script: `sort -o nodir/f a.txt; echo $?`, want: "sort: open failed: nodir/f: No such file or directory\n2\n"},
	{name: "uniq OUTPUT onto a directory", files: map[string]string{"a.txt": "a\na\n", "d/x": ""}, script: `uniq a.txt d; echo $?`, want: "uniq: d: Is a directory\n1\n"},
	{name: "uniq OUTPUT to /dev/null", files: map[string]string{"a.txt": "a\na\n"}, script: `uniq a.txt /dev/null; find . -path ./dev`, want: ""},
	{name: "awk print onto a directory", files: map[string]string{"d/x": ""}, script: `env -i awk 'BEGIN{print "x" > "d"}'; echo $?; ls d`, want: "awk: can't redirect to `d' (Is a directory)\n2\nx\n"},
	{name: "awk print to /dev/null", script: `env -i awk 'BEGIN{print "x" > "/dev/null"; print "y" > "/dev/stdout"}'; find . -path ./dev`, want: "y\n"},
	{name: "awk print without parent", script: `env -i awk 'BEGIN{print "x" > "nodir/f"}'; echo $?`, want: "awk: can't redirect to `nodir/f' (No such file or directory)\n2\n"},
	{name: "awk print stores the file", script: `env -i awk 'BEGIN{print "x" > "f"; print "y" >> "f"}'; cat f`, want: "x\ny\n"},
	{name: "diff -r sees a made directory", script: `mkdir -p a/empty b; diff -r a b; echo $?; mkdir b/empty; diff -r a b; echo $?`, want: "Only in a: empty\n1\n0\n"},
	{name: "sed interval above the cap", script: `echo x | sed 's/x\{1001\}//'`, want: "sed: -e expression #1, char 13: Regular expression too big\n", status: 1},
}

func TestReviewFixes(t *testing.T) {
	runScriptCases(t, reviewCases, nil)
}

// TestStreamingStopsEarly runs commands that know their answer early over
// an endless producer; the timeout turns a regression into a failure
// instead of a hang.
func TestStreamingStopsEarly(t *testing.T) {
	runScriptCases(t, []scriptCase{
		{name: "grep -q", script: `seq 1 1000000000000 | grep -q 1; echo $?`, want: "0\n"},
		{name: "grep -m", script: `seq 1 1000000000000 | grep -m2 1`, want: "1\n10\n"},
		{name: "grep -l", script: `seq 1 1000000000000 | grep -l 5`, want: "(standard input)\n"},
		{name: "grep -m with context", script: `seq 1 1000000000000 | grep -m1 -B1 -A1 5`, want: "4\n5\n6\n"},
		{name: "jq endless filter", script: `jq -n 'repeat(1)' | head -n 2`, want: "1\n1\n"},
		{name: "jq endless range", script: `jq -n 'range(1000000000000)' | head -n 2; echo done`, want: "0\n1\ndone\n"},
		{name: "sed q", script: `seq 1 1000000000000 | sed 3q`, want: "1\n2\n3\n"},
		{name: "sed Q with address", script: `seq 1 1000000000000 | sed -n '/^12$/{p;Q}'`, want: "12\n"},
	}, &Options{Timeout: 20 * time.Second})
}

// TestRunawayCommandsStop: loops without input (a sed branch, diff's
// search) are stopped by the timeout; growing spaces and output files by
// the input budget and the file cap, well within a generous timeout.
func TestRunawayCommandsStop(t *testing.T) {
	runScriptCases(t, []scriptCase{
		{name: "branch loop", script: `echo x | sed ':again;b again'`, want: "bash: timed out after 1s; the script was stopped\n", status: 124},
		{name: "substitute loop", script: `echo x | sed ':a;s/^/ /;ta'`, want: "bash: timed out after 1s; the script was stopped\n", status: 124},
		{name: "diff of disjoint files", files: disjointFiles(), script: `diff a b > /dev/null`, want: "bash: timed out after 1s; the script was stopped\n", status: 124},
	}, &Options{Timeout: time.Second})
	runScriptCases(t, []scriptCase{
		{name: "doubling hold space", script: `echo x | sed ':a;G;h;ba'; echo $?`, want: "sed: input exceeds 67108864 bytes\n2\n"},
		{name: "endless append queue", script: `echo x | sed -e ':a;a foo' -e 'ba'; echo $?`, want: "sed: input exceeds 67108864 bytes\n2\n"},
		{
			name: "sed -i result cap", files: map[string]string{"f": "x\n"},
			script: "sed -i '" + strings.Repeat("h;G;", 20) + strings.Repeat("p;", 40) + "' f; echo $?; cat f",
			want:   "sed: couldn't edit f: File too large\n4\nx\n",
		},
		{name: "awk output file cap", script: `env -i awk 'BEGIN{while(1) print "0123456789abcdef0123456789abcdef" > "f"}'; echo $?; ls`, want: "awk: File too large\n2\nf\n"},
	}, &Options{Timeout: time.Minute})
}

// TestJqSummaryNamesSortedKeys: jq reorders object keys, which the model
// must read before it rewrites a file with jq.
func TestJqSummaryNamesSortedKeys(t *testing.T) {
	if summary := jqCommand().Summary; !strings.Contains(summary, "object keys always sorted") {
		t.Fatalf("jq summary = %q", summary)
	}
	runScriptCases(t, []scriptCase{
		{name: "keys sorted", script: `echo '{"b":1,"a":2}' | jq -c .`, want: "{\"a\":2,\"b\":1}\n"},
	}, nil)
}

// disjointFiles are two files without a common line, diff's worst case.
func disjointFiles() map[string]string {
	var left, right strings.Builder
	for index := range 40000 {
		fmt.Fprintf(&left, "a%d\n", index)
		fmt.Fprintf(&right, "b%d\n", index)
	}
	return map[string]string{"a": left.String(), "b": right.String()}
}

// TestCommandWritesFailLoudly: an output file a command cannot store makes
// the command fail before && decides (I4).
func TestCommandWritesFailLoudly(t *testing.T) {
	base := memory.New()
	if err := base.WriteFile(context.Background(), "f.txt", []byte("a\n"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	storage, err := mount.New(&mount.Options{Mounts: []mount.Mount{
		{Prefix: "", Storage: memory.New()},
		{Prefix: "ro", Storage: base, ReadOnly: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	shell, err := New(&Options{Storage: storage})
	if err != nil {
		t.Fatal(err)
	}
	for script, want := range map[string]string{
		`env -i awk 'BEGIN {print "x" > "ro/file"}' && echo stored; echo $?`: "awk: can't redirect to `ro/file' (Read-only file system)\n2\n",
		`sed -i s/a/b/ ro/f.txt && echo stored; echo $?; cat ro/f.txt`:       "sed: couldn't edit ro/f.txt: Read-only file system\n4\na\n",
	} {
		if result := mustRun(t, shell, script); result.Output != want {
			t.Errorf("Run(%q) = %q, want %q", script, result.Output, want)
		}
	}
}

// TestInputBudget pipes more than maximumInputBytes to commands that hold
// their input, and lets the streaming ones run over the same pipe.
func TestInputBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("pipes 70 MB")
	}
	// 70000 lines of 1000 bytes: over the budget, with few lines to process.
	line := strings.Repeat("x", 999) + "\n"
	big := map[string]string{"big": strings.Repeat(line, 70_000) + "end\n"}
	runScriptCases(t, []scriptCase{
		{name: "sort refuses", files: big, script: `cat big | sort; echo $?`, want: "sort: input exceeds 67108864 bytes\n2\n"},
		{name: "xargs refuses", files: big, script: `cat big | xargs echo; echo $?`, want: "xargs: input exceeds 67108864 bytes\n2\n"},
		{name: "head -n -1 refuses", files: big, script: `cat big | head -n -1; echo $?`, want: "head: input exceeds 67108864 bytes\n2\n"},
		{name: "jq refuses", files: big, script: `cat big | jq .; echo $?`, want: "jq: input exceeds 67108864 bytes\n2\n"},
		{name: "tail streams lines", files: big, script: `cat big | tail -n 2 | cut -c1-3`, want: "xxx\nend\n"},
		{name: "tail streams bytes", files: big, script: `cat big | tail -c 4`, want: "end\n"},
		{name: "tail +N streams", files: big, script: `cat big | tail -n +70000 | cut -c1-3`, want: "xxx\nend\n"},
		{name: "head -c streams", files: big, script: `cat big | head -c 4`, want: "xxxx"},
		{name: "wc streams", files: big, script: `cat big | wc -l`, want: "70001\n"},
		{name: "cut streams", files: big, script: `cat big | cut -c1 | tail -n 1`, want: "e\n"},
		{name: "sed streams", files: big, script: `cat big | sed -n '$p'`, want: "end\n"},
		{name: "sed N across the stream", files: big, script: `cat big | sed -n '$!N;$p'`, want: "end\n"},
		{name: "uniq streams", files: big, script: `cat big | uniq -c | cut -c1-12`, want: "  70000 xxxx\n      1 end\n"},
	}, nil)
}

// TestGrepBeforeContextBudget: -B holds its context within the input
// budget instead of growing with an endless producer (the review used
// seq 1 1000000000000; long lines reach the budget sooner).
func TestGrepBeforeContextBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("pipes 64 MiB")
	}
	lines := map[string]string{"f": strings.Repeat(strings.Repeat("x", 999)+"\n", 1000)}
	runScriptCases(t, []scriptCase{
		{name: "huge -B", files: lines, script: `while cat f; do :; done | grep -B 1000000000 NEVER; echo $?`, want: "grep: input exceeds 67108864 bytes\n2\n"},
	}, &Options{Timeout: time.Minute})
}

// TestGrepStopsOnWriteError: grep stops at the first failed write, not at
// the end of its input.
func TestGrepStopsOnWriteError(t *testing.T) {
	runScriptCases(t, []scriptCase{
		{name: "closed pipe", script: `seq 1 1000000000000 | grep . | head -1`, want: "1\n"},
	}, &Options{Timeout: 20 * time.Second})
}

// TestWholeInputCommandsStream: base64 and sha256sum stream an endless
// input instead of reading it whole; bash reads a script from a pipe
// within the input budget.
func TestWholeInputCommandsStream(t *testing.T) {
	runScriptCases(t, []scriptCase{
		{name: "base64 encodes as it reads", script: `seq 1 1000000000000 | base64 | head -c 8`, want: "MQoyCjMK"},
		{name: "base64 decodes as it reads", script: `seq 1 1000000000000 | base64 | base64 -d | head -n 2`, want: "1\n2\n"},
		{name: "base64 of nothing prints no line", script: `printf '' | base64 | cat -n; printf '' | base64 -w0 | wc -c`, want: "0\n"},
		{name: "sha256sum of several inputs", files: map[string]string{"a": "abc"}, script: `printf abc | sha256sum - a`, want: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  -\nba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  a\n"},
	}, &Options{Timeout: 20 * time.Second})
	runScriptCases(t, []scriptCase{
		{name: "sha256sum of an endless pipe", script: `seq 1 1000000000000 | sha256sum`, want: "bash: timed out after 1s; the script was stopped\n", status: 124},
	}, &Options{Timeout: time.Second})
	if testing.Short() {
		return
	}
	lines := map[string]string{"f": strings.Repeat(strings.Repeat("x", 999)+"\n", 1000)}
	runScriptCases(t, []scriptCase{
		{name: "bash script beyond the budget", files: lines, script: `while cat f; do :; done | bash; echo $?`, want: "bash: input exceeds 67108864 bytes\n2\n"},
	}, &Options{Timeout: time.Minute})
}
