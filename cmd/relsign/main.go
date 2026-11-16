// relsign – Release-Signatur für das Selbst-Update (Ed25519).
//
//	relsign genkey -out <datei>           Schlüsselpaar erzeugen; privater Schlüssel
//	                                      (Seed, base64) in <datei>, öffentlicher auf stdout
//	relsign sign   -key <datei> <SUMS>    schreibt <SUMS>.sig (base64)
//	relsign verify <SUMS>                 prüft <SUMS>.sig gegen den in vpndongled
//	                                      eingebauten öffentlichen Schlüssel
//
// Der private Schlüssel gehört NIE ins Repo (nur beim Herausgeber).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/peterhagelhof7-cmd/OPENSTICK-VPN-Dongle/internal/swupdate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "genkey":
		fs := flag.NewFlagSet("genkey", flag.ExitOnError)
		out := fs.String("out", "", "Datei für den privaten Schlüssel")
		fs.Parse(os.Args[2:])
		err = genkey(*out)
	case "sign":
		fs := flag.NewFlagSet("sign", flag.ExitOnError)
		key := fs.String("key", "", "privater Schlüssel (aus genkey)")
		fs.Parse(os.Args[2:])
		err = sign(*key, fs.Arg(0))
	case "verify":
		err = verify(os.Args[2])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "FEHLER:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "relsign genkey -out <datei> | sign -key <datei> <SHA256SUMS> | verify <SHA256SUMS>")
	os.Exit(2)
}

func genkey(out string) error {
	if out == "" {
		return errors.New("-out fehlt")
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s existiert schon – wird nicht überschrieben", out)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("Schlüsseldatei unlesbar")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func sign(keyPath, sums string) error {
	if keyPath == "" || sums == "" {
		return errors.New("-key und SHA256SUMS angeben")
	}
	priv, err := loadKey(keyPath)
	if err != nil {
		return err
	}
	if !priv.Public().(ed25519.PublicKey).Equal(swupdate.PublicKey()) {
		return errors.New("Schlüssel passt nicht zum eingebauten öffentlichen Schlüssel (internal/swupdate/pubkey.go)")
	}
	data, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
	return os.WriteFile(sums+".sig", []byte(sig+"\n"), 0o644)
}

func verify(sums string) error {
	data, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sums + ".sig")
	if err != nil {
		return err
	}
	// irgendein Paket aus der Liste genügt für die Signaturprüfung
	first := strings.Fields(strings.SplitN(string(data), "\n", 2)[0])
	if len(first) != 2 {
		return errors.New("SHA256SUMS leer")
	}
	if _, err := swupdate.VerifySums(swupdate.PublicKey(), data, sig, strings.TrimPrefix(first[1], "*")); err != nil {
		return err
	}
	fmt.Println("Signatur gültig")
	return nil
}
