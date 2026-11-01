package shell

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
)

// firstProcessFile is the number of the first /dev/fd/N path <(list)
// substitutes, as in bash.
const firstProcessFile = 63

// processFileStorage holds the files of <(list) for one Run. They are read
// by path only: listings leave them out, so "grep -r x ." and "find ." from
// the root do not find them.
type processFileStorage struct {
	*memory.Storage
}

func (processFileStorage) ListFiles(context.Context) ([]vfs.FileMetadata, error) {
	return nil, nil
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
	s.substituteProcesses(file)
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
// "$( (list) > /dev/fd/N )/dev/fd/N": list runs to completion with its
// output stored in the session's /dev/fd, then the word is the path. The
// consumer starts after the producer has ended, so nothing streams and the
// output is bounded like any file a script writes. Nested substitutions are
// rewritten too, as the walk descends into the new command substitution.
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
	name := fmt.Sprintf("/%s/%d", processFileDirectory, firstProcessFile+s.processFiles.Add(1)-1)
	produce := &syntax.Stmt{
		Position: start,
		Cmd:      &syntax.Subshell{Lparen: start, Rparen: end, Stmts: substitution.Stmts, Last: substitution.Last},
		Redirs: []*syntax.Redirect{{
			OpPos: end, Op: syntax.RdrOut,
			Word: &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{ValuePos: end, ValueEnd: end, Value: name}}},
		}},
	}
	return []syntax.WordPart{
		&syntax.CmdSubst{Left: start, Right: end, Stmts: []*syntax.Stmt{produce}},
		&syntax.Lit{ValuePos: end, ValueEnd: end, Value: name},
	}
}

// rewriteScriptArgument parses the script an eval or trap argument holds and,
// when it contains a process substitution, returns it rewritten; unchanged
// scripts are returned as they are, so error messages keep their text.
func (s *session) rewriteScriptArgument(script, name string) (string, error) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), name)
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
	name := fmt.Sprintf("%s/source-%d", processFileDirectory, s.processFiles.Add(1))
	if err := s.storage.WriteFile(ctx, name, []byte(rewritten), "text/x-shellscript"); err != nil {
		return []string{internalFailure, "bash: source: " + describe(err).Error(), "1"}
	}
	return append([]string{arguments[0], "/" + name}, arguments[2:]...)
}
