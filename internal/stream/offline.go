package stream

// Offline-Umwandlung (SYNO.VideoStation[2].OfflineConversion): wandelt eine
// Videodatei im Hintergrund in eine MP4-Datei (H.264 + AAC-LC), die die
// DS-video-Apps herunterladen und offline abspielen können – so wie die
// Offline-Konvertierung von Video Station.
//
// Eigene Festlegungen (nicht belegt): Bedeutung der Qualitätsstufen
// high/medium/low (höchstens 1080p/720p/480p), Ablage der erzeugten Dateien
// im Datenordner des Pakets, Aufräumen nach OfflineKeep, IDs der erzeugten
// Dateien (ProducedBase + Aufgaben-ID).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/library"
)

// Zustände laut App-Modell (OfflineConversionObject.OfflineConversionStatus).
const (
	OfflineWait    = "wait"
	OfflineProcess = "process"
	OfflineDone    = "done"
	OfflineError   = "error"
	OfflineStop    = "stop"
)

// ProducedBase: Datei-IDs der erzeugten Dateien (weit oberhalb der Bibliotheks-IDs).
const ProducedBase = 1_000_000_000

// OfflineKeep: so lange bleiben fertige/abgebrochene Aufgaben samt Datei liegen.
const OfflineKeep = 7 * 24 * time.Hour

// OfflineSource: Angaben zur Quelldatei, aus denen der Befehl entsteht.
type OfflineSource struct {
	Path         string  `json:"path"`
	Duration     float64 `json:"duration"`
	Height       int     `json:"height"`
	VideoCodec   string  `json:"video_codec"`
	VideoProfile string  `json:"video_profile,omitempty"`
	PixFmt       string  `json:"pix_fmt,omitempty"`
	VideoBitrate int64   `json:"video_bitrate,omitempty"`
	AudioIndex   int     `json:"audio_index"` // relative Audiospur
	AudioCodec   string  `json:"audio_codec,omitempty"`
	AudioProfile string  `json:"audio_profile,omitempty"`
}

// OfflineTask: eine Umwandlung.
type OfflineTask struct {
	ID       int            `json:"id"`
	Owner    string         `json:"owner"`
	FileID   int            `json:"file_id"`
	AudioID  string         `json:"audio_id"`
	Profile  string         `json:"profile"`
	Src      OfflineSource  `json:"src"`
	State    string         `json:"state"`
	Progress float64        `json:"progress"`  // 0…1
	TimeLeft int            `json:"time_left"` // Sekunden, -1 = unbekannt
	Error    int            `json:"error"`
	ErrMsg   string         `json:"err_msg,omitempty"`
	Out      string         `json:"out,omitempty"`
	OutSize  int64          `json:"out_size,omitempty"`
	OutProbe *library.Probe `json:"out_probe,omitempty"`
	Created  int64          `json:"created"`
	Finished int64          `json:"finished,omitempty"`
	Mode     string         `json:"mode,omitempty"` // z.B. „Video kopiert, Ton → AAC“
	// Hidden: fertige Aufgabe, die die App gelöscht hat. Die App löscht die
	// Aufgabe noch vor dem Herunterladen (iOS-Protokoll: delete, dann
	// File download der erzeugten Datei; Android ebenso) – die Datei bleibt
	// daher bis OfflineKeep erhalten, nur die Aufgabe verschwindet aus list.
	Hidden bool `json:"hidden,omitempty"`
}

// ProducedID: Datei-ID der erzeugten Datei.
func (t OfflineTask) ProducedID() int { return ProducedBase + t.ID }

// OfflineManager verwaltet die Aufgaben (eine Umwandlung zur Zeit).
type OfflineManager struct {
	HLS     *HLSManager // FFmpeg und Encoder (gemeinsam mit dem Streaming)
	Dir     string
	FFprobe func() string // Analyse der erzeugten Datei (optional)

	mu      sync.Mutex
	tasks   map[int]*OfflineTask
	nextID  int
	cancel  context.CancelFunc
	running int // ID der laufenden Aufgabe
	wake    chan struct{}
}

