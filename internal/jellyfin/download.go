package jellyfin

import (
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/library"
	"dsvideorevival/internal/stream"
)

// ---------- Bandbreitentest ----------

var (
	testOnce  sync.Once
	testBlock []byte
)

// bitrateTest: GET /Playback/BitrateTest?size=N – N zufällige Bytes, damit
// die App die Verbindung messen kann (wie bei Jellyfin).
func (h *Handler) bitrateTest(x *req) {
	testOnce.Do(func() {
		testBlock = make([]byte, 64<<10)
		_, _ = rand.Read(testBlock)
	})
	n := x.QInt("size", 102400)
	if n <= 0 || n > 20_000_000 {
		n = 102400
	}
	w := x.w
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(n))
	for n > 0 {
		k := min(n, len(testBlock))
		if _, err := w.Write(testBlock[:k]); err != nil {
			return
		}
		n -= k
	}
}

// ---------- Herunterladen mit Umwandlung (Mediathek der App „Jellyfin“) ----------
//
// Die App lädt Dateien herunter und spielt sie offline mit dem Apple-Player –
// MKV & Co. kann der nicht. Dafür nutzt die Mediathek die Offline-Umwandlung
// der DS-video-Apps in der Stufe „apple“: MP4, H.264 und HEVC (auch 4K/HDR)
// nur umverpackt, sonst H.264; Ton bleibt, wenn der Apple-Player ihn kann:
//   POST /DsvrOffline/{itemId}        Umwandlung anlegen (oder vorhandene)
//   GET  /DsvrOffline/{itemId}        Stand der Umwandlung
//   GET  /DsvrOffline/File/{taskId}   fertige MP4-Datei

func (h *Handler) offlineState(x *req, itemID string, t stream.OfflineTask) map[string]any {
	return map[string]any{"Id": t.ID, "State": t.State, "Progress": t.Progress, "TimeLeft": t.TimeLeft,
		"Size": t.OutSize, "Mode": t.Mode, "Error": t.ErrMsg, "Transfer": h.transferState(x, itemID)}
}

// offlineTask: neueste sichtbare Aufgabe des Kontos für die Datei (profile ""
// = jede Stufe).
func (h *Handler) offlineTask(account string, fileID int, profile string) (stream.OfflineTask, bool) {
	var best stream.OfflineTask
	found := false
	for _, t := range h.S.Offline.List(account) {
		if t.FileID == fileID && (profile == "" || t.Profile == profile) && (!found || t.ID > best.ID) {
			best, found = t, true
		}
	}
	return best, found
}

func (h *Handler) offline(x *req, itemID string) {
	if !h.S.MayDownload(x.sess) {
		jsonError(x.w, http.StatusForbidden, "Herunterladen nicht erlaubt")
		return
	}
	if h.S.Offline == nil || !h.S.Offline.Available() {
		jsonError(x.w, http.StatusServiceUnavailable, "Umwandlung nicht möglich (FFmpeg mit AAC fehlt)")
		return
	}
	f, it, ok := h.fileFor(x, itemID, x.Q("MediaSourceId"))
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	itemID = makeID(kItem, uint64(it.ID))
	if x.r.Method != http.MethodPost {
		if t, ok := h.offlineTask(x.sess.Account, f.ID, "apple"); ok {
			writeJSON(x.w, h.offlineState(x, itemID, t))
		} else {
			writeJSON(x.w, map[string]any{"State": "", "Transfer": h.transferState(x, itemID)})
		}
		return
	}
	if f.Probe.NeedsProbe(h.S.FFprobe) && h.S.FFprobe != "" {
		f.Probe = library.RunProbe(h.S.FFprobe, f.Path)
		h.S.Lib.SetProbe(f.ID, f.Probe)
	}
	pr := f.Probe
	if pr == nil {
		pr = &library.Probe{}
	}
	src := stream.OfflineSource{Path: f.Path, Duration: pr.Duration, Height: pr.Height, VideoCodec: pr.VideoCodec,
		VideoProfile: pr.VideoProfile, PixFmt: pr.PixFmt, VideoBitrate: pr.VideoBitrate, AudioCodec: pr.AudioCodec, Origin: "jellyfin"}
	for _, t := range pr.AudioTracks {
		if t.Index == 0 {
			src.AudioCodec, src.AudioProfile = t.Codec, t.Profile
		}
	}
	t, created, err := h.S.Offline.Create(x.sess.Account, f.ID, "", "apple", src)
	if err != nil {
		log.Printf("jellyfin: %s nicht umwandelbar: %v", filepath.Base(f.Path), err)
		jsonError(x.w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if created {
		log.Printf("jellyfin: Umwandlung zum Herunterladen angelegt (Aufgabe %d): %s (Konto %q)", t.ID, filepath.Base(f.Path), x.sess.Account)
	}
	writeJSON(x.w, h.offlineState(x, itemID, t))
}

func (h *Handler) offlineFile(x *req, idStr string) {
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.NotFound(x.w, x.r)
		return
	}
	h.serveOffline(x, id)
}

// doneOffline: fertige Umwandlung, die dieses Konto abrufen darf.
func (h *Handler) doneOffline(x *req, id int) (stream.OfflineTask, library.File, bool) {
	if h.S.Offline == nil || !h.S.MayDownload(x.sess) {
		return stream.OfflineTask{}, library.File{}, false
	}
	t, ok := h.S.Offline.Get(id)
	if !ok || t.State != stream.OfflineDone || t.Out == "" || (t.Owner != x.sess.Account && !h.S.IsAppAdmin(x.sess)) {
		return stream.OfflineTask{}, library.File{}, false
	}
	f, ok := x.view.File(t.FileID) // Zugriff auf die Quelle weiterhin nötig
	if !ok {
		return stream.OfflineTask{}, library.File{}, false
	}
	if _, err := os.Stat(t.Out); err != nil {
		return stream.OfflineTask{}, library.File{}, false
	}
	return t, f, true
}

// serveOffline liefert die fertige MP4-Datei einer Umwandlung (mit Zählung
// der Übertragung, siehe transfer.go).
func (h *Handler) serveOffline(x *req, id int) {
	t, f, ok := h.doneOffline(x, id)
	if !ok {
		http.NotFound(x.w, x.r)
		return
	}
	fh, err := os.Open(t.Out)
	if err != nil {
		http.NotFound(x.w, x.r)
		return
	}
	defer fh.Close()
	st, _ := fh.Stat()
	name := strings.TrimSuffix(filepath.Base(t.Src.Path), filepath.Ext(t.Src.Path)) + ".mp4"
	x.w.Header().Set("Content-Type", "video/mp4")
	x.w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", pathEscape(name)))
	if x.r.Header.Get("Range") == "" {
		log.Printf("jellyfin: Herunterladen (fürs iPhone vorbereitet) %s, Konto %q", name, x.sess.Account)
	}
	w, done := h.track(x, makeID(kItem, uint64(f.ItemID)), st.Size())
	defer done()
	http.ServeContent(w, x.r, name, st.ModTime().Add(0*time.Second), fh)
}

func pathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
