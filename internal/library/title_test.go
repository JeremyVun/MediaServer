package library

import "testing"

func TestParseTitle(t *testing.T) {
	tests := []struct {
		name        string
		wantTitle   string
		wantType    string
		wantYear    int
		wantSeason  int
		wantEpisode int
	}{
		{
			name:      "Big.Buck.Bunny.(2008).1080p.BluRay.x264.mkv",
			wantTitle: "Big Buck Bunny",
			wantType:  "video",
			wantYear:  2008,
		},
		{
			name:        "Example.Show.S02E07.720p.WEB-DL.AAC.mkv",
			wantTitle:   "Example Show",
			wantType:    "episode",
			wantSeason:  2,
			wantEpisode: 7,
		},
		{
			name:      "Sintel_2010_1080p_WEBRip.mp4",
			wantTitle: "Sintel",
			wantType:  "video",
			wantYear:  2010,
		},
		{
			name:      "[Group] The.Movie.Name.2160p.HEVC.mkv",
			wantTitle: "The Movie Name",
			wantType:  "video",
		},
		{
			name:      "les.misérables.(2012).1080p.mkv",
			wantTitle: "Les Misérables",
			wantType:  "video",
			wantYear:  2012,
		},
		{
			// Hyphens are not separators: real hyphenated titles survive.
			name:      "Spider-Man.(2002).1080p.BluRay.x264.mkv",
			wantTitle: "Spider-Man",
			wantType:  "video",
			wantYear:  2002,
		},
		{
			// Scene naming: the release group dangling after a stripped tag
			// ("x264-NTG") is dropped, not folded into the title.
			name:      "Some.Movie.2019.1080p.WEB-DL.x264-NTG.mkv",
			wantTitle: "Some Movie",
			wantType:  "video",
			wantYear:  2019,
		},
	}

	for _, tt := range tests {
		got := ParseTitle(tt.name)
		if got.Title != tt.wantTitle {
			t.Errorf("%s title = %q, want %q", tt.name, got.Title, tt.wantTitle)
		}
		if got.Type != tt.wantType {
			t.Errorf("%s type = %q, want %q", tt.name, got.Type, tt.wantType)
		}
		if tt.wantYear == 0 {
			if got.Year != nil {
				t.Errorf("%s year = %d, want nil", tt.name, *got.Year)
			}
		} else if got.Year == nil || *got.Year != tt.wantYear {
			t.Errorf("%s year = %v, want %d", tt.name, got.Year, tt.wantYear)
		}
		if tt.wantSeason != 0 && (got.Season == nil || *got.Season != tt.wantSeason) {
			t.Errorf("%s season = %v, want %d", tt.name, got.Season, tt.wantSeason)
		}
		if tt.wantEpisode != 0 && (got.Episode == nil || *got.Episode != tt.wantEpisode) {
			t.Errorf("%s episode = %v, want %d", tt.name, got.Episode, tt.wantEpisode)
		}
	}
}
