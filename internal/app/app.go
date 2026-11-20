// Package app verbindet Konfig, Systemdateien und Laufzeitzustand:
// Apply() schreibt nur geänderte Dateien und startet nur betroffene Dienste
// neu; Run() beobachtet Uplink/VPN/LTE und reagiert (Captive, LTE-Rückfall,
// LEDs, Config-Server).
package app

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/led"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/render"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

// Dateipfade auf dem Stick.
const (
	NMDir       = "/etc/NetworkManager/system-connections"
	WGConf      = "/etc/wireguard/wg0.conf"
	DnsmasqConf = "/run/vpndongle/dnsmasq.conf"
	NftFile     = "/run/vpndongle/rules.nft"
	FilesDir    = "/var/lib/vpndongle/files"

	LeasesFile = "/run/vpndongle/dnsmasq.leases"
	UpdateEnv  = "/etc/vpndongle/update.env"
	UpdateDir  = "/var/lib/vpndongle/update" // hochgeladenes Paket für update.sh

	WGUnit      = "wg-quick@wg0"
	DnsmasqUnit = "vpndongle-dnsmasq"
	UpdateUnit  = "vpndongle-update.service"
	UpdateTimer = "vpndongle-update.timer"
)

// Zabbix-Dienste der Debian-Pakete.
var zbxUnits = []string{"zabbix-proxy", "zabbix-agent"}

// SSHUnit = Debian-Dienst des OpenSSH-Servers.
const SSHUnit = "ssh"

// usbPresent: gibt es das USB-Netz (Gadget aktiv)?
func (a *App) usbPresent() bool {
	if a.bouncing.Load() { // bewusst kurz abgemeldet (bounce.go) – kein Ausfall
		return true
	}
	_, err := a.Sys.ReadFile("/sys/class/net/" + a.Net.USBIface + "/operstate")
	return err == nil
}

// SetRootPassword setzt das Root-Passwort per chpasswd über stdin (nie als
// Argument, nicht in Konfig/Export). Gespeichert wird nur der Zeitpunkt.
func (a *App) SetRootPassword(pw string) error {
	if len(pw) < 10 || strings.ContainsAny(pw, "\r\n\x00") {
		return errors.New("Passwort: mindestens 10 Zeichen, kein Zeilenumbruch")
	}
	if _, err := a.Sys.RunInput("root:"+pw+"\n", "chpasswd"); err != nil {
		return err
	}
	_, err := a.Store.Update(func(c *config.Config) error { c.RootPWSet = time.Now(); return nil })
	return err
}

// Zeitkonstanten LTE-Rückfall / VPN.
const (
	pollEvery      = 5 * time.Second
	lteUpAfter     = 20 * time.Second // so lange ohne WLAN -> LTE zuschalten
	lteDownAfter   = 60 * time.Second // so lange WLAN stabil -> LTE ab
	lteRetryEvery  = 60 * time.Second
	handshakeAlive = 180 * time.Second
	cfgCheckDelay  = 3 * time.Minute // wie ESP: einmal 3 min nach Uplink
)

// Status = Laufzeitzustand für Web-UI und LEDs.
type Status struct {
	Uplink     string           `json:"uplink"` // "wlan", "lte" oder ""
	WLANUp     bool             `json:"wlan_up"`
	WLANSSID   string           `json:"wlan_ssid"`
	WLANSignal int              `json:"wlan_signal"`
	LTEUp      bool             `json:"lte_up"`
	LTE        system.LTEStatus `json:"lte"`
	VPNOn      bool             `json:"vpn_on"` // soll laufen
	VPNUp      bool             `json:"vpn_up"` // Handshake aktuell
	Handshake  time.Time        `json:"handshake"`
	RX         int64            `json:"rx"`
	TX         int64            `json:"tx"`
	Locked     bool             `json:"locked"`
	CfgAvail   int              `json:"cfg_avail"` // Version auf dem Config-Server (-1 = unbekannt)
	CfgNews    bool             `json:"cfg_news"`
	LastError  string           `json:"last_error,omitempty"`
	Updated    time.Time        `json:"updated"`
	// für Zabbix (numerisch/einfach auswertbar)
	UplinkCode   int    `json:"uplink_code"`   // 0 = keiner, 1 = WLAN, 2 = LTE
	HandshakeAge int64  `json:"handshake_age"` // Sekunden, -1 = noch nie
	WLANGateway  string `json:"wlan_gw"`       // zuletzt bekanntes Gateway im WLAN
	PCIP         string `json:"pc_ip"`         // DHCP-Lease des PCs
	PCName       string `json:"pc_name"`
	Version      string `json:"version"` // vpndongled-Version
	SSHFallback  bool   `json:"ssh_fallback"`
	VPNKind      string `json:"vpn_kind"` // wireguard | netbird | tailscale
	// NetBird-Client (nur wenn als VPN-Art gewählt)
	NetBird   *system.NBStatus `json:"netbird,omitempty"`
	Tailscale *system.TSStatus `json:"tailscale,omitempty"`
	// Auslastung, gemittelt über loadWindow (60 s); -1 = noch kein Wert
	CPUPct     float64 `json:"cpu_pct"`
	RAMPct     float64 `json:"ram_pct"`
	MemUsedKB  int64   `json:"mem_used_kb"`
	MemTotalKB int64   `json:"mem_total_kb"`
	// Temperaturen in °C (-1 = kein Fühler); ThrottleAt = Drosselgrenze der CPU (0 = unbekannt)
	CPUTemp    float64            `json:"cpu_temp_c"`
	ModemTemp  float64            `json:"modem_temp_c"`
	ThrottleAt float64            `json:"throttle_c"`
	Temps      map[string]float64 `json:"temps_c,omitempty"`
}

