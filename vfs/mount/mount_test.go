package mount_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/mount"
	"github.com/govengers/aikido/vfs/osfs"
	"github.com/govengers/aikido/vfs/vfstest"
)

func TestConformance_RootMount(t *testing.T) {
	vfstest.RunConformance(t, func() vfs.Storage {
		return newMount(t, mount.Mount{Prefix: "", Storage: memory.New()})
	})
}

func TestConformance_RootAndTwoPrefixedMounts(t *testing.T) {
	// The suite writes a.md, b/a.md, b/x.md, c.md and friends, so "b" takes
	// part in every listing and search; "docs" stays empty.
	vfstest.RunConformance(t, func() vfs.Storage {
		return newMount(t,
			mount.Mount{Prefix: "", Storage: memory.New()},
			mount.Mount{Prefix: "b", Storage: memory.New()},
			mount.Mount{Prefix: "docs", Storage: memory.New()},
		)
	})
}

func TestConformance_NotSearchable(t *testing.T) {
	vfstest.RunConformance(t, func() vfs.Storage {
		base, err := osfs.New(&osfs.Options{Directory: t.TempDir()})
		if err != nil {
			t.Fatalf("osfs.New: %v", err)
		}
		t.Cleanup(func() { _ = base.Close() })
		storage := newMount(t, mount.Mount{Prefix: "", Storage: base})
		if _, ok := storage.(vfs.Searchable); ok {
			t.Fatal("mount over a non-searchable storage implements vfs.Searchable")
		}
		return storage
	})
}

func TestNew_Rejects(t *testing.T) {
	testCases := []struct {
		name    string
		options *mount.Options
	}{
		{"nil options", nil},
		{"no mounts", &mount.Options{}},
		{"nil storage", &mount.Options{Mounts: []mount.Mount{{Prefix: "docs"}}}},
		{"invalid prefix", &mount.Options{Mounts: []mount.Mount{{Prefix: "docs/", Storage: memory.New()}}}},
		{"parent prefix", &mount.Options{Mounts: []mount.Mount{{Prefix: "../docs", Storage: memory.New()}}}},
		{"duplicate prefix", &mount.Options{Mounts: []mount.Mount{
			{Prefix: "docs", Storage: memory.New()},
			{Prefix: "docs", Storage: memory.New(), ReadOnly: true},
		}}},
		{"duplicate root", &mount.Options{Mounts: []mount.Mount{
			{Prefix: "", Storage: memory.New()},
			{Prefix: "", Storage: memory.New()},
		}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := mount.New(testCase.options); err == nil {
				t.Error("New err = nil; want an error")
			}
		})
	}
}

func TestRouting(t *testing.T) {
	ctx := context.Background()
	root, docs, api := memory.New(), memory.New(), memory.New()
	storage := newMount(t,
		mount.Mount{Prefix: "docs", Storage: docs},
		mount.Mount{Prefix: "", Storage: root},
		mount.Mount{Prefix: "docs/api", Storage: api},
	)
	writes := map[string]string{"top.md": "root", "docsy.md": "root", "docs/guide.md": "docs", "docs/api/ref.md": "api"}
	for path, content := range writes {
		write(t, storage, path, content)
	}
	for _, check := range []struct {
		storage vfs.Storage
		path    string
	}{{root, "top.md"}, {root, "docsy.md"}, {docs, "guide.md"}, {api, "ref.md"}} {
		if _, _, err := check.storage.ReadFile(ctx, check.path); err != nil {
			t.Errorf("mounted storage lacks %s: %v", check.path, err)
		}
	}
	content, metadata, err := storage.ReadFile(ctx, "docs/api/ref.md")
	if err != nil || string(content) != "api" || metadata.Path != "docs/api/ref.md" {
		t.Errorf("ReadFile = %q, %+v, %v; want api with the full path", content, metadata, err)
	}
	if err := storage.DeleteFile(ctx, "docs/guide.md"); err != nil {
		t.Errorf("DeleteFile: %v", err)
	}
	if _, _, err := docs.ReadFile(ctx, "guide.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("docs still has guide.md: %v", err)
	}
}

