package stream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HLSManager runs FFmpeg jobs that produce fMP4-HLS (iOS/tvOS/Safari nativ,
// übrige Browser per hls.js).
type HLSManager struct {
	FFmpeg       string
	Dir          string // Arbeitsverzeichnis (auf dem Volume, nicht /tmp)
	MaxJobs      int
	HWAccel      string // "auto" | "vaapi" | "off"
	IdleTimeout  time.Duration
	mu           sync.Mutex
	jobs         map[string]*Job
	vaapiChecked bool
	vaapiOK      bool
	encoders     map[string]bool
	h264Checked  bool
	h264Args     []string // erprobter Software-H.264-Encoder (nil = keiner)
}

// Job is one running conversion.
type Job struct {
	ID       string
	Account  string
	Src      string
	Dir      string
	Decision Decision
	Start    float64
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	done     chan struct{}
	err      error
	lastUse  time.Time
	mu       sync.Mutex
	HW       bool
	TS       bool // Rückfall: MPEG-TS-Segmente statt fMP4 (ältere/eingeschränkte FFmpeg-Builds)

	// VOD: vollständige Liste (alle Segmente à SegLen) ab Start. Fehlt ein
	// angefordertes Segment weit vor/hinter der laufenden Umwandlung, startet
	// FFmpeg dort neu (-ss, -copyts, -start_number) – so funktionieren
	// Fortsetzen und Vorspulen an jede Stelle, wie bei Video Station.
	VOD        bool
	ResumeAt   float64 // >0: Startstelle der Liste (#EXT-X-START), z.B. Fortsetzen
	audioIndex int
	runStart   int        // erstes Segment des laufenden FFmpeg-Laufs
	gen        int        // Nummer des laufenden FFmpeg-Laufs (Dateipräfix g<N>-)
	seekMu     sync.Mutex // serialisiert Neustarts
	offs       map[int]map[uint32]int64
	genStart   map[int]int // erstes Segment je Lauf
	pick       map[int]int // Segment → Lauf, der es liefern soll (laufende Umwandlung)
	initNorm   []byte
}

// SegLen: Segmentlänge in Sekunden (VOD-Liste und FFmpeg -hls_time).
const SegLen = 6.0

func (j *Job) doneCh() chan struct{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done
}

// segCount: Anzahl Segmente der VOD-Liste.
func (j *Job) segCount() int {
	n := int(j.Decision.Duration / SegLen)
	if float64(n)*SegLen < j.Decision.Duration {
		n++
	}
	return n
}

// NewHLSManager creates the manager and starts the janitor.
func NewHLSManager(ffmpeg, dir string, maxJobs int) *HLSManager {
	if maxJobs <= 0 {
		maxJobs = 2
	}
	_ = os.RemoveAll(dir) // Reste eines Absturzes entfernen
	_ = os.MkdirAll(dir, 0o750)
	m := &HLSManager{FFmpeg: ffmpeg, Dir: dir, MaxJobs: maxJobs, HWAccel: "auto", IdleTimeout: 90 * time.Second, jobs: map[string]*Job{}}
	go func() {
		for range time.Tick(15 * time.Second) {
			m.reap()
		}
	}()
	return m
}

// FindFFmpeg locates ffmpeg. Ein eingetragener Pfad hat Vorrang; sonst wird
// unter allen gefundenen FFmpeg-Programmen das fähigste gewählt (AAC- und
// H.264-Encoder). Hintergrund: Synologys eigene Builds (/bin/ffmpeg,
// CodecPack „ffmpeg41“) haben oft keinen AAC- oder libx264-Encoder.
func FindFFmpeg(configured string) string {
	isExec := func(c string) bool {
		st, err := os.Stat(c)
		return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
	}
	if configured != "" && isExec(configured) {
		return configured
	}
	var cands []string
	for _, pk := range []string{"ffmpeg7", "ffmpeg6", "ffmpeg5", "ffmpeg"} {
		cands = append(cands, "/var/packages/"+pk+"/target/bin/ffmpeg", "/var/packages/"+pk+"/target/bin/"+pk)
	}
	cands = append(cands, "/usr/local/bin/ffmpeg7", "/usr/local/bin/ffmpeg")
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands, "/var/packages/CodecPack/target/bin/ffmpeg41", "/var/packages/CodecPack/target/pack/bin/ffmpeg41", "/usr/bin/ffmpeg", "/bin/ffmpeg")
	best, bestScore := "", -1
	seen := map[string]bool{}
	for _, c := range cands {
		if !isExec(c) {
			continue
		}
		real, err := filepath.EvalSymlinks(c)
		if err != nil {
			real = c
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		enc := ListEncoders(c)
		score := 0
		if enc["aac"] || enc["libfdk_aac"] {
			score += 4
		}
		if enc["libx264"] {
			score += 4
		} else if enc["libopenh264"] || enc["h264_v4l2m2m"] || enc["h264_vaapi"] {
			score++
		}
		log.Printf("ffmpeg-Kandidat %s: AAC=%v libx264=%v", c, enc["aac"] || enc["libfdk_aac"], enc["libx264"])
		if score > bestScore {
			best, bestScore = c, score
		}
	}
	return best
}

// ListEncoders runs „ffmpeg -encoders“ (Video und Audio).
func ListEncoders(ffmpeg string) map[string]bool {
	out := map[string]bool{}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-encoders").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && len(f[0]) == 6 && (f[0][0] == 'V' || f[0][0] == 'A') {
			out[f[1]] = true
		}
	}
	return out
}

