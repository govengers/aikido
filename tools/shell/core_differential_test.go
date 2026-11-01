package shell

import "testing"

// coreFiles is the workspace most core cases start from.
func coreFiles() map[string]string {
	return map[string]string{
		"a.txt":      "alpha\n",
		"b.txt":      "beta\nbeta two\n",
		"sub/c.md":   "gamma\n",
		"notes/n.md": "no newline at the end",
	}
}

// chainFiles has one entry per directory, so find's order is the same in
// GNU (readdir order) and here (sorted).
func chainFiles() map[string]string {
	return map[string]string{"top/middle/leaf.txt": "leaf\n"}
}

func TestDifferentialShellSemantics(t *testing.T) {
	files := coreFiles()
	runDifferential(t, []differentialCase{
		{name: "pipe", files: files, script: "printf 'b\\na\\n' | cat -n"},
		{name: "and or", files: files, script: "false && echo no || echo yes; true && echo ok"},
		{name: "for loop", files: files, script: `for i in 1 2 3; do echo "item $i"; done`},
		{name: "while read from a redirect", files: files, script: `while read -r line; do echo "<$line>"; done < b.txt`},
		{name: "command substitution", files: files, script: `n=$(seq 4 | xargs echo); echo "got: $n"; echo "$(cat a.txt)!"`},
		{name: "redirect truncates", files: files, script: "echo one > out.txt; echo two > out.txt; cat out.txt"},
		{name: "redirect appends", files: files, script: "echo one > out.txt; echo two >> out.txt; cat out.txt"},
		{name: "stderr into stdout", files: files, script: `cat missing.txt 2>&1; echo "status $?"`},
		{name: "stdin redirect", files: files, script: "cat < b.txt"},
		{name: "globs", files: files, script: "echo *.txt; echo sub/*; echo */*.md"},
		{name: "glob without match stays literal", files: files, script: "echo *.none"},
		{name: "cd and pwd", files: files, script: "cd sub && pwd && cd .. && pwd"},
		{name: "exit status", files: files, script: "bash -c 'exit 7'; echo $?"},
		{name: "exit ends the script", files: files, script: "echo before; exit 3; echo after"},
		{name: "command not found", files: files, script: "nosuchcommand arg; echo $?"},
		{name: "set -e stops", files: files, script: "set -e; echo one; false; echo two"},
		{name: "set -e spares conditions", files: files, script: "set -e; if false; then echo no; fi; false || echo handled; echo end"},
		{name: "here-doc expands", files: files, script: "name=world\ncat <<EOF\nhello $name\n  indented\nEOF"},
		{name: "quoted here-doc is literal", files: files, script: "cat <<'EOF'\n$HOME `x`\nEOF"},
		{name: "here-string", files: files, script: `cat <<< "here string"`},
		{name: "dev null", files: files, script: "echo hidden > /dev/null; cat /dev/null; cat a.txt 2>/dev/null >/dev/null; echo end"},
		{name: "tmp scratch", files: files, script: "echo scratch > /tmp/differential-a; cat /tmp/differential-a; ls /tmp/differential-a"},
		{name: "functions", files: files, script: `greet() { echo "hi $1"; return 4; }; greet bob; echo $?`},
		{name: "arithmetic and case", files: files, script: "x=$((3 * 4 + 1)); case $x in 13) echo thirteen;; *) echo other;; esac"},
		{name: "test operators", files: files, script: "[ -f a.txt ] && echo file; [ -d sub ] && echo dir; [ -e nope ] || echo none; test -s a.txt && echo nonempty"},
		{name: "pipeline status", files: files, script: "false | true; echo $?; set -o pipefail; false | true; echo $?"},
		{name: "subshell keeps variables", files: files, script: "x=1; (x=2; echo $x); echo $x"},
		{name: "redirect into a missing directory", files: files, script: "echo x > nodir/f; echo $?", ignoreStderr: true},
		{name: "redirect onto a directory", files: files, script: "echo x > sub; echo $?", ignoreStderr: true},
		{name: "cd to a missing directory", files: files, script: "cd nosuch; echo $?; cd a.txt; echo $?"},
		{name: "read with IFS", files: files, script: `echo "a:b:c" | { IFS=: read -r x y z; echo "$y"; }`},
		{name: "cat stdin between files", files: files, script: "echo middle | cat a.txt - a.txt"},
		{name: "append in a loop", files: files, script: `for f in *.txt; do cat "$f" >> all.out; done; cat all.out`},
		{name: "background job and wait", files: files, script: "(sleep 0.1; echo late) & echo early; wait; echo done"},
		{name: "bash -c positional", files: files, script: `bash -c 'echo "$1-$2"' zero one two`},
		{name: "variables are not exported by default", files: files, script: `x=1; export y=2; bash -c 'echo "[$x][$y]"'`},
	})
}

