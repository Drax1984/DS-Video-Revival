package vs

// Wiedergabe-APIs nach den Klassen der Original-App DS video 3.4.7:
// OpenStreamTask2, CloseStreamTask, ConnectionManager.getStreamUrl,
// FetchWatchStatusTask/SetWatchStatusTask (WatchStatusVo),
// FetchAudioTrackListTask (AudioTrackVo), FetchPlaybackSettingTask
// (PlaybackSettingVo), FetchSubtitleListV2Task (SubtitleV2Vo).

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dsvideorevival/internal/library"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/webapi"
)

type openParams struct {
	Device       string `json:"device"`
	Profile      string `json:"profile"`
	AudioTrack   *int   `json:"audio_track"`
	AudioFormat  string `json:"audio_format"`
	ForceOpenVTE bool   `json:"force_open_vte"`
}

// capsFromRequest: Gerätetyp aus hls-Parametern bzw. User-Agent.
func capsFromRequest(c *Ctx, device string) stream.Caps {
	if device != "" && device != "chromecast" {
		return stream.CapsFor(device)
	}
	ua := strings.ToLower(c.Req.HTTP.UserAgent())
	switch {
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"), strings.Contains(ua, "ios"),
		strings.Contains(ua, "darwin"), strings.Contains(ua, "cfnetwork"), strings.Contains(ua, "appletv"), strings.Contains(ua, "tvos"):
		return stream.CapsApple
	case strings.Contains(ua, "android"), strings.Contains(ua, "okhttp"), strings.Contains(ua, "exoplayer"):
		return stream.CapsAndroid
	}
	if device == "chromecast" {
		return stream.CapsBrowser
	}
	return stream.CapsApple
}

