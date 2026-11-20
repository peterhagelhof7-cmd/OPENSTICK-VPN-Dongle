# Inbetriebnahme UZ801 + Testplan

> Reihenfolge einhalten. **Schritt 1 (Backup) ist Pflicht.** Ohne die Modem-Partitionen
> verliert der Stick IMEI und Kalibrierung, LTE ist dann dauerhaft tot.
> Flash-Schritte nur nach ausdrücklichem OK (Projektregel).

## 0. Vor dem Auspacken festhalten

- Platinen-Aufdruck (z. B. `UZ801_V3.2`) fotografieren, denn die Images hängen von der Revision ab.
  Unser Gerät (2026-11-15): **`FY_UZ801_V3.31`**. Die Community führt die Revision als unterstützt, gleiche
  Gerätebaum-Familie wie V3.0 (`msm8916-yiming-uz801v3`). Prozessor muss mit `MSM8916` bedruckt sein, der Speicher ein BGA-eMMC.
- ADB in der Original-Firmware: `http://192.168.100.1/usbdebug.html` aufrufen (`.htm` gibt 404; die Seite
  schickt nur `POST /ajax {"funcNo":2001}`). Der Stick meldet sich danach neu an (PID `F00E` → `90B6`,
  `persist.sys.usb.config=rndis,serial_smd,diag,adb`, bleibt also dauerhaft an). Dann nur lesend prüfen:
  `adb shell getprop ro.board.platform` (→ `msm8916`) und
  `adb shell ls -l /dev/block/bootdevice/by-name` (boot, aboot, modem, fsg, modemst1/2 … vorhanden?)
- Original-Firmware einmal kurz testen: Kommt LTE mit der SIM hoch? Dann wissen wir,
  dass Modem und Bänder grundsätzlich gehen. Welche Bänder (vor allem **B20/800 MHz**)?

## 1. Komplett-Backup (EDL)

