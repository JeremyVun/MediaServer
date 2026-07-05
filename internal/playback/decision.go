package playback

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

const (
	ModeDirect = "direct"
	ModeHLS    = "hls"

	ReasonAudioCodec           = "audio_codec"
	ReasonVideoCodec           = "video_codec"
	ReasonContainerUnsupported = "container_not_supported"
	ReasonSubtitleBurnIn       = "subtitle_burn_in"

	TierDirect         = "direct"
	TierRemux          = "remux"
	TierAudioTranscode = "audio_transcode"
	TierFullTranscode  = "full_transcode"
)

type Capabilities struct {
	Containers  []string `json:"containers"`
	VideoCodecs []string `json:"video_codecs"`
	AudioCodecs []string `json:"audio_codecs"`
	MaxHeight   int      `json:"max_height"`
	NativeHLS   bool     `json:"native_hls"`
}

type MediaFile struct {
	ID          int64
	Container   string
	DurationS   float64
	Width       int
	Height      int
	Fingerprint string
}

type Stream struct {
	StreamIndex int
	Kind        string
	Codec       string
	Lang        *string
	Title       *string
	Channels    *int
	IsDefault   bool
}

type Decision struct {
	Mode      string
	Reason    string
	Tier      string
	BurnIn    *Stream
	VideoCopy bool
	AudioCopy bool
}

func Decide(file MediaFile, streams []Stream, caps Capabilities, subtitleStreamIndex *int) Decision {
	if subtitleStreamIndex != nil {
		if st, ok := FindStream(streams, *subtitleStreamIndex); ok && IsImageSubtitle(st.Codec) {
			return Decision{
				Mode:      ModeHLS,
				Reason:    ReasonSubtitleBurnIn,
				Tier:      TierFullTranscode,
				BurnIn:    &st,
				VideoCopy: false,
				AudioCopy: audioStreamsSupported(streams, caps),
			}
		}
	}

	videoOK := videoStreamsSupported(file, streams, caps)
	audioOK := audioStreamsSupported(streams, caps)
	containerOK := containerSupported(file.Container, caps.Containers)
	if videoOK && audioOK && containerOK {
		return Decision{Mode: ModeDirect, Tier: TierDirect, VideoCopy: true, AudioCopy: true}
	}
	if !videoOK {
		return Decision{Mode: ModeHLS, Reason: ReasonVideoCodec, Tier: TierFullTranscode, VideoCopy: false, AudioCopy: audioOK}
	}
	if !audioOK {
		return Decision{Mode: ModeHLS, Reason: ReasonAudioCodec, Tier: TierAudioTranscode, VideoCopy: true, AudioCopy: false}
	}
	return Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux, VideoCopy: true, AudioCopy: true}
}

func FindStream(streams []Stream, streamIndex int) (Stream, bool) {
	for _, st := range streams {
		if st.StreamIndex == streamIndex {
			return st, true
		}
	}
	return Stream{}, false
}

func TextSubtitleStreams(streams []Stream) []Stream {
	out := make([]Stream, 0)
	for _, st := range streams {
		if st.Kind == "subtitle" && IsTextSubtitle(st.Codec) {
			out = append(out, st)
		}
	}
	return out
}

func IsTextSubtitle(codec string) bool {
	switch normalizeCodec(codec) {
	case "subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text":
		return true
	default:
		return false
	}
}

func IsImageSubtitle(codec string) bool {
	switch normalizeCodec(codec) {
	case "hdmv_pgs_subtitle", "pgs", "dvd_subtitle", "vobsub", "xsub":
		return true
	default:
		return false
	}
}

func ProfileHash(file MediaFile, caps Capabilities, decision Decision, subtitleStreamIndex *int) string {
	profile := struct {
		FileID              int64        `json:"file_id"`
		Fingerprint         string       `json:"fingerprint,omitempty"`
		Container           string       `json:"container"`
		DurationS           float64      `json:"duration_s"`
		Width               int          `json:"width"`
		Height              int          `json:"height"`
		Capabilities        Capabilities `json:"capabilities"`
		Tier                string       `json:"tier"`
		Reason              string       `json:"reason,omitempty"`
		SubtitleStreamIndex *int         `json:"subtitle_stream_index,omitempty"`
	}{
		FileID:              file.ID,
		Fingerprint:         file.Fingerprint,
		Container:           normalizeContainer(file.Container),
		DurationS:           file.DurationS,
		Width:               file.Width,
		Height:              file.Height,
		Capabilities:        normalizeCapabilities(caps),
		Tier:                decision.Tier,
		Reason:              decision.Reason,
		SubtitleStreamIndex: subtitleStreamIndex,
	}
	raw, _ := json.Marshal(profile)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

func videoStreamsSupported(file MediaFile, streams []Stream, caps Capabilities) bool {
	for _, st := range streams {
		if st.Kind != "video" {
			continue
		}
		if !codecSupported(st.Codec, caps.VideoCodecs) {
			return false
		}
	}
	if caps.MaxHeight > 0 && file.Height > caps.MaxHeight {
		return false
	}
	return true
}

func audioStreamsSupported(streams []Stream, caps Capabilities) bool {
	for _, st := range streams {
		if st.Kind == "audio" && !codecSupported(st.Codec, caps.AudioCodecs) {
			return false
		}
	}
	return true
}

func containerSupported(container string, supported []string) bool {
	aliases := containerAliases(container)
	for _, have := range supported {
		have = normalizeContainer(have)
		for _, alias := range aliases {
			if have == alias {
				return true
			}
		}
	}
	return false
}

func codecSupported(codec string, supported []string) bool {
	codec = normalizeCodec(codec)
	for _, have := range supported {
		if normalizeCodec(have) == codec {
			return true
		}
	}
	return false
}

func containerAliases(container string) []string {
	switch normalizeContainer(container) {
	case "mp4", "mov", "m4v", "m4a", "3gp", "3g2", "mj2":
		return []string{"mp4", "mov", "m4v"}
	case "matroska", "mkv":
		return []string{"matroska", "mkv"}
	default:
		if container == "" {
			return nil
		}
		return []string{normalizeContainer(container)}
	}
}

func normalizeContainer(container string) string {
	container = strings.ToLower(strings.TrimSpace(container))
	if i := strings.IndexByte(container, ','); i >= 0 {
		container = container[:i]
	}
	switch container {
	case "quicktime":
		return "mov"
	case "matroska,webm":
		return "matroska"
	default:
		return container
	}
}

func normalizeCodec(codec string) string {
	codec = strings.ToLower(strings.TrimSpace(codec))
	switch codec {
	case "avc", "avc1":
		return "h264"
	case "h265", "hvc1", "hev1":
		return "hevc"
	case "mp4a":
		return "aac"
	case "e-ac-3":
		return "eac3"
	case "srt":
		return "subrip"
	case "pgs":
		return "hdmv_pgs_subtitle"
	default:
		return codec
	}
}

func normalizeCapabilities(caps Capabilities) Capabilities {
	out := caps
	out.Containers = normalizeList(out.Containers, normalizeContainer)
	out.VideoCodecs = normalizeList(out.VideoCodecs, normalizeCodec)
	out.AudioCodecs = normalizeList(out.AudioCodecs, normalizeCodec)
	return out
}

func normalizeList(in []string, normalize func(string) string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = normalize(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
