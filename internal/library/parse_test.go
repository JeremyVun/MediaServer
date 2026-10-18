package library

import "testing"

const (
	ep = KindEpisode
	un = KindUnknown
)

// The table is the parser's spec. Paths under incoming/ are the owner's real
// library; the rest pin patterns the design names and the v1 cases. Year 0
// means no year.
var parseCases = []struct {
	path         string
	kind         Kind
	showName     string
	showKey      string
	season       int
	episode      int
	episodeEnd   int
	episodeTitle string
	title        string
	year         int
	sure         bool
}{
	// Plain SxxEyy release names.
	{"incoming/Andor.S01E01.1080p.WEB.h264-KOGi.mkv", ep, "Andor", "andor", 1, 1, 0, "", "Episode 1", 0, true},
	{"incoming/andor.s01e07.1080p.web.h264-poke.mkv", ep, "Andor", "andor", 1, 7, 0, "", "Episode 7", 0, true},
	{"incoming/A.Tale.of.Two.Cities.2026.S01E01.Recalled.to.Life.720p.H.264.mp4", ep, "A Tale of Two Cities", "a tale of two cities", 1, 1, 0, "Recalled to Life", "Recalled to Life", 2026, true},
	{"incoming/Brothers.2026.S01E02.The.Hunt.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", ep, "Brothers", "brothers", 1, 2, 0, "The Hunt", "The Hunt", 2026, true},
	{"incoming/Furious.S01E05.1080p.x265-ELiTE.mkv", ep, "Furious", "furious", 1, 5, 0, "", "Episode 5", 0, true},
	{"incoming/House.of.the.Dragon.S03E07.1080p.x265-ELiTE.mkv", ep, "House of the Dragon", "house of the dragon", 3, 7, 0, "", "Episode 7", 0, true},
	{"incoming/Lioness.2023.S03E03.1080p.x265-ELiTE.mkv", ep, "Lioness", "lioness", 3, 3, 0, "", "Episode 3", 2023, true},
	{"incoming/Lucky.2026.S01E02.Make.em.Dance.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", ep, "Lucky", "lucky", 1, 2, 0, "Make em Dance", "Make em Dance", 2026, true},
	{"incoming/Mobland 2025 S01E01 1080p BluRay HEVC x265 5.1 BONE.mkv", ep, "Mobland", "mobland", 1, 1, 0, "", "Episode 1", 2025, true},
	{"incoming/Monster.2022.S02E01.720p.NF.WEBRip.x264-GalaxyTV.mkv", ep, "Monster", "monster", 2, 1, 0, "", "Episode 1", 2022, true},
	{"incoming/Monster.The.Jeffrey.Dahmer.Story.S01E01.720p.NF.WEBRip.x264-GalaxyTV.mkv", ep, "Monster The Jeffrey Dahmer Story", "monster the jeffrey dahmer story", 1, 1, 0, "", "Episode 1", 0, true},
	{"incoming/Norsemen.S01E01.720p.WEBRip.x264-GalaxyTV.mkv", ep, "Norsemen", "norsemen", 1, 1, 0, "", "Episode 1", 0, true},
	{"incoming/Star.Trek.Strange.New.Worlds.S04E10.Tomorrows.Enterprise.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", ep, "Star Trek Strange New Worlds", "star trek strange new worlds", 4, 10, 0, "Tomorrows Enterprise", "Tomorrows Enterprise", 0, true},
	{"incoming/VisionQuest.S01E01.No.Strings.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", ep, "VisionQuest", "visionquest", 1, 1, 0, "No Strings", "No Strings", 0, true},
	{"Example.Show.S02E07.720p.WEB-DL.AAC.mkv", ep, "Example Show", "example show", 2, 7, 0, "", "Episode 7", 0, true},
	{"Show.Name.S01E03.Title.NF.WEBRip.mkv", ep, "Show Name", "show name", 1, 3, 0, "Title", "Title", 0, true},

	// Title words that look like tags stay; tags that look like words go.
	{"incoming/Silo.S03E03.A.Dark.Web.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", ep, "Silo", "silo", 3, 3, 0, "A Dark Web", "A Dark Web", 0, true},
	{"incoming/Silo.S03E07.1080p.HEVC.x265-MeGusta[EZTVx.to].mkv", ep, "Silo", "silo", 3, 7, 0, "", "Episode 7", 0, true},
	{"incoming/Star.Wars.Andor.S01E08.Narkina.5.1080p.DSNP.WEB-DL.DDP5.1.Atmos.H.264-Bandor.mkv", ep, "Star Wars Andor", "star wars andor", 1, 8, 0, "Narkina 5", "Narkina 5", 0, true},
	{"incoming/The.WONDERfools.S01E06.All.Roads.Lead.to.Chae-ni.1080p.ITA-KOR-ENG.MULTI.WEBRip.x265.AAC-V3SP4EV3R.mkv", ep, "The WONDERfools", "the wonderfools", 1, 6, 0, "All Roads Lead to Chae-ni", "All Roads Lead to Chae-ni", 0, true},
	{"incoming/the.wonderfools.s01e01.mkv", ep, "The Wonderfools", "the wonderfools", 1, 1, 0, "", "Episode 1", 0, true},

	// Show years: with and without, the key matches so the files join.
	{"incoming/Dark.Matter.2024.S02E01.A.Quiet.Life.1080p.ATVP.WEB-DL.DDP5.1.ENG.ITA.Atmos.H265-TBK.mkv", ep, "Dark Matter", "dark matter", 2, 1, 0, "A Quiet Life", "A Quiet Life", 2024, true},
	{"incoming/Dark.Matter.2024.S02E02.1080p.x265-ELiTE.mkv", ep, "Dark Matter", "dark matter", 2, 2, 0, "", "Episode 2", 2024, true},
	{"incoming/Lanterns - S01E01 - Pilot.mkv", ep, "Lanterns", "lanterns", 1, 1, 0, "Pilot", "Pilot", 0, true},
	{"incoming/Lanterns.2026.S01E02.1080p.x265-ELiTE.mkv", ep, "Lanterns", "lanterns", 1, 2, 0, "", "Episode 2", 2026, true},
	{"incoming/Lanterns.S01E05.Lights.Out.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", ep, "Lanterns", "lanterns", 1, 5, 0, "Lights Out", "Lights Out", 0, true},
	{"1883.S01E01.1080p.mkv", ep, "1883", "1883", 1, 1, 0, "", "Episode 1", 0, true},

	// " - " separators and parenthesised years.
	{"incoming/East of Eden - S01E01 - Brothers.mkv", ep, "East of Eden", "east of eden", 1, 1, 0, "Brothers", "Brothers", 0, true},
	{"incoming/East of Eden - S01E03 - In the Valley.mkv", ep, "East of Eden", "east of eden", 1, 3, 0, "In the Valley", "In the Valley", 0, true},
	{"incoming/Euphoria (2019) - S00E01 - Trouble Don't Last Always (1080p AMZN WEB-DL x265 t3nzin) (1).mkv", ep, "Euphoria", "euphoria", 0, 1, 0, "Trouble Don't Last Always", "Trouble Don't Last Always", 2019, true},
	{"incoming/Euphoria (2019) - S01E04 - Shook Ones Pt. II (1080p AMZN WEB-DL x265 t3nzin).mkv", ep, "Euphoria", "euphoria", 1, 4, 0, "Shook Ones Pt. II", "Shook Ones Pt. II", 2019, true},
	{"incoming/Euphoria (2019) - S01E05 - '03 Bonnie and Clyde (1080p AMZN WEB-DL x265 t3nzin).mkv", ep, "Euphoria", "euphoria", 1, 5, 0, "'03 Bonnie and Clyde", "'03 Bonnie and Clyde", 2019, true},
	{"incoming/Invincible - S04E02 - I'LL GIVE YOU THE GRAND TOUR.mkv", ep, "Invincible", "invincible", 4, 2, 0, "I'll Give You the Grand Tour", "I'll Give You the Grand Tour", 0, true},

	// Country suffixes stay in the key; the classifier retries without them.
	{"incoming/Euphoria.US.S02E01.Trying.to.Get.to.Heaven.Before.They.Close.the.Door.1080p.HMAX.10bit.DDP.5.1.x265.[HashMiner].mkv", ep, "Euphoria US", "euphoria us", 2, 1, 0, "Trying to Get to Heaven Before They Close the Door", "Trying to Get to Heaven Before They Close the Door", 0, true},
	{"Shameless (US) - S01E01 - Pilot.mkv", ep, "Shameless US", "shameless us", 1, 1, 0, "Pilot", "Pilot", 0, true},
	{"The.Office.UK.S01E01.Downsize.mkv", ep, "The Office UK", "the office uk", 1, 1, 0, "Downsize", "Downsize", 0, true},

	// Shouted names are title-cased; an "Episode N" prefix on the title goes.
	{"incoming/THE.GHOST.IN.THE.SHELL.S01E01.EPISODE.01.PROLOGUE.SUPER.SPARTAN.i.1080p.AMZN.WEB-DL.ENG.ITA.JAP.H264-kARiDo.mkv", ep, "The Ghost in the Shell", "the ghost in the shell", 1, 1, 0, "Prologue Super Spartan I", "Prologue Super Spartan I", 0, true},
	{"incoming/THE.GHOST.IN.THE.SHELL.S01E04.ROBOT.RONDO.1080p.AMZN.WEB-DL.ENG.ITA.JAP.H264-kARiDo.mkv", ep, "The Ghost in the Shell", "the ghost in the shell", 1, 4, 0, "Robot Rondo", "Robot Rondo", 0, true},

	// Marker first (M8): the show runs to "Episodio N" or a year; unsure.
	{"incoming/S01E05 The Ghost in the Shell Episodio 05 Phantom Fund (2026) WEBRip 1080p x264 EAC3 ITA ENG JPN SUB ITA ENG - Lullozzo.mkv", ep, "The Ghost in the Shell", "the ghost in the shell", 1, 5, 0, "Phantom Fund", "Phantom Fund", 2026, false},
	{"S02E03 Some Show 2024 The Title.mkv", ep, "Some Show", "some show", 2, 3, 0, "The Title", "The Title", 2024, false},

	// Mr. Robot codenames are kept verbatim, dots included.
	{"incoming/Mr.Robot.S01E01.eps1.0.hellofriend.mov.1080p.10bit.BluRay.AAC5.1.HEVC-Vyndros.mkv", ep, "Mr Robot", "mr robot", 1, 1, 0, "eps1.0.hellofriend.mov", "eps1.0.hellofriend.mov", 0, true},
	{"incoming/Mr.Robot.S01E06.eps1.5.br4ve-trave1er.asf.1080p.10bit.BluRay.AAC5.1.HEVC-Vyndros.mkv", ep, "Mr Robot", "mr robot", 1, 6, 0, "eps1.5.br4ve-trave1er.asf", "eps1.5.br4ve-trave1er.asf", 0, true},
	{"incoming/Mr.Robot.S02E01-E02.eps2.0.unm4sk-pt1.tc.&.eps2.0.unm4sk-pt2.tc.1080p.10bit.BluRay.AAC5.1.HEVC-Vyndros.mkv", ep, "Mr Robot", "mr robot", 2, 1, 2, "eps2.0.unm4sk-pt1.tc.&.eps2.0.unm4sk-pt2.tc", "eps2.0.unm4sk-pt1.tc.&.eps2.0.unm4sk-pt2.tc", 0, true},
	{"incoming/Mr.Robot.S03E10.shutdown.-r.1080p.10bit.BluRay.AAC5.1.HEVC-Vyndros.mkv", ep, "Mr Robot", "mr robot", 3, 10, 0, "Shutdown -r", "Shutdown -r", 0, true},
	// Underscores are always separators, so this codename loses its "_".
	{"Mr. Robot - S01E01 - eps1.0_hellofriend.mov.mkv", ep, "Mr. Robot", "mr robot", 1, 1, 0, "eps1.0 hellofriend.mov", "eps1.0 hellofriend.mov", 0, true},

	// Other markers.
	{"Severance.S01E01E02.mkv", ep, "Severance", "severance", 1, 1, 2, "", "Episodes 1–2", 0, true},
	{"Doctor.Who.1x02.The.End.of.the.World.mkv", ep, "Doctor Who", "doctor who", 1, 2, 0, "The End of the World", "The End of the World", 0, true},
	{"Law.&.Order.S01E01.mkv", ep, "Law & Order", "law and order", 1, 1, 0, "", "Episode 1", 0, true},
	{"Grey's.Anatomy.S02E03.mkv", ep, "Grey's Anatomy", "greys anatomy", 2, 3, 0, "", "Episode 3", 0, true},

	// Season folders: the folder supplies the year, or the show and season.
	{"incoming/Carrie S01 (2026) 1080p WEBRip x265 DDP 5.1 ESub/Carrie S01 E01.mkv", ep, "Carrie", "carrie", 1, 1, 0, "", "Episode 1", 2026, true},
	{"incoming/Carrie S01 (2026) 1080p WEBRip x265 DDP 5.1 ESub/Carrie S01 E08.mkv", ep, "Carrie", "carrie", 1, 8, 0, "", "Episode 8", 2026, true},
	{"incoming/Carrie S01 (2026) 1080p WEBRip x265 DDP 5.1 ESub/Carrie E03.mkv", ep, "Carrie", "carrie", 1, 3, 0, "", "Episode 3", 2026, true},
	{"incoming/Below.S01.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir/Below.S01E01.Come.Home.Year.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", ep, "Below", "below", 1, 1, 0, "Come Home Year", "Come Home Year", 0, true},
	{"incoming/The Terminal List Season 1 Mp4 1080p/The Terminal List S01E01.mp4", ep, "The Terminal List", "the terminal list", 1, 1, 0, "", "Episode 1", 0, true},
	{"incoming/The Terminal List Season 1 Mp4 1080p/E04 The Engram.mp4", ep, "The Terminal List", "the terminal list", 1, 4, 0, "The Engram", "The Engram", 0, true},
	{"TV/Breaking Bad/Season 2/Episode 5.mkv", ep, "Breaking Bad", "breaking bad", 2, 5, 0, "", "Episode 5", 0, true},
	{"Anime/Season 01/Bocchi E01.mkv", ep, "Bocchi", "bocchi", 1, 1, 0, "", "Episode 1", 0, true},
	{"Breaking Bad Season 1/S01E02 Cats in the Bag.mkv", ep, "Breaking Bad", "breaking bad", 1, 2, 0, "Cats in the Bag", "Cats in the Bag", 0, true},

	// Fansub convention (M7): season 1; sure only with a leading [Group].
	{"incoming/[DB]Shigatsu wa Kimi no Uso_-_01_(Dual Audio_10bit_BD1080p_x265).mkv", ep, "Shigatsu wa Kimi no Uso", "shigatsu wa kimi no uso", 1, 1, 0, "", "Episode 1", 0, true},
	{"incoming/[DB]Shigatsu wa Kimi no Uso_-_22_(Dual Audio_10bit_BD1080p_x265).mkv", ep, "Shigatsu wa Kimi no Uso", "shigatsu wa kimi no uso", 1, 22, 0, "", "Episode 22", 0, true},
	{"[SubsPlease] Frieren - 05v2 (1080p) [ABCD1234].mkv", ep, "Frieren", "frieren", 1, 5, 0, "", "Episode 5", 0, true},
	{"[Erai-raws] Dandadan - 03 - Unknown Grandma [1080p][HEVC].mkv", ep, "Dandadan", "dandadan", 1, 3, 0, "Unknown Grandma", "Unknown Grandma", 0, true},
	{"Frieren - 06 (1080p).mkv", ep, "Frieren", "frieren", 1, 6, 0, "", "Episode 6", 0, false},

	// Unsure episodes: no name in the file, a generic name, or junk left over.
	{"incoming/Breaking Bad/S01E01.mkv", ep, "Breaking Bad", "breaking bad", 1, 1, 0, "", "Episode 1", 0, false},
	{"S01E01.mkv", ep, "", "", 1, 1, 0, "", "Episode 1", 0, false},
	{"Show.S01E02.1080p.mkv", ep, "Show", "show", 1, 2, 0, "", "Episode 2", 0, false},
	{"Night.Watch.XFR.TBD.S01E01.mkv", ep, "Night Watch XFR TBD", "night watch xfr tbd", 1, 1, 0, "", "Episode 1", 0, false},

	// Movies and other videos.
	{"incoming/12 Angry Men (1957) (in colour) 1080p 640k AC3 (moviesbyrizzo upl).mkv", un, "", "", 0, 0, 0, "", "12 Angry Men", 1957, true},
	{"incoming/28.Years.Later.The.Bone.Temple.2026.1080p.WEBRip.10Bit.DDP.5.1.x265-NeoNoir.mkv", un, "", "", 0, 0, 0, "", "28 Years Later The Bone Temple", 2026, true},
	{"incoming/300.avi", un, "", "", 0, 0, 0, "", "300", 0, true},
	{"incoming/Akira.1988.1080p.BluRay.x264-[YTS.AM].mp4", un, "", "", 0, 0, 0, "", "Akira", 1988, true},
	{"incoming/Arrival.2016.DVDScr.x264-4RRIVED.mkv", un, "", "", 0, 0, 0, "", "Arrival", 2016, true},
	{"incoming/Backrooms 2026 1080p WEB-DL HEVC x265 5.1 BONE.mkv", un, "", "", 0, 0, 0, "", "Backrooms", 2026, true},
	{"incoming/Blade Runner 2049.HDRip.XviD.AC3-EVO.avi", un, "", "", 0, 0, 0, "", "Blade Runner 2049", 0, true},
	{"incoming/Dear.You.2026.HC.1080p.WEBRip.10bit.DD2.0.x265-FaS.mkv", un, "", "", 0, 0, 0, "", "Dear You", 2026, true},
	{"incoming/Disclosure.Day.2026.1080p.MA.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", un, "", "", 0, 0, 0, "", "Disclosure Day", 2026, true},
	{"incoming/Dracula A Love Tale 2025 1080p HDRip HC HEVC x265 5.1 BONE.mkv", un, "", "", 0, 0, 0, "", "Dracula A Love Tale", 2025, true},
	{"incoming/Dune.2021.2160p.HMAX.WEB-DL.DDP5.1.Atmos.HDR.HEVC-CM.mkv", un, "", "", 0, 0, 0, "", "Dune", 2021, true},
	{"incoming/Everything.Everywhere.All.At.Once.2022.1080p.WEBRip.x264-RARBG.mp4", un, "", "", 0, 0, 0, "", "Everything Everywhere All At Once", 2022, true},
	{"incoming/Forbidden Fruits (2026) 1080p H264 iTA EnG AC3 Sub iTA EnG-MIRCrew.mkv", un, "", "", 0, 0, 0, "", "Forbidden Fruits", 2026, true},
	{"incoming/Iron Man (2008) (1080p DS4K BluRay x265 10-bit HDR AAC 7.1) [WeSLeY].mkv", un, "", "", 0, 0, 0, "", "Iron Man", 2008, true},
	{"incoming/Iron.Lung.2026.1080p.WEBRip.x265.6CH-SupaCvnt.mkv", un, "", "", 0, 0, 0, "", "Iron Lung", 2026, true},
	{"incoming/Pressure.2026.2026.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", un, "", "", 0, 0, 0, "", "Pressure", 2026, true},
	{"incoming/Ran (1985) Akira Kurosawa [BDRip720p] [MultiLang - MultiSub] by Pitt@Sk8.mkv", un, "", "", 0, 0, 0, "", "Ran", 1985, true},
	{"incoming/S4 The Bob Lazar Story (2026).mkv", un, "", "", 0, 0, 0, "", "S4 The Bob Lazar Story", 2026, false},
	{"incoming/Soulm8te.2026.1080p.WEBRip.10Bit.DDP5.1.x265-NeoNoir.mkv", un, "", "", 0, 0, 0, "", "Soulm8te", 2026, false},
	{"incoming/The Descent 2005 REMASTERED UNRATED 1080p BluRay HEVC x265 5.1 BONE.mkv", un, "", "", 0, 0, 0, "", "The Descent", 2005, true},
	{"incoming/The Duelist (2016) UNCUT 720p BluRay x264 Eng Subs [Dual Audio] [Hindi DD 2.0 - Russian 2.0]/The Duelist (2016) UNCUT 720p BluRay x264 Eng Subs [Dual Audio] [Hindi DD 2.0 - Russian 2.0].mkv", un, "", "", 0, 0, 0, "", "The Duelist", 2016, true},
	{"incoming/The Secret Agent (2025) (1080p BluRay x265 EAC3 Silence).mkv", un, "", "", 0, 0, 0, "", "The Secret Agent", 2025, true},
	{"incoming/The Secret Agent (2025) (1080p BluRay x265 10bit EAC3 5.1 Portuguese Silence)/Featurettes/Deleted Scenes/Newspaper.mkv", un, "", "", 0, 0, 0, "", "Newspaper", 0, true},
	{"incoming/The Secret Agent (2025) (1080p BluRay x265 10bit EAC3 5.1 Portuguese Silence)/Featurettes/Trailer.mkv", un, "", "", 0, 0, 0, "", "Trailer", 0, false},
	{"incoming/The.Lord.of.the.Rings.The.Fellowship.of.the.Ring.2001.EXTENDED.REMASTERED.1080p.BluRay.x265-RARBG.mp4", un, "", "", 0, 0, 0, "", "The Lord of the Rings The Fellowship of the Ring", 2001, true},
	{"incoming/Toy.Story.5.2026.1080p.DCPRIP.LTE.x264-SPLiCE.mkv", un, "", "", 0, 0, 0, "", "Toy Story 5", 2026, true},
	{"incoming/Wuthering Heights 2026 KORSUB 1080p WEB H264-RGB.mkv", un, "", "", 0, 0, 0, "", "Wuthering Heights", 2026, true},
	{"incoming/YOLO 2024 1080p Chinese WEB.DL HEVC x265 5.1 BONE.mkv", un, "", "", 0, 0, 0, "", "YOLO", 2024, true},
	{"1080p.mkv", un, "", "", 0, 0, 0, "", "1080p", 0, false},
	{"Movies/2001.A.Space.Odyssey.1968.1080p.mkv", un, "", "", 0, 0, 0, "", "2001 A Space Odyssey", 1968, true},

	// The v1 title_test.go cases.
	{"Big.Buck.Bunny.(2008).1080p.BluRay.x264.mkv", un, "", "", 0, 0, 0, "", "Big Buck Bunny", 2008, true},
	{"Sintel_2010_1080p_WEBRip.mp4", un, "", "", 0, 0, 0, "", "Sintel", 2010, true},
	{"[Group] The.Movie.Name.2160p.HEVC.mkv", un, "", "", 0, 0, 0, "", "The Movie Name", 0, true},
	{"les.misérables.(2012).1080p.mkv", un, "", "", 0, 0, 0, "", "Les Misérables", 2012, true},
	{"Spider-Man.(2002).1080p.BluRay.x264.mkv", un, "", "", 0, 0, 0, "", "Spider-Man", 2002, true},
	{"Some.Movie.2019.1080p.WEB-DL.x264-NTG.mkv", un, "", "", 0, 0, 0, "", "Some Movie", 2019, true},
}

