package httpapi

import (
	"fmt"
	"path/filepath"
	"strings"
)

func safeJoin(rootPath, relPath string) (string, error) {
	rel := filepath.Clean(filepath.FromSlash(relPath))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative media path %q", relPath)
	}
	return filepath.Join(rootPath, rel), nil
}
