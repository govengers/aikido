package shell

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/vfs/memory"
)

// scriptCase is one script with the combined output and exit status it
// must produce, for behaviour the differential tests cannot pin down
// (clock, ls -l, host-independent paths, refusals).
type scriptCase struct {
	name   string
	files  map[string]string
	script string
	want   string
	status int
}

func runScriptCases(t *testing.T, cases []scriptCase, options *Options) {
	t.Helper()
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			shell, _ := newTestShell(t, testCase.files, options)
			result := mustRun(t, shell, testCase.script)
			if result.Output != testCase.want || result.ExitCode != testCase.status {
				t.Fatalf("Run(%q)\n got %q, exit %d\nwant %q, exit %d", testCase.script, result.Output, result.ExitCode, testCase.want, testCase.status)
			}
		})
	}
}

func TestFileCommandCases(t *testing.T) {
	files := map[string]string{"a.txt": "alpha\n", "dir/b.txt": "beta\n", "dir/deep/c.txt": "gamma\n"}
	runScriptCases(t, []scriptCase{
		{name: "absolute paths are workspace paths", files: files, script: "cd /dir && cat /a.txt && ls /", want: "alpha\na.txt\ndir\n"},
		{name: "cd to an absolute workspace path", files: files, script: "cd /dir/deep && pwd && cd / && pwd", want: "/dir/deep\n/\n"},
		{name: "cd too many arguments", files: files, script: "cd a b", want: "bash: line 1: cd: too many arguments\n", status: 1},
		{name: "cd home and back", files: files, script: "cd dir; cd; pwd; cd -; cd ~; pwd", want: "/\n/dir\n/\n"},
		{name: "ls -S and -r", files: map[string]string{"one": "1", "three": "333", "two": "22"}, script: "ls -S; ls -Sr", want: "three\ntwo\none\none\ntwo\nthree\n"},
		{name: "ls -F", files: files, script: "ls -F", want: "a.txt\ndir/\n"},
		{name: "rm -rf root refuses", files: files, script: "rm -rf /; ls", want: "rm: it is dangerous to operate recursively on '/'\nrm: use --no-preserve-root to override this failsafe\na.txt\ndir\n"},
		{name: "rm -v", files: files, script: "rm -rv dir a.txt", want: "removed 'dir/b.txt'\nremoved 'dir/deep/c.txt'\nremoved directory 'dir'\nremoved 'a.txt'\n"},
		{name: "rm a device", files: files, script: "rm /dev/null", want: "rm: cannot remove '/dev/null': Permission denied\n", status: 1},
		{name: "rmdir the root", files: files, script: "rmdir /", want: "rmdir: failed to remove '/': Device or resource busy\n", status: 1},
		{name: "mkdir -p over a file", files: files, script: "mkdir -p a.txt/x; mkdir -p a.txt", want: "mkdir: cannot create directory 'a.txt/x': Not a directory\nmkdir: cannot create directory 'a.txt': File exists\n", status: 1},
		{name: "mkdir then ls shows the empty directory", files: files, script: "mkdir empty; ls; ls empty; rmdir empty; ls", want: "a.txt\ndir\nempty\na.txt\ndir\n"},
		{name: "mv -v and -n", files: files, script: "mv -v a.txt x.txt; cp x.txt y.txt; echo new > y.txt; mv -n x.txt y.txt; cat y.txt", want: "renamed 'a.txt' -> 'x.txt'\nnew\n"},
		{name: "mv a directory into itself", files: files, script: "mv dir dir/deep", want: "mv: cannot move 'dir' to a subdirectory of itself, 'dir/deep/dir'\n", status: 1},
		{name: "mv a directory onto a file", files: files, script: "mv dir a.txt", want: "mv: cannot overwrite non-directory 'a.txt' with directory 'dir'\n", status: 1},
		{name: "mv into a non-empty directory", files: map[string]string{"x/f": "1", "y/x/g": "2"}, script: "mv x y", want: "mv: cannot move 'x' to 'y/x': Directory not empty\n", status: 1},
		{name: "mv keeps empty directories", files: files, script: "mkdir -p dir/empty; mv dir moved; ls moved", want: "b.txt\ndeep\nempty\n"},
		{name: "cp -v, -n and into itself", files: files, script: "cp -v a.txt c.txt; cp -n dir/b.txt c.txt; cat c.txt; cp -r dir dir/deep", want: "'a.txt' -> 'c.txt'\nalpha\ncp: cannot copy a directory, 'dir', into itself, 'dir/deep/dir'\n", status: 1},
		{name: "cp onto a directory name inside a directory", files: map[string]string{"a": "1", "d/a/x": "2"}, script: "cp a d", want: "cp: cannot overwrite directory 'd/a' with non-directory\n", status: 1},
		{name: "touch -c", files: files, script: "touch -c new; ls", want: "a.txt\ndir\n"},
		{name: "tee to devices", files: files, script: "echo x | tee /dev/null /dev/stderr", want: "x\nx\n"},
		{name: "cat /dev/stdin", files: files, script: "echo piped | cat /dev/stdin", want: "piped\n"},
		{name: "redirect to /dev/stdout and /dev/stderr", files: files, script: "echo out > /dev/stdout; echo err > /dev/stderr", want: "out\nerr\n"},
		{name: "redirect read of a directory", files: files, script: "cat < dir", want: "bash: dir: Is a directory\n", status: 1},
		{name: "find -exec with a missing command", files: files, script: "find a.txt -exec nosuch {} \\;; echo $?", want: "find: 'nosuch': No such file or directory\n0\n"},
		{name: "find -exec + failing", files: files, script: "find a.txt -exec false {} +; echo $?", want: "1\n"},
		{name: "find -delete a directory", files: files, script: "find dir -delete; ls", want: "a.txt\n"},
		{name: "find -delete a non-empty directory", files: files, script: "find dir -name dir -delete; echo $?", want: "find: cannot delete 'dir': Directory not empty\n1\n"},
		{name: "find bad expressions", files: files, script: "find . -name; find . -type x; find . -maxdepth x; find . \\( -name a; find . -name a \\); find . -exec echo", want: "find: missing argument to `-name'\nfind: Unknown argument to -type: x\nfind: Expected a positive decimal integer argument to -maxdepth, but got `x'\nfind: invalid expression; I was expecting to find a ')' somewhere but did not see one\nfind: invalid expression; you have too many ')'\nfind: missing argument to `-exec'\n", status: 1},
		{name: "find -type l and -true -false", files: files, script: "find . -type l; find . -false; find a.txt -true", want: "a.txt\n"},
		{name: "find -empty on files", files: map[string]string{"empty": "", "full": "x"}, script: "find . -type f -empty", want: "./empty\n"},
		{name: "find /dev", files: files, script: "find /dev/null -type c", want: "/dev/null\n"},
		{name: "find patterns", files: map[string]string{"a[1].txt": "", "b.TXT": "", "c.go": ""}, script: "find . -name 'a\\[1\\]*'; find . -iname '*.txt' -name '[!a]*'; find . -name '[ab]*' -name '*.go'", want: "./a[1].txt\n./b.TXT\n"},
	}, nil)
}

