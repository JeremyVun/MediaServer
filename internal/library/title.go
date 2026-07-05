package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ParsedTitle struct {
	Title   string
	Year    *int
	Season  *int
	Episode *int
	Type    string
}

var (
	episodeRE = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,3})\b`)
	yearRE    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	tagRE     = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|576p|480p|x264|x265|h\.?264|h\.?265|hevc|avc|web[- ]?dl|web[- ]?rip|bluray|blu[- ]?ray|brrip|hdrip|dvdrip|remux|proper|repack|extended|internal|aac|ac3|eac3|dts|truehd|atmos|10bit|8bit|yify|yts)\b`)
	bracketRE = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	parenRE   = regexp.MustCompile(`\(([^)]*)\)`)
	spacesRE  = regexp.MustCompile(`\s+`)
	// A token that *starts* with '-' is a scene release-group left dangling
	// after the tag before it was stripped ("...x264-NTG" → " -NTG").
	// Hyphens inside real words ("Spider-Man") never match.
	orphanGroupRE = regexp.MustCompile(`(^|\s)-\S+`)
)

// ParseTitle converts a filename into catalog hints. It intentionally stays
// heuristic and deterministic; table tests are the contract for future edits.
func ParseTitle(name string) ParsedTitle {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	if ext != "" {
		base = strings.TrimSuffix(base, ext)
	}

	// Only dots/underscores are separators (SPEC-BACKEND); hyphens are part
	// of real titles ("Spider-Man") and must survive.
	clean := strings.NewReplacer(".", " ", "_", " ").Replace(base)
	clean = bracketRE.ReplaceAllString(clean, " ")

	var year *int
	clean = parenRE.ReplaceAllStringFunc(clean, func(group string) string {
		inner := strings.Trim(group, "()")
		if year == nil && yearRE.MatchString(inner) {
			if y, err := strconv.Atoi(yearRE.FindString(inner)); err == nil {
				year = &y
			}
		}
		return " "
	})

	parsed := ParsedTitle{Type: "video"}
	if m := episodeRE.FindStringSubmatch(clean); len(m) == 3 {
		season, _ := strconv.Atoi(m[1])
		episode, _ := strconv.Atoi(m[2])
		parsed.Season = &season
		parsed.Episode = &episode
		parsed.Type = "episode"
		clean = episodeRE.ReplaceAllString(clean, " ")
	}

	if year == nil {
		years := yearRE.FindAllString(clean, -1)
		if len(years) > 0 {
			last := years[len(years)-1]
			if y, err := strconv.Atoi(last); err == nil {
				year = &y
				clean = strings.Replace(clean, last, " ", 1)
			}
		}
	}
	parsed.Year = year

	clean = tagRE.ReplaceAllString(clean, " ")
	clean = orphanGroupRE.ReplaceAllString(clean, " ")
	clean = strings.Trim(spacesRE.ReplaceAllString(clean, " "), " ._-")
	if clean == "" {
		clean = strings.TrimSpace(base)
	}
	parsed.Title = titleCaseAcronyms(clean)
	return parsed
}

func titleCaseAcronyms(s string) string {
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
