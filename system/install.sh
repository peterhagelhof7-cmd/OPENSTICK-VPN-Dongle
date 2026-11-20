#!/bin/sh
# Installation auf dem Stick (als root, aus dem entpackten Paketordner):
#   sh install.sh            installieren / aktualisieren
#   sh install.sh --check    nur prüfen, nichts ändern
#
# Startet NICHT neu und aktiviert das USB-Gadget erst beim nächsten Boot,
# damit eine bestehende SSH-Verbindung über USB nicht abreißt.
set -eu
cd "$(dirname "$0")"

# Zabbix-Reihe von Proxy und Agent = Hauptversion des Zabbix-Servers
ZBX_SERIES=7.4

say() { printf '\n== %s\n' "$*"; }
warn() { printf '!! %s\n' "$*"; }

[ "$(id -u)" = 0 ] || { echo "bitte als root ausführen" >&2; exit 1; }

say "Bestandsaufnahme"
. /etc/os-release && echo "System: $PRETTY_NAME, Kernel $(uname -r), $(uname -m)"
for s in NetworkManager ModemManager rmtfs qrtr-ns; do
	printf '  %-16s %s\n' "$s" "$(systemctl is-active "$s" 2>/dev/null || true)"
done
echo "  USB-Controller: $(ls /sys/class/udc 2>/dev/null | tr '\n' ' ')"
echo "  Gadgets:        $(ls /sys/kernel/config/usb_gadget 2>/dev/null | tr '\n' ' ')"
echo "  LEDs:           $(ls /sys/class/leds 2>/dev/null | tr '\n' ' ')"
echo "  Netz:           $(ip -br link | awk '{print $1}' | tr '\n' ' ')"
if command -v mmcli >/dev/null; then
	mmcli -L 2>/dev/null | sed 's/^/  /' || true
else
	warn "mmcli fehlt"
fi
nmcli -t -f NAME,TYPE,DEVICE connection show 2>/dev/null | grep -Ei 'usb|gadget|rndis|ether' | sed 's/^/  NM-Profil am USB: /' || true

[ "${1:-}" = "--check" ] && exit 0

case "${VERSION_ID:-}" in
12 | 13) ;;
*)
	echo "Debian $VERSION_ID wird nicht unterstützt (Zabbix gibt es für arm64 nur für Debian 12/13)." >&2
	exit 1
	;;
esac

say "Pakete"
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
	wireguard-tools dnsmasq-base nftables network-manager modemmanager iproute2 \
	ca-certificates wget fping util-linux openssh-server
# Die System-dnsmasq (falls vorhanden) würde mit unserer Instanz kollidieren
if systemctl list-unit-files dnsmasq.service >/dev/null 2>&1; then
	systemctl disable --now dnsmasq.service 2>/dev/null || true
fi

say "Zeitzone"
# Images kommen mit UTC -> deutsche Zeit inkl. Sommer-/Winterzeit; eine
# bewusst gewählte andere Zone bleibt
tz=$(timedatectl show -p Timezone --value 2>/dev/null || true)
case "$tz" in
"" | UTC | Etc/UTC)
	timedatectl set-timezone Europe/Berlin
	echo "Zeitzone Europe/Berlin (vorher ${tz:-unbekannt})"
	;;
*) echo "Zeitzone bleibt $tz" ;;
esac

say "Zabbix $ZBX_SERIES (Proxy + Agent)"
# Der Proxy muss dieselbe Hauptversion haben wie der Zabbix-Server (ein älterer
# Proxy gilt dort als „veraltet“ und bekommt keine Konfiguration mehr). apt
# hält ihn innerhalb der Reihe aktuell; eine neue Reihe kommt nur hierüber.
zbx_have=$(dpkg-query -W -f='${Version}' zabbix-release 2>/dev/null | sed 's/^[0-9]*://; s/-.*//' || true)
if [ "$zbx_have" != "$ZBX_SERIES" ]; then
	[ -n "$zbx_have" ] && echo "Zabbix-Paketquelle $zbx_have -> $ZBX_SERIES"
	deb="zabbix-release_latest_${ZBX_SERIES}+debian${VERSION_ID}_all.deb"
	tmp=$(mktemp -d)
	wget -q -O "$tmp/$deb" "https://repo.zabbix.com/zabbix/$ZBX_SERIES/release/debian/pool/main/z/zabbix-release/$deb"
	dpkg -i --force-confnew "$tmp/$deb"
	rm -rf "$tmp"
	apt-get update
	if [ -n "$zbx_have" ] && dpkg -s zabbix-proxy-sqlite3 >/dev/null 2>&1; then
		# Neue Hauptversion: Die SQLite-Datenbank des Proxys ist nur ein Puffer;
		# der neue Proxy legt sie selbst an (sonst Schema-Upgrade auf dem Flash)
		systemctl stop zabbix-proxy 2>/dev/null || true
		rm -f /var/lib/zabbix/zabbix_proxy.db /var/lib/zabbix/zabbix_proxy.db-journal
	fi
fi
zbx_fresh=0
dpkg -s zabbix-proxy-sqlite3 >/dev/null 2>&1 || zbx_fresh=1
# confold: Die Konfigs erzeugt vpndongled, das Paket soll sie nicht ersetzen
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
	-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \
	zabbix-proxy-sqlite3 zabbix-agent
if [ "$zbx_fresh" = 1 ]; then
	# Pakete starten die Dienste mit Beispielkonfig – aus, bis Zabbix in der
	# Weboberfläche eingerichtet ist (vpndongled schaltet sie dann ein)
	systemctl disable --now zabbix-proxy zabbix-agent 2>/dev/null || true
