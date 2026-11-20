// Package config hält die Dongle-Konfiguration: Datenmodell, Speichern als
// JSON und das key=value-Austauschformat des ESP32-USB-VPN-Dongles
// (kompatibel zu tools/admin.ps1 und zum Config-Server).
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Grenzen (großzügiger als beim ESP: 5 WLANs / 12 Ressourcen).
const (
	MaxWLAN       = 10
	MaxRes        = 32
	MaxZbxTargets = 16
)

// Uplink-Modi.
const (
	UplinkAuto = "auto" // WLAN bevorzugt, LTE nur als Rückfall
	UplinkWLAN = "wlan" // nur WLAN
	UplinkLTE  = "lte"  // nur LTE
)

type WLAN struct {
	SSID string `json:"ssid"`
	Pass string `json:"pass"`
}

type Resource struct {
	Name string `json:"name"`
	Type string `json:"type"` // rdp | link | ssh
	Addr string `json:"addr"`
	Port int    `json:"port"`
}

type WireGuard struct {
	Priv       string `json:"priv"`
	Pub        string `json:"pub"`
	PSK        string `json:"psk"`
	Endpoint   string `json:"endpoint"`
	Port       int    `json:"port"`
	Addr       string `json:"addr"`
	Mask       string `json:"mask"`
	Keepalive  int    `json:"keepalive"`
	FullTunnel bool   `json:"fulltunnel"`
	KillSwitch bool   `json:"killswitch"`
	VPNOff     bool   `json:"vpn_off"`
	// Neu gegenüber dem ESP:
	DNS     string `json:"dns"`     // DNS im Volltunnel (Standard 1.1.1.1)
	Allowed string `json:"allowed"` // zusätzliche Split-Netze, kommagetrennt (CIDR)
	MTU     int    `json:"mtu"`     // Tunnel-MTU (Standard 1380, LTE-tauglich)
}

type LTE struct {
	Enabled bool   `json:"enabled"`
	APN     string `json:"apn"`
	User    string `json:"user"`
	Pass    string `json:"pass"`
	PIN     string `json:"pin"`
	Roaming bool   `json:"roaming"`
	// SMS über die Weboberfläche (Seite /sms, ohne Login) – nur mit LTE
	SMS bool `json:"sms"`
	// eigene Rufnummer, falls die SIM sie nicht meldet (nur Anzeige)
	Number string `json:"number"`
}

// ZbxTarget = zusätzliches Ping-Ziel (z. B. Drucker im WLAN am Uplink).
type ZbxTarget struct {
	Name string `json:"name"`
	Addr string `json:"addr"`
}

// Zabbix: lokaler Proxy (aktiv, über den Tunnel zum Server) + Agent für den Stick.
// Was überwacht wird, steuern die Mon*-Schalter: der Agent meldet sie per
// Low-Level-Discovery, der Server legt die Items danach an bzw. entfernt sie.
type Zabbix struct {
	Enabled     bool        `json:"enabled"`
	Server      string      `json:"server"`       // Adresse des Servers im Tunnel, optional :port
	ProxyName   string      `json:"proxy_name"`   // leer = vpndongle-<SN>-proxy
	HostName    string      `json:"host_name"`    // leer = vpndongle-<SN>
	PSKIdentity string      `json:"psk_identity"` // optional: TLS-PSK Proxy -> Server
	PSK         string      `json:"psk"`
	MonPC       bool        `json:"mon_pc"`   // PC am USB anpingen
	PCAgent     bool        `json:"pc_agent"` // Agent auf dem PC darf an den Proxy melden
	MonWLAN     bool        `json:"mon_wlan"` // WLAN-Signal + Gateway
	MonLTE      bool        `json:"mon_lte"`  // Mobilfunk
	MonVPN      bool        `json:"mon_vpn"`  // Tunnel
	Targets     []ZbxTarget `json:"targets"`  // weitere Ping-Ziele
}

// Update: Systemupdates (Debian + Zabbix + eigenes Paket).
type Update struct {
	Auto       bool `json:"auto"`        // wöchentlich (Timer)
	AllowLTE   bool `json:"allow_lte"`   // auch über Mobilfunk (Datenvolumen!)
	AutoReboot bool `json:"auto_reboot"` // neu starten, wenn ein Update es verlangt
	GitHub     bool `json:"github"`      // VPN-Dongle-Software aus signierten GitHub-Releases
}

// VPN-Arten (immer nur eine aktiv). Der VPN-Schalter (WG.VPNOff) gilt für alle.
const (
	VPNWireGuard = "wireguard"
	VPNNetBird   = "netbird"
	VPNTailscale = "tailscale"
)

