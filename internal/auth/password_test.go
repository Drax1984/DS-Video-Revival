package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"dsvideorevival/internal/upstream"
)

// TestCheckPassword: Passwortprüfung über DSM (Jellyfin-Anmeldung) – mit
// Rückfall auf andere Sitzungsnamen (402), 2FA (403) und Abmeldung der
// kurzen DSM-Sitzung.
func TestCheckPassword(t *testing.T) {
	var mu sync.Mutex
	loggedOut, xff := 0, ""
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("api") + "." + r.Form.Get("method") {
		case "SYNO.API.Auth.login":
			mu.Lock()
			xff = r.Header.Get("X-Forwarded-For")
			mu.Unlock()
			switch {
			case r.Form.Get("account") == "otp":
				io.WriteString(w, `{"error":{"code":403},"success":false}`)
			case r.Form.Get("passwd") != "geheim":
				io.WriteString(w, `{"error":{"code":400},"success":false}`)
			case r.Form.Get("account") == "tom" && r.Form.Get("session") != "webui":
				io.WriteString(w, `{"error":{"code":402},"success":false}`) // erst „webui“ klappt
			default:
				io.WriteString(w, `{"data":{"sid":"S1"},"success":true}`)
			}
		case "SYNO.API.Auth.logout":
			mu.Lock()
			loggedOut++
			mu.Unlock()
			io.WriteString(w, `{"success":true}`)
		case "SYNO.Core.Desktop.Initdata.get":
			io.WriteString(w, `{"data":{"Session":{"user":"Tom","is_admin":false},"AppPrivilege":{"SYNO.SDS.DSVideoRevival":true}},"success":true}`)
		default:
			io.WriteString(w, `{"success":false,"error":{"code":103}}`)
		}
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	a := NewAdapter("dsm", up, nil, NewStore(""), nil)
	id, err := a.CheckPassword(context.Background(), "tom", "geheim", "192.168.1.20")
	if err != nil || id.Account != "Tom" || id.IsManager == nil || *id.IsManager {
		t.Fatalf("tom: %+v %v", id, err)
	}
	mu.Lock()
	if loggedOut != 1 || xff != "192.168.1.20" {
		t.Fatalf("Abmeldung %d, X-Forwarded-For %q", loggedOut, xff)
	}
	mu.Unlock()
	if _, err := a.CheckPassword(context.Background(), "tom", "falsch", "192.168.1.20"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("falsches Passwort: %v", err)
	}
	if _, err := a.CheckPassword(context.Background(), "otp", "geheim", "192.168.1.20"); !errors.Is(err, ErrNeedOTP) {
		t.Fatalf("2FA: %v", err)
	}
	// Sperre nach Fehlversuchen (eigene Begrenzung vor DSM)
	for i := 0; i < 6; i++ {
		_, _ = a.CheckPassword(context.Background(), "x", "falsch", "10.0.0.9")
	}
	if _, err := a.CheckPassword(context.Background(), "tom", "geheim", "10.0.0.9"); !errors.Is(err, ErrTooManyTries) {
		t.Fatalf("Sperre: %v", err)
	}
}