fi
install -d -o zabbix -g zabbix -m 0750 /var/lib/zabbix

say "NetBird-Client"
if ! dpkg -s netbird >/dev/null 2>&1; then
	wget -q -O /usr/share/keyrings/netbird-archive-keyring.asc https://pkgs.netbird.io/debian/public.key
	echo 'deb [signed-by=/usr/share/keyrings/netbird-archive-keyring.asc] https://pkgs.netbird.io/debian stable main' \
		>/etc/apt/sources.list.d/netbird.list
	apt-get update
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends netbird
	# Das Paket startet den Dienst sofort; vpndongled schaltet ihn nur ein,
	# wenn in der Weboberfläche NetBird als VPN-Art gewählt ist
	systemctl disable --now netbird 2>/dev/null || true
fi
echo "NetBird $(netbird version 2>/dev/null || echo fehlt)"

say "Tailscale-Client"
if ! dpkg -s tailscale >/dev/null 2>&1; then
	wget -q -O /usr/share/keyrings/tailscale-archive-keyring.gpg \
		"https://pkgs.tailscale.com/stable/debian/${VERSION_CODENAME}.noarmor.gpg"
	wget -q -O /etc/apt/sources.list.d/tailscale.list \
		"https://pkgs.tailscale.com/stable/debian/${VERSION_CODENAME}.tailscale-keyring.list"
	apt-get update
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends tailscale
	# wie NetBird: nur an, wenn Tailscale als VPN-Art gewählt ist
	systemctl disable --now tailscaled 2>/dev/null || true
fi
echo "Tailscale $(tailscale version 2>/dev/null | head -n 1 || echo fehlt)"
# Paket-Cache (.deb-Dateien) vom Flash entfernen
apt-get clean

say "Dateien"
install -D -m 0755 bin/vpndongled /usr/local/bin/vpndongled
install -D -m 0755 system/usb-gadget.sh /usr/local/lib/vpndongle/usb-gadget.sh
install -D -m 0755 system/update.sh /usr/local/lib/vpndongle/update.sh
install -D -m 0644 system/vpndongle-update.service /etc/systemd/system/vpndongle-update.service
install -D -m 0644 system/vpndongle-update.timer /etc/systemd/system/vpndongle-update.timer
install -D -m 0644 system/vpndongle-gadget.service /etc/systemd/system/vpndongle-gadget.service
install -D -m 0644 system/vpndongle-dnsmasq.service /etc/systemd/system/vpndongle-dnsmasq.service
install -D -m 0644 system/vpndongled.service /etc/systemd/system/vpndongled.service
install -D -m 0644 system/NetworkManager-90-vpndongle.conf /etc/NetworkManager/conf.d/90-vpndongle.conf
install -D -m 0644 system/sysctl-90-vpndongle.conf /etc/sysctl.d/90-vpndongle.conf
install -D -m 0644 system/journald-90-vpndongle.conf /etc/systemd/journald.conf.d/90-vpndongle.conf
install -D -m 0644 system/sshd-90-vpndongle.conf /etc/ssh/sshd_config.d/90-vpndongle.conf
if ! sshd -t; then
	warn "sshd-Konfig ungültig – Drop-in wieder entfernt, sshd unverändert"
	rm -f /etc/ssh/sshd_config.d/90-vpndongle.conf
fi
install -d -m 0700 /etc/vpndongle /etc/wireguard
install -d -m 0755 /var/lib/vpndongle/files /run/vpndongle
install -d -m 0700 /var/lib/vpndongle/update
sysctl -q --system || true

say "Härtung des Basis-Images"
sh system/harden-base.sh

say "Dienste"
systemctl daemon-reload
systemctl enable vpndongle-gadget.service vpndongled.service ssh.service
systemctl reload ssh.service 2>/dev/null || systemctl restart ssh.service
# Weitere Konten mit Passwort (z. B. Standardbenutzer des Images) melden
others=$(awk -F: '$2 !~ /^[!*]/ && $2 != "" && $1 != "root" {print $1}' /etc/shadow | tr '\n' ' ')
[ -n "$others" ] && warn "Konten mit Passwort außer root: $others – ggf. sperren (passwd -l <name>)"
systemctl reload NetworkManager || true
# Journal-Grenzen sofort anwenden und Altbestand auf die Grenze kürzen
mkdir -p /var/log/journal
systemctl restart systemd-journald
journalctl --vacuum-size=50M >/dev/null 2>&1 || true

say "Speicher"
df -h / | sed 's/^/  /'
free -m | sed 's/^/  /'

if systemctl is-active -q vpndongled.service; then
	systemctl restart vpndongled.service # Update: neue Version sofort
	echo "vpndongled neu gestartet ($(/usr/local/bin/vpndongled -version))"
fi

say "Fertig"
cat <<'EOF'
Beim nächsten Neustart:
  - meldet sich der Stick am PC als USB-Netzwerkadapter (NCM), PC bekommt 192.168.226.2
  - Weboberfläche: http://192.168.226.1/  (admin/admin – sofort ändern)
  - SSH nur noch über USB (192.168.226.1) und den VPN-Tunnel; Root-Passwort in der Weboberfläche setzen
  - Kommt das USB-Netz nicht hoch, bleibt SSH als Notzugang auch über WLAN offen
Vorher sicherstellen, dass SSH über WLAN geht (Rückweg für genau diesen Fall)!
Neustart:  systemctl reboot
EOF
