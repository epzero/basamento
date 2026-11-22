package workspace

import (
	"path"
	"strings"
)

// ValidPath reports whether name is a clean relative path inside a work
// directory. It rejects an empty name, an absolute path, a backslash path and
// a NUL-bearing name, and requires the path's clean spelling: "foo/../bar",
// "foo//bar" and "foo/" are rejected rather than normalised, because a caller
// that merges files or commits them must not see two keys for one file.
func ValidPath(name string) bool {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) || path.IsAbs(name) {
		return false
	}
	if path.Clean(name) != name {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
