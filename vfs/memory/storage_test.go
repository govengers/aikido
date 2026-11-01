package memory

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/vfstest"
)

func TestConformance(t *testing.T) {
	vfstest.RunConformance(t, func() vfs.Storage { return NewStorage() })
}

func writeFile(t *testing.T, storage *Storage, path, content string) {
	t.Helper()
	if err := storage.WriteFile(context.Background(), path, []byte(content), "text/markdown"); err != nil {
		t.Fatalf("WriteFile %q: %v", path, err)
	}
}

func TestSearch_EmptyQueryMatchesEveryFile(t *testing.T) {
	storage := NewStorage()
	writeFile(t, storage, "b.md", "world")
	writeFile(t, storage, "a.md", "hello")

	paths, err := storage.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := []string{"a.md", "b.md"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %v; want %v", paths, want)
	}
}

func TestSearch_CaseInsensitive(t *testing.T) {
	storage := NewStorage()
	writeFile(t, storage, "a.md", "Hello WORLD")
	paths, err := storage.Search(context.Background(), "world")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(paths) != 1 || paths[0] != "a.md" {
		t.Errorf("paths = %v", paths)
	}
}

func TestWrite_DefenseInDepthValidation(t *testing.T) {
	storage := NewStorage()
	err := storage.WriteFile(context.Background(), "../escape", []byte("x"), "text/plain")
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Fatalf("err = %v; want ErrPathInvalid", err)
	}
}

func TestReadFile_ReturnsACopy(t *testing.T) {
	storage := NewStorage()
	writeFile(t, storage, "a.md", "original")
	content, _, err := storage.ReadFile(context.Background(), "a.md")
	if err != nil {
		t.Fatal(err)
	}
	content[0] = 'X'
	again, _, err := storage.ReadFile(context.Background(), "a.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != "original" {
		t.Errorf("stored content changed through the returned slice: %q", again)
	}
}

func TestCanceledContext(t *testing.T) {
	storage := NewStorage()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storage.ListFiles(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListFiles err = %v; want context.Canceled", err)
	}
	if _, _, err := storage.ReadFile(ctx, "a.md"); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFile err = %v; want context.Canceled", err)
	}
	if err := storage.WriteFile(ctx, "a.md", nil, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("WriteFile err = %v; want context.Canceled", err)
	}
	if err := storage.DeleteFile(ctx, "a.md"); !errors.Is(err, context.Canceled) {
		t.Errorf("DeleteFile err = %v; want context.Canceled", err)
	}
	if _, err := storage.Search(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Search err = %v; want context.Canceled", err)
	}
}

func TestStorage_Concurrency(t *testing.T) {
	storage := NewStorage()
	ctx := context.Background()
	const writers = 50
	var waitGroup sync.WaitGroup
	for writer := range writers {
		waitGroup.Go(func() {
			path := "f-" + strconv.Itoa(writer%26) + ".md"
			if err := storage.WriteFile(ctx, path, []byte("payload"), "text/markdown"); err != nil {
				t.Errorf("WriteFile: %v", err)
			}
			if _, _, err := storage.ReadFile(ctx, path); err != nil {
				t.Errorf("ReadFile: %v", err)
			}
		})
	}
	waitGroup.Wait()
}
