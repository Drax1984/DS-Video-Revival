package admin

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/config"
	"dsvideorevival/internal/library"
	"dsvideorevival/internal/listen"
	"dsvideorevival/internal/stream"
	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/vs"
	"dsvideorevival/internal/webapi"
)

// movingDSM simuliert DSM, die auf SYNO.Core.Web.DSM set ihren Port wechselt.
type movingDSM struct {
	mu        sync.Mutex
	port      int
	httpsPort int
	srv       *http.Server
	t         *testing.T
}

func (d *movingDSM) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		api, method := r.Form.Get("api"), r.Form.Get("method")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case api == "SYNO.API.Info":
			io.WriteString(w, `{"data":{"SYNO.API.Auth":{"maxVersion":7,"minVersion":1,"path":"entry.cgi"}},"success":true}`)
		case api == "SYNO.API.Auth" && method == "login":
			io.WriteString(w, `{"data":{"sid":"ADMINSID","did":"","is_portal_port":false},"success":true}`)
		case api == "SYNO.FileStation.Info":
			io.WriteString(w, `{"data":{"is_manager":true},"success":true}`)
		case api == "SYNO.Core.Web.DSM" && method == "get":
			d.mu.Lock()
			io.WriteString(w, `{"data":{"http_port":`+strconv.Itoa(d.port)+`,"https_port":`+strconv.Itoa(d.httpsPort)+`,"enable_https_redirect":false},"success":true}`)
			d.mu.Unlock()
		case api == "SYNO.Core.Web.DSM" && method == "set":
			np, _ := strconv.Atoi(r.Form.Get("http_port"))
			hp, _ := strconv.Atoi(r.Form.Get("https_port"))
			io.WriteString(w, `{"success":true}`)
			go d.move(np, hp)
		case api == "SYNO.Core.Certificate.CRT":
			io.WriteString(w, `{"data":{"certificates":[]},"success":true}`)
		default:
			io.WriteString(w, `{"data":{"echo":"dsm"},"success":true}`)
		}
	})
}

func (d *movingDSM) start(port int) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		d.t.Fatalf("DSM-Port %d: %v", port, err)
	}
	srv := &http.Server{Handler: d.handler()}
	d.mu.Lock()
	d.srv, d.port = srv, port
	d.mu.Unlock()
	go srv.Serve(ln)
}

func (d *movingDSM) move(port, https int) {
	time.Sleep(300 * time.Millisecond) // DSM startet nginx neu
	d.mu.Lock()
	old := d.srv
	d.httpsPort = https
	d.mu.Unlock()
	old.Close()
	time.Sleep(300 * time.Millisecond)
	d.start(port)
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}

func TestPortModeSwitchAndBack(t *testing.T) {
	if l, err := net.Listen("tcp", "127.0.0.1:5010"); err != nil {
		t.Skip("Port 5010 belegt")
	} else {
		l.Close()
	}
	dsmPort, dsmTLS, maint := freePort(t), freePort(t), freePort(t)
	d := &movingDSM{t: t, httpsPort: dsmTLS}
	d.start(dsmPort)
	defer func() { d.mu.Lock(); d.srv.Close(); d.mu.Unlock() }()

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.AuthMode, cfg.Upstream, cfg.DataDir = "dsm", "http://127.0.0.1:"+strconv.Itoa(dsmPort), dir
	cfg.ListenHTTP = "127.0.0.1:" + strconv.Itoa(maint)
	cfgPath := filepath.Join(dir, "config.json")
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	up, _ := upstream.New(cfg.Upstream)
	lib, _ := library.Open(filepath.Join(dir, "lib.json"))
	s := &vs.Server{Auth: auth.NewAdapter("dsm", up, nil, auth.NewStore(""), nil), Up: up, Lib: lib, Streams: stream.NewManager(),
		Rec: vs.NewRecorder(""), Registry: webapi.VideoStationAPIs(), Started: time.Now()}
	s.SetCfg(cfg)
	s.AdminHandler = New(s, cfgPath, "", make(chan struct{}, 1))
	s.Listen = listen.New(s, func() (string, string) { return "", "" })
	defer s.Listen.CloseAll()
	if err := s.Listen.Ensure(cfg.ListenHTTP, false, time.Second); err != nil {
		t.Fatal(err)
	}
	base := "http://" + cfg.ListenHTTP

	post := func(enable bool) pmResult {
		body := strings.NewReader(`{"enable":` + strconv.FormatBool(enable) + `}`)
		req, _ := http.NewRequest("POST", base+"/dsvr/api/portmode", body)
		req.Header.Set("X-DSVR-SID", "ADMINSID")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r pmResult
		_ = json.NewDecoder(resp.Body).Decode(&r)
		return r
	}
	r := post(true)
	if !r.OK {
		t.Fatalf("Umstellung: %+v", r)
	}
	t.Logf("Schritte: %s", strings.Join(r.Steps, " | "))
	// DS video spricht jetzt den alten DSM-Port an und landet bei uns
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(dsmPort) + "/webapi/query.cgi?api=SYNO.API.Info&version=1&method=query&query=all")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "SYNO.VideoStation2.Streaming") || !strings.Contains(string(b), "SYNO.API.Auth") {
		t.Fatalf("Port %d liefert keine Video-Station-APIs: %s", dsmPort, b)
	}
	// fremde DSM-API wird an DSM (jetzt 5010) durchgereicht
	resp, _ = http.PostForm("http://127.0.0.1:"+strconv.Itoa(dsmPort)+"/webapi/entry.cgi", url.Values{"api": {"SYNO.Core.Foo"}, "version": {"1"}, "method": {"get"}})
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"echo":"dsm"`) {
		t.Fatalf("Durchreichen: %s", b)
	}
	if c, _ := config.Load(cfgPath); !c.FrontendMode || c.Upstream != "http://127.0.0.1:5010" {
		t.Fatalf("Konfiguration nicht gespeichert: %+v", c)
	}
	// zurück
	r = post(false)
	if !r.OK {
		t.Fatalf("Zurückschalten: %+v", r)
	}
	if d.port != dsmPort {
		t.Fatalf("DSM nicht zurück auf %d (ist %d)", dsmPort, d.port)
	}
	if c, _ := config.Load(cfgPath); c.FrontendMode {
		t.Fatal("frontend_mode nicht zurückgesetzt")
	}
}
