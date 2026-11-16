package config

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

// Container-Format (identisch zu ESP-Firmware und tools/admin.ps1):
//
//	"BHVPN1." + base64( IV(16) || AES-256-CBC(PKCS7) || HMAC-SHA256(32) )
//	encKey = SHA256("bhvpn-enc:" + secret)
//	macKey = SHA256("bhvpn-mac:" + secret)
//	HMAC   = HMAC-SHA256(macKey, IV || Ciphertext)   (Encrypt-then-MAC)
const containerPrefix = "BHVPN1."

var ErrBadMAC = errors.New("HMAC ungültig – falsches Secret oder beschädigte Datei")

func deriveKey(prefix, secret string) []byte {
	s := sha256.Sum256([]byte(prefix + secret))
	return s[:]
}

// Encrypt verschlüsselt Klartext in einen BHVPN1-Container.
func Encrypt(plain, secret string) (string, error) {
	block, err := aes.NewCipher(deriveKey("bhvpn-enc:", secret))
	if err != nil {
		return "", err
	}
	pt := []byte(plain)
	pad := aes.BlockSize - len(pt)%aes.BlockSize
	pt = append(pt, bytes.Repeat([]byte{byte(pad)}, pad)...)

	blob := make([]byte, aes.BlockSize+len(pt), aes.BlockSize+len(pt)+sha256.Size)
	iv := blob[:aes.BlockSize]
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(blob[aes.BlockSize:], pt)

	m := hmac.New(sha256.New, deriveKey("bhvpn-mac:", secret))
	m.Write(blob)
	blob = m.Sum(blob)
	return containerPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt prüft den HMAC und entschlüsselt einen BHVPN1-Container.
func Decrypt(container, secret string) (string, error) {
	container = strings.TrimSpace(container)
	rest, ok := strings.CutPrefix(container, containerPrefix)
	if !ok {
		return "", errors.New("falsches Format (kein BHVPN1.)")
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
	if err != nil {
		return "", err
	}
	if len(blob) < aes.BlockSize*2+sha256.Size || (len(blob)-sha256.Size)%aes.BlockSize != 0 {
		return "", errors.New("Container zu kurz oder beschädigt")
	}
	data, mac := blob[:len(blob)-sha256.Size], blob[len(blob)-sha256.Size:]
	m := hmac.New(sha256.New, deriveKey("bhvpn-mac:", secret))
	m.Write(data)
	if !hmac.Equal(m.Sum(nil), mac) {
		return "", ErrBadMAC
	}
	block, err := aes.NewCipher(deriveKey("bhvpn-enc:", secret))
	if err != nil {
		return "", err
	}
	iv, ct := data[:aes.BlockSize], data[aes.BlockSize:]
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
	pad := int(pt[len(pt)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(pt) {
		return "", errors.New("ungültiges Padding")
	}
	for _, b := range pt[len(pt)-pad:] {
		if int(b) != pad {
			return "", errors.New("ungültiges Padding")
		}
	}
	return string(pt[:len(pt)-pad]), nil
}