type App struct {
	Store   *config.Store
	Sys     system.Sys
	Net     render.Net
	Version string

	leds *led.LEDs

	applyMu sync.Mutex
	hashes  map[string][32]byte // zuletzt geschriebener Inhalt je Datei/Bereich
	wgOn    *bool               // zuletzt gesetzter Tunnelzustand (nil = unbekannt)
	zbxOn   *bool               // dito Zabbix
	nbOn    *bool               // dito NetBird
	tsOn    *bool               // dito Tailscale

	// „Kabel ab/an“ nach Routenwechsel (bounce.go)
	bounceMu    sync.Mutex
	bounceTimer *time.Timer
	pushInit    bool
	pushKey     string
	bouncing    atomic.Bool

	mu     sync.RWMutex
	status Status

	// LTE-Rückfall
	wlanDownSince, wlanUpSince time.Time
	lteLastTry                 time.Time
	lteBusy                    atomic.Bool

	uplinkSince time.Time
	cfgChecked  bool
	startedAt   time.Time

	load loadAvg // CPU/RAM-Mittelwerte (nur aus tick)

	// SMS (sms.go): Ablage unter smsMu, Abruf-Zeitpunkte nur aus tick
	smsMu        sync.Mutex
	smsLoaded    bool
	smsList      []SMSMsg
	smsNextID    int64
	smsSent      []time.Time
	smsLastPoll  time.Time
	smsEnableTry time.Time
}

func New(store *config.Store, sys system.Sys, version string) *App {
	return &App{
		Store: store, Sys: sys, Net: render.DefaultNet(), Version: version,
		hashes: map[string][32]byte{}, leds: led.Find(sys), startedAt: time.Now(),
		status: Status{CfgAvail: -1, HandshakeAge: -1, Version: version, CPUPct: -1, RAMPct: -1, CPUTemp: -1, ModemTemp: -1},
	}
}

// Status liefert eine Kopie des Laufzeitzustands.
func (a *App) Status() Status {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *App) setErr(err error) {
	if err == nil {
		return
	}
	log.Printf("FEHLER: %v", err)
	a.mu.Lock()
	a.status.LastError = time.Now().Format("15:04:05 ") + err.Error()
	a.mu.Unlock()
}

// Serial = feste Gerätekennung: die ersten 12 Hex-Zeichen von md5(/etc/machine-id),
// GENAU wie usb-gadget.sh die USB-Seriennummer bildet (md5sum /etc/machine-id).
// Nicht mehr die wlan0-MAC: NetworkManager setzt beim Suchen zufällige und
// verbunden abgeleitete MACs (cloned-mac-address=stable) -> die SN wechselte
// je nach WLAN-Zustand beim Start (2026-11-16 am UZ801 gesehen).
// Ohne machine-id (z. B. Dry-Run unter Windows) bleibt die MAC als Ersatz.
func (a *App) Serial() string {
	if id, err := a.Sys.ReadFile("/etc/machine-id"); err == nil && len(strings.TrimSpace(string(id))) >= 16 {
		h := md5.Sum(id)
		return strings.ToUpper(hex.EncodeToString(h[:])[:12])
	}
	b, err := a.Sys.ReadFile("/sys/class/net/" + a.Net.WLANIface + "/address")
	if err != nil {
		return "000000000000"
	}
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(string(b)), ":", ""))
}

