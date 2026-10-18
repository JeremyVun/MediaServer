package library

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func epState(id int64, season, episode int) EpisodeState {
	return EpisodeState{ItemID: id, Season: season, Episode: episode, Available: true}
}

func (e EpisodeState) done(minute int) EpisodeState {
	e.Progress = &EpisodeProgress{PositionS: 3000, Completed: true, UpdatedAt: at(minute)}
	return e
}

func (e EpisodeState) open(minute int, position float64) EpisodeState {
	e.Progress = &EpisodeProgress{PositionS: position, UpdatedAt: at(minute)}
	return e
}

func (e EpisodeState) offline() EpisodeState {
	e.Available = false
	return e
}

func (e EpisodeState) through(end int) EpisodeState {
	e.EpisodeEnd = end
	return e
}

func TestUpNext(t *testing.T) {
	cases := []struct {
		name     string
		episodes []EpisodeState
		want     int64 // 0 = caught up
		wantOff  bool
	}{
		{name: "empty show", episodes: nil, want: 0},
		{name: "never watched starts at S1E1 in any input order",
			episodes: []EpisodeState{epState(3, 1, 3), epState(1, 1, 1), epState(2, 1, 2)}, want: 1},
		{name: "never watched skips specials",
			episodes: []EpisodeState{epState(9, 0, 1), epState(2, 1, 2), epState(4, 2, 1)}, want: 2},
		{name: "never watched with only specials plays the first special",
			episodes: []EpisodeState{epState(8, 0, 2), epState(7, 0, 1)}, want: 7},
		{name: "never watched first episode offline is still returned",
			episodes: []EpisodeState{epState(1, 1, 1).offline(), epState(2, 1, 2)}, want: 1, wantOff: true},
		{name: "after a completion comes the next episode",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(2, 1, 2), epState(3, 1, 3)}, want: 2},
		{name: "most recent completion wins over episode order",
			episodes: []EpisodeState{epState(1, 1, 1).done(10), epState(2, 1, 2).done(5), epState(3, 1, 3), epState(4, 1, 4)}, want: 2},
		{name: "gap in numbering is skipped",
			episodes: []EpisodeState{epState(1, 1, 3).done(0), epState(2, 1, 5)}, want: 2},
		{name: "season boundary",
			episodes: []EpisodeState{epState(1, 1, 8).done(0), epState(2, 2, 1), epState(3, 2, 2)}, want: 2},
		{name: "multi-episode file occupies its range",
			episodes: []EpisodeState{epState(1, 2, 1).through(2).done(0), epState(2, 2, 2), epState(3, 2, 3)}, want: 3},
		{name: "after the episode before a multi-episode file",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(2, 1, 2).through(3), epState(3, 1, 4)}, want: 2},
		{name: "caught up",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(2, 1, 2).done(5)}, want: 0},
		{name: "a rewatched earlier episode points at the one after it",
			episodes: []EpisodeState{epState(1, 1, 1).done(50), epState(2, 1, 2).done(5)}, want: 2},
		{name: "in progress newer than the latest completion resumes",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(2, 1, 2).open(10, 600), epState(3, 1, 3)}, want: 2},
		{name: "most recently updated in-progress wins",
			episodes: []EpisodeState{epState(1, 1, 1).open(30, 60), epState(2, 1, 2).open(10, 600), epState(3, 1, 3)}, want: 1},
		{name: "M2 stale open older than the latest completion is ignored",
			episodes: []EpisodeState{
				epState(1, 1, 1).done(100), epState(2, 1, 2).done(200), epState(5, 1, 5).open(1, 17),
				epState(3, 1, 3).done(300), epState(4, 1, 4), epState(6, 1, 6),
			}, want: 4},
		{name: "open stamped in the same second as the completion before it resumes",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(2, 1, 2).open(0, 5), epState(3, 1, 3)}, want: 2},
		{name: "zero position is not in progress",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(3, 1, 3).open(10, 0), epState(2, 1, 2)}, want: 2},
		{name: "M10 offline next episode is returned flagged",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(2, 1, 2).offline(), epState(3, 1, 3)}, want: 2, wantOff: true},
		{name: "M10 offline last episode is not caught up",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(2, 1, 2).offline()}, want: 2, wantOff: true},
		{name: "completed special leads to the next special",
			episodes: []EpisodeState{epState(1, 1, 1), epState(7, 0, 1).done(0), epState(8, 0, 2)}, want: 8},
		{name: "completed last special is caught up on the special sequence",
			episodes: []EpisodeState{epState(1, 1, 1), epState(7, 0, 1).done(0)}, want: 0},
		{name: "duplicate files of one episode are passed over together",
			episodes: []EpisodeState{epState(1, 1, 1).done(0), epState(2, 1, 1), epState(3, 1, 2)}, want: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UpNext(tc.episodes)
			if tc.want == 0 {
				if got != nil {
					t.Fatalf("UpNext = item %d, want caught up", got.ItemID)
				}
				return
			}
			if got == nil {
				t.Fatalf("UpNext = caught up, want item %d", tc.want)
			}
			if got.ItemID != tc.want {
				t.Fatalf("UpNext = item %d, want %d", got.ItemID, tc.want)
			}
			if got.Available == tc.wantOff {
				t.Fatalf("UpNext available = %v, want %v", got.Available, !tc.wantOff)
			}
		})
	}
}

func TestNextEpisode(t *testing.T) {
	cases := []struct {
		name     string
		episodes []EpisodeState
		current  int64
		want     int64 // 0 = none
	}{
		{name: "next in season",
			episodes: []EpisodeState{epState(1, 1, 1), epState(2, 1, 2)}, current: 1, want: 2},
		{name: "across a season boundary",
			episodes: []EpisodeState{epState(1, 1, 8), epState(2, 2, 1)}, current: 1, want: 2},
		{name: "gap is skipped",
			episodes: []EpisodeState{epState(1, 1, 3), epState(2, 1, 5)}, current: 1, want: 2},
		{name: "M10 offline episodes are skipped",
			episodes: []EpisodeState{epState(1, 1, 1), epState(2, 1, 2).offline(), epState(3, 1, 3)}, current: 1, want: 3},
		{name: "only offline episodes remain",
			episodes: []EpisodeState{epState(1, 1, 1), epState(2, 1, 2).offline()}, current: 1, want: 0},
		{name: "last episode has no next",
			episodes: []EpisodeState{epState(1, 1, 1), epState(2, 1, 2)}, current: 2, want: 0},
		{name: "multi-episode current skips its range",
			episodes: []EpisodeState{epState(1, 1, 1).through(2), epState(2, 1, 2), epState(3, 1, 3)}, current: 1, want: 3},
		{name: "main sequence never runs into specials",
			episodes: []EpisodeState{epState(1, 1, 1), epState(7, 0, 1)}, current: 1, want: 0},
		{name: "special goes to the next special, not season 1",
			episodes: []EpisodeState{epState(1, 1, 1), epState(7, 0, 1), epState(8, 0, 2)}, current: 7, want: 8},
		{name: "unknown current item",
			episodes: []EpisodeState{epState(1, 1, 1)}, current: 99, want: 0},
		{name: "progress does not matter",
			episodes: []EpisodeState{epState(1, 1, 1), epState(2, 1, 2).done(0), epState(3, 1, 3)}, current: 1, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextEpisode(tc.episodes, tc.current)
			var id int64
			if got != nil {
				id = got.ItemID
			}
			if id != tc.want {
				t.Fatalf("NextEpisode = %d, want %d", id, tc.want)
			}
		})
	}
}