func streamOpen(c *Ctx) {
	var file struct {
		ID int `json:"id"`
	}
	if c.Req.Has("file") {
		if err := c.Req.JSON("file", &file); err != nil {
			webapi.Fail(c.W, webapi.ErrNoParameter)
			return
		}
	} else {
		file.ID = c.Req.Int("id", -1)
	}
	f, ok := c.Lib.File(file.ID)
	if !ok {
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	format := ""
	var op openParams
	for _, k := range []string{"hls_remux", "webm_remux", "hls", "raw"} {
		if c.Req.Has(k) {
			format = k
			_ = c.Req.JSON(k, &op)
			break
		}
	}
	if format == "" {
		c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, "open ohne bekanntes Format", paramKeys(c.Req.Params))
		webapi.Fail(c.W, webapi.ErrVSNoStreamFormat)
		return
	}
	if f.Probe.NeedsProbe(c.S.FFprobe) {
		f.Probe = library.RunProbe(c.S.FFprobe, f.Path)
		c.Lib.SetProbe(f.ID, f.Probe)
	}
	audio := 0
	if op.AudioTrack != nil && *op.AudioTrack > 0 {
		audio = *op.AudioTrack
	}
	if f.Probe != nil && audio >= len(f.Probe.AudioTracks) && len(f.Probe.AudioTracks) > 0 {
		audio = 0 // unbekannte Spur-ID → erste Spur
	}
	caps := capsFromRequest(c, op.Device)
	d := stream.Decide(f.Probe, f.Path, caps)
	hasFF := c.S.HLS != nil && c.S.HLS.FFmpeg != ""
	switch format {
	case "raw":
		d = stream.Decision{Mode: stream.ModeDirect, Reason: "raw angefordert"}
		if f.Probe != nil && f.Probe.Duration > 0 && resumePoint(c, f.ID, f.Probe.Duration) > 0 {
			log.Printf("wiedergabe: App spielt die Originaldatei direkt (raw) – hier bestimmt die App die Startstelle, Fortsetzen durch den Server nicht möglich")
		}
	case "hls":
		// Die App fordert „hls“ (Umwandlung) bei HEVC/>1080p oder Qualitätsprofilen an.
		d = transcodeFor(f.Probe, op.Profile, caps)
	default: // hls_remux / webm_remux: echtes HLS liefern, Video wenn möglich kopieren
		if d.Mode == stream.ModeDirect {
			d = stream.Decision{Mode: stream.ModeRemux, CopyVideo: true, CopyAudio: true,
				HEVC: f.Probe != nil && f.Probe.VideoCodec == "hevc", Reason: "HLS angefordert, Video kopiert"}
			if f.Probe != nil && f.Probe.VideoCodec != "h264" && f.Probe.VideoCodec != "hevc" {
				d = transcodeFor(f.Probe, "", caps)
			}
		}
	}
	d.Client = caps.Name
	if d.Mode != stream.ModeDirect {
		// Ton der gewählten Spur: unverändert übernehmen, wo HLS es erlaubt
		// (App-Wunsch „ac3_copy“ eingeschlossen), sonst umwandeln.
		codec := ""
		if f.Probe != nil {
			codec = f.Probe.AudioCodec
			if audio < len(f.Probe.AudioTracks) {
				codec = f.Probe.AudioTracks[audio].Codec
			}
		}
		d.CopyAudio = codec == "" || stream.HLSAudioCopy(codec, caps) ||
			(op.AudioFormat == "ac3_copy" && (codec == "ac3" || codec == "eac3"))
		if hasFF && !d.CopyAudio && !c.S.HLS.CanConvertAudio(caps.Name) {
			msg := "Ton " + codec + " müsste umgewandelt werden, das gefundene FFmpeg (" + c.S.HLS.FFmpeg + ") hat dafür keinen Encoder"
			log.Printf("streaming: %s – Abhilfe: Paket „ffmpeg7“ (SynoCommunity) installieren", msg)
			c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, msg, nil)
			webapi.Fail(c.W, webapi.ErrVSNoStreamFormat)
			return
		}
		if hasFF && d.Mode == stream.ModeTranscode && !c.S.HLS.CanEncodeH264() && f.Probe != nil &&
			(f.Probe.VideoCodec == "h264" || (f.Probe.VideoCodec == "hevc" && caps.Video["hevc"])) {
			// Kleinere Qualität angefordert, aber kein H.264-Encoder: Original
			// umverpackt liefern statt abzubrechen (höhere Datenrate, läuft aber).
			log.Printf("streaming: %s angefordert, kein H.264-Encoder – liefere Originalqualität", d.Reason)
			d.Mode, d.CopyVideo, d.MaxHeight, d.HEVC = stream.ModeRemux, true, 0, f.Probe.VideoCodec == "hevc"
			d.Reason = "Originalqualität (kein H.264-Encoder für " + op.Profile + ")"
		}
		if hasFF && d.Mode == stream.ModeTranscode && !c.S.HLS.CanEncodeH264() {
			msg := "Video " + d.Reason + ": das gefundene FFmpeg (" + c.S.HLS.FFmpeg + ") hat keinen H.264-Encoder"
			log.Printf("streaming: %s – Abhilfe: Paket „ffmpeg7“ (SynoCommunity) installieren", msg)
			c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, msg, nil)
			webapi.Fail(c.W, webapi.ErrVSNoStreamFormat)
			return
		}
	}
	st := c.S.Streams.Open(c.Sess.Account, f.ID, f.Path, format, op.Device)
	if d.Mode != stream.ModeDirect {
		if !hasFF {
			if format != "hls" && stream.Decide(f.Probe, f.Path, caps).Mode == stream.ModeDirect {
				// Ohne FFmpeg: Originaldatei (AVPlayer/ExoPlayer erkennen MP4 selbst).
				log.Printf("streaming: FFmpeg fehlt – liefere Originaldatei für %s", filepath.Base(f.Path))
				webapi.OK(c.W, map[string]any{"format": format, "stream_id": st.ID})
				return
			}
			c.S.Streams.Close(st.ID, c.Sess.Account)
			c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, "Umwandlung nötig ("+d.Reason+"), aber FFmpeg fehlt", nil)
			webapi.Fail(c.W, webapi.ErrVSNoStreamFormat)
			return
		}
		resume := 0.0
		if f.Probe != nil && f.Probe.Duration > 0 {
			d.Duration = f.Probe.Duration // durchsuchbare Liste: Fortsetzen/Vorspulen
			resume = resumePoint(c, f.ID, d.Duration)
		}
		job, err := c.S.HLS.Start(c.Sess.Account, f.Path, d, resume, audio)
		if err != nil {
			c.S.Streams.Close(st.ID, c.Sess.Account)
			log.Printf("streaming: %v", err)
			c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, "Umwandlung fehlgeschlagen: "+err.Error(), nil)
			webapi.Fail(c.W, webapi.ErrVSNoStreamFormat)
			return
		}
		c.S.Streams.Attach(st.ID, job.ID)
		c.S.Conns().Play(c.Sess.Key, job.ID, "")
		if job.ResumeAt > 0 {
			log.Printf("wiedergabe: Fortsetzen bei %s (zuletzt gesehen), Zurückspulen an den Anfang ist möglich", hms(resume))
		}
	}
	c.S.Conns().Play(c.Sess.Key, "", playTitle(c, f))
	log.Printf("streaming: %s öffnet %q (%s, Gerät %q, UA %q) → %s (%s)", c.Sess.Account, filepath.Base(f.Path), format, op.Device,
		truncateUA(c.Req.HTTP.UserAgent()), d.Mode, d.Reason)
	webapi.OK(c.W, map[string]any{"format": format, "stream_id": st.ID})
}

