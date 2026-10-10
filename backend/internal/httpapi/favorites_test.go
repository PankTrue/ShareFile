package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/security"
)

func TestFavoritesPersonalAccessAndLifecycle(t *testing.T) {
	f := setup(t)
	h := f.a.Public()
	auth := browser(t, f, f.alice)
	bobAuth := browser(t, f, f.bob)
	private, err := f.a.Files.CreateFolder(f.ctx, f.bob.ID, f.bob.HomeID, "Private parent")
	must(t, err)
	shared, err := f.a.Files.CreateFolder(f.ctx, f.bob.ID, private.ID, "Shared")
	must(t, err)
	group, err := f.a.Access.CreateGroup(f.ctx, "Readers")
	must(t, err)
	must(t, f.a.Access.SetMember(f.ctx, group.ID, f.alice.ID, true))
	must(t, f.a.Access.SetGrant(f.ctx, f.bob.ID, shared.ID, access.Grant{PrincipalType: "group", PrincipalID: group.ID, Role: access.Reader}))
	own := putFile(t, f, f.alice, f.alice.HomeID, "Own.txt", "own")
	visible := putFile(t, f, f.bob, shared.ID, "Shared.txt", "shared")
	get := func(view string, headers map[string]string) files.SearchResult {
		t.Helper()
		response := request(t, h, "GET", "/api/v1/search?view="+view, nil, headers)
		status(t, response, 200)
		var result files.SearchResult
		must(t, json.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	status(t, request(t, h, "PUT", "/api/v1/nodes/"+private.ID+"/favorite", nil, auth), 403)
	status(t, request(t, h, "PUT", "/api/v1/nodes/"+security.Token()+"/favorite", nil, auth), 404)
	for _, id := range []string{own.ID, shared.ID, visible.ID} {
		for range 2 { // Retry after a lost response must not create duplicates.
			status(t, request(t, h, "PUT", "/api/v1/nodes/"+id+"/favorite", nil, auth), 204)
		}
	}
	result := get("favorites", auth)
	if len(result.Items) != 3 || len(get("favorites", bobAuth).Items) != 0 || len(get("favorites", browser(t, f, f.admin)).Items) != 0 {
		t.Fatal("favorites must be personal, including for administrators")
	}
	for _, item := range result.Items {
		if !item.Favorite {
			t.Fatal("missing favorite state")
		}
		if item.ID == visible.ID && (len(item.Path) != 1 || item.Path[0].ID != shared.ID) {
			t.Fatalf("private ancestor leaked: %+v", item.Path)
		}
	}
	for _, path := range []string{"/api/v1/nodes/" + own.ID, "/api/v1/folders?parent_id=" + f.alice.HomeID, "/api/v1/search?query=Own"} {
		response := request(t, h, "GET", path, nil, auth)
		status(t, response, 200)
		var value any
		must(t, json.Unmarshal(response.Body.Bytes(), &value))
		var node map[string]any
		switch data := value.(type) {
		case []any:
			node = data[0].(map[string]any)
		case map[string]any:
			if items, ok := data["items"]; ok {
				node = items.([]any)[0].(map[string]any)
			} else {
				node = data
			}
		}
		if node["favorite"] != true {
			t.Fatalf("favorite state missing in %s", path)
		}
	}
	// Rename and move keep the bookmark attached to the node rather than its path.
	destination, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Destination")
	must(t, err)
	must(t, f.a.Files.Move(f.ctx, f.alice.ID, own.ID, destination.ID, "Renamed.txt"))
	if result = get("favorites&query=Renamed", auth); len(result.Items) != 1 || result.Items[0].Path[len(result.Items[0].Path)-1].ID != destination.ID {
		t.Fatal("favorite did not follow rename/move")
	}
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, own.ID))
	if len(get("favorites", auth).Items) != 2 || len(get("recent&query=Renamed", auth).Items) != 0 {
		t.Fatal("trash must not appear in shortcuts")
	}
	_, err = f.a.Files.Restore(f.ctx, f.alice.ID, own.ID, destination.ID)
	must(t, err)
	if len(get("favorites", auth).Items) != 3 {
		t.Fatal("restoring a node must retain its bookmark")
	}
	must(t, f.a.Access.SetMember(f.ctx, group.ID, f.alice.ID, false))
	if len(get("favorites", auth).Items) != 1 || len(get("recent&query=Shared", auth).Items) != 0 {
		t.Fatal("revoked access must be reflected immediately")
	}
	status(t, request(t, h, "PUT", "/api/v1/nodes/"+shared.ID+"/favorite", nil, auth), 403)
	// Cleanup is allowed after losing access, without revealing private metadata.
	for range 2 {
		status(t, request(t, h, "DELETE", "/api/v1/nodes/"+shared.ID+"/favorite", nil, auth), 204)
	}
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, own.ID))
	must(t, f.a.Files.Purge(f.ctx, f.alice.ID, own.ID))
	var remaining int
	must(t, f.a.Store.DB.QueryRow("SELECT count(*) FROM favorites WHERE node_id=?", own.ID).Scan(&remaining))
	if remaining != 0 {
		t.Fatal("purge must remove dangling bookmarks")
	}
	path := "/api/v1/nodes/" + visible.ID + "/favorite"
	status(t, request(t, h, "PUT", path, nil, nil), 401)
	status(t, request(t, h, "DELETE", path, nil, map[string]string{"Cookie": auth["Cookie"]}), 403)
	status(t, request(t, h, "DELETE", path, nil, map[string]string{"Cookie": auth["Cookie"], "X-CSRF-Token": auth["X-CSRF-Token"], "Origin": "https://evil.example"}), 403)
}

