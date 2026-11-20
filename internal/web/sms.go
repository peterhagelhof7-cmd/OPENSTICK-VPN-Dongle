package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/app"
)

// SMS-Seite: öffentlich wie die Übersicht (Nutzer am PC, z. B. für 2FA-Codes),
// nur wenn der Admin in den Einstellungen „SMS aktiv“ gesetzt hat.

type smsData struct {
	pageData
	SMS     []app.SMSMsg
	MaxText int
	Number  string // vorbelegt (Antworten)
}

func (s *Server) smsOn(w http.ResponseWriter, r *http.Request) bool {
	if !app.SMSActive(s.App.Store.Get()) {
		if r.Method == http.MethodGet && r.URL.Path == "/sms" {
			back(w, r, "/", "SMS sind nicht eingeschaltet (Einstellungen → LTE → SMS aktiv).")
		} else {
			http.Error(w, "SMS sind nicht eingeschaltet", http.StatusForbidden)
		}
		return false
	}
	return true
}

func (s *Server) sms(w http.ResponseWriter, r *http.Request) {
	if !s.smsOn(w, r) {
		return
	}
	d := smsData{pageData: s.data(r, false), SMS: s.App.SMSMessages(), MaxText: app.SMSMaxText}
	d.Number = r.URL.Query().Get("to")
	d.SMSPage = true
	s.render(w, "sms", d)
	s.App.SMSMarkRead() // nach dem Anzeigen: neue sind auf der Seite noch markiert
}

func (s *Server) smsSend(w http.ResponseWriter, r *http.Request) {
	if !s.smsOn(w, r) {
		return
	}
	if err := s.App.SMSSend(r.FormValue("number"), r.FormValue("text")); err != nil {
		fail(w, r, "/sms", err)
		return
	}
	back(w, r, "/sms", "SMS gesendet.")
}

func (s *Server) smsDel(w http.ResponseWriter, r *http.Request) {
	if !s.smsOn(w, r) {
		return
	}
	var id int64
	if r.FormValue("all") == "" {
		var err error
		if id, err = strconv.ParseInt(r.FormValue("id"), 10, 64); err != nil || id <= 0 {
			fail(w, r, "/sms", errors.New("ungültige SMS"))
			return
		}
	}
	if err := s.App.SMSDelete(id); err != nil {
		fail(w, r, "/sms", err)
		return
	}
	if id == 0 {
		back(w, r, "/sms", "Alle SMS gelöscht.")
		return
	}
	back(w, r, "/sms", "SMS gelöscht.")
}

// smsJSON: höchste ID + ungelesene, damit die offene Seite neue SMS bemerkt.
func (s *Server) smsJSON(w http.ResponseWriter, r *http.Request) {
	if !s.smsOn(w, r) {
		return
	}
	var last int64
	if l := s.App.SMSMessages(); len(l) > 0 {
		last = l[0].ID
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"last": last, "unread": s.App.SMSUnread()})
}
