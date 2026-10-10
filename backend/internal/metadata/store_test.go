package metadata

import (
	"context"
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
	if err = store.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatal(version, err)
	}
	if err = store.DB.QueryRow("SELECT value FROM settings WHERE key='existing'").Scan(&value); err != nil || value != "retained" {
		t.Fatal(value, err)
	}
	if _, err = store.DB.Exec("SELECT * FROM folder_requests"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec("SELECT * FROM favorites"); err != nil {
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

func TestVersionTwoMigrationAndFavoritesPersist(t *testing.T) {
	file := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(schema + `;
	 CREATE TABLE folder_requests (
	 user_id TEXT NOT NULL REFERENCES users(id), request_key TEXT NOT NULL,
	 parent_id TEXT NOT NULL, name TEXT NOT NULL,
	 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
	 PRIMARY KEY(user_id,request_key));
	 INSERT INTO users(id,username,password,home_id,created_at) VALUES('user','alice','unused','root',1);
	 INSERT INTO nodes(id,owner_id,name,kind,created_at) VALUES('root','user','Home','folder',1);
	 INSERT INTO folder_requests(user_id,request_key,parent_id,name,node_id) VALUES('user','key','','Home','root');
	 PRAGMA user_version=2`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO favorites(user_id,node_id) VALUES('user','root')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count, version int
	if err = store.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatal(version, err)
	}
	if err = store.DB.QueryRow("SELECT count(*) FROM favorites f JOIN folder_requests r ON r.node_id=f.node_id WHERE f.user_id='user'").Scan(&count); err != nil || count != 1 {
		t.Fatal("migration/reopen lost existing metadata or favorites", count, err)
	}
}