// ErrNoFFmpeg: Umwandlung nicht möglich.
var ErrNoFFmpeg = errors.New("FFmpeg nicht gefunden – bitte Paket „ffmpeg7“ (SynoCommunity) installieren oder Pfad eintragen")

// ErrNoAAC: dieses FFmpeg kann kein AAC erzeugen.
var ErrNoAAC = errors.New("das gefundene FFmpeg hat keinen AAC-Encoder – bitte Paket „ffmpeg7“ (SynoCommunity) installieren")

// ErrNoH264: dieses FFmpeg kann kein H.264 erzeugen.
var ErrNoH264 = errors.New("das gefundene FFmpeg hat keinen H.264-Encoder – bitte Paket „ffmpeg7“ (SynoCommunity) installieren")

// ErrBusy: zu viele gleichzeitige Umwandlungen.
var ErrBusy = errors.New("zu viele gleichzeitige Umwandlungen")

func (m *HLSManager) vaapi() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.vaapiChecked {
		return m.vaapiOK
	}
	m.vaapiChecked = true
	if m.HWAccel == "off" {
		return false
	}
	f, err := os.OpenFile("/dev/dri/renderD128", os.O_RDWR, 0)
	if err != nil {
		log.Printf("hls: keine Hardware-Beschleunigung (/dev/dri/renderD128: %v) – Software-Umwandlung", err)
		return false
	}
	f.Close()
	// Selbsttest: 0,2 s Testbild mit h264_vaapi kodieren.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = exec.CommandContext(ctx, m.FFmpeg, "-hide_banner", "-loglevel", "error", "-vaapi_device", "/dev/dri/renderD128",
		"-f", "lavfi", "-i", "testsrc=duration=0.2:size=320x240:rate=25", "-vf", "format=nv12,hwupload", "-c:v", "h264_vaapi", "-f", "null", "-").Run()
	if err != nil {
		log.Printf("hls: VAAPI-Selbsttest fehlgeschlagen (%v) – Software-Umwandlung", err)
		return false
	}
	m.vaapiOK = true
	log.Printf("hls: VAAPI-Hardwarebeschleunigung aktiv")
	return true
}

