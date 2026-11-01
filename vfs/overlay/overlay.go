// Package overlay implements a copy-on-write vfs.Storage over any base
// vfs.Storage: a directory on disk (vfs/osfs), an embedded corpus
// (vfs/embedfs) or a caller's own backend.
//
// Reads fall through to the base unless the overlay holds a write or a
// delete for the path; writes and deletes stay in memory until Commit applies
// them to the base. Changes lists what Commit would do and UnifiedDiff renders
// it for review, so an agent can work on a real directory as a dry run:
//
//	base, _ := osfs.New(&osfs.Options{Directory: "./project"})
//	workspace, _ := overlay.New(&overlay.Options{Base: base})
//	// ... hand workspace to agent.RegisterVFSTools and run the session ...
//	changes, _ := workspace.Changes(ctx)
//	fmt.Print(overlay.UnifiedDiff(changes))
//	err := workspace.Commit(ctx) // or workspace.Discard()
package overlay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/govengers/aikido/vfs"
)

// ChangeKind says what Commit does to the base for one path.
type ChangeKind string

const (
	// ChangeCreate writes a file the base does not have.
	ChangeCreate ChangeKind = "create"
	// ChangeModify replaces a base file with different bytes.
	ChangeModify ChangeKind = "modify"
	// ChangeDelete removes a base file.
	ChangeDelete ChangeKind = "delete"
)

// Change is one pending difference between the overlay and its base.
type Change struct {
	Path string
	Kind ChangeKind
	// Content and ContentType are what Commit writes; both are empty for
	// ChangeDelete.
	Content     []byte
	ContentType string
	// Previous is the base content the change replaces or removes; nil for
	// ChangeCreate.
	Previous []byte
}

// Options configure a Storage.
type Options struct {
	// Base is the storage the overlay reads through to and commits to,
	// required. The overlay never writes to it before Commit.
	Base vfs.Storage
}

// entry is the overlay's state for one path: a written file or, when
// deleted is set, a tombstone that hides the base file.
type entry struct {
	content     []byte
	contentType string
	updatedAt   time.Time
	deleted     bool
}

// Storage is the copy-on-write overlay. It implements vfs.Storage and
// vfs.Searchable and is safe for concurrent use. Content is copied on the way
// in and out, so callers may reuse their slices.
type Storage struct {
	base    vfs.Storage
	now     func() time.Time
	mutex   sync.RWMutex
	entries map[string]entry
}

var (
	_ vfs.Storage    = (*Storage)(nil)
	_ vfs.Searchable = (*Storage)(nil)
)

// New returns an empty overlay over options.Base. It returns an error if
// options or Base is nil.
func New(options *Options) (*Storage, error) {
	if options == nil || options.Base == nil {
		return nil, errors.New("aikido/vfs/overlay: Base is required")
	}
	return &Storage{base: options.Base, now: time.Now, entries: make(map[string]entry)}, nil
}

// ListFiles returns the merged view sorted by path: the base's files without
// the deleted ones, plus the overlay's writes. Errors of the base's ListFiles
// are returned wrapped.
func (s *Storage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	// The lock spans the base listing, so a concurrent Commit cannot move a
	// file from the overlay to the base between the two snapshots.
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	baseFiles, err := s.base.ListFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/overlay: list base: %w", err)
	}
	merged := make(map[string]vfs.FileMetadata, len(baseFiles))
	for _, file := range baseFiles {
		merged[file.Path] = file
	}
	for path, current := range s.entries {
		if current.deleted {
			delete(merged, path)
		} else {
			merged[path] = current.metadata(path)
		}
	}
	files := slices.Collect(maps.Values(merged))
	slices.SortFunc(files, func(left, right vfs.FileMetadata) int { return strings.Compare(left.Path, right.Path) })
	return files, nil
}

// ReadFile returns the overlay's copy of path if it holds one, and the base's
// file otherwise. It returns an error matching vfs.ErrPathInvalid for an
// invalid path and vfs.ErrFileNotFound for a missing or deleted file; other
// base errors are returned as the base reports them.
func (s *Storage) ReadFile(ctx context.Context, path string) ([]byte, vfs.FileMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	s.mutex.RLock()
	current, ok := s.entries[path]
	s.mutex.RUnlock()
	if !ok {
		return s.base.ReadFile(ctx, path)
	}
	if current.deleted {
		return nil, vfs.FileMetadata{}, fmt.Errorf("%w: %s", vfs.ErrFileNotFound, path)
	}
	return bytes.Clone(current.content), current.metadata(path), nil
}

// WriteFile stores a copy of content under path in the overlay; the base is
// not touched. It returns an error matching vfs.ErrPathInvalid for an invalid
// path. A read-only base does not fail here but at Commit.
func (s *Storage) WriteFile(ctx context.Context, path string, content []byte, contentType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return err
	}
	stored := entry{content: bytes.Clone(content), contentType: contentType, updatedAt: s.now()}
	if stored.content == nil {
		stored.content = []byte{}
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.entries[path] = stored
	return nil
}

