package vfs

import (
	"fmt"
	"strings"
)

// MaximumPathBytes is the structural cap on a path's byte length.
const MaximumPathBytes = 512

// ValidatePath enforces aikido-wide structural invariants on a path (see
// Storage). The returned error matches ErrPathInvalid and names the broken
// rule.
//
// Caller-policy filters (max bytes, allowed extensions, hidden-path filtering)
// live on agent.VFSToolOptions, not here.
func ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("%w: empty path", ErrPathInvalid)
	}
	if len(path) > MaximumPathBytes {
		return fmt.Errorf("%w: path exceeds %d bytes", ErrPathInvalid, MaximumPathBytes)
	}
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("%w: contains null byte", ErrPathInvalid)
	}
	if path[0] == '/' {
		return fmt.Errorf("%w: absolute path", ErrPathInvalid)
	}
	if path[len(path)-1] == '/' {
		return fmt.Errorf("%w: trailing slash", ErrPathInvalid)
	}
	if strings.Contains(path, "//") {
		return fmt.Errorf("%w: double slash", ErrPathInvalid)
	}
	if strings.ContainsRune(path, '\\') {
		return fmt.Errorf("%w: backslash", ErrPathInvalid)
	}
	for segment := range strings.SplitSeq(path, "/") {
		switch segment {
		case "..":
			return fmt.Errorf("%w: parent-directory segment", ErrPathInvalid)
		case ".":
			return fmt.Errorf("%w: current-directory segment", ErrPathInvalid)
		}
	}
	return nil
}
