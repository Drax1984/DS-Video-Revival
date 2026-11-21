package jellyfin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/vs"
	"dsvideorevival/internal/webapi"
)

type env struct {
	t   *testing.T
	ts  *httptest.Server
	h   *Handler
	s   *vs.Server
	dir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	movies := filepath.Join(dir, "filme")
	shows := filepath.Join(dir, "serien")
	kids := filepath.Join(dir, "kinder")
	mp4 := append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, make([]byte, 5000)...)
	write := func(p string) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, mp4, 0o644)
	}
	write(filepath.Join(movies, "Film A (2020)", "Film A (2020).mp4"))
	write(filepath.Join(movies, "Film B (2001).mkv"))
	_ = os.WriteFile(filepath.Join(movies, "Film B (2001).de.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nHallo\n"), 0o644)
	write(filepath.Join(shows, "Dexter", "Staffel 1", "Dexter S01E01.mkv"))
	write(filepath.Join(shows, "Dexter", "Staffel 1", "Dexter S01E02.mkv"))
	write(filepath.Join(shows, "Dexter", "Staffel 2", "Dexter S02E01.mkv"))
	write(filepath.Join(kids, "Kinderfilm (2019).mp4"))
	poster := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 100)...)
	_ = os.WriteFile(filepath.Join(movies, "Film A (2020)", "poster.jpg"), poster, 0o644)

	cfg := config.Defaults()
	cfg.AuthMode = "local"
	cfg.JellyfinEnabled = true
	cfg.Folders = []config.Folder{{Path: movies, Type: "movie"}, {Path: shows, Type: "tvshow"}, {Path: kids, Type: "movie", LibraryID: 7}}
	cfg.Libraries = []config.Library{{ID: 7, Title: "Kinderfilme", Type: "movie"}}
	hash, _ := auth.HashPassword("geheim")
	b, _ := json.Marshal([]auth.LocalUser{{Account: "anna", Hash: hash, IsManager: true}, {Account: "tom", Hash: hash}})
	uf := filepath.Join(dir, "users.json")
	_ = os.WriteFile(uf, b, 0o600)
	cfg.UsersFile = uf
	local, _ := auth.NewLocalUsers(uf)
	lib, _ := library.Open(filepath.Join(dir, "library.json"))
	if _, err := lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	// Laufzeit und Spuren wie nach einer Analyse
	items, _ := lib.Items(library.Query{})
	for _, it := range items {
		for _, f := range lib.Files(it) {
			p := &library.Probe{OK: true, Duration: 3000, Width: 1920, Height: 1080, VideoCodec: "h264", AudioCodec: "aac", Channels: 2,
				Container: "mp4", Version: library.ProbeVersion, Tool: "test",
				AudioTracks: []library.StreamTrack{{Index: 0, Codec: "aac", Channels: 2, Language: "ger"}, {Index: 1, Codec: "ac3", Channels: 6, Language: "eng"}}}
			if strings.HasSuffix(f.Path, ".mkv") {
				p.VideoCodec, p.AudioCodec = "hevc", "dts"
				p.AudioTracks = []library.StreamTrack{{Index: 0, Codec: "dts", Channels: 6, Language: "ger"}}
				p.Subtitles = []library.StreamTrack{{Index: 0, Codec: "hdmv_pgs_subtitle", Language: "ger"}}
			}
			lib.SetProbe(f.ID, p)
		}
	}
	ad := auth.NewAdapter("local", nil, local, auth.NewStore(""), nil)
	s := &vs.Server{Auth: ad, Lib: lib, Streams: stream.NewManager(), Rec: vs.NewRecorder(""), Registry: webapi.VideoStationAPIs(), Started: time.Now()}
	s.SetCfg(cfg)
	h := New(s, dir, nil)
	s.Jellyfin = h
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &env{t: t, ts: ts, h: h, s: s, dir: dir}
}

const authHdr = `MediaBrowser Client="Infuse", Device="Apple%20TV", DeviceId="dev-1", Version="8.0"`

func (e *env) do(method, path, token string, body any) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	hv := authHdr
	if token != "" {
		hv += `, Token="` + token + `"`
	}
	req.Header.Set("Authorization", hv)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (e *env) json(method, path, token string, body any) map[string]any {
	e.t.Helper()
	code, b := e.do(method, path, token, body)
	if code != 200 {
		e.t.Fatalf("%s %s: %d %s", method, path, code, b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		e.t.Fatalf("%s %s: %v (%s)", method, path, err, b)
	}
	return m
}

func (e *env) login(user string) string {
	e.t.Helper()
	m := e.json("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": user, "Pw": "geheim"})
	tok, _ := m["AccessToken"].(string)
	if len(tok) != 32 {
		e.t.Fatalf("Token: %v", m)
	}
	return tok
}