// Start launches a job. audioIndex: relative Audiospur (0 = erste).
func (m *HLSManager) Start(account, src string, d Decision, start float64, audioIndex int) (*Job, error) {
	if m.FFmpeg == "" {
		return nil, ErrNoFFmpeg
	}
	m.mu.Lock()
	running := 0
	for _, j := range m.jobs {
		if !j.finished() && j.Decision.Mode == ModeTranscode {
			running++
		}
	}
	m.mu.Unlock()
	if d.Mode == ModeTranscode && running >= m.MaxJobs {
		return nil, ErrBusy
	}
	id := NewStreamID()
	j := &Job{ID: id, Account: account, Src: src, Dir: filepath.Join(m.Dir, id), Decision: d, Start: start, done: make(chan struct{}), lastUse: time.Now(),
		VOD: d.Duration > 0, audioIndex: audioIndex}
	if j.VOD && start > 0 {
		j.ResumeAt = start
		j.runStart = int(start / SegLen) // ab gewünschter Stelle vorbereiten
		if j.runStart >= j.segCount() {
			j.runStart, j.ResumeAt = 0, 0
		}
	}
	if err := os.MkdirAll(j.Dir, 0o750); err != nil {
		return nil, err
	}
	useHW := d.Mode == ModeTranscode && m.vaapi()
	if err := m.launch(j, audioIndex, useHW); err != nil {
		_ = os.RemoveAll(j.Dir)
		return nil, err
	}
	// Bricht FFmpeg sofort ab (z.B. fMP4-HLS nicht unterstützt), einmal mit
	// MPEG-TS-Segmenten wiederholen; die Fehlermeldung steht im Protokoll.
	select {
	case <-j.done:
		if j.err != nil {
			first := j.err
			// Ohne durchsuchbare Liste wären die Zeiten relativ zu Start – die
			// gemeldeten Positionen stimmten nicht mehr. Daher ab Anfang.
			j.TS, j.VOD, j.runStart, j.Start, j.ResumeAt, j.done, j.err = true, false, 0, 0, 0, make(chan struct{}), nil
			_ = os.RemoveAll(j.Dir)
			_ = os.MkdirAll(j.Dir, 0o750)
			if err := m.launch(j, audioIndex, useHW); err != nil {
				_ = os.RemoveAll(j.Dir)
				return nil, first
			}
			select {
			case <-j.done:
				if j.err != nil {
					_ = os.RemoveAll(j.Dir)
					return nil, first
				}
			case <-time.After(1500 * time.Millisecond):
			}
			log.Printf("hls: Rückfall auf MPEG-TS-Segmente aktiv")
		}
	case <-time.After(1500 * time.Millisecond):
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	return j, nil
}

func (m *HLSManager) launch(j *Job, audioIndex int, hw bool) error {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y"}
	if hw {
		args = append(args, "-vaapi_device", "/dev/dri/renderD128")
	}
	segStart, outSS := 0.0, ""
	switch {
	case j.VOD:
		segStart = float64(j.runStart) * SegLen
		switch {
		case segStart > 0 && j.Decision.CopyVideo:
			args = append(args, "-ss", strconv.FormatFloat(segStart, 'f', 3, 64))
		case segStart > 0:
			// Umwandeln: 1 s davor suchen und ab segStart ausgeben – so wird
			// auch kopierter Ton exakt bei segStart abgeschnitten.
			pre := math.Min(1, segStart)
			args = append(args, "-ss", strconv.FormatFloat(segStart-pre, 'f', 3, 64))
			outSS = strconv.FormatFloat(pre, 'f', 3, 64)
		}
		if j.Decision.CopyVideo {
			// Umverpacken beginnt am Schlüsselbild vor segStart: Original-
			// zeitstempel behalten. (Beim Umwandeln bricht -copyts mit
			// kopiertem Ton die Segmentierung von FFmpeg 4.x.)
			args = append(args, "-copyts")
		}
		j.mu.Lock()
		if j.genStart == nil {
			j.genStart = map[int]int{}
		}
		j.genStart[j.gen] = j.runStart
		j.mu.Unlock()
	case j.Start > 0:
		args = append(args, "-ss", strconv.FormatFloat(j.Start, 'f', 1, 64))
	}
	args = append(args, "-i", j.Src, "-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d?", audioIndex), "-sn", "-dn")
	if outSS != "" {
		args = append(args, "-ss", outSS)
	}
	switch {
	case j.Decision.CopyVideo:
		args = append(args, "-c:v", "copy")
		if j.Decision.HEVC {
			args = append(args, "-tag:v", "hvc1") // von Apple für HEVC in fMP4 gefordert
		}
	case hw:
		args = append(args, "-vf", "format=nv12,hwupload,scale_vaapi=w=-2:h='min(ih,"+maxH(j.Decision)+")'", "-c:v", "h264_vaapi", "-qp", "23", "-g", "48")
	default:
		if !m.CanEncodeH264() {
			return ErrNoH264
		}
		args = append(args, "-vf", "scale=-2:'min(ih,"+maxH(j.Decision)+")'")
		args = append(args, m.softwareH264Args()...)
	}
	if j.VOD && !j.Decision.CopyVideo {
		// Schlüsselbilder genau alle SegLen Sekunden, keine weiteren dazwischen
		// (sonst schneidet FFmpeg bis zu ¼ s zu früh).
		args = append(args, "-force_key_frames", "expr:gte(t,n_forced*"+strconv.FormatFloat(SegLen, 'f', 0, 64)+")", "-g", "600")
	}
	if j.Decision.CopyAudio {
		args = append(args, "-c:a", "copy")
	} else {
		a := m.audioArgs(j.Decision.Client)
		if a == nil {
			return ErrNoAAC
		}
		args = append(args, a...)
	}
	args = append(args, "-strict", "-2") // AC-3/E-AC-3 in fMP4 bei älteren FFmpeg-Versionen
	args = append(args, "-max_muxing_queue_size", "2048", "-f", "hls", "-hls_time", "6", "-hls_list_size", "0",
		"-hls_playlist_type", "event")
	if j.TS {
		if j.Decision.CopyVideo && !j.Decision.HEVC {
			args = append(args, "-bsf:v", "h264_mp4toannexb")
		}
		args = append(args, "-hls_flags", "independent_segments+temp_file",
			"-hls_segment_filename", filepath.Join(j.Dir, "seg%05d.ts"), filepath.Join(j.Dir, "index.m3u8"))
	} else {
		playlist, initName, segPat := "index.m3u8", "init.mp4", "seg%05d.m4s"
		if j.VOD {
			// Jeder Lauf schreibt eigene Dateien (g<N>-…); die Liste erzeugt
			// das Paket selbst (serveVODPlaylist).
			pre := fmt.Sprintf("g%d-", j.gen)
			playlist, initName, segPat = pre+"ffmpeg.m3u8", pre+"init.mp4", pre+"seg%05d.m4s"
			args = append(args, "-start_number", strconv.Itoa(j.runStart))
		}
		args = append(args, "-hls_segment_type", "fmp4", "-hls_fmp4_init_filename", initName,
			"-hls_flags", "independent_segments+temp_file",
			"-hls_segment_filename", filepath.Join(j.Dir, segPat), filepath.Join(j.Dir, playlist))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, m.FFmpeg, args...)
	stderr, _ := os.Create(filepath.Join(j.Dir, "ffmpeg.log"))
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	j.mu.Lock()
	j.cmd, j.cancel, j.HW = cmd, cancel, hw
	j.mu.Unlock()
	_ = audioIndex
	at := ""
	if j.VOD && j.runStart > 0 {
		at = fmt.Sprintf(" ab %s", fmtHMS(float64(j.runStart)*SegLen))
	}
	if os.Getenv("DSVR_DEBUG_FFMPEG") != "" {
		log.Printf("ffmpeg %s", strings.Join(args, " "))
	}
	log.Printf("hls: %s %s (%s)%s%s", j.Decision.Mode, filepath.Base(j.Src), j.Decision.Reason, map[bool]string{true: " [VAAPI]", false: ""}[hw], at)
	done := j.done
	go func() {
		err := cmd.Wait()
		stderr.Close()
		if err != nil && ctx.Err() == nil {
			b, _ := os.ReadFile(filepath.Join(j.Dir, "ffmpeg.log"))
			err = fmt.Errorf("%v: %s", err, strings.TrimSpace(lastLines(string(b), 3)))
			log.Printf("hls: ffmpeg-Fehler: %v", err)
		}
		j.mu.Lock()
		j.err = err
		j.mu.Unlock()
		close(done)
	}()
	return nil
}

