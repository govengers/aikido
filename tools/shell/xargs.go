package shell

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// xargs exit statuses, as GNU documents them.
const (
	statusXargsCommandFailed = 123
	statusXargsStopped       = 124
)

func xargsCommand() Command {
	return boundedInput(Command{
		Name:    "xargs",
		Summary: "run a command with arguments from stdin (-0 -d delim -n N -L N -I {} -r -t; -P accepted, runs one at a time)",
		Run:     runXargs,
	})
}

// xargsPlan is one xargs invocation: how input splits and how commands
// are built from it.
type xargsPlan struct {
	command      []string
	replace      string // -I
	maxArgs      int    // -n
	maxLines     int    // -L
	noRunEmpty   bool   // -r
	trace        bool   // -t
	delimiter    string // -0 or -d; "" splits on blanks and honours quotes
	hasDelimiter bool
}

func runXargs(ctx context.Context, invocation *Invocation) error {
	plan, err := parseXargs(invocation.Arguments)
	if err != nil {
		var usageErr *usageError
		if errors.As(err, &usageErr) {
			return usage(invocation, "xargs", err, errFailed)
		}
		complain(invocation, "xargs", "%v", err)
		return errFailed
	}
	input, err := readBounded(invocation.Stdin)
	if err != nil {
		return err
	}
	groups, err := plan.split(string(input))
	if err != nil {
		complain(invocation, "xargs", "%v", err)
		return errFailed
	}
	return plan.run(ctx, invocation, groups)
}

func parseXargs(arguments []string) (xargsPlan, error) {
	parsed, operands, err := optionSet{flags: "0rt", valued: "nLIdP", inOrder: true, long: map[string]longOption{
		"null": {name: "0"}, "no-run-if-empty": {name: "r"}, "verbose": {name: "t"}, "max-args": {name: "n", valued: true},
		"max-lines": {name: "L", valued: true}, "replace": {name: "I", valued: true}, "delimiter": {name: "d", valued: true},
		"max-procs": {name: "P", valued: true},
	}}.parse(arguments)
	if err != nil {
		return xargsPlan{}, err
	}
	plan := xargsPlan{command: operands}
	for _, given := range parsed {
		if err := plan.apply(given); err != nil {
			return xargsPlan{}, err
		}
	}
	if len(plan.command) == 0 {
		plan.command = []string{"echo"}
	}
	return plan, nil
}

func (p *xargsPlan) apply(given option) error {
	switch given.name {
	case "0":
		p.delimiter, p.hasDelimiter = "\x00", true
	case "d":
		delimiter, err := strconv.Unquote(`"` + given.value + `"`)
		if err != nil || len(delimiter) != 1 {
			return fmt.Errorf("invalid input delimiter specification %s: the delimiter must be a single character", given.value)
		}
		p.delimiter, p.hasDelimiter = delimiter, true
	case "r":
		p.noRunEmpty = true
	case "t":
		p.trace = true
	case "I":
		p.replace, p.maxLines = given.value, 1
	case "n", "L":
		count, err := strconv.Atoi(given.value)
		if err != nil || count < 1 {
			return fmt.Errorf("value %s for -%s option should be >= 1", given.value, given.name)
		}
		if given.name == "n" {
			p.maxArgs = count
		} else {
			p.maxLines = count
		}
	case "P":
		// Commands run one at a time; -P only shortens a model's habit.
	}
	return nil
}

// split turns the input into groups of items: one group per item or line
// for -I and -L, else one group.
func (p xargsPlan) split(input string) ([][]string, error) {
	switch {
	case p.hasDelimiter:
		items := strings.Split(input, p.delimiter)
		if len(items) > 0 && items[len(items)-1] == "" {
			items = items[:len(items)-1]
		}
		if p.replace == "" && p.maxLines == 0 {
			return [][]string{items}, nil
		}
		return eachAlone(items), nil
	case p.replace != "":
		// One item per line: blanks inside it are kept, quotes and
		// backslashes are processed as without -I.
		lines := []string{}
		for line := range strings.Lines(input) {
			items, err := splitQuoted(strings.TrimLeft(line, " \t"), false)
			if err != nil {
				return nil, err
			}
			lines = append(lines, items...)
		}
		return eachAlone(lines), nil
	}
	lines, err := quotedLines(input)
	if err != nil || p.maxLines > 0 {
		return lines, err
	}
	all := []string{}
	for _, line := range lines {
		all = append(all, line...)
	}
	return [][]string{all}, nil
}

