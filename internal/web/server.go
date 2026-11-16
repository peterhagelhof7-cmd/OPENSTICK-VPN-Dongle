// Package web liefert die Weboberfläche (öffentliche Seite + /settings mit
// Basic-Auth), angelehnt an die Web-UI v2 des ESP-Dongles.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/app"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/render"
)

//go:embed templates/*.html
var tplFS embed.FS

// Variablen statt Konstanten, damit Tests kleine Grenzen setzen können.
var (
	maxUpload     int64 = 64 << 20  // 64 MB je Datei
	maxFilesTotal int64 = 512 << 20 // 512 MB für alle bereitgestellten Dateien
)

type Server struct {
	App *app.App
	tpl *template.Template
}

func New(a *app.App) *Server {
	funcs := template.FuncMap{
		"bytes": humanBytes,
		"ago": func(t time.Time) string {
			if t.IsZero() {
				return "nie"
			}
			return time.Since(t).Round(time.Second).String()
		},
		"add": func(a, b int) int { return a + b },
		"kib": func(kb int64) string { return humanBytes(kb * 1024) },
		// Farbe der Temperatur: rot ab Drosselgrenze, orange 10 °C darunter
		"tempcls": func(t, throttle float64) string {
			switch {
			case throttle > 0 && t >= throttle:
				return "bad"
			case throttle > 0 && t >= throttle-10:
				return "warn"
			}
			return ""
		},
		// Prozent für Text und Balkenbreite: ganzzahlig, 0..100
		"pct": func(f float64) int {
			switch {
			case f < 0:
				return 0
			case f > 100:
				return 100
			}
			return int(f + 0.5)
		},
		// ssh:// lässt html/template sonst nicht durch -> geprüft freigeben
		"sshurl": func(addr string, port int) template.URL {
			if !reHost.MatchString(addr) {
				return "#"
			}
			if port > 0 {
				return template.URL(fmt.Sprintf("ssh://%s:%d", addr, port))
			}
			return template.URL("ssh://" + addr)
		},
		// Link ohne Schema (z. B. "10.0.0.5") sonst relativ zur Dongle-Seite (ESP-Fix 262372b)
		"link": func(s string) string {
			if !strings.Contains(s, "://") {
				return "http://" + s
			}
			return s
		},
	}
	return &Server{App: a, tpl: template.Must(template.New("").Funcs(funcs).ParseFS(tplFS, "templates/*.html"))}
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	// öffentlich
	m.HandleFunc("GET /{$}", s.index)
	m.HandleFunc("GET /status.json", s.statusJSON)
	m.HandleFunc("GET /scan", s.scan)
	m.HandleFunc("POST /wifi", s.wifiAdd)
	m.HandleFunc("GET /rdp", s.rdp)
	m.HandleFunc("GET /dl", s.download)
	// Admin
	a := func(p string, h http.HandlerFunc) { m.Handle(p, s.auth(h)) }
	a("GET /settings", s.settings)
	a("POST /settings/sys", s.postSys)
	a("POST /settings/lock", s.postLock)
	a("POST /settings/wg", s.postWG)
	a("POST /settings/vpn", s.postVPN)
	a("POST /settings/access", s.postAccess)
	a("POST /settings/netbird", s.postNetBird)
	a("POST /settings/tailscale", s.postTailscale)
	a("POST /settings/vpnkind", s.postVPNKind)
	a("POST /settings/lte", s.postLTE)
	a("POST /settings/wlanup", s.postWLANUp)
	a("POST /settings/wlandel", s.postWLANDel)
	a("POST /settings/admin", s.postAdmin)
	a("POST /settings/res", s.postRes)
	a("POST /settings/resdel", s.postResDel)
	a("POST /settings/upload", s.postUpload)
	a("POST /settings/delfile", s.postDelFile)
	a("GET /settings/export", s.export)
	a("POST /settings/import", s.postImport)
	a("GET /settings/check", s.check)
	a("POST /settings/apply", s.postApply)
	a("POST /settings/reset", s.postReset)
	a("POST /settings/reboot", s.postReboot)
	a("POST /settings/rootpw", s.postRootPW)
	a("POST /settings/zabbix", s.postZabbix)
	a("POST /settings/zbxtarget", s.postZbxTarget)
	a("POST /settings/zbxtargetdel", s.postZbxTargetDel)
	a("POST /settings/updatecfg", s.postUpdateCfg)
	a("POST /settings/update", s.postUpdate)
	a("POST /settings/updatepkg", s.postUpdatePkg)
	a("GET /settings/updatelog", s.updateLog)
	a("GET /settings/swcheck", s.swCheck)
	// nur lokal (Zabbix-Agent auf dem Stick)
	m.HandleFunc("GET /zabbix/discovery", s.zbxDiscovery)
	// Rest: Captive-Weiterleitung bzw. 404
	m.HandleFunc("/", s.fallback)
	return s.captive(s.sameOrigin(m))
}

