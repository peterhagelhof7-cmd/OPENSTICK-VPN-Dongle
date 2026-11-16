package web

import (
	"log"
	"os"
)

// Hochgeladene Dateien müssen auf dem Flash sein, BEVOR die Seite „hochgeladen“
// meldet: ohne fsync lagen die letzten Sekunden noch im RAM (Web-Upload 14,7 MB/s
// gegen ~10 MB/s Flash-Schreibrate, gemessen 2026-11-16) – Stick abziehen = Datei kaputt.
//
// Im Test austauschbar.
var (
	fsyncFile = func(f *os.File) error { return f.Sync() }
	fsyncDir  = func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
)

// closeSynced schreibt die Datei auf den Datenträger und schließt sie.
// Ein Fehler beim Sync ist ein Fehler des Uploads.
func closeSynced(f *os.File) error {
	err := fsyncFile(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncDir sichert die Umbenennung (den Verzeichniseintrag). Nur „best effort“:
// die Daten selbst sind schon gesichert, und unter Windows (Dry-Run) lässt sich
// ein Ordner nicht syncen.
func syncDir(dir string) {
	if err := fsyncDir(dir); err != nil {
		log.Printf("fsync Ordner %s: %v", dir, err)
	}
}
