# Shell

`tools/shell` is a virtual bash for agents. The model writes ordinary shell (pipes, `&&`, loops, variables, `$(...)`, here-docs, redirects, globs) and gets bash's output and exit status back, but every file the script touches is a file of a [`vfs.Storage`](Files.md), and every command is a Go implementation. Overlays, dry runs, mounts and read-only stores keep working; nothing reaches the host unless you register it. Parsing and execution: the interpreter of [mvdan.cc/sh](https://github.com/mvdan/sh) v3 with all of its file and exec handlers routed to the Storage. Decision and benchmark: ADR-041 in [DECISIONS](../DECISIONS.md).

It fits two kinds of hosts: a coding agent over a directory (`grep -rl old | xargs sed -i`, `find ... | wc -l` loops over an overlay of `vfs/osfs`) and an in-memory worker that keeps notes and journals in a Storage and runs a few allowed host programs (git, kubectl) next to Go-native tools.

## Use

From [tools/shell/example_test.go](../../tools/shell/example_test.go) (`ExampleShell_Run`):

```go
	bash, err := shell.New(&shell.Options{Storage: storage})
	result, _ := bash.Run(context.Background(), "cd notes && cat -n todo.md && echo done > status.txt; nosuch")
	fmt.Print(result.Output)
	fmt.Println("exit", result.ExitCode)
	result, _ = bash.Run(context.Background(), "pwd; ls")
```

prints the numbered file, `bash: line 1: nosuch: command not found`, `exit 127`, then `/notes` and both files: the working directory held between the calls.

As an agent tool (`ExampleRegister`):

```go
	err := shell.Register(registry, &shell.Options{Storage: memory.New()})
```

registers one tool, `bash`, with the parameters **`command`** (the script) and `description` (5-10 words for logs, ignored). The model receives `{"output": "...", "exit_code": N, "truncated": bool}`. A failing script is a normal result with its exit code, not a tool error; only a cancelled context is.

The tool description is generated: one paragraph on what works, the list of commands from each `Command.Summary`, and "No network, no package managers, no compilers or interpreters unless listed."

## Options

| `Options` | Effect |
|---|---|
| `Storage` | required; the workspace the script sees at `MountPoint` |
| `MountPoint` | absolute path of the workspace inside the shell, default `/`. Scripts start there and `HOME` defaults to it; `pwd`, `realpath`, `find` and globs print paths below it; every path outside it except `/tmp` and `/dev` is `No such file or directory` (`cd /`, `cd ..` above it and `ls /` fail; globs still walk down to it). Set it to the host directory an `osfs` Storage mirrors so built-in commands and `External` programs agree on absolute paths (`git -C $PWD/sub status`, `git -C /srv/repo log`); the CLI does this for `--direct`. Not `/tmp`, nothing in `/dev`; below `/tmp` works |
| `Commands` | extra `Command`s next to the built-ins; a name of a built-in replaces it. Names are one word without `/`, unique, and not a shell builtin (`cd`, `echo`, `test`, ...), which the interpreter would run instead |
| `HideHiddenPaths` | hides workspace files with a segment starting with `.` or `_` (`.env`, `.git/hooks`, `_private`) like `agent.VFSToolOptions.HideHiddenPaths`: listings, globs, `ls`, `find` and `grep -r` leave them out; reading, writing or deleting one fails with `No such file or directory`. `/tmp` is not affected; `External` programs see the host as it is. The CLI sets it from `--hide-hidden` |
| `Environment` | `NAME=value` the script starts with; the host environment is never passed. `HOME` defaults to `MountPoint` |
| `Timeout` | per `Run`, default 60 s, negative disables. A script out of time is stopped: exit 124 and `bash: timed out after 1m0s; the script was stopped` |
| `MaximumOutputBytes` | stdout and stderr together, default 30 KiB, negative keeps everything. The first and the last half are kept, the middle is replaced by `... [N bytes of output cut] ...` (counted against the limit) and `Truncated` is set |
| `ToolName` | default `bash` |

`New` validates and copies the options. `Shell.Run(ctx, script)` returns `Result{Output, ExitCode, Truncated}`; it is safe for concurrent use: calls run one at a time, and a call waiting for its turn ends with its context and counts the wait against `Timeout` (exit 124, `waiting for the previous call to end`). `shell.Builtins()` lists the built-in commands; run outside a Shell, the ones that start other commands (`bash`, `sh`, `xargs`, `env cmd`, `find -exec`) fail and `which` finds nothing. A `Command` is `{Name, Summary, Run func(ctx, *Invocation) error}`: `Run` gets the expanded arguments, the streams, the working directory as a path of the Storage (the workspace path with the default `MountPoint`), the Storage as the script sees it (workspace, `/tmp`, `/dev/fd`) and the exported variables, and returns nil, an `ExitError(n)`, or an error the shell prints as `name: error` with status 1. Resolve operands with `shell.Resolve(invocation.Directory, operand)`.