func TestUtilityCommandCases(t *testing.T) {
	files := map[string]string{"a.txt": "alpha\n", "dir/b.txt": "beta\n"}
	runScriptCases(t, []scriptCase{
		{name: "which", files: files, script: "which cat echo nosuch; echo $?", want: "cat\necho\n1\n"},
		{name: "command -v and type", files: files, script: "command -v ls find; type ls; type -t ls; command -v cd; command -v nosuch; echo $?", want: "ls\nfind\nls is a shell builtin\nbuiltin\ncd\n1\n"},
		{name: "env -u", files: files, script: "export A=1 B=2; env -u A | cat", want: "B=2\nHOME=/\n"},
		{name: "env runs builtins", files: files, script: "env X=1 printf '%s\\n' hi", want: "hi\n"},
		{name: "xargs -t and -P", files: files, script: "echo a b | xargs -t -P 4 echo", want: "echo a b\na b\n"},
		{name: "xargs status 255 stops", files: files, script: "printf 'a\\nb\\n' | xargs -n1 sh -c 'echo $1; exit 255' sh; echo $?", want: "a\nxargs: sh: exited with status 255; aborting\n124\n"},
		{name: "xargs -d", files: files, script: "printf 'a,b,c' | xargs -d , -n 2 echo", want: "a b\nc\n"},
		{name: "xargs bad options", files: files, script: "xargs -n 0 echo; xargs -d ab echo; echo \"'a\" | xargs echo; xargs --bogus", want: "xargs: value 0 for -n option should be >= 1\nxargs: invalid input delimiter specification ab: the delimiter must be a single character\nxargs: unmatched single quote; by default quotes are special to xargs unless you use the -0 option\nxargs: unrecognized option '--bogus'\nTry 'xargs --help' for more information.\n", status: 1},
		{name: "date conversions", files: files, script: "date +%%%n%t%Z%z%:z%h%c; date +%Q", want: "%\n\tUTC+0000+00:00JanThu Jan  1 00:00:00 1970\ndate: the format conversion %Q is not supported\n", status: 1},
		{name: "date options", files: files, script: "date; date -Ihours; date -Iminutes; date -Ins; date -d now +%s; date -d '2026-03-04T05:06:07Z' +%T; date -d bad; date -Ibad; date x", want: "Thu Jan  1 00:00:00 UTC 1970\n1970-01-01T00+00:00\n1970-01-01T00:00+00:00\n1970-01-01T00:00:00,000000000+00:00\n0\n05:06:07\ndate: invalid date 'bad'\ndate: invalid argument 'bad' for '--iso-8601'\ndate: extra operand 'x'\nTry 'date --help' for more information.\n", status: 1},
		{name: "date pm and 12-hour", files: files, script: "date -d '2026-03-04 13:05:00' '+%I %l %p'; date -d '2026-03-04 00:05:00' +%I", want: "01  1 PM\n12\n"},
		{name: "realpath -e and relative-to", files: files, script: "realpath -e dir/nope; realpath --relative-to=dir/x a.txt; realpath --relative-to=dir dir; realpath -s a.txt", want: "realpath: dir/nope: No such file or directory\n../../a.txt\n.\n/a.txt\n", status: 0},
		{name: "realpath under a file", files: files, script: "realpath a.txt/x; echo $?", want: "realpath: a.txt/x: Not a directory\n1\n"},
		{name: "base64 -i and bad wrap", files: files, script: "printf 'aGV*sbG8=' | base64 -d -i; echo; base64 -w x; base64 a b", want: "hello\nbase64: invalid wrap size: 'x'\nbase64: extra operand 'b'\nTry 'base64 --help' for more information.\n", status: 1},
		{name: "bash reads a script file and stdin", files: map[string]string{"run.sh": "echo \"script $1\"\nexit 4\n"}, script: "bash run.sh arg; echo $?; echo 'echo from stdin' | sh; bash -e -o pipefail -c 'false | true; echo no'; echo $?", want: "script arg\n4\nfrom stdin\n1\n"},
		{name: "bash errors", files: files, script: "bash -c; bash -z; bash nope.sh; bash -c 'echo ('; bash -o", want: "bash: -c: option requires an argument\nbash: -z: invalid option\nbash: nope.sh: No such file or directory\nbash: 1:1: `foo(` must be followed by `)`\nbash: -o: option requires an argument\n", status: 2},
		{name: "path commands", files: map[string]string{"run.sh": "echo hi\n", "dir/x": ""}, script: "./run.sh; echo $?; ./dir; echo $?; ./nope; echo $?", want: "bash: line 1: ./run.sh: Permission denied (no execute bit in the workspace; run it with: bash ./run.sh)\n126\nbash: line 1: ./dir: Is a directory\n126\nbash: line 1: ./nope: No such file or directory\n127\n"},
		{name: "test -x", files: files, script: "[ -x a.txt ] || echo not-executable; [ -x dir ] && echo searchable; [ -r a.txt ] && echo readable", want: "not-executable\nsearchable\nreadable\n"},
		{name: "source", files: map[string]string{"lib.sh": "greeting=hello\n"}, script: "source lib.sh; echo $greeting; . nope.sh", want: "hello\nsource: bash: ./nope.sh: No such file or directory\n", status: 1},
		{name: "pwd -P", files: files, script: "cd dir; pwd -P", want: "/dir\n"},
		{name: "cd -P and -L (review S25)", files: files, script: "cd -P dir && pwd; cd -L .. && pwd; cd -P -- dir && pwd", want: "/dir\n/\n/dir\n"},
		{name: "sleep is cancelled by the timeout", files: files, script: "sleep 1m", want: "bash: timed out after 50ms; the script was stopped\n", status: 124},
	}, &Options{Commands: []Command{dateCommand(func() time.Time { return time.Unix(0, 0) })}, Timeout: 50 * time.Millisecond})
}

