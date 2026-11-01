package shell

import (
	"slices"
	"strings"
	"time"
)

// Builtins returns the commands every Shell has, sorted by name: file
// commands (cat, ls, find, mkdir, rm, mv, cp, ...), text commands (grep,
// sed, head, ...) and small utilities (seq, date, env, base64, ...). Each is
// a Go implementation that accepts a subset of the GNU flags, prints what
// GNU prints for them and fails loudly on any other flag. Run outside a
// Shell (with an Invocation built by the caller), the commands that start
// other commands (bash, sh, xargs, env with a command, find -exec) fail,
// and which knows no commands. Each call returns new values, so callers may
// change them.
func Builtins() []Command {
	commands := []Command{
		basenameCommand(), base64Command(), bashCommand("bash"), catCommand(), cpCommand(),
		dateCommand(time.Now), dirnameCommand(), envCommand(), findCommand(), lsCommand(),
		mkdirCommand(), mvCommand(), realpathCommand(), rmCommand(), rmdirCommand(),
		seqCommand(), sha256sumCommand(), bashCommand("sh"), sleepCommand(), teeCommand(),
		touchCommand(), whichCommand(), xargsCommand(),
	}
	commands = append(commands, textCommands()...)
	slices.SortFunc(commands, func(left, right Command) int { return strings.Compare(left.Name, right.Name) })
	return commands
}
