package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCryptoRoundTrip(t *testing.T) {
	for _, plain := range []string{"", "a", "schema=1\nversion=7\nsystemname=Prüfstück\n", strings.Repeat("x", 16)} {
		enc, err := Encrypt(plain, "Test1234Secret5678AB")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(enc, "BHVPN1.") {
			t.Fatalf("Präfix fehlt: %q", enc)
		}
		got, err := Decrypt(enc, "Test1234Secret5678AB")
		if err != nil || got != plain {
			t.Fatalf("Round-Trip: %q, %v", got, err)
		}
		if _, err := Decrypt(enc, "falsch"); err != ErrBadMAC {
			t.Fatalf("falsches Secret erwartet ErrBadMAC, bekam %v", err)
		}
	}
}

// Gegenprobe zu tools/admin.ps1: Datei aus testdata wurde mit PowerShell
// erzeugt (siehe testdata/README). Fehlt sie, wird übersprungen.
func TestDecryptAdminPS1(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "admin-ps1.bhvpn"))
	if err != nil {
		t.Skip("testdata/admin-ps1.bhvpn fehlt")
	}
	plain, err := Decrypt(string(b), "Test1234Secret5678AB")
	if err != nil {
		t.Fatal(err)
	}
	if VersionOf(plain) != 42 || !strings.Contains(plain, "systemname=Gegenprobe Größe") {
		t.Fatalf("unerwarteter Klartext: %q", plain)
	}
}

func TestKVRoundTrip(t *testing.T) {
	c := Default()
	c.Version = 3
	c.WLANs = []WLAN{{"Büro", "geheim=1"}, {"Hotspot", ""}}
	c.Resources = []Resource{{"Server", "rdp", "10.10.0.5", 3389}}
	c.WG = WireGuard{Priv: "cHJpdg==", Pub: "cHVi", Endpoint: "vpn.example.org", Port: 51820,
		Addr: "10.10.0.206", Mask: "255.255.255.0", Keepalive: 25, FullTunnel: true, KillSwitch: true,
		DNS: "9.9.9.9", MTU: 1360}
	c.LTE = LTE{Enabled: true, APN: "internet.telekom", PIN: "1234", SMS: true, Number: "+49 151 2345678"}
	c.Uplink = UplinkLTE
	c.Zabbix = Zabbix{Enabled: true, Server: "10.10.0.1:10051", ProxyName: "p1", PSKIdentity: "id", PSK: "abcd",
		MonPC: false, PCAgent: true, MonWLAN: true, Targets: []ZbxTarget{{Name: "Drucker", Addr: "192.168.1.20"}}}
	c.Update = Update{Auto: true, AutoReboot: true}

	n := Default()
	if err := ImportKV(n, ExportKV(c, "AABBCCDDEEFF")); err != nil {
		t.Fatal(err)
	}
	if ExportKV(n, "x") != ExportKV(c, "x") {
		t.Fatalf("Round-Trip verschieden:\n%s\n---\n%s", ExportKV(n, "x"), ExportKV(c, "x"))
	}
}

func TestImportMergeKeepsMissingSections(t *testing.T) {
	c := Default()
	c.WLANs = []WLAN{{"Büro", "pw"}}
	c.Resources = []Resource{{"A", "link", "x", 80}}
	if err := ImportKV(c, "version=9\nwg.endpoint=neu.example.org\n"); err != nil {
		t.Fatal(err)
	}
	if len(c.WLANs) != 1 || len(c.Resources) != 1 || c.Version != 9 || c.WG.Endpoint != "neu.example.org" {
		t.Fatalf("Merge falsch: %+v", c)
	}
	if err := ImportKV(c, "wlan.count=0\n"); err != nil {
		t.Fatal(err)
	}
	if len(c.WLANs) != 0 || len(c.Resources) != 1 {
		t.Fatalf("wlan.count=0 sollte nur WLANs leeren: %+v", c)
	}
	c.Zabbix.Targets = []ZbxTarget{{Name: "A", Addr: "1.2.3.4"}}
	if err := ImportKV(c, "zbx.enabled=1\n"); err != nil || len(c.Zabbix.Targets) != 1 || !c.Zabbix.Enabled {
		t.Fatalf("Ziele ohne zbx.target-Schlüssel müssen bleiben: %+v %v", c.Zabbix, err)
	}
}

func TestZabbixNames(t *testing.T) {
	c := Default()
	if p, h := c.ZabbixNames("AABB"); p != "vpndongle-AABB-proxy" || h != "vpndongle-AABB" {
		t.Fatal(p, h)
	}
}

func TestImportESPExport(t *testing.T) {
	// So exportiert die ESP-Firmware v1.0.1 (ohne OpenStick-Schlüssel).
	esp := "schema=1\nversion=5\nsystemname=Dongle\ncaptive_auto=1\nsn=1A2B3C4D5E6F\nwlan.count=1\n" +
		"wlan.0.ssid=Büro\nwlan.0.pass=x\nwg.priv=a\nwg.pub=b\nwg.psk=\nwg.endpoint=e\nwg.port=51820\n" +
		"wg.addr=10.0.0.2\nwg.mask=255.255.255.0\nwg.keepalive=25\nwg.fulltunnel=1\nwg.killswitch=0\n" +
		"wg.vpn_off=0\nadmin.user=admin\nadmin.phash=abc\nres.count=0\n"
	c := Default()
	if err := ImportKV(c, esp); err != nil {
		t.Fatal(err)
	}
	if c.Version != 5 || c.WLANs[0].SSID != "Büro" || !c.WG.FullTunnel || c.Uplink != UplinkAuto || c.WG.MTU != 1380 {
		t.Fatalf("ESP-Import falsch: %+v", c)
	}
}

func TestStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(func(c *Config) error { c.SystemName = "X"; return nil }); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(p)
	if err != nil || s2.Get().SystemName != "X" {
		t.Fatalf("Persistenz: %v %+v", err, s2.Get())
	}
}
