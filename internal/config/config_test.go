package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeConfig(t, "data_dir: "+dir+"/data\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.Port != 8484 || cfg.Server.Bind != "0.0.0.0" {
		t.Errorf("server defaults: %+v", cfg.Server)
	}
	if cfg.Transcode.MaxConcurrent != 2 || cfg.Upload.MinFreeGB != 5 || cfg.Trash.RetentionDays != 7 {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("log level default = %q", cfg.Log.Level)
	}
}

func TestLoadCreatesOwnedDirectories(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeConfig(t, `
data_dir: `+dir+`/data
hls_cache:
  dir: `+dir+`/data/hls
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, d := range []string{cfg.DataDir, cfg.HLSCache.Dir, cfg.LogDir(), cfg.ThumbsDir()} {
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
}

func TestRootsMayPointAtAbsentVolumes(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(writeConfig(t, `
data_dir: `+dir+`/data
library_roots:
  - name: "Media A"
    path: "/Volumes/DoesNotExistYet"
`))
	if err != nil {
		t.Fatalf("absent root path must not fail validation (offline is a state): %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, yaml, wantErr string
	}{
		{"bad port", "data_dir: " + dir + "/d1\nserver:\n  port: 99999\n", "out of range"},
		{"relative root", "data_dir: " + dir + "/d2\nlibrary_roots:\n  - name: A\n    path: relative/path\n", "absolute"},
		{"unnamed root", "data_dir: " + dir + "/d3\nlibrary_roots:\n  - path: /Volumes/A\n", "name is required"},
		{"duplicate path", "data_dir: " + dir + "/d4\nlibrary_roots:\n  - name: A\n    path: /Volumes/A\n  - name: B\n    path: /Volumes/A\n", "duplicate"},
		{"bad log level", "data_dir: " + dir + "/d5\nlog:\n  level: chatty\n", "log.level"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestTildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := expandTilde("~/media-server/data")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, "media-server", "data") {
		t.Errorf("expandTilde = %q", got)
	}
	// A path that merely starts with ~ but isn't ~/ is left alone.
	got, _ = expandTilde("/data/~cache")
	if got != "/data/~cache" {
		t.Errorf("non-tilde path mangled: %q", got)
	}
}

func TestHLSCacheDirInsideRootMustBeHidden(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "Media")
	cases := []struct {
		name, hls, wantErr string
	}{
		{"visible subdir", filepath.Join(root, "hls"), "is not hidden"},
		{"root itself", root, "is a library root"},
		{"hidden subdir", filepath.Join(root, ".hls"), ""},
		{"nested under hidden", filepath.Join(root, ".cache", "hls"), ""},
		{"outside root", filepath.Join(dir, "hls"), ""},
		{"relative", "relative/hls", "must be absolute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, `
data_dir: `+dir+`/data
hls_cache:
  dir: "`+tc.hls+`"
library_roots:
  - name: Media
    path: `+root+`
`))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if info, statErr := os.Stat(cfg.HLSCache.Dir); statErr != nil || !info.IsDir() {
					t.Fatalf("hls dir not created: %v", statErr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestHLSCacheDirOnAbsentVolumeIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeConfig(t, `
data_dir: `+dir+`/data
hls_cache:
  dir: /Volumes/DoesNotExistYet/.hls
library_roots:
  - name: Media
    path: /Volumes/DoesNotExistYet
`))
	if err != nil {
		t.Fatalf("unplugged cache volume must be an offline state, not a boot failure: %v", err)
	}
	if cfg.HLSCache.Dir != "/Volumes/DoesNotExistYet/.hls" {
		t.Fatalf("hls dir = %q", cfg.HLSCache.Dir)
	}
}
