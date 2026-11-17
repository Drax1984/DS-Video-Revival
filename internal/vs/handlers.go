package vs

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dsvideorevival/internal/library"
	"dsvideorevival/internal/webapi"
)

// implTable: API → Methode → Implementierung mit Evidenzstatus.
// Quellen der Belege: docs/protocol.md.
var implTable map[string]map[string]impl

func init() {
	implTable = map[string]map[string]impl{
		"SYNO.VideoStation2.Info": {"get": {infoV2, StBelegt, "Felder laut App-Modell VideoStationInfoVo2 (inkl. ame_status)"}},
		"SYNO.VideoStation.Info":  {"getinfo": {infoV1, StBelegt, "Felder laut Schema SYNO.VideoStation.Info-getinfo"}},

		"SYNO.VideoStation2.Library": {"list": {libraryList("library"), StBelegt, "Schlüssel 'library' laut Schema"}},
		"SYNO.VideoStation.Library":  {"list": {libraryList("libraries"), StBelegt, "Schlüssel 'libraries' laut Schema"}},

		"SYNO.VideoStation2.Movie": {
			"list":    {movieList("movie"), StBelegt, "App-Modell VideoV2Vo/Video2ListVo"},
			"getinfo": {itemGet("movie", "movie"), StBelegt, "App: FetchVideoInfoTask → Video2ListVo.movie"},
		},
		"SYNO.VideoStation.Movie": {
			"list":    {movieList("movies"), StBelegt, "App-Modell VideoListV2Vo.movies"},
			"search":  {movieList("movies"), StTeilbelegt, "Parameter 'title'/'keyword'"},
			"getinfo": {itemGet("movies", "movie"), StBelegt, "App: VideoListV2Vo.movies"},
		},
		"SYNO.VideoStation2.TVShow": {
			"list":    {showList("tvshow"), StBelegt, "App-Modell Video2ListVo.tvshow"},
			"getinfo": {showGet("tvshow"), StBelegt, "App: getinfo mit summary/backdrop_mtime/poster_mtime"},
		},
		"SYNO.VideoStation.TVShow": {"list": {showList("tvshows"), StBelegt, "App-Modell VideoListV2Vo.tvshows"}},
		"SYNO.VideoStation2.TVShowEpisode": {
			"list":    {episodeList("episode"), StBelegt, "App-Modell Video2ListVo.episode"},
			"getinfo": {itemGet("episode", "episode"), StBelegt, "App-Modell Video2ListVo.episode"},
		},
		"SYNO.VideoStation.TVShowEpisode": {
			"list":    {episodeList("episodes"), StBelegt, "App-Modell VideoListV2Vo.episodes; Parameter tvshow_id"},
			"getinfo": {itemGet("episodes", "episode"), StBelegt, "App-Modell VideoListV2Vo.episodes"},
		},
		"SYNO.VideoStation2.HomeVideo": {
			"list":    {homeList("video"), StBelegt, "App-Modell Video2ListVo.video"},
			"getinfo": {itemGet("video", "home_video"), StBelegt, "App-Modell Video2ListVo.video"},
		},
		"SYNO.VideoStation.HomeVideo": {"list": {homeList("videos"), StBelegt, "App-Modell VideoListV2Vo.videos"}},
		"SYNO.VideoStation2.TVRecording": {
			"list":         {recordingList("recording"), StTeilbelegt, "Ordnertyp TV-Aufnahmen; Filter channel_name/title laut App"},
			"getinfo":      {itemGet("recording", "tv_record"), StTeilbelegt, "wie Heimvideo, dazu channel_name"},
			"list_channel": {recordingGroups("channel", true), StTeilbelegt, "App-Modell RecordingGroupVo.channel"},
			"list_program": {recordingGroups("program", false), StTeilbelegt, "App-Modell RecordingGroupVo.program"},
		},
		"SYNO.VideoStation.TVRecording": {
			"list":    {recordingList("recordings"), StTeilbelegt, "Ordnertyp TV-Aufnahmen"},
			"getinfo": {itemGet("recordings", "tv_record"), StTeilbelegt, "wie Heimvideo, dazu channel_name"},
		},
		"SYNO.VideoStation2.Collection":        {"list": {emptyList("collection"), StBelegt, "leer – Sammlungen/Favoriten folgen"}},
		"SYNO.VideoStation.Collection":         {"list": {emptyList("collections"), StBelegt, "leer – Sammlungen/Favoriten folgen"}},
		"SYNO.VideoStation2.OfflineConversion": offlineMethods(),
		"SYNO.VideoStation.OfflineConversion":  offlineMethods(),
		"SYNO.VideoStation2.Folder":            {"list": {folderListV2, StTeilbelegt, "Ordnerobjekte laut Schema (object[], path, sharepath); Dateiobjekte wie VS1"}},
		"SYNO.VideoStation.Folder":             {"list": {folderListV1, StTeilbelegt, "App-Modell FolderContentVo (objects, offset, total); Ordner-IDs eigene Kodierung"}},
		"SYNO.VideoStation2.Controller.Device": {"list": {func(c *Ctx) { webapi.OK(c.W, map[string]any{"device": []any{}}) }, StBelegt, "leer – Renderer (DLNA/AirPlay) nicht unterstützt"}},
		"SYNO.VideoStation2.Setting.Personal":  {"get": {func(c *Ctx) { webapi.OK(c.W, map[string]any{"convert_simplified_to_traditional": false}) }, StBelegt, "Schema SYNO.VideoStation2.Setting.Personal-get"}},

		"SYNO.VideoStation2.Poster":   {"get": {posterGet, StTeilbelegt, "id/type; liefert Bilddatei"}},
		"SYNO.VideoStation.Poster":    {"getimage": {posterGet, StBelegt, "App: getimage&id=&type=&poster_mtime="}},
		"SYNO.VideoStation2.Backdrop": {"get": {backdropGet, StTeilbelegt, "mapper_id/id; liefert Bilddatei"}},
		"SYNO.VideoStation.Backdrop":  {"get": {backdropGet, StBelegt, "App: get&mapper_id="}},

		"SYNO.VideoStation2.Streaming": {
			"open":   {streamOpen, StBelegt, "App OpenStreamTask2: raw | hls_remux | webm_remux | hls(profile, audio_track) + file={\"id\":N}"},
			"stream": {streamStream, StBelegt, "Datei mit Range bzw. HLS-Playlist"},
			"close":  {streamClose, StBelegt, "App CloseStreamTask"},
		},
		"SYNO.VideoStation.Streaming": {
			"open":   {streamOpen, StTeilbelegt, "wie VS2"},
			"stream": {streamStream, StBelegt, "App: …?api=SYNO.VideoStation.Streaming&version=1&method=stream&id=&format="},
			"close":  {streamClose, StBelegt, "App CloseStreamTask (id, format)"},
		},
		"SYNO.VideoStation.WatchStatus": {
			"setinfo": {watchSet, StBelegt, "App SetWatchStatusTask: id (Datei), position"},
			"getinfo": {watchGet, StBelegt, "App WatchStatusVo: watch_status{position:String,last_update}"},
		},
		"SYNO.VideoStation.AudioTrack": {"list": {audioTrackList, StBelegt, "App AudioTrackVo: trackinfo[{id,codec,channel,language,track}]"}},
		// Freigabe-Links gibt es hier nicht; die App erwartet aber eine gültige
		// Antwort (VideoShareDataVo: enable, url) – ein Fehler bricht in der
		// iPhone-App die weitere Abfragekette der Detailansicht ab.
		// Android-App fragt beim Start die TV-Tuner ab (MainActivity.getDTVStatus);
		// fehlt die API in SYNO.API.Info, meldet die App den Benutzer ab
		// (UnsupportApiException → showLoginPage). Keine Tuner = kein DTV-Menü.
		"SYNO.DTV.Tuner": {"list": {func(c *Ctx) {
			webapi.OK(c.W, map[string]any{"tuners": []any{}, "total": 0, "offset": 0})
		}, StBelegt, "App TunerListVo: data.tuners, total"}},
		// Wiedergabegeräte (Cast/DLNA über die NAS): keine – Chromecast findet die App selbst.
		"SYNO.VideoController.Device": {"list": {func(c *Ctx) {
			webapi.OK(c.W, map[string]any{"devices": []any{}})
		}, StBelegt, "App DeviceListVo: data.devices"}},
		"SYNO.VideoStation.Sharing": {"get": {func(c *Ctx) { webapi.OK(c.W, map[string]any{"enable": false, "url": ""}) }, StBelegt, "App VideoShareDataVo: enable, url"}},
		"SYNO.VideoStation.Subtitle": {
			"list": {subtitleList, StBelegt, "App SubtitleV2Vo: data=[{id,title,lang,format,embedded,need_preview}]"},
			"get":  {subtitleGet, StTeilbelegt, "liefert SRT-Text (id, subtitle_id)"},
		},
		"SYNO.VideoStation.Video": {
			"download":       {fileDownload, StTeilbelegt, "Originaldatei (Range-fähig)"},
			"get_track_info": {trackInfo, StTeilbelegt, "video[].profile belegt (TV-App), übrige Felder Hypothese"},
		},
		"SYNO.VideoStation2.File": {
			"download":             {fileDownload, StTeilbelegt, "Originaldatei (Range-fähig); URL laut Android-App"},
			"get_track_info":       {trackInfo, StTeilbelegt, "video[].profile belegt (TV-App), übrige Felder Hypothese"},
			"set_watchstatus":      {watchSet, StTeilbelegt, "id, position"},
			"get_playback_setting": {playbackGet, StBelegt, "App PlaybackSettingVo: audio_track, subtitle_id"},
			"set_playback_setting": {func(c *Ctx) { webapi.OK(c.W, nil) }, StTeilbelegt, "wird nicht gespeichert"},
		},
		"SYNO.VideoStation2.Misc": {"reset_timeout": {func(c *Ctx) { webapi.OK(c.W, nil) }, StHypothese, "leere Erfolgsantwort angenommen"}},
		"SYNO.VideoStation.Misc":  {"reset_timeout": {func(c *Ctx) { webapi.OK(c.W, nil) }, StHypothese, "leere Erfolgsantwort angenommen"}},
	}
}

