package admin

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEnglishComplete: jeder deutsche Text der Weboberfläche (t("…") in
// app.js, Texte und Attribute in index.html) hat eine englische Übersetzung
// in lang-en.js.
func TestEnglishComplete(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile("web/" + p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	unq := func(s string) string { return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s) }
	dict := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*"((?:[^"\\]|\\.)*)":\s*"`).FindAllStringSubmatch(read("lang-en.js"), -1) {
		dict[norm(unq(m[1]))] = true
	}
	var keys []string
	js := read("app.js")
	for _, m := range regexp.MustCompile(`\bt\(\s*"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(js, -1) {
		keys = append(keys, unq(m[1]))
	}
	for _, m := range regexp.MustCompile(`\btc\(\s*"([^"]*)",\s*"((?:[^"\\]|\\.)*)"`).FindAllStringSubmatch(js, -1) {
		keys = append(keys, m[1]+":"+unq(m[2]))
	}
	html := read("index.html")
	html = regexp.MustCompile(`(?s)<(script|style|code|pre)\b.*?</(script|style|code|pre)>`).ReplaceAllString(html, "<x>")
	html = regexp.MustCompile(`(?s)<[^>]*translate="no"[^>]*>[^<]*<`).ReplaceAllString(html, "<")
	html = regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(html, "")
	for _, m := range regexp.MustCompile(`\b(?:placeholder|title|aria-label|alt)="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		keys = append(keys, m[1])
	}
	for _, m := range regexp.MustCompile(`>([^<>]+)<`).FindAllStringSubmatch(html, -1) {
		keys = append(keys, m[1])
	}
	same := map[string]bool{"DS Video Revival": true, "DRAX × BLACK.ZERO – in Cooperation": true, "Wikidata": true, "Wikipedia": true,
		"TVmaze": true, "imdb.com": true, "DSM-WebAPI": true, "/var/packages/ffmpeg7/target/bin/ffmpeg": true, "FSK": true, "A–Z": true, "↑": true, "✕": true}
	letters := regexp.MustCompile(`[A-Za-zÄÖÜäöüß]`)
	for _, k := range keys {
		k = norm(k)
		if k == "" || !letters.MatchString(k) || same[k] || dict[k] {
			continue
		}
		t.Errorf("englische Übersetzung fehlt: %q", k)
	}
}
