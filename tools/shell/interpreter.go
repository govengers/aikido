package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/govengers/aikido/vfs"
)

// The interpreter runs every simple command through the exec handler. A
// call rewritten to one of these names (they contain a NUL byte, which no
// script can pass) is answered by the shell itself.
const (
	// internalFailure prints Arguments[0] to stderr and exits with the
	// status in Arguments[1].
	internalFailure = "\x00failure"
	// internalLookup answers command -v/-V and type from the shell's own
	// commands, builtins, keywords and functions (see lookup).
	internalLookup = "\x00lookup"
)

// Exit statuses the shell itself sets, as bash does.
const (
	statusCannotExecute  = 126
	statusNotFound       = 127
	statusBrokenPipe     = 141 // 128 + SIGPIPE
	statusInternalError  = 2
	statusUnsupported    = 2
	statusTimedOut       = 124 // like timeout(1)
	statusCommandFailure = 1
)

// session is the state one script runs with: the call's storage (the
// workspace with /tmp and /dev/fd mounted), the shell's created directories
// and its command table. A nested bash shares it.
type session struct {
	storage vfs.Storage
	// root is the mount point of the workspace in storage: "" for "/".
	root        string
	directories *directorySet
	commands    map[string]Command
	// budget is the Shell's memory budget, which buffered redirects count
	// against.
	budget *memoryBudget
	// processFiles is /dev/fd for this Run; processFileCount counts the
	// names it has handed out.
	processFiles     processFileStorage
	processFileCount atomic.Int64
	// functions holds the names of the functions the session's scripts
	// define, for command -v and type.
	functions sync.Map
}

func (s *session) files() workspace {
	return workspace{storage: s.storage, root: s.root, directories: s.directories}
}

// runnerStart is what a new interpreter starts with.
type runnerStart struct {
	directory   string // workspace path
	environment []string
	parameters  []string // shell options and positional parameters, as interp.Params takes them
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
}

// scriptRunner is an interpreter with the redirect files it opened.
type scriptRunner struct {
	*interp.Runner
	targets *redirectTargets
}

// newRunner returns an interpreter whose every file access and command goes
// through the session.
func (s *session) newRunner(start runnerStart) (*scriptRunner, error) {
	targets := &redirectTargets{}
	environ := &interpreterEnviron{Environ: expand.ListEnviron(start.environment...), resetting: true}
	runnerOptions := []interp.RunnerOption{
		interp.Env(environ),
		interp.StdIO(stdinFile(start.stdin), start.stdout, start.stderr),
		interp.ExecHandlers(func(interp.ExecHandlerFunc) interp.ExecHandlerFunc { return s.execute }),
		interp.CallHandler(s.rewrite),
		interp.OpenHandler(func(ctx context.Context, name string, flag int, mode os.FileMode) (io.ReadWriteCloser, error) {
			return s.open(ctx, name, flag, mode, targets)
		}),
		interp.StatHandler(s.stat),
		interp.ReadDirHandler2(s.readDirectory),
		interp.AccessHandler(s.access),
	}
	if len(start.parameters) > 0 {
		runnerOptions = append(runnerOptions, interp.Params(start.parameters...))
	}
	runner, err := interp.New(runnerOptions...)
	if err != nil {
		//nolint:errorlint // LIB-010: the interpreter's error is not part of this package's API
		return nil, fmt.Errorf("aikido/tools/shell: start the interpreter: %v", err)
	}
	// The interpreter's directory is a virtual absolute path: "/" is the
	// root of the session Storage. interp.Dir would check it on the host, so
	// it is set directly, before the reset captures it.
	runner.Dir = "/" + start.directory
	runner.Reset()
	environ.resetting = false
	return &scriptRunner{Runner: runner, targets: targets}, nil
}

// stdinFile turns a command's stdin into what the interpreter accepts
// without starting a copying goroutine: an *os.File, or nil for none.
func stdinFile(stdin io.Reader) io.Reader {
	switch typed := stdin.(type) {
	case nil, noInput:
		return nil
	case *os.File:
		return typed
	}
	return stdin
}

// redirectStoreTimeout bounds the stores of redirect targets: each runs
// without the script's context, so a timed-out script still keeps what it
// wrote, but ends this long after the script's deadline at the latest, and
// the stores at the end of a script share one such period. A variable for
// tests.
var redirectStoreTimeout = 30 * time.Second

