package shell

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

var cutTool = &textTool{
	name:        "cut",
	hint:        "Try 'cut --help' for more information.\n",
	usageStatus: 1,
	help: `Usage: cut OPTION... [FILE]...
Print selected parts of lines from each FILE to standard output.
  -b, --bytes=LIST        select only these bytes
  -c, --characters=LIST   select only these characters (bytes, like GNU cut)
  -d, --delimiter=DELIM   use DELIM instead of TAB for field delimiter
  -f, --fields=LIST       select only these fields
  -n                      (ignored)
      --complement        complement the set of selected bytes, characters or fields
  -s, --only-delimited    do not print lines not containing delimiters
      --output-delimiter=STRING  use STRING as the output delimiter
LIST is made up of N, N-, N-M or -M, separated by commas.
`,
}

var cutParser = &optionParser{specs: []optionSpec{
	{short: 'b', long: "bytes", argument: requiredArgument},
	{short: 'c', long: "characters", argument: requiredArgument},
	{short: 'd', long: "delimiter", argument: requiredArgument},
	{short: 'f', long: "fields", argument: requiredArgument},
	{short: 'n'},
	{long: "complement"},
	{short: 's', long: "only-delimited"},
	{long: "output-delimiter", argument: requiredArgument},
	helpOption,
}}

func cutCommand() Command {
	return Command{Name: "cut", Summary: "select fields or bytes of lines (-f LIST -d DELIM -s -b LIST -c LIST --complement)", Run: runCut}
}

// cutRange is an inclusive 1-based range of positions.
type cutRange struct{ low, high int }

// cutSettings is a parsed cut command line.
type cutSettings struct {
	fields          bool
	ranges          []cutRange
	delimiter       byte
	outputDelimiter string
	hasOutputDelim  bool
	onlyDelimited   bool
	complement      bool
}