// locked: Captive-Entscheidung (Automatik: gesperrt solange kein Uplink).
func locked(c *config.Config, uplinkUp bool) bool {
	if c.CaptiveAuto {
		return !uplinkUp
	}
	return !c.Unlocked
}

// vpnWanted: WireGuard soll laufen (NetBird hat eigene Logik, siehe netbird.go).
func vpnWanted(c *config.Config) bool { return c.WGWanted() }

// changed merkt sich den Inhalt und meldet, ob er neu ist.
func (a *App) changed(key, content string) bool {
	h := sha256.Sum256([]byte(content))
	if old, ok := a.hashes[key]; ok && old == h {
		return false
	}
	a.hashes[key] = h
	return true
}

func (a *App) forget(key string) { delete(a.hashes, key) }

// Apply bringt das System auf den Stand der Konfig.
func (a *App) Apply() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	c := a.Store.Get()
	st := a.Status()
	var errs []error

	// 1) NetworkManager-Profile (WLAN + LTE)
	files := render.NMKeyfiles(a.Net, c)
	var all strings.Builder
	for _, f := range files {
		all.WriteString(f.Name + "\x00" + f.Content + "\x00")
	}
	if a.changed("nm", all.String()) {
		if err := a.writeNM(files); err != nil {
			errs = append(errs, err)
			a.forget("nm")
		}
	}

	// 2) WireGuard
	want := vpnWanted(c)
	if want {
		conf, err := render.WGQuick(c)
		if err != nil {
			errs = append(errs, err)
			want = false
		} else if a.changed("wg", conf) || a.wgOn == nil || !*a.wgOn {
			if err := a.Sys.WriteFile(WGConf, []byte(conf), 0o600); err != nil {
				errs = append(errs, err)
				a.forget("wg")
			} else if _, err := a.Sys.Run("systemctl", "restart", WGUnit); err != nil {
				errs = append(errs, err)
				a.forget("wg")
			}
		}
	}
	if !want && (a.wgOn == nil || *a.wgOn) {
		if _, err := a.Sys.Run("systemctl", "stop", WGUnit); err != nil {
			errs = append(errs, err)
		}
		a.forget("wg")
	}
	a.wgOn = &want

	// 2b) NetBird / Tailscale (statt WireGuard; erst trennen, dann verbinden)
	errs = append(errs, a.applyNetBird(c)...)
	errs = append(errs, a.applyTailscale(c)...)

	// 3) dnsmasq + nftables (abhängig vom Laufzeitzustand)
	rs := render.State{Locked: locked(c, st.Uplink != ""), VPNOn: want, SSHFallback: !a.usbPresent()}
	nbRenderState(&rs, c, st.NetBird)
	tsRenderState(&rs, c, st.Tailscale)
	if rs.SSHFallback && !st.SSHFallback {
		log.Printf("WARNUNG: %s fehlt – SSH-Notfallmodus (SSH auch über WLAN/LTE erreichbar)", a.Net.USBIface)
	}
	if st.Updated.IsZero() {
		rs.Locked = c.CaptiveAuto || !c.Unlocked // vor der ersten Messung: sicher
	}
	if conf := render.Dnsmasq(a.Net, c, rs); a.changed("dnsmasq", conf) {
		if err := a.Sys.WriteFile(DnsmasqConf, []byte(conf), 0o644); err != nil {
			errs = append(errs, err)
			a.forget("dnsmasq")
		} else if _, err := a.Sys.Run("systemctl", "restart", DnsmasqUnit); err != nil {
			errs = append(errs, err)
			a.forget("dnsmasq")
		}
	}
	if !st.Updated.IsZero() { // vor der ersten Messung gilt „gesperrt“ – kein Vergleichswert
		a.notePushRoutes(fmt.Sprint(render.PushRoutes(c, rs)))
	}
	if rules := render.Nft(a.Net, c, rs); a.changed("nft", rules) {
		if err := a.Sys.WriteFile(NftFile, []byte(rules), 0o644); err != nil {
			errs = append(errs, err)
			a.forget("nft")
		} else if _, err := a.Sys.Run("nft", "-f", NftFile); err != nil {
			errs = append(errs, err)
			a.forget("nft")
		}
	}

	// 4) Zabbix-Proxy + Agent
	errs = append(errs, a.applyZabbix(c)...)

	// 5) Systemupdates: Einstellungen für update.sh + wöchentlicher Timer
	if env := render.UpdateEnv(c); a.changed("updenv", env) {
		if err := a.Sys.WriteFile(UpdateEnv, []byte(env), 0o644); err != nil {
			errs = append(errs, err)
			a.forget("updenv")
		}
	}
	if a.changed("updtimer", fmt.Sprint(c.Update.Auto)) {
		verb := "disable"
		if c.Update.Auto {
			verb = "enable"
		}
		if _, err := a.Sys.Run("systemctl", verb, "--now", UpdateTimer); err != nil {
			errs = append(errs, err)
			a.forget("updtimer")
		}
	}

	a.mu.Lock()
	a.status.Locked, a.status.VPNOn, a.status.SSHFallback = rs.Locked, rs.VPNOn, rs.SSHFallback
	a.mu.Unlock()

	err := errors.Join(errs...)
	a.setErr(err)
	return err
}

