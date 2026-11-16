package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/render"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/swupdate"
)

var (
	reZbxName = regexp.MustCompile(`^[A-Za-z0-9._ -]{1,128}$`)   // erlaubte Zabbix-Hostnamen
	reHexPSK  = regexp.MustCompile(`^([0-9A-Fa-f]{2}){16,256}$`) // min. 128 Bit
)

// --- Discovery für den lokalen Agent ---

// zbxDiscovery liefert LLD-JSON – nur über Loopback (Agent auf dem Stick).
func (s *Server) zbxDiscovery(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		http.Error(w, "nur lokal", http.StatusForbidden)
		return
	}
	d, err := s.App.Discovery(r.URL.Query().Get("group"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}

// zbxTunnelWarning: liegt der Zabbix-Server sicher im Tunnel? Ein aktiver
// Proxy, der den Server nur übers WLAN/LTE erreicht, wäre ungeschützt bzw.
// funktioniert gar nicht.
func zbxTunnelWarning(c *config.Config) string {
	z := c.Zabbix
	if !z.Enabled || z.Server == "" {
		return ""
	}
	if !c.WGConfigured() || c.WG.VPNOff {
		return "VPN ist nicht eingerichtet oder ausgeschaltet – der Proxy erreicht den Server nicht durch den Tunnel."
	}
	host := z.Server
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "Server ist als Name angegeben – bitte sicherstellen, dass er auf eine Adresse im Tunnel zeigt."
	}
	if c.WG.FullTunnel {
		return ""
	}
	nets, err := render.SplitNets(c)
	if err != nil {
		return ""
	}
	for _, n := range nets {
		if n.Contains(addr) {
			return ""
		}
	}
	return fmt.Sprintf("%s liegt nicht in den Tunnel-Netzen (Split-Modus) – unter WireGuard als zusätzliches Netz eintragen.", addr)
}

// --- Einstellungen ---

