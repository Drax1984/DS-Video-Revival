package auth

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPassword(t *testing.T) {
	h, err := HashPassword("geheim")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "geheim") || VerifyPassword(h, "Geheim") || VerifyPassword("murks", "geheim") {
		t.Fatal("VerifyPassword")
	}
}

func TestRateLimiter(t *testing.T) {
	r := NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		r.Fail("1.2.3.4")
	}
	if !r.Blocked("1.2.3.4") || r.Blocked("5.6.7.8") {
		t.Fatal("Sperre")
	}
	r.Success("1.2.3.4")
	if r.Blocked("1.2.3.4") {
		t.Fatal("Reset")
	}
}

func TestStorePersistsHashedOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	s := NewStore(p)
	s.Put("SECRETSID", Session{Account: "anna"})
	s2 := NewStore(p)
	if got, ok := s2.Get("SECRETSID"); !ok || got.Account != "anna" {
		t.Fatal("persistenz")
	}
	b, _ := readFile(p)
	if contains(b, "SECRETSID") {
		t.Fatal("sid im Klartext gespeichert")
	}
}
