# WireGuard-Test am UZ801 (analog zu den ESP32-Tests)

**Datum:** 2026-11-16
**Gerät:** UZ801 (FY_UZ801_V3.31), Debian 13.6, Kernel 6.6.0-msm8916, vpndongled 9e43def+
**Aufbau:** PC → USB-NCM (`192.168.226.1/29`) → UZ801 → WLAN Gast-WLAN (2,4 GHz, Signal 65 %) → WireGuard → FritzBox-Remote-LAN `10.10.0.0/24`
**Peer:** dieselbe `wg_config.conf` wie beim ESP32-Dongle (Tunnel-IP `10.10.0.205/24`, PSK, Keepalive 25).
Der Schlüssel war während der Tests auf keinem anderen Gerät aktiv (ESP32 nicht gesteckt, kein WireGuard-Client am PC) –
gleicher Schlüssel auf zwei Geräten gleichzeitig verhindert den Datenverkehr (siehe ESP-Erfahrung).

Übernommen wurde der IPv4-Teil der Konfig über `POST /settings/wg` (DNS im Volltunnel `10.10.0.190`).

## Ergebnisse

| # | Test (wie ESP32) | Ergebnis |
|---|---|---|
| 1 | Handshake | ✅ Peer oben, Endpoint aufgelöst, Keepalive hält |
| 2 | Ping vom Stick durch den Tunnel: FritzBox `.253`, LAN `.190`, `.252` | ✅ je 4/4, 0 % Verlust |
| 3 | PC → Dongle → Tunnel (Split): `.253`, `.190`, `.252`, `.68` | ✅ 0 % Verlust, TTL 63/62 (ein Hop mehr durch den Stick); SMB-Port 445 auf `.68` offen |
| 4 | Volltunnel: öffentliche IP über den Dongle | ✅ = FritzBox-WAN-IP; Ping `1.1.1.1` 0 %, DNS über `.190` im Tunnel |
| 4b | Option 121 | ✅ Split: `10.10.0.0/24` + `0.0.0.0/0`; Volltunnel: `0.0.0.0/1` + `128.0.0.0/1` – nach `ipconfig /renew` (ohne Admin) ging der ganze PC durch den Tunnel |
| 5 | Kill-Switch + VPN-Schalter AUS | ✅ keine Verbindung, 100 % Verlust – **kein Leck ins WLAN**; nach EIN sofort wieder Tunnel |
| 6 | SMB `\\10.10.0.68\temp`, Zufallsdaten, SHA-256 | ✅ Integrität bit-genau (Werte unten) |
| 7 | Durchsatz 1 / 4 Streams im Volltunnel | ✅ Werte unten |

### Durchsatz

| Messung | UZ801 | ESP32-S2 (optimiert) | ESP32-S3 |
|---|---|---|---|
| SMB 10 MB schreiben / lesen | **7,4 / 4,3 Mbit/s** | 2,65 / 2,89 | – |
| SMB 100 MB schreiben / lesen | **6,8 / 3,8 Mbit/s** | – | – |
| Volltunnel 1 Stream | **3,4 Mbit/s** | – | 1,5–1,7 |
| Volltunnel 4 Streams | **4,1 Mbit/s** | – | 2,8 |
| CPU des Sticks unter Last (Ø 60 s, 4 Kerne) | **6–13 %** | 100 % (1 Kern) | Krypto-Grenze |

## Wo die Grenze liegt: WLAN-Empfang des Sticks, nicht VPN

| Gegenprobe | Ergebnis |
|---|---|
| Stick lädt **selbst** (wget, ohne Weiterleitung/USB/VPN) | 4,2–4,4 Mbit/s |
| PC → Dongle → WLAN **ohne** VPN | 4,0–4,4 Mbit/s |
| gleiches WLAN, direkt am Laptop | 115 Mbit/s |
| USB-NCM Stick → PC (SSH, 50 MB) | ≥ 74 Mbit/s |
| WLAN-Energiesparen aus (NM `powersave=2`) | keine Änderung (wieder auf Standard gestellt) |

Damit ist der **WLAN-Empfang des Sticks** der Engpass (WCN3620: nur 2,4 GHz, ein Datenstrom; Treiber
`wcn36xx`), nicht CPU, WireGuard oder USB. Das VPN läuft praktisch mit WLAN-Geschwindigkeit; schreibend
(Stick sendet ins WLAN) sogar schneller. Die eigentliche VPN-Leistung des Sticks lässt sich erst über einen
schnelleren Uplink messen (LTE, sobald eine SIM da ist).

## NetBird (2026-11-16)

Selbst gehostetes NetBird-Management (Dashboard/API/Management/Signal/Relay auf Port 443), Client 0.83.1,
Anmeldung per Setup-Key über die Weboberfläche.

