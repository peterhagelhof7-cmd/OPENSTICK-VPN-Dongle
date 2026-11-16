package render

import (
	"strings"
	"testing"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

func testCfg() *config.Config {
	c := config.Default()
	c.WG = config.WireGuard{Priv: "PRIV", Pub: "PUB", PSK: "PSK", Endpoint: "vpn.example.org", Port: 51820,
		Addr: "10.10.0.206", Mask: "255.255.255.0", Keepalive: 25, DNS: "1.1.1.1", MTU: 1380,
		Allowed: "10.20.0.0/16"}
	c.WLANs = []config.WLAN{{SSID: "Büro", Pass: "geheim"}, {SSID: " Offen"}}
	c.LTE = config.LTE{Enabled: true, APN: "internet.telekom"}
	return c
}

func mustContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("fehlt %q in:\n%s", p, s)
		}
	}
}

func mustNotContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(s, p) {
			t.Errorf("unerwartet %q in:\n%s", p, s)
		}
	}
}

func TestWGPrefix(t *testing.T) {
	p, err := WGPrefix("10.10.0.206", "255.255.255.0")
	if err != nil || p.String() != "10.10.0.206/24" {
		t.Fatal(p, err)
	}
	if _, err := WGPrefix("10.0.0.1", "255.0.255.0"); err == nil {
		t.Fatal("nicht zusammenhängende Maske akzeptiert")
	}
}

func TestDnsmasq(t *testing.T) {
	n, c := DefaultNet(), testCfg()

	c.WG.FullTunnel = true
	out := Dnsmasq(n, c, State{VPNOn: true})
	mustContain(t, out, "dhcp-range=192.168.226.2,192.168.226.6,255.255.255.248,10m",
		"classless-static-route,0.0.0.0/1,192.168.226.1,128.0.0.0/1,192.168.226.1", "server=1.1.1.1", "no-resolv")

	c.WG.FullTunnel = false
	out = Dnsmasq(n, c, State{VPNOn: true})
	mustContain(t, out, "10.10.0.0/24,192.168.226.1,10.20.0.0/16,192.168.226.1,0.0.0.0/0,192.168.226.1", "resolv-file=")

	out = Dnsmasq(n, c, State{Locked: true, VPNOn: true})
	mustContain(t, out, "address=/#/192.168.226.1")
	mustNotContain(t, out, "classless-static-route", "resolv-file")

	out = Dnsmasq(n, c, State{})
	mustNotContain(t, out, "classless-static-route")
}

func TestNft(t *testing.T) {
	n, c := DefaultNet(), testCfg()
	c.WG.KillSwitch = true
	out := Nft(n, c, State{Locked: true})
	mustContain(t, out, "delete table inet vpndongle", "policy drop", "# Captive-Portal gesperrt",
		`oifname != "wg0" drop`, "masquerade", "ip saddr 192.168.226.0/29")
	c.WG.KillSwitch, c.Isolate = false, false
	out = Nft(n, c, State{})
	mustNotContain(t, out, "Captive-Portal gesperrt", "Kill-Switch", "LAN-Isolation")
	mustContain(t, out, `iifname "lo" accept`, "tcp dport 10050 drop", "    tcp dport 10051 drop")
	c.Zabbix.Enabled, c.Zabbix.PCAgent = true, true
	mustContain(t, Nft(n, c, State{}), `iifname != "usb0" tcp dport 10051 drop`)
	// Standard-Freigaben = Verhalten vor der Haken-Tabelle
	mustContain(t, out,
		`iifname "usb0" tcp dport 22 accept`, `iifname "wg0" tcp dport 22 accept`, "    tcp dport 22 drop",
		`iifname "usb0" tcp dport { 80, 443 } accept`, "    tcp dport { 80, 443 } drop",
		`iifname "wlan0" icmp type echo-request accept`, `iifname "wwan*" icmp type echo-request accept`,
		`iifname "wwan*" icmpv6 type echo-request accept`, `iifname != "usb0" udp dport { 53, 67 } drop`)
	mustNotContain(t, out, `iifname "wg0" tcp dport { 80, 443 } accept`, `iifname "wlan0" tcp dport 22 accept`)
	fb := Nft(n, c, State{SSHFallback: true})
	mustContain(t, fb, "SSH-Notfallmodus")
	mustNotContain(t, fb, "tcp dport 22")
}

