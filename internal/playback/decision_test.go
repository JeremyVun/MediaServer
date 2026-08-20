package playback

import "testing"

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
