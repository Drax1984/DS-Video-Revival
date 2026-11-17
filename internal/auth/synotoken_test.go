package auth

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/webapi"
)

// Regression 0.3.5: Die Weboberfläche bat DSM um eine SynoToken-Sitzung; DSM
// verlangt den Token dann bei jedem Aufruf, die Sitzungsprüfung scheiterte
// (Anmeldung „ging nicht“). Der Parameter wird für diese Sitzung entfernt.
func TestWebUILoginWithoutSynoToken(t *testing.T) {
	var got url.Values
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if q, _ := url.ParseQuery(string(b)); q.Get("method") == "login" {
			got = q
		}
		io.WriteString(w, `{"data":{"sid":"S1"},"success":true}`)
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	a := NewAdapter("dsm", up, nil, NewStore(""), nil)
	for _, session := range []string{"DSVideoRevival", "VideoStation"} {
		body := "api=SYNO.API.Auth&version=6&method=login&account=admin&passwd=x&format=sid&enable_syno_token=yes&session=" + session
		r := httptest.NewRequest("POST", "/webapi/entry.cgi", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
		raw := []byte(body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		req, err := webapi.Parse(r)
		if err != nil {
			t.Fatal(err)
		}
		a.HandleAuth(httptest.NewRecorder(), req, raw)
		has := got.Get("enable_syno_token") != ""
		if want := session != "DSVideoRevival"; has != want || got.Get("passwd") != "x" {
			t.Fatalf("Sitzung %s: enable_syno_token weitergereicht=%v, Daten %v", session, has, got)
		}
	}
}
