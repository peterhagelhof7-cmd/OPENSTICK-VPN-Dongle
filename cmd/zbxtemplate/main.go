// zbxtemplate schreibt zabbix/template_openstick_vpn_dongle.json neu.
//
//	go run ./cmd/zbxtemplate
package main

import (
	"log"
	"os"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/zbxtpl"
)

func main() {
	b, err := zbxtpl.JSON()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll("zabbix", 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(zbxtpl.File, b, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("%s geschrieben (%d B)", zbxtpl.File, len(b))
}
