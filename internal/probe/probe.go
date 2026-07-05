package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const DefaultTimeout = 30 * time.Second

// waitDelay bounds how long Wait blocks on stdio after the context kills
// the child, so the "hard timeout" holds even if pipes are left open.
const waitDelay = 5 * time.Second

type Runner struct {
	Binary  string
	Timeout time.Duration
	Log     *slog.Logger
}

type Result struct {
	Container string
	DurationS float64
	Bitrate   int64
	Width     int
	Height    int
	Streams   []Stream
}

type Stream struct {
	Index     int
	Kind      string
	Codec     string
	Lang      *string
	Title     *string
	Channels  *int
	IsDefault bool
}

func (r Runner) Probe(ctx context.Context, path string) (Result, error) {
	bin := r.Binary
	if bin == "" {
		bin = "ffprobe"
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin,
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	cmd.WaitDelay = waitDelay
	if r.Log != nil {
		r.Log.Debug("ffprobe exec", "argv", cmd.Args)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		// Wrap so callers can tell a transient timeout (retry) from a
		// corrupt file (permanent failure).
		return Result{}, fmt.Errorf("ffprobe timed out after %s: %w", timeout, context.DeadlineExceeded)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Result{}, fmt.Errorf("ffprobe %s: %s", path, msg)
	}
	return Parse(out)
}

func Parse(raw []byte) (Result, error) {
	var doc ffprobeDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Result{}, fmt.Errorf("parse ffprobe json: %w", err)
	}

	var res Result
	if doc.Format.FormatName != "" {
		res.Container = strings.Split(doc.Format.FormatName, ",")[0]
	}
	res.DurationS = parseFloat(doc.Format.Duration)
	res.Bitrate = parseInt64(doc.Format.BitRate)

	for _, st := range doc.Streams {
		if st.CodecType == "" || st.CodecName == "" {
			continue
		}
		switch st.CodecType {
		case "video":
			if res.Width == 0 {
				res.Width = st.Width
				res.Height = st.Height
			}
		case "audio", "subtitle":
		default:
			continue
		}
		stream := Stream{
			Index:     st.Index,
			Kind:      st.CodecType,
			Codec:     st.CodecName,
			IsDefault: st.Disposition.Default != 0,
		}
		if st.Tags.Language != "" {
			lang := st.Tags.Language
			stream.Lang = &lang
		}
		if st.Tags.Title != "" {
			title := st.Tags.Title
			stream.Title = &title
		}
		if st.Channels != 0 {
			ch := st.Channels
			stream.Channels = &ch
		}
		res.Streams = append(res.Streams, stream)
	}

	if res.Container == "" {
		return Result{}, fmt.Errorf("ffprobe result missing container")
	}
	if len(res.Streams) == 0 {
		return Result{}, fmt.Errorf("ffprobe result has no supported streams")
	}
	return res, nil
}

type ffprobeDoc struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeFormat struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	BitRate    string `json:"bit_rate"`
}

type ffprobeStream struct {
	Index     int    `json:"index"`
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Channels  int    `json:"channels"`
	Tags      struct {
		Language string `json:"language"`
		Title    string `json:"title"`
	} `json:"tags"`
	Disposition struct {
		Default int `json:"default"`
	} `json:"disposition"`
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseInt64(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}
