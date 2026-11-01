// Package embedfs adapts any fs.FS (including embed.FS) to vfs.Storage.
//
// The backend is read-only: WriteFile and DeleteFile return vfs.ErrReadOnly
// (vfs.ErrPathInvalid for an invalid path).
// Suitable for chatbot/knowledge-base use cases where the corpus is fixed at
// compile time and the model only reads.
//
//	import _ "embed"
//
//	//go:embed knowledge/*.md
//	var knowledgeFS embed.FS
//
//	knowledge, _ := fs.Sub(knowledgeFS, "knowledge")
//	storage, err := embedfs.New(&embedfs.Options{FileSystem: knowledge})
//
// Storage satisfies vfs.Searchable with case-insensitive substring matching
// against file contents, the same syntax as vfs/memory.
package embedfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/govengers/aikido/vfs"
)

// Storage wraps an fs.FS as a read-only vfs.Storage.
type Storage struct {
	fsys fs.FS
}

var (
	_ vfs.Storage    = (*Storage)(nil)
	_ vfs.Searchable = (*Storage)(nil)
)

// Options configure a Storage.
type Options struct {
	// FileSystem is the corpus, required. It is consulted on every call; nothing
	// is cached. Pass fs.Sub(fileSystem, "data") to hide a parent prefix of an
	// embed from the model.
	FileSystem fs.FS
}

// New wraps options.FileSystem. It returns an error if options or the
// FileSystem is nil.
func New(options *Options) (*Storage, error) {
	if options == nil || options.FileSystem == nil {
		return nil, errors.New("aikido/vfs/embedfs: FileSystem is required")
	}
	return &Storage{fsys: options.FileSystem}, nil
}

// ListFiles walks the whole FileSystem and returns every file (no
// directories) sorted by path. It returns ctx.Err() if ctx is done and a
// wrapped fs error if the walk fails.
func (s *Storage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var files []vfs.FileMetadata
	err := fs.WalkDir(s.fsys, ".", func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, vfs.FileMetadata{
			Path:        filePath,
			ContentType: contentTypeFor(filePath),
			Size:        info.Size(),
			UpdatedAt:   info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/embedfs: walk: %w", err)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

// ReadFile returns the content and metadata of filePath. It returns an error
// matching vfs.ErrPathInvalid for a structurally invalid path and
// vfs.ErrFileNotFound for a missing file. UpdatedAt is zero when the
// FileSystem cannot stat the file.
func (s *Storage) ReadFile(ctx context.Context, filePath string) ([]byte, vfs.FileMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	if err := vfs.ValidatePath(filePath); err != nil {
		return nil, vfs.FileMetadata{}, err
	}
	content, err := fs.ReadFile(s.fsys, filePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, vfs.FileMetadata{}, fmt.Errorf("%w: %s", vfs.ErrFileNotFound, filePath)
	}
	if err != nil {
		return nil, vfs.FileMetadata{}, fmt.Errorf("aikido/vfs/embedfs: read %s: %w", filePath, err)
	}
	metadata := vfs.FileMetadata{
		Path:        filePath,
		ContentType: contentTypeFor(filePath),
		Size:        int64(len(content)),
	}
	if info, statError := fs.Stat(s.fsys, filePath); statError == nil {
		metadata.UpdatedAt = info.ModTime()
	}
	return content, metadata, nil
}

// WriteFile returns an error matching vfs.ErrPathInvalid for an invalid path
// and one matching vfs.ErrReadOnly otherwise.
func (s *Storage) WriteFile(_ context.Context, filePath string, _ []byte, _ string) error {
	return readOnly(filePath)
}

// DeleteFile returns an error matching vfs.ErrPathInvalid for an invalid path
// and one matching vfs.ErrReadOnly otherwise.
func (s *Storage) DeleteFile(_ context.Context, filePath string) error {
	return readOnly(filePath)
}

// readOnly is the refusal of a write to filePath; an invalid path is reported
// as such first, like every other backend does.
func readOnly(filePath string) error {
	if err := vfs.ValidatePath(filePath); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", vfs.ErrReadOnly, filePath)
}

// Search returns the paths, sorted, of every file whose content contains query,
// compared case-insensitively. An empty query matches every file.
func (s *Storage) Search(ctx context.Context, query string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lowerQuery := strings.ToLower(query)
	var hits []string
	err := fs.WalkDir(s.fsys, ".", func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(s.fsys, filePath)
		if err != nil {
			return err
		}
		if strings.Contains(strings.ToLower(string(content)), lowerQuery) {
			hits = append(hits, filePath)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("aikido/vfs/embedfs: search: %w", err)
	}
	sort.Strings(hits)
	return hits, nil
}

// SearchSyntax describes the query syntax Search accepts, for the model.
func (s *Storage) SearchSyntax() string {
	return "Case-insensitive substring search against file contents. " +
		"Pass a single substring; the backend returns every file whose contents contain it."
}

func contentTypeFor(filePath string) string {
	if contentType := vfs.ContentTypeFor(filePath); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}
