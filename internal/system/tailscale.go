package system

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"
)

// TSStatus: das Nötige aus `tailscale status --json` (Client 1.104).
type TSStatus struct {
	Backend  string   `json:"backend"` // Running, NeedsLogin, Stopped, Starting …
	Online   bool     `json:"online"`  // Gerät beim Koordinationsserver online
	IP       string   `json:"ip"`      // eigene Tailscale-IPv4
	DNSName  string   `json:"dns_name"`
	Suffix   string   `json:"suffix"` // MagicDNS-Suffix, z. B. tail1234.ts.net
	PeersUp  int      `json:"peers_up"`
	Peers    int      `json:"peers"`
	Routes   []string `json:"routes"`              // Subnetz-Routen der Peers (ohne Exit-Node)
	ExitNode string   `json:"exit_node,omitempty"` // Name des aktiven Exit-Nodes
	Health   []string `json:"health,omitempty"`
}

// Connected: im Tailnet und online.
func (s TSStatus) Connected() bool { return s.Backend == "Running" && s.Online }

// Overlay: Tailscale nutzt 100.64.0.0/10 (CGNAT-Bereich) für alle Geräte.
func (s TSStatus) Overlay() (netip.Prefix, bool) {
	if s.IP == "" {
		return netip.Prefix{}, false
	}
	return netip.MustParsePrefix("100.64.0.0/10"), true
}

// RoutePrefixes: gültige IPv4-Subnetz-Routen.
func (s TSStatus) RoutePrefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, r := range s.Routes {
		if p, err := netip.ParsePrefix(r); err == nil && p.Addr().Is4() && p.Bits() > 0 {
			out = append(out, p.Masked())
		}
	}
	return out
}

// ParseTailscale liest `tailscale status --json`. Kaputte Ausgabe -> Backend "".
func ParseTailscale(out string) TSStatus {
	type node struct {
		HostName      string   `json:"HostName"`
		DNSName       string   `json:"DNSName"`
		TailscaleIPs  []string `json:"TailscaleIPs"`
		Online        bool     `json:"Online"`
		ExitNode      bool     `json:"ExitNode"`
		PrimaryRoutes []string `json:"PrimaryRoutes"`
	}
	var j struct {
		Backend        string           `json:"BackendState"`
		Self           node             `json:"Self"`
		Suffix         string           `json:"MagicDNSSuffix"`
		Peer           map[string]node  `json:"Peer"`
		Health         []string         `json:"Health"`
		CurrentTailnet *json.RawMessage `json:"CurrentTailnet"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &j); err != nil {
		return TSStatus{}
	}
	s := TSStatus{Backend: j.Backend, Online: j.Self.Online, DNSName: strings.TrimSuffix(j.Self.DNSName, "."),
		Suffix: strings.TrimSuffix(j.Suffix, "."), Health: j.Health}
	for _, ip := range j.Self.TailscaleIPs {
		if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
			s.IP = ip
		}
	}
	seen := map[string]bool{}
	for _, p := range j.Peer {
		s.Peers++
		if p.Online {
			s.PeersUp++
		}
		if p.ExitNode {
			s.ExitNode = strings.TrimSuffix(p.DNSName, ".")
			if s.ExitNode == "" {
				s.ExitNode = p.HostName
			}
		}
		for _, r := range p.PrimaryRoutes {
			if r != "0.0.0.0/0" && r != "::/0" && !seen[r] {
				seen[r] = true
				s.Routes = append(s.Routes, r)
			}
		}
	}
	sort.Strings(s.Routes)
	return s
}
