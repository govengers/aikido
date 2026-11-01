package mount_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/mount"
)

// A writable workspace at the root next to read-only reference documents.
func Example() {
	ctx := context.Background()
	docs := memory.New()
	if err := docs.WriteFile(ctx, "style.md", []byte("Write short sentences."), "text/markdown"); err != nil {
		fmt.Println(err)
		return
	}
	storage, err := mount.New(&mount.Options{Mounts: []mount.Mount{
		{Prefix: "", Storage: memory.New()},
		{Prefix: "docs", Storage: docs, ReadOnly: true},
	}})
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := storage.WriteFile(ctx, "draft.md", []byte("Hello."), "text/markdown"); err != nil {
		fmt.Println(err)
		return
	}
	err = storage.WriteFile(ctx, "docs/style.md", []byte("Anything goes."), "text/markdown")
	fmt.Println("write to docs is read-only:", errors.Is(err, vfs.ErrReadOnly))

	files, err := storage.ListFiles(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, file := range files {
		fmt.Println(file.Path)
	}
	// Output:
	// write to docs is read-only: true
	// docs/style.md
	// draft.md
}
