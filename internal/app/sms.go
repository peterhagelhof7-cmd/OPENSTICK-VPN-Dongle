package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

// SMS über die Weboberfläche: Der Stick holt empfangene SMS vom Modem in eine
// eigene Ablage (Flash) und löscht sie danach im Modem, damit der kleine
// SIM-/Modemspeicher nie voll läuft. Gesendete SMS landen ebenfalls dort.

const (
	SMSFile       = "/var/lib/vpndongle/sms.json"
	smsKeep       = 200 // älteste fliegen raus
	smsPollEvery  = 15 * time.Second
	smsEnableWait = time.Minute // Modem einschalten höchstens so oft versuchen
	SMSMaxText    = 640         // Zeichen (mehrteilige SMS, ca. 4–10 Teile)
	smsSendLimit  = 20          // Sendungen je Stunde (Schutz vor Missbrauch/Kosten)
)

// SMSMsg = eine SMS in der Ablage des Sticks.
type SMSMsg struct {
	ID     int64     `json:"id"`
	In     bool      `json:"in"` // true = empfangen, false = gesendet
	Number string    `json:"number"`
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
	Error  string    `json:"error,omitempty"` // nur gesendete: Fehler beim Senden
	Read   bool      `json:"read"`
}

var reSMSNumber = regexp.MustCompile(`^\+?[0-9]{3,20}$`)

// NormalizeNumber entfernt Leerzeichen, Bindestriche, Schrägstriche und Klammern.
func NormalizeNumber(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '/', '(', ')', '.':
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

// ValidNumber: internationale (+49…), nationale (0151…) oder Kurzwahl-Nummer.
func ValidNumber(s string) bool { return reSMSNumber.MatchString(s) }

// SMSActive: SMS-Seite und Abruf nur mit LTE und gesetztem Haken.
func SMSActive(c *config.Config) bool { return c.LTE.Enabled && c.LTE.SMS }

func (a *App) smsLoad() {
	if a.smsLoaded {
		return
	}
	a.smsLoaded = true
	b, err := a.Sys.ReadFile(SMSFile)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &a.smsList); err != nil {
		log.Printf("SMS-Ablage unlesbar, beginne leer: %v", err)
		a.smsList = nil
	}
	for _, m := range a.smsList {
		if m.ID > a.smsNextID {
			a.smsNextID = m.ID
		}
	}
}

// smsSave: Aufrufer hält smsMu.
func (a *App) smsSave() error {
	if len(a.smsList) > smsKeep {
		a.smsList = a.smsList[len(a.smsList)-smsKeep:]
	}
	b, err := json.Marshal(a.smsList)
	if err != nil {
		return err
	}
	return a.Sys.WriteFile(SMSFile, b, 0o600)
}

// smsAdd: Aufrufer hält smsMu.
func (a *App) smsAdd(m SMSMsg) {
	a.smsNextID++
	m.ID = a.smsNextID
	a.smsList = append(a.smsList, m)
}

// smsHave: liegt diese empfangene SMS (Absender, Text, Zeitstempel der Zentrale) schon in
// der Ablage? Aufrufer hält smsMu.
func (a *App) smsHave(m SMSMsg) bool {
	for _, x := range a.smsList {
		if x.In && x.Number == m.Number && x.Text == m.Text && x.Time.Equal(m.Time) {
			return true
		}
	}
	return false
}

