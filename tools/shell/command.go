// Package shell is a virtual bash for agents: the model writes ordinary shell
// (pipes, &&, loops, variables, redirects, globs) and every file the script
// touches is a file of a vfs.Storage, so overlays, dry runs, mounts and path
// policy keep working. Commands are Go implementations of the usual tools
// (cat, grep, sed, find, jq, ...) plus the ones the caller registers: Go
// functions, or host programs it allows explicitly (git, kubectl).
//
// Nothing reaches the host unless the caller registered it: no host files, no
// network, no host environment. A command the script calls that is not
// registered fails like in bash: "bash: line 1: name: command not found",
// exit 127.
//
// The script sees the Storage as "/": absolute paths are workspace paths,
// climbing above the root is "No such file or directory". /tmp is a memory
// scratch area of the Shell (never the Storage) that keeps its files from
// one Run to the next, at most 64 MiB together; /dev/null, /dev/stdin, /dev/stdout
// and /dev/stderr work. A Storage holds only files, so a directory exists
// while a file lies below it or after mkdir created it in this Shell.
// Redirects collect what is written and store it with one WriteFile when
// they close. New builds a Shell, Register adds it to a tools.Registry as
// the "bash" tool, External wraps a host program as a Command.
package shell

import (
	"context"
	"fmt"
	"io"

	"github.com/govengers/aikido/vfs"
)

// Command is one program the shell can run.
type Command struct {
	// Name is what the script types, e.g. "grep". Required, unique per shell.
	Name string
	// Summary is one short line for the tool description the model reads,
	// e.g. "search text (-r -n -l -i -c -v -E -o -w)". Required.
	Summary string
	// Run executes one invocation. It returns nil for exit status 0, an
	// ExitError for another status, and any other error for a failure the
	// shell reports on stderr with status 1. Run must honour ctx.
	Run func(ctx context.Context, invocation *Invocation) error
}

// Invocation is one call of a Command.
type Invocation struct {
	// Arguments are the arguments after the command name, already expanded by
	// the shell (globs, variables, quotes).
	Arguments []string
	// Stdin, Stdout and Stderr are the command's streams. Output written to
	// them is bounded by the shell, not by the command.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Directory is the shell's working directory as a workspace path: "" is
	// the workspace root, otherwise a vfs path without leading slash.
	Directory string
	// Storage is the workspace. Commands resolve relative paths against
	// Directory with Resolve and never touch host files.
	Storage vfs.Storage
	// Environment holds the shell's exported variables as NAME=value.
	Environment []string

	// session gives the built-in commands the shell's created directories
	// and command table; nil when a Command runs outside a Shell.
	session *session
}

// ExitError is a non-zero exit status a Command returns, like grep's 1 for
// "no match".
type ExitError uint8

func (s ExitError) Error() string { return fmt.Sprintf("exit status %d", uint8(s)) }
