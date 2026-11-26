# Änderungen

## v0.3.1 (unveröffentlicht)

Aus dem ersten Test mit SIM (UZ801, PremiumSIM im Vodafone-Netz):
- **LTE-Daten scheiterten endlos** mit „multiple-connection-to-same-pdn-not-allowed“: Die Werks-Firmware
  bucht mit Profil „qdp_profile“ und dem APN „ctlte“ (China Telecom) ein, das Netz baut dabei schon die
  Standard-Verbindung auf, und die zweite zum selben APN lehnt es ab. Der Stick gleicht jetzt das
  Einbuchungs-Profil selbst ab: Hat genau ein Profil einen fremden APN und keines den eingestellten,
  bekommt es den eingestellten APN (per `qmicli`, ModemManager kann das bei diesem Modem nicht), danach
  schaltet der Stick den Funk aus und wieder an (bucht neu ein) und verbindet bei Uplink LTE sofort; am
  UZ801 ~20 s vom Erkennen bis zur Verbindung. Neues Paket `libqmi-utils`
- **SIM-PIN auch ohne LTE-Uplink:** NetworkManager gab die PIN nur beim Aufbau der LTE-Verbindung ein, im
  reinen WLAN-Betrieb blieb die SIM nach einem Neustart gesperrt (keine SMS). Der Stick entsperrt jetzt
  selbst mit der eingestellten PIN: höchstens ein Versuch je PIN, nie mit dem letzten verbleibenden Versuch
- **„sim-pin2 nötig“ war ein Fehlalarm:** PIN2 schützt nur die Rufnummernbeschränkung (FDN), Einbuchen,
  Daten und SMS-Empfang gehen ohne. Der Status zeigt das als grauen Hinweis
- **Kein Verbindungs-Dauerfeuer mehr:** Das LTE-Profil versuchte es endlos ohne Pause (am UZ801 ~6 Versuche
  pro Sekunde, CPU-Last 3, Journal voll); jetzt 3 Versuche, dann 5 Minuten Pause
- **LTE-Datenverbrauch im Status:** aktuelle Sitzung (seit LTE verbunden) und laufender Kalendermonat mit
  ↓/↑, dazu die letzten 6 Vormonate. Gezählt vom Modem (ModemManager, Bytes der Verbindung; am UZ801 zählt
  der Treiber an `wwan0` nichts), sonst an der LTE-Schnittstelle; gespeichert in
  `/var/lib/vpndongle/lte-usage.json` (24 Monate, höchstens alle 5 min geschrieben, beim Sitzungsende und
  Beenden sofort); Zurücksetzen „alles“ löscht die Zahlen. Auch in `status.json` (`lte_session_*`,
  `lte_month`, `lte_months`). Der Zähler misst am Stick – die Abrechnung des Anbieters kann abweichen
- **Externe IP (IPv4 und IPv6) im Status:** alle 10 min und bei jedem Wechsel von Uplink/VPN/WLAN über
  `ipv4.icanhazip.com` bzw. `ipv6.icanhazip.com`; bei LTE hinter CGNAT gibt es die öffentliche IPv4 nur so.
  Auch in `status.json` (`ext_ipv4`, `ext_ipv6`)
- Eigene Rufnummer von der SIM mit „+“: die SIM meldet sie international, aber ohne Plus („4915…“)

## v0.3.0

- **SMS über die Weboberfläche:** neue Seite „SMS“ (Kopfzeile, ohne Login wie die Übersicht) zum Lesen und
  Senden, eingeschaltet über den Haken **„SMS aktiv“** unter Einstellungen → LTE. Empfangene SMS holt der
  Stick alle 15 s vom Modem in eine eigene Ablage auf dem Flash (letzte 200) und löscht sie im Modem, damit
  dessen Speicher nie voll läuft; mehrteilige SMS erst, wenn alle Teile da sind. Senden mit Zeichen- und
  Teile-Zähler, höchstens 640 Zeichen und 20 SMS pro Stunde; Antworten-Knopf, Löschen einzeln/alle. Neue
  SMS: Zähler in der Kopfzeile und im Status, die offene Seite lädt sich neu. SMS-Inhalt und -Nummer stehen
  nie im Journal. Zurücksetzen von LTE bzw. „alles“ löscht die Ablage mit
- **Rufnummer im Status:** von der SIM, sonst die unter Einstellungen → LTE eingetragene, sonst „unbekannt“
- Ist SMS aktiv und das Modem ausgeschaltet (z. B. reiner WLAN-Betrieb), schaltet der Stick es ein, damit
  SMS ankommen

