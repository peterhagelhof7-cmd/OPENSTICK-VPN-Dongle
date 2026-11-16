package swupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		cur, tag string
		want     bool
	}{
		{"v0.1.0", "v0.1.1", true},
		{"v0.1.9", "v0.2.0", true},
		{"v0.9.9", "v1.0.0", true},
		{"v0.1.0", "v0.1.0", false},                  // gleich
		{"v0.2.0", "v0.1.9", false},                  // Downgrade
		{"v0.1.0-3-gabc1234-dirty", "v0.1.0", false}, // Entwicklungsstand nach v0.1.0
		{"v0.1.0-3-gabc1234", "v0.1.1", true},
		{"f4be0f1-dirty", "v0.1.0", true}, // Hash-Build ohne Tag
		{"v0.1.0", "v0.2.0-rc1", false},   // nur saubere Release-Tags
		{"v0.1.0", "latest", false},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.tag); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, erwartet %v", c.cur, c.tag, got, c.want)
		}
	}
}

func TestEmbeddedKey(t *testing.T) {
	if len(PublicKey()) != ed25519.PublicKeySize {
		t.Fatal("eingebauter öffentlicher Schlüssel fehlt/ungültig")
	}
}

// fakeGitHub liefert /repos/<Repo>/releases/latest und die Release-Dateien.
type fakeGitHub struct {
	tag   string
	files map[string][]byte
}

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "UA fehlt", 403)
			return
		}
		if r.URL.Path == "/repos/"+Repo+"/releases/latest" {
			type asset struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}
			var as []asset
			for n := range f.files {
				as = append(as, asset{n, srv.URL + "/dl/" + n})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "assets": as})
			return
		}
		if b, ok := f.files[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	old := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = old })
	return srv
}

func release(t *testing.T, priv ed25519.PrivateKey, tag string, pkg []byte) *fakeGitHub {
	sum := sha256.Sum256(pkg)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + PkgName(tag) + "\n" + strings.Repeat("0", 64) + "  andere.json\n")
	return &fakeGitHub{tag: tag, files: map[string][]byte{
		PkgName(tag):     pkg,
		"SHA256SUMS":     sums,
		"SHA256SUMS.sig": []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)) + "\n"),
	}}
}

func testClient(pub ed25519.PublicKey) *Client {
	return &Client{HTTP: http.DefaultClient, PublicKey: pub, UserAgent: "test"}
}

func TestFetchOK(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pkg := []byte("paket-inhalt-v0.2.0")
	release(t, priv, "v0.2.0", pkg).server(t)
	c := testClient(pub)
	r, err := c.Latest(context.Background())
	if err != nil || r.Tag != "v0.2.0" {
		t.Fatalf("Latest: %v %+v", err, r)
	}
	dest := filepath.Join(t.TempDir(), "update", "vpndongle-update.tar.gz")
	if _, err := c.Fetch(context.Background(), r, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(pkg) {
		t.Fatal("Paket nicht abgelegt")
	}
	if _, err := os.Stat(dest + ".part"); err == nil {
		t.Fatal("Temp-Datei übrig")
	}
}

func TestFetchRejects(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	cases := map[string]func(f *fakeGitHub){
		"fremder Schlüssel": func(f *fakeGitHub) {
			f.files["SHA256SUMS.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(other, f.files["SHA256SUMS"])))
		},
		"SUMS verändert": func(f *fakeGitHub) {
			f.files["SHA256SUMS"] = append([]byte("x"), f.files["SHA256SUMS"]...)
		},
		"Paket verändert": func(f *fakeGitHub) { f.files[PkgName(f.tag)] = []byte("manipuliert") },
		"Signatur fehlt":  func(f *fakeGitHub) { delete(f.files, "SHA256SUMS.sig") },
		"Signatur kaputt": func(f *fakeGitHub) { f.files["SHA256SUMS.sig"] = []byte("kein-base64!") },
		"Paket nicht in SUMS": func(f *fakeGitHub) {
			sums := []byte(strings.Repeat("a", 64) + "  vpndongle-v9.9.9.tar.gz\n")
			f.files["SHA256SUMS"] = sums
			f.files["SHA256SUMS.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sums)))
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			f := release(t, priv, "v0.2.0", []byte("echtes-paket"))
			mod(f)
			f.server(t)
			c := testClient(pub)
			r, err := c.Latest(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "pkg.tar.gz")
			if _, err := c.Fetch(context.Background(), r, dest); err == nil {
				t.Fatal("manipuliertes Release angenommen")
			}
			if _, err := os.Stat(dest); err == nil {
				t.Fatal("Paket trotz Fehler abgelegt")
			}
			if _, err := os.Stat(dest + ".part"); err == nil {
				t.Fatal("Temp-Datei übrig")
			}
		})
	}
}

func TestNoKeyNoUpdate(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	release(t, priv, "v0.2.0", []byte("p")).server(t)
	c := testClient(nil) // kein gültiger eingebauter Schlüssel
	r, _ := c.Latest(context.Background())
	if _, err := c.Fetch(context.Background(), r, filepath.Join(t.TempDir(), "p")); err == nil {
		t.Fatal("ohne Prüfschlüssel installiert")
	}
}
