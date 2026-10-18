package semantic

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeCodex(t *testing.T, mode string) (*Classifier, string) {
	t.Helper()
	bin, err := filepath.Abs("testdata/fake-codex.sh")
	if err != nil {
		t.Fatal(err)
	}
	record := t.TempDir()
	t.Setenv("FAKE_CODEX_MODE", mode)
	t.Setenv("FAKE_CODEX_RECORD", record)
	return &Classifier{Bin: bin, Timeout: 10 * time.Second}, record
}

var sample = Input{
	RelPath:     "Anime/Your Lie in April/Season 1/[Group] Shigatsu wa Kimi no Uso - 05 [1080p].mkv",
	Folders:     []string{"Anime", "Your Lie in April", "Season 1"},
	DurationSec: 1430,
	ShowTitles:  []string{"Dark Matter", "Shigatsu wa Kimi no Uso"},
}

func TestClassifyHappyPath(t *testing.T) {
	c, record := fakeCodex(t, "ok")
	got, err := c.Classify(context.Background(), sample)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindEpisode || got.ShowTitle != "Shigatsu wa Kimi no Uso" || !got.Confident ||
		got.Season == nil || *got.Season != 1 || got.Episode == nil || *got.Episode != 5 ||
		got.EpisodeEnd != nil || got.Year == nil || *got.Year != 2014 || got.Title != "" {
		t.Errorf("answer = %+v", got)
	}

	raw, err := os.ReadFile(filepath.Join(record, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	wantPrefix := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "-s", "read-only", "-m", "gpt-6.1-sol", "--output-schema"}
	if len(argv) != len(wantPrefix)+2 || strings.Join(argv[:len(wantPrefix)], " ") != strings.Join(wantPrefix, " ") {
		t.Fatalf("argv = %q", argv)
	}
	cwd := readFile(t, record, "cwd")
	if filepath.Dir(argv[len(argv)-2]) != cwd {
		t.Errorf("schema %q not in working dir %q", argv[len(argv)-2], cwd)
	}
	prompt := argv[len(argv)-1]
	for _, want := range []string{sample.RelPath, `"Your Lie in April"`, `"duration_minutes": 23.8`, `"Dark Matter"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %s:\n%s", want, prompt)
		}
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("temp working dir %s not removed: %v", cwd, err)
	}

	var s struct {
		Required             []string                  `json:"required"`
		Properties           map[string]map[string]any `json:"properties"`
		AdditionalProperties *bool                     `json:"additionalProperties"`
	}
	if err := json.Unmarshal([]byte(readFile(t, record, "schema.json")), &s); err != nil {
		t.Fatal(err)
	}
	if s.AdditionalProperties == nil || *s.AdditionalProperties || len(s.Required) != len(s.Properties) || len(s.Properties) != 9 {
		t.Errorf("schema breaks structured-output rules: %+v", s)
	}
}

func TestClassifyFailures(t *testing.T) {
	for mode, want := range map[string]string{
		"malformed":     "decode answer",
		"bad-kind":      "unknown kind",
		"no-answer":     "no agent message",
		"login-expired": "401 Unauthorized",
		"crash":         "panicked",
	} {
		t.Run(mode, func(t *testing.T) {
			c, _ := fakeCodex(t, mode)
			_, err := c.Classify(context.Background(), sample)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want it to mention %q", err, want)
			}
		})
	}
}

func TestClassifyTimeoutKillsChild(t *testing.T) {
	c, record := fakeCodex(t, "sleep")
	c.Timeout = 500 * time.Millisecond
	start := time.Now()
	_, err := c.Classify(context.Background(), sample)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Classify returned after %s", took)
	}
	for _, name := range []string{"pid", "sleeppid"} {
		pid, err := strconv.Atoi(readFile(t, record, name))
		if err != nil {
			t.Fatal(err)
		}
		if !dies(pid, 3*time.Second) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("%s %d still alive after timeout", name, pid)
		}
	}
	if _, err := os.Stat(readFile(t, record, "cwd")); !os.IsNotExist(err) {
		t.Errorf("temp working dir not removed: %v", err)
	}
}

func TestClassifyMissingBinary(t *testing.T) {
	c := &Classifier{Bin: filepath.Join(t.TempDir(), "codex")}
	if c.Available() {
		t.Error("Available() = true for a missing binary")
	}
	if _, err := c.Classify(context.Background(), sample); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestAvailable(t *testing.T) {
	c, _ := fakeCodex(t, "ok")
	if !c.Available() {
		t.Error("Available() = false for the fake script")
	}
	t.Setenv("PATH", filepath.Dir(c.Bin))
	if _, err := (&Classifier{}).binary(); err != nil {
		if _, statErr := os.Stat(homebrewCodex); statErr == nil {
			t.Errorf("did not fall back to %s: %v", homebrewCodex, err)
		}
	}
}

func TestDecodeAnswerRejectsIncompleteEpisode(t *testing.T) {
	_, err := decodeAnswer(`{"kind":"episode","show_title":"","season":1,"episode":2,"episode_end":null,"episode_title":null,"title":null,"year":null,"confident":true}`)
	if err == nil {
		t.Error("accepted an episode without a show title")
	}
	_, err = decodeAnswer(`{"kind":"movie","show_title":null,"season":null,"episode":null,"episode_end":null,"episode_title":null,"title":"Heat","year":1995,"confident":true,"extra":1}`)
	if err == nil {
		t.Error("accepted an unknown field")
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// dies polls because a killed orphan is reaped by launchd asynchronously.
func dies(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
