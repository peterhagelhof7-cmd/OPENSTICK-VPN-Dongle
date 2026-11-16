package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ExportKV erzeugt das key=value-Austauschformat. Reihenfolge und Schlüssel
// des ESP bleiben gleich; neue Schlüssel (LTE, uplink, isolate, wg.dns ...)
// stehen dahinter. Die ESP-Firmware ignoriert unbekannte Schlüssel.
func ExportKV(c *Config, sn string) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	p("schema=%d", c.Schema)
	p("version=%d", c.Version)
	p("systemname=%s", c.SystemName)
	p("captive_auto=%d", b2i(c.CaptiveAuto))
	p("sn=%s", sn)
	p("wlan.count=%d", len(c.WLANs))
	for i, w := range c.WLANs {
		p("wlan.%d.ssid=%s", i, w.SSID)
		p("wlan.%d.pass=%s", i, w.Pass)
	}
	p("wg.priv=%s", c.WG.Priv)
	p("wg.pub=%s", c.WG.Pub)
	p("wg.psk=%s", c.WG.PSK)
	p("wg.endpoint=%s", c.WG.Endpoint)
	p("wg.port=%d", c.WG.Port)
	p("wg.addr=%s", c.WG.Addr)
	p("wg.mask=%s", c.WG.Mask)
	p("wg.keepalive=%d", c.WG.Keepalive)
	p("wg.fulltunnel=%d", b2i(c.WG.FullTunnel))
	p("wg.killswitch=%d", b2i(c.WG.KillSwitch))
	p("wg.vpn_off=%d", b2i(c.WG.VPNOff))
	p("admin.user=%s", c.Admin.User)
	p("admin.phash=%s", c.Admin.PHash)
	p("res.count=%d", len(c.Resources))
	for i, r := range c.Resources {
		p("res.%d.name=%s", i, r.Name)
		p("res.%d.type=%s", i, r.Type)
		p("res.%d.addr=%s", i, r.Addr)
		p("res.%d.port=%d", i, r.Port)
	}
	// --- OpenStick-Erweiterungen ---
	p("uplink.mode=%s", c.Uplink)
	p("net.isolate=%d", b2i(c.Isolate))
	p("wg.dns=%s", c.WG.DNS)
	p("wg.allowed=%s", c.WG.Allowed)
	p("wg.mtu=%d", c.WG.MTU)
	p("lte.enabled=%d", b2i(c.LTE.Enabled))
	p("lte.apn=%s", c.LTE.APN)
	p("lte.user=%s", c.LTE.User)
	p("lte.pass=%s", c.LTE.Pass)
	p("lte.pin=%s", c.LTE.PIN)
	p("lte.roaming=%d", b2i(c.LTE.Roaming))
	z := c.Zabbix
	p("zbx.enabled=%d", b2i(z.Enabled))
	p("zbx.server=%s", z.Server)
	p("zbx.proxy=%s", z.ProxyName)
	p("zbx.host=%s", z.HostName)
	p("zbx.psk_identity=%s", z.PSKIdentity)
	p("zbx.psk=%s", z.PSK)
	p("zbx.mon.pc=%d", b2i(z.MonPC))
	p("zbx.mon.pcagent=%d", b2i(z.PCAgent))
	p("zbx.mon.wlan=%d", b2i(z.MonWLAN))
	p("zbx.mon.lte=%d", b2i(z.MonLTE))
	p("zbx.mon.vpn=%d", b2i(z.MonVPN))
	p("zbx.target.count=%d", len(z.Targets))
	for i, t := range z.Targets {
		p("zbx.target.%d.name=%s", i, t.Name)
		p("zbx.target.%d.addr=%s", i, t.Addr)
	}
	p("upd.auto=%d", b2i(c.Update.Auto))
	p("upd.lte=%d", b2i(c.Update.AllowLTE))
	p("upd.reboot=%d", b2i(c.Update.AutoReboot))
	p("upd.github=%d", b2i(c.Update.GitHub))
	p("vpn.kind=%s", c.VPNKind())
	p("nb.management=%s", c.NetBird.Management)
	p("nb.setup_key=%s", c.NetBird.SetupKey)
	p("nb.hostname=%s", c.NetBird.Hostname)
	p("nb.killswitch=%d", b2i(c.NetBird.KillSwitch))
	p("ts.login_server=%s", c.Tailscale.LoginServer)
	p("ts.auth_key=%s", c.Tailscale.AuthKey)
	p("ts.hostname=%s", c.Tailscale.Hostname)
	p("ts.exit_node=%s", c.Tailscale.ExitNode)
	p("ts.killswitch=%d", b2i(c.Tailscale.KillSwitch))
	// Freigaben: access.<dienst>=<netz>,<netz> (leer = nirgends)
	for _, svc := range AccessServices {
		p("access.%s=%s", svc, strings.Join(c.Access[svc], ","))
	}
	return b.String()
}