// NewOfflineManager lädt gespeicherte Aufgaben und startet die Abarbeitung.
func NewOfflineManager(hls *HLSManager, dir string) *OfflineManager {
	_ = os.MkdirAll(dir, 0o750)
	m := &OfflineManager{HLS: hls, Dir: dir, tasks: map[int]*OfflineTask{}, nextID: 1, wake: make(chan struct{}, 1)}
	m.load()
	go m.worker()
	go func() {
		for range time.Tick(time.Hour) {
			m.expire()
		}
	}()
	m.expire()
	return m
}

func (m *OfflineManager) stateFile() string { return filepath.Join(m.Dir, "tasks.json") }

func (m *OfflineManager) load() {
	b, err := os.ReadFile(m.stateFile())
	if err != nil {
		return
	}
	var list []*OfflineTask
	if json.Unmarshal(b, &list) != nil {
		return
	}
	for _, t := range list {
		if t.State == OfflineProcess { // Paket wurde während der Umwandlung beendet
			t.State, t.Progress, t.TimeLeft = OfflineWait, 0, -1
		}
		if t.State == OfflineDone {
			if st, err := os.Stat(t.Out); err != nil || st.Size() != t.OutSize {
				t.State, t.Error, t.ErrMsg = OfflineError, 100, "erzeugte Datei fehlt"
			}
		}
		m.tasks[t.ID] = t
		if t.ID >= m.nextID {
			m.nextID = t.ID + 1
		}
	}
	// Reste abgebrochener Läufe
	parts, _ := filepath.Glob(filepath.Join(m.Dir, "*.part.mp4"))
	for _, p := range parts {
		_ = os.Remove(p)
	}
}

// saveLocked schreibt die Aufgabenliste (m.mu gehalten).
func (m *OfflineManager) saveLocked() {
	list := make([]*OfflineTask, 0, len(m.tasks))
	for _, t := range m.tasks {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	b, _ := json.MarshalIndent(list, "", " ")
	tmp := m.stateFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o640) == nil {
		_ = os.Rename(tmp, m.stateFile())
	}
}

func (m *OfflineManager) poke() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Available: Umwandlung grundsätzlich möglich (FFmpeg mit AAC-Encoder).
func (m *OfflineManager) Available() bool { return m != nil && m.HLS != nil && m.HLS.CanEncodeAAC() }

// ErrOfflineVideo: Video müsste umgewandelt werden, kein H.264-Encoder.
var ErrOfflineVideo = errors.New("kein H.264-Encoder für die Umwandlung")

// NormProfile: Qualitätsstufe der App (high/medium/low; „original“ = high).
func NormProfile(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "medium":
		return "medium"
	case "low":
		return "low"
	}
	return "high"
}

type offlineLimits struct {
	height int
	rate   int64 // Bit/s Video
	audio  string
}

func limitsFor(profile string) offlineLimits {
	switch profile {
	case "medium":
		return offlineLimits{720, 3_000_000, "160k"}
	case "low":
		return offlineLimits{480, 1_200_000, "128k"}
	}
	return offlineLimits{1080, 8_000_000, "192k"}
}

// copyVideo: Videospur unverändert übernehmen (H.264 8 Bit 4:2:0 innerhalb der Stufe).
func copyVideo(s OfflineSource, l offlineLimits) bool {
	if s.VideoCodec != "h264" || s.Height <= 0 || s.Height > l.height {
		return false
	}
	p := strings.ToLower(s.VideoProfile)
	if strings.Contains(p, "10") || strings.Contains(p, "4:2:2") || strings.Contains(p, "4:4:4") {
		return false
	}
	if s.PixFmt != "" && s.PixFmt != "yuv420p" && s.PixFmt != "yuvj420p" {
		return false
	}
	// Höchste Stufe: Bitrate egal; sonst nur, wenn die Datei nicht deutlich größer wäre.
	return l.height >= 1080 || s.VideoBitrate == 0 || s.VideoBitrate <= l.rate*5/4
}

func copyAudio(s OfflineSource) bool {
	return s.AudioCodec == "aac" && strings.EqualFold(s.AudioProfile, "LC")
}

// NeedsVideoEncode: ob für diese Stufe ein H.264-Encoder nötig ist.
func NeedsVideoEncode(s OfflineSource, profile string) bool {
	return !copyVideo(s, limitsFor(NormProfile(profile)))
}

