package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInsecureConfigurationIsLoopbackOnly(t *testing.T) {
	c := Config{DataDir: "/tmp/sharefile", VolumeID: "test", PublicAddr: "127.0.0.1:8080", AdminAddr: "127.0.0.1:8081", PublicURL: "http://127.0.0.1:8080", AdminURL: "http://127.0.0.1:8081", UploadSlots: 2, DownloadSlots: 4, ChunkBytes: 1024, InsecureLocal: true}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.AdminAddr = "0.0.0.0:8081"
	if err := c.Validate(); err == nil {
		t.Fatal("insecure LAN listener allowed")
	}
	c.AdminAddr = "127.0.0.1:8081"
	c.PublicURL = "http://files.example"
	if err := c.Validate(); err == nil {
		t.Fatal("insecure public origin allowed")
	}
}

func TestRejectsUnsafeBindingsAndTrailingConfiguration(t *testing.T) {
	c := Config{DataDir: "/tmp/store", VolumeID: "test", PublicAddr: "127.0.0.1:8080", PublicURL: "https://files.example", AdminAddr: "192.168.1.1:8081", AdminURL: "https://192.168.1.1:8081", AdminCert: "/tmp/cert.pem", AdminKey: "/tmp/key.pem", UploadSlots: 2, DownloadSlots: 4, ChunkBytes: 1024}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"0.0.0.0:8081", ":8081", "8.8.8.8:8081"} {
		c.AdminAddr = addr
		if err := c.Validate(); err == nil {
			t.Fatalf("unsafe listener %s accepted", addr)
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{} {}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("multiple configuration objects accepted")
	}
}
