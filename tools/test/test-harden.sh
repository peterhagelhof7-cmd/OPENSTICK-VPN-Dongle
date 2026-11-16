#!/bin/bash
# test-harden.sh - prüft system/harden-base.sh offline (ROOT=...) in WSL/Linux.
# Arbeitet nur in einem Temp-Ordner, das eigene System bleibt unberührt.
#
#   bash tools/test/test-harden.sh [rootfs.bin]
#
# Mit rootfs.bin (ext4, z. B. aus raw2qfil) laufen die Prüfungen gegen die echten
# Dateien des Images (per debugfs rdump), sonst gegen ein nachgebautes Minimal-System.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
SCRIPT="$HERE/../../system/harden-base.sh"
T=$(mktemp -d)
R="$T/root"
fails=0
ok() { printf '  ok     %s\n' "$1"; }
bad() { printf '  FEHLER %s\n' "$1"; fails=$((fails + 1)); }
check() { if eval "$1"; then ok "$2"; else bad "$2"; fi; }
trap 'chmod -R u+w "$T" 2>/dev/null; rm -rf "$T"' EXIT

mkdir -p "$R"
if [ -n "${1:-}" ]; then
	echo "== Dateien aus $1"
	for d in etc var/lib/dbus root; do
		mkdir -p "$R/$(dirname "$d")"
		debugfs -R "rdump /$d $R/$(dirname "$d")" "$1" 2>/dev/null
	done
	mkdir -p "$R/usr/lib/systemd"
	debugfs -R "rdump /usr/lib/systemd/system $R/usr/lib/systemd" "$1" 2>/dev/null
else
	echo "== nachgebautes Minimal-System"
	mkdir -p "$R/etc/ssh" "$R/etc/sudoers.d" "$R/etc/systemd/system" "$R/usr/lib/systemd/system" \
		"$R/etc/NetworkManager/system-connections" "$R/var/lib/dbus"
	printf 'root:x:0:0:root:/root:/bin/bash\nuser:x:1000:1000::/home/user:/bin/bash\n' >"$R/etc/passwd"
	printf 'root::20000:0:99999:7:::\nuser:$y$j9T$abc$def:20000:0:99999:7:::\n' >"$R/etc/shadow"
	echo 'user ALL=(ALL:ALL) NOPASSWD: ALL' >"$R/etc/sudoers.d/user"
	# wie im openstick-Image: authorized_keys aus 102 NUL-Bytes (nicht leer, aber wertlos)
	mkdir -p "$R/root/.ssh"
	head -c 102 /dev/zero >"$R/root/.ssh/authorized_keys"
	echo 25bb693bc92f48f094fec7493896c4fd >"$R/etc/machine-id"
	# wie im openstick-Image: eigene Datei mit derselben ID, kein Symlink
	cp "$R/etc/machine-id" "$R/var/lib/dbus/machine-id"
	ssh-keygen -q -t ed25519 -N '' -f "$R/etc/ssh/ssh_host_ed25519_key"
	printf '[Unit]\n[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=multi-user.target\n' >"$R/usr/lib/systemd/system/hostapd.service"
	printf '[Unit]\n[Service]\nExecStart=/bin/true\n[Install]\nWantedBy=usb-gadget.target\n' >"$R/etc/systemd/system/usb-gadget.service"
	nm="$R/etc/NetworkManager/system-connections"
	printf '[connection]\nid=hotspot\ntype=wifi\n[wifi]\nmode=ap\nssid=Openstick\n' >"$nm/hotspot.nmconnection"
	printf '[connection]\nid=lte\ntype=gsm\n[gsm]\napn=internet\n' >"$nm/lte.nmconnection"
	printf '[connection]\nid=usb\ntype=ethernet\ninterface-name=usb0\n' >"$nm/usb.nmconnection"
	printf '[connection]\nid=usb1\ntype=ethernet\ninterface-name=usb1\n' >"$nm/usb1.nmconnection"
fi
nm="$R/etc/NetworkManager/system-connections"
# zusätzlich: eigene Profile, die bleiben müssen
printf '[connection]\nid=Heim\ntype=wifi\n[wifi]\nmode=infrastructure\nssid=Heim\n' >"$nm/Heim.nmconnection"
printf '[connection]\nid=vpndongle-lte\ntype=gsm\n' >"$nm/vpndongle-lte.nmconnection"
chmod 640 "$R/etc/shadow"

id_before=$(cat "$R/etc/machine-id")
key_before=$(cat "$R"/etc/ssh/ssh_host_ed25519_key.pub)
mode_before=$(stat -c %a "$R/etc/shadow")

