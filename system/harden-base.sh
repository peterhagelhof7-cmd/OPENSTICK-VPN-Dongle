#!/bin/sh
# Härtung des Basis-Images. Wird von install.sh aufgerufen, kann aber auch
# allein laufen (als root). Zugeschnitten auf das openstick-installer-Image
# (Debian 13, gleiche Dateien auf allen Sticks), schadet aber auf anderen
# Images nicht: jeder Schritt prüft, ob es das Betroffene gibt.
#
#   sh harden-base.sh                 auf dem Stick
#   ROOT=/pfad sh harden-base.sh      offline gegen ein entpacktes Dateisystem (Test)
#
# Einmalig (Marker /var/lib/vpndongle/base-hardened):
#   - neue machine-id (Image: in allen Sticks gleich; Gadget-MAC/Seriennummer hängen daran)
#   - neue SSH-Host-Schlüssel (Image: in allen Sticks gleich)
# Bei jedem Lauf:
#   - leeres root-Passwort sperren
#   - Image-Konten (IMAGE_USERS, Standard "user") sperren + sudo-Regel entfernen,
#     aber nur wenn root erreichbar ist (Passwort oder authorized_keys)
#   - Image-Dienste aus: hostapd, usb-gadget.service (eigenes ECM/RNDIS-Gadget)
#   - NM-Profile des Images wegräumen: WLAN-Zugangspunkt, LTE (gsm), USB-Schnittstellen
#
# Läuft live NICHTS ab, was eine bestehende SSH-Verbindung trennt: das
# Image-Gadget und die NM-Profile verschwinden erst beim nächsten Neustart.
set -eu

ROOT="${ROOT:-}"
IMAGE_USERS="${IMAGE_USERS:-user}"
STATE="$ROOT/var/lib/vpndongle"
MARK="$STATE/base-hardened"
NMDIR="$ROOT/etc/NetworkManager/system-connections"
PARK="$STATE/nm-image-profiles"

note() { printf '  %s\n' "$*"; }
warn() { printf '!! %s\n' "$*"; }
live() { [ -z "$ROOT" ]; }

# Feld 2 einer shadow-Zeile ändern, Rechte/Eigentümer der Datei bleiben
# (Inhalt wird über eine Kopie zurückgeschrieben, nicht ersetzt)
shadow_set() { # $1 = Konto, $2 = awk-Ausdruck für das neue Feld 2 (Variable p = alt)
	f="$ROOT/etc/shadow"
	tmp=$(mktemp)
	awk -F: -v OFS=: -v u="$1" "\$1 == u { p = \$2; \$2 = $2 } { print }" "$f" >"$tmp"
	cat "$tmp" >"$f"
	rm -f "$tmp"
}
shadow_get() { awk -F: -v u="$1" '$1 == u { print $2 }' "$ROOT/etc/shadow"; }
usable_hash() { case "$1" in "" | "!"* | "*"*) return 1 ;; *) return 0 ;; esac; }

unit_exists() {
	for d in etc/systemd/system usr/lib/systemd/system lib/systemd/system; do
		[ -e "$ROOT/$d/$1" ] && return 0
	done
	return 1
}
unit_off() { # $1 = Unit, $2 = "now" -> sofort stoppen
	unit_exists "$1" || return 0
	u="$ROOT/etc/systemd/system/$1"
	# Liegt die Unit als echte Datei in /etc/systemd/system, verweigert
	# "systemctl mask" -> erst wegräumen (Image: usb-gadget.service)
	if [ -f "$u" ] && [ ! -L "$u" ]; then
		if live; then systemctl disable "$1" >/dev/null 2>&1 || true; fi
		mkdir -p "$STATE/image-units"
		mv "$u" "$STATE/image-units/"
		live && systemctl daemon-reload
	fi
	if live; then
		if [ "${2:-}" = now ]; then
			systemctl disable --now "$1" >/dev/null 2>&1 || true
		else
			systemctl disable "$1" >/dev/null 2>&1 || true
		fi
		systemctl mask "$1" >/dev/null 2>&1 || warn "$1 liess sich nicht maskieren"
	else
		systemctl --root="$ROOT" disable "$1" >/dev/null 2>&1 || true
		# mask = Symlink nach /dev/null (offline ohne systemctl prüfbar)
		ln -sf /dev/null "$ROOT/etc/systemd/system/$1"
	fi
	note "Dienst $1 abgeschaltet"
}

mkdir -p "$STATE"

