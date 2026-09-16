package playback

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	QualityOriginal = "original"
	QualityAuto     = "auto"
	QualityAudio    = "audio"
)

var ErrUnknownQuality = errors.New("unknown playback quality")

// Rung is one step of the quality ladder: a box the output is fitted into, the
// video bitrate it encodes at, and the CODECS strings the multivariant playlist
// declares. Boxes and bitrates are owner numbers (design decision 4).
//
// Levels are the lowest that carry 60 fps at the rung's box: H.264 by MaxMBPS
// (4.2, 3.2, 3.1, 3.1) and HEVC by MaxLumaSr (4.1, 4.0, 3.1, 3.0).
// VideoToolbox stamps by box size alone (below these at both 30 and 60 fps),
// and a declared level may exceed the stream's but never fall short of it.
type Rung struct {
	ID         string
	BoxW       int
	BoxH       int
	BitrateK   int
	H264Codecs string
	HEVCCodecs string
}

var ladderRungs = []Rung{
	{ID: "1080p", BoxW: 1920, BoxH: 1080, BitrateK: 6000, H264Codecs: "avc1.64002A", HEVCCodecs: "hvc1.1.6.L123.B0"},
	{ID: "720p", BoxW: 1280, BoxH: 720, BitrateK: 3000, H264Codecs: "avc1.640020", HEVCCodecs: "hvc1.1.6.L120.B0"},
	{ID: "480p", BoxW: 854, BoxH: 480, BitrateK: 1500, H264Codecs: "avc1.64001F", HEVCCodecs: "hvc1.1.6.L93.B0"},
	{ID: "360p", BoxW: 640, BoxH: 360, BitrateK: 800, H264Codecs: "avc1.64001F", HEVCCodecs: "hvc1.1.6.L90.B0"},
}

const (
	ladderAudioCodecs = "mp4a.40.2"
	ladderAudioKbps   = 192
	// peakBandwidthFactor turns a rung's average video bitrate into the peak
	// BANDWIDTH the playlist advertises. Phase 2 measures real per-segment
	// peaks and tunes it.
	peakBandwidthFactor = 1.5
	// scaleDivisibleBy keeps both output edges even for the encoders.
	scaleDivisibleBy = 2
)