// playTitle: Anzeigename für die Verbindungsliste.
func playTitle(c *Ctx, f library.File) string {
	if it, ok := c.Lib.Item(f.ItemID); ok {
		if it.Kind == "episode" {
			return fmt.Sprintf("%s S%02dE%02d", it.Show, it.Season, it.Episode)
		}
		return it.DisplayTitle()
	}
	return filepath.Base(f.Path)
}

func truncateUA(s string) string {
	if len(s) > 60 {
		return s[:60]
	}
	return s
}

// transcodeFor: H.264-Umwandlung; Profile der App (z.B. hd_medium, sd_low)
// begrenzen die Auflösung.
func transcodeFor(p *library.Probe, profile string, caps stream.Caps) stream.Decision {
	d := stream.Decision{Mode: stream.ModeTranscode, CopyAudio: p != nil && p.AudioCodec == "aac", Reason: "Umwandlung nach H.264"}
	if p != nil && p.VideoCodec == "h264" && p.Height > 0 && p.Height <= 1080 && profile == "" {
		d = stream.Decision{Mode: stream.ModeRemux, CopyVideo: true, CopyAudio: p.AudioCodec == "aac", Reason: "H.264 ≤1080p – nur umverpackt"}
	}
	switch {
	case strings.Contains(profile, "low"):
		d.MaxHeight = 480
	case strings.Contains(profile, "medium"):
		d.MaxHeight = 720
	}
	if d.MaxHeight > 0 {
		d.Mode, d.CopyVideo, d.Reason = stream.ModeTranscode, false, "Profil "+profile+" → H.264 "+strconv.Itoa(d.MaxHeight)+"p"
	}
	return d
}