func items(m map[string]any) []map[string]any {
	var out []map[string]any
	list, _ := m["Items"].([]any)
	for _, x := range list {
		out = append(out, x.(map[string]any))
	}
	return out
}

func names(list []map[string]any) []string {
	var out []string
	for _, x := range list {
		out = append(out, x["Name"].(string))
	}
	return out
}

// requireKeys: Pflichtfelder der Jellyfin-Kotlin-SDK (sonst bricht die App
// Jellyfin für Android TV beim Einlesen ab).
func requireKeys(t *testing.T, what string, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Fatalf("%s: Pflichtfeld %s fehlt: %v", what, k, m)
		}
	}
}

func TestPublicInfoAndLogin(t *testing.T) {
	e := newEnv(t)
	m := e.json("GET", "/System/Info/Public", "", nil)
	if m["ProductName"] != ProductName || m["Version"] != ServerVersion || len(m["Id"].(string)) != 32 {
		t.Fatalf("Info: %v", m)
	}
	if code, _ := e.do("GET", "/Users/Me", "", nil); code != 401 {
		t.Fatalf("ohne Token: %d", code)
	}
	if code, _ := e.do("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": "anna", "Pw": "falsch"}); code != 401 {
		t.Fatalf("falsches Passwort: %d", code)
	}
	r := e.json("POST", "/Users/AuthenticateByName", "", map[string]string{"Username": "anna", "Pw": "geheim"})
	user := r["User"].(map[string]any)
	requireKeys(t, "UserDto", user, "Id", "Name", "Policy", "Configuration")
	requireKeys(t, "UserPolicy", user["Policy"].(map[string]any), "IsAdministrator", "IsHidden", "IsDisabled", "EnableUserPreferenceAccess",
		"EnableRemoteControlOfOtherUsers", "EnableSharedDeviceControl", "EnableRemoteAccess", "EnableLiveTvManagement", "EnableLiveTvAccess",
		"EnableMediaPlayback", "EnableAudioPlaybackTranscoding", "EnableVideoPlaybackTranscoding", "EnablePlaybackRemuxing",
		"ForceRemoteSourceTranscoding", "EnableContentDeletion", "EnableContentDownloading", "EnableSyncTranscoding", "EnableMediaConversion",
		"EnableAllDevices", "EnableAllChannels", "EnableAllFolders", "InvalidLoginAttemptCount", "LoginAttemptsBeforeLockout",
		"MaxActiveSessions", "EnablePublicSharing", "RemoteClientBitrateLimit", "AuthenticationProviderId", "PasswordResetProviderId", "SyncPlayAccess")
	requireKeys(t, "UserConfiguration", user["Configuration"].(map[string]any), "PlayDefaultAudioTrack", "DisplayMissingEpisodes", "GroupedFolders",
		"SubtitleMode", "DisplayCollectionsView", "EnableLocalPassword", "OrderedViews", "LatestItemsExcludes", "MyMediaExcludes",
		"HidePlayedInLatest", "RememberAudioSelections", "RememberSubtitleSelections", "EnableNextEpisodeAutoPlay")
	requireKeys(t, "SessionInfoDto", r["SessionInfo"].(map[string]any), "PlayableMediaTypes", "UserId", "LastActivityDate", "LastPlaybackCheckIn",
		"IsActive", "SupportsMediaControl", "SupportsRemoteControl", "HasCustomDeviceName", "SupportedCommands")
	tok := r["AccessToken"].(string)
	me := e.json("GET", "/Users/Me", tok, nil)
	if me["Id"] != userID("anna") {
		t.Fatalf("Me: %v", me)
	}
	// Verbindung erscheint unter „Verbindungen“ mit dem Gerätenamen
	found := false
	for _, c := range e.s.Conns().List() {
		if c.Account == "anna" && strings.Contains(c.Device, "Infuse") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Verbindung fehlt: %+v", e.s.Conns().List())
	}
	// Abmelden macht das Token ungültig
	if code, _ := e.do("POST", "/Sessions/Logout", tok, nil); code != 204 {
		t.Fatalf("Logout: %d", code)
	}
	if code, _ := e.do("GET", "/Users/Me", tok, nil); code != 401 {
		t.Fatalf("nach Logout: %d", code)
	}
	// Token aus der Datei wiederherstellbar (Neustart)
	tok = e.login("anna")
	e.h.store.flush()
	s2 := newStore(filepath.Join(e.dir, "jellyfin.json"))
	if _, ok := s2.lookup(tok, ""); !ok || s2.serverID() != e.h.store.serverID() {
		t.Fatal("Token nach Neustart unbekannt")
	}
}

