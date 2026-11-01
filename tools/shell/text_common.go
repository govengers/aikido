package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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

// operandError is a file operand that cannot be read; reason is the
// strerror text GNU prints for it.
type operandError struct{ reason string }

func (e *operandError) Error() string { return e.reason }

// readOperand returns the content of a file operand. "-" and /dev/stdin read
// stdin, /dev/null is empty. A missing file or a directory yields an
// *operandError; other errors come from the storage or ctx.
func readOperand(ctx context.Context, invocation *Invocation, name string) ([]byte, error) {
	switch name {
	case "-", "/dev/stdin":
		content, err := io.ReadAll(invocation.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return content, nil
	case "/dev/null":
		return nil, nil
	}
	resolved, err := Resolve(invocation.Directory, name)
	if err != nil {
		return nil, &operandError{reasonNoSuchFile}
	}
	content, _, err := invocation.Storage.ReadFile(ctx, resolved)
	switch {
	case err == nil:
		return content, nil
	case isNotFile(err):
		// The shell's view decides: directories made with mkdir, the mount
		// point, and paths outside it.
		found, statErr := workspaceOf(invocation).stat(ctx, resolved)
		switch {
		case statErr != nil:
			return nil, fmt.Errorf("stat %s: %w", name, statErr)
		case found.kind == entryDirectory:
			return nil, &operandError{reasonIsDirectory}
		}
		return nil, &operandError{reasonNoSuchFile}
	default:
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
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
