package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

// LTE-Datenverbrauch und externe IP (Status-Anzeige, status.json).
//
// Verbrauch: Bytes an der IP-Schnittstelle des LTE-Modems (/sys/class/net/<if>/statistics),
// je Sitzung (seit LTE verbunden ist) und je Kalendermonat (Ortszeit). Die Monatswerte stehen
// in UsageFile und werden höchstens alle usageSaveEvery geschrieben (Flash schonen); ein
// Absturz kostet also höchstens so viel Zählung. Die Zähler der Schnittstelle können beim
// Neuaufbau bei 0 anfangen – kleiner als zuletzt = neu gestartet, dann zählt der ganze Wert.
//
// Externe IP: Abruf bei einem IP-Echo-Dienst über IPv4 bzw. IPv6, alle extIPEvery und bei
// jedem Uplink-Wechsel. Hinter CGNAT (LTE) gibt es die öffentliche IPv4 nur so.
const (
	UsageFile      = "/var/lib/vpndongle/lte-usage.json"
	usageSaveEvery = 5 * time.Minute
	usageKeep      = 24 // Monate
	extIPEvery     = 10 * time.Minute
	extIPTimeout   = 8 * time.Second
	extIPv4URL     = "https://ipv4.icanhazip.com"
	extIPv6URL     = "https://ipv6.icanhazip.com"
)

// MonthUsage ist der Verbrauch eines Monats ("2026-11").
type MonthUsage struct {
	Month string `json:"month"`
	Label string `json:"label"` // „Nov 2026“
	RX    int64  `json:"rx"`
	TX    int64  `json:"tx"`
}

func (m MonthUsage) Total() int64 { return m.RX + m.TX }

type usageFile struct {
	Months map[string][2]int64 `json:"months"` // Monat -> [rx, tx]
}

// usageState lebt nur im tick (keine Sperre nötig).
type usageState struct {
	loaded         bool
	months         map[string][2]int64
	iface          string
	lastRX, lastTX int64
	have           bool   // lastRX/lastTX gültig
	src            string // Quelle der Zähler (lteCounters)
	dirty          bool
	saved          time.Time
	// Sitzung
	sessUp         bool
	sessSince      time.Time
	sessRX, sessTX int64
}

var monthNames = [...]string{"Jan", "Feb", "Mär", "Apr", "Mai", "Jun", "Jul", "Aug", "Sep", "Okt", "Nov", "Dez"}

func monthLabel(key string) string {
	t, err := time.Parse("2006-01", key)
	if err != nil {
		return key
	}
	return fmt.Sprintf("%s %d", monthNames[t.Month()-1], t.Year())
}

func (a *App) usageLoad() {
	u := &a.usage
	u.loaded, u.months = true, map[string][2]int64{}
	b, err := a.Sys.ReadFile(UsageFile)
	if err != nil {
		return
	}
	var f usageFile
	if json.Unmarshal(b, &f) == nil && f.Months != nil {
		u.months = f.Months
	}
}