## What persists between calls

| Kept from one `Run` to the next | Not kept |
|---|---|
| the working directory (`cd`) | variables, `export`, functions, aliases, `set` options |
| directories created with `mkdir` (see below) | |
| everything written to the Storage | background jobs (they end with the script) |
| files in `/tmp` | |

## Built-in commands

Every command accepts the listed GNU flags (clustered `-rf`, attached `-n5`, long `--recursive`, options after operands, `--`), prints what GNU prints for them and fails loudly on any other flag with GNU's message and status (`ls: invalid option -- 'z'` and `Try 'ls --help' for more information.`, exit 2 for ls, 1 for most others). Error messages use the C locale's ASCII quotes (`cannot access 'x'`).

| Command | Flags and forms |
|---|---|
| `cat` | `-n -b -s -E -T -v -A -e -t -u` and their long forms, with GNU's notation (`^I`, `$`, `^M$` for CRLF, `M-`); numbering and squeezing run across files; `-` is stdin |
| `ls` | one entry per line; `-1 -a -A -l -h -R -d -t -S -r -F -p`; sorted by byte order. `-l` shows `user user` as owner and the Storage's modification time; files are `-rw-r--r--`, directories `drwxr-xr-x` |
| `find` | start paths, then `-name -iname -path -ipath -wholename -type f/d/c (l b p s never match) -empty -maxdepth -mindepth -true -false ! -not -a -and -o -or ( ) -prune -print -print0 -delete -exec cmd {} ;` and `-exec cmd {} +`; `-H -L -P` are accepted. Walks in sorted order |
| `mkdir` | `-p -v` |
| `rmdir` | empty directories only |
| `rm` | `-r -R -f -v`; refuses `/`, `.` and `..` like GNU |
| `mv` | `-f -n -v`; files and directories, into a directory, renames |
| `cp` | `-r -R -a -f -n -v`, `-p` accepted |
| `touch` | `-c`; creates empty files, leaves existing ones unchanged |
| `tee` | `-a`; also to `/dev/null`, `/dev/stdout`, `/dev/stderr` |
| `basename` | `NAME [SUFFIX]`, `-a`, `-s SUFFIX` |
| `dirname` | several names |
| `realpath` | prints `/`-rooted workspace paths; `-e`, `-m`, `-s`, `--relative-to=DIR` |
| `xargs` | `-0 -d DELIM -n N -L N -I REPLACE -r -t`; `-P` is accepted and runs one command at a time. Quotes and backslashes in input like GNU; statuses 123, 124, 126, 127 |
| `env` | prints the exported variables sorted; `-i`, `-u NAME`, `NAME=value... command args` |
| `which` | names of registered commands and shell builtins (the name, not a path); `-a` |
| `seq` | `[FIRST [STEP]] LAST`, `-s SEP`, `-w`; integers only (a decimal argument is refused) |
| `date` | UTC always; `+FORMAT` (`%a %A %b %h %B %c %C %d %D %e %F %H %I %j %k %l %m %M %n %N %p %R %s %S %t %T %u %w %y %Y %z %:z %Z %%`, any other conversion is an error), `-u`, `-d @SECONDS / now / 2026-03-04 [15:04[:05]] / RFC 3339`, `-I[date,hours,minutes,seconds,ns]`, `-R` |
| `sleep` | numbers with `s m h d`, decimals; ends with the context |
| `base64` | `-d -i -w COLS` (default 76, 0 for one line) |
| `sha256sum` | files or stdin, `-b -t` accepted |
| `bash`, `sh` | `-c script [name [args]]`, `file [args]`, or a script on stdin; `-e -u -x -o option`. A nested interpreter in the same workspace; its `cd` and variables stay inside. `$0` is not set |

Text commands (`grep`, `sed`, `head`, `tail`, `wc`, `sort`, `uniq`, `cut`, `tr`, `diff`, `jq`, `awk`) are built in the same way; their flags appear in the tool description from their `Summary`. `grep -Z` (`--null`) ends every printed file name with a NUL byte, for `grep -rlZ x . | xargs -0 sed -i ...`; `grep -z` (`--null-data`) reads and writes NUL-terminated lines. `awk` sees the exported variables in `ENVIRON`.

