package admin

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/metadata"
	"dsvideorevival/internal/stream"
)

func (h *Handler) ffmpegPath() string {
	if h.S.HLS == nil {
		return ""
	}
	return h.S.HLS.FFmpeg
}

// settingsView: Schlüssel werden nie vollständig ausgeliefert.
type settingsView struct {
	TMDbKey         string `json:"tmdb_key"`
	OMDbKey         string `json:"omdb_key"`
	MetaLanguage    string `json:"meta_language"`
	FFmpeg          string `json:"ffmpeg"`
	MaxTranscodes   int    `json:"max_transcodes"`
	HWAccel         string `json:"hw_accel"`
	SendPosterMtime bool   `json:"send_poster_mtime"`
	ListenHTTP      string `json:"listen_http"`
	ListenHTTPS     string `json:"listen_https"`
	TLSCert         string `json:"tls_cert"`
	TLSKey          string `json:"tls_key"`
	Upstream        string `json:"upstream"`
}

func mask(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 4 {
		return "••••"
	}
	return "••••" + k[len(k)-4:]
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	c := *h.S.Cfg()
	if r.Method == http.MethodGet {
		writeJSON(w, settingsView{mask(c.TMDbKey), mask(c.OMDbKey), c.MetaLanguage, c.FFmpeg, c.MaxTranscodes, c.HWAccel,
			c.SendPosterMtime, c.ListenHTTP, c.ListenHTTPS, c.TLSCert, c.TLSKey, c.Upstream})
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "GET/PUT", http.StatusMethodNotAllowed)
		return
	}
	// Nicht mitgeschickte Felder (z.B. Ports, die die Oberfläche nicht mehr
	// zeigt) behalten ihren Wert.
	v := settingsView{mask(c.TMDbKey), mask(c.OMDbKey), c.MetaLanguage, c.FFmpeg, c.MaxTranscodes, c.HWAccel,
		c.SendPosterMtime, c.ListenHTTP, c.ListenHTTPS, c.TLSCert, c.TLSKey, c.Upstream}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	keep := func(newV, old string) string {
		if strings.HasPrefix(newV, "••••") {
			return old
		}
		return strings.TrimSpace(newV)
	}
	restart := v.ListenHTTP != c.ListenHTTP || v.ListenHTTPS != c.ListenHTTPS || v.TLSCert != c.TLSCert || v.TLSKey != c.TLSKey || v.Upstream != c.Upstream
	c.TMDbKey, c.OMDbKey = keep(v.TMDbKey, c.TMDbKey), keep(v.OMDbKey, c.OMDbKey)
	c.MetaLanguage, c.FFmpeg, c.MaxTranscodes, c.HWAccel, c.SendPosterMtime = v.MetaLanguage, strings.TrimSpace(v.FFmpeg), v.MaxTranscodes, v.HWAccel, v.SendPosterMtime
	c.ListenHTTP, c.ListenHTTPS, c.TLSCert, c.TLSKey, c.Upstream = strings.TrimSpace(v.ListenHTTP), strings.TrimSpace(v.ListenHTTPS), strings.TrimSpace(v.TLSCert), strings.TrimSpace(v.TLSKey), strings.TrimSpace(v.Upstream)
	if err := config.Save(h.CfgPath, c); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	h.S.SetCfg(c)
	h.S.Meta.SetClient(metadata.NewClient(c.TMDbKey, c.OMDbKey, c.MetaLanguage, h.S.Meta.CacheDir))
	if h.S.HLS != nil {
		ff := stream.FindFFmpeg(c.FFmpeg)
		h.S.HLS.SetFFmpeg(ff)
		if library.IsFFmpegTool(h.S.FFprobe) || h.S.FFprobe == "" {
			if fp := library.FindFFprobe(ff); fp != "" {
				h.S.FFprobe = fp
			} else {
				h.S.FFprobe = ff
			}
		}
		h.S.HLS.MaxJobs, h.S.HLS.HWAccel = c.MaxTranscodes, c.HWAccel
	}
	log.Printf("admin: Einstellungen geändert (Neustart nötig: %v)", restart)
	updateMenuPort(c.ListenHTTP)
	msg := "Gespeichert."
	if restart {
		msg = "Gespeichert. Port-/Adressänderungen werden nach Neustart des Pakets wirksam (Paket-Zentrum → Stoppen/Starten)."
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg, "restart": restart})
}

// updateMenuPort passt den Port des DSM-Hauptmenü-Eintrags (ui/config) an.
func updateMenuPort(listen string) {
	dest := os.Getenv("SYNOPKG_PKGDEST")
	_, port, ok := strings.Cut(listen, ":")
	if dest == "" || !ok || port == "" {
		return
	}
	p := filepath.Join(dest, "ui", "config")
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	out := regexp.MustCompile(`"port": *"[0-9]*"`).ReplaceAll(b, []byte(`"port": "`+port+`"`))
	if err := os.WriteFile(p, out, 0o644); err != nil {
		log.Printf("admin: Menüeintrag nicht aktualisiert: %v", err)
	}
}
