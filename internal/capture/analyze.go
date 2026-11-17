package capture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type agg struct {
	API, Method string
	Version     int
	Paths       map[string]bool
	Count       int
	Params      map[string]string // key → Beispielwert
	Shape       map[string]string // JSON-Pfad → Typ
	Errors      map[string]int
	Statuses    map[string]int
	Agents      map[string]bool
}

// Analyze reads a capture file and writes a Markdown report.
func Analyze(path string, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	aggs := map[string]*agg{}
	var seq []string
	n := 0
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		n++
		key := fmt.Sprintf("%s|%d|%s", e.API, e.Version, e.Method)
		if e.API == "" {
			key = "(ohne api)|0|" + e.Path
		}
		a := aggs[key]
		if a == nil {
			a = &agg{API: e.API, Method: e.Method, Version: e.Version, Paths: map[string]bool{}, Params: map[string]string{}, Shape: map[string]string{},
				Errors: map[string]int{}, Statuses: map[string]int{}, Agents: map[string]bool{}}
			if e.API == "" {
				a.API, a.Method = "(ohne api)", e.Path
			}
			aggs[key] = a
		}
		a.Count++
		a.Paths[e.HTTPMethod+" "+pathPattern(e.Path)] = true
		a.Agents[e.UserAgent] = true
		a.Statuses[fmt.Sprintf("%d %s", e.Status, e.RespType)]++
		for k, v := range e.Params {
			if k == "api" || k == "method" || k == "version" {
				continue
			}
			if _, ok := a.Params[k]; !ok {
				a.Params[k] = v
			}
		}
		if m, ok := e.RespJSON.(map[string]any); ok {
			if s, _ := m["success"].(bool); !s {
				if er, ok := m["error"].(map[string]any); ok {
					a.Errors[fmt.Sprint(er["code"])]++
				}
			}
			if d, ok := m["data"]; ok {
				if e.API == "SYNO.API.Info" {
					// Nur die gemeldeten API-Namen, nicht jedes Feld.
					if dm, ok := d.(map[string]any); ok {
						for name, v := range dm {
							if vm, ok := v.(map[string]any); ok {
								a.Shape["api "+name] = fmt.Sprintf("v%v–%v %v %v", vm["minVersion"], vm["maxVersion"], vm["path"], vm["requestFormat"])
							}
						}
					}
				} else {
					shape(d, "data", a.Shape)
				}
			}
		}
		if len(seq) < 400 {
			seq = append(seq, fmt.Sprintf("%s v%d %s", a.API, e.Version, a.Method))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	keys := make([]string, 0, len(aggs))
	for k := range aggs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(w, "# Mitschnitt-Auswertung\n\nQuelle: `%s` – %d Aufrufe, %d verschiedene API/Version/Methode-Kombinationen.\n\n", path, n, len(keys))
	fmt.Fprintln(w, "## Übersicht\n\n| API | v | Methode | Anzahl | Fehlercodes |\n|---|---|---|---|---|")
	for _, k := range keys {
		a := aggs[k]
		fmt.Fprintf(w, "| `%s` | %d | %s | %d | %s |\n", a.API, a.Version, a.Method, a.Count, joinCounts(a.Errors))
	}
	fmt.Fprintln(w, "\n## Details")
	for _, k := range keys {
		a := aggs[k]
		fmt.Fprintf(w, "\n### %s v%d %s\n\n", a.API, a.Version, a.Method)
		fmt.Fprintf(w, "- HTTP: %s\n", strings.Join(sortedKeys(a.Paths), ", "))
		fmt.Fprintf(w, "- Antworten: %s\n", joinCounts(a.Statuses))
		fmt.Fprintf(w, "- Clients: %s\n", strings.Join(sortedKeys(a.Agents), " · "))
		if len(a.Params) > 0 {
			fmt.Fprintln(w, "- Parameter (Beispielwert):")
			for _, p := range sortedKeys2(a.Params) {
				fmt.Fprintf(w, "  - `%s` = `%s`\n", p, strings.ReplaceAll(truncate(a.Params[p], 300), "`", "'"))
			}
		}
		if len(a.Shape) > 0 {
			fmt.Fprintln(w, "- Antwortstruktur:")
			for _, p := range sortedKeys2(a.Shape) {
				fmt.Fprintf(w, "  - `%s`: %s\n", p, a.Shape[p])
			}
		}
	}
	fmt.Fprintln(w, "\n## Reihenfolge (erste Aufrufe)\n\n```")
	for i, s := range seq {
		fmt.Fprintf(w, "%3d  %s\n", i+1, s)
	}
	fmt.Fprintln(w, "```")
	return nil
}

// pathPattern: lange Suffixe (Dateinamen) unkenntlich machen.
func pathPattern(p string) string {
	if i := strings.Index(p, ".cgi/"); i >= 0 {
		return p[:i+5] + "<datei>"
	}
	return p
}

func shape(v any, p string, out map[string]string) {
	if len(out) > 400 {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			out[p] = "object (leer)"
		}
		for k, x := range t {
			shape(x, p+"."+k, out)
		}
	case []any:
		if len(t) == 0 {
			if _, ok := out[p+"[]"]; !ok {
				out[p+"[]"] = "array (leer)"
			}
			return
		}
		delete(out, p+"[]")
		for i, x := range t {
			if i >= 3 {
				break
			}
			shape(x, p+"[]", out)
		}
	case string:
		out[p] = fmt.Sprintf("string (z.B. %q)", truncate(t, 60))
	case float64:
		if t == float64(int64(t)) {
			out[p] = fmt.Sprintf("number (z.B. %d)", int64(t))
		} else {
			out[p] = fmt.Sprintf("number (z.B. %v)", t)
		}
	case bool:
		out[p] = fmt.Sprintf("bool (z.B. %v)", t)
	case nil:
		out[p] = "null"
	}
}

func joinCounts(m map[string]int) string {
	if len(m) == 0 {
		return "–"
	}
	var parts []string
	for _, k := range sortedKeys3(m) {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys2(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys3(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
