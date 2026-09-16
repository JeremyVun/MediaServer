package playback

import (
	"errors"
	"strings"
	"testing"
)

var videoAndAudio = []Stream{
	{StreamIndex: 0, Kind: "video", Codec: "h264"},
	{StreamIndex: 1, Kind: "audio", Codec: "aac", IsDefault: true},
}

// The output sizes are what ffmpeg's scale filter actually produces (measured
// on ffmpeg 9.0.1). Two differ from the design table, which rounded down where
// the filter rounds to the nearest even: 1920x1080 at 480p is 854x480, not
// 852x480, and 1920x800 at 720p is 1280x534, not 1280x532.
func TestOfferedRungs(t *testing.T) {
	type size struct {
		id string
		w  int
		h  int
	}
	tests := []struct {
		name  string
		file  MediaFile
		want  []size
		lists []Stream
	}{
		{
			name: "4k",
			file: MediaFile{Width: 3840, Height: 2160},
			want: []size{{"1080p", 1920, 1080}, {"720p", 1280, 720}, {"480p", 854, 480}, {"360p", 640, 360}},
		},
		{
			name: "1080p",
			file: MediaFile{Width: 1920, Height: 1080},
			want: []size{{"1080p", 1920, 1080}, {"720p", 1280, 720}, {"480p", 854, 480}, {"360p", 640, 360}},
		},
		{
			name: "scope",
			file: MediaFile{Width: 1920, Height: 800},
			want: []size{{"1080p", 1920, 800}, {"720p", 1280, 534}, {"480p", 854, 356}, {"360p", 640, 266}},
		},
		{
			name: "four by three hd",
			file: MediaFile{Width: 1440, Height: 1080},
			want: []size{{"1080p", 1440, 1080}, {"720p", 960, 720}, {"480p", 640, 480}, {"360p", 480, 360}},
		},
		{
			name: "portrait",
			file: MediaFile{Width: 1080, Height: 1920},
			want: []size{{"1080p", 1080, 1920}, {"720p", 720, 1280}, {"480p", 480, 854}, {"360p", 360, 640}},
		},
		{
			name: "720p source",
			file: MediaFile{Width: 1280, Height: 720},
			want: []size{{"720p", 1280, 720}, {"480p", 854, 480}, {"360p", 640, 360}},
		},
		{
			name: "pal",
			file: MediaFile{Width: 720, Height: 576},
			want: []size{{"720p", 720, 576}, {"480p", 600, 480}, {"360p", 450, 360}},
		},
		{
			name: "tiny",
			file: MediaFile{Width: 320, Height: 240},
			want: []size{{"360p", 320, 240}},
		},
		{
			name: "unprobed dimensions",
			file: MediaFile{Width: 0, Height: 0},
		},
		{
			name:  "audio only",
			file:  MediaFile{Width: 1920, Height: 1080},
			lists: []Stream{{StreamIndex: 0, Kind: "audio", Codec: "aac"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streams := tt.lists
			if streams == nil {
				streams = videoAndAudio
			}
			got := OfferedRungs(tt.file, streams)
			if len(got) != len(tt.want) {
				t.Fatalf("OfferedRungs() = %+v, want %+v", got, tt.want)
			}
			for i, want := range tt.want {
				if got[i].ID != want.id || got[i].Width != want.w || got[i].Height != want.h {
					t.Fatalf("rung %d = %s %dx%d, want %s %dx%d",
						i, got[i].ID, got[i].Width, got[i].Height, want.id, want.w, want.h)
				}
			}
		})
	}
}

// A rotate flag is not probed, so ffmpeg autorotates a phone clip stored as
// 1920x1080 to 1080x1920 and the landscape box fits it to 608x1080 (design
// decision 4, known limit). The scale target, not the predicted size, is what
// the filter receives.
func TestRungScaleTargetIsTheBoxClamp(t *testing.T) {
	rungs := OfferedRungs(MediaFile{Width: 1920, Height: 800}, videoAndAudio)
	for _, rung := range rungs {
		if rung.ID != "480p" {
			continue
		}
		if rung.ScaleW != 854 || rung.ScaleH != 480 {
			t.Fatalf("480p scale target = %dx%d, want 854x480", rung.ScaleW, rung.ScaleH)
		}
		if rung.Width != 854 || rung.Height != 356 {
			t.Fatalf("480p output = %dx%d, want 854x356", rung.Width, rung.Height)
		}
		return
	}
	t.Fatal("480p was not offered")
}

func TestResolveQuality(t *testing.T) {
	full := OfferedRungs(MediaFile{Width: 1920, Height: 1080}, videoAndAudio)
	small := OfferedRungs(MediaFile{Width: 320, Height: 240}, videoAndAudio)
	none := OfferedRungs(MediaFile{}, videoAndAudio)

	tests := []struct {
		name      string
		requested string
		offered   []RungOutput
		want      string
		wantRungs int
	}{
		{name: "omitted", requested: "", offered: full, want: QualityOriginal},
		{name: "original", requested: QualityOriginal, offered: full, want: QualityOriginal},
		{name: "auto", requested: QualityAuto, offered: full, want: QualityAuto, wantRungs: 4},
		{name: "auto with no rungs", requested: QualityAuto, offered: none, want: QualityOriginal},
		{name: "offered fixed size", requested: "720p", offered: full, want: "720p", wantRungs: 1},
		{name: "fixed size resolves down", requested: "1080p", offered: small, want: "360p", wantRungs: 1},
		{name: "fixed size with no rungs", requested: "1080p", offered: none, want: QualityOriginal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveQuality(tt.requested, tt.offered)
			if err != nil {
				t.Fatalf("ResolveQuality(%q) error: %v", tt.requested, err)
			}
			if got != tt.want {
				t.Fatalf("ResolveQuality(%q) = %q, want %q", tt.requested, got, tt.want)
			}
			if rungs := RungsFor(got, tt.offered); len(rungs) != tt.wantRungs {
				t.Fatalf("RungsFor(%q) = %d rungs, want %d", got, len(rungs), tt.wantRungs)
			}
		})
	}

	if _, err := ResolveQuality("4k", full); !errors.Is(err, ErrUnknownQuality) {
		t.Fatalf("ResolveQuality(\"4k\") error = %v, want ErrUnknownQuality", err)
	}
}

