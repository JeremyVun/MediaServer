package watcher

import (
	"context"
	"log/slog"
	"time"
)

type streamEvent struct {
	Path   string
	Rescan bool
}

type streamFactory func(ctx context.Context, path string, latency time.Duration, log *slog.Logger) (<-chan streamEvent, error)

func firstStreamFactory(factory streamFactory) streamFactory {
	if factory != nil {
		return factory
	}
	return watchPath
}
