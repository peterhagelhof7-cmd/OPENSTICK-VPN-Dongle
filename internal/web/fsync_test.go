package web

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubSync ersetzt fsyncFile/fsyncDir für einen Test und protokolliert die Aufrufe.
func stubSync(t *testing.T, fileErr error) (files *[]string, dirs *[]string) {
	t.Helper()
	oldF, oldD := fsyncFile, fsyncDir
	t.Cleanup(func() { fsyncFile, fsyncDir = oldF, oldD })
	var f, d []string
	fsyncFile = func(fh *os.File) error { f = append(f, fh.Name()); return fileErr }
	fsyncDir = func(dir string) error { d = append(d, dir); return nil }
	return &f, &d
}

func msgOf(loc string) string {
	u, _ := url.Parse(loc)
	return u.Query().Get("msg")
}

func TestUploadSyncedBeforeRename(t *testing.T) {
	a, h := setup(t)
	dir := a.FilesDirReal()
	final := filepath.Join(dir, "daten.bin")
	files, dirs := stubSync(t, nil)
	var finalAtSync bool
	fsyncFile = func(fh *os.File) error {
		*files = append(*files, fh.Name())
		_, err := os.Stat(final)
		finalAtSync = err == nil // darf beim Sync noch NICHT unter dem Endnamen existieren
		return nil
	}
	if m := msgOf(upload(h, "daten.bin", 4096).Header().Get("Location")); !strings.Contains(m, "hochgeladen") {
		t.Fatalf("Upload: %s", m)
	}
	if len(*files) != 1 || !strings.HasSuffix((*files)[0], ".daten.bin.part") {
		t.Fatalf("Sync nicht auf der Temp-Datei: %v", *files)
	}
	if finalAtSync {
		t.Fatal("Datei war schon vor dem Sync unter ihrem Namen sichtbar")
	}
	if len(*dirs) != 1 || filepath.Clean((*dirs)[0]) != filepath.Clean(dir) {
		t.Fatalf("Ordner nicht gesynct: %v", *dirs)
	}
	if b, err := os.ReadFile(final); err != nil || len(b) != 4096 {
		t.Fatalf("Datei fehlt/falsch: %v", err)
	}
}

func TestUploadSyncFailure(t *testing.T) {
	a, h := setup(t)
	stubSync(t, errors.New("E/A-Fehler"))
	m := msgOf(upload(h, "kaputt.bin", 100).Header().Get("Location"))
	if !strings.Contains(m, "E/A-Fehler") {
		t.Fatalf("Sync-Fehler nicht gemeldet: %s", m)
	}
	entries, _ := os.ReadDir(a.FilesDirReal())
	if len(entries) != 0 {
		t.Fatalf("Reste nach fehlgeschlagenem Sync: %v", entries)
	}
}

func TestUpdatePkgSynced(t *testing.T) {
	a, h := setup(t)
	files, dirs := stubSync(t, nil)
	sendPkg(t, h, "vpndongle-1.2.3.tar.gz")
	if len(*files) != 1 || !strings.HasSuffix((*files)[0], ".upload.part") {
		t.Fatalf("Paket nicht gesynct: %v", *files)
	}
	if len(*dirs) != 1 || filepath.Clean((*dirs)[0]) != filepath.Clean(a.UpdateDirReal()) {
		t.Fatalf("Update-Ordner nicht gesynct: %v", *dirs)
	}

	// Sync-Fehler -> kein Paket, kein Update
	a2, h2 := setup(t)
	stubSync(t, errors.New("E/A-Fehler"))
	if m := sendPkg(t, h2, "vpndongle-1.2.3.tar.gz"); !strings.Contains(m, "E/A-Fehler") {
		t.Fatalf("Sync-Fehler beim Paket nicht gemeldet: %s", m)
	}
	if _, err := os.Stat(filepath.Join(a2.UpdateDirReal(), "vpndongle-update.tar.gz")); err == nil {
		t.Fatal("Paket trotz Sync-Fehler abgelegt")
	}
}
