package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type parsedTitleV1 struct {
	Title   string
	Year    *int
	Season  *int
	Episode *int
	Type    string
}

var (
	episodeV1RE = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,3})\b`)
	yearV1RE    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	tagV1RE     = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|576p|480p|x264|x265|h\.?264|h\.?265|hevc|avc|web[- ]?dl|web[- ]?rip|bluray|blu[- ]?ray|brrip|hdrip|dvdrip|remux|proper|repack|extended|internal|aac|ac3|eac3|dts|truehd|atmos|10bit|8bit|yify|yts)\b`)
	bracketV1RE = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	parenV1RE   = regexp.MustCompile(`\(([^)]*)\)`)
	spacesV1RE  = regexp.MustCompile(`\s+`)
	// A token that *starts* with '-' is a scene release-group left dangling
	// after the tag before it was stripped ("...x264-NTG" → " -NTG").
	// Hyphens inside real words ("Spider-Man") never match.
	orphanGroupV1RE = regexp.MustCompile(`(^|\s)-\S+`)
)

// parseTitleV1 is the frozen pre-shows parser. The backfill lock heuristic
// compares stored titles against it to detect owner edits; do not change it.
func parseTitleV1(name string) parsedTitleV1 {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	if ext != "" {
		base = strings.TrimSuffix(base, ext)
	}

	// Only dots/underscores are separators (SPEC-BACKEND); hyphens are part
	// of real titles ("Spider-Man") and must survive.
	clean := strings.NewReplacer(".", " ", "_", " ").Replace(base)
	clean = bracketV1RE.ReplaceAllString(clean, " ")

	var year *int
	clean = parenV1RE.ReplaceAllStringFunc(clean, func(group string) string {
		inner := strings.Trim(group, "()")
		if year == nil && yearV1RE.MatchString(inner) {
			if y, err := strconv.Atoi(yearV1RE.FindString(inner)); err == nil {
				year = &y
			}
		}
		return " "
	})

	parsed := parsedTitleV1{Type: "video"}
	if m := episodeV1RE.FindStringSubmatch(clean); len(m) == 3 {
		season, _ := strconv.Atoi(m[1])
		episode, _ := strconv.Atoi(m[2])
		parsed.Season = &season
		parsed.Episode = &episode
		parsed.Type = "episode"
		clean = episodeV1RE.ReplaceAllString(clean, " ")
	}

	if year == nil {
		years := yearV1RE.FindAllString(clean, -1)
		if len(years) > 0 {
			last := years[len(years)-1]
			if y, err := strconv.Atoi(last); err == nil {
				year = &y
				clean = strings.Replace(clean, last, " ", 1)
			}
		}
	}
	parsed.Year = year

	clean = tagV1RE.ReplaceAllString(clean, " ")
	clean = orphanGroupV1RE.ReplaceAllString(clean, " ")
	clean = strings.Trim(spacesV1RE.ReplaceAllString(clean, " "), " ._-")
	if clean == "" {
		clean = strings.TrimSpace(base)
	}
	parsed.Title = titleCaseAcronymsV1(clean)
	return parsed
}

func titleCaseAcronymsV1(s string) string {
	words := strings.Fields(s)
	for i, word := range words {
		lower := strings.ToLower(word)
		switch lower {
		case "a", "an", "and", "as", "at", "but", "by", "for", "from", "in", "nor", "of", "on", "or", "the", "to", "with":
			if i != 0 {
				words[i] = lower
				continue
			}
		}
		if word == strings.ToUpper(word) && utf8.RuneCountInString(word) <= 4 {
			words[i] = word
			continue
		}
		// Hyphenated words capitalize each segment: spider-man → Spider-Man.
		segments := strings.Split(lower, "-")
		for j, segment := range segments {
			if segment == "" {
				continue
			}
			first, size := utf8.DecodeRuneInString(segment)
			segments[j] = string(unicode.ToUpper(first)) + segment[size:]
		}
		words[i] = strings.Join(segments, "-")
	}
	return strings.Join(words, " ")
}

// LegacyTitle is what pre-shows ingest wrote for relPath: the item type
// (video|episode), title and year. The backfill compares stored items with
// it to tell owner edits from parser output.
func LegacyTitle(relPath string) (itemType, title string, year *int) {
	p := parseTitleV1(relPath)
	return p.Type, p.Title, p.Year
}
