// Package system kapselt alle Seiteneffekte: Befehle ausführen und Dateien
// schreiben. Im Dry-Run (Entwicklung unter Windows) werden Befehle nur
// protokolliert und Dateien unter einem Ersatz-Wurzelverzeichnis abgelegt.
package system

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Sys = Befehle + Dateisystem.
type Sys interface {
	Run(name string, args ...string) (string, error)
	// RunInput wie Run, aber mit stdin (für Geheimnisse wie chpasswd –
	// nie als Argument, sonst in der Prozessliste sichtbar). input wird nie geloggt.
	RunInput(input string, name string, args ...string) (string, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	ReadFile(path string) ([]byte, error)
	Remove(path string) error
	Glob(pattern string) ([]string, error)
	DryRun() bool
}

// Real führt Befehle wirklich aus.
type Real struct{ Timeout time.Duration }

func (r Real) DryRun() bool { return false }

func (r Real) Run(name string, args ...string) (string, error) { return r.run(nil, name, args...) }

func (r Real) RunInput(input string, name string, args ...string) (string, error) {
	return r.run(strings.NewReader(input), name, args...)
}

func (r Real) run(stdin *strings.Reader, name string, args ...string) (string, error) {
	to := r.Timeout
	if to == 0 {
		to = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	// UTF-8 für alle Werkzeuge: Ohne Locale (systemd setzt keins) gibt mmcli jedes
	// Nicht-ASCII-Zeichen als „?“ aus – SMS „Grüße“ kam als „Gr??e“ an (26.11.2026).
	// C.UTF-8 lässt die Meldungen englisch, die Parser bleiben gültig.
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = stdin
	}
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s %s: %v: %s", name, strings.Join(redact(args), " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (r Real) WriteFile(path string, data []byte, perm os.FileMode) error {
	if strings.HasPrefix(path, "/sys/") || strings.HasPrefix(path, "/proc/") {
		return os.WriteFile(path, data, perm) // sysfs: kein tmp+rename möglich
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r Real) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (r Real) Remove(path string) error              { return os.Remove(path) }
func (r Real) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }

// Dry protokolliert Befehle und schreibt Dateien unter Root.
type Dry struct {
	Root string
	// Canned liefert Beispielausgaben für lesende Befehle (UI-Entwicklung).
	Canned func(name string, args []string) string
}

func (d Dry) DryRun() bool { return true }

func (d Dry) Run(name string, args ...string) (string, error) {
	log.Printf("[dry] %s %s", name, strings.Join(redact(args), " "))
	if d.Canned != nil {
		return d.Canned(name, args), nil
	}
	return "", nil
}

func (d Dry) RunInput(input string, name string, args ...string) (string, error) {
	log.Printf("[dry] %s %s (mit stdin, %d B – Inhalt nicht protokolliert)", name, strings.Join(redact(args), " "), len(input))
	return "", nil
}

func (d Dry) path(p string) string { return filepath.Join(d.Root, filepath.FromSlash(p)) }

func (d Dry) WriteFile(path string, data []byte, perm os.FileMode) error {
	log.Printf("[dry] schreibe %s (%d B)", path, len(data))
	return Real{}.WriteFile(d.path(path), data, perm)
}
func (d Dry) ReadFile(path string) ([]byte, error) { return os.ReadFile(d.path(path)) }
func (d Dry) Remove(path string) error             { return os.Remove(d.path(path)) }
func (d Dry) Glob(pattern string) ([]string, error) {
	m, err := filepath.Glob(d.path(pattern))
	for i := range m {
		rel, _ := filepath.Rel(d.Root, m[i])
		m[i] = "/" + filepath.ToSlash(rel)
	}
	return m, err
}

// redact verhindert, dass Geheimnisse im Log landen.
func redact(args []string) []string {
	out := make([]string, len(args))
	hide := false
	for i, a := range args {
		if hide {
			out[i], hide = "***", false
			continue
		}
		out[i] = a
		// SMS-Inhalt und -Empfänger nie ins Journal
		if strings.HasPrefix(a, "--messaging-create-sms") {
			out[i] = "--messaging-create-sms=***"
			continue
		}
		switch a {
		case "password", "psk", "pin", "--pin", "wifi-sec.psk", "gsm.password", "gsm.pin":
			hide = true
		}
	}
	return out
}