// SetFFmpeg switches the FFmpeg binary (Einstellungen) and resets cached capabilities.
func (m *HLSManager) SetFFmpeg(path string) {
	m.mu.Lock()
	m.FFmpeg, m.encoders, m.vaapiChecked, m.vaapiOK = path, nil, false, false
	m.h264Checked, m.h264Args = false, nil
	m.mu.Unlock()
}

// Encoders lists FFmpeg's encoders once (für Diagnose und Auswahl).
func (m *HLSManager) Encoders() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.encoders != nil || m.FFmpeg == "" {
		return m.encoders
	}
	m.encoders = ListEncoders(m.FFmpeg)
	return m.encoders
}

// aacEncoder: verfügbarer AAC-Encoder ("" = keiner).
func (m *HLSManager) aacEncoder() string {
	enc := m.Encoders()
	switch {
	case len(enc) == 0 || enc["aac"]:
		return "aac"
	case enc["libfdk_aac"]:
		return "libfdk_aac"
	}
	return ""
}

// audioArgs: AAC (Stereo); ohne AAC-Encoder für Apple-Geräte AC-3 (5.1), für
// Browser Opus. nil = keine passende Umwandlung möglich.
func (m *HLSManager) audioArgs(client string) []string {
	if enc := m.aacEncoder(); enc != "" {
		return []string{"-c:a", enc, "-ac", "2", "-b:a", "192k"}
	}
	enc := m.Encoders()
	switch {
	case client == "apple" && enc["ac3"]:
		return []string{"-c:a", "ac3", "-b:a", "448k"}
	case client != "apple" && enc["libopus"]:
		return []string{"-c:a", "libopus", "-ac", "2", "-b:a", "160k"}
	}
	return nil
}

