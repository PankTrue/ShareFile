package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
)

func TestSearchAccessAndPaths(t *testing.T) {
	f := setup(t)
	h := f.a.Public()
	auth := browser(t, f, f.alice)
	hidden, err := f.a.Files.CreateFolder(f.ctx, f.bob.ID, f.bob.HomeID, "Private parent")
	must(t, err)
	shared, err := f.a.Files.CreateFolder(f.ctx, f.bob.ID, hidden.ID, "Shared folder")
	must(t, err)
	nested, err := f.a.Files.CreateFolder(f.ctx, f.bob.ID, shared.ID, "Nested")
	must(t, err)
	group, err := f.a.Access.CreateGroup(f.ctx, "Readers")
	must(t, err)
	must(t, f.a.Access.SetMember(f.ctx, group.ID, f.alice.ID, true))
	must(t, f.a.Access.SetGrant(f.ctx, f.bob.ID, shared.ID, access.Grant{PrincipalType: "group", PrincipalID: group.ID, Role: access.Reader}))
	must(t, f.a.Access.SetGrant(f.ctx, f.bob.ID, nested.ID, access.Grant{PrincipalType: "user", PrincipalID: f.alice.ID, Role: access.Editor}))
	// Overlapping grants must yield one result with the maximum inherited role.
	must(t, f.a.Access.SetGrant(f.ctx, f.bob.ID, nested.ID, access.Grant{PrincipalType: "group", PrincipalID: group.ID, Role: access.Reader}))
	own := putFile(t, f, f.alice, f.alice.HomeID, "ОТЧЁТ %_ Личный.txt", "own")
	visible := putFile(t, f, f.bob, nested.ID, "Отчёт %_ Общий.PDF", "shared")
	private := putFile(t, f, f.bob, hidden.ID, "Отчёт %_ Секрет.txt", "secret")
	deleted := putFile(t, f, f.alice, f.alice.HomeID, "Отчёт %_ Удалённый.txt", "trash")
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, deleted.ID))
	get := func(query string, headers map[string]string) files.SearchResult {
		t.Helper()
		response := request(t, h, "GET", "/api/v1/search?"+query, nil, headers)
		status(t, response, 200)
		var result files.SearchResult
		must(t, json.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	query := "query=" + url.QueryEscape("  отчёт %_  ")
	result := get(query, auth)
	if len(result.Items) != 2 || result.HasMore {
		t.Fatalf("expected own and shared results, got %+v", result)
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		seen[item.ID] = true
		role, err := access.Role(f.ctx, f.a.Store.DB, f.alice.ID, item.ID)
		must(t, err)
		if role != item.EffectiveRole {
			t.Fatalf("search role %d != access.Role %d", item.EffectiveRole, role)
		}
		if item.ID == visible.ID {
			if len(item.Path) != 2 || item.Path[0].ID != shared.ID || item.Path[1].ID != nested.ID {
				t.Fatalf("path leaked a private ancestor or missed an accessible one: %+v", item.Path)
			}
		}
	}
	if !seen[own.ID] || !seen[visible.ID] || seen[private.ID] || seen[deleted.ID] {
		t.Fatal("search access boundary failed")
	}
	all := get("", auth)
	for _, item := range all.Items {
		role, err := access.Role(f.ctx, f.a.Store.DB, f.alice.ID, item.ID)
		must(t, err)
		if role < access.Reader || role != item.EffectiveRole {
			t.Fatalf("incorrect accessible item: %+v", item)
		}
	}
	admin := get(query, browser(t, f, f.admin))
	if len(admin.Items) != 3 {
		t.Fatal("administrator must find all live matching files")
	}
	for _, item := range admin.Items {
		if item.EffectiveRole != access.Manager {
			t.Fatal("administrator role missing")
		}
	}
	// Membership removal removes inherited access immediately; direct access remains.
	must(t, f.a.Access.SetMember(f.ctx, group.ID, f.alice.ID, false))
	result = get(query, auth)
	if len(result.Items) != 2 {
		t.Fatal("direct grant should survive membership removal")
	}
	for _, item := range result.Items {
		if item.ID == visible.ID && (len(item.Path) != 1 || item.Path[0].ID != nested.ID) {
			t.Fatal("path leaked a revoked group ancestor")
		}
	}
	must(t, f.a.Access.SetGrant(f.ctx, f.bob.ID, nested.ID, access.Grant{PrincipalType: "user", PrincipalID: f.alice.ID, Role: 0}))
	result = get(query, auth)
	if len(result.Items) != 1 || result.Items[0].ID != own.ID {
		t.Fatal("revoked folder still searchable")
	}
	status(t, request(t, h, "GET", "/api/v1/search", nil, nil), 401)
	_, err = f.a.Store.DB.ExecContext(f.ctx, "UPDATE users SET active=0 WHERE id=?", f.alice.ID)
	must(t, err)
	status(t, request(t, h, "GET", "/api/v1/search", nil, auth), 401)
}

func TestSearchFiltersAndPagination(t *testing.T) {
	f := setup(t)
	auth := browser(t, f, f.alice)
	other, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Elsewhere")
	must(t, err)
	// Enough candidates to cross both folder-list and search pagination boundaries.
	must(t, f.a.Store.Write(f.ctx, func(tx *sql.Tx) error {
		for i := 0; i < 105; i++ {
			parent := f.alice.HomeID
			if i%2 == 0 {
				parent = other.ID
			}
			_, err := tx.ExecContext(f.ctx, "INSERT INTO nodes(id,parent_id,owner_id,name,kind,size,created_at) VALUES(?,?,?,?,'file',?,?)", security.Token(), parent, f.alice.ID, fmt.Sprintf("Отчёт %03d.PDF", i), i, 1000+i)
			if err != nil {
				return err
			}
		}
		for _, name := range []string{"photo.JPG", "music.mp3", "movie.mp4", "bundle.tar.gz", "data.bin"} {
			_, err := tx.ExecContext(f.ctx, "INSERT INTO nodes(id,parent_id,owner_id,name,kind,size,created_at) VALUES(?,?,?,?,'file',0,2000)", security.Token(), other.ID, f.alice.ID, name)
			if err != nil {
				return err
			}
		}
		return nil
	}))
	get := func(query string) files.SearchResult {
		t.Helper()
		response := request(t, f.a.Public(), "GET", "/api/v1/search?"+query, nil, auth)
		status(t, response, 200)
		var result files.SearchResult
		must(t, json.Unmarshal(response.Body.Bytes(), &result))
		return result
	}
	query := "query=" + url.QueryEscape("ОТЧЁТ")
	first, second := get(query), get(query+"&offset=100")
	if len(first.Items) != 100 || !first.HasMore || len(second.Items) != 5 || second.HasMore || second.Items[0].Name != "Отчёт 100.PDF" {
		t.Fatalf("incorrect filtered pagination: %d %t %d %t", len(first.Items), first.HasMore, len(second.Items), second.HasMore)
	}
	if len(get(query+"&offset=105").Items) != 0 {
		t.Fatal("past-end page should be empty")
	}
	filtered := get(query + "&type=document&created_after=1010&created_before=1021&min_size=12&max_size=15&limit=2")
	if len(filtered.Items) != 2 || !filtered.HasMore || filtered.Items[0].Size != 12 || filtered.Items[1].Size != 13 {
		t.Fatalf("combined filters failed: %+v", filtered)
	}
	last := get(query + "&type=document&created_after=1010&created_before=1021&min_size=12&max_size=15&limit=2&offset=2")
	if len(last.Items) != 2 || last.HasMore || last.Items[1].Size != 15 {
		t.Fatal("exactly full final page must not indicate more results")
	}
	for kind, name := range map[string]string{"image": "photo.JPG", "audio": "music.mp3", "video": "movie.mp4", "archive": "bundle.tar.gz", "other": "data.bin"} {
		result := get("type=" + kind + "&max_size=0")
		if len(result.Items) != 1 || result.Items[0].Name != name {
			t.Fatalf("type %s failed: %+v", kind, result)
		}
	}
	for _, item := range get("type=file").Items {
		if item.Kind != "file" {
			t.Fatal("file filter returned folder")
		}
	}
	if result := get("type=folder"); len(result.Items) != 3 || result.HasMore {
		t.Fatalf("folder filter failed: %+v", result)
	}
	if len(get("type=folder&min_size=0").Items) != 0 {
		t.Fatal("size filter should exclude folders")
	}
	if len(get(query+"&created_after=1104&created_before=1105").Items) != 1 || len(get(query+"&created_before=1000").Items) != 0 {
		t.Fatal("date boundary must be inclusive after, exclusive before")
	}
	for _, invalid := range []string{
		"type=invalid", "min_size=-1", "max_size=no", "max_size=9223372036854775808", "min_size=10&max_size=9",
		"created_after=-1", "created_before=1.5", "created_after=10&created_before=10", "created_after=10&created_before=9",
		"limit=0", "limit=101", "offset=-1", "offset=abc", "query=" + strings.Repeat("a", 241), "query=%FF",
	} {
		status(t, request(t, f.a.Public(), "GET", "/api/v1/search?"+invalid, nil, auth), 400)
	}
	// Validation is also enforced by the service, independently of HTTP parsing.
	_, err = f.a.Files.Search(f.ctx, f.alice.ID, files.SearchOptions{Limit: 101})
	if err != metadata.ErrInvalid {
		t.Fatal("service validation missing")
	}
}
