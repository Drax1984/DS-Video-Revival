package jellyfin

import (
	"encoding/json"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
)

// DiscoveryPort: UDP-Port, über den Jellyfin-Apps Server im Heimnetz suchen.
const DiscoveryPort = 7359

// discovery beantwortet „who is JellyfinServer?“ – so finden die Apps die NAS
// ohne Eingabe der Adresse.
type discovery struct {
	conn *net.UDPConn
	once sync.Once
}

func startDiscovery(h *Handler) *discovery {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: DiscoveryPort})
	if err != nil {
		log.Printf("jellyfin: Suche im Heimnetz (UDP %d) nicht verfügbar: %v", DiscoveryPort, err)
		return &discovery{}
	}
	d := &discovery{conn: conn}
	go func() {
		buf := make([]byte, 1024)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			msg := strings.ToLower(strings.TrimSpace(string(buf[:n])))
			if !strings.Contains(msg, "who is jellyfinserver") && !strings.Contains(msg, "who is embyserver") {
				continue
			}
			if !h.Enabled() {
				continue
			}
			ip := localIPFor(from.IP)
			if ip == "" {
				continue
			}
			resp, _ := json.Marshal(map[string]any{
				"Address":         "http://" + ip + ":" + strconv.Itoa(h.S.Cfg().JellyfinListenPort()),
				"Id":              h.store.serverID(),
				"Name":            h.serverName(),
				"EndpointAddress": nil,
			})
			_, _ = conn.WriteToUDP(resp, from)
		}
	}()
	return d
}

func (d *discovery) stop() {
	d.once.Do(func() {
		if d.conn != nil {
			_ = d.conn.Close()
		}
	})
}

// localIPFor: eigene Adresse auf dem Weg zum Client (ohne Pakete zu senden).
func localIPFor(remote net.IP) string {
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: remote, Port: DiscoveryPort})
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}
