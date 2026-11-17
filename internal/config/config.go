// Package config holds the runtime configuration (var/config.json).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Folder is a media folder mapped into a library.
type Folder struct {
	Path string `json:"path"`
	// Type: "movie", "tvshow", "home_video" oder "tv_record" (Bibliothekstypen der Original-VS,
	// belegt durch SYNO.VideoStation2.Library list: movie, tvshow, home_video, tv_record).
	Type      string `json:"type"`
	LibraryID int    `json:"library_id"`
	// Allow: wer den Ordner sehen darf ("user:name", "group:name"); leer = alle.
	Allow []string `json:"allow,omitempty"`
}

// Restriction: FSK-Altersgrenze für einen Benutzer oder eine Gruppe.
type Restriction struct {
	MaxFSK       int  `json:"max_fsk"`       // 0, 6, 12, 16, 18
	BlockUnrated bool `json:"block_unrated"` // Titel ohne FSK-Angabe sperren
}

// Library is an additional user-defined library (id > 0).
type Library struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	Type     string `json:"type"`
	IsPublic bool   `json:"is_public"`
}

// Config is the full server configuration.
type Config struct {
	ListenHTTP  string `json:"listen_http"`  // e.g. ":5180"
	ListenHTTPS string `json:"listen_https"` // e.g. ":5181" ("" = aus)
	TLSCert     string `json:"tls_cert"`     // leer + ListenHTTPS gesetzt → selbstsigniert
	TLSKey      string `json:"tls_key"`

	// AuthMode: "dsm" (Anmeldung wird an DSM durchgereicht – Standard auf der NAS)
	// oder "local" (eigene Benutzerdatei, für Tests/Entwicklung).
	AuthMode   string   `json:"auth_mode"`
	Upstream   string   `json:"upstream"` // DSM-WebAPI, z.B. http://127.0.0.1:5000
	UsersFile  string   `json:"users_file"`
	AdminUsers []string `json:"admin_users"`

	DataDir string `json:"data_dir"` // leer = Ordner der Konfigurationsdatei

	Folders []Folder `json:"folders"`
	// Groups: eigene Gruppen von DS Video Revival (Name → Benutzer), in
	// Freigaben/Regeln als "vgroup:name".
	Groups map[string][]string `json:"groups,omitempty"`
	// Restrictions: "user:name"/"group:name"/"vgroup:name" → FSK-Grenze (Jugendschutz).
	Restrictions map[string]Restriction `json:"restrictions,omitempty"`
	// DownloadAllow: wer in den DS-video-Apps herunterladen darf (Download und
	// Offline-Umwandlung), Einträge wie bei Folder.Allow; leer = alle.
	// Administratoren dürfen immer.
	DownloadAllow []string `json:"download_allow,omitempty"`
	// DupIgnore: Dateipfade, die die Doppelgänger-Suche der Ordneransicht
	// ignoriert („Ignorieren“ in der Ansicht „Nur Doppelgänger“).
	DupIgnore []string `json:"dup_ignore,omitempty"`
	// SingleSession: Benutzer/Gruppen, die nur eine Verbindung gleichzeitig
	// nutzen dürfen (App oder Weboberfläche); weitere sehen keine Medien.
	// Administratoren sind ausgenommen.
	SingleSession []string `json:"single_session,omitempty"`
	// SingleSessionIdle: Minuten ohne Aktivität/Wiedergabe, nach denen der
	// Platz frei wird (0 = 5).
	SingleSessionIdle int       `json:"single_session_idle,omitempty"`
	Libraries         []Library `json:"libraries"`

	ScanIntervalMinutes int `json:"scan_interval_minutes"`

	// Advertise: "all" = alle Original-Video-Station-APIs in SYNO.API.Info melden
	// (wie das Original), "implemented" = nur tatsächlich implementierte.
	Advertise string `json:"advertise"`

	// ReportVersion: an Clients gemeldete Video-Station-Version.
	// 3.1.0-3153 = letzte Version, die nachweislich unter DSM 7.2.2 läuft (007revad).
	ReportVersion string `json:"report_version"`

	// ExperimentalShapes aktiviert Antwortstrukturen, die NICHT belegt sind
	// (Hypothesen, z.B. Serien-Objekte). Standard aus – siehe docs/protocol.md.
	ExperimentalShapes bool `json:"experimental_shapes"`

	FFmpeg   string `json:"ffmpeg"` // leer = automatisch suchen (SynoCommunity ffmpeg7, PATH)
	Timezone string `json:"timezone"`

	// Metadaten (TMDb, optional IMDb-Bewertung über OMDb)
	TMDbKey      string `json:"tmdb_key"`
	OMDbKey      string `json:"omdb_key"`
	MetaLanguage string `json:"meta_language"` // z.B. de-DE

	// Umwandlung
	MaxTranscodes int    `json:"max_transcodes"`
	HWAccel       string `json:"hw_accel"` // auto | off

	// Port-Modus: DS Video Revival übernimmt 5000/5001, DSM läuft dahinter
	// (Standard 5010/5011). Vorherige DSM-Werte für die Rücknahme.
	FrontendMode    bool   `json:"frontend_mode"`
	FrontHTTP       string `json:"front_http"`
	FrontHTTPS      string `json:"front_https"`
	DSMPrevHTTP     int    `json:"dsm_prev_http,omitempty"`
	DSMPrevHTTPS    int    `json:"dsm_prev_https,omitempty"`
	DSMPrevRedirect bool   `json:"dsm_prev_redirect,omitempty"`

	// Poster-Zeitstempel an Apps melden (Hypothese, siehe docs/protocol.md)
	SendPosterMtime bool `json:"send_poster_mtime"`
}