// storeContext returns the context a redirect store runs with: detached
// from ctx, ending redirectStoreTimeout from now or after ctx's deadline,
// whichever comes first.
func storeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), redirectStoreTimeout)
	deadline, ok := ctx.Deadline()
	if !ok {
		return detached, cancel
	}
	bounded, cancelBounded := context.WithDeadline(detached, deadline.Add(redirectStoreTimeout))
	return bounded, func() {
		cancelBounded()
		cancel()
	}
}

// jobCleanupTimeout bounds the wait for background jobs after a script:
// they are cancelled first, so only a job stuck in something that ignores
// its context takes that long.
const jobCleanupTimeout = 5 * time.Second

// runFile runs a parsed script to its end and returns the interpreter's
// final directory. Background jobs (&) end with the script, as they do when
// bash -c returns: they are cancelled and awaited so none writes after
// runFile returns (unless one ignores cancellation for jobCleanupTimeout).
func runFile(ctx context.Context, runner *scriptRunner, file *syntax.File) (string, error) {
	jobs, stopJobs := context.WithCancel(ctx)
	err := runner.Run(jobs, file)
	stopJobs()
	directory := runner.Dir
	awaitJobs(ctx, runner.Runner)
	runner.targets.closeAll(ctx)
	return directory, err
}

// awaitJobs runs the builtin wait without any script code: the script's
// functions are dropped first (a function named wait or builtin would run
// instead), and wait runs as a statement, not a file, so the EXIT trap does
// not run a second time.
func awaitJobs(ctx context.Context, runner *interp.Runner) {
	wait, err := syntax.NewParser().Parse(strings.NewReader("wait"), "")
	if err != nil {
		return
	}
	runner.Funcs = nil
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A panic here must not end the host; the status is already decided.
		defer func() { _ = recover() }()
		// The status of the closing wait is not the script's.
		_ = runner.Run(context.WithoutCancel(ctx), wait.Stmts[0])
	}()
	timer := time.NewTimer(jobCleanupTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// recoverHandler turns a panic in an interpreter handler into its error,
// which stops the script: handlers run on the interpreter's goroutines
// (pipelines, background jobs), where a panic would end the host process.
// The deferred call must be the handler's first.
func recoverHandler(err *error) {
	if recovered := recover(); recovered != nil {
		*err = fmt.Errorf("aikido/tools/shell: internal error: %v", recovered)
	}
}

// exitStatusOf returns the exit status a Run error carries, and whether the
// error is one (nil is status 0).
func exitStatusOf(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status), true
	}
	return 0, false
}

// workspaceDirectory turns the interpreter's directory into a workspace path.
func workspaceDirectory(directory string) string {
	return strings.TrimPrefix(path.Clean("/"+directory), "/")
}

// execute is the interpreter's exec handler: every simple command that is
// not a function or a shell builtin.
func (s *session) execute(ctx context.Context, arguments []string) (err error) {
	defer recoverHandler(&err)
	handler := interp.HandlerCtx(ctx)
	invocation := &Invocation{
		Arguments:   arguments[1:],
		Stdin:       handler.Stdin,
		Stdout:      handler.Stdout,
		Stderr:      handler.Stderr,
		Directory:   workspaceDirectory(handler.Dir),
		Storage:     s.storage,
		Environment: exported(handler.Env),
		session:     s,
	}
	if invocation.Stdin == nil {
		invocation.Stdin = emptyInput()
	}
	if buffer, ok := handler.Stdout.(*strings.Builder); ok {
		// The interpreter collects $(...) in this buffer.
		invocation.Stdout = substitutionWriter{buffer: buffer}
	}
	status := s.dispatch(ctx, arguments[0], invocation, handler.Pos)
	if err := ctx.Err(); err != nil {
		return err
	}
	if status == 0 {
		status = flushOwned(handler)
	}
	if status == 0 {
		return nil
	}
	return interp.ExitStatus(status)
}

// exported returns the exported string variables as NAME=value.
func exported(environment expand.Environ) []string {
	variables := []string{}
	for name, variable := range environment.Each {
		if variable.Exported && variable.Kind == expand.String {
			variables = append(variables, name+"="+variable.Str)
		}
	}
	return variables
}