// DeleteFile hides path in the overlay; the base is not touched. It returns
// an error matching vfs.ErrPathInvalid for an invalid path and
// vfs.ErrFileNotFound when the merged view holds no file at path.
func (s *Storage) DeleteFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return err
	}
	// The lock spans the base read, so no write to path can slip in between
	// the existence check and the tombstone.
	s.mutex.Lock()
	defer s.mutex.Unlock()
	current, ok := s.entries[path]
	if ok && current.deleted {
		return fmt.Errorf("%w: %s", vfs.ErrFileNotFound, path)
	}
	if !ok {
		if _, _, err := s.base.ReadFile(ctx, path); err != nil {
			return err
		}
	}
	s.entries[path] = entry{deleted: true, updatedAt: s.now()}
	return nil
}

// Search returns the paths, sorted, of every file in the merged view whose
// content contains query, compared case-insensitively; an empty query matches
// every file. It reads every file of the merged view, so it costs one base
// ReadFile per base file the overlay does not hold. The base's own Search is
// not used: its syntax may differ and it does not see the overlay's writes.
func (s *Storage) Search(ctx context.Context, query string) ([]string, error) {
	files, err := s.ListFiles(ctx)
	if err != nil {
		return nil, err
	}
	lowerQuery := strings.ToLower(query)
	paths := make([]string, 0)
	for _, file := range files {
		content, _, err := s.ReadFile(ctx, file.Path)
		if errors.Is(err, vfs.ErrFileNotFound) {
			continue // deleted since ListFiles
		}
		if err != nil {
			return nil, fmt.Errorf("aikido/vfs/overlay: search %s: %w", file.Path, err)
		}
		if strings.Contains(strings.ToLower(string(content)), lowerQuery) {
			paths = append(paths, file.Path)
		}
	}
	return paths, nil
}

// SearchSyntax describes the query syntax Search accepts, for the model.
func (s *Storage) SearchSyntax() string {
	return "Case-insensitive substring search against file contents. " +
		"Pass a single substring; the backend returns every file whose contents contain it. " +
		"Example query: 'release notes'."
}

// Changes returns what Commit would apply, sorted by path. Paths the overlay
// touched but left equal to the base are not listed: a write of the base's
// bytes, and a delete of a file the base does not have (such as a file
// created and deleted again in the overlay). Content types are not compared,
// since backends such as vfs/osfs do not store them. Errors of the base's
// ReadFile other than vfs.ErrFileNotFound are returned wrapped.
func (s *Storage) Changes(ctx context.Context) ([]Change, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	changes := make([]Change, 0, len(s.entries))
	for _, path := range slices.Sorted(maps.Keys(s.entries)) {
		change, changed, err := s.changeFor(ctx, path)
		if err != nil {
			return nil, err
		}
		if changed {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

// Commit applies the changes to the base one path at a time in path order
// (writes and deletes alike) and drops each applied entry from the overlay,
// together with the entries that turned out to be no change. On the first
// failure it stops and returns an error naming the path that wraps the base's
// error, so errors.Is matches vfs.ErrReadOnly for a read-only base; the
// changes not applied yet stay in the overlay and a later Commit retries
// them. Commit holds the overlay's lock while it writes, so other calls wait
// until it returns.
func (s *Storage) Commit(ctx context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	for _, path := range slices.Sorted(maps.Keys(s.entries)) {
		change, changed, err := s.changeFor(ctx, path)
		if err != nil {
			return err
		}
		if changed {
			if err := s.apply(ctx, change); err != nil {
				return fmt.Errorf("aikido/vfs/overlay: commit %s: %w", path, err)
			}
		}
		delete(s.entries, path)
	}
	return nil
}

// Discard drops every pending write and delete, so the overlay shows the base
// again.
func (s *Storage) Discard() {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	clear(s.entries)
}

// changeFor compares the overlay's entry for path with the base; changed is
// false when committing the entry would leave the base as it is. The caller
// holds the lock.
func (s *Storage) changeFor(ctx context.Context, path string) (change Change, changed bool, err error) {
	current := s.entries[path]
	previous, _, err := s.base.ReadFile(ctx, path)
	baseHasFile := err == nil
	if err != nil && !errors.Is(err, vfs.ErrFileNotFound) {
		return Change{}, false, fmt.Errorf("aikido/vfs/overlay: read base %s: %w", path, err)
	}
	change = Change{Path: path, Previous: previous}
	switch {
	case current.deleted:
		change.Kind = ChangeDelete
		return change, baseHasFile, nil
	case !baseHasFile:
		change.Kind = ChangeCreate
	case bytes.Equal(previous, current.content):
		return Change{}, false, nil
	default:
		change.Kind = ChangeModify
	}
	change.Content = bytes.Clone(current.content)
	change.ContentType = current.contentType
	return change, true, nil
}

func (s *Storage) apply(ctx context.Context, change Change) error {
	if change.Kind == ChangeDelete {
		return s.base.DeleteFile(ctx, change.Path)
	}
	return s.base.WriteFile(ctx, change.Path, change.Content, change.ContentType)
}

func (e entry) metadata(path string) vfs.FileMetadata {
	return vfs.FileMetadata{Path: path, ContentType: e.contentType, Size: int64(len(e.content)), UpdatedAt: e.updatedAt}
}