// --- Middleware ---

// captive: gesperrt + fremder Hostname -> Portal (löst OS-„Anmelden“ aus).
func (s *Server) captive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.App.Status().Locked && !s.ownHost(r.Host) {
			http.Redirect(w, r, "http://"+s.App.Net.DongleIP.String()+"/", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ownHost(h string) bool {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return h == "" || h == "localhost" || h == "dongle.vpndongle" || net.ParseIP(h) != nil
}

// sameOrigin: einfache CSRF-Bremse für POSTs (Basic-Auth wird vom Browser
// sonst auch bei fremden Formularen mitgeschickt).
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			src := r.Header.Get("Origin")
			if src == "" {
				src = r.Header.Get("Referer")
			}
			if src != "" {
				u, err := url.Parse(src)
				if err != nil || u.Host != r.Host {
					http.Error(w, "fremde Herkunft", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		c := s.App.Store.Get()
		if ok &&
			subtle.ConstantTimeCompare([]byte(u), []byte(c.Admin.User)) == 1 &&
			subtle.ConstantTimeCompare([]byte(config.HashPassword(p)), []byte(c.Admin.PHash)) == 1 {
			h(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="VPN-Dongle", charset="UTF-8"`)
		http.Error(w, "Anmeldung erforderlich", http.StatusUnauthorized)
	})
}

// --- Hilfen ---

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Template %s: %v", name, err)
	}
}

func back(w http.ResponseWriter, r *http.Request, to, msg string) {
	http.Redirect(w, r, to+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func fail(w http.ResponseWriter, r *http.Request, to string, err error) {
	back(w, r, to, "Fehler: "+err.Error())
}

func checked(r *http.Request, k string) bool { return r.FormValue(k) != "" }

func formInt(r *http.Request, k string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue(k))); err == nil {
		return v
	}
	return def
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// --- Validierung ---

var (
	reWGKey = regexp.MustCompile(`^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$`)
	reHost  = regexp.MustCompile(`^[A-Za-z0-9.\-:\[\]]{1,253}$`)
	reAPN   = regexp.MustCompile(`^[A-Za-z0-9.\-_]{1,63}$`)
	rePIN   = regexp.MustCompile(`^[0-9]{4,8}$`)
	reHex64 = regexp.MustCompile(`^[0-9A-Fa-f]{64}$`)
)

func validWLAN(ssid, pass string) error {
	if l := len(ssid); l < 1 || l > 32 {
		return fmt.Errorf("SSID muss 1–32 Bytes lang sein")
	}
	if pass != "" && (len(pass) < 8 || len(pass) > 63) && !reHex64.MatchString(pass) {
		return fmt.Errorf("WLAN-Passwort muss 8–63 Zeichen lang sein")
	}
	if strings.ContainsAny(ssid+pass, "\r\n") {
		return fmt.Errorf("Zeilenumbruch nicht erlaubt")
	}
	return nil
}

// --- Datenmodell für Templates ---

type fileInfo struct {
	Name string
	Size int64
}

type pageData struct {
	Cfg     *config.Config
	St      app.Status
	Sys     app.SysInfo
	Version string
	Serial  string
	Msg     string
	Files   []fileInfo
	// Kontingent für bereitgestellte Dateien
	FilesUsed, FilesQuota int64
	DefPW                 bool
	Settings              bool
	DryRun                bool
	// Zabbix / Updates (nur Einstellungsseite)
	ZbxProxy, ZbxHost string
	ZbxWarn           string
	Services          map[string]string
	// Haken-Tabelle „Zugriff auf den Stick“
	AccessCols []accessCol
	AccessRows []accessRow
	NBHost     string // Standardname im NetBird-Netz
}

func (s *Server) data(r *http.Request, settings bool) pageData {
	d := pageData{
		Cfg: s.App.Store.Get(), St: s.App.Status(), Version: s.App.Version,
		Serial: s.App.Serial(), Msg: r.URL.Query().Get("msg"), Settings: settings,
		DryRun: s.App.Sys.DryRun(),
	}
	d.DefPW = d.Cfg.DefaultPassword()
	if settings {
		d.Sys = s.App.SysInfo()
		d.ZbxProxy, d.ZbxHost = d.Cfg.ZabbixNames(d.Serial)
		d.ZbxWarn = zbxTunnelWarning(d.Cfg)
		d.Services = s.App.ServiceStates()
		d.AccessCols, d.AccessRows = accessTable(d.Cfg)
		d.NBHost = "vpndongle-" + strings.ToLower(d.Serial)
	}
	entries, _ := os.ReadDir(s.App.FilesDirReal())
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			d.Files = append(d.Files, fileInfo{e.Name(), fi.Size()})
			d.FilesUsed += fi.Size()
		}
	}
	d.FilesQuota = maxFilesTotal
	sort.Slice(d.Files, func(i, j int) bool { return d.Files[i].Name < d.Files[j].Name })
	return d
}

// --- öffentliche Seiten ---

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	s.render(w, "index", s.data(r, false))
}

func (s *Server) fallback(w http.ResponseWriter, r *http.Request) {
	// OS-Prüfadressen (generate_204, hotspot-detect ...) bei freiem Zugang:
	// normal beantworten wäre Aufgabe des echten Internets -> 404 genügt.
	http.NotFound(w, r)
}

func (s *Server) statusJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.App.Status())
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	aps, err := s.App.ScanWLAN()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type ap struct {
		SSID string `json:"ssid"`
		RSSI int    `json:"signal"`
		Lock bool   `json:"lock"`
	}
	out := make([]ap, 0, len(aps))
	for _, a := range aps {
		out = append(out, ap{a.SSID, a.Signal, a.Locked()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RSSI > out[j].RSSI })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// wifiAdd (öffentlich, wie ESP): bekanntes WLAN aktualisieren oder anhängen,
// dann sofort verbinden.
func (s *Server) wifiAdd(w http.ResponseWriter, r *http.Request) {
	ssid, pass := r.FormValue("ssid"), r.FormValue("pass")
	if err := validWLAN(ssid, pass); err != nil {
		fail(w, r, "/", err)
		return
	}
	idx := -1
	err := s.App.Update(func(c *config.Config) error {
		for i, x := range c.WLANs {
			if x.SSID == ssid {
				c.WLANs[i].Pass = pass
				idx = i
				return nil
			}
		}
		if len(c.WLANs) >= config.MaxWLAN {
			return fmt.Errorf("maximal %d WLANs", config.MaxWLAN)
		}
		c.WLANs = append(c.WLANs, config.WLAN{SSID: ssid, Pass: pass})
		idx = len(c.WLANs) - 1
		return nil
	})
	if err != nil {
		fail(w, r, "/", err)
		return
	}
	s.App.ConnectWLAN(idx)
	back(w, r, "/", "WLAN „"+ssid+"“ gespeichert – verbinde …")
}

func (s *Server) rdp(w http.ResponseWriter, r *http.Request) {
	c := s.App.Store.Get()
	i, err := strconv.Atoi(r.URL.Query().Get("i"))
	if err != nil || i < 0 || i >= len(c.Resources) || c.Resources[i].Type != "rdp" {
		http.NotFound(w, r)
		return
	}
	res := c.Resources[i]
	port := res.Port
	if port == 0 {
		port = 3389
	}
	name := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(res.Name, "_")
	w.Header().Set("Content-Type", "application/x-rdp")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.rdp"`)
	fmt.Fprintf(w, "full address:s:%s:%d\r\nprompt for credentials:i:1\r\nadministrative session:i:0\r\n"+
		"screen mode id:i:2\r\nauthentication level:i:2\r\n", res.Addr, port)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	p, err := s.App.FilesPath(r.URL.Query().Get("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	http.ServeContent(w, r, filepath.Base(p), fi.ModTime(), f)
}

// --- Einstellungen ---

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	s.render(w, "settings", s.data(r, true))
}

func (s *Server) postSys(w http.ResponseWriter, r *http.Request) {
	err := s.App.Update(func(c *config.Config) error {
		if n := strings.TrimSpace(r.FormValue("systemname")); n != "" {
			if len(n) > 40 || strings.ContainsAny(n, "\r\n") {
				return fmt.Errorf("Systemname zu lang")
			}
			c.SystemName = n
		}
		c.CaptiveAuto = checked(r, "captive_auto")
		c.Isolate = checked(r, "isolate")
		switch u := r.FormValue("uplink"); u {
		case config.UplinkAuto, config.UplinkWLAN, config.UplinkLTE:
			c.Uplink = u
		}
		srv := strings.TrimSpace(r.FormValue("server"))
		if srv != "" && !strings.HasPrefix(srv, "http://") && !strings.HasPrefix(srv, "https://") {
			return fmt.Errorf("Config-Server muss mit http:// oder https:// beginnen")
		}
		c.Server = srv
		if sec := r.FormValue("secret"); sec != "" {
			if !regexp.MustCompile(`^[A-Za-z0-9]{20}$`).MatchString(sec) {
				return fmt.Errorf("Secret muss 20 Zeichen A–Z/a–z/0–9 haben")
			}
			c.Secret = sec
		}
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "System gespeichert")
}

func (s *Server) postLock(w http.ResponseWriter, r *http.Request) {
	unlock := r.FormValue("state") == "unlock"
	if err := s.App.Update(func(c *config.Config) error { c.Unlocked = unlock; return nil }); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", map[bool]string{true: "Internet freigegeben", false: "Internet gesperrt"}[unlock])
}

func (s *Server) postWG(w http.ResponseWriter, r *http.Request) {
	err := s.App.Update(func(c *config.Config) error {
		g := c.WG
		if v := strings.TrimSpace(r.FormValue("priv")); v != "" { // leer = unverändert
			g.Priv = v
		}
		if v := strings.TrimSpace(r.FormValue("psk")); v != "" {
			g.PSK = v
		}
		if checked(r, "psk_clear") {
			g.PSK = ""
		}
		g.Pub = strings.TrimSpace(r.FormValue("pub"))
		g.Endpoint = strings.TrimSpace(r.FormValue("endpoint"))
		g.Port = formInt(r, "port", 51820)
		g.Addr = strings.TrimSpace(r.FormValue("addr"))
		g.Mask = strings.TrimSpace(r.FormValue("mask"))
		g.Keepalive = formInt(r, "keepalive", 25)
		g.FullTunnel = checked(r, "fulltunnel")
		g.KillSwitch = checked(r, "killswitch")
		g.DNS = strings.TrimSpace(r.FormValue("dns"))
		g.Allowed = strings.TrimSpace(r.FormValue("allowed"))
		g.MTU = formInt(r, "mtu", 1380)
		for name, k := range map[string]string{"Private Key": g.Priv, "Public Key": g.Pub, "Preshared Key": g.PSK} {
			if k != "" && !reWGKey.MatchString(k) {
				return fmt.Errorf("%s ist kein gültiger WireGuard-Schlüssel", name)
			}
		}
		if g.Endpoint != "" && !reHost.MatchString(g.Endpoint) {
			return fmt.Errorf("ungültiger Endpoint")
		}
		if g.Port < 1 || g.Port > 65535 || g.Keepalive < 0 || g.Keepalive > 3600 {
			return fmt.Errorf("Port/Keepalive außerhalb des Bereichs")
		}
		c.WG = g
		if c.WGConfigured() {
			tmp := c.Clone()
			tmp.Normalize()
			if _, err := renderCheck(tmp); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "WireGuard gespeichert")
}

func (s *Server) postVPN(w http.ResponseWriter, r *http.Request) {
	off := r.FormValue("state") == "off"
	if err := s.App.Update(func(c *config.Config) error { c.WG.VPNOff = off; return nil }); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", map[bool]string{true: "VPN ausgeschaltet", false: "VPN eingeschaltet"}[off])
}

// postAccess: Haken-Tabelle „Zugriff auf den Stick“ (Feld a_<dienst>_<netz>).
func (s *Server) postAccess(w http.ResponseWriter, r *http.Request) {
	err := s.App.Update(func(c *config.Config) error {
		acc := config.Access{}
		for _, svc := range config.AccessServices {
			nets := []string{}
			for _, n := range config.AccessNets {
				if checked(r, "a_"+svc+"_"+n) {
					nets = append(nets, n)
				}
			}
			acc[svc] = nets
		}
		if c.DefaultPassword() {
			for _, n := range acc[config.SvcWeb] {
				if n != config.NetUSB {
					return fmt.Errorf("Weboberfläche über andere Netze erst nach Ändern des Admin-Passworts (noch admin/admin)")
				}
			}
		}
		c.Access = acc // Normalize: Web über USB bleibt immer an
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Freigaben gespeichert")
}

type accessCol struct{ Key, Label string }

type accessCell struct {
	Name           string // Formularfeld
	Checked, Fixed bool   // Fixed: nicht änderbar (Web über USB, Web ohne eigenes Passwort)
	Title          string
}

type accessRow struct {
	Label, Hint string
	Cells       []accessCell
}

func accessTable(c *config.Config) ([]accessCol, []accessRow) {
	cols := []accessCol{{config.NetUSB, "USB (PC)"}, {config.NetVPN, "VPN"}, {config.NetWLAN, "WLAN"}, {config.NetLTE, "LTE"}}
	labels := map[string][2]string{
		config.SvcWeb:  {"Weboberfläche", "TCP 80/443"},
		config.SvcSSH:  {"SSH", "TCP 22"},
		config.SvcPing: {"Ping", "ICMP"},
	}
	var rows []accessRow
	for _, svc := range config.AccessServices {
		row := accessRow{Label: labels[svc][0], Hint: labels[svc][1]}
		for _, col := range cols {
			cell := accessCell{Name: "a_" + svc + "_" + col.Key, Checked: c.Access.Allows(svc, col.Key)}
			switch {
			case svc == config.SvcWeb && col.Key == config.NetUSB:
				cell.Fixed, cell.Title = true, "immer an – sonst sperrt man sich aus"
			case svc == config.SvcWeb && c.DefaultPassword():
				cell.Fixed, cell.Title = true, "erst Admin-Passwort ändern"
			}
			row.Cells = append(row.Cells, cell)
		}
		rows = append(rows, row)
	}
	return cols, rows
}

func (s *Server) postLTE(w http.ResponseWriter, r *http.Request) {
	err := s.App.Update(func(c *config.Config) error {
		l := c.LTE
		l.Enabled = checked(r, "enabled")
		l.Roaming = checked(r, "roaming")
		l.APN = strings.TrimSpace(r.FormValue("apn"))
		l.User = strings.TrimSpace(r.FormValue("user"))
		if v := r.FormValue("pass"); v != "" {
			l.Pass = v
		}
		if v := strings.TrimSpace(r.FormValue("pin")); v != "" {
			l.PIN = v
		}
		if checked(r, "pin_clear") {
			l.PIN = ""
		}
		if l.APN != "" && !reAPN.MatchString(l.APN) {
			return fmt.Errorf("ungültiger APN")
		}
		if l.PIN != "" && !rePIN.MatchString(l.PIN) {
			return fmt.Errorf("PIN muss 4–8 Ziffern haben")
		}
		if strings.ContainsAny(l.User+l.Pass, "\r\n") {
			return fmt.Errorf("Zeilenumbruch nicht erlaubt")
		}
		c.LTE = l
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "LTE gespeichert")
}

func (s *Server) postWLANUp(w http.ResponseWriter, r *http.Request) {
	i := formInt(r, "i", -1)
	err := s.App.Update(func(c *config.Config) error {
		if i < 1 || i >= len(c.WLANs) {
			return fmt.Errorf("ungültiger Eintrag")
		}
		c.WLANs[i-1], c.WLANs[i] = c.WLANs[i], c.WLANs[i-1]
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Reihenfolge geändert")
}

func (s *Server) postWLANDel(w http.ResponseWriter, r *http.Request) {
	i := formInt(r, "i", -1)
	err := s.App.Update(func(c *config.Config) error {
		if i < 0 || i >= len(c.WLANs) {
			return fmt.Errorf("ungültiger Eintrag")
		}
		c.WLANs = append(c.WLANs[:i], c.WLANs[i+1:]...)
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "WLAN gelöscht")
}

func (s *Server) postAdmin(w http.ResponseWriter, r *http.Request) {
	user, pw, pw2 := strings.TrimSpace(r.FormValue("user")), r.FormValue("pw"), r.FormValue("pw2")
	if user == "" || len(pw) < 8 || pw != pw2 || strings.Contains(user, ":") {
		fail(w, r, "/settings", fmt.Errorf("Benutzer fehlt, Passwort zu kurz (min. 8) oder Wiederholung falsch"))
		return
	}
	if err := s.App.Update(func(c *config.Config) error {
		c.Admin.User, c.Admin.PHash = user, config.HashPassword(pw)
		return nil
	}); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Zugangsdaten geändert – bitte neu anmelden")
}

func (s *Server) postRes(w http.ResponseWriter, r *http.Request) {
	res := config.Resource{
		Name: strings.TrimSpace(r.FormValue("name")), Type: r.FormValue("type"),
		Addr: strings.TrimSpace(r.FormValue("addr")), Port: formInt(r, "port", 0),
	}
	err := s.App.Update(func(c *config.Config) error {
		if res.Name == "" || res.Addr == "" || strings.ContainsAny(res.Name+res.Addr, "\r\n") {
			return fmt.Errorf("Name und Adresse nötig")
		}
		switch res.Type {
		case "rdp", "link", "ssh":
		default:
			return fmt.Errorf("unbekannter Typ")
		}
		if len(c.Resources) >= config.MaxRes {
			return fmt.Errorf("maximal %d Ressourcen", config.MaxRes)
		}
		c.Resources = append(c.Resources, res)
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Ressource angelegt")
}

func (s *Server) postResDel(w http.ResponseWriter, r *http.Request) {
	i := formInt(r, "i", -1)
	err := s.App.Update(func(c *config.Config) error {
		if i < 0 || i >= len(c.Resources) {
			return fmt.Errorf("ungültiger Eintrag")
		}
		c.Resources = append(c.Resources[:i], c.Resources[i+1:]...)
		return nil
	})
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Ressource gelöscht")
}

// filesUsage = Summe der bereitgestellten Dateien (ohne die Datei skip).
func (s *Server) filesUsage(skip string) int64 {
	var n int64
	entries, _ := os.ReadDir(s.App.FilesDirReal())
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.Mode().IsRegular() && e.Name() != skip {
			n += fi.Size()
		}
	}
	return n
}

// postUpload streamt die Datei direkt auf den Flash: kein Puffern im RAM
// bzw. in /tmp (bei Debian tmpfs = RAM). Grenzen: maxUpload je Datei und
// maxFilesTotal für alle Dateien zusammen; zu große Uploads werden verworfen
// (früher: stilles Abschneiden bei 64 MB).
func (s *Server) postUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			fail(w, r, "/settings", fmt.Errorf("keine Datei gesendet"))
			return
		}
		if err != nil {
			fail(w, r, "/settings", err)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		name, err := s.saveUpload(part, part.FileName())
		part.Close()
		if err != nil {
			fail(w, r, "/settings", err)
			return
		}
		back(w, r, "/settings", "Datei hochgeladen: "+name)
		return
	}
}

func (s *Server) saveUpload(src io.Reader, filename string) (string, error) {
	p, err := s.App.FilesPath(filename)
	if err != nil {
		return "", err
	}
	name := filepath.Base(p)
	limit := maxUpload
	if free := maxFilesTotal - s.filesUsage(name); free < limit {
		limit = free
	}
	if limit <= 0 {
		return "", fmt.Errorf("Speicherkontingent für Dateien (%s) ist voll", humanBytes(maxFilesTotal))
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(filepath.Dir(p), "."+name+".part") // Punkt = in Listen unsichtbar
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(out, io.LimitReader(src, limit+1))
	if cerr := closeSynced(out); err == nil { // erst auf dem Flash, dann umbenennen
		err = cerr
	}
	if err == nil && n > limit {
		if limit < maxUpload {
			err = fmt.Errorf("Datei passt nicht mehr ins Kontingent (frei %s von %s)", humanBytes(limit), humanBytes(maxFilesTotal))
		} else {
			err = fmt.Errorf("Datei größer als %s", humanBytes(maxUpload))
		}
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	syncDir(filepath.Dir(p))
	return name, nil
}

func (s *Server) postDelFile(w http.ResponseWriter, r *http.Request) {
	p, err := s.App.FilesPath(r.FormValue("name"))
	if err == nil {
		err = os.Remove(p)
	}
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Datei gelöscht")
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	c := s.App.Store.Get()
	if c.Secret == "" {
		fail(w, r, "/settings", fmt.Errorf("zuerst ein Konfig-Secret setzen"))
		return
	}
	enc, err := config.Encrypt(config.ExportKV(c, s.App.Serial()), c.Secret)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="vpndongle-`+s.App.Serial()+`.bhvpn"`)
	io.WriteString(w, enc)
}

func (s *Server) postImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	f, _, err := r.FormFile("file")
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	c := s.App.Store.Get()
	secret := c.Secret
	if v := r.FormValue("secret"); v != "" {
		secret = v
	}
	plain, err := config.Decrypt(string(b), secret)
	if err != nil {
		fail(w, r, "/settings", err)
		return
	}
	if err := s.App.Update(func(c *config.Config) error { return config.ImportKV(c, plain) }); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Konfig eingespielt")
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	v, news, err := s.App.CheckServer()
	w.Header().Set("Content-Type", "application/json")
	res := map[string]any{"version": v, "neu": news, "aktuell": s.App.Store.Get().Version}
	if err != nil {
		res["fehler"] = err.Error()
	}
	_ = json.NewEncoder(w).Encode(res)
}

func (s *Server) postApply(w http.ResponseWriter, r *http.Request) {
	if err := s.App.ApplyServer(); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Konfig vom Server eingespielt")
}

func (s *Server) postReset(w http.ResponseWriter, r *http.Request) {
	scope := r.FormValue("scope")
	if r.FormValue("confirm") != "ja" {
		fail(w, r, "/settings", fmt.Errorf("Bestätigung fehlt"))
		return
	}
	if err := s.App.Reset(scope); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Zurückgesetzt: "+scope)
}

func (s *Server) postReboot(w http.ResponseWriter, r *http.Request) {
	s.App.Reboot()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, `<meta http-equiv="refresh" content="60;url=/">Neustart … Seite lädt in 60 s neu.`)
}

// renderCheck prüft Tunnel-IP/Maske/Split-Netze durch Probe-Rendern.
func renderCheck(c *config.Config) (string, error) { return render.WGQuick(c) }
