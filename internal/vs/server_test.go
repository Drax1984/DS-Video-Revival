package vs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/webapi"
)

// fakeDSM simuliert die DSM-WebAPI-Teile, die DS Video Revival nutzt.
type fakeDSM struct {
	lastPath string
	mu       sync.Mutex
	valid    map[string]bool
	calls    []string
	lastXFF  string
	// denySession: Sitzungsnamen, die DSM mit 402 ablehnt (normale Benutzer
	// bei unbekannten Namen wie „DSVideoRevival“).
	denySession map[string]bool
}

func (f *fakeDSM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	api, method := r.Form.Get("api"), r.Form.Get("method")
	f.mu.Lock()
	f.calls = append(f.calls, api+"."+method)
	f.lastXFF = r.Header.Get("X-Forwarded-For")
	f.lastPath = r.URL.Path
	f.mu.Unlock()
	w.Header().Set("Content-Type", `application/json; charset="UTF-8"`)
	switch api {
	case "SYNO.API.Info":
		io.WriteString(w, `{"data":{"SYNO.API.Auth":{"maxVersion":7,"minVersion":1,"path":"entry.cgi"},"SYNO.API.Info":{"maxVersion":1,"minVersion":1,"path":"entry.cgi","requestFormat":"JSON"},"SYNO.Core.Foo":{"maxVersion":1,"minVersion":1,"path":"entry.cgi"}},"success":true}`)
	case "SYNO.API.Auth":
		switch method {
		case "login":
			f.mu.Lock()
			deny := f.denySession[r.Form.Get("session")]
			f.mu.Unlock()
			if deny {
				io.WriteString(w, `{"error":{"code":402},"success":false}`)
				return
			}
			if r.Form.Get("account") == "anna" && r.Form.Get("passwd") == "geheim" {
				f.mu.Lock()
				f.valid["DSMSID123"] = true
				f.mu.Unlock()
				if r.Form.Get("format") != "sid" {
					http.SetCookie(w, &http.Cookie{Name: "id", Value: "DSMSID123", Path: "/"})
				}
				io.WriteString(w, `{"data":{"did":"x","is_portal_port":false,"sid":"DSMSID123"},"success":true}`)
				return
			}
			io.WriteString(w, `{"error":{"code":400},"success":false}`)
		case "logout":
			f.mu.Lock()
			delete(f.valid, r.Form.Get("_sid"))
			f.mu.Unlock()
			io.WriteString(w, `{"success":true}`)
		}
	case "SYNO.FileStation.Info":
		f.mu.Lock()
		ok := f.valid[r.Form.Get("_sid")]
		f.mu.Unlock()
		if ok {
			io.WriteString(w, `{"data":{"is_manager":true,"hostname":"DS"},"success":true}`)
			return
		}
		io.WriteString(w, `{"error":{"code":119},"success":false}`)
	case "SYNO.Core.Desktop.Initdata":
		f.mu.Lock()
		ok := f.valid[r.Form.Get("_sid")]
		f.mu.Unlock()
		if ok {
			io.WriteString(w, `{"data":{"Session":{"user":"anna","is_admin":true},"AppPrivilege":{"SYNO.ALLOW.ALL.APPLICATIONS":true}},"success":true}`)
			return
		}
		io.WriteString(w, `{"error":{"code":119},"success":false}`)
	default:
		io.WriteString(w, `{"data":{"echo":"`+api+`"},"success":true}`)
	}
}

func newTestServer(t *testing.T, mode string) (*httptest.Server, *fakeDSM, string) {
	t.Helper()
	dir := t.TempDir()
	media := filepath.Join(dir, "media")
	_ = os.MkdirAll(filepath.Join(media, "Film A (2020)"), 0o755)
	mp4 := append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, make([]byte, 5000)...)
	_ = os.WriteFile(filepath.Join(media, "Film A (2020)", "Film A (2020).mp4"), mp4, 0o644)
	_ = os.WriteFile(filepath.Join(media, "Film B (2001).mkv"), []byte{0x1a, 0x45, 0xdf, 0xa3, 1, 2, 3}, 0o644)

	cfg := config.Defaults()
	cfg.AuthMode = mode
	rec := filepath.Join(dir, "aufnahmen")
	_ = os.MkdirAll(rec, 0o755)
	for _, n := range []string{"20231105 2015 - Das Erste HD - Tatort.ts", "20231106 2015 - ZDF HD - Tatort.ts", "20231107 2200 - ZDF HD - heute-show.ts"} {
		_ = os.WriteFile(filepath.Join(rec, n), mp4, 0o644)
	}
	cfg.Folders = []config.Folder{{Path: media, Type: "movie"}, {Path: rec, Type: "tv_record"}}
	fake := &fakeDSM{valid: map[string]bool{}}
	var up *upstream.Client
	var local *auth.LocalUsers
	if mode == "dsm" {
		dsm := httptest.NewServer(fake)
		t.Cleanup(dsm.Close)
		cfg.Upstream = dsm.URL
		up, _ = upstream.New(dsm.URL)
	} else {
		h, _ := auth.HashPassword("geheim")
		b, _ := json.Marshal([]auth.LocalUser{{Account: "anna", Hash: h, IsManager: true}})
		uf := filepath.Join(dir, "users.json")
		_ = os.WriteFile(uf, b, 0o600)
		cfg.UsersFile = uf
		local, _ = auth.NewLocalUsers(uf)
	}
	lib, _ := library.Open(filepath.Join(dir, "library.json"))
	if _, err := lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	ad := auth.NewAdapter(mode, up, local, auth.NewStore(filepath.Join(dir, "sessions.json")), nil)
	ad.RevalidateAfter = 0 // jede Anfrage gegen DSM prüfen (Test)
	s := &Server{Auth: ad, Up: up, Lib: lib, Streams: stream.NewManager(), Rec: NewRecorder(""), Registry: webapi.VideoStationAPIs(), Started: time.Now()}
	s.SetCfg(cfg)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts, fake, dir
}

