package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"mvdan.cc/sh/v3/interp"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/mount"
)

// Defaults of Options.
const (
	DefaultTimeout            = 60 * time.Second
	DefaultMaximumOutputBytes = 30 << 10
	DefaultToolName           = "bash"
)

// Options configure a Shell. New copies them.
type Options struct {
	// Storage is the workspace the script sees at MountPoint. Required.
	Storage vfs.Storage
	// MountPoint is the absolute path where the workspace appears in the
	// shell, "/" when empty. Scripts start there; pwd, realpath and find
	// print paths below it. Other paths are missing, except /tmp and /dev.
	// Set it to the host directory a Storage mirrors (an osfs over that
	// directory) so built-in commands and External programs agree on every
	// absolute path. It must not be /tmp or lie in /dev; below /tmp it is
	// fine (the workspace then shows up in /tmp next to the scratch files).
	MountPoint string
	// Commands are added to the built-in commands (Builtins); a Command with
	// the name of a built-in replaces it. Names must be unique, and must not
	// be shell builtins (cd, echo, test, ...), which the interpreter runs
	// itself.
	Commands []Command
	// Environment holds the variables a script starts with, as NAME=value.
	// The host environment is never passed. HOME defaults to MountPoint.
	Environment []string
	// Timeout bounds one Run; zero means DefaultTimeout, negative disables
	// it. A script that runs out of time is stopped with exit status 124.
	Timeout time.Duration
	// MaximumOutputBytes bounds Result.Output (stdout and stderr together):
	// the first and the last half are kept, the middle is cut with a marker.
	// Zero means DefaultMaximumOutputBytes, negative keeps everything.
	MaximumOutputBytes int
	// ToolName is the name Register gives the tool; empty means
	// DefaultToolName.
	ToolName string
}

// Result is the outcome of one Run.
type Result struct {
	// Output is stdout and stderr interleaved as the script wrote them,
	// bounded by Options.MaximumOutputBytes.
	Output string
	// ExitCode is the script's exit status, as $? would show it: 127 for a
	// command that does not exist, 124 when the timeout stopped the script, 2
	// for a syntax error.
	ExitCode int
	// Truncated reports that the middle of Output was cut.
	Truncated bool
}

// Shell runs bash scripts over a vfs.Storage. The working directory, the
// directories mkdir created and the files in /tmp (memory, at most 64 MiB
// together) persist from one Run to the next; variables and functions do
// not. Run calls are serialized, so a Shell is safe
// for concurrent use.
type Shell struct {
	options     Options
	commands    map[string]Command
	directories *directorySet
	scratch     *scratchStorage // /tmp
	// root is the MountPoint as a path of the session Storage: "" for "/".
	root string

	mutex     sync.Mutex
	directory string // the working directory, a workspace path
}

// New returns a Shell over options.Storage. It returns an error for nil
// options, a nil Storage, an Environment entry without "=", and a Command
// without Name, Summary or Run, with a name used twice in Commands, with a
// name containing "/" or white space, or with the name of a shell builtin.
func New(options *Options) (*Shell, error) {
	if options == nil || options.Storage == nil {
		return nil, errors.New("aikido/tools/shell: New: Storage is required")
	}
	configured := *options
	configured.Environment = slices.Clone(options.Environment)
	configured.Commands = slices.Clone(options.Commands)
	if err := validateEnvironment(configured.Environment); err != nil {
		return nil, err
	}
	mountPoint, err := validateMountPoint(configured.MountPoint)
	if err != nil {
		return nil, err
	}
	configured.MountPoint = mountPoint
	if !slices.ContainsFunc(configured.Environment, func(variable string) bool { return strings.HasPrefix(variable, "HOME=") }) {
		configured.Environment = append(configured.Environment, "HOME="+mountPoint)
	}
	commands, err := commandTable(configured.Commands)
	if err != nil {
		return nil, err
	}
	if configured.Timeout == 0 {
		configured.Timeout = DefaultTimeout
	}
	if configured.MaximumOutputBytes == 0 {
		configured.MaximumOutputBytes = DefaultMaximumOutputBytes
	}
	if configured.ToolName == "" {
		configured.ToolName = DefaultToolName
	}
	root := strings.TrimPrefix(mountPoint, "/")
	return &Shell{
		options: configured, commands: commands, root: root, directory: root,
		directories: newDirectorySet(), scratch: newScratchStorage(maximumScratchBytes),
	}, nil
}

// validateMountPoint returns the cleaned mount point, "/" for empty.
func validateMountPoint(mountPoint string) (string, error) {
	if mountPoint == "" {
		return "/", nil
	}
	cleaned := path.Clean(mountPoint)
	root := strings.TrimPrefix(cleaned, "/")
	switch {
	case !strings.HasPrefix(mountPoint, "/"):
		return "", fmt.Errorf("aikido/tools/shell: New: MountPoint %q must be absolute", mountPoint)
	case root == scratchDirectory || within(root, "dev"):
		return "", fmt.Errorf("aikido/tools/shell: New: MountPoint %q must not be /tmp or lie in /dev", mountPoint)
	case root != "" && vfs.ValidatePath(root) != nil:
		return "", fmt.Errorf("aikido/tools/shell: New: MountPoint %q is not a valid path", mountPoint)
	}
	return cleaned, nil
}

