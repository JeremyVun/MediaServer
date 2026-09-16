package playback

import (
	"errors"
	"testing"
)

func TestDecideMatrix(t *testing.T) {
	channels := 2
	baseFile := MediaFile{ID: 1, Container: "mov", DurationS: 600, Width: 1920, Height: 1080}
	baseStreams := []Stream{
		{StreamIndex: 0, Kind: "video", Codec: "h264"},
		{StreamIndex: 1, Kind: "audio", Codec: "aac", Channels: &channels, IsDefault: true},
	}
	baseCaps := Capabilities{
		Containers:  []string{"mp4"},
		VideoCodecs: []string{"h264"},
		AudioCodecs: []string{"aac"},
		MaxHeight:   2160,
		NativeHLS:   true,
	}

	multiAudioStreams := []Stream{
		{StreamIndex: 0, Kind: "video", Codec: "h264"},
		{StreamIndex: 1, Kind: "audio", Codec: "aac", Channels: &channels, IsDefault: true},
		{StreamIndex: 2, Kind: "audio", Codec: "aac", Channels: &channels},
	}

	tests := []struct {
		name     string
		file     MediaFile
		streams  []Stream
		caps     Capabilities
		subtitle *int
		audio    *int
		wantMode string
		wantTier string
		wantWhy  string
	}{
		{
			name:     "direct mp4 family",
			file:     baseFile,
			streams:  baseStreams,
			caps:     baseCaps,
			wantMode: ModeDirect,
			wantTier: TierDirect,
		},
		{
			name:     "remux unsupported container with supported codecs",
			file:     MediaFile{ID: 1, Container: "matroska", DurationS: 600, Width: 1920, Height: 1080},
			streams:  baseStreams,
			caps:     baseCaps,
			wantMode: ModeHLS,
			wantTier: TierRemux,
			wantWhy:  ReasonContainerUnsupported,
		},
		{
			name: "audio transcode unsupported audio",
			file: baseFile,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "h264"},
				{StreamIndex: 1, Kind: "audio", Codec: "dts", Channels: &channels, IsDefault: true},
			},
			caps:     baseCaps,
			wantMode: ModeHLS,
			wantTier: TierAudioTranscode,
			wantWhy:  ReasonAudioCodec,
		},
		{
			name: "video transcode unsupported video",
			file: baseFile,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "hevc"},
				{StreamIndex: 1, Kind: "audio", Codec: "aac", Channels: &channels, IsDefault: true},
			},
			caps:     baseCaps,
			wantMode: ModeHLS,
			wantTier: TierFullTranscode,
			wantWhy:  ReasonVideoCodec,
		},
		{
			name:     "video transcode above max height",
			file:     MediaFile{ID: 1, Container: "mov", DurationS: 600, Width: 3840, Height: 2160},
			streams:  baseStreams,
			caps:     Capabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxHeight: 1080},
			wantMode: ModeHLS,
			wantTier: TierFullTranscode,
			wantWhy:  ReasonVideoCodec,
		},
		{
			name: "text subtitle does not force transcode",
			file: baseFile,
			streams: append(append([]Stream{}, baseStreams...),
				Stream{StreamIndex: 2, Kind: "subtitle", Codec: "subrip"}),
			caps:     baseCaps,
			subtitle: intPtr(2),
			wantMode: ModeDirect,
			wantTier: TierDirect,
		},
		{
			name: "image subtitle forces burn in",
			file: baseFile,
			streams: append(append([]Stream{}, baseStreams...),
				Stream{StreamIndex: 2, Kind: "subtitle", Codec: "hdmv_pgs_subtitle"}),
			caps:     baseCaps,
			subtitle: intPtr(2),
			wantMode: ModeHLS,
			wantTier: TierFullTranscode,
			wantWhy:  ReasonSubtitleBurnIn,
		},
		{
			name:     "audio selection forces remux of a direct-playable file",
			file:     baseFile,
			streams:  multiAudioStreams,
			caps:     baseCaps,
			audio:    intPtr(2),
			wantMode: ModeHLS,
			wantTier: TierRemux,
			wantWhy:  ReasonAudioTrackSelection,
		},
		{
			name: "audio selection with unsupported codec transcodes audio",
			file: baseFile,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "h264"},
				{StreamIndex: 1, Kind: "audio", Codec: "aac", Channels: &channels, IsDefault: true},
				{StreamIndex: 2, Kind: "audio", Codec: "dts", Channels: &channels},
			},
			caps:     baseCaps,
			audio:    intPtr(2),
			wantMode: ModeHLS,
			wantTier: TierAudioTranscode,
			wantWhy:  ReasonAudioCodec,
		},
		{
			name: "audio selection ignores other unsupported audio streams",
			file: baseFile,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "h264"},
				{StreamIndex: 1, Kind: "audio", Codec: "dts", Channels: &channels, IsDefault: true},
				{StreamIndex: 2, Kind: "audio", Codec: "aac", Channels: &channels},
			},
			caps:     baseCaps,
			audio:    intPtr(2),
			wantMode: ModeHLS,
			wantTier: TierRemux,
			wantWhy:  ReasonAudioTrackSelection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.file, tt.streams, tt.caps, tt.subtitle, tt.audio)
			if got.Mode != tt.wantMode || got.Tier != tt.wantTier || got.Reason != tt.wantWhy {
				t.Fatalf("Decide() = mode=%q tier=%q reason=%q", got.Mode, got.Tier, got.Reason)
			}
			if tt.audio != nil && (got.AudioPick == nil || got.AudioPick.StreamIndex != *tt.audio) {
				t.Fatalf("Decide() AudioPick = %+v, want stream %d", got.AudioPick, *tt.audio)
			}
		})
	}
}