func TestBrowseLibrary(t *testing.T) {
	e := newEnv(t)
	tok := e.login("anna")
	views := items(e.json("GET", "/UserViews?userId="+userID("anna"), tok, nil))
	if got := names(views); strings.Join(got, ",") != "Filme,Serien,Kinderfilme" {
		t.Fatalf("Bibliotheken: %v", got)
	}
	movieView, tvView := views[0]["Id"].(string), views[1]["Id"].(string)
	if views[0]["CollectionType"] != "movies" || views[1]["CollectionType"] != "tvshows" {
		t.Fatalf("CollectionType: %v", views)
	}
	movies := e.json("GET", "/Items?ParentId="+movieView+"&IncludeItemTypes=Movie&Recursive=true&SortBy=SortName&Fields=Overview", tok, nil)
	if movies["TotalRecordCount"].(float64) != 2 || strings.Join(names(items(movies)), ",") != "Film A,Film B" {
		t.Fatalf("Filme: %v", names(items(movies)))
	}
	a := items(movies)[0]
	requireKeys(t, "BaseItemDto", a, "Id", "Type", "UserData", "ImageTags", "RunTimeTicks", "ProductionYear")
	requireKeys(t, "UserItemDataDto", a["UserData"].(map[string]any), "PlaybackPositionTicks", "PlayCount", "IsFavorite", "Played", "Key", "ItemId")
	if a["Type"] != "Movie" || a["ProductionYear"].(float64) != 2020 || a["RunTimeTicks"].(float64) != 3000*ticksPerSecond {
		t.Fatalf("Film A: %v", a)
	}
	tag := a["ImageTags"].(map[string]any)["Primary"].(string)
	// Bild: mit Tag ohne Anmeldung, ohne Tag nur angemeldet
	if code, _ := e.do("GET", "/Items/"+a["Id"].(string)+"/Images/Primary?tag="+tag+"&maxHeight=300", "", nil); code != 200 {
		t.Fatalf("Bild mit Tag: %d", code)
	}
	if code, _ := e.do("GET", "/Items/"+a["Id"].(string)+"/Images/Primary", "", nil); code != 401 {
		t.Fatalf("Bild ohne Tag: %d", code)
	}
	if code, _ := e.do("GET", "/Items/"+a["Id"].(string)+"/Images/Primary?tag=falsch", "", nil); code != 401 {
		t.Fatalf("Bild mit falschem Tag: %d", code)
	}
	// Suche und A–Z-Sprung
	if got := names(items(e.json("GET", "/Items?SearchTerm=film%20b&IncludeItemTypes=Movie,Series&Recursive=true", tok, nil))); len(got) != 1 || got[0] != "Film B" {
		t.Fatalf("Suche: %v", got)
	}
	if got := names(items(e.json("GET", "/Items?ParentId="+movieView+"&NameStartsWith=f&Recursive=true&IncludeItemTypes=Movie", tok, nil))); len(got) != 2 {
		t.Fatalf("NameStartsWith: %v", got)
	}
	// Serien → Staffeln → Folgen
	series := items(e.json("GET", "/Items?ParentId="+tvView+"&IncludeItemTypes=Series&Recursive=true", tok, nil))
	if len(series) != 1 || series[0]["Name"] != "Dexter" || series[0]["ChildCount"].(float64) != 2 {
		t.Fatalf("Serien: %v", series)
	}
	sid := series[0]["Id"].(string)
	seasons := items(e.json("GET", "/Shows/"+sid+"/Seasons?userId=x", tok, nil))
	if strings.Join(names(seasons), ",") != "Staffel 1,Staffel 2" {
		t.Fatalf("Staffeln: %v", names(seasons))
	}
	eps := items(e.json("GET", "/Shows/"+sid+"/Episodes?SeasonId="+seasons[0]["Id"].(string)+"&Fields=MediaSources", tok, nil))
	if len(eps) != 2 || eps[0]["IndexNumber"].(float64) != 1 || eps[1]["IndexNumber"].(float64) != 2 || eps[0]["SeriesId"] != sid {
		t.Fatalf("Folgen: %v", eps)
	}
	// dieselben Folgen über /Items?ParentId=<Staffel>
	if got := items(e.json("GET", "/Items?ParentId="+seasons[0]["Id"].(string)+"&SortBy=SortName", tok, nil)); len(got) != 2 || got[0]["IndexNumber"].(float64) != 1 {
		t.Fatalf("Folgen über ParentId: %v", got)
	}
	// Einzelabruf mit MediaSources (Pflichtfelder der SDK)
	one := e.json("GET", "/Users/"+userID("anna")+"/Items/"+eps[0]["Id"].(string), tok, nil)
	src := one["MediaSources"].([]any)[0].(map[string]any)
	requireKeys(t, "MediaSourceInfo", src, "Protocol", "Type", "IsRemote", "ReadAtNativeFramerate", "IgnoreDts", "IgnoreIndex", "GenPtsInput",
		"SupportsTranscoding", "SupportsDirectStream", "SupportsDirectPlay", "IsInfiniteStream", "RequiresOpening", "RequiresClosing",
		"RequiresLooping", "SupportsProbing", "TranscodingSubProtocol", "HasSegments")
	for _, st := range src["MediaStreams"].([]any) {
		requireKeys(t, "MediaStream", st.(map[string]any), "IsInterlaced", "IsDefault", "IsForced", "IsHearingImpaired", "IsOriginal", "Type",
			"Index", "IsExternal", "IsTextSubtitleStream", "SupportsExternalStream")
	}
	// Vorfahren der Folge: Staffel, Serie, Bibliothek
	anc := e.do
	code, b := anc("GET", "/Items/"+eps[0]["Id"].(string)+"/Ancestors", tok, nil)
	var al []map[string]any
	_ = json.Unmarshal(b, &al)
	if code != 200 || len(al) != 3 || al[0]["Type"] != "Season" || al[1]["Type"] != "Series" || al[2]["Type"] != "CollectionFolder" {
		t.Fatalf("Ancestors: %d %s", code, b)
	}
	// Zuletzt hinzugefügt: Array
	code, b = e.do("GET", "/Items/Latest?ParentId="+movieView+"&Limit=10", tok, nil)
	var latest []map[string]any
	if code != 200 || json.Unmarshal(b, &latest) != nil || len(latest) != 2 {
		t.Fatalf("Latest: %d %s", code, b)
	}
	// Genres und Root ohne Fehler
	e.json("GET", "/Genres?ParentId="+movieView, tok, nil)
	root := e.json("GET", "/Items", tok, nil)
	if len(items(root)) != 3 {
		t.Fatalf("Root: %v", names(items(root)))
	}
}

