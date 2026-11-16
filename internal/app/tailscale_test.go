package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

const tsRunning = `{"BackendState":"Running","MagicDNSSuffix":"tail1234.ts.net",
"Self":{"HostName":"vpndongle-x","DNSName":"vpndongle-x.tail1234.ts.net.","TailscaleIPs":["100.101.2.3"],"Online":true},
"Peer":{"nodekey:a":{"HostName":"nas","DNSName":"nas.tail1234.ts.net.","TailscaleIPs":["100.80.1.1"],"Online":true,"PrimaryRoutes":["192.168.50.0/24"]}}}`

func TestTailscaleLoginClearsKey(t *testing.T) {
	a, fs := newTestApp(t)
	fs.files["/etc/machine-id"] = "0123456789abcdef0123456789abcdef\n"
	var keyAtUp string
	fs.hook = func(c string) {
		if strings.HasPrefix(c, "tailscale up") {
			keyAtUp = fs.files[TSKeyFile]
		}
	}
	err := a.Update(func(c *config.Config) error {
		c.VPN = config.VPNTailscale
		c.Tailscale = config.Tailscale{AuthKey: "tskey-auth-kAbC123CNTRL-xyzXYZ0123456789", LoginServer: "https://hs.example.org", ExitNode: "100.80.1.2"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cmds := fs.take()
	var up string
	for _, c := range cmds {
		if strings.HasPrefix(c, "tailscale up") {
			up = c
		}
	}
	for _, want := range []string{"--reset", "--hostname=vpndongle-a7a2e88fdc5e", "--accept-routes", "--accept-dns=false",
		"--netfilter-mode=off", "--login-server=https://hs.example.org", "--exit-node=100.80.1.2",
		"--exit-node-allow-lan-access", "--auth-key=file:" + TSKeyFile} {
		if !strings.Contains(up, want) {
			t.Fatalf("tailscale up ohne %q: %s", want, up)
		}
	}
	if strings.Contains(up, "tskey-auth-") || strings.TrimSpace(keyAtUp) != "tskey-auth-kAbC123CNTRL-xyzXYZ0123456789" {
		t.Fatalf("Key auf der Kommandozeile oder nicht in der Datei: %s / %q", up, keyAtUp)
	}
	if !has(cmds, "systemctl enable --now tailscaled") {
		t.Fatal("Dienst nicht gestartet")
	}
	if _, ok := fs.files[TSKeyFile]; ok || a.Store.Get().Tailscale.AuthKey != "" {
		t.Fatal("Key-Datei bzw. Key in der Konfig nach der Anmeldung noch da")
	}
	_ = a.Apply()
	if has(fs.take(), "tailscale up") {
		t.Fatal("tailscale up ohne Änderung wiederholt")
	}
	// Exit-Node entfernen -> neues up ohne --exit-node
	_ = a.Update(func(c *config.Config) error { c.Tailscale.ExitNode = ""; return nil })
	cmds = fs.take()
	if !has(cmds, "tailscale up") || has(cmds, "--exit-node") {
		t.Fatalf("Exit-Node nicht entfernt: %v", cmds)
	}
}

func TestTailscaleFailureKeepsKey(t *testing.T) {
	a, fs := newTestApp(t)
	fs.fail["tailscale up"] = errors.New("exit status 1")
	_ = a.Update(func(c *config.Config) error {
		c.VPN, c.Tailscale = config.VPNTailscale, config.Tailscale{AuthKey: "tskey-auth-abcdefgh12345678"}
		return nil
	})
	if a.Store.Get().Tailscale.AuthKey == "" || a.Status().LastError == "" {
		t.Fatal("Key verworfen oder Fehler nicht gemeldet")
	}
	if _, ok := fs.files[TSKeyFile]; ok {
		t.Fatal("Key-Datei nach Fehler nicht gelöscht")
	}
}

func TestSwitchNetBirdToTailscale(t *testing.T) {
	a, fs := newTestApp(t)
	_ = a.Update(func(c *config.Config) error { c.VPN = config.VPNNetBird; return nil })
	fs.take()
	_ = a.Update(func(c *config.Config) error { c.VPN = config.VPNTailscale; return nil })
	cmds := fs.take()
	down, up := -1, -1
	for i, c := range cmds {
		if strings.HasPrefix(c, "netbird down") {
			down = i
		}
		if strings.HasPrefix(c, "tailscale up") {
			up = i
		}
	}
	if down < 0 || up < 0 || down > up {
		t.Fatalf("erst NetBird trennen, dann Tailscale verbinden: %v", cmds)
	}
	if !has(cmds, "systemctl disable --now netbird") {
		t.Fatal("NetBird-Dienst nicht abgeschaltet")
	}
}

func TestTailscaleStatusDrivesRules(t *testing.T) {
	a, fs := newTestApp(t)
	fs.files["/sys/class/net/usb0/operstate"] = "up\n"
	fs.out["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION"] = "wlan0:wifi:connected:vpndongle-wlan-0\n"
	_ = a.Update(func(c *config.Config) error {
		c.WLANs = []config.WLAN{{SSID: "x", Pass: "geheimgeheim"}}
		c.VPN, c.Tailscale = config.VPNTailscale, config.Tailscale{KillSwitch: true}
		return nil
	})
	fs.out["tailscale status --json"] = tsRunning
	a.tick()
	st := a.Status()
	if !st.VPNUp || st.Tailscale == nil || st.Tailscale.PeersUp != 1 || st.NetBird != nil {
		t.Fatalf("Status: %+v", st)
	}
	dns, nft := fs.files[DnsmasqConf], fs.files[NftFile]
	for _, want := range []string{"classless-static-route,100.64.0.0/10,192.168.226.1,192.168.50.0/24,192.168.226.1,0.0.0.0/0",
		"server=/tail1234.ts.net/100.100.100.100"} {
		if !strings.Contains(dns, want) {
			t.Fatalf("dnsmasq ohne %q:\n%s", want, dns)
		}
	}
	if !strings.Contains(nft, `oifname != "tailscale0" drop`) || !strings.Contains(nft, `iifname "tailscale0" tcp dport 22 accept`) {
		t.Fatalf("Kill-Switch/Zugriff nicht auf tailscale0:\n%s", nft)
	}
	// Exit-Node aktiv -> Volltunnel, DNS komplett über MagicDNS
	fs.out["tailscale status --json"] = strings.Replace(tsRunning, `"PrimaryRoutes":["192.168.50.0/24"]}`,
		`"PrimaryRoutes":["192.168.50.0/24"]},"nodekey:b":{"HostName":"exit","DNSName":"exit.tail1234.ts.net.","Online":true,"ExitNode":true,"PrimaryRoutes":["0.0.0.0/0"]}`, 1)
	a.tick()
	dns = fs.files[DnsmasqConf]
	if !strings.Contains(dns, "0.0.0.0/1,192.168.226.1,128.0.0.0/1") || !strings.Contains(dns, "server=100.100.100.100") {
		t.Fatalf("Exit-Node nicht als Volltunnel:\n%s", dns)
	}
}
