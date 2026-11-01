package shell

import (
	"context"
	"sync"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
)

// maximumSessionBytes is the memory budget of a Shell: the files in /tmp,
// the files of <(list) in /dev/fd and the output redirects buffer before
// they store it share it. /tmp lives in memory for the life of the Shell,
// so a script must not be able to fill memory without bound. Twice the
// per-file cap, so one file of the largest size can be buffered and stored
// in /tmp.
const maximumSessionBytes = 2 * maximumBufferedFileBytes

const errNoSpace fileError = "No space left on device"

// memoryBudget counts the bytes a Shell's scripts hold in memory outside
// the workspace. Safe for concurrent use (the commands of a pipeline run at
// once).
type memoryBudget struct {
	mutex sync.Mutex
	limit int
	used  int
}

func newMemoryBudget(limit int) *memoryBudget {
	return &memoryBudget{limit: limit}
}

// reserve adds size bytes (a negative size releases them) and reports
// whether they fit; a reservation that does not fit changes nothing.
func (b *memoryBudget) reserve(size int) bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if size > 0 && b.used+size > b.limit {
		return false
	}
	b.used += size
	return true
}

func (b *memoryBudget) release(size int) {
	b.reserve(-size)
}

// scratchStorage is a memory Storage whose files count against a
// memoryBudget: /tmp (for the life of the Shell) and /dev/fd (for one Run).
// A write that does not fit fails with "No space left on device" and keeps
// the old content.
type scratchStorage struct {
	files  *memory.Storage
	budget *memoryBudget

	mutex sync.Mutex
	sizes map[string]int
}

func newScratchStorage(budget *memoryBudget) *scratchStorage {
	return &scratchStorage{files: memory.New(), budget: budget, sizes: map[string]int{}}
}

func (s *scratchStorage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	return s.files.ListFiles(ctx)
}

func (s *scratchStorage) ReadFile(ctx context.Context, name string) ([]byte, vfs.FileMetadata, error) {
	return s.files.ReadFile(ctx, name)
}

func (s *scratchStorage) WriteFile(ctx context.Context, name string, content []byte, contentType string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	growth := len(content) - s.sizes[name]
	if !s.budget.reserve(growth) {
		return errNoSpace
	}
	if err := s.files.WriteFile(ctx, name, content, contentType); err != nil {
		s.budget.release(growth)
		return err //nolint:wrapcheck // the memory Storage's errors are vfs errors the shell maps to GNU wording
	}
	s.sizes[name] = len(content)
	return nil
}

func (s *scratchStorage) DeleteFile(ctx context.Context, name string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if err := s.files.DeleteFile(ctx, name); err != nil {
		return err //nolint:wrapcheck // the memory Storage's errors are vfs errors the shell maps to GNU wording
	}
	s.budget.release(s.sizes[name])
	delete(s.sizes, name)
	return nil
}

// clear deletes every file and returns its bytes to the budget.
func (s *scratchStorage) clear(ctx context.Context) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	for name, size := range s.sizes {
		_ = s.files.DeleteFile(ctx, name)
		s.budget.release(size)
	}
	clear(s.sizes)
}