// dispatch runs one command by name and returns its exit status; errors are
// already on stderr.
func (s *session) dispatch(ctx context.Context, name string, invocation *Invocation, position syntax.Pos) uint8 {
	switch name {
	case internalFailure:
		if len(invocation.Arguments) != 2 {
			return statusInternalError
		}
		fmt.Fprintln(invocation.Stderr, invocation.Arguments[0])
		status, _ := strconv.Atoi(invocation.Arguments[1])
		return uint8(status) //nolint:gosec // G115: the shell itself writes statuses 1, 2 and 127 here
	case internalLookup:
		if len(invocation.Arguments) < 2 {
			return statusInternalError
		}
		return s.lookup(invocation, position)
	case internalOutput:
		if len(invocation.Arguments) == 0 {
			return statusInternalError
		}
		return runOutput(invocation, position)
	}
	if command, ok := s.commands[name]; ok {
		return runCommand(ctx, command, invocation)
	}
	if interp.IsBuiltin(name) {
		return s.runBuiltin(ctx, name, invocation)
	}
	if strings.Contains(name, "/") {
		return s.runPath(ctx, name, invocation, position)
	}
	fmt.Fprintf(invocation.Stderr, "bash: %s%s: command not found\n", linePrefix(position), name)
	return statusNotFound
}

// known reports whether name runs as a command: registered or a shell
// builtin.
func (s *session) known(name string) bool {
	_, ok := s.commands[name]
	return ok || interp.IsBuiltin(name)
}

// linePrefix is bash's "line N: " in front of a message, empty when the
// position is unknown.
func linePrefix(position syntax.Pos) string {
	if !position.IsValid() {
		return ""
	}
	return fmt.Sprintf("line %d: ", position.Line())
}

// runPath answers a command given as a path (./build.sh): files carry no
// execute bit in a Storage, so it fails like bash on a non-executable file.
func (s *session) runPath(ctx context.Context, name string, invocation *Invocation, position syntax.Pos) uint8 {
	reason, status := error(errNotFound), uint8(statusNotFound)
	if resolved, err := Resolve(invocation.Directory, name); err == nil {
		found, err := s.files().stat(ctx, resolved)
		switch {
		case err != nil:
			reason = describe(err)
		case found.kind == entryDirectory:
			reason, status = errIsDirectory, statusCannotExecute
		case found.kind == entryFile:
			reason, status = fileError("Permission denied (no execute bit in the workspace; run it with: bash "+name+")"), statusCannotExecute
		case found.kind == entryMissing, found.kind == entryDevice:
		}
	}
	fmt.Fprintf(invocation.Stderr, "bash: %s%s: %v\n", linePrefix(position), name, reason)
	return status
}

// runCommand runs a Command and turns its result into an exit status. A
// panic is reported as an internal error, because a pipeline runs commands
// on goroutines where a panic would end the process.
func runCommand(ctx context.Context, command Command, invocation *Invocation) (status uint8) {
	defer func() {
		if recovered := recover(); recovered != nil {
			complain(invocation, command.Name, "internal error: %v", recovered)
			status = statusInternalError
		}
	}()
	err := command.Run(ctx, invocation)
	var exit ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return uint8(exit)
	case errors.Is(err, syscall.EPIPE):
		// The reader of a pipe is gone (head -1): bash's SIGPIPE status,
		// silently.
		return statusBrokenPipe
	case ctx.Err() != nil:
		return statusCommandFailure
	}
	complain(invocation, command.Name, "%v", describe(err))
	return statusCommandFailure
}

// rewrite is the interpreter's call handler. It runs before every simple
// command, builtins included, and changes the few builtins that would
// otherwise consult the host: lookups in PATH, symlink resolution, and the
// wording of cd's errors. A builtin reached through command, builtin or exec
// is rewritten like a direct call: normalizeCall resolves those wrappers
// first, in every form the interpreter accepts.
func (s *session) rewrite(ctx context.Context, arguments []string) (_ []string, err error) {
	defer recoverHandler(&err)
	handler := interp.HandlerCtx(ctx)
	call, failure := normalizeCall(arguments)
	switch {
	case failure != "":
		return []string{internalFailure, "bash: " + linePrefix(handler.Pos) + failure, strconv.Itoa(statusUnsupported)}, nil
	case len(call.arguments) > 0 && strings.ContainsRune(call.arguments[0], 0):
		// Only these rewrites may name an internal command. A script can
		// still spell a NUL byte (read puts one in $REPLY); bash cuts the
		// name there.
		name, _, _ := strings.Cut(call.arguments[0], "\x00")
		return []string{internalFailure, "bash: " + linePrefix(handler.Pos) + name + ": command not found", strconv.Itoa(statusNotFound)}, nil
	case call.show != "":
		return append(call.prefix(), append([]string{internalLookup, "command " + call.show, ""}, call.arguments...)...), nil
	case len(call.arguments) == 0:
		return append(call.prefix(), call.arguments...), nil
	case call.builtinOnly && !interp.IsBuiltin(call.arguments[0]):
		return []string{internalFailure, "bash: " + linePrefix(handler.Pos) + "builtin: " + call.arguments[0] + ": not a shell builtin", "1"}, nil
	case routesOutput(handler, call.arguments):
		return append(call.prefix(), append([]string{internalOutput}, call.arguments...)...), nil
	}
	return append(call.prefix(), s.rewriteCall(ctx, handler, call.arguments)...), nil
}