// smsPoll holt empfangene SMS vom Modem. Aus tick, nur wenn SMS aktiv.
func (a *App) smsPoll(s Status, now time.Time) {
	if now.Sub(a.smsLastPoll) < smsPollEvery {
		return
	}
	a.smsLastPoll = now
	if !s.LTE.Present {
		return
	}
	// Ohne eingeschaltetes Modem kommen keine SMS an. NetworkManager schaltet es
	// erst mit einer LTE-Verbindung ein, bei WLAN-Betrieb also womöglich nie.
	if s.LTE.State == "disabled" && now.Sub(a.smsEnableTry) >= smsEnableWait {
		a.smsEnableTry = now
		if _, err := a.Sys.Run("mmcli", "-m", "any", "-e"); err != nil {
			log.Printf("SMS: Modem einschalten: %v", err)
		}
		return
	}
	out, err := a.Sys.Run("mmcli", "-m", "any", "--messaging-list-sms", "-J")
	if err != nil {
		return
	}
	var got []system.ModemSMS
	var paths []string
	for _, p := range system.ParseSMSList(out) {
		o, err := a.Sys.Run("mmcli", "-s", p, "-J")
		if err != nil {
			continue
		}
		m, ok := system.ParseSMS(o)
		if !ok || !m.Received() {
			continue // Mehrteil-SMS noch unvollständig, Statusbericht, eigene Entwürfe
		}
		got = append(got, m)
		paths = append(paths, p)
	}
	if len(got) == 0 {
		return
	}
	a.smsMu.Lock()
	a.smsLoad()
	added := 0
	for _, m := range got {
		t := m.Time
		if t.IsZero() {
			t = now
		}
		in := SMSMsg{In: true, Number: m.Number, Text: m.Text, Time: t}
		// Dieselbe SMS nur einmal: ModemManager führte eine SMS als zwei Objekte, und
		// scheitert das Löschen im Modem, kommt sie beim nächsten Abruf wieder (UZ801).
		if !m.Time.IsZero() && a.smsHave(in) {
			continue
		}
		a.smsAdd(in)
		added++
	}
	if added > 0 {
		err = a.smsSave()
	}
	a.smsMu.Unlock()
	if err != nil {
		// Erst sicher in der eigenen Ablage, dann im Modem löschen
		log.Printf("SMS-Ablage schreiben: %v – SMS bleiben im Modem", err)
		return
	}
	for _, p := range paths {
		if _, err := a.Sys.Run("mmcli", "-m", "any", "--messaging-delete-sms="+p); err != nil {
			// Bleibt eine SMS im Modem hängen, kommt sie alle 15 s wieder: nur einmal melden.
			if !a.smsDelFailed[p] {
				log.Printf("SMS im Modem löschen: %v", err)
			}
			a.smsDelFailed[p] = true
		} else {
			delete(a.smsDelFailed, p)
		}
	}
	switch dup := len(got) - added; {
	case added == 0:
		// nur schon bekannte (im Modem hängengeblieben) – kein Eintrag alle 15 s
	case dup > 0:
		log.Printf("SMS: %d neue empfangen, %d schon in der Ablage", added, dup)
	default:
		log.Printf("SMS: %d neue empfangen", added)
	}
}

