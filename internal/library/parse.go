// Package library is the Media Library Engine (Komponente D): Dateinamen-
// erkennung, Scanner und Index.
package library

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// VideoExts: von FFmpeg unterstützte, in der Praxis übliche Container.
var VideoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".mpg": true,
	".mpeg": true, ".ts": true, ".m2ts": true, ".mts": true, ".webm": true, ".wmv": true,
	".flv": true, ".vob": true, ".iso": false, ".divx": true, ".ogm": true, ".3gp": true,
}

// Parsed is the result of filename recognition.
type Parsed struct {
	Kind       string // "movie" | "episode"
	Title      string // Film- bzw. Serientitel
	Year       int
	Season     int
	Episode    int
	EpisodeEnd int // >Episode bei Mehrfachfolgen (S01E01E02, S01E01-E03)
	EpTitle    string
	Channel    string // TV-Aufnahmen: Sender (aus dem Dateinamen)
	Recorded   string // TV-Aufnahmen: Aufnahmezeit "YYYY-MM-DD HH:MM:SS"
}

// reRecording: Dateiname einer Enigma2-Aufnahme (VU+, Gigablue …):
// „20231105 2015 - Das Erste HD - Tatort.ts“.
var reRecording = regexp.MustCompile(`^(\d{4})(\d{2})(\d{2})\s+(\d{2})(\d{2})\s+-\s+(.+?)\s+-\s+(.+)$`)

// parseRecording: TV-Aufnahme – Titel, Sender und Aufnahmezeit, soweit der
// Dateiname sie hergibt; sonst wie ein Heimvideo.
func parseRecording(base string) Parsed {
	if m := reRecording.FindStringSubmatch(base); m != nil {
		title := cleanTitle(m[7])
		if title == "" {
			title = cleanTitle(base)
		}
		return Parsed{Kind: "tv_record", Title: title, Channel: strings.TrimSpace(m[6]),
			Recorded: m[1] + "-" + m[2] + "-" + m[3] + " " + m[4] + ":" + m[5] + ":00"}
	}
	return Parsed{Kind: "tv_record", Title: cleanTitle(base)}
}

var (
	reSxxEyy  = regexp.MustCompile(`(?i)^(.*?)[\s._\-\[\(]*s(\d{1,2})[\s._\-]*e(\d{1,3})((?:[\s._\-]*(?:e|-e|-)\d{1,3})*)(.*)$`)
	reNxNN    = regexp.MustCompile(`(?i)^(.*?)[\s._\-]+(\d{1,2})x(\d{2,3})((?:[\s._\-]*x?\d{2,3})*)(.*)$`)
	reEpOnly  = regexp.MustCompile(`(?i)^(?:episode|folge|ep|e)[\s._\-]*(\d{1,3})(.*)$`)
	reNumLead = regexp.MustCompile(`^(\d{1,3})(?:[\s._\-]+(.*))?$`)
	reSeason  = regexp.MustCompile(`(?i)^(?:season|staffel|s)[\s._\-]*(\d{1,2})$`)
	reYear    = regexp.MustCompile(`^(.*)[\s._\-]+[\(\[]?((?:19|20)\d{2})[\)\]]?(?:[\s._\-\[\(]|$)`)
	reMultiEp = regexp.MustCompile(`\d{1,3}`)
	reJunk    = regexp.MustCompile(`(?i)[\s._\-](?:2160p|1080p|1080i|720p|576p|480p|4k|uhd|hdr10?|dv|bluray|blu-ray|bdrip|brrip|web-?dl|webrip|hdtv|dvdrip|x26[45]|h\.?26[45]|hevc|avc|aac|ac3|dts|truehd|atmos|remux|german|deutsch|multi|dl|ger|eng)\b.*$`)
)

// ParseName recognises movie and episode names. dir is the folder path
// relative to the library root (für "Serie/Season 01/Episode 03.mp4").
func ParseName(relPath string, folderType string) Parsed {
	base := strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	dirParts := splitDirs(filepath.Dir(relPath))

	switch folderType {
	case "home_video":
		return Parsed{Kind: "home_video", Title: cleanTitle(base)}
	case "tv_record":
		return parseRecording(base)
	case "tvshow":
		if p, ok := parseEpisode(base, dirParts); ok {
			return p
		}
		// Serienordner ohne erkennbares Muster: als Folge 0 der Staffel 0 führen.
		show := base
		if len(dirParts) > 0 {
			show = dirParts[0]
		}
		return Parsed{Kind: "episode", Title: cleanTitle(show), EpTitle: cleanTitle(base)}
	}
	return parseMovie(base, dirParts)
}

func splitDirs(d string) []string {
	if d == "." || d == "" || d == "/" {
		return nil
	}
	return strings.Split(filepath.ToSlash(d), "/")
}

