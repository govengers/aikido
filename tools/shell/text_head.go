package shell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

var headTool = &textTool{
	name:        "head",
	hint:        "Try 'head --help' for more information.\n",
	usageStatus: 1,
	help: `Usage: head [OPTION]... [FILE]...
Print the first 10 lines of each FILE to standard output.
  -c, --bytes=[-]NUM       print the first NUM bytes; with -NUM, all but the last NUM bytes
  -n, --lines=[-]NUM       print the first NUM lines; with -NUM, all but the last NUM lines
  -q, --quiet, --silent    never print headers giving file names
  -v, --verbose            always print headers giving file names
`,
}

var headParser = &optionParser{specs: []optionSpec{
	{short: 'c', long: "bytes", argument: requiredArgument},
	{short: 'n', long: "lines", argument: requiredArgument},
	{short: 'q', long: "quiet"},
	{long: "silent"},
	{short: 'v', long: "verbose"},
	helpOption,
}}

func headCommand() Command {
	return Command{Name: "head", Summary: "first lines of files (-n [-]N, -N, -c [-]N, -q, -v)", Run: runHead}
}

// sliceOptions are the settings head and tail share.
type sliceOptions struct {
	bytes bool
	// count is the number of lines or bytes.
	count int
	// fromEnd is head's -n -N (all but the last N) or tail's default (the last N).
	fromEnd bool
	// headers: 0 for one header per file when there are several, 1 always, -1 never.
	headers int
}

func runHead(ctx context.Context, invocation *Invocation) error {
	invocation = withObsoleteCount(invocation, "-n")
	line, err := headTool.parseOptions(invocation, headParser)
	if line.done {
		return err
	}
	settings := sliceOptions{count: 10}
	for _, option := range line.options {
		if usageErr := settings.apply(invocation, headTool, option, false); usageErr != nil {
			return usageErr
		}
	}
	return settings.sliceFiles(ctx, invocation, headTool, line.operands, headInput)
}

// withObsoleteCount rewrites a first argument "-N" into "flag N", the
// obsolete form head -5 and tail -5 accept.
func withObsoleteCount(invocation *Invocation, flag string) *Invocation {
	if len(invocation.Arguments) == 0 {
		return invocation
	}
	first := invocation.Arguments[0]
	if len(first) < 2 || first[0] != '-' || strings.Trim(first[1:], "0123456789") != "" {
		return invocation
	}
	rewritten := *invocation
	rewritten.Arguments = append([]string{flag, first[1:]}, invocation.Arguments[1:]...)
	return &rewritten
}

// apply records one parsed head or tail option. A plain count means the
// first N for head and the last N for tail (countsFromEnd); "-N" always
// counts from the end, "+N" from the start.
func (s *sliceOptions) apply(invocation *Invocation, tool *textTool, option parsedOption, countsFromEnd bool) error {
	switch option.key {
	case "bytes", "lines":
		s.bytes = option.key == "bytes"
		value := option.value
		s.fromEnd = countsFromEnd
		if value != "" && (value[0] == '-' || value[0] == '+') {
			s.fromEnd = value[0] == '-'
			value = value[1:]
		}
		count, ok := parseCount(value)
		if !ok {
			unit := "lines"
			if s.bytes {
				unit = "bytes"
			}
			return tool.fatal(invocation, 1, "invalid number of %s: '%s'", unit, option.value)
		}
		s.count = count
	case "quiet", "silent":
		s.headers = -1
	case "verbose":
		s.headers = 1
	}
	return nil
}

// sliceFiles runs slice over every input with head's and tail's headers and
// error reports.
func (s sliceOptions) sliceFiles(ctx context.Context, invocation *Invocation, tool *textTool, operands []string,
	slice func(io.Reader, io.Writer, sliceOptions) error,
) error {
	settings := s
	inputs := operandsOrStdin(operands)
	printHeaders := settings.headers == 1 || (settings.headers == 0 && len(inputs) > 1)
	var status ExitError
	first := true
	for _, name := range inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		reader, err := openForSlice(ctx, invocation, name)
		if reason, ok := operandReason(err); ok && reason == reasonNoSuchFile {
			tool.complain(invocation, "cannot open '%s' for reading: %s", name, reason)
			status = 1
			continue
		}
		if printHeaders {
			separator := "\n"
			if first {
				separator = ""
			}
			fmt.Fprintf(invocation.Stdout, "%s==> %s <==\n", separator, headerName(name))
			first = false
		}
		if reason, ok := operandReason(err); ok {
			tool.complain(invocation, "error reading '%s': %s", name, reason)
			status = 1
			continue
		}
		if err != nil {
			return err
		}
		if err := slice(reader, invocation.Stdout, settings); err != nil {
			return err
		}
	}
	return exitStatus(status)
}

// openForSlice opens an input as a reader; stdin stays a stream so head can
// stop early.
func openForSlice(ctx context.Context, invocation *Invocation, name string) (io.Reader, error) {
	if name == "-" {
		return invocation.Stdin, nil
	}
	content, err := readOperand(ctx, invocation, name)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(content), nil
}

// headerName is how head and tail name an input in a header.
func headerName(name string) string {
	if name == "-" {
		return "standard input"
	}
	return name
}

// headInput writes the first count lines or bytes of input, or with fromEnd
// all but the last count.
func headInput(input io.Reader, output io.Writer, settings sliceOptions) error {
	if settings.fromEnd {
		content, err := io.ReadAll(input)
		if err != nil {
			return fmt.Errorf("read input: %w", err)
		}
		end := len(content) - min(settings.count, len(content))
		if !settings.bytes {
			end = lineOffsetFromEnd(content, settings.count)
		}
		return writeAll(output, content[:end])
	}
	if settings.bytes {
		if _, err := io.CopyN(output, input, int64(settings.count)); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("copy input: %w", err)
		}
		return nil
	}
	reader := bufio.NewReader(input)
	for written := 0; written < settings.count; written++ {
		line, err := reader.ReadBytes('\n')
		if writeErr := writeAll(output, line); writeErr != nil {
			return writeErr
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read input: %w", err)
		}
	}
	return nil
}

// lineOffsetFromEnd is the offset where the last count lines of content
// begin; a final line without "\n" counts as a line.
func lineOffsetFromEnd(content []byte, count int) int {
	end := len(content)
	if end > 0 && content[end-1] == '\n' {
		end--
	}
	for ; count > 0; count-- {
		index := bytes.LastIndexByte(content[:end], '\n')
		if index < 0 {
			return 0
		}
		end = index
	}
	if end == len(content) {
		return end
	}
	return end + 1
}

func writeAll(output io.Writer, content []byte) error {
	if _, err := output.Write(content); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// countSuffixes are the multipliers GNU's xstrtoumax accepts after a count.
var countSuffixes = map[string]int{
	"": 1, "b": 512, "K": 1 << 10, "k": 1 << 10, "KiB": 1 << 10, "kB": 1000, "KB": 1000,
	"M": 1 << 20, "MiB": 1 << 20, "MB": 1e6, "G": 1 << 30, "GiB": 1 << 30, "GB": 1e9,
	"T": 1 << 40, "TiB": 1 << 40, "TB": 1e12,
}

// parseCount reads a non-negative count with an optional size suffix;
// values beyond int saturate like GNU's beyond uintmax.
func parseCount(text string) (int, bool) {
	digits := strings.TrimRight(text, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	multiplier, ok := countSuffixes[text[len(digits):]]
	if !ok || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, false
	}
	value, err := strconv.Atoi(digits)
	if err != nil || value > math.MaxInt/multiplier {
		return math.MaxInt, true
	}
	return value * multiplier, true
}
