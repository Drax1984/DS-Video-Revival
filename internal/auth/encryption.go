package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"dsvideorevival/internal/webapi"
)

// Verschlüsselte Anmeldung der DS-video-Apps.
//
// Die Apps holen per SYNO.API.Encryption getinfo einen RSA-Schlüssel und
// schicken account/passwd (iOS evtl. auch session) verschlüsselt. Damit das
// Paket die Sitzung als „DSVideoRevival“ statt „VideoStation“ anmelden kann
// (sonst verweigert DSM normalen Benutzern mit 402), beantwortet es getinfo
// mit einem eigenen Schlüssel, entschlüsselt die Anmeldung und meldet sich
// stellvertretend bei DSM an (Upstream = 127.0.0.1, verlässt die NAS nicht).
//
// Format laut DS video (Android) com.synology.sylib.syhttp3.util.CgiEncryption:
// getinfo → data{cipherkey, ciphertoken, public_key (Base64 X.509/SPKI),
// server_time}; Klartext "<ciphertoken>=<zeit>&<urlencodierte Parameter>",
// RSA/ECB/PKCS1 verschlüsselt, Base64, als Parameter <cipherkey>.

const (
	cipherKey   = "__cIpHeRtExT"
	cipherToken = "__cIpHeRtOkEn"
)

var encKey struct {
	once sync.Once
	key  *rsa.PrivateKey
	pub  string
}

func encryptionKey() (*rsa.PrivateKey, string) {
	encKey.once.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			log.Printf("auth: RSA-Schlüssel nicht erzeugt: %v", err)
			return
		}
		der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			return
		}
		encKey.key, encKey.pub = k, base64.StdEncoding.EncodeToString(der)
	})
	return encKey.key, encKey.pub
}

// HandleEncryption beantwortet SYNO.API.Encryption getinfo mit eigenem Schlüssel.
func (a *Adapter) HandleEncryption(w http.ResponseWriter, req *webapi.Request) bool {
	if a.Mode == "local" || !strings.EqualFold(req.Method, "getinfo") {
		return false
	}
	_, pub := encryptionKey()
	if pub == "" {
		return false
	}
	webapi.OK(w, map[string]any{"cipherkey": cipherKey, "ciphertoken": cipherToken, "public_key": pub, "server_time": time.Now().Unix()})
	return true
}

// decryptLogin: entschlüsselte Parameter oder nil (z.B. mit DSM-Schlüssel
// verschlüsselt → unverändert an DSM).
func decryptLogin(p url.Values) url.Values {
	ct := p.Get(cipherKey)
	if ct == "" {
		return nil
	}
	k, _ := encryptionKey()
	if k == nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		return nil
	}
	plain, err := rsa.DecryptPKCS1v15(nil, k, raw)
	if err != nil {
		return nil
	}
	v, err := url.ParseQuery(string(plain))
	if err != nil {
		return nil
	}
	ts, err := strconv.ParseInt(v.Get(cipherToken), 10, 64)
	if err != nil || ts < time.Now().Add(-15*time.Minute).Unix() || ts > time.Now().Add(15*time.Minute).Unix() {
		log.Printf("auth: verschlüsselte Anmeldung mit ungültigem Zeitstempel verworfen")
		return nil
	}
	v.Del(cipherToken)
	return v
}