// wrappedCall is a simple command with its leading command, builtin and
// exec words resolved.
type wrappedCall struct {
	// arguments are the command that runs and its arguments; with show, the
	// names to look up.
	arguments []string
	// exec and command record the wrappers that were present: the rewritten
	// call keeps their meaning (exec ends the shell, command and builtin skip
	// functions) in the one form the interpreter parses without options.
	exec, command bool
	// builtinOnly is set when the last wrapper was builtin: the command must
	// be a shell builtin.
	builtinOnly bool
	// show is "-v" or "-V" when command looks the names up instead.
	show string
}

// prefix returns the wrapper words the rewritten call starts with.
func (c wrappedCall) prefix() []string {
	switch {
	case c.exec:
		return []string{"exec"}
	case c.command:
		return []string{"command"}
	}
	return nil
}

// normalizeCall resolves the wrappers in front of a command like bash does:
// any sequence of "command [-pvV]... [--]", "builtin [--]" and "exec [--]",
// with combined flags (-pv) and repetitions. It returns the message for an
// invalid option of command, without the "bash: " prefix.
func normalizeCall(arguments []string) (wrappedCall, string) {
	var call wrappedCall
	for len(arguments) > 1 {
		switch arguments[0] {
		case "builtin":
			call.command, call.builtinOnly = true, true
			arguments = skipDoubleDash(arguments[1:])
		case "exec":
			call.exec, call.builtinOnly = true, false
			arguments = skipDoubleDash(arguments[1:])
		case "command":
			call.command, call.builtinOnly = true, false
			var failure string
			arguments, failure = commandOptions(&call, arguments[1:])
			if failure != "" || call.show != "" {
				call.arguments = arguments
				return call, failure
			}
		default:
			call.arguments = arguments
			return call, ""
		}
	}
	call.arguments = arguments
	return call, ""
}

// commandOptions consumes the options of command: -p (the default PATH,
// which changes nothing here), -v and -V (the last one counts), up to "--".
func commandOptions(call *wrappedCall, arguments []string) ([]string, string) {
	for len(arguments) > 0 && len(arguments[0]) > 1 && arguments[0][0] == '-' {
		option := arguments[0]
		arguments = arguments[1:]
		if option == "--" {
			break
		}
		for _, letter := range option[1:] {
			switch letter {
			case 'p':
			case 'v', 'V':
				call.show = "-" + string(letter)
			default:
				return arguments, "command: -" + string(letter) + ": invalid option\ncommand: usage: command [-pVv] command [arg ...]"
			}
		}
	}
	return arguments, ""
}

func skipDoubleDash(arguments []string) []string {
	if len(arguments) > 0 && arguments[0] == "--" {
		return arguments[1:]
	}
	return arguments
}

// rewriteCall rewrites one builtin call without its wrappers.
func (s *session) rewriteCall(ctx context.Context, handler interp.HandlerContext, arguments []string) []string {
	switch arguments[0] {
	case "cd":
		return s.rewriteChangeDirectory(ctx, handler, arguments)
	case "source", ".":
		if len(arguments) > 1 && !strings.Contains(arguments[1], "/") {
			// Without a slash the builtin searches PATH on the host; the
			// virtual shell sources from the working directory.
			arguments = append([]string{arguments[0], "./" + arguments[1]}, arguments[2:]...)
		}
		return s.rewriteSource(ctx, handler, arguments)
	case "eval", "trap":
		return s.rewriteScriptArguments(arguments)
	case "pwd":
		// -P resolves symlinks on the host; workspace paths have none.
		return []string{"pwd"}
	case "type":
		return s.rewriteType(handler, arguments)
	case "shift":
		// The interpreter slices the parameters with a negative count and
		// panics, in a goroutine for "shift -1 &".
		if len(arguments) == 2 {
			if count, err := strconv.Atoi(arguments[1]); err == nil && count < 0 {
				return []string{internalFailure, "bash: " + linePrefix(handler.Pos) + "shift: " + arguments[1] + ": shift count out of range", "1"}
			}
		}
	}
	return arguments
}

