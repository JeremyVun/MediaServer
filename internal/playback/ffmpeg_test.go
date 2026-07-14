package playback

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildFFmpegArgsModes(t *testing.T) {
	file := MediaFile{ID: 1, DurationS: 60, Width: 1920, Height: 1080}
	caps := Capabilities{VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxHeight: 1080}

	remux := BuildFFmpegArgs(FFmpegRequest{
		SourcePath: "/media/movie.mkv",
		OutputDir:  "/cache",
		File:       file,
		Decision:   Decision{Tier: TierRemux},
	})
	assertContainsSequence(t, remux, "-c:v", "copy")
	assertContainsSequence(t, remux, "-c:a", "copy")
	assertContainsSequence(t, remux, "-hls_segment_type", "fmp4")
	assertContainsSequence(t, remux, "-hls_segment_filename", "/cache/seg-%05d.m4s")
	// Copy tiers close every segment on a real source keyframe. The server's
	// indexed playlist advertises the matching variable durations.
	assertContainsSequence(t, remux, "-hls_time", "0.01")
	assertContainsSequence(t, remux, "-hls_flags", "independent_segments+temp_file")
	assertContainsSequence(t, remux, "-copyts", "-start_at_zero")
	assertContainsSequence(t, remux, "-avoid_negative_ts", "make_non_negative")
	assertContainsSequence(t, remux, "-hls_segment_options", "movflags=+frag_discont")

	audio := BuildFFmpegArgs(FFmpegRequest{
		SourcePath: "/media/movie.mkv",
		OutputDir:  "/cache",
		File:       file,
		Decision:   Decision{Tier: TierAudioTranscode},
	})
	assertContainsSequence(t, audio, "-c:v", "copy")
	assertContainsSequence(t, audio, "-c:a", "aac")
	assertContainsSequence(t, audio, "-b:a", "192k")

	full := BuildFFmpegArgs(FFmpegRequest{
		SourcePath:   "/media/movie.mkv",
		OutputDir:    "/cache",
		File:         file,
		Capabilities: caps,
		Decision:     Decision{Tier: TierFullTranscode},
		StartSegment: 3,
	})
	assertContainsSequence(t, full, "-ss", "12.000")
	assertContainsSequence(t, full, "-c:v", "h264_videotoolbox")
	assertContainsSequence(t, full, "-b:v", "6000k")
	assertContainsSequence(t, full, "-start_number", "3")
	assertContainsSequence(t, full, "-hls_flags", "independent_segments+temp_file")
	assertContainsSequence(t, full, "-copyts", "-start_at_zero")
}

func TestBuildFFmpegArgsBurnInAndHEVC(t *testing.T) {
	file := MediaFile{ID: 1, DurationS: 60, Width: 3840, Height: 2160}
	decision := Decision{
		Tier:   TierFullTranscode,
		Reason: ReasonSubtitleBurnIn,
		BurnIn: &Stream{StreamIndex: 4, Kind: "subtitle", Codec: "hdmv_pgs_subtitle"},
	}
	args := BuildFFmpegArgs(FFmpegRequest{
		SourcePath:   "/media/movie.mkv",
		OutputDir:    "/cache",
		File:         file,
		Capabilities: Capabilities{VideoCodecs: []string{"hevc"}, MaxHeight: 1080},
		Decision:     decision,
	})
	assertContainsSequence(t, args, "-filter_complex", "[0:v:0][0:4]overlay,scale=-2:1080[v]")
	assertContainsSequence(t, args, "-map", "[v]")
	assertContainsSequence(t, args, "-c:v", "hevc_videotoolbox")
	assertContainsSequence(t, args, "-tag:v", "hvc1")
}

func assertContainsSequence(t *testing.T, args []string, seq ...string) {
	t.Helper()
	for i := 0; i <= len(args)-len(seq); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return
		}
	}
	t.Fatalf("args do not contain %s:\n%s", strings.Join(seq, " "), strings.Join(args, " "))
}
