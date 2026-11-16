package system

import (
	"encoding/json"
	"net/netip"
	"strings"
)

// NBStatus: das Nötige aus `netbird status --json` (Client 0.83).
type NBStatus struct {
	Daemon     string   `json:"daemon"`     // Connected, Connecting, NeedsLogin, Idle, LoginFailed …
	Management bool     `json:"management"` // mit dem Management verbunden
	MgmtError  string   `json:"mgmt_error,omitempty"`
	IP         string   `json:"ip"`       // Overlay-Adresse mit Präfix, z. B. 100.92.1.7/16
	FQDN       string   `json:"fqdn"`     // z. B. vpndongle-x.netbird.selfhosted
	PeersUp    int      `json:"peers_up"` // verbundene Peers
	Peers      int      `json:"peers"`    // bekannte Peers
	Networks   []string `json:"networks"` // Routen aus dem Management (CIDR)
}

// Connected: Tunnel nutzbar (Daemon verbunden und Management erreichbar).
func (s NBStatus) Connected() bool { return s.Daemon == "Connected" && s.Management }

// Domain: DNS-Zone der Peers (FQDN ohne den eigenen Namen).
func (s NBStatus) Domain() string {
	if _, d, ok := strings.Cut(s.FQDN, "."); ok {
		return d
	}
	return ""
}

// Overlay: Netz der NetBird-Adressen (aus der eigenen IP mit Präfix).
func (s NBStatus) Overlay() (netip.Prefix, bool) {
	p, err := netip.ParsePrefix(s.IP)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, false
	}
	return p.Masked(), true
}

// Routes: gültige IPv4-Routen aus Networks (sortiert wie geliefert, ohne Doppelte).
func (s NBStatus) Routes() []netip.Prefix {
	seen := map[netip.Prefix]bool{}
	var out []netip.Prefix
	for _, n := range s.Networks {
		p, err := netip.ParsePrefix(strings.TrimSpace(n))
		if err != nil || !p.Addr().Is4() {
			continue
		}
		p = p.Masked()
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// ExitNode: kommt eine Standardroute (0.0.0.0/0) über NetBird?
func (s NBStatus) ExitNode() bool {
	for _, p := range s.Routes() {
		if p.Bits() == 0 {
			return true
		}
	}
	return false
}

// ParseNetBird liest `netbird status --json`. Leere/kaputte Ausgabe -> Daemon "".
func ParseNetBird(out string) NBStatus {
	var j struct {
		Daemon string `json:"daemonStatus"`
		Mgmt   struct {
			Connected bool   `json:"connected"`
			Error     string `json:"error"`
		} `json:"management"`
		IP    string `json:"netbirdIp"`
		FQDN  string `json:"fqdn"`
		Peers struct {
			Total     int `json:"total"`
			Connected int `json:"connected"`
		} `json:"peers"`
		Networks []string `json:"networks"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &j); err != nil {
		return NBStatus{}
	}
	return NBStatus{Daemon: j.Daemon, Management: j.Mgmt.Connected, MgmtError: j.Mgmt.Error, IP: j.IP,
		FQDN: j.FQDN, PeersUp: j.Peers.Connected, Peers: j.Peers.Total, Networks: j.Networks}
}
