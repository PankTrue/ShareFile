package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sharefile/backend/internal/access"
)

func TestUIViewsAndManagerDirectory(t *testing.T) {
	f := setup(t)
	public, private := f.a.Public(), f.a.Private()
	for _, test := range []struct {
		handler http.Handler
		private bool
	}{{public, false}, {private, true}} {
		result := request(t, test.handler, "GET", "/api/v1/ui", nil, nil)
		var info struct {
			AdminListener bool `json:"admin_listener"`
		}
		must(t, json.Unmarshal(result.Body.Bytes(), &info))
		if result.Code != 200 || info.AdminListener != test.private {
			t.Fatal("incorrect listener capability")
		}
	}
	_, token, csrf, err := f.a.Identity.Login(f.ctx, "alice", testPassword)
	must(t, err)
	auth := map[string]string{"Cookie": "sharefile_dev=" + token, "X-CSRF-Token": csrf}
	result := request(t, public, "GET", "/api/v1/nodes/"+f.alice.HomeID, nil, auth)
	var node nodeView
	must(t, json.Unmarshal(result.Body.Bytes(), &node))
	if node.EffectiveRole != access.Manager {
		t.Fatal("owner should manage home")
	}
	group, err := f.a.Access.CreateGroup(f.ctx, "Readers")
	must(t, err)
	must(t, f.a.Access.SetGrant(f.ctx, f.alice.ID, f.alice.HomeID, access.Grant{PrincipalType: "group", PrincipalID: group.ID, Role: access.Reader}))
	result = request(t, public, "GET", "/api/v1/nodes/"+f.alice.HomeID+"/principals?query=Readers&limit=1", nil, auth)
	var principals []principal
	must(t, json.Unmarshal(result.Body.Bytes(), &principals))
	if result.Code != 200 || len(principals) != 1 || principals[0].ID != group.ID {
		t.Fatal("directory search failed")
	}
	result = request(t, public, "GET", "/api/v1/nodes/"+f.alice.HomeID+"/grants", nil, auth)
	var grants []struct {
		PrincipalName string `json:"principal_name"`
	}
	must(t, json.Unmarshal(result.Body.Bytes(), &grants))
	if len(grants) != 1 || grants[0].PrincipalName != group.Name {
		t.Fatal("grant name missing")
	}
	must(t, f.a.Access.SetGrant(f.ctx, f.admin.ID, f.bob.HomeID, access.Grant{PrincipalType: "user", PrincipalID: f.alice.ID, Role: access.Reader}))
	if request(t, public, "GET", "/api/v1/nodes/"+f.bob.HomeID+"/principals", nil, auth).Code != 403 {
		t.Fatal("reader can search directory")
	}
	result = request(t, public, "GET", "/api/v1/folders?parent_id="+f.bob.HomeID, nil, auth)
	var nodes []nodeView
	must(t, json.Unmarshal(result.Body.Bytes(), &nodes))
	if len(nodes) != 1 || nodes[0].EffectiveRole != access.Reader {
		t.Fatal("inherited role missing")
	}
	if request(t, public, "GET", "/api/v1/admin/users", nil, auth).Code != 404 {
		t.Fatal("public admin route leaked")
	}
	file, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Folder")
	must(t, err)
	_, err = f.a.Store.DB.ExecContext(f.ctx, "UPDATE nodes SET kind='file' WHERE id=?", file.ID)
	must(t, err)
	if request(t, public, "GET", "/api/v1/nodes/"+file.ID+"/principals", nil, auth).Code != 400 {
		t.Fatal("file directory should be rejected")
	}
}

func TestStaticUIAndAPIRoutesCoexist(t *testing.T) {
	f := setup(t)
	f.a.Config.WebDir = t.TempDir()
	must(t, os.WriteFile(filepath.Join(f.a.Config.WebDir, "index.html"), []byte("<html>React UI</html>"), 0600))
	for _, handler := range []http.Handler{f.a.Public(), f.a.Private()} {
		for _, path := range []string{"/", "/device/connect?request=test"} {
			response := request(t, handler, "GET", path, nil, nil)
			if response.Code != 200 || !strings.Contains(response.Body.String(), "React UI") {
				t.Fatal("SPA fallback failed")
			}
			if strings.Contains(response.Header().Get("Content-Security-Policy"), "unsafe-inline") {
				t.Fatal("CSP weakened")
			}
		}
		if request(t, handler, "GET", "/api/v1/unknown", nil, nil).Code != 404 {
			t.Fatal("unknown API must not return SPA")
		}
		if request(t, handler, "GET", "/assets/missing.js", nil, nil).Code != 404 {
			t.Fatal("missing asset must not return SPA")
		}
		if request(t, handler, "POST", "/", nil, nil).Code != 405 {
			t.Fatal("static methods must be restricted")
		}
		if request(t, handler, "POST", "/api/v1/uploads", nil, nil).Code != 401 {
			t.Fatal("Tus route missing")
		}
	}
}
