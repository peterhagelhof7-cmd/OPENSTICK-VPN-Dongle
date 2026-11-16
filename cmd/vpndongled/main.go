// vpndongled – Steuerdienst des OpenStick-VPN-Dongles.
//
// Auf dem Stick:   vpndongled                       (als root via systemd)
// Unter Windows:   vpndongled -dry-run -listen 127.0.0.1:8080
//
//	(Weboberfläche mit Beispieldaten; Befehle werden nur protokolliert,
//	 erzeugte Dateien landen unter -dry-root)
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/app"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/config"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/swupdate"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/system"
	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/web"
)

var version = "dev" // per -ldflags "-X main.version=…"

func main() {
	var (
		cfgPath = flag.String("config", "/etc/vpndongle/config.json", "Konfig-Datei")
		listen  = flag.String("listen", ":80", "HTTP-Adresse (nftables lässt nur usb0 zu)")
		dry     = flag.Bool("dry-run", false, "nichts am System ändern (Entwicklung)")
		dryRoot = flag.String("dry-root", "dryrun-root", "Wurzel für Dateien im Dry-Run")
		showVer = flag.Bool("version", false, "Version ausgeben")
		zbx     = flag.String("zbx", "", "Abfrage für den Zabbix-Agent: status | discovery <gruppe>")
		swu     = flag.String("swupdate", "", "Selbst-Update aus GitHub-Releases: check | fetch")
		swDest  = flag.String("swupdate-dest", "/var/lib/vpndongle/update/vpndongle-update.tar.gz", "Ziel für -swupdate fetch")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version) // stdout (install.sh liest es per $(...))
		return
	}
	if *zbx != "" {
		os.Exit(zbxClient(*zbx, flag.Args(), *listen))
	}
	if *swu != "" {
		os.Exit(swUpdate(*swu, *swDest))
	}
	log.SetFlags(0)
	if os.Getenv("INVOCATION_ID") == "" { // nicht unter systemd -> Zeitstempel
		log.SetFlags(log.LstdFlags)
	}

	var sys system.Sys = system.Real{Timeout: 90 * time.Second}
	if *dry {
		root, _ := filepath.Abs(*dryRoot)
		sys = system.Dry{Root: root, Canned: canned}
		if *cfgPath == "/etc/vpndongle/config.json" {
			*cfgPath = filepath.Join(root, "etc", "vpndongle", "config.json")
		}
		seedDry(sys)
	}

	store, err := config.Open(*cfgPath)
	if err != nil {
		log.Fatalf("Konfig %s: %v", *cfgPath, err)
	}
	a := app.New(store, sys, version)
	log.Printf("vpndongled %s startet (SN %s, Dry-Run %v)", version, a.Serial(), *dry)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { a.Run(stop); close(done) }()

	srv := &http.Server{
		Addr: *listen, Handler: web.New(a).Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Printf("beende …")
	_ = srv.Close()
	close(stop)
	<-done
}

// zbxClient fragt den laufenden Dienst über Loopback ab und gibt die Antwort
// aus (für UserParameter des Zabbix-Agents, der als Benutzer zabbix läuft und
// die Konfig selbst nicht lesen darf). Rückgabe = Exit-Code.
func zbxClient(cmd string, args []string, listen string) int {
	port := "80"
	if _, p, err := net.SplitHostPort(listen); err == nil && p != "" {
		port = p
	}
	base := "http://127.0.0.1:" + port
	var u string
	switch cmd {
	case "status":
		u = base + "/status.json"
	case "discovery":
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "Aufruf: -zbx discovery <pc|wlan|wlangw|lte|vpn|targets>")
			return 2
		}
		u = base + "/zabbix/discovery?group=" + url.QueryEscape(args[0])
	default:
		fmt.Fprintln(os.Stderr, "Aufruf: -zbx status | -zbx discovery <gruppe>")
		return 2
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(u)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "HTTP %d\n", resp.StatusCode)
		return 1
	}
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		return 1
	}
	return 0
}

// swUpdate: neueres signiertes Release holen (für update.sh). Exit 0 auch,
// wenn es nichts Neues gibt; 1 bei Fehlern (Netz, Signatur, Prüfsumme).
func swUpdate(cmd, dest string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c := swupdate.New(version)
	r, err := c.Latest(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "GitHub-Abfrage:", err)
		return 1
	}
	newer := swupdate.Newer(version, r.Tag)
	switch cmd {
	case "check":
		fmt.Printf("installiert %s, neuestes Release %s, Update: %v\n", version, r.Tag, newer)
		return 0
	case "fetch":
		if !newer {
			fmt.Printf("Software aktuell (%s, neuestes Release %s)\n", version, r.Tag)
			return 0
		}
		sum, err := c.Fetch(ctx, r, dest)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Release", r.Tag+":", err)
			return 1
		}
		fmt.Printf("Release %s geladen, Signatur und SHA256 (%s…) geprüft\n", r.Tag, sum[:16])
		return 0
	}
	fmt.Fprintln(os.Stderr, "Aufruf: -swupdate check | fetch")
	return 2
}

// seedDry legt Beispiel-Systemdateien für den Dry-Run an.
func seedDry(s system.Sys) {
	_ = s.WriteFile("/sys/class/net/wlan0/address", []byte("02:00:5e:12:34:56\n"), 0o644)
	_ = s.WriteFile("/sys/class/net/usb0/operstate", []byte("up\n"), 0o644)
	_ = s.WriteFile("/proc/uptime", []byte("12345.67 23456.78\n"), 0o644)
	_ = s.WriteFile("/proc/meminfo", []byte("MemTotal:         385000 kB\nMemFree:  100000 kB\nMemAvailable:     214000 kB\n"), 0o644)
	for _, l := range []string{"red:os", "green:internet", "blue:wifi"} {
		_ = s.WriteFile("/sys/class/leds/"+l+"/brightness", []byte("0"), 0o644)
	}
}

// canned liefert plausible Ausgaben für die UI-Entwicklung.
func canned(name string, args []string) string {
	a := strings.Join(args, " ")
	switch {
	case name == "nmcli" && strings.Contains(a, "DEVICE,TYPE,STATE,CONNECTION"):
		return "wlan0:wifi:connected:vpndongle-wlan-0\nwwan0:gsm:disconnected:\nusb0:ethernet:unmanaged:\n"
	case name == "nmcli" && strings.Contains(a, "wifi list"):
		return "*:Büro-WLAN:78:WPA2\n :Gast:52:--\n :FRITZ!Box 7590 XY:40:WPA2 WPA3\n"
	case name == "mmcli":
		return `{"modem":{"generic":{"state":"registered","state-failed-reason":"--","unlock-required":"--",` +
			`"access-technologies":["lte"],"signal-quality":{"value":"64"}},"3gpp":{"operator-name":"Telekom.de","registration-state":"home"}}}`
	case name == "wg" && strings.HasSuffix(a, "latest-handshakes"):
		return "PUBKEY=\t" + strconv.FormatInt(time.Now().Add(-20*time.Second).Unix(), 10) + "\n"
	case name == "wg" && strings.HasSuffix(a, "transfer"):
		return "PUBKEY=\t12345678\t2345678\n"
	case name == "df":
		return "Avail\n 1.9G\n"
	case name == "ip" && strings.Contains(a, "route"):
		return "default via 192.168.50.1 dev wlan0 proto dhcp src 192.168.50.23 metric 100\n"
	case name == "systemctl" && strings.HasPrefix(a, "is-active"):
		return "inactive\n"
	}
	return ""
}