func call(t *testing.T, base string, v url.Values) map[string]any {
	t.Helper()
	resp, err := http.PostForm(base+"/webapi/entry.cgi", v)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func errCode(m map[string]any) int {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(float64)
	return int(c)
}

func login(t *testing.T, base string) string {
	m := call(t, base, url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"login"}, "account": {"anna"}, "passwd": {"geheim"}, "session": {"VideoStation"}, "format": {"sid"}})
	d, _ := m["data"].(map[string]any)
	sid, _ := d["sid"].(string)
	if sid == "" {
		t.Fatalf("login fehlgeschlagen: %v", m)
	}
	return sid
}

func TestDSMModeFlow(t *testing.T) {
	ts, fake, _ := newTestServer(t, "dsm")

	// query.cgi: DSM-APIs + Video-Station-APIs zusammengeführt
	resp, _ := http.Get(ts.URL + "/webapi/query.cgi?api=SYNO.API.Info&version=1&method=query&query=all")
	var q map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&q)
	resp.Body.Close()
	d := q["data"].(map[string]any)
	for _, k := range []string{"SYNO.Core.Foo", "SYNO.API.Auth", "SYNO.VideoStation2.Streaming", "SYNO.VideoStation.Info"} {
		if _, ok := d[k]; !ok {
			t.Errorf("query: %s fehlt", k)
		}
	}
	vs2 := d["SYNO.VideoStation2.Streaming"].(map[string]any)
	if vs2["path"] != "entry.cgi" || vs2["requestFormat"] != "JSON" {
		t.Errorf("VS2.Streaming Eintrag falsch: %v", vs2)
	}

	// falsches Passwort → 400 von DSM durchgereicht
	m := call(t, ts.URL, url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"login"}, "account": {"anna"}, "passwd": {"falsch"}})
	if errCode(m) != 400 {
		t.Errorf("erwartet 400, bekam %v", m)
	}
	if fake.lastXFF == "" {
		t.Error("X-Forwarded-For nicht gesetzt")
	}

	sid := login(t, ts.URL)
	if sid != "DSMSID123" {
		t.Fatalf("DSM-sid muss unverändert durchgereicht werden, bekam %q", sid)
	}
	// VS-Aufruf mit DSM-sid
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Info"}, "version": {"1"}, "method": {"get"}, "_sid": {sid}})
	if m["success"] != true || m["data"].(map[string]any)["is_manager"] != true {
		t.Errorf("Info: %v", m)
	}
	// Fremde API wird an DSM durchgereicht
	m = call(t, ts.URL, url.Values{"api": {"SYNO.Core.Foo"}, "version": {"1"}, "method": {"get"}, "_sid": {sid}})
	if m["data"].(map[string]any)["echo"] != "SYNO.Core.Foo" {
		t.Errorf("Durchreichen fehlgeschlagen: %v", m)
	}
	// Logout bei DSM → sid ungültig → 119
	call(t, ts.URL, url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"logout"}, "session": {"VideoStation"}, "_sid": {sid}})
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Info"}, "version": {"1"}, "method": {"get"}, "_sid": {sid}})
	if errCode(m) != 119 {
		t.Errorf("nach Logout erwartet 119, bekam %v", m)
	}
}

func TestErrorsAndStreaming(t *testing.T) {
	ts, _, _ := newTestServer(t, "local")
	sid := login(t, ts.URL)

	if c := errCode(call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Info"}, "version": {"1"}, "method": {"get"}, "_sid": {"falsch"}})); c != 119 {
		t.Errorf("falsche sid: %d", c)
	}
	if c := errCode(call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Info"}, "version": {"9"}, "method": {"get"}, "_sid": {sid}})); c != 104 {
		t.Errorf("Version: %d", c)
	}
	if c := errCode(call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Subtitle"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}})); c != 103 {
		t.Errorf("nicht implementiert: %d", c)
	}
	if c := errCode(call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation9.X"}, "version": {"1"}, "method": {"x"}, "_sid": {sid}})); c != 102 {
		t.Errorf("unbekannte API: %d", c)
	}
	// Cookie-Variante (format=cookie, sid im Cookie "id")
	m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Movie"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}, "additional": {`["file","poster_mtime"]`}})
	movies := m["data"].(map[string]any)["movie"].([]any)
	if len(movies) != 2 {
		t.Fatalf("2 Filme erwartet: %v", m)
	}
	var mp4ID, mkvID float64
	for _, x := range movies {
		mo := x.(map[string]any)
		f := mo["additional"].(map[string]any)["file"].([]any)[0].(map[string]any)
		if strings.HasSuffix(f["path"].(string), ".mp4") {
			mp4ID = num(f["id"])
		} else {
			mkvID = num(f["id"])
		}
	}
	// MKV + tvOS + hls_remux → 1211 (kein Remux ohne FFmpeg)
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Streaming"}, "version": {"1"}, "method": {"open"}, "_sid": {sid},
		"file": {`{"id":` + itoa(int(mkvID)) + `}`}, "hls_remux": {`{"device":"tvos"}`}})
	if errCode(m) != 1211 {
		t.Errorf("MKV/tvOS erwartet 1211: %v", m)
	}
	// MKV raw → ok
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Streaming"}, "version": {"1"}, "method": {"open"}, "_sid": {sid},
		"file": {`{"id":` + itoa(int(mkvID)) + `}`}, "raw": {`{}`}})
	if m["success"] != true {
		t.Errorf("MKV raw: %v", m)
	}
	// MP4 + tvOS → Stream mit Range
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Streaming"}, "version": {"1"}, "method": {"open"}, "_sid": {sid},
		"file": {`{"id":` + itoa(int(mp4ID)) + `}`}, "hls_remux": {`{"device":"tvos"}`}})
	id := m["data"].(map[string]any)["stream_id"].(string)
	req, _ := http.NewRequest("GET", ts.URL+"/webapi/VideoStation/vtestreaming.cgi/1.mp4?api=SYNO.VideoStation.Streaming&version=1&method=stream&format=hls_remux&id=%22"+id+"%22&_sid="+sid, nil)
	req.Header.Set("Range", "bytes=4-7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(b) != "ftyp" || resp.Header.Get("Content-Range") != "bytes 4-7/5012" {
		t.Errorf("Range: %d %q %s", resp.StatusCode, b, resp.Header.Get("Content-Range"))
	}
}

