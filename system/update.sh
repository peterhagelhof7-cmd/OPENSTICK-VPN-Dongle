#!/bin/sh
# vpndongle-update – aktualisiert den Stick:
#   1. Debian- und Zabbix-Pakete (apt upgrade, Zabbix bleibt in seiner Reihe; eine
#      neue Reihe bringt install.sh eines neuen Pakets mit)
#   2. VPN-Dongle-Software: neueres signiertes GitHub-Release (falls eingeschaltet)
#      oder ein hochgeladenes Paket (/var/lib/vpndongle/update/)
#
# Läuft als systemd-Dienst vpndongle-update.service (Web-UI-Knopf oder
# wöchentlicher Timer); Ausgabe landet im Journal.
#
#   update.sh            normaler Lauf (respektiert LTE-Sperre)
#   update.sh --force    auch über LTE
#
# Schutz:
#   - Kernel/Firmware/Bootloader-Pakete werden gehalten (der Kernel des Sticks
#     kommt nicht aus Debian; ein Kernel-Update würde nicht gebootet bzw.
#     Module passen nicht mehr)
#   - kein dist-upgrade (keine Pakete entfernen), Konfigdateien bleiben (confold)
#   - Abbruch bei < 300 MB freiem Platz, nur ein Lauf gleichzeitig
set -eu

ENV=/etc/vpndongle/update.env
PKGDIR=/var/lib/vpndongle/update
MIN_FREE_KB=307200
FORCE=0
[ "${1:-}" = "--force" ] && FORCE=1

UPDATE_ALLOW_LTE=0
UPDATE_REBOOT=0
UPDATE_GITHUB=0
# shellcheck disable=SC1090
[ -r "$ENV" ] && . "$ENV"

exec 9>/run/vpndongle-update.lock
flock -n 9 || { echo "Update läuft bereits"; exit 0; }

say() { echo "== $*"; }

say "Start $(date '+%Y-%m-%d %H:%M:%S')"

# --- Uplink prüfen (LTE = Datenvolumen) --------------------------------------
st=$(/usr/local/bin/vpndongled -zbx status 2>/dev/null || true)
case "$st" in
*'"uplink":"lte"'*)
	if [ "$FORCE" = 0 ] && [ "$UPDATE_ALLOW_LTE" != 1 ]; then
		echo "Uplink ist LTE und Updates über LTE sind aus – übersprungen."
		exit 0
	fi
	;;
*'"uplink":""'*)
	echo "Kein Uplink – übersprungen."
	exit 0
	;;
esac

# --- Platz prüfen -------------------------------------------------------------
free_kb=$(df --output=avail -k / | tail -n 1 | tr -d ' ')
if [ "$free_kb" -lt "$MIN_FREE_KB" ]; then
	echo "Nur $((free_kb / 1024)) MB frei (< 300 MB) – Abbruch."
	exit 1
fi

# --- Kernel/Firmware halten ---------------------------------------------------
held=$(dpkg-query -W -f='${Package} ${db:Status-Abbrev}\n' \
	'linux-image*' 'linux-headers*' 'linux-modules*' 'firmware-*' '*-firmware' \
	'u-boot*' 'lk2nd*' 'qcom-*' 2>/dev/null | awk '$2 ~ /^ii/ {print $1}' || true)
if [ -n "$held" ]; then
	# shellcheck disable=SC2086
	apt-mark hold $held >/dev/null
	echo "Gehalten: $(echo "$held" | tr '\n' ' ')"
fi

# --- Pakete -------------------------------------------------------------------
say "apt update"
apt-get update -q
say "apt upgrade"
upg=$(apt-get -s upgrade | grep -c '^Inst ' || true)
echo "$upg Paket(e) zu aktualisieren"
if [ "$upg" -gt 0 ]; then
	DEBIAN_FRONTEND=noninteractive apt-get -y -q \
		-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold upgrade
fi
apt-get clean

# --- VPN-Dongle-Software aus GitHub -------------------------------------------
# vpndongled holt nur ein NEUERES Release und nur, wenn die Ed25519-Signatur und
# die Prüfsumme stimmen; sonst bleibt alles, wie es ist.
pkg="$PKGDIR/vpndongle-update.tar.gz"
if [ "$UPDATE_GITHUB" = 1 ] && [ ! -f "$pkg" ]; then
	say "VPN-Dongle-Software: neues Release auf GitHub?"
	/usr/local/bin/vpndongled -swupdate fetch -swupdate-dest "$pkg" ||
		echo "GitHub-Update übersprungen (Meldung oben)"
fi

# --- eigenes Paket ------------------------------------------------------------
if [ -f "$pkg" ]; then
	say "VPN-Dongle-Paket einspielen"
	work=$(mktemp -d)
	trap 'rm -rf "$work"' EXIT
	if tar -xzf "$pkg" -C "$work" && [ -f "$work/install.sh" ] && [ -f "$work/bin/vpndongled" ]; then
		chmod 0755 "$work/bin/vpndongled" # Windows-tar setzt kein x-Bit
		new=$("$work/bin/vpndongled" -version 2>/dev/null || echo "?")
		old=$(/usr/local/bin/vpndongled -version 2>/dev/null || echo "?")
		echo "Version $old -> $new"
		rm -f "$pkg"
		sh "$work/install.sh"
	else
		echo "Paket ungültig (install.sh/bin/vpndongled fehlt) – verworfen."
		rm -f "$pkg"
		exit 1
	fi
fi

# --- Neustart? ----------------------------------------------------------------
if [ -f /run/reboot-required ]; then
	if [ "$UPDATE_REBOOT" = 1 ]; then
		say "Neustart in 1 Minute (vom Update verlangt)"
		shutdown -r +1 "vpndongle-update" || systemctl reboot
	else
		echo "Hinweis: Ein Neustart wird empfohlen ($(cat /run/reboot-required.pkgs 2>/dev/null | tr '\n' ' '))."
	fi
fi
say "Fertig $(date '+%Y-%m-%d %H:%M:%S')"