// eachAlone puts every item in its own group.
func eachAlone(items []string) [][]string {
	groups := make([][]string, 0, len(items))
	for _, item := range items {
		groups = append(groups, []string{item})
	}
	return groups
}

// quotedLines splits every non-blank input line with splitQuoted.
func quotedLines(input string) ([][]string, error) {
	lines := [][]string{}
	for line := range strings.Lines(input) {
		items, err := splitQuoted(line, true)
		if err != nil {
			return nil, err
		}
		if len(items) > 0 {
			lines = append(lines, items)
		}
	}
	return lines, nil
}

// splitQuoted splits one input line on blanks, honouring '...', "..." and
// backslash escapes like GNU xargs.
func splitQuoted(line string, splitBlanks bool) ([]string, error) {
	items := []string{}
	var current strings.Builder
	inItem := false
	quote := rune(0)
	escaped := false
	for _, character := range line {
		switch {
		case escaped:
			current.WriteRune(character)
			escaped = false
		case quote != 0 && character == quote:
			quote = 0
		case quote != 0:
			if character == '\n' {
				return nil, unmatchedQuote(quote)
			}
			current.WriteRune(character)
		case character == '\\':
			escaped, inItem = true, true
		case character == '\'' || character == '"':
			quote, inItem = character, true
		case (character == ' ' || character == '\t') && splitBlanks || character == '\n':
			if inItem {
				items = append(items, current.String())
				current.Reset()
				inItem = false
			}
		default:
			current.WriteRune(character)
			inItem = true
		}
	}
	if quote != 0 {
		return nil, unmatchedQuote(quote)
	}
	if inItem {
		items = append(items, current.String())
	}
	return items, nil
}

func unmatchedQuote(quote rune) error {
	kind := "single"
	if quote == '"' {
		kind = "double"
	}
	return fmt.Errorf("unmatched %s quote; by default quotes are special to xargs unless you use the -0 option", kind)
}

// run builds and runs the command lines. The exit status is 123 if a
// command failed, 124 if one exited with 255, 127 for an unknown command.
func (p xargsPlan) run(ctx context.Context, invocation *Invocation, groups [][]string) error {
	lines := p.commandLines(groups)
	if len(lines) == 0 && !p.noRunEmpty && p.replace == "" {
		lines = [][]string{p.command}
	}
	failed := false
	for _, line := range lines {
		if p.trace {
			fmt.Fprintln(invocation.Stderr, strings.Join(line, " "))
		}
		known := invocation.session != nil && invocation.session.known(line[0])
		status := runSubcommand(ctx, invocation, "xargs", line, emptyInput())
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case !known:
			// xargs could not start the command; a command's own 126 or 127
			// is an ordinary failure below.
			return ExitError(status)
		case status == 255:
			complain(invocation, "xargs", "%s: exited with status 255; aborting", line[0])
			return ExitError(statusXargsStopped)
		case status != 0:
			failed = true
		}
	}
	if failed {
		return ExitError(statusXargsCommandFailed)
	}
	return nil
}

// commandLines turns the item groups into the command lines to run.
func (p xargsPlan) commandLines(groups [][]string) [][]string {
	lines := [][]string{}
	if p.replace != "" {
		for _, group := range groups {
			line := make([]string, len(p.command))
			for index, argument := range p.command {
				line[index] = strings.ReplaceAll(argument, p.replace, group[0])
			}
			lines = append(lines, line)
		}
		return lines
	}
	if p.maxLines > 0 {
		for start := 0; start < len(groups); start += p.maxLines {
			items := []string{}
			for _, group := range groups[start:min(start+p.maxLines, len(groups))] {
				items = append(items, group...)
			}
			lines = append(lines, p.withItems(items)...)
		}
		return lines
	}
	for _, group := range groups {
		lines = append(lines, p.withItems(group)...)
	}
	return lines
}

// withItems appends items to the command, in runs of -n items (or of
// maximumBatchArguments, standing in for the system's argument limit).
func (p xargsPlan) withItems(items []string) [][]string {
	size := p.maxArgs
	if size == 0 {
		size = maximumBatchArguments
	}
	lines := [][]string{}
	for start := 0; start < len(items); start += size {
		line := append(append([]string{}, p.command...), items[start:min(start+size, len(items))]...)
		lines = append(lines, line)
	}
	return lines
}