func TestRegistryComplete(t *testing.T) {
	reg := webapi.VideoStationAPIs()
	for api, methods := range implTable {
		def, ok := reg[api]
		if !ok {
			t.Errorf("%s implementiert, aber nicht in Original-Registry", api)
			continue
		}
		for m := range methods {
			found := false
			for v := def.MinVersion; v <= def.MaxVersion; v++ {
				if def.HasMethod(v, m) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s.%s nicht in Original-Registry", api, m)
			}
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Vertragstest: Antworten müssen sich typstreng in die Datenmodelle der
// Original-App DS video 3.4.7 einlesen lassen (VideoV2Vo, FileVo, …).
type appFile struct {
	AudioBitrate       int64  `json:"audio_bitrate"`
	AudioCodec         string `json:"audio_codec"`
	Channel            int    `json:"channel"`
	ContainerType      string `json:"container_type"`
	ConversionProduced bool   `json:"conversion_produced"`
	Duration           string `json:"duration"`
	Filesize           string `json:"filesize"`
	FrameBitrate       int64  `json:"frame_bitrate"`
	ID                 string `json:"id"`
	Path               string `json:"path"`
	ResolutionX        string `json:"resolutionx"`
	ResolutionY        string `json:"resolutiony"`
	Sharepath          string `json:"sharepath"`
	VideoBitrate       int64  `json:"video_bitrate"`
	VideoCodec         string `json:"video_codec"`
}

type appVideo struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Tagline           string `json:"tagline"`
	OriginalAvailable string `json:"original_available"`
	Certificate       string `json:"certificate"`
	Rating            int    `json:"rating"`
	LastWatched       int64  `json:"last_watched"`
	MapperID          string `json:"mapper_id"`
	Additional        struct {
		Actor         []string  `json:"actor"`
		Director      []string  `json:"director"`
		Writer        []string  `json:"writer"`
		Genre         []string  `json:"genre"`
		Summary       string    `json:"summary"`
		Extra         string    `json:"extra"`
		PosterMtime   *string   `json:"poster_mtime"`
		BackdropMtime *string   `json:"backdrop_mtime"`
		WatchedRatio  float64   `json:"watched_ratio"`
		Collection    []any     `json:"collection"`
		IsParental    bool      `json:"is_parental_controlled"`
		File          []appFile `json:"file"`
	} `json:"additional"`
}

// strictDecode: wie Gson – Zahlen dürfen in String-Felder (Gson wandelt um),
// aber keine Arrays/Objekte in Strings und keine Strings in Zahlenfelder.
func strictDecode(t *testing.T, raw []byte, v any) {
	t.Helper()
	fixed := regexp.MustCompile(`"(id|mapper_id)":(\d+)`).ReplaceAll(raw, []byte(`"$1":"$2"`))
	if err := json.Unmarshal(fixed, v); err != nil {
		t.Fatalf("App-Modell kann Antwort nicht lesen: %v\n%s", err, raw)
	}
}

func TestAppContractGetinfo(t *testing.T) {
	ts, _, _ := newTestServer(t, "local")
	sid := login(t, ts.URL)
	m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Movie"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}})
	id := m["data"].(map[string]any)["movie"].([]any)[0].(map[string]any)["id"].(float64)
	// exakt die Zusatzfelder, die FetchVideoInfoTask (Film) anfordert
	add := `["extra","summary","file","actor","writer","director","genre","collection","watched_ratio","conversion_produced","backdrop_mtime","poster_mtime","parental_control"]`
	resp, err := http.PostForm(ts.URL+"/webapi/entry.cgi", url.Values{"api": {"SYNO.VideoStation2.Movie"}, "version": {"1"}, "method": {"getinfo"}, "_sid": {sid},
		"id": {"[" + strconv.Itoa(int(id)) + "]"}, "additional": {add}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			Movie []appVideo `json:"movie"`
		} `json:"data"`
	}
	strictDecode(t, raw, &out)
	if !out.Success || len(out.Data.Movie) != 1 {
		t.Fatalf("%s", raw)
	}
	v := out.Data.Movie[0]
	a := v.Additional
	if a.Actor == nil || a.Genre == nil || a.Writer == nil || a.Director == nil {
		t.Errorf("String-Arrays fehlen: %s", raw)
	}
	if len(a.File) == 0 {
		t.Fatalf("keine Datei: %s", raw)
	}
	f := a.File[0]
	// VideoInfoFragment: files[0].getDuration().split(":") – darf nie fehlen
	if !regexp.MustCompile(`^\d{2}:\d{2}:\d{2}$`).MatchString(f.Duration) {
		t.Errorf("duration %q nicht HH:MM:SS", f.Duration)
	}
	if _, err := strconv.ParseInt(f.Filesize, 10, 64); err != nil {
		t.Errorf("filesize %q", f.Filesize)
	}
	if _, err := strconv.ParseFloat(f.ResolutionX, 64); err != nil {
		t.Errorf("resolutionx %q (App: Double.parseDouble)", f.ResolutionX)
	}
	if !strings.Contains(string(raw), `"video_codec"`) {
		t.Error("video_codec fehlt (App: videoCodec.equals ohne Null-Prüfung)")
	}
	if v.Rating != -1 {
		t.Errorf("ohne Metadaten muss rating -1 sein, ist %d", v.Rating)
	}
	if a.PosterMtime != nil {
		t.Errorf("poster_mtime ohne Bild gesetzt")
	}
	// OfflineConversion, AudioTrack, WatchStatus, PlaybackSetting, Subtitle
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.OfflineConversion"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}, "additional": {"[]"}})
	if _, ok := m["data"].(map[string]any)["task"]; !ok {
		t.Errorf("OfflineConversion: %v", m)
	}
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.WatchStatus"}, "version": {"1"}, "method": {"setinfo"}, "_sid": {sid}, "id": {f.ID}, "position": {"125"}})
	if m["success"] != true {
		t.Errorf("setinfo: %v", m)
	}
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.WatchStatus"}, "version": {"1"}, "method": {"getinfo"}, "_sid": {sid}, "id": {f.ID}})
	ws := m["data"].(map[string]any)["watch_status"].(map[string]any)
	if ws["position"] != float64(125) { // Zahl: Android (Gson, String-Feld) liest sie ebenso
		t.Errorf("WatchStatus position: %v", ws)
	}
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.File"}, "version": {"2"}, "method": {"get_playback_setting"}, "_sid": {sid}, "id": {f.ID}})
	if d, _ := m["data"].(map[string]any); d == nil || d["position"] != float64(125) {
		t.Errorf("get_playback_setting ohne Position: %v", m)
	}
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.AudioTrack"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}, "id": {f.ID}})
	if _, ok := m["data"].(map[string]any)["trackinfo"].([]any); !ok {
		t.Errorf("AudioTrack: %v", m)
	}
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.File"}, "version": {"2"}, "method": {"get_playback_setting"}, "_sid": {sid}, "id": {f.ID}})
	if _, ok := m["data"].(map[string]any)["audio_track"]; !ok {
		t.Errorf("PlaybackSetting: %v", m)
	}
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.Subtitle"}, "version": {"2"}, "method": {"list"}, "_sid": {sid}, "id": {f.ID}})
	if _, ok := m["data"].([]any); !ok {
		t.Errorf("Subtitle list muss Array sein: %v", m)
	}
	m = call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Info"}, "version": {"1"}, "method": {"get"}, "_sid": {sid}})
	if _, ok := m["data"].(map[string]any)["ame_status"].(map[string]any); !ok {
		t.Errorf("ame_status fehlt: %v", m)
	}
}

// Port-Modus: Nicht-Video-Station-Pfade und WebSocket-Upgrades gehen
// unverändert (inkl. Host-Header) an DSM.
func TestPassthroughHostAndUpgrade(t *testing.T) {
	var gotHost string
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		if r.Header.Get("Upgrade") == "websocket" {
			hj, _ := w.(http.Hijacker)
			conn, buf, _ := hj.Hijack()
			buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			buf.Flush()
			line, _ := buf.ReadString('\n')
			buf.WriteString("echo:" + line)
			buf.Flush()
			conn.Close()
			return
		}
		io.WriteString(w, "DSM-Seite")
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	s := &Server{Up: up, Rec: NewRecorder(""), Registry: webapi.VideoStationAPIs()}
	s.SetCfg(config.Defaults())
	ts := httptest.NewServer(s)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/webman/index.cgi", nil)
	req.Host = "nas.example.de"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "DSM-Seite" || gotHost != "nas.example.de" {
		t.Fatalf("Durchreichen: %q Host=%q", b, gotHost)
	}

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "GET /webman/ws HTTP/1.1\r\nHost: nas\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("kein Upgrade: %q", status)
	}
	for {
		l, _ := br.ReadString('\n')
		if l == "\r\n" || l == "" {
			break
		}
	}
	io.WriteString(conn, "hallo\n")
	echo, _ := br.ReadString('\n')
	if echo != "echo:hallo\n" {
		t.Fatalf("WebSocket-Daten: %q", echo)
	}
}

// num: Datei-IDs sind laut App-Modell Text ("12"), Objekt-IDs Zahlen.
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		n, _ := strconv.ParseFloat(x, 64)
		return n
	}
	return -1
}