echo "== 1. erster Lauf (root ohne Passwort)"
out=$(ROOT="$R" sh "$SCRIPT" 2>&1); rc=$?
check '[ $rc = 0 ]' "läuft durch (rc=$rc)"
id_after=$(cat "$R/etc/machine-id")
check '[ "$id_after" != "$id_before" ] && echo "$id_after" | grep -qE "^[0-9a-f]{32}$"' "machine-id neu ($id_after)"
check '[ "$(readlink "$R/var/lib/dbus/machine-id")" = /etc/machine-id ]' "dbus-machine-id zeigt auf /etc/machine-id"
check '[ "$(cat "$R"/etc/ssh/ssh_host_ed25519_key.pub)" != "$key_before" ]' "SSH-Host-Schlüssel neu"
check '[ "$(awk -F: "\$1==\"root\"{print \$2}" "$R/etc/shadow")" = "!" ]' "leeres root-Passwort gesperrt"
check '[ "$(stat -c %a "$R/etc/shadow")" = "$mode_before" ]' "Rechte von /etc/shadow unverändert"
check 'awk -F: "\$1==\"user\"{print \$2}" "$R/etc/shadow" | grep -q "^\\\$y"' "user NICHT gesperrt, solange root unerreichbar"
check '[ -e "$R/etc/sudoers.d/user" ]' "sudo-Regel für user bleibt vorerst"
check 'echo "$out" | grep -q "Konto user bleibt aktiv"' "Warnung zum aktiven Konto ausgegeben"
check '[ "$(readlink "$R/etc/systemd/system/hostapd.service")" = /dev/null ]' "hostapd maskiert"
check '[ "$(readlink "$R/etc/systemd/system/usb-gadget.service")" = /dev/null ]' "Image-Gadget maskiert"
check '[ -f "$R/var/lib/vpndongle/image-units/usb-gadget.service" ]' "Image-Gadget-Unit vorher weggeräumt (nicht überschrieben)"
for p in hotspot lte usb usb1; do
	check '[ ! -e "$nm/$p.nmconnection" ] && [ -e "$R/var/lib/vpndongle/nm-image-profiles/$p.nmconnection" ]' "NM-Profil $p weggeräumt"
done
check '[ -e "$nm/Heim.nmconnection" ]' "WLAN-Client-Profil bleibt"
check '[ -e "$nm/vpndongle-lte.nmconnection" ]' "eigenes LTE-Profil bleibt"
check '[ -e "$R/var/lib/vpndongle/base-hardened" ]' "Marker gesetzt"

echo "== 2. zweiter Lauf, root hat jetzt ein Passwort"
awk -F: -v OFS=: '$1=="root"{$2="$y$j9T$roothash$xyz"}{print}' "$R/etc/shadow" >"$T/s" && cat "$T/s" >"$R/etc/shadow"
key2=$(cat "$R"/etc/ssh/ssh_host_ed25519_key.pub)
out=$(ROOT="$R" sh "$SCRIPT" 2>&1); rc=$?
check '[ $rc = 0 ]' "läuft durch (rc=$rc)"
check '[ "$(cat "$R/etc/machine-id")" = "$id_after" ]' "machine-id NICHT erneut geändert"
check '[ "$(cat "$R"/etc/ssh/ssh_host_ed25519_key.pub)" = "$key2" ]' "SSH-Schlüssel NICHT erneut geändert"
check 'awk -F: "\$1==\"user\"{print \$2}" "$R/etc/shadow" | grep -q "^!\\\$y"' "user-Passwort gesperrt (Hash bleibt hinter !)"
check '[ "$(awk -F: "\$1==\"user\"{print \$7}" "$R/etc/passwd")" = /usr/sbin/nologin ]' "user-Shell nologin"
check '[ ! -e "$R/etc/sudoers.d/user" ]' "sudo-Regel für user entfernt"
check '[ "$(awk -F: "\$1==\"root\"{print \$2}" "$R/etc/shadow")" = "\$y\$j9T\$roothash\$xyz" ]' "root-Passwort unangetastet"

echo "== 3. dritter Lauf ändert nichts mehr"
sum1=$(cd "$R" && find etc -type f -exec md5sum {} + 2>/dev/null | sort | md5sum)
ROOT="$R" sh "$SCRIPT" >/dev/null 2>&1
sum2=$(cd "$R" && find etc -type f -exec md5sum {} + 2>/dev/null | sort | md5sum)
check '[ "$sum1" = "$sum2" ]' "idempotent (keine Datei unter /etc verändert)"

echo "== 4. shellcheck"
check 'shellcheck -s sh "$SCRIPT"' "harden-base.sh ohne Befunde"
check 'shellcheck -s sh -S warning "$HERE/../../system/install.sh"' "install.sh ohne Warnungen"

if [ "$fails" -gt 0 ]; then echo "$fails Prüfung(en) fehlgeschlagen"; exit 1; fi
echo "Alle Prüfungen bestanden"
