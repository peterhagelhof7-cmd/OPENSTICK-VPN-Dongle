package zbxtpl

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/app"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
)

// Regeln aus Zabbix 7.0 C70XmlValidator.php (release/7.0):
var (
	// PREPROCESSING_STEP_TYPE_PARAMS: diese Typen brauchen "parameters"
	needParams = map[string]bool{"MULTIPLIER": true, "RTRIM": true, "LTRIM": true, "TRIM": true, "REGEX": true,
		"XMLPATH": true, "JSONPATH": true, "IN_RANGE": true, "MATCHES_REGEX": true, "NOT_MATCHES_REGEX": true,
		"CHECK_JSON_ERROR": true, "CHECK_XML_ERROR": true, "CHECK_REGEX_ERROR": true, "DISCARD_UNCHANGED_HEARTBEAT": true,
		"JAVASCRIPT": true, "PROMETHEUS_PATTERN": true, "PROMETHEUS_TO_JSON": true, "CSV_TO_JSON": true, "STR_REPLACE": true,
		"CHECK_NOT_SUPPORTED": true}
	// PREPROCESSING_STEP_TYPE_ERROR_HANDLING_SUBSET: nur diese dürfen error_handler haben
	errHandling = map[string]bool{"MULTIPLIER": true, "REGEX": true, "BOOL_TO_DECIMAL": true, "OCTAL_TO_DECIMAL": true,
		"HEX_TO_DECIMAL": true, "SIMPLE_CHANGE": true, "CHANGE_PER_SECOND": true, "XMLPATH": true, "JSONPATH": true,
		"IN_RANGE": true, "MATCHES_REGEX": true, "NOT_MATCHES_REGEX": true, "CHECK_JSON_ERROR": true, "CHECK_XML_ERROR": true,
		"CHECK_REGEX_ERROR": true, "PROMETHEUS_PATTERN": true, "PROMETHEUS_TO_JSON": true, "CSV_TO_JSON": true, "XML_TO_JSON": true}
	noParams     = map[string]bool{"BOOL_TO_DECIMAL": true, "CHANGE_PER_SECOND": true, "SIMPLE_CHANGE": true}
	valueTypes   = map[string]bool{"": true, "UNSIGNED": true, "FLOAT": true, "CHAR": true, "TEXT": true, "LOG": true}
	itemTypes    = map[string]bool{"ZABBIX_ACTIVE": true, "DEPENDENT": true, "SIMPLE": true}
	priorities   = map[string]bool{"INFO": true, "WARNING": true, "AVERAGE": true, "HIGH": true, "DISASTER": true}
	reUUIDv4     = regexp.MustCompile(`^[0-9a-f]{12}4[0-9a-f]{3}[89ab][0-9a-f]{15}$`)
	reUserMacro  = regexp.MustCompile(`\{\$[A-Z0-9_.]+\}`)
	reLLDMacro   = regexp.MustCompile(`\{#[A-Z0-9_]+\}`)
	numericTypes = map[string]bool{"": true, "UNSIGNED": true, "FLOAT": true}
)

// LLD-Makros, die app.Discovery je Gruppe liefert (siehe internal/app).
var groupMacros = map[string][]string{
	"pc": {"{#PCIP}"}, "wlan": {"{#IF}"}, "wlangw": {"{#WLANGW}"}, "lte": {"{#IF}"}, "vpn": {"{#IF}"},
	"targets": {"{#TNAME}", "{#TADDR}"},
}

func str(m obj, k string) string { s, _ := m[k].(string); return s }

func objs(v any) []obj {
	var out []obj
	if l, ok := v.(list); ok {
		for _, x := range l {
			out = append(out, x.(obj))
		}
	}
	return out
}

// refs extrahiert die Item-Schlüssel aus "/Template/key"-Verweisen.
func refs(expr string) []string {
	var keys []string
	prefix := "/" + Name + "/"
	for {
		i := strings.Index(expr, prefix)
		if i < 0 {
			return keys
		}
		expr = expr[i+len(prefix):]
		depth, end := 0, len(expr)
	loop:
		for j, r := range expr {
			switch {
			case r == '[':
				depth++
			case r == ']':
				depth--
			case depth == 0 && (r == ',' || r == ')'):
				end = j
				break loop
			}
		}
		keys = append(keys, expr[:end])
		expr = expr[end:]
	}
}