// Große Uploads (z.B. .spk über Paket-Zentrum → Manuelle Installation) müssen
// ungekürzt bei DSM ankommen – auch ohne Content-Length (chunked).
func TestPassthroughLargeUpload(t *testing.T) {
	payload := bytes.Repeat([]byte("SPKDATA-"), 20<<20/8) // 20 MB
	var got int64
	var gotType string
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.Copy(io.Discard, r.Body)
		gotType = r.Header.Get("Content-Type")
		io.WriteString(w, `{"success":true}`)
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	s := &Server{Up: up, Rec: NewRecorder(""), Registry: webapi.VideoStationAPIs()}
	s.SetCfg(config.Defaults())
	ts := httptest.NewServer(s)
	defer ts.Close()
	for _, chunked := range []bool{false, true} {
		var body io.Reader = bytes.NewReader(payload)
		if chunked {
			body = io.MultiReader(body) // keine bekannte Länge
		}
		req, _ := http.NewRequest("POST", ts.URL+"/webapi/entry.cgi?api=SYNO.Core.Package.Installation&method=upload&version=1", body)
		req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got != int64(len(payload)) || gotType != "multipart/form-data; boundary=x" {
			t.Fatalf("chunked=%v: DSM erhielt %d von %d Bytes (%s)", chunked, got, len(payload), gotType)
		}
	}
}

