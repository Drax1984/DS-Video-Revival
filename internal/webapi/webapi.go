// Package webapi implements the Synology-WebAPI request/response conventions
// that original Synology clients expect.
//
// Belege (siehe docs/protocol.md):
//   - Request-Aufbau api/version/method/_sid, Envelope {"success":..,"data"|"error"}
//     und Fehlercodes 100–119: offizieller "Synology File Station API Guide".
//   - Content-Type `application/json; charset="UTF-8"`: holg/ds-video-hevc
//     (Antwort, die DS Video tvOS nachweislich akzeptiert).
//   - requestFormat "JSON" (Parameterwerte sind JSON-kodiert, z.B. file={"id":1}):
//     .lib-Registrierungen der Original-Video-Station + holg/ds-video-hevc selftest.
package webapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Common error codes (File Station API Guide, "Common Error Codes").
const (
	ErrUnknown           = 100
	ErrNoParameter       = 101
	ErrNoSuchAPI         = 102
	ErrNoSuchMethod      = 103
	ErrVersionNotSupport = 104
	ErrPermission        = 105
	ErrSessionTimeout    = 106
	ErrDuplicateLogin    = 107
	ErrSIDNotFound       = 119

	// SYNO.API.Auth login errors (File Station API Guide).
	ErrAuthBadCredentials = 400
	ErrAuthDisabled       = 401
	ErrAuthPermission     = 402
	ErrAuthOTPRequired    = 403
	ErrAuthOTPFailed      = 404

	// Video Station: open with hls_remux refused for this source
	// (holg/ds-video-hevc: "error 1211" → DS Video fällt auf Transcoding zurück).
	ErrVSNoStreamFormat = 1211
)

// Request is a parsed WebAPI call.
type Request struct {
	API     string
	Version int
	Method  string
	Params  url.Values // merged query + form body, raw (still JSON-encoded where applicable)
	HTTP    *http.Request
}

// maxFormBytes limits urlencoded bodies (uploads are not part of the PoC).
const maxFormBytes = 4 << 20

// Parse merges query string and urlencoded/multipart body parameters.
func Parse(r *http.Request) (*Request, error) {
	vals := url.Values{}
	for k, v := range r.URL.Query() {
		vals[k] = append(vals[k], v...)
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		switch ct {
		case "application/x-www-form-urlencoded", "":
			body, err := io.ReadAll(io.LimitReader(r.Body, maxFormBytes))
			if err != nil {
				return nil, err
			}
			form, err := url.ParseQuery(string(body))
			if err == nil {
				for k, v := range form {
					vals[k] = append(vals[k], v...)
				}
			}
		case "multipart/form-data":
			if err := r.ParseMultipartForm(maxFormBytes); err == nil && r.MultipartForm != nil {
				for k, v := range r.MultipartForm.Value {
					vals[k] = append(vals[k], v...)
				}
			}
		}
	}
	req := &Request{Params: vals, HTTP: r}
	req.API = Unquote(vals.Get("api"))
	req.Method = Unquote(vals.Get("method"))
	if v := Unquote(vals.Get("version")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return req, errors.New("bad version")
		}
		req.Version = n
	}
	return req, nil
}

// Unquote removes JSON string quoting if present ("abc" → abc).
func Unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if json.Unmarshal([]byte(s), &out) == nil {
			return out
		}
		return s[1 : len(s)-1]
	}
	return s
}

// Has reports whether the parameter is present.
func (r *Request) Has(key string) bool { _, ok := r.Params[key]; return ok }

// Str returns a parameter, JSON-unquoted.
func (r *Request) Str(key string) string { return Unquote(r.Params.Get(key)) }

// Int returns an integer parameter or def.
func (r *Request) Int(key string, def int) int {
	s := strings.TrimSpace(r.Str(key))
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// Bool returns a boolean parameter or def.
func (r *Request) Bool(key string, def bool) bool {
	switch strings.ToLower(r.Str(key)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	return def
}

// JSON decodes a JSON-encoded parameter into v.
func (r *Request) JSON(key string, v any) error {
	raw := r.Params.Get(key)
	if raw == "" {
		return errors.New("missing " + key)
	}
	return json.Unmarshal([]byte(raw), v)
}

// StrList accepts either a JSON array (["a","b"]) or a comma separated list.
func (r *Request) StrList(key string) []string {
	raw := strings.TrimSpace(r.Params.Get(key))
	if raw == "" {
		return nil
	}
	var arr []string
	if strings.HasPrefix(raw, "[") && json.Unmarshal([]byte(raw), &arr) == nil {
		return arr
	}
	var out []string
	for _, p := range strings.Split(Unquote(raw), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IntList accepts a JSON array of numbers, a single number or a comma list.
func (r *Request) IntList(key string) []int {
	raw := strings.TrimSpace(r.Params.Get(key))
	if raw == "" {
		return nil
	}
	var arr []int
	if strings.HasPrefix(raw, "[") && json.Unmarshal([]byte(raw), &arr) == nil {
		return arr
	}
	var out []int
	for _, p := range strings.Split(Unquote(raw), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// SID extracts the session id: `_sid` parameter or the `id` cookie
// (File Station API Guide: "_sid ... or in the id cookie").
func (r *Request) SID() string {
	if s := r.Str("_sid"); s != "" {
		return s
	}
	if c, err := r.HTTP.Cookie("id"); err == nil {
		return c.Value
	}
	return ""
}

const contentTypeJSON = `application/json; charset="UTF-8"`

// OK writes a success envelope. data may be nil.
func OK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.Header().Set("Cache-Control", "no-cache")
	body := map[string]any{"success": true}
	if data != nil {
		body["data"] = data
	}
	_ = json.NewEncoder(w).Encode(body)
}

// Fail writes an error envelope.
func Fail(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"error":   map[string]any{"code": code},
	})
}
