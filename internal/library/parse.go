package library

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Kind string

const (
	KindEpisode Kind = "episode"
	KindUnknown Kind = "unknown"
)

// Parsed is the catalog guess for one file. Season, Episode and EpisodeEnd
// are meaningful only when Kind is KindEpisode; Season 0 is specials and
// EpisodeEnd is 0 unless the file spans several episodes.
type Parsed struct {
	Kind         Kind
	ShowName     string
	ShowKey      string
	Season       int
	Episode      int
	EpisodeEnd   int
	EpisodeTitle string
	Title        string
	Year         *int
	Sure         bool
}

var (
	bracketRE  = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
	parenRE    = regexp.MustCompile(`\(([^)]*)\)`)
	h26xRE     = regexp.MustCompile(`(?i)\bh\.(26[45])\b`)
	webDotRE   = regexp.MustCompile(`(?i)\bweb\.(dl|rip)\b`)
	channelsRE = regexp.MustCompile(`(?i)\b(ddp|dd|aac|ac3|eac3|dts|truehd|atmos|flac|opus)?[ .]?([2-8])\.[01]\b`)
	sxxeyyRE   = regexp.MustCompile(`(?i)\bS(\d{1,2})[ ._]?E(\d{1,3})(?:-?E(\d{1,3}))?\b`)
	nxnnRE     = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`)
	bareEpRE   = regexp.MustCompile(`(?i)\b(?:E|Ep|Episode)[ ._]?(\d{1,3})\b`)
	seasonRE   = regexp.MustCompile(`(?i)\b(?:S(\d{1,2})|Season[ ._]?(\d{1,2}))\b`)
	fansubRE   = regexp.MustCompile(`^(?:\[([^\]]+)\]\s*)?(.+?)\s+-\s+(\d{1,3})(?:v\d+)?\b(.*)$`)
	codenameRE = regexp.MustCompile(`(?i)^eps\d+\.\d+`)
	strongJunk = regexp.MustCompile(`^(?:(?:bd|bdrip)?\d{3,4}[pi]|4k|uhd|x26[45]|h26[45]|hevc|avc|xvid|divx|av1|web-?dl|web-?rip|bluray|blu-ray|brrip|bdrip|hdrip|dvdrip|dvdscr|hdtv|hdcam|hdts|screener|dcprip|remux|aac|e?ac3|dts|truehd|atmos|ddp?|flac|opus|\d{1,2}ch|\d{1,2}-?bit|hdr\d*|ds4k)$`)
	ordinalRE  = regexp.MustCompile(`(?i)^\d+(st|nd|rd|th)$`)
	romanRE    = regexp.MustCompile(`(?i)^[ivx]+$`)
)

// Matched case-sensitively and only at the end of a name, so a title word
// like "Web" in "A Dark Web" survives.
var weakJunk = map[string]bool{
	"AMZN": true, "HMAX": true, "ATVP": true, "NF": true, "MA": true, "DSNP": true, "HULU": true,
	"WEB": true, "web": true, "HC": true, "ESub": true, "ESubs": true, "iTA": true, "ITA": true,
	"EnG": true, "ENG": true, "Eng": true, "Sub": true, "Subs": true, "MULTI": true, "KORSUB": true,
	"UNCUT": true, "REMASTERED": true, "EXTENDED": true, "UNRATED": true, "PROPER": true,
	"REPACK": true, "INTERNAL": true, "LIMITED": true, "BONE": true,
}

var genericNames = map[string]bool{
	"episode": true, "episodes": true, "season": true, "series": true, "show": true, "part": true,
	"video": true, "movie": true, "film": true, "anime": true, "tv": true, "complete": true,
	"extras": true, "featurette": true, "featurettes": true, "trailer": true, "sample": true,
	"bonus": true, "clip": true, "untitled": true, "unknown": true,
}

var smallWords = map[string]bool{
	"a": true, "an": true, "and": true, "as": true, "at": true, "but": true, "by": true, "for": true,
	"from": true, "in": true, "nor": true, "of": true, "on": true, "or": true, "the": true, "to": true,
	"with": true,
}

// ParsePath guesses what a file is from its root-relative path: the basename
// first, then its parent folders for season-pack layouts. Pure and
// deterministic; the fixture table in parse_test.go is its contract.
func ParsePath(relPath string) Parsed {
	parts := strings.Split(strings.Trim(relPath, "/"), "/")
	base := parts[len(parts)-1]
	base = strings.TrimSuffix(base, path.Ext(base))
	folders := parts[:len(parts)-1]

	var parent, grandparent string
	if len(folders) > 0 {
		parent = folders[len(folders)-1]
	}
	if len(folders) > 1 {
		grandparent = folders[len(folders)-2]
	}
	season, hasSeasonFolder := parseSeasonFolder(parent, grandparent)

	pre := preclean(base)
	if loc := sxxeyyRE.FindStringSubmatchIndex(pre); loc != nil {
		p := episodeAt(pre, loc, 2, 4, 6)
		return fileEpisode(p, pre[:loc[0]], pre[loc[1]:], season, hasSeasonFolder, parent)
	}
	if loc := nxnnRE.FindStringSubmatchIndex(pre); loc != nil {
		p := episodeAt(pre, loc, 2, 4, -1)
		return fileEpisode(p, pre[:loc[0]], pre[loc[1]:], season, hasSeasonFolder, parent)
	}
	if p, ok := fansub(base); ok {
		return finish(p)
	}
	if loc := bareEpRE.FindStringSubmatchIndex(pre); loc != nil && hasSeasonFolder && len(season.show) > 0 {
		episode, _ := strconv.Atoi(pre[loc[2]:loc[3]])
		p := Parsed{Kind: KindEpisode, Season: season.number, Episode: episode}
		return fileEpisode(p, pre[:loc[0]], pre[loc[1]:], season, true, parent)
	}
	return finish(parseUnknown(pre))
}

// ShowKey normalizes a show name for matching: lowercase, & as "and",
// apostrophes dropped, other punctuation runs as one space, and a trailing
// year token removed unless it is the whole name.
func ShowKey(name string) string {
	name = strings.ToLower(strings.ReplaceAll(name, "&", " and "))
	var b strings.Builder
	space := false
	for _, r := range name {
		switch {
		case r == '\'' || r == '’':
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	words := strings.Fields(b.String())
	if n := len(words); n > 1 && isYear(words[n-1]) {
		words = words[:n-1]
	}
	return strings.Join(words, " ")
}

type seasonFolder struct {
	show   []string
	number int
	year   *int
}

func parseSeasonFolder(name, above string) (seasonFolder, bool) {
	if name == "" {
		return seasonFolder{}, false
	}
	pre := preclean(name)
	if sxxeyyRE.MatchString(pre) {
		return seasonFolder{}, false
	}
	sep := separate(pre)
	loc := seasonRE.FindStringSubmatchIndex(sep)
	if loc == nil {
		return seasonFolder{}, false
	}
	group := 2
	if loc[2] < 0 {
		group = 4
	}
	digits := sep[loc[group]:loc[group+1]]
	number, _ := strconv.Atoi(digits)
	sf := seasonFolder{number: number}
	sf.show, sf.year = splitTrailingYear(trimEdges(cutStrong(strings.Fields(sep[:loc[0]]))))
	if sf.year == nil {
		for _, w := range trimEdges(cutStrong(strings.Fields(sep[loc[1]:]))) {
			if isYear(w) {
				sf.year = yearPtr(w)
				break
			}
		}
	}
	if len(sf.show) == 0 && above != "" {
		sf.show, sf.year = movieWords(preclean(above))
	}
	return sf, true
}

func episodeAt(s string, loc []int, seasonGroup, episodeGroup, endGroup int) Parsed {
	season, _ := strconv.Atoi(s[loc[seasonGroup]:loc[seasonGroup+1]])
	episode, _ := strconv.Atoi(s[loc[episodeGroup]:loc[episodeGroup+1]])
	p := Parsed{Kind: KindEpisode, Season: season, Episode: episode}
	if endGroup > 0 && loc[endGroup] >= 0 {
		end, _ := strconv.Atoi(s[loc[endGroup]:loc[endGroup+1]])
		if end > episode {
			p.EpisodeEnd = end
		}
	}
	return p
}

func fileEpisode(p Parsed, before, after string, season seasonFolder, hasSeasonFolder bool, parent string) Parsed {
	show, year := splitTrailingYear(trimEdges(cutStrong(strings.Fields(separate(before)))))
	p.Year = year

	afterRaw := strings.TrimLeft(after, " ._-|")
	var title []string
	codename := ""
	if codenameRE.MatchString(afterRaw) {
		codename = rawUntilJunk(afterRaw)
	} else {
		title = episodeTitleWords(after)
	}

	switch {
	case len(show) > 0:
		p.Sure = nameIsSure(show)
	case hasSeasonFolder && len(season.show) > 0:
		show = season.show
		p.Sure = nameIsSure(show)
	case len(title) > 0:
		show, title, p.Year = splitMarkerFirst(title)
		p.Sure = false
	case parent != "":
		show, p.Year = movieWords(preclean(parent))
		p.Sure = false
	}

	p.ShowName = tidy(show)
	if hasSeasonFolder && p.Year == nil && ShowKey(p.ShowName) == ShowKey(tidy(season.show)) {
		p.Year = season.year
	}
	if codename != "" {
		p.EpisodeTitle = codename
	} else {
		p.EpisodeTitle = tidy(title)
		p.Sure = p.Sure && !looksJunky(title)
	}
	return finish(p)
}

// splitMarkerFirst handles names that lead with the marker (M8): the show
// runs up to an "Episode N" token or a year, and the rest is the title.
func splitMarkerFirst(words []string) (show, title []string, year *int) {
	show = words
	for i := 0; i < len(words); i++ {
		if isEpisodeWord(words[i]) && i+1 < len(words) && isDigits(words[i+1]) {
			show, title = words[:i], words[i+2:]
			break
		}
		if i > 0 && isYear(words[i]) {
			show, title, year = words[:i], words[i+1:], yearPtr(words[i])
			break
		}
	}
	title, trailing := splitTrailingYear(title)
	if year == nil {
		year = trailing
	}
	return show, title, year
}

func fansub(base string) (Parsed, bool) {
	m := fansubRE.FindStringSubmatch(strings.ReplaceAll(base, "_", " "))
	if m == nil {
		return Parsed{}, false
	}
	show, year := movieWords(preclean(m[2]))
	if len(show) == 0 {
		return Parsed{}, false
	}
	episode, _ := strconv.Atoi(m[3])
	title := trimEdges(cutStrong(strings.Fields(separate(preclean(m[4])))))
	p := Parsed{
		Kind:         KindEpisode,
		ShowName:     tidy(show),
		Season:       1,
		Episode:      episode,
		EpisodeTitle: tidy(title),
		Year:         year,
	}
	p.Sure = m[1] != "" && nameIsSure(show) && !looksJunky(title)
	return p, true
}

func parseUnknown(pre string) Parsed {
	words, year := movieWords(pre)
	p := Parsed{Kind: KindUnknown, Year: year}
	if len(words) == 0 {
		p.Title = strings.Join(strings.Fields(separate(pre)), " ")
		return p
	}
	p.Title = tidy(words)
	p.Sure = nameIsSure(words)
	return p
}

func finish(p Parsed) Parsed {
	p.ShowKey = ShowKey(p.ShowName)
	if p.Kind != KindEpisode {
		return p
	}
	if p.ShowName == "" {
		p.Sure = false
	}
	switch {
	case p.EpisodeTitle != "":
		p.Title = p.EpisodeTitle
	case p.EpisodeEnd > 0:
		p.Title = fmt.Sprintf("Episodes %d–%d", p.Episode, p.EpisodeEnd)
	default:
		p.Title = fmt.Sprintf("Episode %d", p.Episode)
	}
	return p
}

// preclean drops bracketed tags and rewrites tokens whose dots are part of
// the tag (H.264, WEB.DL, 5.1) so later dot splitting leaves no residue.
// Replacements are fenced with '|', a separator that, unlike a space, does
// not make a preceding dot look like punctuation.
func preclean(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = bracketRE.ReplaceAllString(s, "|")
	yearSeen := false
	s = parenRE.ReplaceAllStringFunc(s, func(group string) string {
		inner := strings.TrimSpace(group[1 : len(group)-1])
		switch {
		case isYear(inner) && !yearSeen:
			yearSeen = true
			return "|" + inner + "|"
		case isCountryCode(inner):
			return "|" + inner + "|"
		}
		return "|"
	})
	s = h26xRE.ReplaceAllString(s, "h$1")
	s = webDotRE.ReplaceAllString(s, "web-$1")
	return channelsRE.ReplaceAllString(s, "|$1|${2}ch|")
}

// separate turns dots, underscores and '|' into spaces, except a dot followed
// by a space, which is real punctuation ("Pt. II"). Byte offsets are kept.
func separate(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '_' || c == '|' || (c == '.' && (i+1 >= len(b) || b[i+1] != ' ')) {
			b[i] = ' '
		}
	}
	return string(b)
}

func episodeTitleWords(after string) []string {
	words := trimEdges(cutStrong(strings.Fields(separate(after))))
	if len(words) >= 2 && isEpisodeWord(words[0]) && isDigits(words[1]) {
		words = words[2:]
	}
	return words
}

// rawUntilJunk keeps a Mr. Robot-style codename verbatim, dots included.
func rawUntilJunk(raw string) string {
	sep := separate(raw)
	end := len(raw)
	for i := 0; i < len(sep); {
		for i < len(sep) && sep[i] == ' ' {
			i++
		}
		start := i
		for i < len(sep) && sep[i] != ' ' {
			i++
		}
		if start < i && isJunk(sep[start:i]) {
			end = start
			break
		}
	}
	return strings.TrimRight(raw[:end], " ._-|")
}

func movieWords(pre string) ([]string, *int) {
	words := cutStrong(strings.Fields(separate(pre)))
	for i := len(words) - 1; i >= 1; i-- {
		if isYear(words[i]) {
			title, _ := splitTrailingYear(trimEdges(words[:i]))
			return title, yearPtr(words[i])
		}
	}
	return trimEdges(words), nil
}

func cutStrong(words []string) []string {
	for i, w := range words {
		if isJunk(w) {
			return words[:i]
		}
	}
	return words
}

func trimEdges(words []string) []string {
	for len(words) > 0 && (weakJunk[words[len(words)-1]] || words[len(words)-1] == "-") {
		words = words[:len(words)-1]
	}
	for len(words) > 0 && words[0] == "-" {
		words = words[1:]
	}
	return words
}

func splitTrailingYear(words []string) ([]string, *int) {
	var year *int
	for len(words) > 1 && isYear(words[len(words)-1]) {
		if year == nil {
			year = yearPtr(words[len(words)-1])
		}
		words = words[:len(words)-1]
	}
	return words, year
}

func isJunk(word string) bool {
	w := strings.ToLower(strings.Trim(word, "-.,"))
	if strongJunk.MatchString(w) {
		return true
	}
	// A scene group rides on the last tag: "x265-NeoNoir", "6CH-SupaCvnt".
	if i := strings.LastIndexByte(w, '-'); i > 0 && strongJunk.MatchString(w[:i]) {
		return true
	}
	return false
}

// isYear bounds years so a title number like "Blade Runner 2049" stays in
// the title.
func isYear(w string) bool {
	if len(w) != 4 || !isDigits(w) {
		return false
	}
	n, _ := strconv.Atoi(w)
	return n >= 1900 && n <= 2039
}

func yearPtr(w string) *int {
	n, _ := strconv.Atoi(w)
	return &n
}

func isDigits(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isEpisodeWord(w string) bool {
	switch strings.ToLower(w) {
	case "episode", "episodio", "ep":
		return true
	}
	return false
}

func isCountryCode(w string) bool {
	switch strings.ToUpper(w) {
	case "US", "UK", "AU", "NZ", "CA":
		return true
	}
	return false
}

func nameIsSure(words []string) bool {
	if len(words) == 0 || looksJunky(words) {
		return false
	}
	return len(words) > 1 || !genericNames[strings.ToLower(strings.Trim(words[0], ".,-"))]
}

// looksJunky flags leftovers a release name would leave behind: letters
// fused with digits, or a run of all-caps words inside mixed-case text.
func looksJunky(words []string) bool {
	shouting := isShouting(words)
	capsRun := 0
	for _, w := range words {
		hasLetter, hasDigit := false, false
		for _, r := range w {
			hasLetter = hasLetter || unicode.IsLetter(r)
			hasDigit = hasDigit || unicode.IsDigit(r)
		}
		if hasLetter && hasDigit && !ordinalRE.MatchString(w) {
			return true
		}
		if !shouting && utf8.RuneCountInString(w) >= 2 && hasLetter && strings.ToUpper(w) == w && !romanRE.MatchString(w) {
			capsRun++
			if capsRun >= 2 {
				return true
			}
		} else {
			capsRun = 0
		}
	}
	return false
}

func isShouting(words []string) bool {
	upper := false
	for _, w := range words {
		if romanRE.MatchString(w) {
			continue
		}
		for _, r := range w {
			if unicode.IsLower(r) {
				return false
			}
			upper = upper || unicode.IsUpper(r)
		}
	}
	return upper
}

func isAllLower(words []string) bool {
	for _, w := range words {
		for _, r := range w {
			if unicode.IsUpper(r) {
				return false
			}
		}
	}
	return true
}

// tidy joins words and fixes casing only when the source had none to keep:
// all-caps or all-lowercase text is title-cased, mixed case is left alone.
func tidy(words []string) string {
	if len(words) == 0 {
		return ""
	}
	joined := strings.Join(words, " ")
	if len(words) == 1 && utf8.RuneCountInString(words[0]) <= 4 && isShouting(words) {
		return joined
	}
	if !isShouting(words) && !isAllLower(words) {
		return joined
	}
	out := make([]string, len(words))
	for i, w := range words {
		lower := strings.ToLower(w)
		switch {
		case romanRE.MatchString(w):
			out[i] = strings.ToUpper(w)
		case i > 0 && smallWords[lower]:
			out[i] = lower
		default:
			out[i] = capitalize(lower)
		}
	}
	return strings.Join(out, " ")
}

// capitalize upper-cases the first letter and each letter that follows a
// hyphen joined to a letter: "spider-man" → "Spider-Man", "-r" stays "-r".
func capitalize(w string) string {
	runes := []rune(w)
	first := true
	for i, r := range runes {
		if !unicode.IsLetter(r) {
			continue
		}
		joined := i >= 2 && runes[i-1] == '-' && unicode.IsLetter(runes[i-2])
		if joined || (first && (i == 0 || runes[i-1] != '-')) {
			runes[i] = unicode.ToUpper(r)
		}
		first = false
	}
	return string(runes)
}
