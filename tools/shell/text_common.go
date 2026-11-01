package shell

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/govengers/aikido/vfs"
)

// textTool carries what the text commands share for reporting errors the way
// their GNU counterparts do.
type textTool struct {
	name string
	// hint follows a usage error, e.g. "Try 'wc --help' for more information.\n".
	hint string
	// usageStatus is the exit status of a usage error (1, or 2 for grep, sort, diff).
	usageStatus ExitError
	// help is printed on stdout for --help.
	help string
}

// usageFailure reports a command-line error and returns the usage status.
func (t *textTool) usageFailure(invocation *Invocation, message string) error {
	fmt.Fprintf(invocation.Stderr, "%s: %s\n%s", t.name, message, t.hint)
	return t.usageStatus
}

// fatal reports "name: message" and returns status.
func (t *textTool) fatal(invocation *Invocation, status ExitError, format string, arguments ...any) error {
	t.complain(invocation, format, arguments...)
	return status
}

// complain writes "name: message" to stderr.
func (t *textTool) complain(invocation *Invocation, format string, arguments ...any) {
	fmt.Fprintf(invocation.Stderr, "%s: %s\n", t.name, fmt.Sprintf(format, arguments...))
}

// commandLine is a parsed command line. done means the command has already
// finished (--help, or a usage error that was reported) and returns the
// accompanying error.
type commandLine struct {
	options  []parsedOption
	operands []string
	done     bool
}

// parseOptions runs parser and turns a usage error into GNU's report; it
// also answers --help.
func (t *textTool) parseOptions(invocation *Invocation, parser *optionParser) (commandLine, error) {
	options, operands, err := parser.parse(invocation.Arguments)
	var usage *usageError
	if errors.As(err, &usage) {
		return commandLine{done: true}, t.usageFailure(invocation, usage.message)
	}
	for _, option := range options {
		if option.key == "help" {
			if _, err := io.WriteString(invocation.Stdout, t.help); err != nil {
				return commandLine{done: true}, fmt.Errorf("write help: %w", err)
			}
			return commandLine{done: true}, nil
		}
	}
	return commandLine{options: options, operands: operands}, nil
}

// helpOption is the --help every text command answers.
var helpOption = optionSpec{long: "help"}

// The reasons GNU tools print after a file name, from strerror.
const (
	reasonNoSuchFile  = "No such file or directory"
	reasonIsDirectory = "Is a directory"
)

// maximumInputBytes is the input budget of a text command: the most it
// holds of its input in memory at once, a whole input for the commands
// that need one (sort, tail, diff, jq, xargs), one line for the ones that
// stream (grep, sed, cut, uniq), or all inputs together. A pipe has no
// end of its own, so this bounds what a producer can make a command hold.
const maximumInputBytes = maximumBufferedFileBytes

// errInputTooLarge is an input beyond maximumInputBytes; boundedInput
// reports it as "name: input exceeds 67108864 bytes" with status 2.
var errInputTooLarge = fmt.Errorf("input exceeds %d bytes", maximumInputBytes)

// boundedInput wraps a command's Run so an input beyond the budget ends it
// with GNU's status for trouble (2) and one clear message.
func boundedInput(command Command) Command {
	run := command.Run
	command.Run = func(ctx context.Context, invocation *Invocation) error {
		err := run(ctx, invocation)
		if errors.Is(err, errInputTooLarge) {
			complain(invocation, command.Name, "%v", errInputTooLarge)
			return ExitError(2)
		}
		return err
	}
	return command
}

// inputBudget counts what a command holds of several inputs together.
type inputBudget struct{ used int }

// charge adds size bytes; beyond maximumInputBytes it returns
// errInputTooLarge.
func (b *inputBudget) charge(size int) error {
	b.used += size
	if b.used > maximumInputBytes {
		return errInputTooLarge
	}
	return nil
}

// readBounded reads reader to the end, or fails with errInputTooLarge
// after maximumInputBytes.
func readBounded(reader io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maximumInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if len(content) > maximumInputBytes {
		return nil, errInputTooLarge
	}
	return content, nil
}

// lineReader reads a stream line by line, each line within the budget, so
// a command can stop early (grep -q, sed q) and holds one line at a time.
type lineReader struct {
	reader *bufio.Reader
}

func newLineReader(reader io.Reader) *lineReader {
	return &lineReader{reader: bufio.NewReaderSize(reader, 2*binaryProbeBytes)}
}

