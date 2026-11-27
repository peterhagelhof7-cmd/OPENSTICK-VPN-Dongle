package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

// fakeSys: Dateien im Speicher, Befehle werden mitgeschrieben.
type fakeSys struct {
	mu    sync.Mutex
	files map[string]string
	cmds  []string
	out   map[string]string // Präfix des Befehls -> Ausgabe
	fail  map[string]error  // Präfix des Befehls -> Fehler
	hook  func(cmd string)  // läuft bei jedem Befehl (ohne Sperre)
	stdin []string
}

func newFake() *fakeSys {
	return &fakeSys{files: map[string]string{}, out: map[string]string{}, fail: map[string]error{}}
}

func (f *fakeSys) DryRun() bool { return true }
func (f *fakeSys) Run(name string, args ...string) (string, error) {
	f.mu.Lock()
	c := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, c)
	hook := f.hook
	var out string
	var err error
	for p, o := range f.out {
		if strings.HasPrefix(c, p) {
			out = o
			break
		}
	}
	for p, e := range f.fail {
		if strings.HasPrefix(c, p) {
			err = e
			break
		}
	}
	f.mu.Unlock()
	if hook != nil {
		hook(c)
	}
	return out, err
}
func (f *fakeSys) RunInput(input, name string, args ...string) (string, error) {
	f.mu.Lock()
	f.stdin = append(f.stdin, input)
	f.mu.Unlock()
	return f.Run(name, args...)
}
func (f *fakeSys) WriteFile(p string, d []byte, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[p] = string(d)
	return nil
}
func (f *fakeSys) ReadFile(p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.files[p]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}
func (f *fakeSys) Remove(p string) error { f.mu.Lock(); delete(f.files, p); f.mu.Unlock(); return nil }
func (f *fakeSys) Glob(pat string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var m []string
	for p := range f.files {
		if ok, _ := filepath.Match(pat, p); ok {
			m = append(m, p)
		}
	}
	return m, nil
}
func (f *fakeSys) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.cmds
	f.cmds = nil
	return c
}

func has(cmds []string, s string) bool {
	for _, c := range cmds {
		if strings.Contains(c, s) {
			return true
		}
	}
	return false
}

