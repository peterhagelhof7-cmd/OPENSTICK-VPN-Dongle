# Änderungen

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
