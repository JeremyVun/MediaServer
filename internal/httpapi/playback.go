package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	playbackpkg "github.com/JeremyVun/MediaServer/internal/playback"
	"github.com/JeremyVun/MediaServer/internal/store"
)

type playRequest struct {
	FileID              *int64                   `json:"file_id"`
	Capabilities        playbackpkg.Capabilities `json:"capabilities"`
	SubtitleStreamIndex *int                     `json:"subtitle_stream_index"`
}

type playResponse struct {
	Mode      string             `json:"mode"`
	Reason    *string            `json:"reason"`
	URL       string             `json:"url"`
	SessionID *string            `json:"session_id,omitempty"`
	Subtitles []subtitleResponse `json:"subtitles"`
}

type subtitleResponse struct {
	StreamIndex int    `json:"stream_index"`
	Lang        string `json:"lang,omitempty"`
	URL         string `json:"url"`
}

func (s *Server) handlePlayItem(w http.ResponseWriter, r *http.Request) {
	itemID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, err := s.store.GetItem(r.Context(), itemID); err != nil {
		writeStoreError(w, err)
		return
	}

	var req playRequest
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid play request")
			return
		}
	}

	file, root, ok := s.playableFile(w, r, itemID, req.FileID)
	if !ok {
		return
	}
	mediaPath, err := safeJoin(root.Path, file.RelPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "invalid media path")
		s.log.Error("invalid media path", "file_id", file.ID, "rel_path", file.RelPath, "error", err)
		return
	}
	streams, err := s.store.ListFileStreams(r.Context(), file.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing streams failed")
		s.log.Error("list streams for play", "file_id", file.ID, "error", err)
		return
	}
	if file.Container == nil || len(streams) == 0 {
		writeError(w, http.StatusConflict, "not_ready", "file has not been probed yet")
		return
	}
	playbackStreams := toPlaybackStreams(streams)
	if req.SubtitleStreamIndex != nil {
		st, ok := playbackpkg.FindStream(playbackStreams, *req.SubtitleStreamIndex)
		if !ok || st.Kind != "subtitle" {
			writeError(w, http.StatusBadRequest, "bad_request", "subtitle stream not found")
			return
		}
	}
	media := toPlaybackFile(file)
	decision := playbackpkg.Decide(media, playbackStreams, req.Capabilities, req.SubtitleStreamIndex)
	subtitles := subtitleResponses(file.ID, streams)
	if decision.Mode == playbackpkg.ModeDirect {
		writeJSON(w, http.StatusOK, playResponse{
			Mode:      "direct",
			Reason:    nil,
			URL:       "/api/files/" + strconv.FormatInt(file.ID, 10) + "/stream",
			Subtitles: subtitles,
		})
		return
	}
	if s.playback == nil {
		writeError(w, http.StatusServiceUnavailable, "playback_unavailable", "transcoded playback is not configured")
		return
	}
	session, err := s.playback.StartSession(r.Context(), playbackpkg.StartRequest{
		File:                media,
		SourcePath:          mediaPath,
		Streams:             playbackStreams,
		Capabilities:        req.Capabilities,
		Decision:            decision,
		SubtitleStreamIndex: req.SubtitleStreamIndex,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "starting playback session failed")
		s.log.Error("start playback session", "file_id", file.ID, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, playResponse{
		Mode:      "hls",
		Reason:    &decision.Reason,
		URL:       session.URL,
		SessionID: &session.ID,
		Subtitles: subtitles,
	})
}

func (s *Server) handleFileStream(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	file, err := s.store.GetFile(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	root, err := s.store.GetRoot(r.Context(), file.RootID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !root.Online || file.Status != "online" {
		writeError(w, http.StatusConflict, "root_offline", "file is not available")
		return
	}
	path, err := safeJoin(root.Path, file.RelPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "invalid media path")
		s.log.Error("invalid media path", "file_id", file.ID, "rel_path", file.RelPath, "error", err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not_found", "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "opening file failed")
		s.log.Error("open stream file", "file_id", file.ID, "path", path, "error", err)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "stat file failed")
		s.log.Error("stat stream file", "file_id", file.ID, "path", path, "error", err)
		return
	}
	w.Header().Set("Content-Type", streamContentType(file))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filepath.Base(file.RelPath), stat.ModTime(), f)
}

