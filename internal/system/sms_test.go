package system

import (
	"strings"
	"testing"
	"time"
)

func TestParseSMSList(t *testing.T) {
	out := `{"modem.messaging.sms":["/org/freedesktop/ModemManager1/SMS/0","/org/freedesktop/ModemManager1/SMS/3"]}`
	l := ParseSMSList(out)
	if len(l) != 2 || l[1] != "/org/freedesktop/ModemManager1/SMS/3" {
		t.Fatalf("%v", l)
	}
	if ParseSMSList(`{"modem.messaging.sms":[]}`) != nil || ParseSMSList("kaputt") != nil {
		t.Fatal("leer/kaputt muss nil liefern")
	}
}

func TestParseSMS(t *testing.T) {
	out := `{"sms":{"content":{"data":"--","number":"+4915112345678","text":"Ihr Code: 123456\nGültig 5 min"},` +
		`"dbus-path":"/org/freedesktop/ModemManager1/SMS/0","properties":{"class":"--","pdu-type":"deliver",` +
		`"smsc":"+491770610000","state":"received","storage":"me","timestamp":"2026-11-20T10:11:12+01:00"}}}`
	m, ok := ParseSMS(out)
	if !ok || !m.Received() || m.Number != "+4915112345678" || !strings.Contains(m.Text, "Gültig") {
		t.Fatalf("%+v", m)
	}
	if want := time.Date(2026, 11, 20, 9, 11, 12, 0, time.UTC); !m.Time.Equal(want) {
		t.Fatalf("Zeit %v", m.Time)
	}
	// Teil einer Mehrteil-SMS
	m, _ = ParseSMS(strings.Replace(out, `"state":"received"`, `"state":"receiving"`, 1))
	if m.Received() {
		t.Fatal("receiving darf nicht als empfangen gelten")
	}
	// eigener Entwurf
	m, _ = ParseSMS(strings.Replace(out, `"pdu-type":"deliver"`, `"pdu-type":"submit"`, 1))
	if m.Received() {
		t.Fatal("submit darf nicht als empfangen gelten")
	}
	// Zeitzone nur mit Stunden
	m, _ = ParseSMS(strings.Replace(out, "+01:00", "+01", 1))
	if m.Time.IsZero() {
		t.Fatal("Zeit +01 nicht gelesen")
	}
	if _, ok := ParseSMS("{}"); ok {
		t.Fatal("leeres JSON")
	}
}

func TestParseOwnNumber(t *testing.T) {
	out := `{"modem":{"generic":{"state":"registered","own-numbers":["+4915199999999"],"signal-quality":{"value":"50"}},"3gpp":{}}}`
	if s := ParseMMCLI(out); s.Number != "+4915199999999" {
		t.Fatalf("%+v", s)
	}
	out = `{"modem":{"generic":{"state":"registered","own-numbers":[],"signal-quality":{"value":"50"}},"3gpp":{}}}`
	if s := ParseMMCLI(out); s.Number != "" {
		t.Fatalf("%+v", s)
	}
}

func TestSMSCreateArg(t *testing.T) {
	cases := []struct{ text, want string }{
		{"Hallo, Welt", `--messaging-create-sms=number='+491511',text='Hallo, Welt'`},
		{"geht's?", `--messaging-create-sms=number="+491511",text="geht's?"`},
		{`geht's "so"?`, `--messaging-create-sms=number="+491511",text="geht's ”so”?"`},
	}
	for _, c := range cases {
		if got := SMSCreateArg("+491511", c.text); got != c.want {
			t.Errorf("%q: %s", c.text, got)
		}
	}
	if p := ParseCreatedSMS("Successfully created new SMS: /org/freedesktop/ModemManager1/SMS/7\n"); p != "/org/freedesktop/ModemManager1/SMS/7" {
		t.Fatal(p)
	}
}

func TestRedactSMS(t *testing.T) {
	r := redact([]string{"-m", "any", SMSCreateArg("+491511", "geheimer Code 1234")})
	if strings.Contains(strings.Join(r, " "), "1234") || strings.Contains(strings.Join(r, " "), "+491511") {
		t.Fatalf("%v", r)
	}
}