// ---------- Info ----------

func (s *Server) versionParts() (string, string) {
	v := s.Cfg().ReportVersion
	build := v
	if i := strings.LastIndex(v, "-"); i >= 0 {
		build = v[i+1:]
	}
	return build, v
}

func (s *Server) tz() (string, int) {
	loc, err := time.LoadLocation(s.Cfg().Timezone)
	if err != nil {
		loc = time.Local
	}
	_, off := time.Now().In(loc).Zone()
	return s.Cfg().Timezone, off
}

func isManager(c *Ctx) bool { return c.S.IsAppAdmin(c.Sess) }

func infoV2(c *Ctx) {
	build, vs := c.S.versionParts()
	tzName, off := c.S.tz()
	webapi.OK(c.W, map[string]any{
		"is_dtv_enabled":             false,
		"is_manager":                 isManager(c),
		"is_subtitle_search_enabled": false,
		"privilege":                  map[string]bool{"dtv": false, "offline_conversion": offlineAvailable(c), "renderer": false, "sharing": false},
		"timezone":                   tzName,
		"timezone_offset":            off,
		"transcoding_capability": map[string]bool{
			"dtv_transcode": false, "fhd_hardware_transcode": false, "hardware_transcode": false,
			// transcode=true: Die App verlangt unterwegs (nicht im Heimnetz) bei
			// „Automatisch“ eine niedrigere Qualität und startet ohne diese
			// Fähigkeit nicht. Ohne H.264-Encoder liefert das Paket dann die
			// Originalqualität (nur umverpackt) – siehe streamOpen.
			"remux": true, "software_transcode": true, "transcode": true,
		},
		"version":        build,
		"version_string": vs,
		// App-Modell VideoStationInfoVo2 (Kotlin, nicht-null): AME-Status melden,
		// sonst prüft die App fehlende Codecs gegen null.
		"ame_status": map[string]any{"ame_major_version": 3, "has_aac": true, "has_hevc": true, "has_license": true,
			"is_aac_activated": true, "is_ame_broken": false, "is_ame_install": true},
		"need_aac":  false,
		"need_hevc": false,
	})
}