func TestProfileHashVariesWithAudioSelection(t *testing.T) {
	file := MediaFile{ID: 1, Container: "matroska", DurationS: 600, Width: 1920, Height: 1080}
	caps := Capabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}}
	decision := Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux}

	base := ProfileHash(file, caps, decision, nil, nil)
	if again := ProfileHash(file, caps, decision, nil, nil); again != base {
		t.Fatalf("hash not stable: %q vs %q", base, again)
	}
	if picked := ProfileHash(file, caps, decision, nil, intPtr(2)); picked == base {
		t.Fatal("audio selection did not change the profile hash")
	}
}

func intPtr(v int) *int {
	return &v
}

func TestDecideQuality(t *testing.T) {
	file := MediaFile{ID: 1, Container: "mov", DurationS: 600, Width: 1920, Height: 1080}
	caps := Capabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxHeight: 2160}
	streams := []Stream{
		{StreamIndex: 0, Kind: "video", Codec: "h264"},
		{StreamIndex: 1, Kind: "audio", Codec: "aac", IsDefault: true},
	}

	original := Decide(file, streams, caps, nil, nil)
	for _, quality := range []string{"", QualityOriginal} {
		got, err := DecideQuality(quality, file, streams, caps, nil, nil)
		if err != nil {
			t.Fatalf("DecideQuality(%q): %v", quality, err)
		}
		if got.Quality != QualityOriginal || got.Mode != original.Mode || got.Tier != original.Tier || len(got.Rungs) != 0 {
			t.Fatalf("DecideQuality(%q) = %+v, want today's %+v", quality, got, original)
		}
	}

	auto, err := DecideQuality(QualityAuto, file, streams, caps, nil, nil)
	if err != nil {
		t.Fatalf("DecideQuality(auto): %v", err)
	}
	if auto.Mode != ModeHLS || auto.Tier != TierLadder || auto.Reason != ReasonQuality || len(auto.Rungs) != 4 {
		t.Fatalf("DecideQuality(auto) = %+v", auto)
	}

	fixed, err := DecideQuality("720p", file, streams, caps, nil, nil)
	if err != nil {
		t.Fatalf("DecideQuality(720p): %v", err)
	}
	if fixed.Quality != "720p" || len(fixed.Rungs) != 1 || fixed.Rungs[0].ID != "720p" {
		t.Fatalf("DecideQuality(720p) = %+v", fixed)
	}

	// A picked audio track and an image subtitle still reach the ladder's
	// single ffmpeg, which overlays before the split.
	withPicks := append(append([]Stream{}, streams...),
		Stream{StreamIndex: 2, Kind: "audio", Codec: "aac"},
		Stream{StreamIndex: 3, Kind: "subtitle", Codec: "hdmv_pgs_subtitle"})
	picked, err := DecideQuality(QualityAuto, file, withPicks, caps, intPtr(3), intPtr(2))
	if err != nil {
		t.Fatalf("DecideQuality(auto, picks): %v", err)
	}
	if picked.BurnIn == nil || picked.BurnIn.StreamIndex != 3 || picked.AudioPick == nil || picked.AudioPick.StreamIndex != 2 {
		t.Fatalf("DecideQuality(auto, picks) = %+v", picked)
	}

	// A file with nothing to offer resolves every quality to original.
	audioOnly := MediaFile{ID: 2, Container: "mov", DurationS: 600}
	silent, err := DecideQuality(QualityAuto, audioOnly, []Stream{{StreamIndex: 0, Kind: "audio", Codec: "aac"}}, caps, nil, nil)
	if err != nil {
		t.Fatalf("DecideQuality(auto, audio only): %v", err)
	}
	if silent.Quality != QualityOriginal || silent.Tier == TierLadder {
		t.Fatalf("DecideQuality(auto, audio only) = %+v", silent)
	}

	if _, err := DecideQuality("1440p", file, streams, caps, nil, nil); !errors.Is(err, ErrUnknownQuality) {
		t.Fatalf("DecideQuality(1440p) error = %v, want ErrUnknownQuality", err)
	}
}

