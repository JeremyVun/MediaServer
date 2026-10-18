package tvmaze

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

type Result struct {
	ID    int
	Name  string
	Year  int // premiere year, 0 when unknown
	Score float64
	// Network is the broadcast network, else the web channel (Disney+), else "".
	Network string
	// Country is the ISO code of that network or web channel (US, GB), else "".
	Country         string
	PosterURL       string
	PosterMediumURL string
}

type Show struct {
	ID              int
	Name            string
	Year            int
	Summary         string // plain text, paragraphs separated by a blank line
	Network         string // as on Result
	PosterURL       string
	PosterMediumURL string
	Episodes        []Episode
}

// Episode is one TVmaze episode. TVmaze files unnumbered specials under the
// season they aired in; they are moved to season 0 and numbered 1..n in
// airdate order, so (Season, Number) is unique.
type Episode struct {
	Season     int
	Number     int
	Title      string
	Airdate    string // YYYY-MM-DD, "" when unknown
	RuntimeMin int    // 0 when unknown
}

type apiShow struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	Premiered  string   `json:"premiered"`
	Network    *apiName `json:"network"`
	WebChannel *apiName `json:"webChannel"`
	Image      *struct {
		Medium   string `json:"medium"`
		Original string `json:"original"`
	} `json:"image"`
	Summary string `json:"summary"`
}

type apiName struct {
	Name    string `json:"name"`
	Country *struct {
		Code string `json:"code"`
	} `json:"country"`
}

type apiEpisode struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Season  int    `json:"season"`
	Number  *int   `json:"number"`
	Airdate string `json:"airdate"`
	Runtime *int   `json:"runtime"`
}

type apiImage struct {
	Type        string `json:"type"`
	Main        bool   `json:"main"`
	Resolutions struct {
		Original struct {
			URL   string `json:"url"`
			Width int    `json:"width"`
		} `json:"original"`
	} `json:"resolutions"`
}

func (c *Client) Search(ctx context.Context, q string) ([]Result, error) {
	var raw []struct {
		Score float64 `json:"score"`
		Show  apiShow `json:"show"`
	}
	if err := c.getJSON(ctx, "/search/shows?q="+url.QueryEscape(q), &raw); err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(raw))
	for _, r := range raw {
		s := r.Show
		res := Result{ID: s.ID, Name: s.Name, Year: year(s.Premiered), Score: r.Score}
		res.Network, res.Country = s.network()
		res.PosterURL, res.PosterMediumURL = s.posters()
		results = append(results, res)
	}
	return results, nil
}

// Show fetches the show and its full episode list, specials included.
func (c *Client) Show(ctx context.Context, id int) (Show, error) {
	var s apiShow
	if err := c.getJSON(ctx, fmt.Sprintf("/shows/%d", id), &s); err != nil {
		return Show{}, err
	}
	eps, err := c.Episodes(ctx, id)
	if err != nil {
		return Show{}, err
	}
	show := Show{ID: s.ID, Name: s.Name, Year: year(s.Premiered), Summary: StripHTML(s.Summary), Episodes: eps}
	show.Network, _ = s.network()
	show.PosterURL, show.PosterMediumURL = s.posters()
	return show, nil
}

// Episodes fetches the show's full episode list, specials included.
func (c *Client) Episodes(ctx context.Context, id int) ([]Episode, error) {
	var eps []apiEpisode
	if err := c.getJSON(ctx, fmt.Sprintf("/shows/%d/episodes?specials=1", id), &eps); err != nil {
		return nil, err
	}
	return episodes(eps), nil
}

// BackgroundMaxWidth is the widest background preferred; wider ones are
// only offered when nothing narrower exists.
const BackgroundMaxWidth = 3840

type Background struct {
	URL   string
	Width int // 0 when unknown
}

// Backgrounds lists the show's background ("splash") images best first:
// the main one, then the widest up to BackgroundMaxWidth, then wider ones
// narrowest first.
func (c *Client) Backgrounds(ctx context.Context, id int) ([]Background, error) {
	var imgs []apiImage
	if err := c.getJSON(ctx, fmt.Sprintf("/shows/%d/images", id), &imgs); err != nil {
		return nil, err
	}
	type candidate struct {
		Background
		main bool
	}
	var cands []candidate
	for _, img := range imgs {
		if img.Type != "background" || img.Resolutions.Original.URL == "" {
			continue
		}
		o := img.Resolutions.Original
		cands = append(cands, candidate{Background{o.URL, o.Width}, img.Main})
	}
	slices.SortStableFunc(cands, func(a, b candidate) int {
		if a.main != b.main {
			if a.main {
				return -1
			}
			return 1
		}
		aOver, bOver := a.Width > BackgroundMaxWidth, b.Width > BackgroundMaxWidth
		switch {
		case aOver != bOver:
			if aOver {
				return 1
			}
			return -1
		case aOver:
			return cmp.Compare(a.Width, b.Width)
		default:
			return cmp.Compare(b.Width, a.Width)
		}
	})
	out := make([]Background, len(cands))
	for i, c := range cands {
		out[i] = c.Background
	}
	return out, nil
}

func (s apiShow) network() (name, country string) {
	ch := s.Network
	if ch == nil {
		ch = s.WebChannel
	}
	if ch == nil {
		return "", ""
	}
	if ch.Country != nil {
		country = ch.Country.Code
	}
	return ch.Name, country
}

func (s apiShow) posters() (original, medium string) {
	if s.Image == nil {
		return "", ""
	}
	return cmp.Or(s.Image.Original, s.Image.Medium), s.Image.Medium
}

func episodes(raw []apiEpisode) []Episode {
	out := make([]Episode, 0, len(raw))
	var specials []apiEpisode
	lastSpecial := 0
	for _, e := range raw {
		if e.Number == nil {
			specials = append(specials, e)
			continue
		}
		if e.Season == 0 {
			lastSpecial = max(lastSpecial, *e.Number)
		}
		out = append(out, toEpisode(e, e.Season, *e.Number))
	}
	slices.SortStableFunc(specials, func(a, b apiEpisode) int {
		if a.Airdate != b.Airdate {
			switch {
			case a.Airdate == "":
				return 1
			case b.Airdate == "":
				return -1
			}
			return strings.Compare(a.Airdate, b.Airdate)
		}
		return cmp.Compare(a.ID, b.ID)
	})
	for i, e := range specials {
		out = append(out, toEpisode(e, 0, lastSpecial+i+1))
	}
	return out
}

func toEpisode(e apiEpisode, season, number int) Episode {
	ep := Episode{Season: season, Number: number, Title: e.Name, Airdate: e.Airdate}
	if e.Runtime != nil {
		ep.RuntimeMin = *e.Runtime
	}
	return ep
}

func year(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}

var (
	lineBreak  = regexp.MustCompile(`(?i)<br\s*/?>`)
	paraEnd    = regexp.MustCompile(`(?i)</p\s*>`)
	anyTag     = regexp.MustCompile(`<[^>]*>`)
	spaceRun   = regexp.MustCompile(`[ \t\r\f]+`)
	blankLines = regexp.MustCompile(`\n\s*\n\s*`)
)

// StripHTML turns a TVmaze summary into plain text.
func StripHTML(s string) string {
	s = lineBreak.ReplaceAllString(s, "\n")
	s = paraEnd.ReplaceAllString(s, "\n\n")
	s = anyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = spaceRun.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = blankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(s)
}
