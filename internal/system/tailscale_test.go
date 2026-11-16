package system

import (
	"reflect"
	"testing"
)

func TestParseTailscaleNeedsLogin(t *testing.T) {
	// gekürzte echte Ausgabe von Client 1.104.7 vor der Anmeldung
	out := `{"Version":"1.104.7","TUN":true,"BackendState":"NeedsLogin","AuthURL":"","TailscaleIPs":null,
	"Self":{"ID":"","HostName":"openstick-debian","DNSName":"","OS":"linux","TailscaleIPs":null,"Online":false,"ExitNode":false},
	"Health":["Tailscale is stopped."],"MagicDNSSuffix":"","CurrentTailnet":null,"Peer":null,"User":null}`
	s := ParseTailscale(out)
	if s.Backend != "NeedsLogin" || s.Connected() || s.IP != "" || len(s.Health) != 1 {
		t.Fatalf("%+v", s)
	}
	if _, ok := s.Overlay(); ok {
		t.Fatal("ohne IP kein Overlay")
	}
}

func TestParseTailscaleRunning(t *testing.T) {
	out := `{"BackendState":"Running","MagicDNSSuffix":"tail1234.ts.net",
	"Self":{"HostName":"vpndongle-x","DNSName":"vpndongle-x.tail1234.ts.net.","TailscaleIPs":["100.101.2.3","fd7a:115c:a1e0::1"],"Online":true},
	"Peer":{
	 "nodekey:a":{"HostName":"nas","DNSName":"nas.tail1234.ts.net.","TailscaleIPs":["100.80.1.1"],"Online":true,"PrimaryRoutes":["192.168.50.0/24"]},
	 "nodekey:b":{"HostName":"exit","DNSName":"exit.tail1234.ts.net.","TailscaleIPs":["100.80.1.2"],"Online":true,"ExitNode":true,"PrimaryRoutes":["0.0.0.0/0","::/0","10.20.0.0/16"]},
	 "nodekey:c":{"HostName":"laptop","DNSName":"laptop.tail1234.ts.net.","TailscaleIPs":["100.80.1.3"],"Online":false}}}`
	s := ParseTailscale(out)
	if !s.Connected() || s.IP != "100.101.2.3" || s.Suffix != "tail1234.ts.net" || s.DNSName != "vpndongle-x.tail1234.ts.net" {
		t.Fatalf("%+v", s)
	}
	if s.Peers != 3 || s.PeersUp != 2 || s.ExitNode != "exit.tail1234.ts.net" {
		t.Fatalf("Peers/Exit: %+v", s)
	}
	if !reflect.DeepEqual(s.Routes, []string{"10.20.0.0/16", "192.168.50.0/24"}) {
		t.Fatalf("Routen %v (ohne Standardrouten, sortiert)", s.Routes)
	}
	if p, ok := s.Overlay(); !ok || p.String() != "100.64.0.0/10" {
		t.Fatalf("Overlay %v", p)
	}
	if len(s.RoutePrefixes()) != 2 {
		t.Fatal("RoutePrefixes")
	}
	s.Online = false
	if s.Connected() {
		t.Fatal("offline gilt nicht als verbunden")
	}
	if ParseTailscale("kaputt").Backend != "" {
		t.Fatal("kaputte Ausgabe")
	}
}
