package config

import "strings"

// Freigaben: welcher Dienst des Sticks ist aus welchem Netz erreichbar
// (Einstellungen → „Zugriff auf den Stick“, eine Haken-Tabelle).
// DNS und DHCP sind NICHT dabei: dnsmasq lauscht nur auf usb0 (192.168.226.1).

// Netze (Spalten)
const (
	NetUSB  = "usb"  // PC am USB (usb0)
	NetVPN  = "vpn"  // durch den WireGuard-Tunnel (wg0)
	NetWLAN = "wlan" // WLAN am Uplink (wlan0)
	NetLTE  = "lte"  // Mobilfunk (wwan*)
)

// Dienste (Zeilen)
const (
	SvcWeb  = "web"  // Weboberfläche, TCP 80/443
	SvcSSH  = "ssh"  // TCP 22
	SvcPing = "ping" // ICMP/ICMPv6 Echo
)

// AccessNets / AccessServices: feste Reihenfolge für Tabelle, Export und Firewall.
var (
	AccessNets     = []string{NetUSB, NetVPN, NetWLAN, NetLTE}
	AccessServices = []string{SvcWeb, SvcSSH, SvcPing}
)

// Access: Dienst -> Netze, aus denen er erreichbar ist.
type Access map[string][]string

// DefaultAccess = Verhalten vor der Tabelle: Web nur USB, SSH USB + Tunnel, Ping überall.
func DefaultAccess() Access {
	return Access{
		SvcWeb:  {NetUSB},
		SvcSSH:  {NetUSB, NetVPN},
		SvcPing: {NetUSB, NetVPN, NetWLAN, NetLTE},
	}
}

// Allows meldet, ob svc aus net erreichbar ist.
func (a Access) Allows(svc, net string) bool {
	for _, n := range a[svc] {
		if n == net {
			return true
		}
	}
	return false
}

// normalizeAccess: fehlende Dienste mit Standard füllen, unbekannte Netze/Dienste
// verwerfen, Reihenfolge festlegen und die Schutzregeln durchsetzen:
//   - Weboberfläche über USB immer an (sonst sperrt man sich aus)
//   - Weboberfläche über andere Netze nur ohne Standard-Passwort admin/admin
func normalizeAccess(in Access, defaultPW bool) Access {
	def := DefaultAccess()
	out := Access{}
	for _, svc := range AccessServices {
		nets, ok := in[svc]
		if !ok {
			nets = def[svc]
		}
		var clean []string
		for _, n := range AccessNets {
			for _, x := range nets {
				if x == n {
					clean = append(clean, n)
					break
				}
			}
		}
		if clean == nil {
			clean = []string{} // bewusst „nirgends“ – nicht wieder auf Standard fallen
		}
		out[svc] = clean
	}
	if !out.Allows(SvcWeb, NetUSB) {
		out[SvcWeb] = append([]string{NetUSB}, out[SvcWeb]...)
	}
	if defaultPW {
		out[SvcWeb] = []string{NetUSB}
	}
	return out
}

func isService(s string) bool {
	for _, x := range AccessServices {
		if x == s {
			return true
		}
	}
	return false
}

// parseNets: "usb, vpn" -> [usb vpn] (für Import und Formulare).
func parseNets(v string) []string {
	var out []string
	for _, f := range strings.Split(v, ",") {
		if f = strings.TrimSpace(strings.ToLower(f)); f != "" {
			out = append(out, f)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}
