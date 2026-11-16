#!/bin/sh
# gadget-probe.sh - unser NCM-Gadget LIVE testen, mit automatischem Rückfall.
# Auf dem Stick als root, losgelöst von der SSH-Sitzung starten:
#
#   systemd-run --unit=gadget-probe --collect sh /run/gadget-probe.sh [sekunden]
#
# Ablauf: Image-Gadget "dual" (gt) entfernen -> usb-gadget.sh start (NCM,
# usb0 192.168.226.1/29) + Test-DHCP ohne Gateway/DNS -> auf /run/gadget-ok
# warten. Kommt die Bestätigung nicht (touch /run/gadget-ok über das neue
# USB-Netz), wird unser Gadget abgebaut und "dual" wieder geladen
# (usb1 = 192.168.5.1, RNDIS). Log: journalctl -u gadget-probe
set -u
WAIT="${1:-120}"
G=/sys/kernel/config/usb_gadget/vpndongle
GADGET=/usr/local/lib/vpndongle/usb-gadget.sh
log() { echo "gadget-probe: $*"; }

restore_image() {
	log "RUECKFALL auf Image-Gadget"
	pkill -f 'dnsmasq.*gadget-probe' 2>/dev/null || true
	if [ -d "$G" ]; then
		echo "" >"$G/UDC" 2>/dev/null || true
		rm -f "$G/configs/c.1/ncm.usb0"
		rmdir "$G/configs/c.1/strings/0x409" "$G/configs/c.1" "$G/functions/ncm.usb0" \
			"$G/strings/0x409" "$G" 2>/dev/null || true
	fi
	gt load dual.scheme dual || log "gt load fehlgeschlagen"
	sleep 15
	if ! ip -4 addr show usb1 2>/dev/null | grep -q '192.168.5.1/'; then
		log "usb1 ohne Adresse - setze 192.168.5.1 + Not-DHCP"
		ip link set usb1 up
		ip addr replace 192.168.5.1/24 dev usb1
		# eigene Unit: Prozesse dieser Probe-Unit beendet systemd, sobald das Skript endet
		systemd-run --unit=gadget-probe-dhcp-usb1 --collect \
			dnsmasq --keep-in-foreground --conf-file=/dev/null --port=0 \
			--interface=usb1 --bind-interfaces --except-interface=lo \
			--dhcp-range=192.168.5.100,192.168.5.150,255.255.255.0,10m \
			--dhcp-option=3 --dhcp-option=6 --dhcp-leasefile=/run/gadget-probe-usb1.leases \
			|| log "Not-DHCP auf usb1 startet nicht"
	fi
	log "Image-Gadget wieder aktiv: $(ip -br addr show usb1 2>/dev/null)"
}

rm -f /run/gadget-ok
log "entferne Image-Gadget dual"
gt rm -rf dual 2>/dev/null || log "gt rm meldet Fehler (evtl. schon weg)"
if ! sh "$GADGET" start; then
	log "usb-gadget.sh start FEHLGESCHLAGEN"
	restore_image
	exit 1
fi
# Test-DHCP: nur Adresse, kein Gateway (3) und kein DNS (6) -> PC-Internet bleibt unberührt
dnsmasq --conf-file=/dev/null --pid-file=/run/gadget-probe-dnsmasq.pid --port=0 \
	--interface=usb0 --bind-interfaces --except-interface=lo \
	--dhcp-range=192.168.226.2,192.168.226.6,255.255.255.248,10m \
	--dhcp-option=3 --dhcp-option=6 --dhcp-leasefile=/run/gadget-probe.leases \
	|| log "Test-DHCP auf usb0 startet nicht (PC braucht dann feste IP 192.168.226.2/29)"
log "NCM aktiv: $(ip -br addr show usb0) - warte ${WAIT}s auf /run/gadget-ok"
i=0
while [ "$i" -lt "$WAIT" ]; do
	if [ -e /run/gadget-ok ]; then
		log "BESTAETIGT - NCM-Gadget funktioniert, bleibt aktiv"
		pkill -f 'dnsmasq.*gadget-probe' 2>/dev/null || true
		exit 0
	fi
	sleep 1
	i=$((i + 1))
done
restore_image
exit 2
