package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

var wcTool = &textTool{
	name:        "wc",
	hint:        "Try 'wc --help' for more information.\n",
	usageStatus: 1,
	help: `Usage: wc [OPTION]... [FILE]...
Print newline, word, and byte counts for each FILE, and a total line if
more than one FILE is specified.
  -c, --bytes            print the byte counts
  -m, --chars            print the character counts (UTF-8)
  -l, --lines            print the newline counts
  -L, --max-line-length  print the maximum display width
  -w, --words            print the word counts
`,
}

var wcParser = &optionParser{specs: []optionSpec{
	{short: 'c', long: "bytes"},
	{short: 'm', long: "chars"},
	{short: 'l', long: "lines"},
	{short: 'L', long: "max-line-length"},
	{short: 'w', long: "words"},
	helpOption,
}}

func wcCommand() Command {
	return Command{Name: "wc", Summary: "count lines, words, bytes (-l -w -c -m -L)", Run: runWc}
}

// wcCounts are the counts of one input, in wc's output order.
type wcCounts struct {
	lines, words, characters, bytes, maximumLineLength int
}

// wcSelection says which counts wc prints.
type wcSelection struct {
	lines, words, characters, bytes, maximumLineLength bool
}

func (s wcSelection) selected() int {
	count := 0
	for _, on := range []bool{s.lines, s.words, s.characters, s.bytes, s.maximumLineLength} {
		if on {
			count++
		}
	}
	return count
}

// wcResult is one input's outcome: counts, or the failure already reported.
type wcResult struct {
	name    string
	counts  wcCounts
	failed  bool
	printed bool
	// regular is false for stdin and directories, whose size wc cannot know
	// up front, which widens the columns to 7.
	regular bool
}

func runWc(ctx context.Context, invocation *Invocation) error {
	line, err := wcTool.parseOptions(invocation, wcParser)
	if line.done {
		return err
	}
	selection := wcSelection{}
	for _, option := range line.options {
		switch option.key {
		case "bytes":
			selection.bytes = true
		case "chars":
			selection.characters = true
		case "lines":
			selection.lines = true
		case "max-line-length":
			selection.maximumLineLength = true
		case "words":
			selection.words = true
		}
	}
	if selection.selected() == 0 {
		selection = wcSelection{lines: true, words: true, bytes: true}
	}
	results, status, err := countInputs(ctx, invocation, line.operands)
	if err != nil {
		return err
	}
	width := wcWidth(results, selection.selected() == 1 && len(results) == 1)
	var output strings.Builder
	total := wcCounts{}
	for _, result := range results {
		if result.printed {
			output.WriteString(formatCounts(result.counts, selection, width, result.name))
		}
		total.lines += result.counts.lines
		total.words += result.counts.words
		total.characters += result.counts.characters
		total.bytes += result.counts.bytes
		total.maximumLineLength = max(total.maximumLineLength, result.counts.maximumLineLength)
	}
	if len(results) > 1 {
		output.WriteString(formatCounts(total, selection, width, "total"))
	}
	if _, err := fmt.Fprint(invocation.Stdout, output.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return exitStatus(status)
}

// countInputs counts every operand (stdin without one, printed without a
// name), reporting the ones that cannot be read.
func countInputs(ctx context.Context, invocation *Invocation, operands []string) ([]wcResult, ExitError, error) {
	inputs := operandsOrStdin(operands)
	results := make([]wcResult, 0, len(inputs))
	var status ExitError
	for _, name := range inputs {
		content, err := readOperand(ctx, invocation, name)
		reason, isOperandError := operandReason(err)
		switch {
		case isOperandError:
			wcTool.complain(invocation, "%s: %s", name, reason)
			status = 1
			// A directory still gets its line of zeros, a missing file none.
			results = append(results, wcResult{name: name, failed: true, printed: reason == reasonIsDirectory})
			continue
		case err != nil:
			return nil, 0, err
		}
		displayName := name
		if len(operands) == 0 {
			displayName = ""
		}
		regular := name != "-" && name != "/dev/stdin" && name != "/dev/null"
		results = append(results, wcResult{name: displayName, counts: countText(content), printed: true, regular: regular})
	}
	return results, status, nil
}

// wcWidth is GNU's column width: 1 for a single count of a single input,
// otherwise the digits of the summed regular file sizes, at least 7 when an
// input has no known size.
func wcWidth(results []wcResult, single bool) int {
	if single || len(results) == 0 || (results[0].failed && !results[0].printed) {
		return 1
	}
	minimum, total := 1, 0
	for _, result := range results {
		switch {
		case result.failed && !result.printed:
		case !result.regular || result.failed:
			minimum = 7
		default:
			total += result.counts.bytes
		}
	}
	return max(minimum, len(strconv.Itoa(total)))
}

func formatCounts(counts wcCounts, selection wcSelection, width int, name string) string {
	var fields []string
	add := func(on bool, value int) {
		if on {
			fields = append(fields, fmt.Sprintf("%*d", width, value))
		}
	}
	add(selection.lines, counts.lines)
	add(selection.words, counts.words)
	add(selection.characters, counts.characters)
	add(selection.bytes, counts.bytes)
	add(selection.maximumLineLength, counts.maximumLineLength)
	line := strings.Join(fields, " ")
	if name != "" {
		line += " " + name
	}
	return line + "\n"
}

// countText counts like GNU wc: words are runs of non-space bytes, the line
// length expands tabs to multiples of 8.
func countText(content []byte) wcCounts {
	counts := wcCounts{bytes: len(content), characters: utf8.RuneCount(content)}
	inWord := false
	lineLength := 0
	for _, character := range content {
		switch character {
		case '\n':
			counts.lines++
			counts.maximumLineLength = max(counts.maximumLineLength, lineLength)
			lineLength = 0
		case '\t':
			lineLength += 8 - lineLength%8
		case '\r', '\f':
			lineLength = 0
		default:
			if character >= ' ' && character < 0x7f || character >= 0x80 && utf8.RuneStart(character) {
				lineLength++
			}
		}
		isSpace := character == ' ' || (character >= '\t' && character <= '\r')
		if !isSpace && !inWord {
			counts.words++
		}
		inWord = !isSpace
	}
	counts.maximumLineLength = max(counts.maximumLineLength, lineLength)
	return counts
}
