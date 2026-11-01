package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

func mkdirCommand() Command {
	return Command{Name: "mkdir", Summary: "create directories (-p parents, -v)", Run: runMkdir}
}

func runMkdir(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := (optionSet{flags: "pv", long: map[string]longOption{"parents": {name: "p"}, "verbose": {name: "v"}}}).parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "mkdir", err, errFailed)
	}
	if len(operands) == 0 {
		return usage(invocation, "mkdir", errors.New("missing operand"), errFailed)
	}
	files := workspaceOf(invocation)
	var status error
	for _, operand := range operands {
		created, err := makeDirectory(ctx, files, invocation.Directory, operand, parsed.has("p"))
		if err != nil {
			complain(invocation, "mkdir", "cannot create directory '%s': %v", operand, err)
			status = errFailed
			continue
		}
		if parsed.has("v") {
			for _, name := range created {
				fmt.Fprintf(invocation.Stdout, "mkdir: created directory '%s'\n", name)
			}
		}
	}
	return status
}

// makeDirectory creates operand (and with parents its missing ancestors)
// and returns the operand spellings of what it created, for -v.
func makeDirectory(ctx context.Context, files workspace, directory, operand string, parents bool) ([]string, error) {
	target, err := Resolve(directory, operand)
	if err != nil {
		return nil, errNotFound
	}
	if !parents {
		if err := checkCreatable(ctx, files, target); err != nil {
			return nil, err
		}
		files.directories.add(target)
		return []string{operand}, nil
	}
	created := []string{}
	root := ""
	if strings.HasPrefix(operand, "/") {
		root = "/"
	}
	segments := strings.Split(strings.Trim(operand, "/"), "/")
	for index := range segments {
		spelled := root + strings.Join(segments[:index+1], "/")
		name, err := Resolve(directory, spelled)
		if err != nil {
			return created, errNotFound
		}
		found, err := files.stat(ctx, name)
		switch {
		case err != nil:
			return created, err
		case found.kind == entryDirectory:
			continue
		case found.kind != entryMissing && name == target:
			return created, errExists
		case found.kind != entryMissing:
			return created, errNotDirectory
		}
		files.directories.add(name)
		created = append(created, spelled)
	}
	return created, nil
}

// checkCreatable returns nil when name does not exist and its parent is a
// directory.
func checkCreatable(ctx context.Context, files workspace, name string) error {
	found, err := files.stat(ctx, name)
	if err != nil {
		return err
	}
	if found.kind != entryMissing {
		return errExists
	}
	parent, err := files.stat(ctx, parentOf(name))
	switch {
	case err != nil:
		return err
	case parent.kind == entryDirectory:
		return nil
	case parent.kind == entryMissing:
		return errNotFound
	}
	return errNotDirectory
}

func rmdirCommand() Command {
	return Command{Name: "rmdir", Summary: "remove empty directories", Run: runRmdir}
}

func runRmdir(ctx context.Context, invocation *Invocation) error {
	_, operands, err := optionSet{}.parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "rmdir", err, errFailed)
	}
	if len(operands) == 0 {
		return usage(invocation, "rmdir", errors.New("missing operand"), errFailed)
	}
	files := workspaceOf(invocation)
	var status error
	for _, operand := range operands {
		if err := removeEmptyDirectory(ctx, files, invocation.Directory, operand); err != nil {
			complain(invocation, "rmdir", "failed to remove '%s': %v", operand, err)
			status = errFailed
		}
	}
	return status
}

func removeEmptyDirectory(ctx context.Context, files workspace, directory, operand string) error {
	name, err := Resolve(directory, operand)
	if err != nil {
		return errNotFound
	}
	found, err := files.stat(ctx, name)
	switch {
	case err != nil:
		return err
	case found.kind == entryMissing:
		return errNotFound
	case found.kind != entryDirectory:
		return errNotDirectory
	case isMountPoint(name):
		return fileError("Device or resource busy")
	}
	listed, err := files.children(ctx, name)
	if err != nil {
		return err
	}
	if len(listed) > 0 {
		return errNotEmpty
	}
	files.directories.removeTree(name)
	return nil
}

func rmCommand() Command {
	return Command{Name: "rm", Summary: "remove files (-r -R -f -v)", Run: runRm}
}

func runRm(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := (optionSet{flags: "rRfv", long: map[string]longOption{
		"recursive": {name: "r"}, "force": {name: "f"}, "verbose": {name: "v"},
	}}).parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "rm", err, errFailed)
	}
	force := parsed.has("f")
	if len(operands) == 0 {
		if force {
			return nil
		}
		return usage(invocation, "rm", errors.New("missing operand"), errFailed)
	}
	remover := remover{
		invocation: invocation,
		files:      workspaceOf(invocation),
		recursive:  parsed.has("r") || parsed.has("R"),
		force:      force,
		verbose:    parsed.has("v"),
	}
	var status error
	for _, operand := range operands {
		if !remover.remove(ctx, operand) {
			status = errFailed
		}
	}
	return status
}

// remover removes one rm operand at a time and reports failures itself.
type remover struct {
	invocation *Invocation
	files      workspace
	recursive  bool
	force      bool
	verbose    bool
}

