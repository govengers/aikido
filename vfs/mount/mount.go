// Package mount combines several vfs.Storage backends into one by path
// prefix, so an agent sees, say, a writable workspace next to read-only
// reference documents:
//
//	storage, err := mount.New(&mount.Options{Mounts: []mount.Mount{
//		{Prefix: "", Storage: workspace},
//		{Prefix: "docs", Storage: docs, ReadOnly: true},
//	}})
//
// A path belongs to the mount with the longest prefix that equals it or is
// followed by a slash in it ("docs/a.md" goes to "docs", "docsy.md" to the
// root mount); the mount's storage sees the path without its prefix. Files of
// an outer mount that fall under an inner mount's prefix are hidden, so
// every path has exactly one owner.
package mount

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/govengers/aikido/vfs"
)

// Mount attaches one storage at a path prefix.
type Mount struct {
	// Prefix is where Storage appears: "" mounts it at the root, anything
	// else must be a valid vfs path (see vfs.ValidatePath), such as "docs"
	// or "reference/api".
	Prefix string
	// Storage serves the paths under Prefix, required.
	Storage vfs.Storage
	// ReadOnly rejects writes and deletes under Prefix with vfs.ErrReadOnly,
	// whatever Storage supports.
	ReadOnly bool
}

// Options configure the combined storage.
type Options struct {
	// Mounts lists at least one mount; no two may share a prefix. New copies
	// the slice.
	Mounts []Mount
}

// storage routes every call to the mount that owns the path. It is immutable
// after New, so it is as safe for concurrent use as its mounts are.
type storage struct {
	mounts []Mount // longest prefix first
}

// searchableStorage adds vfs.Searchable when at least one mount supports it.
type searchableStorage struct {
	*storage
}

var (
	_ vfs.Storage    = (*storage)(nil)
	_ vfs.Searchable = (*searchableStorage)(nil)
)

// New returns one vfs.Storage over options.Mounts. The result implements
// vfs.Searchable exactly when at least one mount's storage does, so
// agent.RegisterVFSTools offers the search tool only when it can find
// something; that is why New returns the interface and not a concrete type.
//
// It returns an error for nil options, no mounts, a nil Storage, an invalid
// prefix or two mounts with the same prefix. Nested prefixes ("docs" and
// "docs/api") are allowed; the longer one wins.
//
// A path no mount owns, or a path equal to a mount's prefix, is rejected
// with an error matching vfs.ErrPathInvalid. Errors of a mount's storage are
// returned wrapped with the full path.
func New(options *Options) (vfs.Storage, error) {
	if options == nil || len(options.Mounts) == 0 {
		return nil, errors.New("aikido/vfs/mount: at least one Mount is required")
	}
	mounts := slices.Clone(options.Mounts)
	searchable := false
	seen := make(map[string]bool, len(mounts))
	for _, mount := range mounts {
		if err := validateMount(mount, seen); err != nil {
			return nil, err
		}
		seen[mount.Prefix] = true
		_, isSearchable := mount.Storage.(vfs.Searchable)
		searchable = searchable || isSearchable
	}
	slices.SortStableFunc(mounts, func(left, right Mount) int { return len(right.Prefix) - len(left.Prefix) })
	combined := &storage{mounts: mounts}
	if searchable {
		return &searchableStorage{combined}, nil
	}
	return combined, nil
}

func validateMount(mount Mount, seen map[string]bool) error {
	if mount.Storage == nil {
		return fmt.Errorf("aikido/vfs/mount: mount %q has no Storage", mount.Prefix)
	}
	if mount.Prefix != "" {
		if err := vfs.ValidatePath(mount.Prefix); err != nil {
			return fmt.Errorf("aikido/vfs/mount: prefix %q: %w", mount.Prefix, err)
		}
	}
	if seen[mount.Prefix] {
		return fmt.Errorf("aikido/vfs/mount: prefix %q is mounted twice", mount.Prefix)
	}
	return nil
}

// ListFiles returns the files of every mount with their full paths, sorted by
// path, without the files an inner mount hides.
func (s *storage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	files := []vfs.FileMetadata{}
	for index, mount := range s.mounts {
		mountFiles, err := mount.Storage.ListFiles(ctx)
		if err != nil {
			return nil, fmt.Errorf("aikido/vfs/mount: list %q: %w", mount.Prefix, err)
		}
		for _, file := range mountFiles {
			file.Path = join(mount.Prefix, file.Path)
			if s.owner(file.Path) == index {
				files = append(files, file)
			}
		}
	}
	slices.SortFunc(files, func(left, right vfs.FileMetadata) int { return strings.Compare(left.Path, right.Path) })
	return files, nil
}