// SMSMessages liefert die Ablage, neueste zuerst.
func (a *App) SMSMessages() []SMSMsg {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.smsLoad()
	out := append([]SMSMsg(nil), a.smsList...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// SMSUnread = Zahl ungelesener empfangener SMS.
func (a *App) SMSUnread() int {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.smsLoad()
	n := 0
	for _, m := range a.smsList {
		if m.In && !m.Read {
			n++
		}
	}
	return n
}

// SMSMarkRead markiert alle empfangenen SMS als gelesen (beim Öffnen der Seite).
func (a *App) SMSMarkRead() {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.smsLoad()
	changed := false
	for i := range a.smsList {
		if a.smsList[i].In && !a.smsList[i].Read {
			a.smsList[i].Read, changed = true, true
		}
	}
	if changed {
		if err := a.smsSave(); err != nil {
			log.Printf("SMS-Ablage schreiben: %v", err)
		}
	}
}

// SMSDelete löscht eine SMS (id > 0) oder alle (id == 0).
func (a *App) SMSDelete(id int64) error {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.smsLoad()
	if id == 0 {
		a.smsList = nil
		return a.smsSave()
	}
	for i, m := range a.smsList {
		if m.ID == id {
			a.smsList = append(a.smsList[:i], a.smsList[i+1:]...)
			return a.smsSave()
		}
	}
	return errors.New("SMS nicht gefunden")
}

// SMSSend sendet eine SMS über das Modem und legt sie in der Ablage ab – auch
// wenn das Senden scheitert (mit Fehler), damit der Nutzer es sieht.
func (a *App) SMSSend(number, text string) error {
	c := a.Store.Get()
	if !SMSActive(c) {
		return errors.New("SMS sind nicht eingeschaltet")
	}
	number = NormalizeNumber(number)
	if !ValidNumber(number) {
		return errors.New("ungültige Rufnummer (Ziffern, optional mit + am Anfang)")
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return errors.New("Text ist leer")
	}
	// Ungültige Bytes (z. B. Text im Windows-Zeichensatz per Skript) lehnt das Modem nur
	// mit „nicht angenommen“ ab – lieber gleich sagen, was los ist.
	if !utf8.ValidString(text) {
		return errors.New("Text ist kein gültiges UTF-8 (Zeichensatz prüfen)")
	}
	if n := utf8.RuneCountInString(text); n > SMSMaxText {
		return fmt.Errorf("Text zu lang (%d von max. %d Zeichen)", n, SMSMaxText)
	}
	if !a.Status().LTE.Present {
		return errors.New("kein Modem gefunden")
	}

	now := time.Now()
	a.smsMu.Lock()
	var recent []time.Time
	for _, t := range a.smsSent {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= smsSendLimit {
		a.smsSent = recent
		a.smsMu.Unlock()
		return fmt.Errorf("höchstens %d SMS pro Stunde – bitte später erneut versuchen", smsSendLimit)
	}
	a.smsSent = append(recent, now)
	a.smsMu.Unlock()

	sendErr := a.smsModemSend(number, text)

	a.smsMu.Lock()
	a.smsLoad()
	m := SMSMsg{In: false, Number: number, Text: text, Time: now, Read: true}
	if sendErr != nil {
		m.Error = sendErr.Error()
	}
	a.smsAdd(m)
	saveErr := a.smsSave()
	a.smsMu.Unlock()
	if saveErr != nil {
		log.Printf("SMS-Ablage schreiben: %v", saveErr)
	}
	return sendErr
}

// smsSendTries/smsRetryWait: Der erste Versuch scheiterte am UZ801 einmal mit
// „WmsMessageDeliveryFailure“ (mmcli: Timeout), der zweite kurz danach ging (26.11.2026).
// Nur ein Wiederholen: Meldet das Modem einen Fehler, obwohl die SMS doch rausging, käme
// sie sonst mehrfach an.
const (
	smsSendTries = 2
	smsRetryWait = 10 * time.Second
)

func (a *App) smsModemSend(number, text string) error {
	var err error
	for i := 1; i <= smsSendTries; i++ {
		if i > 1 {
			log.Printf("SMS senden: Versuch %d in %s", i, smsRetryWait)
			a.sleep(smsRetryWait)
		}
		var created bool
		if created, err = a.smsModemSendOnce(number, text); err == nil || !created {
			return err // geschafft, oder das Modem nahm sie gar nicht an (kein Netzproblem)
		}
	}
	return err
}

// smsModemSendOnce legt die SMS im Modem an und sendet sie. created = angelegt (ein Fehler
// danach ist einer beim Senden und lohnt einen zweiten Versuch).
func (a *App) smsModemSendOnce(number, text string) (created bool, err error) {
	out, err := a.Sys.Run("mmcli", "-m", "any", system.SMSCreateArg(number, text))
	if err != nil {
		return false, errors.New("Modem hat die SMS nicht angenommen")
	}
	p := system.ParseCreatedSMS(out)
	if p == "" {
		if a.Sys.DryRun() {
			return false, nil
		}
		return false, errors.New("Modem hat die SMS nicht angelegt")
	}
	_, err = a.Sys.Run("mmcli", "-s", p, "--send")
	// Entwurf/Gesendetes im Modem nicht aufheben – die Ablage ist im Stick
	if _, derr := a.Sys.Run("mmcli", "-m", "any", "--messaging-delete-sms="+p); derr != nil {
		log.Printf("gesendete SMS im Modem löschen: %v", derr)
	}
	if err != nil {
		log.Printf("SMS senden: %v", err)
		return true, errors.New("Senden fehlgeschlagen (Netz, Guthaben oder Nummer prüfen)")
	}
	return true, nil
}
