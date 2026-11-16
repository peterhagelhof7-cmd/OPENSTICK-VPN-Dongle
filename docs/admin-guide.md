# Admin-Guide

Vom neuen UZ801-Stick bis zum laufenden VPN-Dongle. Entwicklerdetails und der vollständige Testplan
stehen in [inbetriebnahme.md](inbetriebnahme.md).

- [1. Was man braucht](#1-was-man-braucht)
- [2. Backup (Pflicht)](#2-backup-pflicht)
- [3. Debian aufspielen](#3-debian-aufspielen)
- [4. Erster Start](#4-erster-start)
- [5. VPN-Dongle installieren](#5-vpn-dongle-installieren)
- [6. Bedienung](#6-bedienung)
- [7. Updates](#7-updates)
- [8. Konfiguration sichern und verteilen](#8-konfiguration-sichern-und-verteilen)
- [9. Fehlersuche und Rettung](#9-fehlersuche-und-rettung)
- [10. Sicherheit](#10-sicherheit)

---

## 1. Was man braucht

**Stick:** UZ801 mit **Qualcomm MSM8916** und **4-GB-eMMC**. Gehäuse öffnen und den Platinen-Aufdruck
notieren (z. B. `UZ801_V3.0`, `FY_UZ801_V3.31`) – das Image muss zur Revision passen.
Getestet ist `FY_UZ801_V3.31`.

**PC (Windows 10/11)** für Backup und Flash:

| Werkzeug | Zweck |
|---|---|
| [Android platform-tools](https://developer.android.com/tools/releases/platform-tools) | `adb` (Original-Firmware), `fastboot` |
| Python 3.10+ und [bkerler/edl](https://github.com/bkerler/edl) | Lesen/Schreiben im Qualcomm-EDL-Modus |
| [Zadig](https://zadig.akeo.ie/) | WinUSB-Treiber für das EDL-Gerät `05C6:9008` (einmalig, Admin-Rechte) |
| dieses Repository | `tools\flash.ps1`, `tools\raw2qfil.ps1`, `tools\deploy.ps1` |

> `edl` unter Windows: Lässt sich `pylzma` mangels C++-Compiler nicht bauen, reicht
> `pip install -r requirements.txt` ohne pylzma. Startet `edl` wegen Git-Symlinks nicht, über einen
> kleinen Wrapper aufrufen (`python <edl-Ordner>\edl.py %*`) und den an `-EdlCmd` übergeben.

**Stromversorgung:** Der Stick zieht beim Senden über 1 A. Direkt an einen USB-Port des Rechners
stecken, nicht an schwache Hubs – sonst Abstürze oder ein „unbekanntes USB-Gerät“.

## 2. Backup (Pflicht)

Ohne Backup der Modem-Partitionen (`modemst1`, `modemst2`, `fsg`, `fsc`, `persist`, `modem`, `sec`)
verliert der Stick IMEI und Funk-Kalibrierung – LTE wäre dauerhaft tot.

1. Stick einstecken, die Original-Firmware meldet sich als RNDIS-Netzwerk (`192.168.100.1`).
2. ADB einschalten: `http://192.168.100.1/usbdebug.html` aufrufen (bei manchen Firmwares `.htm`).
   Der Stick meldet sich danach neu an (USB-ID `05C6:90B6`).
3. In den EDL-Modus: `adb reboot edl` → Gerät `05C6:9008` („QHSUSB__BULK“).
4. Einmalig mit Zadig dem Gerät **WinUSB** zuweisen (*Options → List All Devices*, USB-ID prüfen).
   Danach Stick abziehen, 15 s warten, direkt am Rechner einstecken und Schritt 3 wiederholen.
5. Backup:

   ```powershell
   .\tools\flash.ps1 -EdlCmd <edl> -CopyTo <zweiter-Ablageort>
   ```

   Das Skript liest die Partitionstabelle und alle Partitionen (außer `userdata`), vergleicht jede
   Datei mit der Länge aus der GPT, liest die Modem-Partitionen ein zweites Mal und vergleicht die
   Hashes, schreibt `SHA256SUMS` und kopiert alles nach `-CopyTo` (mit Hash-Prüfung).

Das Backup enthält IMEI und Gerätedaten – **nicht weitergeben**, nicht in ein Repository legen.

## 3. Debian aufspielen

Empfohlen ist das Image `openstick-uz801-linux.zip` des
[openstick-installer](https://github.com/Basistiy/openstick-installer/releases) (getestet: v0.0.5,
Debian 13.6, Kernel 6.6). Es enthält ein 1:1-Abbild der eMMC (`full_emmc.bin`). Den Installer des
Projekts benutzen wir **nicht**, er schreibt das Abbild komplett, inklusive fremder Modem-Partitionen.

```powershell
# Zip entpacken, dann in ein QFIL-Paket zerlegen (prüft GPT, CRC und eMMC-Größe gegen das Backup):
.\tools\raw2qfil.ps1 -Image <pfad>\full_emmc.bin -OutDir <qfil-ordner> -BackupDir backup\<backup-ordner>

# Stick wieder in EDL bringen, dann:
.\tools\flash.ps1 -Mode Flash -ImageDir <qfil-ordner> -EdlCmd <edl> -CopyTo <zweiter-Ablageort>
```

`flash.ps1` macht zuerst ein **frisches Backup**, zeigt den Plan und schreibt erst nach der Eingabe
`FLASHEN`. Die Modem-Partitionen werden aus dem Image **gestrichen** und danach aus dem Backup
zurückgeschrieben und verifiziert. Ein USB-Fehler beim abschließenden Neustart ist normal.

## 4. Erster Start

Der Stick meldet sich als RNDIS-Netzwerk des Images (`192.168.5.1`, Benutzer `user`, Passwort `1`).

```powershell
ssh user@192.168.5.1
sudo passwd root                       # ZUERST: eigenes root-Passwort
# und/oder eigenen SSH-Schlüssel für root eintragen:
#   /root/.ssh/authorized_keys NEU schreiben (im Image liegt dort eine Datei aus NUL-Bytes)
```

Prüfen (optional): `mmcli -m any` – Modem erkannt, IMEI nicht 0? `uname -a`, `ls /sys/class/udc`.

Für die Installation braucht der Stick **Internet**:

```sh
nmcli device wifi connect "<SSID>" password "<Passwort>"
```

Der WLAN-Hotspot des Images („Openstick“, Passwort `openstick`) sendet ab Werk – die Installation
schaltet ihn ab.

## 5. VPN-Dongle installieren

Paket `vpndongle-<version>.tar.gz` aus den [Releases](https://github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/releases) laden, dann:

```powershell
.\tools\deploy.ps1 -StickHost 192.168.5.1      # braucht SSH-Schlüssel für root
```

oder von Hand: Paket nach `/root` kopieren, entpacken, `sh install.sh` als root.

`install.sh` installiert die Pakete (u. a. Zabbix 7.0), setzt die Zeitzone `Europe/Berlin` und
**härtet das Image** (`system/harden-base.sh`): neue machine-id und SSH-Host-Schlüssel, leeres
root-Passwort gesperrt, Benutzer `user` gesperrt (nur wenn root ein Passwort oder einen Schlüssel hat),
Hotspot-, LTE- und USB-Profile des Images weggeräumt, Image-Gadget und `hostapd` maskiert.
Danach den PC-Eintrag des alten Host-Schlüssels löschen (`ssh-keygen -R 192.168.5.1`).

**Vor dem Neustart:** Erst danach übernimmt das eigene USB-Netz (NCM, `192.168.226.1`). Kommt es nicht
hoch, öffnet der Stick SSH als Notzugang über WLAN/LTE. In einem **Gäste-WLAN mit Client-Isolation**
funktioniert dieser Rückweg nicht – dann vorher das Gadget live testen (fällt nach 120 s ohne
Bestätigung automatisch auf das Image-Gadget zurück):

```powershell
scp tools\gadget-probe.sh root@192.168.5.1:/run/gadget-probe.sh     # Datei mit LF, ohne BOM
ssh root@192.168.5.1 "systemd-run --unit=gadget-probe --collect sh /run/gadget-probe.sh 120"
# PC bekommt am neuen Adapter 192.168.226.x, dann innerhalb von 120 s:
ssh root@192.168.226.1 "touch /run/gadget-ok"
```

Dann `systemctl reboot`. Windows bindet das NCM-Gerät ohne Treiber, der PC bekommt `192.168.226.2–6`.

## 6. Bedienung

Weboberfläche: **`http://192.168.226.1/`**. Die Übersicht ist ohne Login erreichbar (Status, WLAN
verbinden, Ressourcen, Dateien), die **Einstellungen** brauchen den Admin-Login – ab Werk `admin`/`admin`,
**sofort ändern** (Einstellungen → Zugang).

**Status:** Uplink, WLAN mit Signal, LTE, VPN mit letztem Handshake und Datenmenge, CPU- und
RAM-Auslastung (Mittel über 60 s). Maschinenlesbar unter `/status.json`.

**WLAN:** auf der Übersicht suchen und verbinden. Bis zu 10 Netze, die Reihenfolge ist die Priorität.
Ohne Uplink sperrt das Captive-Portal das Internet und leitet auf die WLAN-Seite um.

**WireGuard** (Einstellungen → WireGuard), Zuordnung aus einer `wg-quick`-Datei:

| wg-quick | Feld |
|---|---|
| `[Interface] PrivateKey` | Private Key |
| `[Interface] Address = 10.10.0.205/24` | Tunnel-IP `10.10.0.205`, Maske `255.255.255.0` |
| `[Interface] DNS` | DNS im Volltunnel |
| `[Peer] PublicKey` / `PresharedKey` | Peer Public Key / Preshared Key |
| `[Peer] Endpoint = host:port` | Endpoint + Port |
| `[Peer] AllowedIPs = 0.0.0.0/0` | Haken **Volltunnel**; sonst Tunnel-Netz + „Zusätzliche Netze“ |
| `PersistentKeepalive` | Keepalive |

Schlüssel und PSK werden nie angezeigt; leer lassen = unverändert. **Jeder Dongle braucht einen eigenen
Peer** (eigener Schlüssel, eigene Tunnel-IP): Laufen zwei Geräte mit demselben Schlüssel, bekommt nur
eines Antworten.

Routen kommen per DHCP Option 121 auf den PC: Split = Tunnel-Netz + Standardroute über den Dongle,
Volltunnel = `0.0.0.0/1` + `128.0.0.0/1` (schlägt jede andere Standardroute). **Kill-Switch**: ohne
Tunnel kein Internet für den PC. **VPN-Schalter**: Tunnel aus, Konfiguration bleibt.

**LTE:** APN, ggf. PIN und Roaming. „Uplink: Automatisch“ schaltet LTE zu, wenn 20 s kein WLAN da ist,
und wieder ab, wenn das WLAN 60 s stabil ist.

**Zugriff auf den Stick:** Haken-Tabelle, aus welchen Netzen Weboberfläche, SSH und Ping erreichbar
sind. Ab Werk: Weboberfläche nur USB, SSH über USB und VPN, Ping überall. Die Weboberfläche über USB
lässt sich nicht abschalten; über VPN/WLAN/LTE erst nach Ändern des Admin-Passworts.

**SSH:** `ssh root@192.168.226.1` (über VPN: Tunnel-IP). Das root-Passwort lässt sich in den
Einstellungen setzen; es landet nur in `/etc/shadow`, nicht in der Konfig.

**Ressourcen & Dateien:** RDP-Ziele (die Übersicht liefert fertige `.rdp`-Dateien), Links, SSH-Ziele;
Dateien bis 64 MB (512 MB gesamt) zum Herunterladen bereitstellen.

**Zabbix:** siehe [zabbix.md](zabbix.md).

## 7. Updates

Einstellungen → Systemupdate:

- **Jetzt aktualisieren** bzw. wöchentlich (So. nachts): Debian- und Zabbix-Pakete. Kernel, Firmware und
  Bootloader werden **gehalten** (der Kernel des Sticks stammt nicht aus Debian). Über LTE nur mit Haken.
- **VPN-Dongle-Software aus GitHub-Releases** (Haken, ab Werk an): Beim Update-Lauf holt der Stick ein
  neueres Release, **nur wenn** die Ed25519-Signatur zum eingebauten Schlüssel passt und die Prüfsumme
  stimmt. Ältere oder gleiche Versionen werden nie installiert. „Nach neuer Version suchen“ zeigt nur an.
- **Paket einspielen:** ein `vpndongle-<version>.tar.gz` hochladen (z. B. ohne Internet am Stick).

Das Protokoll steht unter „Update-Protokoll“.

## 8. Konfiguration sichern und verteilen

Einstellungen → Konfiguration: Export/Import als verschlüsselte Datei (Format BHVPN1, AES-256 +
HMAC, Schlüssel = 20-stelliges Konfig-Secret). Fehlende Bereiche bleiben beim Import erhalten.
Das Format ist mit dem ESP32-Dongle kompatibel.

`tools\admin.ps1` erstellt und entschlüsselt solche Dateien am PC:

```powershell
.\tools\admin.ps1 -Mode create -Out stick.bhvpn -Secret <20 Zeichen>
.\tools\admin.ps1 -Mode decrypt -In stick.bhvpn -Secret <20 Zeichen>
```

**Config-Server:** Basis-URL eintragen; der Stick fragt `<URL>/<Seriennummer>/config.json` ab und
meldet neuere Versionen (`version=`), eingespielt wird per Knopf. Die Seriennummer steht in den
Einstellungen (abgeleitet aus der machine-id, gleich der USB-Seriennummer).

## 9. Fehlersuche und Rettung

| Problem | Abhilfe |
|---|---|
| PC sieht kein Netzwerk | anderen USB-Port (direkt am Rechner), 15 s stromlos; Geräte-Manager: „UsbNcm Host Device“? |
| USB-Netz fehlt nach Update | SSH-Notfallmodus: der Stick öffnet SSH über WLAN/LTE (Warnung in der Weboberfläche) |
| „Unbekanntes USB-Gerät“ (Code 43) | abziehen, 15 s warten, direkt am Rechner einstecken; schwacher Hub? |
| WLAN langsam | WCN3620 kann nur 2,4 GHz; ~4–7 Mbit/s sind normal |
| VPN „verbindet …“ ohne Daten | gleicher Schlüssel auf einem zweiten Gerät aktiv? Endpoint/Port erreichbar? |
| Updates laufen nicht | Uplink LTE ohne Freigabe? Update-Protokoll prüfen |

**Kein Zugang mehr:** Der UZ801 hat keine Taste für fastboot/EDL. Dann Gehäuse öffnen, den
EDL-Testpunkt gegen Masse halten und einstecken (`05C6:9008`). Im EDL-Modus lässt sich jede Partition aus
dem Backup zurückschreiben (`edl w <partition> <backup>\<partition>.bin`) oder das Image neu flashen.

## 10. Sicherheit

- Admin-Passwort und root-Passwort setzen; SSH-Schlüssel statt Passwort.
- Weboberfläche nur dort freigeben, wo nötig (über VPN ist auch die Übersicht ohne Login erreichbar).
- Konfig-Exporte enthalten Schlüssel (verschlüsselt) – das Secret getrennt aufbewahren.
- Backups enthalten IMEI und Kalibrierung, Images proprietäre Firmware – nicht weitergeben.
- Selbst-Updates sind signiert; der private Signaturschlüssel liegt nur beim Herausgeber.