// catFiles hold what cat's display options show: control bytes, high
// bytes, tabs, CRLF line ends and runs of blank lines.
func catFiles() map[string]string {
	return map[string]string{
		"show.txt":  "a\tb\x01\x7f\n\xe9\x89\x80\xff\x89\r\n",
		"crlf.txt":  "x\r\ny\rz\r\n",
		"blank.txt": "\n\nfirst\n\n\n\nsecond\n\n",
		"part.txt":  "no newline",
	}
}

func TestDifferentialFileCommands(t *testing.T) {
	files := coreFiles()
	runDifferential(t, []differentialCase{
		{name: "cat -n numbers across files", files: files, script: "cat -n a.txt notes/n.md b.txt"},
		{name: "cat missing file", files: files, script: "cat nope a.txt; echo $?"},
		{name: "cat directory", files: files, script: "cat sub; echo $?"},
		{name: "cat unknown option", files: files, script: "cat --nope; echo $?; cat -z; echo $?"},
		{name: "cat -A", files: catFiles(), script: "cat -A show.txt; cat -e show.txt; cat -t show.txt; cat -vET show.txt"},
		{name: "cat -E and -T alone", files: catFiles(), script: "cat -E show.txt; cat -T show.txt; cat --show-ends --show-tabs crlf.txt"},
		{name: "cat -s -b -n", files: catFiles(), script: "cat -s blank.txt; cat -b blank.txt; cat -sn blank.txt; cat -bn blank.txt; cat -sb blank.txt"},
		{name: "cat state across files", files: catFiles(), script: "cat -n part.txt blank.txt; cat -s blank.txt blank.txt; cat -u part.txt"},
		{name: "mkdir -p and cp into it", files: files, script: "mkdir -p out/deep && cp a.txt out/deep/ && ls -R out"},
		{name: "mkdir existing", files: files, script: "mkdir sub; echo $?; mkdir a.txt; echo $?"},
		{name: "mkdir missing parent", files: files, script: "mkdir x/y; echo $?; mkdir -p x/y && ls x"},
		{name: "mkdir -v", files: files, script: "mkdir -pv p/q"},
		{name: "rm files", files: files, script: "rm a.txt; ls; rm nope; echo $?; rm -f nope; echo $?"},
		{name: "rm directory without -r", files: files, script: "rm sub; echo $?"},
		{name: "rm -rf directory", files: files, script: "rm -rf sub notes; ls"},
		{name: "rm refuses dot", files: files, script: "rm -rf .; echo $?; ls"},
		{name: "rm missing operand", files: files, script: "rm; echo $?"},
		{name: "rmdir", files: files, script: "mkdir e; rmdir e; ls; rmdir sub; echo $?; rmdir nope; echo $?"},
		{name: "mv rename", files: files, script: "mv a.txt renamed.txt; ls; cat renamed.txt"},
		{name: "mv into a directory", files: files, script: "mv a.txt b.txt sub/; ls; ls sub"},
		{name: "mv directory", files: files, script: "mv sub moved; ls; ls moved"},
		{name: "mv errors", files: files, script: "mv nope x; echo $?; mv a.txt; echo $?; mv a.txt b.txt notadir; echo $?"},
		{name: "cp file", files: files, script: "cp a.txt copy.txt; cat copy.txt; cp a.txt sub; ls sub"},
		{name: "cp -r directory", files: files, script: "cp -r sub copy; ls copy; cp -r sub copy; ls copy"},
		{name: "cp directory without -r", files: files, script: "cp sub x; echo $?"},
		{name: "cp missing source", files: files, script: "cp nope x; echo $?"},
		{name: "cp same file", files: files, script: "cp a.txt a.txt; echo $?"},
		{name: "trailing slash targets", files: files, script: "cp a.txt out/; echo $?; mv a.txt out/; echo $?; cp b.txt a.txt/; touch t/; touch a.txt/; echo x > r/; ls"},
		{name: "touch", files: files, script: "touch new.txt a.txt; ls; cat a.txt; touch nodir/x; echo $?"},
		{name: "tee", files: files, script: "echo data | tee t1 t2; cat t1 t2; echo more | tee -a t1 >/dev/null; cat t1"},
		{name: "ls operands", files: files, script: "ls; ls sub a.txt notes"},
		{name: "ls -a and -A", files: map[string]string{".hidden": "h\n", "shown": "s\n"}, script: "ls; ls -a; ls -A"},
		{name: "ls missing operand", files: files, script: "ls nope; echo $?"},
		{name: "ls -R", files: files, script: "ls -R"},
		{name: "ls -d and -p", files: files, script: "ls -d sub; ls -p; ls -1"},
		{name: "ls invalid option", files: files, script: "ls -z; echo $?"},
		{name: "find a chain", files: chainFiles(), script: "find top; find top -type f; find top -type d -name middle"},
		{name: "find depth", files: chainFiles(), script: "find . -maxdepth 2; find . -mindepth 3"},
		{name: "find not and or", files: chainFiles(), script: "find . -not -name '*.txt' -type d; find . \\( -name leaf.txt -o -name middle \\) -print"},
		{name: "find prune", files: chainFiles(), script: "find . -name middle -prune -o -print"},
		{name: "find exec", files: chainFiles(), script: "find . -name '*.txt' -exec cat {} \\; ; find top -type f -exec echo found {} +"},
		{name: "find -path", files: chainFiles(), script: "find . -path './top/*' -name '*.txt'; find . -ipath '*MIDDLE*' -type d"},
		{name: "find print0", files: chainFiles(), script: "find top -type f -print0 | xargs -0 echo"},
		{name: "find missing start", files: chainFiles(), script: "find nope; echo $?"},
		{name: "find unknown predicate", files: chainFiles(), script: "find . -bogus; echo $?"},
		{name: "find empty and delete", files: chainFiles(), script: "mkdir top/empty; find . -empty; find . -name leaf.txt -delete; find top/middle"},
		{name: "find sorted", files: files, script: "find . | sort; find . -name '*.md' | sort", requires: []string{"sort"}},
	})
}