// ReadFile reads path from the mount that owns it; the metadata carries the
// full path.
func (s *storage) ReadFile(ctx context.Context, path string) ([]byte, vfs.FileMetadata, error) {
	mount, innerPath, err := s.route(path)
	if err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	content, metadata, err := mount.Storage.ReadFile(ctx, innerPath)
	if err != nil {
		return nil, vfs.FileMetadata{}, fmt.Errorf("aikido/vfs/mount: read %s: %w", path, err)
	}
	metadata.Path = path
	return content, metadata, nil
}

// WriteFile writes path to the mount that owns it; a ReadOnly mount returns
// an error matching vfs.ErrReadOnly.
func (s *storage) WriteFile(ctx context.Context, path string, content []byte, contentType string) error {
	mount, innerPath, err := s.writableRoute(path)
	if err != nil {
		return err
	}
	if err := mount.Storage.WriteFile(ctx, innerPath, content, contentType); err != nil {
		return fmt.Errorf("aikido/vfs/mount: write %s: %w", path, err)
	}
	return nil
}

// DeleteFile deletes path from the mount that owns it; a ReadOnly mount
// returns an error matching vfs.ErrReadOnly.
func (s *storage) DeleteFile(ctx context.Context, path string) error {
	mount, innerPath, err := s.writableRoute(path)
	if err != nil {
		return err
	}
	if err := mount.Storage.DeleteFile(ctx, innerPath); err != nil {
		return fmt.Errorf("aikido/vfs/mount: delete %s: %w", path, err)
	}
	return nil
}

// Search asks every searchable mount and returns the full paths of the
// matches, sorted, without the files an inner mount hides. Mounts that are
// not searchable are skipped.
func (s *searchableStorage) Search(ctx context.Context, query string) ([]string, error) {
	paths := []string{}
	for index, mount := range s.mounts {
		search, ok := mount.Storage.(vfs.Searchable)
		if !ok {
			continue
		}
		matches, err := search.Search(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("aikido/vfs/mount: search %q: %w", mount.Prefix, err)
		}
		for _, match := range matches {
			if path := join(mount.Prefix, match); s.owner(path) == index {
				paths = append(paths, path)
			}
		}
	}
	slices.Sort(paths)
	return paths, nil
}

// SearchSyntax returns the syntax of the searchable mounts: the shared text
// when they all agree, one line per mount otherwise.
func (s *searchableStorage) SearchSyntax() string {
	var syntaxes, lines []string
	for _, mount := range s.mounts {
		search, ok := mount.Storage.(vfs.Searchable)
		if !ok {
			continue
		}
		syntax := search.SearchSyntax()
		syntaxes = append(syntaxes, syntax)
		lines = append(lines, fmt.Sprintf("Files under %q: %s", mount.Prefix, syntax))
	}
	if len(slices.Compact(slices.Clone(syntaxes))) == 1 {
		return syntaxes[0]
	}
	return "The query goes to every searchable mount; each reads it in its own syntax.\n" + strings.Join(lines, "\n")
}

// route validates path and returns its mount and the path inside it.
func (s *storage) route(path string) (Mount, string, error) {
	if err := vfs.ValidatePath(path); err != nil {
		return Mount{}, "", err
	}
	index := s.owner(path)
	if index < 0 {
		return Mount{}, "", fmt.Errorf("%w: no mount holds %s", vfs.ErrPathInvalid, path)
	}
	mount := s.mounts[index]
	if mount.Prefix == path {
		return Mount{}, "", fmt.Errorf("%w: %s is a mount point, not a file", vfs.ErrPathInvalid, path)
	}
	if mount.Prefix == "" {
		return mount, path, nil
	}
	return mount, path[len(mount.Prefix)+1:], nil
}

func (s *storage) writableRoute(path string) (Mount, string, error) {
	mount, innerPath, err := s.route(path)
	if err != nil {
		return Mount{}, "", err
	}
	if mount.ReadOnly {
		return Mount{}, "", fmt.Errorf("%w: %s is mounted read-only", vfs.ErrReadOnly, path)
	}
	return mount, innerPath, nil
}

// owner returns the index of the mount with the longest prefix covering
// path, or -1 when none does. Mounts are sorted longest prefix first.
func (s *storage) owner(path string) int {
	for index, mount := range s.mounts {
		if covers(mount.Prefix, path) {
			return index
		}
	}
	return -1
}

// covers reports whether prefix owns path: the root prefix owns everything,
// any other prefix owns itself and the paths below it.
func covers(prefix, path string) bool {
	return prefix == "" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

func join(prefix, path string) string {
	if prefix == "" {
		return path
	}
	return prefix + "/" + path
}