// TestBenchmarkCommands replays commands models ran in the coding benchmark
// (benchmark/results/coding-vsh-*) that failed in the virtual shell.
func TestBenchmarkCommands(t *testing.T) {
	lines := map[string]string{"LINES.md": "pkg/a.go: 12\npkg/b.go: 30\n"}
	runScriptCases(t, []scriptCase{
		{
			name: "awk with an exported environment", files: lines,
			script: `awk -F': ' '{s+=$2} END{print "total: " s}' LINES.md; awk 'BEGIN {print ENVIRON["HOME"]}'`,
			want:   "total: 42\n/\n",
		},
		{
			name: "grep -rlZ into xargs -0 sed -i", files: map[string]string{"a.go": "api.acme-legacy.io\n", "my docs/b.md": "x api.acme-legacy.io\n", "c.txt": "none\n"},
			script: `grep -rlZ 'api\.acme-legacy\.io' . | xargs -0 sed -i 's/api\.acme-legacy\.io/api.acme.dev/g' && grep -rn 'api\.acme' . | sort`,
			want:   "./a.go:1:api.acme.dev\n./my docs/b.md:1:x api.acme.dev\n",
		},
		{
			name: "cat -A to check indentation", files: map[string]string{"top.go": "package top\n\nfunc f() {\n\treturn\n}\n"},
			script: "cat -A top.go | tail -2", want: "^Ireturn$\n}$\n",
		},
		{
			name: "diff of two process substitutions", files: lines,
			script: `diff <(cat LINES.md) LINES.md && echo MATCH; diff <(sort -r LINES.md) <(sort LINES.md) | head -1`,
			want:   "MATCH\n1d0\n",
		},
		{
			name: "while read from a process substitution", files: map[string]string{"pkg/a.go": "", "pkg/b.go": "", "c.txt": ""},
			script: `n=0; while IFS= read -r f; do n=$((n+1)); echo "$f"; done < <(find . -name '*.go'); echo "$n files"`,
			want:   "./pkg/a.go\n./pkg/b.go\n2 files\n",
		},
	}, nil)
}

