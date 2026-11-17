package vs

// Offline-Umwandlung: SYNO.VideoStation.OfflineConversion (Android) und
// SYNO.VideoStation2.OfflineConversion (iOS u. a.).
//
// Belegt aus DS video 3.4.7 (net/offline/*, vos/offline/*, VideoInfoActivity,
// OfflineConversionMonitorService, ConversionAdapter):
//   create  file_id, audio_id (optional), video_profile (high|medium|low)
//           → data{id}
//   list    additional=["produced_file"] → data{task[]}; Aufgabe:
//           id, setting{file_id, audio_id, audio_codec, video_profile,
//           additional{file, produced_file, video}}, status{id, state
//           (wait|process|done|error|stop), progress (0…1), time_left (s,
//           <0 unbekannt), error, owner, owner_name, need_codecs[]}
//   stop/restart/delete  id=[…] → data{errors[{id, error}]}
//   stop_all/restart_all
//   Fertig: Die App lädt setting.additional.produced_file über
//   SYNO.VideoStation2.File download (id = produced_file.id) herunter und
//   löscht danach die Aufgabe.
// Die iOS-App ist nicht dekompiliert: gleiche Methoden laut Schema; die
// Aufrufe werden protokolliert (offline: …), Parameternamen-Varianten werden
// angenommen.

import (
	"errors"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"dsvideorevival/internal/library"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/webapi"
)

// offlineAvailable: Umwandlung möglich und für dieses Konto erlaubt
// (Info-Privileg offline_conversion; ohne Download-Recht aus).
func offlineAvailable(c *Ctx) bool { return c.S.Offline.Available() && c.S.MayDownload(c.Sess) }

