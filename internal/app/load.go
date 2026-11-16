package app

import (
	"strconv"
	"strings"
	"time"
)

// loadWindow: Zeitraum für die gemittelte CPU-/RAM-Auslastung im Status.
const loadWindow = 60 * time.Second

type loadSample struct {
	t           time.Time
	busy, total uint64  // CPU-Zähler aus /proc/stat (Jiffies, alle Kerne)
	memPct      float64 // belegter RAM in % (MemTotal - MemAvailable)
	ok          bool    // CPU-Zähler gültig
}

// loadAvg hält die Messwerte des letzten Zeitfensters. Nur aus tick() benutzt
// (eine Goroutine) – daher ohne Sperre.
type loadAvg struct {
	s []loadSample
}

// add nimmt einen Messwert auf und verwirft alles, was für das Fenster nicht
// mehr gebraucht wird. Der jüngste Wert VOR dem Fenster bleibt als Basis für
// die CPU-Differenz erhalten.
func (l *loadAvg) add(s loadSample) {
	l.s = append(l.s, s)
	cut := s.t.Add(-loadWindow)
	i := 0
	for i+1 < len(l.s) && !l.s[i+1].t.After(cut) {
		i++
	}
	l.s = l.s[i:]
}

// cpu = beschäftigte Zeit / Gesamtzeit zwischen ältestem und jüngstem Wert, in %.
// -1, solange es keine zwei gültigen Werte gibt.
func (l *loadAvg) cpu() float64 {
	var first, last *loadSample
	for i := range l.s {
		if !l.s[i].ok {
			continue
		}
		if first == nil {
			first = &l.s[i]
		}
		last = &l.s[i]
	}
	if first == nil || last == first || last.total <= first.total || last.busy < first.busy {
		return -1
	}
	return float64(last.busy-first.busy) * 100 / float64(last.total-first.total)
}

// ram = Mittelwert der RAM-Belegung über die Werte im Fenster, in %. -1 ohne Werte.
func (l *loadAvg) ram() float64 {
	if len(l.s) == 0 {
		return -1
	}
	cut := l.s[len(l.s)-1].t.Add(-loadWindow)
	var sum float64
	n := 0
	for _, s := range l.s {
		if s.t.After(cut) && s.memPct >= 0 {
			sum += s.memPct
			n++
		}
	}
	if n == 0 {
		return -1
	}
	return sum / float64(n)
}

// parseProcStat liest die Summenzeile "cpu  user nice system idle iowait irq softirq steal ..."
// busy = alles außer idle und iowait.
func parseProcStat(b string) (busy, total uint64, ok bool) {
	for _, line := range strings.Split(b, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v []uint64
		for _, x := range f[1:] {
			n, err := strconv.ParseUint(x, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			v = append(v, n)
		}
		// guest/guest_nice (Spalte 9/10) sind in user/nice schon enthalten
		if len(v) > 8 {
			v = v[:8]
		}
		for _, n := range v {
			total += n
		}
		idle := v[3]
		if len(v) > 4 {
			idle += v[4] // iowait
		}
		return total - idle, total, true
	}
	return 0, 0, false
}

// parseMemInfo liefert MemTotal und MemAvailable in kB.
func parseMemInfo(b string) (total, avail int64) {
	for _, line := range strings.Split(b, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = n
		case "MemAvailable:":
			avail = n
		}
	}
	return total, avail
}

// sampleLoad liest /proc/stat und /proc/meminfo, nimmt den Wert auf und
// schreibt die Mittelwerte in den Status.
func (a *App) sampleLoad(now time.Time, ns *Status) {
	s := loadSample{t: now, memPct: -1}
	if b, err := a.Sys.ReadFile("/proc/stat"); err == nil {
		s.busy, s.total, s.ok = parseProcStat(string(b))
	}
	if b, err := a.Sys.ReadFile("/proc/meminfo"); err == nil {
		tot, av := parseMemInfo(string(b))
		if tot > 0 {
			s.memPct = float64(tot-av) * 100 / float64(tot)
			ns.MemTotalKB, ns.MemUsedKB = tot, tot-av
		}
	}
	a.load.add(s)
	ns.CPUPct, ns.RAMPct = round1(a.load.cpu()), round1(a.load.ram())
}

func round1(f float64) float64 {
	if f < 0 {
		return -1
	}
	return float64(int64(f*10+0.5)) / 10
}
