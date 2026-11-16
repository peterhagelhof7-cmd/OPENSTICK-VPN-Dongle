package system

import (
	"reflect"
	"testing"
)

func TestParseNetBirdNeedsLogin(t *testing.T) {
	// echte Ausgabe von Client 0.83.1 vor der Anmeldung
	out := `{"peers":{"total":0,"connected":0,"details":null},"cliVersion":"0.83.1","daemonVersion":"0.83.1","daemonStatus":"NeedsLogin","management":{"url":"https://api.netbird.io:443","connected":false,"error":""},"signal":{"url":"","connected":false,"error":""},"relays":{"total":0,"available":0,"details":null},"netbirdIp":"","publicKey":"","usesKernelInterface":false,"wireguardPort":0,"fqdn":"","quantumResistance":false,"quantumResistancePermissive":false,"networks":null,"dnsServers":[],"events":[],"lazyConnectionEnabled":false,"profileName":"default","sshServer":{"enabled":false,"sessions":[]}}`
	s := ParseNetBird(out)
	if s.Daemon != "NeedsLogin" || s.Connected() || s.Domain() != "" || len(s.Routes()) != 0 {
		t.Fatalf("%+v", s)
	}
	if _, ok := s.Overlay(); ok {
		t.Fatal("ohne IP kein Overlay")
	}
}

func TestParseNetBirdConnected(t *testing.T) {
	out := `{"peers":{"total":5,"connected":3},"daemonStatus":"Connected","management":{"connected":true,"error":""},
	"netbirdIp":"100.92.17.4/16","fqdn":"vpndongle-a7a2e8.netbird.selfhosted",
	"networks":["10.10.0.0/24","10.10.0.0/24","fd00::/64","kaputt","172.16.5.0/24"]}`
	s := ParseNetBird(out)
	if !s.Connected() || s.PeersUp != 3 || s.Peers != 5 || s.Domain() != "netbird.selfhosted" {
		t.Fatalf("%+v / %s", s, s.Domain())
	}
	if p, ok := s.Overlay(); !ok || p.String() != "100.92.0.0/16" {
		t.Fatalf("Overlay %v", p)
	}
	var got []string
	for _, r := range s.Routes() {
		got = append(got, r.String())
	}
	if !reflect.DeepEqual(got, []string{"10.10.0.0/24", "172.16.5.0/24"}) {
		t.Fatalf("Routen %v (IPv6, Doppelte, Unsinn müssen raus)", got)
	}
	if s.ExitNode() {
		t.Fatal("kein Exit-Node erwartet")
	}
	s.Networks = append(s.Networks, "0.0.0.0/0")
	if !s.ExitNode() {
		t.Fatal("Exit-Node nicht erkannt")
	}
	if ParseNetBird("").Daemon != "" || ParseNetBird("Fehler: kein Daemon").Daemon != "" {
		t.Fatal("kaputte Ausgabe")
	}
	// Management weg -> nicht verbunden, auch wenn der Daemon noch Connected meldet
	s.Management = false
	if s.Connected() {
		t.Fatal("ohne Management nicht verbunden")
	}
}
