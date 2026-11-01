package memory_test

import (
	"context"
	"fmt"

	"github.com/govengers/aikido/vfs/memory"
)

func ExampleStorage_Search() {
	storage := memory.New()
	ctx := context.Background()
	files := map[string]string{
		"notes/release.md": "Release notes for v1",
		"notes/todo.md":    "Buy milk",
	}
	for path, content := range files {
		if err := storage.WriteFile(ctx, path, []byte(content), "text/markdown"); err != nil {
			fmt.Println(err)
			return
		}
	}
	paths, err := storage.Search(ctx, "RELEASE")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(paths)
	// Output: [notes/release.md]
}
