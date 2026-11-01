package shell

// hostJq is the reference jq (jq 1.8.1 from Homebrew); cases are checked
// against it when it exists.
const hostJq = "/opt/homebrew/bin/jq"

var jqFiles = map[string]string{
	"o.json":   `{"a":[1,2.50,{"x":null}],"c":"hé\t","n":{"k":"v"}}` + "\n",
	"l.json":   "1\n2\n\"x\"\n",
	"bad.json": "{\n",
	"two.json": `{"name":"a","v":1}` + "\n" + `{"name":"b","v":2}` + "\n",
}

var jqCases = []textCase{
	{
		name: "jq pretty", files: jqFiles, script: "jq . o.json", reference: hostJq,
		stdout: "{\n  \"a\": [\n    1,\n    2.50,\n    {\n      \"x\": null\n    }\n  ],\n  \"c\": \"hé\\t\",\n  \"n\": {\n    \"k\": \"v\"\n  }\n}\n",
	},
	{name: "jq compact", files: jqFiles, script: "jq -c . o.json", reference: hostJq, stdout: "{\"a\":[1,2.50,{\"x\":null}],\"c\":\"hé\\t\",\"n\":{\"k\":\"v\"}}\n"},
	{name: "jq path", files: jqFiles, script: "jq .n.k o.json", reference: hostJq, stdout: "\"v\"\n"},
	{name: "jq raw", files: jqFiles, script: "jq -r .n.k o.json", reference: hostJq, stdout: "v\n"},
	{name: "jq raw combined flags", files: jqFiles, script: "jq -rc '.a[1], .n' o.json", reference: hostJq, stdout: "2.50\n{\"k\":\"v\"}\n"},
	{name: "jq join", stdin: "\"a\"\n", script: "jq -j '.,.'", reference: hostJq, stdout: "aa"},
	{name: "jq stream select", files: jqFiles, script: "jq -c 'select(.v > 1) | .name' two.json", reference: hostJq, stdout: "\"b\"\n"},
	{name: "jq slurp", stdin: "1 2\n", script: "jq -s -c .", reference: hostJq, stdout: "[1,2]\n"},
	{name: "jq raw input", stdin: "l1\nl2\n", script: "jq -R .", reference: hostJq, stdout: "\"l1\"\n\"l2\"\n"},
	{name: "jq raw input slurp", stdin: "l1\nl2\n", script: "jq -Rs .", reference: hostJq, stdout: "\"l1\\nl2\\n\"\n"},
	{name: "jq null input", script: "jq -n -c '[limit(3;range(10))]'", reference: hostJq, stdout: "[0,1,2]\n"},
	{name: "jq inputs", stdin: "1 2 3\n", script: "jq -n -c '[inputs]'", reference: hostJq, stdout: "[1,2,3]\n"},
	{name: "jq args", script: "jq -n -c --arg x 1 --argjson y '{\"z\":2}' '[$x,$y]'", reference: hostJq, stdout: "[\"1\",{\"z\":2}]\n"},
	{name: "jq named args", script: "jq -n -c --arg x 1 '$ARGS.named'", reference: hostJq, stdout: "{\"x\":\"1\"}\n"},
	{name: "jq tab", stdin: "[1,[]]\n", script: "jq --tab .", reference: hostJq, stdout: "[\n\t1,\n\t[]\n]\n"},
	{name: "jq indent", script: "jq -n --indent 1 '[1]'", reference: hostJq, stdout: "[\n 1\n]\n"},
	{name: "jq formats", script: "jq -rn '[1,2]|@csv, @tsv, @json, @base64'", reference: hostJq, stdout: "1,2\n1\t2\n[1,2]\nWzEsMl0=\n"},
	{name: "jq update", script: "jq -n -c '{} | .a.b.c = 1'", reference: hostJq, stdout: "{\"a\":{\"b\":{\"c\":1}}}\n"},
	{name: "jq sort keys", script: "jq -n -S -c '{\"b\":1,\"a\":2}'", reference: hostJq, stdout: "{\"a\":2,\"b\":1}\n"},
	{name: "jq arithmetic", script: "jq -n '1/3, 100/2, 0.1+0.2'", reference: hostJq, stdout: "0.3333333333333333\n50\n0.30000000000000004\n"},
	{name: "jq exit status null", stdin: "null\n", script: "jq -e .", reference: hostJq, stdout: "null\n", exit: 1},
	{name: "jq exit status no output", script: "jq -n -e empty", reference: hostJq, exit: 4},
	{name: "jq halt_error string", script: "jq -n '\"x\"|halt_error'", reference: hostJq, stderr: "x", exit: 5},
	{name: "jq halt_error object", script: "jq -n '{\"a\":1}|halt_error'", reference: hostJq, stderr: "{\"a\":1}\n", exit: 5},
	{name: "jq default filter", stdin: "{\"a\":1}\n", script: "jq -c", reference: hostJq, stdout: "{\"a\":1}\n"},
	{
		name: "jq missing file", script: "jq . nope.json", reference: hostJq, stderr: "jq: error: Could not open nope.json: No such file or directory\n", exit: 2,
		gnuSkip: "jq 1.8 says 'Could not open file'; message wording differs",
	},
	{
		name: "jq unknown option", script: "jq -z .", reference: hostJq, exit: 2,
		stderr: "jq: Unknown option -z\nUse jq --help for help with command-line options,\nor see the jq manpage, or online docs  at https://jqlang.org\n",
	},
	{
		name: "jq arg without value", script: "jq -n --arg", reference: hostJq, exit: 2,
		stderr: "jq: --arg takes two parameters (e.g. --arg varname value)\nUse jq --help for help with command-line options,\nor see the jq manpage, or online docs  at https://jqlang.org\n",
	},
	{
		name: "jq bad argjson", script: "jq -n --argjson x '{' '$x'", reference: hostJq, exit: 2,
		stderr: "jq: invalid JSON text passed to --argjson\nUse jq --help for help with command-line options,\nor see the jq manpage, or online docs  at https://jqlang.org\n",
	},
	{
		name: "jq runtime error continues", files: jqFiles, script: "jq '.+1' l.json", exit: 5, stdout: "2\n3\n",
		stderr: "jq: error (at l.json:3): cannot add: string (\"x\") and number (1)\n", gnuSkip: "error text is gojq's (jq: string (\"x\") and number (1) cannot be added)",
	},
	{
		name: "jq compile error", script: "jq -n '1 +'", exit: 3, stderr: "jq: error: unexpected EOF\njq: 1 compile error\n",
		gnuSkip: "compile error text is gojq's",
	},
	{
		name: "jq parse error", files: jqFiles, script: "jq . bad.json", exit: 2, stderr: "jq: error (at bad.json:1): unexpected EOF\n",
		gnuSkip: "parse error text is Go's encoding/json",
	},
	{
		name: "jq sorts object keys", stdin: "{\"b\":1,\"a\":2}\n", script: "jq -c .", stdout: "{\"a\":2,\"b\":1}\n", reference: hostJq,
		gnuSkip: "gojq sorts object keys; jq keeps their input order ({\"b\":1,\"a\":2})",
	},
	{
		name: "jq pipeline", files: jqFiles, script: "jq -r '.name' two.json | sort -r | head -n 1", stdout: "b\n",
		gnuSkip: "debian:12 has no jq; the pieces are checked separately",
	},
}
