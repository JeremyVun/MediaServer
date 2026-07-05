package logging

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingWriterRotatesAndCaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")

	// Tiny limits so the test is fast: 100-byte files, keep 3.
	w, err := newRotatingWriter(path, 100, 3)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Close()

	line := bytes.Repeat([]byte("x"), 39)
	line = append(line, '\n') // 40 bytes → 2 lines per file
	for range 20 {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// Current file must be under the limit.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat current: %v", err)
	}
	if info.Size() > 100 {
		t.Errorf("current log %d bytes, want <= 100", info.Size())
	}

	// Rotated files exist up to .2 and never beyond keep-1.
	for i := 1; i <= 2; i++ {
		if _, err := os.Stat(fmt.Sprintf("%s.%d", path, i)); err != nil {
			t.Errorf("expected rotated file .%d: %v", i, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("rotation kept more than %d files", 3)
	}
}

func TestSetupWritesJSONAndParsesLevels(t *testing.T) {
	dir := t.TempDir()
	logger, closeLog, err := Setup(dir, "warn")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	logger.Warn("hello", "k", "v")
	logger.Info("filtered out")
	if err := closeLog(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"msg":"hello"`)) {
		t.Errorf("warn line missing from JSON log: %s", raw)
	}
	if bytes.Contains(raw, []byte("filtered out")) {
		t.Errorf("info line written despite warn level: %s", raw)
	}

	if _, err := ParseLevel("chatty"); err == nil {
		t.Error("bad level accepted")
	}
}