## v0.2.2

- Zabbix-Proxy und -Agent auf **7.4** (vorher 7.0): Ein 7.4-Server meldete den 7.0-Proxy als veraltet
  und gab ihm keine Konfiguration mehr. Vorhandene Sticks stellen beim Update die Paketquelle um,
  der Proxy legt seine Puffer-Datenbank neu an. Die Reihe steht in `system/install.sh` (`ZBX_SERIES`)
- `install.sh` behält beim Paket-Upgrade die von vpndongled erzeugten Zabbix-Konfigs (keine Rückfrage)
- Gebaut mit Go 1.27.4 (Sicherheitskorrekturen der Standardbibliothek)

## v0.2.1

- Routenwechsel kommen sofort beim PC an: Ändern sich die per DHCP verteilten Routen (VPN-Art,
  Volltunnel, NetBird/Tailscale-Routen), meldet der Stick sein USB-Netz kurz ab und wieder an – der PC
  hat nach ~4 s genau die neuen Routen (vorher bis zu 5 min die alten)
- Status: VPN-Zeile nennt die Technik (WireGuard / NetBird / Tailscale), `status.json` hat `vpn_kind`
- Zurücksetzen auch für NetBird, Tailscale und alle VPN-Zugänge – inklusive Abmeldung des Clients und
  Löschen seiner lokalen Anmeldung

## v0.2.0

Zero-Trust-Netze: der Stick als Hardware-Client für NetBird und Tailscale/Headscale.


- **NetBird** als zweite VPN-Art neben WireGuard: Anmeldung per Setup-Key (danach gelöscht, nie auf
  der Kommandozeile), Routen/DNS/Exit-Node aus dem Management per Option 121 an den PC,
  NetBird-Namen über den lokalen NetBird-Resolver, Kill-Switch/Isolation/Zugriffs-Tabelle auf `wt0`,
  Client läuft nur, wenn NetBird gewählt ist
- **Tailscale** (Tailscale-Cloud oder Headscale) als dritte VPN-Art: Auth-Key (danach gelöscht),
  Subnetz-Routen und MagicDNS an den PC, Exit-Node als Volltunnel, `--netfilter-mode=off`
- **VPN-Art** als Auswahl (WireGuard | NetBird | Tailscale); VPN-Schalter gilt für alle
- Status: **Temperatur** (CPU, Modem, Drosselgrenze; alle Fühler in `status.json`)
- Fix: CPU/RAM-Auslastung fehlte bei NetBird/Tailscale in der Status-Karte

## v0.1.0

Erste öffentliche Version, getestet auf einem UZ801 (FY_UZ801_V3.31) mit Debian 13 (openstick-installer v0.0.5).

**Dongle**
- USB-Netzwerkadapter (NCM, ohne Treiber), DHCP/DNS mit Option 121
- WLAN (bis 10 Netze mit Priorität) und LTE mit automatischem Rückfall
- WireGuard Split/Volltunnel, Kill-Switch, VPN-Schalter, LAN-Isolation, Captive-Portal
- Weboberfläche: Status inkl. CPU/RAM (Mittel 60 s), WLAN, Ressourcen/RDP, Datei-Downloads
- Zugriff auf den Stick als Haken-Tabelle (Weboberfläche/SSH/Ping × USB/VPN/WLAN/LTE)
- verschlüsselter Konfig-Export/-Import (kompatibel zum ESP32-Dongle), Config-Server
- Zabbix 7.0: aktiver Proxy + Agent durch den Tunnel, Vorlage
- Updates: Debian/Zabbix (Kernel/Firmware gehalten), Selbst-Update aus GitHub-Releases mit
  Ed25519-Signatur, kein Downgrade; Uploads werden vor der Erfolgsmeldung auf den Flash geschrieben
- Seriennummer aus der machine-id (stabil, gleich der USB-Seriennummer), Zeitzone Europe/Berlin

**Installation**
- `flash.ps1`: Pflicht-Backup per EDL, Flash QFIL/fastboot, Modem-Partitionen aus dem Backup zurück
- `raw2qfil.ps1`: 1:1-eMMC-Abbilder (openstick-installer) sicher flashbar machen
- `harden-base.sh`: Image härten (machine-id, SSH-Host-Schlüssel, Konten, Hotspot, Image-Gadget)
- `gadget-probe.sh`: eigenes USB-Gadget live testen, mit automatischem Rückfall