// applyZabbix schreibt Proxy-/Agent-Konfig und schaltet die Dienste.
func (a *App) applyZabbix(c *config.Config) []error {
	var errs []error
	on := c.Zabbix.Enabled && c.Zabbix.Server != ""
	if on {
		sn := a.Serial()
		px, ag, psk := render.ZabbixProxy(c, sn), render.ZabbixAgent(c, sn), c.Zabbix.PSK
		if a.changed("zbx", px+"\x00"+ag+"\x00"+psk) || a.zbxOn == nil || !*a.zbxOn {
			err := a.Sys.WriteFile(render.ZbxProxyConf, []byte(px), 0o644)
			if err == nil {
				err = a.Sys.WriteFile(render.ZbxAgentConf, []byte(ag), 0o644)
			}
			if err == nil && psk != "" && c.Zabbix.PSKIdentity != "" {
				// PSK nur für root + Gruppe zabbix lesbar
				if err = a.Sys.WriteFile(render.ZbxPSKFile, []byte(psk+"\n"), 0o640); err == nil {
					_, err = a.Sys.Run("chown", "root:zabbix", render.ZbxPSKFile)
				}
			} else if err == nil {
				_ = a.Sys.Remove(render.ZbxPSKFile)
			}
			if err == nil {
				_, err = a.Sys.Run("systemctl", append([]string{"enable"}, zbxUnits...)...)
			}
			if err == nil {
				_, err = a.Sys.Run("systemctl", append([]string{"restart"}, zbxUnits...)...)
			}
			if err != nil {
				errs = append(errs, err)
				a.forget("zbx")
			}
		}
	} else if a.zbxOn == nil || *a.zbxOn {
		// beim Start immer einmal sicher abschalten; fehlende Pakete sind kein Fehler
		if _, err := a.Sys.Run("systemctl", append([]string{"disable", "--now"}, zbxUnits...)...); err != nil {
			log.Printf("Zabbix abschalten: %v", err)
		}
		a.forget("zbx")
	}
	a.zbxOn = &on
	return errs
}

func (a *App) writeNM(files []render.NMFile) error {
	old, _ := a.Sys.Glob(NMDir + "/" + render.NMPrefix + "*.nmconnection")
	keep := map[string]bool{}
	for _, f := range files {
		p := NMDir + "/" + f.Name + ".nmconnection"
		keep[p] = true
		if err := a.Sys.WriteFile(p, []byte(f.Content), 0o600); err != nil {
			return err
		}
	}
	for _, p := range old {
		if !keep[p] {
			_ = a.Sys.Remove(p)
		}
	}
	_, err := a.Sys.Run("nmcli", "connection", "reload")
	return err
}

// Run ist die Hauptschleife.
func (a *App) Run(stop <-chan struct{}) {
	if err := a.Apply(); err != nil {
		log.Printf("Erst-Apply mit Fehlern: %v", err)
	}
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		a.tick()
		select {
		case <-stop:
			a.leds.Set(led.Off)
			return
		case <-t.C:
		}
	}
}