func TestParsePath(t *testing.T) {
	for _, tc := range parseCases {
		got := ParsePath(tc.path)
		year := 0
		if got.Year != nil {
			year = *got.Year
		}
		check := func(field string, got, want any) {
			if got != want {
				t.Errorf("%s\n  %s = %#v, want %#v", tc.path, field, got, want)
			}
		}
		check("Kind", got.Kind, tc.kind)
		check("ShowName", got.ShowName, tc.showName)
		check("ShowKey", got.ShowKey, tc.showKey)
		check("Season", got.Season, tc.season)
		check("Episode", got.Episode, tc.episode)
		check("EpisodeEnd", got.EpisodeEnd, tc.episodeEnd)
		check("EpisodeTitle", got.EpisodeTitle, tc.episodeTitle)
		check("Title", got.Title, tc.title)
		check("Year", year, tc.year)
		check("Sure", got.Sure, tc.sure)
	}
}

func TestShowKey(t *testing.T) {
	tests := []struct{ name, want string }{
		{"Dark.Matter.2024", "dark matter"},
		{"Euphoria US", "euphoria us"},
		{"Lanterns (2026)", "lanterns"},
		{"Law & Order", "law and order"},
		{"Grey's Anatomy", "greys anatomy"},
		{"Grey’s Anatomy", "greys anatomy"},
		{"Spider-Man: Far From Home", "spider man far from home"},
		{"Les Misérables", "les misérables"},
		{"1883", "1883"},
		{"  ", ""},
	}
	for _, tt := range tests {
		if got := ShowKey(tt.name); got != tt.want {
			t.Errorf("ShowKey(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