// ImportKV spielt key=value-Text in c ein (Merge-Semantik wie ESP ab e0ea71d):
// fehlt ein Bereich (WLAN/Ressourcen) komplett, bleibt er unverändert; sobald
// ein wlan.*/res.*-Schlüssel auftaucht, wird der Bereich ersetzt. Alle anderen
// Felder werden einzeln übernommen. Die Seriennummer (sn) wird ignoriert.
func ImportKV(c *Config, text string) error {
	var (
		hasWLAN, hasRes, hasTgt bool
		wlans                   = map[int]*WLAN{}
		res                     = map[int]*Resource{}
		tgts                    = map[int]*ZbxTarget{}
		known                   int
	)
	atoi := func(v string) int { n, _ := strconv.Atoi(strings.TrimSpace(v)); return n }
	on := func(v string) bool { return atoi(v) != 0 }

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		known++
		switch k {
		case "schema", "sn":
		case "version":
			c.Version = atoi(v)
		case "systemname":
			c.SystemName = v
		case "captive_auto":
			c.CaptiveAuto = on(v)
		case "wlan.count":
			hasWLAN = true
		case "wg.priv":
			c.WG.Priv = v
		case "wg.pub":
			c.WG.Pub = v
		case "wg.psk":
			c.WG.PSK = v
		case "wg.endpoint":
			c.WG.Endpoint = v
		case "wg.port":
			c.WG.Port = atoi(v)
		case "wg.addr":
			c.WG.Addr = v
		case "wg.mask":
			c.WG.Mask = v
		case "wg.keepalive":
			c.WG.Keepalive = atoi(v)
		case "wg.fulltunnel":
			c.WG.FullTunnel = on(v)
		case "wg.killswitch":
			c.WG.KillSwitch = on(v)
		case "wg.vpn_off":
			c.WG.VPNOff = on(v)
		case "wg.dns":
			c.WG.DNS = v
		case "wg.allowed":
			c.WG.Allowed = v
		case "wg.mtu":
			c.WG.MTU = atoi(v)
		case "admin.user":
			c.Admin.User = v
		case "admin.phash":
			c.Admin.PHash = v
		case "res.count":
			hasRes = true
		case "uplink.mode":
			c.Uplink = v
		case "net.isolate":
			c.Isolate = on(v)
		case "lte.enabled":
			c.LTE.Enabled = on(v)
		case "lte.apn":
			c.LTE.APN = v
		case "lte.user":
			c.LTE.User = v
		case "lte.pass":
			c.LTE.Pass = v
		case "lte.pin":
			c.LTE.PIN = v
		case "lte.roaming":
			c.LTE.Roaming = on(v)
		case "zbx.enabled":
			c.Zabbix.Enabled = on(v)
		case "zbx.server":
			c.Zabbix.Server = v
		case "zbx.proxy":
			c.Zabbix.ProxyName = v
		case "zbx.host":
			c.Zabbix.HostName = v
		case "zbx.psk_identity":
			c.Zabbix.PSKIdentity = v
		case "zbx.psk":
			c.Zabbix.PSK = v
		case "zbx.mon.pc":
			c.Zabbix.MonPC = on(v)
		case "zbx.mon.pcagent":
			c.Zabbix.PCAgent = on(v)
		case "zbx.mon.wlan":
			c.Zabbix.MonWLAN = on(v)
		case "zbx.mon.lte":
			c.Zabbix.MonLTE = on(v)
		case "zbx.mon.vpn":
			c.Zabbix.MonVPN = on(v)
		case "zbx.target.count":
			hasTgt = true
		case "upd.auto":
			c.Update.Auto = on(v)
		case "upd.lte":
			c.Update.AllowLTE = on(v)
		case "upd.reboot":
			c.Update.AutoReboot = on(v)
		case "upd.github":
			c.Update.GitHub = on(v)
		case "vpn.kind":
			c.VPN = v
		case "nb.management":
			c.NetBird.Management = v
		case "nb.setup_key":
			c.NetBird.SetupKey = v
		case "nb.hostname":
			c.NetBird.Hostname = v
		case "nb.killswitch":
			c.NetBird.KillSwitch = on(v)
		case "ts.login_server":
			c.Tailscale.LoginServer = v
		case "ts.auth_key":
			c.Tailscale.AuthKey = v
		case "ts.hostname":
			c.Tailscale.Hostname = v
		case "ts.exit_node":
			c.Tailscale.ExitNode = v
		case "ts.killswitch":
			c.Tailscale.KillSwitch = on(v)
		default:
			if svc, ok := strings.CutPrefix(k, "access."); ok && isService(svc) {
				if c.Access == nil {
					c.Access = DefaultAccess()
				}
				c.Access[svc] = parseNets(v) // je Dienst ersetzen, fehlende Dienste bleiben
			} else if idx, field, ok := indexed(k, "wlan."); ok {
				hasWLAN = true
				if idx < MaxWLAN {
					w := wlans[idx]
					if w == nil {
						w = &WLAN{}
						wlans[idx] = w
					}
					switch field {
					case "ssid":
						w.SSID = v
					case "pass":
						w.Pass = v
					}
				}
			} else if idx, field, ok := indexed(k, "res."); ok {
				hasRes = true
				if idx < MaxRes {
					r := res[idx]
					if r == nil {
						r = &Resource{}
						res[idx] = r
					}
					switch field {
					case "name":
						r.Name = v
					case "type":
						r.Type = v
					case "addr":
						r.Addr = v
					case "port":
						r.Port = atoi(v)
					}
				}
			} else if idx, field, ok := indexed(k, "zbx.target."); ok {
				hasTgt = true
				if idx < MaxZbxTargets {
					t := tgts[idx]
					if t == nil {
						t = &ZbxTarget{}
						tgts[idx] = t
					}
					switch field {
					case "name":
						t.Name = v
					case "addr":
						t.Addr = v
					}
				}
			} else {
				known-- // unbekannter Schlüssel
			}
		}
	}
	if known == 0 {
		return fmt.Errorf("keine gültigen Konfig-Einträge gefunden")
	}
	if hasWLAN {
		c.WLANs = nil
		for _, i := range sortedKeys(wlans) {
			if wlans[i].SSID != "" {
				c.WLANs = append(c.WLANs, *wlans[i])
			}
		}
	}
	if hasRes {
		c.Resources = nil
		for _, i := range sortedKeys(res) {
			if res[i].Name != "" {
				c.Resources = append(c.Resources, *res[i])
			}
		}
	}
	if hasTgt {
		c.Zabbix.Targets = nil
		for _, i := range sortedKeys(tgts) {
			if tgts[i].Addr != "" {
				c.Zabbix.Targets = append(c.Zabbix.Targets, *tgts[i])
			}
		}
	}
	c.Normalize()
	return nil
}

// indexed zerlegt "wlan.3.ssid" -> 3, "ssid".
func indexed(k, prefix string) (int, string, bool) {
	if !strings.HasPrefix(k, prefix) {
		return 0, "", false
	}
	num, field, ok := strings.Cut(k[len(prefix):], ".")
	if !ok {
		return 0, "", false
	}
	i, err := strconv.Atoi(num)
	if err != nil || i < 0 {
		return 0, "", false
	}
	return i, field, true
}

func sortedKeys[T any](m map[int]T) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}

// VersionOf liest nur "version=" aus key=value-Text (-1 wenn nicht vorhanden).
func VersionOf(text string) int {
	for _, line := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "version="); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err == nil {
				return n
			}
		}
	}
	return -1
}
