package jellyfin

import (
	"net/http"
)

// route: angemeldete Aufrufe. Jellyfin kennt viele Pfade doppelt (alt mit
// /Users/{userId}/…, ab 10.9 ohne) – beide Formen werden bedient; die
// userId wird ignoriert, maßgeblich ist das Konto zum Token.
func (h *Handler) route(x *req) {
	m := x.r.Method
	get := m == http.MethodGet || m == http.MethodHead
	post := m == http.MethodPost
	del := m == http.MethodDelete
	if v, ok := x.match("users/*/items/*"); ok && len(x.segs) == 4 {
		switch x.segs[3] {
		case "latest":
			h.latest(x)
		case "resume":
			h.resume(x)
		case "root":
			writeJSON(x.w, h.dto(x, entry{kind: kRoot}, dtoOpts{}))
		default:
			h.item(x, v[1])
		}
		return
	}
	switch {
	// System und Benutzer
	case x.is("system/info"):
		writeJSON(x.w, h.systemInfo(x))
	case x.is("system/endpoint"):
		writeJSON(x.w, map[string]any{"IsLocal": false, "IsInNetwork": true})
	case x.is("users/me"):
		writeJSON(x.w, h.userDto(x))
	case x.is("users") && get:
		writeJSON(x.w, []any{h.userDto(x)})
	case x.is("sessions/logout") && post:
		h.logout(x)
	case x.is("sessions") && get:
		writeJSON(x.w, []any{})
	case x.is("sessions/capabilities") || x.is("sessions/capabilities/full"):
		noContent(x.w)
	case x.is("quickconnect/authorize") && post:
		if err := h.QuickConnect(x.Q("code"), x.sess); err != nil {
			jsonError(x.w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(x.w, true)

	// Bibliotheken und Objekte
	case x.is("userviews"), x.is("library/mediafolders"):
		h.userViews(x)
	case x.is("items") && get:
		h.items(x)
	case x.is("items/latest"):
		h.latest(x)
	case x.is("useritems/resume"):
		h.resume(x)
	case x.is("items/root"):
		writeJSON(x.w, h.dto(x, entry{kind: kRoot}, dtoOpts{}))
	case x.is("items/filters"):
		writeJSON(x.w, map[string]any{"Genres": []string{}, "Tags": []string{}, "OfficialRatings": []string{}, "Years": []int{}})
	case x.is("items/filters2"):
		writeJSON(x.w, map[string]any{"Genres": []any{}, "Tags": []string{}})
	case x.is("genres"):
		h.genres(x)
	case x.is("search/hints"):
		h.searchHints(x)
	case x.is("shows/nextup"):
		h.nextUp(x)
	case x.is("shows/upcoming"), x.is("persons"), x.is("studios"), x.is("artists"), x.is("channels"),
		x.is("livetv/channels"), x.is("livetv/programs"), x.is("livetv/recordings"), x.is("trailers"), x.is("playlists"):
		writeJSON(x.w, emptyResult())
	case x.is("livetv/info"):
		writeJSON(x.w, map[string]any{"Services": []any{}, "IsEnabled": false, "EnabledUsers": []any{}})
	case x.is("localization/options"), x.is("localization/cultures"), x.is("localization/countries"),
		x.is("localization/parentalratings"), x.is("plugins"), x.is("packages"):
		writeJSON(x.w, []any{})
	case x.is("playback/bitratetest") && get:
		h.bitrateTest(x)
	case x.is("dsvrdiag/requests") && get:
		h.diagRequests(x)
	case x.is("sessions/playing") && post:
		h.report(x, "", "start")
	case x.is("sessions/playing/progress") && post:
		h.report(x, "", "progress")
	case x.is("sessions/playing/stopped") && post:
		h.report(x, "", "stopped")
	case x.is("sessions/playing/ping") && post:
		noContent(x.w)
	case x.is("videos/activeencodings") && del:
		h.stopSession(x.Q("PlaySessionId"))
		noContent(x.w)
	default:
		h.routeVars(x, get, post, del)
	}
}

// routeVars: Pfade mit IDs.
func (h *Handler) routeVars(x *req, get, post, del bool) {
	if v, ok := x.match("items/*"); ok && get {
		h.item(x, v[0])
		return
	}
	if v, ok := x.match("users/*"); ok && get {
		_ = v
		writeJSON(x.w, h.userDto(x))
		return
	}
	for _, p := range []struct {
		pat string
		fn  func(v []string)
	}{
		{"users/*/views", func(v []string) { h.userViews(x) }},
		{"users/*/groupingoptions", func(v []string) { writeJSON(x.w, []any{}) }},
		{"users/*/configuration", func(v []string) { noContent(x.w) }},
		{"users/*/policy", func(v []string) { noContent(x.w) }},
		{"items/*/ancestors", func(v []string) { h.ancestors(x, v[0]) }},
		{"items/*/similar", func(v []string) { h.similar(x, v[0]) }},
		{"shows/*/similar", func(v []string) { h.similar(x, v[0]) }},
		{"movies/*/similar", func(v []string) { h.similar(x, v[0]) }},
		{"items/*/playbackinfo", func(v []string) { h.playbackInfo(x, v[0]) }},
		{"items/*/download", func(v []string) { h.streamFile(x, v[0], true) }},
		{"dsvroffline/file/*", func(v []string) { h.offlineFile(x, v[0]) }},
		{"dsvroffline/*", func(v []string) { h.offline(x, v[0]) }},
		{"items/*/file", func(v []string) { h.streamFile(x, v[0], true) }},
		{"items/*/intros", func(v []string) { writeJSON(x.w, emptyResult()) }},
		{"users/*/items/*/intros", func(v []string) { writeJSON(x.w, emptyResult()) }},
		{"items/*/specialfeatures", func(v []string) { writeJSON(x.w, []any{}) }},
		{"users/*/items/*/specialfeatures", func(v []string) { writeJSON(x.w, []any{}) }},
		{"items/*/localtrailers", func(v []string) { writeJSON(x.w, []any{}) }},
		{"users/*/items/*/localtrailers", func(v []string) { writeJSON(x.w, []any{}) }},
		{"items/*/thememedia", func(v []string) {
			empty := map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0, "OwnerId": v[0]}
			writeJSON(x.w, map[string]any{"ThemeVideosResult": empty, "ThemeSongsResult": empty, "SoundtrackSongsResult": empty})
		}},
		{"items/*/themesongs", func(v []string) { writeJSON(x.w, emptyResult()) }},
		{"items/*/themevideos", func(v []string) { writeJSON(x.w, emptyResult()) }},
		{"items/*/criticreviews", func(v []string) { writeJSON(x.w, emptyResult()) }},
		{"mediasegments/*", func(v []string) { writeJSON(x.w, emptyResult()) }},
		{"episode/*/introtimestamps", func(v []string) { http.NotFound(x.w, x.r) }},
		{"shows/*/seasons", func(v []string) { h.showSeasons(x, v[0]) }},
		{"shows/*/episodes", func(v []string) { h.showEpisodes(x, v[0]) }},
		{"users/*/items", func(v []string) { h.items(x) }},
		{"displaypreferences/*", func(v []string) { h.displayPrefs(x, v[0]) }},
		{"users/*/images/*", func(v []string) { http.NotFound(x.w, x.r) }},
		{"users/*/images/*/*", func(v []string) { http.NotFound(x.w, x.r) }},
		// Wiedergabe
		{"videos/*/stream", func(v []string) { h.streamFile(x, v[0], false) }},
		{"videos/*/master.m3u8", func(v []string) { h.master(x, v[0]) }},
		{"videos/*/main.m3u8", func(v []string) { h.mainPlaylist(x, v[0]) }},
		{"videos/*/*/subtitles/*/*/*", func(v []string) { h.subtitle(x, v[0], v[1], v[2], v[4]) }},
		{"videos/*/*/subtitles/*/*", func(v []string) { h.subtitle(x, v[0], v[1], v[2], v[3]) }},
		// Fortschritt (alt und neu)
		{"users/*/playingitems/*", func(v []string) {
			if del {
				h.report(x, v[1], "stopped")
			} else {
				h.report(x, v[1], "start")
			}
		}},
		{"users/*/playingitems/*/progress", func(v []string) { h.report(x, v[1], "progress") }},
		{"playingitems/*", func(v []string) {
			if del {
				h.report(x, v[0], "stopped")
			} else {
				h.report(x, v[0], "start")
			}
		}},
		{"playingitems/*/progress", func(v []string) { h.report(x, v[0], "progress") }},
		// gesehen / Favoriten / Benutzerdaten
		{"users/*/playeditems/*", func(v []string) { h.setPlayed(x, v[1], !del) }},
		{"userplayeditems/*", func(v []string) { h.setPlayed(x, v[0], !del) }},
		{"users/*/favoriteitems/*", func(v []string) { h.setFavorite(x, v[1], !del) }},
		{"userfavoriteitems/*", func(v []string) { h.setFavorite(x, v[0], !del) }},
		{"users/*/items/*/userdata", func(v []string) { h.itemUserData(x, v[1]) }},
		{"useritems/*/userdata", func(v []string) { h.itemUserData(x, v[0]) }},
		{"users/*/items/*/rating", func(v []string) { h.itemUserData(x, v[1]) }},
		{"useritems/*/rating", func(v []string) { h.itemUserData(x, v[0]) }},
	} {
		if v, ok := x.match(p.pat); ok {
			p.fn(v)
			return
		}
	}
	// /Videos/{id}/stream.mkv usw.
	if len(x.segs) == 3 && x.segs[0] == "videos" && len(x.segs[2]) > 7 && x.segs[2][:7] == "stream." {
		h.streamFile(x, x.raw[1], false)
		return
	}
	h.noteUnknown(x)
	http.NotFound(x.w, x.r)
}