func TestNftAccessTable(t *testing.T) {
	n, c := DefaultNet(), testCfg()
	c.Access = config.Access{config.SvcWeb: {"usb", "vpn"}, config.SvcSSH: {}, config.SvcPing: {"usb"}}
	out := Nft(n, c, State{})
	mustContain(t, out,
		`iifname "wg0" tcp dport { 80, 443 } accept`, "# ssh: nirgends", "    tcp dport 22 drop",
		`iifname "usb0" icmp type echo-request accept`, "    icmp type echo-request drop")
	mustNotContain(t, out, `tcp dport 22 accept`, `iifname "wlan0" icmp`, `iifname "wwan*"`)
	// Reihenfolge: erst die Freigaben, dann das Verwerfen
	if strings.Index(out, `iifname "wg0" tcp dport { 80, 443 } accept`) > strings.Index(out, "    tcp dport { 80, 443 } drop") {
		t.Fatal("drop vor accept")
	}
	// ohne Access-Eintrag (alte Konfig) gelten die Standards
	c.Access = nil
	mustContain(t, Nft(n, c, State{}), `iifname "wg0" tcp dport 22 accept`)
}

func TestZabbixConfs(t *testing.T) {
	c := testCfg()
	c.Zabbix = config.Zabbix{Enabled: true, Server: "10.10.0.1"}
	px := ZabbixProxy(c, "AABB")
	mustContain(t, px, "ProxyMode=0", "Server=10.10.0.1", "Hostname=vpndongle-AABB-proxy",
		"ProxyBufferMode=hybrid", "StartPreprocessors=2", "DBName=/var/lib/zabbix/zabbix_proxy.db")
	mustNotContain(t, px, "TLSConnect")
	c.Zabbix.PSKIdentity, c.Zabbix.PSK = "dongle", "0123456789abcdef0123456789abcdef"
	mustContain(t, ZabbixProxy(c, "AABB"), "TLSConnect=psk", "TLSPSKIdentity=dongle", "TLSPSKFile="+ZbxPSKFile)
	ag := ZabbixAgent(c, "AABB")
	mustContain(t, ag, "StartAgents=0", "ServerActive=127.0.0.1:10051", "Hostname=vpndongle-AABB",
		"HostMetadata=openstick-vpn-dongle", "UserParameter=vpndongle.discovery[*]")
	c.Update.AllowLTE, c.Update.GitHub = true, true
	mustContain(t, UpdateEnv(c), "UPDATE_ALLOW_LTE=1", "UPDATE_REBOOT=0", "UPDATE_GITHUB=1")
	c.Update.GitHub = false
	mustContain(t, UpdateEnv(c), "UPDATE_GITHUB=0")
}

func TestWGQuick(t *testing.T) {
	c := testCfg()
	c.WG.FullTunnel = true
	out, err := WGQuick(c)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, out, "Address = 10.10.0.206/24", "AllowedIPs = 0.0.0.0/0", "PresharedKey = PSK",
		"Endpoint = vpn.example.org:51820", "PersistentKeepalive = 25", "MTU = 1380")
	mustNotContain(t, out, "DNS")
	c.WG.FullTunnel = false
	out, _ = WGQuick(c)
	mustContain(t, out, "AllowedIPs = 10.10.0.0/24, 10.20.0.0/16")
	c.WG.Priv = "x\n[Peer]"
	if _, err := WGQuick(c); err == nil {
		t.Fatal("Zeilenumbruch-Injektion akzeptiert")
	}
}

func TestNMKeyfiles(t *testing.T) {
	n, c := DefaultNet(), testCfg()
	files := NMKeyfiles(n, c)
	if len(files) != 3 {
		t.Fatalf("erwartet 3 Profile, bekam %d", len(files))
	}
	mustContain(t, files[0].Content, "ssid=Büro", "psk=geheim", "autoconnect-priority=100", "autoconnect=true", "route-metric=100")
	mustContain(t, files[1].Content, `ssid=\sOffen`, "autoconnect-priority=99")
	mustNotContain(t, files[1].Content, "wifi-security")
	mustContain(t, files[2].Content, "type=gsm", "apn=internet.telekom", "autoconnect=false", "home-only=true", "route-metric=700")

	c.Uplink = config.UplinkLTE
	files = NMKeyfiles(n, c)
	mustContain(t, files[0].Content, "autoconnect=false")
	mustContain(t, files[2].Content, "autoconnect=true")

	c.LTE.Enabled = false
	if len(NMKeyfiles(n, c)) != 2 {
		t.Fatal("LTE-Profil trotz deaktiviert")
	}
	if uuidFor("a") != uuidFor("a") || uuidFor("a") == uuidFor("b") {
		t.Fatal("UUID nicht stabil/eindeutig")
	}
}
