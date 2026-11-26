package system

import (
	"regexp"
	"strconv"
	"strings"
)

// QMIProfile ist ein 3GPP-Profil im Modem (qmicli --wds-get-profile-list=3gpp).
type QMIProfile struct {
	Index int
	Name  string
	APN   string
}

var (
	qmiProfileHead = regexp.MustCompile(`^\s*\[(\d+)\]\s+3gpp\s+-\s+(.*)$`)
	qmiProfileAPN  = regexp.MustCompile(`^\s*APN:\s+'(.*)'\s*$`)
)

// ParseQMIProfiles liest die Ausgabe von `qmicli --wds-get-profile-list=3gpp`:
//
//	Profile list retrieved:
//		[3] 3gpp - qdp_profile
//			APN: 'ctlte'
func ParseQMIProfiles(out string) []QMIProfile {
	var ps []QMIProfile
	for _, l := range strings.Split(out, "\n") {
		if m := qmiProfileHead.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			ps = append(ps, QMIProfile{Index: n, Name: strings.TrimSpace(m[2])})
			continue
		}
		if m := qmiProfileAPN.FindStringSubmatch(l); m != nil && len(ps) > 0 {
			ps[len(ps)-1].APN = m[1]
		}
	}
	return ps
}
