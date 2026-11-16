package system

import (
	"reflect"
	"testing"
	"time"
)

func TestSplitNmcli(t *testing.T) {
	got := SplitNmcli(`*:Mein\:WLAN\\x:72:WPA2`)
	want := []string{"*", `Mein:WLAN\x`, "72", "WPA2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestParseDevices(t *testing.T) {
	ds := ParseDevices("wlan0:wifi:connected:vpndongle-wlan-0\nwwan0:gsm:disconnected:\nusb0:ethernet:unmanaged:\n")
	if len(ds) != 3 || !ds[0].Connected() || ds[1].Connected() || ds[0].Conn != "vpndongle-wlan-0" {
		t.Fatalf("%+v", ds)
	}
}

func TestParseAPs(t *testing.T) {
	aps := ParseAPs(" :Büro:40:WPA2\n*:Büro:70:WPA2\n :Gast:55:--\n ::90:WPA2\n")
	if len(aps) != 2 || aps[0].SSID != "Büro" || aps[0].Signal != 70 || !aps[0].Active || aps[1].Locked() {
		t.Fatalf("%+v", aps)
	}
}

func TestParseWG(t *testing.T) {
	if !ParseHandshake("PUB=\t1700000000\n").Equal(time.Unix(1700000000, 0)) {
		t.Fatal("Handshake")
	}
	if !ParseHandshake("PUB=\t0\n").IsZero() {
		t.Fatal("0 = noch nie")
	}
	rx, tx := ParseTransfer("PUB=\t100\t200\n")
	if rx != 100 || tx != 200 {
		t.Fatal(rx, tx)
	}
}

func TestParseMMCLI(t *testing.T) {
	out := `{"modem":{"generic":{"state":"connected","state-failed-reason":"--","unlock-required":"sim-pin2",
	"access-technologies":["lte"],"signal-quality":{"value":"63","recent":"yes"}},
	"3gpp":{"operator-name":"Telekom.de","registration-state":"home","imei":"123"}}}`
	s := ParseMMCLI(out)
	if !s.Present || s.State != "connected" || s.Signal != 63 || s.Tech != "lte" || s.Operator != "Telekom.de" || s.Unlock != "sim-pin2" {
		t.Fatalf("%+v", s)
	}
	if ParseMMCLI("error: couldn't find modem").Present {
		t.Fatal("kein Modem erwartet")
	}
}

func TestParseGWLeases(t *testing.T) {
	if gw := ParseDefaultGW("default via 192.168.50.1 dev wlan0 proto dhcp src 192.168.50.23 metric 100\n"); gw != "192.168.50.1" {
		t.Fatal(gw)
	}
	if ParseDefaultGW("") != "" {
		t.Fatal("leer erwartet")
	}
	ip, host := ParseLeases("1700000000 02:00:00:00:00:01 192.168.226.3 alt *\n1700000600 02:00:00:00:00:02 192.168.226.2 NB-PETER 01:02\n")
	if ip != "192.168.226.2" || host != "NB-PETER" {
		t.Fatal(ip, host)
	}
	if ip, host := ParseLeases("1700000000 aa 192.168.226.4 * *\n"); ip != "192.168.226.4" || host != "" {
		t.Fatal(ip, host)
	}
}

func TestRedact(t *testing.T) {
	got := redact([]string{"connection", "modify", "x", "gsm.pin", "1234"})
	if got[4] != "***" {
		t.Fatal(got)
	}
}
