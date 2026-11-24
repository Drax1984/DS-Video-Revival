package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dsvideorevival/internal/auth"
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
	// WebPort: Port der Weboberfläche (aus listen_http), 0 = unverändert.
	WebPort         int  `json:"web_port"`
	JellyfinEnabled bool `json:"jellyfin_enabled"`
	JellyfinPort    int  `json:"jellyfin_port"`
	// Nur Anzeige: laufender Port (vor einem Neustart), interner Brückenport.
	RunningWebPort int `json:"running_web_port,omitempty"`
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

// withPort ersetzt den Port und behält eine evtl. angegebene Adresse.
func withPort(listen string, port int) string {
	host := ""
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		host = listen[:i]
	}
	return host + ":" + strconv.Itoa(port)
}

func (h *Handler) view(c config.Config) settingsView {
	return settingsView{TMDbKey: mask(c.TMDbKey), OMDbKey: mask(c.OMDbKey), MetaLanguage: c.MetaLanguage, FFmpeg: c.FFmpeg,
		MaxTranscodes: c.MaxTranscodes, HWAccel: c.HWAccel, SendPosterMtime: c.SendPosterMtime, ListenHTTP: c.ListenHTTP,
		ListenHTTPS: c.ListenHTTPS, TLSCert: c.TLSCert, TLSKey: c.TLSKey, Upstream: c.Upstream, WebPort: portOf(c.ListenHTTP),
		JellyfinEnabled: c.JellyfinEnabled, JellyfinPort: c.JellyfinListenPort(), RunningWebPort: h.runningWebPort}
}

// Ports, die DS Video Revival nicht belegen darf (DSM).
var reservedPorts = map[int]string{5000: "DSM (HTTP)", 5001: "DSM (HTTPS)", 80: "Webserver", 443: "Webserver", 7359: "Jellyfin-Suche (UDP)"}

// BridgePort: interner Port für DSMs Webserver (siehe main.DSMBridgePort).
var BridgePort = 5180

