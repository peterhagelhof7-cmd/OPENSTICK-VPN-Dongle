package web

import (
	"fmt"
	"net/http"
)

// postRootPW setzt das Root-Passwort (SSH-Login). Das Passwort geht nur per
// stdin an chpasswd; gespeichert wird lediglich der Zeitpunkt.
func (s *Server) postRootPW(w http.ResponseWriter, r *http.Request) {
	pw, pw2 := r.FormValue("pw"), r.FormValue("pw2")
	if pw != pw2 {
		fail(w, r, "/settings", fmt.Errorf("Passwörter stimmen nicht überein"))
		return
	}
	if err := s.App.SetRootPassword(pw); err != nil {
		fail(w, r, "/settings", err)
		return
	}
	back(w, r, "/settings", "Root-Passwort gesetzt")
}