func TestShortcutFilteringPaginationAndCompletion(t *testing.T) {
	f := setup(t)
	auth := browser(t, f, f.alice)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Working")
	must(t, err)
	must(t, f.a.Files.SetFavorite(f.ctx, f.alice.ID, folder.ID, true))
	_, err = f.a.Files.CreateUpload(f.ctx, f.alice.ID, folder.ID, "Incomplete.txt", 100, "", "")
	must(t, err)
	complete := putFile(t, f, f.alice, folder.ID, "Complete.txt", "")
	_, err = f.a.Store.DB.Exec("UPDATE nodes SET created_at=500 WHERE id=?", complete.ID)
	must(t, err)
	must(t, f.a.Store.Write(f.ctx, func(tx *sql.Tx) error {
		for i := range 105 {
			id := security.Token()
			if _, err := tx.ExecContext(f.ctx, "INSERT INTO nodes(id,parent_id,owner_id,name,kind,size,created_at) VALUES(?,?,?,?,'file',?,?)", id, folder.ID, f.alice.ID, fmt.Sprintf("Report %03d.PDF", i), i, 1000+i); err != nil {
				return err
			}
			if _, err := tx.ExecContext(f.ctx, "INSERT INTO favorites(user_id,node_id) VALUES(?,?)", f.alice.ID, id); err != nil {
				return err
			}
		}
		// A newer inaccessible file cannot consume a page or leak a name/path.
		_, err := tx.ExecContext(f.ctx, "INSERT INTO nodes(id,parent_id,owner_id,name,kind,size,created_at) VALUES(?,?,?,'Private.txt','file',0,9999)", security.Token(), f.bob.HomeID, f.bob.ID)
		return err
	}))
	get := func(query string) files.SearchResult {
		t.Helper()
		response := request(t, f.a.Public(), "GET", "/api/v1/search?"+query, nil, auth)
		status(t, response, 200)
		var result files.SearchResult
		must(t, json.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	first, last := get("view=recent"), get("view=recent&offset=100")
	if len(first.Items) != 100 || !first.HasMore || first.Items[0].Name != "Report 104.PDF" || len(last.Items) != 6 || last.HasMore || last.Items[5].ID != complete.ID {
		t.Fatalf("incorrect recent ordering/pagination: %+v %+v", first, last)
	}
	for _, item := range append(first.Items, last.Items...) {
		if item.Kind != "file" || item.Name == "Incomplete.txt" {
			t.Fatal("recent contains a folder or incomplete upload")
		}
	}
	first, last = get("view=favorites&type=document"), get("view=favorites&type=document&offset=100")
	if len(first.Items) != 100 || !first.HasMore || first.Items[0].Name != "Report 000.PDF" || len(last.Items) != 5 || last.HasMore {
		t.Fatal("favorites pagination/filtering failed")
	}
	filtered := get("view=recent&type=document&created_after=1010&created_before=1021&min_size=12&max_size=15&limit=2")
	if len(filtered.Items) != 2 || !filtered.HasMore || filtered.Items[0].Size != 15 || filtered.Items[1].Size != 14 {
		t.Fatal("recent must combine all filters before pagination")
	}
	if len(get("view=favorites&type=folder").Items) != 1 || len(get("view=recent&type=folder").Items) != 0 || len(get("view=recent&query=Incomplete").Items) != 0 {
		t.Fatal("shortcut type/completion filtering failed")
	}
	status(t, request(t, f.a.Public(), "GET", "/api/v1/search?view=invalid", nil, auth), 400)
}
