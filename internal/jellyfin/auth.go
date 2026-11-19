package jellyfin

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/auth"
	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/vs"
)

// ---------- Anmeldung mit Konto und Passwort ----------

func (h *Handler) authenticateByName(x *req) {
	if x.r.Method != http.MethodPost {
		jsonError(x.w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Username string
		Pw       string
		Password string
	}
	readBody(x.r, &body)
	pw := body.Pw
	if pw == "" {
		pw = body.Password
	}
	ip := upstream.ClientIP(x.r)
	id, err := h.S.Auth.CheckPassword(x.r.Context(), body.Username, pw, ip)
	if err != nil {
		log.Printf("jellyfin: Anmeldung von %q (%s, %s) abgelehnt: %v", body.Username, x.auth.Client, ip, err)
		code := http.StatusUnauthorized
		switch {
		case errors.Is(err, auth.ErrDSMUnreachable):
			code = http.StatusServiceUnavailable
		case errors.Is(err, auth.ErrNoPermission), errors.Is(err, auth.ErrAccountLocked):
			code = http.StatusForbidden
		case errors.Is(err, auth.ErrTooManyTries):
			code = http.StatusTooManyRequests
		}
		jsonError(x.w, code, err.Error())
		return
	}
	h.issue(x, id.Account, id.IsManager, "Passwort")
}

// issue legt das Gerät an und antwortet mit AuthenticationResult.
func (h *Handler) issue(x *req, account string, mgr *bool, how string) {
	d := device{Account: account, IsManager: mgr, Client: x.auth.Client, ClientVersion: x.auth.Version,
		DeviceName: x.auth.Device, DeviceID: x.auth.DeviceID, IP: upstream.ClientIP(x.r)}
	tok, dev := h.store.login(d)
	log.Printf("jellyfin: %q angemeldet (%s, %s, %s)", account, dev.label(), how, dev.IP)
	sess := auth.Session{Account: account, IsManager: mgr, Key: dev.key()}
	h.S.Conns().Touch(sess.Key, account, h.S.SingleSession(sess), vs.ConnInfo{Kind: "jellyfin", Device: dev.label(), Sure: true, IP: dev.IP})
	x.dev, x.sess = dev, sess
	writeJSON(x.w, map[string]any{
		"User":        h.userDto(x),
		"SessionInfo": h.sessionInfo(x),
		"AccessToken": tok,
		"ServerId":    h.store.serverID(),
	})
}

// ---------- Quick Connect ----------

// quickConnect: Code wird in der App angezeigt und in der Weboberfläche von
// DS Video Revival bestätigt (dort ist man mit DSM angemeldet, auch mit 2FA).
type quickConnect struct {
	mu sync.Mutex
	m  map[string]*qcRequest // secret → Anfrage
}

type qcRequest struct {
	Secret, Code                          string
	DeviceID, DeviceName, AppName, AppVer string
	Created                               time.Time
	Account                               string
	IsManager                             *bool
	Used                                  bool
}

const qcTTL = 10 * time.Minute

func newQuickConnect() *quickConnect { return &quickConnect{m: map[string]*qcRequest{}} }

func (q *quickConnect) gcLocked() {
	for k, r := range q.m {
		if time.Since(r.Created) > qcTTL {
			delete(q.m, k)
		}
	}
}

func (r *qcRequest) dto() map[string]any {
	return map[string]any{"Authenticated": r.Account != "", "Secret": r.Secret, "Code": r.Code,
		"DeviceId": r.DeviceID, "DeviceName": r.DeviceName, "AppName": r.AppName, "AppVersion": r.AppVer,
		"DateAdded": jfTime(r.Created)}
}

func (h *Handler) qcInitiate(x *req) {
	h.qc.mu.Lock()
	defer h.qc.mu.Unlock()
	h.qc.gcLocked()
	if len(h.qc.m) > 200 {
		jsonError(x.w, http.StatusTooManyRequests, "zu viele offene Anfragen")
		return
	}
	code := ""
	for {
		n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000))
		code = fmt.Sprintf("%06d", n.Int64())
		dup := false
		for _, r := range h.qc.m {
			if r.Code == code {
				dup = true
			}
		}
		if !dup {
			break
		}
	}
	r := &qcRequest{Secret: randHex(32), Code: code, DeviceID: x.auth.DeviceID, DeviceName: x.auth.Device,
		AppName: x.auth.Client, AppVer: x.auth.Version, Created: time.Now()}
	h.qc.m[r.Secret] = r
	log.Printf("jellyfin: Quick Connect angefragt (%s · %s, Code %s)", r.AppName, r.DeviceName, r.Code)
	writeJSON(x.w, r.dto())
}

func (h *Handler) qcConnect(x *req) {
	h.qc.mu.Lock()
	defer h.qc.mu.Unlock()
	h.qc.gcLocked()
	r, ok := h.qc.m[x.Q("secret")]
	if !ok {
		jsonError(x.w, http.StatusNotFound, "Unbekannt oder abgelaufen")
		return
	}
	writeJSON(x.w, r.dto())
}

