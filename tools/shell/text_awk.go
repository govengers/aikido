package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/benhoyt/goawk/interp"
	"github.com/benhoyt/goawk/lexer"
	"github.com/benhoyt/goawk/parser"
)

const awkUsage = `Usage: awk [-F fs] [-v var=value] [-f progfile | 'program'] [file ...]
POSIX awk (GoAWK inside). File arguments and print > "file" use the workspace;
system(), "cmd" | getline and print | "cmd" are not available.
  -F fs            field separator
  -v var=value     assign a variable before the program runs (repeatable)
  -f progfile      read the program from progfile (repeatable)
`

func awkCommand() Command {
	return Command{Name: "awk", Summary: "POSIX awk via GoAWK (-F FS -v var=value -f progfile; no system() or pipes)", Run: runAwk}
}

// awkSettings is a parsed awk command line.
type awkSettings struct {
	variables    []string
	programFiles []string
	program      string
	operands     []string
}

func runAwk(ctx context.Context, invocation *Invocation) error {
	settings, message := parseAwkArguments(invocation.Arguments)
	switch {
	case message == "help":
		if _, err := io.WriteString(invocation.Stdout, awkUsage); err != nil {
			return fmt.Errorf("write help: %w", err)
		}
		return nil
	case message != "":
		fmt.Fprintf(invocation.Stderr, "awk: %s\n%s", message, awkUsage)
		return ExitError(2)
	}
	source := settings.program
	if len(settings.programFiles) > 0 {
		var err error
		if source, err = readAwkPrograms(ctx, invocation, settings.programFiles); err != nil {
			return err
		}
	}
	program, err := parser.ParseProgram([]byte(source), nil)
	if err != nil {
		fmt.Fprintf(invocation.Stderr, "awk: %v\n", err)
		return ExitError(1)
	}
	return executeAwk(ctx, invocation, settings, program)
}

// parseAwkArguments reads awk's options; the message is the complaint, or
// "help".
func parseAwkArguments(arguments []string) (awkSettings, string) {
	settings := awkSettings{}
	index := 0
	for ; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			index++
			break
		}
		if argument == "--help" {
			return settings, "help"
		}
		if len(argument) < 2 || argument[0] != '-' {
			break
		}
		flag, value := argument[1], argument[2:]
		if strings.IndexByte("Fvf", flag) < 0 {
			return settings, fmt.Sprintf("invalid option -- '%c'", flag)
		}
		if value == "" {
			if index+1 >= len(arguments) {
				return settings, fmt.Sprintf("option requires an argument -- '%c'", flag)
			}
			index++
			value = arguments[index]
		}
		if message := settings.apply(flag, value); message != "" {
			return settings, message
		}
	}
	rest := arguments[index:]
	if len(settings.programFiles) == 0 {
		if len(rest) == 0 {
			return settings, "no program given"
		}
		settings.program, rest = rest[0], rest[1:]
	}
	settings.operands = rest
	return settings, ""
}

// apply records -F, -v or -f with its value.
func (s *awkSettings) apply(flag byte, value string) string {
	switch flag {
	case 'F':
		s.variables = append(s.variables, "FS="+awkFieldSeparator(value))
	case 'v':
		if !strings.Contains(value, "=") {
			return fmt.Sprintf("`%s' argument to `-v' not in `var=value' form", value)
		}
		s.variables = append(s.variables, value)
	default:
		s.programFiles = append(s.programFiles, value)
	}
	return ""
}

// awkFieldSeparator applies POSIX's special case: -F t means a tab.
func awkFieldSeparator(value string) string {
	if value == "t" {
		return "\t"
	}
	return value
}

func readAwkPrograms(ctx context.Context, invocation *Invocation, names []string) (string, error) {
	var source strings.Builder
	for _, name := range names {
		content, err := readOperand(ctx, invocation, name)
		if reason, ok := operandReason(err); ok {
			fmt.Fprintf(invocation.Stderr, "awk: fatal: can't open source file `%s' for reading: %s\n", name, reason)
			return "", ExitError(2)
		}
		if err != nil {
			return "", err
		}
		source.Write(content)
		source.WriteByte('\n')
	}
	return source.String(), nil
}

// executeAwk runs the program with the workspace as its file system.
func executeAwk(ctx context.Context, invocation *Invocation, settings awkSettings, program *parser.Program) error {
	files := newAwkFileSystem(ctx, invocation)
	config := &interp.Config{
		Stdin: invocation.Stdin, Output: invocation.Stdout, Error: invocation.Stderr,
		Argv0: "awk", Args: settings.operands, Environ: awkEnvironment(invocation.Environment), NoExec: true,
		FileSystem: files,
	}
	for _, assignment := range settings.variables {
		name, value, _ := strings.Cut(assignment, "=")
		if unescaped, err := lexer.Unescape(value); err == nil {
			value = unescaped
		}
		config.Vars = append(config.Vars, name, value)
	}
	interpreter, err := interp.New(program)
	if err != nil {
		fmt.Fprintf(invocation.Stderr, "awk: %v\n", err)
		return ExitError(2)
	}
	status, err := interpreter.ExecuteContext(ctx, config)
	if err != nil {
		return reportAwkError(ctx, invocation, err)
	}
	if files.failure != nil {
		fmt.Fprintf(invocation.Stderr, "awk: %v\n", files.failure)
		return ExitError(2)
	}
	return exitStatus(ExitError(status & 0xff))
}

