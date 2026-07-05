// Package logging sets up slog with JSON output to a size-rotated file
// (keep 5 × 10 MB per SPEC-BACKEND) plus human-readable output on stderr.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	maxLogSize = 10 << 20 // rotate after 10 MB
	keepLogs   = 5        // server.log + server.log.1 .. .4
)

// Setup returns a logger writing JSON to {logDir}/server.log (rotated) and
// text to stderr, plus a close function for the file.
func Setup(logDir, level string) (*slog.Logger, func() error, error) {
	lvl, err := ParseLevel(level)
	if err != nil {
		return nil, nil, err
	}
	rw, err := newRotatingWriter(filepath.Join(logDir, "server.log"), maxLogSize, keepLogs)
	if err != nil {
		return nil, nil, err
	}
	fileHandler := slog.NewJSONHandler(rw, &slog.HandlerOptions{Level: lvl})
	stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	logger := slog.New(multiHandler{fileHandler, stderrHandler})
	return logger, rw.Close, nil
}

func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q", s)
	}
}

// rotatingWriter is a size-based rotating file writer: when the current file
// would exceed maxSize, server.log → server.log.1 → … → server.log.{keep-1},
// dropping the oldest.
type rotatingWriter struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
	file    *os.File
	size    int64
}

func newRotatingWriter(path string, maxSize int64, keep int) (*rotatingWriter, error) {
	w := &rotatingWriter{path: path, maxSize: maxSize, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.size = info.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	for i := w.keep - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", w.path, i)
		to := fmt.Sprintf("%s.%d", w.path, i+1)
		if i == w.keep-1 {
			os.Remove(from)
			continue
		}
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, to); err != nil {
				return err
			}
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

var _ io.WriteCloser = (*rotatingWriter)(nil)

// multiHandler fans a record out to several handlers.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, lvl slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, lvl) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}
