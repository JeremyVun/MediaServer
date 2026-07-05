//go:build darwin

package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsevents"
)

func watchPath(ctx context.Context, path string, latency time.Duration, _ *slog.Logger) (<-chan streamEvent, error) {
	out := make(chan streamEvent, 128)
	stream := &fsevents.EventStream{
		Paths:   []string{path},
		Flags:   fsevents.FileEvents | fsevents.WatchRoot,
		Latency: latency,
	}
	if err := stream.Start(); err != nil {
		close(out)
		return nil, err
	}

	go func() {
		defer close(out)
		defer stream.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case batch, ok := <-stream.Events:
				if !ok {
					return
				}
				for _, event := range batch {
					if event.Flags&fsevents.HistoryDone != 0 {
						continue
					}
					rescan := event.Flags&(fsevents.MustScanSubDirs|fsevents.KernelDropped|fsevents.UserDropped|fsevents.RootChanged) != 0
					ev := streamEvent{Path: filepath.Clean(event.Path), Rescan: rescan}
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out, nil
}