// The CODECS strings are the contract the player picks variants on. Phase 2
// checks each declared level against what ffprobe reports in init.mp4.
func TestLadderCodecsTable(t *testing.T) {
	want := map[string][2]string{
		"1080p": {"avc1.64002A,mp4a.40.2", "hvc1.1.6.L123.B0,mp4a.40.2"},
		"720p":  {"avc1.640020,mp4a.40.2", "hvc1.1.6.L120.B0,mp4a.40.2"},
		"480p":  {"avc1.64001F,mp4a.40.2", "hvc1.1.6.L93.B0,mp4a.40.2"},
		"360p":  {"avc1.64001F,mp4a.40.2", "hvc1.1.6.L90.B0,mp4a.40.2"},
	}
	for _, rung := range ladderRungs {
		out := RungOutput{ID: rung.ID}
		if got := out.codecs(false); got != want[rung.ID][0] {
			t.Fatalf("%s h264 codecs = %q, want %q", rung.ID, got, want[rung.ID][0])
		}
		if got := out.codecs(true); got != want[rung.ID][1] {
			t.Fatalf("%s hevc codecs = %q, want %q", rung.ID, got, want[rung.ID][1])
		}
	}
}

func TestLadderBandwidths(t *testing.T) {
	want := map[string][2]int{
		"1080p": {9192000, 6192000},
		"720p":  {4692000, 3192000},
		"480p":  {2442000, 1692000},
		"360p":  {1392000, 992000},
	}
	for _, rung := range ladderRungs {
		out := RungOutput{ID: rung.ID, BitrateK: rung.BitrateK}
		if got := out.peakBandwidth(); got != want[rung.ID][0] {
			t.Fatalf("%s BANDWIDTH = %d, want %d", rung.ID, got, want[rung.ID][0])
		}
		if got := out.averageBandwidth(); got != want[rung.ID][1] {
			t.Fatalf("%s AVERAGE-BANDWIDTH = %d, want %d", rung.ID, got, want[rung.ID][1])
		}
	}
}

func TestMasterPlaylist(t *testing.T) {
	rungs := OfferedRungs(MediaFile{Width: 1920, Height: 1080}, videoAndAudio)
	got := MasterPlaylist(rungs, false)
	want := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-INDEPENDENT-SEGMENTS",
		`#EXT-X-STREAM-INF:BANDWIDTH=4692000,AVERAGE-BANDWIDTH=3192000,RESOLUTION=1280x720,CODECS="avc1.640020,mp4a.40.2"`,
		"720p/stream.m3u8",
		`#EXT-X-STREAM-INF:BANDWIDTH=9192000,AVERAGE-BANDWIDTH=6192000,RESOLUTION=1920x1080,CODECS="avc1.64002A,mp4a.40.2"`,
		"1080p/stream.m3u8",
		`#EXT-X-STREAM-INF:BANDWIDTH=2442000,AVERAGE-BANDWIDTH=1692000,RESOLUTION=854x480,CODECS="avc1.64001F,mp4a.40.2"`,
		"480p/stream.m3u8",
		`#EXT-X-STREAM-INF:BANDWIDTH=1392000,AVERAGE-BANDWIDTH=992000,RESOLUTION=640x360,CODECS="avc1.64001F,mp4a.40.2"`,
		"360p/stream.m3u8",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("master playlist =\n%s\nwant\n%s", got, want)
	}

	// Without 720p the largest offered rung leads instead.
	pal := MasterPlaylist(OfferedRungs(MediaFile{Width: 320, Height: 240}, videoAndAudio), true)
	if !strings.Contains(pal, `CODECS="hvc1.1.6.L90.B0,mp4a.40.2"`) || !strings.Contains(pal, "360p/stream.m3u8") {
		t.Fatalf("hevc single-rung master playlist:\n%s", pal)
	}

	wide := MasterPlaylist(OfferedRungs(MediaFile{Width: 1440, Height: 1080}, videoAndAudio), false)
	if !strings.HasPrefix(firstVariantURI(wide), "720p/") {
		t.Fatalf("first variant = %q, want 720p", firstVariantURI(wide))
	}
}

func firstVariantURI(playlist string) string {
	lines := strings.Split(playlist, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}

func TestAudioOnlyOffered(t *testing.T) {
	video := MediaFile{Width: 1920, Height: 1080}
	tests := []struct {
		name    string
		file    MediaFile
		streams []Stream
		want    bool
	}{
		{name: "video with audio", file: video, streams: videoAndAudio, want: true},
		{name: "silent video", file: video, streams: []Stream{{StreamIndex: 0, Kind: "video", Codec: "h264"}}},
		{name: "audio file", streams: []Stream{{StreamIndex: 0, Kind: "audio", Codec: "aac"}}},
		{name: "video without probed dimensions", streams: videoAndAudio},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AudioOnlyOffered(OfferedRungs(tt.file, tt.streams), tt.streams); got != tt.want {
				t.Fatalf("AudioOnlyOffered = %v, want %v", got, tt.want)
			}
		})
	}
}
