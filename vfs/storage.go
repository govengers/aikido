package vfs

import (
	"context"
	"time"
)

// FileMetadata describes one file. Size is in bytes; UpdatedAt is zero when
// the backend does not know the modification time.
type FileMetadata struct {
	Path        string
	ContentType string
	Size        int64
	UpdatedAt   time.Time
}

// Storage is the base contract every VFS backend implements.
//
// Path semantics: paths are forward-slash separated, relative, no leading
// slash, no `.` or `..` segments, no backslashes, no double slashes, no
// trailing slash, no null bytes, length <= 512 bytes. See ValidatePath.
//
// Atomicity: each call is atomic at the backend level. The agent loop is not
// transactional in v1 (ADR-019).
type Storage interface {
	// ListFiles returns every file sorted by path.
	ListFiles(ctx context.Context) ([]FileMetadata, error)
	// ReadFile returns the content and metadata of path; errors match
	// ErrPathInvalid or ErrFileNotFound.
	ReadFile(ctx context.Context, path string) ([]byte, FileMetadata, error)
	// WriteFile creates or replaces path; errors match ErrPathInvalid or
	// ErrReadOnly.
	WriteFile(ctx context.Context, path string, content []byte, contentType string) error
	// DeleteFile removes path; errors match ErrPathInvalid, ErrFileNotFound or
	// ErrReadOnly.
	DeleteFile(ctx context.Context, path string) error
}
