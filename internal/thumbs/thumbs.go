package thumbs

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const DefaultTimeout = 60 * time.Second

// waitDelay bounds how long Wait blocks on stdio after the context kills
// the child, so the "hard timeout" holds even if pipes are left open.
const waitDelay = 5 * time.Second

type Generator struct {
	Binary  string
	Dir     string
	Timeout time.Duration
	Log     *slog.Logger
}

func Path(dir string, fileID int64) string {
	return filepath.Join(dir, fmt.Sprintf("%d.jpg", fileID))
}

func PosterPath(dir string, fileID int64) string {
	return filepath.Join(dir, fmt.Sprintf("%d_poster.jpg", fileID))
}

func (g Generator) Generate(ctx context.Context, fileID int64, mediaPath string, durationS float64) error {
	if g.Dir == "" {
		return fmt.Errorf("thumbnail dir is required")
	}
	if err := os.MkdirAll(g.Dir, 0o755); err != nil {
		return fmt.Errorf("create thumbnail dir: %w", err)
	}

	bin := g.Binary
	if bin == "" {
		bin = "ffmpeg"
	}
	timeout := g.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	seek := 0.0
	if durationS > 0 {
		seek = durationS * 0.10
	}

	// Each invocation gets the full timeout budget: a slow first pass must
	// not starve the poster pass into a spurious deadline failure.
	if err := g.runFFmpeg(ctx, bin, timeout, mediaPath,
		"-y",
		"-v", "error",
		"-ss", strconv.FormatFloat(seek, 'f', 3, 64),
		"-i", mediaPath,
		"-frames:v", "1",
		"-vf", "scale=640:-2",
		"-q:v", "3",
		Path(g.Dir, fileID),
	); err != nil {
		return err
	}
	return g.runFFmpeg(ctx, bin, timeout, mediaPath,
		"-y",
		"-v", "error",
		"-ss", strconv.FormatFloat(seek, 'f', 3, 64),
		"-i", mediaPath,
		"-frames:v", "1",
		"-vf", "scale='if(gt(a,0.6667),-2,480)':'if(gt(a,0.6667),720,-2)',crop=480:720",
		"-q:v", "3",
		PosterPath(g.Dir, fileID),
	)
}

func (g Generator) runFFmpeg(ctx context.Context, bin string, timeout time.Duration, mediaPath string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = waitDelay
	if g.Log != nil {
		g.Log.Debug("ffmpeg exec", "argv", cmd.Args)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			// Wrapped so the job manager retries timeouts instead of
			// failing the job permanently.
			return fmt.Errorf("ffmpeg thumbnail timed out after %s: %w", timeout, context.DeadlineExceeded)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg thumbnail %s: %s", mediaPath, msg)
	}
	return nil
}
