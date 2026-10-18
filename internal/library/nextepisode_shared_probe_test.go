package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// probe: the server's NextEpisode answers the table web/src/player/nextEpisode.probe.test.ts holds the client to, so the two sides stay locked together.
func TestProbe_NextEpisode_SharedTableWithClient(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "player", "testdata", "next-episode-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []struct {
			Name     string  `json:"name"`
			Current  int64   `json:"current"`
			Expected *int64  `json:"expected"`
			Episodes [][]any `json:"episodes"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	num := func(v any, fallback int) int {
		if f, ok := v.(float64); ok {
			return int(f)
		}
		return fallback
	}
	for _, c := range table.Cases {
		var states []EpisodeState
		for _, row := range c.Episodes {
			states = append(states, EpisodeState{
				ItemID:     int64(num(row[0], 0)),
				Season:     num(row[1], 1),
				Episode:    num(row[2], 0),
				EpisodeEnd: num(row[3], 0),
				Available:  row[4] == true,
			})
		}
		got := NextEpisode(states, c.Current)
		switch {
		case c.Expected == nil && got != nil:
			t.Errorf("%s: NextEpisode = %d, want none", c.Name, got.ItemID)
		case c.Expected != nil && (got == nil || got.ItemID != *c.Expected):
			t.Errorf("%s: NextEpisode = %+v, want %d", c.Name, got, *c.Expected)
		}
	}
	if len(table.Cases) == 0 {
		t.Fatal("empty table")
	}
}
