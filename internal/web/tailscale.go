package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

var (
	// Tailscale: tskey-auth-…; Headscale: Hex; ohne Leer-/Sonderzeichen
	reTSKey = regexp.MustCompile(`^[A-Za-z0-9_-]{8,200}$`)
	// Exit-Node: Tailscale-IP oder Name (auch FQDN im Tailnet)
	reTSExit = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252})$`)
)

func (s *Server) postTailscale(w http.ResponseWriter, r *http.Request) {
	err := s.App.Update(func(c *config.Config) error {
		ts := c.Tailscale
		ts.KillSwitch = checked(r, "killswitch")
		srv := strings.TrimRight(strings.TrimSpace(r.FormValue("login_server")), "/")
		if err := validServerURL(srv, "Login-Server"); err != nil {
			return err
		}
		host := strings.ToLower(strings.TrimSpace(r.FormValue("hostname")))
		if host != "" && !reNBHost.MatchString(host) {
			return fmt.Errorf("Name im Tailnet: nur a-z, 0-9 und Bindestrich (max. 63)")
		}
		exit := strings.TrimSpace(r.FormValue("exit_node"))
		if exit != "" && !reTSExit.MatchString(exit) {
			return fmt.Errorf("Exit-Node: Tailscale-IP oder Gerätename")
		}
		if v := strings.TrimSpace(r.FormValue("auth_key")); v != "" { // leer = unverändert
			if !reTSKey.MatchString(v) {
				return fmt.Errorf("Auth-Key hat ein ungültiges Format")
			}
			ts.AuthKey = v
		}
		ts.LoginServer, ts.Hostname, ts.ExitNode = srv, host, exit
		c.Tailscale = ts
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Tailscale gespeichert")
}