// Create legt eine Aufgabe an. Gibt es für Konto, Datei, Tonspur und Stufe
// schon eine offene oder fertige Aufgabe, wird deren ID geliefert.
func (m *OfflineManager) Create(owner string, fileID int, audioID, profile string, src OfflineSource) (OfflineTask, bool, error) {
	profile = NormProfile(profile)
	if !m.Available() {
		return OfflineTask{}, false, ErrNoAAC
	}
	if NeedsVideoEncode(src, profile) && !m.HLS.CanEncodeH264() {
		return OfflineTask{}, false, ErrOfflineVideo
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.Owner == owner && t.FileID == fileID && t.AudioID == audioID && t.Profile == profile &&
			t.State != OfflineError && t.State != OfflineStop {
			if t.Hidden { // erneut angefordert: fertige Datei wieder anbieten
				t.Hidden = false
				m.saveLocked()
			}
			return *t, false, nil
		}
	}
	t := &OfflineTask{ID: m.nextID, Owner: owner, FileID: fileID, AudioID: audioID, Profile: profile, Src: src,
		State: OfflineWait, TimeLeft: -1, Created: time.Now().Unix()}
	m.nextID++
	m.tasks[t.ID] = t
	m.saveLocked()
	m.poke()
	return *t, true, nil
}