func (s *Server) handleHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	if s.playback == nil {
		writeError(w, http.StatusServiceUnavailable, "playback_unavailable", "transcoded playback is not configured")
		return
	}
	body, err := s.playback.Playlist(r.Context(), r.PathValue("sid"))
	if err != nil {
		writePlaybackError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (s *Server) handleHLSSegment(w http.ResponseWriter, r *http.Request) {
	if s.playback == nil {
		writeError(w, http.StatusServiceUnavailable, "playback_unavailable", "transcoded playback is not configured")
		return
	}
	if err := s.playback.ServeSegment(w, r, r.PathValue("sid"), r.PathValue("segment")); err != nil {
		writePlaybackError(w, err)
	}
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if s.playback != nil {
		s.playback.EndSession(r.PathValue("sid"))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFileSubtitle(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	name := r.PathValue("name")
	if filepath.Ext(name) != ".vtt" {
		writeError(w, http.StatusNotFound, "not_found", "subtitle stream not found")
		return
	}
	streamIndex, err := strconv.Atoi(name[:len(name)-len(".vtt")])
	if err != nil || streamIndex < 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid subtitle stream")
		return
	}
	file, err := s.store.GetFile(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	root, err := s.store.GetRoot(r.Context(), file.RootID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !root.Online || file.Status != "online" {
		writeError(w, http.StatusConflict, "root_offline", "file is not available")
		return
	}
	mediaPath, err := safeJoin(root.Path, file.RelPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "invalid media path")
		s.log.Error("invalid subtitle media path", "file_id", file.ID, "rel_path", file.RelPath, "error", err)
		return
	}
	streams, err := s.store.ListFileStreams(r.Context(), file.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing streams failed")
		s.log.Error("list streams for subtitles", "file_id", file.ID, "error", err)
		return
	}
	st, ok := playbackpkg.FindStream(toPlaybackStreams(streams), streamIndex)
	if !ok || st.Kind != "subtitle" {
		writeError(w, http.StatusNotFound, "not_found", "subtitle stream not found")
		return
	}
	if !playbackpkg.IsTextSubtitle(st.Codec) {
		writeError(w, http.StatusConflict, "subtitle_burn_in", "subtitle stream requires burn-in")
		return
	}
	extractor := s.subtitles
	if extractor.CacheDir == "" {
		extractor.CacheDir = filepath.Join(s.thumbsDir, "subs")
	}
	path, err := extractor.Extract(r.Context(), file.ID, mediaPath, st)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "extracting subtitle failed")
		s.log.Error("extract subtitle", "file_id", file.ID, "stream_index", streamIndex, "error", err)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeFile(w, r, path)
}

// streamContentType prefers the probed container (SPEC-BACKEND: "the probed
// MIME type") and only falls back to extension guessing for unprobed files —
// extension tables vary by host and a mis-named file would lie to the player.
func streamContentType(file store.File) string {
	if file.Container != nil {
		switch *file.Container {
		case "mov", "mp4", "m4a", "3gp", "3g2", "mj2": // ffprobe's mp4-family names
			return "video/mp4"
		case "matroska":
			return "video/x-matroska"
		case "webm":
			return "video/webm"
		case "avi":
			return "video/x-msvideo"
		case "mpegts":
			return "video/mp2t"
		case "asf":
			return "video/x-ms-wmv"
		case "flv":
			return "video/x-flv"
		}
	}
	if ct := mime.TypeByExtension(filepath.Ext(file.RelPath)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func (s *Server) playableFile(w http.ResponseWriter, r *http.Request, itemID int64, requested *int64) (store.File, store.Root, bool) {
	files, err := s.store.ListFilesForItem(r.Context(), itemID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing files failed")
		s.log.Error("list playable files", "item_id", itemID, "error", err)
		return store.File{}, store.Root{}, false
	}
	for _, file := range files {
		if requested != nil && file.ID != *requested {
			continue
		}
		root, err := s.store.GetRoot(r.Context(), file.RootID)
		if err != nil {
			writeStoreError(w, err)
			return store.File{}, store.Root{}, false
		}
		if file.Status == "online" && root.Online {
			return file, root, true
		}
		if requested != nil {
			writeError(w, http.StatusConflict, "root_offline", "file is not available")
			return store.File{}, store.Root{}, false
		}
	}
	if requested != nil {
		writeError(w, http.StatusNotFound, "not_found", "file not found")
		return store.File{}, store.Root{}, false
	}
	writeError(w, http.StatusConflict, "root_offline", "no online file is available")
	return store.File{}, store.Root{}, false
}

func subtitleResponses(fileID int64, streams []store.Stream) []subtitleResponse {
	text := playbackpkg.TextSubtitleStreams(toPlaybackStreams(streams))
	out := make([]subtitleResponse, 0, len(text))
	for _, st := range text {
		res := subtitleResponse{
			StreamIndex: st.StreamIndex,
			URL:         "/api/files/" + strconv.FormatInt(fileID, 10) + "/subs/" + strconv.Itoa(st.StreamIndex) + ".vtt",
		}
		if st.Lang != nil {
			res.Lang = *st.Lang
		}
		out = append(out, res)
	}
	return out
}

func toPlaybackFile(file store.File) playbackpkg.MediaFile {
	media := playbackpkg.MediaFile{
		ID:          file.ID,
		Fingerprint: file.Fingerprint,
	}
	if file.Container != nil {
		media.Container = *file.Container
	}
	if file.DurationS != nil {
		media.DurationS = *file.DurationS
	}
	if file.Width != nil {
		media.Width = *file.Width
	}
	if file.Height != nil {
		media.Height = *file.Height
	}
	return media
}

func toPlaybackStreams(streams []store.Stream) []playbackpkg.Stream {
	out := make([]playbackpkg.Stream, 0, len(streams))
	for _, st := range streams {
		out = append(out, playbackpkg.Stream{
			StreamIndex: st.StreamIndex,
			Kind:        st.Kind,
			Codec:       st.Codec,
			Lang:        st.Lang,
			Title:       st.Title,
			Channels:    st.Channels,
			IsDefault:   st.IsDefault,
		})
	}
	return out
}

func writePlaybackError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, playbackpkg.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "playback session not found")
	case errors.Is(err, playbackpkg.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, "segment_timeout", "playback segment is not ready")
	default:
		writeError(w, http.StatusInternalServerError, "internal", "playback failed")
	}
}
