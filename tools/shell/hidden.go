package shell

import (
	"context"
	"fmt"
	"strings"

	"github.com/govengers/aikido/vfs"
)

// hiddenFilter is the workspace with Options.HideHiddenPaths: files with a
// path segment that starts with "." or "_" are left out of listings, and
// reading, writing or deleting them fails as if they did not exist. Every
// file access of the shell (redirects, globs, ls, find, grep -r, the text
// commands) goes through it.
type hiddenFilter struct {
	vfs.Storage
}

func (h hiddenFilter) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	files, err := h.Storage.ListFiles(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // the workspace's errors keep their vfs meaning for the shell's GNU wording
	}
	visible := files[:0:0]
	for _, file := range files {
		if !isHiddenPath(file.Path) {
			visible = append(visible, file)
		}
	}
	return visible, nil
}

func (h hiddenFilter) ReadFile(ctx context.Context, name string) ([]byte, vfs.FileMetadata, error) {
	if isHiddenPath(name) {
		return nil, vfs.FileMetadata{}, fmt.Errorf("%w: %s is hidden", vfs.ErrFileNotFound, name)
	}
	return h.Storage.ReadFile(ctx, name) //nolint:wrapcheck // see ListFiles
}

func (h hiddenFilter) WriteFile(ctx context.Context, name string, content []byte, contentType string) error {
	if isHiddenPath(name) {
		return fmt.Errorf("%w: %s is hidden", vfs.ErrPathInvalid, name)
	}
	return h.Storage.WriteFile(ctx, name, content, contentType) //nolint:wrapcheck // see ListFiles
}

func (h hiddenFilter) DeleteFile(ctx context.Context, name string) error {
	if isHiddenPath(name) {
		return fmt.Errorf("%w: %s is hidden", vfs.ErrFileNotFound, name)
	}
	return h.Storage.DeleteFile(ctx, name) //nolint:wrapcheck // see ListFiles
}

// isHiddenPath reports whether a workspace path has a segment starting with
// "." or "_", the rule of agent.VFSToolOptions.HideHiddenPaths.
func isHiddenPath(name string) bool {
	for segment := range strings.SplitSeq(name, "/") {
		if segment != "" && (segment[0] == '.' || segment[0] == '_') {
			return true
		}
	}
	return false
}
