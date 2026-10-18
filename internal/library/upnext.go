package library

import (
	"sort"
	"time"
)

// EpisodeState is one library episode of a show as UpNext and NextEpisode
// see it. EpisodeEnd is zero unless the file spans several episodes.
type EpisodeState struct {
	ItemID     int64
	Season     int
	Episode    int
	EpisodeEnd int
	Available  bool
	Progress   *EpisodeProgress
}

type EpisodeProgress struct {
	PositionS float64
	Completed bool
	UpdatedAt time.Time
}

func (e EpisodeState) special() bool { return e.Season == 0 }

func (e EpisodeState) last() int {
	if e.EpisodeEnd > e.Episode {
		return e.EpisodeEnd
	}
	return e.Episode
}

func (e EpisodeState) inProgress() bool {
	return e.Progress != nil && !e.Progress.Completed && e.Progress.PositionS > 0
}

func (e EpisodeState) completed() bool {
	return e.Progress != nil && e.Progress.Completed
}

// UpNext picks the episode a show card plays, or nil when the viewer has
// caught up. The returned episode may be unavailable (ruling M10).
func UpNext(episodes []EpisodeState) *EpisodeState {
	ordered := inEpisodeOrder(episodes)
	if len(ordered) == 0 {
		return nil
	}

	var latestDone, latestOpen *EpisodeState
	for i := range ordered {
		e := &ordered[i]
		if e.completed() && (latestDone == nil || !e.Progress.UpdatedAt.Before(latestDone.Progress.UpdatedAt)) {
			latestDone = e
		}
		if e.inProgress() && (latestOpen == nil || !e.Progress.UpdatedAt.Before(latestOpen.Progress.UpdatedAt)) {
			latestOpen = e
		}
	}

	// Ruling M2: an open older than the latest completion is a stale glance.
	if latestOpen != nil && (latestDone == nil || openBeatsDone(*latestOpen, *latestDone)) {
		return found(*latestOpen)
	}
	if latestDone != nil {
		return after(ordered, *latestDone, false)
	}
	for _, e := range ordered {
		if !e.special() {
			return found(e)
		}
	}
	return found(ordered[0])
}

// NextEpisode is what autoplay plays after current: the next available
// episode in the same sequence (specials or main), skipping gaps and
// offline files.
func NextEpisode(episodes []EpisodeState, currentItemID int64) *EpisodeState {
	ordered := inEpisodeOrder(episodes)
	for _, e := range ordered {
		if e.ItemID == currentItemID {
			return after(ordered, e, true)
		}
	}
	return nil
}

func openBeatsDone(open, done EpisodeState) bool {
	switch {
	case open.Progress.UpdatedAt.After(done.Progress.UpdatedAt):
		return true
	case open.Progress.UpdatedAt.Equal(done.Progress.UpdatedAt):
		// Store timestamps have one-second resolution; advancing from the
		// Next card can stamp both rows in the same second.
		return open.special() == done.special() && follows(open, done)
	}
	return false
}

func after(ordered []EpisodeState, current EpisodeState, availableOnly bool) *EpisodeState {
	for _, e := range ordered {
		if e.special() != current.special() || !follows(e, current) {
			continue
		}
		if availableOnly && !e.Available {
			continue
		}
		return found(e)
	}
	return nil
}

func follows(e, current EpisodeState) bool {
	if e.Season != current.Season {
		return e.Season > current.Season
	}
	return e.Episode > current.last()
}

func inEpisodeOrder(episodes []EpisodeState) []EpisodeState {
	ordered := append([]EpisodeState(nil), episodes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.special() != b.special() {
			return !a.special()
		}
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		return a.ItemID < b.ItemID
	})
	return ordered
}

func found(e EpisodeState) *EpisodeState { return &e }