// Ladder caching must not disturb what is already on disk: these hashes are the
// values main produced before the ladder existed.
func TestProfileHashUnchangedForExistingTiers(t *testing.T) {
	file := MediaFile{ID: 42, Container: "matroska", DurationS: 1234.5, Width: 1920, Height: 1080, Fingerprint: "abc123"}
	caps := Capabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxHeight: 1080, NativeHLS: true}

	full := Decision{Mode: ModeHLS, Reason: ReasonVideoCodec, Tier: TierFullTranscode}
	if got := ProfileHash(file, caps, full, nil, nil); got != "70a4bbe24c199bb0" {
		t.Fatalf("full transcode hash = %q, want 70a4bbe24c199bb0", got)
	}
	remux := Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux}
	if got := ProfileHash(file, caps, remux, nil, intPtr(3)); got != "f8323a6a05127fb3" {
		t.Fatalf("remux hash = %q, want f8323a6a05127fb3", got)
	}
}

// Ladders size themselves, so devices differing only in max_height share one
// cache entry — but a different ladder must not.
func TestProfileHashIgnoresMaxHeightForLadders(t *testing.T) {
	file := MediaFile{ID: 42, Container: "matroska", DurationS: 1234.5, Width: 1920, Height: 1080}
	phone := Capabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxHeight: 844}
	tablet := phone
	tablet.MaxHeight = 2160
	streams := []Stream{{StreamIndex: 0, Kind: "video", Codec: "h264"}, {StreamIndex: 1, Kind: "audio", Codec: "aac"}}

	auto, err := DecideQuality(QualityAuto, file, streams, phone, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := ProfileHash(file, phone, auto, nil, nil)
	if other := ProfileHash(file, tablet, auto, nil, nil); other != base {
		t.Fatalf("ladder hash varies with max_height: %q vs %q", base, other)
	}

	fixed, err := DecideQuality("480p", file, streams, phone, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProfileHash(file, phone, fixed, nil, nil); got == base {
		t.Fatal("a one-rung ladder hashes the same as the full ladder")
	}

	full := Decide(file, streams, phone, nil, nil)
	if got := ProfileHash(file, phone, full, nil, nil); got == base {
		t.Fatal("ladder and full transcode share a profile hash")
	}
}

func TestDecideQualityAudioOnly(t *testing.T) {
	video := MediaFile{ID: 1, Container: "mov", DurationS: 600, Width: 1920, Height: 1080}
	caps := Capabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxHeight: 2160}

	tests := []struct {
		name        string
		file        MediaFile
		streams     []Stream
		subtitle    *int
		audio       *int
		wantTier    string
		wantQuality string
		wantPick    int
	}{
		{
			name: "offered alongside rungs",
			file: video,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "h264"},
				{StreamIndex: 1, Kind: "audio", Codec: "aac", IsDefault: true},
			},
			wantTier: TierAudioOnly, wantQuality: QualityAudio, wantPick: 1,
		},
		{
			name:        "silent video",
			file:        video,
			streams:     []Stream{{StreamIndex: 0, Kind: "video", Codec: "h264"}},
			wantTier:    TierDirect,
			wantQuality: QualityOriginal,
			wantPick:    -1,
		},
		{
			name:        "audio file",
			file:        MediaFile{ID: 2, Container: "mp4", DurationS: 600},
			streams:     []Stream{{StreamIndex: 0, Kind: "audio", Codec: "aac", IsDefault: true}},
			wantTier:    TierDirect,
			wantQuality: QualityOriginal,
			wantPick:    -1,
		},
		{
			name: "explicit pick",
			file: video,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "h264"},
				{StreamIndex: 1, Kind: "audio", Codec: "aac", IsDefault: true},
				{StreamIndex: 2, Kind: "audio", Codec: "eac3"},
			},
			audio:    intPtr(2),
			wantTier: TierAudioOnly, wantQuality: QualityAudio, wantPick: 2,
		},
		{
			// The other HLS tiers map 0:a:0, which is this file's second
			// choice. This tier follows the default flag instead.
			name: "default stream second in file order",
			file: video,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "h264"},
				{StreamIndex: 1, Kind: "audio", Codec: "aac"},
				{StreamIndex: 2, Kind: "audio", Codec: "aac", IsDefault: true},
			},
			wantTier: TierAudioOnly, wantQuality: QualityAudio, wantPick: 2,
		},
		{
			name: "no default flag",
			file: video,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "h264"},
				{StreamIndex: 1, Kind: "audio", Codec: "aac"},
				{StreamIndex: 2, Kind: "audio", Codec: "aac"},
			},
			wantTier: TierAudioOnly, wantQuality: QualityAudio, wantPick: 1,
		},
		{
			name: "image subtitle is ignored",
			file: video,
			streams: []Stream{
				{StreamIndex: 0, Kind: "video", Codec: "h264"},
				{StreamIndex: 1, Kind: "audio", Codec: "aac", IsDefault: true},
				{StreamIndex: 2, Kind: "subtitle", Codec: "hdmv_pgs_subtitle"},
			},
			subtitle: intPtr(2),
			wantTier: TierAudioOnly, wantQuality: QualityAudio, wantPick: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecideQuality(QualityAudio, tt.file, tt.streams, caps, tt.subtitle, tt.audio)
			if err != nil {
				t.Fatalf("DecideQuality(audio): %v", err)
			}
			if got.Tier != tt.wantTier || got.Quality != tt.wantQuality {
				t.Fatalf("DecideQuality(audio) = tier=%q quality=%q, want tier=%q quality=%q",
					got.Tier, got.Quality, tt.wantTier, tt.wantQuality)
			}
			if tt.wantTier != TierAudioOnly {
				return
			}
			if got.Mode != ModeHLS || got.Reason != ReasonQuality || len(got.Rungs) != 0 || got.BurnIn != nil || got.VideoCopy || got.AudioCopy {
				t.Fatalf("DecideQuality(audio) = %+v", got)
			}
			if got.AudioPick == nil || got.AudioPick.StreamIndex != tt.wantPick {
				t.Fatalf("DecideQuality(audio) AudioPick = %+v, want stream %d", got.AudioPick, tt.wantPick)
			}
		})
	}
}

