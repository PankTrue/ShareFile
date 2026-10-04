package maintenance

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"sharefile/backend/internal/files"
)

func TestRestoreRejectsTraversalAndLinks(t *testing.T) {
	for _, test := range []struct {
		name string
		kind byte
	}{{"../escape", tar.TypeReg}, {"objects/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", tar.TypeSymlink}} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			archive := filepath.Join(parent, "unsafe.tar")
			f, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			w := tar.NewWriter(f)
			if err = w.WriteHeader(&tar.Header{Name: test.name, Typeflag: test.kind, Mode: 0600, Linkname: "../../escape"}); err != nil {
				t.Fatal(err)
			}
			w.Close()
			f.Close()
			if err = Restore(context.Background(), filepath.Join(parent, "restore"), "test", archive); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err = os.Stat(filepath.Join(parent, "escape")); !os.IsNotExist(err) {
				t.Fatal("escaped restore root")
			}
		})
	}
}

func TestBackupRejectsSymlinkIntoStorage(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "storage")
	if err := files.Initialize(dir, "test"); err != nil {
		t.Fatal(err)
	}
	disk, err := files.OpenDisk(dir, "test", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	alias := filepath.Join(parent, "outside-looking-alias")
	if err = os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if err = Backup(context.Background(), disk, filepath.Join(alias, "backup.tar")); err == nil {
		t.Fatal("backup inside storage through a symlink was accepted")
	}
	if _, err = os.Stat(filepath.Join(dir, "backup.tar")); !os.IsNotExist(err) {
		t.Fatal("unsafe backup created a file")
	}
}