func infoV1(c *Ctx) {
	build, vs := c.S.versionParts()
	tzName, off := c.S.tz()
	webapi.OK(c.W, map[string]any{
		"is_dtv_enabled":                 false,
		"is_manager":                     isManager(c),
		"is_subtitle_search_enabled":     false,
		"privilege":                      map[string]bool{"dtv": false, "offline_conversion": offlineAvailable(c), "renderer": false, "sharing": false},
		"support_appletv_os":             true,
		"support_dtv_transcode":          false,
		"support_fhd_hardware_transcode": false,
		"support_hardware_transcode":     false,
		"support_remux":                  true,
		"support_software_transcode":     false,
		"support_transcode":              false,
		"timezone":                       tzName,
		"timezone_offset":                off,
		"version":                        build,
		"version_string":                 vs,
	})
}

// ---------- Bibliotheken ----------

func libraryList(key string) handlerFunc {
	return func(c *Ctx) {
		// Standardbibliotheken (id 0) wie in der Community-Doku einer Original-Antwort.
		libs := []map[string]any{
			{"id": 0, "is_public": true, "title": "Movie", "type": "movie", "visible": true},
			{"id": 0, "is_public": true, "title": "TVShow", "type": "tvshow", "visible": true},
			{"id": 0, "is_public": true, "title": "Home Video", "type": "home_video", "visible": true},
			{"id": 0, "is_public": true, "title": "TV Recording", "type": "tv_record", "visible": true},
		}
		for _, l := range c.S.Cfg().Libraries {
			libs = append(libs, map[string]any{"id": l.ID, "is_public": l.IsPublic, "title": l.Title, "type": l.Type, "visible": true})
		}
		off, lim := c.Req.Int("offset", 0), c.Req.Int("limit", 0)
		total := len(libs)
		if off > len(libs) {
			off = len(libs)
		}
		libs = libs[off:]
		if lim > 0 && lim < len(libs) {
			libs = libs[:lim]
		}
		webapi.OK(c.W, map[string]any{key: libs, "offset": off, "total": total})
	}
}