// CanConvertAudio: Tonumwandlung für diesen Client möglich.
func (m *HLSManager) CanConvertAudio(client string) bool {
	return m != nil && m.FFmpeg != "" && m.audioArgs(client) != nil
}

// CanEncodeAAC reports whether audio can be converted to AAC.
func (m *HLSManager) CanEncodeAAC() bool { return m != nil && m.FFmpeg != "" && m.aacEncoder() != "" }

// CanEncodeH264 reports whether video can be converted to H.264.
func (m *HLSManager) CanEncodeH264() bool {
	if m == nil || m.FFmpeg == "" {
		return false
	}
	if m.HWAccel != "off" && m.Encoders()["h264_vaapi"] && m.vaapi() {
		return true
	}
	return m.softwareH264Args() != nil
}

// Diagnostics: was die Umwandlung auf diesem Gerät kann (für die Statusseite).
func (m *HLSManager) Diagnostics() map[string]any {
	if m == nil || m.FFmpeg == "" {
		return map[string]any{"available": false}
	}
	h264 := "keiner"
	if a := m.softwareH264Args(); a != nil {
		h264 = a[1]
	}
	hw := false
	if m.HWAccel != "off" {
		hw = m.vaapi()
	}
	return map[string]any{"available": true, "path": m.FFmpeg, "h264": h264, "vaapi": hw, "aac": m.aacEncoder() != ""}
}

// softwareH264Args wählt den verfügbaren H.264-Encoder (libx264 bevorzugt;
// Synologys System-FFmpeg bringt teils nur andere Encoder mit).
func (m *HLSManager) softwareH264Args() []string {
	m.probeH264()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.h264Args
}

// probeH264 prüft die H.264-Encoder durch eine kurze Probe-Umwandlung: Die
// Encoderliste allein reicht nicht (z.B. h264_v4l2m2m ohne passendes Gerät,
// libx264 in eingeschränkten Synology-Builds).
func (m *HLSManager) probeH264() {
	m.mu.Lock()
	if m.h264Checked || m.FFmpeg == "" {
		m.mu.Unlock()
		return
	}
	ff := m.FFmpeg
	m.mu.Unlock()
	enc := m.Encoders()
	cands := [][]string{
		{"-c:v", "libx264", "-preset", "veryfast", "-crf", "22", "-maxrate", "8M", "-bufsize", "16M", "-pix_fmt", "yuv420p", "-profile:v", "high", "-g", "48", "-sc_threshold", "0"},
		{"-c:v", "libopenh264", "-b:v", "6M", "-pix_fmt", "yuv420p", "-g", "48"},
		{"-c:v", "h264_v4l2m2m", "-b:v", "6M", "-pix_fmt", "yuv420p", "-g", "48"},
	}
	var ok []string
	for _, c := range cands {
		if len(enc) > 0 && !enc[c[1]] {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		args := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=0.4"}, c...)
		err := exec.CommandContext(ctx, ff, append(args, "-f", "null", "-")...).Run()
		cancel()
		if err == nil {
			ok = c
			log.Printf("hls: H.264-Encoder %s einsatzbereit", c[1])
			break
		}
	}
	if ok == nil {
		log.Printf("hls: kein funktionierender H.264-Encoder in %s – Umwandlung nicht möglich, Remux/Direkt weiter möglich", ff)
	}
	m.mu.Lock()
	m.h264Checked, m.h264Args = true, ok
	m.mu.Unlock()
}

func maxH(d Decision) string {
	if d.MaxHeight > 0 {
		return strconv.Itoa(d.MaxHeight)
	}
	return "1080"
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " | ")
}

func fmtHMS(sec float64) string {
	t := int(sec)
	return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
}

func (j *Job) finished() bool {
	select {
	case <-j.doneCh():
		return true
	default:
		return false
	}
}

func (j *Job) hasSegment() bool {
	_, err := os.Stat(filepath.Join(j.Dir, "seg00000.m4s"))
	return err == nil
}

