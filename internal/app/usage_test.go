package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

func setCounters(fs *fakeSys, rx, tx int64) {
	fs.mu.Lock()
	fs.files["/sys/class/net/wwan0/statistics/rx_bytes"] = fmt.Sprintf("%d\n", rx)
	fs.files["/sys/class/net/wwan0/statistics/tx_bytes"] = fmt.Sprintf("%d\n", tx)
	fs.mu.Unlock()
}

func TestUsageSessionMonthPersist(t *testing.T) {
	a, fs := newTestApp(t)
	fs.out["nmcli -g GENERAL.IP-IFACE device show wwan0qmi0"] = "wwan0\n"
	t0 := time.Date(2026, 11, 30, 23, 50, 0, 0, time.Local)
	var ns Status

	setCounters(fs, 1000, 100) // Startwert zählt nicht (Stand vor der Sitzung)
	a.sampleUsage(t0, true, "wwan0qmi0", system.LTEStatus{}, &ns)
	setCounters(fs, 5000, 600)
	a.sampleUsage(t0.Add(5*time.Second), true, "wwan0qmi0", system.LTEStatus{}, &ns)
	if ns.LTEIface != "wwan0" || ns.LTESessRX != 4000 || ns.LTESessTX != 500 || ns.LTEMonth.Month != "2026-11" || ns.LTEMonth.Total() != 4500 {
		t.Fatalf("%+v", ns)
	}
	// Schnittstelle neu (Zähler kleiner): ganzer Wert zählt; Monatswechsel
	setCounters(fs, 300, 30)
	a.sampleUsage(t0.Add(15*time.Minute), true, "wwan0qmi0", system.LTEStatus{}, &ns) // 1. Dez.
	if ns.LTEMonth.Month != "2026-12" || ns.LTEMonth.RX != 300 || ns.LTESessRX != 4300 {
		t.Fatalf("%+v", ns)
	}
	if p := ns.PrevMonths(); len(p) != 1 || p[0].Label != "Nov 2026" || p[0].Total() != 4500 {
		t.Fatalf("Vormonate %+v", p)
	}
	// Sitzungsende speichert sofort
	a.sampleUsage(t0.Add(16*time.Minute), false, "", system.LTEStatus{}, &ns)
	if ns.LTEIface != "" || ns.LTESessRX != 0 {
		t.Fatalf("Sitzung nicht beendet: %+v", ns)
	}
	var f usageFile
	if err := json.Unmarshal([]byte(fs.files[UsageFile]), &f); err != nil || f.Months["2026-11"] != [2]int64{4000, 500} || f.Months["2026-12"] != [2]int64{300, 30} {
		t.Fatalf("Datei %q", fs.files[UsageFile])
	}

	// Neustart des Dienstes: Monate aus der Datei, neue Sitzung zählt weiter
	b, _ := newTestApp(t)
	b.Sys = fs
	setCounters(fs, 1000, 0)
	b.sampleUsage(t0.Add(time.Hour), true, "wwan0qmi0", system.LTEStatus{}, &ns)
	setCounters(fs, 1700, 0)
	b.sampleUsage(t0.Add(time.Hour+5*time.Second), true, "wwan0qmi0", system.LTEStatus{}, &ns)
	if ns.LTEMonth.RX != 1000 || ns.LTESessRX != 700 {
		t.Fatalf("nach Neustart %+v", ns)
	}
	// Zurücksetzen „alles“
	b.usageReset()
	b.sampleUsage(t0.Add(2*time.Hour), true, "wwan0qmi0", system.LTEStatus{}, &ns)
	if ns.LTEMonth.Total() != 0 || len(ns.LTEMonths) != 0 {
		t.Fatalf("nicht zurückgesetzt %+v", ns)
	}
	if _, ok := fs.files[UsageFile]; ok {
		t.Fatal("Datei nicht gelöscht")
	}
}

func TestUsageSaveThrottled(t *testing.T) {
	a, fs := newTestApp(t)
	t0 := time.Now()
	var ns Status
	setCounters(fs, 0, 0)
	a.sampleUsage(t0, true, "", system.LTEStatus{}, &ns) // ohne Gerät: a.Net.LTEIface (wwan0)
	writes := 0
	for i := 1; i <= 30; i++ { // 30 × 5 s = 2,5 min
		setCounters(fs, int64(i*1000), 0)
		delete(fs.files, UsageFile)
		a.sampleUsage(t0.Add(time.Duration(i)*5*time.Second), true, "", system.LTEStatus{}, &ns)
		if _, ok := fs.files[UsageFile]; ok {
			writes++
		}
	}
	if writes != 1 { // erste Änderung gleich (saved war null), danach erst nach 5 min
		t.Fatalf("%d Schreibvorgänge in 2,5 min", writes)
	}
}

