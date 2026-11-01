package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/govengers/aikido/vfs"
	vfsmem "github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/mount"
	"github.com/govengers/aikido/vfs/osfs"
	"github.com/govengers/aikido/vfs/overlay"
)

// readOnlySuffix marks a --mount as read-only: docs=../docs:ro.
const readOnlySuffix = ":ro"

// mountSpec is one parsed --mount flag.
type mountSpec struct {
	name      string
	directory string
	readOnly  bool
}

// parseMountFlag reads "name=path" or "name=path:ro".
func parseMountFlag(value string) (mountSpec, error) {
	name, directory, found := strings.Cut(value, "=")
	if !found || name == "" || directory == "" {
		return mountSpec{}, fmt.Errorf("--mount %q: want name=path or name=path:ro", value)
	}
	directory, readOnly := strings.CutSuffix(directory, readOnlySuffix)
	if directory == "" {
		return mountSpec{}, fmt.Errorf("--mount %q: path is empty", value)
	}
	return mountSpec{name: name, directory: directory, readOnly: readOnly}, nil
}

func parseMountFlags(values []string) ([]mountSpec, error) {
	specs := make([]mountSpec, 0, len(values))
	for _, value := range values {
		spec, err := parseMountFlag(value)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// workspace is the storage the agent works on. Without --dir and --mount it
// is the in-memory store of old; every directory sits behind an overlay, so
// nothing reaches the disk unless the run ends with --apply.
type workspace struct {
	direct bool // directories are used without an overlay
	// refuseSymlinks keeps the disk from following links, under
	// --hide-hidden: a visible link could reach a hidden file.
	refuseSymlinks bool
	storage        vfs.Storage
	memory         *vfsmem.Storage // the in-memory root without --dir, else nil
	overlays       []directoryOverlay
	disks          []*osfs.Storage
}

// directoryOverlay is one directory's overlay and where the agent sees it.
type directoryOverlay struct {
	prefix    string
	directory string
	overlay   *overlay.Storage
}

// openWorkspace opens --dir and the --mount directories, each behind an
// overlay, or directly on disk with direct; with refuseSymlinks no path
// through a symbolic link is reachable.
func openWorkspace(directory string, mounts []mountSpec, direct, refuseSymlinks bool) (*workspace, error) {
	opened := &workspace{direct: direct, refuseSymlinks: refuseSymlinks}
	root, err := opened.openRoot(directory)
	if err != nil {
		return nil, err
	}
	if len(mounts) == 0 {
		opened.storage = root
		return opened, nil
	}
	if opened.storage, err = opened.mountAll(root, mounts); err != nil {
		opened.close()
		return nil, err
	}
	return opened, nil
}

func (w *workspace) openRoot(directory string) (vfs.Storage, error) {
	if directory == "" {
		w.memory = vfsmem.New()
		return w.memory, nil
	}
	return w.openDirectory("", directory)
}

func (w *workspace) mountAll(root vfs.Storage, specs []mountSpec) (vfs.Storage, error) {
	mounts := []mount.Mount{{Prefix: "", Storage: root}}
	for _, spec := range specs {
		storage, err := w.openDirectory(spec.name, spec.directory)
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, mount.Mount{Prefix: spec.name, Storage: storage, ReadOnly: spec.readOnly})
	}
	storage, err := mount.New(&mount.Options{Mounts: mounts})
	if err != nil {
		return nil, fmt.Errorf("--mount: %w", err)
	}
	return storage, nil
}

// openDirectory opens directory on disk, behind an overlay unless the workspace
// is direct. Read-only mounts get an overlay too:
// it never holds a change, but gives them the same search as the rest.
func (w *workspace) openDirectory(prefix, directory string) (vfs.Storage, error) {
	disk, err := osfs.New(&osfs.Options{Directory: directory, RefuseSymlinks: w.refuseSymlinks})
	if err != nil {
		return nil, err
	}
	w.disks = append(w.disks, disk)
	if w.direct {
		return disk, nil
	}
	layer, err := overlay.New(&overlay.Options{Base: disk})
	if err != nil {
		return nil, err
	}
	w.overlays = append(w.overlays, directoryOverlay{prefix: prefix, directory: directory, overlay: layer})
	return layer, nil
}

// close releases the directory handles. Nothing is buffered in them, so a
// failing close loses no data and is not reported.
func (w *workspace) close() {
	for _, disk := range w.disks {
		_ = disk.Close()
	}
}

// report prints what the run left behind: the in-memory files, or the
// directories' changes as a list and a unified diff, which apply then writes.
// A refused --apply is an error even when there is nothing to write.
func (w *workspace) report(ctx context.Context, out io.Writer, decision commitDecision) error {
	if w.memory != nil {
		// memory.Storage fails only on a done ctx, and then the run was
		// interrupted and there is no final state worth printing.
		if files, err := w.memory.ListFiles(ctx); err == nil {
			printWorkspace(out, files)
		}
	}
	changes, err := w.changes(ctx)
	if err != nil {
		return err
	}
	if len(w.overlays) > 0 {
		printChanges(out, changes)
	}
	switch {
	case decision.refusedReason != "":
		fmt.Fprintf(out, "\nnot applied: the run ended with %s, nothing written\n", decision.refusedReason)
		return fmt.Errorf("--apply refused: the run ended with %s", decision.refusedReason)
	case len(changes) == 0:
		return nil
	case !decision.apply:
		fmt.Fprintln(out, "\ndry run: nothing written; run again with --apply to write the changes")
		return nil
	default:
		return w.commit(ctx, out)
	}
}

// changes lists every directory's changes with the paths the agent saw.
func (w *workspace) changes(ctx context.Context) ([]overlay.Change, error) {
	var all []overlay.Change
	for _, layer := range w.overlays {
		changes, err := layer.overlay.Changes(ctx)
		if err != nil {
			return nil, fmt.Errorf("changes in %s: %w", layer.directory, err)
		}
		for _, change := range changes {
			if layer.prefix != "" {
				change.Path = layer.prefix + "/" + change.Path
			}
			all = append(all, change)
		}
	}
	return all, nil
}

func (w *workspace) commit(ctx context.Context, out io.Writer) error {
	var failures []error
	for _, layer := range w.overlays {
		if err := layer.overlay.Commit(ctx); err != nil {
			failures = append(failures, fmt.Errorf("apply to %s: %w", layer.directory, err))
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	fmt.Fprintln(out, "\napplied the changes")
	return nil
}

func printChanges(out io.Writer, changes []overlay.Change) {
	if len(changes) == 0 {
		fmt.Fprintln(out, "\n--- no changes ---")
		return
	}
	fmt.Fprintln(out, "\n--- changes ---")
	for _, change := range changes {
		fmt.Fprintf(out, "%-6s %s\n", change.Kind, change.Path)
	}
	fmt.Fprintln(out, "\n--- diff ---")
	fmt.Fprint(out, overlay.UnifiedDiff(changes))
}
