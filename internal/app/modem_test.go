package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

// echte Ausgabe am UZ801 vor dem Abgleich (26.11.2026), gekürzt
const qmiProfilesUZ801 = `Profile list retrieved:
	[1] 3gpp - profile1
		APN: ''
		PDP type: 'ipv4-or-ipv6'
	[2] 3gpp - profile2
		APN: ''
	[3] 3gpp - qdp_profile
		APN: 'ctlte'
		PDP type: 'ipv4-or-ipv6'
`

func waitModem(t *testing.T, a *App) {
	t.Helper()
	for i := 0; i < 200 && a.modemBusy.Load(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if a.modemBusy.Load() {
		t.Fatal("Modem-Aktion hängt")
	}
}

func lteCfg(pin string) *config.Config {
	return &config.Config{LTE: config.LTE{Enabled: true, APN: "internet", PIN: pin}}
}

func TestAttachPlan(t *testing.T) {
	ps := system.ParseQMIProfiles(qmiProfilesUZ801)
	if len(ps) != 3 || ps[2].Index != 3 || ps[2].Name != "qdp_profile" || ps[2].APN != "ctlte" || ps[0].APN != "" {
		t.Fatalf("%+v", ps)
	}
	if idx, ok, _ := attachPlan(ps, "internet"); !ok || idx != 3 {
		t.Fatal("Profil 3 sollte den APN bekommen", idx, ok)
	}
	ps[2].APN = "INTERNET"
	if idx, ok, _ := attachPlan(ps, "internet"); !ok || idx != 0 {
		t.Fatal("APN schon da – nichts zu tun", idx)
	}
	if idx, ok, _ := attachPlan(ps[:2], "internet"); !ok || idx != 0 {
		t.Fatal("nur leere Profile – nichts zu tun", idx)
	}
	two := []system.QMIProfile{{Index: 1, APN: "a"}, {Index: 3, APN: "b"}}
	if _, ok, msg := attachPlan(two, "internet"); ok || !strings.Contains(msg, "mehrere") {
		t.Fatal("zwei fremde APNs dürfen nicht geändert werden", msg)
	}
}

func TestModemCareAttachFix(t *testing.T) {
	a, fs := newTestApp(t)
	fs.out["qmicli -p -d /dev/wwan0qmi0 --wds-get-profile-list"] = qmiProfilesUZ801
	s := system.LTEStatus{Present: true, State: "registered", QMIPort: "wwan0qmi0", PINRetries: 3}
	now := time.Now()
	a.modemCare(lteCfg(""), s, now)
	waitModem(t, a)
	cmds := fs.take()
	if !has(cmds, "--wds-modify-profile=3gpp,3,apn=internet") || !has(cmds, "mmcli -m any --set-power-state-low") || !has(cmds, "mmcli -m any --set-power-state-on") {
		t.Fatalf("%q", cmds)
	}
	// erledigt: kein zweiter Abgleich für denselben APN
	a.modemCare(lteCfg(""), s, now.Add(time.Hour))
	waitModem(t, a)
	if c := fs.take(); len(c) != 0 {
		t.Fatalf("doppelt: %q", c)
	}
	// verbunden oder ohne QMI: gar nicht erst nachsehen
	b, fs2 := newTestApp(t)
	for _, st := range []system.LTEStatus{
		{Present: true, State: "connected", QMIPort: "wwan0qmi0"},
		{Present: true, State: "registered"},
		{Present: true, State: "registered", QMIPort: "wwan0qmi0", Unlock: "sim-pin"},
	} {
		b.modemCare(lteCfg(""), st, now)
		waitModem(t, b)
	}
	if c := fs2.take(); has(c, "qmicli") {
		t.Fatalf("%q", c)
	}
	// APN mit Sonderzeichen geht nicht in den qmicli-Parameter
	c := lteCfg("")
	c.LTE.APN = "a,b"
	b.modemCare(c, system.LTEStatus{Present: true, State: "registered", QMIPort: "wwan0qmi0"}, now)
	waitModem(t, b)
	if c := fs2.take(); has(c, "qmicli") {
		t.Fatalf("%q", c)
	}
}

func TestModemCarePIN(t *testing.T) {
	a, fs := newTestApp(t)
	s := system.LTEStatus{Present: true, State: "locked", Unlock: "sim-pin", PINRetries: 3, SIMPath: "/org/freedesktop/ModemManager1/SIM/0"}
	now := time.Now()
	a.modemCare(lteCfg("1234"), s, now)
	waitModem(t, a)
	if c := fs.take(); !has(c, "mmcli -i /org/freedesktop/ModemManager1/SIM/0 --pin 1234") {
		t.Fatalf("%q", c)
	}

	// falsche PIN: genau ein Versuch, auch Minuten später kein zweiter mit derselben PIN
	fs.fail["mmcli -i "] = errors.New("wrong pin")
	a.pinLastTry = time.Time{}
	a.modemCare(lteCfg("9999"), s, now)
	waitModem(t, a)
	fs.take()
	a.modemCare(lteCfg("9999"), s, now.Add(10*time.Minute))
	waitModem(t, a)
	if c := fs.take(); has(c, "--pin") {
		t.Fatalf("zweiter Versuch mit falscher PIN: %q", c)
	}
	if st := a.Status(); !strings.Contains(st.LastError, "kein weiterer Versuch") || strings.Contains(st.LastError, "9999") {
		t.Fatalf("Fehlertext: %q", st.LastError)
	}

	// nur noch 1 Versuch übrig: nie automatisch
	b, fs2 := newTestApp(t)
	s.PINRetries = 1
	b.modemCare(lteCfg("1234"), s, now)
	waitModem(t, b)
	if c := fs2.take(); has(c, "--pin") {
		t.Fatalf("letzter Versuch verbrannt: %q", c)
	}
	// ohne PIN, PIN2 (nicht blockierend) oder LTE aus: nichts
	for _, c := range []*config.Config{lteCfg(""), {LTE: config.LTE{PIN: "1234"}}} {
		s.PINRetries = 3
		b.modemCare(c, s, now)
		waitModem(t, b)
	}
	b.modemCare(lteCfg("1234"), system.LTEStatus{Present: true, State: "registered", SIMPath: "/x"}, now)
	waitModem(t, b)
	if c := fs2.take(); has(c, "--pin") {
		t.Fatalf("%q", c)
	}
}

// Uplink LTE: nach dem Neu-Einbuchen gleich verbinden, nicht auf die NM-Pause warten.
func TestModemCareAttachReconnect(t *testing.T) {
	for _, uplink := range []string{config.UplinkLTE, config.UplinkWLAN} {
		a, fs := newTestApp(t)
		fs.out["qmicli -p -d /dev/wwan0qmi0 --wds-get-profile-list"] = qmiProfilesUZ801
		c := lteCfg("")
		c.Uplink = uplink
		a.modemCare(c, system.LTEStatus{Present: true, State: "registered", QMIPort: "wwan0qmi0"}, time.Now())
		waitModem(t, a)
		cmds := fs.take()
		up := has(cmds, "nmcli --wait 60 connection up vpndongle-lte")
		if !has(cmds, "--set-power-state-on") || up != (uplink == config.UplinkLTE) {
			t.Fatalf("%s: %q", uplink, cmds)
		}
	}
}
