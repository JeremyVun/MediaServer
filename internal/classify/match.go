package classify

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/store"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

// maxEpisodeProbes bounds the episode lists fetched to break a namesake tie.
const maxEpisodeProbes = 3

type match struct {
	result    tvmaze.Result
	confident bool
	found     bool
}

// showQuery is what one arrival knows about its show.
type showQuery struct {
	name, key string
	year      *int
	season    int
	episode   int // 0: no episode to break a namesake tie with
}

// decision is the confident-match test's verdict on one set of results;
// ties are the name matches it could not choose between, best first.
type decision struct {
	match
	ties []tvmaze.Result
}

// match searches TVmaze for a show key, with its country code and then
// without (rulings M1, B7). Only a confident match may be attached; the best
// weak result is returned for logging. Any TVmaze error other than not found
// means unreachable.
func (s *Service) match(ctx context.Context, q showQuery) (match, error) {
	var seen []tvmaze.Result
	var d decision
	for _, text := range searchQueries(q.key) {
		results, err := s.tvmaze.Search(ctx, text)
		if errors.Is(err, tvmaze.ErrNotFound) {
			continue
		}
		if err != nil {
			return match{}, tvmazeError{err}
		}
		seen = appendNew(seen, results)
		if d = decide(seen, q.key, q.year); d.confident {
			return d.match, nil
		}
	}
	if len(d.ties) >= 2 && q.episode > 0 {
		r, ok, err := s.byEpisode(ctx, d.ties, q.season, q.episode)
		if err != nil {
			return match{}, err
		}
		if ok {
			return match{result: r, confident: true, found: true}, nil
		}
	}
	if d.found {
		s.log.Info("classify: weak TVmaze result not attached", "show", q.name, "candidate", d.result.Name,
			"candidate_year", d.result.Year, "tvmaze_id", d.result.ID)
	}
	return d.match, nil
}

// searchQueries never carry the year: TVmaze search finds nothing for
// "euphoria 2019", so the year only takes part in decide.
func searchQueries(key string) []string {
	if bare := stripCountry(key); bare != key {
		return []string{key, bare}
	}
	return []string{key}
}

func appendNew(seen, results []tvmaze.Result) []tvmaze.Result {
	for _, r := range results {
		if !slices.ContainsFunc(seen, func(o tvmaze.Result) bool { return o.ID == r.ID }) {
			seen = append(seen, r)
		}
	}
	return seen
}

// decide applies the confident-match test: the result's name normalizes to
// the key (or the key without its country code), its premiere year equals
// the parsed year when there is one, and it is the only such result, or
// the only one from the key's country (ruling B7).
func decide(results []tvmaze.Result, key string, year *int) decision {
	if len(results) == 0 {
		return decision{}
	}
	bare := stripCountry(key)
	var named []tvmaze.Result
	for _, r := range byScore(results) {
		if k := library.ShowKey(r.Name); k == key || k == bare {
			named = append(named, r)
		}
	}
	if len(named) == 0 {
		return decision{match: match{result: best(results), found: true}}
	}
	cands := named
	if year != nil {
		cands = filter(named, func(r tvmaze.Result) bool { return r.Year == *year })
		if len(cands) == 0 {
			return decision{match: match{result: named[0], found: true}}
		}
	}
	if code := countryCode(key); code != "" && len(cands) > 1 {
		if local := filter(cands, func(r tvmaze.Result) bool { return r.Country == code }); len(local) > 0 {
			cands = local
		}
	}
	if len(cands) == 1 {
		return decision{match: match{result: cands[0], confident: true, found: true}}
	}
	return decision{match: match{result: cands[0], found: true}, ties: cands}
}

// byEpisode breaks a namesake tie by asking which candidate has the
// arriving episode: exactly one must (ruling B7).
func (s *Service) byEpisode(ctx context.Context, ties []tvmaze.Result, season, episode int) (tvmaze.Result, bool, error) {
	var hits []tvmaze.Result
	for _, r := range ties[:min(len(ties), maxEpisodeProbes)] {
		tvSeason := season
		if season > 0 {
			offset, err := s.localOffset(ctx, r.ID)
			if err != nil {
				return tvmaze.Result{}, false, err
			}
			tvSeason += offset
		}
		eps, err := s.tvmaze.Episodes(ctx, r.ID)
		if errors.Is(err, tvmaze.ErrNotFound) {
			continue
		}
		if err != nil {
			return tvmaze.Result{}, false, tvmazeError{err}
		}
		if slices.ContainsFunc(eps, func(e tvmaze.Episode) bool { return e.Season == tvSeason && e.Number == episode }) {
			hits = append(hits, r)
		}
	}
	if len(hits) != 1 {
		return tvmaze.Result{}, false, nil
	}
	return hits[0], true, nil
}

func filter(results []tvmaze.Result, keep func(tvmaze.Result) bool) []tvmaze.Result {
	var out []tvmaze.Result
	for _, r := range results {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func best(results []tvmaze.Result) tvmaze.Result {
	return byScore(results)[0]
}

func byScore(results []tvmaze.Result) []tvmaze.Result {
	out := slices.Clone(results)
	slices.SortStableFunc(out, func(a, b tvmaze.Result) int { return cmp.Compare(b.Score, a.Score) })
	return out
}

// countries maps a trailing show-key country code to TVmaze's ISO code.
var countries = map[string]string{"us": "US", "uk": "GB", "au": "AU", "nz": "NZ", "ca": "CA"}

func countryCode(key string) string {
	i := strings.LastIndexByte(key, ' ')
	if i < 0 {
		return ""
	}
	return countries[key[i+1:]]
}

func stripCountry(key string) string {
	if countryCode(key) == "" {
		return key
	}
	return key[:strings.LastIndexByte(key, ' ')]
}

// localOffset is the season offset of the local show holding tvmazeID, 0
// for a show not yet in the catalog.
func (s *Service) localOffset(ctx context.Context, tvmazeID int) (int, error) {
	show, err := s.store.GetShowByTVmazeID(ctx, int64(tvmazeID))
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	return show.SeasonOffset, err
}
