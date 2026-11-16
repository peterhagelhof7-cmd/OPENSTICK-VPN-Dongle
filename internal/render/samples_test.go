package render

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

// TestWriteSamples schreibt alle Szenarien als Dateien, wenn RENDER_SAMPLES
// gesetzt ist – für die Prüfung mit echten Tools (tools/check-render.sh:
// nft -c, dnsmasq --test, wg setconf).
func TestWriteSamples(t *testing.T) {
	dir := os.Getenv("RENDER_SAMPLES")
	if dir == "" {
		t.Skip("RENDER_SAMPLES nicht gesetzt")
	}
	n := DefaultNet()
	// gültige WireGuard-Schlüssel (beliebige 32 Byte, base64)
	const key = "YAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	base := func() *config.Config {
		c := config.Default()
		c.WG = config.WireGuard{Priv: key, Pub: key, PSK: key, Endpoint: "198.51.100.7", Port: 51820,
			Addr: "10.10.0.206", Mask: "255.255.255.0", Keepalive: 25, DNS: "1.1.1.1, 9.9.9.9", MTU: 1380,
			Allowed: "10.20.0.0/16, 172.16.5.0/24"}
		c.WLANs = []config.WLAN{{SSID: "Büro", Pass: "geheimgeheim"}, {SSID: " Gast;\\x"}}
		c.LTE = config.LTE{Enabled: true, APN: "internet.telekom", PIN: "1234", User: "u", Pass: "p"}
		c.Zabbix = config.Zabbix{Enabled: true, Server: "10.10.0.10", PSKIdentity: "dongle",
			PSK: "0123456789abcdef0123456789abcdef", PCAgent: true}
		return c
	}
	type scen struct {
		name string
		mod  func(c *config.Config)
		st   State
	}
	scens := []scen{
		{"gesperrt", func(c *config.Config) {}, State{Locked: true}},
		{"frei-ohne-vpn", func(c *config.Config) { c.Isolate = false }, State{}},
		{"volltunnel-ks", func(c *config.Config) { c.WG.FullTunnel, c.WG.KillSwitch = true, true }, State{VPNOn: true}},
		{"split-isolate", func(c *config.Config) {}, State{VPNOn: true}},
		{"gesperrt-ks", func(c *config.Config) { c.WG.KillSwitch = true }, State{Locked: true, VPNOn: true}},
		{"ssh-notfall", func(c *config.Config) {}, State{SSHFallback: true}},
		{"zugriff-alles", func(c *config.Config) {
			all := []string{config.NetUSB, config.NetVPN, config.NetWLAN, config.NetLTE}
			c.Access = config.Access{config.SvcWeb: all, config.SvcSSH: all, config.SvcPing: all}
		}, State{VPNOn: true}},
		{"netbird-split-ks", func(c *config.Config) {
			c.VPN, c.NetBird = config.VPNNetBird, config.NetBird{KillSwitch: true}
		}, State{Ov: true, OvIface: "wt0", OvResolver: NBResolverDNS, VPNOn: true, OvNet: netip.MustParsePrefix("100.92.0.0/16"),
			OvRoutes: []netip.Prefix{netip.MustParsePrefix("10.10.0.0/24")}, OvDomain: "netbird.selfhosted"}},
		{"netbird-exit", func(c *config.Config) {
			c.VPN = config.VPNNetBird
		}, State{Ov: true, OvIface: "wt0", OvResolver: NBResolverDNS, VPNOn: true, OvExit: true, OvNet: netip.MustParsePrefix("100.92.0.0/16"), OvDomain: "netbird.selfhosted"}},
		{"tailscale-exit-ks", func(c *config.Config) {
			c.VPN, c.Tailscale = config.VPNTailscale, config.Tailscale{KillSwitch: true, ExitNode: "100.80.1.2"}
		}, State{Ov: true, OvIface: "tailscale0", OvResolver: TSResolverDNS, VPNOn: true, OvExit: true,
			OvNet: netip.MustParsePrefix("100.64.0.0/10"), OvDomain: "tail1234.ts.net"}},
		{"tailscale-split", func(c *config.Config) { c.VPN = config.VPNTailscale }, State{Ov: true, OvIface: "tailscale0",
			OvResolver: TSResolverDNS, VPNOn: true, OvNet: netip.MustParsePrefix("100.64.0.0/10"),
			OvRoutes: []netip.Prefix{netip.MustParsePrefix("192.168.50.0/24")}, OvDomain: "tail1234.ts.net"}},
		{"zugriff-nichts", func(c *config.Config) {
			c.Access = config.Access{config.SvcWeb: {config.NetUSB}, config.SvcSSH: {}, config.SvcPing: {}}
		}, State{}},
	}
	for _, s := range scens {
		c := base()
		s.mod(c)
		d := filepath.Join(dir, s.name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		wg, err := WGQuick(c)
		if err != nil {
			t.Fatal(err)
		}
		files := map[string]string{
			"dnsmasq.conf":       Dnsmasq(n, c, s.st),
			"rules.nft":          Nft(n, c, s.st),
			"wg0.conf":           wg,
			"zabbix_proxy.conf":  ZabbixProxy(c, "AABBCCDDEEFF"),
			"zabbix_agentd.conf": ZabbixAgent(c, "AABBCCDDEEFF"),
		}
		for _, f := range NMKeyfiles(n, c) {
			files[f.Name+".nmconnection"] = f.Content
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
