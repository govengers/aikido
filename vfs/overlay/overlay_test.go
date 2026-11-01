package overlay_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/embedfs"
	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/osfs"
	"github.com/govengers/aikido/vfs/overlay"
	"github.com/govengers/aikido/vfs/vfstest"
)

func TestConformance_MemoryBase(t *testing.T) {
	vfstest.RunConformance(t, func() vfs.Storage { return newOverlay(t, memory.New()) })
}

func TestConformance_OSBase(t *testing.T) {
	vfstest.RunConformance(t, func() vfs.Storage {
		base, err := osfs.New(&osfs.Options{Directory: t.TempDir()})
		if err != nil {
			t.Fatalf("osfs.New: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		return newOverlay(t, base)
	})
}

func TestNew_RequiresBase(t *testing.T) {
	for _, options := range []*overlay.Options{nil, {}} {
		if _, err := overlay.New(options); err == nil {
			t.Errorf("New(%v) err = nil; want an error", options)
		}
	}
}

func TestWritesStayInTheOverlay(t *testing.T) {
	ctx := context.Background()
	base := baseWith(t, map[string]string{"kept.md": "base"})
	storage := newOverlay(t, base)
	write(t, storage, "kept.md", "overlay")
	write(t, storage, "new.md", "fresh")
	if err := storage.DeleteFile(ctx, "kept.md"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if got := read(t, base, "kept.md"); got != "base" {
		t.Errorf("base kept.md = %q; want base", got)
	}
	if _, _, err := base.ReadFile(ctx, "new.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("base new.md err = %v; want vfs.ErrFileNotFound", err)
	}
	if _, _, err := storage.ReadFile(ctx, "kept.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("deleted kept.md err = %v; want vfs.ErrFileNotFound", err)
	}
	if err := storage.DeleteFile(ctx, "kept.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("second delete err = %v; want vfs.ErrFileNotFound", err)
	}
	if got := paths(t, storage); !slices.Equal(got, []string{"new.md"}) {
		t.Errorf("ListFiles = %v; want [new.md]", got)
	}
}

func TestReadsFallThroughToTheBase(t *testing.T) {
	storage := newOverlay(t, baseWith(t, map[string]string{"a.md": "from base"}))
	if got := read(t, storage, "a.md"); got != "from base" {
		t.Errorf("ReadFile = %q; want from base", got)
	}
	write(t, storage, "a.md", "from overlay")
	if got := read(t, storage, "a.md"); got != "from overlay" {
		t.Errorf("ReadFile = %q; want from overlay", got)
	}
}

func TestChanges(t *testing.T) {
	testCases := []struct {
		name  string
		edit  func(t *testing.T, storage *overlay.Storage)
		want  []string // "<kind> <path>"
		check func(t *testing.T, changes []overlay.Change)
	}{
		{
			name: "write then delete of a new file is no change",
			edit: func(t *testing.T, storage *overlay.Storage) {
				write(t, storage, "scratch.md", "x")
				deleteFile(t, storage, "scratch.md")
			},
		},
		{
			name: "restoring the base bytes is no change",
			edit: func(t *testing.T, storage *overlay.Storage) {
				write(t, storage, "readme.md", "changed")
				write(t, storage, "readme.md", "base readme")
			},
		},
		{
			name: "deleting a base file is a delete",
			edit: func(t *testing.T, storage *overlay.Storage) { deleteFile(t, storage, "readme.md") },
			want: []string{"delete readme.md"},
			check: func(t *testing.T, changes []overlay.Change) {
				if string(changes[0].Previous) != "base readme" || changes[0].Content != nil {
					t.Errorf("delete change = %+v; want Previous=base readme and no Content", changes[0])
				}
			},
		},
		{
			name: "deleting and rewriting a base file is a modify",
			edit: func(t *testing.T, storage *overlay.Storage) {
				deleteFile(t, storage, "readme.md")
				write(t, storage, "readme.md", "rewritten")
			},
			want: []string{"modify readme.md"},
		},
		{
			name: "changes are sorted by path",
			edit: func(t *testing.T, storage *overlay.Storage) {
				write(t, storage, "z.md", "new")
				write(t, storage, "readme.md", "changed")
				write(t, storage, "a.md", "new")
			},
			want: []string{"create a.md", "modify readme.md", "create z.md"},
			check: func(t *testing.T, changes []overlay.Change) {
				modify := changes[1]
				if string(modify.Content) != "changed" || string(modify.Previous) != "base readme" || modify.ContentType != "text/markdown" {
					t.Errorf("modify change = %+v", modify)
				}
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			storage := newOverlay(t, baseWith(t, map[string]string{"readme.md": "base readme"}))
			testCase.edit(t, storage)
			changes := changesOf(t, storage)
			if got := describe(changes); !slices.Equal(got, testCase.want) {
				t.Fatalf("Changes = %v; want %v", got, testCase.want)
			}
			if testCase.check != nil {
				testCase.check(t, changes)
			}
		})
	}
}

func TestCommit_AppliesAndClears(t *testing.T) {
	ctx := context.Background()
	base := baseWith(t, map[string]string{"old.md": "old", "edit.md": "v1", "same.md": "same"})
	storage := newOverlay(t, base)
	write(t, storage, "edit.md", "v2")
	write(t, storage, "new.md", "new")
	write(t, storage, "same.md", "same")
	deleteFile(t, storage, "old.md")
	if err := storage.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := paths(t, base); !slices.Equal(got, []string{"edit.md", "new.md", "same.md"}) {
		t.Errorf("base files = %v", got)
	}
	if got := read(t, base, "edit.md"); got != "v2" {
		t.Errorf("base edit.md = %q; want v2", got)
	}
	if changes := changesOf(t, storage); len(changes) != 0 {
		t.Errorf("Changes after Commit = %v; want none", describe(changes))
	}
}

func TestCommit_FailureKeepsTheRestAndIsRetryable(t *testing.T) {
	ctx := context.Background()
	base := &failingStorage{Storage: memory.New(), failWrite: map[string]bool{"b.md": true}}
	storage := newOverlay(t, base)
	for _, path := range []string{"a.md", "b.md", "c.md"} {
		write(t, storage, path, path)
	}
	err := storage.Commit(ctx)
	if err == nil || !strings.Contains(err.Error(), "b.md") || !errors.Is(err, errInjected) {
		t.Fatalf("Commit err = %v; want the injected error naming b.md", err)
	}
	if got := paths(t, base); !slices.Equal(got, []string{"a.md"}) {
		t.Errorf("base after failed Commit = %v; want [a.md]", got)
	}
	if got := describe(changesOf(t, storage)); !slices.Equal(got, []string{"create b.md", "create c.md"}) {
		t.Errorf("Changes after failed Commit = %v", got)
	}
	base.failWrite = nil
	if err := storage.Commit(ctx); err != nil {
		t.Fatalf("retried Commit: %v", err)
	}
	if got := paths(t, base); !slices.Equal(got, []string{"a.md", "b.md", "c.md"}) {
		t.Errorf("base after retry = %v", got)
	}
}

func TestCommit_CaseOnlyRenameOnCaseInsensitiveBase(t *testing.T) {
	ctx := context.Background()
	base := newFoldingStorage(map[string]string{"Readme.md": "hello\n"})
	storage := newOverlay(t, base)
	renameCaseOnly(t, storage)
	if got := describe(changesOf(t, storage)); !slices.Equal(got, []string{"create README.md", "delete Readme.md"}) {
		t.Errorf("Changes = %v; want [create README.md delete Readme.md]", got)
	}
	if err := storage.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := paths(t, base); !slices.Equal(got, []string{"README.md"}) {
		t.Errorf("base files = %v; want [README.md]", got)
	}
	if got := read(t, base, "README.md"); got != "hello\n" {
		t.Errorf("base README.md = %q", got)
	}
}

func TestCommit_CaseOnlyRenameOnDisk(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "Readme.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "README.md")); err != nil {
		t.Skipf("%s is case-sensitive", directory)
	}
	base, err := osfs.New(&osfs.Options{Directory: directory})
	if err != nil {
		t.Fatalf("osfs.New: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	storage := newOverlay(t, base)
	renameCaseOnly(t, storage)
	if err := storage.Commit(context.Background()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "README.md" {
		t.Fatalf("directory holds %v; want only README.md", entries)
	}
	if got := read(t, base, "README.md"); got != "hello\n" {
		t.Errorf("README.md = %q", got)
	}
}

// Regression: a case-variant delete turned an unchanged rewrite into a
// create, also on a case-sensitive base that holds both spellings, so
// Changes showed the whole file as added and Commit rewrote it.
func TestChanges_CaseVariantDeleteOnCaseSensitiveBase(t *testing.T) {
	base := baseWith(t, map[string]string{"Readme.md": "old\n", "README.md": "hello\n"})
	storage := newOverlay(t, base)
	deleteFile(t, storage, "Readme.md")
	write(t, storage, "README.md", "hello\n")
	if got := describe(changesOf(t, storage)); !slices.Equal(got, []string{"delete Readme.md"}) {
		t.Errorf("Changes = %v; want [delete Readme.md]", got)
	}

	failing := newOverlay(t, &failingStorage{Storage: base, failList: true})
	deleteFile(t, failing, "Readme.md")
	write(t, failing, "README.md", "hello\n")
	if _, err := failing.Changes(context.Background()); !errors.Is(err, errInjected) {
		t.Errorf("Changes over a failing base listing err = %v; want the injected error", err)
	}
}

// renameCaseOnly does what move_file does for Readme.md -> README.md: write
// the destination, then delete the source.
func renameCaseOnly(t *testing.T, storage *overlay.Storage) {
	t.Helper()
	write(t, storage, "README.md", read(t, storage, "Readme.md"))
	deleteFile(t, storage, "Readme.md")
}

// foldingStorage is a case-insensitive, case-preserving base like the
// default filesystems of macOS and Windows: one file per path under
// strings.ToLower, listed under the name it was created with.
type foldingStorage struct {
	files map[string]foldedFile
}

type foldedFile struct{ name, content string }

func newFoldingStorage(files map[string]string) *foldingStorage {
	storage := &foldingStorage{files: map[string]foldedFile{}}
	for name, content := range files {
		storage.files[strings.ToLower(name)] = foldedFile{name: name, content: content}
	}
	return storage
}

func (s *foldingStorage) ListFiles(context.Context) ([]vfs.FileMetadata, error) {
	files := []vfs.FileMetadata{}
	for _, file := range s.files {
		files = append(files, vfs.FileMetadata{Path: file.name, Size: int64(len(file.content))})
	}
	slices.SortFunc(files, func(left, right vfs.FileMetadata) int { return strings.Compare(left.Path, right.Path) })
	return files, nil
}

func (s *foldingStorage) ReadFile(_ context.Context, path string) ([]byte, vfs.FileMetadata, error) {
	file, ok := s.files[strings.ToLower(path)]
	if !ok {
		return nil, vfs.FileMetadata{}, fmt.Errorf("%w: %s", vfs.ErrFileNotFound, path)
	}
	return []byte(file.content), vfs.FileMetadata{Path: path, Size: int64(len(file.content))}, nil
}

func (s *foldingStorage) WriteFile(_ context.Context, path string, content []byte, _ string) error {
	file, ok := s.files[strings.ToLower(path)]
	if !ok {
		file.name = path
	}
	file.content = string(content)
	s.files[strings.ToLower(path)] = file
	return nil
}

func (s *foldingStorage) DeleteFile(_ context.Context, path string) error {
	if _, ok := s.files[strings.ToLower(path)]; !ok {
		return fmt.Errorf("%w: %s", vfs.ErrFileNotFound, path)
	}
	delete(s.files, strings.ToLower(path))
	return nil
}

func TestCommit_ReadOnlyBase(t *testing.T) {
	base, err := embedfs.New(&embedfs.Options{FileSystem: fstest.MapFS{"doc.md": {Data: []byte("doc")}}})
	if err != nil {
		t.Fatalf("embedfs.New: %v", err)
	}
	storage := newOverlay(t, base)
	write(t, storage, "doc.md", "edited")
	if err := storage.Commit(context.Background()); !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("Commit err = %v; want vfs.ErrReadOnly", err)
	}
	if got := read(t, storage, "doc.md"); got != "edited" {
		t.Errorf("overlay lost the change: %q", got)
	}
}

func TestDiscard(t *testing.T) {
	storage := newOverlay(t, baseWith(t, map[string]string{"a.md": "base"}))
	write(t, storage, "a.md", "changed")
	write(t, storage, "b.md", "new")
	storage.Discard()
	if got := read(t, storage, "a.md"); got != "base" {
		t.Errorf("a.md after Discard = %q; want base", got)
	}
	if changes := changesOf(t, storage); len(changes) != 0 {
		t.Errorf("Changes after Discard = %v", describe(changes))
	}
}

func TestSearch_MergedView(t *testing.T) {
	ctx := context.Background()
	storage := newOverlay(t, baseWith(t, map[string]string{"gone.md": "needle", "kept.md": "Needle", "other.md": "hay"}))
	deleteFile(t, storage, "gone.md")
	write(t, storage, "new.md", "a NEEDLE here")
	write(t, storage, "other.md", "still hay")
	got, err := storage.Search(ctx, "needle")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := []string{"kept.md", "new.md"}; !slices.Equal(got, want) {
		t.Errorf("Search = %v; want %v", got, want)
	}
}

func TestBaseErrorsAreReturned(t *testing.T) {
	ctx := context.Background()
	base := &failingStorage{Storage: memory.New()}
	storage := newOverlay(t, base)
	write(t, storage, "a.md", "x")
	base.failRead, base.failList = true, true
	if _, err := storage.Changes(ctx); !errors.Is(err, errInjected) {
		t.Errorf("Changes err = %v; want the injected error", err)
	}
	if err := storage.Commit(ctx); !errors.Is(err, errInjected) {
		t.Errorf("Commit err = %v; want the injected error", err)
	}
	if _, err := storage.ListFiles(ctx); !errors.Is(err, errInjected) {
		t.Errorf("ListFiles err = %v; want the injected error", err)
	}
	if _, err := storage.Search(ctx, "x"); !errors.Is(err, errInjected) {
		t.Errorf("Search err = %v; want the injected error", err)
	}
	base.failList = false
	if _, err := storage.Search(ctx, "x"); err != nil {
		t.Errorf("Search over overlay-only files err = %v", err)
	}
	write(t, base, "base.md", "x")
	if _, err := storage.Search(ctx, "x"); !errors.Is(err, errInjected) {
		t.Errorf("Search reading a base file err = %v; want the injected error", err)
	}
}

func TestDoneContext(t *testing.T) {
	storage := newOverlay(t, memory.New())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := storage.ReadFile(ctx, "a.md"); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFile err = %v", err)
	}
	if err := storage.WriteFile(ctx, "a.md", nil, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("WriteFile err = %v", err)
	}
	if err := storage.DeleteFile(ctx, "a.md"); !errors.Is(err, context.Canceled) {
		t.Errorf("DeleteFile err = %v", err)
	}
}

func TestConcurrentUse(t *testing.T) {
	ctx := context.Background()
	storage := newOverlay(t, baseWith(t, map[string]string{"shared.md": "base"}))
	var group sync.WaitGroup
	for worker := range 8 {
		group.Go(func() {
			path := fmt.Sprintf("worker-%d.md", worker)
			for round := range 20 {
				_ = storage.WriteFile(ctx, path, []byte(fmt.Sprint(round)), "text/markdown")
				_ = storage.WriteFile(ctx, "shared.md", []byte(path), "text/markdown")
				_, _, _ = storage.ReadFile(ctx, "shared.md")
				_, _ = storage.ListFiles(ctx)
				_, _ = storage.Search(ctx, "worker")
				_, _ = storage.Changes(ctx)
				_ = storage.DeleteFile(ctx, path)
			}
		})
	}
	group.Go(func() { _ = storage.Commit(ctx) })
	group.Wait()
	if err := storage.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestListFiles_ConcurrentCommitKeepsTheFile(t *testing.T) {
	ctx := context.Background()
	base := &pausingStorage{Storage: memory.New()}
	storage := newOverlay(t, base)
	write(t, storage, "new.md", "created in the overlay")
	committed := make(chan error, 1)
	base.afterList = func() {
		go func() { committed <- storage.Commit(ctx) }()
		// Give a Commit that is not held off by the overlay's lock the time to
		// move new.md to the base; with the lock held it waits for ListFiles.
		select {
		case err := <-committed:
			committed <- err
		case <-time.After(50 * time.Millisecond):
		}
	}
	listed := paths(t, storage)
	if err := <-committed; err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !slices.Equal(listed, []string{"new.md"}) {
		t.Errorf("ListFiles during Commit = %v; want [new.md]", listed)
	}
}

// pausingStorage runs afterList once, after its base listing and before
// ListFiles returns it.
type pausingStorage struct {
	vfs.Storage
	afterList func()
}

func (s *pausingStorage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	files, err := s.Storage.ListFiles(ctx)
	if hook := s.afterList; hook != nil {
		s.afterList = nil
		hook()
	}
	return files, err
}

var errInjected = errors.New("injected failure")

// failingStorage wraps a Storage and fails the operations a test switches on.
type failingStorage struct {
	vfs.Storage
	failWrite map[string]bool
	failRead  bool
	failList  bool
}

func (s *failingStorage) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	if s.failList {
		return nil, errInjected
	}
	return s.Storage.ListFiles(ctx)
}

func (s *failingStorage) ReadFile(ctx context.Context, path string) ([]byte, vfs.FileMetadata, error) {
	if s.failRead {
		return nil, vfs.FileMetadata{}, errInjected
	}
	return s.Storage.ReadFile(ctx, path)
}

func (s *failingStorage) WriteFile(ctx context.Context, path string, content []byte, contentType string) error {
	if s.failWrite[path] {
		return errInjected
	}
	return s.Storage.WriteFile(ctx, path, content, contentType)
}

func newOverlay(t *testing.T, base vfs.Storage) *overlay.Storage {
	t.Helper()
	storage, err := overlay.New(&overlay.Options{Base: base})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return storage
}

func baseWith(t *testing.T, files map[string]string) *memory.Storage {
	t.Helper()
	base := memory.New()
	for path, content := range files {
		write(t, base, path, content)
	}
	return base
}

func write(t *testing.T, storage vfs.Storage, path, content string) {
	t.Helper()
	if err := storage.WriteFile(context.Background(), path, []byte(content), "text/markdown"); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func deleteFile(t *testing.T, storage vfs.Storage, path string) {
	t.Helper()
	if err := storage.DeleteFile(context.Background(), path); err != nil {
		t.Fatalf("DeleteFile %s: %v", path, err)
	}
}

func read(t *testing.T, storage vfs.Storage, path string) string {
	t.Helper()
	content, _, err := storage.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(content)
}

func paths(t *testing.T, storage vfs.Storage) []string {
	t.Helper()
	files, err := storage.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	result := make([]string, len(files))
	for index, file := range files {
		result[index] = file.Path
	}
	return result
}

func changesOf(t *testing.T, storage *overlay.Storage) []overlay.Change {
	t.Helper()
	changes, err := storage.Changes(context.Background())
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	return changes
}

func describe(changes []overlay.Change) []string {
	var described []string
	for _, change := range changes {
		described = append(described, string(change.Kind)+" "+change.Path)
	}
	return described
}
