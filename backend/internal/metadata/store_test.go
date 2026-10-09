package metadata

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestVersionOneMigrationPreservesExistingData(t *testing.T) {
	file := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema + "; PRAGMA user_version=1; INSERT INTO settings(key,value) VALUES('existing','retained')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	var value string
	if err = store.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal(version, err)
	}
	if err = store.DB.QueryRow("SELECT value FROM settings WHERE key='existing'").Scan(&value); err != nil || value != "retained" {
		t.Fatal(value, err)
	}
	if _, err = store.DB.Exec("SELECT * FROM folder_requests"); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
}
