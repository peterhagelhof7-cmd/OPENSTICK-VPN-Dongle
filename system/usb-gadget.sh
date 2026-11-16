#!/bin/sh
# USB-Gadget des VPN-Dongles: EINE NCM-Netzwerkfunktion (keine Composite mit
# Konsole – macOS/iPadOS binden NCM sonst nicht, Erkenntnis aus ESP 7362bf7).
# Windows 10 2004+/11, macOS, iPadOS und Linux erkennen NCM ohne Treiber.
#
#   usb-gadget.sh start | stop | status
#
# TODO(HW): am UZ801 prüfen – UDC-Name, ob die Original-Firmware/das
# OpenStick-Image schon ein Gadget (RNDIS/g_ether) anlegt, und ob die
# Schnittstelle "usb0" heißt.
set -eu

G=/sys/kernel/config/usb_gadget/vpndongle
IFNAME=usb0
ADDR=192.168.226.1/29

serial() {
	# stabil je Gerät: aus machine-id (nicht die IMEI!)
	md5sum /etc/machine-id | cut -c1-12 | tr a-f A-F
}

mac() { # $1 = erstes Byte (lokal verwaltet, unicast)
	h=$(md5sum /etc/machine-id | cut -c13-22)
	echo "$1:$(echo "$h" | sed 's/../&:/g; s/:$//')"
}

start() {
	modprobe libcomposite 2>/dev/null || true
	mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config

	# Fremde Gadgets (z. B. RNDIS des Images) vom Controller lösen
	for g in /sys/kernel/config/usb_gadget/*; do
		[ -d "$g" ] || continue
		[ "$g" = "$G" ] && continue
		# NICHT [ -s UDC ]: configfs-Dateien melden immer eine Größe > 0
		if [ -n "$(cat "$g/UDC" 2>/dev/null)" ]; then
			echo "löse fremdes Gadget $(basename "$g")"
			echo "" >"$g/UDC" || true
		fi
	done
	modprobe -r g_ether 2>/dev/null || true

	if [ ! -d "$G" ]; then
		mkdir -p "$G"
		cd "$G"
		echo 0x1d6b >idVendor  # Linux Foundation
		echo 0x0104 >idProduct # Multifunction Composite Gadget
		echo 0x0100 >bcdDevice
		echo 0x0200 >bcdUSB
		echo 0xEF >bDeviceClass # Misc + IAD (f_ncm bringt die IAD mit)
		echo 0x02 >bDeviceSubClass
		echo 0x01 >bDeviceProtocol
		mkdir -p strings/0x409
		serial >strings/0x409/serialnumber
		echo "OpenStick" >strings/0x409/manufacturer
		echo "VPN-Dongle" >strings/0x409/product

		mkdir -p functions/ncm.usb0
		mac 02 >functions/ncm.usb0/host_addr # PC-Seite
		mac 06 >functions/ncm.usb0/dev_addr  # Stick-Seite (muss abweichen)

		mkdir -p configs/c.1/strings/0x409
		echo "NCM" >configs/c.1/strings/0x409/configuration
		echo 250 >configs/c.1/MaxPower
		ln -s functions/ncm.usb0 configs/c.1/
	fi

	# Der Controller (ci_hdrc) kann beim frühen Start noch fehlen -> bis 30 s warten
	udc=""
	i=0
	while [ "$i" -lt 30 ]; do
		udc=$(ls /sys/class/udc 2>/dev/null | head -n1)
		[ -n "$udc" ] && break
		sleep 1
		i=$((i + 1))
	done
	[ -n "$udc" ] || { echo "kein USB-Device-Controller gefunden" >&2; exit 1; }
	[ -n "$(cat "$G/UDC")" ] || echo "$udc" >"$G/UDC"

	ifn=$(cat "$G/functions/ncm.usb0/ifname")
	if [ "$ifn" != "$IFNAME" ]; then
		ip link set "$ifn" down
		ip link set "$ifn" name "$IFNAME"
	fi
	ip link set "$IFNAME" up
	ip addr replace "$ADDR" dev "$IFNAME"
	echo 1 >"/proc/sys/net/ipv6/conf/$IFNAME/disable_ipv6" 2>/dev/null || true # kein IPv6 zum PC
	echo "USB-NCM aktiv: $IFNAME $ADDR (UDC $udc)"
}

stop() {
	[ -d "$G" ] || return 0
	echo "" >"$G/UDC" 2>/dev/null || true
}

case "${1:-start}" in
start) start ;;
stop) stop ;;
status)
	[ -d "$G" ] && echo "UDC: $(cat "$G/UDC")"
	ip -br addr show "$IFNAME" 2>/dev/null || echo "$IFNAME fehlt"
	;;
*)
	echo "Aufruf: $0 start|stop|status" >&2
	exit 2
	;;
esac
