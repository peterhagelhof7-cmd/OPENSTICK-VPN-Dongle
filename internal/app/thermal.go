package app

import (
	"path"
	"strconv"
	"strings"
)

// Temperaturen aus /sys/class/thermal (MSM8916: tsens-Fühler im Chip –
// cpu0-1, cpu2-3, gpu, modem, camera). Werte in Milligrad.
const thermalGlob = "/sys/class/thermal/thermal_zone*/type"

func (a *App) readMilliC(p string) (float64, bool) {
	b, err := a.Sys.ReadFile(p)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < -40000 || n > 200000 { // unplausibel -> ignorieren
		return 0, false
	}
	return float64(n) / 1000, true
}

// sampleThermal füllt CPU-/Modem-Temperatur, alle Zonen und die Drosselgrenze.
func (a *App) sampleThermal(ns *Status) {
	ns.CPUTemp, ns.ModemTemp, ns.ThrottleAt, ns.Temps = -1, -1, 0, nil
	types, _ := a.Sys.Glob(thermalGlob)
	for _, tf := range types {
		dir := path.Dir(strings.ReplaceAll(tf, "\\", "/"))
		b, err := a.Sys.ReadFile(dir + "/type")
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(b)), "-thermal")
		t, ok := a.readMilliC(dir + "/temp")
		if !ok || name == "" {
			continue
		}
		if ns.Temps == nil {
			ns.Temps = map[string]float64{}
		}
		ns.Temps[name] = t
		switch {
		case strings.HasPrefix(name, "cpu"):
			if t > ns.CPUTemp {
				ns.CPUTemp = t
			}
			// Drosselgrenze: erster „passive“-Auslösepunkt einer CPU-Zone
			for i := 0; i < 4; i++ {
				tp, err := a.Sys.ReadFile(dir + "/trip_point_" + strconv.Itoa(i) + "_type")
				if err != nil {
					break
				}
				if strings.TrimSpace(string(tp)) == "passive" {
					if v, ok := a.readMilliC(dir + "/trip_point_" + strconv.Itoa(i) + "_temp"); ok && (ns.ThrottleAt == 0 || v < ns.ThrottleAt) {
						ns.ThrottleAt = v
					}
					break
				}
			}
		case name == "modem":
			ns.ModemTemp = t
		}
	}
}