func streamStream(c *Ctx) {
	id := c.Req.Str("id")
	if c.Blocked {
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	st, err := c.S.Streams.Get(id, c.Sess.Account)
	if err != nil {
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	if st.HLSJob != "" && c.S.HLS != nil {
		if j, ok := c.S.HLS.Get(st.HLSJob); ok {
			c.S.HLS.ServeFile(c.W, c.Req.HTTP, j, "index.m3u8", HLSPrefix(c.Req.HTTP, j.ID))
			return
		}
	}
	stream.ServeRaw(c.W, c.Req.HTTP, st.Path)
}

func streamClose(c *Ctx) {
	id := c.Req.Str("id")
	if st, err := c.S.Streams.Get(id, c.Sess.Account); err == nil && st.HLSJob != "" && c.S.HLS != nil {
		c.S.HLS.Stop(st.HLSJob)
	}
	c.S.Streams.Close(id, c.Sess.Account)
	webapi.OK(c.W, nil)
}

// ---------- Wiedergabeposition ----------

// resumePoint: Startstelle beim Öffnen. Die iOS-App fragt die gespeicherte
// Position nie ab (kein WatchStatus getinfo), meldet sie aber laufend
// (setinfo). Deshalb setzt der Server die HLS-Startstelle selbst
// (#EXT-X-START), so wie die Android-App nach getinfo fortsetzt. Nicht bei
// weniger als 30 s Laufzeit oder in den letzten 3 Minuten (Abspann).
func resumePoint(c *Ctx, fileID int, duration float64) float64 {
	ws, ok := c.Lib.Watch(c.Sess.Account, fileID)
	if !ok || ws.Position < 30 || float64(ws.Position) > duration-180 {
		return 0
	}
	return float64(ws.Position)
}

func watchSet(c *Ctx) {
	id := c.Req.Int("id", -1)
	pos := c.Req.Int("position", -1)
	if id < 0 || pos < 0 {
		c.S.Rec.Record(c.Req.API, c.Req.Version, c.Req.Method, "Parameter abweichend (id/position)", paramKeys(c.Req.Params))
		webapi.Fail(c.W, webapi.ErrNoParameter)
		return
	}
	if err := c.Lib.SetWatch(c.Sess.Account, id, pos); err != nil {
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	log.Printf("wiedergabe: Position %ds für Datei %d gespeichert (Konto %q)", pos, id, c.Sess.Account)
	webapi.OK(c.W, nil)
}

func watchGet(c *Ctx) {
	id := c.Req.Int("id", -1)
	ws, _ := c.Lib.Watch(c.Sess.Account, id)
	log.Printf("wiedergabe: Position %ds für Datei %d abgefragt (Konto %q)", ws.Position, id, c.Sess.Account)
	// Zahl statt Text: Die Android-App liest beides (Gson), die iOS-App
	// erwartet sehr wahrscheinlich eine Zahl.
	webapi.OK(c.W, map[string]any{"watch_status": map[string]any{"position": ws.Position, "last_update": ws.UpdatedAt}})
}

// ---------- Audiospuren ----------

func fileWithProbe(c *Ctx, id int) (library.File, bool) {
	if id >= stream.ProducedBase {
		f, t, ok := producedFile(c, id)
		if ok && t.OutProbe == nil && c.S.FFprobe != "" {
			f.Probe = library.RunProbe(c.S.FFprobe, f.Path)
		}
		return f, ok
	}
	f, ok := c.Lib.File(id)
	if !ok {
		return f, false
	}
	if f.Probe.NeedsProbe(c.S.FFprobe) || f.Probe.NeedsProfiles(c.S.FFprobe) {
		f.Probe = library.RunProbe(c.S.FFprobe, f.Path)
		c.Lib.SetProbe(f.ID, f.Probe)
	}
	return f, true
}

// trackInfo: SYNO.VideoStation2.File get_track_info (v1/v2) bzw.
// SYNO.VideoStation.Video get_track_info (v3). Belegt aus DS video für
// Android TV 1.1.8 (TrackInfoVo: video[0].profile, verglichen mit
// „High 10“); die iOS-App prüft vor dem Herunterladen Profil und
// Audioformat (H.264/MPEG-4 ohne Hi10, AAC-LC). Weitere Felder: Hypothese.
func trackInfo(c *Ctx) {
	f, ok := fileWithProbe(c, c.Req.Int("id", -1))
	if !ok {
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	pr := f.Probe
	if pr == nil {
		pr = &library.Probe{}
	}
	video := []map[string]any{}
	if pr.VideoCodec != "" {
		video = append(video, map[string]any{"codec": pr.VideoCodec, "profile": pr.VideoProfile, "pix_fmt": pr.PixFmt,
			"width": pr.Width, "height": pr.Height, "bitrate": pr.VideoBitrate, "frame_rate": pr.FrameRate})
	}
	audio := []map[string]any{}
	for _, t := range pr.AudioTracks {
		audio = append(audio, map[string]any{"id": strconv.Itoa(t.Index), "codec": t.Codec, "profile": t.Profile,
			"channel": t.Channels, "language": t.Language, "track": trackLabel(t)})
	}
	webapi.OK(c.W, map[string]any{"video": video, "audio": audio})
}

// fileDownload: SYNO.VideoStation2.File download (v1/v2) bzw.
// SYNO.VideoStation.Video download – die Originaldatei zum Herunterladen
// (Offline-Wiedergabe in der App). URL-Aufbau belegt aus DS video Android
// (ConnectionManager: …/entry.cgi?api=SYNO.VideoStation2.File&version=2&method=download&id=…).
func fileDownload(c *Ctx) {
	id := c.Req.Int("id", -1)
	if !c.S.MayDownload(c.Sess) {
		log.Printf("download: Konto %q darf nicht herunterladen (Benutzerrechte → Herunterladen)", c.Sess.Account)
		c.W.Header().Set("Content-Type", "application/json")
		c.W.WriteHeader(http.StatusForbidden)
		_, _ = c.W.Write([]byte(`{"error":{"code":105},"success":false}`))
		return
	}
	f, ok := c.Lib.File(id)
	name := filepath.Base(f.Path)
	if id >= stream.ProducedBase { // Ergebnis einer Offline-Umwandlung
		var t stream.OfflineTask
		if f, t, ok = producedFile(c, id); ok {
			name = strings.TrimSuffix(filepath.Base(t.Src.Path), filepath.Ext(t.Src.Path)) + ".mp4"
		}
	}
	if !ok {
		// HTTP-Fehler statt „success:false“ mit Status 200: sonst speichert die
		// App die Fehlermeldung als Videodatei („Öffnen fehlgeschlagen“).
		log.Printf("download: Datei %d nicht gefunden oder kein Zugriff (%s)", id, c.Sess.Account)
		c.W.Header().Set("Content-Type", "application/json")
		c.W.WriteHeader(http.StatusNotFound)
		_, _ = c.W.Write([]byte(`{"error":{"code":100},"success":false}`))
		return
	}
	c.W.Header().Set("Content-Disposition", "attachment; filename=\""+asciiName(name)+"\"; filename*=UTF-8''"+url.PathEscape(name))
	log.Printf("download: %s als „%s“ (%.1f MB, %s)", f.Path, name, float64(f.Size)/1e6, c.Sess.Account)
	c.S.Conns().Play(c.Sess.Key, "", "Download: "+playTitle(c, f))
	stream.ServeRaw(c.W, c.Req.HTTP, f.Path)
}

// asciiName: Ersatzname für alte Clients (nur druckbares ASCII, ohne ").
func asciiName(s string) string {
	b := []byte{}
	for _, r := range s {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' {
			b = append(b, byte(r))
		} else {
			b = append(b, '_')
		}
	}
	return string(b)
}

var langNames = map[string]string{"ger": "Deutsch", "deu": "Deutsch", "de": "Deutsch", "eng": "Englisch", "en": "Englisch",
	"fre": "Französisch", "fra": "Französisch", "fr": "Französisch", "spa": "Spanisch", "es": "Spanisch", "ita": "Italienisch",
	"it": "Italienisch", "jpn": "Japanisch", "ja": "Japanisch", "tur": "Türkisch", "tr": "Türkisch", "rus": "Russisch", "pol": "Polnisch",
	"nld": "Niederländisch", "dut": "Niederländisch"}

func trackLabel(t library.StreamTrack) string {
	parts := []string{}
	if n := langNames[strings.ToLower(t.Language)]; n != "" {
		parts = append(parts, n)
	} else if t.Language != "" && t.Language != "und" {
		parts = append(parts, t.Language)
	}
	if t.Title != "" {
		parts = append(parts, t.Title)
	}
	parts = append(parts, strings.ToUpper(t.Codec))
	switch t.Channels {
	case 0:
	case 1:
		parts = append(parts, "Mono")
	case 2:
		parts = append(parts, "Stereo")
	case 6:
		parts = append(parts, "5.1")
	case 8:
		parts = append(parts, "7.1")
	default:
		parts = append(parts, strconv.Itoa(t.Channels)+" Kanäle")
	}
	return strings.Join(parts, " · ")
}

func audioTrackList(c *Ctx) {
	f, ok := fileWithProbe(c, c.Req.Int("id", -1))
	if !ok {
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	out := []map[string]any{}
	if f.Probe != nil {
		for _, t := range f.Probe.AudioTracks {
			out = append(out, map[string]any{"id": strconv.Itoa(t.Index), "codec": t.Codec, "channel": t.Channels,
				"language": t.Language, "track": trackLabel(t)})
		}
	}
	webapi.OK(c.W, map[string]any{"trackinfo": out})
}

func playbackGet(c *Ctx) {
	// Zusätzlich die letzte Position (zusätzliche Felder stören die Apps nicht;
	// die iOS-App fragt WatchStatus nicht ab, sondern nur diese Einstellung).
	ws, _ := c.Lib.Watch(c.Sess.Account, c.Req.Int("id", -1))
	webapi.OK(c.W, map[string]any{"audio_track": 0, "subtitle_id": "", "position": ws.Position,
		"watch_status": map[string]any{"position": ws.Position, "last_update": ws.UpdatedAt}})
}

// ---------- Untertitel ----------

type subEntry struct {
	ID       string
	Title    string
	Lang     string
	Format   string
	Embedded bool
	Path     string // extern
	Index    int    // eingebettet
}

var subExts = map[string]string{".srt": "srt", ".vtt": "vtt", ".ass": "ass", ".ssa": "ass"}

// textSubCodecs: eingebettete Textuntertitel, die FFmpeg nach SRT wandeln kann.
var textSubCodecs = map[string]bool{"subrip": true, "ass": true, "ssa": true, "mov_text": true, "webvtt": true, "text": true}

func subtitlesFor(f library.File) []subEntry {
	var out []subEntry
	dir := filepath.Dir(f.Path)
	base := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		format, ok := subExts[strings.ToLower(filepath.Ext(name))]
		if !ok || e.IsDir() || !strings.HasPrefix(name, base) {
			continue
		}
		middle := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(name, base), filepath.Ext(name)), ". _-")
		lang := ""
		for _, part := range strings.FieldsFunc(middle, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == ' ' }) {
			if _, ok := langNames[strings.ToLower(part)]; ok {
				lang = strings.ToLower(part)
			}
		}
		title := middle
		if n := langNames[lang]; n != "" {
			title = n
			if i := strings.Index(strings.ToLower(middle), lang); i >= 0 {
				if rest := strings.Trim(middle[:i]+middle[i+len(lang):], ". _-"); rest != "" {
					title += " (" + rest + ")" // z.B. „Deutsch (forced)“
				}
			}
		}
		if title == "" {
			title = name
		}
		out = append(out, subEntry{ID: "ext:" + base64.RawURLEncoding.EncodeToString([]byte(name)), Title: title, Lang: lang, Format: format, Path: filepath.Join(dir, name)})
	}
	if f.Probe != nil {
		for _, t := range f.Probe.Subtitles {
			if !textSubCodecs[t.Codec] {
				continue // Bilduntertitel (PGS/VobSub) nicht unterstützt
			}
			title := t.Title
			if title == "" {
				title = trackLabel(library.StreamTrack{Language: t.Language, Codec: t.Codec})
			}
			out = append(out, subEntry{ID: "emb:" + strconv.Itoa(t.Index), Title: title, Lang: t.Language, Format: "srt", Embedded: true, Index: t.Index})
		}
	}
	return out
}

