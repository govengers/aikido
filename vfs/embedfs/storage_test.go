package embedfs_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/embedfs"
)

func newTestStorage(t *testing.T) *embedfs.Storage {
	t.Helper()
	return newStorage(t, newTestFS())
}

func newStorage(t *testing.T, fileSystem fs.FS) *embedfs.Storage {
	t.Helper()
	storage, err := embedfs.New(&embedfs.Options{FileSystem: fileSystem})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return storage
}

func TestNewRejectsMissingFileSystem(t *testing.T) {
	if _, err := embedfs.New(nil); err == nil {
		t.Error("New(nil) returned no error")
	}
	if _, err := embedfs.New(&embedfs.Options{}); err == nil {
		t.Error("New without FileSystem returned no error")
	}
}

func newTestFS() fstest.MapFS {
	return fstest.MapFS{
		"a.md":      &fstest.MapFile{Data: []byte("# A\n\nApple Banana")},
		"b.md":      &fstest.MapFile{Data: []byte("# B\n\nCarrot Date")},
		"sub/c.md":  &fstest.MapFile{Data: []byte("# C\n\napple again")},
		"data.json": &fstest.MapFile{Data: []byte(`{"k":"v"}`)},
		"logo.png":  &fstest.MapFile{Data: []byte("\x89PNG")},
	}
}

func TestStorage_ListFiles(t *testing.T) {
	storage := newTestStorage(t)
	files, err := storage.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.Path)
	}
	want := []string{"a.md", "b.md", "data.json", "logo.png", "sub/c.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paths = %v; want %v", got, want)
	}
}

func TestStorage_ListFiles_Meta(t *testing.T) {
	storage := newTestStorage(t)
	files, err := storage.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	contentTypes := make(map[string]string, len(files))
	for _, file := range files {
		contentTypes[file.Path] = file.ContentType
		if file.Size == 0 {
			t.Errorf("%s: Size = 0", file.Path)
		}
	}
	want := map[string]string{
		"a.md":      "text/markdown",
		"data.json": "application/json",
		"logo.png":  "application/octet-stream",
		"sub/c.md":  "text/markdown",
		"b.md":      "text/markdown",
	}
	for path, wantType := range want {
		if contentTypes[path] != wantType {
			t.Errorf("%s: ContentType = %q; want %q", path, contentTypes[path], wantType)
		}
	}
}

func TestStorage_ReadFile(t *testing.T) {
	storage := newTestStorage(t)
	data, metadata, err := storage.ReadFile(context.Background(), "a.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "Apple Banana") {
		t.Errorf("content = %q", data)
	}
	if metadata.ContentType != "text/markdown" {
		t.Errorf("ContentType = %q", metadata.ContentType)
	}
	if metadata.Size != int64(len(data)) {
		t.Errorf("Size = %d; want %d", metadata.Size, len(data))
	}
}

func TestStorage_ReadFile_Missing(t *testing.T) {
	storage := newTestStorage(t)
	_, _, err := storage.ReadFile(context.Background(), "nope.md")
	if !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want ErrFileNotFound", err)
	}
}

func TestStorage_ReadFile_InvalidPath(t *testing.T) {
	storage := newTestStorage(t)
	_, _, err := storage.ReadFile(context.Background(), "../escape.md")
	if !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("err = %v; want ErrPathInvalid", err)
	}
}

func TestStorage_WriteFile_ReadOnly(t *testing.T) {
	storage := newTestStorage(t)
	err := storage.WriteFile(context.Background(), "x.md", []byte("hi"), "text/markdown")
	if !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("err = %v; want ErrReadOnly", err)
	}
}

func TestStorage_DeleteFile_ReadOnly(t *testing.T) {
	storage := newTestStorage(t)
	err := storage.DeleteFile(context.Background(), "a.md")
	if !errors.Is(err, vfs.ErrReadOnly) {
		t.Errorf("err = %v; want ErrReadOnly", err)
	}
}

func TestStorage_Search(t *testing.T) {
	storage := newTestStorage(t)
	hits, err := storage.Search(context.Background(), "apple")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := append([]string(nil), hits...)
	sort.Strings(got)
	want := []string{"a.md", "sub/c.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hits = %v; want %v", got, want)
	}
}

func TestStorage_SearchSyntax_NonEmpty(t *testing.T) {
	storage := newTestStorage(t)
	if storage.SearchSyntax() == "" {
		t.Error("SearchSyntax should be non-empty")
	}
}

func TestStorage_AsSearchable(t *testing.T) {
	var storage vfs.Storage = newTestStorage(t)
	if _, ok := storage.(vfs.Searchable); !ok {
		t.Error("Storage should satisfy vfs.Searchable")
	}
}

func TestStorage_FsSubScoped(t *testing.T) {
	// fs.Sub-style usage: scope to "sub/" so callers see paths without prefix.
	parent := fstest.MapFS{
		"data/a.md": &fstest.MapFile{Data: []byte("inside data")},
		"data/b.md": &fstest.MapFile{Data: []byte("also inside")},
		"outside":   &fstest.MapFile{Data: []byte("filtered out")},
	}
	sub, err := fs.Sub(parent, "data")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	storage := newStorage(t, sub)
	files, err := storage.ListFiles(context.Background())
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d; want 2 (filtered)", len(files))
	}
	if files[0].Path != "a.md" || files[1].Path != "b.md" {
		t.Errorf("paths = %+v; want [a.md, b.md]", files)
	}
}

func TestStorage_ReadFile_UnknownExtensionIsOctetStream(t *testing.T) {
	storage := newTestStorage(t)
	_, metadata, err := storage.ReadFile(context.Background(), "logo.png")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if metadata.ContentType != "application/octet-stream" {
		t.Errorf("ContentType = %q; want application/octet-stream", metadata.ContentType)
	}
}

func TestStorage_ReadFile_Directory(t *testing.T) {
	storage := newTestStorage(t)
	_, _, err := storage.ReadFile(context.Background(), "sub")
	if err == nil || errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("err = %v; want a read error that is not ErrFileNotFound", err)
	}
}

func TestStorage_CanceledContext(t *testing.T) {
	storage := newTestStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storage.ListFiles(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ListFiles err = %v; want context.Canceled", err)
	}
	if _, _, err := storage.ReadFile(ctx, "a.md"); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadFile err = %v; want context.Canceled", err)
	}
	if _, err := storage.Search(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Search err = %v; want context.Canceled", err)
	}
}

func TestStorage_WalkErrorsAreWrapped(t *testing.T) {
	storage := newStorage(t, failingFS{})
	if _, err := storage.ListFiles(context.Background()); !errors.Is(err, errBroken) {
		t.Errorf("ListFiles err = %v; want errBroken", err)
	}
	if _, err := storage.Search(context.Background(), "a"); !errors.Is(err, errBroken) {
		t.Errorf("Search err = %v; want errBroken", err)
	}
}

var errBroken = errors.New("broken file system")

type failingFS struct{}

func (failingFS) Open(string) (fs.File, error) { return nil, errBroken }

func ExampleNew() {
	knowledge := fstest.MapFS{
		"faq.md": &fstest.MapFile{Data: []byte("Opening hours: 9 to 5")},
	}
	storage, err := embedfs.New(&embedfs.Options{FileSystem: knowledge})
	if err != nil {
		fmt.Println(err)
		return
	}
	content, metadata, err := storage.ReadFile(context.Background(), "faq.md")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(metadata.ContentType, string(content))
	// Output: text/markdown Opening hours: 9 to 5
}
