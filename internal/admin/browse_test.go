package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/vs"
)

// Leserecht setzen: nur der eigene Eintrag wird geschickt. Ersetzt DSM beim
// „set“ die ganze Liste (unbelegt, aber denkbar), werden die übrigen
// Einträge wiederhergestellt.
func TestGrantReadKeepsOthers(t *testing.T) {
	for _, replace := range []bool{false, true} {
		var mu sync.Mutex
		perms := []map[string]any{
			{"name": "pkg", "is_readonly": false, "is_writable": false, "is_deny": false, "is_custom": false},
			{"name": "other", "is_readonly": true, "is_writable": false, "is_deny": false, "is_custom": false},
			{"name": "writer", "is_readonly": false, "is_writable": true, "is_deny": false, "is_custom": false},
		}
		dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			mu.Lock()
			defer mu.Unlock()
			if r.Form.Get("api") != "SYNO.Core.Share.Permission" || r.Form.Get("user_group_type") != "system" || r.Form.Get("name") != "video" {
				w.Write([]byte(`{"success":false,"error":{"code":103}}`))
				return
			}
			switch r.Form.Get("method") {
			case "list":
				b, _ := json.Marshal(map[string]any{"success": true, "data": map[string]any{"items": perms, "total": len(perms)}})
				w.Write(b)
			case "set":
				var in []map[string]any
				_ = json.Unmarshal([]byte(r.Form.Get("permissions")), &in)
				if replace {
					// alle nicht genannten Einträge auf „kein Recht“
					for _, p := range perms {
						p["is_readonly"], p["is_writable"] = false, false
					}
				}
				for _, n := range in {
					for _, p := range perms {
						if p["name"] == n["name"] {
							for k, v := range n {
								p[k] = v
							}
						}
					}
				}
				w.Write([]byte(`{"success":true}`))
			}
		}))
		up, _ := upstream.New(dsm.URL)
		h := &Handler{S: &vs.Server{Up: up}}
		if err := h.grantPerm(context.Background(), "SID", "video", "pkg", false); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		if perms[0]["is_readonly"] != true || perms[1]["is_readonly"] != true || perms[2]["is_writable"] != true {
			t.Fatalf("replace=%v: %v", replace, perms)
		}
		mu.Unlock()
		dsm.Close()
	}
}

func TestSplitShare(t *testing.T) {
	if v, s, fp, ok := splitShare("/volume1/video/Filme/"); !ok || v != "/volume1" || s != "video" || fp != "/video/Filme" {
		t.Fatal(v, s, fp, ok)
	}
	for _, bad := range []string{"/", "/volume1", "/etc/passwd", "/volume1/@appstore/x", "/volume1/../etc"} {
		if _, _, _, ok := splitShare(bad); ok {
			t.Fatal("akzeptiert:", bad)
		}
	}
}
