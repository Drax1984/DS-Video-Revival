// dsvr – DS Video Revival.
//
//	dsvr serve       --config var/config.json [--log var/dsvr.log]
//	dsvr init-config --config var/config.json [--port 5180] [--upstream URL] [--folders "movie:/volume1/video/Filme;tvshow:/volume1/video/Serien"]
//	dsvr adduser     --users var/users.json --account NAME [--manager]   (Passwort über stdin)
//	dsvr capture     --listen :5081 --upstream http://NAS:5000 --out capture.jsonl
//	dsvr analyze     capture.jsonl [--out report.md]
//	dsvr probe       --base http://NAS:5180 --account NAME              (Passwort: $DSVR_PASSWORD oder stdin)
//	dsvr version
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"dsvideorevival/internal/admin"
	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/capture"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/listen"
	"dsvideorevival/internal/metadata"
	"dsvideorevival/internal/probe"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/vs"
	"dsvideorevival/internal/webapi"
)

// version wird beim Build per -ldflags gesetzt.
var version = "0.1.0-dev"

// DSMBridgePort: interner Port, an den DSMs Webserver die DS-video-Aufrufe
// weiterreicht (muss zu packaging/synology/nginx/dsm.conf passen).
const DSMBridgePort = "5180"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	admin.Version = version
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "init-config":
		err = cmdInitConfig(os.Args[2:])
	case "adduser":
		err = cmdAddUser(os.Args[2:])
	case "capture":
		err = cmdCapture(os.Args[2:])
	case "analyze":
		err = cmdAnalyze(os.Args[2:])
	case "probe":
		err = cmdProbe(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("dsvr", version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `DS Video Revival `+version+`
Befehle: serve | init-config | adduser | capture | analyze | probe | version
Hilfe zu einem Befehl: dsvr <befehl> -h`)
}

