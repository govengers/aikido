package overlay_test

import (
	"context"
	"fmt"

	"github.com/govengers/aikido/vfs/memory"
	"github.com/govengers/aikido/vfs/overlay"
)

// The overlay collects an agent's edits without touching the base; Changes
// and UnifiedDiff show them for review before Commit applies them.
func Example() {
	ctx := context.Background()
	base := memory.New() // any vfs.Storage: osfs over a directory, embedfs, ...
	if err := base.WriteFile(ctx, "notes.md", []byte("# Notes\n- milk\n"), "text/markdown"); err != nil {
		fmt.Println(err)
		return
	}
	workspace, err := overlay.New(&overlay.Options{Base: base})
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := workspace.WriteFile(ctx, "notes.md", []byte("# Notes\n- milk\n- eggs\n"), "text/markdown"); err != nil {
		fmt.Println(err)
		return
	}

	changes, err := workspace.Changes(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, change := range changes {
		fmt.Println(change.Kind, change.Path)
	}
	fmt.Print(overlay.UnifiedDiff(changes))

	if err := workspace.Commit(ctx); err != nil {
		fmt.Println(err)
		return
	}
	committed, _, err := base.ReadFile(ctx, "notes.md")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("base now has %d bytes\n", len(committed))
	// Output:
	// modify notes.md
	// --- a/notes.md
	// +++ b/notes.md
	// @@ -1,2 +1,3 @@
	//  # Notes
	//  - milk
	// +- eggs
	// base now has 22 bytes
}
