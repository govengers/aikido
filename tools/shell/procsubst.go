package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/govengers/aikido/vfs"
)

// firstProcessFile is the number of the first /dev/fd/N path <(list)
// substitutes, as in bash.
const firstProcessFile = 63

// processFileMarker is the name of the redirect a rewritten <(list) stores
// its output with: open hands out a fresh /dev/fd path for it on every
// evaluation. It starts with a byte that is never valid UTF-8, so no
// ordinary path collides with it. The word spells it as $'\xff...'
// (processFileMarkerSource), which survives the printing and reparsing of
// eval and trap scripts; a script that spells it too only gets a fresh
// process file, like a <(list) of its own.
const (
	processFileMarker       = "\xffprocess-file"
	processFileMarkerSource = `\xffprocess-file`
)

// errPermission is a write to /dev/fd by a script.
const errPermission fileError = "Permission denied"

// processFileStorage is /dev/fd for one Run: the files of <(list) and of a
// rewritten source, counted against the Shell's memory budget. Only the
// shell itself stores them (store); a script's write or delete there fails
// with "Permission denied". They are read by path only: listings leave them
// out, so "grep -r x ." and "find ." from the root do not find them.
type processFileStorage struct {
	files *scratchStorage
}

func (processFileStorage) ListFiles(context.Context) ([]vfs.FileMetadata, error) {
	return nil, nil
}

func (p processFileStorage) ReadFile(ctx context.Context, name string) ([]byte, vfs.FileMetadata, error) {
	return p.files.ReadFile(ctx, name)
}

func (processFileStorage) WriteFile(context.Context, string, []byte, string) error {
	return errPermission
}

func (processFileStorage) DeleteFile(context.Context, string) error {
	return errPermission
}

// store writes a file of the shell's own; name is relative to /dev/fd.
func (p processFileStorage) store(ctx context.Context, name string, content []byte, contentType string) error {
	return p.files.WriteFile(ctx, name, content, contentType)
}

// consume drops a process file a redirect reads (cmd < <(list)) from
// /dev/fd: like the pipe bash substitutes, it is read once, and a loop over
// many substitutions does not hold them all. Its bytes stay charged to the
// budget until release runs, when the reader no longer holds the copy.
// resolved is a session path; for any other file release does nothing.
func (p processFileStorage) consume(ctx context.Context, resolved string) (release func()) {
	name, ok := strings.CutPrefix(resolved, processFileDirectory+"/")
	if !ok {
		return func() {}
	}
	size := p.files.detach(ctx, name)
	return func() { p.files.budget.release(size) }
}

// releaseNamed drops the process files arguments name (/dev/fd/63, also
// inside a word like --file=/dev/fd/63) once the command that got them has
// ended: bash closes the pipe of a <(list) after that command, so a loop
// that hands a substitution to a command operand (diff <(sort f) want)
// does not hold them all.
func (p processFileStorage) releaseNamed(ctx context.Context, arguments []string) {
	directory := "/" + processFileDirectory + "/"
	for _, argument := range arguments {
		rest := argument
		for {
			_, after, found := strings.Cut(rest, directory)
			if !found {
				break
			}
			end := strings.IndexFunc(after, func(character rune) bool { return character < '0' || character > '9' })
			if end < 0 {
				end = len(after)
			}
			if end > 0 {
				// A name that is no process file (any more) leaves nothing to drop.
				_ = p.files.DeleteFile(ctx, after[:end])
			}
			rest = after[end:]
		}
	}
}

// openProcessFile opens a fresh /dev/fd/N for the output of a <(list): it
// writes the path to the substitution's output, which becomes the word, and
// returns the file list writes to. Allocating at run time, not at parse
// time, gives two runs of one function (f A & f B) their own files.
func (s *session) openProcessFile(ctx context.Context, prefix string, handler interp.HandlerContext, targets *redirectTargets) (*bufferedFile, error) {
	number := strconv.FormatInt(firstProcessFile+s.processFileCount.Add(1)-1, 10)
	path := "/" + processFileDirectory + "/" + number
	if _, err := io.WriteString(handler.Stdout, path); err != nil {
		return nil, pathError(prefix, path, err)
	}
	file := s.newBufferedFile(ctx, prefix+path, handler.Stderr)
	file.dirty = true // a list that prints nothing still leaves an empty file
	file.commit = func(ctx context.Context, content []byte) error {
		return s.processFiles.store(ctx, number, content, "text/plain")
	}
	targets.add(file)
	return file, nil
}

// parse parses bash source, refuses what the virtual shell cannot run and
// replaces every <(list) with an in-memory file. Every script the session
// runs goes through it: the top-level script, nested bash, and the strings
// eval, source and trap hand to the interpreter. Errors are the message for
// stderr.
func (s *session) parse(script, name string) (*syntax.File, error) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), name)
	if err != nil {
		return nil, err
	}
	if err := refuseUnsupported(file); err != nil {
		return nil, err
	}
	s.noteFunctions(file)
	s.substituteProcesses(file)
	markRedirectLines(file)
	return file, nil
}

// refuseUnsupported returns an error for >(list) and coproc, which would
// need a reader running next to the writer.
func refuseUnsupported(file *syntax.File) error {
	var unsupported error
	syntax.Walk(file, func(node syntax.Node) bool {
		switch typed := node.(type) {
		case *syntax.ProcSubst:
			if typed.Op != syntax.CmdIn {
				unsupported = fmt.Errorf("%s: process substitution %s...) is not supported; write to a file in /tmp and run the command on it", typed.Pos(), typed.Op)
			}
		case *syntax.CoprocClause:
			unsupported = fmt.Errorf("%s: coproc is not supported", typed.Pos())
		}
		return unsupported == nil
	})
	return unsupported
}