// Port-Modus: DSM-Oberfläche im Browser → direkt zu DSM (5010); WebAPI bleibt hier.
func TestDSMUIRedirectInPortMode(t *testing.T) {
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "DSM") }))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	s := &Server{Up: up, Rec: NewRecorder(""), Registry: webapi.VideoStationAPIs()}
	ts := httptest.NewServer(s)
	defer ts.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	c := config.Defaults()
	c.FrontendMode, c.FrontHTTP, c.Upstream = true, ":"+port, "http://127.0.0.1:5010"
	s.SetCfg(c)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", ts.URL+"/webman/index.cgi?x=1", nil)
	req.Host = "nas.example.de:5000"
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "http://nas.example.de:5010/webman/index.cgi?x=1" {
		t.Fatalf("Weiterleitung: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Über Reverse-Proxy (Nginx Proxy Manager): nicht umleiten, durchreichen
	req, _ = http.NewRequest("GET", ts.URL+"/", nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	resp, _ = noFollow.Do(req)
	b0, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b0) != "DSM" {
		t.Fatalf("Reverse-Proxy: %d %q", resp.StatusCode, b0)
	}
	// Nicht-HTML (Skripte, andere DS-Apps) und Wartungsport: weiter durchreichen
	req, _ = http.NewRequest("GET", ts.URL+"/webman/x.js", nil)
	resp, _ = noFollow.Do(req)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "DSM" {
		t.Fatalf("Durchreichen: %d %q", resp.StatusCode, b)
	}
	c.FrontHTTP = ":5000"
	s.SetCfg(c)
	req, _ = http.NewRequest("GET", ts.URL+"/", nil)
	req.Header.Set("Accept", "text/html")
	resp, _ = noFollow.Do(req)
	resp.Body.Close()
	if resp.StatusCode == http.StatusFound {
		t.Fatal("Wartungsport darf nicht umleiten")
	}
}

// Integration in DSMs eigenen Webserver (wie früher Video Station): DSM leitet
// query.cgi der DS-video-Apps und alles unter /webapi/_dsvr/ an das Paket.
func TestBehindDSMWebServer(t *testing.T) {
	ts, fake, _ := newTestServer(t, "dsm")
	post := func(path string, v url.Values) map[string]any {
		t.Helper()
		resp, err := http.PostForm(ts.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return m
	}
	// 1. App fragt die API-Liste ab (nginx hat query.cgi auf _dsvr umgeschrieben)
	q := post(DSMPrefix+"/query.cgi", url.Values{"api": {"SYNO.API.Info"}, "version": {"1"}, "method": {"query"}, "query": {"all"}})
	d, _ := q["data"].(map[string]any)
	path := func(api string) any { e, _ := d[api].(map[string]any); return e["path"] }
	if path("SYNO.API.Auth") != "entry.cgi" || path("SYNO.Core.Foo") != "entry.cgi" {
		t.Fatalf("DSM-APIs müssen unverändert bleiben: %v", d)
	}
	if path("SYNO.VideoStation2.Movie") != "_dsvr/entry.cgi" || path("SYNO.VideoStation.Streaming") != "_dsvr/VideoStation/vtestreaming.cgi" {
		t.Fatalf("VS-Pfade: %v / %v", path("SYNO.VideoStation2.Movie"), path("SYNO.VideoStation.Streaming"))
	}
	// 2. Anmeldung direkt bei DSM, 3. Video-Station-Aufruf über den gemeldeten Pfad
	sid := login(t, ts.URL)
	m := post("/webapi/"+path("SYNO.VideoStation2.Movie").(string), url.Values{"api": {"SYNO.VideoStation2.Movie"}, "version": {"1"}, "method": {"list"},
		"library_id": {"0"}, "offset": {"0"}, "limit": {"10"}, "_sid": {sid}})
	if ok, _ := m["success"].(bool); !ok {
		t.Fatalf("Movie list über DSM-Pfad: %v", m)
	}
	// 4. Fremd-API an query.cgi (über DSM) darf nicht zurück an query.cgi (Schleife)
	post(DSMPrefix+"/query.cgi", url.Values{"api": {"SYNO.Core.Foo"}, "version": {"1"}, "method": {"get"}})
	fake.mu.Lock()
	lp := fake.lastPath
	fake.mu.Unlock()
	if lp != "/webapi/entry.cgi" {
		t.Fatalf("Weitergabe an DSM über %q statt /webapi/entry.cgi", lp)
	}
	// 5. HLS-Pfade passend zum Zugangsweg
	r := httptest.NewRequest("GET", DSMPrefix+"/hls/x/index.m3u8", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxViaDSM, true))
	if HLSPrefix(r, "abc") != "/webapi/_dsvr/hls/abc/" {
		t.Fatal(HLSPrefix(r, "abc"))
	}
}

// Android-Ordnerbrowser: SYNO.VideoStation.Folder list v2 (Modell FolderContentVo).
func TestFolderListV1(t *testing.T) {
	ts, _, _ := newTestServer(t, "local")
	sid := login(t, ts.URL)
	q := func(id string) map[string]any {
		m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.Folder"}, "version": {"2"}, "method": {"list"}, "_sid": {sid},
			"library_id": {"0"}, "type": {"movie"}, "id": {id}, "offset": {"0"}, "limit": {"100"}, "sort_by": {"title"}, "sort_direction": {"asc"},
			"additional": {`["watched_ratio","backdrop_mtime","poster_mtime"]`}, "preview_video": {"4"}})
		if m["success"] != true {
			t.Fatalf("Ordnerliste: %v", m)
		}
		return m["data"].(map[string]any)
	}
	root := q("")
	objs := root["objects"].([]any)
	if len(objs) != 1 || num(root["total"]) != 1 {
		t.Fatalf("Wurzel: %v", root)
	}
	r := objs[0].(map[string]any)
	if r["type"] != "folder" || num(r["file_count"]) != 2 || r["title"] != "media" {
		t.Fatalf("Wurzelordner: %v", r)
	}
	// Geöffneter Ordner: alle Medien samt Unterordnern, ohne Unterordner-Objekte.
	in := q(r["id"].(string))
	objs = in["objects"].([]any)
	if len(objs) != 2 {
		t.Fatalf("Inhalt: %v", in)
	}
	f0, f1 := objs[0].(map[string]any), objs[1].(map[string]any)
	if f0["type"] != "movie" || f0["title"] != "Film A" || f1["type"] != "movie" || f1["title"] != "Film B" {
		t.Fatalf("flache Liste erwartet: %v / %v", f0, f1)
	}
	meta := f1["additional"].(map[string]any)["metadata"].(map[string]any)
	if meta["type"] != "movie" || meta["id"] == nil {
		t.Fatalf("metadata: %v", meta)
	}
	if _, ok := meta["additional"].(map[string]any)["watched_ratio"]; !ok {
		t.Fatalf("watched_ratio fehlt: %v", meta)
	}
	// fremde Ordner-ID → leere Liste
	if d := q(encodeFolderID("/etc")); num(d["total"]) != 0 {
		t.Fatalf("/etc sichtbar: %v", d)
	}
	// Bibliothekstyp ohne Ordner → leer
	m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.Folder"}, "version": {"2"}, "method": {"list"}, "_sid": {sid}, "library_id": {"0"}, "type": {"tvshow"}, "id": {""}})
	if num(m["data"].(map[string]any)["total"]) != 0 {
		t.Fatalf("tvshow-Wurzel: %v", m)
	}
}

