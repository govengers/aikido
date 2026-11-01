// Package vfstest is the conformance suite every vfs.Storage backend runs in
// its tests:
//
//	func TestConformance(t *testing.T) {
//		vfstest.RunConformance(t, func() vfs.Storage { return memory.NewStorage() })
//	}
package vfstest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/govengers/aikido/vfs"
)

// invalidPaths violate one structural rule of vfs.ValidatePath each.
var invalidPaths = []string{"", "/abs", "../parent.md", "trailing/", "double//slash", "with\x00null"}

// RunConformance executes the standard conformance suite against a vfs.Storage
// factory. Each sub-test calls the factory to obtain a fresh, empty
// vfs.Storage. Sub-tests run serially.
//
// The vfs.Searchable sub-tests run only when the factory's vfs.Storage
// satisfies vfs.Searchable.
func RunConformance(t *testing.T, factory func() vfs.Storage) {
	t.Helper()

	t.Run("ListEmpty", func(t *testing.T) { testListEmpty(t, factory()) })
	t.Run("WriteReadRoundTrip", func(t *testing.T) { testWriteReadRoundTrip(t, factory()) })
	t.Run("WriteOverwrites", func(t *testing.T) { testWriteOverwrites(t, factory()) })
	t.Run("ReadMissing", func(t *testing.T) { testReadMissing(t, factory()) })
	t.Run("DeleteHappyPath", func(t *testing.T) { testDeleteHappyPath(t, factory()) })
	t.Run("DeleteMissing", func(t *testing.T) { testDeleteMissing(t, factory()) })
	t.Run("ListIsDeterministic", func(t *testing.T) { testListIsDeterministic(t, factory()) })
	t.Run("WriteRejectsInvalidPath", func(t *testing.T) { testWriteRejectsInvalidPath(t, factory()) })
	t.Run("DeleteRejectsInvalidPath", func(t *testing.T) { testDeleteRejectsInvalidPath(t, factory()) })
	t.Run("ReadRejectsInvalidPath", func(t *testing.T) { testReadRejectsInvalidPath(t, factory()) })

	if _, ok := factory().(vfs.Searchable); ok {
		runSearchableConformance(t, factory)
	}
}

func runSearchableConformance(t *testing.T, factory func() vfs.Storage) {
	t.Helper()

	t.Run("vfs.Searchable/EmptyCorpus", func(t *testing.T) { testSearchEmptyCorpus(t, factory()) })
	t.Run("vfs.Searchable/SubstringMatch", func(t *testing.T) { testSearchSubstringMatch(t, factory()) })
	t.Run("vfs.Searchable/SyntaxNonEmpty", func(t *testing.T) { testSearchSyntaxNonEmpty(t, factory()) })
}

func testListEmpty(t *testing.T, storage vfs.Storage) {
	t.Helper()
	files, err := storage.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("ListFiles on empty = %v; want []", files)
	}
}

func testWriteReadRoundTrip(t *testing.T, storage vfs.Storage) {
	t.Helper()
	ctx := context.Background()
	want := []byte("# Hello\n\nWorld.\n")
	writeFile(t, storage, "notes.md", want)
	got, metadata, err := storage.ReadFile(ctx, "notes.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q; want %q", got, want)
	}
	if metadata.Path != "notes.md" {
		t.Errorf("metadata.Path = %q", metadata.Path)
	}
	if metadata.ContentType != "text/markdown" {
		t.Errorf("metadata.ContentType = %q", metadata.ContentType)
	}
	if metadata.Size != int64(len(want)) {
		t.Errorf("metadata.Size = %d; want %d", metadata.Size, len(want))
	}
	if metadata.UpdatedAt.IsZero() {
		t.Error("metadata.UpdatedAt is zero")
	}
}

func testWriteOverwrites(t *testing.T, storage vfs.Storage) {
	t.Helper()
	writeFile(t, storage, "x.md", []byte("v1"))
	writeFile(t, storage, "x.md", []byte("v2"))
	got, _, err := storage.ReadFile(context.Background(), "x.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "v2" {
		t.Errorf("content = %q; want v2", got)
	}
}

func testReadMissing(t *testing.T, storage vfs.Storage) {
	t.Helper()
	_, _, err := storage.ReadFile(context.Background(), "missing.md")
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want vfs.ErrFileNotFound", err)
	}
}

