package library

import "testing"

// probe: UpNext and NextEpisode hold at the seams of M2, M10, B2, specials and multi-episode ranges.
func TestProbe_UpNext_Seams(t *testing.T) {
	cases := []struct {
		name     string
		episodes []EpisodeState
		want     int64
		wantOff  bool
	}{
		{name: "B2: after the last special comes the first unwatched main episode",
			episodes: []EpisodeState{epState(9, 0, 1).done(20), epState(1, 1, 1).done(10), epState(2, 1, 2)}, want: 2},
		{name: "B2: last special with every main episode watched is caught up",
			episodes: []EpisodeState{epState(9, 0, 1).done(20), epState(1, 1, 1).done(10)}, want: 0},
		{name: "B2: a special that is not the last leads to the next special",
			episodes: []EpisodeState{epState(9, 0, 1).done(20), epState(10, 0, 2), epState(1, 1, 1)}, want: 10},
		{name: "M2: an open special newer than a main completion wins",
			episodes: []EpisodeState{epState(1, 1, 1).done(10), epState(2, 1, 2), epState(9, 0, 1).open(20, 60)}, want: 9},
		{name: "M2: a same-second open behind the completion loses",
			episodes: []EpisodeState{epState(1, 1, 1).open(10, 60), epState(3, 1, 3).done(10), epState(4, 1, 4)}, want: 4},
		{name: "M2: a same-second open ahead of the completion wins",
			episodes: []EpisodeState{epState(3, 1, 3).done(10), epState(4, 1, 4), epState(5, 1, 5).open(10, 60)}, want: 5},
		{name: "M10: an offline next episode is returned, not caught up",
			episodes: []EpisodeState{epState(1, 1, 1).done(10), epState(2, 1, 2).offline()}, want: 2, wantOff: true},
		{name: "an offline open episode is still resumed",
			episodes: []EpisodeState{epState(1, 1, 1).done(10), epState(2, 1, 2).open(20, 60).offline(), epState(3, 1, 3)}, want: 2, wantOff: true},
		{name: "a completed multi-episode file leads past its whole range",
			episodes: []EpisodeState{epState(1, 1, 1).through(2).done(10), epState(2, 1, 2), epState(3, 1, 3)}, want: 3},
	}
	for _, tc := range cases {
		got := UpNext(tc.episodes)
		switch {
		case tc.want == 0 && got != nil:
			t.Errorf("%s: UpNext = %d, want caught up", tc.name, got.ItemID)
		case tc.want != 0 && (got == nil || got.ItemID != tc.want || got.Available == tc.wantOff):
			t.Errorf("%s: UpNext = %+v, want %d (offline %v)", tc.name, got, tc.want, tc.wantOff)
		}
	}

	next := []struct {
		name     string
		episodes []EpisodeState
		current  int64
		want     int64
	}{
		{"skips an offline episode and a gap across a season",
			[]EpisodeState{epState(1, 1, 8), epState(2, 2, 1).offline(), epState(3, 2, 3)}, 1, 3},
		{"the last special never runs into season 1",
			[]EpisodeState{epState(9, 0, 2), epState(1, 1, 1)}, 9, 0},
		{"the last main episode never runs into specials",
			[]EpisodeState{epState(1, 1, 1), epState(9, 0, 1)}, 1, 0},
		{"a multi-episode file skips the numbers it covers",
			[]EpisodeState{epState(1, 1, 1).through(3), epState(2, 1, 2), epState(4, 1, 4)}, 1, 4},
	}
	for _, tc := range next {
		got := NextEpisode(tc.episodes, tc.current)
		switch {
		case tc.want == 0 && got != nil:
			t.Errorf("%s: NextEpisode = %d, want none", tc.name, got.ItemID)
		case tc.want != 0 && (got == nil || got.ItemID != tc.want):
			t.Errorf("%s: NextEpisode = %+v, want %d", tc.name, got, tc.want)
		}
	}
}
