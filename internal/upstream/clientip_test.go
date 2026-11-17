package upstream

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	for _, tc := range []struct{ remote, xff, want string }{
		{"203.0.113.9:4000", "", "203.0.113.9"},                                  // direkt aus dem Internet
		{"203.0.113.9:4000", "1.2.3.4", "203.0.113.9"},                           // XFF von außen wird ignoriert
		{"127.0.0.1:5000", "198.51.100.7", "198.51.100.7"},                       // über DSMs Webserver
		{"127.0.0.1:5000", "198.51.100.7, 192.168.0.2", "198.51.100.7"},          // Internet → NPM (LAN) → DSM
		{"127.0.0.1:5000", "6.6.6.6, 198.51.100.7, 192.168.0.2", "198.51.100.7"}, // gefälschter Eintrag vorne
		{"127.0.0.1:5000", "192.168.0.50", "192.168.0.50"},                       // iPhone im WLAN über DSM
		{"127.0.0.1:5000", "", "127.0.0.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := ClientIP(r); got != tc.want {
			t.Errorf("%s / %q: %s, erwartet %s", tc.remote, tc.xff, got, tc.want)
		}
	}
}