func newTestApp(t *testing.T) (*App, *fakeSys) {
	st, err := config.Open(filepath.Join(t.TempDir(), "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	fs := newFake()
	a := New(st, fs, "test")
	a.sleep = func(time.Duration) {}
	a.extFetch = func(string, string) (string, error) { return "", os.ErrNotExist } // nie ins Internet
	return a, fs
}

func TestApplyIdempotent(t *testing.T) {
	a, fs := newTestApp(t)
	err := a.Update(func(c *config.Config) error {
		c.WLANs = []config.WLAN{{SSID: "Büro", Pass: "geheimgeheim"}}
		c.LTE.APN = "internet"
		c.WG = config.WireGuard{Priv: "P", Pub: "Q", Endpoint: "e", Port: 51820, Addr: "10.0.0.2",
			Mask: "255.255.255.0", FullTunnel: true, KillSwitch: true, DNS: "1.1.1.1", MTU: 1380}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cmds := fs.take()
	for _, want := range []string{"nmcli connection reload", "systemctl restart wg-quick@wg0",
		"systemctl restart vpndongle-dnsmasq", "nft -f " + NftFile} {
		if !has(cmds, want) {
			t.Errorf("Befehl fehlt: %q in %q", want, cmds)
		}
	}
	for _, f := range []string{NMDir + "/vpndongle-wlan-0.nmconnection", NMDir + "/vpndongle-lte.nmconnection", WGConf, DnsmasqConf, NftFile} {
		if _, ok := fs.files[f]; !ok {
			t.Errorf("Datei fehlt: %s", f)
		}
	}
	// Vor der ersten Messung: gesperrt (sicherer Start)
	if !strings.Contains(fs.files[DnsmasqConf], "address=/#/") {
		t.Error("Start sollte gesperrt sein")
	}

	// Zweites Apply ohne Änderung: keine Neustarts
	if err := a.Apply(); err != nil {
		t.Fatal(err)
	}
	if c := fs.take(); len(c) != 0 {
		t.Errorf("unnötige Befehle: %q", c)
	}

	// WLAN entfernen -> Keyfile weg
	_ = a.Update(func(c *config.Config) error { c.WLANs = nil; return nil })
	if _, ok := fs.files[NMDir+"/vpndongle-wlan-0.nmconnection"]; ok {
		t.Error("altes WLAN-Profil nicht gelöscht")
	}
	fs.take()

	// VPN aus -> Tunnel stoppen
	_ = a.Update(func(c *config.Config) error { c.WG.VPNOff = true; return nil })
	if cmds := fs.take(); !has(cmds, "systemctl stop wg-quick@wg0") {
		t.Errorf("Tunnel nicht gestoppt: %q", cmds)
	}
}

func TestTickUnlocksWithUplink(t *testing.T) {
	a, fs := newTestApp(t)
	_ = a.Apply()
	fs.out["nmcli -t -f DEVICE"] = "wlan0:wifi:connected:vpndongle-wlan-0\nusb0:ethernet:unmanaged:\n"
	fs.out["nmcli -t -f IN-USE"] = "*:Büro:80:WPA2\n"
	a.tick()
	s := a.Status()
	if s.Uplink != "wlan" || s.WLANSSID != "Büro" || s.Locked {
		t.Fatalf("Status: %+v", s)
	}
	if strings.Contains(fs.files[DnsmasqConf], "address=/#/") {
		t.Error("mit WLAN sollte entsperrt sein")
	}
	// Uplink weg -> wieder gesperrt
	fs.out["nmcli -t -f DEVICE"] = "wlan0:wifi:disconnected:\n"
	a.tick()
	if !a.Status().Locked {
		t.Error("ohne Uplink sollte gesperrt sein")
	}
}

func TestLTEFallback(t *testing.T) {
	a, fs := newTestApp(t)
	_ = a.Update(func(c *config.Config) error { c.LTE.APN = "internet"; return nil })
	c := a.Store.Get()
	t0 := time.Now()
	a.lteFallback(c, Status{}, t0)
	a.lteFallback(c, Status{}, t0.Add(lteUpAfter+time.Second))
	waitFor(t, func() bool { return !a.lteBusy.Load() })
	if !has(fs.take(), "connection up vpndongle-lte") {
		t.Fatal("LTE nicht zugeschaltet")
	}
	t1 := t0.Add(time.Minute)
	a.lteFallback(c, Status{WLANUp: true, LTEUp: true}, t1)
	a.lteFallback(c, Status{WLANUp: true, LTEUp: true}, t1.Add(lteDownAfter+time.Second))
	waitFor(t, func() bool { return !a.lteBusy.Load() })
	if !has(fs.take(), "connection down vpndongle-lte") {
		t.Fatal("LTE nicht abgeschaltet")
	}
}

func TestZabbixApply(t *testing.T) {
	a, fs := newTestApp(t)
	_ = a.Apply()
	if c := fs.take(); !has(c, "systemctl disable --now zabbix-proxy zabbix-agent") {
		t.Fatalf("Start ohne Zabbix sollte Dienste abschalten: %q", c)
	}
	_ = a.Update(func(c *config.Config) error {
		c.Zabbix.Enabled, c.Zabbix.Server = true, "10.10.0.1"
		c.Zabbix.PSKIdentity, c.Zabbix.PSK = "id", "0123456789abcdef0123456789abcdef"
		return nil
	})
	cmds := fs.take()
	for _, w := range []string{"chown root:zabbix /etc/zabbix/vpndongle-proxy.psk", "systemctl enable zabbix-proxy zabbix-agent",
		"systemctl restart zabbix-proxy zabbix-agent"} {
		if !has(cmds, w) {
			t.Errorf("fehlt %q in %q", w, cmds)
		}
	}
	if !strings.Contains(fs.files["/etc/zabbix/zabbix_proxy.conf"], "TLSConnect=psk") || fs.files["/etc/zabbix/vpndongle-proxy.psk"] == "" {
		t.Error("Proxy-Konfig/PSK fehlt")
	}
	// unveränderte Konfig -> kein Neustart
	_ = a.Apply()
	if c := fs.take(); has(c, "zabbix") {
		t.Errorf("unnötiger Zabbix-Neustart: %q", c)
	}
	// Überwachungsschalter ändern nur die Discovery, nicht die Dienste
	_ = a.Update(func(c *config.Config) error { c.Zabbix.MonLTE = false; return nil })
	if c := fs.take(); has(c, "zabbix") {
		t.Errorf("Schalter sollte keinen Neustart auslösen: %q", c)
	}
	_ = a.Update(func(c *config.Config) error { c.Zabbix.Enabled = false; return nil })
	if c := fs.take(); !has(c, "systemctl disable --now zabbix-proxy zabbix-agent") {
		t.Errorf("Abschalten fehlt: %q", c)
	}
}

func TestDiscovery(t *testing.T) {
	a, fs := newTestApp(t)
	_ = a.Update(func(c *config.Config) error {
		c.Zabbix.MonLTE = false
		c.Zabbix.Targets = []config.ZbxTarget{{Name: "Drucker", Addr: "192.168.1.20"}}
		return nil
	})
	fs.out["nmcli -t -f DEVICE"] = "wlan0:wifi:connected:vpndongle-wlan-0\n"
	fs.out["ip -4 route"] = "default via 192.168.50.1 dev wlan0 proto dhcp metric 100\n"
	fs.files[LeasesFile] = "1700000600 02:00:00:00:00:02 192.168.226.2 NB 01:02\n"
	a.tick()

	get := func(g string) []map[string]string {
		d, err := a.Discovery(g)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := get("pc"); len(d) != 1 || d[0]["{#PCIP}"] != "192.168.226.2" {
		t.Errorf("pc: %v", d)
	}
	if d := get("wlangw"); len(d) != 1 || d[0]["{#WLANGW}"] != "192.168.50.1" {
		t.Errorf("wlangw: %v", d)
	}
	if d := get("lte"); len(d) != 0 {
		t.Errorf("lte ist aus: %v", d)
	}
	if d := get("vpn"); len(d) != 0 {
		t.Errorf("vpn ohne Konfig: %v", d)
	}
	if d := get("targets"); len(d) != 1 || d[0]["{#TNAME}"] != "Drucker" {
		t.Errorf("targets: %v", d)
	}
	if _, err := a.Discovery("quatsch"); err == nil {
		t.Error("unbekannte Gruppe akzeptiert")
	}
	// WLAN weg: Gateway bleibt bekannt (Items sollen nicht verschwinden)
	fs.out["nmcli -t -f DEVICE"] = "wlan0:wifi:disconnected:\n"
	a.tick()
	if d := get("wlangw"); len(d) != 1 {
		t.Errorf("Gateway nach WLAN-Verlust vergessen: %v", d)
	}
	s := a.Status()
	if s.UplinkCode != 0 || s.HandshakeAge != -1 {
		t.Errorf("Status: %+v", s)
	}
}

func TestSSHFallbackAndRootPW(t *testing.T) {
	a, fs := newTestApp(t)
	_ = a.Apply()
	if !strings.Contains(fs.files[NftFile], "SSH-Notfallmodus") || !a.Status().SSHFallback {
		t.Fatal("ohne usb0 sollte der Notfallmodus aktiv sein")
	}
	fs.files["/sys/class/net/usb0/operstate"] = "up\n"
	a.tick()
	nft := fs.files[NftFile]
	if !strings.Contains(nft, `iifname "usb0" tcp dport 22 accept`) || !strings.Contains(nft, `iifname "wg0" tcp dport 22 accept`) ||
		!strings.Contains(nft, "    tcp dport 22 drop") || strings.Contains(nft, `iifname "wlan0" tcp dport 22`) || a.Status().SSHFallback {
		t.Fatal("mit usb0 muss SSH auf USB/VPN beschränkt sein")
	}
	fs.take()
	if err := a.SetRootPassword("kurz"); err == nil {
		t.Fatal("kurzes Passwort akzeptiert")
	}
	if err := a.SetRootPassword("sehrGeheim123"); err != nil {
		t.Fatal(err)
	}
	cmds := fs.take()
	if !has(cmds, "chpasswd") || has(cmds, "sehrGeheim123") {
		t.Fatalf("Passwort muss per stdin gehen, nicht als Argument: %q", cmds)
	}
	if fs.stdin[len(fs.stdin)-1] != "root:sehrGeheim123\n" {
		t.Fatalf("stdin: %q", fs.stdin)
	}
	if a.Store.Get().RootPWSet.IsZero() {
		t.Fatal("Zeitpunkt nicht gespeichert")
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Zeitüberschreitung")
}

// Uplink „wlan“: eine noch laufende LTE-Verbindung wird getrennt, gedrosselt.
func TestLTEDownInWLANMode(t *testing.T) {
	a, fs := newTestApp(t)
	c := &config.Config{Uplink: config.UplinkWLAN, LTE: config.LTE{Enabled: true, APN: "internet"}}
	now := time.Now()
	a.lteFallback(c, Status{LTEUp: true}, now)
	for i := 0; i < 200 && a.lteBusy.Load(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if cmds := fs.take(); !has(cmds, "nmcli --wait 60 connection down vpndongle-lte") {
		t.Fatalf("%q", cmds)
	}
	a.lteFallback(c, Status{LTEUp: true}, now.Add(10*time.Second)) // gedrosselt
	if cmds := fs.take(); len(cmds) != 0 {
		t.Fatalf("zu oft: %q", cmds)
	}
	c.Uplink = config.UplinkLTE // Uplink LTE: nie trennen
	a.lteFallback(c, Status{LTEUp: true, WLANUp: true}, now.Add(5*time.Minute))
	if cmds := fs.take(); has(cmds, "down") {
		t.Fatalf("%q", cmds)
	}
}
