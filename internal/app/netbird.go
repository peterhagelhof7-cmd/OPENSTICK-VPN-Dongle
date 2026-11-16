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

// NetBird: der Client (Paket netbird, Dienst netbird.service) läuft nur, wenn die
// VPN-Art NetBird gewählt ist. Angemeldet wird einmalig mit dem Setup-Key
// (Datei unter /run, danach gelöscht); der Key wird nach Erfolg aus der Konfig
// entfernt. Der Stick routet nicht für andere (--disable-server-routes).
const (
	NBUnit    = "netbird"
	NBKeyFile = "/run/vpndongle/netbird.setupkey"
)

// nbHostname: Name des Peers im NetBird-Dashboard.
func (a *App) nbHostname(c *config.Config) string {
	if c.NetBird.Hostname != "" {
		return c.NetBird.Hostname
	}
	return "vpndongle-" + strings.ToLower(a.Serial())
}

// nbUpArgs: Argumente für `netbird up` (ohne Setup-Key).
func (a *App) nbUpArgs(c *config.Config) []string {
	return []string{"up",
		"--management-url", c.NBManagement(),
		"--hostname", a.nbHostname(c),
		"--interface-name", a.Net.NBIface,
		"--dns-resolver-address", render.NBResolverAddr,
		"--disable-server-routes",
	}
}

// applyNetBird schaltet den Client passend zur Konfig. Läuft unter applyMu.
func (a *App) applyNetBird(c *config.Config) []error {
	want := c.NBWanted()
	if !want {
		if a.nbOn == nil || *a.nbOn {
			// beim Start einmal sicher abschalten; fehlendes Paket ist kein Fehler
			if _, err := a.Sys.Run("netbird", "down"); err != nil {
				log.Printf("NetBird trennen: %v", err)
			}
			if _, err := a.Sys.Run("systemctl", "disable", "--now", NBUnit); err != nil {
				log.Printf("NetBird-Dienst abschalten: %v", err)
			}
			a.forget("nb")
		}
		a.nbOn = &want
		return nil
	}

	args := a.nbUpArgs(c)
	key := c.NetBird.SetupKey
	if !a.changed("nb", strings.Join(args, "\x00")+"\x00"+key) && a.nbOn != nil && *a.nbOn {
		return nil
	}
	a.nbOn = &want
	if _, err := a.Sys.Run("systemctl", "enable", "--now", NBUnit); err != nil {
		a.forget("nb")
		return []error{fmt.Errorf("NetBird-Dienst starten: %w", err)}
	}
	if key != "" {
		if err := a.Sys.WriteFile(NBKeyFile, []byte(key+"\n"), 0o600); err != nil {
			a.forget("nb")
			return []error{err}
		}
		args = append(args, "--setup-key-file", NBKeyFile)
		defer a.Sys.Remove(NBKeyFile)
	}
	out, err := a.Sys.Run("netbird", args...)
	if err != nil {
		a.forget("nb") // beim nächsten Apply erneut versuchen
		msg := strings.TrimSpace(out)
		if key == "" && strings.Contains(strings.ToLower(msg+err.Error()), "login") {
			return []error{errors.New("NetBird: Anmeldung nötig – Setup-Key in den Einstellungen eintragen")}
		}
		return []error{fmt.Errorf("NetBird verbinden: %v %s", err, msg)}
	}
	if key != "" {
		// angemeldet: Setup-Key nicht weiter aufbewahren (direkt im Store, ohne
		// erneutes Apply – wir laufen schon unter applyMu)
		if _, err := a.Store.Update(func(n *config.Config) error {
			if n.NetBird.SetupKey == key {
				n.NetBird.SetupKey = ""
			}
			return nil
		}); err != nil {
			log.Printf("Setup-Key entfernen: %v", err)
		}
		a.changed("nb", strings.Join(a.nbUpArgs(c), "\x00")+"\x00") // ohne Key gilt als angewendet
		log.Printf("NetBird: angemeldet bei %s als %s", c.NBManagement(), a.nbHostname(c))
	}
	return nil
}

// nbStatus fragt den Client ab (nur wenn NetBird gewählt ist).
func (a *App) nbStatus() *system.NBStatus {
	out, err := a.Sys.Run("netbird", "status", "--json")
	if err != nil && out == "" {
		return &system.NBStatus{Daemon: "nicht erreichbar"}
	}
	s := system.ParseNetBird(out)
	return &s
}

// nbRenderState überträgt den NetBird-Status in den Render-Zustand.
func nbRenderState(rs *render.State, c *config.Config, st *system.NBStatus) {
	if c.VPNKind() != config.VPNNetBird {
		return
	}
	rs.Ov, rs.OvIface, rs.OvResolver = true, render.DefaultNet().NBIface, render.NBResolverDNS
	rs.VPNOn = c.NBWanted()
	if st == nil || !st.Connected() {
		return
	}
	for _, p := range st.Routes() {
		if p.Bits() == 0 {
			rs.OvExit = true
			continue
		}
		rs.OvRoutes = append(rs.OvRoutes, p)
	}
	if o, ok := st.Overlay(); ok {
		rs.OvNet = o
	}
	rs.OvDomain = st.Domain()
}

// nbKey: alles am NetBird-Status, was dnsmasq/nftables beeinflusst.
func nbKey(st *system.NBStatus) string {
	if st == nil {
		return ""
	}
	var r []string
	for _, p := range st.Routes() {
		r = append(r, p.String())
	}
	o, _ := st.Overlay()
	return fmt.Sprint(st.Connected(), r, o, st.Domain())
}
