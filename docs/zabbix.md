# Zabbix-Überwachung (Server 7.4)

Proxy und Agent auf dem Stick kommen aus der Zabbix-Reihe **7.4** (`ZBX_SERIES` in `system/install.sh`),
apt hält sie innerhalb der Reihe aktuell. Der Proxy muss dieselbe Hauptversion haben wie der Server:
Ein älterer Proxy gilt dort als „veraltet“ und bekommt keine Konfiguration mehr, ein neuerer wird
nicht angenommen. Wechselt der Server die Reihe (z. B. auf 8.0), `ZBX_SERIES` ändern und ein neues
Release bauen – `install.sh` stellt dann die Paketquelle um und legt die Proxy-Datenbank (nur Puffer) neu an.

Auf dem Stick laufen ein **aktiver Zabbix-Proxy** und ein **Agent**. Der Proxy verbindet sich selbst
durch den WireGuard-Tunnel zum Server, von außen ist kein offener Port nötig. Fällt der Tunnel aus,
puffert er die Daten (bis 16 MB im RAM, danach in SQLite, maximal 24 h) und liefert sie nach.

```
 Agent (Stick) ──127.0.0.1:10051──► Proxy (Stick) ══WireGuard══► Zabbix-Server 7.4
 Ping WLAN-Gateway / PC / Ziele ◄── Simple Checks (fping) auf dem Proxy
 Agent auf dem PC (optional) ──192.168.226.1:10051──┘
```

## Was überwacht wird

Ein- und ausschalten lässt sich das in der Weboberfläche des Sticks unter **Einstellungen → Zabbix-Überwachung**.
Der Agent meldet die eingeschalteten Bereiche per Low-Level-Discovery. Der Server legt die Items
danach an und deaktiviert bzw. entfernt sie, wenn ein Bereich ausgeschaltet wird (nach 1 Tag gelöscht).

| Bereich | Items | Trigger (Schwellen als Makros) |
|---|---|---|
| Stick (immer) | Uplink (keiner/WLAN/LTE), Captive gesperrt, Version, Laufzeit, CPU-Last, RAM %, Flash % | keine Daten, kein Uplink, LTE-Rückfall aktiv, neu gestartet, wenig RAM/Flash |
| verbundenes WLAN | verbunden, Signal %, SSID; Gateway: erreichbar, Verlust, Antwortzeit | WLAN getrennt, schwaches Signal, Gateway nicht erreichbar, Verlust/Antwortzeit |
| LTE | verbunden, Signal, Zustand, Netz, Technik, SIM-Sperre | schwaches Signal, SIM verlangt PIN/PUK |
| VPN | soll laufen, verbunden, Alter Handshake, Durchsatz rx/tx | Tunnel getrennt |
| PC am USB | Ping: erreichbar, Verlust, Antwortzeit | PC antwortet nicht (Info) |
| weitere Ping-Ziele | je Ziel: erreichbar, Verlust, Antwortzeit | Ziel nicht erreichbar |

Hinweis zum PC-Ping: Windows beantwortet Ping nur, wenn die Firewall eingehendes ICMP-Echo erlaubt.

## Einrichtung auf dem Server

1. **Template importieren:** *Datenerfassung → Vorlagen → Importieren* →
   `zabbix/template_openstick_vpn_dongle.json` (Vorlage „OpenStick VPN-Dongle“, Gruppe *Templates/Network devices*).
2. **Proxy anlegen:** *Administration → Proxys → Proxy anlegen*
   - Name = der **Proxy-Name** aus der Weboberfläche des Sticks (Standard `vpndongle-<SN>-proxy`)
   - Modus **Aktiv**
   - Mit PSK: *Verschlüsselung → Verbindungen vom Proxy: PSK*, Identität und Schlüssel wie am Stick.
     Einen Schlüssel erzeugt `openssl rand -hex 32`.
3. **Autoregistrierung:** *Alarme → Aktionen → Autoregistrierungsaktionen → Aktion anlegen*
   - Bedingung: *Host-Metadaten* **enthält** `openstick-vpn-dongle`
   - Operationen: *Host hinzufügen*, *Zu Hostgruppen hinzufügen* (z. B. „VPN-Dongles“),
     *Mit Vorlagen verknüpfen* → „OpenStick VPN-Dongle“

   Der Host wird automatisch von dem Proxy überwacht, über den er sich registriert hat.

## Einrichtung am Stick

*Einstellungen → Zabbix-Überwachung*:
- Haken „aktiv“ setzen
- **Zabbix-Server** = Adresse des Servers *im Tunnel*, optional mit `:Port`
- Proxy- und Host-Name leer lassen (Standard aus der Seriennummer) oder selbst setzen
- Bereiche anhaken, weitere Ping-Ziele eintragen

Im **Split-Tunnel** muss die Server-Adresse in den Tunnel-Netzen liegen, sonst warnt die Seite. Dann unter
*WireGuard → Zusätzliche Netze* eintragen. Im Volltunnel passt es immer.

## Agent auf dem PC (optional)

Haken „Zabbix-Agent auf dem PC darf an den Proxy melden“ setzen. Dann öffnet die Firewall des Sticks Port 10051
nur Richtung USB. In der Agent-Konfig auf dem PC:

```
ServerActive=192.168.226.1
Hostname=<PC-Name>
HostMetadata=vpndongle-pc
```

Für die PCs eine zweite Autoregistrierungsaktion anlegen (Bedingung *Host-Metadaten enthält* `vpndongle-pc`,
passende Windows-Vorlage).

## Ressourcen auf dem Stick

| | |
|---|---|
| RAM | Proxy schlank eingestellt (Caches 8/4/2 MB, Speicherpuffer 16 MB, wenige Prozesse), geschätzt **40–70 MB** zusammen mit dem Agent. Am Stick messen (Testplan). |
| Flash | Pakete ~20 MB. SQLite wächst nur bei längerem Serverausfall (`ProxyOfflineBuffer=24` h). |
| LTE-Datenvolumen | grob einige bis einige zehn MB pro Tag, je nach Anzahl der Ziele |

Die Konfigdateien `/etc/zabbix/zabbix_proxy.conf` und `/etc/zabbix/zabbix_agentd.conf` schreibt vpndongled.
Änderungen von Hand werden überschrieben. Die Parameter wurden gegen die offiziellen Beispielkonfigs von 7.0 und 7.4.16 geprüft
(Name und Wertebereich). Die Vorlage ist im 7.0-Format (Server ab 7.0 importieren sie) und wurde gegen
die Regeln des 7.0-Import-Validators geprüft (`go test ./internal/zbxtpl`). Ein echter Import steht noch aus.

## Template ändern

`internal/zbxtpl/template.go` bearbeiten, dann `go run ./cmd/zbxtemplate`. `go test ./internal/zbxtpl`
prüft unter anderem UUIDv4, Trigger-Verweise, Makros, Preprocessing-Regeln, JSONPaths gegen den Status von
vpndongled und LLD-Makros gegen die Discovery.
