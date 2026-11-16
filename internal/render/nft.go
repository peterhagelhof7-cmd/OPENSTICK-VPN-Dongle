package render

import (
	"fmt"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

// privateNets: Ziele, die bei LAN-Isolation nicht über den Uplink erreichbar
// sein dürfen (lokales WLAN, Carrier-NAT, Link-Local).
const privateNets = "10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16"

// Nft erzeugt die eigene Tabelle "inet vpndongle" (idempotent ladbar mit
// `nft -f`). Fremde Tabellen bleiben unberührt.
//
//   - Weiterleitung nur usb0 -> Uplink/Tunnel, Rückweg per conntrack
//   - Captive gesperrt: nichts weiterleiten
//   - Kill-Switch: vom PC nur noch über wg0 (wie ESP: gilt auch bei VPN-AUS)
//   - Isolation: PC erreicht keine privaten Netze am Uplink (ESP: offen gewesen)
//   - Web/SSH/Ping des Dongles je Netz laut Haken-Tabelle (Config.Access);
//     DNS/DHCP fest nur auf usb0 (ESP: alles offen gewesen)
//   - MSS-Clamping (WireGuard + LTE haben kleinere MTU)
func Nft(n Net, c *config.Config, s State) string {
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	b.WriteString(header)
	p("table inet vpndongle")
	p("delete table inet vpndongle")
	p("table inet vpndongle {")

	p("  chain input {")
	p("    type filter hook input priority filter; policy accept;")
	p("    iifname \"lo\" accept") // lokale Dienste (Zabbix-Agent -> vpndongled/Proxy)
	// Freigaben aus der Haken-Tabelle (Einstellungen → Zugriff auf den Stick)
	acc := c.Access
	if acc == nil {
		acc = config.DefaultAccess()
	}
	if s.SSHFallback {
		p("    # SSH-Notfallmodus: USB-Netz fehlt, SSH aus allen Netzen")
	} else {
		allowRules(p, n, acc, config.SvcSSH, "tcp dport 22")
	}
	allowRules(p, n, acc, config.SvcWeb, "tcp dport { 80, 443 }")
	allowRules(p, n, acc, config.SvcPing, "icmp type echo-request")
	allowRules(p, n, acc, config.SvcPing, "icmpv6 type echo-request")
	// Zabbix: Agent-Port nie von außen; Proxy-Port nur vom PC, wenn dort ein
	// Agent melden darf (aktiver Proxy braucht selbst keinen offenen Port)
	p("    tcp dport 10050 drop")
	if c.Zabbix.Enabled && c.Zabbix.PCAgent {
		p("    iifname != %q tcp dport 10051 drop", n.USBIface)
	} else {
		p("    tcp dport 10051 drop")
	}
	// DNS/DHCP fest nur USB (dnsmasq lauscht ohnehin nur auf usb0)
	p("    iifname != %q udp dport { 53, 67 } drop", n.USBIface)
	p("    iifname != %q tcp dport 53 drop", n.USBIface)
	p("  }")

	p("  chain forward {")
	p("    type filter hook forward priority filter; policy drop;")
	p("    tcp flags syn tcp option maxseg size set rt mtu")
	p("    ct state established,related accept")
	p("    ct state invalid drop")
	p("    iifname %q jump from_pc", n.USBIface)
	p("  }")

	p("  chain from_pc {")
	p("    meta nfproto ipv6 drop")
	p("    ip saddr != %s drop", n.USBNet)
	if s.Locked {
		p("    # Captive-Portal gesperrt")
		p("    drop")
	}
	tun := s.TunnelIface(n)
	if c.KillSwitchOn() {
		p("    # Kill-Switch: nur durch den Tunnel")
		p("    oifname != %q drop", tun)
	}
	if c.Isolate {
		p("    # LAN-Isolation am Uplink")
		p("    oifname != %q ip daddr { %s } drop", tun, privateNets)
	}
	// Uplink-Namen (wlan0, wwan0/rmnet*, wg0) nicht fest verdrahten:
	// alles außer zurück ins USB-Netz ist erlaubt.
	p("    oifname != %q accept", n.USBIface)
	p("  }")

	p("  chain postrouting {")
	p("    type nat hook postrouting priority srcnat; policy accept;")
	p("    ip saddr %s oifname != %q masquerade", n.USBNet, n.USBIface)
	p("  }")
	p("}")
	return b.String()
}

// accessIfaces: Netz der Haken-Tabelle -> iifname(s). „VPN“ = WireGuard und
// NetBird; LTE als Muster, der Name des Datennetzes hängt vom Modem ab (wwan0, wwan1 …).
func accessIfaces(n Net, net string) []string {
	switch net {
	case config.NetUSB:
		return []string{n.USBIface}
	case config.NetVPN:
		return []string{n.WGIface, n.NBIface, n.TSIface}
	case config.NetWLAN:
		return []string{n.WLANIface}
	case config.NetLTE:
		return []string{strings.TrimRight(n.LTEIface, "0123456789") + "*"}
	}
	return nil
}

// allowRules: <match> aus den freigegebenen Netzen annehmen, sonst verwerfen.
func allowRules(p func(string, ...any), n Net, acc config.Access, svc, match string) {
	var nets []string
	for _, net := range config.AccessNets {
		if acc.Allows(svc, net) {
			nets = append(nets, net)
		}
	}
	where := strings.Join(nets, ", ")
	if where == "" {
		where = "nirgends"
	}
	p("    # %s: %s", svc, where)
	for _, net := range nets {
		for _, ifn := range accessIfaces(n, net) {
			p("    iifname %q %s accept", ifn, match)
		}
	}
	p("    %s drop", match)
}