// iOS-Ordnerbrowser: SYNO.VideoStation2.Folder list (Schema object[], path, sharepath).
func TestFolderListV2(t *testing.T) {
	ts, _, _ := newTestServer(t, "local")
	sid := login(t, ts.URL)
	q := func(extra url.Values) map[string]any {
		v := url.Values{"api": {"SYNO.VideoStation2.Folder"}, "version": {"1"}, "method": {"list"}, "_sid": {sid},
			"library_id": {"0"}, "type": {`"movie"`}, "offset": {"0"}, "limit": {"5000"}}
		for k, x := range extra {
			v[k] = x
		}
		m := call(t, ts.URL, v)
		if m["success"] != true {
			t.Fatalf("Ordnerliste: %v", m)
		}
		return m["data"].(map[string]any)
	}
	root := q(nil)
	objs := root["object"].([]any)
	if len(objs) != 1 {
		t.Fatalf("Wurzel: %v", root)
	}
	r := objs[0].(map[string]any)
	for _, k := range []string{"file_count", "id", "path", "sharepath", "title", "type"} {
		if _, ok := r[k]; !ok {
			t.Fatalf("Feld %s fehlt: %v", k, r)
		}
	}
	in := q(url.Values{"id": {`"` + r["id"].(string) + `"`}})
	objs = in["object"].([]any)
	if len(objs) != 2 || objs[0].(map[string]any)["type"] != "movie" || objs[1].(map[string]any)["type"] != "movie" {
		t.Fatalf("Inhalt: %v", in)
	}
	if p, _ := objs[0].(map[string]any)["path"].(string); !strings.HasSuffix(p, "Film A (2020).mp4") {
		t.Fatalf("Pfad: %v", objs[0])
	}
	// absteigend sortiert
	desc := q(url.Values{"id": {`"` + r["id"].(string) + `"`}, "sort_direction": {`"desc"`}})
	if do := desc["object"].([]any); do[0].(map[string]any)["title"] != "Film B" {
		t.Fatalf("absteigend: %v", desc)
	}
}

// Ordner per Pfad/Freigabepfad oder unbekanntem Parameternamen.
func TestFolderListResolve(t *testing.T) {
	ts, _, dir := newTestServer(t, "local")
	sid := login(t, ts.URL)
	media := filepath.Join(dir, "media")
	for _, kv := range [][2]string{{"path", `"` + filepath.Join(media, "Film A (2020)") + `"`}, {"parent", encodeFolderID(filepath.Join(media, "Film A (2020)"))}} {
		m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Folder"}, "version": {"1"}, "method": {"list"}, "_sid": {sid},
			"library_id": {"0"}, "type": {`"movie"`}, kv[0]: {kv[1]}})
		objs := m["data"].(map[string]any)["object"].([]any)
		if len(objs) != 1 || objs[0].(map[string]any)["title"] != "Film A" {
			t.Fatalf("%s: %v", kv[0], m)
		}
	}
}

// Android list v2 schickt Filter als JSON-Listen (title=[] = kein Filter).
func TestListTitleFilterJSON(t *testing.T) {
	ts, _, _ := newTestServer(t, "local")
	sid := login(t, ts.URL)
	q := func(title string) float64 {
		m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation.Movie"}, "version": {"2"}, "method": {"list"}, "_sid": {sid},
			"library_id": {"0"}, "title": {title}, "year": {"[]"}, "genre": {"[]"}})
		return num(m["data"].(map[string]any)["total"])
	}
	if n := q("[]"); n != 2 {
		t.Fatalf("title=[]: %v Filme, erwartet 2", n)
	}
	if n := q(`["Film A"]`); n != 1 {
		t.Fatalf(`title=["Film A"]: %v`, n)
	}
	if n := q("Film B"); n != 1 {
		t.Fatalf("title=Film B: %v", n)
	}
}

