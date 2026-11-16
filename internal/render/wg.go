package render

import (
	"fmt"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

// WGQuick erzeugt /etc/wireguard/wg0.conf für wg-quick.
// Volltunnel: AllowedIPs 0.0.0.0/0 -> wg-quick setzt fwmark-Policy-Routing,
// der Tunnel selbst läuft weiter über den aktuellen Uplink (WLAN/LTE) und
// WireGuard wechselt beim Uplink-Wechsel automatisch den Weg (Roaming).
// Split: nur Tunnel-Subnetz + wg.allowed (beim ESP fehlte das noch).
// Kein DNS= (würde resolvconf anfassen) – DNS macht dnsmasq.
func WGQuick(c *config.Config) (string, error) {
	pfx, err := WGPrefix(c.WG.Addr, c.WG.Mask)
	if err != nil {
		return "", err
	}
	allowed := "0.0.0.0/0"
	if !c.WG.FullTunnel {
		nets, err := SplitNets(c)
		if err != nil {
			return "", err
		}
		var s []string
		for _, n := range nets {
			s = append(s, n.String())
		}
		allowed = strings.Join(s, ", ")
	}
	for _, v := range []string{c.WG.Priv, c.WG.Pub, c.WG.PSK, c.WG.Endpoint} {
		if strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("Zeilenumbruch in WireGuard-Feld")
		}
	}

	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	b.WriteString(header)
	p("[Interface]")
	p("PrivateKey = %s", c.WG.Priv)
	p("Address = %s", pfx)
	p("MTU = %d", c.WG.MTU)
	p("")
	p("[Peer]")
	p("PublicKey = %s", c.WG.Pub)
	if c.WG.PSK != "" {
		p("PresharedKey = %s", c.WG.PSK)
	}
	host := c.WG.Endpoint
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // IPv6-Literal
	}
	p("Endpoint = %s:%d", host, c.WG.Port)
	p("AllowedIPs = %s", allowed)
	if c.WG.Keepalive > 0 {
		p("PersistentKeepalive = %d", c.WG.Keepalive)
	}
	return b.String(), nil
}