// TestProcessSubstitution covers <(list) in every place the interpreter
// parses a script: the script, functions, nested bash, eval, source, trap.
func TestProcessSubstitution(t *testing.T) {
	runScriptCases(t, []scriptCase{
		{name: "path and empty list", script: "echo <(true) <()", want: "/dev/fd/63 /dev/null\n"},
		{name: "nested", script: "cat <(cat <(echo inner))", want: "inner\n"},
		{name: "in a function and a loop", script: `f() { cat <(echo "f $1"); }; for i in 1 2; do f $i; done`, want: "f 1\nf 2\n"},
		{name: "exit stays inside", script: "cat <(echo a; exit 3; echo b); echo $?", want: "a\n0\n"},
		{name: "stderr passes through", script: "cat <(echo err >&2) 2>/dev/null; cat <(nosuch)", want: "bash: line 1: nosuch: command not found\n"},
		{name: "nested bash", script: `bash -c 'wc -l < <(printf "a\nb\n")'`, want: "2\n"},
		{name: "eval", script: `eval 'cat <(echo from eval)'`, want: "from eval\n"},
		// The review's S2 script: mvdan created a host FIFO for it and the
		// unopened FIFO hung the cleanup.
		{name: "eval without a reader", script: `eval 'echo <(true)'; source /dev/null`, want: "/dev/fd/63\n"},
		{name: "source with arguments", files: map[string]string{"lib.sh": "cat <(echo \"sourced $1\")\n"}, script: "source lib.sh one; . ./lib.sh two", want: "sourced one\nsourced two\n"},
		{name: "trap", script: `trap -- 'cat <(echo trapped)' ERR; false; trap - ERR; false`, want: "trapped\n", status: 1},
		{name: "files stay out of listings and the workspace", script: "cat <(echo x) >/dev/null; ls /; ls /dev/fd; find . | wc -l", want: "1\n"},
		{name: "write side refused in eval", script: "eval 'tee >(cat)'; echo $?", want: "bash: eval: 1:5: process substitution >(...) is not supported; write to a file in /tmp and run the command on it\n2\n"},
	}, nil)
}