func TestAccessRestrictions(t *testing.T) {
	e := newEnv(t)
	cfg := *e.s.Cfg()
	cfg.Folders[2].Allow = []string{"user:anna"} // Kinderfilme nur für anna
	e.s.SetCfg(cfg)
	tom := e.login("tom")
	views := names(items(e.json("GET", "/UserViews", tom, nil)))
	if strings.Join(views, ",") != "Filme,Serien" {
		t.Fatalf("tom sieht: %v", views)
	}
	all := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true", tom, nil))
	for _, it := range all {
		if it["Name"] == "Kinderfilm" {
			t.Fatal("gesperrter Titel sichtbar")
		}
	}
	anna := e.login("anna")
	if n := len(items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true", anna, nil))); n != 3 {
		t.Fatalf("anna sieht %d Filme", n)
	}
	// gesperrtes Objekt per ID: 404
	kid := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SearchTerm=kinder", anna, nil))[0]["Id"].(string)
	if code, _ := e.do("GET", "/Items/"+kid, tom, nil); code != 404 {
		t.Fatalf("gesperrtes Objekt: %d", code)
	}
	if code, _ := e.do("GET", "/Videos/"+kid+"/stream?static=true", tom, nil); code != 404 {
		t.Fatalf("gesperrter Stream: %d", code)
	}
}

