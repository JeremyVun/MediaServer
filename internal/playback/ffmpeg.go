package playback

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSegmentDuration = 4 * time.Second
	defaultWaitDelay       = 5 * time.Second
)

type FFmpegRequest struct {
	Binary       string
	SourcePath   string
	OutputDir    string
	Decision     Decision
	Capabilities Capabilities
	File         MediaFile
	Streams      []Stream
	StartSegment int
	// SeekSeconds is the -ss position for restarted runs. It can sit below
	// StartSegment*SegmentDuration when the copy tiers align the seek to the
	// source keyframe that StartSegment's grid slot contains. Zero means
	// derive it from StartSegment.
	SeekSeconds float64
	// TimestampOffset reproduces the make_non_negative shift from a run that
	// started at the beginning when this run starts at a later keyframe.
	TimestampOffset float64
	SegmentDuration time.Duration
}

func BuildFFmpegArgs(req FFmpegRequest) []string {
	segmentDuration := req.SegmentDuration
	if segmentDuration <= 0 {
		segmentDuration = DefaultSegmentDuration
	}
	segmentSeconds := int(math.Round(segmentDuration.Seconds()))
	if segmentSeconds < 1 {
		segmentSeconds = 4
	}

	args := []string{
		"-hide_banner",
		"-nostdin",
		"-y",
		"-v", "error",
	}
	seekS := req.SeekSeconds
	if seekS <= 0 && req.StartSegment > 0 {
		seekS = float64(req.StartSegment) * segmentDuration.Seconds()
	}
	if seekS > 0 {
		args = append(args, "-ss", strconv.FormatFloat(seekS, 'f', 3, 64))
	}
	args = append(args, "-i", req.SourcePath)

	switch req.Decision.Tier {
	case TierLadder:
		args = append(args, ladderEncodeArgs(req)...)
	case TierAudioOnly:
		args = append(args, audioOnlyArgs(req, seekS)...)
	default:
		args = append(args, singleOutputArgs(req)...)
	}

	if videoEncodingTier(req.Decision.Tier) {
		args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", segmentSeconds))
	}

	// The synthesized VOD playlist maps segment n to [n*4s, (n+1)*4s), and MSE
	// places fragments by their embedded timestamps — so those timestamps must
	// stay absolute across -ss restarts. -copyts keeps source timestamps
	// (-start_at_zero normalizes containers with a nonzero start);
	// make_non_negative only shifts codec-delay negatives, never a seeked run.
	args = append(args,
		"-copyts",
		"-start_at_zero",
		"-avoid_negative_ts", "make_non_negative",
	)
	if req.TimestampOffset > 0 {
		args = append(args, "-output_ts_offset", strconv.FormatFloat(req.TimestampOffset, 'f', 6, 64))
	}

	// Copy tiers cut at every source keyframe. Their server-generated playlist
	// uses the same indexed keyframes and variable durations, so every segment
	// is independently decodable without lying about the timeline. A tiny HLS
	// target makes the muxer close a segment at each next keyframe. Full
	// transcodes retain the fixed grid created by -force_key_frames.
	hlsTime := strconv.Itoa(segmentSeconds)
	hlsFlags := "independent_segments+temp_file"
	if !fixedGridTier(req.Decision.Tier) {
		hlsTime = "0.01"
	}

	args = append(args,
		"-f", "hls",
		"-hls_time", hlsTime,
		"-hls_playlist_type", "vod",
		"-hls_segment_type", "fmp4",
		"-hls_flags", hlsFlags,
		// frag_discont writes absolute tfdt times instead of rebasing each run
		// to zero behind an init.mp4 edit list the client never re-fetches.
		"-hls_segment_options", "movflags=+frag_discont",
	)
	if req.Decision.Tier == TierLadder {
		return append(args,
			"-hls_fmp4_init_filename", ladderInitFilename(len(req.Decision.Rungs)),
			"-var_stream_map", varStreamMap(req.Decision.Rungs),
			"-hls_segment_filename", filepath.Join(req.OutputDir, "%v", "seg-%05d.m4s"),
			"-start_number", strconv.Itoa(req.StartSegment),
			filepath.Join(req.OutputDir, "%v", "stream.m3u8"),
		)
	}
	args = append(args,
		"-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(req.OutputDir, "seg-%05d.m4s"),
		"-start_number", strconv.Itoa(req.StartSegment),
		filepath.Join(req.OutputDir, "stream.m3u8"),
	)
	return args
}