func TestDifferentialUtilities(t *testing.T) {
	files := coreFiles()
	runDifferential(t, []differentialCase{
		{name: "xargs default echo", files: files, script: "printf 'a b\\nc\\n' | xargs; printf 'a b\\nc\\n' | xargs echo got"},
		{name: "xargs -n", files: files, script: "seq 5 | xargs -n 2 echo"},
		{name: "xargs -I", files: files, script: "printf 'a\\nb\\n' | xargs -I{} echo 'item {} done'"},
		{name: "xargs -0", files: files, script: "printf 'x y\\0z\\0' | xargs -0 -n1 echo"},
		{name: "xargs -r", files: files, script: "printf '' | xargs -r echo none; printf '' | xargs echo empty"},
		{name: "xargs quotes", files: files, script: `echo "'a b' \"c d\" e\\ f" | xargs -n1 echo`},
		{name: "xargs -L", files: files, script: "printf 'a b\\nc\\nd e\\n' | xargs -L 2 echo"},
		{name: "xargs status", files: files, script: "printf 'a\\nb\\n' | xargs -n1 sh -c 'exit 1'; echo $?"},
		{name: "xargs missing command", files: files, script: "echo a | xargs nosuch; echo $?"},
		{name: "xargs into sh -c", files: files, script: `printf 'a.txt\nb.txt\n' | xargs -I{} sh -c 'echo "[{}]"; cat {}'`},
		{name: "basename and dirname", files: files, script: "basename /a/b/c.txt .txt; basename /a/b/; basename /; basename -a x/y z/w; basename -s .go a.go b.go; dirname /a/b/c; dirname c; dirname /; dirname a/b/ x//y"},
		{name: "basename missing operand", files: files, script: "basename; echo $?; dirname; echo $?"},
		{name: "realpath", files: files, script: "cd sub && realpath ../a.txt c.md && realpath -m nope/x && realpath --relative-to=.. c.md && realpath nope/x; echo $?"},
		{name: "seq", files: files, script: "seq 3; seq 2 5; seq 10 -3 1; seq -s, 3; seq -w 8 11; seq 5 1; echo end"},
		{name: "seq errors", files: files, script: "seq; echo $?; seq 1 0 5; echo $?; seq x; echo $?"},
		{name: "date", files: files, script: "date -u -d @86400 +%Y-%m-%dT%H:%M:%S%z; date -d @0 '+%a %A %b %B %e %d %j %s %y %C %u %w %D %F %T %R %p %I %k %l %N'; date -u -d @0; date -d @1700000000 -Iseconds; date -d @0 -I; date -d @0 -R"},
		{name: "date parses ISO", files: files, script: "date -d 2026-03-04 +%F_%T; date -d '2026-03-04 05:06:07' +%s"},
		{name: "base64", files: files, script: "printf 'hello world' | base64; printf 'aGVsbG8=' | base64 -d; echo; base64 a.txt; seq 40 | base64; seq 40 | base64 -w 0; echo; printf '' | base64 | cat -n"},
		{name: "base64 invalid", files: files, script: "printf '!!!' | base64 -d; echo $?"},
		{name: "sha256sum", files: files, script: "sha256sum a.txt b.txt; printf abc | sha256sum; sha256sum nope; echo $?"},
		{name: "sleep", files: files, script: "sleep 0.05; echo slept; sleep 0.01s; sleep x; echo $?; sleep; echo $?"},
		{name: "env -i with variables", files: files, script: "env -i A=1 B=2 env; env -i A=1 bash -c 'echo $A'"},
		{name: "env missing command", files: files, script: "env nosuch; echo $?"},
	})
}
