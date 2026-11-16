package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

func TestSWCheck(t *testing.T) {
	_, h := setup(t) // App-Version "test" = Hash-Build -> jedes saubere Release ist neuer
	old := swLatest
	t.Cleanup(func() { swLatest = old })

	swLatest = func(context.Context, string) (string, error) { return "v0.2.0", nil }
	if w := do(h, "GET", "/settings/swcheck", nil, false); w.Code != 401 {
		t.Fatalf("ohne Login: %d", w.Code)
	}
	var j map[string]any
	_ = json.Unmarshal(do(h, "GET", "/settings/swcheck", nil, true).Body.Bytes(), &j)
	if j["release"] != "v0.2.0" || j["neu"] != true || j["aktuell"] != "test" {
		t.Fatalf("Antwort: %v", j)
	}

	swLatest = func(context.Context, string) (string, error) { return "", errors.New("keine Verbindung") }
	j = nil
	_ = json.Unmarshal(do(h, "GET", "/settings/swcheck", nil, true).Body.Bytes(), &j)
	if j["fehler"] != "keine Verbindung" {
		t.Fatalf("Fehler nicht gemeldet: %v", j)
	}
}

func TestUpdateCfgGitHub(t *testing.T) {
	a, h := setup(t)
	if !a.Store.Get().Update.GitHub {
		t.Fatal("GitHub-Updates sollten standardmäßig an sein")
	}
	body := do(h, "GET", "/settings", nil, true).Body.String()
	if !strings.Contains(body, `name="github" checked`) || !strings.Contains(body, "Nach neuer Version suchen") {
		t.Fatal("Einstellungsseite ohne GitHub-Update-Schalter")
	}
	do(h, "POST", "/settings/updatecfg", url.Values{"auto": {"on"}}, true) // github nicht angehakt
	if c := a.Store.Get(); c.Update.GitHub || !c.Update.Auto {
		t.Fatalf("Speichern: %+v", c.Update)
	}
	if kv := config.ExportKV(a.Store.Get(), "SN"); !strings.Contains(kv, "upd.github=0") {
		t.Fatal("upd.github fehlt im Export")
	}
}