// fixedGridTier covers the tiers the muxer cuts on the 4 s grid. Copy tiers cut
// at source keyframes instead.
func fixedGridTier(tier string) bool {
	return tier == TierFullTranscode || tier == TierLadder || tier == TierAudioOnly
}

// audioOnlyArgs encodes the picked stream alone, mixed under silence that runs
// to the container duration. The catalog records the container duration, so the
// playlist advertises D/4 segments; a file whose audio ends early would stop
// short of them and hang the player at the tail. The silence is amix's first
// input because amix clocks its output from it, and asetpts lifts lavfi's
// zero-based timestamps to the restart position so every run lands on the same
// grid. normalize=0 keeps amix from attenuating the mix, and aformat pins the
// output rate so the grid tolerance is one AAC frame at 48 kHz.
func audioOnlyArgs(req FFmpegRequest, seekS float64) []string {
	source := audioFilterInput(req.Decision)
	convert := "aformat=sample_rates=48000:channel_layouts=stereo"
	var args []string
	graph := source + convert + "[a]"
	// anullsrc is infinite, so duration=longest would never end without a
	// padding window; a restart at or past the container end has none.
	if pad := req.File.DurationS - seekS; pad > 0 {
		args = append(args,
			"-t", strconv.FormatFloat(pad, 'f', 3, 64),
			"-f", "lavfi",
			"-i", "anullsrc=r=48000:cl=stereo",
		)
		graph = "[1:a]asetpts=PTS+" + strconv.FormatFloat(seekS, 'f', 3, 64) + "/TB[s];" +
			source + convert + "[a0];" +
			"[s][a0]amix=inputs=2:normalize=0:duration=longest[a]"
	}
	args = append(args, "-filter_complex", graph, "-map", "[a]")
	return append(args, audioTranscodeArgs()...)
}

func audioFilterInput(decision Decision) string {
	if decision.AudioPick != nil {
		return "[0:" + strconv.Itoa(decision.AudioPick.StreamIndex) + "]"
	}
	return "[0:a:0]"
}

func singleOutputArgs(req FFmpegRequest) []string {
	audioMap := audioMapSpec(req.Decision)
	var args []string
	if req.Decision.BurnIn != nil {
		filter := fmt.Sprintf("[0:v:0][0:%d]overlay", req.Decision.BurnIn.StreamIndex)
		if req.Capabilities.MaxHeight > 0 && req.File.Height > req.Capabilities.MaxHeight {
			filter += ",scale=-2:" + strconv.Itoa(req.Capabilities.MaxHeight)
		}
		filter += "[v]"
		args = append(args,
			"-filter_complex", filter,
			"-map", "[v]",
			"-map", audioMap,
		)
	} else {
		args = append(args,
			"-map", "0:v:0?",
			"-map", audioMap,
			"-sn",
		)
	}

	switch req.Decision.Tier {
	case TierRemux:
		args = append(args, "-c:v", "copy", "-c:a", "copy")
	case TierAudioTranscode:
		args = append(args, "-c:v", "copy")
		args = append(args, audioTranscodeArgs()...)
	case TierFullTranscode:
		args = append(args, videoTranscodeArgs(req.File, req.Capabilities, req.Decision.BurnIn == nil)...)
		args = append(args, audioTranscodeArgs()...)
	default:
		args = append(args, "-c:v", "copy", "-c:a", "copy")
	}
	return args
}

func audioMapSpec(decision Decision) string {
	if decision.AudioPick != nil {
		return "0:" + strconv.Itoa(decision.AudioPick.StreamIndex)
	}
	return "0:a:0?"
}

