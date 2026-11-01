package shell

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/govengers/aikido/vfs"
)

// fileError is an error text as the GNU tools print it (strerror wording), so
// messages read like bash: "cat: x: No such file or directory".
type fileError string

func (e fileError) Error() string { return string(e) }

const (
	errNotFound     fileError = "No such file or directory"
	errIsDirectory  fileError = "Is a directory"
	errNotDirectory fileError = "Not a directory"
	errExists       fileError = "File exists"
	errNotEmpty     fileError = "Directory not empty"
	errReadOnly     fileError = "Read-only file system"
	errTooLarge     fileError = "File too large"
	errUnsupported  fileError = "Operation not supported"
)

// scratchDirectory is the workspace path of /tmp: a scratchStorage of the
// Shell, never the workspace.
const scratchDirectory = "tmp"

// processFileDirectory is the workspace path of /dev/fd, where <(list)
// stores the output of list for one Run.
const processFileDirectory = "dev/fd"

// isMountPoint reports whether name is the workspace's mount point or a
// directory the shell mounts: always a directory, never removable.
func (w workspace) isMountPoint(name string) bool {
	return name == w.root || name == scratchDirectory || name == processFileDirectory
}

// inWorkspace reports whether name, a path of the session Storage, lies in
// the workspace mounted at root rather than in /tmp or /dev/fd, which are
// mounted over it when they are longer prefixes (vfs/mount routes to the
// longest).
func inWorkspace(name, root string) bool {
	if !within(name, root) {
		return false
	}
	for _, mounted := range []string{scratchDirectory, processFileDirectory} {
		if len(mounted) > len(root) && within(name, mounted) {
			return false
		}
	}
	return true
}

// reachable reports whether a script can see name: a path at or below the
// mount point, in /tmp, /dev/fd or a device. With the mount point "/"
// everything is reachable.
func (w workspace) reachable(name string) bool {
	return within(name, w.root) || within(name, scratchDirectory) || within(name, processFileDirectory) || isDevice(name)
}

// maximumBufferedFileBytes caps one file a script writes through a redirect
// or a command, so a runaway loop cannot exhaust memory before the timeout.
const maximumBufferedFileBytes = 64 << 20

// describe turns a storage error into the GNU wording; unknown errors keep
// their text.
func describe(err error) error {
	switch {
	case errors.Is(err, vfs.ErrFileNotFound), errors.Is(err, vfs.ErrPathInvalid):
		return errNotFound
	case errors.Is(err, vfs.ErrReadOnly):
		return errReadOnly
	case errors.Is(err, vfs.ErrFileTooLarge):
		return errTooLarge
	}
	var known fileError
	if errors.As(err, &known) {
		return known
	}
	return err
}

// entryKind is what a workspace path names.
type entryKind int

const (
	entryMissing entryKind = iota
	entryFile
	entryDirectory
	entryDevice
)

// entry is the result of a stat: the kind and, for a file, its metadata.
type entry struct {
	kind     entryKind
	metadata vfs.FileMetadata
}

// isDevice reports whether name is one of the /dev paths the shell provides;
// every other /dev path is missing.
func isDevice(name string) bool {
	switch name {
	case "dev/null", "dev/stdin", "dev/stdout", "dev/stderr":
		return true
	}
	return false
}

// directorySet holds the directories a script created with mkdir. A Storage
// only stores files, so a directory exists if a file lies below it or if it
// is in this set. It lives as long as the Shell and is safe for concurrent
// use.
type directorySet struct {
	mutex sync.Mutex
	paths map[string]bool
}

func newDirectorySet() *directorySet {
	return &directorySet{paths: map[string]bool{}}
}

func (d *directorySet) add(directory string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.paths[directory] = true
}

// removeTree forgets directory and every directory below it.
func (d *directorySet) removeTree(directory string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	for known := range d.paths {
		if within(known, directory) {
			delete(d.paths, known)
		}
	}
}

// list returns the known directories, sorted.
func (d *directorySet) list() []string {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	directories := make([]string, 0, len(d.paths))
	for directory := range d.paths {
		directories = append(directories, directory)
	}
	slices.Sort(directories)
	return directories
}

// within reports whether name is directory or lies below it; every path lies
// within the root "".
func within(name, directory string) bool {
	return directory == "" || name == directory || strings.HasPrefix(name, directory+"/")
}

