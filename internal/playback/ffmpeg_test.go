package playback

import (
	"slices"
	"strconv"
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
	assertContainsSequence(t, remux, "-map", "0:a:0?")
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

func TestBuildFFmpegArgsAudioPick(t *testing.T) {
	file := MediaFile{ID: 1, DurationS: 60, Width: 1920, Height: 1080}
	args := BuildFFmpegArgs(FFmpegRequest{
		SourcePath: "/media/movie.mkv",
		OutputDir:  "/cache",
		File:       file,
		Decision: Decision{
			Tier:      TierRemux,
			Reason:    ReasonAudioTrackSelection,
			AudioPick: &Stream{StreamIndex: 2, Kind: "audio", Codec: "aac"},
		},
	})
	assertContainsSequence(t, args, "-map", "0:2")
	assertContainsSequence(t, args, "-c:a", "copy")

	burnIn := BuildFFmpegArgs(FFmpegRequest{
		SourcePath: "/media/movie.mkv",
		OutputDir:  "/cache",
		File:       file,
		Decision: Decision{
			Tier:      TierFullTranscode,
			Reason:    ReasonSubtitleBurnIn,
			BurnIn:    &Stream{StreamIndex: 4, Kind: "subtitle", Codec: "hdmv_pgs_subtitle"},
			AudioPick: &Stream{StreamIndex: 2, Kind: "audio", Codec: "aac"},
		},
	})
	assertContainsSequence(t, burnIn, "-map", "0:2")
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

const (
	ladderScale1080 = "scale=w=1920:h=1080:force_original_aspect_ratio=decrease:force_divisible_by=2"
	ladderScale720  = "scale=w=1280:h=720:force_original_aspect_ratio=decrease:force_divisible_by=2"
	ladderScale480  = "scale=w=854:h=480:force_original_aspect_ratio=decrease:force_divisible_by=2"
	ladderScale360  = "scale=w=640:h=360:force_original_aspect_ratio=decrease:force_divisible_by=2"
)

// ladderTail is everything from the shared timestamp flags onward. The HLS
// muxer's %v expands to the rung name in the segment and playlist paths, and
// the init filename resolves against each rung's own directory — both verified
// against ffmpeg 9.0.1 before these goldens were written.
func ladderTail(initName, varStreamMap string, startNumber int) string {
	return strings.Join([]string{
		"-force_key_frames", "expr:gte(t,n_forced*4)",
		"-copyts", "-start_at_zero", "-avoid_negative_ts", "make_non_negative",
		"-f", "hls",
		"-hls_time", "4",
		"-hls_playlist_type", "vod",
		"-hls_segment_type", "fmp4",
		"-hls_flags", "independent_segments+temp_file",
		"-hls_segment_options", "movflags=+frag_discont",
		"-hls_fmp4_init_filename", initName,
		"-var_stream_map", varStreamMap,
		"-hls_segment_filename", "/cache/%v/seg-%05d.m4s",
		"-start_number", strconv.Itoa(startNumber),
		"/cache/%v/stream.m3u8",
	}, "\n")
}

func TestBuildFFmpegArgsLadderOneRung(t *testing.T) {
	file := MediaFile{ID: 1, DurationS: 60, Width: 1920, Height: 1080}
	offered := OfferedRungs(file, videoAndAudio)
	args := BuildFFmpegArgs(FFmpegRequest{
		SourcePath:   "/media/movie.mkv",
		OutputDir:    "/cache",
		File:         file,
		Capabilities: Capabilities{VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}},
		Decision: Decision{
			Mode: ModeHLS, Reason: ReasonQuality, Tier: TierLadder,
			Quality: "480p", Rungs: RungsFor("480p", offered),
		},
		StartSegment: 5,
	})
	assertArgs(t, args, strings.Join([]string{
		"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-ss", "20.000",
		"-i", "/media/movie.mkv",
		"-filter_complex", "[0:v:0]split=1[s0];[s0]" + ladderScale480 + "[v0]",
		"-map", "[v0]", "-map", "0:a:0?",
		"-c:v:0", "h264_videotoolbox", "-b:v:0", "1500k", "-profile:v:0", "high",
		"-c:a", "aac", "-b:a", "192k", "-ac", "2",
		ladderTail("init.mp4", "v:0,a:0,name:480p", 5),
	}, "\n"))
}

func TestBuildFFmpegArgsLadderFourRungs(t *testing.T) {
	file := MediaFile{ID: 1, DurationS: 60, Width: 1920, Height: 1080}
	args := BuildFFmpegArgs(FFmpegRequest{
		SourcePath:   "/media/movie.mkv",
		OutputDir:    "/cache",
		File:         file,
		Capabilities: Capabilities{VideoCodecs: []string{"hevc"}, AudioCodecs: []string{"aac"}, MaxHeight: 1080},
		Decision: Decision{
			Mode: ModeHLS, Reason: ReasonQuality, Tier: TierLadder,
			Quality: QualityAuto, Rungs: OfferedRungs(file, videoAndAudio),
		},
	})
	assertArgs(t, args, strings.Join([]string{
		"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-i", "/media/movie.mkv",
		"-filter_complex", "[0:v:0]split=4[s0][s1][s2][s3]" +
			";[s0]" + ladderScale1080 + "[v0]" +
			";[s1]" + ladderScale720 + "[v1]" +
			";[s2]" + ladderScale480 + "[v2]" +
			";[s3]" + ladderScale360 + "[v3]",
		"-map", "[v0]", "-map", "0:a:0?",
		"-map", "[v1]", "-map", "0:a:0?",
		"-map", "[v2]", "-map", "0:a:0?",
		"-map", "[v3]", "-map", "0:a:0?",
		"-c:v:0", "hevc_videotoolbox", "-b:v:0", "6000k", "-profile:v:0", "main", "-tag:v:0", "hvc1",
		"-c:v:1", "hevc_videotoolbox", "-b:v:1", "3000k", "-profile:v:1", "main", "-tag:v:1", "hvc1",
		"-c:v:2", "hevc_videotoolbox", "-b:v:2", "1500k", "-profile:v:2", "main", "-tag:v:2", "hvc1",
		"-c:v:3", "hevc_videotoolbox", "-b:v:3", "800k", "-profile:v:3", "main", "-tag:v:3", "hvc1",
		"-c:a", "aac", "-b:a", "192k", "-ac", "2",
		ladderTail("../%v/init.mp4", "v:0,a:0,name:1080p v:1,a:1,name:720p v:2,a:2,name:480p v:3,a:3,name:360p", 0),
	}, "\n"))
}

func TestBuildFFmpegArgsLadderBurnIn(t *testing.T) {
	file := MediaFile{ID: 1, DurationS: 60, Width: 1920, Height: 1080}
	args := BuildFFmpegArgs(FFmpegRequest{
		SourcePath:   "/media/movie.mkv",
		OutputDir:    "/cache",
		File:         file,
		Capabilities: Capabilities{VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}},
		Decision: Decision{
			Mode: ModeHLS, Reason: ReasonQuality, Tier: TierLadder,
			Quality: QualityAuto, Rungs: OfferedRungs(file, videoAndAudio),
			BurnIn: &Stream{StreamIndex: 4, Kind: "subtitle", Codec: "hdmv_pgs_subtitle"},
		},
	})
	assertArgs(t, args, strings.Join([]string{
		"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-i", "/media/movie.mkv",
		"-filter_complex", "[0:v:0][0:4]overlay,split=4[s0][s1][s2][s3]" +
			";[s0]" + ladderScale1080 + "[v0]" +
			";[s1]" + ladderScale720 + "[v1]" +
			";[s2]" + ladderScale480 + "[v2]" +
			";[s3]" + ladderScale360 + "[v3]",
		"-map", "[v0]", "-map", "0:a:0?",
		"-map", "[v1]", "-map", "0:a:0?",
		"-map", "[v2]", "-map", "0:a:0?",
		"-map", "[v3]", "-map", "0:a:0?",
		"-c:v:0", "h264_videotoolbox", "-b:v:0", "6000k", "-profile:v:0", "high",
		"-c:v:1", "h264_videotoolbox", "-b:v:1", "3000k", "-profile:v:1", "high",
		"-c:v:2", "h264_videotoolbox", "-b:v:2", "1500k", "-profile:v:2", "high",
		"-c:v:3", "h264_videotoolbox", "-b:v:3", "800k", "-profile:v:3", "high",
		"-c:a", "aac", "-b:a", "192k", "-ac", "2",
		ladderTail("../%v/init.mp4", "v:0,a:0,name:1080p v:1,a:1,name:720p v:2,a:2,name:480p v:3,a:3,name:360p", 0),
	}, "\n"))
}

