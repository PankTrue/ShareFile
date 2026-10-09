package httpapi

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"sharefile/backend/internal/config"
	"sharefile/backend/internal/settings"
)

func TestConfigurationPrivateAdminAndCSRFBoundaries(t *testing.T) {
	f := setup(t)
	c := f.a.Config
	c.PublicAddr, c.AdminAddr = "127.0.0.1:32080", "127.0.0.1:32081"
	c.PublicURL, c.AdminURL = "http://127.0.0.1:32080", "http://127.0.0.1:32081"
	path := filepath.Join(t.TempDir(), "config.json")
	must(t, config.Save(path, c))
	m, err := settings.New(path, c)
	must(t, err)
	f.a.Settings = m
	private, public := f.a.Private(), f.a.Public()
	admin, alice := browser(t, f, f.admin), browser(t, f, f.alice)
	for _, route := range []string{"/api/v1/admin/config", "/api/v1/admin/volumes"} {
		status(t, request(t, public, "GET", route, nil, admin), 404)
		status(t, request(t, private, "GET", route, nil, nil), 401)
		status(t, request(t, private, "GET", route, nil, alice), 403)
		status(t, request(t, private, "GET", route, nil, admin), 200)
	}
	device, err := f.a.Identity.StartDevice(f.ctx, "admin device")
	must(t, err)
	must(t, f.a.Identity.ApproveDevice(f.ctx, f.admin.ID, device.ID))
	token, err := f.a.Identity.PollDevice(f.ctx, device.ID, device.Secret)
	must(t, err)
	status(t, request(t, private, "GET", "/api/v1/admin/config", nil, map[string]string{"Authorization": "Bearer " + token}), 403)
	next := c
	next.ChunkBytes = 128
	body := map[string]any{"config": next, "revision": m.Snapshot().Revision}
	status(t, request(t, private, "PUT", "/api/v1/admin/config", body, map[string]string{"Cookie": admin["Cookie"]}), 403)
	badOrigin := map[string]string{"Cookie": admin["Cookie"], "X-CSRF-Token": admin["X-CSRF-Token"], "Origin": "https://other.example"}
	status(t, request(t, private, "PUT", "/api/v1/admin/config", body, badOrigin), 403)
	result := request(t, private, "PUT", "/api/v1/admin/config", body, admin)
	status(t, result, 200)
	var saved settings.Snapshot
	must(t, json.Unmarshal(result.Body.Bytes(), &saved))
	if saved.Pending == nil || saved.Active.ChunkBytes != 64 || saved.Pending.ChunkBytes != 128 {
		t.Fatal("draft changed active settings")
	}
	status(t, request(t, private, "PUT", "/api/v1/admin/config", body, admin), 409)
	status(t, request(t, private, "POST", "/api/v1/admin/config/apply", map[string]string{"revision": saved.Revision}, admin), 202)
	select {
	case <-m.Requests():
	default:
		t.Fatal("reload was not requested")
	}
}
