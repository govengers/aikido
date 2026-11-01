// Package memory implements an in-process vfs.Storage with case-insensitive
// substring search.
//
// Suitable for single-process production use and tests; data is lost on
// process exit.
package memory

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/govengers/aikido/vfs"
)

type fileEntry struct {
	content     []byte
	contentType string
	updatedAt   time.Time
}

// Storage is the in-memory implementation. It is safe for concurrent use and
// copies content on the way in and out, so callers may reuse their slices.
type Storage struct {
	mutex sync.RWMutex
	files map[string]fileEntry
	now   func() time.Time
}

var (
	_ vfs.Storage    = (*Storage)(nil)
	_ vfs.Searchable = (*Storage)(nil)
)

// New returns an empty in-memory Storage.
func New() *Storage {
	return &Storage{
		files: make(map[string]fileEntry),
		now:   time.Now,
	}
}

// ListFiles returns every file sorted by path; an empty Storage yields an
// empty, non-nil slice. It returns ctx.Err() if ctx is done.
func (s *Storage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	files := make([]vfs.FileMetadata, 0, len(s.files))
	for path, entry := range s.files {
		files = append(files, entry.metadata(path))
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

// ReadFile returns a copy of the content and the metadata of path. It returns
// an error matching vfs.ErrPathInvalid for an invalid path and
// vfs.ErrFileNotFound for a missing file.
func (s *Storage) ReadFile(ctx context.Context, path string) ([]byte, vfs.FileMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	entry, ok := s.files[path]
	if !ok {
		return nil, vfs.FileMetadata{}, fmt.Errorf("%w: %s", vfs.ErrFileNotFound, path)
	}
	return slices.Clone(entry.content), entry.metadata(path), nil
}

// WriteFile stores a copy of content under path, replacing any previous file.
// It returns an error matching vfs.ErrPathInvalid for an invalid path.
func (s *Storage) WriteFile(ctx context.Context, path string, content []byte, contentType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return err
	}
	stored := make([]byte, len(content))
	copy(stored, content)
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.files[path] = fileEntry{
		content:     stored,
		contentType: contentType,
		updatedAt:   s.now(),
	}
	return nil
}

// DeleteFile removes path. It returns an error matching vfs.ErrPathInvalid for
// an invalid path and vfs.ErrFileNotFound for a missing file.
func (s *Storage) DeleteFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := vfs.ValidatePath(path); err != nil {
		return err
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if _, ok := s.files[path]; !ok {
		return fmt.Errorf("%w: %s", vfs.ErrFileNotFound, path)
	}
	delete(s.files, path)
	return nil
}

// Search returns the paths, sorted, of every file whose content contains
// query, compared case-insensitively. An empty query matches every file.
func (s *Storage) Search(ctx context.Context, query string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lowerQuery := strings.ToLower(query)
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	paths := make([]string, 0)
	for path, entry := range s.files {
		if strings.Contains(strings.ToLower(string(entry.content)), lowerQuery) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// SearchSyntax describes the query syntax Search accepts, for the model.
func (s *Storage) SearchSyntax() string {
	return "Case-insensitive substring search against file contents. " +
		"Pass a single substring; the backend returns every file whose contents contain it. " +
		"Example query: 'release notes'."
}

func (e fileEntry) metadata(path string) vfs.FileMetadata {
	return vfs.FileMetadata{
		Path:        path,
		ContentType: e.contentType,
		Size:        int64(len(e.content)),
		UpdatedAt:   e.updatedAt,
	}
}
