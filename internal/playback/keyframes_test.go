package playback

import (
	"strings"
	"testing"
)

func TestParseKeyframeIndex(t *testing.T) {
	raw := strings.NewReader(`pts_time=0.000000|dts_time=N/A|duration_time=0.040000|flags=K__
pts_time=0.080000|dts_time=N/A|duration_time=0.040000|flags=___
pts_time=0.040000|dts_time=0.000000|duration_time=0.040000|flags=___
pts_time=6.000000|dts_time=5.920000|duration_time=0.040000|flags=K__
pts_time=6.080000|dts_time=5.960000|duration_time=0.040000|flags=___
`)
	idx, err := parseKeyframeIndex(raw, 99)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(idx.Starts) != 2 || idx.Starts[0] != 0 || idx.Starts[1] != 6 {
		t.Fatalf("starts = %v, want [0 6]", idx.Starts)
	}
	if idx.End < 6.119 || idx.End > 6.121 {
		t.Fatalf("end = %.6f, want 6.120000", idx.End)
	}
	if idx.TimestampShift < 0.079 || idx.TimestampShift > 0.081 {
		t.Fatalf("timestamp shift = %.6f, want 0.080000", idx.TimestampShift)
	}
	if got := idx.targetDuration(); got != 6 {
		t.Fatalf("target duration = %d, want 6", got)
	}
}

func TestKeyframePlaylistUsesExactVariableDurations(t *testing.T) {
	w := &worker{keyframes: &keyframeIndex{
		Version: keyframeIndexVersion,
		Starts:  []float64{0, 6.006, 8.383},
		End:     10.636,
	}}
	playlist := w.playlist()
	for _, want := range []string{
		"#EXT-X-TARGETDURATION:7",
		"#EXT-X-INDEPENDENT-SEGMENTS",
		"#EXTINF:6.006000,",
		"#EXTINF:2.377000,",
		"#EXTINF:2.253000,",
	} {
		if !strings.Contains(playlist, want) {
			t.Fatalf("playlist missing %q:\n%s", want, playlist)
		}
	}
}
