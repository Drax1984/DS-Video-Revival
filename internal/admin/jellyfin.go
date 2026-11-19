package admin

// Jellyfin-Schnittstelle in der Weboberfläche: Quick-Connect-Code bestätigen,
// eigene (bzw. als Administrator alle) angemeldeten Jellyfin-Geräte.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"dsvideorevival/internal/auth"
)

func (h *Handler) jellyfin(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	jf := h.S.Jellyfin
	if jf == nil {
		jsonErr(w, http.StatusNotFound, "Jellyfin-Schnittstelle nicht verfügbar")
		return
	}
	admin := h.S.IsAppAdmin(sess)
	owner := sess.Account
	if admin {
		owner = "" // Administratoren sehen und verwalten alle Geräte
	}
	switch {
	case r.Method == http.MethodGet:
		st := jf.Status(r)
		st["devices"] = jf.Devices(owner)
		st["admin"] = admin
		writeJSON(w, st)
	case r.Method == http.MethodPost && r.URL.Path == "/dsvr/api/jellyfin/quickconnect":
		var req struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req)
		if err := jf.QuickConnect(req.Code, sess); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case r.Method == http.MethodDelete:
		id := r.URL.Query().Get("id")
		if !jf.RemoveDevice(id, owner) {
			jsonErr(w, http.StatusNotFound, "Gerät nicht gefunden")
			return
		}
		log.Printf("jellyfin: %s hat ein Gerät abgemeldet", sess.Account)
		writeJSON(w, map[string]any{"ok": true})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "GET/POST/DELETE")
	}
}
