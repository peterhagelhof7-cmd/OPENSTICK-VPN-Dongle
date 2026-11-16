package app

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/render"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

// Tailscale: der Client (Paket tailscale, Dienst tailscaled.service) läuft nur,
// wenn die VPN-Art Tailscale gewählt ist. Angemeldet wird einmalig mit dem
// Auth-Key (Datei unter /run, danach gelöscht; Key danach aus der Konfig).
// --netfilter-mode=off: unsere nftables bleiben maßgeblich (Tailnet-Richtlinien
// setzt der Client selbst durch); außerdem verwirft Tailscale sonst Pakete aus
// 100.64.0.0/10, die nicht über tailscale0 kommen – genau den Bereich nutzen
// LTE-Anbieter für Carrier-NAT.
const (
	TSUnit    = "tailscaled"
	TSKeyFile = "/run/vpndongle/tailscale.authkey"
)

func (a *App) tsHostname(c *config.Config) string {
	if c.Tailscale.Hostname != "" {
		return c.Tailscale.Hostname
	}
	return "vpndongle-" + strings.ToLower(a.Serial())
}

// tsUpArgs: Argumente für `tailscale up` (ohne Auth-Key). --reset, damit nicht
// angegebene Schalter auf Standard gehen (sonst verweigert `up` Änderungen).
func (a *App) tsUpArgs(c *config.Config) []string {
	args := []string{"up", "--reset", "--timeout=60s",
		"--hostname=" + a.tsHostname(c),
		"--accept-routes", "--accept-dns=false", "--netfilter-mode=off",
	}
	if c.Tailscale.LoginServer != "" {
		args = append(args, "--login-server="+c.Tailscale.LoginServer)
	}
	if c.Tailscale.ExitNode != "" {
		// LAN-Zugriff für den Stick selbst (Router, DNS, NTP im WLAN)
		args = append(args, "--exit-node="+c.Tailscale.ExitNode, "--exit-node-allow-lan-access")
	}
	return args
}

// applyTailscale schaltet den Client passend zur Konfig. Läuft unter applyMu.
func (a *App) applyTailscale(c *config.Config) []error {
	want := c.TSWanted()
	if !want {
		if a.tsOn == nil || *a.tsOn {
			if _, err := a.Sys.Run("tailscale", "down"); err != nil {
				log.Printf("Tailscale trennen: %v", err)
			}
			if _, err := a.Sys.Run("systemctl", "disable", "--now", TSUnit); err != nil {
				log.Printf("Tailscale-Dienst abschalten: %v", err)
			}
			a.forget("ts")
		}
		a.tsOn = &want
		return nil
	}

	args := a.tsUpArgs(c)
	key := c.Tailscale.AuthKey
	if !a.changed("ts", strings.Join(args, "\x00")+"\x00"+key) && a.tsOn != nil && *a.tsOn {
		return nil
	}
	a.tsOn = &want
	if _, err := a.Sys.Run("systemctl", "enable", "--now", TSUnit); err != nil {
		a.forget("ts")
		return []error{fmt.Errorf("Tailscale-Dienst starten: %w", err)}
	}
	if key != "" {
		if err := a.Sys.WriteFile(TSKeyFile, []byte(key+"\n"), 0o600); err != nil {
			a.forget("ts")
			return []error{err}
		}
		args = append(args, "--auth-key=file:"+TSKeyFile)
		defer a.Sys.Remove(TSKeyFile)
	}
	out, err := a.Sys.Run("tailscale", args...)
	if err != nil {
		a.forget("ts")
		msg := strings.TrimSpace(out)
		if key == "" && strings.Contains(strings.ToLower(msg+err.Error()), "login") {
			return []error{errors.New("Tailscale: Anmeldung nötig – Auth-Key in den Einstellungen eintragen")}
		}
		return []error{fmt.Errorf("Tailscale verbinden: %v %s", err, msg)}
	}
	if key != "" {
		if _, err := a.Store.Update(func(n *config.Config) error {
			if n.Tailscale.AuthKey == key {
				n.Tailscale.AuthKey = ""
			}
			return nil
		}); err != nil {
			log.Printf("Auth-Key entfernen: %v", err)
		}
		a.changed("ts", strings.Join(a.tsUpArgs(c), "\x00")+"\x00")
		log.Printf("Tailscale: angemeldet als %s", a.tsHostname(c))
	}
	return nil
}

func (a *App) tsStatus() *system.TSStatus {
	out, err := a.Sys.Run("tailscale", "status", "--json")
	if err != nil && out == "" {
		return &system.TSStatus{Backend: "nicht erreichbar"}
	}
	s := system.ParseTailscale(out)
	return &s
}

// tsRenderState überträgt den Tailscale-Status in den Render-Zustand.
func tsRenderState(rs *render.State, c *config.Config, st *system.TSStatus) {
	if c.VPNKind() != config.VPNTailscale {
		return
	}
	rs.Ov, rs.OvIface, rs.OvResolver = true, render.DefaultNet().TSIface, render.TSResolverDNS
	rs.VPNOn = c.TSWanted()
	if st == nil || !st.Connected() {
		return
	}
	rs.OvRoutes = st.RoutePrefixes()
	if o, ok := st.Overlay(); ok {
		rs.OvNet = o
	}
	rs.OvDomain = st.Suffix
	rs.OvExit = st.ExitNode != ""
}

func tsKey(st *system.TSStatus) string {
	if st == nil {
		return ""
	}
	return fmt.Sprint(st.Connected(), st.Routes, st.IP, st.Suffix, st.ExitNode)
}
