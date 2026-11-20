package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

func TestSMSPageOffByDefault(t *testing.T) {
	_, h := setup(t)
	w := do(h, "GET", "/sms", nil, false)
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "nicht+eingeschaltet") {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Location"))
	}
	if w := do(h, "POST", "/sms/send", url.Values{"number": {"+491511234567"}, "text": {"x"}}, false); w.Code != 403 {
		t.Fatalf("Senden ausgeschaltet: %d", w.Code)
	}
	if strings.Contains(do(h, "GET", "/", nil, false).Body.String(), `href="/sms"`) {
		t.Fatal("kein SMS-Link, solange ausgeschaltet")
	}
}

func TestSMSSettingsAndPage(t *testing.T) {
	a, h := setup(t)
	// Haken + Rufnummer über die Einstellungen
	w := do(h, "POST", "/settings/lte", url.Values{"enabled": {"on"}, "sms": {"on"}, "number": {"+49 151 2345 678"}}, true)
	if w.Code != 303 || strings.Contains(w.Header().Get("Location"), "Fehler") {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Location"))
	}
	c := a.Store.Get()
	if !c.LTE.SMS || c.LTE.Number != "+491512345678" {
		t.Fatalf("%+v", c.LTE)
	}
	if w := do(h, "POST", "/settings/lte", url.Values{"enabled": {"on"}, "number": {"keine Nummer"}}, true); !strings.Contains(w.Header().Get("Location"), "Fehler") {
		t.Fatal("ungültige Rufnummer muss abgelehnt werden")
	}
	if !a.Store.Get().LTE.SMS {
		t.Fatal("abgelehnte Eingabe darf nichts ändern")
	}

	// Übersicht: Rufnummer (eingetragen) + SMS-Link, ohne Login
	b := do(h, "GET", "/", nil, false).Body.String()
	if !strings.Contains(b, "&#43;491512345678") || !strings.Contains(b, "(eingetragen)") || !strings.Contains(b, `href="/sms"`) {
		t.Fatalf("Übersicht: %s", b)
	}
	// SMS-Seite ohne Login
	w = do(h, "GET", "/sms?to=%2B4917000", nil, false)
	b = w.Body.String()
	if w.Code != 200 || !strings.Contains(b, "SMS senden") || !strings.Contains(b, `value="&#43;4917000"`) || strings.Contains(b, "ZgotmplZ") {
		t.Fatalf("SMS-Seite %d: %s", w.Code, b)
	}
	if !strings.Contains(b, `href="/">Übersicht`) || strings.Contains(b, `<a href="/sms">SMS`) {
		t.Fatal("Kopfzeile auf der SMS-Seite: Übersicht statt SMS-Link")
	}
	// Senden ohne Modem (Dry-Run, kein LTE-Status) -> Fehlermeldung, kein Absturz
	w = do(h, "POST", "/sms/send", url.Values{"number": {"+491511234567"}, "text": {"Hallo"}}, false)
	if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "Fehler") {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Location"))
	}
	// gesendete (fehlgeschlagene) SMS ist nicht abgelegt, weil vor dem Senden geprüft
	if len(a.SMSMessages()) != 0 {
		t.Fatal("ohne Modem nichts ablegen")
	}
	if w := do(h, "GET", "/sms.json", nil, false); w.Code != 200 || !strings.Contains(w.Body.String(), `"last":0`) {
		t.Fatalf("sms.json %d %s", w.Code, w.Body.String())
	}
	if w := do(h, "POST", "/sms/del", url.Values{"id": {"abc"}}, false); !strings.Contains(w.Header().Get("Location"), "Fehler") {
		t.Fatal("ungültige ID")
	}
	if w := do(h, "POST", "/sms/del", url.Values{"all": {"1"}}, false); !strings.Contains(w.Header().Get("Location"), "gel%C3%B6scht") {
		t.Fatalf("alle löschen: %s", w.Header().Get("Location"))
	}
}

func TestSMSCSRF(t *testing.T) {
	a, h := setup(t)
	_ = a.Update(func(c *config.Config) error { c.LTE.Enabled, c.LTE.SMS = true, true; return nil })
	r := httptest.NewRequest("POST", "http://192.168.226.1/sms/send", strings.NewReader("number=%2B491511234567&text=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://boese.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("fremde Herkunft erwartet 403, bekam %d", w.Code)
	}
}