func (a *App) usageSave(now time.Time) {
	u := &a.usage
	keys := make([]string, 0, len(u.months))
	for k := range u.months {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for len(keys) > usageKeep {
		delete(u.months, keys[0])
		keys = keys[1:]
	}
	b, _ := json.Marshal(usageFile{Months: u.months})
	if err := a.Sys.WriteFile(UsageFile, b, 0o600); err != nil {
		log.Printf("LTE-Verbrauch speichern: %v", err)
		return
	}
	u.dirty, u.saved = false, now
}

// readCounter liest einen Zähler aus /sys/class/net/<if>/statistics.
func (a *App) readCounter(iface, name string) (int64, bool) {
	b, err := a.Sys.ReadFile("/sys/class/net/" + iface + "/statistics/" + name)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n, err == nil
}

// lteIPIface: IP-Schnittstelle des LTE-Geräts (bei QMI z. B. wwan0 zum Gerät wwan0qmi0).
func (a *App) lteIPIface(dev string) string {
	if dev != "" {
		if out, err := a.Sys.Run("nmcli", "-g", "GENERAL.IP-IFACE", "device", "show", dev); err == nil {
			if s := strings.TrimSpace(out); s != "" && s != "--" {
				return s
			}
		}
	}
	return a.Net.LTEIface
}

// lteCounters liefert die Bytes der LTE-Verbindung. Zuerst vom Modem (ModemManager,
// Bearer-Statistik seit Verbindungsbeginn) – am UZ801 zählt der Treiber bam-dmux an wwan0
// nichts (/sys/.../statistics bleibt 0). Sonst die Zähler der Schnittstelle.
// src nennt die Quelle; wechselt sie, fängt die Zählung neu an (keine Sprünge).
func (a *App) lteCounters(s system.LTEStatus, iface string) (rx, tx int64, src string, ok bool) {
	for _, p := range s.Bearers {
		out, err := a.Sys.Run("mmcli", "-b", p, "-J")
		if err != nil {
			continue
		}
		if b := system.ParseBearer(out); b.Connected && b.OK {
			return b.RX, b.TX, "bearer " + p, true
		}
	}
	rx, ok1 := a.readCounter(iface, "rx_bytes")
	tx, ok2 := a.readCounter(iface, "tx_bytes")
	return rx, tx, "if " + iface, ok1 && ok2
}

// sampleUsage zählt den LTE-Verbrauch und trägt ihn in den Status ein.
func (a *App) sampleUsage(now time.Time, lteUp bool, gsmDev string, lte system.LTEStatus, ns *Status) {
	u := &a.usage
	if !u.loaded {
		a.usageLoad()
	}
	if a.usageResetReq.Swap(false) {
		u.months, u.dirty, u.sessRX, u.sessTX = map[string][2]int64{}, false, 0, 0
		if err := a.Sys.Remove(UsageFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("LTE-Verbrauch löschen: %v", err)
		}
	}
	month := now.Format("2006-01")
	if lteUp {
		if !u.sessUp {
			u.sessUp, u.sessSince, u.sessRX, u.sessTX = true, now, 0, 0
			u.iface, u.have = a.lteIPIface(gsmDev), false
		}
		rx, tx, src, ok := a.lteCounters(lte, u.iface)
		if ok {
			if src != u.src {
				u.src, u.have = src, false
			}
			if u.have {
				dr, dt := rx-u.lastRX, tx-u.lastTX
				if dr < 0 { // Schnittstelle neu: Zähler fing bei 0 an
					dr = rx
				}
				if dt < 0 {
					dt = tx
				}
				if dr > 0 || dt > 0 {
					m := u.months[month]
					m[0] += dr
					m[1] += dt
					u.months[month] = m
					u.sessRX += dr
					u.sessTX += dt
					u.dirty = true
				}
			}
			u.lastRX, u.lastTX, u.have = rx, tx, true
		}
	} else if u.sessUp {
		u.sessUp, u.have = false, false
		if u.dirty {
			a.usageSave(now) // Sitzungsende: gleich sichern
		}
	}
	if u.dirty && now.Sub(u.saved) >= usageSaveEvery {
		a.usageSave(now)
	}

	ns.LTEIface = ""
	if u.sessUp {
		ns.LTEIface = u.iface
		ns.LTESessSince, ns.LTESessRX, ns.LTESessTX = u.sessSince, u.sessRX, u.sessTX
	} else {
		ns.LTESessSince, ns.LTESessRX, ns.LTESessTX = time.Time{}, 0, 0
	}
	ns.LTEMonths = ns.LTEMonths[:0:0]
	keys := make([]string, 0, len(u.months))
	for k := range u.months {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	for _, k := range keys {
		m := u.months[k]
		ns.LTEMonths = append(ns.LTEMonths, MonthUsage{Month: k, Label: monthLabel(k), RX: m[0], TX: m[1]})
	}
	cur := u.months[month]
	ns.LTEMonth = MonthUsage{Month: month, Label: monthLabel(month), RX: cur[0], TX: cur[1]}
}

// usageReset löscht die Verbrauchszahlen beim nächsten tick (Zurücksetzen „alles“).
// Der Zustand gehört dem tick, deshalb nur ein Merker.
func (a *App) usageReset() {
	a.usageResetReq.Store(true)
}

// --- externe IP ---

// fetchExtIP fragt den Echo-Dienst über die gewählte Familie ("tcp4" | "tcp6").
func fetchExtIP(network, url string) (string, error) {
	d := &net.Dialer{Timeout: extIPTimeout}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout: extIPTimeout,
	}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: extIPTimeout}
	resp, err := cl.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 100))
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.TrimSpace(string(b)))
	if ip == nil || (network == "tcp4") != (ip.To4() != nil) {
		return "", fmt.Errorf("keine %s-Adresse: %q", network, strings.TrimSpace(string(b)))
	}
	return ip.String(), nil
}

// sampleExtIP holt die externe IP im Hintergrund neu, wenn sie fällig ist. route beschreibt
// den Weg ins Internet (Uplink, VPN): ändert er sich, wird sofort neu gefragt.
func (a *App) sampleExtIP(now time.Time, uplink, route string, ns *Status) {
	if uplink == "" {
		a.extMu.Lock()
		a.extV4, a.extV6, a.extAt, a.extUplink = "", "", time.Time{}, ""
		a.extMu.Unlock()
	} else if !a.extBusy.Load() {
		a.extMu.Lock()
		due := route != a.extUplink || now.Sub(a.extAt) >= extIPEvery
		if due {
			a.extUplink, a.extAt = route, now
		}
		a.extMu.Unlock()
		if due {
			a.extBusy.Store(true)
			go func() {
				defer a.extBusy.Store(false)
				v4, err4 := a.extFetch("tcp4", extIPv4URL)
				v6, err6 := a.extFetch("tcp6", extIPv6URL)
				if err4 != nil && err6 != nil {
					log.Printf("externe IP: %v / %v", err4, err6)
				}
				a.extMu.Lock()
				a.extV4, a.extV6 = v4, v6
				a.extMu.Unlock()
			}()
		}
	}
	a.extMu.Lock()
	ns.ExtIPv4, ns.ExtIPv6 = a.extV4, a.extV6
	a.extMu.Unlock()
}

// PrevMonths: die Vormonate (ohne den laufenden), neueste zuerst, höchstens 6 – für die Anzeige.
func (s Status) PrevMonths() []MonthUsage {
	var out []MonthUsage
	for _, m := range s.LTEMonths {
		if m.Month != s.LTEMonth.Month && len(out) < 6 {
			out = append(out, m)
		}
	}
	return out
}
