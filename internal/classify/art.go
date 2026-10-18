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
	// ArtMaxWidth caps cached art so phones never receive 8K originals.
	ArtMaxWidth      = 2560
	downscaleTimeout = 30 * time.Second
)

// cacheImage downloads url to dst, downscaling it to ArtMaxWidth when its
// header says it is wider. A failed downscale fails the image, so the
// caller can try the next candidate.
func (s *Service) cacheImage(ctx context.Context, url, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	download := dst + ".download"
	defer os.Remove(download)
	if err := s.tvmaze.FetchImage(ctx, url, download); err != nil {
		return err
	}
	if imageWidth(download) <= ArtMaxWidth {
		return os.Rename(download, dst)
	}
	return s.downscale(ctx, download, dst)
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

// downscale writes src to dst as a JPEG at most ArtMaxWidth wide, replacing
// dst only once ffmpeg has finished.
func (s *Service) downscale(ctx context.Context, src, dst string) error {
	ctx, cancel := context.WithTimeout(ctx, downscaleTimeout)
	defer cancel()
	tmp := dst + ".scaled.jpg"
	defer os.Remove(tmp)
	cmd := exec.CommandContext(ctx, s.ffmpeg,
		"-y", "-v", "error",
		"-i", src,
		"-frames:v", "1",
		"-vf", "scale="+strconv.Itoa(ArtMaxWidth)+":-2",
		"-q:v", "3",
		"-f", "image2", "-c:v", "mjpeg",
		tmp,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("downscale art: %w", ctx.Err())
		}
		return fmt.Errorf("downscale art: %s", cmp.Or(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return os.Rename(tmp, dst)
}
