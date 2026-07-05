package playback

import (
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

const DefaultSubtitleTimeout = 2 * time.Minute

type SubtitleExtractor struct {
	Binary   string
	CacheDir string
	Timeout  time.Duration
	Log      *slog.Logger
}

func SubtitlePath(cacheDir string, fileID int64, streamIndex int) string {
	return filepath.Join(cacheDir, fmt.Sprintf("%d-%d.vtt", fileID, streamIndex))
}

func (e SubtitleExtractor) Extract(ctx context.Context, fileID int64, sourcePath string, stream Stream) (string, error) {
	if !IsTextSubtitle(stream.Codec) {
		return "", fmt.Errorf("stream %d codec %q is not a text subtitle", stream.StreamIndex, stream.Codec)
	}
	if e.CacheDir == "" {
		return "", fmt.Errorf("subtitle cache dir is required")
	}
	if err := os.MkdirAll(e.CacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create subtitle cache: %w", err)
	}
	out := SubtitlePath(e.CacheDir, fileID, stream.StreamIndex)
	if info, err := os.Stat(out); err == nil && info.Size() > 0 {
		return out, nil
	}

	bin := e.Binary
	if bin == "" {
		bin = "ffmpeg"
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultSubtitleTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tmp := out + ".tmp"
	_ = os.Remove(tmp)
	cmd := exec.CommandContext(runCtx, bin,
		"-hide_banner",
		"-nostdin",
		"-y",
		"-v", "error",
		"-i", sourcePath,
		"-map", "0:"+strconv.Itoa(stream.StreamIndex),
		"-f", "webvtt",
		tmp,
	)
	cmd.WaitDelay = defaultWaitDelay
	if e.Log != nil {
		e.Log.Debug("ffmpeg subtitle exec", "argv", cmd.Args)
	}
	stderr := newTailBuffer(16 * 1024)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		if runCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("ffmpeg subtitle timed out after %s: %w", timeout, context.DeadlineExceeded)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("ffmpeg subtitle %s stream %d: %s", sourcePath, stream.StreamIndex, msg)
	}
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return out, nil
}
