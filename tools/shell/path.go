package shell

import (
	"fmt"
	"path"
	"strings"

	"github.com/govengers/aikido/vfs"
)

// Resolve turns a path as a script writes it into a workspace path. A leading
// "/" means the workspace root, anything else is relative to directory (a
// workspace path, "" for the root). "." and ".." are resolved; a path that
// climbs above the root is vfs.ErrPathInvalid, like everything ValidatePath
// refuses. The root itself resolves to "".
func Resolve(directory, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty path", vfs.ErrPathInvalid)
	}
	joined := name
	if !strings.HasPrefix(name, "/") {
		joined = directory + "/" + name
	}
	if climbsAboveRoot(joined) {
		return "", fmt.Errorf("%w: %s is outside the workspace", vfs.ErrPathInvalid, name)
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+joined), "/")
	if cleaned == "" {
		return "", nil
	}
	if err := vfs.ValidatePath(cleaned); err != nil {
		return "", err
	}
	return cleaned, nil
}

// climbsAboveRoot reports whether a "/"-separated path has more ".." segments
// at some point than names before them; path.Clean would silently stop at the
// root instead.
func climbsAboveRoot(name string) bool {
	depth := 0
	for segment := range strings.SplitSeq(name, "/") {
		switch segment {
		case "", ".":
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}