// Get returns a job (token = Job-ID, nicht erratbar).
func (m *HLSManager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if ok {
		j.mu.Lock()
		j.lastUse = time.Now()
		j.mu.Unlock()
	}
	return j, ok
}

// Stop ends a job and removes its files.
func (m *HLSManager) Stop(id string) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	delete(m.jobs, id)
	m.mu.Unlock()
	if !ok {
		return
	}
	j.mu.Lock()
	cancel, done := j.cancel, j.done
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	go func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		_ = os.RemoveAll(j.Dir)
	}()
}

// StopAll beendet alle Umwandlungen (Neustart/Beenden) und wartet kurz auf FFmpeg.
func (m *HLSManager) StopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	var dones []chan struct{}
	ids := make([]string, 0, len(m.jobs))
	for id, j := range m.jobs {
		ids = append(ids, id)
		dones = append(dones, j.doneCh())
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
	deadline := time.After(3 * time.Second)
	for _, d := range dones {
		select {
		case <-d:
		case <-deadline:
			return
		}
	}
}

// Count returns running jobs.
func (m *HLSManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

func (m *HLSManager) reap() {
	m.mu.Lock()
	var stale []string
	for id, j := range m.jobs {
		j.mu.Lock()
		idle := time.Since(j.lastUse)
		j.mu.Unlock()
		if idle > m.IdleTimeout {
			stale = append(stale, id)
		}
	}
	m.mu.Unlock()
	for _, id := range stale {
		log.Printf("hls: Sitzung %s beendet (inaktiv)", id[:8])
		m.Stop(id)
	}
}

// ServeFile serves index.m3u8 / init.mp4 / segNNNNN.m4s. Playlist-Einträge
// werden auf absolute Pfade (prefix) umgeschrieben, damit Clients, deren
// Stream-URL anders aussieht (z.B. …/vtestreaming.cgi/1.mp4), Segmente finden.
func (m *HLSManager) ServeFile(w http.ResponseWriter, r *http.Request, j *Job, name, prefix string) {
	m.serveFile(w, r, j, name, prefix, false)
}

// ServeFileRanged wie ServeFile, aber Listen beantworten Teilabfragen mit 206
// (Safari/AVPlayer im Web-Fenster fragen zuerst „Range: bytes=0-1“ ab). Die
// DS-video-Apps erwarten bei Listen dagegen immer 200 wie Video Station.
func (m *HLSManager) ServeFileRanged(w http.ResponseWriter, r *http.Request, j *Job, name, prefix string) {
	m.serveFile(w, r, j, name, prefix, true)
}

func (m *HLSManager) serveFile(w http.ResponseWriter, r *http.Request, j *Job, name, prefix string, ranges bool) {
	if strings.Contains(name, "/") || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	if name == "" || name == "index.m3u8" {
		if j.VOD {
			m.serveVODPlaylist(w, r, j, prefix, ranges)
			return
		}
		m.servePlaylist(w, r, j, prefix, ranges)
		return
	}
	if j.VOD {
		if name == "init.mp4" {
			m.serveVODInit(w, r, j)
			return
		}
		if k, ok := segIndex(name); ok {
			if k >= j.segCount() {
				http.NotFound(w, r)
				return
			}
			if err := m.ensureSegment(j, k); err != nil {
				log.Printf("hls: Segment %d: %v", k, err)
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			m.serveVODSegment(w, r, j, k)
			return
		}
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(j.Dir, name)
	// Warten, bis FFmpeg das Segment geschrieben hat.
	deadline := time.Now().Add(45 * time.Second)
	for {
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			break
		}
		if j.finished() || time.Now().After(deadline) || r.Context().Err() != nil {
			http.NotFound(w, r)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if strings.HasSuffix(name, "init.mp4") {
		if b, err := os.ReadFile(p); err == nil {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(cleanInit(b)))
			return
		}
	}
	switch {
	case strings.HasSuffix(name, ".mp4") || strings.HasSuffix(name, ".m4s"):
		w.Header().Set("Content-Type", "video/mp4")
	case strings.HasSuffix(name, ".ts"):
		w.Header().Set("Content-Type", "video/mp2t")
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, p)
}

func segIndex(name string) (int, bool) {
	if !strings.HasPrefix(name, "seg") || !strings.HasSuffix(name, ".m4s") {
		return 0, false
	}
	k, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "seg"), ".m4s"))
	return k, err == nil && k >= 0
}

func segName(k int) string { return fmt.Sprintf("seg%05d.m4s", k) }

