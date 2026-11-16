#!/bin/sh
# Prüft die von internal/render erzeugten Dateien mit den echten Werkzeugen.
# Aufruf (als root, z. B. in WSL):  sh tools/check-render.sh <samples-dir>
# Samples erzeugen:  RENDER_SAMPLES=<dir> go test ./internal/render -run TestWriteSamples
#
#   nft -c          Syntax + Semantik der Firewall (ohne zu laden)
#   dnsmasq --test  Konfig-Syntax
#   wg setconf      wg-quick-Datei auf ein Test-Interface (falls Kernel-WireGuard da)
#   NM-Keyfiles     mit python3-configparser auf Struktur (nmcli fehlt meist)
set -u
dir=${1:?Samples-Verzeichnis angeben}
fail=0
ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FEHLER %s\n' "$*"; fail=1; }

have_wg_if=0
if ip link add vdcheck0 type wireguard 2>/dev/null; then have_wg_if=1; fi

for s in "$dir"/*/; do
	name=$(basename "$s")
	echo "== $name"
	if out=$(nft -c -f "$s/rules.nft" 2>&1); then ok "nft"; else bad "nft: $out"; fi
	if out=$(dnsmasq --test --conf-file="$s/dnsmasq.conf" 2>&1); then ok "dnsmasq"; else bad "dnsmasq: $out"; fi
	install -m 0600 "$s/wg0.conf" /tmp/vdcheck0.conf
	if stripped=$(wg-quick strip /tmp/vdcheck0.conf 2>/tmp/vdcheck0.err); then
		ok "wg-quick strip"
		if [ $have_wg_if = 1 ]; then
			printf '%s\n' "$stripped" >/tmp/vdcheck0.stripped
			if out=$(wg setconf vdcheck0 /tmp/vdcheck0.stripped 2>&1); then ok "wg setconf ($(wg show vdcheck0 allowed-ips | cut -f2))"; else bad "wg setconf: $out"; fi
		fi
	else
		bad "wg-quick strip: $(cat /tmp/vdcheck0.err)"
	fi
	rm -f /tmp/vdcheck0.conf /tmp/vdcheck0.stripped /tmp/vdcheck0.err
	for k in "$s"/*.nmconnection; do
		if out=$(python3 - "$k" <<'EOF' 2>&1
import configparser, sys
p = configparser.ConfigParser(interpolation=None, strict=True)
p.optionxform = str
p.read(sys.argv[1], encoding="utf-8")
assert p["connection"]["type"] in ("wifi", "gsm"), "type"
assert p["connection"]["uuid"].count("-") == 4, "uuid"
EOF
		); then ok "$(basename "$k")"; else bad "$(basename "$k"): $out"; fi
	done
done
[ $have_wg_if = 1 ] && ip link del vdcheck0
[ $have_wg_if = 1 ] || echo "(Kernel-WireGuard nicht verfügbar – wg setconf übersprungen)"
exit $fail
