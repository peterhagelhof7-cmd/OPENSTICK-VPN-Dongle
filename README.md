# OpenStick-VPN-Dongle

**Plug-and-Play-VPN-Gateway auf einem 10-€-LTE-Stick.** Ein UZ801-USB-Stick (Qualcomm MSM8916) mit
Debian wird zum USB-Netzwerkadapter: PC einstecken, der Stick bringt ihn über WLAN oder LTE ins Internet
und auf Wunsch per **WireGuard**, **NetBird** oder **Tailscale** ins Firmen- oder Heimnetz – ohne Treiber,
ohne Software auf dem PC.

> *English:* Turns a cheap UZ801 4G USB stick (MSM8916, OpenStick Debian) into a driverless USB network
> adapter that routes the PC through Wi-Fi or LTE and an optional WireGuard, NetBird or Tailscale/Headscale
> connection – a hardware client for zero-trust networks (kill switch, captive portal, web UI, Zabbix proxy,
> signed self-updates). Documentation is in German.

```
 PC ──USB (NCM, ohne Treiber)──► UZ801 ──WLAN / LTE──► Internet
       192.168.226.2               │  └── WireGuard / NetBird / Tailscale ──► Heim-/Firmennetz
                                   └── Weboberfläche http://192.168.226.1/
```