// workspace is the file view the built-in commands work on: the call's
// Storage plus the shell's created directories.
type workspace struct {
	storage vfs.Storage
	// root is the mount point of the workspace in storage: "" for "/".
	root        string
	directories *directorySet
}

// workspaceOf returns the view of an invocation; outside a Shell it has no
// created directories.
func workspaceOf(invocation *Invocation) workspace {
	if invocation.session != nil {
		return workspace{storage: invocation.Storage, root: invocation.session.root, directories: invocation.session.directories}
	}
	return workspace{storage: invocation.Storage, directories: newDirectorySet()}
}

// stat reports what name (a workspace path) is. A missing path is
// entryMissing with a nil error; the error is for a failing Storage.
func (w workspace) stat(ctx context.Context, name string) (entry, error) {
	if !w.reachable(name) {
		return entry{kind: entryMissing}, nil
	}
	if w.isMountPoint(name) {
		return entry{kind: entryDirectory}, nil
	}
	if isDevice(name) {
		return entry{kind: entryDevice}, nil
	}
	if err := vfs.ValidatePath(name); err != nil {
		return entry{kind: entryMissing}, nil //nolint:nilerr // an invalid path names nothing: missing, not a failure
	}
	_, metadata, err := w.storage.ReadFile(ctx, name)
	switch {
	case err == nil:
		return entry{kind: entryFile, metadata: metadata}, nil
	case !isNotFile(err):
		return entry{}, err
	}
	isDirectory, err := w.isDirectory(ctx, name)
	if err != nil || !isDirectory {
		return entry{kind: entryMissing}, err
	}
	return entry{kind: entryDirectory}, nil
}

// isNotFile reports whether a ReadFile error means that no file lies at the
// path, which may still be a directory: not found, or invalid as a file path
// (vfs/mount refuses a path equal to a mount prefix).
func isNotFile(err error) bool {
	return errors.Is(err, vfs.ErrFileNotFound) || errors.Is(err, vfs.ErrPathInvalid)
}

// isDirectory reports whether a file or a created directory lies at or below
// name.
func (w workspace) isDirectory(ctx context.Context, name string) (bool, error) {
	for _, directory := range w.directories.list() {
		if within(directory, name) {
			return true, nil
		}
	}
	files, err := w.storage.ListFiles(ctx)
	if err != nil {
		return false, err
	}
	for _, file := range files {
		if strings.HasPrefix(file.Path, name+"/") {
			return true, nil
		}
	}
	return false, nil
}

// child is one entry directly inside a directory.
type child struct {
	name string // the last path segment
	entry
}

// children lists the entries directly inside directory, sorted by name
// (byte order, like GNU tools in the C locale).
func (w workspace) children(ctx context.Context, directory string) ([]child, error) {
	files, err := w.storage.ListFiles(ctx)
	if err != nil {
		return nil, err
	}
	found := map[string]child{}
	add := func(name string, value entry) {
		known, seen := found[name]
		switch {
		case !seen || value.kind == entryFile:
			found[name] = child{name: name, entry: value}
		case known.kind == entryDirectory && value.metadata.UpdatedAt.After(known.metadata.UpdatedAt):
			known.metadata.UpdatedAt = value.metadata.UpdatedAt
			found[name] = known
		}
	}
	for _, file := range files {
		if rest, ok := relativeTo(file.Path, directory); ok {
			if name, _, nested := strings.Cut(rest, "/"); nested {
				// A directory shows the time of the newest file below it.
				add(name, entry{kind: entryDirectory, metadata: vfs.FileMetadata{UpdatedAt: file.UpdatedAt}})
			} else {
				add(name, entry{kind: entryFile, metadata: file})
			}
		}
	}
	for _, known := range w.directories.list() {
		if rest, ok := relativeTo(known, directory); ok {
			name, _, _ := strings.Cut(rest, "/")
			add(name, entry{kind: entryDirectory})
		}
	}
	listed := make([]child, 0, len(found))
	for _, value := range found {
		listed = append(listed, value)
	}
	slices.SortFunc(listed, func(left, right child) int { return strings.Compare(left.name, right.name) })
	return listed, nil
}

