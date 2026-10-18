// Package semantic asks Sol, via the Codex CLI, to classify a media file
// the filename parser is unsure about.
package semantic

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	Model          = "gpt-6.1-sol"
	DefaultTimeout = 120 * time.Second
	// launchd's PATH omits Homebrew.
	homebrewCodex = "/opt/homebrew/bin/codex"
	waitDelay     = 2 * time.Second
	stderrTail    = 500
)

const (
	KindMovie   = "movie"
	KindEpisode = "episode"
	KindVideo   = "video"
)

var ErrUnavailable = errors.New("codex binary not found")

//go:embed schema.json
var schema []byte

type Input struct {
	RelPath     string
	Folders     []string // parent folder names, outermost first
	DurationSec float64
	ShowTitles  []string // existing local shows, so Sol can reuse one
}

// Answer mirrors schema.json. Nullable integers are pointers because
// season 0 (specials) is meaningful; a null string decodes to "".
type Answer struct {
	Kind         string `json:"kind"`
	ShowTitle    string `json:"show_title"`
	Season       *int   `json:"season"`
	Episode      *int   `json:"episode"`
	EpisodeEnd   *int   `json:"episode_end"`
	EpisodeTitle string `json:"episode_title"`
	Title        string `json:"title"`
	Year         *int   `json:"year"`
	Confident    bool   `json:"confident"`
}

// Classifier's zero value resolves codex on PATH and uses DefaultTimeout.
type Classifier struct {
	Bin     string
	Timeout time.Duration
}

func (c *Classifier) Available() bool {
	_, err := c.binary()
	return err == nil
}

func (c *Classifier) binary() (string, error) {
	if c.Bin != "" {
		if p, err := exec.LookPath(c.Bin); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("%w: %s", ErrUnavailable, c.Bin)
	}
	if p, err := exec.LookPath("codex"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath(homebrewCodex); err == nil {
		return p, nil
	}
	return "", ErrUnavailable
}

func (c *Classifier) Classify(ctx context.Context, in Input) (Answer, error) {
	bin, err := c.binary()
	if err != nil {
		return Answer{}, fmt.Errorf("sol: %w", err)
	}
	dir, err := os.MkdirTemp("", "media-server-sol-*")
	if err != nil {
		return Answer{}, fmt.Errorf("sol: %w", err)
	}
	defer os.RemoveAll(dir)
	schemaPath := filepath.Join(dir, "schema.json")
	if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
		return Answer{}, fmt.Errorf("sol: %w", err)
	}
	prompt, err := buildPrompt(in)
	if err != nil {
		return Answer{}, fmt.Errorf("sol: %w", err)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "exec", "--json", "--ephemeral", "--skip-git-repo-check",
		"-s", "read-only", "-m", Model, "--output-schema", schemaPath, prompt)
	cmd.Dir = dir
	// A nil Stdin is /dev/null; codex would otherwise wait on a piped stdin.
	cmd.Stdin = nil
	// Kill the whole process group so codex's own children die with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	runErr := cmd.Run()
	if ctx.Err() != nil {
		return Answer{}, fmt.Errorf("sol: codex did not finish in %s: %w", timeout, ctx.Err())
	}
	text, failure := lastAgentMessage(stdout.Bytes())
	if runErr != nil || failure != "" {
		reason := cmp.Or(failure, tail(stderr.String()))
		return Answer{}, fmt.Errorf("sol: codex failed (%v): %s", runErr, reason)
	}
	if text == "" {
		return Answer{}, errors.New("sol: codex returned no agent message")
	}
	return decodeAnswer(text)
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > stderrTail {
		s = "…" + s[len(s)-stderrTail:]
	}
	return s
}

// lastAgentMessage scans codex's --json event stream for the final agent
// message, and for any error or turn failure it reports.
func lastAgentMessage(jsonl []byte) (text, failure string) {
	sc := bufio.NewScanner(bytes.NewReader(jsonl))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Item    *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "item.completed":
			if ev.Item != nil && ev.Item.Type == "agent_message" {
				text = ev.Item.Text
			}
		case "error":
			failure = ev.Message
		case "turn.failed":
			if ev.Error != nil {
				failure = ev.Error.Message
			}
			failure = cmp.Or(failure, "turn failed")
		}
	}
	return text, failure
}

func decodeAnswer(text string) (Answer, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var a Answer
	if err := dec.Decode(&a); err != nil {
		return Answer{}, fmt.Errorf("sol: decode answer: %w", err)
	}
	if dec.More() {
		return Answer{}, errors.New("sol: decode answer: trailing data")
	}
	switch a.Kind {
	case KindMovie, KindVideo:
	case KindEpisode:
		if a.ShowTitle == "" || a.Episode == nil {
			return Answer{}, errors.New("sol: episode answer without show_title or episode")
		}
	default:
		return Answer{}, fmt.Errorf("sol: unknown kind %q", a.Kind)
	}
	return a, nil
}
