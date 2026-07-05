// Command server is the single-binary media server: config → db → services
// → http, per docs/SPEC-BACKEND.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof" // registers pprof handlers on http.DefaultServeMux
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/JeremyVun/MediaServer/internal/config"
	"github.com/JeremyVun/MediaServer/internal/db"
	"github.com/JeremyVun/MediaServer/internal/events"
	"github.com/JeremyVun/MediaServer/internal/httpapi"
	"github.com/JeremyVun/MediaServer/internal/jobs"
	"github.com/JeremyVun/MediaServer/internal/logging"
	"github.com/JeremyVun/MediaServer/internal/playback"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/watcher"
	webdist "github.com/JeremyVun/MediaServer/web"
)

// version is stamped by the Makefile via -ldflags.
var version = "1.0.0"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", config.DefaultPath(), "path to config.yml")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger, closeLog, err := logging.Setup(cfg.LogDir(), cfg.Log.Level)
	if err != nil {
		return err
	}
	defer closeLog()

	if err := checkTools(cfg); err != nil {
		return err
	}

	sqldb, err := db.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer sqldb.Close()
	if err := db.Migrate(sqldb); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	schemaVersion, _ := db.Version(sqldb)
	logger.Info("database ready", "path", cfg.DBPath(), "schema_version", schemaVersion)

	st := store.New(sqldb)
	bus := events.NewBus()
	defer bus.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := seedRoots(ctx, cfg, st, logger); err != nil {
		return err
	}

	playbackManager := playback.NewManager(playback.Options{
		CacheDir:        cfg.HLSCache.Dir,
		MaxBytes:        int64(cfg.HLSCache.MaxGB * 1000 * 1000 * 1000),
		FFmpeg:          cfg.Transcode.FFmpeg,
		MaxVideoWorkers: cfg.Transcode.MaxConcurrent,
		Log:             logger,
	})

	jobManager := jobs.NewManager(jobs.Options{
		Store:              st,
		Bus:                bus,
		Log:                logger,
		FFprobe:            cfg.Transcode.FFprobe,
		FFmpeg:             cfg.Transcode.FFmpeg,
		ThumbsDir:          cfg.ThumbsDir(),
		TrashRetentionDays: cfg.Trash.RetentionDays,
		UploadMaxAge:       7 * 24 * time.Hour,
		CleanupInterval:    time.Hour,
		HLSPruner:          playbackManager,
	})
	jobManager.Start(ctx)

	ignoreSet := watcher.NewIgnoreSet()
	watchManager := watcher.NewManager(watcher.Options{
		Store:  st,
		Jobs:   jobManager,
		Bus:    bus,
		Log:    logger,
		Ignore: ignoreSet,
	})
	if err := watchManager.Start(ctx); err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}
	playbackManager.Start(ctx)

	api := httpapi.NewServer(httpapi.Options{
		Store:         st,
		Bus:           bus,
		Log:           logger,
		Jobs:          jobManager,
		RootLifecycle: watchManager,
		Version:       version,
		WebFS:         webdist.FS(),
		ThumbsDir:     cfg.ThumbsDir(),
		Playback:      playbackManager,
		Subtitles: playback.SubtitleExtractor{
			Binary:   cfg.Transcode.FFmpeg,
			CacheDir: filepath.Join(cfg.ThumbsDir(), "subs"),
			Log:      logger,
		},
		UploadIgnore: ignoreSet,
		UploadMinGB:  cfg.Upload.MinFreeGB,
	})

	if port := cfg.Debug.PprofPort; port != 0 {
		// Loopback only: pprof exposes internals and is a DoS surface, so it
		// never rides the LAN bind. net/http/pprof registered its handlers on
		// DefaultServeMux via its import side effect; the API uses its own mux.
		pprofSrv := &http.Server{
			Addr:              net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
			Handler:           http.DefaultServeMux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			logger.Info("pprof listening", "addr", pprofSrv.Addr)
			if err := pprofSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				logger.Warn("pprof server", "error", err)
			}
		}()
		defer pprofSrv.Close()
	}

	addr := net.JoinHostPort(cfg.Server.Bind, strconv.Itoa(cfg.Server.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Derive every request context from the signal context so that on
		// SIGTERM long-lived handlers (SSE streams, in-flight upload chunks)
		// see a cancelled context and return at once, letting Shutdown finish
		// well inside the target window instead of waiting them out.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr, "version", version)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Graceful shutdown (SPEC-BACKEND deployment, target < 3 s): stop
	// accepting, kill ffmpeg children, drain the background workers, then let
	// the deferred bus/DB closes run last with nothing still writing. ctx is
	// already cancelled here (the signal fired), so the subsystems are winding
	// down on their own; this just orders and bounds the join.
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown incomplete", "error", err)
	}
	playbackManager.Shutdown(shutdownCtx)
	if !waitFor(shutdownCtx, jobManager.Wait, watchManager.Wait) {
		logger.Warn("shutdown timed out draining workers; closing anyway")
	}
	logger.Info("shutdown complete")
	return nil
}

// waitFor runs each blocking function in its own goroutine and reports whether
// they all returned before ctx expired.
func waitFor(ctx context.Context, fns ...func()) bool {
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		wg.Add(len(fns))
		for _, fn := range fns {
			go func(f func()) { defer wg.Done(); f() }(fn)
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// seedRoots upserts config-listed roots into the DB (the DB is the source
// of truth afterwards) and records each root's current online state.
func seedRoots(ctx context.Context, cfg config.Config, st *store.Store, logger *slog.Logger) error {
	for _, rc := range cfg.Roots {
		root, err := st.UpsertRoot(ctx, rc.Name, rc.Path)
		if err != nil {
			return fmt.Errorf("seed root %q: %w", rc.Name, err)
		}
		online := dirExists(rc.Path)
		if err := st.SetRootOnline(ctx, root.ID, online); err != nil {
			return fmt.Errorf("seed root %q: %w", rc.Name, err)
		}
		if online {
			logger.Info("library root online", "name", rc.Name, "path", rc.Path)
		} else {
			logger.Warn("library root offline (volume not mounted)", "name", rc.Name, "path", rc.Path)
		}
	}
	return nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// checkTools fails startup with a clear message when ffmpeg/ffprobe are
// missing (SPEC-BACKEND toolchain rule).
func checkTools(cfg config.Config) error {
	for name, override := range map[string]string{
		"ffmpeg":  cfg.Transcode.FFmpeg,
		"ffprobe": cfg.Transcode.FFprobe,
	} {
		bin := override
		if bin == "" {
			bin = name
		}
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf(
				"%s not found (looked for %q): install it (brew install ffmpeg) or set transcode.%s in config",
				name, bin, name)
		}
	}
	return nil
}