// statusPaths = alle JSON-Pfade des (voll belegten) Status von vpndongled.
func statusPaths(t *testing.T) map[string]bool {
	s := app.Status{Uplink: "wlan", Handshake: time.Now(), Version: "x",
		LTE: system.LTEStatus{Present: true, State: "s", Failed: "f", Unlock: "u", Operator: "o", Registered: "r", Tech: "t", Signal: 1}}
	b, _ := json.Marshal(s)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		paths[prefix] = true
		if mm, ok := v.(map[string]any); ok {
			for k, x := range mm {
				walk(prefix+"."+k, x)
			}
		}
	}
	walk("$", m)
	return paths
}

func TestTemplate(t *testing.T) {
	doc := Build()["zabbix_export"].(obj)
	if doc["version"] != "7.0" {
		t.Fatal("version")
	}
	tpl := objs(doc["templates"])[0]
	uuids := map[string]string{}
	checkUUID := func(where string, m obj) {
		u := str(m, "uuid")
		if !reUUIDv4.MatchString(u) {
			t.Errorf("%s: keine UUIDv4: %q", where, u)
		}
		if prev, dup := uuids[u]; dup {
			t.Errorf("UUID doppelt: %s und %s", prev, where)
		}
		uuids[u] = where
	}
	checkUUID("template", tpl)
	for _, g := range objs(doc["template_groups"]) {
		checkUUID("group", g)
	}

	macros := map[string]bool{}
	for _, m := range objs(tpl["macros"]) {
		macros[str(m, "macro")] = true
	}
	valuemaps := map[string]bool{}
	for _, v := range objs(tpl["valuemaps"]) {
		checkUUID("valuemap "+str(v, "name"), v)
		valuemaps[str(v, "name")] = true
	}
	paths := statusPaths(t)

	staticKeys := map[string]bool{}
	for _, it := range objs(tpl["items"]) {
		staticKeys[str(it, "key")] = true
	}

	checkItem := func(where string, it obj, scope map[string]bool, prototype bool, allowedLLD []string) {
		key := str(it, "key")
		where += " " + key
		checkUUID(where, it)
		if !itemTypes[str(it, "type")] {
			t.Errorf("%s: Typ %q", where, it["type"])
		}
		vt := str(it, "value_type")
		if !valueTypes[vt] {
			t.Errorf("%s: value_type %q", where, vt)
		}
		if _, ok := it["units"]; ok && !numericTypes[vt] {
			t.Errorf("%s: units nur für numerische Items", where)
		}
		if vmap, ok := it["valuemap"].(obj); ok {
			if vt == "TEXT" || vt == "LOG" || !valuemaps[str(vmap, "name")] {
				t.Errorf("%s: valuemap %v", where, vmap)
			}
		}
		if prototype {
			lld := reLLDMacro.FindAllString(key, -1)
			if len(lld) == 0 {
				t.Errorf("%s: Prototyp-Schlüssel ohne LLD-Makro", where)
			}
			for _, m := range append(lld, reLLDMacro.FindAllString(str(it, "name"), -1)...) {
				if !contains(allowedLLD, m) {
					t.Errorf("%s: LLD-Makro %s liefert die Discovery nicht (%v)", where, m, allowedLLD)
				}
			}
		}
		if str(it, "type") == "DEPENDENT" {
			mk := str(it["master_item"].(obj), "key")
			if !staticKeys[mk] || str(it, "delay") != "0" {
				t.Errorf("%s: master_item %q / delay", where, mk)
			}
		}
		for i, s := range objs(it["preprocessing"]) {
			typ := str(s, "type")
			_, hasP := s["parameters"]
			if needParams[typ] && !hasP {
				t.Errorf("%s: Schritt %d %s braucht parameters", where, i, typ)
			}
			if noParams[typ] && hasP {
				t.Errorf("%s: Schritt %d %s ohne parameters", where, i, typ)
			}
			if !needParams[typ] && !noParams[typ] {
				t.Errorf("%s: Schritt-Typ %s nicht geprüft", where, typ)
			}
			if _, ok := s["error_handler"]; ok && !errHandling[typ] {
				t.Errorf("%s: %s darf keinen error_handler haben", where, typ)
			}
			if typ == "JSONPATH" {
				p := s["parameters"].(list)[0].(string)
				if !paths[p] {
					t.Errorf("%s: JSONPath %s gibt es im Status von vpndongled nicht", where, p)
				}
			}
		}
		for _, k := range []string{"triggers", "trigger_prototypes"} {
			for _, tr := range objs(it[k]) {
				checkTrigger(t, where, tr, scope, macros, checkUUID, k == "trigger_prototypes")
			}
		}
	}

	for _, it := range objs(tpl["items"]) {
		if _, ok := it["trigger_prototypes"]; ok {
			t.Errorf("%s: trigger_prototypes an normalem Item", str(it, "key"))
		}
		checkItem("Item", it, staticKeys, false, nil)
	}
	for _, r := range objs(tpl["discovery_rules"]) {
		checkUUID("Regel "+str(r, "key"), r)
		group := strings.TrimSuffix(strings.TrimPrefix(str(r, "key"), "vpndongle.discovery["), "]")
		lld, ok := groupMacros[group]
		if !ok {
			t.Errorf("Regel %s: unbekannte Gruppe", str(r, "key"))
		}
		scope := map[string]bool{}
		for k := range staticKeys {
			scope[k] = true
		}
		for _, p := range objs(r["item_prototypes"]) {
			scope[str(p, "key")] = true
		}
		for _, p := range objs(r["item_prototypes"]) {
			if _, ok := p["triggers"]; ok {
				t.Errorf("%s: triggers an Prototyp (muss trigger_prototypes heißen)", str(p, "key"))
			}
			checkItem("Prototyp", p, scope, true, lld)
		}
	}
	for _, s := range []string{"name", "description"} {
		for _, m := range reUserMacro.FindAllString(str(tpl, s), -1) {
			if !macros[m] {
				t.Errorf("Template-%s: Makro %s fehlt", s, m)
			}
		}
	}
}

