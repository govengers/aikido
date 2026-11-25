package shell

import (
	"context"
	"fmt"

	"github.com/govengers/aikido/vfs"
)

// hiddenFilter is the workspace with Options.HideHiddenPaths: the files
// hidden says are hidden are left out of listings, and
// reading, writing or deleting them fails as if they did not exist. Every
// file access of the shell (redirects, globs, ls, find, grep -r, the text
// commands) goes through it.
type hiddenFilter struct {
	vfs.Storage
	hidden func(path string) bool
}

func (h hiddenFilter) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	files, err := h.Storage.ListFiles(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // the workspace's errors keep their vfs meaning for the shell's GNU wording
	}
	visible := files[:0:0]
	for _, file := range files {
		if !h.hidden(file.Path) {
			visible = append(visible, file)
		}
	}
	return visible, nil
}

func (h hiddenFilter) ReadFile(ctx context.Context, name string) ([]byte, vfs.FileMetadata, error) {
	if h.hidden(name) {
		return nil, vfs.FileMetadata{}, fmt.Errorf("%w: %s is hidden", vfs.ErrFileNotFound, name)
	}
	return h.Storage.ReadFile(ctx, name) //nolint:wrapcheck // see ListFiles
}

func (h hiddenFilter) WriteFile(ctx context.Context, name string, content []byte, contentType string) error {
	if h.hidden(name) {
		return fmt.Errorf("%w: %s is hidden", vfs.ErrPathInvalid, name)
	}
	return h.Storage.WriteFile(ctx, name, content, contentType) //nolint:wrapcheck // see ListFiles
}

func (h hiddenFilter) DeleteFile(ctx context.Context, name string) error {
	if h.hidden(name) {
		return fmt.Errorf("%w: %s is hidden", vfs.ErrFileNotFound, name)
	}
	return h.Storage.DeleteFile(ctx, name) //nolint:wrapcheck // see ListFiles
}
