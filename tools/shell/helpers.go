package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"mvdan.cc/sh/v3/syntax"
)

// errFailed is the exit status of a command that failed on one of its
// operands, the GNU convention for most tools.
const errFailed ExitError = 1

// complain writes "command: message" to stderr, the GNU error format.
func complain(invocation *Invocation, command, format string, arguments ...any) {
	fmt.Fprintf(invocation.Stderr, "%s: %s\n", command, fmt.Sprintf(format, arguments...))
}

// usage reports a command-line error the GNU way (the message, then a "Try
// --help" line) and returns status.
func usage(invocation *Invocation, command string, err error, status ExitError) error {
	complain(invocation, command, "%v", err)
	fmt.Fprintf(invocation.Stderr, "Try '%s --help' for more information.\n", command)
	return status
}

// runSubcommand runs arguments[0] with the rest as its arguments, the way
// xargs, find -exec and env (the caller) start a command: same streams,
// directory, environment and session, the given stdin. It returns the
// exit status; errors are already on stderr, an unknown command as
// "caller: 'name': No such file or directory" (xargs without the quotes)
// with status 127.
func runSubcommand(ctx context.Context, invocation *Invocation, caller string, arguments []string, stdin io.Reader) uint8 {
	if invocation.session == nil || !invocation.session.known(arguments[0]) {
		name := "'" + arguments[0] + "'"
		if caller == "xargs" {
			name = arguments[0] // GNU xargs does not quote the name
		}
		complain(invocation, caller, "%s: %v", name, errNotFound)
		return statusNotFound
	}
	sub := &Invocation{
		Arguments:   arguments[1:],
		Stdin:       stdin,
		Stdout:      invocation.Stdout,
		Stderr:      invocation.Stderr,
		Directory:   invocation.Directory,
		Storage:     invocation.Storage,
		Environment: invocation.Environment,
		session:     invocation.session,
	}
	return invocation.session.dispatch(ctx, arguments[0], sub, syntax.Pos{})
}

// resolve turns an operand into a workspace path relative to the
// invocation's directory.
func resolve(invocation *Invocation, operand string) (string, error) {
	resolved, err := Resolve(invocation.Directory, operand)
	if err != nil {
		return "", errNotFound
	}
	return resolved, nil
}

// openInput returns the content of an operand: "-" and /dev/stdin are stdin,
// /dev/null is empty, a directory is errIsDirectory.
func openInput(ctx context.Context, invocation *Invocation, operand string) (io.Reader, error) {
	if operand == "-" {
		return invocation.Stdin, nil
	}
	name, err := resolve(invocation, operand)
	if err != nil {
		return nil, err
	}
	switch name {
	case "dev/stdin":
		return invocation.Stdin, nil
	case "dev/null":
		return bytes.NewReader(nil), nil
	}
	content, err := readFile(ctx, workspaceOf(invocation), name)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(content), nil
}

// readInput is openInput read to the end.
func readInput(ctx context.Context, invocation *Invocation, operand string) ([]byte, error) {
	reader, err := openInput(ctx, invocation, operand)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

// readFile reads a workspace file; a directory is errIsDirectory, other
// failures are in GNU wording.
func readFile(ctx context.Context, files workspace, name string) ([]byte, error) {
	if isMountPoint(name) {
		return nil, errIsDirectory
	}
	content, _, err := files.storage.ReadFile(ctx, name)
	if err == nil {
		return content, nil
	}
	if isNotFile(err) {
		isDirectory, listErr := files.isDirectory(ctx, name)
		if listErr != nil {
			return nil, listErr
		}
		if isDirectory {
			return nil, errIsDirectory
		}
	}
	return nil, describe(err)
}