// NetBird: Stick als Peer in einem NetBird-Netz.
type NetBird struct {
	Management string `json:"management"` // leer = NetBird-Cloud (https://api.netbird.io:443)
	SetupKey   string `json:"setup_key"`  // nur bis zur erfolgreichen Anmeldung gespeichert
	Hostname   string `json:"hostname"`   // leer = vpndongle-<SN>
	KillSwitch bool   `json:"killswitch"` // ohne NetBird-Verbindung kein Internet für den PC
}

// Tailscale: Stick als Gerät in einem Tailnet (Tailscale-Cloud oder Headscale).
type Tailscale struct {
	LoginServer string `json:"login_server"` // leer = Tailscale-Cloud; sonst z. B. Headscale
	AuthKey     string `json:"auth_key"`     // nur bis zur erfolgreichen Anmeldung gespeichert
	Hostname    string `json:"hostname"`     // leer = vpndongle-<SN>
	ExitNode    string `json:"exit_node"`    // Tailscale-IP oder Name; leer = kein Volltunnel
	KillSwitch  bool   `json:"killswitch"`
}

// NBDefaultManagement: Management der NetBird-Cloud.
const NBDefaultManagement = "https://api.netbird.io:443"

// NBManagement liefert die wirksame Management-URL.
func (c *Config) NBManagement() string {
	if c.NetBird.Management == "" {
		return NBDefaultManagement
	}
	return c.NetBird.Management
}

// VPNKind liefert die gewählte VPN-Art (Standard WireGuard).
func (c *Config) VPNKind() string {
	switch c.VPN {
	case VPNNetBird, VPNTailscale:
		return c.VPN
	}
	return VPNWireGuard
}

// NBWanted / TSWanted / WGWanted: soll diese VPN-Art laufen?
func (c *Config) NBWanted() bool { return c.VPNKind() == VPNNetBird && !c.WG.VPNOff }
func (c *Config) TSWanted() bool { return c.VPNKind() == VPNTailscale && !c.WG.VPNOff }
func (c *Config) WGWanted() bool {
	return c.VPNKind() == VPNWireGuard && c.WGConfigured() && !c.WG.VPNOff
}

// KillSwitchOn: Kill-Switch der gewählten VPN-Art.
func (c *Config) KillSwitchOn() bool {
	switch c.VPNKind() {
	case VPNNetBird:
		return c.NetBird.KillSwitch
	case VPNTailscale:
		return c.Tailscale.KillSwitch
	}
	return c.WG.KillSwitch
}

type Admin struct {
	User  string `json:"user"`
	PHash string `json:"phash"` // SHA-256-Hex des Passworts (wie ESP)
}

// Config ist der komplette, persistente Zustand.
type Config struct {
	Schema      int        `json:"schema"`
	Version     int        `json:"version"` // Konfig-Version (Config-Server-Vergleich)
	SystemName  string     `json:"systemname"`
	CaptiveAuto bool       `json:"captive_auto"`
	Unlocked    bool       `json:"unlocked"` // manueller Captive-Zustand (nur ohne Automatik)
	Uplink      string     `json:"uplink"`
	Isolate     bool       `json:"isolate"` // PC darf nicht ins lokale Netz am Uplink
	WLANs       []WLAN     `json:"wlans"`
	VPN         string     `json:"vpn"` // VPN-Art: wireguard | netbird | tailscale
	WG          WireGuard  `json:"wg"`
	NetBird     NetBird    `json:"netbird"`
	Tailscale   Tailscale  `json:"tailscale"`
	LTE         LTE        `json:"lte"`
	Admin       Admin      `json:"admin"`
	Resources   []Resource `json:"resources"`
	Zabbix      Zabbix     `json:"zabbix"`
	Update      Update     `json:"update"`
	Access      Access     `json:"access"` // Dienst -> Netze (Haken-Tabelle)
	// Zeitpunkt, zu dem das Root-Passwort über die Weboberfläche gesetzt wurde
	// (das Passwort selbst liegt nur in /etc/shadow, nicht hier/im Export).
	RootPWSet time.Time `json:"root_pw_set"`
	// Verwaltung (wird NICHT exportiert, wie beim ESP):
	Server string `json:"server"` // Config-Server-Basis-URL
	Secret string `json:"secret"` // 20 Zeichen, Schlüssel für BHVPN1
}

