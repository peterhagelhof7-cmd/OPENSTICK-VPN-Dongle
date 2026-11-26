package app

import (
	"crypto/sha256"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/render"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

// Modem-Pflege, aus dem SIM-Test am UZ801 (26.11.2026):
//
//  1. PIN1: NetworkManager gibt die PIN nur beim Aktivieren des LTE-Profils ein. Im Modus
//     „nur WLAN“ (oder nach einem Aus/Ein des Modems) bleibt die SIM gesperrt – ohne
//     Einbuchen kommen keine SMS. Der Dienst entsperrt deshalb selbst, mit der
//     konfigurierten PIN, höchstens einmal je PIN, nie mit dem letzten Versuch.
//  2. Einbuchungs-APN: Die Werks-Firmware bucht mit Profil „qdp_profile“ (APN „ctlte“,
//     China Telecom) ein. Das Netz baut dabei schon die Standard-Verbindung auf; das
//     LTE-Profil mit demselben APN scheitert dann endlos an
//     „multiple-connection-to-same-pdn-not-allowed“. ModemManager kann das Profil hier
//     nicht ändern (QMI 81 invalid-parameter-length), qmicli schon. Hat genau ein Profil
//     einen fremden APN und keines den konfigurierten, bekommt es den konfigurierten
//     APN; danach bucht das Modem neu ein (aus/ein).
const (
	pinMinRetries    = 2                // so viele Versuche müssen übrig sein; der letzte bleibt dem Menschen
	pinRetryEvery    = time.Minute      // Abstand zwischen zwei Entsperr-Versuchen
	attachRetryEvery = 30 * time.Minute // Abstand nach einem gescheiterten Profil-Abgleich
)

// apnOK: nur, was in einen qmicli-Parameter passt (keine Kommas, Leerzeichen, Anführungszeichen).
var apnOK = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]{0,99}$`)

func pinKey(pin string) [32]byte { return sha256.Sum256([]byte("pin:" + pin)) }

// modemCare läuft in jedem tick, solange LTE eingerichtet ist und ein Modem da ist.
// Die Aktionen laufen im Hintergrund, immer nur eine gleichzeitig.
func (a *App) modemCare(c *config.Config, s system.LTEStatus, now time.Time) {
	if !c.LTE.Enabled || !s.Present || a.modemBusy.Load() {
		return
	}
	if a.pinUnlockDue(c, s, now) {
		pin, path := c.LTE.PIN, s.SIMPath
		a.modemAsync(func() { a.unlockSIM(pin, path) })
		return
	}
	if a.attachCheckDue(c, s, now) {
		apn, port, lteUplink := c.LTE.APN, s.QMIPort, c.Uplink == config.UplinkLTE
		a.modemAsync(func() { a.fixAttachProfile(apn, port, lteUplink) })
	}
}

func (a *App) modemAsync(fn func()) {
	a.modemBusy.Store(true)
	go func() {
		defer a.modemBusy.Store(false)
		fn()
	}()
}

// pinUnlockDue: SIM wartet auf PIN1, PIN ist konfiguriert, genug Versuche übrig und diese
// PIN ist noch nicht gescheitert.
func (a *App) pinUnlockDue(c *config.Config, s system.LTEStatus, now time.Time) bool {
	if s.Unlock != "sim-pin" || c.LTE.PIN == "" || s.SIMPath == "" {
		return false
	}
	a.modemMu.Lock()
	defer a.modemMu.Unlock()
	if a.pinFailed == pinKey(c.LTE.PIN) || now.Sub(a.pinLastTry) < pinRetryEvery {
		return false
	}
	if s.PINRetries >= 0 && s.PINRetries < pinMinRetries {
		if !a.pinWarned {
			a.pinWarned = true
			a.setErr(fmt.Errorf("SIM: nur noch %d PIN-Versuch(e) – automatisches Entsperren aus, PIN von Hand prüfen", s.PINRetries))
		}
		return false
	}
	a.pinLastTry = now
	return true
}

func (a *App) unlockSIM(pin, simPath string) {
	log.Printf("SIM: gebe PIN ein")
	// "--pin" und PIN als getrennte Argumente: redact() verdeckt den Wert im Fehlertext.
	_, err := a.Sys.Run("mmcli", "-i", simPath, "--pin", pin)
	a.modemMu.Lock()
	defer a.modemMu.Unlock()
	if err != nil {
		// Jeder Fehler zählt als „PIN falsch“: lieber von Hand nachsehen als Versuche verbrennen.
		a.pinFailed = pinKey(pin)
		a.setErr(fmt.Errorf("SIM entsperren fehlgeschlagen, kein weiterer Versuch mit dieser PIN: %v", err))
		return
	}
	a.pinFailed = [32]byte{}
	a.pinWarned = false
	log.Printf("SIM: entsperrt")
}

// attachCheckDue: Modem entsperrt, QMI-Port bekannt, APN brauchbar, noch nicht für diesen APN
// geprüft und nicht gerade verbunden (dann ist nichts zu reparieren).
func (a *App) attachCheckDue(c *config.Config, s system.LTEStatus, now time.Time) bool {
	if c.LTE.APN == "" || s.Unlock != "" || s.QMIPort == "" || !apnOK.MatchString(c.LTE.APN) {
		return false
	}
	switch s.State {
	case "connected", "connecting", "disconnecting", "locked", "disabled", "disabling", "enabling", "initializing", "failed":
		return false
	}
	a.modemMu.Lock()
	defer a.modemMu.Unlock()
	if strings.EqualFold(a.attachDone, c.LTE.APN) || now.Sub(a.attachLastTry) < attachRetryEvery {
		return false
	}
	a.attachLastTry = now
	return true
}

// attachPlan entscheidet anhand der Profile: nichts zu tun (ok, idx 0), Profil idx auf den APN
// setzen, oder nicht eindeutig (msg sagt warum, nichts ändern).
func attachPlan(ps []system.QMIProfile, apn string) (idx int, ok bool, msg string) {
	var foreign []system.QMIProfile
	for _, p := range ps {
		if strings.EqualFold(p.APN, apn) {
			return 0, true, ""
		}
		if p.APN != "" {
			foreign = append(foreign, p)
		}
	}
	switch len(foreign) {
	case 0:
		return 0, true, "" // nur leere Profile: das Netz vergibt den APN selbst
	case 1:
		return foreign[0].Index, true, ""
	}
	var names []string
	for _, p := range foreign {
		names = append(names, fmt.Sprintf("%d %s (%s)", p.Index, p.Name, p.APN))
	}
	return 0, false, "mehrere Profile mit fremdem APN, nichts geändert: " + strings.Join(names, ", ")
}

func (a *App) fixAttachProfile(apn, port string, lteUplink bool) {
	dev := "/dev/" + port
	out, err := a.Sys.Run("qmicli", "-p", "-d", dev, "--wds-get-profile-list=3gpp")
	if err != nil {
		log.Printf("Modem-Profile: %v (Paket libqmi-utils installiert?)", err)
		return
	}
	idx, ok, msg := attachPlan(system.ParseQMIProfiles(out), apn)
	done := func() {
		a.modemMu.Lock()
		a.attachDone = apn
		a.modemMu.Unlock()
	}
	switch {
	case !ok:
		log.Printf("Modem-Profile: %s", msg)
		done() // nicht alle 30 min wiederholen; neuer APN = neue Prüfung
		return
	case idx == 0:
		done()
		return
	}
	log.Printf("Modem-Profile: setze APN von Profil %d auf %q (Einbuchen mit fremdem APN)", idx, apn)
	if _, err := a.Sys.Run("qmicli", "-p", "-d", dev, fmt.Sprintf("--wds-modify-profile=3gpp,%d,apn=%s", idx, apn)); err != nil {
		a.setErr(fmt.Errorf("Modem-Profil %d ändern: %v", idx, err))
		return
	}
	done()
	// Neu einbuchen, damit das Netz die Standard-Verbindung mit dem neuen APN aufbaut.
	// --disable/--enable reicht nicht: Das Modem blieb eingebucht (nach 1 s wieder
	// „registered“, weiter mit altem APN). Funk aus/an über den Energiezustand bucht
	// wirklich neu ein (UZ801, 26.11.2026). Fragt die SIM danach wieder nach der PIN,
	// übernimmt das der nächste tick.
	if _, err := a.Sys.Run("mmcli", "-m", "any", "--set-power-state-low"); err != nil {
		a.setErr(fmt.Errorf("Modem Funk aus (neu einbuchen): %v", err))
	}
	a.sleep(reattachPause)
	if _, err := a.Sys.Run("mmcli", "-m", "any", "--set-power-state-on"); err != nil {
		a.setErr(fmt.Errorf("Modem Funk an (neu einbuchen): %v", err))
		return
	}
	// Uplink LTE: gleich verbinden. NetworkManager hat nach den gescheiterten Versuchen
	// sonst bis zu 5 min Pause (autoconnect-retries=3). Im Modus „auto“ übernimmt das
	// der LTE-Rückfall, falls WLAN fehlt.
	if lteUplink {
		a.sleep(reconnectPause) // Einbuchen dauert am UZ801 ~10 s
		log.Printf("Modem-Profile: neu eingebucht, verbinde LTE")
		if _, err := a.Sys.Run("nmcli", "--wait", "60", "connection", "up", render.LTEConnName); err != nil {
			log.Printf("LTE nach Neu-Einbuchen: %v (NetworkManager versucht es später selbst)", err)
		}
	}
}

// reconnectPause: nach Funk an, bevor LTE verbunden wird.
const reconnectPause = 15 * time.Second

// reattachPause: Funk aus, bevor er wieder angeht (Tests setzen a.sleep auf no-op).
const reattachPause = 5 * time.Second
