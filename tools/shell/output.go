package shell

import (
	"fmt"
	"io"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// internalOutput runs echo or printf (Arguments[0]) through the exec
// handler instead of the interpreter, see routesOutput.
const internalOutput = "\x00output"

// routesOutput reports whether a call of echo or printf goes to
// internalOutput: when its stdout or stderr is a redirect target. The
// interpreter's builtins ignore write errors, so "echo x >/tmp/f && echo
// saved" would print saved with /tmp full; the shell's own echo and printf
// fail like bash's, and as exec handler calls their redirect is stored when
// they end (flushOwned). Neither keeps state (the interpreter's printf has
// no -v), so running them outside the interpreter changes nothing else.
func routesOutput(handler interp.HandlerContext, arguments []string) bool {
	if arguments[0] != "echo" && arguments[0] != "printf" {
		return false
	}
	_, stdout := handler.Stdout.(*bufferedFile)
	_, stderr := handler.Stderr.(*bufferedFile)
	return stdout || stderr
}

// runOutput runs echo or printf as the interpreter does, but a failed write
// is reported ("bash: line 1: echo: write error: No space left on device")
// and fails the call, as in bash.
func runOutput(invocation *Invocation, position syntax.Pos) uint8 {
	name, arguments := invocation.Arguments[0], invocation.Arguments[1:]
	output := &stickyWriter{writer: invocation.Stdout}
	status := uint8(0)
	if name == "echo" {
		echo(output, arguments)
	} else {
		status = printf(output, invocation.Stderr, arguments)
	}
	if output.err != nil {
		fmt.Fprintf(invocation.Stderr, "bash: %s%s: write error: %v\n", linePrefix(position), name, describe(output.err))
		return statusCommandFailure
	}
	return status
}

// echo is the interpreter's echo builtin (mvdan.cc/sh/v3 interp).
func echo(output io.Writer, arguments []string) {
	newline, escapes := true, false
options:
	for len(arguments) > 0 {
		switch arguments[0] {
		case "-n":
			newline = false
		case "-e":
			escapes = true
		case "-E":
		default:
			break options
		}
		arguments = arguments[1:]
	}
	for index, argument := range arguments {
		if index > 0 {
			_, _ = io.WriteString(output, " ")
		}
		if escapes {
			argument, _, _ = expand.Format(&expand.Config{}, argument, nil)
		}
		_, _ = io.WriteString(output, argument)
	}
	if newline {
		_, _ = io.WriteString(output, "\n")
	}
}

// printf is the interpreter's printf builtin without -v (mvdan.cc/sh/v3
// interp), messages included.
func printf(output, stderr io.Writer, arguments []string) uint8 {
	if len(arguments) == 0 {
		fmt.Fprint(stderr, "usage: printf format [arguments]\n")
		return statusUnsupported
	}
	format, arguments := arguments[0], arguments[1:]
	for {
		formatted, consumed, err := expand.Format(&expand.Config{}, format, arguments)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return statusCommandFailure
		}
		_, _ = io.WriteString(output, formatted)
		arguments = arguments[consumed:]
		if consumed == 0 || len(arguments) == 0 {
			return 0
		}
	}
}

// stickyWriter keeps the first write error and writes nothing after it.
type stickyWriter struct {
	writer io.Writer
	err    error
}

func (w *stickyWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	written, err := w.writer.Write(data)
	w.err = err
	return written, err //nolint:wrapcheck // the error is the stream's, reported by runOutput
}