// Android: leere Namenslisten weglassen (App-Absturz bei []), iOS unverändert.
func TestEmptyNameListsAndroid(t *testing.T) {
	ts, _, _ := newTestServer(t, "local")
	sid := login(t, ts.URL)
	get := func(ua string) map[string]any {
		v := url.Values{"api": {"SYNO.VideoStation2.Movie"}, "version": {"1"}, "method": {"list"}, "_sid": {sid},
			"additional": {`["actor","genre","director","writer","summary"]`}, "limit": {"1"}}
		rq, _ := http.NewRequest("POST", ts.URL+"/webapi/entry.cgi", strings.NewReader(v.Encode()))
		rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rq.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return m["data"].(map[string]any)["movie"].([]any)[0].(map[string]any)["additional"].(map[string]any)
	}
	a := get("Synology-DS video_3.4.7_rv:328_SM-A405FN_Android")
	for _, k := range []string{"actor", "genre", "director", "writer"} {
		if _, ok := a[k]; ok {
			t.Fatalf("Android: %s sollte fehlen: %v", k, a)
		}
	}
	if _, ok := get("DS video iOS")["actor"]; !ok {
		t.Fatal("iOS: actor fehlt")
	}
}

// TV-Aufnahmen: Liste, Gruppen nach Sender/Sendung, Filter wie die Android-App.
func TestRecordings(t *testing.T) {
	ts, _, _ := newTestServer(t, "local")
	sid := login(t, ts.URL)
	q := func(method string, extra url.Values) map[string]any {
		v := url.Values{"api": {"SYNO.VideoStation2.TVRecording"}, "version": {"1"}, "method": {method}, "_sid": {sid}, "library_id": {"0"}}
		for k, x := range extra {
			v[k] = x
		}
		m := call(t, ts.URL, v)
		if m["success"] != true {
			t.Fatalf("%s: %v", method, m)
		}
		return m["data"].(map[string]any)
	}
	all := q("list", nil)["recording"].([]any)
	if len(all) != 3 {
		t.Fatalf("Aufnahmen: %v", all)
	}
	r0 := all[0].(map[string]any)
	if r0["channel_name"] == nil || r0["original_available"] == "" {
		t.Fatalf("Felder: %v", r0)
	}
	ch := q("list_channel", nil)["channel"].([]any)
	if len(ch) != 2 || ch[0].(map[string]any)["title"] != "Das Erste HD" {
		t.Fatalf("Sender: %v", ch)
	}
	pr := q("list_program", nil)["program"].([]any)
	if len(pr) != 2 {
		t.Fatalf("Sendungen: %v", pr)
	}
	if n := len(q("list", url.Values{"channel_name": {`["ZDF HD"]`}})["recording"].([]any)); n != 2 {
		t.Fatalf("Filter Sender: %d", n)
	}
	if n := len(q("list", url.Values{"title": {`["Tatort"]`}})["recording"].([]any)); n != 2 {
		t.Fatalf("Filter Sendung: %d", n)
	}
}

// Herunterladen (Offline-Wiedergabe) und Spurinformationen.
func TestFileDownloadAndTrackInfo(t *testing.T) {
	ts, _, _ := newTestServer(t, "local")
	sid := login(t, ts.URL)
	m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Movie"}, "version": {"1"}, "method": {"list"}, "_sid": {sid}, "additional": {`["file"]`}, "limit": {"1"}})
	mv := m["data"].(map[string]any)["movie"].([]any)[0].(map[string]any)
	fid := mv["additional"].(map[string]any)["file"].([]any)[0].(map[string]any)["id"].(string)
	resp, err := http.Get(ts.URL + "/webapi/entry.cgi?api=SYNO.VideoStation2.File&version=2&method=download&id=" + fid + "&_sid=" + sid)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(b) == 0 || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("Download: %d %q %d Bytes", resp.StatusCode, resp.Header.Get("Content-Disposition"), len(b))
	}
	ti := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.File"}, "version": {"1"}, "method": {"get_track_info"}, "_sid": {sid}, "id": {fid}})
	if ti["success"] != true {
		t.Fatalf("get_track_info: %v", ti)
	}
	d := ti["data"].(map[string]any)
	if _, ok := d["video"].([]any); !ok {
		t.Fatalf("get_track_info: %v", d)
	}
}

// Serienordner mit Staffel-Unterordnern: geöffneter Ordner zeigt alle Folgen flach.
func TestFolderListFlatEpisodes(t *testing.T) {
	ts, _, dir := newTestServer(t, "local")
	s := ts.Config.Handler.(*Server)
	serien := filepath.Join(dir, "serien")
	for _, p := range []string{"Kanalwelt/Staffel 2/Kanalwelt - S02E01.mkv", "Kanalwelt/Staffel 1/Kanalwelt - S01E01.mkv", "Kanalwelt/Staffel 1/Kanalwelt - S01E02.mkv"} {
		full := filepath.Join(serien, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o644)
	}
	cfg := *s.Cfg()
	cfg.Folders = append(cfg.Folders, config.Folder{Path: serien, Type: "tvshow"})
	s.SetCfg(cfg)
	if _, err := s.Lib.Scan(cfg.Folders); err != nil {
		t.Fatal(err)
	}
	sid := login(t, ts.URL)
	q := func(id string) []any {
		m := call(t, ts.URL, url.Values{"api": {"SYNO.VideoStation2.Folder"}, "version": {"1"}, "method": {"list"}, "_sid": {sid},
			"library_id": {"0"}, "type": {`"tvshow"`}, "id": {`"` + id + `"`}, "sort_by": {`"title"`}, "sort_direction": {`"asc"`}})
		return m["data"].(map[string]any)["object"].([]any)
	}
	root := q("")
	if len(root) != 1 || root[0].(map[string]any)["title"] != "serien" || num(root[0].(map[string]any)["file_count"]) != 3 {
		t.Fatalf("Wurzel: %v", root)
	}
	var titles []string
	for _, o := range q(root[0].(map[string]any)["id"].(string)) {
		m := o.(map[string]any)
		if m["type"] != "tvshow_episode" {
			t.Fatalf("Typ: %v", m)
		}
		titles = append(titles, m["title"].(string))
	}
	if strings.Join(titles, "|") != "Kanalwelt – S01E01|Kanalwelt – S01E02|Kanalwelt – S02E01" {
		t.Fatalf("Folgen: %v", titles)
	}
}

