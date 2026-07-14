package playback

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	keyframeIndexVersion = 2
	keyframeProbeTimeout = 2 * time.Minute
)

// keyframeIndex is the copy-tier timeline. Starts are source presentation
// timestamps for independently decodable video packets; End is the actual end
// of the final video packet, not the often-rounded container duration.
type keyframeIndex struct {
	Version        int       `json:"version"`
	Starts         []float64 `json:"starts"`
	End            float64   `json:"end"`
	TimestampShift float64   `json:"timestamp_shift"`
}

func (idx keyframeIndex) valid() bool {
	if idx.Version != keyframeIndexVersion || len(idx.Starts) == 0 || !finite(idx.End) ||
		!finite(idx.TimestampShift) || idx.TimestampShift < 0 {
		return false
	}
	for i, start := range idx.Starts {
		if !finite(start) || (i > 0 && start <= idx.Starts[i-1]) {
			return false
		}
	}
	return idx.End > idx.Starts[len(idx.Starts)-1]
}

func (idx keyframeIndex) segmentDuration(n int) float64 {
	if n < 0 || n >= len(idx.Starts) {
		return 0
	}
	if n+1 < len(idx.Starts) {
		return idx.Starts[n+1] - idx.Starts[n]
	}
	return idx.End - idx.Starts[n]
}

func (idx keyframeIndex) targetDuration() int {
	longest := 0.0
	for n := range idx.Starts {
		longest = math.Max(longest, idx.segmentDuration(n))
	}
	target := int(math.Ceil(longest))
	if target < 1 {
		return 1
	}
	return target
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func loadOrProbeKeyframes(ctx context.Context, cacheDir, ffprobe, source string, file MediaFile) (keyframeIndex, error) {
	path := keyframeIndexPath(cacheDir, file)
	if idx, err := readKeyframeIndex(path); err == nil {
		return idx, nil
	}

	idx, err := probeKeyframes(ctx, ffprobe, source, file.DurationS)
	if err != nil {
		return keyframeIndex{}, err
	}
	if err := writeKeyframeIndex(path, idx); err != nil {
		return keyframeIndex{}, fmt.Errorf("cache keyframe index: %w", err)
	}
	return idx, nil
}

func keyframeIndexPath(cacheDir string, file MediaFile) string {
	identity := fmt.Sprintf("%d\x00%s\x00%.6f", file.ID, file.Fingerprint, file.DurationS)
	sum := sha256.Sum256([]byte(identity))
	dir := "_keyframes-v" + strconv.Itoa(keyframeIndexVersion) + "-" + hex.EncodeToString(sum[:])[:16]
	return filepath.Join(cacheDir, strconv.FormatInt(file.ID, 10), dir, "index.json")
}

func readKeyframeIndex(path string) (keyframeIndex, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return keyframeIndex{}, err
	}
	var idx keyframeIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return keyframeIndex{}, err
	}
	if !idx.valid() {
		return keyframeIndex{}, fmt.Errorf("invalid keyframe index")
	}
	return idx, nil
}

func writeKeyframeIndex(path string, idx keyframeIndex) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "index-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := json.NewEncoder(tmp).Encode(idx); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func probeKeyframes(ctx context.Context, ffprobe, source string, fallbackDuration float64) (keyframeIndex, error) {
	if source == "" {
		return keyframeIndex{}, fmt.Errorf("source path is required for keyframe indexing")
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	runCtx, cancel := context.WithTimeout(ctx, keyframeProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ffprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_packets",
		"-show_entries", "packet=pts_time,dts_time,duration_time,flags",
		"-of", "compact=p=0:nk=0",
		source,
	)
	cmd.WaitDelay = defaultWaitDelay
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return keyframeIndex{}, err
	}
	stderr := newTailBuffer(16 * 1024)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return keyframeIndex{}, err
	}
	idx, parseErr := parseKeyframeIndex(stdout, fallbackDuration)
	waitErr := cmd.Wait()
	if runCtx.Err() == context.DeadlineExceeded {
		return keyframeIndex{}, fmt.Errorf("ffprobe keyframe scan timed out after %s: %w", keyframeProbeTimeout, context.DeadlineExceeded)
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return keyframeIndex{}, fmt.Errorf("ffprobe keyframe scan: %s", msg)
	}
	if parseErr != nil {
		return keyframeIndex{}, parseErr
	}
	return idx, nil
}

func parseKeyframeIndex(r io.Reader, fallbackDuration float64) (keyframeIndex, error) {
	scanner := bufio.NewScanner(r)
	starts := make([]float64, 0, 1024)
	end := 0.0
	lastPTS := 0.0
	previousPTS := 0.0
	havePTS := false
	lastStep := 0.0
	leadingDecodeDelay := 0.0
	haveDTS := false

	for scanner.Scan() {
		var pts, duration float64
		var havePacketPTS, havePacketDTS bool
		flags := ""
		for _, field := range strings.Split(scanner.Text(), "|") {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "pts_time":
				pts, havePacketPTS = parseFiniteFloat(value)
			case "dts_time":
				_, havePacketDTS = parseFiniteFloat(value)
			case "duration_time":
				duration, _ = parseFiniteFloat(value)
			case "flags":
				flags = value
			}
		}
		if !havePacketPTS {
			continue
		}
		if !haveDTS {
			if havePacketDTS {
				haveDTS = true
			} else if duration > 0 {
				leadingDecodeDelay += duration
			}
		}
		if havePTS && pts > previousPTS {
			lastStep = pts - previousPTS
		}
		if !havePTS || pts > lastPTS {
			lastPTS = pts
		}
		previousPTS = pts
		havePTS = true
		if duration > 0 {
			end = math.Max(end, pts+duration)
		}
		if strings.Contains(flags, "K") {
			starts = append(starts, pts)
		}
	}
	if err := scanner.Err(); err != nil {
		return keyframeIndex{}, fmt.Errorf("read ffprobe keyframe output: %w", err)
	}

	sort.Float64s(starts)
	starts = compactTimestamps(starts)
	if end <= lastPTS && lastStep > 0 {
		end = lastPTS + lastStep
	}
	if end <= 0 && fallbackDuration > 0 {
		end = fallbackDuration
	}
	if !haveDTS {
		leadingDecodeDelay = 0
	}
	idx := keyframeIndex{
		Version:        keyframeIndexVersion,
		Starts:         starts,
		End:            end,
		TimestampShift: leadingDecodeDelay,
	}
	if !idx.valid() {
		return keyframeIndex{}, fmt.Errorf("ffprobe returned no usable video keyframe timeline")
	}
	return idx, nil
}

func compactTimestamps(in []float64) []float64 {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, value := range in[1:] {
		if value > out[len(out)-1]+1e-6 {
			out = append(out, value)
		}
	}
	return out
}

func parseFiniteFloat(raw string) (float64, bool) {
	v, err := strconv.ParseFloat(raw, 64)
	return v, err == nil && finite(v)
}