// ---------- serve ----------

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "var/config.json", "Konfigurationsdatei")
	logPath := fs.String("log", "", "Logdatei (für die Anzeige in der Admin-Oberfläche)")
	logCalls := fs.Bool("log-calls", true, "jeden Video-Station-Aufruf protokollieren")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("Konfiguration %s: %w", *cfgPath, err)
	}
	// data_dir leer → Ordner der Konfigurationsdatei; relativ → relativ zu diesem.
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Dir(*cfgPath)
	} else if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(filepath.Dir(*cfgPath), cfg.DataDir)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	log.SetFlags(log.LstdFlags)
	log.Printf("DS Video Revival %s startet (Anmeldung: %s, Daten: %s)", version, cfg.AuthMode, cfg.DataDir)

	var up *upstream.Client
	var local *auth.LocalUsers
	if cfg.AuthMode == "dsm" {
		if up, err = upstream.New(cfg.Upstream); err != nil {
			return err
		}
	} else {
		if local, err = auth.NewLocalUsers(cfg.UsersFile); err != nil {
			return fmt.Errorf("Benutzerdatei: %w", err)
		}
	}
	store := auth.NewStore(filepath.Join(cfg.DataDir, "sessions.json"))
	ad := auth.NewAdapter(cfg.AuthMode, up, local, store, cfg.AdminUsers)
	lib, err := library.Open(filepath.Join(cfg.DataDir, "library.json"))
	if err != nil {
		return err
	}
	srv := &vs.Server{Auth: ad, Up: up, Lib: lib, Streams: stream.NewManager(),
		Rec: vs.NewRecorder(filepath.Join(cfg.DataDir, "unhandled.json")), Registry: webapi.VideoStationAPIs(),
		Started: time.Now(), LogCalls: *logCalls}
	srv.SetCfg(cfg)
	ffmpeg := stream.FindFFmpeg(cfg.FFmpeg)
	if ffmpeg == "" {
		log.Printf("FFmpeg nicht gefunden – nur Direktwiedergabe. Für MKV/AVI/AC3/DTS usw.: Paket „ffmpeg7“ (SynoCommunity) installieren.")
	} else {
		log.Printf("ffmpeg: %s", ffmpeg)
	}
	probeHint := cfg.FFmpeg
	if probeHint == "" {
		probeHint = ffmpeg
	}
	if os.Getenv("DSVR_NO_FFPROBE") == "" { // nur Tests: Synology ohne ffprobe nachstellen
		srv.FFprobe = library.FindFFprobe(probeHint)
	}
	switch {
	case srv.FFprobe != "":
		log.Printf("Analyse: %s", srv.FFprobe)
	case ffmpeg != "":
		srv.FFprobe = ffmpeg // Stream-Übersicht von „ffmpeg -i“ auswerten
		log.Printf("Analyse: ffprobe fehlt – nutze %s", ffmpeg)
	default:
		log.Printf("Analyse nicht möglich (weder ffprobe noch ffmpeg) – Auflösung/Laufzeit bleiben leer")
	}
	srv.HLS = stream.NewHLSManager(ffmpeg, filepath.Join(cfg.DataDir, "tmp", "hls"), cfg.MaxTranscodes)
	srv.HLS.HWAccel = cfg.HWAccel
	srv.Offline = stream.NewOfflineManager(srv.HLS, filepath.Join(cfg.DataDir, "offline"))
	srv.Offline.FFprobe = func() string { return srv.FFprobe }
	srv.Meta = metadata.NewEngine(lib)
	srv.Meta.CacheDir = filepath.Join(cfg.DataDir, "cache", "images")
	srv.Meta.SetClient(metadata.NewClient(cfg.TMDbKey, cfg.OMDbKey, cfg.MetaLanguage, srv.Meta.CacheDir))
	go srv.Meta.Run()
	scanCh := make(chan struct{}, 1)
	srv.AdminHandler = admin.New(srv, *cfgPath, *logPath, scanCh)

	go scanLoop(srv, scanCh)
	scanCh <- struct{}{}

	// Zertifikat: konfiguriert → aus DSM exportiert → selbstsigniert.
	selfCert, selfKey := filepath.Join(cfg.DataDir, "selfsigned.crt"), filepath.Join(cfg.DataDir, "selfsigned.key")
	if err := ensureSelfSigned(selfCert, selfKey); err != nil {
		log.Printf("selbstsigniertes Zertifikat: %v", err)
	}
	certs := func() (string, string) {
		c := srv.Cfg()
		if c.TLSCert != "" && c.TLSKey != "" {
			return c.TLSCert, c.TLSKey
		}
		return selfCert, selfKey
	}
	lm := listen.New(srv, certs)
	srv.Listen = lm
	// Wartungs-/Standardport: muss sofort klappen.
	if err := lm.Ensure(cfg.ListenHTTP, false, 5*time.Second); err != nil {
		return fmt.Errorf("Port %s: %w", cfg.ListenHTTP, err)
	}
	if err := lm.Ensure(cfg.ListenHTTPS, true, 5*time.Second); err != nil {
		log.Printf("HTTPS %s: %v", cfg.ListenHTTPS, err)
	}
	// Brücke für DSMs Webserver: die nginx-Einbindung (packaging/synology/nginx)
	// leitet DS video fest an 127.0.0.1:5180 – unabhängig vom Wartungsport.
	if !strings.HasSuffix(cfg.ListenHTTP, ":"+DSMBridgePort) {
		if err := lm.Ensure("127.0.0.1:"+DSMBridgePort, false, 5*time.Second); err != nil {
			log.Printf("DSM-Brücke 127.0.0.1:%s: %v – DS video über DSM nicht erreichbar", DSMBridgePort, err)
		}
	}
	if cfg.FrontendMode {
		// Port-Modus: 5000/5001 übernehmen (DSM läuft auf den verschobenen Ports).
		go func() {
			if err := lm.Ensure(cfg.FrontHTTP, false, 120*time.Second); err != nil {
				log.Printf("Port-Modus: %s nicht frei: %v – DSM noch auf diesem Port?", cfg.FrontHTTP, err)
			}
		}()
		go func() {
			if err := lm.Ensure(cfg.FrontHTTPS, true, 120*time.Second); err != nil {
				log.Printf("Port-Modus: %s nicht frei: %v", cfg.FrontHTTPS, err)
			}
		}()
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	s := <-sig
	log.Printf("Signal %v – beende", s)
	_ = lib.Flush()
	lm.CloseAll()
	return nil
}