The interpreter itself provides the shell builtins: `cd`, `pwd`, `echo`, `printf`, `test` and `[`, `read`, `export`, `set`, `shift`, `exit`, `return`, `source`/`.`, `eval`, `local`, `declare`, `trap`, `wait`, `true`, `false`, `type`, `command`, `alias`, `getopts`, `pushd`/`popd`, `mapfile`. `xargs`, `find -exec` and `env` can start them too (`xargs echo`). `command -v` and `type` answer for registered commands as for builtins, and know the functions a script defines.

## External

`shell.External(&shell.ExternalOptions{...})` returns a `Command` that runs a host program; add it to `Options.Commands`. From `ExampleExternal`:

```go
	git, err := shell.External(&shell.ExternalOptions{
		Name:        "git",
		Path:        "/usr/bin/git",
		Directory:   "/srv/checkout",
		Environment: []string{"HOME=/srv/checkout", "GIT_CONFIG_NOSYSTEM=1"},
		Allow: func(arguments []string) error {
			if slices.Contains(arguments, "push") {
				return errors.New("push is not allowed")
			}
			return nil
		},
	})
```

| `ExternalOptions` | Effect |
|---|---|
| `Name` | the command name in scripts, one word |
| `Path` | absolute path of the binary; checked to exist at `External` |
| `Directory` | absolute host working directory, required. The program sees the host there, not the workspace; the script's `cd` does not move it |
| `FollowWorkingDirectory` | declares `Directory` the host path of the workspace root (an `osfs` over it, the shell's `MountPoint` usually set to it): the program runs in the host directory the script `cd`'d to. It refuses to run in `/tmp`, in a directory that exists only in the shell (`mkdir`), and where a symbolic link on disk leads out of `Directory` |
| `Environment` | the program's complete environment; nil means none. Neither the host's nor the script's variables are passed |
| `Allow` | asked with the arguments before every run; an error is printed as `name: error`, exit 1, and the program does not start |
| `Timeout` | per run; the program is killed (exit 124, `name: killed after the 10s timeout`) |
| `Summary` | the line in the tool description; default `host program <Path>` |

stdin, stdout and stderr are the script's, so `git log | grep fix` and `kubectl get pods -o json | jq` work. On Unix the program runs in its own process group, and the whole group is killed when the program ends (children it left in the background included), times out, or the call is cancelled. Exit codes pass through; a signal is 128 + its number.

## Filesystem semantics

- **`/` is the workspace root** (with the default `MountPoint`). `/docs/a.md` and `docs/a.md` from the root are the same file. A path that climbs above the root (`../../etc/passwd`) is `No such file or directory`; host paths such as `/etc/hosts` are workspace paths and normally missing. `pwd`, `realpath` and `$PWD` print `/`-rooted workspace paths. With `MountPoint: "/work/repo"` the same holds below `/work/repo`, `/`, `/work` and `/etc` do not exist, and a workspace `tmp/` directory is not hidden by `/tmp`.
- **Directories are implied.** A Storage holds files only: a directory exists while a file lies below it. `mkdir` additionally records the directory in the Shell, so `mkdir -p build && cd build` and `cp x build/` work and `ls` shows it, for the life of the Shell; the Storage is unchanged until a file is written below it (another Shell or the file tools do not see empty directories). `rmdir` removes an empty one; `rm -r` forgets the ones below.
- **Writes are buffered.** A redirect (`>`, `>>`, `2>&1 > f`) opens its target like bash (`>` truncates right away, so `sort f > f` leaves `f` empty as in bash; a target the Storage refuses, such as a read-only mount, fails the redirect with status 1 before the command runs and before `&&`/`||` decide), then collects what is written and stores it with one `WriteFile` when it closes. Targets `exec >file` keeps open are stored when the script (or the nested `bash`) ends. The store at close runs on after a timeout, so a timed-out script keeps what it wrote, but is bounded at 30 s (`Connection timed out`). Commands write whole files the same way. A write needs an existing parent directory (`echo x > nodir/f` fails like bash) and is capped at 64 MiB per file.
- **`/tmp` is scratch.** It is a memory store of the Shell, never the Storage: a file written to `/tmp/x` in one call can be read in the next (one registered tool is one agent session), and nothing of it reaches the workspace, an overlay's changes or a commit. Its files are capped at 64 MiB together; a write beyond that fails with `No space left on device`. `/tmp` is always a directory and hides workspace files under `tmp/`.
- **Devices.** `/dev/null`, `/dev/stdin`, `/dev/stdout`, `/dev/stderr`; every other `/dev` path is missing.
- **No execute bit.** `./build.sh` fails with exit 126 and a hint (`run it with: bash ./build.sh`); `bash build.sh` works. `test -x` is true for directories only. There is no `chmod`, no symlinks, no file modes.
- **Ordering.** `ls`, `find` and globs sort by byte order (the C locale); GNU `find` prints in directory order instead, and GNU `ls` sorts by the locale.
- **Process substitution reads, without streaming.** `<(list)` runs `list` to completion, stores its output in memory as `/dev/fd/63` (then 64, ...) and substitutes that path, so `diff <(sort a) <(sort b)` and `while read l; do ...; done < <(find . -name '*.go')` work, in scripts, functions, nested `bash`, `eval`, `source` and `trap` alike. No host FIFO is involved. The producer ends before the consumer starts: nothing streams, an endless producer runs into the timeout, and the output is capped like any file (64 MiB). The files live for one `Run`, are readable by path only (`ls /dev/fd`, `find` and `grep -r` do not list them) and never reach the Storage. `>(list)` and `coproc` need a reader running next to the writer and are refused with exit 2 and a hint. Background jobs (`&`) run; `wait` waits for them, and the ones still running when the script ends are stopped, as when `bash -c` returns.

## Security

- Every file access of the interpreter goes through handlers over the Storage: redirects, `source`, globs, `test -f`, `cd`. The handlers resolve paths with `shell.Resolve`; nothing above the root resolves. `pwd -P` and `source name` (which would consult the host's symlinks and `PATH`) are rewritten before they run, the same holds behind `builtin` and `command`, and `command -v`/`-V` and `type [-t|-p|-P]` are answered by the shell from its commands, the builtins, keywords and the functions its scripts define, never from the host PATH (no file is executable, so `type -P` finds nothing). The interpreter would open paths named `sh-interp-*` in its temporary directory (`$TMPDIR`) on the host and create process substitution FIFOs there; the shell gives it a temporary directory no host call can open, so `/tmp/sh-interp-x` is an ordinary file of the virtual `/tmp` and no FIFO is ever created.
- The script and the built-in commands never see the host environment; `HOME` defaults to `MountPoint`; a nested `env -i bash` sees an empty `HOME` (`cd` alone: `HOME not set`), never the host user's home.
- Host programs run only as `External` commands, with the environment you give them.
- Each `Run` is bounded by `Timeout` (commands honour the context; `sleep`, `External` and loops stop) and its output by `MaximumOutputBytes`. A panicking `Command` becomes `name: internal error: ...` with status 2, not a crashed process.
- Combine with `vfs/overlay` to review every change before `Commit`, with `vfs/mount` `ReadOnly` mounts for reference files (writes fail with `Read-only file system`), and with `ScopedStorage.Scope` for tenants.

Known limits:

- `~name` (another user's home) expands through the host's user database: the script learns that user's home directory path, not its files.
- An `External` program sees whatever its `Directory` and its user can reach; `Allow` sees arguments only, not what a program does with them. Register only programs whose arguments you can judge, and give them no secrets in `Environment` that the model should not be able to print.
- The variables `UID`, `EUID`, `GID` and `$$` are the host process's.
- Directories created by `mkdir` live in memory per Shell.
- The whole Storage is listed for `ls`, `find`, globs and directory checks; a very large store makes those calls slow.
- Memory: files, `/tmp`, process substitution and what commands write into `$(...)` are capped (64 MiB each; a command writing more into `$(...)` fails with `File too large`). What the interpreter itself holds is not: shell builtins (`echo`, `printf`) writing into `$(...)` and variables grown in a loop (`x=$x$x`) are bounded only by `Timeout`. Run scripts from untrusted input in a process with a memory limit.

## Gotchas

- Variables do not persist between calls; write `cd dir && make-something` in one call or keep values in files.
- `echo $x` in a later call is empty; the working directory is not.
- A redirect target is written when the statement ends, so `cmd > f &` stores `f` when the background job finishes.
- A store that fails only at close (the Storage accepted the target at open, then refused the content) prints `bash: f: <reason>` but does not change the exit status; a target refused at open fails like in bash.
- Messages say `bash: line N:` like `bash -c`; redirect errors omit the line.
- `seq` takes integers only, `date` is always UTC, `ls -l` owners are `user`.

## Testing

`tools/shell/differential_test.go` runs table cases in this shell over a memory Storage and in GNU bash over a temporary directory with the same files, and compares stdout, stderr, exit status and the resulting files. The GNU reference is the host's bash when the host has GNU tools (`sed --version` says GNU: the Linux CI runners), else docker (`golang:1.26`, native on arm64, or `debian:12`) after a 15 s probe, else the cases skip; `TestDifferentialBackend` logs which one ran. `AIKIDO_DIFFERENTIAL_BASH=/opt/homebrew/bin/bash` uses a GNU bash on a host without GNU tools, which is meaningful for the shell-semantics cases only. A case lists the commands it needs besides the core ones in `requires` and skips while one is not built in.