func parseEpisode(base string, dirs []string) (Parsed, bool) {
	if m := reSxxEyy.FindStringSubmatch(base); m != nil {
		p := Parsed{Kind: "episode"}
		p.Season, _ = strconv.Atoi(m[2])
		p.Episode, _ = strconv.Atoi(m[3])
		p.EpisodeEnd = lastNum(m[4], p.Episode)
		p.Title = cleanTitle(m[1])
		p.EpTitle = cleanTitle(strings.TrimLeft(m[5], " ._-"))
		if p.Title == "" {
			p.Title = showFromDirs(dirs)
		}
		return p, true
	}
	if m := reNxNN.FindStringSubmatch(base); m != nil && m[1] != "" {
		p := Parsed{Kind: "episode"}
		p.Season, _ = strconv.Atoi(m[2])
		p.Episode, _ = strconv.Atoi(m[3])
		p.EpisodeEnd = lastNum(m[4], p.Episode)
		p.Title = cleanTitle(m[1])
		p.EpTitle = cleanTitle(strings.TrimLeft(m[5], " ._-"))
		return p, true
	}
	// Ordnerstruktur: Serie/Season 01/Episode 03.mp4 bzw. Serie/Staffel 1/03 - Titel.mkv
	if len(dirs) >= 1 {
		season, showIdx := -1, -1
		if sm := reSeason.FindStringSubmatch(dirs[len(dirs)-1]); sm != nil {
			season, _ = strconv.Atoi(sm[1])
			showIdx = len(dirs) - 2
		}
		ep, rest := -1, ""
		if em := reEpOnly.FindStringSubmatch(base); em != nil {
			ep, _ = strconv.Atoi(em[1])
			rest = em[2]
		} else if nm := reNumLead.FindStringSubmatch(base); nm != nil && season >= 0 {
			ep, _ = strconv.Atoi(nm[1])
			rest = nm[2]
		}
		if ep >= 0 && (season >= 0 || len(dirs) >= 1) {
			p := Parsed{Kind: "episode", Season: max(season, 1), Episode: ep, EpisodeEnd: ep}
			if season < 0 {
				p.Season = 1
				showIdx = len(dirs) - 1
			}
			if showIdx >= 0 {
				p.Title = cleanTitle(dirs[showIdx])
			}
			p.EpTitle = cleanTitle(strings.TrimLeft(rest, " ._-"))
			return p, true
		}
	}
	return Parsed{}, false
}

func showFromDirs(dirs []string) string {
	for i := len(dirs) - 1; i >= 0; i-- {
		if reSeason.MatchString(dirs[i]) {
			continue
		}
		return cleanTitle(dirs[i])
	}
	return ""
}

func lastNum(s string, def int) int {
	nums := reMultiEp.FindAllString(s, -1)
	if len(nums) == 0 {
		return def
	}
	n, _ := strconv.Atoi(nums[len(nums)-1])
	if n < def {
		return def
	}
	return n
}

func parseMovie(base string, dirs []string) Parsed {
	p := Parsed{Kind: "movie"}
	name := base
	if m := reYear.FindStringSubmatch(name); m != nil && strings.TrimSpace(m[1]) != "" {
		p.Year, _ = strconv.Atoi(m[2])
		name = m[1]
	} else {
		name = reJunk.ReplaceAllString(name, "")
		// Generische Dateinamen (movie.mkv, film.mp4) → Ordnername verwenden.
		if len(dirs) > 0 && isGeneric(name) {
			return parseMovie(dirs[len(dirs)-1], dirs[:len(dirs)-1])
		}
	}
	p.Title = cleanTitle(name)
	if p.Title == "" {
		p.Title = base
	}
	return p
}

func isGeneric(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "movie", "film", "video", "main", "feature", "index", "1":
		return true
	}
	return false
}

func cleanTitle(s string) string {
	orig := s
	s = reJunk.ReplaceAllString(" "+s, "")
	if !strings.Contains(strings.TrimSpace(orig), " ") {
		s = strings.NewReplacer(".", " ", "_", " ").Replace(s)
	} else {
		s = strings.ReplaceAll(s, "_", " ")
	}
	s = strings.Trim(s, " -._[]()")
	return strings.Join(strings.Fields(s), " ")
}

// SortTitle strips leading articles (deutsch/englisch) für die Sortierung.
func SortTitle(t string) string {
	l := strings.ToLower(t)
	for _, a := range []string{"the ", "a ", "an ", "der ", "die ", "das ", "ein ", "eine "} {
		if strings.HasPrefix(l, a) && len(t) > len(a) {
			return t[len(a):]
		}
	}
	return t
}