// Default liefert die Werkseinstellung.
func Default() *Config {
	return &Config{
		Schema:      1,
		SystemName:  "OpenStick-VPN",
		CaptiveAuto: true,
		Unlocked:    true,
		VPN:         VPNWireGuard,
		Uplink:      UplinkAuto,
		Isolate:     true,
		WG: WireGuard{
			Port: 51820, Mask: "255.255.255.0", Keepalive: 25,
			DNS: "1.1.1.1", MTU: 1380,
		},
		LTE:    LTE{Enabled: true},
		Admin:  Admin{User: "admin", PHash: HashPassword("admin")},
		Zabbix: Zabbix{MonPC: true, MonWLAN: true, MonLTE: true, MonVPN: true},
		Update: Update{GitHub: true},
		Access: DefaultAccess(),
	}
}

// ZabbixNames liefert Proxy- und Hostnamen (Standard aus der Seriennummer).
func (c *Config) ZabbixNames(sn string) (proxy, host string) {
	proxy, host = c.Zabbix.ProxyName, c.Zabbix.HostName
	if proxy == "" {
		proxy = "vpndongle-" + sn + "-proxy"
	}
	if host == "" {
		host = "vpndongle-" + sn
	}
	return proxy, host
}

// HashPassword = SHA-256-Hex (Format des ESP/admin.ps1).
func HashPassword(pw string) string {
	s := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(s[:])
}

// DefaultPassword meldet, ob noch admin/admin gesetzt ist.
func (c *Config) DefaultPassword() bool {
	return c.Admin.PHash == HashPassword("admin")
}

// Normalize füllt leere/ungültige Felder mit Standardwerten.
func (c *Config) Normalize() {
	d := Default()
	if c.Schema == 0 {
		c.Schema = 1
	}
	if c.SystemName == "" {
		c.SystemName = d.SystemName
	}
	switch c.Uplink {
	case UplinkAuto, UplinkWLAN, UplinkLTE:
	default:
		c.Uplink = UplinkAuto
	}
	if c.WG.Port <= 0 || c.WG.Port > 65535 {
		c.WG.Port = d.WG.Port
	}
	if c.WG.Mask == "" {
		c.WG.Mask = d.WG.Mask
	}
	if c.WG.DNS == "" {
		c.WG.DNS = d.WG.DNS
	}
	if c.WG.MTU < 1280 || c.WG.MTU > 1500 {
		c.WG.MTU = d.WG.MTU
	}
	if c.Admin.User == "" {
		c.Admin.User = d.Admin.User
	}
	if c.Admin.PHash == "" {
		c.Admin.PHash = d.Admin.PHash
	}
	if len(c.WLANs) > MaxWLAN {
		c.WLANs = c.WLANs[:MaxWLAN]
	}
	if len(c.Resources) > MaxRes {
		c.Resources = c.Resources[:MaxRes]
	}
	if len(c.Zabbix.Targets) > MaxZbxTargets {
		c.Zabbix.Targets = c.Zabbix.Targets[:MaxZbxTargets]
	}
	c.VPN = c.VPNKind()
	// nach Admin (Schutzregel hängt am Passwort)
	c.Access = normalizeAccess(c.Access, c.DefaultPassword())
}

// WGConfigured: genug Angaben für einen Tunnel vorhanden?
func (c *Config) WGConfigured() bool {
	return c.WG.Priv != "" && c.WG.Pub != "" && c.WG.Endpoint != "" && c.WG.Addr != ""
}

// Clone liefert eine tiefe Kopie.
func (c *Config) Clone() *Config {
	b, _ := json.Marshal(c)
	n := &Config{}
	_ = json.Unmarshal(b, n)
	return n
}

// Store kapselt Laden/Speichern mit Mutex.
type Store struct {
	mu   sync.Mutex
	path string
	cfg  *Config
}

// Open lädt die Konfig oder legt die Werkseinstellung an.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.cfg = Default()
		return s, s.saveLocked()
	case err != nil:
		return nil, err
	}
	c := Default()
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	c.Normalize()
	s.cfg = c
	return s, nil
}

// Get liefert eine Kopie der aktuellen Konfig.
func (s *Store) Get() *Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Clone()
}

// Update ändert die Konfig atomar und speichert sie.
func (s *Store) Update(fn func(c *Config) error) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.cfg.Clone()
	if err := fn(n); err != nil {
		return nil, err
	}
	n.Normalize()
	old := s.cfg
	s.cfg = n
	if err := s.saveLocked(); err != nil {
		s.cfg = old
		return nil, err
	}
	return n.Clone(), nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