func validateEnvironment(environment []string) error {
	for _, variable := range environment {
		if name, _, ok := strings.Cut(variable, "="); !ok || name == "" {
			return fmt.Errorf("aikido/tools/shell: New: Environment entry %q is not NAME=value", variable)
		}
	}
	return nil
}

// commandTable merges the built-ins with the caller's commands, which win.
func commandTable(extra []Command) (map[string]Command, error) {
	commands := map[string]Command{}
	for _, command := range Builtins() {
		commands[command.Name] = command
	}
	seen := map[string]bool{}
	for _, command := range extra {
		if err := validateCommand(command); err != nil {
			return nil, err
		}
		if seen[command.Name] {
			return nil, fmt.Errorf("aikido/tools/shell: New: command %q is listed twice", command.Name)
		}
		seen[command.Name] = true
		commands[command.Name] = command
	}
	return commands, nil
}

func validateCommand(command Command) error {
	switch {
	case command.Name == "" || strings.ContainsAny(command.Name, "/ \t\n"):
		return fmt.Errorf("aikido/tools/shell: New: command name %q must be one word without slashes", command.Name)
	case interp.IsBuiltin(command.Name):
		return fmt.Errorf("aikido/tools/shell: New: %q is a shell builtin; the interpreter would never run the command", command.Name)
	case command.Summary == "":
		return fmt.Errorf("aikido/tools/shell: New: command %q has no Summary", command.Name)
	case command.Run == nil:
		return fmt.Errorf("aikido/tools/shell: New: command %q has no Run", command.Name)
	}
	return nil
}

// Run executes script like bash -c and returns its output and exit status.
// A failing script is not an error: its status and messages are in the
// Result. Run returns an error only when ctx ends before the script does
// (the Result then holds the output so far) or the interpreter cannot start.
func (s *Shell) Run(ctx context.Context, script string) (Result, error) {
	output := newBoundedOutput(s.options.MaximumOutputBytes)
	exitCode, err := s.run(ctx, script, output, output)
	text, truncated := output.result()
	return Result{Output: text, ExitCode: exitCode, Truncated: truncated}, err
}

// run executes script with separate stdout and stderr writers.
func (s *Shell) run(ctx context.Context, script string, stdout, stderr io.Writer) (int, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	call, err := s.newSession()
	if err != nil {
		return 0, err
	}
	file, err := call.parse(script, "")
	if err != nil {
		fmt.Fprintf(stderr, "bash: %v\n", err)
		return statusUnsupported, nil
	}
	s.recoverDirectory(ctx, call, stderr)
	runner, err := call.newRunner(runnerStart{
		directory:   s.directory,
		environment: s.options.Environment,
		stdout:      stdout,
		stderr:      stderr,
	})
	if err != nil {
		return 0, err
	}
	runContext, cancel := s.withTimeout(ctx)
	defer cancel()
	runErr := runFile(runContext, runner, file)
	s.directory = workspaceDirectory(runner.Dir)
	if status, ok := exitStatusOf(runErr); ok {
		return status, nil
	}
	if ctx.Err() != nil {
		return statusCommandFailure, ctx.Err()
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		fmt.Fprintf(stderr, "bash: timed out after %v; the script was stopped\n", s.options.Timeout)
		return statusTimedOut, nil
	}
	fmt.Fprintf(stderr, "bash: %v\n", runErr)
	return statusCommandFailure, nil
}

// newSession mounts the Shell's /tmp and a fresh /dev/fd for the files of
// <(list) over the workspace for one Run.
func (s *Shell) newSession() (*session, error) {
	storage, err := mount.New(&mount.Options{Mounts: []mount.Mount{
		{Prefix: s.root, Storage: s.options.Storage},
		{Prefix: scratchDirectory, Storage: s.scratch},
		{Prefix: processFileDirectory, Storage: processFileStorage{memory.New()}},
	}})
	if err != nil {
		return nil, fmt.Errorf("aikido/tools/shell: mount /tmp and /dev/fd: %w", err)
	}
	return &session{storage: storage, root: s.root, directories: s.directories, commands: s.commands}, nil
}

// recoverDirectory moves back to the mount point when the working directory
// no longer exists (a previous call removed it, or the Storage changed).
func (s *Shell) recoverDirectory(ctx context.Context, call *session, stderr io.Writer) {
	if s.directory == s.root {
		return
	}
	found, err := call.files().stat(ctx, s.directory)
	if err == nil && found.kind == entryDirectory {
		return
	}
	fmt.Fprintf(stderr, "bash: the working directory /%s no longer exists; now in %s\n", s.directory, s.options.MountPoint)
	s.directory = s.root
}

func (s *Shell) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.options.Timeout < 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.options.Timeout)
}

// commandNames returns the registered command names, sorted.
func (s *Shell) commandNames() []string {
	names := make([]string, 0, len(s.commands))
	for name := range s.commands {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