// checkPort: gültig und (falls neu) frei? Fehlertexte für die Weboberfläche.
func checkPort(p int, what string, changed bool, others map[int]string) error {
	if p < 1024 || p > 65535 {
		return fmt.Errorf("%s: Port %d ungültig (1024–65535)", what, p)
	}
	if n, ok := reservedPorts[p]; ok {
		return fmt.Errorf("%s: Port %d ist für %s reserviert", what, p, n)
	}
	if n, ok := others[p]; ok {
		return fmt.Errorf("%s: Port %d wird schon für %s verwendet", what, p, n)
	}
	if changed && p != BridgePort {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err != nil {
			return fmt.Errorf("%s: Port %d ist auf der NAS bereits belegt", what, p)
		}
		_ = ln.Close()
	}
	return nil
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	c := *h.S.Cfg()
	if h.runningWebPort == 0 {
		h.runningWebPort = portOf(c.ListenHTTP)
	}
	if r.Method == http.MethodGet {
		writeJSON(w, h.view(c))
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "GET/PUT", http.StatusMethodNotAllowed)
		return
	}
	// Nicht mitgeschickte Felder (z.B. Ports, die die Oberfläche nicht mehr
	// zeigt) behalten ihren Wert.
	v := h.view(c)
	v.WebPort = 0
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
	listenHTTP := strings.TrimSpace(v.ListenHTTP)
	if v.WebPort > 0 && v.WebPort != portOf(listenHTTP) {
		listenHTTP = withPort(listenHTTP, v.WebPort)
	}
	webPort, oldWeb := portOf(listenHTTP), portOf(c.ListenHTTP)
	if listenHTTP != c.ListenHTTP {
		if err := checkPort(webPort, "Weboberfläche", webPort != h.runningWebPort, map[int]string{portOf(c.ListenHTTPS): "HTTPS",
			v.JellyfinPort: "die Jellyfin-Schnittstelle"}); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if v.JellyfinPort == 0 {
		v.JellyfinPort = config.JellyfinDefaultPort
	}
	if v.JellyfinEnabled && (v.JellyfinPort != c.JellyfinListenPort() || !c.JellyfinEnabled) {
		running := h.S.Jellyfin != nil && c.JellyfinEnabled && v.JellyfinPort == c.JellyfinListenPort()
		if err := checkPort(v.JellyfinPort, "Jellyfin-Schnittstelle", !running, map[int]string{webPort: "die Weboberfläche",
			oldWeb: "die Weboberfläche (bis zum Neustart)", BridgePort: "die interne DSM-Anbindung", portOf(c.ListenHTTPS): "HTTPS"}); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	restart := listenHTTP != c.ListenHTTP || v.ListenHTTPS != c.ListenHTTPS || v.TLSCert != c.TLSCert || v.TLSKey != c.TLSKey || v.Upstream != c.Upstream
	jfChanged := v.JellyfinEnabled != c.JellyfinEnabled || v.JellyfinPort != c.JellyfinListenPort()
	c.TMDbKey, c.OMDbKey = keep(v.TMDbKey, c.TMDbKey), keep(v.OMDbKey, c.OMDbKey)
	c.MetaLanguage, c.FFmpeg, c.MaxTranscodes, c.HWAccel, c.SendPosterMtime = v.MetaLanguage, strings.TrimSpace(v.FFmpeg), v.MaxTranscodes, v.HWAccel, v.SendPosterMtime
	c.ListenHTTP, c.ListenHTTPS, c.TLSCert, c.TLSKey, c.Upstream = listenHTTP, strings.TrimSpace(v.ListenHTTPS), strings.TrimSpace(v.TLSCert), strings.TrimSpace(v.TLSKey), strings.TrimSpace(v.Upstream)
	c.JellyfinEnabled = v.JellyfinEnabled
	c.JellyfinPort = v.JellyfinPort
	if c.JellyfinPort == config.JellyfinDefaultPort {
		c.JellyfinPort = 0
	}
	if err := config.Save(h.CfgPath, c); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	h.S.SetCfg(c)
	if h.S.Meta != nil {
		h.S.Meta.SetClient(metadata.NewClient(c.TMDbKey, c.OMDbKey, c.MetaLanguage, h.S.Meta.CacheDir))
	}
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
	if jfChanged && h.S.Jellyfin != nil {
		h.S.Jellyfin.Apply()
		log.Printf("admin: Jellyfin-Schnittstelle %s (Port %d)", map[bool]string{true: "eingeschaltet", false: "ausgeschaltet"}[c.JellyfinEnabled], c.JellyfinListenPort())
	}
	// Auch eine früher gespeicherte, noch nicht wirksame Portänderung meldet „Neustart nötig“.
	restart = restart || (h.runningWebPort != 0 && webPort != h.runningWebPort)
	log.Printf("admin: Einstellungen geändert (Neustart nötig: %v)", restart)
	updateMenuPort(c.ListenHTTP)
	msg := "Gespeichert."
	if restart {
		msg = "Gespeichert. Die Änderung wird nach einem Neustart wirksam."
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg, "restart": restart, "web_port": webPort,
		"running_web_port": h.runningWebPort, "can_restart": h.Restart != nil})
}

// restart: Neustart von DS Video Revival (Administratoren).
func (h *Handler) restart(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if h.Restart == nil {
		jsonErr(w, http.StatusNotImplemented, "Neustart hier nicht möglich – bitte im Paket-Zentrum stoppen und starten.")
		return
	}
	log.Printf("admin: Neustart angefordert von %s", sess.Account)
	writeJSON(w, map[string]any{"ok": true, "web_port": portOf(h.S.Cfg().ListenHTTP)})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	time.AfterFunc(500*time.Millisecond, h.Restart)
}

// updateMenuPort passt den Port im DSM-Hauptmenü-Eintrag an: Ausweichadresse
// in ui/open.js (der Eintrag öffnet die Weboberfläche über die DSM-Adresse,
// nur ohne DSMs Weiterleitung direkt über den Port) und – aus Versionen bis
// 0.6.7 – „port“ in ui/config.
func updateMenuPort(listen string) {
	dest := os.Getenv("SYNOPKG_PKGDEST")
	_, port, ok := strings.Cut(listen, ":")
	if dest == "" || !ok || port == "" {
		return
	}
	for name, re := range map[string]*regexp.Regexp{
		"config":  regexp.MustCompile(`"port": *"[0-9]*"`),
		"open.js": regexp.MustCompile(`var port = "[0-9]*"`),
	} {
		p := filepath.Join(dest, "ui", name)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out := re.ReplaceAllFunc(b, func(m []byte) []byte {
			return regexp.MustCompile(`"[0-9]*"$`).ReplaceAll(m, []byte(`"`+port+`"`))
		})
		if err := os.WriteFile(p, out, 0o644); err != nil {
			log.Printf("admin: Menüeintrag nicht aktualisiert: %v", err)
		}
	}
}
