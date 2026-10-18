package classify

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// BackdropMaxWidth caps cached splash art so phones never receive 8K
	// originals.
	BackdropMaxWidth = 2560
	// PosterMaxWidth covers the widest poster the client draws, the spine of
	// a library card (about 140 CSS px), at 3x density with room to spare.
	PosterMaxWidth   = 680
	downscaleTimeout = 30 * time.Second
)

// prepareImage downloads url into a temp file beside the cached art,
// downscaled to maxWidth when its header says it is wider, and returns the
// file for installArt to rename into place. A failed downscale fails the
// image, so the caller can try the next candidate.
func (s *Service) prepareImage(ctx context.Context, url string, maxWidth int) (string, error) {
	download, err := s.artTemp()
	if err != nil {
		return "", err
	}
	if err := s.tvmaze.FetchImage(ctx, url, download); err != nil {
		os.Remove(download)
		return "", err
	}
	if imageWidth(download) <= maxWidth {
		return download, nil
	}
	defer os.Remove(download)
	return s.downscale(ctx, download, maxWidth)
}

func (s *Service) artTemp() (string, error) {
	dir := filepath.Join(s.artDir, "shows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".art-*.jpg")
	if err != nil {
		return "", err
	}
	return f.Name(), f.Close()
}

// imageWidth is 0 for a file that is not a JPEG or PNG.
func imageWidth(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0
	}
	return cfg.Width
}

// downscale writes src as a JPEG at most maxWidth wide to a new temp file.
func (s *Service) downscale(ctx context.Context, src string, maxWidth int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, downscaleTimeout)
	defer cancel()
	dst, err := s.artTemp()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, s.ffmpeg,
		"-y", "-v", "error",
		"-i", src,
		"-frames:v", "1",
		"-vf", "scale="+strconv.Itoa(maxWidth)+":-2",
		"-q:v", "3",
		"-f", "image2", "-c:v", "mjpeg",
		dst,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(dst)
		if ctx.Err() != nil {
			return "", fmt.Errorf("downscale art: %w", ctx.Err())
		}
		return "", fmt.Errorf("downscale art: %s", cmp.Or(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return dst, nil
}
