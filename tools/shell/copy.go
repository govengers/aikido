package shell

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// transfer is one source and destination of mv or cp, as operands (for
// messages) and as workspace paths.
type transfer struct {
	sourceOperand, source string
	targetOperand, target string
	sourceEntry           entry
	targetEntry           entry
}

// transferMode is what mv and cp share: the command name and its flags.
type transferMode struct {
	command   string
	move      bool
	recursive bool
	noClobber bool
	verbose   bool
}

func mvCommand() Command {
	return Command{Name: "mv", Summary: "move or rename files and directories (-f -n -v)", Run: runMv}
}

func runMv(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := optionSet{flags: "fnv", long: map[string]longOption{
		"force": {name: "f"}, "no-clobber": {name: "n"}, "verbose": {name: "v"},
	}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "mv", err, errFailed)
	}
	mode := transferMode{command: "mv", move: true, recursive: true, noClobber: parsed.has("n"), verbose: parsed.has("v")}
	return runTransfers(ctx, invocation, mode, operands)
}

func cpCommand() Command {
	return Command{Name: "cp", Summary: "copy files (-r -R -a recursive, -f -n -v; -p accepted)", Run: runCp}
}

func runCp(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := optionSet{flags: "rRafnpv", long: map[string]longOption{
		"recursive": {name: "r"}, "archive": {name: "a"}, "force": {name: "f"},
		"no-clobber": {name: "n"}, "verbose": {name: "v"}, "preserve": {name: "p"},
	}}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "cp", err, errFailed)
	}
	mode := transferMode{
		command:   "cp",
		recursive: parsed.has("r") || parsed.has("R") || parsed.has("a"),
		noClobber: parsed.has("n"),
		verbose:   parsed.has("v"),
	}
	return runTransfers(ctx, invocation, mode, operands)
}

// runTransfers plans and runs the transfers of one mv or cp.
func runTransfers(ctx context.Context, invocation *Invocation, mode transferMode, operands []string) error {
	switch len(operands) {
	case 0:
		return usage(invocation, mode.command, errors.New("missing file operand"), errFailed)
	case 1:
		return usage(invocation, mode.command, fmt.Errorf("missing destination file operand after '%s'", operands[0]), errFailed)
	}
	files := workspaceOf(invocation)
	transfers, err := planTransfers(ctx, invocation, files, operands, mode.move)
	if err != nil {
		complain(invocation, mode.command, "%v", err)
		return errFailed
	}
	var status error
	for _, planned := range transfers {
		if err := mode.run(ctx, files, invocation.Directory, planned); err != nil {
			for _, problem := range splitErrors(err) {
				complain(invocation, mode.command, "%v", problem)
			}
			status = errFailed
			continue
		}
		if mode.verbose {
			verb := ""
			if mode.move {
				verb = "renamed "
			}
			fmt.Fprintf(invocation.Stdout, "%s'%s' -> '%s'\n", verb, planned.sourceOperand, planned.targetOperand)
		}
	}
	return status
}

// planTransfers pairs every source with its target: into the last operand
// when it is a directory, else the one source onto it.
func planTransfers(ctx context.Context, invocation *Invocation, files workspace, operands []string, move bool) ([]transfer, error) {
	sources, last := operands[:len(operands)-1], operands[len(operands)-1]
	lastName, err := resolve(invocation, last)
	if err != nil {
		return nil, fmt.Errorf("cannot stat '%s': %w", last, err)
	}
	lastEntry, err := files.stat(ctx, lastName)
	if err != nil {
		return nil, err
	}
	intoDirectory := lastEntry.kind == entryDirectory
	switch {
	case len(sources) > 1 && !intoDirectory:
		return nil, fmt.Errorf("target '%s' is not a directory", last)
	case strings.HasSuffix(last, "/") && !intoDirectory:
		// "out/" names a directory; GNU never creates a file for it.
		switch {
		case lastEntry.kind != entryMissing:
			return nil, fmt.Errorf("failed to access '%s': %w", last, errNotDirectory)
		case move:
			return nil, fmt.Errorf("cannot move '%s' to '%s': %w", sources[0], last, errNotDirectory)
		}
		return nil, fmt.Errorf("cannot create regular file '%s': %w", last, errNotDirectory)
	}
	transfers := make([]transfer, 0, len(sources))
	for _, source := range sources {
		planned := transfer{sourceOperand: source, targetOperand: last, target: lastName, targetEntry: lastEntry}
		if intoDirectory {
			base := path.Base(source)
			planned.targetOperand = strings.TrimSuffix(last, "/") + "/" + base
			planned.target = path.Join(lastName, base)
			if planned.targetEntry, err = files.stat(ctx, planned.target); err != nil {
				return nil, err
			}
		}
		transfers = append(transfers, planned)
	}
	return transfers, nil
}

// run executes one planned transfer; the error is the message after
// "mv: " or "cp: ".
func (m transferMode) run(ctx context.Context, files workspace, directory string, planned transfer) error {
	var err error
	if planned.source, err = Resolve(directory, planned.sourceOperand); err != nil {
		return fmt.Errorf("cannot stat '%s': %w", planned.sourceOperand, errNotFound)
	}
	if planned.sourceEntry, err = files.stat(ctx, planned.source); err != nil {
		return err
	}
	switch {
	case planned.sourceEntry.kind == entryMissing:
		return fmt.Errorf("cannot stat '%s': %w", planned.sourceOperand, errNotFound)
	case planned.source == planned.target:
		return fmt.Errorf("'%s' and '%s' are the same file", planned.sourceOperand, planned.targetOperand)
	case planned.sourceEntry.kind == entryDirectory:
		return m.transferDirectory(ctx, files, planned)
	case planned.targetEntry.kind == entryDirectory:
		return fmt.Errorf("cannot overwrite directory '%s' with non-directory", planned.targetOperand)
	case m.noClobber && planned.targetEntry.kind != entryMissing:
		return nil
	}
	return m.transferFile(ctx, files, planned)
}

