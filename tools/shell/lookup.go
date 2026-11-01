package shell

import (
	"fmt"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// The interpreter's command -v, type and type -p/-P search PATH and stat
// paths on the host. The shell answers every lookup itself instead, from
// its commands, the shell builtins and keywords, and the functions its
// scripts define; nothing is found on a PATH, because no file is executable.

// lookupKinds are the lookup forms the shell answers, by their flag.
var lookupKinds = map[string]map[string]string{
	"command": {"-v": "command -v", "-V": "command -V"},
	"type":    {"": "type", "-t": "type -t", "-p": "type -p", "-P": "type -P"},
}

// rewriteLookup sends command -v/-V and type [-t|-p|-P] with their names to
// the shell's lookup. Other forms (command name args runs the command) stay.
func (s *session) rewriteLookup(arguments []string) []string {
	kinds := lookupKinds[arguments[0]]
	names := arguments[1:]
	flag := ""
	if len(names) > 0 && len(names[0]) == 2 && names[0][0] == '-' {
		flag, names = names[0], names[1:]
	}
	kind, ok := kinds[flag]
	if !ok || len(names) == 0 {
		return arguments
	}
	return append([]string{internalLookup, kind}, names...)
}

// noteFunctions records the functions file defines. The set over-approximates
// (a definition in a branch that never runs counts, unset -f is ignored); it
// only feeds command -v and type.
func (s *session) noteFunctions(file *syntax.File) {
	syntax.Walk(file, func(node syntax.Node) bool {
		if declaration, ok := node.(*syntax.FuncDecl); ok && declaration.Name != nil {
			s.functions.Store(declaration.Name.Value, true)
		}
		return true
	})
}

// classify says what bash's type -t would answer for name: keyword,
// function, builtin (registered commands count as builtins), or "".
func (s *session) classify(name string) string {
	switch {
	case syntax.IsKeyword(name):
		return "keyword"
	case s.isFunction(name):
		return "function"
	case s.known(name):
		return "builtin"
	}
	return ""
}

func (s *session) isFunction(name string) bool {
	_, ok := s.functions.Load(name)
	return ok
}

// lookup answers the forms rewriteLookup sends, like bash; the status is 1
// when a name was not found.
func (s *session) lookup(invocation *Invocation, position syntax.Pos) uint8 {
	kind, names := invocation.Arguments[0], invocation.Arguments[1:]
	status := uint8(0)
	for _, name := range names {
		class := s.classify(name)
		if class == "" || kind == "type -P" {
			status = 1
			if kind == "type" || kind == "command -V" {
				builtin, _, _ := strings.Cut(kind, " ")
				fmt.Fprintf(invocation.Stderr, "bash: %s%s: %s: not found\n", linePrefix(position), builtin, name)
			}
			continue
		}
		switch kind {
		case "command -v":
			fmt.Fprintln(invocation.Stdout, name)
		case "type -t":
			fmt.Fprintln(invocation.Stdout, class)
		case "type -p":
		default:
			fmt.Fprintf(invocation.Stdout, "%s is %s\n", name, describeClass(class))
		}
	}
	return status
}

func describeClass(class string) string {
	switch class {
	case "keyword":
		return "a shell keyword"
	case "function":
		return "a function"
	}
	return "a shell builtin"
}
