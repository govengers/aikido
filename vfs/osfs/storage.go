// Package osfs implements vfs.Storage over a directory on the local disk.
//
// All access goes through an os.Root, so paths (including symlinks) cannot
// resolve outside the directory. Writes are atomic: content goes to a temp
// file in the target directory, which is then renamed over the destination.
// ListFiles reports regular files only and skips any `.git` directory.
//
// The content type is not stored on disk; ReadFile and ListFiles infer it from
// the extension (vfs.ContentTypeFor, "application/octet-stream" otherwise).
package osfs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/govengers/aikido/vfs"
)

// Storage is the local-disk implementation. It is safe for concurrent use;
// Close it when done.
type Storage struct {
	root           *os.Root
	refuseSymlinks bool
}

var _ vfs.Storage = (*Storage)(nil)

// Options configure a Storage.
type Options struct {
	// Directory is the storage root, required. It must exist; nothing outside it
	// is reachable, symlinks included (os.Root).
	Directory string
	// RefuseSymlinks makes a path that passes through a symbolic link, at
	// the final name or at a parent directory, unreachable: ReadFile and
	// DeleteFile return vfs.ErrFileNotFound, WriteFile vfs.ErrPathInvalid.
	// Set it under a hidden-path policy that checks the spelled path
	// (agent.VFSToolOptions.HideHiddenPaths, shell.Options.HideHiddenPaths):
	// a visible link such as public -> .env would otherwise read or write
	// the hidden target. ListFiles never lists links either way.
	RefuseSymlinks bool
}

// New opens options.Directory as the storage root. It returns an error if
// options is nil, Directory is empty or the directory cannot be opened. Close
// the Storage when done.
func New(options *Options) (*Storage, error) {
	if options == nil || options.Directory == "" {
		return nil, errors.New("aikido/vfs/osfs: Directory is required")
	}
	root, err := os.OpenRoot(options.Directory)
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/osfs: open root %q: %w", options.Directory, err)
	}
	return &Storage{root: root, refuseSymlinks: options.RefuseSymlinks}, nil
}

// Close releases the root directory handle. The Storage must not be used
// afterwards.
func (s *Storage) Close() error { return s.root.Close() }

// ListFiles walks the root and returns every regular file sorted by path,
// skipping .git directories. An empty root yields an empty, non-nil slice.
func (s *Storage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	files := []vfs.FileMetadata{}
	err := fs.WalkDir(s.root.FS(), ".", func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, metadataFor(filePath, info))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/osfs: list: %w", err)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

// ReadFile returns the content and metadata of filePath. It returns an error
// matching vfs.ErrPathInvalid for an invalid path or one that resolves outside
// the root, and vfs.ErrFileNotFound for a missing file or anything that is not
// a regular file.
func (s *Storage) ReadFile(ctx context.Context, filePath string) ([]byte, vfs.FileMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	if err := vfs.ValidatePath(filePath); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	if s.throughSymlink(filePath) {
		return nil, vfs.FileMetadata{}, fmt.Errorf("%w: %s passes through a symbolic link", vfs.ErrFileNotFound, filePath)
	}
	info, err := s.regularFile(filePath)
	if err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	content, err := s.root.ReadFile(filePath)
	if err != nil {
		return nil, vfs.FileMetadata{}, mapError(filePath, err)
	}
	metadata := metadataFor(filePath, info)
	metadata.Size = int64(len(content))
	return content, metadata, nil
}

// WriteFile atomically replaces filePath with content, creating parent
// directories as needed. A replaced file keeps its permission bits; a new
// file gets 0644 (not reduced by the umask). The content type is ignored (see
// the package comment). It returns an error matching vfs.ErrPathInvalid for
// an invalid path.
func (s *Storage) WriteFile(ctx context.Context, filePath string, content []byte, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(filePath); err != nil {
		return err
	}
	if s.throughSymlink(filePath) {
		return fmt.Errorf("%w: %s passes through a symbolic link", vfs.ErrPathInvalid, filePath)
	}
	directory := path.Dir(filePath)
	if directory != "." {
		if err := s.root.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("aikido/vfs/osfs: mkdir %q: %w", directory, err)
		}
	}
	temporaryPath := path.Join(directory, "."+path.Base(filePath)+".tmp-"+rand.Text())
	if err := s.writeNewFile(temporaryPath, content, s.modeFor(filePath)); err != nil {
		return fmt.Errorf("aikido/vfs/osfs: write %q: %w", filePath, err)
	}
	if err := s.root.Rename(temporaryPath, filePath); err != nil {
		_ = s.root.Remove(temporaryPath) // best-effort cleanup; the rename error is what the caller needs
		return fmt.Errorf("aikido/vfs/osfs: write %q: %w", filePath, err)
	}
	return nil
}

