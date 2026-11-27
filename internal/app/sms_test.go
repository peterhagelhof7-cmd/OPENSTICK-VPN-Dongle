package app

import (
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

const smsPath = "/org/freedesktop/ModemManager1/SMS/"

func smsJSON(state, pdu, text string) string {
	return `{"sms":{"content":{"number":"+4915112345678","text":"` + text + `"},"properties":{"pdu-type":"` + pdu +
		`","state":"` + state + `","timestamp":"2026-11-20T10:11:12+01:00"}}}`
}

func smsApp(t *testing.T) (*App, *fakeSys) {
	a, fs := newTestApp(t)
	if err := a.Update(func(c *config.Config) error { c.LTE.Enabled, c.LTE.SMS = true, true; return nil }); err != nil {
		t.Fatal(err)
	}
	a.status.LTE = system.LTEStatus{Present: true, State: "registered"}
	fs.take()
	return a, fs
}

func hasCmd(cmds []string, sub string) bool {
	for _, c := range cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestSMSPollReceive(t *testing.T) {
	a, fs := smsApp(t)
	fs.out["mmcli -m any --messaging-list-sms"] = `{"modem.messaging.sms":["` + smsPath + `0","` + smsPath + `1","` + smsPath + `2"]}`
	fs.out["mmcli -s "+smsPath+"0"] = smsJSON("received", "deliver", "Code 123456")
	fs.out["mmcli -s "+smsPath+"1"] = smsJSON("receiving", "deliver", "Teil 1")
	fs.out["mmcli -s "+smsPath+"2"] = smsJSON("sent", "submit", "alt")

	a.smsPoll(a.Status(), time.Now())
	cmds := fs.take()
	l := a.SMSMessages()
	if len(l) != 1 || !l[0].In || l[0].Text != "Code 123456" || l[0].Read {
		t.Fatalf("%+v", l)
	}
	if !hasCmd(cmds, "--messaging-delete-sms="+smsPath+"0") || hasCmd(cmds, "--messaging-delete-sms="+smsPath+"1") {
		t.Fatalf("nur die vollständige SMS löschen: %v", cmds)
	}
	if a.SMSUnread() != 1 {
		t.Fatal("ungelesen")
	}
	// Ablage überlebt Neustart
	b := New(a.Store, fs, "test")
	if l := b.SMSMessages(); len(l) != 1 || l[0].Text != "Code 123456" {
		t.Fatalf("nach Neustart: %+v", l)
	}
	a.SMSMarkRead()
	if a.SMSUnread() != 0 {
		t.Fatal("gelesen")
	}
	// Intervall: sofort danach kein neuer Abruf
	a.smsPoll(a.Status(), time.Now())
	if len(fs.take()) != 0 {
		t.Fatal("Abruf trotz Intervall")
	}
}

func TestSMSPollKeepsOnSaveError(t *testing.T) {
	a, fs := smsApp(t)
	fs.out["mmcli -m any --messaging-list-sms"] = `{"modem.messaging.sms":["` + smsPath + `0"]}`
	fs.out["mmcli -s "+smsPath+"0"] = smsJSON("received", "deliver", "x")
	a.smsLoaded = true
	ws := &writeFailSys{fakeSys: fs}
	a.Sys = ws
	a.smsPoll(a.Status(), time.Now())
	if hasCmd(fs.take(), "--messaging-delete-sms") {
		t.Fatal("ohne gespeicherte Ablage nicht im Modem löschen")
	}
}

type writeFailSys struct{ *fakeSys }

func (w *writeFailSys) WriteFile(string, []byte, os.FileMode) error { return errors.New("Flash voll") }

func TestSMSPollEnablesModem(t *testing.T) {
	a, fs := smsApp(t)
	s := a.Status()
	s.LTE.State = "disabled"
	a.smsPoll(s, time.Now())
	if c := fs.take(); !hasCmd(c, "mmcli -m any -e") || hasCmd(c, "messaging") {
		t.Fatalf("%v", c)
	}
}

func TestSMSSend(t *testing.T) {
	a, fs := smsApp(t)
	fs.out["mmcli -m any --messaging-create-sms"] = "Successfully created new SMS: " + smsPath + "5\n"
	if err := a.SMSSend("+49 151 / 1234-5678", "Hallo"); err != nil {
		t.Fatal(err)
	}
	c := fs.take()
	if !hasCmd(c, "number='+4915112345678',text='Hallo'") || !hasCmd(c, "mmcli -s "+smsPath+"5 --send") || !hasCmd(c, "--messaging-delete-sms="+smsPath+"5") {
		t.Fatalf("%v", c)
	}
	l := a.SMSMessages()
	if len(l) != 1 || l[0].In || l[0].Error != "" || l[0].Number != "+4915112345678" {
		t.Fatalf("%+v", l)
	}

	// Senden scheitert -> trotzdem in der Ablage, mit Fehler
	fs.fail["mmcli -s "+smsPath+"5 --send"] = errors.New("timeout")
	if err := a.SMSSend("01511234567", "Zweite"); err == nil {
		t.Fatal("Fehler erwartet")
	}
	if l := a.SMSMessages(); len(l) != 2 || l[0].Error == "" {
		t.Fatalf("%+v", l)
	}
}

func TestSMSSendChecks(t *testing.T) {
	a, fs := smsApp(t)
	for _, c := range []struct{ num, text string }{
		{"abc", "x"}, {"12", "x"}, {"+491511234567", ""}, {"+491511234567", strings.Repeat("ä", SMSMaxText+1)},
		{"+491511234567", "Gr" + string([]byte{0xfc, 0xdf}) + "e"}, // cp1252 statt UTF-8
	} {
		if err := a.SMSSend(c.num, c.text); err == nil {
			t.Errorf("%q/%d Zeichen: Fehler erwartet", c.num, len(c.text))
		}
	}
	if len(fs.take()) != 0 {
		t.Fatal("ungültige Eingaben dürfen nichts ans Modem schicken")
	}
	// Limit je Stunde
	fs.out["mmcli -m any --messaging-create-sms"] = "Successfully created new SMS: " + smsPath + "1\n"
	for i := 0; i < smsSendLimit; i++ {
		if err := a.SMSSend("+491511234567", "x"); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := a.SMSSend("+491511234567", "x"); err == nil || !strings.Contains(err.Error(), "pro Stunde") {
		t.Fatalf("Limit: %v", err)
	}
	// ausgeschaltet
	_ = a.Update(func(c *config.Config) error { c.LTE.SMS = false; return nil })
	if err := a.SMSSend("+491511234567", "x"); err == nil {
		t.Fatal("ausgeschaltet muss scheitern")
	}
}

func TestSMSDelete(t *testing.T) {
	a, fs := smsApp(t)
	fs.out["mmcli -m any --messaging-create-sms"] = "Successfully created new SMS: " + smsPath + "1\n"
	for _, s := range []string{"a", "b", "c"} {
		_ = a.SMSSend("+491511234567", s)
	}
	l := a.SMSMessages()
	if err := a.SMSDelete(l[1].ID); err != nil {
		t.Fatal(err)
	}
	if l = a.SMSMessages(); len(l) != 2 || l[0].Text != "c" || l[1].Text != "a" {
		t.Fatalf("%+v", l)
	}
	if a.SMSDelete(999) == nil {
		t.Fatal("unbekannte ID")
	}
	_ = a.SMSDelete(0)
	if len(a.SMSMessages()) != 0 {
		t.Fatal("alle löschen")
	}
}

func TestResetWipesSMS(t *testing.T) {
	a, fs := smsApp(t)
	fs.out["mmcli -m any --messaging-create-sms"] = "Successfully created new SMS: " + smsPath + "1\n"
	_ = a.SMSSend("+491511234567", "x")
	if err := a.Reset("wifi"); err != nil || len(a.SMSMessages()) != 1 {
		t.Fatal("WLAN-Reset darf SMS nicht löschen")
	}
	if err := a.Reset("lte"); err != nil || len(a.SMSMessages()) != 0 {
		t.Fatal("LTE-Reset muss SMS löschen")
	}
}

// UZ801: dieselbe SMS als zwei Modem-Objekte, Löschen scheitert -> nur einmal in der Ablage.
func TestSMSPollNoDuplicates(t *testing.T) {
	a, fs := smsApp(t)
	fs.out["mmcli -m any --messaging-list-sms"] = `{"modem.messaging.sms":["` + smsPath + `3","` + smsPath + `4"]}`
	fs.out["mmcli -s "+smsPath+"3"] = smsJSON("received", "deliver", "Test2 ok")
	fs.out["mmcli -s "+smsPath+"4"] = smsJSON("received", "deliver", "Test2 ok")
	fs.fail["mmcli -m any --messaging-delete-sms="] = errors.New("Couldn't delete 1 parts from this SMS")
	now := time.Now()
	a.smsPoll(a.Status(), now)
	a.smsPoll(a.Status(), now.Add(smsPollEvery)) // liegt noch im Modem
	if l := a.SMSMessages(); len(l) != 1 || l[0].Text != "Test2 ok" {
		t.Fatalf("%d Einträge: %+v", len(l), l)
	}
	// gleicher Text, anderer Zeitstempel = neue SMS
	fs.out["mmcli -s "+smsPath+"4"] = strings.Replace(smsJSON("received", "deliver", "Test2 ok"), "10:11:12", "10:12:00", 1)
	a.smsPoll(a.Status(), now.Add(2*smsPollEvery))
	if l := a.SMSMessages(); len(l) != 2 {
		t.Fatalf("%d Einträge", len(l))
	}
}

// Erster Sendeversuch scheitert, zweiter geht: eine SMS, ohne Fehler.
func TestSMSSendRetry(t *testing.T) {
	a, fs := smsApp(t)
	fs.out["mmcli -m any --messaging-create-sms"] = "Successfully created new SMS: " + smsPath + "5\n"
	sends := 0
	fs.fail["mmcli -s "+smsPath+"5 --send"] = errors.New("Timeout was reached")
	fs.hook = func(c string) {
		if strings.HasSuffix(c, "--send") {
			sends++
			fs.mu.Lock()
			delete(fs.fail, "mmcli -s "+smsPath+"5 --send") // ab jetzt klappt es
			fs.mu.Unlock()
		}
	}
	if err := a.SMSSend("+4915112345678", "Grüße äöü ß€@"); err != nil {
		t.Fatal(err)
	}
	if sends != 2 {
		t.Fatalf("%d Sendeversuche", sends)
	}
	if l := a.SMSMessages(); len(l) != 1 || l[0].Error != "" || l[0].Text != "Grüße äöü ß€@" {
		t.Fatalf("%+v", l)
	}
	// Modem nimmt sie gar nicht an: kein zweiter Versuch
	b, fs2 := smsApp(t)
	fs2.fail["mmcli -m any --messaging-create-sms"] = errors.New("x")
	_ = b.SMSSend("+4915112345678", "x")
	n := 0
	for _, c := range fs2.take() {
		if strings.Contains(c, "--messaging-create-sms") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d Anlegeversuche", n)
	}
}

// Hängt eine schon gespeicherte SMS im Modem, wird nicht alle 15 s geloggt.
func TestSMSPollQuietForStuck(t *testing.T) {
	a, fs := smsApp(t)
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	fs.out["mmcli -m any --messaging-list-sms"] = `{"modem.messaging.sms":["` + smsPath + `7"]}`
	fs.out["mmcli -s "+smsPath+"7"] = smsJSON("received", "deliver", "hängt")
	fs.fail["mmcli -m any --messaging-delete-sms="] = errors.New("Couldn't delete 1 parts from this SMS")
	now := time.Now()
	for i := 0; i < 4; i++ {
		a.smsPoll(a.Status(), now.Add(time.Duration(i)*smsPollEvery))
	}
	out := buf.String()
	if strings.Count(out, "1 neue empfangen") != 1 || strings.Count(out, "löschen") != 1 || strings.Contains(out, "0 neue") {
		t.Fatalf("Log:\n%s", out)
	}
	if l := a.SMSMessages(); len(l) != 1 {
		t.Fatalf("%d Einträge", len(l))
	}
}
