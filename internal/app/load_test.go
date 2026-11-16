package app

import (
	"math"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	// echtes Format (Kernel 6.6): user nice system idle iowait irq softirq steal guest guest_nice
	in := "cpu  100 5 50 800 40 3 2 0 7 0\ncpu0 25 1 12 200 10 1 0 0 0 0\nintr 123\n"
	busy, total, ok := parseProcStat(in)
	if !ok {
		t.Fatal("nicht erkannt")
	}
	// total ohne guest-Spalten: 100+5+50+800+40+3+2+0 = 1000; busy = 1000-800-40
	if total != 1000 || busy != 160 {
		t.Fatalf("busy=%d total=%d", busy, total)
	}
	if _, _, ok := parseProcStat("cpu0 1 2 3 4 5\n"); ok {
		t.Fatal("ohne Summenzeile darf nichts erkannt werden")
	}
	if _, _, ok := parseProcStat("cpu  1 x 3 4 5\n"); ok {
		t.Fatal("kaputte Zahl darf nicht erkannt werden")
	}
}

func TestParseMemInfo(t *testing.T) {
	tot, av := parseMemInfo("MemTotal:         325000 kB\nMemFree:  9 kB\nMemAvailable:     237000 kB\n")
	if tot != 325000 || av != 237000 {
		t.Fatalf("tot=%d av=%d", tot, av)
	}
}

func TestLoadAvgWindow(t *testing.T) {
	t0 := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	var l loadAvg
	if l.cpu() != -1 || l.ram() != -1 {
		t.Fatal("ohne Werte muss -1 kommen")
	}
	// alle 5 s ein Wert: CPU-Zähler +100 je 5 s; die ersten 60 s 10 % busy, danach 50 %
	var busy, total uint64
	for i := 0; i <= 36; i++ { // 0..180 s
		pct := uint64(10)
		mem := 20.0
		if i > 12 {
			pct, mem = 50, 60
		}
		if i > 0 {
			busy += pct
			total += 100
		}
		l.add(loadSample{t: t0.Add(time.Duration(i) * 5 * time.Second), busy: busy, total: total, memPct: mem, ok: true})
		if i == 1 {
			if got := l.cpu(); math.Abs(got-10) > 0.01 {
				t.Fatalf("nach 2 Werten: cpu=%.2f, erwartet 10", got)
			}
		}
	}
	// nach 180 s: Fenster enthält nur noch 50-%-Werte (+1 Basiswert davor)
	if got := l.cpu(); math.Abs(got-50) > 0.01 {
		t.Fatalf("cpu=%.2f, erwartet 50", got)
	}
	if got := l.ram(); math.Abs(got-60) > 0.01 {
		t.Fatalf("ram=%.2f, erwartet 60", got)
	}
	// Speicher bleibt begrenzt: 60 s / 5 s = 12 Werte + Basis
	if len(l.s) > 14 {
		t.Fatalf("%d Werte gehalten, erwartet <= 14", len(l.s))
	}
	// erster Wert im Fenster liegt höchstens 60 s zurück, Basis genau davor
	last := l.s[len(l.s)-1].t
	if last.Sub(l.s[0].t) < loadWindow || last.Sub(l.s[1].t) >= loadWindow {
		t.Fatalf("Basis/Fenster falsch: %v / %v", last.Sub(l.s[0].t), last.Sub(l.s[1].t))
	}
}

func TestLoadAvgMixedWindow(t *testing.T) {
	// Übergang: 30 s mit 0 %, dann 30 s mit 100 % -> Mittel über 60 s = 50 %
	t0 := time.Unix(0, 0)
	var l loadAvg
	var busy, total uint64
	for i := 0; i <= 12; i++ {
		if i > 0 {
			total += 100
			if i > 6 {
				busy += 100
			}
		}
		l.add(loadSample{t: t0.Add(time.Duration(i) * 5 * time.Second), busy: busy, total: total, memPct: float64(i * 10), ok: true})
	}
	if got := l.cpu(); math.Abs(got-50) > 0.01 {
		t.Fatalf("cpu=%.2f, erwartet 50", got)
	}
	// RAM: Werte im Fenster (t > jetzt-60 s) = i 1..12 -> 10..120, Mittel 65
	if got := l.ram(); math.Abs(got-65) > 0.01 {
		t.Fatalf("ram=%.2f, erwartet 65", got)
	}
}

func TestLoadAvgCounterReset(t *testing.T) {
	// ungültige Lesungen werden übersprungen, rückläufige Zähler ergeben -1
	t0 := time.Unix(0, 0)
	var l loadAvg
	l.add(loadSample{t: t0, busy: 500, total: 1000, ok: true, memPct: -1})
	l.add(loadSample{t: t0.Add(5 * time.Second), ok: false, memPct: -1})
	if l.cpu() != -1 {
		t.Fatal("nur ein gültiger Wert -> -1")
	}
	if l.ram() != -1 {
		t.Fatal("ohne RAM-Werte -> -1")
	}
	l.add(loadSample{t: t0.Add(10 * time.Second), busy: 400, total: 900, ok: true})
	if l.cpu() != -1 {
		t.Fatal("rückläufige Zähler -> -1")
	}
}

func TestSerialFromMachineID(t *testing.T) {
	a, fs := newTestApp(t)
	fs.files["/sys/class/net/wlan0/address"] = "02:11:22:33:44:55\n"
	if got := a.Serial(); got != "021122334455" {
		t.Fatalf("ohne machine-id: %s, erwartet MAC als Ersatz", got)
	}
	// Wert vom echten Stick: usb-gadget.sh lieferte dort A7A2E88FDC5E
	fs.files["/etc/machine-id"] = "0123456789abcdef0123456789abcdef\n"
	if got := a.Serial(); got != "A7A2E88FDC5E" {
		t.Fatalf("SN %s, erwartet A7A2E88FDC5E (gleich wie USB-Seriennummer)", got)
	}
	// MAC-Wechsel (zufällige Such-MAC) ändert die SN nicht mehr
	fs.files["/sys/class/net/wlan0/address"] = "0a:66:77:88:99:aa\n"
	if got := a.Serial(); got != "A7A2E88FDC5E" {
		t.Fatalf("SN hängt noch an der MAC: %s", got)
	}
}

func TestRound1(t *testing.T) {
	if round1(-1) != -1 || round1(12.341) != 12.3 || round1(12.36) != 12.4 {
		t.Fatalf("%v %v %v", round1(-1), round1(12.341), round1(12.36))
	}
}
