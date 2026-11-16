package app

import (
	"fmt"
	"log"
	"time"
)

// Routenwechsel sofort und sauber zum PC bringen: Windows übernimmt neue
// DHCP-Optionen (Option 121) erst bei der nächsten Lease-Erneuerung – nach einem
// VPN-Wechsel bis zu ~5 min mit alten Routen. Nur „Link ab/an“ (ip link) reicht
// nicht: Windows ergänzt dann die neuen Routen, behält aber die alten (am UZ801
// gemessen 2026-11-16). Darum wird das USB-Gadget kurz vom Controller gelöst –
// wie Ab- und Anstecken: Windows baut den Adapter neu auf, die alten Routen sind
// weg, per DHCP kommen genau die aktuellen (gemessen: ~3,5 s bis wieder IP).
var (
	bounceDelay = 3 * time.Second // Ruhezeit: mehrere Änderungen zusammenfassen
	bounceDown  = 2 * time.Second // so lange ist das Gadget „abgesteckt“
)

// GadgetScript: USB-Gadget an-/abmelden (system/usb-gadget.sh).
const GadgetScript = "/usr/local/lib/vpndongle/usb-gadget.sh"

// notePushRoutes merkt sich die per DHCP verteilten Routen und stößt bei einer
// Änderung (nicht beim ersten Mal nach dem Start) das Wieder-Einstecken an.
func (a *App) notePushRoutes(key string) {
	a.bounceMu.Lock()
	defer a.bounceMu.Unlock()
	if !a.pushInit {
		a.pushInit, a.pushKey = true, key
		return
	}
	if key == a.pushKey {
		return
	}
	a.pushKey = key
	if a.bounceTimer == nil {
		a.bounceTimer = time.AfterFunc(bounceDelay, a.bounceUSB)
	} else {
		a.bounceTimer.Reset(bounceDelay)
	}
}

// bounceUSB: Gadget kurz lösen und wieder anmelden. usb-gadget.sh start stellt
// Namen (usb0) und Adresse wieder her. Währenddessen gilt usb0 als vorhanden
// (kein SSH-Notfallmodus für die paar Sekunden).
func (a *App) bounceUSB() {
	if !a.usbPresent() {
		return
	}
	a.bouncing.Store(true)
	defer a.bouncing.Store(false)
	log.Printf("Routen für den PC geändert – USB-Netz kurz neu angemeldet, damit der PC sie sofort per DHCP holt")
	if _, err := a.Sys.Run(GadgetScript, "stop"); err != nil {
		a.setErr(fmt.Errorf("USB-Netz lösen: %w", err))
	}
	time.Sleep(bounceDown)
	if _, err := a.Sys.Run(GadgetScript, "start"); err != nil {
		a.setErr(fmt.Errorf("USB-Netz wieder anmelden: %w", err))
	}
}
