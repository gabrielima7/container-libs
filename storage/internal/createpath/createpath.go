package createpath

import (
	"fmt"
	"path/filepath"
	"strings"
)

// CleanAbsPath removes any ".." and "." from the path
// and ensures it starts with a "/".  If the path refers to the root
// directory, it returns "/".
func CleanAbsPath(path string) string {
	return filepath.Clean("/" + path)
}

// SplitPath takes a file path as input and returns two components: dir and base.
// Differently than filepath.Split(), this function handles some edge cases.
// If the path refers to a file in the root directory, the returned dir is "/".
// The returned base value is never empty, it never contains any slash and the
// value "..".
func SplitPath(path string) (string, string, error) {
	path = CleanAbsPath(path)
	dir, base := filepath.Split(path)
	if base == "" {
		base = "."
	}
	// Remove trailing slashes from dir, but make sure that "/" is preserved.
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "/"
	}

	if strings.Contains(base, "/") {
		// This should never happen, but be safe as the base is passed to *at syscalls.
		return "", "", fmt.Errorf("internal error: SplitPath(%q) contains a slash", path)
	}
	return dir, base, nil
}
