package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitializeRefusesExistingUnmanagedData(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(original, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(dir, "test"); err == nil {
		t.Fatal("adopted a nonempty HDD directory")
	}
	data, err := os.ReadFile(original)
	if err != nil || string(data) != "keep" {
		t.Fatal("existing data changed")
	}
	if _, err = os.Stat(filepath.Join(dir, ".sharefile-volume")); !os.IsNotExist(err) {
		t.Fatal("marker created for unmanaged data")
	}
}
