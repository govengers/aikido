package osfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/vfstest"
)

func newStorage(t *testing.T, directory string) *Storage {
	t.Helper()
	storage, err := New(&Options{Directory: directory})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := storage.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return storage
}

func TestConformance(t *testing.T) {
	vfstest.RunConformance(t, func() vfs.Storage { return newStorage(t, t.TempDir()) })
}

func TestNewRejectsMissingDirectory(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("New(nil) returned no error")
	}
	if _, err := New(&Options{}); err == nil {
		t.Error("New without Directory returned no error")
	}
	if _, err := New(&Options{Directory: filepath.Join(t.TempDir(), "missing")}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("New on a missing directory err = %v; want os.ErrNotExist", err)
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secretPath, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Symlink(secretPath, filepath.Join(directory, "link.md")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	storage := newStorage(t, directory)
	ctx := context.Background()

	if _, _, err := storage.ReadFile(ctx, "link.md"); !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("ReadFile via escaping symlink err = %v; want ErrPathInvalid", err)
	}
	if err := storage.WriteFile(ctx, "link.md", []byte("x"), ""); err != nil {
		t.Fatalf("WriteFile over symlink: %v", err)
	}
	got, err := os.ReadFile(secretPath) //nolint:gosec // G304: the path is the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "s3cret" {
		t.Errorf("write followed the symlink out of the root: outside file = %q", got)
	}
}

// TestRefuseSymlinks covers links inside the root, which os.Root follows: a
// visible public -> .env or pub -> .git reached the hidden target under a
// hidden-path policy that checks the spelled path.
func TestRefuseSymlinks(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, ".git", "hooks"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", filepath.Join(directory, "public")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(".git", filepath.Join(directory, "pub")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if content, _, err := newStorage(t, directory).ReadFile(ctx, "public"); err != nil || string(content) != "SECRET=1" {
		t.Fatalf("without RefuseSymlinks the link is followed: %q, %v", content, err)
	}
	storage, err := New(&Options{Directory: directory, RefuseSymlinks: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if _, _, err := storage.ReadFile(ctx, "public"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("ReadFile(public) err = %v; want ErrFileNotFound", err)
	}
	if err := storage.WriteFile(ctx, "pub/hooks/pre-commit", []byte("x"), ""); !errors.Is(err, vfs.ErrPathInvalid) {
		t.Errorf("WriteFile(pub/hooks/pre-commit) err = %v; want ErrPathInvalid", err)
	}
	if err := storage.DeleteFile(ctx, "public"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("DeleteFile(public) err = %v; want ErrFileNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(directory, ".git", "hooks", "pre-commit")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the write reached .git/hooks: %v", err)
	}
	if err := storage.WriteFile(ctx, "new/file.txt", []byte("x"), ""); err != nil {
		t.Errorf("WriteFile(new/file.txt): %v", err)
	}
}

func TestListSkipsGitAndCreatesDirs(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".git", "HEAD"), []byte("ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := newStorage(t, directory)
	ctx := context.Background()
	if err := storage.WriteFile(ctx, "a/b/c.md", []byte("deep"), ""); err != nil {
		t.Fatalf("WriteFile nested: %v", err)
	}
	files, err := storage.ListFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "a/b/c.md" || files[0].ContentType != "text/markdown" {
		t.Errorf("ListFiles = %+v; want only a/b/c.md as text/markdown", files)
	}
}

func TestDirectoryIsNotAFile(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "folder"), 0o750); err != nil {
		t.Fatal(err)
	}
	storage := newStorage(t, directory)
	ctx := context.Background()
	if _, _, err := storage.ReadFile(ctx, "folder"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("ReadFile(directory) err = %v; want ErrFileNotFound", err)
	}
	if err := storage.DeleteFile(ctx, "folder"); !errors.Is(err, vfs.ErrFileNotFound) {
		t.Errorf("DeleteFile(directory) err = %v; want ErrFileNotFound", err)
	}
	if err := storage.WriteFile(ctx, "folder", []byte("x"), ""); err == nil {
		t.Error("WriteFile over a directory returned no error")
	}
}

func TestWriteFileKeepsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps only the read-only bit")
	}
	directory := t.TempDir()
	storage := newStorage(t, directory)
	ctx := context.Background()
	for _, mode := range []fs.FileMode{0o600, 0o755} {
		name := fmt.Sprintf("file-%o.sh", mode)
		if err := os.WriteFile(filepath.Join(directory, name), []byte("v1"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(directory, name), mode); err != nil { // undo the umask
			t.Fatal(err)
		}
		if err := storage.WriteFile(ctx, name, []byte("v2"), ""); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
		if got := fileMode(t, filepath.Join(directory, name)); got != mode {
			t.Errorf("mode after replacing a %o file = %o; want %o", mode, got, mode)
		}
	}
	if err := storage.WriteFile(ctx, "new.md", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	if got := fileMode(t, filepath.Join(directory, "new.md")); got != newFileMode {
		t.Errorf("mode of a new file = %o; want %o", got, newFileMode)
	}
}

func fileMode(t *testing.T, filePath string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestUnknownExtensionIsOctetStream(t *testing.T) {
	storage := newStorage(t, t.TempDir())
	ctx := context.Background()
	if err := storage.WriteFile(ctx, "logo.png", []byte("x"), "image/png"); err != nil {
		t.Fatal(err)
	}
	_, metadata, err := storage.ReadFile(ctx, "logo.png")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ContentType != "application/octet-stream" {
		t.Errorf("ContentType = %q; want application/octet-stream", metadata.ContentType)
	}
}

func TestCanceledContext(t *testing.T) {
	storage := newStorage(t, t.TempDir())
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
}