// rewriteChangeDirectory checks cd's target first, so a failure reads like
// bash's ("cd: x: No such file or directory").
func (s *session) rewriteChangeDirectory(ctx context.Context, handler interp.HandlerContext, arguments []string) []string {
	prefix := "bash: " + linePrefix(handler.Pos) + "cd: "
	// -P and -L differ only on symlinks, which workspace paths do not have;
	// they are dropped so the builtin never resolves host links.
	operands := arguments[1:]
	for len(operands) > 0 && (operands[0] == "-P" || operands[0] == "-L") {
		operands = operands[1:]
	}
	if len(operands) > 0 && operands[0] == "--" {
		operands = operands[1:]
	}
	arguments = append([]string{"cd"}, operands...)
	if len(arguments) > 2 {
		return []string{internalFailure, prefix + "too many arguments", "1"}
	}
	if len(arguments) < 2 && handler.Env.Get("HOME").String() == "" {
		return []string{internalFailure, prefix + "HOME not set", "1"}
	}
	if len(arguments) < 2 || arguments[1] == "-" || arguments[1] == "" {
		return arguments
	}
	target := arguments[1]
	if !strings.HasPrefix(target, "/") {
		target = handler.Dir + "/" + target
	}
	found, err := s.files().stat(ctx, workspaceDirectory(target))
	switch {
	case err != nil:
		return []string{internalFailure, prefix + arguments[1] + ": " + describe(err).Error(), "1"}
	case found.kind == entryMissing:
		return []string{internalFailure, prefix + arguments[1] + ": " + errNotFound.Error(), "1"}
	case found.kind != entryDirectory:
		return []string{internalFailure, prefix + arguments[1] + ": " + errNotDirectory.Error(), "1"}
	}
	return arguments
}

// pathError is how a handler reports a file error: the interpreter prints
// it as "<prefix>name: reason", prefix being "bash: " or "bash: line N: ".
func pathError(prefix, name string, err error) error {
	return &os.PathError{Op: strings.TrimSuffix(prefix, " "), Path: name, Err: describe(err)}
}

// redirectLineMarker separates a redirect's word from what
// markRedirectLines appends. The interpreter cuts words at a NUL byte, so
// the marker starts with a byte that is never valid UTF-8 instead; a script
// that spells it only changes a message or skips an early store.
const redirectLineMarker = "\xffline="

// markRedirectLines appends to the word of every file redirect its line and
// the offset+1 of the simple command its statement runs (0 for a compound
// statement): the interpreter gives the open handler no position, bash's
// message for a failed redirect starts with "bash: line N: ", and
// flushOwned stores the targets of a simple command when it ends. The
// marker goes at the end so a leading ~ still expands; open takes it off.
func markRedirectLines(file *syntax.File) {
	syntax.Walk(file, func(node syntax.Node) bool {
		statement, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		owner := uint(0)
		if call, ok := statement.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
			owner = call.Args[0].Pos().Offset() + 1
		}
		for _, redirect := range statement.Redirs {
			if slices.Contains(fileRedirects, redirect.Op) {
				end := redirect.Word.End()
				marker := fmt.Sprintf("%s%d,%d", redirectLineMarker, redirect.OpPos.Line(), owner)
				redirect.Word.Parts = append(redirect.Word.Parts, &syntax.Lit{ValuePos: end, ValueEnd: end, Value: marker})
			}
		}
		return true
	})
}

// fileRedirects are the redirect operators the interpreter opens a file for.
var fileRedirects = []syntax.RedirOperator{syntax.RdrOut, syntax.AppOut, syntax.RdrIn, syntax.RdrAll, syntax.AppAll}

