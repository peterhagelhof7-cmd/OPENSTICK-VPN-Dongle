package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

func TestAccessTableForm(t *testing.T) {
	a, h := setup(t)
	// Seite zeigt die Tabelle; Web/USB fest, Web/VPN gesperrt solange admin/admin
	body := do(h, "GET", "/settings", nil, true).Body.String()
	for _, want := range []string{"Zugriff auf den Stick", `name="a_ssh_vpn" aria-label="a_ssh_vpn" checked`,
		`name="a_web_usb" aria-label="a_web_usb" checked disabled`, `name="a_web_vpn" aria-label="a_web_vpn"  disabled title="erst Admin-Passwort ändern"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("Einstellungsseite ohne %q", want)
		}
	}
	// admin/admin: Web über VPN wird abgelehnt
	f := url.Values{"a_web_vpn": {"on"}, "a_ssh_usb": {"on"}}
	if m := msgOf(do(h, "POST", "/settings/access", f, true).Header().Get("Location")); !strings.Contains(m, "Admin-Passworts") {
		t.Fatalf("Web über VPN mit admin/admin angenommen: %s", m)
	}
	if a.Store.Get().Access.Allows("web", "vpn") {
		t.Fatal("trotz Ablehnung gespeichert")
	}
	// Passwort ändern -> erlaubt; SSH nur USB, Ping nirgends; Web/USB bleibt an
	if err := a.Update(func(c *config.Config) error { c.Admin.PHash = config.HashPassword("langesPasswort1"); return nil }); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://192.168.226.1/settings/access", strings.NewReader(f.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("admin", "langesPasswort1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if m := msgOf(w.Header().Get("Location")); !strings.Contains(m, "Freigaben gespeichert") {
		t.Fatalf("Speichern: %s", m)
	}
	acc := a.Store.Get().Access
	if !acc.Allows("web", "usb") || !acc.Allows("web", "vpn") || acc.Allows("ssh", "vpn") || !acc.Allows("ssh", "usb") || len(acc["ping"]) != 0 {
		t.Fatalf("Freigaben falsch: %v", acc)
	}
}
