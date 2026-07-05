// Package httpapi wires the HTTP surface: REST handlers, the SSE hub
// (later milestone), and the embedded web app. Handlers call the store and
// publish events; they never touch database/sql.
package httpapi

import (
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/jobs"
	"github.com/JeremyVun/MediaServer/internal/playback"
	"github.com/JeremyVun/MediaServer/internal/store"
)

func init() {
	// Go's built-in MIME table has no .webmanifest entry, so the embedded file
	// server would sniff the PWA manifest as text/plain. Register the correct
	// type once at load.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

type Server struct {
	store         *store.Store
	bus           *events.Bus
	log           *slog.Logger
	jobs          *jobs.Manager
	rootLifecycle RootLifecycle
	version       string
	startedAt     time.Time
	webFS         fs.FS // nil when the web app is not embedded (dev mode)
	thumbsDir     string
	playback      *playback.Manager
	subtitles     playback.SubtitleExtractor
	eventSeq      atomic.Uint64
	uploadIgnore  ignoreSet
	uploadMinGB   float64
	uploadRename  sync.Mutex
}

type Options struct {
	Store         *store.Store
	Bus           *events.Bus
	Log           *slog.Logger
	Jobs          *jobs.Manager
	RootLifecycle RootLifecycle
	Version       string
	WebFS         fs.FS // pass webdist.FS() output; nil serves a dev placeholder
	ThumbsDir     string
	Playback      *playback.Manager
	Subtitles     playback.SubtitleExtractor
	UploadIgnore  ignoreSet
	UploadMinGB   float64
}

type ignoreSet interface {
	Add(path string) func()
}

func NewServer(opts Options) *Server {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Server{
		store:         opts.Store,
		bus:           opts.Bus,
		log:           opts.Log,
		jobs:          opts.Jobs,
		rootLifecycle: opts.RootLifecycle,
		version:       opts.Version,
		startedAt:     time.Now(),
		webFS:         opts.WebFS,
		thumbsDir:     opts.ThumbsDir,
		playback:      opts.Playback,
		subtitles:     opts.Subtitles,
		uploadIgnore:  opts.UploadIgnore,
		uploadMinGB:   opts.UploadMinGB,
	}
}

// Handler builds the route table. Route patterns use 1.22+ method routing;
// no framework.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/items", s.handleListItems)
	mux.HandleFunc("GET /api/items/{id}", s.handleGetItem)
	mux.HandleFunc("PATCH /api/items/{id}", s.handlePatchItem)
	mux.HandleFunc("DELETE /api/items/{id}", s.handleDeleteItem)
	mux.HandleFunc("POST /api/items/{id}/restore", s.handleRestoreItem)
	mux.HandleFunc("DELETE /api/items/{id}/purge", s.handlePurgeItem)
	mux.HandleFunc("POST /api/items/{id}/play", s.handlePlayItem)
	mux.HandleFunc("PUT /api/items/{id}/progress", s.handlePutProgress)
	mux.HandleFunc("GET /api/items/{id}/thumb", s.handleItemThumb)
	mux.HandleFunc("GET /api/files/{id}/stream", s.handleFileStream)
	mux.HandleFunc("GET /api/files/{id}/subs/{name}", s.handleFileSubtitle)
	mux.HandleFunc("GET /api/sessions/{sid}/master.m3u8", s.handleHLSPlaylist)
	mux.HandleFunc("GET /api/sessions/{sid}/{segment}", s.handleHLSSegment)
	mux.HandleFunc("DELETE /api/sessions/{sid}", s.handleDeleteSession)
	mux.HandleFunc("POST /api/sessions/{sid}/teardown", s.handleDeleteSession)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/collections", s.handleListCollections)
	mux.HandleFunc("POST /api/collections", s.handleCreateCollection)
	mux.HandleFunc("PATCH /api/collections/{id}", s.handlePatchCollection)
	mux.HandleFunc("DELETE /api/collections/{id}", s.handleDeleteCollection)
	mux.HandleFunc("POST /api/collections/{id}/items", s.handleAddCollectionItem)
	mux.HandleFunc("DELETE /api/collections/{id}/items/{itemId}", s.handleRemoveCollectionItem)
	mux.HandleFunc("PUT /api/collections/{id}/order", s.handleReorderCollection)
	mux.HandleFunc("POST /api/trash/purge", s.handlePurgeTrash)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/roots", s.handleListRoots)
	mux.HandleFunc("POST /api/roots", s.handleAddRoot)
	mux.HandleFunc("DELETE /api/roots/{id}", s.handleDetachRoot)
	mux.HandleFunc("POST /api/roots/{id}/rescan", s.handleRootRescan)
	mux.HandleFunc("GET /api/fs/dirs", s.handleFSDirs)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("POST /api/jobs/{id}/retry", s.handleRetryJob)
	mux.HandleFunc("POST /api/uploads", s.handleCreateUpload)
	mux.HandleFunc("GET /api/uploads/{id}", s.handleGetUpload)
	mux.HandleFunc("PUT /api/uploads/{id}", s.handlePutUploadChunk)
	mux.HandleFunc("POST /api/uploads/{id}/complete", s.handleCompleteUpload)
	mux.HandleFunc("DELETE /api/uploads/{id}", s.handleAbortUpload)

	// Unknown /api paths get the JSON 404 envelope, not the SPA fallback.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	mux.Handle("/", s.spaHandler())

	return s.requestLog(mux)
}

// spaHandler serves the embedded web app with an index.html fallback for
// client-side routes, or a plain placeholder when running without an
// embedded build (go run ./cmd/server during development).
func (s *Server) spaHandler() http.Handler {
	if s.webFS == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("media-server API is running.\n\nWeb app not embedded in this build; run the Vite dev server (cd web && npm run dev)\nor build with `make build`.\n"))
		})
	}
	fileServer := http.FileServerFS(s.webFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p != "/" {
			if _, err := fs.Stat(s.webFS, p[1:]); err != nil {
				// Client-side route: let the SPA router handle it.
				r.URL.Path = "/"
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.log.Debug("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush must pass through so SSE streaming works behind the logger.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