// offlineIDs: id als JSON-Liste von Zahlen oder Texten, Einzelwert oder Komma-Liste.
func offlineIDs(c *Ctx) []int {
	if ids := c.Req.IntList("id"); len(ids) > 0 {
		return ids
	}
	var out []int
	for _, s := range c.Req.StrList("id") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func firstParam(c *Ctx, keys ...string) string {
	for _, k := range keys {
		v := strings.TrimSpace(c.Req.Str(k))
		if strings.HasPrefix(v, "[") {
			if l := c.Req.StrList(k); len(l) > 0 {
				v = l[0]
			}
		}
		if v != "" {
			return v
		}
	}
	return ""
}

func offlineCreate(c *Ctx) {
	log.Printf("offline: %s %s (Konto %q) %s", c.Req.API, c.Req.Method, c.Sess.Account, paramDump(c))
	if !c.S.MayDownload(c.Sess) {
		log.Printf("offline: Konto %q darf nicht herunterladen (Benutzerrechte → Herunterladen)", c.Sess.Account)
		webapi.Fail(c.W, webapi.ErrPermission)
		return
	}
	if !c.S.Offline.Available() {
		log.Printf("offline: Umwandlung nicht möglich – FFmpeg mit AAC-Encoder fehlt")
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	id, err := strconv.Atoi(firstParam(c, "file_id", "id"))
	if err != nil {
		webapi.Fail(c.W, webapi.ErrNoParameter)
		return
	}
	f, ok := fileWithProbe(c, id)
	if !ok || id >= stream.ProducedBase {
		webapi.Fail(c.W, webapi.ErrUnknown)
		return
	}
	pr := f.Probe
	if pr == nil {
		pr = &library.Probe{}
	}
	audioID := firstParam(c, "audio_id", "audio_track", "audio")
	src := stream.OfflineSource{Path: f.Path, Duration: pr.Duration, Height: pr.Height, VideoCodec: pr.VideoCodec,
		VideoProfile: pr.VideoProfile, PixFmt: pr.PixFmt, VideoBitrate: pr.VideoBitrate,
		AudioCodec: pr.AudioCodec}
	if n, err := strconv.Atoi(audioID); err == nil && n >= 0 {
		src.AudioIndex = n
	}
	for _, t := range pr.AudioTracks {
		if t.Index == src.AudioIndex {
			src.AudioCodec, src.AudioProfile = t.Codec, t.Profile
		}
	}
	profile := firstParam(c, "video_profile", "profile", "quality")
	t, created, err := c.S.Offline.Create(c.Sess.Account, f.ID, audioID, profile, src)
	if err != nil {
		log.Printf("offline: %s nicht umwandelbar: %v", filepath.Base(f.Path), err)
		code := webapi.ErrUnknown
		if errors.Is(err, stream.ErrOfflineVideo) {
			code = webapi.ErrVSNoStreamFormat
		}
		webapi.Fail(c.W, code)
		return
	}
	if created {
		log.Printf("offline: Aufgabe %d angelegt: %s (Stufe %s, Tonspur %q, Konto %s)", t.ID, filepath.Base(f.Path), t.Profile, audioID, c.Sess.Account)
	} else {
		log.Printf("offline: Aufgabe %d besteht schon (%s, %s)", t.ID, filepath.Base(f.Path), t.State)
	}
	webapi.OK(c.W, map[string]any{"id": t.ID})
}

// producedLibFile: erzeugte Datei als Dateiobjekt.
func producedLibFile(c *Ctx, t stream.OfflineTask) library.File {
	f := library.File{ID: t.ProducedID(), ItemID: 0, Path: t.Out, Size: t.OutSize, Probe: t.OutProbe}
	if src, ok := c.Lib.File(t.FileID); ok {
		f.ItemID = src.ItemID
		if f.Probe == nil && src.Probe != nil { // ohne Analyse: Werte der Quelle, Formate wie erzeugt
			p := *src.Probe
			p.Container, p.VideoCodec, p.AudioCodec, p.AudioTracks, p.Subtitles = "mp4", "h264", "aac", nil, nil
			f.Probe = &p
		}
	}
	return f
}

// producedFile: erzeugte Datei zur Datei-ID (nur Eigentümer oder Administrator,
// Zugriff auf die Quelldatei vorausgesetzt).
func producedFile(c *Ctx, id int) (library.File, stream.OfflineTask, bool) {
	t, ok := c.S.Offline.Produced(id)
	if !ok || (t.Owner != c.Sess.Account && !c.S.IsAppAdmin(c.Sess)) {
		return library.File{}, t, false
	}
	if _, ok := c.Lib.File(t.FileID); !ok {
		return library.File{}, t, false
	}
	return producedLibFile(c, t), t, true
}

// subtitleFileID: Untertitel einer erzeugten Datei kommen von der Quelldatei.
func subtitleFileID(c *Ctx, id int) int {
	if _, t, ok := producedFile(c, id); ok {
		return t.FileID
	}
	return id
}

func offlineTaskObject(c *Ctx, t stream.OfflineTask) map[string]any {
	add := map[string]any{}
	if f, ok := c.Lib.File(t.FileID); ok {
		add["file"] = fileObject(f, false)
		if it, ok := c.Lib.Item(f.ItemID); ok {
			v := videoObject(c, it, nil, false)
			v["type"] = kindType(it.Kind)
			add["video"] = v
		}
	}
	if t.State == stream.OfflineDone {
		add["produced_file"] = fileObject(producedLibFile(c, t), true)
	}
	return map[string]any{
		"id": t.ID,
		"setting": map[string]any{"file_id": t.FileID, "audio_id": t.AudioID, "audio_codec": "aac",
			"video_profile": t.Profile, "additional": add},
		"status": map[string]any{"id": t.ID, "state": t.State, "progress": t.Progress, "time_left": t.TimeLeft,
			"error": t.Error, "owner": t.Owner, "owner_name": t.Owner, "need_codecs": []string{}},
	}
}

func offlineList(c *Ctx) {
	if c.S.LogCalls {
		log.Printf("offline: %s list (Konto %q) %s", c.Req.API, c.Sess.Account, paramDump(c))
	}
	tasks := []map[string]any{}
	if c.S.Offline != nil && !c.Blocked {
		for _, t := range c.S.Offline.List(c.Sess.Account) {
			tasks = append(tasks, offlineTaskObject(c, t))
		}
	}
	webapi.OK(c.W, map[string]any{"task": tasks})
}

// offlineOp: stop/restart/delete für id=[…].
func offlineOp(name string, op func(m *stream.OfflineManager, owner string, id int) bool) handlerFunc {
	return func(c *Ctx) {
		log.Printf("offline: %s %s (Konto %q) %s", c.Req.API, name, c.Sess.Account, paramDump(c))
		errs := []map[string]any{}
		for _, id := range offlineIDs(c) {
			if c.S.Offline == nil || !op(c.S.Offline, c.Sess.Account, id) {
				errs = append(errs, map[string]any{"id": strconv.Itoa(id), "error": webapi.ErrUnknown})
			}
		}
		webapi.OK(c.W, map[string]any{"errors": errs})
	}
}

// offlineAll: stop_all/restart_all für alle eigenen Aufgaben.
func offlineAll(name string, op func(m *stream.OfflineManager, owner string, id int) bool) handlerFunc {
	return func(c *Ctx) {
		log.Printf("offline: %s %s (Konto %q)", c.Req.API, name, c.Sess.Account)
		if c.S.Offline != nil {
			for _, t := range c.S.Offline.List(c.Sess.Account) {
				op(c.S.Offline, c.Sess.Account, t.ID)
			}
		}
		webapi.OK(c.W, nil)
	}
}

func offlineMethods() map[string]impl {
	stop := func(m *stream.OfflineManager, o string, id int) bool { return m.Stop(o, id) }
	restart := func(m *stream.OfflineManager, o string, id int) bool { return m.Restart(o, id) }
	del := func(m *stream.OfflineManager, o string, id int) bool { return m.Delete(o, id) }
	return map[string]impl{
		"create":      {offlineCreate, StTeilbelegt, "App: file_id, audio_id, video_profile → id; MP4 H.264/AAC-LC"},
		"list":        {offlineList, StTeilbelegt, "App-Modell OfflineConversionObject (task[])"},
		"stop":        {offlineOp("stop", stop), StTeilbelegt, "id=[…] → errors[]"},
		"restart":     {offlineOp("restart", restart), StTeilbelegt, "id=[…] → errors[]"},
		"delete":      {offlineOp("delete", del), StTeilbelegt, "id=[…] → errors[]; löscht die erzeugte Datei"},
		"stop_all":    {offlineAll("stop_all", stop), StTeilbelegt, "alle eigenen Aufgaben"},
		"restart_all": {offlineAll("restart_all", restart), StTeilbelegt, "alle eigenen Aufgaben"},
	}
}