// List: Aufgaben eines Kontos ("" = alle), älteste zuerst.
func (m *OfflineManager) List(owner string) []OfflineTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []OfflineTask
	for _, t := range m.tasks {
		if !t.Hidden && (owner == "" || t.Owner == owner) {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get liefert eine Aufgabe.
func (m *OfflineManager) Get(id int) (OfflineTask, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return OfflineTask{}, false
	}
	return *t, true
}

// Produced: fertige Aufgabe zur Datei-ID einer erzeugten Datei.
func (m *OfflineManager) Produced(fileID int) (OfflineTask, bool) {
	if m == nil || fileID <= ProducedBase {
		return OfflineTask{}, false
	}
	t, ok := m.Get(fileID - ProducedBase)
	if !ok || t.State != OfflineDone {
		return OfflineTask{}, false
	}
	return t, true
}

// Stop hält wartende/laufende Aufgaben an.
func (m *OfflineManager) Stop(owner string, id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || (owner != "" && t.Owner != owner) {
		return false
	}
	if t.State == OfflineWait || t.State == OfflineProcess {
		if m.running == id && m.cancel != nil {
			m.cancel()
		}
		t.State, t.Progress, t.TimeLeft = OfflineStop, 0, -1
		m.saveLocked()
	}
	return true
}

// Restart stellt angehaltene/fehlgeschlagene Aufgaben wieder an.
func (m *OfflineManager) Restart(owner string, id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || (owner != "" && t.Owner != owner) {
		return false
	}
	if t.State == OfflineStop || t.State == OfflineError {
		t.State, t.Progress, t.TimeLeft, t.Error, t.ErrMsg = OfflineWait, 0, -1, 0, ""
		m.saveLocked()
		m.poke()
	}
	return true
}

// Delete entfernt eine Aufgabe. Bei fertigen Aufgaben bleibt die erzeugte
// Datei zum Herunterladen erhalten (siehe Hidden), sonst wird alles entfernt.
func (m *OfflineManager) Delete(owner string, id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok || (owner != "" && t.Owner != owner) {
		return false
	}
	if t.State == OfflineDone {
		t.Hidden = true
	} else {
		m.removeLocked(t)
	}
	m.saveLocked()
	return true
}

func (m *OfflineManager) removeLocked(t *OfflineTask) {
	if m.running == t.ID && m.cancel != nil {
		m.cancel()
	}
	if t.Out != "" {
		_ = os.Remove(t.Out)
	}
	_ = os.Remove(m.partFile(t.ID))
	delete(m.tasks, t.ID)
}

// expire: alte fertige/abgebrochene Aufgaben samt Dateien entfernen.
func (m *OfflineManager) expire() {
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := time.Now().Add(-OfflineKeep).Unix()
	changed := false
	for _, t := range m.tasks {
		ts := t.Finished
		if ts == 0 {
			ts = t.Created
		}
		if (t.State == OfflineDone || t.State == OfflineError || t.State == OfflineStop) && ts < cut {
			log.Printf("offline: Aufgabe %d (%s, %s) nach %d Tagen entfernt", t.ID, filepath.Base(t.Src.Path), t.Owner, int(OfflineKeep.Hours()/24))
			m.removeLocked(t)
			changed = true
		}
	}
	if changed {
		m.saveLocked()
	}
}

func (m *OfflineManager) partFile(id int) string {
	return filepath.Join(m.Dir, strconv.Itoa(id)+".part.mp4")
}

func (m *OfflineManager) worker() {
	for {
		t := m.nextWaiting()
		if t == nil {
			<-m.wake
			continue
		}
		m.run(t)
	}
}

func (m *OfflineManager) nextWaiting() *OfflineTask {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *OfflineTask
	for _, t := range m.tasks {
		if t.State == OfflineWait && (best == nil || t.ID < best.ID) {
			best = t
		}
	}
	if best == nil {
		return nil
	}
	best.State, best.Progress, best.TimeLeft = OfflineProcess, 0, -1
	m.running = best.ID
	m.saveLocked()
	c := *best
	return &c
}

// update ändert die Aufgabe, solange sie noch existiert und läuft.
func (m *OfflineManager) update(id int, fn func(t *OfflineTask)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tasks[id]; ok && t.State == OfflineProcess {
		fn(t)
	}
}

func (m *OfflineManager) run(t *OfflineTask) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		m.cancel, m.running = nil, 0
		m.mu.Unlock()
	}()

	part := m.partFile(t.ID)
	_ = os.Remove(part)
	start := time.Now()
	hw := m.HLS.HWAccel != "off" && m.HLS.Encoders()["h264_vaapi"] && m.HLS.vaapi()
	args, mode, err := m.args(t, part, hw)
	if err == nil {
		log.Printf("offline: Aufgabe %d startet: %s → %s (%s, %s)", t.ID, filepath.Base(t.Src.Path), t.Profile, mode, t.Owner)
		err = m.ffmpeg(ctx, t, args, start)
		if err != nil && hw && ctx.Err() == nil && strings.Contains(mode, "VAAPI") {
			log.Printf("offline: Aufgabe %d: VAAPI fehlgeschlagen (%v) – erneut per Software", t.ID, err)
			if args, mode, err = m.args(t, part, false); err == nil {
				err = m.ffmpeg(ctx, t, args, start)
			}
		}
	}
	if ctx.Err() != nil { // angehalten oder gelöscht
		_ = os.Remove(part)
		log.Printf("offline: Aufgabe %d abgebrochen", t.ID)
		return
	}
	if err != nil {
		_ = os.Remove(part)
		log.Printf("offline: Aufgabe %d fehlgeschlagen: %v", t.ID, err)
		m.update(t.ID, func(x *OfflineTask) {
			x.State, x.Error, x.ErrMsg, x.TimeLeft, x.Finished = OfflineError, 100, err.Error(), -1, time.Now().Unix()
		})
		m.mu.Lock()
		m.saveLocked()
		m.mu.Unlock()
		return
	}
	out := filepath.Join(m.Dir, strconv.Itoa(t.ID)+".mp4")
	if err := os.Rename(part, out); err != nil {
		_ = os.Remove(part)
		m.update(t.ID, func(x *OfflineTask) {
			x.State, x.Error, x.ErrMsg, x.Finished = OfflineError, 100, err.Error(), time.Now().Unix()
		})
		m.mu.Lock()
		m.saveLocked()
		m.mu.Unlock()
		return
	}
	st, _ := os.Stat(out)
	var pr *library.Probe
	if m.FFprobe != nil {
		if tool := m.FFprobe(); tool != "" {
			pr = library.RunProbe(tool, out)
		}
	}
	keep := false
	m.update(t.ID, func(x *OfflineTask) {
		keep = true
		x.State, x.Progress, x.TimeLeft, x.Out, x.Finished, x.Mode, x.OutProbe = OfflineDone, 1, 0, out, time.Now().Unix(), mode, pr
		if st != nil {
			x.OutSize = st.Size()
		}
	})
	if !keep { // während des Abschlusses gelöscht
		_ = os.Remove(out)
		return
	}
	m.mu.Lock()
	m.saveLocked()
	m.mu.Unlock()
	size := int64(0)
	if st != nil {
		size = st.Size()
	}
	log.Printf("offline: Aufgabe %d fertig: %s (%.1f MB, %s)", t.ID, filepath.Base(t.Src.Path), float64(size)/1e6, time.Since(start).Round(time.Second))
}