// redirectName splits a redirect name marked by markRedirectLines into
// bash's message prefix, the name itself and the owning command's offset+1.
func redirectName(marked string) (prefix, name string, owner uint) {
	index := strings.LastIndex(marked, redirectLineMarker)
	if index < 0 {
		return "bash: ", marked, 0
	}
	line, offset, _ := strings.Cut(marked[index+len(redirectLineMarker):], ",")
	parsed, err := strconv.ParseUint(offset, 10, 0)
	if err != nil {
		parsed = 0
	}
	return "bash: line " + line + ": ", marked[:index], uint(parsed)
}

// open is the interpreter's open handler for redirects and source. Reads get
// a pipe filled from the file; writes are buffered and stored with one
// WriteFile when the redirect closes.
func (s *session) open(ctx context.Context, name string, flag int, _ os.FileMode, targets *redirectTargets) (_ io.ReadWriteCloser, err error) {
	defer recoverHandler(&err)
	handler := interp.HandlerCtx(ctx)
	prefix, name, owner := redirectName(name)
	if name == processFileMarker && flag&os.O_WRONLY != 0 {
		return s.openProcessFile(ctx, prefix, handler, targets)
	}
	resolved, err := Resolve(workspaceDirectory(handler.Dir), name)
	if err != nil {
		return nil, pathError(prefix, name, errNotFound)
	}
	if isDevice(resolved) {
		return openDevice(resolved, flag, handler)
	}
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_RDONLY:
		content, err := readFile(ctx, s.files(), resolved)
		if err == nil && strings.HasSuffix(name, "/") {
			err = errNotDirectory // "< f/" with f a file
		}
		if err != nil {
			return nil, pathError(prefix, name, err)
		}
		s.processFiles.consume(ctx, resolved)
		return pipeReader(content)
	case os.O_WRONLY:
		file, err := s.openForWriting(ctx, prefix, name, resolved, flag&os.O_APPEND != 0)
		if err != nil {
			return nil, err
		}
		file.owner = owner
		targets.add(file)
		return file, nil
	}
	return nil, pathError(prefix, name, errUnsupported)
}

// openForWriting opens a redirect target like bash: ">" truncates the file
// and ">>" keeps it, both right away, so a target the Storage refuses (a
// read-only mount) fails the redirect before the command runs and before
// && or || decide. The returned file collects what is written and stores
// it on Close.
func (s *session) openForWriting(ctx context.Context, prefix, name, resolved string, appending bool) (*bufferedFile, error) {
	files := s.files()
	if err := files.checkWritable(ctx, resolved); err != nil {
		return nil, pathError(prefix, name, err)
	}
	if strings.HasSuffix(name, "/") {
		// "out/" names a directory, which cannot be created as a file:
		// Linux refuses O_CREAT with a trailing slash with EISDIR, also
		// when out is a file.
		return nil, pathError(prefix, name, errIsDirectory)
	}
	file := s.newBufferedFile(ctx, prefix+name, interp.HandlerCtx(ctx).Stderr)
	if appending {
		content, _, err := files.storage.ReadFile(ctx, resolved)
		switch {
		case errors.Is(err, vfs.ErrFileNotFound):
		case err != nil:
			return nil, pathError(prefix, name, err)
		case !s.budget.reserve(len(content)):
			return nil, pathError(prefix, name, errNoSpace)
		default:
			file.content, file.reserved = content, len(content)
		}
	}
	contentType := contentTypeFor(resolved)
	if err := files.storage.WriteFile(ctx, resolved, file.content, contentType); err != nil {
		s.budget.release(file.reserved)
		return nil, pathError(prefix, name, err)
	}
	file.commit = func(ctx context.Context, content []byte) error {
		return files.storage.WriteFile(ctx, resolved, content, contentType)
	}
	return file, nil
}

// newBufferedFile returns an empty redirect target that counts against the
// session's budget and stores with storeContext(ctx).
func (s *session) newBufferedFile(ctx context.Context, label string, stderr io.Writer) *bufferedFile {
	return &bufferedFile{
		label: label, stderr: stderr, budget: s.budget,
		storeContext: func() (context.Context, context.CancelFunc) { return storeContext(ctx) },
	}
}

