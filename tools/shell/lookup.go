package shell

import (
	"fmt"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// The interpreter's command -v, type and type -p/-P search PATH and stat
// paths on the host. The shell answers every lookup itself instead, from
// its commands, the shell builtins and keywords, and the functions its
// scripts define; nothing is found on a PATH, because no file is executable.

// rewriteType sends every form of type to the shell's lookup: options are
// parsed like bash does (combined letters, "--", the last of -t, -p and -P
// counts), so no form reaches the interpreter's type and its host PATH.
func (s *session) rewriteType(handler interp.HandlerContext, arguments []string) []string {
	kind, skipFunctions := "type", ""
	names := arguments[1:]
	for len(names) > 0 && len(names[0]) > 1 && names[0][0] == '-' {
		option := names[0]
		names = names[1:]
		if option == "--" {
			break
		}
		for _, letter := range option[1:] {
			switch letter {
			case 'a':
				// Every match is the one match: nothing is found on a PATH.
			case 'f':
				skipFunctions = "f"
			case 't', 'p', 'P':
				kind = "type -" + string(letter)
			default:
				return []string{internalFailure, "bash: " + linePrefix(handler.Pos) + "type: -" + string(letter) +
					": invalid option\ntype: usage: type [-afptP] name [name ...]", strconv.Itoa(statusUnsupported)}
			}
		}
	}
	return append([]string{internalLookup, kind, skipFunctions}, names...)
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
func (s *session) classify(name string, skipFunctions bool) string {
	switch {
	case syntax.IsKeyword(name):
		return "keyword"
	case !skipFunctions && s.isFunction(name):
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

// lookup answers command -v/-V and type: Arguments are the kind ("command
// -v", "type -t", ...), "f" to skip functions or "", and the names. The
// status is 1 when a name was not found.
func (s *session) lookup(invocation *Invocation, position syntax.Pos) uint8 {
	kind, skipFunctions, names := invocation.Arguments[0], invocation.Arguments[1] == "f", invocation.Arguments[2:]
	status := uint8(0)
	for _, name := range names {
		class := s.classify(name, skipFunctions)
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
