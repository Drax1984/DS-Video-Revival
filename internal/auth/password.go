package auth

import (
	"context"
	"errors"
	"log"
	"net/url"
	"strings"
	"time"
)

// Fehler von CheckPassword (Anmeldung ohne bleibende DSM-Sitzung, z.B. für
// die Jellyfin-Schnittstelle).
var (
	ErrBadCredentials = errors.New("Konto oder Passwort falsch")
	ErrNeedOTP        = errors.New("für dieses Konto ist die Zwei-Faktor-Anmeldung aktiv – bitte Quick Connect verwenden")
	ErrNoPermission   = errors.New("keine Berechtigung für DS Video Revival (DSM → Anwendungsberechtigungen)")
	ErrAccountLocked  = errors.New("Konto gesperrt oder deaktiviert")
	ErrTooManyTries   = errors.New("zu viele Fehlversuche – später erneut versuchen")
	ErrDSMUnreachable = errors.New("DSM nicht erreichbar")
)

// Identity: geprüftes Konto.
type Identity struct {
	Account   string
	IsManager *bool
}

// CheckPassword prüft Konto und Passwort – im DSM-Modus über eine kurze
// DSM-Anmeldung (Passwort, Kontosperre, Anwendungsberechtigung), die gleich
// wieder abgemeldet wird; das Passwort wird nicht gespeichert. Konten mit
// Zwei-Faktor-Anmeldung liefern ErrNeedOTP.
func (a *Adapter) CheckPassword(ctx context.Context, account, passwd, ip string) (Identity, error) {
	account = strings.TrimSpace(account)
	if account == "" || passwd == "" {
		return Identity{}, ErrBadCredentials
	}
	if a.Limiter.Blocked(ip) {
		time.Sleep(time.Second)
		return Identity{}, ErrTooManyTries
	}
	if a.Mode == "local" {
		u, err := a.Local.Check(account, passwd)
		if err != nil {
			a.Limiter.Fail(ip)
			if err == ErrDisabled {
				return Identity{}, ErrAccountLocked
			}
			return Identity{}, ErrBadCredentials
		}
		a.Limiter.Success(ip)
		mgr := u.IsManager
		return Identity{Account: u.Account, IsManager: &mgr}, nil
	}
	if a.Upstream == nil {
		return Identity{}, ErrDSMUnreachable
	}
	var sid, used string
	for _, cand := range a.sessionOrder(account) {
		v := url.Values{"api": {"SYNO.API.Auth"}, "version": {"6"}, "method": {"login"},
			"account": {account}, "passwd": {passwd}, "format": {"sid"}}
		if cand != noSession {
			v.Set("session", cand)
		}
		m, err := a.Upstream.CallFrom(ctx, "entry.cgi", v, ip)
		if err != nil {
			return Identity{}, ErrDSMUnreachable
		}
		if ok, _ := m["success"].(bool); ok {
			d, _ := m["data"].(map[string]any)
			sid, _ = d["sid"].(string)
			used = cand
			break
		}
		switch c := errCode(m); c {
		case 402:
			continue // nächster Sitzungsname
		case 400, 404:
			a.Limiter.Fail(ip)
			return Identity{}, ErrBadCredentials
		case 403, 406:
			return Identity{}, ErrNeedOTP
		case 401, 407, 408, 409, 410:
			return Identity{}, ErrAccountLocked
		default:
			log.Printf("auth: Passwortprüfung für %q: DSM-Fehler %d", account, c)
			return Identity{}, ErrBadCredentials
		}
	}
	if sid == "" {
		return Identity{}, ErrNoPermission
	}
	defer a.logoutUpstream(sid)
	a.Limiter.Success(ip)
	info := a.sessionInfo(ctx, sid)
	if info.User != "" {
		account = info.User
	}
	if info.Priv != nil && !*info.Priv && !a.isAdminInfo(account, info) {
		return Identity{}, ErrNoPermission
	}
	a.rememberSession(account, used)
	return Identity{Account: account, IsManager: info.IsAdmin}, nil
}
