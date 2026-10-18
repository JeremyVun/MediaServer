package classify

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/JeremyVun/MediaServer/internal/library"
	"github.com/JeremyVun/MediaServer/internal/tvmaze"
)

type match struct {
	result    tvmaze.Result
	confident bool
	found     bool
}

// match searches TVmaze for a show key, retrying a miss without a trailing
// country code and then without the year (ruling M1). Only a confident
// match may be attached; the best weak result is returned for logging.
// Any TVmaze error other than not found means unreachable.
func (s *Service) match(ctx context.Context, name, key string, year *int) (match, error) {
	var weak match
	for _, q := range searchQueries(key, year) {
		results, err := s.tvmaze.Search(ctx, q)
		if errors.Is(err, tvmaze.ErrNotFound) {
			continue
		}
		if err != nil {
			return match{}, tvmazeError{err}
		}
		m := pick(results, key, year)
		if m.confident {
			return m, nil
		}
		if !weak.found && m.found {
			weak = m
		}
	}
	if weak.found {
		s.log.Info("classify: weak TVmaze result not attached", "show", name, "candidate", weak.result.Name,
			"candidate_year", weak.result.Year, "tvmaze_id", weak.result.ID)
	}
	return weak, nil
}

func searchQueries(key string, year *int) []string {
	bare := stripCountry(key)
	var qs []string
	add := func(q string) {
		for _, seen := range qs {
			if seen == q {
				return
			}
		}
		qs = append(qs, q)
	}
	if year != nil {
		y := " " + strconv.Itoa(*year)
		add(key + y)
		add(bare + y)
	} else {
		add(key)
	}
	add(bare)
	return qs
}

// pick applies the confident-match test: the result's name normalizes to
// the key (or the key without its country code), and its premiere year
// equals the parsed year when there is one. Among several name matches the
// year decides; with no year to decide, the best score wins unconfidently.
func pick(results []tvmaze.Result, key string, year *int) match {
	if len(results) == 0 {
		return match{}
	}
	bare := stripCountry(key)
	var named []tvmaze.Result
	for _, r := range results {
		if k := library.ShowKey(r.Name); k == key || k == bare {
			named = append(named, r)
		}
	}
	switch {
	case len(named) == 0:
		return match{result: best(results), found: true}
	case year != nil:
		for _, r := range byScore(named) {
			if r.Year == *year {
				return match{result: r, confident: true, found: true}
			}
		}
		return match{result: best(named), found: true}
	case len(named) == 1:
		return match{result: named[0], confident: true, found: true}
	default:
		return match{result: best(named), found: true}
	}
}

func best(results []tvmaze.Result) tvmaze.Result {
	return byScore(results)[0]
}

func byScore(results []tvmaze.Result) []tvmaze.Result {
	out := slices.Clone(results)
	slices.SortStableFunc(out, func(a, b tvmaze.Result) int { return cmp.Compare(b.Score, a.Score) })
	return out
}

func stripCountry(key string) string {
	i := strings.LastIndexByte(key, ' ')
	if i < 0 {
		return key
	}
	switch key[i+1:] {
	case "us", "uk", "au", "nz", "ca":
		return key[:i]
	}
	return key
}