func (m transferMode) transferFile(ctx context.Context, files workspace, planned transfer) error {
	content := []byte{}
	if planned.sourceEntry.kind == entryFile {
		read, err := readFile(ctx, files, planned.source)
		if err != nil {
			return fmt.Errorf("cannot open '%s' for reading: %w", planned.sourceOperand, err)
		}
		content = read
	}
	if err := files.writeFile(ctx, planned.target, content); err != nil {
		return m.targetError(planned, err)
	}
	if m.move && planned.sourceEntry.kind == entryFile {
		if err := files.storage.DeleteFile(ctx, planned.source); err != nil {
			return fmt.Errorf("cannot remove '%s': %w", planned.sourceOperand, describe(err))
		}
	}
	return nil
}

func (m transferMode) transferDirectory(ctx context.Context, files workspace, planned transfer) error {
	switch {
	case !m.recursive:
		return fmt.Errorf("-r not specified; omitting directory '%s'", planned.sourceOperand)
	case planned.source == "" || within(planned.target, planned.source):
		if m.move {
			return fmt.Errorf("cannot move '%s' to a subdirectory of itself, '%s'", planned.sourceOperand, planned.targetOperand)
		}
		return fmt.Errorf("cannot copy a directory, '%s', into itself, '%s'", planned.sourceOperand, planned.targetOperand)
	case planned.targetEntry.kind == entryFile || planned.targetEntry.kind == entryDevice:
		return fmt.Errorf("cannot overwrite non-directory '%s' with directory '%s'", planned.targetOperand, planned.sourceOperand)
	}
	if err := m.checkDirectoryTarget(ctx, files, planned); err != nil {
		return err
	}
	below, err := files.filesBelow(ctx, planned.source)
	if err != nil {
		return err
	}
	var problems []error
	for _, file := range below {
		relative := strings.TrimPrefix(file.Path, planned.source+"/")
		inner := transfer{
			source: file.Path, sourceOperand: path.Join(planned.sourceOperand, relative),
			target: path.Join(planned.target, relative), targetOperand: path.Join(planned.targetOperand, relative),
		}
		if err := m.transferBelow(ctx, files, inner, file.ContentType); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	for _, directory := range files.directories.list() {
		if rest, ok := relativeTo(directory, planned.source); ok {
			files.directories.add(path.Join(planned.target, rest))
		}
	}
	files.directories.add(planned.target)
	if m.move {
		files.directories.removeTree(planned.source)
	}
	return nil
}

// transferBelow copies or moves one file of a recursive transfer, with the
// checks GNU makes per destination: no file over a directory or below a
// file, and -n keeps an existing file.
func (m transferMode) transferBelow(ctx context.Context, files workspace, inner transfer, contentType string) error {
	targetEntry, err := files.stat(ctx, inner.target)
	if err != nil {
		return err
	}
	parent, err := files.stat(ctx, parentOf(inner.target))
	switch {
	case err != nil:
		return err
	case parent.kind == entryFile:
		return fmt.Errorf("cannot overwrite non-directory '%s' with directory '%s'", path.Dir(inner.targetOperand), path.Dir(inner.sourceOperand))
	case targetEntry.kind == entryDirectory:
		return fmt.Errorf("cannot overwrite directory '%s' with non-directory", inner.targetOperand)
	case m.noClobber && targetEntry.kind != entryMissing:
		return nil
	}
	content, err := readFile(ctx, files, inner.source)
	if err == nil {
		err = files.storage.WriteFile(ctx, inner.target, content, contentType)
	}
	if err == nil && m.move {
		err = files.storage.DeleteFile(ctx, inner.source)
	}
	if err != nil {
		return m.targetError(inner, err)
	}
	return nil
}

// checkDirectoryTarget refuses to merge into a non-empty directory on mv and
// needs the target's parent to exist.
func (m transferMode) checkDirectoryTarget(ctx context.Context, files workspace, planned transfer) error {
	if planned.targetEntry.kind == entryDirectory {
		listed, err := files.children(ctx, planned.target)
		if err != nil {
			return err
		}
		if m.move && len(listed) > 0 {
			return fmt.Errorf("cannot move '%s' to '%s': %w", planned.sourceOperand, planned.targetOperand, errNotEmpty)
		}
		return nil
	}
	parent, err := files.stat(ctx, parentOf(planned.target))
	if err != nil {
		return err
	}
	if parent.kind != entryDirectory {
		return m.targetError(planned, errNotFound)
	}
	return nil
}

// targetError words a failure to write the target the way mv and cp do.
func (m transferMode) targetError(planned transfer, err error) error {
	if m.move {
		return fmt.Errorf("cannot move '%s' to '%s': %w", planned.sourceOperand, planned.targetOperand, describe(err))
	}
	return fmt.Errorf("cannot create regular file '%s': %w", planned.targetOperand, describe(err))
}

// splitErrors returns the errors errors.Join combined, or err alone.
func splitErrors(err error) []error {
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		return joined.Unwrap()
	}
	return []error{err}
}
