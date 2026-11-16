package app

import "testing"

// Werte wie am UZ801 (2026-11-16)
func seedThermal(fs *fakeSys) {
	zones := []struct{ typ, temp, t0, t0type string }{
		{"modem-thermal", "54000", "85000", "hot"},
		{"camera-thermal", "53000", "75000", "hot"},
		{"gpu-thermal", "52000", "75000", "passive"},
		{"cpu2-3-thermal", "53000", "75000", "passive"},
		{"cpu0-1-thermal", "61500", "75000", "passive"},
	}
	for i, z := range zones {
		d := "/sys/class/thermal/thermal_zone" + string(rune('0'+i))
		fs.files[d+"/type"] = z.typ + "\n"
		fs.files[d+"/temp"] = z.temp + "\n"
		fs.files[d+"/trip_point_0_temp"] = z.t0 + "\n"
		fs.files[d+"/trip_point_0_type"] = z.t0type + "\n"
	}
}

func TestSampleThermal(t *testing.T) {
	a, fs := newTestApp(t)
	seedThermal(fs)
	var s Status
	a.sampleThermal(&s)
	if s.CPUTemp != 61.5 || s.ModemTemp != 54 || s.ThrottleAt != 75 {
		t.Fatalf("CPU %.1f Modem %.1f Drossel %.1f", s.CPUTemp, s.ModemTemp, s.ThrottleAt)
	}
	if len(s.Temps) != 5 || s.Temps["gpu"] != 52 || s.Temps["cpu2-3"] != 53 {
		t.Fatalf("Zonen: %v", s.Temps)
	}
	// unplausibler Wert wird ignoriert
	fs.files["/sys/class/thermal/thermal_zone4/temp"] = "-273000\n"
	a.sampleThermal(&s)
	if s.CPUTemp != 53 {
		t.Fatalf("unplausibler Wert nicht ignoriert: %.1f", s.CPUTemp)
	}
	// ohne Fühler: -1 (Anzeige entfällt)
	b, _ := newTestApp(t)
	var e Status
	b.sampleThermal(&e)
	if e.CPUTemp != -1 || e.ModemTemp != -1 || e.Temps != nil {
		t.Fatalf("ohne Fühler: %+v", e)
	}
}