func runCut(ctx context.Context, invocation *Invocation) error {
	line, err := cutTool.parseOptions(invocation, cutParser)
	if line.done {
		return err
	}
	settings, err := parseCutSettings(invocation, line.options)
	if err != nil {
		return err
	}
	var output strings.Builder
	var status ExitError
	for _, name := range operandsOrStdin(line.operands) {
		content, readErr := readOperand(ctx, invocation, name)
		if reason, ok := operandReason(readErr); ok {
			cutTool.complain(invocation, "%s: %s", name, reason)
			status = 1
			continue
		}
		if readErr != nil {
			return readErr
		}
		lines, _ := splitLines(string(content))
		for _, text := range lines {
			if selected, keep := settings.cutLine(text); keep {
				output.WriteString(selected + "\n")
			}
		}
	}
	if _, err := fmt.Fprint(invocation.Stdout, output.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return exitStatus(status)
}

func parseCutSettings(invocation *Invocation, options []parsedOption) (cutSettings, error) {
	settings := cutSettings{delimiter: '\t'}
	lists := 0
	hasDelimiter := false
	var list string
	for _, option := range options {
		switch option.key {
		case "bytes", "characters", "fields":
			lists++
			settings.fields = option.key == "fields"
			list = option.value
		case "delimiter":
			if len(option.value) > 1 {
				return settings, cutTool.usageFailure(invocation, "the delimiter must be a single character")
			}
			settings.delimiter = 0
			if option.value != "" {
				settings.delimiter = option.value[0]
			}
			hasDelimiter = true
		case "only-delimited":
			settings.onlyDelimited = true
		case "complement":
			settings.complement = true
		case "output-delimiter":
			settings.outputDelimiter, settings.hasOutputDelim = option.value, true
		}
	}
	switch {
	case lists == 0:
		return settings, cutTool.usageFailure(invocation, "you must specify a list of bytes, characters, or fields")
	case lists > 1:
		return settings, cutTool.usageFailure(invocation, "only one list may be specified")
	case !settings.fields && hasDelimiter:
		return settings, cutTool.usageFailure(invocation, "an input delimiter may be specified only when operating on fields")
	case !settings.fields && settings.onlyDelimited:
		return settings, cutTool.usageFailure(invocation, "suppressing non-delimited lines makes sense\n\tonly when operating on fields")
	}
	ranges, message := parseCutList(list, settings.fields)
	if message != "" {
		return settings, cutTool.usageFailure(invocation, message)
	}
	settings.ranges = ranges
	if settings.fields && !settings.hasOutputDelim {
		settings.outputDelimiter = string(settings.delimiter)
	}
	return settings, nil
}

// parseCutList reads a LIST into sorted, merged ranges; the message is GNU's
// when the list is invalid.
func parseCutList(list string, fields bool) ([]cutRange, string) {
	var ranges []cutRange
	for item := range strings.FieldsFuncSeq(list, func(character rune) bool { return character == ',' || character == ' ' || character == '\t' }) {
		parsed, message := parseCutItem(item, fields)
		if message != "" {
			return nil, message
		}
		ranges = append(ranges, parsed)
	}
	if len(ranges) == 0 {
		return nil, "missing list of fields"
	}
	slices.SortFunc(ranges, func(left, right cutRange) int { return left.low - right.low })
	merged := []cutRange{ranges[0]}
	for _, next := range ranges[1:] {
		last := &merged[len(merged)-1]
		if last.high == math.MaxInt || next.low <= last.high+1 {
			last.high = max(last.high, next.high)
			continue
		}
		merged = append(merged, next)
	}
	return merged, ""
}

// parseCutItem reads one N, N-, N-M or -M.
func parseCutItem(item string, fields bool) (cutRange, string) {
	invalid := fmt.Sprintf("invalid byte/character position '%s'", item)
	zero := "byte/character positions are numbered from 1"
	if fields {
		invalid = fmt.Sprintf("invalid field value '%s'", item)
		zero = "fields are numbered from 1"
	}
	lowText, highText, isRange := strings.Cut(item, "-")
	if isRange && lowText == "" && highText == "" {
		return cutRange{}, "invalid range with no endpoint: -"
	}
	low, high := 1, math.MaxInt
	var err error
	if lowText != "" {
		if low, err = strconv.Atoi(lowText); err != nil || low < 0 {
			return cutRange{}, invalid
		}
		if low == 0 {
			return cutRange{}, zero
		}
	}
	switch {
	case !isRange:
		high = low
	case highText != "":
		if high, err = strconv.Atoi(highText); err != nil || high < 0 {
			return cutRange{}, invalid
		}
		if high == 0 {
			return cutRange{}, zero
		}
	}
	if high < low {
		return cutRange{}, "invalid decreasing range"
	}
	return cutRange{low, high}, ""
}

// selected reports whether 1-based position is in the list, honouring
// --complement.
func (s *cutSettings) selected(position int) bool {
	for _, candidate := range s.ranges {
		if position >= candidate.low && position <= candidate.high {
			return !s.complement
		}
	}
	return s.complement
}

// cutLine returns the selected part of one line, and false when -s drops it.
func (s *cutSettings) cutLine(line string) (string, bool) {
	if !s.fields {
		return s.cutBytes(line), true
	}
	if strings.IndexByte(line, s.delimiter) < 0 {
		return line, !s.onlyDelimited
	}
	var kept []string
	for index, field := range strings.Split(line, string(s.delimiter)) {
		if s.selected(index + 1) {
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, s.outputDelimiter), true
}

// cutBytes keeps the selected bytes; --output-delimiter separates runs.
func (s *cutSettings) cutBytes(line string) string {
	var output strings.Builder
	previous := -1
	for index := range len(line) {
		if !s.selected(index + 1) {
			continue
		}
		if previous >= 0 && previous != index-1 && s.hasOutputDelim {
			output.WriteString(s.outputDelimiter)
		}
		output.WriteByte(line[index])
		previous = index
	}
	return output.String()
}