func (s *Server) postZabbix(w http.ResponseWriter, r *http.Request) {
	err := s.App.Update(func(c *config.Config) error {
		z := c.Zabbix
		z.Enabled = checked(r, "enabled")
		z.Server = strings.TrimSpace(r.FormValue("server"))
		z.ProxyName = strings.TrimSpace(r.FormValue("proxy_name"))
		z.HostName = strings.TrimSpace(r.FormValue("host_name"))
		z.PSKIdentity = strings.TrimSpace(r.FormValue("psk_identity"))
		if v := strings.TrimSpace(r.FormValue("psk")); v != "" { // leer = unverändert
			z.PSK = v
		}
		if checked(r, "psk_clear") {
			z.PSK, z.PSKIdentity = "", ""
		}
		z.MonPC = checked(r, "mon_pc")
		z.PCAgent = checked(r, "pc_agent")
		z.MonWLAN = checked(r, "mon_wlan")
		z.MonLTE = checked(r, "mon_lte")
		z.MonVPN = checked(r, "mon_vpn")

		if z.Enabled && z.Server == "" {
			return fmt.Errorf("Zabbix-Server fehlt")
		}
		if z.Server != "" && !reHost.MatchString(z.Server) {
			return fmt.Errorf("ungültige Server-Adresse")
		}
		for label, v := range map[string]string{"Proxy-Name": z.ProxyName, "Host-Name": z.HostName} {
			if v != "" && !reZbxName.MatchString(v) {
				return fmt.Errorf("%s: nur Buchstaben, Ziffern, Leerzeichen, . _ - (max. 128)", label)
			}
		}
		if (z.PSK == "") != (z.PSKIdentity == "") {
			return fmt.Errorf("PSK-Identität und PSK nur gemeinsam setzen")
		}
		if z.PSK != "" && !reHexPSK.MatchString(z.PSK) {
			return fmt.Errorf("PSK muss hexadezimal sein, mindestens 32 Zeichen (z. B. openssl rand -hex 32)")
		}
		if strings.ContainsAny(z.PSKIdentity, "\r\n") || len(z.PSKIdentity) > 128 {
			return fmt.Errorf("ungültige PSK-Identität")
		}
		c.Zabbix = z
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Zabbix gespeichert")
}

func (s *Server) postZbxTarget(w http.ResponseWriter, r *http.Request) {
	t := config.ZbxTarget{Name: strings.TrimSpace(r.FormValue("name")), Addr: strings.TrimSpace(r.FormValue("addr"))}
	err := s.App.Update(func(c *config.Config) error {
		if t.Name == "" || len(t.Name) > 64 || strings.ContainsAny(t.Name, "\r\n") {
			return fmt.Errorf("Name fehlt oder zu lang")
		}
		if !reHost.MatchString(t.Addr) || strings.Contains(t.Addr, ":") && net.ParseIP(t.Addr) == nil {
			return fmt.Errorf("ungültige Adresse")
		}
		if len(c.Zabbix.Targets) >= config.MaxZbxTargets {
			return fmt.Errorf("maximal %d Ziele", config.MaxZbxTargets)
		}
		c.Zabbix.Targets = append(c.Zabbix.Targets, t)
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Ping-Ziel angelegt")
}

func (s *Server) postZbxTargetDel(w http.ResponseWriter, r *http.Request) {
	i := formInt(r, "i", -1)
	err := s.App.Update(func(c *config.Config) error {
		if i < 0 || i >= len(c.Zabbix.Targets) {
			return fmt.Errorf("ungültiger Eintrag")
		}
		c.Zabbix.Targets = append(c.Zabbix.Targets[:i], c.Zabbix.Targets[i+1:]...)
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Ping-Ziel gelöscht")
}

// --- Systemupdate ---

func (s *Server) postUpdateCfg(w http.ResponseWriter, r *http.Request) {
	err := s.App.Update(func(c *config.Config) error {
		c.Update.Auto = checked(r, "auto")
		c.Update.AllowLTE = checked(r, "allow_lte")
		c.Update.AutoReboot = checked(r, "auto_reboot")
		c.Update.GitHub = checked(r, "github")
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Update-Einstellungen gespeichert")
}

func (s *Server) postUpdate(w http.ResponseWriter, r *http.Request) {
	if err := s.App.StartUpdate(); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Systemupdate gestartet – Fortschritt unter „Update-Protokoll“")
}

// swCheck: neueste Version auf GitHub (nur anzeigen, nichts installieren).
// Im Test austauschbar.
var swLatest = func(ctx context.Context, version string) (string, error) {
	r, err := swupdate.New(version).Latest(ctx)
	return r.Tag, err
}

func (s *Server) swCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res := map[string]any{"aktuell": s.App.Version}
	if tag, err := swLatest(ctx, s.App.Version); err != nil {
		res["fehler"] = err.Error()
	} else {
		res["release"], res["neu"] = tag, swupdate.Newer(s.App.Version, tag)
	}
	_ = json.NewEncoder(w).Encode(res)
}

// postUpdatePkg nimmt ein vpndongle-*.tar.gz entgegen (gestreamt wie Dateien),
// legt es für update.sh ab und startet das Update.
func (s *Server) postUpdatePkg(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			fail(w, r, "/settings", fmt.Errorf("kein Paket gesendet"))
			return
		}
		if part.FormName() != "pkg" {
			part.Close()
			continue
		}
		name := filepath.Base(part.FileName())
		if !strings.HasPrefix(name, "vpndongle-") || !strings.HasSuffix(name, ".tar.gz") {
			part.Close()
			fail(w, r, "/settings", fmt.Errorf("erwartet wird ein Paket vpndongle-<version>.tar.gz aus tools\\build.ps1"))
			return
		}
		sum, err := s.savePkg(part)
		part.Close()
		if err != nil {
			fail(w, r, "/settings", err)
			return
		}
		if err := s.App.StartUpdate(); err != nil {
			fail(w, r, "/settings", err)
			return
		}
		back(w, r, "/settings", fmt.Sprintf("Paket %s angenommen (SHA256 %s…) – Update läuft", name, sum[:16]))
		return
	}
}

func (s *Server) savePkg(src io.Reader) (string, error) {
	dir := s.App.UpdateDirReal()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, ".upload.part")
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(src, maxUpload+1))
	if cerr := closeSynced(out); err == nil { // erst auf dem Flash, dann umbenennen
		err = cerr
	}
	if err == nil && n > maxUpload {
		err = fmt.Errorf("Paket größer als %s", humanBytes(maxUpload))
	}
	if err == nil {
		err = os.Rename(tmp, filepath.Join(dir, "vpndongle-update.tar.gz"))
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	syncDir(dir)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Server) updateLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, s.App.UpdateLog())
}