// serveVODPlaylist: vollständige Liste über die ganze Laufzeit, sofort.
func (m *HLSManager) serveVODPlaylist(w http.ResponseWriter, r *http.Request, j *Job, prefix string, ranges bool) {
	n := j.segCount()
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:")
	// Bei kopiertem Video schneidet FFmpeg erst am nächsten Schlüsselbild –
	// Segmente können länger als 6 s sein.
	if j.Decision.CopyVideo {
		sb.WriteString("12")
	} else {
		sb.WriteString("6")
	}
	sb.WriteString("\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	if j.ResumeAt > 0 {
		// Player beginnen hier; die ganze Liste bleibt erreichbar (Zurückspulen).
		fmt.Fprintf(&sb, "#EXT-X-START:TIME-OFFSET=%.3f,PRECISE=YES\n", j.ResumeAt)
	}
	sb.WriteString(`#EXT-X-MAP:URI="` + prefix + "init.mp4\"\n")
	for k := 0; k < n; k++ {
		d := SegLen
		if k == n-1 {
			d = j.Decision.Duration - float64(k)*SegLen
		}
		fmt.Fprintf(&sb, "#EXTINF:%.6f,\n%s%s\n", d, prefix, segName(k))
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	servePlaylistText(w, r, sb.String(), ranges)
}

// servePlaylistText: Liste mit Range-Unterstützung – Safari/WebKit fragt
// Adressen zuerst mit „Range: bytes=0-1“ ab und erwartet dann 206.
func servePlaylistText(w http.ResponseWriter, r *http.Request, body string, ranges bool) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-cache")
	if ranges {
		http.ServeContent(w, r, "index.m3u8", time.Time{}, strings.NewReader(body))
		return
	}
	_, _ = w.Write([]byte(body))
}

func (j *Job) genFile(g int, name string) string {
	return filepath.Join(j.Dir, fmt.Sprintf("g%d-%s", g, name))
}

// segFile: Segment k – bevorzugt aus dem gewählten Lauf (j.pick), sonst aus
// dem jüngsten Lauf, der es geschrieben hat.
func (j *Job) segFile(k int) (string, int, bool) {
	j.mu.Lock()
	g, pref := j.gen, -1
	if p, ok := j.pick[k]; ok {
		pref = p
	}
	j.mu.Unlock()
	if pref >= 0 {
		if p := j.genFile(pref, segName(k)); fileExists(p) {
			return p, pref, true
		}
	}
	for ; g >= 0; g-- {
		if p := j.genFile(g, segName(k)); fileExists(p) {
			return p, g, true
		}
	}
	return "", 0, false
}

// runOffsets: Versatz der Segmente eines Laufs (aus dessen init.mp4).
func (j *Job) runOffsets(g int) (map[uint32]int64, error) {
	j.mu.Lock()
	o, ok := j.offs[g]
	start := j.genStart[g]
	j.mu.Unlock()
	if ok {
		return o, nil
	}
	b, err := os.ReadFile(j.genFile(g, "init.mp4"))
	if err != nil {
		return nil, err
	}
	ed, err := initEdits(b)
	if err != nil {
		return nil, err
	}
	o = runOffsetsFor(ed, float64(start)*SegLen, j.Decision.CopyVideo)
	j.mu.Lock()
	if j.offs == nil {
		j.offs = map[int]map[uint32]int64{}
	}
	j.offs[g] = o
	j.mu.Unlock()
	return o, nil
}

