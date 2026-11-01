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
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"

	"github.com/govengers/aikido/vfs"
)

// Storage is the local-disk implementation. Close it when done.
type Storage struct {
	root *os.Root
}

var _ vfs.Storage = (*Storage)(nil)

// Options configure a Storage.
type Options struct {
	// Directory is the storage root, required. It must exist; nothing outside it
	// is reachable, symlinks included (os.Root).
	Directory string
}

// New opens options.Directory as the storage root. Close the Storage when done.
func New(options *Options) (*Storage, error) {
	if options == nil || options.Directory == "" {
		return nil, errors.New("aikido/vfs/osfs: Directory is required")
	}
	root, err := os.OpenRoot(options.Directory)
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/osfs: open root %q: %w", options.Directory, err)
	}
	return &Storage{root: root}, nil
}

// Close releases the root directory handle.
func (s *Storage) Close() error { return s.root.Close() }

func (s *Storage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	var out []vfs.FileMetadata
	err := fs.WalkDir(s.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, metaFor(p, info))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/osfs: list: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if out == nil {
		out = []vfs.FileMetadata{}
	}
	return out, nil
}

func (s *Storage) ReadFile(ctx context.Context, p string) ([]byte, vfs.FileMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	if err := vfs.ValidatePath(p); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	info, err := s.regularFile(p)
	if err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	content, err := s.root.ReadFile(p)
	if err != nil {
		return nil, vfs.FileMetadata{}, mapErr(p, err)
	}
	meta := metaFor(p, info)
	meta.Size = int64(len(content))
	return content, meta, nil
}

func (s *Storage) WriteFile(ctx context.Context, p string, content []byte, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(p); err != nil {
		return err
	}
	dir := path.Dir(p)
	if dir != "." {
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("aikido/vfs/osfs: mkdir %q: %w", dir, err)
		}
	}
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	tmp := path.Join(dir, "."+path.Base(p)+".tmp-"+hex.EncodeToString(suffix[:]))
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("aikido/vfs/osfs: write %q: %w", p, err)
	}
	_, werr := f.Write(content)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = s.root.Remove(tmp)
		return fmt.Errorf("aikido/vfs/osfs: write %q: %w", p, err)
	}
	if err := s.root.Rename(tmp, p); err != nil {
		_ = s.root.Remove(tmp)
		return fmt.Errorf("aikido/vfs/osfs: write %q: %w", p, err)
	}
	return nil
}

func (s *Storage) DeleteFile(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(p); err != nil {
		return err
	}
	if _, err := s.regularFile(p); err != nil {
		return err
	}
	if err := s.root.Remove(p); err != nil {
		return mapErr(p, err)
	}
	return nil
}

// regularFile stats p (following symlinks inside the root) and reports
// anything that is not a regular file as not found.
func (s *Storage) regularFile(p string) (fs.FileInfo, error) {
	info, err := s.root.Stat(p)
	if err != nil {
		return nil, mapErr(p, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", vfs.ErrFileNotFound, p)
	}
	return info, nil
}

func metaFor(p string, info fs.FileInfo) vfs.FileMetadata {
	ct := vfs.ContentTypeFor(p)
	if ct == "" {
		ct = "application/octet-stream"
	}
	return vfs.FileMetadata{Path: p, ContentType: ct, Size: info.Size(), UpdatedAt: info.ModTime()}
}

// mapErr turns not-exist into vfs.ErrFileNotFound and a root escape into
// vfs.ErrPathInvalid; anything else is wrapped as is.
func mapErr(p string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", vfs.ErrFileNotFound, p)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Err.Error() == "path escapes from parent" {
		return fmt.Errorf("%w: %s escapes the storage root", vfs.ErrPathInvalid, p)
	}
	return fmt.Errorf("aikido/vfs/osfs: %s: %w", p, err)
}
