// Package zbxtpl erzeugt das Zabbix-7.0-Template für den VPN-Dongle als
// Import-JSON (gleicher Aufbau wie YAML-Export). Der Agent auf dem Stick
// liefert einen Status-Block (vpndongle.status), alle Werte sind davon
// abhängige Items; was überwacht wird, steuert die Low-Level-Discovery
// (Schalter in der Weboberfläche). Ping-Prüfungen laufen als Simple Checks
// auf dem Proxy des Sticks.
//
// Neu erzeugen:  go run ./cmd/zbxtemplate
package zbxtpl

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
)

// File = Ablage im Repo (relativ zur Repo-Wurzel).
const File = "zabbix/template_openstick_vpn_dongle.json"

// JSON = formatiertes Import-Dokument (stabile Reihenfolge, UTF-8 ohne Escapes).
func JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Build()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Name = technischer Template-Name (ohne Leerzeichen/Klammern, weil er in
// jeder Trigger-Expression steht).
const Name = "OpenStick-VPN-Dongle"

type obj = map[string]any
type list = []any

// UUID liefert eine stabile UUIDv4 (Zabbix prüft die Versionsbits).
func UUID(seed string) string {
	h := sha1.Sum([]byte("openstick-vpn-dongle/zabbix/" + seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

func ref(key string) string { return "/" + Name + "/" + key }

func tags(kv ...string) list {
	var l list
	for i := 0; i+1 < len(kv); i += 2 {
		l = append(l, obj{"tag": kv[i], "value": kv[i+1]})
	}
	return l
}

func trig(name, expr, prio, desc string) obj {
	t := obj{"uuid": UUID("trigger/" + name + "/" + expr), "expression": expr, "name": name, "priority": prio}
	if desc != "" {
		t["description"] = desc
	}
	return t
}

func step(typ string, params ...string) obj {
	s := obj{"type": typ}
	if len(params) > 0 {
		p := list{}
		for _, x := range params {
			p = append(p, x)
		}
		s["parameters"] = p
	}
	return s
}

// dep = abhängiges Item aus dem Status-Block.
func dep(name, key, jsonPath, valueType string, pre ...obj) obj {
	steps := list{step("JSONPATH", jsonPath)}
	for _, p := range pre {
		steps = append(steps, p)
	}
	it := obj{
		"uuid": UUID("item/" + key), "name": name, "type": "DEPENDENT", "key": key, "delay": "0",
		"preprocessing": steps, "master_item": obj{"key": "vpndongle.status"},
	}
	if valueType != "" {
		it["value_type"] = valueType
	}
	return it
}

func boolDep(name, key, jsonPath string) obj {
	it := dep(name, key, jsonPath, "", step("BOOL_TO_DECIMAL"))
	it["valuemap"] = obj{"name": "VPN-Dongle Ja/Nein"}
	return it
}

// optional: fehlt der Wert im JSON (z. B. kein Modem), Ersatzwert statt Fehler.
func orDefault(it obj, def string) obj {
	steps := it["preprocessing"].(list)
	steps[0].(obj)["error_handler"] = "CUSTOM_VALUE"
	steps[0].(obj)["error_handler_params"] = def
	return it
}

func with(it obj, kv ...any) obj {
	for i := 0; i+1 < len(kv); i += 2 {
		it[kv[i].(string)] = kv[i+1]
	}
	return it
}

// pingSet = drei Simple Checks (Erreichbarkeit, Verlust, Antwortzeit) samt Triggern.
func pingSet(label, macro, comp, downPrio, downName, downDesc string) list {
	up := "icmpping[" + macro + ",3]"
	loss := "icmppingloss[" + macro + ",3]"
	rtt := "icmppingsec[" + macro + ",3,,,,avg]"
	downExpr := "max(" + ref(up) + ",#3)=0"
	return list{
		obj{"uuid": UUID("item/" + up), "name": label + ": erreichbar", "type": "SIMPLE", "key": up,
			"valuemap": obj{"name": "Service state"}, "tags": tags("component", comp),
			"trigger_prototypes": list{trig(downName, downExpr, downPrio, downDesc)}},
		obj{"uuid": UUID("item/" + loss), "name": label + ": Paketverlust", "type": "SIMPLE", "key": loss,
			"value_type": "FLOAT", "units": "%", "tags": tags("component", comp),
			"trigger_prototypes": list{trig(label+": hoher Paketverlust",
				"min("+ref(loss)+",5m)>{$VPNDONGLE.LOSS.WARN} and min("+ref(loss)+",5m)<100", "WARNING", "")}},
		obj{"uuid": UUID("item/" + rtt), "name": label + ": Antwortzeit", "type": "SIMPLE", "key": rtt,
			"value_type": "FLOAT", "units": "s", "tags": tags("component", comp),
			"trigger_prototypes": list{trig(label+": hohe Antwortzeit",
				"avg("+ref(rtt)+",5m)>{$VPNDONGLE.RTT.WARN}", "WARNING", "")}},
	}
}

func rule(group, name, desc string, protos list) obj {
	key := "vpndongle.discovery[" + group + "]"
	return obj{
		"uuid": UUID("rule/" + key), "name": name, "type": "ZABBIX_ACTIVE", "key": key, "delay": "5m",
		"lifetime": "1d", "description": desc, "item_prototypes": protos,
	}
}

// protoTriggers verschiebt "triggers" eines Items nach "trigger_prototypes".
func proto(it obj) obj {
	if t, ok := it["triggers"]; ok {
		it["trigger_prototypes"] = t
		delete(it, "triggers")
	}
	return it
}

// Build liefert das komplette Import-Dokument.
func Build() obj {
	items := list{
		obj{"uuid": UUID("item/vpndongle.status"), "name": "Dongle: Status (Rohdaten)", "type": "ZABBIX_ACTIVE",
			"key": "vpndongle.status", "delay": "1m", "history": "0", "value_type": "TEXT",
			"description": "JSON von vpndongled; alle Dongle-Werte sind davon abhängige Items.",
			"tags":        tags("component", "raw")},
		with(dep("Dongle: Uplink", "vpndongle.uplink.code", "$.uplink_code", ""),
			"valuemap", obj{"name": "VPN-Dongle Uplink"}, "tags", tags("component", "network"),
			"triggers", list{
				trig("Dongle: keine Daten seit {$VPNDONGLE.NODATA}", "nodata("+ref("vpndongle.uplink.code")+",{$VPNDONGLE.NODATA})=1", "AVERAGE",
					"Der Agent auf dem Stick meldet nichts mehr (Stick aus, Tunnel/Proxy gestört)."),
				trig("Dongle: kein Uplink seit {$VPNDONGLE.UPLINK.DOWN}", "max("+ref("vpndongle.uplink.code")+",{$VPNDONGLE.UPLINK.DOWN})=0", "WARNING",
					"Weder WLAN noch LTE verbunden (Werte kommen verzögert über den Proxy-Puffer)."),
				trig("Dongle: LTE-Rückfall aktiv", "last("+ref("vpndongle.uplink.code")+")=2", "INFO",
					"Kein WLAN – der Stick nutzt Mobilfunk (Datenvolumen)."),
			}),
		with(boolDep("Dongle: Internet gesperrt (Captive)", "vpndongle.locked", "$.locked"), "tags", tags("component", "network")),
		with(dep("Dongle: Version", "vpndongle.version", "$.version", "CHAR", step("DISCARD_UNCHANGED_HEARTBEAT", "1d")),
			"tags", tags("component", "system")),
		obj{"uuid": UUID("item/system.uptime"), "name": "Dongle: Laufzeit", "type": "ZABBIX_ACTIVE", "key": "system.uptime",
			"delay": "5m", "units": "uptime", "tags": tags("component", "system"),
			"triggers": list{trig("Dongle: neu gestartet", "last("+ref("system.uptime")+")<10m", "INFO", "")}},
		obj{"uuid": UUID("item/system.cpu.load[all,avg5]"), "name": "Dongle: CPU-Last (5 min)", "type": "ZABBIX_ACTIVE",
			"key": "system.cpu.load[all,avg5]", "delay": "1m", "value_type": "FLOAT", "tags": tags("component", "cpu")},
		obj{"uuid": UUID("item/vm.memory.size[pavailable]"), "name": "Dongle: RAM verfügbar", "type": "ZABBIX_ACTIVE",
			"key": "vm.memory.size[pavailable]", "delay": "1m", "value_type": "FLOAT", "units": "%", "tags": tags("component", "memory"),
			"triggers": list{trig("Dongle: wenig RAM (<{$VPNDONGLE.MEM.MIN}%)",
				"max("+ref("vm.memory.size[pavailable]")+",5m)<{$VPNDONGLE.MEM.MIN}", "AVERAGE", "")}},
		obj{"uuid": UUID("item/vfs.fs.size[/,pfree]"), "name": "Dongle: Flash frei", "type": "ZABBIX_ACTIVE",
			"key": "vfs.fs.size[/,pfree]", "delay": "5m", "value_type": "FLOAT", "units": "%", "tags": tags("component", "storage"),
			"triggers": list{trig("Dongle: wenig Flash frei (<{$VPNDONGLE.DISK.MIN}%)",
				"last("+ref("vfs.fs.size[/,pfree]")+")<{$VPNDONGLE.DISK.MIN}", "AVERAGE", "")}},
	}

	wlanUp := "vpndongle.wlan.up[{#IF}]"
	lteUp := "vpndongle.lte.up[{#IF}]"
	vpnUp, vpnOn := "vpndongle.vpn.up[{#IF}]", "vpndongle.vpn.on[{#IF}]"

	rules := list{
		rule("wlan", "WLAN am Uplink", "Schalter „verbundenes WLAN“ in der Weboberfläche.", list{
			proto(with(boolDep("WLAN: verbunden", wlanUp, "$.wlan_up"), "tags", tags("component", "wlan"),
				"triggers", list{trig("WLAN getrennt seit {$VPNDONGLE.WLAN.DOWN}", "max("+ref(wlanUp)+",{$VPNDONGLE.WLAN.DOWN})=0", "WARNING", "")})),
			proto(with(dep("WLAN: Signal", "vpndongle.wlan.signal[{#IF}]", "$.wlan_signal", ""), "units", "%", "tags", tags("component", "wlan"),
				"triggers", list{trig("WLAN: schwaches Signal (<{$VPNDONGLE.WLAN.SIGNAL.MIN}%)",
					"avg("+ref("vpndongle.wlan.signal[{#IF}]")+",15m)<{$VPNDONGLE.WLAN.SIGNAL.MIN} and last("+ref(wlanUp)+")=1", "WARNING", "")})),
			with(dep("WLAN: SSID", "vpndongle.wlan.ssid[{#IF}]", "$.wlan_ssid", "CHAR", step("DISCARD_UNCHANGED_HEARTBEAT", "1h")),
				"tags", tags("component", "wlan")),
		}),
		rule("wlangw", "WLAN-Gateway", "Ping auf das Gateway des verbundenen WLANs (Simple Check auf dem Proxy des Sticks).",
			pingSet("WLAN-Gateway {#WLANGW}", "{#WLANGW}", "wlan", "AVERAGE", "WLAN-Gateway {#WLANGW} nicht erreichbar", "")),
		rule("pc", "PC am USB", "Ping auf den PC am USB-Anschluss. Windows beantwortet Ping nur, wenn die Firewall ICMP erlaubt.",
			pingSet("PC {#PCIP}", "{#PCIP}", "pc", "INFO", "PC {#PCIP} antwortet nicht auf Ping", "Häufig nur die Windows-Firewall (ICMP-Echo eingehend).")),
		rule("lte", "LTE / Mobilfunk", "Schalter „LTE“ in der Weboberfläche (nur wenn ein APN eingerichtet ist).", list{
			with(boolDep("LTE: verbunden", lteUp, "$.lte_up"), "tags", tags("component", "lte")),
			proto(with(dep("LTE: Signal", "vpndongle.lte.signal[{#IF}]", "$.lte.signal", ""), "units", "%", "tags", tags("component", "lte"),
				"triggers", list{trig("LTE: schwaches Signal (<{$VPNDONGLE.LTE.SIGNAL.MIN}%)",
					"avg("+ref("vpndongle.lte.signal[{#IF}]")+",15m)<{$VPNDONGLE.LTE.SIGNAL.MIN} and last("+ref(lteUp)+")=1", "WARNING", "")})),
			with(orDefault(dep("LTE: Zustand", "vpndongle.lte.state[{#IF}]", "$.lte.state", "CHAR"), "kein Modem"), "tags", tags("component", "lte")),
			with(orDefault(dep("LTE: Netz", "vpndongle.lte.operator[{#IF}]", "$.lte.operator", "CHAR", step("DISCARD_UNCHANGED_HEARTBEAT", "1h")), "-"),
				"tags", tags("component", "lte")),
			with(orDefault(dep("LTE: Technik", "vpndongle.lte.tech[{#IF}]", "$.lte.tech", "CHAR"), "-"), "tags", tags("component", "lte")),
			proto(with(orDefault(dep("LTE: SIM-Sperre", "vpndongle.lte.unlock[{#IF}]", "$.lte.unlock", "CHAR"), "-"), "tags", tags("component", "lte"),
				"triggers", list{trig("LTE: SIM verlangt {ITEM.LASTVALUE1}", "last("+ref("vpndongle.lte.unlock[{#IF}]")+")<>\"-\"", "HIGH",
					"PIN/PUK nötig – in der Weboberfläche unter LTE eintragen.")})),
		}),
		rule("vpn", "VPN-Tunnel", "Schalter „VPN“ in der Weboberfläche (nur wenn WireGuard eingerichtet ist).", list{
			with(boolDep("VPN: soll laufen", vpnOn, "$.vpn_on"), "tags", tags("component", "vpn")),
			proto(with(boolDep("VPN: verbunden", vpnUp, "$.vpn_up"), "tags", tags("component", "vpn"),
				"triggers", list{trig("VPN-Tunnel getrennt seit {$VPNDONGLE.VPN.DOWN}",
					"max("+ref(vpnUp)+",{$VPNDONGLE.VPN.DOWN})=0 and last("+ref(vpnOn)+")=1", "HIGH", "")})),
			with(dep("VPN: Alter des letzten Handshakes", "vpndongle.vpn.handshake[{#IF}]", "$.handshake_age", "FLOAT"),
				"units", "s", "tags", tags("component", "vpn")),
			with(dep("VPN: empfangen", "vpndongle.vpn.rx[{#IF}]", "$.rx", "FLOAT", step("CHANGE_PER_SECOND"), step("MULTIPLIER", "8")),
				"units", "bps", "tags", tags("component", "vpn")),
			with(dep("VPN: gesendet", "vpndongle.vpn.tx[{#IF}]", "$.tx", "FLOAT", step("CHANGE_PER_SECOND"), step("MULTIPLIER", "8")),
				"units", "bps", "tags", tags("component", "vpn")),
		}),
		rule("targets", "Weitere Ping-Ziele", "Liste „Weitere Ping-Ziele“ in der Weboberfläche.",
			pingSet("Ziel {#TNAME}", "{#TADDR}", "target", "AVERAGE", "{#TNAME} ({#TADDR}) nicht erreichbar", "")),
	}

	macros := list{}
	for _, m := range [][3]string{
		{"{$VPNDONGLE.NODATA}", "5m", "Keine Daten vom Stick nach dieser Zeit."},
		{"{$VPNDONGLE.UPLINK.DOWN}", "10m", "Kein Uplink (WLAN/LTE) so lange -> Warnung."},
		{"{$VPNDONGLE.MEM.MIN}", "10", "Untergrenze freier RAM in %."},
		{"{$VPNDONGLE.DISK.MIN}", "10", "Untergrenze freier Flash in %."},
		{"{$VPNDONGLE.WLAN.DOWN}", "10m", "WLAN getrennt so lange -> Warnung."},
		{"{$VPNDONGLE.WLAN.SIGNAL.MIN}", "30", "WLAN-Signal (Mittel 15 min) unter diesem Wert in %."},
		{"{$VPNDONGLE.LTE.SIGNAL.MIN}", "20", "LTE-Signal (Mittel 15 min) unter diesem Wert in %."},
		{"{$VPNDONGLE.VPN.DOWN}", "5m", "Tunnel ohne Handshake so lange -> Alarm."},
		{"{$VPNDONGLE.LOSS.WARN}", "20", "Paketverlust-Warnschwelle in %."},
		{"{$VPNDONGLE.RTT.WARN}", "0.3", "Antwortzeit-Warnschwelle in s."},
	} {
		macros = append(macros, obj{"macro": m[0], "value": m[1], "description": m[2]})
	}

	vm := func(name string, kv ...string) obj {
		var maps list
		for i := 0; i+1 < len(kv); i += 2 {
			maps = append(maps, obj{"value": kv[i], "newvalue": kv[i+1]})
		}
		return obj{"uuid": UUID("valuemap/" + name), "name": name, "mappings": maps}
	}

	tpl := obj{
		"uuid":     UUID("template/" + Name),
		"template": Name,
		"name":     "OpenStick VPN-Dongle",
		"description": "VPN-Dongle (UZ801/OpenStick) mit vpndongled: Uplink WLAN/LTE, VPN, WLAN-Gateway, PC, weitere Ziele.\n" +
			"Autoregistrierung über HostMetadata \"openstick-vpn-dongle\"; die Überwachungsbereiche schaltet man in der Weboberfläche des Sticks.",
		"vendor":          obj{"name": "OPENSTICK-VPN-Dongle", "version": "7.0-1"},
		"groups":          list{obj{"name": "Templates/Network devices"}},
		"items":           items,
		"discovery_rules": rules,
		"tags":            tags("class", "network", "target", "vpndongle"),
		"macros":          macros,
		"valuemaps": list{
			vm("Service state", "0", "Down", "1", "Up"),
			vm("VPN-Dongle Ja/Nein", "0", "Nein", "1", "Ja"),
			vm("VPN-Dongle Uplink", "0", "keiner", "1", "WLAN", "2", "LTE"),
		},
	}
	return obj{"zabbix_export": obj{
		"version": "7.0",
		// gleiche UUID wie die offizielle Gruppe -> keine Dublette
		"template_groups": list{obj{"uuid": "36bff6c29af64692839d077febfc7079", "name": "Templates/Network devices"}},
		"templates":       list{tpl},
	}}
}
