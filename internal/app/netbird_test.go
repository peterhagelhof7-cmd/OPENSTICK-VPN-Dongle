package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

const nbConnected = `{"peers":{"total":4,"connected":2},"daemonStatus":"Connected",
"management":{"connected":true,"error":""},"netbirdIp":"100.92.17.4/16",
"fqdn":"vpndongle-x.netbird.selfhosted","networks":["10.10.0.0/24"]}`

func TestNetBirdLoginClearsKey(t *testing.T) {
	a, fs := newTestApp(t)
	fs.files["/etc/machine-id"] = "0123456789abcdef0123456789abcdef\n"
	var keyAtUp string
	fs.hook = func(c string) {
		if strings.HasPrefix(c, "netbird up") {
			keyAtUp = fs.files[NBKeyFile]
		}
	}
	err := a.Update(func(c *config.Config) error {
		c.VPN, c.NetBird = config.VPNNetBird, config.NetBird{Management: "https://nb.example.org:443", SetupKey: "A1B2C3D4-E5F6-7890-ABCD-EF0123456789"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cmds := fs.take()
	if !has(cmds, "systemctl enable --now netbird") {
		t.Fatalf("Dienst nicht gestartet: %v", cmds)
	}
	var up string
	for _, c := range cmds {
		if strings.HasPrefix(c, "netbird up") {
			up = c
		}
	}
	for _, want := range []string{"--management-url https://nb.example.org:443", "--hostname vpndongle-a7a2e88fdc5e",
		"--interface-name wt0", "--dns-resolver-address 127.0.0.1:5053", "--disable-server-routes", "--setup-key-file " + NBKeyFile} {
		if !strings.Contains(up, want) {
			t.Fatalf("netbird up ohne %q: %s", want, up)
		}
	}
	if strings.Contains(up, "A1B2C3D4") {
		t.Fatal("Setup-Key auf der Kommandozeile (Prozessliste)!")
	}
	if strings.TrimSpace(keyAtUp) != "A1B2C3D4-E5F6-7890-ABCD-EF0123456789" {
		t.Fatalf("Key-Datei beim up: %q", keyAtUp)
	}
	if _, ok := fs.files[NBKeyFile]; ok {
		t.Fatal("Key-Datei nach dem up nicht gelöscht")
	}
	if k := a.Store.Get().NetBird.SetupKey; k != "" {
		t.Fatalf("Setup-Key nach erfolgreicher Anmeldung noch gespeichert: %q", k)
	}
	if !has(cmds, "systemctl stop wg-quick") && has(cmds, "systemctl restart wg-quick") {
		t.Fatal("WireGuard darf neben NetBird nicht laufen")
	}

	// erneutes Apply ohne Änderung: kein weiteres up
	_ = a.Apply()
	if has(fs.take(), "netbird up") {
		t.Fatal("netbird up ohne Änderung wiederholt")
	}

	// abschalten
	_ = a.Update(func(c *config.Config) error { c.VPN = config.VPNWireGuard; return nil })
	cmds = fs.take()
	if !has(cmds, "netbird down") || !has(cmds, "systemctl disable --now netbird") {
		t.Fatalf("NetBird nicht abgeschaltet: %v", cmds)
	}
}

func TestNetBirdLoginFailureKeepsKey(t *testing.T) {
	a, fs := newTestApp(t)
	fs.fail["netbird up"] = errors.New("exit status 1")
	_ = a.Update(func(c *config.Config) error {
		c.VPN, c.NetBird = config.VPNNetBird, config.NetBird{SetupKey: "A1B2C3D4-0000-0000-0000-000000000000"}
		return nil
	})
	if a.Store.Get().NetBird.SetupKey == "" {
		t.Fatal("Key nach fehlgeschlagener Anmeldung verworfen")
	}
	if _, ok := fs.files[NBKeyFile]; ok {
		t.Fatal("Key-Datei nach Fehler nicht gelöscht")
	}
	if a.Status().LastError == "" {
		t.Fatal("Fehler nicht im Status")
	}
	// nächstes Apply versucht es erneut
	fs.take()
	delete(fs.fail, "netbird up")
	_ = a.Apply()
	if !has(fs.take(), "netbird up") {
		t.Fatal("kein neuer Versuch")
	}
	if a.Store.Get().NetBird.SetupKey != "" {
		t.Fatal("Key nach erfolgreichem zweiten Versuch nicht entfernt")
	}
}

func TestNetBirdStatusDrivesRules(t *testing.T) {
	a, fs := newTestApp(t)
	fs.files["/sys/class/net/usb0/operstate"] = "up\n"
	fs.out["nmcli -t -f DEVICE,TYPE,STATE,CONNECTION"] = "wlan0:wifi:connected:vpndongle-wlan-0\n"
	_ = a.Update(func(c *config.Config) error {
		c.WLANs = []config.WLAN{{SSID: "x", Pass: "geheimgeheim"}}
		c.VPN, c.NetBird = config.VPNNetBird, config.NetBird{KillSwitch: true}
		return nil
	})
	fs.out["netbird status --json"] = nbConnected
	a.tick()
	st := a.Status()
	if !st.VPNUp || st.NetBird == nil || st.NetBird.PeersUp != 2 {
		t.Fatalf("Status: %+v / %+v", st, st.NetBird)
	}
	dns, nft := fs.files[DnsmasqConf], fs.files[NftFile]
	for _, want := range []string{"classless-static-route,100.92.0.0/16,192.168.226.1,10.10.0.0/24,192.168.226.1,0.0.0.0/0",
		"server=/netbird.selfhosted/127.0.0.1#5053"} {
		if !strings.Contains(dns, want) {
			t.Fatalf("dnsmasq ohne %q:\n%s", want, dns)
		}
	}
	if !strings.Contains(nft, `oifname != "wt0" drop`) || strings.Contains(nft, `oifname != "wg0" drop`) {
		t.Fatalf("Kill-Switch nicht auf wt0:\n%s", nft)
	}

	// Exit-Node vom Management -> Volltunnel, DNS komplett über NetBird
	fs.out["netbird status --json"] = strings.Replace(nbConnected, `["10.10.0.0/24"]`, `["10.10.0.0/24","0.0.0.0/0"]`, 1)
	a.tick()
	dns = fs.files[DnsmasqConf]
	if !strings.Contains(dns, "classless-static-route,0.0.0.0/1,192.168.226.1,128.0.0.0/1,192.168.226.1") ||
		!strings.Contains(dns, "no-resolv") || !strings.Contains(dns, "server=127.0.0.1#5053") {
		t.Fatalf("Exit-Node nicht als Volltunnel:\n%s", dns)
	}

	// Verbindung weg -> VPN nicht oben, keine NetBird-Routen mehr
	fs.out["netbird status --json"] = `{"daemonStatus":"Connecting","management":{"connected":false,"error":"timeout"}}`
	a.tick()
	if a.Status().VPNUp || strings.Contains(fs.files[DnsmasqConf], "100.92.0.0/16") {
		t.Fatal("getrennt, aber noch als verbunden behandelt")
	}
}
