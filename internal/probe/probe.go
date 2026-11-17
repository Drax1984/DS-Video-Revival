// Package probe simuliert die Aufrufkette eines DS-Video-Clients (Login →
// Bibliothek → Wiedergabe). Läuft gegen DS Video Revival UND gegen eine
// Original-Video-Station – der Vergleich beider Ausgaben ist ein direkter
// Konformitätstest. Die Aufrufkette für die Wiedergabe ist belegt durch
// holg/ds-video-hevc (selftest, DS Video tvOS).
package probe

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Options configure a probe run.
type Options struct {
	Base     string
	Account  string
	Password string
	Device   string // hls_remux device, Standard "tvos"
	Raw      bool
	Verbose  bool
	Insecure bool
	Out      io.Writer
}

type step struct {
	name string
	ok   bool
	info string
}

// Run executes the probe; returns error if a mandatory step fails.
func Run(o Options) error {
	if o.Device == "" {
		o.Device = "tvos"
	}
	base := strings.TrimRight(o.Base, "/")
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: o.Insecure}}} //nolint:gosec // Testwerkzeug, explizit per Flag
	var steps []step
	report := func(name string, ok bool, info string) {
		steps = append(steps, step{name, ok, info})
		mark := "PASS"
		if !ok {
			mark = "FAIL"
		}
		fmt.Fprintf(o.Out, "[%s] %-34s %s\n", mark, name, info)
	}
	post := func(params url.Values) (map[string]any, error) {
		resp, err := hc.PostForm(base+"/webapi/entry.cgi", params)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("keine JSON-Antwort (HTTP %d): %.120s", resp.StatusCode, b)
		}
		if o.Verbose {
			fmt.Fprintf(o.Out, "      %s %s → %s\n", params.Get("api"), params.Get("method"), compact(b, 600))
		}
		return m, nil
	}

	// 1. API-Discovery über query.cgi
	resp, err := hc.Get(base + "/webapi/query.cgi?api=SYNO.API.Info&version=1&method=query&query=all")
	if err != nil {
		report("query.cgi (SYNO.API.Info)", false, err.Error())
		return err
	}
	var info map[string]any
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	_ = json.Unmarshal(b, &info)
	data, _ := info["data"].(map[string]any)
	_, hasAuth := data["SYNO.API.Auth"]
	_, hasStream := data["SYNO.VideoStation2.Streaming"]
	report("query.cgi (SYNO.API.Info)", hasAuth && hasStream, fmt.Sprintf("%d APIs, Auth=%v, VS2.Streaming=%v", len(data), hasAuth, hasStream))
	if !hasAuth {
		return fmt.Errorf("SYNO.API.Auth fehlt")
	}

	// 2. Anmeldung (wie selftest: v6, session=VideoStation, format=sid)
	m, err := post(url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"login"}, "account": {o.Account}, "passwd": {o.Password}, "session": {"VideoStation"}, "format": {"sid"}})
	sid := ""
	if err == nil {
		if d, _ := m["data"].(map[string]any); d != nil {
			sid, _ = d["sid"].(string)
		}
	}
	report("SYNO.API.Auth login", sid != "", errInfo(m, err))
	if sid == "" {
		return fmt.Errorf("Anmeldung fehlgeschlagen")
	}
	defer func() {
		m, err := post(url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"logout"}, "session": {"VideoStation"}, "_sid": {sid}})
		report("SYNO.API.Auth logout", err == nil && m["success"] == true, errInfo(m, err))
	}()

	call := func(api, version, method string, extra url.Values) (map[string]any, error) {
		v := url.Values{"api": {api}, "version": {version}, "method": {method}, "_sid": {sid}}
		for k, x := range extra {
			v[k] = x
		}
		return post(v)
	}

	m, err = call("SYNO.VideoStation2.Info", "1", "get", nil)
	d, _ := m["data"].(map[string]any)
	report("SYNO.VideoStation2.Info get", err == nil && m["success"] == true, fmt.Sprintf("version_string=%v %s", d["version_string"], errInfo(m, err)))

	m, err = call("SYNO.VideoStation2.Library", "1", "list", url.Values{"offset": {"0"}, "limit": {"100"}})
	d, _ = m["data"].(map[string]any)
	libs, _ := d["library"].([]any)
	report("SYNO.VideoStation2.Library list", err == nil && m["success"] == true, fmt.Sprintf("%d Bibliotheken %s", len(libs), errInfo(m, err)))

	m, err = call("SYNO.VideoStation2.Movie", "1", "list", url.Values{"offset": {"0"}, "limit": {"50"}, "library_id": {"0"},
		"sort_by": {`"title"`}, "sort_direction": {`"asc"`}, "additional": {`["watched_ratio","file"]`}})
	d, _ = m["data"].(map[string]any)
	movies, _ := d["movie"].([]any)
	report("SYNO.VideoStation2.Movie list", err == nil && m["success"] == true && len(movies) > 0, fmt.Sprintf("%d Filme (total=%v) %s", len(movies), d["total"], errInfo(m, err)))

	fileID := -1.0
	title := ""
	for _, mv := range movies {
		mo, _ := mv.(map[string]any)
		add, _ := mo["additional"].(map[string]any)
		files, _ := add["file"].([]any)
		if len(files) > 0 {
			f0, _ := files[0].(map[string]any)
			switch id := f0["id"].(type) {
			case float64:
				fileID = id
			case string:
				fileID, _ = strconv.ParseFloat(id, 64)
			}
			if fileID >= 0 {
				title = fmt.Sprint(mo["title"])
				break
			}
		}
	}
	if fileID < 0 {
		report("Datei-ID aus additional.file", false, "kein Film mit Datei gefunden")
		return fmt.Errorf("keine abspielbare Datei")
	}
	report("Datei-ID aus additional.file", true, fmt.Sprintf("%q → file id %.0f", title, fileID))

	open := url.Values{"file": {fmt.Sprintf(`{"id":%.0f}`, fileID)}}
	format := "hls_remux"
	if o.Raw {
		open.Set("raw", "{}")
		format = "raw"
	} else {
		open.Set("hls_remux", fmt.Sprintf(`{"device":%q,"force_open_vte":false,"audio_track":1,"profile":""}`, o.Device))
	}
	m, err = call("SYNO.VideoStation2.Streaming", "1", "open", open)
	d, _ = m["data"].(map[string]any)
	streamID, _ := d["stream_id"].(string)
	report("SYNO.VideoStation2.Streaming open", streamID != "", fmt.Sprintf("format=%v %s", d["format"], errInfo(m, err)))
	if streamID == "" {
		return fmt.Errorf("Stream konnte nicht geöffnet werden")
	}

	u := fmt.Sprintf("%s/webapi/VideoStation/vtestreaming.cgi/1.mp4?api=SYNO.VideoStation.Streaming&version=1&method=stream&id=%s&format=%s&_sid=%s",
		base, url.QueryEscape(streamID), format, url.QueryEscape(sid))
	rq, _ := http.NewRequest(http.MethodGet, u, nil)
	rq.Header.Set("Range", "bytes=0-1023")
	sr, err := hc.Do(rq)
	if err != nil {
		report("vtestreaming.cgi stream (Range)", false, err.Error())
	} else {
		head, _ := io.ReadAll(io.LimitReader(sr.Body, 2048))
		sr.Body.Close()
		isMP4 := len(head) >= 12 && string(head[4:8]) == "ftyp"
		isMKV := len(head) >= 4 && head[0] == 0x1a && head[1] == 0x45 && head[2] == 0xdf && head[3] == 0xa3
		if strings.Contains(sr.Header.Get("Content-Type"), "mpegurl") { // hls_remux mit FFmpeg: echte HLS-Playlist
			report("vtestreaming.cgi stream (HLS-Playlist)", sr.StatusCode == http.StatusOK && strings.HasPrefix(string(head), "#EXTM3U"),
				fmt.Sprintf("HTTP %d, %s, %d Bytes", sr.StatusCode, sr.Header.Get("Content-Type"), len(head)))
		} else {
			report("vtestreaming.cgi stream (Range)", sr.StatusCode == http.StatusPartialContent,
				fmt.Sprintf("HTTP %d, %s, %s, %d Bytes, mp4=%v mkv=%v", sr.StatusCode, sr.Header.Get("Content-Type"), sr.Header.Get("Content-Range"), len(head), isMP4, isMKV))
		}
	}
	cr, err := hc.Get(fmt.Sprintf("%s/webapi/VideoStation/vtestreaming.cgi?api=SYNO.VideoStation.Streaming&version=1&method=close&id=%s&format=%s&_sid=%s",
		base, url.QueryEscape(streamID), format, url.QueryEscape(sid)))
	if err == nil {
		cr.Body.Close()
	}
	report("vtestreaming.cgi close", err == nil && cr.StatusCode == 200, "")

	fails := 0
	for _, s := range steps {
		if !s.ok {
			fails++
		}
	}
	if fails > 0 {
		return fmt.Errorf("%d Schritte fehlgeschlagen", fails)
	}
	return nil
}

func errInfo(m map[string]any, err error) string {
	if err != nil {
		return err.Error()
	}
	if m == nil {
		return ""
	}
	if s, _ := m["success"].(bool); !s {
		return fmt.Sprintf("error=%v", m["error"])
	}
	return ""
}

func compact(b []byte, n int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
