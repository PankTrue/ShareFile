package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sharefile/backend/internal/config"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/httpapi"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/settings"
)

func TestTLSListenerAndGracefulSettingsReload(t *testing.T) {
	parent := t.TempDir()
	pubKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pubKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(parent, "cert.pem"), filepath.Join(parent, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	address := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		return addr
	}
	c := config.Config{DataDir: filepath.Join(parent, "data"), VolumeID: "tls-test", PublicAddr: address(), AdminAddr: address(), PublicURL: "https://files.example", AdminCert: certPath, AdminKey: keyPath, UploadSlots: 2, DownloadSlots: 4, ChunkBytes: 1024}
	c.AdminURL = "https://" + c.AdminAddr
	if err := files.Initialize(c.DataDir, c.VolumeID); err != nil {
		t.Fatal(err)
	}
	disk, err := files.OpenDisk(c.DataDir, c.VolumeID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	store, err := metadata.Open(filepath.Join(c.DataDir, "metadata/sharefile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app, err := httpapi.New(c, store, disk)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "config.json")
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	manager, err := settings.New(path, c)
	if err != nil {
		t.Fatal(err)
	}
	app.Settings = manager
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, app) }()
	// Trust the actual disposable self-signed certificate, rather than bypassing TLS.
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for {
		response, err := client.Get(c.AdminURL + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatal(response.Status)
			}
			break
		}
		select {
		case err := <-done:
			t.Fatal("server failed", err)
		case <-ctx.Done():
			t.Fatal("TLS listener did not start", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	next := c
	next.UploadSlots = 3
	saved, err := manager.Save(next, manager.Snapshot().Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RequestApply(saved.Revision); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errReload) {
			t.Fatal("server did not drain for reload", err)
		}
	case <-ctx.Done():
		t.Fatal("reload did not finish")
	}
}