func TestBuildFFmpegArgsLadderAudioPick(t *testing.T) {
	file := MediaFile{ID: 2, DurationS: 60, Width: 1280, Height: 720}
	args := BuildFFmpegArgs(FFmpegRequest{
		SourcePath:   "/media/movie.mkv",
		OutputDir:    "/cache",
		File:         file,
		Capabilities: Capabilities{VideoCodecs: []string{"hevc"}, AudioCodecs: []string{"aac"}},
		Decision: Decision{
			Mode: ModeHLS, Reason: ReasonQuality, Tier: TierLadder,
			Quality: QualityAuto, Rungs: OfferedRungs(file, videoAndAudio),
			AudioPick: &Stream{StreamIndex: 3, Kind: "audio", Codec: "aac"},
		},
	})
	assertArgs(t, args, strings.Join([]string{
		"-hide_banner", "-nostdin", "-y", "-v", "error",
		"-i", "/media/movie.mkv",
		"-filter_complex", "[0:v:0]split=3[s0][s1][s2]" +
			";[s0]" + ladderScale720 + "[v0]" +
			";[s1]" + ladderScale480 + "[v1]" +
			";[s2]" + ladderScale360 + "[v2]",
		"-map", "[v0]", "-map", "0:3",
		"-map", "[v1]", "-map", "0:3",
		"-map", "[v2]", "-map", "0:3",
		"-c:v:0", "hevc_videotoolbox", "-b:v:0", "3000k", "-profile:v:0", "main", "-tag:v:0", "hvc1",
		"-c:v:1", "hevc_videotoolbox", "-b:v:1", "1500k", "-profile:v:1", "main", "-tag:v:1", "hvc1",
		"-c:v:2", "hevc_videotoolbox", "-b:v:2", "800k", "-profile:v:2", "main", "-tag:v:2", "hvc1",
		"-c:a", "aac", "-b:a", "192k", "-ac", "2",
		ladderTail("../%v/init.mp4", "v:0,a:0,name:720p v:1,a:1,name:480p v:2,a:2,name:360p", 0),
	}, "\n"))
}

func assertArgs(t *testing.T, got []string, want string) {
	t.Helper()
	if joined := strings.Join(got, "\n"); joined != want {
		t.Fatalf("args =\n%s\n\nwant\n%s", joined, want)
	}
}
