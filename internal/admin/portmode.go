package admin

// Portumstellung per Knopf: DSM zieht von 5000/5001 auf 5010/5011 um,
// DS Video Revival übernimmt 5000/5001 und reicht alles Fremde an DSM durch.
// Damit genügt in DS video die IP bzw. DynDNS-Adresse ohne Port.
//
// DSM-API (belegt durch reale DSM-7-Registry/Antworten, mikespub/synology):
//   SYNO.Core.Web.DSM v2 get/set: http_port, https_port, enable_https_redirect
//   SYNO.Core.Certificate.CRT v1 list: certificates[].id/is_default
//   SYNO.Core.Certificate v1 export (Antwortformat unbelegt → Versuch, sonst selbstsigniert)
// Ausgeführt mit der Sitzung des angemeldeten DSM-Administrators.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dsvideorevival/internal/config"
)

const movedHTTP, movedHTTPS = 5010, 5011

type pmResult struct {
	OK    bool     `json:"ok"`
	Steps []string `json:"steps"`
	Error string   `json:"error,omitempty"`
}

func (r *pmResult) step(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	r.Steps = append(r.Steps, msg)
	log.Printf("portmodus: %s", msg)
}

func portOf(addr string) int {
	_, p, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(p)
	return n
}

func (h *Handler) portmode(w http.ResponseWriter, r *http.Request, sid string) {
	c := *h.S.Cfg()
	if r.Method == http.MethodGet {
		certSrc := "selbstsigniert"
		if c.TLSCert != "" {
			certSrc = certSubject(c.TLSCert)
			if certSrc == "" {
				certSrc = c.TLSCert
			}
		}
		writeJSON(w, map[string]any{"frontend_mode": c.FrontendMode, "front_http": c.FrontHTTP, "front_https": c.FrontHTTPS,
			"front_http_running":  h.S.Listen != nil && h.S.Listen.Running(c.FrontHTTP),
			"front_https_running": h.S.Listen != nil && h.S.Listen.Running(c.FrontHTTPS),
			"upstream":            c.Upstream, "maintenance": c.ListenHTTP, "cert": certSrc, "auth_mode": c.AuthMode})
		return
	}
	var req struct {
		Enable bool `json:"enable"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if c.AuthMode != "dsm" || h.S.Up == nil || h.S.Listen == nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, pmResult{Error: "Nur im DSM-Anmeldemodus auf der NAS möglich."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	var res pmResult
	var err error
	if req.Enable {
		err = h.enablePortMode(ctx, sid, r.Header.Get("X-DSVR-SYNOTOKEN"), &res)
	} else {
		err = h.disablePortMode(ctx, sid, &res)
	}
	if err != nil {
		res.Error = err.Error()
		w.WriteHeader(http.StatusBadGateway)
	} else {
		res.OK = true
	}
	writeJSON(w, res)
}

func (h *Handler) dsmCall(ctx context.Context, sid string, v url.Values) (map[string]any, error) {
	v.Set("_sid", sid)
	m, err := h.S.Up.Call(ctx, "entry.cgi", v)
	if err != nil {
		return nil, err
	}
	if ok, _ := m["success"].(bool); !ok {
		return m, fmt.Errorf("DSM meldet Fehler %v", m["error"])
	}
	return m, nil
}

func (h *Handler) dsmPorts(ctx context.Context, sid string) (int, int, bool, error) {
	m, err := h.dsmCall(ctx, sid, url.Values{"api": {"SYNO.Core.Web.DSM"}, "version": {"2"}, "method": {"get"}})
	if err != nil {
		return 0, 0, false, err
	}
	d, _ := m["data"].(map[string]any)
	hp, _ := d["http_port"].(float64)
	sp, _ := d["https_port"].(float64)
	rd, _ := d["enable_https_redirect"].(bool)
	if hp == 0 || sp == 0 {
		return 0, 0, false, fmt.Errorf("DSM-Ports nicht lesbar: %v", d)
	}
	return int(hp), int(sp), rd, nil
}

func (h *Handler) setDSMPorts(ctx context.Context, sid string, httpPort, httpsPort int, redirect bool) error {
	_, err := h.dsmCall(ctx, sid, url.Values{"api": {"SYNO.Core.Web.DSM"}, "version": {"2"}, "method": {"set"},
		"http_port": {strconv.Itoa(httpPort)}, "https_port": {strconv.Itoa(httpsPort)}, "enable_https_redirect": {strconv.FormatBool(redirect)}})
	return err
}

// waitDSM wartet, bis DSM unter base antwortet.
func (h *Handler) waitDSM(ctx context.Context, base string, d time.Duration) bool {
	cl := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		resp, err := cl.Get(base + "/webapi/query.cgi?api=SYNO.API.Info&version=1&method=query&query=SYNO.API.Auth")
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			if strings.Contains(string(b), "SYNO.API.Auth") {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

func (h *Handler) enablePortMode(ctx context.Context, sid, token string, res *pmResult) error {
	c := *h.S.Cfg()
	if c.FrontendMode {
		res.step("Port-Modus ist bereits aktiv.")
		return nil
	}
	hp, sp, rd, err := h.dsmPorts(ctx, sid)
	if err != nil {
		return fmt.Errorf("DSM-Einstellungen lesen: %w", err)
	}
	res.step("DSM läuft auf Port %d (HTTP) / %d (HTTPS), HTTPS-Umleitung %v.", hp, sp, rd)
	if hp == movedHTTP || sp == movedHTTPS {
		return fmt.Errorf("DSM nutzt bereits Port %d/%d – bitte manuell prüfen", movedHTTP, movedHTTPS)
	}
	if cf, kf, err := h.exportDSMCert(ctx, sid, token, c.DataDir); err == nil {
		c.TLSCert, c.TLSKey = cf, kf
		res.step("DSM-Zertifikat übernommen (für HTTPS auf Port %d).", sp)
	} else {
		res.step("DSM-Zertifikat nicht exportierbar (%v) – HTTPS nutzt ein selbstsigniertes Zertifikat.", err)
	}

	// DSM verschieben. Die Antwort kann ausbleiben, weil DSM dabei neu startet.
	if err := h.setDSMPorts(ctx, sid, movedHTTP, movedHTTPS, false); err != nil {
		res.step("Antwort auf Portänderung: %v (prüfe Ergebnis …)", err)
	} else {
		res.step("DSM auf Port %d/%d verschoben.", movedHTTP, movedHTTPS)
	}
	newBase := "http://127.0.0.1:" + strconv.Itoa(movedHTTP)
	if !h.waitDSM(ctx, newBase, 90*time.Second) {
		res.step("DSM antwortet nicht auf Port %d – Rücknahme.", movedHTTP)
		_ = h.setDSMPorts(ctx, sid, hp, sp, rd)
		return fmt.Errorf("DSM hat die Ports nicht übernommen; nichts geändert")
	}
	_ = h.S.Up.SetBase(newBase)
	res.step("DSM antwortet auf Port %d.", movedHTTP)

	front, frontTLS := ":"+strconv.Itoa(hp), ":"+strconv.Itoa(sp)
	if err := h.S.Listen.Ensure(front, false, 60*time.Second); err != nil {
		res.step("Port %s nicht frei (%v) – Rücknahme.", front, err)
		h.rollback(ctx, sid, hp, sp, rd)
		return fmt.Errorf("Port %s blieb belegt; DSM zurückgesetzt", front)
	}
	if err := h.S.Listen.Ensure(frontTLS, true, 60*time.Second); err != nil {
		res.step("HTTPS-Port %s nicht frei: %v (HTTP läuft trotzdem).", frontTLS, err)
	}
	// Selbsttest über den neuen Port
	if !h.selfTest("http://127.0.0.1" + front) {
		res.step("Selbsttest über %s fehlgeschlagen – Rücknahme.", front)
		h.S.Listen.Stop(front)
		h.S.Listen.Stop(frontTLS)
		h.rollback(ctx, sid, hp, sp, rd)
		return fmt.Errorf("Selbsttest fehlgeschlagen; DSM zurückgesetzt")
	}
	c.FrontendMode, c.FrontHTTP, c.FrontHTTPS = true, front, frontTLS
	c.DSMPrevHTTP, c.DSMPrevHTTPS, c.DSMPrevRedirect = hp, sp, rd
	c.Upstream = newBase
	if err := config.Save(h.CfgPath, c); err != nil {
		res.step("Konfiguration speichern: %v", err)
	}
	h.S.SetCfg(c)
	res.step("Fertig: DS video verbindet sich jetzt nur mit IP/DynDNS (Port %d bzw. %d). Die DSM-Oberfläche liegt jetzt auf Port %d (HTTPS %d); Aufrufe über %d werden dorthin weitergeleitet.", hp, sp, movedHTTP, movedHTTPS, hp)
	return nil
}

func (h *Handler) rollback(ctx context.Context, sid string, hp, sp int, rd bool) {
	if err := h.setDSMPorts(ctx, sid, hp, sp, rd); err != nil {
		log.Printf("portmodus: Rücknahme meldet %v", err)
	}
	old := "http://127.0.0.1:" + strconv.Itoa(hp)
	if h.waitDSM(ctx, old, 90*time.Second) {
		_ = h.S.Up.SetBase(old)
	}
}

func (h *Handler) disablePortMode(ctx context.Context, sid string, res *pmResult) error {
	c := *h.S.Cfg()
	if !c.FrontendMode {
		res.step("Port-Modus ist nicht aktiv.")
		return nil
	}
	hp, sp := c.DSMPrevHTTP, c.DSMPrevHTTPS
	if hp == 0 {
		hp, sp = portOf(c.FrontHTTP), portOf(c.FrontHTTPS)
	}
	h.S.Listen.Stop(c.FrontHTTP)
	h.S.Listen.Stop(c.FrontHTTPS)
	res.step("Ports %s/%s freigegeben.", c.FrontHTTP, c.FrontHTTPS)
	if err := h.setDSMPorts(ctx, sid, hp, sp, c.DSMPrevRedirect); err != nil {
		res.step("Antwort auf Portänderung: %v (prüfe Ergebnis …)", err)
	}
	old := "http://127.0.0.1:" + strconv.Itoa(hp)
	if !h.waitDSM(ctx, old, 90*time.Second) {
		// DSM noch auf 5010 → Listener wiederherstellen, damit nichts unerreichbar wird
		_ = h.S.Listen.Ensure(c.FrontHTTP, false, 10*time.Second)
		_ = h.S.Listen.Ensure(c.FrontHTTPS, true, 10*time.Second)
		return fmt.Errorf("DSM antwortet nicht auf Port %d; Port-Modus bleibt aktiv", hp)
	}
	_ = h.S.Up.SetBase(old)
	c.FrontendMode, c.Upstream = false, old
	if err := config.Save(h.CfgPath, c); err != nil {
		res.step("Konfiguration speichern: %v", err)
	}
	h.S.SetCfg(c)
	res.step("DSM wieder auf Port %d/%d. DS video wieder mit Port %s verbinden.", hp, sp, c.ListenHTTP)
	return nil
}

func (h *Handler) selfTest(base string) bool {
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(base + "/webapi/query.cgi?api=SYNO.API.Info&version=1&method=query&query=SYNO.API.Auth,SYNO.VideoStation2.Info")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return strings.Contains(string(b), "SYNO.VideoStation2.Info") && strings.Contains(string(b), "SYNO.API.Auth")
}

// exportDSMCert versucht, das Standardzertifikat von DSM zu exportieren
// (wie Systemsteuerung → Sicherheit → Zertifikat → Exportieren).
func (h *Handler) exportDSMCert(ctx context.Context, sid, token, dataDir string) (string, string, error) {
	m, err := h.dsmCall(ctx, sid, url.Values{"api": {"SYNO.Core.Certificate.CRT"}, "version": {"1"}, "method": {"list"}})
	if err != nil {
		return "", "", err
	}
	d, _ := m["data"].(map[string]any)
	list, _ := d["certificates"].([]any)
	id := ""
	for _, x := range list {
		cm, _ := x.(map[string]any)
		if def, _ := cm["is_default"].(bool); def {
			id, _ = cm["id"].(string)
		}
	}
	if id == "" {
		return "", "", fmt.Errorf("kein Standardzertifikat")
	}
	// Aufrufform wie die DSM-Oberfläche (Download per GET mit file="archive");
	// belegt durch N4S4/synology-api export_cert und aerialls/synology-srm #13
	// (ohne „file“ → Fehler 5503). Ein SynoToken wird nur mitgeschickt, falls
	// vorhanden: Unsere Anmeldung fordert bewusst keinen an, denn eine Sitzung
	// mit SynoToken verlangt ihn bei JEDEM Aufruf (sonst Fehler 119) – das
	// brach in 0.3.5 die Anmeldung an der Weboberfläche.
	var lastErr error
	for _, idv := range []string{id, `"` + id + `"`} {
		q := url.Values{"api": {"SYNO.Core.Certificate"}, "version": {"1"}, "method": {"export"}, "file": {`"archive"`}, "id": {idv}, "_sid": {sid}}
		if token != "" {
			q.Set("SynoToken", token)
		}
		raw, ct, err := h.S.Up.GetRaw(ctx, "entry.cgi", q, map[string]string{"X-SYNO-TOKEN": token})
		if err != nil {
			lastErr = err
			continue
		}
		cf, kf, err := saveCertZip(raw, dataDir)
		if err == nil {
			return cf, kf, nil
		}
		snippet := string(raw)
		if len(snippet) > 160 {
			snippet = snippet[:160]
		}
		lastErr = fmt.Errorf("Export kein ZIP (%s: %s)", ct, strings.TrimSpace(snippet))
	}
	return "", "", lastErr
}

// certSync: DSM-Standardzertifikat übernehmen (Knopf und automatisch nach
// der Anmeldung im Port-Modus). So nutzen 5001 und 5011 dasselbe Zertifikat
// wie vor der Umstellung – die Apps sehen keinen Unterschied.
func (h *Handler) certSync(w http.ResponseWriter, r *http.Request, sid string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST erwartet", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	c := *h.S.Cfg()
	cf, kf, err := h.exportDSMCert(ctx, sid, r.Header.Get("X-DSVR-SYNOTOKEN"), c.DataDir)
	if err != nil {
		log.Printf("admin: DSM-Zertifikat nicht übernommen: %v", err)
		w.WriteHeader(http.StatusBadGateway)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	c.TLSCert, c.TLSKey = cf, kf
	if err := config.Save(h.CfgPath, c); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	h.S.SetCfg(c)
	log.Printf("admin: DSM-Zertifikat übernommen (%s)", certSubject(cf))
	writeJSON(w, map[string]any{"ok": true, "message": "DSM-Zertifikat übernommen: " + certSubject(cf)})
}

// saveCertZip übernimmt ein aus DSM exportiertes Zertifikat (ZIP mit cert.pem,
// privkey.pem, chain.pem – Systemsteuerung → Sicherheit → Zertifikat → Exportieren).
func saveCertZip(raw []byte, dataDir string) (string, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", "", fmt.Errorf("keine ZIP-Datei")
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		files[strings.ToLower(filepath.Base(f.Name))] = b
	}
	// DSM-Export: cert.pem, privkey.pem, chain.pem. Nginx Proxy Manager /
	// Let's Encrypt: fullchainN.pem, privkeyN.pem, certN.pem, chainN.pem.
	pick := func(prefix string) []byte {
		for name, b := range files {
			if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".pem") {
				return b
			}
		}
		return nil
	}
	key := pick("privkey")
	full := pick("fullchain")
	if len(full) == 0 {
		if cert := pick("cert"); len(cert) > 0 {
			full = append(append(append([]byte{}, cert...), '\n'), pick("chain")...)
		}
	}
	if len(full) == 0 || len(key) == 0 {
		return "", "", fmt.Errorf("ZIP ohne Zertifikat/privaten Schlüssel (erwartet cert.pem + privkey.pem oder fullchain*.pem + privkey*.pem)")
	}
	if _, err := tls.X509KeyPair(full, key); err != nil {
		return "", "", fmt.Errorf("Zertifikat/Schlüssel passen nicht: %w", err)
	}
	dir := filepath.Join(dataDir, "tls")
	_ = os.MkdirAll(dir, 0o700)
	cf, kf := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(kf, key, 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(cf, full, 0o644); err != nil {
		return "", "", err
	}
	return cf, kf, nil
}

// certUpload: POST /dsvr/api/cert mit der Export-ZIP aus DSM als Rumpf.
func (h *Handler) certUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST erwartet", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	c := *h.S.Cfg()
	cf, kf, err := saveCertZip(raw, c.DataDir)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	c.TLSCert, c.TLSKey = cf, kf
	if err := config.Save(h.CfgPath, c); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	h.S.SetCfg(c)
	log.Printf("admin: HTTPS-Zertifikat hochgeladen (%s)", certSubject(cf))
	writeJSON(w, map[string]any{"ok": true, "message": "Zertifikat übernommen: " + certSubject(cf) + ". Gilt sofort für neue HTTPS-Verbindungen."})
}

func certSubject(certFile string) string {
	b, err := os.ReadFile(certFile)
	if err != nil {
		return ""
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return ""
	}
	x, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return ""
	}
	names := x.DNSNames
	if len(names) == 0 {
		names = []string{x.Subject.CommonName}
	}
	return strings.Join(names, ", ") + " (gültig bis " + x.NotAfter.Format("02.01.2006") + ")"
}