func TestPlaybackAndProgress(t *testing.T) {
	e := newEnv(t)
	tok := e.login("anna")
	movies := items(e.json("GET", "/Items?IncludeItemTypes=Movie&Recursive=true&SortBy=SortName", tok, nil))
	a, b := movies[0]["Id"].(string), movies[1]["Id"].(string)
	// Profil: nur MP4/H.264/AAC direkt
	prof := map[string]any{"DeviceProfile": map[string]any{
		"DirectPlayProfiles":  []any{map[string]any{"Container": "mp4,m4v", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac"}},
		"TranscodingProfiles": []any{map[string]any{"Container": "ts", "Type": "Video", "VideoCodec": "h264", "AudioCodec": "aac", "Protocol": "hls"}},
		"SubtitleProfiles":    []any{map[string]any{"Format": "srt", "Method": "External"}},
	}}
	pa := e.json("POST", "/Items/"+a+"/PlaybackInfo?UserId=x&StartTimeTicks=0", tok, prof)
	srcA := pa["MediaSources"].([]any)[0].(map[string]any)
	if srcA["SupportsDirectPlay"] != true || pa["PlaySessionId"] == "" {
		t.Fatalf("Film A direkt: %v", pa)
	}
	// Film B (HEVC/DTS in MKV): ohne FFmpeg trotzdem direkt (besser als gar nicht)
	pb := e.json("POST", "/Items/"+b+"/PlaybackInfo", tok, prof)
	srcB := pb["MediaSources"].([]any)[0].(map[string]any)
	if srcB["SupportsDirectPlay"] != true {
		t.Fatalf("Film B ohne FFmpeg: %v", srcB)
	}
	// externe SRT erscheint als Untertitel mit Adresse
	var ext map[string]any
	for _, s := range srcB["MediaStreams"].([]any) {
		if s.(map[string]any)["IsExternal"] == true {
			ext = s.(map[string]any)
		}
	}
	if ext == nil || ext["DeliveryMethod"] != "External" {
		t.Fatalf("externer Untertitel: %v", srcB["MediaStreams"])
	}
	u, _ := url.Parse(ext["DeliveryUrl"].(string))
	code, sub := e.do("GET", u.RequestURI(), "", nil)
	if code != 200 || !strings.Contains(string(sub), "Hallo") {
		t.Fatalf("Untertitel: %d %s", code, sub)
	}
	code, sub = e.do("GET", strings.Replace(u.RequestURI(), "Stream.srt", "Stream.vtt", 1), "", nil)
	if code != 200 || !strings.HasPrefix(string(sub), "WEBVTT") || !strings.Contains(string(sub), "00:00:01.000") {
		t.Fatalf("Untertitel VTT: %d %s", code, sub)
	}
	// Direktwiedergabe mit Range über api_key
	req, _ := http.NewRequest("GET", e.ts.URL+"/Videos/"+a+"/stream.mp4?Static=true&MediaSourceId="+srcA["Id"].(string)+"&api_key="+tok, nil)
	req.Header.Set("Range", "bytes=0-11")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 206 {
		t.Fatalf("Stream: %v %v", err, resp)
	}
	resp.Body.Close()
	// Fortschritt → Fortsetzen
	if code, _ := e.do("POST", "/Sessions/Playing/Progress", tok, map[string]any{"ItemId": a, "MediaSourceId": srcA["Id"], "PositionTicks": 600 * ticksPerSecond}); code != 204 {
		t.Fatalf("Progress: %d", code)
	}
	res := items(e.json("GET", "/UserItems/Resume?MediaTypes=Video", tok, nil))
	if len(res) != 1 || res[0]["Id"] != a || res[0]["UserData"].(map[string]any)["PlaybackPositionTicks"].(float64) != 600*ticksPerSecond {
		t.Fatalf("Resume: %v", res)
	}
	// Position auch für die DS-video-Apps gespeichert
	files := e.s.Lib.Files(func() library.Item { it, _ := e.s.Lib.Item(int(mustPayload(t, a))); return it }())
	if ws, _ := e.s.Lib.Watch("anna", files[0].ID); ws.Position != 600 {
		t.Fatalf("Position: %+v", ws)
	}
	// Ende → gesehen, nicht mehr in Fortsetzen
	e.do("POST", "/Sessions/Playing/Stopped", tok, map[string]any{"ItemId": a, "PositionTicks": 2990 * ticksPerSecond})
	if res := items(e.json("GET", "/UserItems/Resume", tok, nil)); len(res) != 0 {
		t.Fatalf("Resume nach Ende: %v", res)
	}
	if ud := e.json("GET", "/Items/"+a, tok, nil)["UserData"].(map[string]any); ud["Played"] != true {
		t.Fatalf("gesehen: %v", ud)
	}
	// als nicht gesehen markieren, dann wieder gesehen
	if ud := e.json("DELETE", "/UserPlayedItems/"+a, tok, nil); ud["Played"] != false {
		t.Fatalf("ungesehen: %v", ud)
	}
	if ud := e.json("POST", "/Users/x/PlayedItems/"+a, tok, nil); ud["Played"] != true {
		t.Fatalf("gesehen markieren: %v", ud)
	}
	// Favorit
	if ud := e.json("POST", "/UserFavoriteItems/"+b, tok, nil); ud["IsFavorite"] != true {
		t.Fatalf("Favorit: %v", ud)
	}
	if favs := items(e.json("GET", "/Items?Recursive=true&IncludeItemTypes=Movie&Filters=IsFavorite", tok, nil)); len(favs) != 1 || favs[0]["Id"] != b {
		t.Fatalf("Favoriten: %v", favs)
	}
}

func mustPayload(t *testing.T, id string) uint64 {
	_, p, ok := parseID(id)
	if !ok {
		t.Fatalf("ID %s", id)
	}
	return p
}

func TestNextUp(t *testing.T) {
	e := newEnv(t)
	tok := e.login("anna")
	eps := items(e.json("GET", "/Items?IncludeItemTypes=Episode&Recursive=true&SortBy=SortName", tok, nil))
	if len(eps) != 3 {
		t.Fatalf("Folgen: %v", names(eps))
	}
	if up := items(e.json("GET", "/Shows/NextUp", tok, nil)); len(up) != 0 {
		t.Fatalf("NextUp ohne Verlauf: %v", up)
	}
	e.json("POST", "/UserPlayedItems/"+eps[0]["Id"].(string), tok, nil)
	up := items(e.json("GET", "/Shows/NextUp?Limit=5", tok, nil))
	if len(up) != 1 || up[0]["Id"] != eps[1]["Id"] {
		t.Fatalf("NextUp: %v", up)
	}
	// Serie als gesehen markieren → alle Folgen
	sid := eps[0]["SeriesId"].(string)
	if ud := e.json("POST", "/UserPlayedItems/"+sid, tok, nil); ud["Played"] != true || ud["UnplayedItemCount"].(float64) != 0 {
		t.Fatalf("Serie gesehen: %v", ud)
	}
}

func TestQuickConnect(t *testing.T) {
	e := newEnv(t)
	init := e.json("POST", "/QuickConnect/Initiate", "", nil)
	requireKeys(t, "QuickConnectResult", init, "Authenticated", "Secret", "Code", "DeviceId", "DeviceName", "AppName", "AppVersion", "DateAdded")
	secret, code := init["Secret"].(string), init["Code"].(string)
	if c, _ := e.do("POST", "/Users/AuthenticateWithQuickConnect", "", map[string]string{"Secret": secret}); c != 401 {
		t.Fatalf("unbestätigt: %d", c)
	}
	if err := e.h.QuickConnect("000000x", auth.Session{Account: "tom"}); err == nil {
		t.Fatal("falscher Code angenommen")
	}
	if err := e.h.QuickConnect(code, auth.Session{Account: "tom"}); err != nil {
		t.Fatal(err)
	}
	if st := e.json("GET", "/QuickConnect/Connect?Secret="+secret, "", nil); st["Authenticated"] != true {
		t.Fatalf("Connect: %v", st)
	}
	r := e.json("POST", "/Users/AuthenticateWithQuickConnect", "", map[string]string{"Secret": secret})
	if r["User"].(map[string]any)["Name"] != "tom" || r["AccessToken"] == "" {
		t.Fatalf("Quick Connect: %v", r)
	}
	// Secret nur einmal verwendbar
	if c, _ := e.do("POST", "/Users/AuthenticateWithQuickConnect", "", map[string]string{"Secret": secret}); c != 401 {
		t.Fatalf("zweite Verwendung: %d", c)
	}
	// Gerät in der Liste, entfernbar
	devs := e.h.Devices("tom").([]DeviceView)
	if len(devs) != 1 || !strings.Contains(devs[0].Label, "Infuse") {
		t.Fatalf("Geräte: %+v", devs)
	}
	if e.h.RemoveDevice(devs[0].ID, "anna") {
		t.Fatal("fremdes Gerät entfernt")
	}
	if !e.h.RemoveDevice(devs[0].ID, "tom") {
		t.Fatal("eigenes Gerät nicht entfernt")
	}
}

func TestKickRevokesDevice(t *testing.T) {
	e := newEnv(t)
	tok := e.login("tom")
	key := ""
	for _, c := range e.s.Conns().List() {
		if c.Account == "tom" {
			key = c.ID
		}
	}
	if !strings.HasPrefix(key, vs.JellyfinKeyPrefix) {
		t.Fatalf("Schlüssel: %q", key)
	}
	e.s.Conns().Kick(key, 0)
	e.s.RevokeKey(key)
	if c, _ := e.do("GET", "/Users/Me", tok, nil); c != 401 {
		t.Fatalf("nach Trennen: %d", c)
	}
}

func TestDisabledAndPrefix(t *testing.T) {
	e := newEnv(t)
	// über den Server von DS Video Revival unter /jellyfin
	srv := httptest.NewServer(e.s)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/jellyfin/System/Info/Public")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("/jellyfin: %v %v", err, resp)
	}
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if !strings.HasSuffix(m["LocalAddress"].(string), "/jellyfin") {
		t.Fatalf("LocalAddress: %v", m["LocalAddress"])
	}
	cfg := *e.s.Cfg()
	cfg.JellyfinEnabled = false
	e.s.SetCfg(cfg)
	if c, _ := e.do("GET", "/System/Info/Public", "", nil); c != 404 {
		t.Fatalf("ausgeschaltet: %d", c)
	}
}

func TestDeviceProfile(t *testing.T) {
	p := &deviceProfile{DirectPlayProfiles: []codecProfile{{Container: "mkv,mp4", Type: "Video", VideoCodec: "h264,hevc", AudioCodec: "aac,ac3,dca"}}}
	cases := []struct {
		c, v, a string
		want    bool
	}{{"mkv", "hevc", "dts", true}, {"mp4", "h264", "aac", true}, {"avi", "h264", "aac", false}, {"mkv", "av1", "aac", false}, {"mkv", "h264", "truehd", false}}
	for _, c := range cases {
		if got := p.canDirectPlay(c.c, c.v, c.a); got != c.want {
			t.Errorf("%v: %v", c, got)
		}
	}
	var nilP *deviceProfile
	if !nilP.canDirectPlay("avi", "mpeg4", "mp3") {
		t.Error("ohne Profil direkt")
	}
}

func TestParseAuth(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", `MediaBrowser Client="Jellyfin Android TV", Device="SHIELD%20Android%20TV", DeviceId="abc,def", Version="0.18.0", Token="tok"`)
	a := parseAuth(r, map[string][]string{})
	if a.Client != "Jellyfin Android TV" || a.Device != "SHIELD Android TV" || a.DeviceID != "abc,def" || a.Token != "tok" {
		t.Fatalf("%+v", a)
	}
	r = httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-Emby-Token", "t2")
	if parseAuth(r, nil).Token != "t2" {
		t.Fatal("X-Emby-Token")
	}
	if parseAuth(httptest.NewRequest("GET", "/x", nil), map[string][]string{"api_key": {"t3"}}).Token != "t3" {
		t.Fatal("api_key")
	}
}

func TestIDs(t *testing.T) {
	id := makeID(kItem, 42)
	if len(id) != 32 {
		t.Fatal(id)
	}
	k, p, ok := parseID(strings.ToUpper(id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]))
	if !ok || k != kItem || p != 42 {
		t.Fatalf("%v %v %v", k, p, ok)
	}
	v := viewID("tvshow", 7)
	k, p, _ = parseID(v)
	typ, lib, ok := parseViewPayload(p)
	if k != kView || typ != "tvshow" || lib != 7 || !ok {
		t.Fatal(typ, lib)
	}
	k, p, _ = parseID(seasonID(12, 3))
	if k != kSeason || p>>16 != 12 || uint16(p) != 3 {
		t.Fatal(p)
	}
	if k, _, ok := parseID(nameID(kGenre, "Drama")); !ok || k != kGenre {
		t.Fatal("Genre-ID")
	}
}