func TestExtIP(t *testing.T) {
	a, _ := newTestApp(t)
	calls := 0
	a.extFetch = func(network, url string) (string, error) {
		calls++
		if network == "tcp6" {
			return "", fmt.Errorf("kein IPv6")
		}
		return "203.0.113.7", nil
	}
	wait := func() {
		for i := 0; i < 200 && a.extBusy.Load(); i++ {
			time.Sleep(5 * time.Millisecond)
		}
	}
	t0 := time.Now()
	var ns Status
	a.sampleExtIP(t0, "lte", "lte|false|", &ns)
	wait()
	a.sampleExtIP(t0.Add(5*time.Second), "lte", "lte|false|", &ns)
	if ns.ExtIPv4 != "203.0.113.7" || ns.ExtIPv6 != "" || calls != 2 {
		t.Fatalf("%q %q %d", ns.ExtIPv4, ns.ExtIPv6, calls)
	}
	// gleicher Weg, < 10 min: kein neuer Abruf; anderer Weg (VPN an): sofort
	a.sampleExtIP(t0.Add(time.Minute), "lte", "lte|false|", &ns)
	wait()
	if calls != 2 {
		t.Fatal("zu früh neu abgefragt")
	}
	a.sampleExtIP(t0.Add(2*time.Minute), "lte", "lte|true|", &ns)
	wait()
	if calls != 4 {
		t.Fatal("Wegwechsel nicht erkannt", calls)
	}
	// kein Uplink: leer
	a.sampleExtIP(t0.Add(3*time.Minute), "", "|false|", &ns)
	if ns.ExtIPv4 != "" {
		t.Fatal("ohne Uplink noch eine IP")
	}
}

func TestFetchExtIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.TrimPrefix(r.URL.Path, "/") + "\n"))
	}))
	defer srv.Close() // lauscht auf 127.0.0.1 -> tcp4
	if ip, err := fetchExtIP("tcp4", srv.URL+"/198.51.100.4"); err != nil || ip != "198.51.100.4" {
		t.Fatal(ip, err)
	}
	if _, err := fetchExtIP("tcp4", srv.URL+"/2001:db8::1"); err == nil {
		t.Fatal("IPv6-Antwort über tcp4 angenommen")
	}
	if _, err := fetchExtIP("tcp4", srv.URL+"/kaputt"); err == nil {
		t.Fatal("Unsinn angenommen")
	}
}

// UZ801: wwan0 zählt nichts (bam-dmux), die Bytes kommen vom Bearer.
func TestUsageFromBearer(t *testing.T) {
	a, fs := newTestApp(t)
	setCounters(fs, 0, 0)
	lte := system.LTEStatus{Bearers: []string{"/org/freedesktop/ModemManager1/Bearer/1"}}
	bearer := func(rx, tx int64) {
		fs.mu.Lock()
		fs.out["mmcli -b /org/freedesktop/ModemManager1/Bearer/1 -J"] = fmt.Sprintf(
			`{"bearer":{"status":{"connected":"yes","interface":"wwan0"},"stats":{"bytes-rx":"%d","bytes-tx":"%d"}}}`, rx, tx)
		fs.mu.Unlock()
	}
	t0 := time.Now()
	var ns Status
	bearer(1792532, 805493) // schon gelaufen vor dem ersten Blick: Basis
	a.sampleUsage(t0, true, "wwan0qmi0", lte, &ns)
	bearer(2840000, 830000) // 1-MB-Download
	a.sampleUsage(t0.Add(5*time.Second), true, "wwan0qmi0", lte, &ns)
	if ns.LTESessRX != 2840000-1792532 || ns.LTESessTX != 830000-805493 {
		t.Fatalf("%+v", ns)
	}
	// Bearer getrennt: Zähler der Schnittstelle als Ersatz -> neue Basis, kein Sprung
	fs.mu.Lock()
	fs.out["mmcli -b /org/freedesktop/ModemManager1/Bearer/1 -J"] = `{"bearer":{"status":{"connected":"no"},"stats":{"bytes-rx":"--"}}}`
	fs.mu.Unlock()
	setCounters(fs, 50000, 50000)
	a.sampleUsage(t0.Add(10*time.Second), true, "wwan0qmi0", lte, &ns)
	if ns.LTESessRX != 2840000-1792532 {
		t.Fatalf("Quellwechsel zählte mit: %+v", ns)
	}
	if b := system.ParseBearer(`{"bearer":{"status":{"connected":"yes","interface":"--"},"stats":{"bytes-rx":"--","bytes-tx":"--"}}}`); !b.Connected || b.OK || b.Iface != "" {
		t.Fatalf("%+v", b)
	}
}
