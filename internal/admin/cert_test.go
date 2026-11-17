package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dsvideorevival/internal/upstream"
	"dsvideorevival/internal/vs"
)

// ZIP wie aus DSM → Sicherheit → Zertifikat → Exportieren (cert.pem, privkey.pem, chain.pem).
func TestSaveCertZip(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nas.example.org"}, DNSNames: []string{"nas.example.org"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	kb, _ := x509.MarshalECPrivateKey(key)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range map[string][]byte{
		"cert.pem":    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"privkey.pem": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}),
		"chain.pem":   nil,
	} {
		w, _ := zw.Create("archive/" + name)
		w.Write(b)
	}
	zw.Close()
	dir := t.TempDir()
	cf, kf, err := saveCertZip(buf.Bytes(), dir)
	if err != nil || !strings.HasPrefix(cf, dir) || !strings.HasPrefix(kf, dir) {
		t.Fatal(cf, kf, err)
	}
	if s := certSubject(cf); !strings.HasPrefix(s, "nas.example.org (gültig bis ") {
		t.Fatalf("Subjekt: %q", s)
	}
	// Nginx-Proxy-Manager-Download (Let's Encrypt-Dateinamen)
	var npm bytes.Buffer
	zw2 := zip.NewWriter(&npm)
	for name, b := range map[string][]byte{
		"fullchain1.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"privkey1.pem":   pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}),
	} {
		w, _ := zw2.Create(name)
		w.Write(b)
	}
	zw2.Close()
	if _, _, err := saveCertZip(npm.Bytes(), t.TempDir()); err != nil {
		t.Fatalf("NPM-ZIP: %v", err)
	}
	if _, _, err := saveCertZip([]byte(`{"success":false}`), dir); err == nil {
		t.Fatal("JSON statt ZIP muss abgelehnt werden")
	}
}

func testCertZip(t *testing.T) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "nas.example.de"}, DNSNames: []string{"nas.example.de"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	kb, _ := x509.MarshalECPrivateKey(key)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range map[string][]byte{"cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"privkey.pem": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), "chain.pem": nil} {
		w, _ := zw.Create(name)
		w.Write(b)
	}
	zw.Close()
	return buf.Bytes()
}

// Export wie die DSM-Oberfläche: GET, file="archive", Klartext-ID, SynoToken.
// Ohne „file“ antwortet DSM mit Fehler 5503 (synology-srm #13).
func TestExportDSMCert(t *testing.T) {
	zipData := testCertZip(t)
	dsm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("api") {
		case "SYNO.Core.Certificate.CRT":
			io.WriteString(w, `{"data":{"certificates":[{"id":"Ab12Cd","is_default":true},{"id":"other","is_default":false}]},"success":true}`)
		case "SYNO.Core.Certificate":
			if r.Method != http.MethodGet || r.Form.Get("file") != `"archive"` || r.Form.Get("id") != "Ab12Cd" || r.Form.Get("_sid") != "SID" {
				io.WriteString(w, `{"error":{"code":5503},"success":false}`)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Write(zipData)
		}
	}))
	defer dsm.Close()
	up, _ := upstream.New(dsm.URL)
	h := &Handler{S: &vs.Server{Up: up}}
	for _, tok := range []string{"", "TOK"} { // Sitzung ohne bzw. mit SynoToken
		cf, _, err := h.exportDSMCert(context.Background(), "SID", tok, t.TempDir())
		if err != nil || !strings.HasPrefix(certSubject(cf), "nas.example.de") {
			t.Fatalf("Token %q: %v %q", tok, err, certSubject(cf))
		}
	}
}
