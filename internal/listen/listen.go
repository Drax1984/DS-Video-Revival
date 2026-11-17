// Package listen verwaltet die HTTP(S)-Listener zur Laufzeit: Wartungsport
// (Standard 5180) läuft immer; im Port-Modus kommen 5000/5001 hinzu, sobald
// DSM sie freigegeben hat.
package listen

import (
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// CertSource liefert Zertifikat- und Schlüsseldatei (wird bei Änderung neu geladen).
type CertSource func() (certFile, keyFile string)

// Manager hält die laufenden Server.
type Manager struct {
	Handler http.Handler
	Certs   CertSource
	mu      sync.Mutex
	servers map[string]*http.Server
	cache   struct {
		sync.Mutex
		cert  *tls.Certificate
		file  string
		mtime time.Time
	}
}

// New creates a manager.
func New(h http.Handler, certs CertSource) *Manager {
	return &Manager{Handler: h, Certs: certs, servers: map[string]*http.Server{}}
}

func (m *Manager) getCert(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cf, kf := m.Certs()
	st, err := os.Stat(cf)
	if err != nil {
		return nil, err
	}
	m.cache.Lock()
	defer m.cache.Unlock()
	if m.cache.cert != nil && m.cache.file == cf && m.cache.mtime.Equal(st.ModTime()) {
		return m.cache.cert, nil
	}
	c, err := tls.LoadX509KeyPair(cf, kf)
	if err != nil {
		return nil, err
	}
	m.cache.cert, m.cache.file, m.cache.mtime = &c, cf, st.ModTime()
	return &c, nil
}

// Ensure starts a listener on addr (retrying while the port is busy, max wait).
func (m *Manager) Ensure(addr string, useTLS bool, wait time.Duration) error {
	if addr == "" {
		return nil
	}
	m.mu.Lock()
	if _, ok := m.servers[addr]; ok {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	deadline := time.Now().Add(wait)
	var ln net.Listener
	var err error
	for {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
	srv := &http.Server{Handler: m.Handler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	if useTLS {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: m.getCert}
		ln = tls.NewListener(ln, srv.TLSConfig)
	}
	m.mu.Lock()
	m.servers[addr] = srv
	m.mu.Unlock()
	scheme := map[bool]string{true: "HTTPS", false: "HTTP"}[useTLS]
	log.Printf("listen: %s auf %s", scheme, addr)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("listen: %s beendet: %v", addr, err)
		}
		m.mu.Lock()
		if m.servers[addr] == srv {
			delete(m.servers, addr)
		}
		m.mu.Unlock()
	}()
	return nil
}

// Stop closes a listener.
func (m *Manager) Stop(addr string) {
	m.mu.Lock()
	srv, ok := m.servers[addr]
	delete(m.servers, addr)
	m.mu.Unlock()
	if ok {
		_ = srv.Close()
		log.Printf("listen: %s geschlossen", addr)
	}
}

// Running reports whether addr is served.
func (m *Manager) Running(addr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.servers[addr]
	return ok
}

// CloseAll stops everything.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	all := m.servers
	m.servers = map[string]*http.Server{}
	m.mu.Unlock()
	for _, s := range all {
		_ = s.Close()
	}
}
