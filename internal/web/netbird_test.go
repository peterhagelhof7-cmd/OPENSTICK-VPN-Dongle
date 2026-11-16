package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestPostNetBird(t *testing.T) {
	a, h := setup(t)
	post := func(f url.Values) string {
		return msgOf(do(h, "POST", "/settings/netbird", f, true).Header().Get("Location"))
	}
	bad := map[string]url.Values{
		"URL ohne Schema":       {"management": {"nb.example.org"}},
		"URL mit Pfad":          {"management": {"https://nb.example.org/x"}},
		"URL mit Login":         {"management": {"https://u:p@nb.example.org"}},
		"Name mit Leerzeichen":  {"hostname": {"mein stick"}},
		"Key mit Sonderzeichen": {"setup_key": {"abc def;rm -rf"}},
	}
	for name, f := range bad {
		if m := post(f); !strings.HasPrefix(m, "Fehler") {
			t.Errorf("%s angenommen: %s", name, m)
		}
	}
	if a.Store.Get().NetBird.Management != "" {
		t.Fatal("ungültige Eingaben haben etwas gespeichert")
	}

	key := "A1B2C3D4-E5F6-7890-ABCD-EF0123456789"
	m := post(url.Values{"management": {"https://nb.example.org:443/"}, "hostname": {"Buero-Stick"},
		"setup_key": {key}, "killswitch": {"on"}})
	if !strings.Contains(m, "NetBird gespeichert") {
		t.Fatalf("gültige Eingabe abgelehnt: %s", m)
	}
	nb := a.Store.Get().NetBird
	if nb.Management != "https://nb.example.org:443" || nb.Hostname != "buero-stick" || !nb.KillSwitch || nb.SetupKey != key {
		t.Fatalf("gespeichert: %+v", nb)
	}
	// VPN-Art wählen
	if m := msgOf(do(h, "POST", "/settings/vpnkind", url.Values{"vpn": {"netbird"}}, true).Header().Get("Location")); !strings.Contains(m, "NetBird") {
		t.Fatalf("VPN-Art: %s", m)
	}
	if a.Store.Get().VPN != "netbird" {
		t.Fatal("VPN-Art nicht gespeichert")
	}
	if m := msgOf(do(h, "POST", "/settings/vpnkind", url.Values{"vpn": {"openvpn"}}, true).Header().Get("Location")); !strings.HasPrefix(m, "Fehler") {
		t.Fatalf("unbekannte VPN-Art angenommen: %s", m)
	}
	// Setup-Key wird im Dry-Run nicht „angemeldet“ – er darf trotzdem nie in der Seite stehen
	body := do(h, "GET", "/settings", nil, true).Body.String()
	if strings.Contains(body, key) {
		t.Fatal("Setup-Key in der Einstellungsseite")
	}
	if !strings.Contains(body, `<option value="netbird" selected>`) || !strings.Contains(body, "Inaktiv – als VPN-Art ist nicht WireGuard gewählt") ||
		!strings.Contains(body, "Inaktiv – als VPN-Art ist nicht Tailscale gewählt") {
		t.Fatal("VPN-Art-Auswahl/Hinweise fehlen")
	}
}
