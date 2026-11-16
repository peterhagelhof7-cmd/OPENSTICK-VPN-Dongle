package render

import (
	"fmt"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
)

// Pfade der Zabbix-Dateien (Debian-Pakete von repo.zabbix.com, 7.0).
const (
	ZbxProxyConf = "/etc/zabbix/zabbix_proxy.conf"
	ZbxAgentConf = "/etc/zabbix/zabbix_agentd.conf"
	ZbxPSKFile   = "/etc/zabbix/vpndongle-proxy.psk"
	ZbxMetadata  = "openstick-vpn-dongle" // HostMetadata für die Autoregistrierung
)

// ZabbixProxy erzeugt die Konfig des aktiven Proxys. Ausgelegt auf wenig RAM
// (Stick: ~320 MiB nutzbar) und wenig Flash-Schreiblast: Daten bleiben im
// Speicherpuffer und landen nur bei längerem Serverausfall in SQLite.
func ZabbixProxy(c *config.Config, sn string) string {
	proxy, _ := c.ZabbixNames(sn)
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	b.WriteString(header)
	p("ProxyMode=0") // aktiv: Proxy verbindet sich selbst zum Server (durch den Tunnel)
	p("Server=%s", c.Zabbix.Server)
	p("Hostname=%s", proxy)
	p("ListenPort=10051")
	p("LogType=system")
	p("PidFile=/run/zabbix/zabbix_proxy.pid")
	p("SocketDir=/run/zabbix")
	p("DBName=/var/lib/zabbix/zabbix_proxy.db")
	p("ProxyBufferMode=hybrid")
	p("ProxyMemoryBufferSize=16M")
	p("ProxyOfflineBuffer=24")
	p("CacheSize=8M")
	p("HistoryCacheSize=4M")
	p("HistoryIndexCacheSize=2M")
	p("StartPollers=1")
	p("StartPollersUnreachable=1")
	p("StartTrappers=2")
	p("StartPingers=1")
	p("StartPreprocessors=2") // Threads, kaum RAM; Doku empfiehlt >= Kerne
	p("StartDBSyncers=1")
	p("StartDiscoverers=0")
	p("StartHTTPPollers=0")
	p("StartODBCPollers=0")
	p("StartAgentPollers=0")
	p("StartHTTPAgentPollers=0")
	p("StartSNMPPollers=0")
	p("Timeout=4")
	p("FpingLocation=/usr/bin/fping")
	if c.Zabbix.PSKIdentity != "" && c.Zabbix.PSK != "" {
		p("TLSConnect=psk")
		p("TLSPSKIdentity=%s", c.Zabbix.PSKIdentity)
		p("TLSPSKFile=%s", ZbxPSKFile)
	}
	return b.String()
}

// ZabbixAgent erzeugt die Konfig des Agents auf dem Stick: nur aktiv, meldet
// an den lokalen Proxy. Die Werte holt er über vpndongled (-zbx).
func ZabbixAgent(c *config.Config, sn string) string {
	_, host := c.ZabbixNames(sn)
	var b strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	b.WriteString(header)
	p("PidFile=/run/zabbix/zabbix_agentd.pid")
	p("LogType=system")
	p("StartAgents=0") // kein passiver Port
	p("ServerActive=127.0.0.1:10051")
	p("Hostname=%s", host)
	p("HostMetadata=%s", ZbxMetadata)
	p("RefreshActiveChecks=60")
	p("Timeout=10")
	p("UserParameter=vpndongle.status,/usr/local/bin/vpndongled -zbx status")
	p("UserParameter=vpndongle.discovery[*],/usr/local/bin/vpndongled -zbx discovery $1")
	return b.String()
}

// UpdateEnv = Einstellungen für update.sh (/etc/vpndongle/update.env).
func UpdateEnv(c *config.Config) string {
	return fmt.Sprintf("%sUPDATE_ALLOW_LTE=%d\nUPDATE_REBOOT=%d\nUPDATE_GITHUB=%d\n", header,
		b2i(c.Update.AllowLTE), b2i(c.Update.AutoReboot), b2i(c.Update.GitHub))
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
