package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/JeremyVun/MediaServer/internal/config"
	"github.com/JeremyVun/MediaServer/internal/store"
)

type hlsCacheSettings struct {
	Dir       string `json:"dir"`
	MaxBytes  int64  `json:"max_bytes"`
	UsedBytes int64  `json:"used_bytes"`
	Available bool   `json:"available"`
}

type semanticSettings struct {
	Enabled   bool `json:"enabled"`
	Available bool `json:"available"`
}

type tmdbSettings struct {
	Configured bool `json:"configured"`
}

type settingsResponse struct {
	HLSCache               hlsCacheSettings `json:"hls_cache"`
	SemanticClassification semanticSettings `json:"semantic_classification"`
	TMDB                   tmdbSettings     `json:"tmdb"`
}

type setSemanticRequest struct {
	Enabled *bool `json:"enabled"`
}

type setHLSCacheRequest struct {
	Dir string `json:"dir"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s.writeSettings(w, r)
}

// handleSetSemanticClassification stores the Sol toggle. Enabling it while
// codex is unavailable is allowed: classify falls back to the parser.
func (s *Server) handleSetSemanticClassification(w http.ResponseWriter, r *http.Request) {
	var req setSemanticRequest
	if !decodeStrict(w, r, &req, "invalid settings") {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "enabled is required")
		return
	}
	value := "0"
	if *req.Enabled {
		value = "1"
	}
	if err := s.store.SetSetting(r.Context(), store.SettingSemanticClassification, value); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "saving setting failed")
		s.log.Error("save semantic classification", "error", err)
		return
	}
	s.writeSettings(w, r)
}

func (s *Server) writeSettings(w http.ResponseWriter, r *http.Request) {
	res := s.settingsResponse()
	value, err := s.store.GetSetting(r.Context(), store.SettingSemanticClassification)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "loading settings failed")
		s.log.Error("load semantic classification", "error", err)
		return
	}
	res.SemanticClassification = semanticSettings{
		Enabled:   value == "1",
		Available: s.semantic != nil && s.semantic.Available(),
	}
	res.TMDB = tmdbSettings{Configured: s.tmdb.Configured()}
	writeJSON(w, http.StatusOK, res)
}

// handleSetHLSCacheDir moves the transcode cache. The directory is created
// when its parent exists (so a hidden ".hls" can be typed into the picker),
// every playing session is stopped, and the old cache trees are deleted in
// the background to give the space back.
func (s *Server) handleSetHLSCacheDir(w http.ResponseWriter, r *http.Request) {
	if s.playback == nil {
		writeError(w, http.StatusServiceUnavailable, "playback_unavailable", "transcoded playback is not configured")
		return
	}
	var req setHLSCacheRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid settings")
		return
	}
	dir, ok := cleanAbsolutePath(req.Dir)
	if !ok {
		writeError(w, http.StatusBadRequest, "path_not_absolute", "path must be absolute")
		return
	}
	roots, err := s.store.ListRoots(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "listing roots failed")
		return
	}
	rootPaths := make([]string, 0, len(roots))
	for _, root := range roots {
		rootPaths = append(rootPaths, filepath.Clean(root.Path))
	}
	if err := config.CheckHLSCacheDir(dir, rootPaths); err != nil {
		switch {
		case errors.Is(err, config.ErrCacheDirIsRoot):
			writeError(w, http.StatusConflict, "cache_dir_is_root", "folder is itself a library folder")
		case errors.Is(err, config.ErrCacheDirNotHidden):
			writeError(w, http.StatusConflict, "cache_dir_not_hidden", "folder inside a library folder must be hidden, with a name starting with a dot")
		default:
			writeError(w, http.StatusBadRequest, "path_not_absolute", "path must be absolute")
		}
		return
	}
	if info, err := os.Stat(filepath.Dir(dir)); err != nil || !info.IsDir() {
		writeError(w, http.StatusConflict, "dir_missing", "folder does not exist")
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusConflict, "not_writable", "cannot write to this folder")
		return
	}
	probe, err := os.CreateTemp(dir, ".write-check-*")
	if err != nil {
		writeError(w, http.StatusConflict, "not_writable", "cannot write to this folder")
		return
	}
	probe.Close()
	os.Remove(probe.Name())

	if dir != s.playback.CacheDir() {
		if err := s.store.SetSetting(r.Context(), store.SettingHLSCacheDir, dir); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "saving setting failed")
			s.log.Error("save hls cache dir", "dir", dir, "error", err)
			return
		}
		old := s.playback.SetCacheDir(dir)
		s.log.Info("hls cache dir changed", "from", old, "to", dir)
		go func() {
			if err := s.playback.ClearCache(old); err != nil {
				s.log.Warn("clear old hls cache", "dir", old, "error", err)
			}
		}()
	}
	s.writeSettings(w, r)
}

func (s *Server) settingsResponse() settingsResponse {
	var res settingsResponse
	if s.playback == nil {
		return res
	}
	dir := s.playback.CacheDir()
	res.HLSCache = hlsCacheSettings{Dir: dir, MaxBytes: s.playback.CacheLimit()}
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		res.HLSCache.Available = true
		if used, err := s.playback.CacheUsage(); err == nil {
			res.HLSCache.UsedBytes = used
		}
	}
	return res
}
