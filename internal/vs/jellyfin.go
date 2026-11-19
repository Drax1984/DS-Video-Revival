package vs

import (
	"context"
	"net/http"
	"strings"

	"dsvideorevival/internal/auth"
)

// JellyfinAPI: die Jellyfin-kompatible Schnittstelle (Paket jellyfin). Als
// Schnittstelle hier, damit vs und admin sie ohne Importzyklus nutzen.
type JellyfinAPI interface {
	http.Handler
	// Apply übernimmt geänderte Einstellungen (Port, Suche im Netz).
	Apply()
	// Alive: Verbindungsschlüssel (JellyfinKeyPrefix…) gehört zu einem angemeldeten Gerät.
	Alive(key string) bool
	// Revoke meldet das Gerät zum Verbindungsschlüssel ab („Trennen“).
	Revoke(key string) bool
	// QuickConnect bestätigt einen in einer Jellyfin-App angezeigten Code für das Konto.
	QuickConnect(code string, sess auth.Session) error
	// Devices listet die angemeldeten Geräte (nil = alle Konten).
	Devices(account string) any
	// RemoveDevice meldet ein Gerät ab (id aus Devices); account "" = beliebiges Konto.
	RemoveDevice(id, account string) bool
	// Status für die Weboberfläche (aktiv, Port, Adressen).
	Status(r *http.Request) map[string]any
}

// JellyfinKeyPrefix kennzeichnet Verbindungen über die Jellyfin-Schnittstelle.
const JellyfinKeyPrefix = "jf:"

// JellyfinPrefix: Pfad der Jellyfin-Schnittstelle auf dem Port der
// Weboberfläche und über DSMs Webserver (https://nas:5001/jellyfin).
const JellyfinPrefix = "/jellyfin"

const ctxJellyfinBase ctxKey = 100

// JellyfinBase: Basis-Pfad, unter dem die Anfrage kam ("" oder "/jellyfin").
func JellyfinBase(r *http.Request) string {
	v, _ := r.Context().Value(ctxJellyfinBase).(string)
	return v
}

// serveJellyfin reicht /jellyfin/… an die Schnittstelle weiter (ohne Präfix).
func (s *Server) serveJellyfin(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if s.Jellyfin == nil || !(p == JellyfinPrefix || strings.HasPrefix(p, JellyfinPrefix+"/")) {
		return false
	}
	r2 := r.WithContext(context.WithValue(r.Context(), ctxJellyfinBase, JellyfinPrefix))
	u := *r.URL
	u.Path, u.RawPath = strings.TrimPrefix(p, JellyfinPrefix), ""
	if u.Path == "" {
		u.Path = "/"
	}
	r2.URL = &u
	s.Jellyfin.ServeHTTP(w, r2)
	return true
}

// keyAlive: besteht die Sitzung bzw. das Jellyfin-Gerät zum Verbindungsschlüssel noch?
func (s *Server) keyAlive(key string) bool {
	if strings.HasPrefix(key, JellyfinKeyPrefix) {
		return s.Jellyfin != nil && s.Jellyfin.Alive(key)
	}
	if s.Auth == nil || s.Auth.Sessions == nil {
		return true
	}
	return s.Auth.Sessions.Alive(key)
}

// RevokeKey trennt eine Verbindung dauerhaft (DSM-Sitzung bzw. Jellyfin-Gerät).
func (s *Server) RevokeKey(key string) {
	if strings.HasPrefix(key, JellyfinKeyPrefix) {
		if s.Jellyfin != nil {
			s.Jellyfin.Revoke(key)
		}
		return
	}
	if s.Auth != nil && s.Auth.Sessions != nil {
		s.Auth.Sessions.Revoke(key)
	}
}
