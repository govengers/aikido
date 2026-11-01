package shell

import (
	"mvdan.cc/sh/v3/expand"
)

// noHostTempDirectory is the temporary directory the interpreter is made to
// believe in. The interpreter opens any path in its temporary directory
// whose name starts with "sh-interp-" on the host, past the open handler,
// and creates its process substitution FIFOs there. A NUL byte makes every
// such host call fail ("invalid argument"), and no script can spell the
// path, so /tmp/sh-interp-x stays a file of the virtual /tmp.
const noHostTempDirectory = "/\x00aikido-no-host-temp-dir"

// interpreterEnviron is the environment an interpreter starts with. While
// the interpreter resets (it reads TMPDIR once, then), TMPDIR is
// noHostTempDirectory; afterwards the script sees its own variables. A
// missing HOME is an empty, unexported variable, as in bash started without
// one (env -i bash): unset, the interpreter would fill in the host user's
// home directory.
type interpreterEnviron struct {
	expand.Environ
	resetting bool
}

func (e *interpreterEnviron) Get(name string) expand.Variable {
	switch variable := e.Environ.Get(name); {
	case e.resetting && name == "TMPDIR":
		return expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: noHostTempDirectory}
	case name == "HOME" && !variable.IsSet():
		return expand.Variable{Set: true, Kind: expand.String}
	default:
		return variable
	}
}
