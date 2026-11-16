# Konzept OpenStick-VPN-Dongle

## Warum der Umzug vom ESP32

Beim ESP32-USB-VPN-Dongle war bei **~2,8 Mbit/s** Schluss: ChaCha20 lief in Software
auf einem Kern, ohne Hardware-Beschleunigung (siehe ESP-Repo `docs/dualcore-wg-krypto-konzept.md`).
Der UZ801 (Snapdragon 410, 4× Cortex-A53 mit NEON, 1,2 GHz) bietet stattdessen:

- WireGuard im Linux-Kernel mit NEON-ChaCha → erwartet zweistellige Mbit/s,
  begrenzt dann eher durch WLAN (nur 2,4 GHz) oder LTE
- ein LTE-Modem als zweiten Uplink
- fertige Bausteine (NetworkManager, ModemManager, dnsmasq, nftables) statt
  selbst geschriebener lwIP-Hooks

## Zuordnung ESP → Linux

| ESP-Baustein | OpenStick | Anmerkung |
|---|---|---|
| TinyUSB-NCM + eigener esp_netif | configfs-Gadget `ncm` (`system/usb-gadget.sh`) | nur NCM, keine Composite-Konsole (macOS-Lehre) |
| WLAN-Liste in NVS, eigener Auto-Switch | NM-Keyfiles `vpndongle-wlan-N`, `autoconnect-priority` | Scan-während-Connect-Falle des ESP entfällt |
| DHCP-Server + `LWIP_HOOK_DHCPS_POST_APPEND_OPTS` | dnsmasq `classless-static-route` | gleiche Routen: 0/1 + 128/1 |
| eigener DNS-Hijack-Server | dnsmasq `address=/#/…` | |
| lwIP-NAPT, `update_internet_gate()` | nftables-Tabelle `inet vpndongle` | Captive/Kill-Switch als Regeln |
| esp_wireguard + `MAX_SRC_IPS`-Fix | wg-quick (Kernel) | Split-Tunnel jetzt wirklich (AllowedIPs) |
| NVS | `/etc/vpndongle/config.json` (0600) | Klartext wie beim ESP, aber nur root lesbar |
| OTA-Upload | Paket per Weboberfläche, `deploy.ps1` oder signiertes GitHub-Release | Selbst-Update mit Ed25519-Signatur |
| SPIFFS-Dateien | `/var/lib/vpndongle/files` | 64 MB je Datei, 512 MB gesamt; Upload direkt auf den Flash gestreamt (kein RAM/tmpfs) |
| KY-016 / WS2812 | `/sys/class/leds` | Namen am Gerät prüfen |

Beim ESP offen, hier umgesetzt: echter **Split-Tunnel**, **LAN-Isolation**, **Zugriff auf den Stick
je Netz** (Haken-Tabelle; ab Werk Web-UI nur über usb0), CSRF-Bremse, MSS-Clamping.

## LTE

- **ModemManager** verwaltet das Modem. Auf dem MSM8916 braucht das Modem `rmtfs`
  (liest die Partitionen modemst1/2 und fsg) und `qrtr-ns`. Ohne diese Partitionen bleibt
  das Modem stumm. Deshalb gilt das Backup vor dem Flashen (siehe Inbetriebnahme) als Pflicht.
- **NM-Profil** `vpndongle-lte` (Typ gsm): APN, optional Benutzer/Passwort/PIN, `home-only` ohne Roaming.
- **Uplink-Modi** (`uplink.mode`):
  - `auto`: WLAN bevorzugt. Fehlt WLAN 20 s lang, schaltet der Dienst LTE zu (neuer Versuch alle 60 s).
    Läuft WLAN wieder 60 s stabil, schaltet er LTE ab. LTE bleibt also nicht dauerhaft parallel online und spart Datenvolumen.
  - `wlan`: nur WLAN.  `lte`: nur LTE (NM-Autoconnect).
- **Routing:** Metrik WLAN 100, LTE 700. WireGuard folgt dem Uplinkwechsel von selbst (Roaming),
  wg-quick braucht dafür keinen Neustart.
- **MTU:** Standard 1380 (wg.mtu) plus MSS-Clamping, damit WireGuard auch über LTE-Pfade mit
  kleinerer MTU durchkommt.
- **Datenschutz:** Die IMEI wird geparst, aber nie in API/Log/Export ausgegeben.
  Seriennummer = wlan0-MAC (wie die Modul-MAC beim ESP).

