package osfs_test

import (
	"context"
	"fmt"
	"os"

	"github.com/govengers/aikido/vfs/osfs"
)

func ExampleNew() {
	directory, err := os.MkdirTemp("", "osfs-example")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(directory)

	storage, err := osfs.New(&osfs.Options{Directory: directory})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer storage.Close()

	ctx := context.Background()
	if err := storage.WriteFile(ctx, "notes/today.md", []byte("# Today"), "text/markdown"); err != nil {
		fmt.Println(err)
		return
	}
	files, err := storage.ListFiles(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(files[0].Path, files[0].ContentType)
	// Output: notes/today.md text/markdown
}