// Ordnerkacheln: preview_video mit bis zu 4 Titeln, die ein Plakat haben.
func TestFolderPreviewVideo(t *testing.T) {
	ts, _, dir := newTestServer(t, "local")
	s := ts.Config.Handler.(*Server)
	media := filepath.Join(dir, "media")
	_ = os.MkdirAll(filepath.Join(media, "Film C (2019)"), 0o755)
	_ = os.WriteFile(filepath.Join(media, "Film C (2019)", "Film C (2019).mkv"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(media, "Film C (2019)", "poster.jpg"), []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01"), 0o644)
	if _, err := s.Lib.Scan(s.Cfg().Folders); err != nil {
		t.Fatal(err)
	}
	sid := login(t, ts.URL)
	for _, api := range []string{"SYNO.VideoStation2.Folder", "SYNO.VideoStation.Folder"} {
		m := call(t, ts.URL, url.Values{"api": {api}, "version": {"1"}, "method": {"list"}, "_sid": {sid},
			"library_id": {"0"}, "type": {`"movie"`}, "id": {`""`}, "preview_video": {"4"}})
		d := m["data"].(map[string]any)
		key := "object"
		if api == "SYNO.VideoStation.Folder" {
			key = "objects"
		}
		f := d[key].([]any)[0].(map[string]any)
		pv, ok := f["preview_video"].([]any)
		if !ok || len(pv) != 1 {
			t.Fatalf("%s preview_video: %v", api, f)
		}
		p := pv[0].(map[string]any)
		if p["type"] != "movie" {
			t.Fatalf("preview: %v", p)
		}
		// Plakat über die ID abrufbar
		resp, err := http.Get(ts.URL + "/webapi/entry.cgi?api=SYNO.VideoStation.Poster&version=2&method=getimage&_sid=" + sid + "&id=" + p["id"].(string) + "&type=movie")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("Poster %v: %d", p, resp.StatusCode)
		}
	}
}

// TestAdminViaDSMWeb: Weboberfläche unter der DSM-Adresse (…/dsvr/, nginx-
// Einbindung setzt X-DSVR-Via). Nur Aufrufe von DSMs nginx (Loopback) gelten
// als „über DSM“ – dann liegen die Videos der Mediathek unter /webapi/_dsvr/hls/.
func TestAdminViaDSMWeb(t *testing.T) {
	var got string
	s := &Server{AdminHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = HLSPrefix(r, "j1") })}
	for _, c := range []struct{ remote, via, want string }{
		{"127.0.0.1:40000", "dsm", "/webapi/_dsvr/hls/j1/"},
		{"[::1]:40000", "dsm", "/webapi/_dsvr/hls/j1/"},
		{"192.168.1.20:40000", "dsm", "/hls/j1/"}, // direkt aus dem Heimnetz: Kennzeichen zählt nicht
		{"127.0.0.1:40000", "", "/hls/j1/"},
	} {
		r := httptest.NewRequest("POST", "/dsvr/api/play", nil)
		r.RemoteAddr = c.remote
		if c.via != "" {
			r.Header.Set("X-DSVR-Via", c.via)
		}
		got = ""
		s.ServeHTTP(httptest.NewRecorder(), r)
		if got != c.want {
			t.Errorf("%s via=%q: %q, erwartet %q", c.remote, c.via, got, c.want)
		}
	}
}

// TestWebLoginViaDSM: Die Weboberfläche meldet sich über den Paketpfad
// /webapi/_dsvr/entry.cgi an (auch unter der DSM-Adresse …/dsvr/). Lehnt DSM
// den Sitzungsnamen „DSVideoRevival“ für normale Benutzer mit 402 ab, meldet
// das Paket mit einer DSM-eigenen Sitzung an; DSMs Cookie „id“ bleibt außen vor.
func TestWebLoginViaDSM(t *testing.T) {
	ts, fake, _ := newTestServer(t, "dsm")
	fake.mu.Lock()
	fake.denySession = map[string]bool{"DSVideoRevival": true}
	fake.mu.Unlock()
	body := url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"login"}, "session": {"DSVideoRevival"},
		"format": {"sid"}, "account": {"anna"}, "passwd": {"geheim"}}
	resp, err := http.PostForm(ts.URL+DSMPrefix+"/entry.cgi", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	d, _ := m["data"].(map[string]any)
	if ok, _ := m["success"].(bool); !ok || d["sid"] == "" {
		t.Fatalf("Anmeldung über %s/entry.cgi: %v", DSMPrefix, m)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "id" {
			t.Fatalf("DSM-Cookie „id“ an den Browser gereicht")
		}
	}
	fake.mu.Lock()
	n := 0
	for _, c := range fake.calls {
		if c == "SYNO.API.Auth.login" {
			n++
		}
	}
	fake.mu.Unlock()
	if n < 2 {
		t.Fatalf("keine Ersatzanmeldung nach 402 (%d Anmeldungen bei DSM)", n)
	}
}