// One cache entry per file and audio track: the audio tier encodes no video, so
// the device profile and a burn-in pick cannot change its output.
func TestProfileHashIgnoresDeviceForAudioOnly(t *testing.T) {
	file := MediaFile{ID: 42, Container: "matroska", DurationS: 1234.5, Width: 1920, Height: 1080}
	streams := []Stream{
		{StreamIndex: 0, Kind: "video", Codec: "h264"},
		{StreamIndex: 1, Kind: "audio", Codec: "aac", IsDefault: true},
		{StreamIndex: 2, Kind: "subtitle", Codec: "hdmv_pgs_subtitle"},
	}
	phone := Capabilities{Containers: []string{"mp4"}, VideoCodecs: []string{"h264"}, AudioCodecs: []string{"aac"}, MaxHeight: 844, NativeHLS: true}
	desktop := Capabilities{Containers: []string{"mp4", "matroska"}, VideoCodecs: []string{"h264", "hevc"}, AudioCodecs: []string{"aac", "opus"}, MaxHeight: 2160}

	decide := func(caps Capabilities, subtitle, audio *int) (Decision, string) {
		decision, err := DecideQuality(QualityAudio, file, streams, caps, subtitle, audio)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Tier != TierAudioOnly {
			t.Fatalf("decided tier %q", decision.Tier)
		}
		return decision, ProfileHash(file, caps, decision, subtitle, audio)
	}

	_, base := decide(phone, nil, nil)
	if _, other := decide(desktop, nil, nil); other != base {
		t.Fatalf("audio-only hash varies with capabilities: %q vs %q", base, other)
	}
	if _, burnIn := decide(phone, intPtr(2), nil); burnIn != base {
		t.Fatalf("audio-only hash varies with a burn-in pick: %q vs %q", base, burnIn)
	}
	if _, picked := decide(phone, nil, intPtr(1)); picked == base {
		t.Fatal("audio-only hash ignores the audio track pick")
	}

	ladder, err := DecideQuality(QualityAuto, file, streams, phone, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProfileHash(file, phone, ladder, nil, nil); got == base {
		t.Fatal("the audio tier and the ladder share a profile hash")
	}
}