func (h *Handler) authenticateWithQuickConnect(x *req) {
	var body struct{ Secret string }
	readBody(x.r, &body)
	h.qc.mu.Lock()
	r, ok := h.qc.m[body.Secret]
	if ok && (r.Account == "" || r.Used || time.Since(r.Created) > qcTTL) {
		ok = false
	}
	if ok {
		r.Used = true
		delete(h.qc.m, body.Secret)
	}
	h.qc.mu.Unlock()
	if !ok {
		jsonError(x.w, http.StatusUnauthorized, "Quick Connect nicht bestätigt")
		return
	}
	if x.auth.DeviceID == "" {
		x.auth.DeviceID, x.auth.Device, x.auth.Client, x.auth.Version = r.DeviceID, r.DeviceName, r.AppName, r.AppVer
	}
	h.issue(x, r.Account, r.IsManager, "Quick Connect")
}

// ErrQuickConnectCode: Code unbekannt oder abgelaufen.
var ErrQuickConnectCode = errors.New("Code unbekannt oder abgelaufen – in der App einen neuen Code anzeigen lassen")

// QuickConnect bestätigt einen Code für das angemeldete Konto (Weboberfläche).
func (h *Handler) QuickConnect(code string, sess auth.Session) error {
	code = strings.TrimSpace(code)
	if !h.Enabled() {
		return errors.New("Jellyfin-Schnittstelle ist ausgeschaltet")
	}
	h.qc.mu.Lock()
	defer h.qc.mu.Unlock()
	h.qc.gcLocked()
	for _, r := range h.qc.m {
		if r.Code == code && r.Account == "" {
			r.Account, r.IsManager = sess.Account, sess.IsManager
			log.Printf("jellyfin: Quick Connect für %q bestätigt (%s · %s)", sess.Account, r.AppName, r.DeviceName)
			return nil
		}
	}
	return ErrQuickConnectCode
}

// ---------- Geräte (Weboberfläche) ----------