// DeleteFile removes filePath. It returns an error matching vfs.ErrPathInvalid
// for an invalid path and vfs.ErrFileNotFound for a missing file or anything
// that is not a regular file.
func (s *Storage) DeleteFile(ctx context.Context, filePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(filePath); err != nil {
		return err
	}
	if s.throughSymlink(filePath) {
		return fmt.Errorf("%w: %s passes through a symbolic link", vfs.ErrFileNotFound, filePath)
	}
	if _, err := s.regularFile(filePath); err != nil {
		return err
	}
	if err := s.root.Remove(filePath); err != nil {
		return mapError(filePath, err)
	}
	return nil
}

// newFileMode is the permission of a file WriteFile creates.
const newFileMode fs.FileMode = 0o644

// modeFor returns the permission bits WriteFile gives filePath: those of the
// regular file it replaces, newFileMode for anything else.
func (s *Storage) modeFor(filePath string) fs.FileMode {
	info, err := s.root.Stat(filePath)
	if err != nil || !info.Mode().IsRegular() {
		return newFileMode
	}
	return info.Mode().Perm()
}

// writeNewFile creates filePath, which must not exist yet, owner-only, writes
// content and then sets mode, so no other user can open it in between; on
// failure it removes the partial file.
func (s *Storage) writeNewFile(filePath string, content []byte, mode fs.FileMode) error {
	file, err := s.root.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeError := file.Write(content)
	if writeError == nil {
		writeError = file.Chmod(mode)
	}
	if err := errors.Join(writeError, file.Close()); err != nil {
		_ = s.root.Remove(filePath) // best-effort cleanup; the write error is what the caller needs
		return err
	}
	return nil
}

// throughSymlink reports, with RefuseSymlinks, whether filePath or one of
// its parent directories is a symbolic link; the check ends at the first
// component that does not exist.
func (s *Storage) throughSymlink(filePath string) bool {
	if !s.refuseSymlinks {
		return false
	}
	current := ""
	for segment := range strings.SplitSeq(filePath, "/") {
		current = path.Join(current, segment)
		info, err := s.root.Lstat(current)
		if err != nil {
			return false
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// regularFile stats filePath (following symlinks inside the root) and reports
// anything that is not a regular file as not found.
func (s *Storage) regularFile(filePath string) (fs.FileInfo, error) {
	info, err := s.root.Stat(filePath)
	if err != nil {
		return nil, mapError(filePath, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", vfs.ErrFileNotFound, filePath)
	}
	return info, nil
}

func metadataFor(filePath string, info fs.FileInfo) vfs.FileMetadata {
	contentType := vfs.ContentTypeFor(filePath)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return vfs.FileMetadata{Path: filePath, ContentType: contentType, Size: info.Size(), UpdatedAt: info.ModTime()}
}

// mapError turns not-exist into vfs.ErrFileNotFound and a root escape into
// vfs.ErrPathInvalid; anything else is wrapped as is.
func mapError(filePath string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", vfs.ErrFileNotFound, filePath)
	}
	if escapesRoot(err) {
		return fmt.Errorf("%w: %s escapes the storage root", vfs.ErrPathInvalid, filePath)
	}
	return fmt.Errorf("aikido/vfs/osfs: %s: %w", filePath, err)
}

// escapesRoot reports whether err is os.Root's refusal to leave the root. The
// os package keeps that error unexported (errPathEscapes), so its message is
// the only handle; TestSymlinkEscapeRejected fails if a Go release changes it.
func escapesRoot(err error) bool {
	var pathError *fs.PathError
	return errors.As(err, &pathError) && pathError.Err.Error() == "path escapes from parent"
}