// awkEnvironment turns NAME=value variables into GoAWK's Environ, which
// alternates names and values. It is never nil: nil would make GoAWK read
// the host environment.
func awkEnvironment(variables []string) []string {
	environ := make([]string, 0, 2*len(variables))
	for _, variable := range variables {
		name, value, _ := strings.Cut(variable, "=")
		environ = append(environ, name, value)
	}
	return environ
}

// reportAwkError reports a run-time error like gawk, with status 2.
func reportAwkError(ctx context.Context, invocation *Invocation, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var pathError *fs.PathError
	if !errors.As(err, &pathError) {
		fmt.Fprintf(invocation.Stderr, "awk: %v\n", err)
		return ExitError(2)
	}
	reason := reasonNoSuchFile
	if errors.Is(err, errAwkDirectory) {
		reason = reasonIsDirectory
	}
	fmt.Fprintf(invocation.Stderr, "awk: fatal: cannot open file `%s' for reading: %s\n", pathError.Path, reason)
	return ExitError(2)
}

var errAwkDirectory = errors.New("is a directory")

// awkFileSystem is the workspace as GoAWK sees it: files to read and, for
// print > "file", files written through Storage.WriteFile on close.
type awkFileSystem struct {
	read  func(name string) ([]byte, error)
	write func(name string, content []byte) error
	// failure is the first output file that could not be stored.
	failure *awkCommitError
}

func newAwkFileSystem(ctx context.Context, invocation *Invocation) *awkFileSystem {
	return &awkFileSystem{
		read: func(name string) ([]byte, error) {
			content, err := readOperand(ctx, invocation, name)
			if reason, ok := operandReason(err); ok {
				problem := fs.ErrNotExist
				if reason == reasonIsDirectory {
					problem = errAwkDirectory
				}
				return nil, &fs.PathError{Op: "open", Path: name, Err: problem}
			}
			return content, err
		},
		write: func(name string, content []byte) error {
			return storeOutput(ctx, invocation, name, content)
		},
	}
}

// commit stores one output file on Close and remembers the first failure:
// GoAWK drops the errors of the files it closes itself.
func (f *awkFileSystem) commit(name string, content []byte) error {
	err := f.write(name, content)
	if err != nil && f.failure == nil {
		f.failure = &awkCommitError{name: name, reason: err}
	}
	return err
}

// awkCommitError is an output file awk could not store.
type awkCommitError struct {
	name   string
	reason error
}

func (e *awkCommitError) Error() string {
	return fmt.Sprintf("can't redirect to `%s' (%v)", e.name, e.reason)
}

// Open opens a workspace file for reading.
func (f *awkFileSystem) Open(name string) (fs.File, error) {
	content, err := f.read(name)
	if err != nil {
		return nil, err
	}
	return &awkFile{Reader: bytes.NewReader(content), name: name, size: int64(len(content))}, nil
}

// Create starts a new file that is written on Close.
func (f *awkFileSystem) Create(name string) (io.WriteCloser, error) {
	return newAwkOutputFile(name, f.commit), nil
}

// Append starts a file with its current content that is written on Close.
func (f *awkFileSystem) Append(name string) (io.WriteCloser, error) {
	output := newAwkOutputFile(name, f.commit)
	content, err := f.read(name)
	if err == nil {
		if _, err := output.Write(content); err != nil {
			return nil, err
		}
	}
	return output, nil
}

// awkFile is an open workspace file.
type awkFile struct {
	*bytes.Reader
	name string
	size int64
}

func (f *awkFile) Stat() (fs.FileInfo, error) { return awkFileInfo{name: f.name, size: f.size}, nil }
func (f *awkFile) Close() error               { return nil }

type awkFileInfo struct {
	name string
	size int64
}

func (i awkFileInfo) Name() string       { return i.name }
func (i awkFileInfo) Size() int64        { return i.size }
func (i awkFileInfo) Mode() fs.FileMode  { return 0o644 }
func (i awkFileInfo) ModTime() time.Time { return time.Time{} }
func (i awkFileInfo) IsDir() bool        { return false }
func (i awkFileInfo) Sys() any           { return nil }

// awkOutputFile buffers a redirect's output, up to the 64 MiB file cap, and
// stores it on Close.
type awkOutputFile struct {
	name    string
	content strings.Builder
	buffer  limitedBuilder
	commit  func(name string, content []byte) error
}

func newAwkOutputFile(name string, commit func(name string, content []byte) error) *awkOutputFile {
	output := &awkOutputFile{name: name, commit: commit}
	output.buffer.builder = &output.content
	return output
}

func (f *awkOutputFile) Write(content []byte) (int, error) { return f.buffer.Write(content) }
func (f *awkOutputFile) Close() error {
	return f.commit(f.name, []byte(f.content.String()))
}