func testDeleteHappyPath(t *testing.T, storage vfs.Storage) {
	t.Helper()
	ctx := context.Background()
	writeFile(t, storage, "x.md", []byte("hi"))
	if err := storage.DeleteFile(ctx, "x.md"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	_, _, err := storage.ReadFile(ctx, "x.md")
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("after delete, ReadFile err = %v; want vfs.ErrFileNotFound", err)
	}
}

func testDeleteMissing(t *testing.T, storage vfs.Storage) {
	t.Helper()
	err := storage.DeleteFile(context.Background(), "missing.md")
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want vfs.ErrFileNotFound", err)
	}
}

func testListIsDeterministic(t *testing.T, storage vfs.Storage) {
	t.Helper()
	paths := []string{"c.md", "a.md", "b/x.md", "b/a.md"}
	for _, path := range paths {
		writeFile(t, storage, path, []byte("x"))
	}
	firstListing := listPaths(t, storage)
	secondListing := listPaths(t, storage)
	if !slices.Equal(firstListing, secondListing) {
		t.Errorf("non-deterministic order: %v then %v", firstListing, secondListing)
	}
	want := slices.Sorted(slices.Values(paths))
	got := slices.Sorted(slices.Values(firstListing))
	if !slices.Equal(got, want) {
		t.Errorf("listed paths = %v; want %v", got, want)
	}
}

func testWriteRejectsInvalidPath(t *testing.T, storage vfs.Storage) {
	t.Helper()
	for _, path := range invalidPaths {
		err := storage.WriteFile(context.Background(), path, []byte("x"), "text/plain")
		if !errors.Is(err, vfs.ErrPathInvalid) {
			t.Errorf("WriteFile(%q) err = %v; want vfs.ErrPathInvalid", path, err)
		}
	}
}

func testDeleteRejectsInvalidPath(t *testing.T, storage vfs.Storage) {
	t.Helper()
	err := storage.DeleteFile(context.Background(), "../etc/passwd")
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("DeleteFile invalid path err = %v; want vfs.ErrPathInvalid", err)
	}
}

func testReadRejectsInvalidPath(t *testing.T, storage vfs.Storage) {
	t.Helper()
	_, _, err := storage.ReadFile(context.Background(), "../etc/passwd")
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("ReadFile invalid path err = %v; want vfs.ErrPathInvalid", err)
	}
}

func testSearchEmptyCorpus(t *testing.T, storage vfs.Storage) {
	t.Helper()
	paths, err := searchable(t, storage).Search(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v; want empty", paths)
	}
}

func testSearchSubstringMatch(t *testing.T, storage vfs.Storage) {
	t.Helper()
	writeFile(t, storage, "a.md", []byte("apple banana"))
	writeFile(t, storage, "b.md", []byte("carrot date"))
	writeFile(t, storage, "c.md", []byte("avocado banana"))
	paths, err := searchable(t, storage).Search(context.Background(), "banana")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := slices.Sorted(slices.Values(paths))
	want := []string{"a.md", "c.md"}
	if !slices.Equal(got, want) {
		t.Errorf("paths = %v; want %v", got, want)
	}
}

func testSearchSyntaxNonEmpty(t *testing.T, storage vfs.Storage) {
	t.Helper()
	if searchable(t, storage).SearchSyntax() == "" {
		t.Error("SearchSyntax() returned empty")
	}
}

func writeFile(t *testing.T, storage vfs.Storage, path string, content []byte) {
	t.Helper()
	if err := storage.WriteFile(context.Background(), path, content, "text/markdown"); err != nil {
		t.Fatalf("WriteFile %q: %v", path, err)
	}
}

func listPaths(t *testing.T, storage vfs.Storage) []string {
	t.Helper()
	files, err := storage.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	paths := make([]string, len(files))
	for index, file := range files {
		paths[index] = file.Path
	}
	return paths
}

func searchable(t *testing.T, storage vfs.Storage) vfs.Searchable {
	t.Helper()
	search, ok := storage.(vfs.Searchable)
	if !ok {
		t.Fatalf("%T does not implement vfs.Searchable", storage)
	}
	return search
}