// Defaults returns a configuration with safe defaults.
func Defaults() Config {
	return Config{
		ListenHTTP:          ":5180",
		AuthMode:            "dsm",
		Upstream:            "http://127.0.0.1:5000",
		ScanIntervalMinutes: 30,
		Advertise:           "all",
		ReportVersion:       "3.1.0-3153",
		Timezone:            "Europe/Berlin",
		MetaLanguage:        "de-DE",
		MaxTranscodes:       2,
		HWAccel:             "auto",
		SendPosterMtime:     true,
		FrontHTTP:           ":5000",
		FrontHTTPS:          ":5001",
	}
}

// Load reads a JSON config file on top of Defaults.
func Load(path string) (Config, error) {
	c := Defaults()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// Save writes the config atomically (0600).
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate checks basic consistency.
func (c Config) Validate() error {
	if c.ListenHTTP == "" && c.ListenHTTPS == "" {
		return errors.New("listen_http oder listen_https muss gesetzt sein")
	}
	switch c.AuthMode {
	case "dsm":
		if c.Upstream == "" {
			return errors.New("auth_mode=dsm benötigt upstream")
		}
	case "local":
		if c.UsersFile == "" {
			return errors.New("auth_mode=local benötigt users_file")
		}
	default:
		return errors.New("auth_mode muss dsm oder local sein")
	}
	libType := map[int]string{}
	for _, l := range c.Libraries {
		if l.ID <= 0 {
			return errors.New("Bibliotheks-ID muss größer als 0 sein")
		}
		if _, dup := libType[l.ID]; dup {
			return fmt.Errorf("Bibliotheks-ID %d doppelt", l.ID)
		}
		if strings.TrimSpace(l.Title) == "" {
			return errors.New("Bibliothek ohne Namen")
		}
		if !ValidType(l.Type) {
			return errors.New("Bibliothekstyp ungültig (movie|tvshow|home_video|tv_record): " + l.Type)
		}
		libType[l.ID] = l.Type
	}
	for _, f := range c.Folders {
		if !filepath.IsAbs(f.Path) {
			return errors.New("Ordnerpfad muss absolut sein: " + f.Path)
		}
		if !ValidType(f.Type) {
			return errors.New("Ordnertyp ungültig (movie|tvshow|home_video|tv_record): " + f.Type)
		}
		if f.LibraryID != 0 {
			t, ok := libType[f.LibraryID]
			if !ok {
				return fmt.Errorf("Ordner %s: Bibliothek %d gibt es nicht", f.Path, f.LibraryID)
			}
			if t != f.Type {
				return fmt.Errorf("Ordner %s: Typ %s passt nicht zur Bibliothek (%s)", f.Path, f.Type, t)
			}
		}
	}
	switch c.Advertise {
	case "all", "implemented":
	default:
		return errors.New("advertise muss all oder implemented sein")
	}
	return nil
}

// AdminGroup: eigene Gruppe, deren Mitglieder in DS Video Revival (nicht in
// DSM) Administratoren sind. Sie kann nicht gelöscht werden.
const AdminGroup = "administrators"

// ValidType: Bibliotheks-/Ordnertypen der Original-VS (SYNO.VideoStation2.Library list).
func ValidType(t string) bool {
	switch t {
	case "movie", "tvshow", "home_video", "tv_record":
		return true
	}
	return false
}

// ParseFolderList parses "type:/path;type:/path" (Installationsassistent).
// Ohne Typ-Präfix wird "movie" angenommen.
func ParseFolderList(s string) ([]Folder, error) {
	var out []Folder
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		typ, path := "movie", part
		if i := strings.Index(part, ":/"); i > 0 {
			typ, path = part[:i], part[i+1:]
		}
		f := Folder{Path: filepath.Clean(path), Type: typ}
		out = append(out, f)
	}
	c := Defaults()
	c.Folders = out
	return out, c.Validate()
}
