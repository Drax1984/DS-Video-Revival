// hlslab: Entwicklerwerkzeug (nicht im Paket). Exportiert genau das, was der
// Server für eine Datei ausliefert (Liste, init.mp4, Segmente), als statische
// Dateien – und liefert sie mit „serve“ samt Testseite aus, z.B. für Safari
// auf einem Mac (Apple-Player wie auf dem iPhone).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/stream"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("hlslab export|serve …")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "export":
		export(os.Args[2:])
	case "serve":
		serve(os.Args[2:])
	}
}

func export(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	src := fs.String("src", "", "Quelldatei")
	out := fs.String("out", "", "Zielordner")
	mode := fs.String("mode", "remux", "remux|transcode")
	ts := fs.Bool("ts", false, "MPEG-TS")
	hevc := fs.Bool("hevc", false, "Video ist HEVC")
	copyA := fs.Bool("copyaudio", true, "Ton kopieren")
	dur := fs.Float64("dur", 60, "Dauer")
	segs := fs.Int("segs", 0, "nur so viele Segmente (0 = alle)")
	start := fs.Float64("start", 0, "Startstelle")
	_ = fs.Parse(args)
	ff, _ := os.LookupEnv("FFMPEG")
	if ff == "" {
		ff = stream.FindFFmpeg("")
	}
	work, _ := os.MkdirTemp("", "hlslab")
	defer os.RemoveAll(work)
	m := stream.NewHLSManager(ff, work, 2)
	m.HWAccel = "off"
	d := stream.Decision{Mode: stream.ModeRemux, CopyVideo: true, CopyAudio: *copyA, HEVC: *hevc, Client: "apple", Duration: *dur, TS: *ts}
	if *mode == "transcode" {
		d = stream.Decision{Mode: stream.ModeTranscode, CopyAudio: *copyA, Client: "apple", Duration: *dur, TS: *ts}
	}
	j, err := m.Start("lab", *src, d, *start, 0)
	if err != nil {
		log.Fatal(err)
	}
	defer m.Stop(j.ID)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeFileRanged(w, r, j, strings.TrimPrefix(r.URL.Path, "/"), "")
	}))
	defer srv.Close()
	_ = os.MkdirAll(*out, 0o755)
	get := func(name string) []byte {
		resp, err := http.Get(srv.URL + "/" + name)
		if err != nil || resp.StatusCode != 200 {
			log.Fatalf("%s: %v %v", name, err, resp)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if err := os.WriteFile(filepath.Join(*out, name), b, 0o644); err != nil {
			log.Fatal(err)
		}
		return b
	}
	pl := string(get("index.m3u8"))
	n := 0
	var keep []string
	for _, l := range strings.Split(pl, "\n") {
		if strings.HasPrefix(l, "#EXT-X-MAP:URI=") {
			get(strings.Trim(strings.TrimPrefix(l, "#EXT-X-MAP:URI="), `"`))
		}
		if l != "" && !strings.HasPrefix(l, "#") {
			if *segs > 0 && n >= *segs {
				// Liste kürzen (Rest fehlt)
				continue
			}
			get(l)
			n++
		}
		keep = append(keep, l)
	}
	if *segs > 0 {
		// gekürzte Liste: nur exportierte Segmente
		var out2 []string
		cnt := 0
		lines := strings.Split(pl, "\n")
		for i := 0; i < len(lines); i++ {
			l := lines[i]
			if strings.HasPrefix(l, "#EXTINF") {
				if cnt >= *segs {
					i++
					continue
				}
				cnt++
			}
			out2 = append(out2, l)
		}
		pl = strings.Join(out2, "\n")
		if !strings.Contains(pl, "#EXT-X-ENDLIST") {
			pl += "#EXT-X-ENDLIST\n"
		}
		_ = os.WriteFile(filepath.Join(*out, "index.m3u8"), []byte(pl), 0o644)
	}
	_ = keep
	fmt.Printf("%s: %d Segmente\n", *out, n)
}

type entry struct {
	At     time.Time
	Path   string
	Range  string
	Status int
	Bytes  int64
	Ms     int64
	Agent  string
}

type rec struct {
	http.ResponseWriter
	status int
	n      int64
}

func (r *rec) WriteHeader(c int) { r.status = c; r.ResponseWriter.WriteHeader(c) }
func (r *rec) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.n += int64(n)
	return n, err
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dir := fs.String("dir", ".", "Ordner")
	addr := fs.String("addr", "127.0.0.1:8765", "Adresse")
	resFile := fs.String("results", "", "Ergebnisse als JSON hierhin schreiben")
	_ = fs.Parse(args)
	save := func(v any, name string) {
		if *resFile == "" {
			return
		}
		b, _ := json.MarshalIndent(v, "", " ")
		_ = os.WriteFile(filepath.Join(filepath.Dir(*resFile), name), b, 0o644)
	}
	var mu sync.Mutex
	var logs []entry
	results := map[string]any{}
	types := map[string]string{".m3u8": "application/vnd.apple.mpegurl", ".m4s": "video/mp4", ".mp4": "video/mp4", ".ts": "video/mp2t", ".html": "text/html; charset=utf-8", ".js": "text/javascript"}
	http.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(logs)
	})
	http.HandleFunc("/result", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "POST" {
			var v map[string]any
			_ = json.NewDecoder(r.Body).Decode(&v)
			for k, x := range v {
				results[k] = x
			}
			log.Printf("Ergebnis: %v", v)
			save(results, filepath.Base(*resFile))
			save(logs, "requests.json")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(results)
	})
	// Als „Jellyfin-Server“ in der App eintragbar: Die App prüft
	// /System/Info/Public und lädt dann /web/index.html in ihr Web-Fenster –
	// so läuft die Testseite im echten Web-Fenster der App.
	info := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(map[string]any{"LocalAddress": "http://" + r.Host, "ServerName": "HLS-Labor", "Version": "10.10.7",
			"ProductName": "Jellyfin Server", "OperatingSystem": "Linux", "Id": "0123456789abcdef0123456789abcdef", "StartupWizardCompleted": true})
	}
	http.HandleFunc("/System/Info/Public", info)
	http.HandleFunc("/system/info/public", info)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/web/") || r.URL.Path == "/web" {
			r.URL.Path = strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/web"), "/")
			r.URL.Path = "/" + r.URL.Path
		}
		t0 := time.Now()
		rw := &rec{ResponseWriter: w, status: 200}
		p := filepath.Join(*dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			p = filepath.Join(p, "index.html")
		}
		if ct, ok := types[filepath.Ext(p)]; ok {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(rw, r, p)
		if !strings.HasSuffix(r.URL.Path, ".html") && r.URL.Path != "/" {
			mu.Lock()
			logs = append(logs, entry{t0, r.URL.Path, r.Header.Get("Range"), rw.status, rw.n, time.Since(t0).Milliseconds(), r.UserAgent()})
			if len(logs) > 400 {
				logs = logs[len(logs)-400:]
			}
			mu.Unlock()
		}
	})
	log.Printf("http://%s/", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