func (a *App) tick() {
	c := a.Store.Get()
	now := time.Now()
	ns := a.Status()

	// Uplinks
	out, err := a.Sys.Run("nmcli", "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device", "status")
	if err != nil {
		a.setErr(err)
	}
	ns.WLANUp, ns.LTEUp = false, false
	for _, d := range system.ParseDevices(out) {
		switch {
		case d.Name == a.Net.WLANIface && d.Connected():
			ns.WLANUp = true
		case d.Type == "gsm" && d.Connected():
			ns.LTEUp = true
		}
	}
	ns.WLANSSID, ns.WLANSignal = "", 0
	if ns.WLANUp {
		out, _ := a.Sys.Run("nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", "no")
		for _, ap := range system.ParseAPs(out) {
			if ap.Active {
				ns.WLANSSID, ns.WLANSignal = ap.SSID, ap.Signal
			}
		}
	}
	switch {
	case ns.WLANUp:
		ns.Uplink = "wlan"
	case ns.LTEUp:
		ns.Uplink = "lte"
	default:
		ns.Uplink = ""
	}

	// LTE-Modem
	if c.LTE.Enabled {
		out, err := a.Sys.Run("mmcli", "-m", "any", "-J")
		if err == nil {
			ns.LTE = system.ParseMMCLI(out)
		} else {
			ns.LTE = system.LTEStatus{}
		}
	} else {
		ns.LTE = system.LTEStatus{}
	}

	// VPN
	ns.VPNUp, ns.Handshake, ns.RX, ns.TX, ns.HandshakeAge = false, time.Time{}, 0, 0, -1
	if vpnWanted(c) {
		out, _ := a.Sys.Run("wg", "show", a.Net.WGIface, "latest-handshakes")
		ns.Handshake = system.ParseHandshake(out)
		ns.VPNUp = !ns.Handshake.IsZero() && now.Sub(ns.Handshake) < handshakeAlive
		if !ns.Handshake.IsZero() {
			ns.HandshakeAge = int64(now.Sub(ns.Handshake).Seconds())
		}
		out, _ = a.Sys.Run("wg", "show", a.Net.WGIface, "transfer")
		ns.RX, ns.TX = system.ParseTransfer(out)
	}
	ns.VPNKind = c.VPNKind()
	prevOv := nbKey(ns.NetBird) + "|" + tsKey(ns.Tailscale)
	ns.NetBird, ns.Tailscale = nil, nil
	if c.NBWanted() {
		ns.NetBird = a.nbStatus()
		ns.VPNUp = ns.NetBird.Connected()
	}
	if c.TSWanted() {
		ns.Tailscale = a.tsStatus()
		ns.VPNUp = ns.Tailscale.Connected()
	}

	// für Zabbix: Uplink-Code, WLAN-Gateway (zuletzt bekannt), PC-Lease
	ns.UplinkCode = map[string]int{"": 0, "wlan": 1, "lte": 2}[ns.Uplink]
	if ns.WLANUp {
		out, _ := a.Sys.Run("ip", "-4", "route", "show", "default", "dev", a.Net.WLANIface)
		if gw := system.ParseDefaultGW(out); gw != "" {
			ns.WLANGateway = gw
		}
	}
	if b, err := a.Sys.ReadFile(LeasesFile); err == nil {
		ns.PCIP, ns.PCName = system.ParseLeases(string(b))
	}
	a.sampleLoad(now, &ns)
	a.sampleThermal(&ns)
	ns.Updated = now

	a.mu.Lock()
	prevLocked, prevUplink := a.status.Locked, a.status.Uplink
	ns.Locked, ns.VPNOn, ns.LastError = a.status.Locked, a.status.VPNOn, a.status.LastError
	ns.CfgAvail, ns.CfgNews = a.status.CfgAvail, a.status.CfgNews
	a.status = ns
	a.mu.Unlock()

	// Captive-Zustand / Uplinkwechsel / USB-Netz da oder weg -> Regeln nachziehen
	if locked(c, ns.Uplink != "") != prevLocked || ns.Uplink != prevUplink || !a.usbPresent() != ns.SSHFallback ||
		nbKey(ns.NetBird)+"|"+tsKey(ns.Tailscale) != prevOv { // Overlay: Routen/DNS/Verbindung geändert
		_ = a.Apply()
	}

	a.lteFallback(c, ns, now)
	a.updateLED(ns)
	if SMSActive(c) {
		a.smsPoll(ns, now)
	}

	// Config-Server: einmal 3 min nach erstem Uplink
	if ns.Uplink == "" {
		a.uplinkSince = time.Time{}
	} else if a.uplinkSince.IsZero() {
		a.uplinkSince = now
	}
	if !a.cfgChecked && !a.uplinkSince.IsZero() && now.Sub(a.uplinkSince) > cfgCheckDelay && c.Server != "" && c.Secret != "" {
		a.cfgChecked = true
		go func() {
			if _, _, err := a.CheckServer(); err != nil {
				log.Printf("Config-Server: %v", err)
			}
		}()
	}
}

