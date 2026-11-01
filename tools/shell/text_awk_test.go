package shell

var awkFiles = map[string]string{
	"t.txt":    "alice 30 x\nbob 25 y\ncarol 35 z\n",
	"c.csv":    "a,1\nb,2\n",
	"prog.awk": "{ sum += $2 }\nEND { print sum }\n",
	"d/f":      "f\n",
}

// awkCases use POSIX awk only, so Debian's awk (mawk) can check them.
var awkCases = []textCase{
	{name: "awk print field", files: awkFiles, script: "awk '{print $1}' t.txt", stdout: "alice\nbob\ncarol\n"},
	{name: "awk pattern", files: awkFiles, script: "awk '$2 > 28 {print $1, $2}' t.txt", stdout: "alice 30\ncarol 35\n"},
	{name: "awk -F", files: awkFiles, script: "awk -F, '{print $2}' c.csv", stdout: "1\n2\n"},
	{name: "awk -F attached later", files: awkFiles, script: "awk -F , '{print $1 \"-\" $2}' c.csv", stdout: "a-1\nb-2\n"},
	{name: "awk -v", files: awkFiles, script: "awk -v n=2 'NR == n' t.txt", stdout: "bob 25 y\n"},
	{name: "awk -v escapes", script: "awk -v s='a\\tb' 'BEGIN {print s}'", stdout: "a\tb\n"},
	{name: "awk END sum", files: awkFiles, script: "awk '{s += $2} END {print s, NR}' t.txt", stdout: "90 3\n"},
	{name: "awk -f", files: awkFiles, script: "awk -f prog.awk t.txt", stdout: "90\n"},
	{name: "awk printf", files: awkFiles, script: "awk '{printf \"%-6s|%3d\\n\", $1, $2}' t.txt", stdout: "alice | 30\nbob   | 25\ncarol | 35\n"},
	{name: "awk NF and last field", files: awkFiles, script: "awk '{print NF, $NF}' t.txt", stdout: "3 x\n3 y\n3 z\n"},
	{name: "awk FILENAME FNR", files: awkFiles, script: "awk 'FNR == 1 {print FILENAME}' t.txt c.csv", stdout: "t.txt\nc.csv\n"},
	{name: "awk assignment operand", files: awkFiles, script: "awk '{print x, $1}' x=1 c.csv", stdout: "1 a,1\n1 b,2\n"},
	{name: "awk stdin", stdin: "3 4\n", script: "awk '{print $1 * $2}'", stdout: "12\n"},
	{name: "awk arrays", files: awkFiles, script: "awk '{n[$3]++} END {print n[\"x\"] + n[\"y\"]}' t.txt", stdout: "2\n"},
	{name: "awk gsub length", stdin: "hello\n", script: "awk '{gsub(/l/, \"L\"); print $0, length($0)}'", stdout: "heLLo 5\n"},
	{name: "awk exit code", script: "awk 'BEGIN {exit 3}'", exit: 3},
	{
		name: "awk redirect to file", files: awkFiles, script: "awk '{print $1 > \"names.txt\"}' t.txt",
		wantFiles: map[string]string{"names.txt": "alice\nbob\ncarol\n"},
	},
	{name: "awk getline from file", files: awkFiles, script: "awk 'BEGIN {while ((getline line < \"c.csv\") > 0) print \"got \" line}'", stdout: "got a,1\ngot b,2\n"},
	{name: "awk ENVIRON", script: "awk 'BEGIN {print ENVIRON[\"NOPE\"] \"|\"}'", stdout: "|\n"},
	{
		name: "awk missing file", files: awkFiles, script: "awk '{print}' nope", exit: 2,
		stderr: "awk: fatal: cannot open file `nope' for reading: No such file or directory\n", gnuSkip: "message is gawk's; Debian's mawk words it differently",
	},
	{
		name: "awk syntax error", script: "awk 'BEGIN {'", exit: 1, stderr: "awk: parse error at 1:8: expected } instead of EOF\n",
		gnuSkip: "parse error text is GoAWK's",
	},
	{
		name: "awk system refused", script: "awk 'BEGIN {system(\"ls\")}'", exit: 2, stderr: "awk: can't call system() due to NoExec\n",
		gnuSkip: "system() is not available in the virtual shell",
	},
	{name: "awk invalid option", script: "awk -z 1", exit: 2, stderr: "awk: invalid option -- 'z'\n" + awkUsage, gnuSkip: "usage text differs"},
	{name: "awk pipeline", files: awkFiles, script: "sort -k2n t.txt | awk 'NR == 1 {print $1}'", stdout: "bob\n"},
}
