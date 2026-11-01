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
	a, err := readTree(before)
	if err != nil {
		return 0, 0, err
	}
	b, err := readTree(after)
	if err != nil {
		return 0, 0, err
	}
	for p, old := range a {
		cur, ok := b[p]
		if !ok {
			files, bytes = files+1, bytes+len(old)
			continue
		}
		if d := changedBytes(old, cur); d > 0 {
			files, bytes = files+1, bytes+d
		}
	}
	for p, cur := range b {
		if _, ok := a[p]; !ok {
			files, bytes = files+1, bytes+len(cur)
		}
	}
	return files, bytes, nil
}

func changedBytes(a, b []byte) int {
	n := min(len(a), len(b))
	pre := 0
	for pre < n && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < n-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	return max(len(a), len(b)) - pre - suf
}

// modifiedSince returns the first path under root changed after t, or "".
func modifiedSince(root string, t time.Time) string {
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(t) {
			found = p
		}
		return nil
	})
	return found
}

func readTree(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = data
		return nil
	})
	return out, err
}
