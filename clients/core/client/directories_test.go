package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryManifestEmptyFoldersUnicodeAndChangedSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "Проект 日本語")
	for _, directory := range []string{"empty", "a", "b"} {
		if err := os.MkdirAll(filepath.Join(source, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"a/report.txt", "b/report.txt", "zero.txt"} {
		if err := os.WriteFile(filepath.Join(source, file), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	job, err := PrepareDirectory(source, testFolder)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Directory.Folders) != 3 || len(job.Directory.Files) != 3 || job.Size != 0 {
		t.Fatal(job)
	}
	for _, file := range job.Directory.Files {
		if file.Job.ID == job.ID {
			t.Fatal("files must have independent idempotency keys")
		}
	}
	if err := os.WriteFile(filepath.Join(source, "new.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	client, _ := New("http://127.0.0.1:1", "")
	if err = client.UploadDirectory(context.Background(), &job, func(DirectoryJob) {}); err == nil || !strings.Contains(err.Error(), "изменился") {
		t.Fatal(err)
	}
}
func TestDirectoryRejectsSymlinksAndInvalidNames(t *testing.T) {
	source := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "shortcut")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDirectory(source, testFolder); err == nil || !strings.Contains(err.Error(), "символическую") {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bad\\name"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDirectory(source, testFolder); err == nil {
		t.Fatal("accepted invalid name")
	}
}
