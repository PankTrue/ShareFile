package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sharefile/backend/internal/metadata"
	"testing"
)

func TestMigratePreservesSourceAndCopiesOfflineStorage(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := Initialize(source, "old-volume"); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenDisk(source, "old-volume", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	for path, data := range map[string]string{"objects/file": "file contents", "uploads/part": "partial upload", "metadata/sharefile.db": "offline database", "metadata/process.lock": "lock"} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(parent, "destination")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), disk, target, "new-volume", 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"objects/file", "uploads/part", "metadata/sharefile.db"} {
		old, err := os.ReadFile(filepath.Join(source, path))
		if err != nil {
			t.Fatal(err)
		}
		new, err := os.ReadFile(filepath.Join(target, path))
		if err != nil {
			t.Fatal(err)
		}
		if string(old) != string(new) {
			t.Fatalf("changed contents: %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "metadata/process.lock")); !os.IsNotExist(err) {
		t.Fatal("process lock copied")
	}
	if err := disk.Check(); err != nil {
		t.Fatal("source changed", err)
	}
	copy, err := OpenDisk(target, "new-volume", 0)
	if err != nil {
		t.Fatal(err)
	}
	copy.Close()
}

func TestMigrationCancellationAndReserveLeaveNoDestination(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := Initialize(source, "source"); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenDisk(source, "source", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if err := os.WriteFile(filepath.Join(source, "objects/file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	target := filepath.Join(parent, "cancelled")
	if err := Migrate(ctx, disk, target, "next", 0); !errors.Is(err, context.Canceled) {
		t.Fatal("wrong cancellation error", err)
	}
	target = filepath.Join(parent, "no-space")
	if err := Migrate(context.Background(), disk, target, "next", ^uint64(0)); !errors.Is(err, metadata.ErrSpace) {
		t.Fatal("reserve not enforced", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("failed target was published")
	}
	stages, _ := filepath.Glob(filepath.Join(parent, ".sharefile-migrate-*"))
	if len(stages) > 0 {
		t.Fatal("partial staging directories leaked")
	}
	if err := disk.Check(); err != nil {
		t.Fatal("source changed", err)
	}
}

func TestMigrateRefusesOccupiedDirectoryAndSymlinks(t *testing.T) {
	parent := t.TempDir()
	source, target := filepath.Join(parent, "source"), filepath.Join(parent, "target")
	if err := Initialize(source, "source"); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenDisk(source, "source", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), disk, target, "next", 0); err == nil {
		t.Fatal("occupied destination accepted")
	}
	if err := os.Symlink(filepath.Join(target, "keep"), filepath.Join(source, "objects/link")); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), disk, filepath.Join(parent, "empty"), "next", 0); err == nil {
		t.Fatal("source symlink accepted")
	}
	if data, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(data) != "existing" {
		t.Fatal("existing data changed")
	}
	if _, err := os.Stat(filepath.Join(parent, "empty")); !os.IsNotExist(err) {
		t.Fatal("failed migration published destination")
	}
}