// binaryProbeBytes is how much of an input startsBinary looks at, like the
// first buffer GNU grep checks.
const binaryProbeBytes = 32 << 10

// startsBinary reports whether the first bytes contain a NUL. It waits for
// them (or the end of input) without consuming anything.
func (r *lineReader) startsBinary() bool {
	start, _ := r.reader.Peek(binaryProbeBytes)
	return bytes.IndexByte(start, 0) >= 0
}

// openOperand opens an input for streaming: stdin stays a stream, a file
// is read from the Storage. Errors are readOperand's.
func openOperand(ctx context.Context, invocation *Invocation, name string) (io.Reader, error) {
	if name == "-" || name == "/dev/stdin" {
		return invocation.Stdin, nil
	}
	content, err := readOperand(ctx, invocation, name)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(content), nil
}

// next returns the next line without its "\n" and whether it had one; it
// returns io.EOF after the last line and errInputTooLarge for a line
// beyond the budget.
func (r *lineReader) next() (string, bool, error) {
	var line []byte
	for {
		chunk, err := r.reader.ReadSlice('\n')
		if len(line)+len(chunk) > maximumInputBytes+1 {
			return "", false, errInputTooLarge
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return string(line[:len(line)-1]), true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return string(line), false, nil
		case errors.Is(err, io.EOF):
			return "", false, io.EOF
		default:
			return "", false, fmt.Errorf("read input: %w", err)
		}
	}
}

// operandError is a file operand that cannot be read; reason is the
// strerror text GNU prints for it.
type operandError struct{ reason string }

func (e *operandError) Error() string { return e.reason }

// readOperand returns the content of a file operand. "-" and /dev/stdin read
// stdin, at most maximumInputBytes of it, /dev/null is empty. A missing
// file or a directory yields an *operandError; other errors come from the
// storage or ctx, errInputTooLarge from a stdin beyond the budget.
func readOperand(ctx context.Context, invocation *Invocation, name string) ([]byte, error) {
	switch name {
	case "-", "/dev/stdin":
		return readBounded(invocation.Stdin)
	case "/dev/null":
		return nil, nil
	}
	resolved, err := Resolve(invocation.Directory, name)
	if err != nil {
		return nil, &operandError{reasonNoSuchFile}
	}
	if resolved == "" {
		return nil, &operandError{reasonIsDirectory}
	}
	content, _, err := invocation.Storage.ReadFile(ctx, resolved)
	switch {
	case err == nil:
		return content, nil
	case errors.Is(err, vfs.ErrFileNotFound), errors.Is(err, vfs.ErrPathInvalid):
		isDirectory, listError := directoryExists(ctx, invocation.Storage, resolved)
		if listError != nil {
			return nil, listError
		}
		if isDirectory {
			return nil, &operandError{reasonIsDirectory}
		}
		return nil, &operandError{reasonNoSuchFile}
	default:
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
}

// directoryExists reports whether a workspace path is a directory, that is
// whether a file lies below it. The root always exists.
func directoryExists(ctx context.Context, storage vfs.Storage, directory string) (bool, error) {
	if directory == "" {
		return true, nil
	}
	files, err := storage.ListFiles(ctx)
	if err != nil {
		return false, fmt.Errorf("list files: %w", err)
	}
	prefix := directory + "/"
	for _, file := range files {
		if strings.HasPrefix(file.Path, prefix) {
			return true, nil
		}
	}
	return false, nil
}

// operandReason is the strerror text for err if it is an *operandError.
func operandReason(err error) (string, bool) {
	var problem *operandError
	if errors.As(err, &problem) {
		return problem.reason, true
	}
	return "", false
}

// splitLines splits text into lines without their "\n". A final line
// without a newline is kept; terminated reports whether the text ended in
// "\n" (or was empty).
func splitLines(text string) ([]string, bool) {
	if text == "" {
		return nil, true
	}
	terminated := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	return lines, terminated
}

// joinLines writes each line followed by "\n", as tools that always
// terminate their output do (sort, uniq, cut).
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// operandsOrStdin is the list of inputs: the operands, or "-" for none.
func operandsOrStdin(operands []string) []string {
	if len(operands) == 0 {
		return []string{"-"}
	}
	return operands
}

// exitStatus turns an accumulated status into Run's result.
func exitStatus(status ExitError) error {
	if status == 0 {
		return nil
	}
	return status
}