// index lists the entries of every directory in one pass over the Storage:
// a directory's workspace path maps to its children, sorted by name. find
// walks it instead of listing the Storage once per directory.
func (w workspace) index(ctx context.Context) (map[string][]child, error) {
	files, err := w.storage.ListFiles(ctx)
	if err != nil {
		return nil, err
	}
	found := map[string]map[string]entry{}
	add := func(directory, name string, value entry) {
		if found[directory] == nil {
			found[directory] = map[string]entry{}
		}
		if known, seen := found[directory][name]; !seen || known.kind != entryFile {
			found[directory][name] = value
		}
	}
	addWithAncestors := func(directory string) {
		for directory != "" {
			add(parentOf(directory), path.Base(directory), entry{kind: entryDirectory})
			directory = parentOf(directory)
		}
	}
	for _, file := range files {
		addWithAncestors(parentOf(file.Path))
		add(parentOf(file.Path), path.Base(file.Path), entry{kind: entryFile, metadata: file})
	}
	for _, directory := range w.directories.list() {
		addWithAncestors(directory)
	}
	index := make(map[string][]child, len(found))
	for directory, entries := range found {
		listed := make([]child, 0, len(entries))
		for name, value := range entries {
			listed = append(listed, child{name: name, entry: value})
		}
		slices.SortFunc(listed, func(left, right child) int { return strings.Compare(left.name, right.name) })
		index[directory] = listed
	}
	return index, nil
}

// relativeTo returns name relative to directory when it lies strictly below
// it.
func relativeTo(name, directory string) (string, bool) {
	if directory == "" {
		return name, name != ""
	}
	rest, ok := strings.CutPrefix(name, directory+"/")
	return rest, ok && rest != ""
}

// filesBelow returns the files at or below directory, sorted by path.
func (w workspace) filesBelow(ctx context.Context, directory string) ([]vfs.FileMetadata, error) {
	files, err := w.storage.ListFiles(ctx)
	if err != nil {
		return nil, err
	}
	below := []vfs.FileMetadata{}
	for _, file := range files {
		if within(file.Path, directory) {
			below = append(below, file)
		}
	}
	return below, nil
}

// checkWritable returns nil when a file may be written at name: name is not
// a directory or device and its parent is an existing directory.
func (w workspace) checkWritable(ctx context.Context, name string) error {
	if name == "" {
		return errIsDirectory
	}
	current, err := w.stat(ctx, name)
	if err != nil {
		return err
	}
	switch current.kind {
	case entryDirectory:
		return errIsDirectory
	case entryDevice:
		return errUnsupported
	case entryMissing, entryFile:
	}
	parent, err := w.stat(ctx, parentOf(name))
	if err != nil {
		return err
	}
	switch parent.kind {
	case entryDirectory:
		return nil
	case entryFile:
		return errNotDirectory
	case entryMissing, entryDevice:
	}
	return errNotFound
}

// writeFile writes content to name after checkWritable, with the content
// type the extension implies.
func (w workspace) writeFile(ctx context.Context, name string, content []byte) error {
	if err := w.checkWritable(ctx, name); err != nil {
		return err
	}
	if len(content) > maximumBufferedFileBytes {
		return errTooLarge
	}
	return w.storage.WriteFile(ctx, name, content, contentTypeFor(name))
}

// contentTypeFor is the content type the shell writes a file with.
func contentTypeFor(name string) string {
	if contentType := vfs.ContentTypeFor(name); contentType != "" {
		return contentType
	}
	return "text/plain"
}

// parentOf returns the directory of a workspace path, "" for the root.
func parentOf(name string) string {
	parent := path.Dir(name)
	if parent == "." {
		return ""
	}
	return parent
}

// fileInfo is the fs.FileInfo of a workspace entry for the interpreter's
// stat and glob handlers.
type fileInfo struct {
	name string
	entry
}

func (i fileInfo) Name() string { return i.name }

func (i fileInfo) Size() int64 { return i.metadata.Size }

func (i fileInfo) Mode() fs.FileMode {
	switch i.kind {
	case entryDirectory:
		return fs.ModeDir | 0o755
	case entryDevice:
		return fs.ModeDevice | fs.ModeCharDevice | 0o666
	case entryMissing, entryFile:
	}
	return 0o644
}

func (i fileInfo) ModTime() time.Time { return i.metadata.UpdatedAt }

func (i fileInfo) IsDir() bool { return i.kind == entryDirectory }

func (i fileInfo) Sys() any { return ownerStat() }
