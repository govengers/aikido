package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
)

var tailTool = &textTool{
	name:        "tail",
	hint:        "Try 'tail --help' for more information.\n",
	usageStatus: 1,
	help: `Usage: tail [OPTION]... [FILE]...
Print the last 10 lines of each FILE to standard output.
  -c, --bytes=[+]NUM       output the last NUM bytes; with +NUM, start at byte NUM
  -n, --lines=[+]NUM       output the last NUM lines; with +NUM, start at line NUM
  -q, --quiet, --silent    never output headers giving file names
  -v, --verbose            always output headers giving file names
`,
}

var tailParser = &optionParser{specs: []optionSpec{
	{short: 'c', long: "bytes", argument: requiredArgument},
	{short: 'n', long: "lines", argument: requiredArgument},
	{short: 'q', long: "quiet"},
	{long: "silent"},
	{short: 'v', long: "verbose"},
	helpOption,
}}

func tailCommand() Command {
	return Command{Name: "tail", Summary: "last lines of files (-n N, -n +N, -N, -c N, -q, -v)", Run: runTail}
}

func runTail(ctx context.Context, invocation *Invocation) error {
	invocation = withObsoleteCount(invocation, "-n")
	if len(invocation.Arguments) > 0 && len(invocation.Arguments[0]) > 1 && invocation.Arguments[0][0] == '+' &&
		strings.Trim(invocation.Arguments[0][1:], "0123456789") == "" {
		rewritten := *invocation
		rewritten.Arguments = append([]string{"-n"}, invocation.Arguments...)
		invocation = &rewritten
	}
	line, err := tailTool.parseOptions(invocation, tailParser)
	if line.done {
		return err
	}
	settings := sliceOptions{count: 10, fromEnd: true}
	for _, option := range line.options {
		if usageErr := settings.apply(invocation, tailTool, option, true); usageErr != nil {
			return usageErr
		}
	}
	return settings.sliceFiles(ctx, invocation, tailTool, line.operands, tailInput)
}

// tailInput writes the last count lines or bytes of input, or without
// fromEnd everything from line or byte count on (1-based, 0 counts as 1).
func tailInput(input io.Reader, output io.Writer, settings sliceOptions) error {
	content, err := io.ReadAll(input)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	if settings.fromEnd {
		start := len(content) - min(settings.count, len(content))
		if !settings.bytes {
			start = lineOffsetFromEnd(content, settings.count)
		}
		return writeAll(output, content[start:])
	}
	skip := settings.count
	if skip > 0 {
		skip--
	}
	if settings.bytes {
		return writeAll(output, content[min(skip, len(content)):])
	}
	start := 0
	for ; skip > 0 && start < len(content); skip-- {
		index := bytes.IndexByte(content[start:], '\n')
		if index < 0 {
			return nil
		}
		start += index + 1
	}
	return writeAll(output, content[start:])
}