func subtitleList(c *Ctx) {
	f, ok := fileWithProbe(c, subtitleFileID(c, c.Req.Int("id", -1)))
	if !ok {
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	out := []map[string]any{}
	for _, s := range subtitlesFor(f) {
		out = append(out, map[string]any{"id": s.ID, "title": s.Title, "lang": s.Lang, "format": s.Format, "embedded": s.Embedded, "need_preview": false})
	}
	webapi.OK(c.W, out)
}

func subtitleGet(c *Ctx) {
	f, ok := fileWithProbe(c, subtitleFileID(c, c.Req.Int("id", -1)))
	if !ok {
		http.NotFound(c.W, c.Req.HTTP)
		return
	}
	data, err := c.S.SubtitleText(f, c.Req.Str("subtitle_id"))
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(c.W, c.Req.HTTP)
		return
	}
	if err != nil {
		http.Error(c.W, "Untertitel nicht lesbar", http.StatusInternalServerError)
		return
	}
	c.W.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = c.W.Write(data)
}

// SubtitleTrack: Textuntertitel einer Datei (extern oder eingebettet).
type SubtitleTrack struct {
	ID       string // "ext:…" bzw. "emb:<n>"
	Title    string
	Lang     string
	Format   string // srt | vtt | ass
	Embedded bool
	Index    int // eingebettet: relative Untertitelspur
}