// flushOwned stores the redirect targets of the simple command that just
// ran through the exec handler when its statement opened them (cmd >file):
// a failed store then fails the command, before && or || decide. Targets of
// compound statements ({ ...; } >file, loops) are stored when they close;
// a failure there is reported but cannot change a status any more.
func flushOwned(handler interp.HandlerContext) uint8 {
	status := uint8(0)
	for _, stream := range []io.Writer{handler.Stdout, handler.Stderr} {
		file, ok := stream.(*bufferedFile)
		if ok && file.owner != 0 && file.owner == handler.Pos.Offset()+1 && file.flush() != nil {
			status = statusCommandFailure
		}
	}
	return status
}

// redirectTargets are the redirect files one interpreter (with its
// subshells) opened. The interpreter closes each after its statement,
// except the ones exec keeps open for the rest of the script (exec >file);
// closeAll stores those when the interpreter is done.
type redirectTargets struct {
	mutex sync.Mutex
	files []*bufferedFile
}

// add tracks file until it closes, so a closed file and its content are
// released at once.
func (r *redirectTargets) add(file *bufferedFile) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.files = append(r.files, file)
	file.onClose = r.remove
}

func (r *redirectTargets) remove(file *bufferedFile) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if index := slices.Index(r.files, file); index >= 0 {
		r.files = slices.Delete(r.files, index, index+1)
	}
}

// closeAll stores every file still open, all within one store period
// (storeContext of ctx); each reports a failed store on stderr itself.
func (r *redirectTargets) closeAll(ctx context.Context) {
	r.mutex.Lock()
	files := slices.Clone(r.files)
	r.mutex.Unlock()
	shared, cancel := storeContext(ctx)
	defer cancel()
	for _, file := range files {
		_ = file.closeWith(shared)
	}
}

// openDevice opens /dev/null, /dev/stdin, /dev/stdout or /dev/stderr.
func openDevice(name string, flag int, handler interp.HandlerContext) (io.ReadWriteCloser, error) {
	reading := flag&(os.O_WRONLY|os.O_RDWR) == 0
	switch {
	case name == "dev/stdin" && reading && handler.Stdin != nil:
		return &streamFile{reader: handler.Stdin}, nil
	case name == "dev/stdout" && !reading:
		return &streamFile{writer: handler.Stdout}, nil
	case name == "dev/stderr" && !reading:
		return &streamFile{writer: handler.Stderr}, nil
	case reading:
		return pipeReader(nil)
	}
	return &streamFile{writer: io.Discard}, nil
}

// stat is the interpreter's stat handler (test -e/-f/-d, cd); name is a
// virtual absolute path.
func (s *session) stat(ctx context.Context, name string, _ bool) (_ fs.FileInfo, err error) {
	defer recoverHandler(&err)
	resolved := workspaceDirectory(name)
	found, err := s.files().stat(ctx, resolved)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: describe(err)}
	}
	if found.kind == entryMissing {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return fileInfo{name: path.Base(name), entry: found}, nil
}

// readDirectory is the interpreter's glob handler; name is a virtual
// absolute path.
func (s *session) readDirectory(ctx context.Context, name string) (_ []fs.DirEntry, err error) {
	defer recoverHandler(&err)
	resolved := workspaceDirectory(name)
	if s.root != resolved && within(s.root, resolved) {
		// The interpreter globs /work/repo/* one directory at a time; an
		// ancestor of the mount point shows only the way down.
		next, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(s.root, resolved), "/"), "/")
		return []fs.DirEntry{fs.FileInfoToDirEntry(fileInfo{name: next, entry: entry{kind: entryDirectory}})}, nil
	}
	files := s.files()
	found, err := files.stat(ctx, resolved)
	switch {
	case err != nil:
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	case found.kind == entryMissing:
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	case found.kind != entryDirectory:
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: syscall.ENOTDIR}
	}
	listed, err := files.children(ctx, resolved)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	entries := make([]fs.DirEntry, 0, len(listed))
	for _, item := range listed {
		entries = append(entries, fs.FileInfoToDirEntry(fileInfo(item)))
	}
	return entries, nil
}

// access is the interpreter's access handler (test -r/-w/-x, cd): every
// existing path is readable and writable, only directories are executable.
func (s *session) access(ctx context.Context, name string, mode interp.AccessMode) (err error) {
	defer recoverHandler(&err)
	found, err := s.files().stat(ctx, workspaceDirectory(name))
	switch {
	case err != nil:
		return err
	case found.kind == entryMissing:
		return fs.ErrNotExist
	case mode&interp.AccessExec != 0 && found.kind != entryDirectory:
		return fs.ErrPermission
	}
	return nil
}