// substituteProcesses rewrites every <(list) in file into
// "$( (list) > processFileMarker )": list runs to completion with its output
// stored in a fresh file of the session's /dev/fd, whose path open writes as
// the substitution's output, so the word is the path. The consumer starts
// after the producer has ended, so nothing streams and the output is
// bounded like any file a script writes. Nested substitutions are rewritten
// too, as the walk descends into the new command substitution.
func (s *session) substituteProcesses(file *syntax.File) {
	syntax.Walk(file, func(node syntax.Node) bool {
		word, ok := node.(*syntax.Word)
		if !ok {
			return true
		}
		parts := make([]syntax.WordPart, 0, len(word.Parts))
		for _, part := range word.Parts {
			if substitution, ok := part.(*syntax.ProcSubst); ok {
				parts = append(parts, s.processFileParts(substitution)...)
				continue
			}
			parts = append(parts, part)
		}
		word.Parts = parts
		return true
	})
}

// processFileParts are the word parts that replace one <(list).
func (s *session) processFileParts(substitution *syntax.ProcSubst) []syntax.WordPart {
	start, end := substitution.OpPos, substitution.Rparen
	if len(substitution.Stmts) == 0 {
		return []syntax.WordPart{&syntax.Lit{ValuePos: start, ValueEnd: end, Value: "/dev/null"}}
	}
	produce := &syntax.Stmt{
		Position: start,
		Cmd:      &syntax.Subshell{Lparen: start, Rparen: end, Stmts: substitution.Stmts, Last: substitution.Last},
		Redirs: []*syntax.Redirect{{
			OpPos: end, Op: syntax.RdrOut,
			Word: &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{Left: end, Right: end, Dollar: true, Value: processFileMarkerSource}}},
		}},
	}
	return []syntax.WordPart{&syntax.CmdSubst{Left: start, Right: end, Stmts: []*syntax.Stmt{produce}}}
}

// rewriteScriptArgument parses the script an eval or trap argument holds and,
// when it contains a process substitution, returns it rewritten; unchanged
// scripts are returned as they are, so error messages keep their text.
func (s *session) rewriteScriptArgument(script, name string) (string, error) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), name)
	if err == nil {
		s.noteFunctions(file)
	}
	if err != nil || !containsProcessSubstitution(file) {
		return script, nil //nolint:nilerr // a syntax error is the interpreter's to report, with its own wording
	}
	if err := refuseUnsupported(file); err != nil {
		return "", err
	}
	s.substituteProcesses(file)
	var printed strings.Builder
	if err := syntax.NewPrinter().Print(&printed, file); err != nil {
		return "", errors.New("cannot print the rewritten script")
	}
	return printed.String(), nil
}

func containsProcessSubstitution(file *syntax.File) bool {
	found := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if _, ok := node.(*syntax.ProcSubst); ok {
			found = true
		}
		return !found
	})
	return found
}

// rewriteScriptArguments rewrites the script of "eval words..." and of
// "trap action signals...", which the interpreter parses itself, when it
// holds a process substitution.
func (s *session) rewriteScriptArguments(arguments []string) []string {
	if arguments[0] == "eval" {
		script := strings.Join(arguments[1:], " ")
		rewritten, err := s.rewriteScriptArgument(script, "")
		switch {
		case err != nil:
			return []string{internalFailure, "bash: eval: " + err.Error(), "2"}
		case rewritten != script:
			return []string{"eval", rewritten}
		}
		return arguments
	}
	index := 1
	if index < len(arguments) && arguments[index] == "--" {
		index++
	}
	if len(arguments)-index < 2 {
		return arguments // no action, only signals to reset
	}
	rewritten, err := s.rewriteScriptArgument(arguments[index], "trap")
	if err != nil {
		return []string{internalFailure, "bash: trap: " + err.Error(), "2"}
	}
	arguments = slices.Clone(arguments)
	arguments[index] = rewritten
	return arguments
}

// rewriteSource lets "source file args..." run a file that holds a process
// substitution: the rewritten script is stored in /dev/fd and sourced from
// there. Files without one, and files that cannot be read (the builtin
// reports those), are left alone.
func (s *session) rewriteSource(ctx context.Context, handler interp.HandlerContext, arguments []string) []string {
	if len(arguments) < 2 {
		return arguments
	}
	resolved, err := Resolve(workspaceDirectory(handler.Dir), arguments[1])
	if err != nil {
		return arguments
	}
	content, err := readFile(ctx, s.files(), resolved)
	if err != nil {
		return arguments
	}
	rewritten, err := s.rewriteScriptArgument(string(content), arguments[1])
	switch {
	case err != nil:
		return []string{internalFailure, "bash: source: " + err.Error(), "1"}
	case rewritten == string(content):
		return arguments
	}
	name := fmt.Sprintf("source-%d", s.processFileCount.Add(1))
	if err := s.processFiles.store(ctx, name, []byte(rewritten), "text/x-shellscript"); err != nil {
		return []string{internalFailure, "bash: source: " + describe(err).Error(), "1"}
	}
	return append([]string{arguments[0], "/" + processFileDirectory + "/" + name}, arguments[2:]...)
}