// lteFallback: Modus "auto" – LTE nur, wenn WLAN fehlt.
func (a *App) lteFallback(c *config.Config, s Status, now time.Time) {
	if c.Uplink != config.UplinkAuto || !c.LTE.Enabled || c.LTE.APN == "" {
		return
	}
	if s.WLANUp {
		a.wlanDownSince = time.Time{}
		if a.wlanUpSince.IsZero() {
			a.wlanUpSince = now
		}
	} else {
		a.wlanUpSince = time.Time{}
		if a.wlanDownSince.IsZero() {
			a.wlanDownSince = now
		}
	}
	if a.lteBusy.Load() {
		return
	}
	switch {
	case !s.WLANUp && !s.LTEUp && now.Sub(a.wlanDownSince) >= lteUpAfter && now.Sub(a.lteLastTry) >= lteRetryEvery:
		a.lteLastTry = now
		a.lteAsync("up")
	case s.WLANUp && s.LTEUp && now.Sub(a.wlanUpSince) >= lteDownAfter:
		a.lteAsync("down")
	}
}

func (a *App) lteAsync(dir string) {
	a.lteBusy.Store(true)
	log.Printf("LTE-Rückfall: %s", dir)
	go func() {
		defer a.lteBusy.Store(false)
		if _, err := a.Sys.Run("nmcli", "--wait", "60", "connection", dir, render.LTEConnName); err != nil {
			a.setErr(err)
		}
	}()
}

func (a *App) updateLED(s Status) {
	switch {
	case s.Uplink == "":
		a.leds.Set(led.Red)
	case s.VPNUp:
		a.leds.Set(led.Green)
	case s.Uplink == "lte":
		a.leds.Set(led.BlueBlink)
	default:
		a.leds.Set(led.Blue)
	}
}

// --- Aktionen für die Web-UI ---

// ScanWLAN sucht Netze (blockiert einige Sekunden).
func (a *App) ScanWLAN() ([]system.AP, error) {
	out, err := a.Sys.Run("nmcli", "-t", "-f", "IN-USE,SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", "yes")
	if err != nil {
		return nil, err
	}
	return system.ParseAPs(out), nil
}

// ConnectWLAN verbindet sofort mit Profil i (asynchron).
func (a *App) ConnectWLAN(i int) {
	go func() {
		name := fmt.Sprintf("%swlan-%d", render.NMPrefix, i)
		if _, err := a.Sys.Run("nmcli", "--wait", "30", "connection", "up", name); err != nil {
			a.setErr(err)
		}
	}()
}

// Update ändert die Konfig und wendet sie an.
func (a *App) Update(fn func(c *config.Config) error) error {
	if _, err := a.Store.Update(fn); err != nil {
		return err
	}
	return a.Apply()
}

func (a *App) serverURL(c *config.Config) string {
	return strings.TrimRight(c.Server, "/") + "/" + a.Serial() + "/config.json"
}

func (a *App) fetchServer(c *config.Config) (string, error) {
	if c.Server == "" || c.Secret == "" {
		return "", errors.New("Config-Server oder Secret fehlt")
	}
	cl := &http.Client{Timeout: 10 * time.Second}
	resp, err := cl.Get(a.serverURL(c))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Config-Server: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", err
	}
	return config.Decrypt(string(b), c.Secret)
}

// CheckServer: gibt (Server-Version, neuer?, Fehler).
func (a *App) CheckServer() (int, bool, error) {
	c := a.Store.Get()
	plain, err := a.fetchServer(c)
	if err != nil {
		return -1, false, err
	}
	v := config.VersionOf(plain)
	news := v > c.Version
	a.mu.Lock()
	a.status.CfgAvail, a.status.CfgNews = v, news
	a.mu.Unlock()
	return v, news, nil
}

// ApplyServer holt die Konfig vom Server und spielt sie ein (Merge).
func (a *App) ApplyServer() error {
	plain, err := a.fetchServer(a.Store.Get())
	if err != nil {
		return err
	}
	if err := a.Update(func(c *config.Config) error { return config.ImportKV(c, plain) }); err != nil {
		return err
	}
	a.mu.Lock()
	a.status.CfgNews = false
	a.mu.Unlock()
	return nil
}