// TestWebClient: Mediathek für die App „Jellyfin“ unter /web/ (auch über /jellyfin).
func TestWebClient(t *testing.T) {
	e := newEnv(t)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for path, want := range map[string]string{"/": "/web/", "/web": "/web/", "/index.html": "/web/"} {
		resp, err := noRedirect.Get(e.ts.URL + path)
		if err != nil || resp.StatusCode != 302 || resp.Header.Get("Location") != want {
			t.Fatalf("%s: %v %v", path, err, resp)
		}
		resp.Body.Close()
	}
	for _, f := range []string{"/web/", "/web/index.html", "/web/main.dsvr.bundle.js", "/web/main.dsvr.bundle.js?deferred=true&ts=1", "/web/app.js", "/web/style.css", "/web/hls.light.min.js", "/web/logo.webp"} {
		resp, err := http.Get(e.ts.URL + f)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", f, err, resp)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Die App „Jellyfin“ für Android gilt als verbunden, sobald die Seite ein
		// Skript lädt, dessen Pfad auf ihr Muster für das Hauptskript der
		// Jellyfin-Weboberfläche passt (Constants.MAIN_BUNDLE_PATH_REGEX) –
		// sonst bricht sie nach 10 s ab.
		if f == "/web/" {
			m := regexp.MustCompile(`<script src="([^"]+)"`).FindAllStringSubmatch(string(b), -1)
			bundle := regexp.MustCompile(`.*/main\.[^/\s]+\.bundle\.js$`)
			ok := false
			for _, x := range m {
				ok = ok || bundle.MatchString("/web/"+x[1])
			}
			if !ok {
				t.Fatalf("index ohne main.*.bundle.js: %s", b)
			}
		}
	}
	srv := httptest.NewServer(e.s)
	defer srv.Close()
	resp, err := noRedirect.Get(srv.URL + "/jellyfin")
	if err != nil || resp.Header.Get("Location") != "/jellyfin/web/" {
		t.Fatalf("/jellyfin: %v %v", err, resp)
	}
	resp.Body.Close()
	if !isApple("Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15") || isApple("Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/130") {
		t.Fatal("isApple")
	}
}

