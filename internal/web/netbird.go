package web

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

var (
	reNBHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// Setup-Keys sind UUIDs; großzügig, aber ohne Leer-/Sonderzeichen
	reNBKey = regexp.MustCompile(`^[A-Za-z0-9-]{8,128}$`)
)

// validServerURL: http(s)://host[:port] ohne Pfadspielereien (leer = Cloud des Anbieters).
func validServerURL(s, what string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || !reHost.MatchString(u.Hostname()) ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return fmt.Errorf("%s ungültig (erwartet https://host[:port])", what)
	}
	return nil
}

// postVPNKind: VPN-Art wählen (WireGuard | NetBird | Tailscale).
func (s *Server) postVPNKind(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("vpn")
	switch kind {
	case config.VPNWireGuard, config.VPNNetBird, config.VPNTailscale:
	default:
		fail(w, r, "/settings", fmt.Errorf("unbekannte VPN-Art"))
		return
	}
	if err := s.App.Update(func(c *config.Config) error { c.VPN = kind; return nil }); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "VPN-Art: "+map[string]string{config.VPNWireGuard: "WireGuard", config.VPNNetBird: "NetBird", config.VPNTailscale: "Tailscale"}[kind])
}

func (s *Server) postNetBird(w http.ResponseWriter, r *http.Request) {
	err := s.App.Update(func(c *config.Config) error {
		nb := c.NetBird
		nb.KillSwitch = checked(r, "killswitch")
		mgmt := strings.TrimRight(strings.TrimSpace(r.FormValue("management")), "/")
		if err := validServerURL(mgmt, "Management-URL"); err != nil {
			return err
		}
		host := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
		if host != "" && !reNBHost.MatchString(host) {
			return fmt.Errorf("Name im NetBird-Netz: nur a-z, 0-9 und Bindestrich (max. 63)")
		}
		if v := strings.TrimSpace(r.FormValue("setup_key")); v != "" { // leer = unverändert
			if !reNBKey.MatchString(v) {
				return fmt.Errorf("Setup-Key hat ein ungültiges Format")
			}
			nb.SetupKey = v
		}
		nb.Management, nb.Hostname = mgmt, host
		c.NetBird = nb
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "NetBird gespeichert")
}
