package system

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// SplitNmcli zerlegt eine `nmcli -t`-Zeile (":" getrennt, "\:" und "\\" maskiert).
func SplitNmcli(line string) []string {
	var (
		fields []string
		cur    strings.Builder
		esc    bool
	)
	for _, r := range line {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(fields, cur.String())
}

// Device = Zeile aus `nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device status`.
type Device struct {
	Name, Type, State, Conn string
}

func (d Device) Connected() bool { return d.State == "connected" }

func ParseDevices(out string) []Device {
	var ds []Device
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := SplitNmcli(strings.TrimRight(l, "\r"))
		if len(f) >= 4 {
			ds = append(ds, Device{f[0], f[1], f[2], f[3]})
		}
	}
	return ds
}

// AP = Zeile aus `nmcli -t -f IN-USE,SSID,SIGNAL,SECURITY device wifi list`.
type AP struct {
	Active   bool   `json:"active"`
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"` // 0–100 %
	Security string `json:"security"`
}

func (a AP) Locked() bool { return a.Security != "" && a.Security != "--" }

// ParseAPs: doppelte SSIDs zusammenfassen (stärkstes Signal), versteckte weglassen.
func ParseAPs(out string) []AP {
	best := map[string]AP{}
	var order []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := SplitNmcli(strings.TrimRight(l, "\r"))
		if len(f) < 4 || f[1] == "" {
			continue
		}
		sig, _ := strconv.Atoi(f[2])
		ap := AP{Active: f[0] == "*", SSID: f[1], Signal: sig, Security: f[3]}
		old, seen := best[ap.SSID]
		if !seen {
			order = append(order, ap.SSID)
		}
		if !seen || ap.Signal > old.Signal || ap.Active {
			ap.Active = ap.Active || old.Active
			best[ap.SSID] = ap
		}
	}
	aps := make([]AP, 0, len(order))
	for _, s := range order {
		aps = append(aps, best[s])
	}
	return aps
}

// ParseHandshake liest `wg show wg0 latest-handshakes` (pubkey<TAB>unixzeit).
func ParseHandshake(out string) time.Time {
	var latest time.Time
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		sec, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil || sec == 0 {
			continue
		}
		if t := time.Unix(sec, 0); t.After(latest) {
			latest = t
		}
	}
	return latest
}

// ParseTransfer liest `wg show wg0 transfer` (pubkey rx tx).
func ParseTransfer(out string) (rx, tx int64) {
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(l)
		if len(f) == 3 {
			r, _ := strconv.ParseInt(f[1], 10, 64)
			t, _ := strconv.ParseInt(f[2], 10, 64)
			rx, tx = rx+r, tx+t
		}
	}
	return
}

// ParseDefaultGW liest "default via X dev wlan0 ..." aus `ip -4 route`.
func ParseDefaultGW(out string) string {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		for i := 0; i+1 < len(f); i++ {
			if f[0] == "default" && f[i] == "via" {
				return f[i+1]
			}
		}
	}
	return ""
}

// ParseLeases liest die dnsmasq-Leases (expiry mac ip host clientid) und
// liefert den Eintrag mit der spätesten Ablaufzeit (= aktuell verbundener PC).
func ParseLeases(s string) (ip, host string) {
	var best int64 = -1
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		exp, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil || exp <= best {
			continue
		}
		best, ip, host = exp, f[2], f[3]
	}
	if host == "*" {
		host = ""
	}
	return ip, host
}

// LTEStatus aus `mmcli -m any -J`.
type LTEStatus struct {
	Present    bool   `json:"present"`
	State      string `json:"state"` // z. B. registered, connected, locked, failed
	Failed     string `json:"failed,omitempty"`
	Unlock     string `json:"unlock,omitempty"` // z. B. sim-pin
	Operator   string `json:"operator,omitempty"`
	Registered string `json:"registration,omitempty"`
	Tech       string `json:"tech,omitempty"`
	Signal     int    `json:"signal"`           // 0–100 %
	IMEI       string `json:"-"`                // bewusst nicht in API/Log
	Number     string `json:"number,omitempty"` // eigene Rufnummer, wenn die SIM sie meldet
}

func ParseMMCLI(out string) LTEStatus {
	var raw struct {
		Modem struct {
			Generic struct {
				State         string   `json:"state"`
				FailedReason  string   `json:"state-failed-reason"`
				Unlock        string   `json:"unlock-required"`
				AccessTech    []string `json:"access-technologies"`
				OwnNumbers    []string `json:"own-numbers"`
				SignalQuality struct {
					Value string `json:"value"`
				} `json:"signal-quality"`
			} `json:"generic"`
			ThreeGPP struct {
				Operator     string `json:"operator-name"`
				Registration string `json:"registration-state"`
				IMEI         string `json:"imei"`
			} `json:"3gpp"`
		} `json:"modem"`
	}
	if json.Unmarshal([]byte(out), &raw) != nil || raw.Modem.Generic.State == "" {
		return LTEStatus{}
	}
	g, t := raw.Modem.Generic, raw.Modem.ThreeGPP
	s := LTEStatus{Present: true, State: g.State, Operator: t.Operator, Registered: t.Registration, IMEI: t.IMEI}
	if g.FailedReason != "--" {
		s.Failed = g.FailedReason
	}
	if g.Unlock != "--" && g.Unlock != "none" {
		s.Unlock = g.Unlock
	}
	if s.Operator == "--" {
		s.Operator = ""
	}
	s.Tech = strings.Join(g.AccessTech, ",")
	if s.Tech == "--" {
		s.Tech = ""
	}
	s.Signal, _ = strconv.Atoi(g.SignalQuality.Value)
	for _, n := range g.OwnNumbers {
		if n = strings.TrimSpace(n); n != "" && n != "--" {
			s.Number = n
			break
		}
	}
	return s
}
