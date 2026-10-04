package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DataDir       string   `json:"data_dir"`
	VolumeID      string   `json:"volume_id"`
	PublicAddr    string   `json:"public_addr"`
	PublicURL     string   `json:"public_url"`
	AdminAddr     string   `json:"admin_addr"`
	AdminURL      string   `json:"admin_url"`
	AdminCert     string   `json:"admin_cert"`
	AdminKey      string   `json:"admin_key"`
	WebDir        string   `json:"web_dir"`
	ImportDirs    []string `json:"import_dirs"`
	ReserveBytes  uint64   `json:"reserve_bytes"`
	UploadSlots   int      `json:"upload_slots"`
	DownloadSlots int      `json:"download_slots"`
	ChunkBytes    int64    `json:"chunk_bytes"`
	LogFile       string   `json:"log_file"`
	InsecureLocal bool     `json:"insecure_local"`
}

func Load(path string) (Config, error) {
	c := Config{PublicAddr: "127.0.0.1:8080", AdminAddr: "127.0.0.1:8081", ReserveBytes: 64 << 20, UploadSlots: 2, DownloadSlots: 4, ChunkBytes: 16 << 20}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return c, err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("configuration must contain one JSON object")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if !filepath.IsAbs(c.DataDir) || filepath.Clean(c.DataDir) == string(filepath.Separator) || c.VolumeID == "" || len(c.VolumeID) > 128 || strings.ContainsAny(c.VolumeID, "\r\n") {
		return fmt.Errorf("absolute data_dir and volume_id are required")
	}
	if c.PublicAddr == c.AdminAddr {
		return fmt.Errorf("listeners must have different addresses")
	}
	if c.UploadSlots < 1 || c.DownloadSlots < 1 || c.ChunkBytes < 1 {
		return fmt.Errorf("transfer limits must be positive")
	}
	for _, raw := range []string{c.PublicURL, c.AdminURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("origin URL required")
		}
		if u.Scheme != "https" && !(c.InsecureLocal && u.Scheme == "http" && isLoopback(u.Host)) {
			return fmt.Errorf("HTTPS required outside loopback development")
		}
	}
	for _, addr := range []string{c.PublicAddr, c.AdminAddr} {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())) {
			return fmt.Errorf("listeners must bind an explicit loopback or private LAN/VPN address")
		}
	}
	if c.InsecureLocal {
		if !isLoopback(c.PublicAddr) || !isLoopback(c.AdminAddr) {
			return fmt.Errorf("insecure_local requires loopback listeners")
		}
	} else if c.AdminCert == "" || c.AdminKey == "" {
		return fmt.Errorf("admin TLS certificate and key required")
	}
	for _, dir := range c.ImportDirs {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("absolute import directories required")
		}
	}
	for _, path := range []string{c.LogFile, c.WebDir, c.AdminCert, c.AdminKey} {
		if path != "" && !filepath.IsAbs(path) {
			return fmt.Errorf("absolute log, web and TLS paths required")
		}
	}
	return nil
}

func isLoopback(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
