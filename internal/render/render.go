// Package render erzeugt aus Konfig + Laufzeitzustand die Systemdateien
// (dnsmasq, nftables, WireGuard, NetworkManager-Keyfiles). Reine Funktionen
// ohne Seiteneffekte -> vollständig unter Windows testbar.
package render

import (
	"crypto/sha1"
	"fmt"
	"net/netip"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

// Net beschreibt die festen Schnittstellen des Sticks.
type Net struct {
	USBIface  string       // USB-Gadget-Netz zum PC (usb0)
	WLANIface string       // wlan0
	LTEIface  string       // wwan0 (je nach Kernel/Modem auch rmnet_*)
	WGIface   string       // wg0
	NBIface   string       // wt0 (NetBird)
	TSIface   string       // tailscale0
	USBNet    netip.Prefix // 192.168.226.0/29 (wie ESP)
	DongleIP  netip.Addr   // 192.168.226.1
}

// DefaultNet = Werte wie beim ESP-Dongle (PC bekommt .2–.6).
func DefaultNet() Net {
	return Net{
		USBIface: "usb0", WLANIface: "wlan0", LTEIface: "wwan0", WGIface: "wg0", NBIface: "wt0", TSIface: "tailscale0",
		USBNet:   netip.MustParsePrefix("192.168.226.0/29"),
		DongleIP: netip.MustParseAddr("192.168.226.1"),
	}
}

// State = Laufzeitentscheidungen, die die Regeln beeinflussen.
type State struct {
	Locked bool // Captive-Portal gesperrt -> DNS-Hijack, keine Weiterleitung
	VPNOn  bool // Tunnel soll laufen (konfiguriert und nicht abgeschaltet)
	// SSHFallback: USB-Netz fehlt (Gadget nicht hochgekommen) -> SSH ausnahmsweise
	// auch über WLAN/LTE, damit man sich nicht aussperrt.
	SSHFallback bool

	// Overlay-Netz (VPN-Art NetBird oder Tailscale): Routen/DNS kommen vom Client
	Ov         bool
	OvIface    string         // wt0 / tailscale0
	OvResolver string         // DNS-Resolver des Clients in dnsmasq-Schreibweise
	OvRoutes   []netip.Prefix // Netze aus dem Management/Tailnet (ohne 0.0.0.0/0)
	OvNet      netip.Prefix   // Netz der Overlay-Adressen, z. B. 100.92.0.0/16
	OvDomain   string         // DNS-Zone der Peers, z. B. netbird.selfhosted
	OvExit     bool           // Exit-Node -> Volltunnel
}

// Resolver der Overlay-Clients.
const (
	NBResolverAddr = "127.0.0.1:5053"  // für `netbird up --dns-resolver-address`
	NBResolverDNS  = "127.0.0.1#5053"  // dnsmasq-Schreibweise
	TSResolverDNS  = "100.100.100.100" // MagicDNS (Quad100) des Tailscale-Clients
)

// TunnelIface: Interface der gewählten VPN-Art.
func (s State) TunnelIface(n Net) string {
	if s.Ov && s.OvIface != "" {
		return s.OvIface
	}
	return n.WGIface
}

// WGPrefix wandelt addr+mask (ESP-Format) in ein Präfix, z. B. 10.0.0.2/24.
func WGPrefix(addr, mask string) (netip.Prefix, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(addr))
	if err != nil || !a.Is4() {
		return netip.Prefix{}, fmt.Errorf("ungültige Tunnel-IP %q", addr)
	}
	m, err := netip.ParseAddr(strings.TrimSpace(mask))
	if err != nil || !m.Is4() {
		return netip.Prefix{}, fmt.Errorf("ungültige Maske %q", mask)
	}
	b := m.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	bits := 0
	for v&0x80000000 != 0 {
		bits++
		v <<= 1
	}
	if v != 0 {
		return netip.Prefix{}, fmt.Errorf("Maske %q nicht zusammenhängend", mask)
	}
	return netip.PrefixFrom(a, bits), nil
}

// SplitNets = Netze, die im Split-Modus durch den Tunnel gehen:
// Tunnel-Subnetz + wg.allowed (kommagetrennt).
func SplitNets(c *config.Config) ([]netip.Prefix, error) {
	p, err := WGPrefix(c.WG.Addr, c.WG.Mask)
	if err != nil {
		return nil, err
	}
	nets := []netip.Prefix{p.Masked()}
	for _, s := range strings.Split(c.WG.Allowed, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		q, err := netip.ParsePrefix(s)
		if err != nil || !q.Addr().Is4() {
			return nil, fmt.Errorf("ungültiges Split-Netz %q", s)
		}
		nets = append(nets, q.Masked())
	}
	return nets, nil
}

// uuidFor liefert eine stabile UUID aus einem Namen (für NM-Keyfiles).
func uuidFor(name string) string {
	h := sha1.Sum([]byte("openstick-vpn-dongle:" + name))
	h[6] = (h[6] & 0x0f) | 0x50
	h[8] = (h[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

const header = "# Erzeugt von vpndongled – Änderungen werden überschrieben.\n"
