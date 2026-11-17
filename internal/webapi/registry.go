package webapi

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// registry_videostation.json: API-Namen, Pfade, Versionen und Methoden der
// Original-Video-Station, extrahiert aus deren .lib-Registrierungsdateien
// (Quelle: mikespub/synology docs/VideoStation*.json, MIT). Keine Synology-
// Binärdateien, nur Schnittstellen-Fakten.
//
//go:embed registry_videostation.json
var registryJSON []byte

// APIDef describes one API entry as reported by SYNO.API.Info.
type APIDef struct {
	Path          string              `json:"path"`
	MinVersion    int                 `json:"minVersion"`
	MaxVersion    int                 `json:"maxVersion"`
	RequestFormat string              `json:"requestFormat,omitempty"`
	Methods       map[string][]string `json:"methods,omitempty"`
}

// InfoEntry is the exact per-API shape of a SYNO.API.Info query response
// (abgeglichen mit einer realen DSM-7-Antwort: maxVersion, minVersion, path,
// requestFormat nur bei JSON-APIs).
type InfoEntry struct {
	MaxVersion    int    `json:"maxVersion"`
	MinVersion    int    `json:"minVersion"`
	Path          string `json:"path"`
	RequestFormat string `json:"requestFormat,omitempty"`
}

var (
	regOnce sync.Once
	regDefs map[string]APIDef
)

// VideoStationAPIs returns the original Video Station API registry.
func VideoStationAPIs() map[string]APIDef {
	regOnce.Do(func() {
		var f struct {
			APIs map[string]APIDef `json:"apis"`
		}
		if err := json.Unmarshal(registryJSON, &f); err != nil {
			panic("registry_videostation.json: " + err.Error())
		}
		regDefs = f.APIs
	})
	return regDefs
}

// HasMethod reports whether the original registry lists method for api/version.
func (d APIDef) HasMethod(version int, method string) bool {
	ms, ok := d.Methods[itoa(version)]
	if !ok {
		return false
	}
	for _, m := range ms {
		if m == method {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// InfoEntries converts definitions to SYNO.API.Info response entries,
// filtered by the query parameter ("all" or comma separated names, offiziell
// belegt). Zusätzlich wird ein Name mit abschließendem Punkt als Präfix
// behandelt ("SYNO.VideoStation.") – DSM-Verhalten, NICHT offiziell belegt;
// harmlos, falls kein Client es nutzt.
func InfoEntries(defs map[string]APIDef, query string) map[string]InfoEntry {
	out := map[string]InfoEntry{}
	want := strings.Split(query, ",")
	all := query == "" || query == "all"
	for name, d := range defs {
		if !all && !matchQuery(name, want) {
			continue
		}
		out[name] = InfoEntry{MaxVersion: d.MaxVersion, MinVersion: d.MinVersion, Path: d.Path, RequestFormat: d.RequestFormat}
	}
	return out
}

func matchQuery(name string, want []string) bool {
	for _, w := range want {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if w == name || (strings.HasSuffix(w, ".") && strings.HasPrefix(name, w)) {
			return true
		}
	}
	return false
}

// SortedNames returns map keys sorted (for stable output/tests).
func SortedNames[T any](m map[string]T) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
