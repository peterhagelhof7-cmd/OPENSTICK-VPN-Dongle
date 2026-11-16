package render

import (
	"fmt"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

// NMPrefix: alle vom Dienst verwalteten NetworkManager-Profile beginnen so.
const NMPrefix = "vpndongle-"

// LTEConnName ist das Profil für den Mobilfunk-Uplink.
const LTEConnName = NMPrefix + "lte"

// Route-Metriken: WLAN vor LTE.
const (
	MetricWLAN = 100
	MetricLTE  = 700
)

// NMFile = ein Keyfile für /etc/NetworkManager/system-connections.
type NMFile struct {
	Name    string // Dateiname ohne Endung = connection.id
	Content string
}

// NMKeyfiles erzeugt die Profile für alle WLANs + LTE.
//
// Uplink-Modi:
//   - auto: WLANs autoconnect, LTE NICHT autoconnect – der Dienst schaltet
//     LTE nur zu, wenn kein WLAN da ist (spart Datenvolumen), und wieder ab,
//     sobald WLAN stabil läuft.
//   - wlan: nur WLAN, LTE nie.
//   - lte:  LTE autoconnect, WLANs nicht.
func NMKeyfiles(n Net, c *config.Config) []NMFile {
	var out []NMFile
	wlanAuto := c.Uplink != config.UplinkLTE
	for i, w := range c.WLANs {
		name := fmt.Sprintf("%swlan-%d", NMPrefix, i)
		var b strings.Builder
		p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
		b.WriteString(header)
		p("[connection]")
		p("id=%s", name)
		p("uuid=%s", uuidFor(name))
		p("type=wifi")
		p("interface-name=%s", n.WLANIface)
		p("autoconnect=%t", wlanAuto)
		p("autoconnect-priority=%d", 100-i) // Reihenfolge = Priorität (wie ESP)
		p("autoconnect-retries=0")
		p("")
		p("[wifi]")
		p("mode=infrastructure")
		p("ssid=%s", keyfileEscape(w.SSID))
		p("")
		if w.Pass != "" {
			p("[wifi-security]")
			p("key-mgmt=wpa-psk")
			p("psk=%s", keyfileEscape(w.Pass))
			p("")
		}
		p("[ipv4]")
		p("method=auto")
		p("route-metric=%d", MetricWLAN)
		p("")
		p("[ipv6]")
		p("method=ignore")
		out = append(out, NMFile{Name: name, Content: b.String()})
	}

	if c.LTE.Enabled && c.LTE.APN != "" {
		var b strings.Builder
		p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
		b.WriteString(header)
		p("[connection]")
		p("id=%s", LTEConnName)
		p("uuid=%s", uuidFor(LTEConnName))
		p("type=gsm")
		p("autoconnect=%t", c.Uplink == config.UplinkLTE)
		p("autoconnect-retries=0")
		p("")
		p("[gsm]")
		p("apn=%s", keyfileEscape(c.LTE.APN))
		if c.LTE.User != "" {
			p("username=%s", keyfileEscape(c.LTE.User))
		}
		if c.LTE.Pass != "" {
			p("password=%s", keyfileEscape(c.LTE.Pass))
		}
		if c.LTE.PIN != "" {
			p("pin=%s", keyfileEscape(c.LTE.PIN))
		}
		p("home-only=%t", !c.LTE.Roaming)
		p("")
		p("[ipv4]")
		p("method=auto")
		p("route-metric=%d", MetricLTE)
		p("")
		p("[ipv6]")
		p("method=ignore")
		out = append(out, NMFile{Name: LTEConnName, Content: b.String()})
	}
	return out
}

// keyfileEscape maskiert Werte für GKeyFile (Backslash, Steuerzeichen,
// führende Leerzeichen).
func keyfileEscape(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == ' ' && i == 0:
			b.WriteString(`\s`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
