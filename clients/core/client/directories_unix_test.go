//go:build unix

package client

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDirectoryRejectsNamedPipeWithoutOpeningIt(t *testing.T) {
	source := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(source, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDirectory(source, testFolder); err == nil || !strings.Contains(err.Error(), "специальный") {
		t.Fatal(err)
	}
}