# ---------------------------------------------------------------- einmalig
if [ ! -e "$MARK" ]; then
	old=$(cat "$ROOT/etc/machine-id" 2>/dev/null || true)
	# Auch die dbus-Kopie weg: systemd-machine-id-setup übernimmt sonst die alte
	# ID von dort (im openstick-Image eine eigene Datei, kein Symlink).
	dbus_id=0
	if [ -e "$ROOT/var/lib/dbus/machine-id" ] || [ -L "$ROOT/var/lib/dbus/machine-id" ]; then dbus_id=1; fi
	rm -f "$ROOT/etc/machine-id" "$ROOT/var/lib/dbus/machine-id"
	if live; then
		systemd-machine-id-setup >/dev/null 2>&1
	else
		systemd-machine-id-setup --root="$ROOT" >/dev/null 2>&1
	fi
	new=$(cat "$ROOT/etc/machine-id")
	[ -n "$new" ] && [ "$new" != "$old" ] || { warn "machine-id nicht erneuert"; exit 1; }
	[ "$dbus_id" = 1 ] && ln -sf /etc/machine-id "$ROOT/var/lib/dbus/machine-id"
	note "machine-id neu erzeugt (wirkt für Gadget-MAC/-Seriennummer ab dem nächsten Start)"

	if [ -d "$ROOT/etc/ssh" ]; then
		rm -f "$ROOT"/etc/ssh/ssh_host_*
		if live; then ssh-keygen -A >/dev/null; else ssh-keygen -A -f "$ROOT" >/dev/null; fi
		note "SSH-Host-Schlüssel neu erzeugt - am PC den alten Eintrag löschen: ssh-keygen -R <adresse>"
		for k in "$ROOT"/etc/ssh/ssh_host_*_key.pub; do
			[ -e "$k" ] && note "  $(ssh-keygen -lf "$k")"
		done
		live && { systemctl reload ssh.service 2>/dev/null || true; }
	fi
	date -u +%Y-%m-%dT%H:%M:%SZ >"$MARK"
fi

# ---------------------------------------------------------------- Konten
if grep -q '^root:' "$ROOT/etc/shadow" && [ -z "$(shadow_get root)" ]; then
	shadow_set root '"!"'
	note "root hatte ein LEERES Passwort - gesperrt (Passwort in der Weboberfläche setzen)"
fi

root_ok=0
usable_hash "$(shadow_get root)" && root_ok=1
# Nur echte Schlüsselzeilen zählen: im openstick-Image liegt eine authorized_keys
# aus lauter NUL-Bytes (nicht leer, aber wertlos)
has_key() { [ -f "$1" ] && grep -aqE '^[[:space:]]*(ssh-|ecdsa-|sk-)[A-Za-z0-9@.-]+ [A-Za-z0-9+/=]{40,}' "$1"; }
has_key "$ROOT/root/.ssh/authorized_keys" && root_ok=1

for u in $IMAGE_USERS; do
	grep -q "^$u:" "$ROOT/etc/passwd" || continue
	if [ "$root_ok" != 1 ]; then
		warn "Konto $u bleibt aktiv: root hat weder Passwort noch SSH-Schlüssel."
		warn "  Erst 'passwd root' (oder Schlüssel nach /root/.ssh/authorized_keys), dann install.sh erneut."
		continue
	fi
	h=$(shadow_get "$u")
	case "$h" in
	"!"*) ;;
	*)
		shadow_set "$u" '"!" p'
		note "Konto $u gesperrt (Passwort)"
		;;
	esac
	awk -F: -v OFS=: -v u="$u" '$1 == u { $7 = "/usr/sbin/nologin" } { print }' "$ROOT/etc/passwd" >"$STATE/.passwd.tmp"
	cat "$STATE/.passwd.tmp" >"$ROOT/etc/passwd"
	rm -f "$STATE/.passwd.tmp"
	if [ -e "$ROOT/etc/sudoers.d/$u" ]; then
		rm -f "$ROOT/etc/sudoers.d/$u"
		note "sudo-Regel für $u entfernt"
	fi
	has_key "$ROOT/home/$u/.ssh/authorized_keys" && warn "$u hat SSH-Schlüssel in /home/$u/.ssh/authorized_keys (Login per nologin trotzdem gesperrt)"
done

# ---------------------------------------------------------------- Dienste
unit_off hostapd.service now
# Image-Gadget (ECM+RNDIS über gt): NICHT sofort stoppen, die SSH-Verbindung
# läuft evtl. darüber. Ab dem nächsten Start übernimmt vpndongle-gadget.
unit_off usb-gadget.service

# ---------------------------------------------------------------- NM-Profile
if [ -d "$NMDIR" ]; then
	for f in "$NMDIR"/*.nmconnection; do
		[ -e "$f" ] || continue
		case "$(basename "$f")" in vpndongle-*) continue ;; esac
		why=""
		grep -qiE '^type=(gsm|cdma)$' "$f" && why="LTE-Profil"
		grep -qiE '^mode=ap$' "$f" && why="WLAN-Zugangspunkt"
		grep -qiE '^interface-name=usb[0-9]+$' "$f" && why="USB-Schnittstelle"
		[ -n "$why" ] || continue
		mkdir -p "$PARK"
		chmod 700 "$PARK"
		mv "$f" "$PARK/"
		note "NM-Profil $(basename "$f") weggeräumt ($why) -> $PARK"
	done
fi
exit 0
