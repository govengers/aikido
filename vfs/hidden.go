package vfs

import "strings"

// IsHiddenPath reports whether a path has a segment starting with "." or
// "_" (.env, .git/hooks, _private): the default rule of the HideHiddenPaths
// options of the agent's VFS tools and the shell.
func IsHiddenPath(name string) bool {
	return hasSegmentStartingWith(name, "._")
}

// IsDotPath reports whether a path has a segment starting with ".": a rule
// for HiddenPath that keeps "_" folders visible, e.g. the _shared workbook
// stages of an instruction repository.
func IsDotPath(name string) bool {
	return hasSegmentStartingWith(name, ".")
}

func hasSegmentStartingWith(name, prefixes string) bool {
	for segment := range strings.SplitSeq(name, "/") {
		if segment != "" && strings.IndexByte(prefixes, segment[0]) >= 0 {
			return true
		}
	}
	return false
}
