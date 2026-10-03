package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLogStorageStaysBounded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.jsonl")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	entry := bytes.Repeat([]byte("x"), MaxBytes/3)
	for i := 0; i < 20; i++ {
		if _, err = w.Write(entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unbounded archive count: %d", len(entries))
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > MaxBytes {
			t.Fatal("log exceeds budget")
		}
	}
}