// serveVODInit: eine gemeinsame init.mp4 ohne Startversatz für alle Läufe.
func (m *HLSManager) serveVODInit(w http.ResponseWriter, r *http.Request, j *Job) {
	deadline := time.Now().Add(45 * time.Second)
	for {
		j.mu.Lock()
		cached, g := j.initNorm, j.gen
		j.mu.Unlock()
		if cached != nil {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeContent(w, r, "init.mp4", time.Time{}, bytes.NewReader(cached))
			return
		}
		for ; g >= 0 && cached == nil; g-- {
			b, err := os.ReadFile(j.genFile(g, "init.mp4"))
			if err != nil || len(b) == 0 {
				continue
			}
			if n, err := normalizeInit(b); err == nil {
				if _, err := initEdits(n); err == nil {
					n = cleanInit(n)
					cached = n
					j.mu.Lock()
					j.initNorm = n
					j.mu.Unlock()
				}
			}
		}
		if cached != nil {
			continue
		}
		if j.finished() || time.Now().After(deadline) || r.Context().Err() != nil {
			http.NotFound(w, r)
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// serveVODSegment: Segment k mit absoluten Zeitstempeln ausliefern.
func (m *HLSManager) serveVODSegment(w http.ResponseWriter, r *http.Request, j *Job, k int) {
	deadline := time.Now().Add(45 * time.Second)
	for {
		if p, g, ok := j.segFile(k); ok {
			b, err := os.ReadFile(p)
			if err == nil && len(b) > 0 {
				o, err := j.runOffsets(g)
				if err == nil {
					err = shiftSegment(b, o)
				}
				if err != nil {
					log.Printf("hls: Segment %d: Zeitstempel: %v", k, err)
				}
				w.Header().Set("Content-Type", "video/mp4")
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeContent(w, r, segName(k), time.Time{}, bytes.NewReader(b))
				return
			}
		}
		if j.finished() || time.Now().After(deadline) || r.Context().Err() != nil {
			http.NotFound(w, r)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ensureSegment sorgt dafür, dass Segment k entsteht: Liegt es in Reichweite
// der laufenden Umwandlung, wird gewartet, sonst startet FFmpeg bei k neu.
func (m *HLSManager) ensureSegment(j *Job, k int) error {
	j.seekMu.Lock()
	defer j.seekMu.Unlock()
	j.mu.Lock()
	start, g := j.runStart, j.gen
	j.mu.Unlock()
	setPick := func(gen int) {
		j.mu.Lock()
		if j.pick == nil {
			j.pick = map[int]int{}
		}
		j.pick[k] = gen
		j.mu.Unlock()
	}
	if fileExists(j.genFile(g, segName(k))) {
		setPick(g)
		return nil
	}
	reach := 3 // Umwandlung: ~Echtzeit
	if j.Decision.CopyVideo {
		reach = 40 // Umverpacken läuft vielfach schneller als Echtzeit
	}
	// Liegt k direkt vor der laufenden Umwandlung, auf diese warten – auch
	// wenn ein früherer Lauf das Segment schon hat: Segmente eines Laufs
	// schließen lückenlos aneinander an.
	if !j.finished() && k >= start {
		h := start - 1
		for fileExists(j.genFile(g, segName(h+1))) {
			h++
		}
		if k <= h+reach {
			setPick(g)
			return nil // kommt gleich
		}
	}
	if _, og, ok := j.segFile(k); ok {
		setPick(og)
		return nil
	}
	if err := m.restartAt(j, k); err != nil {
		return err
	}
	setPick(g + 1)
	return nil
}

func (m *HLSManager) restartAt(j *Job, k int) error {
	j.mu.Lock()
	cancel, done := j.cancel, j.done
	j.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	j.mu.Lock()
	j.runStart, j.done, j.err = k, make(chan struct{}), nil
	j.gen++
	j.mu.Unlock()
	return m.launch(j, j.audioIndex, j.HW)
}

func (m *HLSManager) servePlaylist(w http.ResponseWriter, r *http.Request, j *Job, prefix string, ranges bool) {
	p := filepath.Join(j.Dir, "index.m3u8")
	deadline := time.Now().Add(60 * time.Second)
	for {
		if b, err := os.ReadFile(p); err == nil && (strings.Contains(string(b), ".m4s") || strings.Contains(string(b), ".ts\n")) {
			break
		}
		if j.finished() && !fileExists(p) {
			msg := "Umwandlung fehlgeschlagen"
			if j.err != nil {
				msg += ": " + j.err.Error()
			}
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		if time.Now().After(deadline) || r.Context().Err() != nil {
			http.Error(w, "Zeitüberschreitung beim Start der Umwandlung", http.StatusGatewayTimeout)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	var sb strings.Builder
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#EXT-X-MAP:URI=\""):
			line = `#EXT-X-MAP:URI="` + prefix + strings.TrimPrefix(line, `#EXT-X-MAP:URI="`)
		case line != "" && !strings.HasPrefix(line, "#"):
			line = prefix + line
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
		if line == "#EXTM3U" {
			// Die Liste wächst während der Umwandlung (EVENT). Ohne diese Angabe
			// beginnen iOS-Player und hls.js am „Live-Rand“, also mitten im Film.
			sb.WriteString("#EXT-X-START:TIME-OFFSET=0,PRECISE=YES\n")
		}
	}
	servePlaylistText(w, r, sb.String(), ranges)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