// DeviceView: Gerät in der Weboberfläche.
type DeviceView struct {
	ID       string    `json:"id"`
	Account  string    `json:"account"`
	Label    string    `json:"label"`
	Version  string    `json:"version,omitempty"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
	IP       string    `json:"ip,omitempty"`
}

// Devices listet die angemeldeten Geräte (account "" = alle).
func (h *Handler) Devices(account string) any {
	out := []DeviceView{}
	for _, d := range h.store.devices(account) {
		out = append(out, DeviceView{ID: d.Hash, Account: d.Account, Label: d.label(), Version: d.ClientVersion,
			Created: d.Created, LastSeen: d.LastSeen, IP: d.IP})
	}
	return out
}

// RemoveDevice meldet ein Gerät ab.
func (h *Handler) RemoveDevice(id, account string) bool {
	d, ok := h.store.remove(id, account)
	if ok {
		h.dropConn(d)
	}
	return ok
}

// Alive: Verbindungsschlüssel gehört zu einem angemeldeten Gerät.
func (h *Handler) Alive(key string) bool {
	return h.store.alive(strings.TrimPrefix(key, vs.JellyfinKeyPrefix))
}

// Revoke: „Trennen“ in der Weboberfläche – Gerät abmelden.
func (h *Handler) Revoke(key string) bool {
	d, ok := h.store.remove(strings.TrimPrefix(key, vs.JellyfinKeyPrefix), "")
	if ok {
		h.stopSessionsOf(d.key())
		log.Printf("jellyfin: Gerät %s von %q abgemeldet", d.label(), d.Account)
	}
	return ok
}

// dropConn beendet laufende Wiedergaben des Geräts.
func (h *Handler) dropConn(d *device) {
	_, jobs, _ := h.S.Conns().Kick(d.key(), 0)
	for _, j := range jobs {
		if h.S.HLS != nil {
			h.S.HLS.Stop(j)
		}
	}
	h.stopSessionsOf(d.key())
}

// Status für die Weboberfläche.
func (h *Handler) Status(r *http.Request) map[string]any {
	cfg := h.S.Cfg()
	return map[string]any{"enabled": cfg.JellyfinEnabled, "port": cfg.JellyfinListenPort(), "listening": h.Listening(),
		"server_name": h.serverName(), "version": ServerVersion}
}

func (h *Handler) logout(x *req) {
	if _, ok := h.store.remove(x.dev.Hash, ""); ok {
		h.dropConn(x.dev)
		log.Printf("jellyfin: %q hat sich abgemeldet (%s)", x.dev.Account, x.dev.label())
	}
	noContent(x.w)
}

// ---------- Benutzer und System ----------

func (h *Handler) userDto(x *req) map[string]any {
	now := jfTime(time.Now())
	may := h.S.MayDownload(x.sess)
	return map[string]any{
		"Name": x.sess.Account, "ServerId": h.store.serverID(), "ServerName": h.serverName(), "Id": userID(x.sess.Account),
		"HasPassword": true, "HasConfiguredPassword": true, "HasConfiguredEasyPassword": false, "EnableAutoLogin": false,
		"LastLoginDate": jfTime(x.dev.Created), "LastActivityDate": now,
		"Configuration": map[string]any{
			"AudioLanguagePreference": "", "PlayDefaultAudioTrack": true, "SubtitleLanguagePreference": "",
			"DisplayMissingEpisodes": false, "GroupedFolders": []string{}, "SubtitleMode": "Default",
			"DisplayCollectionsView": false, "EnableLocalPassword": false, "OrderedViews": []string{},
			"LatestItemsExcludes": []string{}, "MyMediaExcludes": []string{}, "HidePlayedInLatest": true,
			"RememberAudioSelections": true, "RememberSubtitleSelections": true, "EnableNextEpisodeAutoPlay": true,
		},
		"Policy": map[string]any{
			"IsAdministrator": false, "IsHidden": true, "EnableCollectionManagement": false, "EnableSubtitleManagement": false,
			"EnableLyricManagement": false, "IsDisabled": false, "BlockedTags": []string{}, "AllowedTags": []string{},
			"EnableUserPreferenceAccess": true, "AccessSchedules": []any{}, "BlockUnratedItems": []string{},
			"EnableRemoteControlOfOtherUsers": false, "EnableSharedDeviceControl": false, "EnableRemoteAccess": true,
			"EnableLiveTvManagement": false, "EnableLiveTvAccess": false, "EnableMediaPlayback": true,
			"EnableAudioPlaybackTranscoding": true, "EnableVideoPlaybackTranscoding": true, "EnablePlaybackRemuxing": true,
			"ForceRemoteSourceTranscoding": false, "EnableContentDeletion": false, "EnableContentDeletionFromFolders": []string{},
			"EnableContentDownloading": may, "EnableSyncTranscoding": false, "EnableMediaConversion": false,
			"EnabledDevices": []string{}, "EnableAllDevices": true, "EnabledChannels": []string{}, "EnableAllChannels": false,
			"EnabledFolders": []string{}, "EnableAllFolders": true, "InvalidLoginAttemptCount": 0, "LoginAttemptsBeforeLockout": -1,
			"MaxActiveSessions": 0, "EnablePublicSharing": false, "BlockedMediaFolders": []string{}, "BlockedChannels": []string{},
			"RemoteClientBitrateLimit": 0,
			"AuthenticationProviderId": "Jellyfin.Server.Implementations.Users.DefaultAuthenticationProvider",
			"PasswordResetProviderId":  "Jellyfin.Server.Implementations.Users.DefaultPasswordResetProvider",
			"SyncPlayAccess":           "None",
		},
	}
}

func (h *Handler) sessionInfo(x *req) map[string]any {
	now := jfTime(time.Now())
	return map[string]any{
		"PlayState":                map[string]any{"CanSeek": false, "IsPaused": false, "IsMuted": false, "RepeatMode": "RepeatNone", "PlaybackOrder": "Default"},
		"AdditionalUsers":          []any{},
		"Capabilities":             map[string]any{"PlayableMediaTypes": []string{"Video"}, "SupportedCommands": []string{}, "SupportsMediaControl": false, "SupportsPersistentIdentifier": true},
		"RemoteEndPoint":           x.dev.IP,
		"PlayableMediaTypes":       []string{"Video"},
		"Id":                       x.dev.Hash[:32],
		"UserId":                   userID(x.sess.Account),
		"UserName":                 x.sess.Account,
		"Client":                   x.dev.Client,
		"LastActivityDate":         now,
		"LastPlaybackCheckIn":      now,
		"DeviceName":               x.dev.DeviceName,
		"DeviceId":                 x.dev.DeviceID,
		"ApplicationVersion":       x.dev.ClientVersion,
		"IsActive":                 true,
		"SupportsMediaControl":     false,
		"SupportsRemoteControl":    false,
		"NowPlayingQueue":          []any{},
		"NowPlayingQueueFullItems": []any{},
		"HasCustomDeviceName":      false,
		"ServerId":                 h.store.serverID(),
		"SupportedCommands":        []string{},
	}
}

func (h *Handler) systemInfo(x *req) map[string]any {
	m := h.publicInfo(x.r)
	for k, v := range map[string]any{
		"OperatingSystemDisplayName": "Synology DSM", "PackageName": "DS Video Revival", "HasPendingRestart": false,
		"IsShuttingDown": false, "SupportsLibraryMonitor": false, "WebSocketPortNumber": h.S.Cfg().JellyfinListenPort(),
		"CompletedInstallations": []any{}, "CanSelfRestart": false, "CanLaunchWebBrowser": false,
		"ProgramDataPath": "", "WebPath": "", "ItemsByNamePath": "", "CachePath": "", "LogPath": "",
		"InternalMetadataPath": "", "TranscodingTempPath": "", "CastReceiverApplications": []any{},
		"HasUpdateAvailable": false, "EncoderLocation": "System",
	} {
		m[k] = v
	}
	return m
}
