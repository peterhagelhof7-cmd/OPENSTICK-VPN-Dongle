package app

import (
	"strings"
	"testing"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

func waitCmd(fs *fakeSys, s string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		fs.mu.Lock()
		for _, c := range fs.cmds {
			if strings.Contains(c, s) {
				fs.mu.Unlock()
				return true
			}
		}
		fs.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestBounceOnRouteChange(t *testing.T) {
	oldD, oldDown := bounceDelay, bounceDown
	bounceDelay, bounceDown = 20*time.Millisecond, 5*time.Millisecond
	defer func() { bounceDelay, bounceDown = oldD, oldDown }()

	a, fs := newTestApp(t)
	fs.files["/sys/class/net/usb0/operstate"] = "up\n"
	fs.out["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION"] = "wlan0:wifi:connected:vpndongle-wlan-0\n"
	key := "YAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="
	_ = a.Update(func(c *config.Config) error {
		c.WLANs = []config.WLAN{{SSID: "x", Pass: "geheimgeheim"}}
		c.WG = config.WireGuard{Priv: key, Pub: key, Endpoint: "198.51.100.7", Port: 51820, Addr: "10.10.0.206",
			Mask: "255.255.255.0", Keepalive: 25, DNS: "1.1.1.1", MTU: 1380}
		return nil
	})
	a.tick() // erste Messung: Ausgangswert setzen, kein Anstoß
	time.Sleep(60 * time.Millisecond)
	if has(fs.take(), "usb-gadget.sh stop") {
		t.Fatal("Anstoß beim Start")
	}

	// unveränderte Routen -> kein Anstoß
	_ = a.Apply()
	time.Sleep(60 * time.Millisecond)
	if has(fs.take(), "usb-gadget.sh") {
		t.Fatal("Anstoß ohne Routenänderung")
	}

	// Volltunnel an -> andere Routen -> Gadget kurz lösen und wieder anmelden
	_ = a.Update(func(c *config.Config) error { c.WG.FullTunnel = true; return nil })
	if !waitCmd(fs, GadgetScript+" start", time.Second) {
		t.Fatalf("kein Anstoß nach Routenwechsel: %v", fs.take())
	}
	cmds := fs.take()
	down, up := -1, -1
	for i, c := range cmds {
		if c == GadgetScript+" stop" {
			down = i
		}
		if c == GadgetScript+" start" {
			up = i
		}
	}
	if down < 0 || up < down {
		t.Fatalf("Reihenfolge stop/start falsch: %v", cmds)
	}

	// zwei schnelle Änderungen -> ein Anstoß
	_ = a.Update(func(c *config.Config) error { c.WG.FullTunnel = false; return nil })
	_ = a.Update(func(c *config.Config) error { c.WG.FullTunnel = true; return nil })
	_ = a.Update(func(c *config.Config) error { c.WG.FullTunnel = false; return nil })
	time.Sleep(150 * time.Millisecond)
	n := 0
	for _, c := range fs.take() {
		if c == GadgetScript+" stop" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d Anstöße für drei schnelle Änderungen, erwartet 1", n)
	}
}

// Während das Gadget kurz abgemeldet ist, fehlt usb0 – das darf nicht als
// Ausfall gelten (sonst ginge kurz der SSH-Notfallmodus auf).
func TestBounceKeepsUSBPresent(t *testing.T) {
	oldDown := bounceDown
	bounceDown = 30 * time.Millisecond
	defer func() { bounceDown = oldDown }()
	a, fs := newTestApp(t)
	fs.files["/sys/class/net/usb0/operstate"] = "up\n"
	var during bool
	fs.hook = func(c string) {
		switch c {
		case GadgetScript + " stop":
			fs.mu.Lock()
			delete(fs.files, "/sys/class/net/usb0/operstate")
			fs.mu.Unlock()
			during = a.usbPresent()
		case GadgetScript + " start":
			fs.mu.Lock()
			fs.files["/sys/class/net/usb0/operstate"] = "up\n"
			fs.mu.Unlock()
		}
	}
	a.bounceUSB()
	if !during {
		t.Fatal("usb0 galt während des Neu-Anmeldens als fehlend")
	}
	fs.mu.Lock()
	delete(fs.files, "/sys/class/net/usb0/operstate")
	fs.mu.Unlock()
	if a.usbPresent() {
		t.Fatal("nach dem Neu-Anmelden wird ein echtes Fehlen nicht mehr erkannt")
	}
}

func TestResetOverlay(t *testing.T) {
	a, fs := newTestApp(t)
	_ = a.Update(func(c *config.Config) error {
		c.VPN = config.VPNNetBird
		c.NetBird = config.NetBird{Management: "https://nb.example.org", KillSwitch: true}
		c.Tailscale = config.Tailscale{ExitNode: "100.80.1.2"}
		return nil
	})
	fs.files["/var/lib/netbird/default.json"] = "{}"
	fs.files["/var/lib/netbird/active_profile.json"] = "{}"
	fs.files["/var/lib/netbird/profiles.v1/x.json"] = "{}"
	fs.files["/var/lib/tailscale/tailscaled.state"] = "{}"
	fs.take()

	if err := a.Reset("netbird"); err != nil {
		t.Fatal(err)
	}
	cmds := fs.take()
	c := a.Store.Get()
	if !has(cmds, "netbird deregister") || c.NetBird.Management != "" || c.VPN != config.VPNWireGuard {
		t.Fatalf("NetBird-Reset: %v / %+v / %s", cmds, c.NetBird, c.VPN)
	}
	for _, f := range []string{"/var/lib/netbird/default.json", "/var/lib/netbird/active_profile.json", "/var/lib/netbird/profiles.v1/x.json"} {
		if _, ok := fs.files[f]; ok {
			t.Fatalf("%s nicht gelöscht", f)
		}
	}
	if _, ok := fs.files["/var/lib/tailscale/tailscaled.state"]; !ok || c.Tailscale.ExitNode == "" {
		t.Fatal("Tailscale beim NetBird-Reset angefasst")
	}

	if err := a.Reset("vpn"); err != nil {
		t.Fatal(err)
	}
	cmds = fs.take()
	c = a.Store.Get()
	if !has(cmds, "tailscale logout") || c.Tailscale.ExitNode != "" || c.VPN != config.VPNWireGuard {
		t.Fatalf("VPN-Reset: %v / %+v", cmds, c.Tailscale)
	}
	if _, ok := fs.files["/var/lib/tailscale/tailscaled.state"]; ok {
		t.Fatal("Tailscale-Zustand nicht gelöscht")
	}
	if err := a.Reset("quatsch"); err == nil {
		t.Fatal("unbekannter Bereich angenommen")
	}
}
