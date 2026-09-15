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
	ReasonAudioTrackSelection  = "audio_track_selection"
	ReasonQuality              = "quality"

	TierDirect         = "direct"
	TierRemux          = "remux"
	TierAudioTranscode = "audio_transcode"
	TierFullTranscode  = "full_transcode"
	TierLadder         = "ladder"
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
	AudioPick *Stream
	VideoCopy bool
	AudioCopy bool
	// Quality is the resolved request (design decision 3); Rungs is the ladder
	// it encodes, empty for every non-ladder tier.
	Quality string
	Rungs   []RungOutput
}

// Decide picks direct play or an HLS tier. A non-nil audioStreamIndex always
// yields HLS: browsers cannot switch audio tracks on a plain <video src>, so
// clients omit it for the container's default track and any explicit pick is
// remapped by ffmpeg. Codec support then only matters for the picked stream.
func Decide(file MediaFile, streams []Stream, caps Capabilities, subtitleStreamIndex, audioStreamIndex *int) Decision {
	audioPick := pickAudioStream(streams, audioStreamIndex)
	if burnIn := pickBurnInStream(streams, subtitleStreamIndex); burnIn != nil {
		return Decision{
			Mode:      ModeHLS,
			Reason:    ReasonSubtitleBurnIn,
			Tier:      TierFullTranscode,
			BurnIn:    burnIn,
			AudioPick: audioPick,
			VideoCopy: false,
			AudioCopy: audioStreamsSupported(streams, caps, audioPick),
		}
	}

	videoOK := videoStreamsSupported(file, streams, caps)
	audioOK := audioStreamsSupported(streams, caps, audioPick)
	containerOK := containerSupported(file.Container, caps.Containers)
	if videoOK && audioOK && containerOK && audioPick == nil {
		return Decision{Mode: ModeDirect, Tier: TierDirect, VideoCopy: true, AudioCopy: true}
	}
	if !videoOK {
		return Decision{Mode: ModeHLS, Reason: ReasonVideoCodec, Tier: TierFullTranscode, AudioPick: audioPick, VideoCopy: false, AudioCopy: audioOK}
	}
	if !audioOK {
		return Decision{Mode: ModeHLS, Reason: ReasonAudioCodec, Tier: TierAudioTranscode, AudioPick: audioPick, VideoCopy: true, AudioCopy: false}
	}
	if !containerOK {
		return Decision{Mode: ModeHLS, Reason: ReasonContainerUnsupported, Tier: TierRemux, AudioPick: audioPick, VideoCopy: true, AudioCopy: true}
	}
	return Decision{Mode: ModeHLS, Reason: ReasonAudioTrackSelection, Tier: TierRemux, AudioPick: audioPick, VideoCopy: true, AudioCopy: true}
}

// DecideQuality resolves a requested quality against the rungs the file offers
// and returns the ladder decision for anything but original. Original delegates
// to Decide, so the whole existing pipeline stays byte-identical.
func DecideQuality(quality string, file MediaFile, streams []Stream, caps Capabilities, subtitleStreamIndex, audioStreamIndex *int) (Decision, error) {
	offered := OfferedRungs(file, streams)
	resolved, err := ResolveQuality(quality, offered)
	if err != nil {
		return Decision{}, err
	}
	rungs := RungsFor(resolved, offered)
	if len(rungs) == 0 {
		decision := Decide(file, streams, caps, subtitleStreamIndex, audioStreamIndex)
		decision.Quality = QualityOriginal
		return decision, nil
	}
	return Decision{
		Mode:      ModeHLS,
		Reason:    ReasonQuality,
		Tier:      TierLadder,
		BurnIn:    pickBurnInStream(streams, subtitleStreamIndex),
		AudioPick: pickAudioStream(streams, audioStreamIndex),
		Quality:   resolved,
		Rungs:     rungs,
	}, nil
}

func pickAudioStream(streams []Stream, audioStreamIndex *int) *Stream {
	if audioStreamIndex == nil {
		return nil
	}
	if st, ok := FindStream(streams, *audioStreamIndex); ok && st.Kind == "audio" {
		return &st
	}
	return nil
}

func pickBurnInStream(streams []Stream, subtitleStreamIndex *int) *Stream {
	if subtitleStreamIndex == nil {
		return nil
	}
	if st, ok := FindStream(streams, *subtitleStreamIndex); ok && IsImageSubtitle(st.Codec) {
		return &st
	}
	return nil
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

// profileVersion invalidates cached HLS output when the ffmpeg pipeline's
// on-disk format changes. Version 3 replaces mid-GOP copy-tier cuts with
// independently decodable, source-keyframe-aligned segments.
const profileVersion = 3

func ProfileHash(file MediaFile, caps Capabilities, decision Decision, subtitleStreamIndex, audioStreamIndex *int) string {
	profile := struct {
		Version             int          `json:"version"`
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
		AudioStreamIndex    *int         `json:"audio_stream_index,omitempty"`
		Rungs               []RungOutput `json:"rungs,omitempty"`
	}{
		Version:             profileVersion,
		FileID:              file.ID,
		Fingerprint:         file.Fingerprint,
		Container:           normalizeContainer(file.Container),
		DurationS:           file.DurationS,
		Width:               file.Width,
		Height:              file.Height,
		Capabilities:        normalizeCapabilities(ladderCapabilities(caps, decision)),
		Tier:                decision.Tier,
		Reason:              decision.Reason,
		SubtitleStreamIndex: subtitleStreamIndex,
		AudioStreamIndex:    audioStreamIndex,
		Rungs:               decision.Rungs,
	}
	raw, _ := json.Marshal(profile)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

// ladderCapabilities drops max_height from a ladder profile: the ladder sizes
// itself, so a phone and an iPad share one cache entry.
func ladderCapabilities(caps Capabilities, decision Decision) Capabilities {
	if decision.Tier != TierLadder {
		return caps
	}
	caps.MaxHeight = 0
	return caps
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

func audioStreamsSupported(streams []Stream, caps Capabilities, pick *Stream) bool {
	if pick != nil {
		return codecSupported(pick.Codec, caps.AudioCodecs)
	}
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
