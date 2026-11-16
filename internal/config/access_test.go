package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAccessDefaults(t *testing.T) {
	c := Default()
	c.Normalize()
	want := Access{SvcWeb: {NetUSB}, SvcSSH: {NetUSB, NetVPN}, SvcPing: {NetUSB, NetVPN, NetWLAN, NetLTE}}
	if !reflect.DeepEqual(c.Access, want) {
		t.Fatalf("Standard: %v", c.Access)
	}
}

func TestAccessNormalizeRules(t *testing.T) {
	c := Default()
	c.Admin.PHash = HashPassword("eigenesPasswort")
	// Web ohne USB, unbekanntes Netz, doppelt, falsche Reihenfolge, SSH bewusst leer, Ping fehlt
	c.Access = Access{SvcWeb: {"wlan", "xyz", "vpn", "wlan"}, SvcSSH: {}, "telnet": {"usb"}}
	c.Normalize()
	if got := c.Access[SvcWeb]; !reflect.DeepEqual(got, []string{NetUSB, NetVPN, NetWLAN}) {
		t.Fatalf("Web: %v (USB muss dazu, Reihenfolge fest, Unbekanntes weg)", got)
	}
	if got := c.Access[SvcSSH]; got == nil || len(got) != 0 {
		t.Fatalf("SSH bewusst leer muss leer bleiben, nicht Standard: %v", got)
	}
	if got := c.Access[SvcPing]; len(got) != 4 {
		t.Fatalf("fehlender Dienst muss Standard bekommen: %v", got)
	}
	if _, ok := c.Access["telnet"]; ok {
		t.Fatal("unbekannter Dienst übernommen")
	}
	// mit Standard-Passwort: Web nur USB
	c.Admin.PHash = HashPassword("admin")
	c.Normalize()
	if got := c.Access[SvcWeb]; !reflect.DeepEqual(got, []string{NetUSB}) {
		t.Fatalf("admin/admin: Web muss auf USB beschränkt sein: %v", got)
	}
}

func TestAccessKVRoundTrip(t *testing.T) {
	c := Default()
	c.Admin.PHash = HashPassword("eigenesPasswort")
	c.Access = Access{SvcWeb: {NetUSB, NetVPN}, SvcSSH: {}, SvcPing: {NetUSB}}
	c.Normalize()
	kv := ExportKV(c, "SN")
	for _, l := range []string{"access.web=usb,vpn", "access.ssh=\n", "access.ping=usb"} {
		if !strings.Contains(kv, l) {
			t.Fatalf("Export ohne %q:\n%s", l, kv)
		}
	}
	d := Default()
	if err := ImportKV(d, kv); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Access, c.Access) {
		t.Fatalf("Round-Trip: %v != %v", d.Access, c.Access)
	}
	// Teil-Import: nur ssh -> web/ping bleiben
	if err := ImportKV(d, "access.ssh=usb,lte\n"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.Access[SvcSSH], []string{NetUSB, NetLTE}) || !reflect.DeepEqual(d.Access[SvcWeb], []string{NetUSB, NetVPN}) {
		t.Fatalf("Teil-Import: %v", d.Access)
	}
	// Import mit admin/admin kann Web nicht nach außen öffnen
	e := Default()
	if err := ImportKV(e, "access.web=usb,wlan\n"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.Access[SvcWeb], []string{NetUSB}) {
		t.Fatalf("Import mit Standard-Passwort öffnet Web: %v", e.Access[SvcWeb])
	}
}

func TestVPNKindKV(t *testing.T) {
	c := Default()
	if c.VPNKind() != VPNWireGuard || c.TSWanted() || c.NBWanted() {
		t.Fatal("Standard muss WireGuard sein")
	}
	c.VPN, c.Tailscale = VPNTailscale, Tailscale{LoginServer: "https://hs.example.org", ExitNode: "100.80.1.2", KillSwitch: true}
	kv := ExportKV(c, "SN")
	d := Default()
	if err := ImportKV(d, kv); err != nil {
		t.Fatal(err)
	}
	if d.VPNKind() != VPNTailscale || d.Tailscale != c.Tailscale || !d.KillSwitchOn() {
		t.Fatalf("Round-Trip: %s %+v", d.VPN, d.Tailscale)
	}
	d.WG.VPNOff = true
	if d.TSWanted() {
		t.Fatal("VPN-Schalter AUS muss auch Tailscale abschalten")
	}
	// unbekannte VPN-Art -> WireGuard
	_ = ImportKV(d, "vpn.kind=openvpn\n")
	if d.VPN != VPNWireGuard {
		t.Fatalf("unbekannte Art: %q", d.VPN)
	}
}

func TestAccessOldConfigFile(t *testing.T) {
	// config.json von vor der Haken-Tabelle (kein "access") -> Standards
	p := filepath.Join(t.TempDir(), "c.json")
	old := Default()
	old.Access = nil
	b, _ := json.Marshal(old)
	b = []byte(strings.Replace(string(b), `"access":null,`, "", 1))
	if strings.Contains(string(b), "access") {
		t.Fatal("Testdaten enthalten noch access")
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Get().Access.Allows(SvcSSH, NetVPN) || s.Get().Access.Allows(SvcWeb, NetVPN) {
		t.Fatalf("alte Konfig: %v", s.Get().Access)
	}
}