// Reset löscht Bereiche der Konfig (wie ESP /reset).
func (a *App) Reset(scope string) error {
	d := config.Default()
	var forget []string // Overlay-Clients, deren Anmeldung mit gelöscht wird
	switch scope {
	case "all", "vpn":
		forget = []string{config.VPNNetBird, config.VPNTailscale}
	case config.VPNNetBird, config.VPNTailscale:
		forget = []string{scope}
	}
	// 1) solange der Client ggf. noch läuft: beim Management/Tailnet abmelden
	for _, k := range forget {
		a.logoutOverlay(k)
	}
	err := a.Update(func(c *config.Config) error {
		switch scope {
		case "all":
			*c = *d
		case "wifi":
			c.WLANs = nil
		case "wg":
			c.WG = d.WG
		case config.VPNNetBird:
			c.NetBird = d.NetBird
		case config.VPNTailscale:
			c.Tailscale = d.Tailscale
		case "vpn":
			c.VPN, c.WG, c.NetBird, c.Tailscale = d.VPN, d.WG, d.NetBird, d.Tailscale
		case "lte":
			c.LTE = d.LTE
		case "wifiwg":
			c.WLANs, c.WG = nil, d.WG
		default:
			return fmt.Errorf("unbekannter Bereich %q", scope)
		}
		// gelöschte VPN-Art nicht ausgewählt lassen
		if scope == c.VPN {
			c.VPN = config.VPNWireGuard
		}
		return nil
	})
	// 2) Client ist jetzt aus (Apply) -> lokalen Anmeldezustand löschen, damit er
	// beim erneuten Auswählen nicht mit der alten Identität weitermacht
	for _, k := range forget {
		a.wipeOverlayState(k)
	}
	// SMS sind persönliche Daten: mit dem LTE-Zugang bzw. „alles“ mit weg
	if err == nil && (scope == "all" || scope == "lte") {
		if derr := a.SMSDelete(0); derr != nil {
			log.Printf("SMS-Ablage löschen: %v", derr)
		}
	}
	return err
}

// logoutOverlay meldet den Client beim Management/Tailnet ab (best effort:
// läuft der Dienst nicht, scheitert das sofort – dann bleibt das Gerät dort
// stehen und muss im Dashboard gelöscht werden).
func (a *App) logoutOverlay(kind string) {
	var err error
	switch kind {
	case config.VPNNetBird:
		_, err = a.Sys.Run("netbird", "deregister")
	case config.VPNTailscale:
		_, err = a.Sys.Run("tailscale", "logout")
	}
	if err != nil {
		log.Printf("%s abmelden: %v (Gerät ggf. im Dashboard löschen)", kind, err)
	}
}

// Lokaler Anmeldezustand der Clients (Schlüssel, Identität).
var overlayState = map[string][]string{
	config.VPNNetBird:   {"/var/lib/netbird/*.json", "/var/lib/netbird/profiles.v1/*"},
	config.VPNTailscale: {"/var/lib/tailscale/tailscaled.state"},
}

func (a *App) wipeOverlayState(kind string) {
	for _, pat := range overlayState[kind] {
		files, _ := a.Sys.Glob(pat)
		for _, f := range files {
			if err := a.Sys.Remove(f); err != nil {
				log.Printf("%s löschen: %v", f, err)
			}
		}
	}
}

func (a *App) Reboot() {
	go func() {
		time.Sleep(time.Second)
		_, _ = a.Sys.Run("systemctl", "reboot")
	}()
}

// SysInfo für die Einstellungsseite.
type SysInfo struct {
	Uptime, DaemonUptime time.Duration
	MemTotal, MemAvail   int64 // kB
	DiskFree             string
	Serial               string
	LEDs                 []string
}

