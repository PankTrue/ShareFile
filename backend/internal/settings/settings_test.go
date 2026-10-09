package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"sharefile/backend/internal/config"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/metadata"
)

func testManager(t *testing.T) (*Manager, config.Config, string) {
	t.Helper()
	parent := t.TempDir()
	c := config.Config{DataDir: filepath.Join(parent, "data"), VolumeID: "test", PublicAddr: "127.0.0.1:32180", AdminAddr: "127.0.0.1:32181", PublicURL: "http://127.0.0.1:32180", AdminURL: "http://127.0.0.1:32181", InsecureLocal: true, UploadSlots: 2, DownloadSlots: 4, ChunkBytes: 1024}
	if err := files.Initialize(c.DataDir, c.VolumeID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	m, err := New(path, c)
	if err != nil {
		t.Fatal(err)
	}
	return m, c, path
}

func TestDraftPersistenceConflictsAndDiscard(t *testing.T) {
	m, c, path := testManager(t)
	revision := m.Snapshot().Revision
	next := c
	next.UploadSlots = 3
	saved, err := m.Save(next, revision)
	if err != nil {
		t.Fatal(err)
	}
	active, err := config.Load(path)
	if err != nil || active.UploadSlots != 2 {
		t.Fatal("draft changed live config")
	}
	if saved.Pending == nil || saved.Pending.UploadSlots != 3 {
		t.Fatal("missing draft")
	}
	if _, err := m.Save(c, revision); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	restarted, err := New(path, c)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Snapshot().Pending == nil {
		t.Fatal("draft lost on restart")
	}
	if err := restarted.Discard(saved.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".pending"); !os.IsNotExist(err) {
		t.Fatal("draft still exists")
	}
}

func TestRejectsUnsafeStorageAndDoesNotOverwriteManualConfig(t *testing.T) {
	m, c, path := testManager(t)
	for _, target := range []string{filepath.Dir(c.DataDir), filepath.Join(c.DataDir, "nested"), c.DataDir + "/."} {
		next := c
		next.DataDir = target
		if _, err := m.Save(next, m.Snapshot().Revision); err == nil {
			t.Fatalf("unsafe target allowed %s", target)
		}
	}
	next := c
	next.VolumeID = "new"
	if _, err := m.Save(next, m.Snapshot().Revision); err == nil {
		t.Fatal("in-place identity change allowed")
	}
	c.DownloadSlots = 5
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(next, m.Snapshot().Revision); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("manual edits overwritten", err)
	}
}

func TestOfflineApplyPersistsAndRetainsOriginal(t *testing.T) {
	m, c, path := testManager(t)
	disk, err := files.OpenDisk(c.DataDir, c.VolumeID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if err := os.WriteFile(filepath.Join(c.DataDir, "objects/file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	next := c
	next.DataDir = filepath.Join(filepath.Dir(c.DataDir), "next")
	next.VolumeID = "next"
	saved, err := m.Save(next, m.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RequestApply(saved.Revision); err != nil {
		t.Fatal(err)
	}
	<-m.Requests()
	actual, err := m.Commit(context.Background(), disk)
	if err != nil {
		t.Fatal(err)
	}
	if actual.DataDir != next.DataDir {
		t.Fatal("wrong destination")
	}
	if err := m.Activated(actual); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil || loaded.VolumeID != "next" {
		t.Fatal("config not persisted", err)
	}
	if err := disk.Check(); err != nil {
		t.Fatal("source lost", err)
	}
}

func TestMountsDecodePathsAndIgnoreVirtualFilesystems(t *testing.T) {
	v := parseMounts("proc /proc proc rw 0 0\n/dev/sda /tmp/mnt/My\\040Disk ext4 rw 0 0\n/dev/sdb /opt ext4 ro 0 0\n")
	if len(v) != 2 || v[0].Path != "/tmp/mnt/My Disk" || !v[1].ReadOnly {
		t.Fatalf("incorrect mounts: %+v", v)
	}
}
