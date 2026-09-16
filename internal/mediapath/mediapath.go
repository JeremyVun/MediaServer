// Package mediapath holds the path helpers shared by the watcher, the job
// handlers and the HTTP layer, so a root-relative path is validated the same
// way everywhere it is joined back onto a root.
package mediapath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafeJoin resolves a forward-slash root-relative path under rootPath and
// rejects anything that would escape the root.
func SafeJoin(rootPath, relPath string) (string, error) {
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative media path %q", relPath)
	}
	return filepath.Join(rootPath, rel), nil
}

func IsVideoPath(name string) bool {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "mp4", "m4v", "mov", "mkv", "webm", "avi", "ts", "m2ts", "wmv", "flv":
		return true
	default:
		return false
	}
}

func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
