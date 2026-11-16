package render

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

// NMResolv ist die von NetworkManager gepflegte Upstream-resolv.conf.
const NMResolv = "/run/NetworkManager/resolv.conf"

// Dnsmasq erzeugt die Konfig der dongle-eigenen dnsmasq-Instanz (nur usb0):
// DHCP für den PC inkl. Option 121 (Routen-Push wie ESP Phase 9) und DNS.
// Gesperrt: jede A-Anfrage -> Dongle-IP (Captive-Portal-Erkennung der OS).
func Dnsmasq(n Net, c *config.Config, s State) string {
	var b strings.Builder
	b.WriteString(header)
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }

	first := n.DongleIP.Next()
	last := lastHost(n.USBNet)
	p("interface=%s", n.USBIface)
	p("bind-dynamic") // startet auch, wenn usb0 noch keine Adresse hat
	p("listen-address=%s", n.DongleIP)
	p("except-interface=lo")
	p("dhcp-authoritative")
	p("dhcp-range=%s,%s,%s,10m", first, last, maskString(n.USBNet.Bits()))
	p("dhcp-option=option:router,%s", n.DongleIP)
	p("dhcp-option=option:dns-server,%s", n.DongleIP)
	p("dhcp-option=option:domain-name,vpndongle")
	p("domain-needed")
	p("bogus-priv")
	p("no-hosts")
	p("address=/dongle.vpndongle/%s", n.DongleIP)
	p("cache-size=500")

	// Option 121: Windows/Linux/macOS (neuere) bevorzugen den Dongle ohne
	// Metrik-Spielereien. Volltunnel als zwei /1 – spezifischer als jede
	// Default-Route, wie beim ESP.
	if routes := pushRoutes(c, s); len(routes) > 0 {
		var parts []string
		for _, r := range routes {
			parts = append(parts, r.String(), n.DongleIP.String())
		}
		p("dhcp-option=option:classless-static-route,%s", strings.Join(parts, ","))
	}

	switch {
	case s.Locked:
		b.WriteString("# Captive-Portal: gesperrt\n")
		p("address=/#/%s", n.DongleIP)
		p("no-resolv")
	case s.Ov && s.VPNOn && s.OvExit:
		b.WriteString("# Overlay mit Exit-Node: alle Anfragen über den Resolver des Clients\n")
		p("no-resolv")
		p("server=%s", s.OvResolver)
	case !s.Ov && s.VPNOn && c.WG.FullTunnel:
		b.WriteString("# Volltunnel: DNS durch den Tunnel\n")
		p("no-resolv")
		for _, d := range strings.Split(c.WG.DNS, ",") {
			if d = strings.TrimSpace(d); d != "" {
				p("server=%s", d)
			}
		}
	default:
		p("resolv-file=%s", NMResolv)
		if s.Ov && s.VPNOn && reDomain.MatchString(s.OvDomain) {
			b.WriteString("# Namen im Overlay-Netz über den Resolver des Clients\n")
			p("server=/%s/%s", s.OvDomain, s.OvResolver)
		}
	}
	return b.String()
}

// reDomain: nur harmlose DNS-Namen in die dnsmasq-Konfig übernehmen.
var reDomain = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)*[A-Za-z0-9-]{1,63}$`)

// pushRoutes: welche Routen der PC per Option 121 bekommt.
// Ohne VPN: keine (PC nimmt Option 3 = Dongle als Default-Gateway).
func pushRoutes(c *config.Config, s State) []netip.Prefix {
	if s.Locked || !s.VPNOn {
		return nil
	}
	if s.Ov {
		if s.OvExit {
			return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
		}
		var nets []netip.Prefix
		if s.OvNet.IsValid() {
			nets = append(nets, s.OvNet)
		}
		nets = append(nets, s.OvRoutes...)
		if len(nets) == 0 {
			return nil // noch nicht verbunden: Option 3 genügt
		}
		return append(nets, netip.MustParsePrefix("0.0.0.0/0"))
	}
	if c.WG.FullTunnel {
		return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}
	}
	nets, err := SplitNets(c)
	if err != nil {
		return nil
	}
	// Bei Option 121 ignoriert Windows Option 3 -> Default-Route nicht vergessen,
	// sonst fehlt dem PC im Split-Modus das normale Internet über den Dongle.
	return append(nets, netip.MustParsePrefix("0.0.0.0/0"))
}

func lastHost(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := (uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])) | host
	v-- // Broadcast auslassen
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func maskString(bits int) string {
	m := ^uint32(0) << (32 - bits)
	return fmt.Sprintf("%d.%d.%d.%d", m>>24, m>>16&0xff, m>>8&0xff, m&0xff)
}
