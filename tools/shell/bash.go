package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func bashCommand(name string) Command {
	return Command{
		Name:    name,
		Summary: "run a script in a new shell: " + name + " [-e] [-u] [-x] [-o option] (-c script [name [args]] | file [args])",
		Run: func(ctx context.Context, invocation *Invocation) error {
			return runBash(ctx, name, invocation)
		},
	}
}

// bashArguments are the parsed arguments of bash or sh.
type bashArguments struct {
	options    []string // -e, -u, -x, -o name, as interp.Params takes them
	script     string
	hasScript  bool // -c
	file       string
	positional []string
}

func runBash(ctx context.Context, name string, invocation *Invocation) error {
	if invocation.session == nil {
		complain(invocation, name, "can only run inside the shell")
		return ExitError(statusNotFound)
	}
	parsed, err := parseBashArguments(invocation.Arguments)
	if err != nil {
		complain(invocation, name, "%v", err)
		return ExitError(statusUnsupported)
	}
	script, scriptName := parsed.script, ""
	if !parsed.hasScript {
		source := "-"
		if parsed.file != "" {
			source, scriptName = parsed.file, parsed.file
		}
		content, err := readInput(ctx, invocation, source)
		if err != nil {
			complain(invocation, name, "%s: %v", source, err)
			return ExitError(statusNotFound)
		}
		script = string(content)
	}
	file, err := invocation.session.parse(script, scriptName)
	if err != nil {
		complain(invocation, name, "%v", err)
		return ExitError(statusUnsupported)
	}
	stdin := invocation.Stdin
	if !parsed.hasScript && parsed.file == "" {
		stdin = emptyInput()
	}
	parameters := append(append([]string{}, parsed.options...), "--")
	return invocation.session.runNested(ctx, file, invocation, stdin, append(parameters, parsed.positional...))
}

// parseBashArguments reads bash's options up to the script: -c takes the
// next operand as the script, else the first operand is a file.
func parseBashArguments(arguments []string) (bashArguments, error) {
	parsed := bashArguments{}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		switch {
		case argument == "-c":
			parsed.hasScript = true
		case argument == "-o" || argument == "+o":
			rest := arguments[index+1:]
			if len(rest) == 0 {
				return parsed, errors.New(argument + ": option requires an argument")
			}
			parsed.options = append(parsed.options, argument, rest[0])
			index++
		case argument == "--":
			return parsed.withOperands(arguments[index+1:])
		case len(argument) == 2 && (argument[0] == '-' || argument[0] == '+') && strings.ContainsRune("eux", rune(argument[1])):
			parsed.options = append(parsed.options, argument)
		case strings.HasPrefix(argument, "-") && argument != "-":
			return parsed, fmt.Errorf("%s: invalid option", argument)
		default:
			return parsed.withOperands(arguments[index:])
		}
	}
	return parsed.withOperands(nil)
}

// withOperands assigns the operands: with -c the script and $0 (dropped,
// the interpreter does not set it), else the script file; the rest are the
// positional parameters.
func (b bashArguments) withOperands(operands []string) (bashArguments, error) {
	if b.hasScript {
		if len(operands) == 0 {
			return b, errors.New("-c: option requires an argument")
		}
		b.script, operands = operands[0], operands[1:]
		if len(operands) > 0 {
			operands = operands[1:]
		}
	} else if len(operands) > 0 {
		b.file, operands = operands[0], operands[1:]
	}
	b.positional = operands
	return b, nil
}

// runBuiltin runs a shell builtin (echo, printf, test, ...) for xargs, find
// -exec and env, which start commands by name: a nested interpreter runs
// "$@".
func (s *session) runBuiltin(ctx context.Context, name string, invocation *Invocation) uint8 {
	file, err := s.parse(`"$@"`, "")
	if err != nil {
		complain(invocation, name, "%v", err)
		return statusInternalError
	}
	err = s.runNested(ctx, file, invocation, invocation.Stdin, append([]string{"--", name}, invocation.Arguments...))
	var exit ExitError
	if errors.As(err, &exit) {
		return uint8(exit)
	}
	return 0
}

// runNested runs a parsed script in a new interpreter that starts where
// invocation is (directory, environment, streams). Its status is an
// ExitError; cancellation is ctx's error.
func (s *session) runNested(ctx context.Context, file *syntax.File, invocation *Invocation, stdin io.Reader, parameters []string) error {
	runner, err := s.newRunner(runnerStart{
		directory:   invocation.Directory,
		environment: invocation.Environment,
		parameters:  parameters,
		stdin:       stdin,
		stdout:      invocation.Stdout,
		stderr:      invocation.Stderr,
	})
	if err != nil {
		return err
	}
	runErr := runFile(ctx, runner, file)
	if status, ok := exitStatusOf(runErr); ok {
		if status == 0 {
			return nil
		}
		return ExitError(status) //nolint:gosec // G115: exit statuses are 0-255
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return runErr
}
