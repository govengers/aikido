package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// diffTrees counts files that differ between the fixture and the result tree
// and estimates the edited bytes: whole size for added or removed files, the
// differing middle (after common prefix and suffix) for modified files.
func diffTrees(before, after string) (files, bytes int, err error) {
	beforeTree, err := readTree(before)
	if err != nil {
		return 0, 0, err
	}
	afterTree, err := readTree(after)
	if err != nil {
		return 0, 0, err
	}
	for path, old := range beforeTree {
		current, ok := afterTree[path]
		if !ok {
			files, bytes = files+1, bytes+len(old)
			continue
		}
		if changed := changedBytes(old, current); changed > 0 {
			files, bytes = files+1, bytes+changed
		}
	}
	for path, current := range afterTree {
		if _, ok := beforeTree[path]; !ok {
			files, bytes = files+1, bytes+len(current)
		}
	}
	return files, bytes, nil
}

func changedBytes(before, after []byte) int {
	shorter := min(len(before), len(after))
	prefix := 0
	for prefix < shorter && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < shorter-prefix && before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	return max(len(before), len(after)) - prefix - suffix
}

// modifiedSince returns the first path under root changed after since, or "".
func modifiedSince(root string, since time.Time) string {
	var found string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if info, err := entry.Info(); err == nil && info.ModTime().After(since) {
			found = path
		}
		return nil
	})
	return found
}

// readTree maps every file under root, by its slash-separated relative path,
// to its content.
func readTree(root string) (map[string][]byte, error) {
	tree := os.DirFS(root)
	files := map[string][]byte{}
	err := fs.WalkDir(tree, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(tree, path)
		if err != nil {
			return err
		}
		files[path] = data
		return nil
	})
	return files, err
}