func TestInnerMountHidesOuterFiles(t *testing.T) {
	ctx := context.Background()
	root := memory.New()
	write(t, root, "docs/shadowed.md", "needle in root")
	write(t, root, "docs", "a file named like the mount point")
	write(t, root, "visible.md", "needle")
	docs := memory.New()
	write(t, docs, "own.md", "needle in docs")
	storage := newMount(t, mount.Mount{Prefix: "", Storage: root}, mount.Mount{Prefix: "docs", Storage: docs})

	if got, want := paths(t, storage), []string{"docs/own.md", "visible.md"}; !slices.Equal(got, want) {
		t.Errorf("ListFiles = %v; want %v", got, want)
	}
	found, err := storage.(vfs.Searchable).Search(ctx, "NEEDLE")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := []string{"docs/own.md", "visible.md"}; !slices.Equal(found, want) {
		t.Errorf("Search = %v; want %v", found, want)
	}
	if _, _, err := storage.ReadFile(ctx, "docs/shadowed.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("ReadFile shadowed err = %v; want vfs.ErrFileNotFound", err)
	}
}

func TestListFilesOrdering(t *testing.T) {
	storage := newMount(t,
		mount.Mount{Prefix: "", Storage: memory.New()},
		mount.Mount{Prefix: "m", Storage: memory.New()},
	)
	for _, path := range []string{"z.md", "m/b.md", "a.md", "m/a.md", "ma.md", "m-.md"} {
		write(t, storage, path, "x")
	}
	want := []string{"a.md", "m-.md", "m/a.md", "m/b.md", "ma.md", "z.md"}
	if got := paths(t, storage); !slices.Equal(got, want) {
		t.Errorf("ListFiles = %v; want %v", got, want)
	}
}

func TestReadOnlyMount(t *testing.T) {
	ctx := context.Background()
	docs := memory.New()
	write(t, docs, "guide.md", "read me")
	storage := newMount(t, mount.Mount{Prefix: "", Storage: memory.New()}, mount.Mount{Prefix: "docs", Storage: docs, ReadOnly: true})
	if err := storage.WriteFile(ctx, "docs/new.md", []byte("x"), "text/markdown"); !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("WriteFile err = %v; want vfs.ErrReadOnly", err)
	}
	if err := storage.DeleteFile(ctx, "docs/guide.md"); !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("DeleteFile err = %v; want vfs.ErrReadOnly", err)
	}
	if got := read(t, storage, "docs/guide.md"); got != "read me" {
		t.Errorf("ReadFile = %q", got)
	}
	write(t, storage, "notes.md", "the root stays writable")
}

func TestUnroutablePaths(t *testing.T) {
	ctx := context.Background()
	storage := newMount(t, mount.Mount{Prefix: "docs", Storage: memory.New()})
	for _, path := range []string{"elsewhere.md", "docs", "../docs/a.md"} {
		if _, _, err := storage.ReadFile(ctx, path); !errors.Is(err, vfs.ErrPathInvalid) {
			t.Errorf("ReadFile(%q) err = %v; want vfs.ErrPathInvalid", path, err)
		}
		if err := storage.WriteFile(ctx, path, nil, ""); !errors.Is(err, vfs.ErrPathInvalid) {
			t.Errorf("WriteFile(%q) err = %v; want vfs.ErrPathInvalid", path, err)
		}
		if err := storage.DeleteFile(ctx, path); !errors.Is(err, vfs.ErrPathInvalid) {
			t.Errorf("DeleteFile(%q) err = %v; want vfs.ErrPathInvalid", path, err)
		}
	}
	if err := storage.DeleteFile(ctx, "docs/missing.md"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("DeleteFile missing err = %v; want vfs.ErrFileNotFound", err)
	}
}

