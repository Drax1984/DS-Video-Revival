package jellyfin

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------- Externe Player (App „Jellyfin“ für Android) ----------
//
// Mit eingestelltem externem Player (VLC, MX Player, mpv …) holt die App die
// Wiedergabeinfo selbst und gibt dem Player die direkte Adresse
// „/Videos/{id}/stream?static=true&mediaSourceId=…&playSessionId=…“ – ohne
// Anmeldedaten (Jellyfin liefert Streams ohne Anmeldung aus). Hier gilt eine
// solche Adresse nur mit der PlaySessionId einer angemeldet abgefragten
// Wiedergabeinfo für genau diesen Titel und läuft unter dieser Anmeldung.

type streamGrant struct {
	Token string
	Item  string
	Used  time.Time
	To    map[string]string // MediaSourceId → Adresse der Umwandlung (externer Player)
}

type streamGrants struct {
	mu sync.Mutex
	m  map[string]*streamGrant
}

// grantTTL: so lange nach dem letzten Abruf gilt die Freigabe (Pause, Spulen).
const grantTTL = 12 * time.Hour

func (g *streamGrants) add(psid, token, item string) {
	if psid == "" || token == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = map[string]*streamGrant{}
	}
	g.m[psid] = &streamGrant{Token: token, Item: normID(item), Used: time.Now()}
}

func (g *streamGrants) token(psid, item string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.m[psid]
	if !ok || gr.Item != normID(item) || time.Since(gr.Used) > grantTTL {
		return "", false
	}
	gr.Used = time.Now()
	return gr.Token, true
}

// redirect: Abruf der Datei msid in dieser Sitzung auf die Umwandlung tu umleiten.
func (g *streamGrants) redirect(psid, msid, tu string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gr, ok := g.m[psid]; ok {
		if gr.To == nil {
			gr.To = map[string]string{}
		}
		gr.To[normID(msid)] = tu
	}
}

// target: Umleitung für psid/msid ("" = keine).
func (g *streamGrants) target(psid, msid string) (string, bool) {
	if psid == "" {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gr, ok := g.m[psid]
	if !ok || gr.To == nil {
		return "", false
	}
	tu, ok := gr.To[normID(msid)]
	return tu, ok
}

func (g *streamGrants) gc() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, gr := range g.m {
		if time.Since(gr.Used) > grantTTL {
			delete(g.m, k)
		}
	}
}

// streamToken: Anmeldung für einen Abruf ohne Anmeldedaten, wenn er ein
// freigegebener Stream ist ("" = keiner).
func (h *Handler) streamToken(x *req) string {
	if x.r.Method != http.MethodGet && x.r.Method != http.MethodHead {
		return ""
	}
	if len(x.segs) != 3 || x.segs[0] != "videos" || (x.segs[2] != "stream" && !strings.HasPrefix(x.segs[2], "stream.")) {
		return ""
	}
	psid := x.Q("PlaySessionId")
	if psid == "" {
		return ""
	}
	tok, _ := h.grants.token(psid, x.raw[1])
	return tok
}
