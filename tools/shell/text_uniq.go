package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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
	reader, err := openOperand(ctx, invocation, input)
	if reason, ok := operandReason(err); ok {
		if reason == reasonIsDirectory {
			return uniqTool.fatal(invocation, 1, "error reading '%s'", input)
		}
		return uniqTool.fatal(invocation, 1, "%s: %s", input, reason)
	}
	if err != nil {
		return err
	}
	if len(line.operands) == 2 && line.operands[1] != "-" {
		var result strings.Builder
		if err := settings.filter(ctx, newLineReader(reader), &limitedBuilder{builder: &result}); err != nil {
			return err
		}
		return writeOutputFile(ctx, invocation, uniqTool, line.operands[1], result.String())
	}
	output := bufio.NewWriter(invocation.Stdout)
	err = settings.filter(ctx, newLineReader(reader), output)
	if flushErr := output.Flush(); err == nil && flushErr != nil {
		err = fmt.Errorf("write output: %w", flushErr)
	}
	return err
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

// filter streams the lines, groups adjacent equal ones and writes the
// selected groups as they end; -D lines are written as they arrive.
func (s *uniqSettings) filter(ctx context.Context, lines *lineReader, output io.Writer) error {
	var first string
	size := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, _, err := lines.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if size > 0 && s.equal(first, line) {
			size++
			if err := s.writeRepeated(output, first, line, size); err != nil {
				return err
			}
			continue
		}
		if err := s.render(output, first, size); err != nil {
			return err
		}
		first, size = line, 1
	}
	return s.render(output, first, size)
}

// writeRepeated writes the lines of a repeated group for -D: the first
// once the group repeats, then each further one.
func (s *uniqSettings) writeRepeated(output io.Writer, first, line string, size int) error {
	if !s.allRepeated || s.unique {
		return nil
	}
	if size == 2 {
		if _, err := io.WriteString(output, first+"\n"); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}
	if _, err := io.WriteString(output, line+"\n"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// render writes a group of size equal lines that has ended, as the options
// select it; -D groups are already written.
func (s *uniqSettings) render(output io.Writer, first string, size int) error {
	repeated := size > 1
	if size == 0 || s.allRepeated || s.repeated && !repeated || s.unique && repeated {
		return nil
	}
	prefix := ""
	if s.count {
		prefix = fmt.Sprintf("%7d ", size)
	}
	if _, err := io.WriteString(output, prefix+first+"\n"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
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
