package shell

import (
	"context"
	"sync"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
)

// maximumScratchBytes caps the files in /tmp together: /tmp lives in memory
// for the life of the Shell, so a script must not be able to fill it
// without bound.
const maximumScratchBytes = 64 << 20

const errNoSpace fileError = "No space left on device"

// scratchStorage is /tmp: a memory Storage that lives as long as the Shell
// and refuses a write that would make its files larger than limit together.
// It is safe for concurrent use (the commands of a pipeline run at once).
type scratchStorage struct {
	files *memory.Storage
	limit int

	mutex sync.Mutex
	sizes map[string]int
	total int
}

func newScratchStorage(limit int) *scratchStorage {
	return &scratchStorage{files: memory.New(), limit: limit, sizes: map[string]int{}}
}

func (s *scratchStorage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	return s.files.ListFiles(ctx)
}

func (s *scratchStorage) ReadFile(ctx context.Context, name string) ([]byte, vfs.FileMetadata, error) {
	return s.files.ReadFile(ctx, name)
}

// WriteFile stores content unless the files would exceed the limit; then it
// fails with "No space left on device" and keeps the old content.
func (s *scratchStorage) WriteFile(ctx context.Context, name string, content []byte, contentType string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	total := s.total - s.sizes[name] + len(content)
	if total > s.limit {
		return errNoSpace
	}
	if err := s.files.WriteFile(ctx, name, content, contentType); err != nil {
		return err //nolint:wrapcheck // the memory Storage's errors are vfs errors the shell maps to GNU wording
	}
	s.sizes[name], s.total = len(content), total
	return nil
}

func (s *scratchStorage) DeleteFile(ctx context.Context, name string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if err := s.files.DeleteFile(ctx, name); err != nil {
		return err //nolint:wrapcheck // the memory Storage's errors are vfs errors the shell maps to GNU wording
	}
	s.total -= s.sizes[name]
	delete(s.sizes, name)
	return nil
}
