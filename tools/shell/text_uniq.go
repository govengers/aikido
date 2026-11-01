package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

var uniqTool = &textTool{
	name:        "uniq",
	hint:        "Try 'uniq --help' for more information.\n",
	usageStatus: 1,
	help: `Usage: uniq [OPTION]... [INPUT [OUTPUT]]
Filter adjacent matching lines from INPUT (or standard input), writing to OUTPUT (or standard output).
  -c, --count           prefix lines by the number of occurrences
  -d, --repeated        only print duplicate lines, one for each group
  -D                    print all duplicate lines
  -f, --skip-fields=N   avoid comparing the first N fields
  -i, --ignore-case     ignore differences in case when comparing
  -s, --skip-chars=N    avoid comparing the first N characters
  -u, --unique          only print unique lines
  -w, --check-chars=N   compare no more than N characters in lines
`,
}

var uniqParser = &optionParser{specs: []optionSpec{
	{short: 'c', long: "count"},
	{short: 'd', long: "repeated"},
	{short: 'D'},
	{short: 'f', long: "skip-fields", argument: requiredArgument},
	{short: 'i', long: "ignore-case"},
	{short: 's', long: "skip-chars", argument: requiredArgument},
	{short: 'u', long: "unique"},
	{short: 'w', long: "check-chars", argument: requiredArgument},
	helpOption,
}}

func uniqCommand() Command {
	return Command{Name: "uniq", Summary: "drop adjacent duplicate lines (-c -d -D -u -i -f N -s N -w N)", Run: runUniq}
}

// uniqSettings is a parsed uniq command line.
type uniqSettings struct {
	count, repeated, allRepeated, unique, ignoreCase bool
	skipFields, skipCharacters                       int
	// checkCharacters is -1 for the whole rest of the line.
	checkCharacters int
}

func runUniq(ctx context.Context, invocation *Invocation) error {
	line, err := uniqTool.parseOptions(invocation, uniqParser)
	if line.done {
		return err
	}
	settings, err := parseUniqSettings(invocation, line.options)
	if err != nil {
		return err
	}
	if len(line.operands) > 2 {
		return uniqTool.usageFailure(invocation, fmt.Sprintf("extra operand '%s'", line.operands[2]))
	}
	input := "-"
	if len(line.operands) > 0 {
		input = line.operands[0]
	}
	content, err := readOperand(ctx, invocation, input)
	if reason, ok := operandReason(err); ok {
		if reason == reasonIsDirectory {
			return uniqTool.fatal(invocation, 1, "error reading '%s'", input)
		}
		return uniqTool.fatal(invocation, 1, "%s: %s", input, reason)
	}
	if err != nil {
		return err
	}
	lines, _ := splitLines(string(content))
	result := settings.filter(lines)
	if len(line.operands) == 2 && line.operands[1] != "-" {
		return writeOutputFile(ctx, invocation, uniqTool, line.operands[1], result)
	}
	if _, err := fmt.Fprint(invocation.Stdout, result); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func parseUniqSettings(invocation *Invocation, options []parsedOption) (uniqSettings, error) {
	settings := uniqSettings{checkCharacters: -1}
	for _, option := range options {
		switch option.key {
		case "count":
			settings.count = true
		case "repeated":
			settings.repeated = true
		case "D":
			settings.allRepeated = true
		case "unique":
			settings.unique = true
		case "ignore-case":
			settings.ignoreCase = true
		case "skip-fields", "skip-chars", "check-chars":
			value, err := strconv.Atoi(option.value)
			if err != nil || value < 0 {
				what := map[string]string{
					"skip-fields": "number of fields to skip", "skip-chars": "number of bytes to skip",
					"check-chars": "number of bytes to compare",
				}[option.key]
				return settings, uniqTool.fatal(invocation, 1, "%s: invalid %s", option.value, what)
			}
			switch option.key {
			case "skip-fields":
				settings.skipFields = value
			case "skip-chars":
				settings.skipCharacters = value
			default:
				settings.checkCharacters = value
			}
		}
	}
	if settings.allRepeated && settings.count {
		return settings, uniqTool.usageFailure(invocation, "printing all duplicated lines and repeat counts is meaningless")
	}
	return settings, nil
}

// filter groups adjacent equal lines and renders the selected groups.
func (s *uniqSettings) filter(lines []string) string {
	var output strings.Builder
	for start := 0; start < len(lines); {
		end := start + 1
		for end < len(lines) && s.equal(lines[start], lines[end]) {
			end++
		}
		s.render(&output, lines[start:end])
		start = end
	}
	return output.String()
}

// render writes one group of equal lines as the options select it.
func (s *uniqSettings) render(output *strings.Builder, group []string) {
	repeated := len(group) > 1
	if (s.repeated || s.allRepeated) && !repeated || s.unique && repeated {
		return
	}
	if s.allRepeated {
		for _, line := range group {
			output.WriteString(line + "\n")
		}
		return
	}
	if s.count {
		fmt.Fprintf(output, "%7d ", len(group))
	}
	output.WriteString(group[0] + "\n")
}

// equal compares the parts of two lines uniq looks at.
func (s *uniqSettings) equal(left, right string) bool {
	left, right = s.comparedPart(left), s.comparedPart(right)
	if s.ignoreCase {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// comparedPart skips -f fields and -s characters and keeps -w characters.
func (s *uniqSettings) comparedPart(line string) string {
	position := 0
	for range s.skipFields {
		for position < len(line) && (line[position] == ' ' || line[position] == '\t') {
			position++
		}
		for position < len(line) && line[position] != ' ' && line[position] != '\t' {
			position++
		}
	}
	position = min(len(line), position+s.skipCharacters)
	part := line[position:]
	if s.checkCharacters >= 0 && len(part) > s.checkCharacters {
		part = part[:s.checkCharacters]
	}
	return part
}

// writeOutputFile writes a command's result to a file operand.
func writeOutputFile(ctx context.Context, invocation *Invocation, tool *textTool, name, text string) error {
	resolved, err := Resolve(invocation.Directory, name)
	if err != nil || resolved == "" {
		return tool.fatal(invocation, 1, "%s: %s", name, reasonNoSuchFile)
	}
	if err := invocation.Storage.WriteFile(ctx, resolved, []byte(text), "text/plain"); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}