func checkTrigger(t *testing.T, where string, tr obj, scope, macros map[string]bool, checkUUID func(string, obj), prototype bool) {
	expr := str(tr, "expression")
	where += " Trigger " + str(tr, "name")
	checkUUID(where, tr)
	if !priorities[str(tr, "priority")] {
		t.Errorf("%s: Priorität %q", where, tr["priority"])
	}
	ks := refs(expr)
	if len(ks) == 0 {
		t.Errorf("%s: Expression ohne Item-Verweis: %s", where, expr)
	}
	hasProto := false
	for _, k := range ks {
		if !scope[k] {
			t.Errorf("%s: Item %q existiert nicht", where, k)
		}
		if reLLDMacro.MatchString(k) {
			hasProto = true
		}
	}
	if prototype && !hasProto {
		t.Errorf("%s: Trigger-Prototyp ohne Prototyp-Item", where)
	}
	for _, s := range []string{expr, str(tr, "name"), str(tr, "description")} {
		for _, m := range reUserMacro.FindAllString(s, -1) {
			if !macros[m] {
				t.Errorf("%s: Makro %s nicht definiert", where, m)
			}
		}
	}
	if strings.Count(expr, "(") != strings.Count(expr, ")") {
		t.Errorf("%s: Klammern unausgeglichen", where)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestRefs(t *testing.T) {
	got := refs("min(/" + Name + "/icmppingloss[{#A},3],5m)>1 and last(/" + Name + "/x)=1")
	if len(got) != 2 || got[0] != "icmppingloss[{#A},3]" || got[1] != "x" {
		t.Fatal(got)
	}
}

// Die Datei im Repo muss dem Generator entsprechen.
func TestFileUpToDate(t *testing.T) {
	want, err := JSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(File)))
	if err != nil {
		t.Fatalf("%v – go run ./cmd/zbxtemplate ausführen", err)
	}
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatal("zabbix/template_openstick_vpn_dongle.json veraltet – go run ./cmd/zbxtemplate ausführen")
	}
}