// ladderEncodeArgs decodes the source once, overlays burn-in before the split,
// then scales and encodes one output per rung. Each rung muxes its own copy of
// the chosen audio, so every variant is independently playable.
func ladderEncodeArgs(req FFmpegRequest) []string {
	audioMap := audioMapSpec(req.Decision)
	rungs := req.Decision.Rungs
	var graph strings.Builder
	graph.WriteString("[0:v:0]")
	if req.Decision.BurnIn != nil {
		fmt.Fprintf(&graph, "[0:%d]overlay,", req.Decision.BurnIn.StreamIndex)
	}
	graph.WriteString("split=" + strconv.Itoa(len(rungs)))
	for i := range rungs {
		fmt.Fprintf(&graph, "[s%d]", i)
	}
	for i, rung := range rungs {
		fmt.Fprintf(&graph, ";[s%d]scale=w=%d:h=%d:force_original_aspect_ratio=decrease:force_divisible_by=2[v%d]",
			i, rung.ScaleW, rung.ScaleH, i)
	}

	args := []string{"-filter_complex", graph.String()}
	for i := range rungs {
		args = append(args, "-map", fmt.Sprintf("[v%d]", i), "-map", audioMap)
	}
	codec, hevc := ladderVideoCodec(req.Capabilities)
	for i, rung := range rungs {
		n := strconv.Itoa(i)
		args = append(args, "-c:v:"+n, codec, "-b:v:"+n, strconv.Itoa(rung.BitrateK)+"k")
		if hevc {
			args = append(args, "-profile:v:"+n, "main", "-tag:v:"+n, "hvc1")
		} else {
			args = append(args, "-profile:v:"+n, "high")
		}
	}
	return append(args, audioTranscodeArgs()...)
}

func ladderVideoCodec(caps Capabilities) (string, bool) {
	if codecSupported("hevc", caps.VideoCodecs) {
		return "hevc_videotoolbox", true
	}
	return "h264_videotoolbox", false
}

func varStreamMap(rungs []RungOutput) string {
	specs := make([]string, 0, len(rungs))
	for i, rung := range rungs {
		specs = append(specs, fmt.Sprintf("v:%d,a:%d,name:%s", i, i, rung.ID))
	}
	return strings.Join(specs, " ")
}

// ladderInitFilename works around the HLS muxer (verified on ffmpeg 9.0.1):
// with several variants it appends _N unless the name carries %v, and the name
// resolves against each variant's playlist directory; with a single variant it
// never expands %v at all.
func ladderInitFilename(rungs int) string {
	if rungs == 1 {
		return "init.mp4"
	}
	return "../%v/init.mp4"
}

func videoTranscodeArgs(file MediaFile, caps Capabilities, includeScale bool) []string {
	codec := "h264_videotoolbox"
	if codecSupported("hevc", caps.VideoCodecs) {
		codec = "hevc_videotoolbox"
	}
	args := []string{"-c:v", codec}
	if codec == "hevc_videotoolbox" {
		args = append(args, "-tag:v", "hvc1")
	}
	if includeScale && caps.MaxHeight > 0 && file.Height > caps.MaxHeight {
		args = append(args, "-vf", "scale=-2:"+strconv.Itoa(caps.MaxHeight))
	}
	args = append(args, "-b:v", videoBitrate(file.Height, caps.MaxHeight))
	return args
}

func audioTranscodeArgs() []string {
	return []string{"-c:a", "aac", "-b:a", "192k", "-ac", "2"}
}

func videoBitrate(height, maxHeight int) string {
	if maxHeight > 0 && (height == 0 || height > maxHeight) {
		height = maxHeight
	}
	switch {
	case height <= 0:
		return "6000k"
	case height <= 480:
		return "1500k"
	case height <= 720:
		return "3000k"
	case height <= 1080:
		return "6000k"
	default:
		return "10000k"
	}
}

func ffmpegCommand(ctx context.Context, req FFmpegRequest, log *slog.Logger) *exec.Cmd {
	bin := req.Binary
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := exec.CommandContext(ctx, bin, BuildFFmpegArgs(req)...)
	cmd.WaitDelay = defaultWaitDelay
	if log != nil {
		log.Debug("ffmpeg exec", "argv", cmd.Args)
	}
	return cmd
}

type tailBuffer struct {
	buf bytes.Buffer
	max int
}

func newTailBuffer(max int) *tailBuffer {
	if max <= 0 {
		max = 16 * 1024
	}
	return &tailBuffer{max: max}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= b.max {
		b.buf.Reset()
		b.buf.Write(p[n-b.max:])
		return n, nil
	}
	if b.buf.Len()+n > b.max {
		keep := b.buf.Bytes()[b.buf.Len()+n-b.max:]
		copied := append([]byte(nil), keep...)
		b.buf.Reset()
		b.buf.Write(copied)
	}
	b.buf.Write(p)
	return n, nil
}

func (b *tailBuffer) String() string {
	return strings.TrimSpace(b.buf.String())
}