Nachfolger des [ESP32-USB-VPN-Dongle](https://github.com/peterhagelhof7-cmd/ESP32-USB-VPN-Dongle):
gleiche Idee und Bedienung, aber Linux statt ESP-IDF – WireGuard im Kernel statt in Software auf einem
ESP-Kern, dazu **LTE** als zweiter Internet-Zugang.

## Stand

Läuft auf einem echten **UZ801 (Platine FY_UZ801_V3.31)** mit dem Debian-13-Image des
[openstick-installer](https://github.com/Basistiy/openstick-installer). Getestet: Backup/Flash,
USB-Netz unter Windows 11, WLAN, WireGuard (Split und Volltunnel), NetBird (eigener Server: Anmeldung,
Peers, DNS, Kill-Switch), Tailscale (Cloud: Anmeldung, MagicDNS, Kill-Switch), Option 121, signiertes
Selbst-Update. Noch offen: LTE (keine SIM im Test), Tailscale-Subnetz-Routen/Exit-Node, macOS/iPadOS am
UZ801, LED-Anzeige.

Messwerte (Details in [docs/vpn-test-uz801.md](docs/vpn-test-uz801.md)):

| | UZ801 | ESP32-Vorgänger |
|---|---|---|
| SMB durch den Tunnel, schreiben / lesen | 7,4 / 4,3 Mbit/s | 2,7 / 2,9 Mbit/s |
| CPU-Last dabei | 6–13 % | 100 % |
| USB-Strecke Stick ↔ PC | > 200 Mbit/s | ~ 10 Mbit/s |

Der Engpass ist das 2,4-GHz-WLAN des Sticks (WCN3620, ~4–7 Mbit/s), nicht CPU oder VPN.

## Funktionen

| | |
|---|---|
| USB-Netzwerkadapter | NCM-Gadget, ohne Treiber unter Windows 10 2004+/11, macOS, iPadOS, Linux |
| Uplink | bis 10 WLANs mit Priorität, **LTE als Rückfall** (oder nur WLAN / nur LTE) |
| DHCP/DNS | eigene dnsmasq-Instanz nur auf usb0, Routen per **Option 121** (kein Eingriff am PC) |
| WireGuard | Kernel-WireGuard, Voll- oder Split-Tunnel (zusätzliche Netze möglich), VPN-Schalter |
| NetBird / Tailscale | alternativ zu WireGuard (VPN-Art wählbar): Stick als Peer in einem [NetBird](https://netbird.io)-Netz oder als Gerät in einem [Tailscale](https://tailscale.com)-Tailnet bzw. bei Headscale – Zugriffe per Gruppen/Richtlinien im Management, Routen/DNS/Exit-Node von dort, Anmeldung per Setup-/Auth-Key |
| Sicherheit | Kill-Switch, LAN-Isolation, Captive-Portal ohne Uplink, Basic-Auth, CSRF-Bremse |
| Zugriff auf den Stick | **Haken-Tabelle** Weboberfläche/SSH/Ping × USB/VPN/WLAN/LTE (Firewall) |
| SMS | **lesen und senden über die Weboberfläche** (einschaltbar), eigene **Rufnummer** im Status |
| Status | Uplink, WLAN-Signal, LTE, VPN, **CPU/RAM gemittelt über 60 s**, **Temperatur** (CPU/Modem, Drosselgrenze); alles auch als `status.json` |
| Ressourcen | RDP-Dateien, Links, SSH, Datei-Downloads vom Stick |
| Konfig | Export/Import verschlüsselt (BHVPN1, kompatibel zum ESP und zu `tools/admin.ps1`), Config-Server |
| Zabbix 7.4 | aktiver Proxy + Agent durch den Tunnel ([docs/zabbix.md](docs/zabbix.md), Vorlage in `zabbix/`) |
| Updates | Debian/Zabbix wöchentlich oder per Knopf; **Selbst-Update aus GitHub-Releases mit Ed25519-Signatur** |
| Härtung des Images | neue machine-id und SSH-Host-Schlüssel, Image-Konten/Hotspot/Gadget aus ([system/harden-base.sh](system/harden-base.sh)) |

## Zero Trust (NetBird / Tailscale)

Mit NetBird oder Tailscale wird der Stick zum **Hardware-Client für ein Zero-Trust-Netz**: Er meldet sich
mit eigener Identität (Setup-/Auth-Key) an, welche Ziele erreichbar sind, regeln Gruppen bzw. ACLs zentral
im Management – nicht IP-Bereiche in einer Konfigdatei. Routen, DNS und Exit-Node kommen von dort; der PC
braucht weiterhin keinen Client.

Ehrlich eingeordnet: Die Zero-Trust-Eigenschaften liefert die jeweilige Plattform. Die Richtlinien gelten
für die **Identität des Sticks** – jeder PC am Stick erbt dessen Rechte, Posture-Checks prüfen den Stick,
nicht den PC. Deshalb: Dongles in eine eigene Gruppe mit minimalen Rechten, Kill-Switch und LAN-Isolation an.

## Loslegen

Kurzfassung – ausführlich im **[Admin-Guide](docs/admin-guide.md)**:

1. UZ801 mit **MSM8916-Chip und 4-GB-eMMC** besorgen (Platinen-Revision prüfen).
2. **Komplett-Backup** per EDL (`tools/flash.ps1`) – ohne die Modem-Partitionen verliert der Stick IMEI
   und Kalibrierung, LTE wäre dauerhaft tot.
3. Debian-Image aufspielen (`tools/raw2qfil.ps1` + `tools/flash.ps1`; die Modem-Partitionen werden
   dabei aus dem eigenen Backup zurückgeschrieben).
4. Release-Paket `vpndongle-<version>.tar.gz` auf den Stick, `install.sh` ausführen, neu starten.
5. Am PC: `http://192.168.226.1/` – WLAN verbinden, WireGuard eintragen, Passwort ändern.

## Aufbau

```
cmd/vpndongled      Steuerdienst (Go, eine statische Binary, linux/arm64, nur Standardbibliothek)
cmd/relsign         Release-Signatur (Ed25519) für das Selbst-Update
cmd/zbxtemplate     erzeugt die Zabbix-Vorlage
internal/config     Datenmodell, JSON-Speicher, key=value-Format, BHVPN1-Krypto, Zugriffs-Tabelle
internal/render     erzeugt dnsmasq-, nftables-, wg-quick- und NM-Dateien (reine Funktionen)
internal/app        Apply (nur Geändertes neu schreiben/starten) + Überwachung (Captive, LTE-Rückfall, LEDs)
internal/swupdate   Selbst-Update aus GitHub-Releases (Signatur, Prüfsumme, kein Downgrade)
internal/system     Befehle/Dateien (echt oder Dry-Run), Parser für nmcli/wg/mmcli
internal/web        Weboberfläche (öffentlich + /settings), Zabbix-Discovery (nur lokal)
system/             USB-Gadget, systemd-Units, Konfig-Schnipsel, install.sh, update.sh, harden-base.sh
tools/              flash.ps1, raw2qfil.ps1, build.ps1, release.ps1, deploy.ps1, admin.ps1,
                    gadget-probe.sh, check-render.sh, test/ (Attrappen + Tests)
docs/               Admin-Guide, Konzept, Inbetriebnahme/Testplan, Messungen, Zabbix
```

## Entwickeln (Windows)

```powershell
go test ./...
go run ./cmd/vpndongled -dry-run -listen 127.0.0.1:8080    # Weboberfläche mit Beispieldaten
.\tools\build.ps1                                          # Tests + arm64-Build + dist\vpndongle-<ver>.tar.gz
.\tools\deploy.ps1 -StickHost 192.168.226.1                # aufspielen + install.sh (SSH-Schlüssel für root)
powershell -File tools\test\test-flash.ps1                 # Backup/Flash gegen edl-/fastboot-Attrappen
```

Weitere Prüfungen (WSL/Linux): `tools/test/test-harden.sh`, `tools/check-render.sh` (erzeugte
nftables-/dnsmasq-/WireGuard-Dateien mit den echten Werkzeugen).

### Release

```powershell
# CHANGELOG.md um "## vX.Y.Z" ergänzen, committen
git tag -a vX.Y.Z -m "vX.Y.Z"
.\tools\release.ps1 -KeyFile <privater-signaturschluessel> -Publish
```

`build.ps1 -Release` baut nur aus einem sauberen, getaggten Stand. `release.ps1` legt
`SHA256SUMS` und die Ed25519-Signatur `SHA256SUMS.sig` bei; die Sticks installieren ein Release nur,
wenn die Signatur zum eingebauten öffentlichen Schlüssel passt und es neuer ist als die laufende Version.

## Fremdsoftware & Lizenzen

Dieses Repository enthält **nur eigenen Code**. Folgendes wird benutzt, aber **nicht** mitgeliefert:

| | Lizenz | Rolle |
|---|---|---|
| [openstick-installer](https://github.com/Basistiy/openstick-installer) | MIT | Quelle des Debian-Images (muss selbst geladen werden) |
| Linux-Kernel, Debian-Pakete im Image | GPL-2.0 u. a. | Betriebssystem des Sticks |
| lk2nd, qhypstub im Image | BSD-artig / GPL-2.0 | Bootloader |
| Qualcomm-Firmware (sbl1, tz, rpm, Modem, WLAN) | proprietär | stammt aus Image bzw. eigenem Backup – **nie weitergeben** |
| [bkerler/edl](https://github.com/bkerler/edl) | GPL-3.0 | EDL-Werkzeug, von `flash.ps1` aufgerufen |
| Zabbix Proxy/Agent | AGPL-3.0 | aus dem offiziellen Zabbix-Repository auf dem Stick installiert |
| NetBird-Client | BSD-3-Clause | aus dem offiziellen NetBird-Repository auf dem Stick installiert |
| Tailscale-Client | BSD-3-Clause | aus dem offiziellen Tailscale-Repository auf dem Stick installiert |
| Go-Standardbibliothek | BSD-3-Clause | steckt in `vpndongled`; Hinweis in `THIRD_PARTY_NOTICES` jedes Pakets |

Backups und Images enthalten gerätespezifische Daten (IMEI, Kalibrierung) und proprietäre Firmware;
`.gitignore` hält `backup*/` und `image-kandidaten/` aus dem Repository.

## Lizenz

[GPL-3.0](LICENSE) – wie der ESP32-Vorgänger.