## Konfig-Format

Das key=value-Format bleibt gleich wie beim ESP, plus neue Schlüssel:

```
uplink.mode=auto|wlan|lte    net.isolate=0|1
wg.dns=1.1.1.1               wg.allowed=10.20.0.0/16,…   wg.mtu=1380
lte.enabled=0|1  lte.apn=…  lte.user=…  lte.pass=…  lte.pin=…  lte.roaming=0|1
```

Die ESP-Firmware ignoriert diese Schlüssel. Eine ESP-Konfig lässt sich in den OpenStick einspielen
und umgekehrt. Import mit Merge-Semantik: Fehlende Bereiche bleiben erhalten.

## Firewall (nftables, Tabelle `inet vpndongle`)

- `forward` Policy drop: nur usb0 → Uplink/Tunnel, Rückweg über conntrack, IPv6 vom PC verworfen
- gesperrt (Captive): nichts weiterleiten
- Kill-Switch: vom PC nur über wg0 (gilt wie beim ESP auch bei „VPN aus“)
- Isolation: keine privaten Ziele (RFC1918, CGNAT, Link-Local) über WLAN/LTE
- `input`: Web (80/443), DNS und DHCP nur über usb0. SSH (22) nur über usb0 und wg0, außer im
  **Notfallmodus**: Fehlt usb0 (Gadget nicht hochgekommen), ist SSH auch über WLAN/LTE offen, damit man sich
  nicht aussperrt. Die Weboberfläche warnt dann.
- SSH: openssh-server mit Drop-in `sshd-90-vpndongle.conf` (Root-Login per Passwort, MaxAuthTries 4). Das Root-Passwort
  wird in der Weboberfläche gesetzt (`chpasswd` über stdin). Gespeichert wird nur der Zeitpunkt, nicht exportiert.
- NAT: Masquerade für 192.168.226.0/29 auf jedem Ausgang außer usb0

## Speicher

- **RAM:** 512 MB, davon ~320 MiB nutzbar (Modem/WLAN-Firmware reservieren den Rest).
  vpndongled braucht ~11–13 MB (gemessen, Linux-Build). Ein 60-MB-Upload erhöht den
  Spitzenwert nur um ~0,6 MB.
- **Flash (eMMC 4 GB):** Unser Anteil ist ~10 MB Binary + wenige MB Pakete. Für variable Daten gelten Grenzen:
  Dateien 512 MB, Journal 50 MB (persistent, `journald-90-vpndongle.conf`), der apt-Cache wird nach der
  Installation geleert.

## Zabbix

Aktiver Proxy + Agent (Zabbix 7.0 aus repo.zabbix.com, Debian 12/13 arm64), Details in [zabbix.md](zabbix.md).
- Der Agent fragt `vpndongled -zbx …` über Loopback ab. Er läuft als Benutzer `zabbix` und darf die Konfig
  nicht lesen, deshalb liefert der Dienst Status und Discovery nur an 127.0.0.1.
- Was überwacht wird, steuert die LLD. Die Schalter in der Weboberfläche ändern nur die Discovery-Antwort,
  die Dienste starten dafür nicht neu.
- Firewall: 10050 nie offen, 10051 nur von usb0 und nur, wenn „Agent auf dem PC“ an ist.

## Updates

`update.sh` als systemd-Dienst `vpndongle-update` (Knopf bzw. Timer So. 03:30 ± 1 h):
- `apt upgrade` ohne Paketentfernung, Konfigdateien bleiben erhalten (confold)
- Kernel-, Firmware- und Bootloader-Pakete per `apt-mark hold` gehalten
- nicht über LTE ohne Freigabe, Abbruch bei < 300 MB frei, nur ein Lauf gleichzeitig
- ein hochgeladenes `vpndongle-*.tar.gz` wird im selben Lauf über dessen `install.sh` eingespielt
- Neustart nur, wenn verlangt und freigegeben

## Bewusst offen / später

- Web-Update der Binary (vorerst `deploy.ps1`)
- Watchdog (systemd `WatchdogSec` + sd_notify)
- HTTPS für die Web-UI (Zertifikat auf einem USB-Gerät wenig sinnvoll)
- Admin-Passwort als SHA-256 ohne Salt (ESP-kompatibel) – auf bcrypt umstellen, sobald
  ESP-Kompatibilität des Exports nicht mehr nötig ist
- SMS-Empfang/-Versand über mmcli (z. B. für home-central als Notfallweg)
