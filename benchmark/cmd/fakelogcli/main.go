// Command fakelogcli is the worker suite's Grafana Loki logcli: `logcli query
// '<logql>' --since 2h --limit N` over the pod logs of the canned cluster state
// ($FAKE_CLUSTER_STATE) plus its extra Loki streams, printed like logcli does.
// Every invocation is recorded in $FAKE_LOG. See benchmark/README.md.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/govengers/aikido/benchmark/internal/fakestate"
)

const defaultLimit = 30

func main() {
	arguments := os.Args[1:]
	exitCode := run(arguments, os.Stdout, os.Stderr)
	fakestate.Record("logcli", arguments, false, exitCode)
	os.Exit(exitCode)
}

type query struct {
	positional []string
	since      time.Duration
	from, to   time.Time
	limit      int
	output     string
	forward    bool
	quiet      bool
}

// messageError carries logcli's own message verbatim.
type messageError string

func (message messageError) Error() string { return string(message) }

func failure(format string, arguments ...any) error {
	return messageError(fmt.Sprintf(format, arguments...))
}

func parse(arguments []string) (query, error) {
	parsed := query{since: time.Hour, limit: defaultLimit, output: "default"}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if !strings.HasPrefix(argument, "-") {
			parsed.positional = append(parsed.positional, argument)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(argument, "-"), "=")
		switch name {
		case "forward":
			parsed.forward = true
			continue
		case "quiet", "q":
			parsed.quiet = true
			continue
		case "no-labels", "tail", "t", "stats", "colored-output":
			continue
		}
		if !hasValue && index+1 < len(arguments) {
			index++
			value = arguments[index]
		}
		if err := parsed.set(name, value); err != nil {
			return parsed, err
		}
	}
	return parsed, nil
}

func (parsed *query) set(name, value string) error {
	var err error
	switch name {
	case "since":
		parsed.since, err = parseDuration(value)
	case "limit":
		parsed.limit, err = strconv.Atoi(value)
	case "from":
		parsed.from, err = time.Parse(time.RFC3339, value)
	case "to":
		parsed.to, err = time.Parse(time.RFC3339, value)
	case "o", "output":
		parsed.output = value
	}
	if err != nil {
		return failure("logcli: error: invalid value %q for flag '--%s': %v", value, name, err)
	}
	return nil
}

// parseDuration is time.ParseDuration plus the d and w units logcli accepts.
func parseDuration(value string) (time.Duration, error) {
	for unit, hours := range map[string]int{"d": 24, "w": 24 * 7} {
		if number, ok := strings.CutSuffix(value, unit); ok {
			count, err := strconv.Atoi(number)
			return time.Duration(count*hours) * time.Hour, err
		}
	}
	return time.ParseDuration(value)
}

func run(arguments []string, stdout, stderr io.Writer) int {
	parsed, err := parse(arguments)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(parsed.positional) == 0 || parsed.positional[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	state, err := fakestate.Load()
	if err != nil {
		fmt.Fprintln(stderr, "logcli:", err)
		return 1
	}
	switch parsed.positional[0] {
	case "query":
		err = runQuery(&parsed, state, stdout, stderr)
	case "instant-query":
		err = failure("Error response from server: unsupported: this Loki only serves log queries to logcli (no metric queries)")
	case "labels":
		runLabels(&parsed, streams(state), stdout)
	case "series":
		err = runSeries(&parsed, streams(state), stdout)
	default:
		err = failure("logcli: error: expected command but got %q, try --help", parsed.positional[0])
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

const usage = `usage: logcli [<flags>] <command> [<args> ...]

A command-line for loki.

Commands:
  query [<flags>] <query>   Run a LogQL query (--since, --from, --to, --limit, --forward, -o default|raw|jsonl, -q).
  labels [<label>]          Find values for a given label.
  series <matcher>          Run a query for streams which match given label matchers.
`