func TestSearchSkipsMountsWithoutSearch(t *testing.T) {
	ctx := context.Background()
	disk, err := osfs.New(&osfs.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("osfs.New: %v", err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	notes := memory.New()
	write(t, notes, "todo.md", "needle")
	storage := newMount(t, mount.Mount{Prefix: "disk", Storage: disk}, mount.Mount{Prefix: "notes", Storage: notes})
	write(t, storage, "disk/a.md", "needle")
	searchable, ok := storage.(vfs.Searchable)
	if !ok {
		t.Fatal("mount with a searchable mount does not implement vfs.Searchable")
	}
	found, err := searchable.Search(ctx, "needle")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if want := []string{"notes/todo.md"}; !slices.Equal(found, want) {
		t.Errorf("Search = %v; want %v", found, want)
	}
	if got, want := searchable.SearchSyntax(), memory.New().SearchSyntax(); got != want {
		t.Errorf("SearchSyntax = %q; want the memory syntax", got)
	}
}

func TestSearchSyntax_Mixed(t *testing.T) {
	storage := newMount(t,
		mount.Mount{Prefix: "a", Storage: memory.New()},
		mount.Mount{Prefix: "b", Storage: &customSearch{Storage: memory.New()}},
	)
	syntax := storage.(vfs.Searchable).SearchSyntax()
	if !strings.Contains(syntax, `Files under "b": regular expressions`) || !strings.Contains(syntax, `Files under "a": Case-insensitive`) {
		t.Errorf("SearchSyntax = %q; want one line per mount", syntax)
	}
}

func TestMountErrorsAreWrapped(t *testing.T) {
	ctx := context.Background()
	storage := newMount(t, mount.Mount{Prefix: "bad", Storage: &customSearch{Storage: memory.New(), fail: true}})
	if _, err := storage.ListFiles(ctx); !errors.Is(err, errInjected) {
		t.Errorf("ListFiles err = %v; want the injected error", err)
	}
	if _, err := storage.(vfs.Searchable).Search(ctx, "x"); !errors.Is(err, errInjected) {
		t.Errorf("Search err = %v; want the injected error", err)
	}
	if err := storage.WriteFile(ctx, "bad/x.md", nil, ""); !errors.Is(err, errInjected) {
		t.Errorf("WriteFile err = %v; want the injected error", err)
	}
}

func TestOptionsAreCopied(t *testing.T) {
	first := memory.New()
	options := &mount.Options{Mounts: []mount.Mount{{Prefix: "", Storage: first}}}
	storage, err := mount.New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	options.Mounts[0].ReadOnly = true
	write(t, storage, "still-writable.md", "x")
}

var errInjected = errors.New("injected failure")

// customSearch is a searchable storage with its own syntax that can fail.
type customSearch struct {
	vfs.Storage
	fail bool
}

func (s *customSearch) ListFiles(ctx context.Context) ([]vfs.FileMetadata, error) {
	if s.fail {
		return nil, errInjected
	}
	return s.Storage.ListFiles(ctx)
}

func (s *customSearch) WriteFile(ctx context.Context, path string, content []byte, contentType string) error {
	if s.fail {
		return errInjected
	}
	return s.Storage.WriteFile(ctx, path, content, contentType)
}

func (s *customSearch) Search(context.Context, string) ([]string, error) {
	if s.fail {
		return nil, errInjected
	}
	return []string{}, nil
}

func (s *customSearch) SearchSyntax() string { return "regular expressions" }

func newMount(t *testing.T, mounts ...mount.Mount) vfs.Storage {
	t.Helper()
	storage, err := mount.New(&mount.Options{Mounts: mounts})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return storage
}

func write(t *testing.T, storage vfs.Storage, path, content string) {
	t.Helper()
	if err := storage.WriteFile(context.Background(), path, []byte(content), "text/markdown"); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
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
