package library

import "testing"

func TestParseName(t *testing.T) {
	cases := []struct {
		rel, typ string
		want     Parsed
	}{
		{"Filmname (2024).mkv", "movie", Parsed{Kind: "movie", Title: "Filmname", Year: 2024}},
		{"Der.Pate.1972.German.1080p.BluRay.x264.mkv", "movie", Parsed{Kind: "movie", Title: "Der Pate", Year: 1972}},
		{"Inception/movie.mkv", "movie", Parsed{Kind: "movie", Title: "Inception"}},
		{"Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv", "movie", Parsed{Kind: "movie", Title: "Blade Runner 2049", Year: 2017}},
		{"Serienname - S01E03.mkv", "tvshow", Parsed{Kind: "episode", Title: "Serienname", Season: 1, Episode: 3, EpisodeEnd: 3}},
		{"Serienname/Season 01/Episode 03.mp4", "tvshow", Parsed{Kind: "episode", Title: "Serienname", Season: 1, Episode: 3, EpisodeEnd: 3}},
		{"Dark/Staffel 2/05 - Die Reise.mkv", "tvshow", Parsed{Kind: "episode", Title: "Dark", Season: 2, Episode: 5, EpisodeEnd: 5, EpTitle: "Die Reise"}},
		{"Show.Name.S02E05E06.720p.mkv", "tvshow", Parsed{Kind: "episode", Title: "Show Name", Season: 2, Episode: 5, EpisodeEnd: 6}},
		{"Show Name - S03E01-E03 - Titel.mkv", "tvshow", Parsed{Kind: "episode", Title: "Show Name", Season: 3, Episode: 1, EpisodeEnd: 3, EpTitle: "Titel"}},
		{"Lost/Season 1/s01e02.mkv", "tvshow", Parsed{Kind: "episode", Title: "Lost", Season: 1, Episode: 2, EpisodeEnd: 2}},
		{"Friends 1x05.avi", "tvshow", Parsed{Kind: "episode", Title: "Friends", Season: 1, Episode: 5, EpisodeEnd: 5}},
		{"Urlaub_2019.mp4", "home_video", Parsed{Kind: "home_video", Title: "Urlaub 2019"}},
	}
	for _, c := range cases {
		got := ParseName(c.rel, c.typ)
		if got != c.want {
			t.Errorf("%s\n got  %+v\n want %+v", c.rel, got, c.want)
		}
	}
}
