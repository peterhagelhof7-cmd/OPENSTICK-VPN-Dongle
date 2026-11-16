package web

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/app"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

func setup(t *testing.T) (*app.App, http.Handler) {
	root := t.TempDir()
	st, err := config.Open(filepath.Join(root, "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(st, system.Dry{Root: root}, "test")
	_ = st
	return a, New(a).Handler()
}

func do(h http.Handler, method, target string, form url.Values, auth bool) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "http://192.168.226.1"+target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if auth {
		r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:admin")))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPagesRender(t *testing.T) {
	a, h := setup(t)
	_ = a.Update(func(c *config.Config) error {
		c.WLANs = []config.WLAN{{SSID: "Büro", Pass: "geheimgeheim"}, {SSID: "Gast"}}
		c.Resources = []config.Resource{{Name: "Srv", Type: "rdp", Addr: "10.0.0.5"}, {Name: "Web", Type: "link", Addr: "10.0.0.6"},
			{Name: "Shell", Type: "ssh", Addr: "10.0.0.7", Port: 22}}
		return nil
	})
	w := do(h, "GET", "/", nil, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "WLAN verbinden") || strings.Contains(w.Body.String(), "ZgotmplZ") {
		t.Fatalf("Index %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `href="http://10.0.0.6"`) || !strings.Contains(w.Body.String(), `href="ssh://10.0.0.7:22"`) {
		t.Error("Links falsch gerendert")
	}
	if w := do(h, "GET", "/settings", nil, false); w.Code != 401 {
		t.Fatalf("ohne Login erwartet 401, bekam %d", w.Code)
	}
	w = do(h, "GET", "/settings", nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Standard-Passwort") || !strings.Contains(w.Body.String(), "LTE / Mobilfunk") {
		t.Fatalf("Settings %d: %s", w.Code, w.Body.String())
	}
	w = do(h, "GET", "/rdp?i=0", nil, false)
	if !strings.Contains(w.Body.String(), "full address:s:10.0.0.5:3389") {
		t.Fatalf("RDP: %s", w.Body.String())
	}
}

func TestCaptiveRedirect(t *testing.T) {
	a, h := setup(t)
	_ = a.Apply() // vor erster Messung gesperrt
	r := httptest.NewRequest("GET", "http://connectivitycheck.gstatic.com/generate_204", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 302 || w.Header().Get("Location") != "http://192.168.226.1/" {
		t.Fatalf("Captive: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestWifiAddAndValidation(t *testing.T) {
	a, h := setup(t)
	w := do(h, "POST", "/wifi", url.Values{"ssid": {"Büro"}, "pass": {"kurz"}}, false)
	if !strings.Contains(w.Header().Get("Location"), "Fehler") {
		t.Fatal("zu kurzes Passwort akzeptiert")
	}
	do(h, "POST", "/wifi", url.Values{"ssid": {"Büro"}, "pass": {"langgenug"}}, false)
	do(h, "POST", "/wifi", url.Values{"ssid": {"Büro"}, "pass": {"neuespasswort"}}, false)
	c := a.Store.Get()
	if len(c.WLANs) != 1 || c.WLANs[0].Pass != "neuespasswort" {
		t.Fatalf("%+v", c.WLANs)
	}
}

func TestCSRF(t *testing.T) {
	_, h := setup(t)
	r := httptest.NewRequest("POST", "http://192.168.226.1/settings/vpn", strings.NewReader("state=off"))
	r.Header.Set("Origin", "http://boese.example")
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:admin")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("fremde Herkunft erwartet 403, bekam %d", w.Code)
	}
}

func TestWGFormKeepsSecrets(t *testing.T) {
	a, h := setup(t)
	key := "YAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	f := url.Values{"priv": {key}, "pub": {key}, "endpoint": {"vpn.example.org"}, "port": {"51820"},
		"addr": {"10.0.0.2"}, "mask": {"255.255.255.0"}, "keepalive": {"25"}, "fulltunnel": {"1"}, "mtu": {"1380"}}
	w := do(h, "POST", "/settings/wg", f, true)
	if strings.Contains(w.Header().Get("Location"), "Fehler") {
		t.Fatal(w.Header().Get("Location"))
	}
	f.Set("priv", "") // leer = unverändert
	f.Del("fulltunnel")
	do(h, "POST", "/settings/wg", f, true)
	c := a.Store.Get()
	if c.WG.Priv != key || c.WG.FullTunnel {
		t.Fatalf("%+v", c.WG)
	}
	f.Set("pub", "kaputt")
	if w := do(h, "POST", "/settings/wg", f, true); !strings.Contains(w.Header().Get("Location"), "Fehler") {
		t.Fatal("ungültiger Schlüssel akzeptiert")
	}
}

func TestExportImport(t *testing.T) {
	a, h := setup(t)
	_ = a.Update(func(c *config.Config) error {
		c.Secret = "Test1234Secret5678AB"
		c.SystemName = "Quelle"
		c.LTE.APN = "internet.telekom"
		return nil
	})
	w := do(h, "GET", "/settings/export", nil, true)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "BHVPN1.") {
		t.Fatalf("Export %d %s", w.Code, w.Body.String())
	}
	exported := w.Body.String()
	_ = a.Update(func(c *config.Config) error { c.SystemName = "geändert"; c.LTE.APN = ""; return nil })

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "x.bhvpn")
	io.WriteString(fw, exported)
	mw.Close()
	r := httptest.NewRequest("POST", "http://192.168.226.1/settings/import", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:admin")))
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	c := a.Store.Get()
	if c.SystemName != "Quelle" || c.LTE.APN != "internet.telekom" {
		t.Fatalf("Import: %s / %+v", rw.Header().Get("Location"), c)
	}
}

func upload(h http.Handler, name string, size int) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("vorher", "egal") // anderes Feld vor der Datei
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(bytes.Repeat([]byte("x"), size))
	mw.Close()
	r := httptest.NewRequest("POST", "http://192.168.226.1/settings/upload", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:admin")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestUploadLimits(t *testing.T) {
	oldMax, oldTotal := maxUpload, maxFilesTotal
	maxUpload, maxFilesTotal = 1000, 2500
	defer func() { maxUpload, maxFilesTotal = oldMax, oldTotal }()
	a, h := setup(t)
	dir := a.FilesDirReal()
	msg := func(w *httptest.ResponseRecorder) string {
		u, _ := url.Parse(w.Header().Get("Location"))
		return u.Query().Get("msg")
	}
	files := func() []string {
		entries, _ := os.ReadDir(dir)
		var n []string
		for _, e := range entries {
			n = append(n, e.Name())
		}
		return n
	}

	if m := msg(upload(h, "a.bin", 1000)); !strings.Contains(m, "hochgeladen") {
		t.Fatalf("Upload genau an der Grenze: %s", m)
	}
	if m := msg(upload(h, "zu-gross.bin", 1001)); !strings.Contains(m, "größer als") {
		t.Fatalf("zu große Datei: %s", m)
	}
	if m := msg(upload(h, "../../boese.bin", 500)); !strings.Contains(m, "boese.bin") {
		t.Fatalf("Pfad: %s", m)
	}
	if _, err := os.Stat(filepath.Join(dir, "boese.bin")); err != nil {
		t.Fatal("Datei nicht im Download-Ordner gelandet")
	}
	// belegt 1500 von 2500 -> 1000 frei; 900 passt, danach nur noch 100
	upload(h, "b.bin", 900)
	if m := msg(upload(h, "c.bin", 200)); !strings.Contains(m, "Kontingent") {
		t.Fatalf("Kontingent: %s", m)
	}
	// Ersetzen einer vorhandenen Datei zählt deren alte Größe nicht doppelt
	if m := msg(upload(h, "b.bin", 1000)); !strings.Contains(m, "hochgeladen") {
		t.Fatalf("Ersetzen: %s", m)
	}
	got := strings.Join(files(), ",")
	if got != "a.bin,b.bin,boese.bin" {
		t.Fatalf("Ordnerinhalt %q (Reste von .part oder abgelehnten Dateien?)", got)
	}
	if w := do(h, "GET", "/settings", nil, true); !strings.Contains(w.Body.String(), "belegt 2.4 KiB von 2.4 KiB") {
		t.Error("Kontingent-Anzeige fehlt")
	}
}

func TestZbxDiscoveryLocalOnly(t *testing.T) {
	_, h := setup(t)
	r := httptest.NewRequest("GET", "http://127.0.0.1/zabbix/discovery?group=wlan", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r) // RemoteAddr von httptest = 192.0.2.1 -> fremd
	if w.Code != 403 {
		t.Fatalf("von außen erwartet 403, bekam %d", w.Code)
	}
	r.RemoteAddr = "127.0.0.1:40000"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"{#IF}":"wlan0"`) {
		t.Fatalf("lokal: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "http://127.0.0.1/zabbix/discovery?group=lte", nil)
	r.RemoteAddr = "[::1]:40000"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("LTE ohne APN darf keine Items liefern: %s", w.Body.String())
	}
}

func TestZabbixForm(t *testing.T) {
	a, h := setup(t)
	loc := func(w *httptest.ResponseRecorder) string { return w.Header().Get("Location") }
	f := url.Values{"enabled": {"1"}, "server": {"10.10.0.10"}, "mon_wlan": {"1"}, "mon_vpn": {"1"},
		"psk_identity": {"dongle"}, "psk": {"zu-kurz"}}
	if !strings.Contains(loc(do(h, "POST", "/settings/zabbix", f, true)), "Fehler") {
		t.Fatal("ungültige PSK akzeptiert")
	}
	f.Set("psk", "0123456789abcdef0123456789abcdef")
	if l := loc(do(h, "POST", "/settings/zabbix", f, true)); strings.Contains(l, "Fehler") {
		t.Fatal(l)
	}
	z := a.Store.Get().Zabbix
	if !z.Enabled || z.MonPC || z.MonLTE || !z.MonWLAN || z.PSK == "" {
		t.Fatalf("%+v", z)
	}
	f.Set("psk", "") // leer = unverändert
	do(h, "POST", "/settings/zabbix", f, true)
	if a.Store.Get().Zabbix.PSK == "" {
		t.Fatal("PSK verloren")
	}
	do(h, "POST", "/settings/zbxtarget", url.Values{"name": {"Drucker"}, "addr": {"192.168.1.20"}}, true)
	if l := loc(do(h, "POST", "/settings/zbxtarget", url.Values{"name": {"X"}, "addr": {"a b"}}, true)); !strings.Contains(l, "Fehler") {
		t.Fatal("ungültige Zieladresse akzeptiert")
	}
	w := do(h, "GET", "/settings", nil, true)
	body := w.Body.String()
	for _, want := range []string{"Zabbix-Überwachung", "Drucker", "VPN ist nicht eingerichtet", "Systemupdate", "vpndongle-" + a.Serial() + "-proxy"} {
		if !strings.Contains(body, want) {
			t.Errorf("Einstellungsseite: %q fehlt", want)
		}
	}
}

func TestZbxTunnelWarning(t *testing.T) {
	c := config.Default()
	c.Zabbix = config.Zabbix{Enabled: true, Server: "10.20.5.5:10051"}
	c.WG = config.WireGuard{Priv: "a", Pub: "b", Endpoint: "e", Addr: "10.10.0.2", Mask: "255.255.255.0", Port: 51820}
	if !strings.Contains(zbxTunnelWarning(c), "nicht in den Tunnel-Netzen") {
		t.Fatal("Warnung fehlt")
	}
	c.WG.Allowed = "10.20.0.0/16"
	if w := zbxTunnelWarning(c); w != "" {
		t.Fatal(w)
	}
	c.WG.Allowed, c.WG.FullTunnel = "", true
	if w := zbxTunnelWarning(c); w != "" {
		t.Fatal(w)
	}
}

func sendPkg(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("pkg", name)
	fw.Write([]byte("paketinhalt"))
	mw.Close()
	r := httptest.NewRequest("POST", "http://192.168.226.1/settings/updatepkg", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:admin")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	u, _ := url.Parse(w.Header().Get("Location"))
	return u.Query().Get("msg")
}

func TestUpdatePkgUpload(t *testing.T) {
	a, h := setup(t)
	send := func(name string) string { return sendPkg(t, h, name) }
	if m := send("boese.sh"); !strings.Contains(m, "erwartet wird") {
		t.Fatalf("falscher Dateiname: %s", m)
	}
	if m := send("vpndongle-1.2.3.tar.gz"); !strings.Contains(m, "Update läuft") {
		t.Fatalf("Paket: %s", m)
	}
	b, err := os.ReadFile(filepath.Join(a.UpdateDirReal(), "vpndongle-update.tar.gz"))
	if err != nil || string(b) != "paketinhalt" {
		t.Fatalf("Paket nicht abgelegt: %v", err)
	}
}

func TestRootPassword(t *testing.T) {
	a, h := setup(t)
	_ = a.Apply()
	loc := func(w *httptest.ResponseRecorder) string {
		u, _ := url.Parse(w.Header().Get("Location"))
		return u.Query().Get("msg")
	}
	if m := loc(do(h, "POST", "/settings/rootpw", url.Values{"pw": {"kurz"}, "pw2": {"kurz"}}, true)); !strings.Contains(m, "mindestens 10") {
		t.Fatal(m)
	}
	if m := loc(do(h, "POST", "/settings/rootpw", url.Values{"pw": {"langesPasswort1"}, "pw2": {"anderes"}}, true)); !strings.Contains(m, "überein") {
		t.Fatal(m)
	}
	if w := do(h, "POST", "/settings/rootpw", url.Values{"pw": {"langesPasswort1"}, "pw2": {"langesPasswort1"}}, false); w.Code != 401 {
		t.Fatal("ohne Login möglich")
	}
	if m := loc(do(h, "POST", "/settings/rootpw", url.Values{"pw": {"langesPasswort1"}, "pw2": {"langesPasswort1"}}, true)); m != "Root-Passwort gesetzt" {
		t.Fatal(m)
	}
	c := a.Store.Get()
	if c.RootPWSet.IsZero() {
		t.Fatal("Zeitpunkt fehlt")
	}
	if strings.Contains(config.ExportKV(c, "x"), "langesPasswort1") {
		t.Fatal("Passwort im Export")
	}
	body := do(h, "GET", "/settings", nil, true).Body.String()
	if !strings.Contains(body, "SSH-Zugang") || !strings.Contains(body, "gesetzt am") || !strings.Contains(body, "SSH-Notfallmodus") {
		t.Error("SSH-Karte unvollständig (im Test fehlt usb0 -> Notfallmodus erwartet)")
	}
}

func TestStatusShowsLoad(t *testing.T) {
	_, h := setup(t)
	// vor dem ersten Tick: noch keine Mittelwerte
	body := do(h, "GET", "/", nil, false).Body.String()
	if !strings.Contains(body, "Auslastung") || !strings.Contains(body, "CPU wird gemessen") {
		t.Fatal("Auslastungszeile fehlt oder zeigt vor der ersten Messung schon Werte")
	}
	js := do(h, "GET", "/status.json", nil, false).Body.String()
	if !strings.Contains(js, `"cpu_pct":-1`) || !strings.Contains(js, `"ram_pct":-1`) {
		t.Fatalf("status.json ohne cpu_pct/ram_pct: %s", js)
	}
}

// Die Auslastung gehört zu jeder VPN-Art (war bei NetBird/Tailscale im WireGuard-Zweig verschwunden).
func TestStatusLoadForEveryVPNKind(t *testing.T) {
	for _, kind := range []string{"wireguard", "netbird", "tailscale"} {
		a, h := setup(t)
		_ = a.Update(func(c *config.Config) error { c.VPN = kind; return nil })
		for _, page := range []string{"/", "/settings"} {
			body := do(h, "GET", page, nil, true).Body.String()
			if !strings.Contains(body, "Auslastung") {
				t.Errorf("VPN-Art %s, Seite %s: Auslastung fehlt", kind, page)
			}
			// vor dem ersten Tick keine Temperatur (kein „-1 °C“)
			if strings.Contains(body, "-1 °C") {
				t.Errorf("VPN-Art %s, Seite %s: Platzhalter-Temperatur angezeigt", kind, page)
			}
		}
	}
}

func TestStatusJSONNoSecrets(t *testing.T) {
	a, h := setup(t)
	_ = a.Update(func(c *config.Config) error { c.WG.Priv = "GEHEIM"; c.LTE.PIN = "1234"; return nil })
	w := do(h, "GET", "/status.json", nil, false)
	if strings.Contains(w.Body.String(), "GEHEIM") || strings.Contains(w.Body.String(), "1234") {
		t.Fatal("Geheimnis im Status-JSON")
	}
}