// TestCatDisplayOptions pins GNU cat's notation; the differential cases
// over the same files compare it with GNU when a reference is available.
func TestCatDisplayOptions(t *testing.T) {
	runScriptCases(t, []scriptCase{
		{name: "-A", files: catFiles(), script: "cat -A show.txt", want: "a^Ib^A^?$\nM-iM-^IM-^@M-^?M-^I^M$\n"},
		{name: "-E shows CRLF", files: catFiles(), script: "cat -E crlf.txt; cat -T crlf.txt", want: "x^M$\ny\rz^M$\nx\r\ny\rz\r\n"},
		{name: "-t keeps line ends", files: catFiles(), script: "cat -t show.txt", want: "a^Ib^A^?\nM-iM-^IM-^@M-^?M-^I^M\n"},
		{name: "-s", files: catFiles(), script: "cat -s blank.txt", want: "\nfirst\n\nsecond\n\n"},
		{name: "-b", files: catFiles(), script: "cat -b blank.txt", want: "\n\n     1\tfirst\n\n\n\n     2\tsecond\n\n"},
		{name: "-sn", files: catFiles(), script: "cat -sn blank.txt", want: "     1\t\n     2\tfirst\n     3\t\n     4\tsecond\n     5\t\n"},
		{name: "-n continues a line across files", files: catFiles(), script: "cat -n part.txt crlf.txt", want: "     1\tno newlinex\r\n     2\ty\rz\r\n"},
		{name: "-s across files", files: map[string]string{"a": "a\n\n", "b": "\n\nb\n"}, script: "cat -s a b", want: "a\n\nb\n"},
		{name: "long options", files: catFiles(), script: "cat --number-nonblank --squeeze-blank --show-ends blank.txt", want: "$\n     1\tfirst$\n$\n     2\tsecond$\n$\n"},
	}, nil)
}

func TestLsLong(t *testing.T) {
	shell, _ := newTestShell(t, map[string]string{"big": strings.Repeat("x", 5000), "dir/small": "s"}, nil)
	mustRun(t, shell, "cp /dev/null empty")
	result := mustRun(t, shell, "ls -l; ls -lh big; ls -la dir")
	pattern := regexp.MustCompile(`^total 12
-rw-r--r-- 1 user user 5000 \w{3} [ \d]\d \d\d:\d\d big
drwxr-xr-x 2 user user 4096 \w{3} [ \d]\d \d\d:\d\d dir
-rw-r--r-- 1 user user    0 \w{3} [ \d]\d \d\d:\d\d empty
-rw-r--r-- 1 user user 4\.9K \w{3} [ \d]\d \d\d:\d\d big
total 12
drwxr-xr-x 2 user user 4096 .+ \.
drwxr-xr-x 2 user user 4096 .+ \.\.
-rw-r--r-- 1 user user    1 .+ small
$`)
	if !pattern.MatchString(result.Output) {
		t.Fatalf("ls -l output:\n%s", result.Output)
	}
	old := listing{now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	if got := old.timestamp(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)); got != "Jan  2  2025" {
		t.Fatalf("old timestamp = %q", got)
	}
}

func TestHumanSize(t *testing.T) {
	testCases := map[int64]string{0: "0", 1023: "1023", 1024: "1.0K", 1536: "1.5K", 10239: "10K", 10240: "10K", 1 << 20: "1.0M", 5 << 30: "5.0G"}
	for bytes, want := range testCases {
		if got := humanSize(bytes); got != want {
			t.Errorf("humanSize(%d) = %q; want %q", bytes, got, want)
		}
	}
}

func TestDateUsesTheInjectedClock(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 11, 1, 10, 30, 5, 0, time.FixedZone("CET", 3600)) }
	shell, _ := newTestShell(t, nil, &Options{Commands: []Command{dateCommand(clock)}})
	if result := mustRun(t, shell, "date; date +%F_%T_%Z; date -u +%s"); result.Output != "Sun Nov  1 09:30:05 UTC 2026\n2026-11-01_09:30:05_UTC\n1793525405\n" {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestBuiltinsOutsideAShell(t *testing.T) {
	storage := memory.New()
	if err := storage.WriteFile(context.Background(), "a.txt", []byte("alpha\n"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	commands := map[string]Command{}
	for _, command := range Builtins() {
		commands[command.Name] = command
	}
	var stdout, stderr strings.Builder
	invocation := &Invocation{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Storage: storage}
	invocation.Arguments = []string{"a.txt"}
	if err := commands["cat"].Run(context.Background(), invocation); err != nil || stdout.String() != "alpha\n" {
		t.Fatalf("cat outside a shell: %v, %q", err, stdout.String())
	}
	invocation.Arguments = []string{"echo"}
	if err := commands["xargs"].Run(context.Background(), invocation); err == nil || !strings.Contains(stderr.String(), "xargs: echo: No such file or directory") {
		t.Fatalf("xargs outside a shell: %v, %q", err, stderr.String())
	}
	invocation.Arguments = []string{"-c", "true"}
	if err := commands["bash"].Run(context.Background(), invocation); err == nil {
		t.Fatal("bash outside a shell succeeded")
	}
}