| Test | Ergebnis |
|---|---|
| Anmeldung | ✅ `Connected`, Kernel-WireGuard auf `wt0`, Name `vpndongle-<SN>.netbird.selfhosted`; Setup-Key danach aus der Konfig gelöscht, Key-Datei weg |
| Firewall | ✅ NetBird legt eigene iptables-Tabellen an (`ip filter/nat/mangle`): FORWARD lässt PC→`wt0` durch, verwirft nur neue Verbindungen *aus* `wt0`; INPUT aus `wt0` nur für Peers laut NetBird-Richtlinie – zusätzlich zur Zugriffs-Tabelle |
| DNS des Sticks | ✅ NetBird biegt `/etc/resolv.conf` auf sich um; Auflösung funktioniert (Internet und `*.netbird.selfhosted`) |
| PC → Peer | ✅ Option 121 liefert das Overlay-Netz; Ping 0 % (TTL 63), HTTP zu einem Peer-Dienst, Verbindung über Relay (Gast-WLAN) |
| DNS am PC | ✅ `<peer>.netbird.selfhosted` über den Stick, normale Namen weiter über den Uplink |
| Kill-Switch | ✅ ohne Exit-Node: Internet für den PC gesperrt, NetBird-Peers erreichbar; aus → Internet sofort wieder da |
| Umschalten | ✅ NetBird → WireGuard (Dienst aus, `wt0` weg, WireGuard-Ziel erreichbar) → NetBird ohne neuen Setup-Key (schon angemeldet) |
| Kosten | NetBird-Dienst 62 MB RAM verbunden; Statusabfrage ~0,1 s alle 5 s, Stick gesamt ~3,5 % CPU; RAM 113 von 317 MiB |

Nicht getestet: Exit-Node (Volltunnel), Netze hinter einem Routing-Peer, SSH/Web auf den Stick aus dem NetBird-Netz.

## Tailscale (2026-11-16)

Tailscale-Cloud, Client 1.104.7, Anmeldung per Auth-Key über die Weboberfläche, VPN-Art „Tailscale“.

| Test | Ergebnis |
|---|---|
| Anmeldung | ✅ `Running`/online, Name `vpndongle-<SN>.<tailnet>.ts.net`; Auth-Key danach aus der Konfig gelöscht, Key-Datei weg |
| Umschalten | ✅ nur `tailscaled` läuft, NetBird und WireGuard aus |
| Firewall | ✅ `--netfilter-mode=off`: Tailscale legt keine Tabellen an, unsere nftables bleiben maßgeblich |
| DNS des Sticks | ✅ `--accept-dns=false`: `/etc/resolv.conf` unverändert |
| Option 121 | ✅ PC bekommt `100.64.0.0/10` + Standardroute über den Stick |
| MagicDNS am PC | ✅ `<gerät>.<tailnet>.ts.net` über den Stick → Tailscale-IP; normale Namen weiter über den Uplink |
| Kill-Switch | ✅ ohne Exit-Node: Internet für den PC gesperrt; aus → sofort wieder da |
| Stabilität | ✅ 60 s Beobachtung: Status konstant, keine Neustarts von dnsmasq |

Nicht getestet (das Tailnet hatte außer dem Stick kein Gerät): Zugriff auf andere Geräte, Subnetz-Routen,
Exit-Node.

**Nebenbefund (unabhängig von der VPN-Art):** Beim Roaming zwischen zwei Access Points des Gast-WLANs
startet NetworkManager DHCP neu; der DHCP-Server brauchte ~15–30 s – so lange kein Internet. Mögliche
Verbesserung: Adresse beim Roaming behalten.

## Interner Speicher (eMMC) und USB-Strecke

SMB gibt es auf dem Stick nicht (kein Samba im Image, unser Paket installiert keins). Dateien kommen über die
Weboberfläche auf den Stick (`/settings/upload`, Download über `/dl`) nach `/var/lib/vpndongle/files`
(ext4 auf `mmcblk0p14`, eMMC Hynix H4G2a; 64 MB je Datei, 512 MB gesamt).

**Direkt am Stick** (`dd`, 200 MB, Cache vor dem Lesen geleert):

| Messung | Ergebnis |
|---|---|
| Schreiben 1-MB-Blöcke, direkt (ohne Cache) | 10,1 MB/s |
| Schreiben gepuffert + `fdatasync` | 9,9 MB/s |
| Lesen direkt / gepuffert | 98,8 / 120 MB/s |
| 4-KB-Blöcke synchron schreiben / direkt lesen | 0,53 MB/s (~130 Schreibvorgänge/s) / 15,7 MB/s |

**Vom PC über USB-NCM** (60 MB Zufallsdaten, SHA-256 jeweils identisch):

| Weg | hoch | runter (Cache am Stick geleert) |
|---|---|---|
| Weboberfläche (vpndongled) | 14,7 MB/s (118 Mbit/s) | 25,9 MB/s (207 Mbit/s) |
| `scp` (SSH-Verschlüsselung am Stick) | 10,2 MB/s | 17,9 MB/s |

CPU des Sticks dabei ~11 % (Ø 60 s). Die USB-Strecke ist also schnell; beim Schreiben begrenzt der Flash (~10 MB/s).

Der erste Web-Upload (14,7 MB/s) lag über der Flash-Schreibrate, weil `saveUpload` **kein `fsync`** machte: die
letzten Sekunden lagen nach der Meldung „hochgeladen“ noch im RAM – Stick sofort abziehen = Datei unvollständig.
**Behoben:** Datei-Upload und Update-Paket werden vor dem Umbenennen gesynct (`internal/web/fsync.go`), danach der
Ordner. Nachmessung: **Web-Upload 7,8 MB/s** – die Meldung kommt jetzt erst, wenn die Daten auf dem Flash sind.

## Zustand nach dem Test

Split-Modus (Volltunnel und Kill-Switch aus), VPN an. PC-Routen wieder normal (`10.10.0.0/24` + Default über den Dongle).
