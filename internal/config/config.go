// Package config loads and validates the server's YAML configuration.
//
// A single file (default ~/media-server/config.yml, overridable with
// --config) configures the whole server. Roots listed here are seeded into
// the database at boot; the database is the source of truth afterwards.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPath is where the server looks for config when --config is not given.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yml"
	}
	return filepath.Join(home, "media-server", "config.yml")
}

type Config struct {
	Server    Server    `yaml:"server"`
	DataDir   string    `yaml:"data_dir"`
	HLSCache  HLSCache  `yaml:"hls_cache"`
	Roots     []Root    `yaml:"library_roots"`
	Transcode Transcode `yaml:"transcode"`
	Upload    Upload    `yaml:"upload"`
	Trash     Trash     `yaml:"trash"`
	Log       Log       `yaml:"log"`
	Debug     Debug     `yaml:"debug"`
}

type Server struct {
	Bind string `yaml:"bind"`
	Port int    `yaml:"port"`
}

type HLSCache struct {
	Dir   string  `yaml:"dir"`
	MaxGB float64 `yaml:"max_gb"`
}

type Root struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
}

type Transcode struct {
	MaxConcurrent int    `yaml:"max_concurrent"`
	FFmpeg        string `yaml:"ffmpeg"`
	FFprobe       string `yaml:"ffprobe"`
}

type Upload struct {
	MinFreeGB float64 `yaml:"min_free_gb"`
}

type Trash struct {
	RetentionDays int `yaml:"retention_days"`
}

type Log struct {
	Level string `yaml:"level"` // debug|info|warn|error
}

// Debug exposes optional operator tooling. PprofPort, when non-zero, serves
// net/http/pprof on 127.0.0.1:<port> only (never the LAN bind) so the soak
// script can take heap/goroutine snapshots. Left 0 (disabled) by default.
type Debug struct {
	PprofPort int `yaml:"pprof_port"`
}

// Default returns a config populated with documented defaults. Load starts
// from this, so an empty file yields a runnable configuration.
func Default() Config {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, "media-server")
	return Config{
		Server:    Server{Bind: "0.0.0.0", Port: 8484},
		DataDir:   filepath.Join(base, "data"),
		HLSCache:  HLSCache{Dir: filepath.Join(base, "data", "hls"), MaxGB: 20},
		Transcode: Transcode{MaxConcurrent: 2},
		Upload:    Upload{MinFreeGB: 5},
		Trash:     Trash{RetentionDays: 7},
		Log:       Log{Level: "info"},
	}
}

// Load reads, parses, and validates the config file at path. Directories the
// server owns (data_dir, logs, thumbnails) are created if missing. Library
// root paths are NOT required to exist: an absent volume is the offline
// state, not a configuration error. The same applies to hls_cache.dir when
// it lives on a removable volume: it is created lazily once the volume is
// mounted.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.normalize(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) normalize() error {
	var err error
	if c.DataDir, err = expandTilde(c.DataDir); err != nil {
		return err
	}
	if c.HLSCache.Dir, err = expandTilde(c.HLSCache.Dir); err != nil {
		return err
	}
	if c.HLSCache.Dir != "" {
		c.HLSCache.Dir = filepath.Clean(c.HLSCache.Dir)
	}
	for i := range c.Roots {
		if c.Roots[i].Path, err = expandTilde(c.Roots[i].Path); err != nil {
			return err
		}
		c.Roots[i].Path = filepath.Clean(c.Roots[i].Path)
	}
	return nil
}

func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d out of range 1-65535", c.Server.Port)
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	for _, dir := range []string{c.DataDir, c.LogDir(), c.ThumbsDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := c.validateHLSCacheDir(); err != nil {
		return err
	}
	seenName := map[string]bool{}
	seenPath := map[string]bool{}
	for _, r := range c.Roots {
		if r.Name == "" {
			return fmt.Errorf("library root %q: name is required", r.Path)
		}
		if !filepath.IsAbs(r.Path) {
			return fmt.Errorf("library root %q: path must be absolute, got %q", r.Name, r.Path)
		}
		if seenName[r.Name] {
			return fmt.Errorf("duplicate library root name %q", r.Name)
		}
		if seenPath[r.Path] {
			return fmt.Errorf("duplicate library root path %q", r.Path)
		}
		seenName[r.Name] = true
		seenPath[r.Path] = true
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level %q must be one of debug, info, warn, error", c.Log.Level)
	}
	if c.Transcode.MaxConcurrent < 1 {
		return fmt.Errorf("transcode.max_concurrent must be >= 1")
	}
	if c.Trash.RetentionDays < 0 {
		return fmt.Errorf("trash.retention_days must be >= 0")
	}
	if c.Debug.PprofPort != 0 && (c.Debug.PprofPort < 1 || c.Debug.PprofPort > 65535) {
		return fmt.Errorf("debug.pprof_port %d out of range 1-65535", c.Debug.PprofPort)
	}
	return nil
}

// validateHLSCacheDir rejects a cache directory the library scanner would
// index (HLS output includes init.mp4, a video extension) and creates it
// unless its volume is unmounted, which is an offline state like any other.
func (c *Config) validateHLSCacheDir() error {
	dir := c.HLSCache.Dir
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("hls_cache.dir must be absolute, got %q", dir)
	}
	for _, r := range c.Roots {
		rel, err := filepath.Rel(r.Path, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if rel == "." {
			return fmt.Errorf("hls_cache.dir %q is library root %q itself; use a hidden subdirectory such as %s",
				dir, r.Name, filepath.Join(r.Path, ".hls"))
		}
		if !hiddenRelPath(rel) {
			return fmt.Errorf("hls_cache.dir %q is inside library root %q and would be scanned as media; use a hidden directory such as %s",
				dir, r.Name, filepath.Join(r.Path, ".hls"))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil && !volumeAbsent(dir) {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

func hiddenRelPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// volumeAbsent reports whether path lives under a /Volumes mount point that
// is not currently present, i.e. the removable disk is unplugged.
func volumeAbsent(path string) bool {
	const volumes = "/Volumes/"
	if !strings.HasPrefix(path, volumes) {
		return false
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(path, volumes), "/")
	if name == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(volumes, name))
	return err != nil
}

// DBPath is the SQLite database file inside data_dir.
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "media.db") }

// LogDir holds rotated server logs.
func (c *Config) LogDir() string { return filepath.Join(c.DataDir, "logs") }

// ThumbsDir holds generated thumbnails.
func (c *Config) ThumbsDir() string { return filepath.Join(c.DataDir, "thumbs") }

func expandTilde(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", p, err)
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	return p, nil
}
