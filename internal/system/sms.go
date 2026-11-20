package system

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

// SMS über ModemManager (mmcli). Empfangene SMS setzt ModemManager aus
// mehrteiligen Nachrichten selbst zusammen; erst dann steht state=received.

// ParseSMSList liest `mmcli -m any --messaging-list-sms -J`.
func ParseSMSList(out string) []string {
	var raw struct {
		List []string `json:"modem.messaging.sms"`
	}
	if json.Unmarshal([]byte(out), &raw) != nil {
		return nil
	}
	var paths []string
	for _, p := range raw.List {
		if p = strings.TrimSpace(p); p != "" && p != "--" {
			paths = append(paths, p)
		}
	}
	return paths
}

// ModemSMS = eine SMS aus `mmcli -s <pfad> -J`.
type ModemSMS struct {
	Number  string
	Text    string
	State   string // received, receiving, stored, sending, sent, unknown
	PDUType string // deliver (empfangen), submit (gesendet), status-report
	Time    time.Time
}

// Received: vollständig empfangene SMS (nicht Teil einer noch laufenden Mehrteil-SMS).
func (m ModemSMS) Received() bool { return m.PDUType == "deliver" && m.State == "received" }

func ParseSMS(out string) (ModemSMS, bool) {
	var raw struct {
		SMS struct {
			Content struct {
				Number string `json:"number"`
				Text   string `json:"text"`
			} `json:"content"`
			Properties struct {
				State     string `json:"state"`
				PDUType   string `json:"pdu-type"`
				Timestamp string `json:"timestamp"`
			} `json:"properties"`
		} `json:"sms"`
	}
	if json.Unmarshal([]byte(out), &raw) != nil || raw.SMS.Properties.State == "" {
		return ModemSMS{}, false
	}
	c, p := raw.SMS.Content, raw.SMS.Properties
	m := ModemSMS{Number: dash(c.Number), Text: dash(c.Text), State: p.State, PDUType: p.PDUType}
	m.Time = parseSMSTime(dash(p.Timestamp))
	return m, true
}

func dash(s string) string {
	if s == "--" {
		return ""
	}
	return s
}

// parseSMSTime: ModemManager liefert z. B. "2026-11-20T10:11:12+01:00", je nach
// Modem auch "+01" oder ohne Zone. Nicht lesbar -> Nullzeit (Aufrufer nimmt "jetzt").
func parseSMSTime(s string) time.Time {
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04:05-07", "2006-01-02T15:04:05Z07", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

var reSMSPath = regexp.MustCompile(`/org/freedesktop/ModemManager1/SMS/[0-9]+`)

// ParseCreatedSMS liest den Pfad aus "Successfully created new SMS: /org/.../SMS/3".
func ParseCreatedSMS(out string) string { return reSMSPath.FindString(out) }

// SMSCreateArg baut das Argument für `mmcli --messaging-create-sms=...`.
// mmcli trennt key=value-Paare an Kommas und kennt Anführungszeichen, aber kein
// Escape. Deshalb wird ein Anführungszeichen gewählt, das im Text nicht vorkommt;
// enthält der Text beide, wird " durch ” ersetzt.
func SMSCreateArg(number, text string) string {
	q := "'"
	if strings.Contains(text, "'") {
		q = `"`
		text = strings.ReplaceAll(text, `"`, "”")
	}
	return "--messaging-create-sms=number=" + q + number + q + ",text=" + q + text + q
}
