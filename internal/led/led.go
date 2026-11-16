// Package led steuert die Status-LEDs über /sys/class/leds.
// Gleiche Bedeutung wie beim ESP: rot = kein Uplink, blau = Uplink ohne VPN,
// grün = VPN steht. LTE als Uplink: blau blinkt.
//
// Die LED-Namen des UZ801 sind je nach Device-Tree verschieden (z. B.
// "red:os", "green:internet", "blue:wifi"); deshalb Suche nach Farbname.
// TODO(HW): Namen am echten Stick prüfen (ls /sys/class/leds).
package led

import (
	"path"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

const base = "/sys/class/leds"

type Color int

const (
	Off Color = iota
	Red
	Blue
	BlueBlink
	Green
)

type LEDs struct {
	sys              system.Sys
	red, green, blue string // Verzeichnisse unter /sys/class/leds
	last             Color
	init             bool
}

// Find sucht die LEDs anhand der Farbe im Namen.
func Find(s system.Sys) *LEDs {
	l := &LEDs{sys: s, last: -1}
	dirs, _ := s.Glob(base + "/*")
	for _, d := range dirs {
		n := strings.ToLower(path.Base(d))
		switch {
		case strings.Contains(n, "red") && l.red == "":
			l.red = d
		case strings.Contains(n, "green") && l.green == "":
			l.green = d
		case strings.Contains(n, "blue") && l.blue == "":
			l.blue = d
		}
	}
	return l
}

func (l *LEDs) Names() []string { return []string{l.red, l.green, l.blue} }

func (l *LEDs) write(dir, file, val string) {
	if dir != "" {
		_ = l.sys.WriteFile(dir+"/"+file, []byte(val), 0o644)
	}
}

// Set schaltet die Farbe (nur bei Änderung).
func (l *LEDs) Set(c Color) {
	if c == l.last {
		return
	}
	l.last = c
	for _, d := range []string{l.red, l.green, l.blue} {
		l.write(d, "trigger", "none")
		l.write(d, "brightness", "0")
	}
	switch c {
	case Red:
		l.write(l.red, "brightness", "1")
	case Green:
		l.write(l.green, "brightness", "1")
	case Blue:
		l.write(l.blue, "brightness", "1")
	case BlueBlink:
		l.write(l.blue, "trigger", "timer") // Kernel blinkt selbst (500/500 ms)
	}
}
