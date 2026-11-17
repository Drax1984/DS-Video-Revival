package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// integration prüft, ob DSMs eigener Webserver die DS-video-Apps an dieses
// Paket weiterreicht (nginx-Einbindung über den Ressourcen-Worker web-config).
// Dazu wird – wie die App – query.cgi über DSM abgefragt.
func (h *Handler) integration(w http.ResponseWriter, r *http.Request) {
	c := h.S.Cfg()
	res := map[string]any{"frontend_mode": c.FrontendMode, "upstream": c.Upstream}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	body := url.Values{"api": {"SYNO.API.Info"}, "version": {"1"}, "method": {"query"}, "query": {"SYNO.API.Auth,SYNO.VideoStation2.Info"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Upstream, "/")+"/webapi/query.cgi", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Synology-DS_video_DSVideoRevival-Selbsttest")
	resp, err := http.DefaultClient.Do(req)
	switch {
	case err != nil:
		res["ok"], res["message"] = false, "DSM nicht erreichbar: "+err.Error()
	default:
		defer resp.Body.Close()
		var m struct {
			Data map[string]struct {
				Path string `json:"path"`
			} `json:"data"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = json.Unmarshal(raw, &m)
		vs, okVS := m.Data["SYNO.VideoStation2.Info"]
		_, okAuth := m.Data["SYNO.API.Auth"]
		switch {
		case okVS && strings.HasPrefix(vs.Path, "_dsvr/") && okAuth:
			res["ok"], res["message"] = true, "DSM reicht DS video an DS Video Revival weiter. In der App genügt die normale DSM-Adresse (IP, DynDNS oder Domain) – wie früher bei Video Station."
		case okAuth:
			res["ok"], res["message"] = false, "DSM antwortet, reicht DS video aber nicht weiter. Paket einmal deinstallieren und neu installieren (DSM richtet die Weiterleitung bei der Installation ein)."
		default:
			res["ok"], res["message"] = false, "Unerwartete Antwort von DSM (HTTP "+resp.Status+")"
		}
	}
	writeJSON(w, res)
}