func (a *App) SysInfo() SysInfo {
	si := SysInfo{DaemonUptime: time.Since(a.startedAt).Round(time.Second), Serial: a.Serial()}
	if b, err := a.Sys.ReadFile("/proc/uptime"); err == nil {
		var s float64
		fmt.Sscanf(string(b), "%f", &s)
		si.Uptime = time.Duration(s) * time.Second
	}
	if b, err := a.Sys.ReadFile("/proc/meminfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			var k string
			var v int64
			if n, _ := fmt.Sscanf(l, "%s %d", &k, &v); n == 2 {
				switch k {
				case "MemTotal:":
					si.MemTotal = v
				case "MemAvailable:":
					si.MemAvail = v
				}
			}
		}
	}
	if out, err := a.Sys.Run("df", "-h", "--output=avail", "/"); err == nil {
		f := strings.Fields(out)
		if len(f) > 0 {
			si.DiskFree = f[len(f)-1]
		}
	}
	for _, n := range a.leds.Names() {
		si.LEDs = append(si.LEDs, path.Base(n))
	}
	return si
}

// FilesPath bildet einen sicheren Pfad im Download-Ordner.
func (a *App) FilesPath(name string) (string, error) {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || name == "" || strings.HasPrefix(name, ".") {
		return "", errors.New("ungültiger Dateiname")
	}
	if a.Sys.DryRun() {
		if d, ok := a.Sys.(system.Dry); ok {
			return d.Root + FilesDir + "/" + name, nil
		}
	}
	return FilesDir + "/" + name, nil
}

// FilesDirReal = echter Ordnerpfad (Dry-Run: unter Root).
func (a *App) FilesDirReal() string { return a.realPath(FilesDir) }

// UpdateDirReal = Ablage für hochgeladene Pakete (Dry-Run: unter Root).
func (a *App) UpdateDirReal() string { return a.realPath(UpdateDir) }

func (a *App) realPath(p string) string {
	if d, ok := a.Sys.(system.Dry); ok {
		return d.Root + p
	}
	return p
}

// --- Zabbix ---

// Discovery liefert die Low-Level-Discovery-Daten einer Überwachungsgruppe.
// Ausgeschaltete Gruppen liefern eine leere Liste -> der Server entfernt die
// zugehörigen Items. Gruppen: pc, wlan, wlangw, lte, vpn, targets.
func (a *App) Discovery(group string) ([]map[string]string, error) {
	c, s, z := a.Store.Get(), a.Status(), a.Store.Get().Zabbix
	one := func(k, v string) []map[string]string { return []map[string]string{{k: v}} }
	switch group {
	case "pc":
		if z.MonPC {
			ip := s.PCIP
			if ip == "" {
				ip = a.Net.DongleIP.Next().String() // erste DHCP-Adresse
			}
			return one("{#PCIP}", ip), nil
		}
	case "wlan":
		if z.MonWLAN {
			return one("{#IF}", a.Net.WLANIface), nil
		}
	case "wlangw":
		if z.MonWLAN && s.WLANGateway != "" {
			return one("{#WLANGW}", s.WLANGateway), nil
		}
	case "lte":
		if z.MonLTE && c.LTE.Enabled && c.LTE.APN != "" { // nur wenn LTE eingerichtet
			return one("{#IF}", "lte"), nil
		}
	case "vpn":
		if z.MonVPN && c.WGConfigured() {
			return one("{#IF}", a.Net.WGIface), nil
		}
	case "targets":
		out := []map[string]string{}
		for _, t := range z.Targets {
			out = append(out, map[string]string{"{#TNAME}": t.Name, "{#TADDR}": t.Addr})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unbekannte Gruppe %q", group)
	}
	return []map[string]string{}, nil
}

// ServiceStates = Zustand der Zusatzdienste (für die Einstellungsseite).
func (a *App) ServiceStates() map[string]string {
	st := map[string]string{}
	for _, u := range append(append([]string{SSHUnit}, zbxUnits...), UpdateUnit, UpdateTimer) {
		out, _ := a.Sys.Run("systemctl", "is-active", u)
		if v := strings.TrimSpace(out); v != "" {
			st[u] = v
		} else {
			st[u] = "unbekannt"
		}
	}
	return st
}

// --- Systemupdate ---

// StartUpdate startet update.sh als eigenen systemd-Dienst (läuft weiter,
// auch wenn vpndongled dabei neu gestartet wird).
func (a *App) StartUpdate() error {
	_, err := a.Sys.Run("systemctl", "start", "--no-block", UpdateUnit)
	return err
}

// UpdateLog = letzte Zeilen des Update-Laufs.
func (a *App) UpdateLog() string {
	out, err := a.Sys.Run("journalctl", "-u", UpdateUnit, "-n", "80", "--no-pager", "-o", "short-iso")
	if err != nil {
		return err.Error()
	}
	return out
}
