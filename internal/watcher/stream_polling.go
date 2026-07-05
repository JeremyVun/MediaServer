//go:build !darwin

package watcher

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

type fileSnapshot struct {
	size  int64
	mtime time.Time
}

func watchPath(ctx context.Context, path string, latency time.Duration, log *slog.Logger) (<-chan streamEvent, error) {
	if latency <= 0 {
		latency = defaultStreamLatency
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	out := make(chan streamEvent, 128)
	go func() {
		defer close(out)
		previous := map[string]fileSnapshot{}
		ticker := time.NewTicker(latency)
		defer ticker.Stop()
		for {
			current, err := snapshot(path)
			if err != nil {
				select {
				case out <- streamEvent{Path: path, Rescan: true}:
				case <-ctx.Done():
					return
				}
			} else {
				for p, snap := range current {
					if old, ok := previous[p]; !ok || old != snap {
						select {
						case out <- streamEvent{Path: p}:
						case <-ctx.Done():
							return
						}
					}
				}
				for p := range previous {
					if _, ok := current[p]; !ok {
						select {
						case out <- streamEvent{Path: p}:
						case <-ctx.Done():
							return
						}
					}
				}
				previous = current
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out, nil
}

func snapshot(root string) (map[string]fileSnapshot, error) {
	seen := map[string]fileSnapshot{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && d.IsDir() && ignoredRel(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		seen[path] = fileSnapshot{size: info.Size(), mtime: info.ModTime()}
		return nil
	})
	return seen, err
}