// args baut den FFmpeg-Aufruf: MP4 mit H.264 (kopiert oder neu) und AAC-LC.
func (m *OfflineManager) args(t *OfflineTask, out string, hw bool) ([]string, string, error) {
	l := limitsFor(t.Profile)
	s := t.Src
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}
	cv := copyVideo(s, l)
	if hw && !cv {
		args = append(args, "-vaapi_device", "/dev/dri/renderD128")
	}
	args = append(args, "-i", s.Path, "-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", s.AudioIndex), "-sn", "-dn")
	var mode []string
	rate := strconv.FormatInt(l.rate, 10)
	switch {
	case cv:
		args = append(args, "-c:v", "copy")
		mode = append(mode, "Video kopiert")
	case hw:
		args = append(args, "-vf", fmt.Sprintf("format=nv12,hwupload,scale_vaapi=w=-2:h='min(ih,%d)'", l.height),
			"-c:v", "h264_vaapi", "-b:v", rate, "-maxrate", rate)
		mode = append(mode, fmt.Sprintf("Video → H.264 ≤%dp [VAAPI]", l.height))
	default:
		enc := m.HLS.softwareH264Args()
		if enc == nil {
			return nil, "", ErrOfflineVideo
		}
		args = append(args, "-vf", fmt.Sprintf("scale=-2:'min(ih,%d)'", l.height))
		args = append(args, offlineRate(enc, rate)...)
		mode = append(mode, fmt.Sprintf("Video → H.264 ≤%dp (%s)", l.height, enc[1]))
	}
	if copyAudio(s) {
		args = append(args, "-c:a", "copy")
		mode = append(mode, "Ton kopiert")
	} else {
		enc := m.HLS.aacEncoder()
		if enc == "" {
			return nil, "", ErrNoAAC
		}
		args = append(args, "-c:a", enc, "-ac", "2", "-b:a", l.audio)
		mode = append(mode, "Ton → AAC")
	}
	args = append(args, "-max_muxing_queue_size", "4096", "-movflags", "+faststart", "-f", "mp4",
		"-progress", "pipe:1", "-nostats", out)
	return args, strings.Join(mode, ", "), nil
}

// offlineRate passt die Streaming-Encoderparameter an die Stufe an
// (Bitrate; Schlüsselbildabstand frei wählen lassen).
func offlineRate(enc []string, rate string) []string {
	n, _ := strconv.ParseInt(rate, 10, 64)
	var out []string
	for i := 0; i < len(enc); i++ {
		switch enc[i] {
		case "-g", "-sc_threshold":
			i++
			continue
		case "-maxrate", "-b:v":
			out = append(out, enc[i], rate)
			i++
			continue
		case "-bufsize":
			out = append(out, enc[i], strconv.FormatInt(n*2, 10))
			i++
			continue
		}
		out = append(out, enc[i])
	}
	return out
}

func (m *OfflineManager) ffmpeg(ctx context.Context, t *OfflineTask, args []string, start time.Time) error {
	cmd := exec.CommandContext(ctx, m.HLS.FFmpeg, args...)
	logf, _ := os.Create(filepath.Join(m.Dir, strconv.Itoa(t.ID)+".log"))
	if logf != nil {
		defer logf.Close()
		cmd.Stderr = logf
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if os.Getenv("DSVR_DEBUG_FFMPEG") != "" {
		log.Printf("ffmpeg %s", strings.Join(args, " "))
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	last := time.Time{}
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || (k != "out_time_us" && k != "out_time_ms") || t.Src.Duration <= 0 {
			continue
		}
		us, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || us <= 0 || time.Since(last) < time.Second {
			continue
		}
		last = time.Now()
		p := float64(us) / 1e6 / t.Src.Duration
		if p > 0.999 {
			p = 0.999
		}
		left := -1
		if el := time.Since(start).Seconds(); p > 0.01 && el > 3 {
			left = int(el * (1 - p) / p)
		}
		m.update(t.ID, func(x *OfflineTask) { x.Progress, x.TimeLeft = p, left })
	}
	err = cmd.Wait()
	if err != nil && ctx.Err() == nil {
		if logf != nil {
			b, _ := os.ReadFile(logf.Name())
			if s := strings.TrimSpace(lastLines(string(b), 3)); s != "" {
				err = fmt.Errorf("%v: %s", err, s)
			}
		}
	}
	if err == nil && logf != nil {
		_ = os.Remove(logf.Name())
	}
	return err
}
