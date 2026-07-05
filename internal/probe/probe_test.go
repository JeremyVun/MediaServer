package probe

import "testing"

func TestParseFFprobeJSON(t *testing.T) {
	raw := []byte(`{
	  "streams": [
	    {
	      "index": 0,
	      "codec_name": "h264",
	      "codec_type": "video",
	      "width": 1920,
	      "height": 1080,
	      "disposition": {"default": 1},
	      "tags": {"language": "und"}
	    },
	    {
	      "index": 1,
	      "codec_name": "aac",
	      "codec_type": "audio",
	      "channels": 6,
	      "disposition": {"default": 1},
	      "tags": {"language": "eng", "title": "Surround"}
	    },
	    {
	      "index": 2,
	      "codec_name": "subrip",
	      "codec_type": "subtitle",
	      "tags": {"language": "eng"}
	    }
	  ],
	  "format": {
	    "format_name": "matroska,webm",
	    "duration": "596.400000",
	    "bit_rate": "9800000"
	  }
	}`)

	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Container != "matroska" {
		t.Errorf("container = %q", got.Container)
	}
	if got.DurationS != 596.4 || got.Bitrate != 9_800_000 {
		t.Errorf("duration/bitrate = %f/%d", got.DurationS, got.Bitrate)
	}
	if got.Width != 1920 || got.Height != 1080 {
		t.Errorf("dimensions = %dx%d", got.Width, got.Height)
	}
	if len(got.Streams) != 3 {
		t.Fatalf("streams = %d", len(got.Streams))
	}
	if got.Streams[1].Codec != "aac" || got.Streams[1].Channels == nil || *got.Streams[1].Channels != 6 {
		t.Errorf("audio stream = %+v", got.Streams[1])
	}
	if got.Streams[2].Kind != "subtitle" || got.Streams[2].Lang == nil || *got.Streams[2].Lang != "eng" {
		t.Errorf("subtitle stream = %+v", got.Streams[2])
	}
}

func TestParseRejectsMissingStreams(t *testing.T) {
	if _, err := Parse([]byte(`{"format":{"format_name":"mov,mp4"}}`)); err == nil {
		t.Fatal("missing streams accepted")
	}
}