Werkzeug: [bkerler/edl](https://github.com/bkerler/edl) (Python). Der Stick muss im
Qualcomm-EDL-Modus sein (USB-ID `05c6:9008`). Wie man hineinkommt, hängt von der Firmware ab.
Die Community-Wege (am Gerät prüfen, nicht blind übernehmen):
- ADB in der Original-Firmware aktivieren und `adb reboot edl`
- Taste beim Einstecken halten bzw. Testpunkte brücken

```powershell
.\tools\flash.ps1            # nur Backup
```

Das Skript erledigt das Backup komplett selbst:
- liest die Partitionstabelle (ohne erkannte Tabelle Abbruch)
- liest alle Partitionen (außer `userdata`, mit `-IncludeUserdata` auch diese) und die primäre GPT
- prüft jede Datei gegen die Länge aus der GPT
- liest die geschützten Partitionen (`modemst1`, `modemst2`, `fsg`, `fsc`, `persist`, `modem`, `sec`)
  ein **zweites Mal** und vergleicht die Hashes
- schreibt `SHA256SUMS` und `info.txt` nach `backup\uz801-<serial>-<zeit>\` (schreibgeschützt)
- kopiert alles zusätzlich nach `-CopyTo <Ordner>` (z. B. ein Netzlaufwerk oder Cloud-Ordner) und prüft die Kopie dort

Das Backup enthält IMEI und Gerätedaten: **nie ins Repo** (`.gitignore` schließt `backup*/` aus).
Getestet ist das Skript bisher nur gegen eine edl-Attrappe (`tools\test\test-flash.ps1`).
Die Ausgabe von `edl printgpt` am echten Stick muss zum Parser passen. Passt sie nicht, bricht das Skript ab, statt blind weiterzumachen.

## 2. OpenStick-Debian aufspielen

Image für die eigene Platinen-Revision wählen (OpenStick-Wiki bzw. aktueller Community-Build). Das Format
erkennt das Skript selbst:

| Format | Erkennung | Schreibweg |
|---|---|---|
| QFIL | `rawprogram*.xml` (+ `patch*.xml`, sonst leere Patch-Datei) | EDL (`edl qfil`) |
| fastboot | `flash*.sh` des Images (z. B. OpenStick `base/flash.sh`) oder eigene Liste per `-PlanFile` | fastboot |
| Rohabbild | ein `full_emmc.bin` o. ä. (1:1-Abbild der eMMC) | erst mit `raw2qfil.ps1` in QFIL umwandeln |

### Gewähltes Image (2026-11-16)

[openstick-installer](https://github.com/Basistiy/openstick-installer) **v0.0.5**, `openstick-uz801-linux.zip`
(SHA256 `b3fd0677e3c26cd948f671df6ab1f69aa745ed3d66e380062775f7dab08113c2`, enthält `full_emmc.bin`):
Debian 13.6, Kernel 6.6.0-msm8916, lk2nd + qhypstub, Gerätebaum `msm8916-yiming-uz801v3`; NetworkManager,
ModemManager, rmtfs, qrtr, nftables, wireguard-tools, dnsmasq und openssh sind schon drin, `wireguard.ko`,
`nf_tables` und `usb_f_ncm` als Module. Neue Partitionstabelle (14 Partitionen, rootfs 3,5 GB), die frühen
Bootloader werden ersetzt (sbl1 BOOT.BF.3.0-00288, tz TZ.BF.3.0-00714, rpm RPM.BF.2.0-00186; Werks-Versionen im Backup).
Den Installer des Projekts benutzen wir **nicht**: er schreibt das Abbild per `edl wf` komplett.

```powershell
# Abbild nach image-kandidaten\ entpacken (gitignored), dann:
.\tools\raw2qfil.ps1 -Image image-kandidaten\openstick-installer-v0.0.5\full_emmc.bin `
    -OutDir image-kandidaten\uz801-v0.0.5-qfil -BackupDir backup\<backup-ordner>
.\tools\flash.ps1 -Mode Flash -ImageDir image-kandidaten\uz801-v0.0.5-qfil -EdlCmd <edl-Ordner>\edl.cmd
```

`raw2qfil.ps1` prüft beide GPT-Kopien (Signatur, CRC, gleiche Einträge), Überlappungen und ob das Abbild
genau so viele Sektoren hat wie die eMMC laut Backup. Danach schreibt `flash.ps1` beide GPT-Kopien und alle
Partitionen außer den geschützten, vergleicht die neue Partitionstabelle mit dem Image und schreibt die
geschützten Partitionen aus dem Backup zurück (zuerst nach dem Flashen, noch in EDL, bevor das Modem startet).

Was das Image mitbringt und `install.sh` (über `system/harden-base.sh`) abstellt:

| Im Image | Wird |
|---|---|
| gleiche machine-id und SSH-Host-Schlüssel in allen Sticks | einmalig neu erzeugt (am PC danach `ssh-keygen -R <adresse>`) |
| `root` mit **leerem** Passwort | gesperrt |
| Benutzer `user` / Passwort `1`, sudo ohne Passwort | gesperrt, nologin, sudo-Regel weg – **nur wenn root ein Passwort oder einen SSH-Schlüssel hat** |
| NM-Profil `hotspot` (WLAN-Zugangspunkt „Openstick“) | weggeräumt nach `/var/lib/vpndongle/nm-image-profiles` |
| NM-Profil `lte` (APN `internet`, verbindet automatisch) | weggeräumt (LTE steuert vpndongled) |
| `usb-gadget.service` (ECM+RNDIS über `gt`), NM-Profile `usb`/`usb1` | maskiert bzw. weggeräumt, wirkt ab dem nächsten Start |
| `hostapd.service` | maskiert |
| feste WLAN-MAC (`wcnss_mac_addr`) | NM `wifi.cloned-mac-address=stable` (eigene MAC je Gerät) |

Vor `install.sh` deshalb als `user` (über USB `192.168.5.1` oder WLAN) einloggen und **zuerst ein
root-Passwort setzen** (`sudo passwd root`) oder einen SSH-Schlüssel nach `/root/.ssh/authorized_keys`
legen. Sonst bleibt `user` aktiv und `install.sh` warnt.

```powershell
.\tools\flash.ps1 -Mode Flash -ImageDir <Image-Ordner>
```

1. Der Flash-Plan wird **vor** dem Backup geprüft. Beim fastboot-Format wird das Skript des Images
   **nicht ausgeführt**. Übernommen werden nur einfache Zeilen `fastboot flash|erase|reboot-bootloader`
   (Option `-S` erlaubt), `adb`-, `echo`- und `reboot`-Zeilen werden übersprungen. Variablen, Schleifen
   oder unbekannte Befehle brechen ab. Dann eine eigene Liste mit `-PlanFile` angeben (gleiche Syntax).
2. Danach kommt **immer** ein frisches Backup wie oben.
3. Geschützte Partitionen werden aus dem Plan **gestrichen**, sowohl flash als auch erase.
4. Das Skript zeigt den Plan an (eine neue Partitionstabelle ist markiert) und flasht erst nach `FLASHEN`.
5. Nur beim fastboot-Format: Wechsel in den fastboot-Modus, wählbar mit `-EnterFastboot`.
   - `BootErase`: die (gesicherte) boot-Partition per EDL löschen und neu starten, dann fällt der
     Bootloader in fastboot.
   - `Manual`: selbst wechseln, z. B. `adb reboot bootloader` in der Original-Firmware.
   - Ohne Angabe fragt das Skript.
6. Nach dem Flashen bootet der Stick **nicht** normal, denn sonst würde das Modem seine EFS-Partitionen verändern.
   Stattdessen wechselt er per `fastboot oem reboot-edl` nach EDL. Klappt das nicht, bittet das Skript, ihn von Hand
   in EDL zu bringen.
7. In EDL schreibt es die geschützten Partitionen aus dem Backup zurück und liest sie zur Kontrolle
   wieder aus. Das klappt auch, wenn das Image neu partitioniert hat.

Bricht etwas mitten im Flashen ab, nennt das Skript den Backup-Ordner und den Befehl zur Wiederherstellung.

Getestet ist alles gegen edl- und fastboot-Attrappen, die als eigene Prozesse laufen
(`tools\test\test-flash.ps1`, 49 Prüfungen inkl. `raw2qfil.ps1`). Die Härtung prüft
`tools/test/test-harden.sh` (WSL, gegen ein Minimal-System oder mit `rootfs.bin` gegen die echten
Image-Dateien). Am echten Stick bestätigt (2026-11-16): `printgpt`-Ausgabe passt zum Parser, Backup komplett,
`edl qfil` mit dem echten Paket (3,5-GB-rootfs, ~18 min), neue Partitionstabelle = Image, alle 7
Modem-Partitionen im neuen Layout zurückgeschrieben und verifiziert, Stick bootet Debian, IMEI erhalten.
`edl reset` meldet am Ende einen USB-Fehler, weil sich der Stick trennt – das ist normal und wird toleriert.
Noch offen (nur fastboot-Weg, für openstick-installer-Images nicht nötig):
- fällt der Original-Bootloader bei leerer boot-Partition wirklich in fastboot?
- kennt er `oem reboot-edl`?

Nach dem ersten Boot prüfen:

```
uname -a; cat /etc/os-release
systemctl status rmtfs qrtr-ns ModemManager NetworkManager
mmcli -L && mmcli -m any            # Modem da? IMEI plausibel (nicht 0)?
ls /sys/class/leds /sys/class/udc
```

WLAN einrichten und **SSH über WLAN** testen. Nach der Installation ist SSH nur noch über USB und
den VPN-Tunnel erreichbar. Fehlt das USB-Netz (Gadget kommt nicht hoch), öffnet vpndongled SSH
automatisch auch über WLAN/LTE (Notfallmodus). Damit bleibt dieser Weg der Rückweg:

```
nmcli device wifi connect "<SSID>" password "<PW>"
```

SSH-Key für root hinterlegen.

Ist das WLAN ein Gastnetz mit Client-Isolation (z. B. ein Gäste-WLAN), gibt es diesen Rückweg nicht.
Dann **vor dem ersten Neustart** nach `install.sh` unser USB-Gadget live testen, mit automatischem
Rückfall auf das Image-Gadget, wenn die Bestätigung ausbleibt:

```powershell
scp tools\gadget-probe.sh root@192.168.5.1:/run/gadget-probe.sh     # Datei mit LF, ohne BOM!
ssh root@192.168.5.1 "systemd-run --unit=gadget-probe --collect sh /run/gadget-probe.sh 120"
# PC bekommt am neuen NCM-Adapter 192.168.226.x, dann binnen 120 s:
ssh root@192.168.226.1 "touch /run/gadget-ok"
```

Erst danach neu starten. (2026-11-16 hat genau dieser Test einen Fehler gefunden: `usb-gadget.sh`
band das Gadget nie, weil `[ -s UDC ]` bei configfs immer wahr ist.)

## 3. Installation

Unter Windows:

```powershell
.\tools\build.ps1
.\tools\deploy.ps1 -StickHost <IP> -Check      # nur Bestandsaufnahme
.\tools\deploy.ps1 -StickHost <IP>             # installieren
```

Danach auf dem Stick `systemctl reboot`.

## 4. Testplan (Hardware)

Abhaken und Ergebnis notieren. In Klammern steht die entsprechende Phase beim ESP.

| # | Test | Erwartung |
|---|---|---|
| 1 | Stick an Windows 11 (P1) | Netzadapter ohne Treiber, IP 192.168.226.2/29, `ping 192.168.226.1` |
| 2 | macOS / iPadOS | Adapter erkannt, IP per DHCP |
| 3 | Web-UI `http://192.168.226.1/` | Seite lädt, Status zeigt WLAN/LTE |
| 4 | Captive ohne Uplink (P4) | WLAN aus → OS-„Anmelden“-Hinweis, DNS liefert 192.168.226.1, kein Forwarding |
| 5 | WLAN über Web hinzufügen (P2) | verbindet, LED blau, Captive frei |
| 6 | Internet ohne VPN (P3) | PC surft über Dongle, `tracert` erster Hop .1 |
| 7 | WireGuard Volltunnel (P5) | Handshake, LED grün, öffentliche IP = Heimnetz |
| 8 | Option 121 (P9) | `route print`: 0.0.0.0/1 + 128.0.0.0/1 via .1 nach `ipconfig /renew` |
| 9 | Split-Tunnel (neu) | nur Tunnel-Netz + wg.allowed durchs VPN, Rest direkt |
| 10 | Kill-Switch (P6) | Endpoint am Peer sperren → PC sofort offline |
| 11 | LAN-Isolation (neu) | PC erreicht 192.168.x im WLAN am Uplink **nicht**, Internet schon |
| 12 | LTE-Rückfall (neu) | WLAN weg → nach ~20 s LTE, LED blau blinkend, VPN kommt wieder; WLAN zurück → nach ~60 s LTE aus |
| 13 | Nur-LTE-Modus | Uplink „Nur LTE“: WLAN getrennt, LTE aktiv |
| 14 | SIM mit PIN | PIN in UI → Modem entsperrt; falsche PIN → Fehler sichtbar, **max. 2 Versuche** |
| 15 | Durchsatz (P10) | SMB-Kopie 100 MB über Volltunnel, WLAN und LTE getrennt messen |
| 16 | Konfig-Export → `admin.ps1 -Mode decrypt` | Klartext vollständig, LTE-Schlüssel enthalten |
| 17 | ESP-Konfig importieren | WLANs/WG übernommen, LTE-Einstellungen unverändert |
| 18 | Neustart / Stromverlust | alles kommt von selbst wieder, Konfig erhalten |
| 19 | 24-h-Dauerlauf | kein Speicherwachstum (`/settings` → Speicher frei), keine Dienst-Neustarts |
| 20 | Zabbix einschalten (siehe [zabbix.md](zabbix.md)) | Proxy + Agent „active“, Proxy auf dem Server „zuletzt gesehen“ aktuell, Host per Autoregistrierung angelegt |
| 21 | Bereiche umschalten (LTE aus, Ziel hinzufügen) | nach ≤ 5 min verschwinden/entstehen die Items auf dem Server |
| 22 | Tunnel 30 min unterbrechen | Werte kommen nach dem Wiederaufbau nach (Proxy-Puffer), Trigger „VPN getrennt“ |
| 23 | RAM mit Zabbix | `ps -o rss,cmd -C zabbix_proxy,zabbix_agentd` → Summe notieren (Schätzung 40–70 MB) |
| 24 | Systemupdate per Knopf | Protokoll zeigt apt-Lauf, Kernel/Firmware „Gehalten“, Dienste laufen danach |
| 25 | Update über LTE | ohne Haken „auch über LTE“ wird übersprungen |
| 26 | Software-Paket per Weboberfläche | neue Version läuft (Fußzeile), Konfig erhalten |
| 27 | Wöchentlicher Timer | `systemctl list-timers vpndongle-update.timer` zeigt den nächsten Lauf |
| 28 | SSH vom PC | `ssh root@192.168.226.1` klappt (Passwort aus der Weboberfläche bzw. Schlüssel) |
| 29 | SSH über VPN | von einem Rechner im Heimnetz `ssh root@<Tunnel-IP>` klappt |
| 30 | SSH über WLAN/LTE | von einem Gerät im selben WLAN: Verbindung wird verworfen (Timeout) |
| 31 | SSH-Notfallmodus | Gadget lösen (`echo "" > /sys/kernel/config/usb_gadget/vpndongle/UDC`): Prüfen, ob usb0 verschwindet; falls ja, SSH über WLAN offen + Warnung in der Weboberfläche. Danach `usb-gadget.sh start` |
| 32 | Root-Passwort per Weboberfläche | neues Passwort gilt sofort, Altes nicht mehr; Export enthält es nicht |
| 33 | Rufnummer (SIM) | Status zeigt die Nummer der SIM; ohne: eingetragene mit „(eingetragen)“, sonst „unbekannt“; `mmcli -m any -J` → `own-numbers` notieren |
| 34 | SMS empfangen | „SMS aktiv“, SMS ans Handy-Gegenüber → nach ≤ 15 s in der Liste, Zähler in der Kopfzeile; `mmcli -m any --messaging-list-sms` danach leer |
| 35 | SMS mehrteilig + Umlaute/Emoji | lange SMS (> 160 Zeichen) mit äöüß und 😀 kommt als **eine** Nachricht, Zeichen korrekt |
| 36 | SMS senden | an ein Handy, mit äöü und mit `'` und `"` im Text → kommt unverändert an (`"` als ”); Fehlversuch (falsche Nummer) steht mit Fehler in der Liste |
| 37 | SMS bei reinem WLAN | Uplink „nur WLAN“: Modem wird eingeschaltet (`mmcli -m any` → enabled/registered), SMS kommen trotzdem an |
| 38 | SMS-Datenschutz | `journalctl -u vpndongled` enthält weder Text noch Nummer; Reset LTE löscht `/var/lib/vpndongle/sms.json` |

Zu prüfende Annahmen (im Code mit `TODO(HW)` markiert):
- UDC-Name und ob das Image schon ein eigenes USB-Gadget (RNDIS) anlegt
- LED-Namen unter `/sys/class/leds`
- Modem-Netzschnittstelle (`wwan0`/`rmnet*`) und NM-Gerätetyp `gsm`
- dnsmasq-Benutzer bei `dnsmasq-base`