func scanLoop(srv *vs.Server, trigger chan struct{}) {
	last := time.Time{}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	run := func() {
		res, err := srv.Lib.Scan(srv.Cfg().Folders)
		if err != nil {
			log.Printf("scan: %v", err)
			return
		}
		last = time.Now()
		go probeMissing(srv)
		srv.Meta.Trigger()
		log.Printf("scan: %d Dateien (+%d, ~%d, -%d) in %s, %d Hinweise", res.Total, res.Added, res.Changed, res.Removed, res.Duration.Round(time.Millisecond), len(res.Errors))
		for i, e := range res.Errors {
			if i >= 10 {
				break
			}
			log.Printf("scan: %s", e)
		}
	}
	for {
		select {
		case <-trigger:
			run()
		case <-tick.C:
			iv := srv.Cfg().ScanIntervalMinutes
			if iv > 0 && time.Since(last) >= time.Duration(iv)*time.Minute {
				run()
			}
		}
	}
}

var probing sync.Mutex

// probeMissing analysiert neue Dateien im Hintergrund (Codecs, Auflösung, Dauer).
func probeMissing(srv *vs.Server) {
	if srv.FFprobe == "" || !probing.TryLock() {
		return
	}
	defer probing.Unlock()
	n := 0
	for _, it := range func() []library.Item { items, _ := srv.Lib.Items(library.Query{}); return items }() {
		for _, f := range srv.Lib.Files(it) {
			if f.Probe.NeedsProbe(srv.FFprobe) {
				srv.Lib.SetProbe(f.ID, library.RunProbe(srv.FFprobe, f.Path))
				n++
			}
		}
	}
	if n > 0 {
		log.Printf("probe: %d Dateien analysiert", n)
	}
}

func ensureSelfSigned(certFile, keyFile string) error {
	if _, err := os.Stat(certFile); err == nil {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	host, _ := os.Hostname()
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "DS Video Revival"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{host, "localhost"}, IPAddresses: localIPs()}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func localIPs() []net.IP {
	ips := []net.IP{net.ParseIP("127.0.0.1")}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
			ips = append(ips, n.IP)
		}
	}
	return ips
}

// ---------- init-config ----------

func cmdInitConfig(args []string) error {
	fs := flag.NewFlagSet("init-config", flag.ExitOnError)
	cfgPath := fs.String("config", "var/config.json", "Zieldatei")
	port := fs.Int("port", 5180, "HTTP-Port")
	httpsPort := fs.Int("https-port", 0, "HTTPS-Port (0 = aus)")
	upstreamURL := fs.String("upstream", "http://127.0.0.1:5000", "DSM-WebAPI")
	folders := fs.String("folders", "", `Medienordner "typ:/pfad;typ:/pfad" (typ: movie|tvshow|home_video)`)
	authMode := fs.String("auth", "dsm", "dsm | local")
	users := fs.String("users", "", "Benutzerdatei (auth=local)")
	force := fs.Bool("force", false, "vorhandene Datei überschreiben")
	_ = fs.Parse(args)
	if _, err := os.Stat(*cfgPath); err == nil && !*force {
		fmt.Println("Konfiguration existiert bereits, unverändert:", *cfgPath)
		return nil
	}
	c := config.Defaults()
	c.ListenHTTP = ":" + strconv.Itoa(*port)
	if *httpsPort > 0 {
		c.ListenHTTPS = ":" + strconv.Itoa(*httpsPort)
	}
	c.Upstream, c.AuthMode, c.UsersFile = *upstreamURL, *authMode, *users
	if *folders != "" {
		f, err := config.ParseFolderList(*folders)
		if err != nil {
			return err
		}
		c.Folders = f
	}
	if err := config.Save(*cfgPath, c); err != nil {
		return err
	}
	fmt.Println("Konfiguration geschrieben:", *cfgPath)
	return nil
}