// remove removes operand and reports whether it succeeded.
func (r remover) remove(ctx context.Context, operand string) bool {
	if base := path.Base(operand); base == "." || base == ".." {
		complain(r.invocation, "rm", "refusing to remove '.' or '..' directory: skipping '%s'", operand)
		return false
	}
	name, err := Resolve(r.invocation.Directory, operand)
	if err != nil {
		return r.fail(operand, errNotFound)
	}
	found, err := r.files.stat(ctx, name)
	switch {
	case err != nil:
		return r.fail(operand, err)
	case found.kind == entryMissing:
		return r.fail(operand, errNotFound)
	case found.kind == entryDevice:
		return r.fail(operand, fileError("Permission denied"))
	case found.kind == entryFile:
		if err := r.files.storage.DeleteFile(ctx, name); err != nil {
			return r.fail(operand, describe(err))
		}
		r.report("removed '%s'", operand)
		return true
	case !r.recursive:
		return r.fail(operand, errIsDirectory)
	case name == "":
		complain(r.invocation, "rm", "it is dangerous to operate recursively on '/'")
		complain(r.invocation, "rm", "use --no-preserve-root to override this failsafe")
		return false
	}
	return r.removeTree(ctx, operand, name)
}

func (r remover) removeTree(ctx context.Context, operand, name string) bool {
	files, err := r.files.filesBelow(ctx, name)
	if err != nil {
		return r.fail(operand, err)
	}
	for _, file := range files {
		if err := r.files.storage.DeleteFile(ctx, file.Path); err != nil {
			return r.fail(operand, describe(err))
		}
		r.report("removed '%s'", path.Join(operand, strings.TrimPrefix(file.Path, name+"/")))
	}
	r.files.directories.removeTree(name)
	r.report("removed directory '%s'", operand)
	return true
}

// fail reports err for operand unless -f hides a missing file; it returns
// whether rm still succeeds.
func (r remover) fail(operand string, err error) bool {
	if r.force && errors.Is(err, errNotFound) {
		return true
	}
	complain(r.invocation, "rm", "cannot remove '%s': %v", operand, err)
	return false
}

func (r remover) report(format string, arguments ...any) {
	if r.verbose {
		fmt.Fprintf(r.invocation.Stdout, format+"\n", arguments...)
	}
}

func touchCommand() Command {
	return Command{Name: "touch", Summary: "create empty files (-c no create); existing files stay unchanged", Run: runTouch}
}

func runTouch(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := (optionSet{flags: "c", long: map[string]longOption{"no-create": {name: "c"}}}).parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "touch", err, errFailed)
	}
	if len(operands) == 0 {
		return usage(invocation, "touch", errors.New("missing file operand"), errFailed)
	}
	files := workspaceOf(invocation)
	var status error
	for _, operand := range operands {
		if err := touch(ctx, files, invocation.Directory, operand, parsed.has("c")); err != nil {
			complain(invocation, "touch", "cannot touch '%s': %v", operand, err)
			status = errFailed
		}
	}
	return status
}

func touch(ctx context.Context, files workspace, directory, operand string, noCreate bool) error {
	name, err := Resolve(directory, operand)
	if err != nil {
		return errNotFound
	}
	found, err := files.stat(ctx, name)
	if err != nil || found.kind != entryMissing || noCreate {
		return err
	}
	return files.writeFile(ctx, name, nil)
}

func teeCommand() Command {
	return Command{Name: "tee", Summary: "copy stdin to stdout and files (-a append)", Run: runTee}
}

func runTee(ctx context.Context, invocation *Invocation) error {
	parsed, operands, err := (optionSet{flags: "a", long: map[string]longOption{"append": {name: "a"}}}).parse(invocation.Arguments)
	if err != nil {
		return usage(invocation, "tee", err, errFailed)
	}
	var collected strings.Builder
	_, copyErr := io.Copy(io.MultiWriter(invocation.Stdout, &limitedBuilder{builder: &collected}), invocation.Stdin)
	target := teeTarget{invocation: invocation, files: workspaceOf(invocation), content: collected.String(), appending: parsed.has("a")}
	var status error
	for _, operand := range operands {
		if err := target.write(ctx, operand); err != nil {
			complain(invocation, "tee", "%s: %v", operand, err)
			status = errFailed
		}
	}
	if copyErr != nil {
		return copyErr
	}
	return status
}

// teeTarget is what tee writes to each of its operands.
type teeTarget struct {
	invocation *Invocation
	files      workspace
	content    string
	appending  bool
}

// write writes what tee read to one operand.
func (t teeTarget) write(ctx context.Context, operand string) error {
	invocation, files, content := t.invocation, t.files, t.content
	name, err := resolve(invocation, operand)
	if err != nil {
		return err
	}
	switch name {
	case "dev/null":
		return nil
	case "dev/stdout":
		_, err := io.WriteString(invocation.Stdout, content)
		return err
	case "dev/stderr":
		_, err := io.WriteString(invocation.Stderr, content)
		return err
	}
	if t.appending {
		existing, _, err := files.storage.ReadFile(ctx, name)
		if err == nil {
			content = string(existing) + content
		}
	}
	return files.writeFile(ctx, name, []byte(content))
}

// limitedBuilder collects up to maximumBufferedFileBytes and then fails
// writes with errTooLarge.
type limitedBuilder struct {
	builder *strings.Builder
}

func (l *limitedBuilder) Write(data []byte) (int, error) {
	if l.builder.Len()+len(data) > maximumBufferedFileBytes {
		return 0, errTooLarge
	}
	return l.builder.Write(data)
}