// RungOutput is an offered rung with the size this file encodes to. ScaleW and
// ScaleH are the scale filter's target box (min(box, source) per design
// decision 4), which differs from the output whenever the aspect ratios do.
type RungOutput struct {
	ID       string `json:"id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	BitrateK int    `json:"bitrate_kbps"`
	ScaleW   int    `json:"scale_w"`
	ScaleH   int    `json:"scale_h"`
}

// OfferedRungs lists the rungs a file offers, largest first. A rung is offered
// when the source does not fit inside the next-lower rung's box, so long edges
// count; 360p is always offered once there is a video stream with dimensions.
func OfferedRungs(file MediaFile, streams []Stream) []RungOutput {
	if file.Width <= 0 || file.Height <= 0 || !hasVideoStream(streams) {
		return nil
	}
	out := make([]RungOutput, 0, len(ladderRungs))
	for i, rung := range ladderRungs {
		if i+1 < len(ladderRungs) && fitsRungBox(file, ladderRungs[i+1]) {
			continue
		}
		scaleW, scaleH := rungScaleTarget(file, rung)
		w, h := scaleFit(file.Width, file.Height, scaleW, scaleH)
		out = append(out, RungOutput{
			ID:       rung.ID,
			Width:    w,
			Height:   h,
			BitrateK: rung.BitrateK,
			ScaleW:   scaleW,
			ScaleH:   scaleH,
		})
	}
	return out
}

// AudioOnlyOffered reports whether the file can be served as audio alone: it
// needs a video stream worth dropping and an audio stream to keep, so an audio
// file is already audio and a silent video has nothing to send.
func AudioOnlyOffered(offered []RungOutput, streams []Stream) bool {
	if len(offered) == 0 {
		return false
	}
	for _, st := range streams {
		if st.Kind == "audio" {
			return true
		}
	}
	return false
}

// ResolveQuality turns a requested quality into the one the server will serve.
// A fixed size the file does not offer resolves down to the largest offered
// rung below it, and a file that offers nothing resolves everything to
// original (design decision 3).
func ResolveQuality(requested string, offered []RungOutput) (string, error) {
	switch requested {
	case "", QualityOriginal:
		return QualityOriginal, nil
	case QualityAuto:
		if len(offered) == 0 {
			return QualityOriginal, nil
		}
		return QualityAuto, nil
	}
	want := ladderIndex(requested)
	if want < 0 {
		return "", fmt.Errorf("%w: %q", ErrUnknownQuality, requested)
	}
	for _, rung := range offered {
		if ladderIndex(rung.ID) >= want {
			return rung.ID, nil
		}
	}
	return QualityOriginal, nil
}

// RungsFor is the ladder a resolved quality encodes: every offered rung for
// auto, one rung for a fixed size, none for original.
func RungsFor(resolved string, offered []RungOutput) []RungOutput {
	switch resolved {
	case QualityAuto:
		return offered
	case "", QualityOriginal:
		return nil
	}
	for _, rung := range offered {
		if rung.ID == resolved {
			return []RungOutput{rung}
		}
	}
	return nil
}

// MasterPlaylist is the multivariant playlist for a ladder session. Safari
// starts on the first variant, so 720p leads when it is offered.
func MasterPlaylist(rungs []RungOutput, hevc bool) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, rung := range variantOrder(rungs) {
		b.WriteString("#EXT-X-STREAM-INF:BANDWIDTH=" + strconv.Itoa(rung.peakBandwidth()))
		b.WriteString(",AVERAGE-BANDWIDTH=" + strconv.Itoa(rung.averageBandwidth()))
		b.WriteString(",RESOLUTION=" + strconv.Itoa(rung.Width) + "x" + strconv.Itoa(rung.Height))
		b.WriteString(",CODECS=\"" + rung.codecs(hevc) + "\"\n")
		b.WriteString(rung.ID + "/stream.m3u8\n")
	}
	return b.String()
}

func variantOrder(rungs []RungOutput) []RungOutput {
	out := make([]RungOutput, 0, len(rungs))
	for _, rung := range rungs {
		if rung.ID == "720p" {
			out = append(out, rung)
			break
		}
	}
	for _, rung := range rungs {
		if len(out) > 0 && rung.ID == out[0].ID {
			continue
		}
		out = append(out, rung)
	}
	return out
}

func (r RungOutput) averageBandwidth() int {
	return (r.BitrateK + ladderAudioKbps) * 1000
}

func (r RungOutput) peakBandwidth() int {
	return (int(float64(r.BitrateK)*peakBandwidthFactor) + ladderAudioKbps) * 1000
}

func (r RungOutput) codecs(hevc bool) string {
	rung, ok := rungByID(r.ID)
	if !ok {
		return ladderAudioCodecs
	}
	if hevc {
		return rung.HEVCCodecs + "," + ladderAudioCodecs
	}
	return rung.H264Codecs + "," + ladderAudioCodecs
}

// scaleFit mirrors ffmpeg's scale filter with force_original_aspect_ratio=
// decrease and force_divisible_by=2: each candidate edge is rescaled with
// round-half-away-from-zero over the divisor before the box clamps it. That
// rounding is why a 1920x1080 source lands on 854x480 rather than 852x480.
func scaleFit(srcW, srcH, boxW, boxH int) (int, int) {
	w := min(boxW, srcW)
	h := min(boxH, srcH)
	fitW := roundedDiv(h*srcW, srcH*scaleDivisibleBy) * scaleDivisibleBy
	fitH := roundedDiv(w*srcH, srcW*scaleDivisibleBy) * scaleDivisibleBy
	return min(fitW, w), min(fitH, h)
}

func roundedDiv(num, den int) int {
	return (num + den/2) / den
}

// rungBox transposes the box for portrait sources, so a rung sizes by long
// edge either way.
func rungBox(file MediaFile, rung Rung) (int, int) {
	if file.Height > file.Width {
		return rung.BoxH, rung.BoxW
	}
	return rung.BoxW, rung.BoxH
}

func rungScaleTarget(file MediaFile, rung Rung) (int, int) {
	boxW, boxH := rungBox(file, rung)
	return min(boxW, file.Width), min(boxH, file.Height)
}

func fitsRungBox(file MediaFile, rung Rung) bool {
	boxW, boxH := rungBox(file, rung)
	return file.Width <= boxW && file.Height <= boxH
}

func ladderIndex(id string) int {
	for i, rung := range ladderRungs {
		if rung.ID == id {
			return i
		}
	}
	return -1
}

func rungByID(id string) (Rung, bool) {
	if i := ladderIndex(id); i >= 0 {
		return ladderRungs[i], true
	}
	return Rung{}, false
}

func hasVideoStream(streams []Stream) bool {
	for _, st := range streams {
		if st.Kind == "video" {
			return true
		}
	}
	return false
}