// ---------- adduser ----------

func cmdAddUser(args []string) error {
	fs := flag.NewFlagSet("adduser", flag.ExitOnError)
	path := fs.String("users", "var/users.json", "Benutzerdatei")
	account := fs.String("account", "", "Kontoname")
	manager := fs.Bool("manager", false, "Administrator")
	_ = fs.Parse(args)
	if *account == "" {
		return errors.New("--account fehlt")
	}
	pw, err := readPassword()
	if err != nil {
		return err
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	var list []auth.LocalUser
	if b, err := os.ReadFile(*path); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	out := list[:0]
	for _, u := range list {
		if !strings.EqualFold(u.Account, *account) {
			out = append(out, u)
		}
	}
	out = append(out, auth.LocalUser{Account: *account, Hash: h, IsManager: *manager})
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.MkdirAll(filepath.Dir(*path), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(*path, b, 0o600); err != nil {
		return err
	}
	fmt.Println("Benutzer gespeichert:", *account)
	return nil
}

func readPassword() (string, error) {
	if p := os.Getenv("DSVR_PASSWORD"); p != "" {
		return p, nil
	}
	fmt.Fprint(os.Stderr, "Passwort: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("kein Passwort gelesen")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ---------- capture / analyze / probe ----------

func cmdCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	listen := fs.String("listen", ":5081", "Adresse, die in der DS-Video-App eingetragen wird")
	up := fs.String("upstream", "", "Original-Video-Station, z.B. http://192.168.0.10:5000")
	out := fs.String("out", "capture.jsonl", "Ausgabedatei (JSONL, ohne Passwörter/sids)")
	_ = fs.Parse(args)
	if *up == "" {
		return errors.New("--upstream fehlt")
	}
	p, err := capture.NewProxy(*up, *out)
	if err != nil {
		return err
	}
	log.Printf("Mitschnitt: DS Video → %s → %s, Ausgabe %s", *listen, *up, *out)
	log.Printf("In der DS-Video-App als Adresse <IP-dieses-Rechners>%s eintragen (HTTPS in der App deaktivieren).", *listen)
	return (&http.Server{Addr: *listen, Handler: p, ReadHeaderTimeout: 15 * time.Second}).ListenAndServe()
}

func cmdAnalyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	out := fs.String("out", "", "Markdown-Ausgabe (Standard: stdout)")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("Mitschnittdatei angeben")
	}
	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	return capture.Analyze(fs.Arg(0), w)
}

func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	base := fs.String("base", "http://127.0.0.1:5180", "Server (DS Video Revival oder Original-Video-Station)")
	account := fs.String("account", "", "Konto")
	device := fs.String("device", "tvos", "hls_remux-Gerätekennung")
	raw := fs.Bool("raw", false, "raw={} statt hls_remux öffnen")
	verbose := fs.Bool("v", false, "Antworten ausgeben")
	insecure := fs.Bool("insecure", false, "TLS-Zertifikat nicht prüfen (selbstsigniert)")
	_ = fs.Parse(args)
	if *account == "" {
		return errors.New("--account fehlt")
	}
	pw, err := readPassword()
	if err != nil {
		return err
	}
	return probe.Run(probe.Options{Base: *base, Account: *account, Password: pw, Device: *device, Raw: *raw, Verbose: *verbose, Insecure: *insecure, Out: os.Stdout})
}
