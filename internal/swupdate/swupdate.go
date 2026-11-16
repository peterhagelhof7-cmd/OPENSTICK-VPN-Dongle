// Package swupdate holt neue vpndongled-Versionen aus den GitHub-Releases.
//
// Sicherheit:
//   - Ein Release enthält vpndongle-<tag>.tar.gz, SHA256SUMS und SHA256SUMS.sig.
//   - SHA256SUMS.sig ist eine Ed25519-Signatur über SHA256SUMS; geprüft wird mit
//     dem in vpndongled eingebauten öffentlichen Schlüssel (PublicKey). Der
//     private Schlüssel liegt nur beim Herausgeber, nie im Repo.
//   - Das Paket muss in der signierten SHA256SUMS unter genau seinem Namen
//     (mit Tag) stehen und die Prüfsumme muss stimmen.
//   - Nur NEUERE Versionen werden geholt (kein Downgrade, auch nicht, wenn
//     jemand ein altes, korrekt signiertes Release als „latest“ ausliefert).
package swupdate

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Repo: Quelle der Releases (owner/name).
const Repo = "peterhagelhof7-cmd/OPENSTICK-VPN-Dongle"

// APIBase ist im Test austauschbar.
var APIBase = "https://api.github.com"

const (
	maxPkg  = 64 << 20 // wie der Upload in der Weboberfläche
	maxSums = 64 << 10
	maxAPI  = 1 << 20
)

// Release ist das, was wir aus der GitHub-API brauchen.
type Release struct {
	Tag    string
	Assets map[string]string // Name -> Download-URL
}

// PkgName: Name des Pakets eines Releases.
func PkgName(tag string) string { return "vpndongle-" + tag + ".tar.gz" }

var reVer = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)`)

// ParseVersion liest den Kern "vX.Y.Z" aus Tags und Build-Versionen
// ("v0.2.0-3-gabc1234-dirty" -> 0.2.0, dev=true). ok=false für Hash-Builds.
func ParseVersion(s string) (v [3]int, dev, ok bool) {
	m := reVer.FindStringSubmatch(s)
	if m == nil {
		return v, false, false
	}
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, len(s) > len(m[0]), true
}

// Newer: ist das Release-Tag neuer als die laufende Version?
// Hash-Builds ohne Tag gelten als 0.0.0; Entwicklungsstände (v0.2.0-3-g…)
// zählen wie ihr Basis-Tag – ein Release mit demselben Kern ist nicht neuer.
func Newer(current, tag string) bool {
	t, tdev, ok := ParseVersion(tag)
	if !ok || tdev {
		return false // nur saubere Release-Tags
	}
	c, _, ok := ParseVersion(current)
	if !ok {
		return true
	}
	for i := 0; i < 3; i++ {
		if t[i] != c[i] {
			return t[i] > c[i]
		}
	}
	return false
}

// Client kapselt HTTP und den Prüfschlüssel (im Test austauschbar).
type Client struct {
	HTTP      *http.Client
	PublicKey ed25519.PublicKey
	UserAgent string
}

// New liefert einen Client mit eingebautem Schlüssel.
func New(version string) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 5 * time.Minute},
		PublicKey: PublicKey(),
		UserAgent: "vpndongled/" + version,
	}
}

func (c *Client) get(ctx context.Context, url string, limit int64, accept string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: zu groß (%d Bytes)", url, resp.ContentLength)
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(resp.Body, limit+1), resp.Body}, nil
}

func (c *Client) readAll(ctx context.Context, url string, limit int64) ([]byte, error) {
	rc, err := c.get(ctx, url, limit, "")
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s: zu groß", url)
	}
	return b, err
}

// Latest fragt das neueste veröffentlichte Release ab (keine Entwürfe/Vorabversionen).
func (c *Client) Latest(ctx context.Context) (Release, error) {
	rc, err := c.get(ctx, APIBase+"/repos/"+Repo+"/releases/latest", maxAPI, "application/vnd.github+json")
	if err != nil {
		return Release{}, err
	}
	defer rc.Close()
	var j struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(rc).Decode(&j); err != nil {
		return Release{}, fmt.Errorf("Antwort der GitHub-API: %w", err)
	}
	r := Release{Tag: j.Tag, Assets: map[string]string{}}
	for _, a := range j.Assets {
		r.Assets[a.Name] = a.URL
	}
	return r, nil
}

// VerifySums prüft die Signatur und liefert die erwartete Prüfsumme des Pakets.
func VerifySums(pub ed25519.PublicKey, sums, sig []byte, pkg string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return "", errors.New("Signaturdatei unlesbar")
	}
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, sums, raw) {
		return "", errors.New("Signatur ungültig – Paket wird NICHT installiert")
	}
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == pkg {
			if len(f[0]) != 64 {
				break
			}
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s steht nicht in der signierten SHA256SUMS", pkg)
}

// Fetch lädt das Paket eines Releases nach dest (erst geprüft, dann umbenannt,
// auf dem Flash gesichert). Liefert die Prüfsumme.
func (c *Client) Fetch(ctx context.Context, r Release, dest string) (string, error) {
	pkg := PkgName(r.Tag)
	for _, n := range []string{pkg, "SHA256SUMS", "SHA256SUMS.sig"} {
		if r.Assets[n] == "" {
			return "", fmt.Errorf("Release %s ohne %s", r.Tag, n)
		}
	}
	sums, err := c.readAll(ctx, r.Assets["SHA256SUMS"], maxSums)
	if err != nil {
		return "", err
	}
	sig, err := c.readAll(ctx, r.Assets["SHA256SUMS.sig"], 1024)
	if err != nil {
		return "", err
	}
	want, err := VerifySums(c.PublicKey, sums, sig, pkg)
	if err != nil {
		return "", err
	}

	rc, err := c.get(ctx, r.Assets[pkg], maxPkg, "")
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), rc)
	if serr := out.Sync(); err == nil {
		err = serr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	got := hex.EncodeToString(h.Sum(nil))
	switch {
	case err != nil:
	case n > maxPkg:
		err = errors.New("Paket zu groß")
	case got != want:
		err = fmt.Errorf("Prüfsumme falsch (%s… statt %s…) – Paket verworfen", got[:12], want[:12])
	}
	if err == nil {
		err = os.Rename(tmp, dest)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return got, nil
}