// Subtitles listet die Textuntertitel einer Datei (externe Dateien daneben
// und eingebettete Textspuren).
func Subtitles(f library.File) []SubtitleTrack {
	var out []SubtitleTrack
	for _, s := range subtitlesFor(f) {
		out = append(out, SubtitleTrack{ID: s.ID, Title: s.Title, Lang: s.Lang, Format: s.Format, Embedded: s.Embedded, Index: s.Index})
	}
	return out
}

// SubtitleText liefert einen Untertitel als UTF-8 (eingebettete Spuren und
// ASS als SRT über FFmpeg). Unbekannte id: os.ErrNotExist.
func (s *Server) SubtitleText(f library.File, id string) ([]byte, error) {
	for _, sub := range subtitlesFor(f) {
		if sub.ID != id {
			continue
		}
		var data []byte
		var err error
		switch {
		case sub.Embedded:
			data, err = s.ffmpegSRT(f.Path, "0:s:"+strconv.Itoa(sub.Index))
		case sub.Format == "ass":
			if data, err = s.ffmpegSRT(sub.Path, "0:s:0"); err != nil {
				data, err = os.ReadFile(sub.Path)
			}
		default:
			data, err = os.ReadFile(sub.Path)
		}
		if err != nil {
			return nil, err
		}
		return toUTF8(data), nil
	}
	return nil, os.ErrNotExist
}

func (s *Server) ffmpegSRT(src, mapping string) ([]byte, error) {
	if s.HLS == nil || s.HLS.FFmpeg == "" {
		return nil, stream.ErrNoFFmpeg
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, s.HLS.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", src, "-map", mapping, "-f", "srt", "-").Output()
}

// toUTF8: Zeichensatzkonvertierung – BOM entfernen, Windows-1252/Latin-1 → UTF-8.
func toUTF8(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(b) {
		return b
	}
	var cp1252 = map[byte]rune{0x80: '€', 0x84: '„', 0x85: '…', 0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”', 0x96: '–', 0x97: '—'}
	var sb strings.Builder
	for _, c := range b {
		if r, ok := cp1252[c]; ok {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return []byte(sb.String())
}
