package admin

// Verbindungen: aktive Sitzungen anzeigen und trennen (Administratoren).

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"dsvideorevival/internal/auth"
)

func (h *Handler) connections(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"connections": h.S.Conns().List(), "bans": h.S.Conns().Bans(), "self": sess.Key,
			"idle_minutes": int(h.S.ConnIdle().Minutes()), "single": len(h.S.Cfg().SingleSession) > 0})
	case http.MethodPost:
		var req struct {
			ID    string `json:"id"`
			Block int    `json:"block_minutes"` // >0: Gerät so lange sperren
			Unban string `json:"unban"`         // Sperre aufheben
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req); err != nil {
			jsonErr(w, http.StatusBadRequest, "Anfrage ungültig")
			return
		}
		if req.Unban != "" {
			if b, ok := h.S.Conns().Unban(req.Unban); ok {
				log.Printf("verbindungen: %s hat die Sperre für %q (%s, %s) aufgehoben", sess.Account, b.Account, orNone(b.Device), orNone(b.IP))
			}
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		if len(req.ID) != 64 {
			jsonErr(w, http.StatusBadRequest, "Verbindung fehlt")
			return
		}
		if req.ID == sess.Key {
			jsonErr(w, http.StatusBadRequest, "Das ist deine eigene Sitzung – bitte oben rechts abmelden.")
			return
		}
		if req.Block < 0 || req.Block > maxBlockMinutes {
			jsonErr(w, http.StatusBadRequest, "Sperre: 0 bis 1440 Minuten")
			return
		}
		c, jobs, ok := h.S.Conns().Kick(req.ID, time.Duration(req.Block)*time.Minute)
		h.S.Auth.Sessions.Revoke(req.ID) // sid bleibt ungültig (auch wenn DSM sie noch kennt)
		h.mu.Lock()
		for tok, t := range h.raw {
			if t.key == req.ID {
				delete(h.raw, tok)
			}
		}
		h.mu.Unlock()
		for _, j := range jobs {
			if h.S.HLS != nil {
				h.S.HLS.Stop(j)
			}
		}
		switch {
		case ok && req.Block > 0:
			log.Printf("verbindungen: %s hat die Verbindung von %q getrennt und das Gerät für %d Min. gesperrt (%s, %s)", sess.Account, c.Account, req.Block, orNone(c.Device), orNone(c.IP))
		case ok:
			log.Printf("verbindungen: %s hat die Verbindung von %q getrennt (%s, %s)", sess.Account, c.Account, orNone(c.Device), orNone(c.IP))
		default:
			log.Printf("verbindungen: %s hat eine Sitzung getrennt (nicht mehr aktiv)", sess.Account)
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "GET/POST")
	}
}

// maxBlockMinutes: längste Gerätesperre beim Trennen (24 Stunden).
const maxBlockMinutes = 24 * 60
