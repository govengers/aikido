package shell

import (
	"bufio"
	"context"
	"errors"
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
// It streams: the last count lines or bytes are kept while reading, within
// maximumInputBytes, and +N skips and copies.
func tailInput(input io.Reader, output io.Writer, settings sliceOptions) error {
	switch {
	case settings.fromEnd && settings.bytes:
		return tailBytes(input, output, settings.count)
	case settings.fromEnd:
		return tailLines(input, output, settings.count)
	}
	skip := settings.count
	if skip > 0 {
		skip--
	}
	reader := bufio.NewReader(input)
	if settings.bytes {
		if _, err := io.CopyN(io.Discard, reader, int64(skip)); err != nil {
			return ignoreEOF(err)
		}
	}
	for lines := 0; !settings.bytes && lines < skip; {
		chunk, err := reader.ReadSlice('\n')
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return ignoreEOF(err)
		}
		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			lines++
		}
	}
	if _, err := io.Copy(output, reader); err != nil {
		return fmt.Errorf("copy input: %w", err)
	}
	return nil
}

// tailLines writes the last count lines, keeping only those while reading.
func tailLines(input io.Reader, output io.Writer, count int) error {
	if count == 0 {
		return nil
	}
	reader := newLineReader(input)
	var kept []string
	held := 0
	for {
		line, terminated, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if terminated {
			line += "\n"
		}
		kept = append(kept, line)
		held += len(line)
		if len(kept) > count {
			held -= len(kept[0])
			kept = kept[1:]
		}
		if held > maximumInputBytes {
			return errInputTooLarge
		}
	}
	return writeAll(output, []byte(strings.Join(kept, "")))
}

// tailBytes writes the last count bytes, keeping at most twice that while
// reading.
func tailBytes(input io.Reader, output io.Writer, count int) error {
	if count > maximumInputBytes {
		content, err := readBounded(input)
		if err != nil {
			return err
		}
		return writeAll(output, content)
	}
	var kept []byte
	chunk := make([]byte, 32<<10)
	for {
		read, err := input.Read(chunk)
		kept = append(kept, chunk[:read]...)
		if len(kept) > 2*count+len(chunk) {
			kept = append(kept[:0], kept[len(kept)-count:]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read input: %w", err)
		}
	}
	return writeAll(output, kept[len(kept)-min(count, len(kept)):])
}

// ignoreEOF treats the end of input as the normal end.
func ignoreEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("read input: %w", err)
}