// TestExoPlayerProfile: Der Player der App „Jellyfin“ für Android (ExoPlayer)
// schickt sein Geräteprofil (Aufbau wie DeviceProfileBuilder der App) und
// spielt MKV mit HEVC und DTS direkt aus der Datei – wie die DS-video-Apps.
func TestExoPlayerProfile(t *testing.T) {
	e := newEnv(t)
	tok := e.login("anna")
	b := items(e.json("GET", "/Items?SearchTerm=film%20b&IncludeItemTypes=Movie&Recursive=true", tok, nil))
	if len(b) != 1 {
		t.Fatalf("Film B: %v", names(b))
	}
	vc := "mpeg1video,mpeg2video,h263,mpeg4,h264,hevc,av1,vp8,vp9"
	ac := "pcm_s16le,mp1,mp2,mp3,aac,vorbis,opus,flac,alac,ac3,eac3,dts,mlp,truehd"
	prof := map[string]any{"DeviceProfile": map[string]any{
		"Name": "ExoPlayer", "MaxStreamingBitrate": 120000000, "MaxStaticBitrate": 100000000,
		"DirectPlayProfiles": []any{
			map[string]any{"Type": "Video", "Container": "mp4", "VideoCodec": "h264,hevc,av1,vp9", "AudioCodec": "mp3,aac,alac,ac3,opus"},
			map[string]any{"Type": "Video", "Container": "mkv", "VideoCodec": vc, "AudioCodec": ac},
		},
		"CodecProfiles": []any{map[string]any{"Type": "Video", "Container": "mkv", "Codec": "hevc", "Conditions": []any{
			map[string]any{"Condition": "EqualsAny", "Property": "VideoProfile", "Value": "Main|Main 10|Main 10 HDR 10", "IsRequired": false}}}},
		"TranscodingProfiles": []any{map[string]any{"Type": "Video", "Container": "ts", "VideoCodec": "h264", "AudioCodec": "aac,ac3,eac3,dts", "Protocol": "hls"}},
	}, "MaxStreamingBitrate": 120000000, "StartTimeTicks": 0, "AutoOpenLiveStream": true}
	pi := e.json("POST", "/Items/"+b[0]["Id"].(string)+"/PlaybackInfo", tok, prof)
	if pi["PlaySessionId"] == nil || pi["PlaySessionId"] == "" {
		t.Fatalf("PlaySessionId fehlt: %v", pi)
	}
	src := pi["MediaSources"].([]any)[0].(map[string]any)
	if src["SupportsDirectPlay"] != true || src["Protocol"] != "File" {
		t.Fatalf("Direktwiedergabe erwartet: %v", src)
	}
	// So baut die App die Adresse (videosApi.getVideoStreamUrl, static=true)
	req, _ := http.NewRequest("GET", e.ts.URL+"/Videos/"+b[0]["Id"].(string)+"/stream?static=true&mediaSourceId="+src["Id"].(string)+"&api_key="+tok, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusPartialContent || resp.ContentLength != 100 {
		t.Fatalf("Datei mit Range: %v %v", err, resp)
	}
	resp.Body.Close()
}
