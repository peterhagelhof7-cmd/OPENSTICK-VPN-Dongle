package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestPostTailscale(t *testing.T) {
	a, h := setup(t)
	post := func(f url.Values) string {
		return msgOf(do(h, "POST", "/settings/tailscale", f, true).Header().Get("Location"))
	}
	for name, f := range map[string]url.Values{
		"Server ohne Schema":    {"login_server": {"hs.example.org"}},
		"Server mit Pfad":       {"login_server": {"https://hs.example.org/admin"}},
		"Name mit Punkt":        {"hostname": {"a.b"}},
		"Key mit Sonderzeichen": {"auth_key": {"tskey auth;x"}},
		"Exit-Node mit Zeichen": {"exit_node": {"100.80.1.2; reboot"}},
	} {
		if m := post(f); !strings.HasPrefix(m, "Fehler") {
			t.Errorf("%s angenommen: %s", name, m)
		}
	}
	key := "tskey-auth-kAbC123CNTRL-xyzXYZ0123456789"
	if m := post(url.Values{"login_server": {"https://hs.example.org/"}, "auth_key": {key}, "hostname": {"Buero"},
		"exit_node": {"exit.tail1234.ts.net"}, "killswitch": {"on"}}); !strings.Contains(m, "Tailscale gespeichert") {
		t.Fatalf("gültige Eingabe abgelehnt: %s", m)
	}
	ts := a.Store.Get().Tailscale
	if ts.LoginServer != "https://hs.example.org" || ts.Hostname != "buero" || ts.ExitNode != "exit.tail1234.ts.net" || !ts.KillSwitch || ts.AuthKey != key {
		t.Fatalf("gespeichert: %+v", ts)
	}
	if body := do(h, "GET", "/settings", nil, true).Body.String(); strings.Contains(body, key) || !strings.Contains(body, "Anmeldung ausstehend") {
		t.Fatal("Auth-Key in der Seite oder Hinweis fehlt")
	}
	// leeres Key-Feld = unverändert
	post(url.Values{"login_server": {"https://hs.example.org"}})
	if a.Store.Get().Tailscale.AuthKey != key {
		t.Fatal("leeres Feld hat den Key gelöscht")
	}
}
