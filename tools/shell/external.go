package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// externalWaitDelay is how long External waits for a killed program's
// output pipes to close (a grandchild may hold them) before it returns.
const externalWaitDelay = 2 * time.Second

// ExternalOptions configure a host program the shell may run.
type ExternalOptions struct {
	// Name is the command name scripts use, e.g. "git". Required, one word.
	Name string
	// Path is the absolute path of the host binary, e.g. "/usr/bin/git".
	// Required; External checks that it exists.
	Path string
	// Directory is the host working directory of the program, e.g. a git
	// checkout. Required and absolute. The program sees the host file
	// system there, not the workspace, and the shell's cd does not move it.
	Directory string
	// Environment is the complete environment of the program as
	// NAME=value; nil or empty means none. The host environment and the
	// script's variables are never passed.
	Environment []string
	// Allow, if set, is asked before every run with the arguments (without
	// the name). A non-nil error refuses the run: its text goes to stderr
	// and the exit status is 1.
	Allow func(arguments []string) error
	// Timeout bounds one run; zero means none beyond the shell's own
	// timeout. A program that runs out of time is killed with its process
	// group (exit status 124).
	Timeout time.Duration
	// Summary is the line the tool description shows; empty means "host
	// program <Path>".
	Summary string
}

// External returns a Command that runs a host program, for Options.Commands.
// Stdin, stdout and stderr are the script's streams, so pipes work
// (git log | grep fix). A canceled script kills the program and its process
// group.
//
// It returns an error for nil options, a Name that is empty or not one
// word, a Path that is not absolute or not a regular file, or a Directory
// that is not an absolute path to a directory.
func External(options *ExternalOptions) (Command, error) {
	if options == nil {
		return Command{}, errors.New("aikido/tools/shell: External: options are required")
	}
	configured := *options
	configured.Environment = slices.Clone(options.Environment)
	if err := validateExternal(configured); err != nil {
		return Command{}, err
	}
	summary := configured.Summary
	if summary == "" {
		summary = "host program " + configured.Path
	}
	return Command{
		Name:    configured.Name,
		Summary: summary,
		Run: func(ctx context.Context, invocation *Invocation) error {
			return runExternal(ctx, configured, invocation)
		},
	}, nil
}

func validateExternal(options ExternalOptions) error {
	switch {
	case options.Name == "" || strings.ContainsAny(options.Name, "/ \t\n"):
		return fmt.Errorf("aikido/tools/shell: External: name %q must be one word without slashes", options.Name)
	case !filepath.IsAbs(options.Path):
		return fmt.Errorf("aikido/tools/shell: External %s: Path %q must be absolute", options.Name, options.Path)
	case !filepath.IsAbs(options.Directory):
		return fmt.Errorf("aikido/tools/shell: External %s: Directory %q must be absolute", options.Name, options.Directory)
	}
	if info, err := os.Stat(options.Path); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("aikido/tools/shell: External %s: Path %q is not a regular file", options.Name, options.Path)
	}
	if info, err := os.Stat(options.Directory); err != nil || !info.IsDir() {
		return fmt.Errorf("aikido/tools/shell: External %s: Directory %q is not a directory", options.Name, options.Directory)
	}
	return nil
}

func runExternal(ctx context.Context, options ExternalOptions, invocation *Invocation) error {
	if options.Allow != nil {
		if err := options.Allow(slices.Clone(invocation.Arguments)); err != nil {
			complain(invocation, options.Name, "%v", err)
			return errFailed
		}
	}
	runContext, cancel := ctx, context.CancelFunc(func() {})
	if options.Timeout > 0 {
		runContext, cancel = context.WithTimeout(ctx, options.Timeout)
	}
	defer cancel()
	//nolint:gosec // G204: running the host program the caller registered is what External is for
	command := exec.CommandContext(runContext, options.Path, invocation.Arguments...)
	command.Dir = options.Directory
	command.Env = append([]string{}, options.Environment...) // never nil: nil would inherit the host environment
	command.Stdin = stdinFile(invocation.Stdin)
	command.Stdout = invocation.Stdout
	command.Stderr = invocation.Stderr
	command.WaitDelay = externalWaitDelay
	killProcessGroup(command)
	err := command.Run()
	return externalStatus(ctx, runContext, options, invocation, err)
}

// externalStatus turns the end of a host program into the command's result.
func externalStatus(ctx, runContext context.Context, options ExternalOptions, invocation *Invocation, err error) error {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case runContext.Err() != nil:
		complain(invocation, options.Name, "killed after the %v timeout", options.Timeout)
		return ExitError(statusTimedOut)
	case errors.As(err, &exitErr):
		if code := exitErr.ExitCode(); code >= 0 {
			return ExitError(code) //nolint:gosec // G115: exit codes are 0-255
		}
		return ExitError(128 + signalOf(exitErr))
	case errors.Is(err, exec.ErrWaitDelay):
		return nil
	}
	complain(invocation, options.Name, "%v", err)
	return ExitError(statusCannotExecute)
}