func libraryParam(c *Ctx) *int {
	if !c.Req.Has("library_id") {
		return nil
	}
	n := c.Req.Int("library_id", 0)
	return &n
}

func listQuery(c *Ctx, kind string) library.Query {
	q := library.Query{Kind: kind, LibraryID: libraryParam(c), Offset: c.Req.Int("offset", 0), Limit: c.Req.Int("limit", 0),
		SortBy: map[string]string{"date": "year", "year": "year", "added": "added", "title": "title", "": ""}[c.Req.Str("sort_by")],
		Desc:   strings.EqualFold(c.Req.Str("sort_direction"), "desc"), Title: titleFilter(c)}
	if recentlyWatched(c) {
		q.SortBy, q.WatchedBy = "watched", c.Sess.Account
		if !c.Req.Has("sort_direction") {
			q.Desc = true // neueste zuerst
		}
	}
	if q.Limit < 0 {
		q.Limit = 0
	}
	return q
}

// recentlyWatched: Ansicht „Zuletzt gesehen“ – VS2: sort_by=watched (App
// FetchVideoListV2Task), VS1: recently_watched=1 und sort_by=last_watched
// (FetchVideoListV1Task). Nur angesehene Titel, neueste zuerst.
func recentlyWatched(c *Ctx) bool {
	sb := c.Req.Str("sort_by")
	return sb == "watched" || sb == "last_watched" || c.Req.Str("recently_watched") == "1" || strings.EqualFold(c.Req.Str("recently_watched"), "true")
}

// titleFilter: Titelsuche. VS1 v1 schickt einen Text, die Android-App bei
// list v2 JSON-Listen (FilterData.createVersion2FilterParams: title=[] ohne
// Filter, ["x"] mit), VS2 zusätzlich keyword. "[]" heißt: kein Filter.
func titleFilter(c *Ctx) string {
	for _, k := range []string{"title", "keyword"} {
		raw := strings.TrimSpace(c.Req.Params.Get(k))
		if strings.HasPrefix(raw, "[") {
			for _, v := range c.Req.StrList(k) {
				if v = strings.TrimSpace(v); v != "" {
					return v
				}
			}
			continue
		}
		if v := strings.TrimSpace(c.Req.Str(k)); v != "" {
			return v
		}
	}
	return ""
}

// ---------- Bilder ----------

func posterGet(c *Ctx) { serveImage(c, false) }

func backdropGet(c *Ctx) { serveImage(c, true) }

func serveImage(c *Ctx, backdrop bool) {
	// Poster: id + type (movie|tvshow|tvshow_episode|home_video|tv_record);
	// Backdrop: mapper_id (App ConnectionManager). IDs sind über Objekte eindeutig.
	id := c.Req.Int("id", c.Req.Int("mapper_id", -1))
	var path string
	if it, ok := c.Lib.Item(id); ok && c.Req.Str("type") != "tvshow" {
		if backdrop {
			if path = findBackdrop(c, it); path == "" {
				path = it.BackdropPath()
			}
		} else {
			path = it.PosterPath()
			if path == "" && it.Kind == "episode" {
				if path = it.BackdropPath(); path == "" {
					if sh, ok := c.Lib.Show(it.ShowID); ok {
						path = showImage(sh, false)
					}
				}
			}
		}
	} else if sh, ok := c.Lib.Show(id); ok {
		path = showImage(sh, backdrop)
	}
	if path == "" {
		c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, "kein Bild vorhanden (Parameter prüfen)", paramKeys(c.Req.Params))
	}
	if path == "" {
		http.NotFound(c.W, c.Req.HTTP)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(c.W, c.Req.HTTP)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	ct := "image/jpeg"
	if strings.HasSuffix(strings.ToLower(path), ".png") {
		ct = "image/png"
	}
	c.W.Header().Set("Content-Type", ct)
	http.ServeContent(c.W, c.Req.HTTP, "", st.ModTime(), f)
}

func findBackdrop(c *Ctx, it library.Item) string {
	for _, f := range c.Lib.Files(it) {
		dir := filepath.Dir(f.Path)
		base := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
		for _, n := range []string{base + "-fanart.jpg", "fanart.jpg", "backdrop.jpg", base + "-backdrop.jpg"} {
			p := filepath.Join(dir, n)
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				return p
			}
		}
	}
	return ""
}
