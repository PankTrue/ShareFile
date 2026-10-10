package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/metadata"
)

func batchInput(action string, ids []string, parent, name string) map[string]any {
	return map[string]any{"action": action, "node_ids": ids, "parent_id": parent, "name": name}
}

func TestBatchMoveAndTrash(t *testing.T) {
	f := setup(t)
	one := putFile(t, f, f.alice, f.alice.HomeID, "один.txt", "one")
	two := putFile(t, f, f.alice, f.alice.HomeID, "два.txt", "two")
	target, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Назначение")
	must(t, err)
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, one.ID, "", nil)
	must(t, err)
	ids := []string{one.ID, two.ID}
	headers := browser(t, f, f.alice)
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("move", ids, target.ID, ""), headers), 204)
	for _, id := range ids {
		n, err := f.a.Files.Node(f.ctx, f.alice.ID, id)
		must(t, err)
		if n.ParentID != target.ID {
			t.Fatal("selection not moved")
		}
	}
	status(t, request(t, f.a.Public(), "GET", "/s/"+token, nil, nil), 200)
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("delete", ids, "", ""), headers), 204)
	status(t, request(t, f.a.Public(), "GET", "/s/"+token, nil, nil), 404)
	for _, id := range ids {
		n, err := metadata.GetNode(f.ctx, f.a.Store.DB, id)
		must(t, err)
		if n.DeletedAt == nil || n.TrashRoot != id {
			t.Fatal("selection not in trash")
		}
		_, err = f.a.Files.Restore(f.ctx, f.alice.ID, id, "")
		must(t, err)
	}
	status(t, request(t, f.a.Public(), "GET", "/s/"+token, nil, nil), 404)
}

func TestBatchRollsBackWholeSelection(t *testing.T) {
	f := setup(t)
	one := putFile(t, f, f.alice, f.alice.HomeID, "one.txt", "one")
	two := putFile(t, f, f.alice, f.alice.HomeID, "two.txt", "two")
	foreign := putFile(t, f, f.bob, f.bob.HomeID, "private.txt", "private")
	target, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "target")
	must(t, err)
	putFile(t, f, f.alice, target.ID, two.Name, "existing")
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, one.ID, "", nil)
	must(t, err)
	headers := browser(t, f, f.alice)
	tests := []struct {
		action string
		ids    []string
		parent string
		want   int
	}{
		{"move", []string{one.ID, two.ID}, target.ID, 409},
		{"move", []string{one.ID, foreign.ID}, target.ID, 403},
		{"delete", []string{one.ID, foreign.ID}, "", 403},
		{"delete", []string{one.ID, f.alice.HomeID}, "", 409},
		{"move", []string{one.ID, "missing"}, target.ID, 404},
		{"collect", []string{one.ID, target.ID}, target.ID, 409},
	}
	for _, test := range tests {
		status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput(test.action, test.ids, test.parent, "For sending"), headers), test.want)
		n, err := f.a.Files.Node(f.ctx, f.alice.ID, one.ID)
		must(t, err)
		if n.ParentID != f.alice.HomeID || n.DeletedAt != nil {
			t.Fatal("failed batch partially changed selection")
		}
		status(t, request(t, f.a.Public(), "GET", "/s/"+token, nil, nil), 200)
	}
	children, err := f.a.Files.List(f.ctx, f.alice.ID, target.ID, 100, 0)
	must(t, err)
	if len(children) != 1 || children[0].Name != two.Name {
		t.Fatal("failed collect left a folder or moved files")
	}
}

func TestCollectDuplicateNamesAndShare(t *testing.T) {
	f := setup(t)
	source, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "source")
	must(t, err)
	one := putFile(t, f, f.alice, f.alice.HomeID, "отчёт.txt", "first")
	two := putFile(t, f, f.alice, source.ID, "отчёт.txt", "second")
	_, err = f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Для отправки")
	must(t, err)
	headers := browser(t, f, f.alice)
	result := request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("collect", []string{one.ID, source.ID, two.ID}, f.alice.HomeID, "Для отправки"), headers)
	status(t, result, 201)
	var folder nodeView
	must(t, json.NewDecoder(result.Body).Decode(&folder))
	if folder.Name != "Для отправки (1)" || folder.EffectiveRole != access.Manager {
		t.Fatalf("bad collected folder: %+v", folder)
	}
	// The selected child stays inside its selected parent.
	live, err := f.a.Files.Node(f.ctx, f.alice.ID, two.ID)
	must(t, err)
	if live.ParentID != source.ID {
		t.Fatal("collect flattened overlapping selection")
	}
	// Collect from two different folders resolves duplicate basenames.
	result = request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("collect", []string{one.ID, two.ID}, f.alice.HomeID, "Пакет"), headers)
	status(t, result, 201)
	must(t, json.NewDecoder(result.Body).Decode(&folder))
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, folder.ID, "", nil)
	must(t, err)
	status(t, request(t, f.a.Public(), "GET", "/s/"+token, nil, nil), 200)
	zip := request(t, f.a.Public(), "GET", "/s/"+token+"/archive", nil, nil)
	status(t, zip, 200)
	want := map[string]string{"Пакет/": "", "Пакет/отчёт.txt": "first", "Пакет/отчёт (1).txt": "second"}
	if got := readArchive(t, zip.Body.Bytes()); !reflect.DeepEqual(got, want) {
		t.Fatalf("collected archive: %v", got)
	}
}

func TestBatchOverlappingMoveAndDelete(t *testing.T) {
	f := setup(t)
	source, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "source")
	must(t, err)
	file := putFile(t, f, f.alice, source.ID, "child.txt", "child")
	target, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "target")
	must(t, err)
	headers := browser(t, f, f.alice)
	ids := []string{file.ID, source.ID}
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("move", ids, target.ID, ""), headers), 204)
	n, err := f.a.Files.Node(f.ctx, f.alice.ID, file.ID)
	must(t, err)
	if n.ParentID != source.ID {
		t.Fatal("overlapping move flattened tree")
	}
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("delete", ids, "", ""), headers), 204)
	n, err = metadata.GetNode(f.ctx, f.a.Store.DB, file.ID)
	must(t, err)
	if n.TrashRoot != source.ID {
		t.Fatal("overlapping delete split trash tree")
	}
	_, err = f.a.Files.Restore(f.ctx, f.alice.ID, source.ID, "")
	must(t, err)
	_, err = f.a.Files.Node(f.ctx, f.alice.ID, file.ID)
	must(t, err)
}

func TestSelectionValidationAndReaderAccess(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "read only")
	must(t, err)
	file := putFile(t, f, f.alice, folder.ID, "visible.txt", "readable")
	must(t, f.a.Access.SetGrant(f.ctx, f.alice.ID, folder.ID, access.Grant{PrincipalType: "user", PrincipalID: f.bob.ID, Role: access.Reader}))
	alice, bob := browser(t, f, f.alice), browser(t, f, f.bob)
	for _, action := range []string{"move", "delete", "collect"} {
		status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput(action, []string{file.ID}, f.bob.HomeID, "send"), bob), 403)
	}
	status(t, request(t, f.a.Public(), "GET", "/api/v1/nodes/archive?id="+file.ID, nil, bob), 200)
	for _, ids := range [][]string{nil, {}, {file.ID, file.ID}, {""}, {strings.Repeat("a", 129)}, make([]string, 101)} {
		status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("delete", ids, "", ""), alice), 400)
	}
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("unknown", []string{file.ID}, "", ""), alice), 400)
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("collect", []string{file.ID}, folder.ID, "../bad"), alice), 400)
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("collect", []string{file.ID}, folder.ID, "send"), bob), 403)
	// Editor access permits ordinary moves, but does not permit sharing a new folder.
	must(t, f.a.Access.SetGrant(f.ctx, f.alice.ID, folder.ID, access.Grant{PrincipalType: "user", PrincipalID: f.bob.ID, Role: access.Editor}))
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("collect", []string{file.ID}, folder.ID, "send"), bob), 403)
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("delete", []string{file.ID}, "", ""), map[string]string{"Cookie": alice["Cookie"]}), 403)
	status(t, request(t, f.a.Public(), "POST", "/api/v1/nodes/batch", batchInput("delete", []string{file.ID}, "", ""), nil), 401)
}

func TestSelectionArchive(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Папка")
	must(t, err)
	_, err = f.a.Files.CreateFolder(f.ctx, f.alice.ID, folder.ID, "Пустая")
	must(t, err)
	child := putFile(t, f, f.alice, folder.ID, "child.txt", "nested")
	one := putFile(t, f, f.alice, f.alice.HomeID, "same.txt", "one")
	two := putFile(t, f, f.alice, folder.ID, "same.txt", "two")
	headers := browser(t, f, f.alice)
	url := "/api/v1/nodes/archive?id=" + one.ID + "&id=" + folder.ID + "&id=" + child.ID
	result := request(t, f.a.Public(), "GET", url, nil, headers)
	status(t, result, 200)
	want := map[string]string{"same.txt": "one", "Папка/": "", "Папка/Пустая/": "", "Папка/child.txt": "nested", "Папка/same.txt": "two"}
	if got := readArchive(t, result.Body.Bytes()); !reflect.DeepEqual(got, want) {
		t.Fatalf("selection ZIP: %v", got)
	}
	result = request(t, f.a.Public(), "GET", "/api/v1/nodes/archive?id="+one.ID+"&id="+two.ID, nil, headers)
	status(t, result, 200)
	if got := readArchive(t, result.Body.Bytes()); !reflect.DeepEqual(got, map[string]string{"same.txt": "one", "same (1).txt": "two"}) {
		t.Fatalf("duplicate names ZIP: %v", got)
	}
	status(t, request(t, f.a.Public(), "GET", url, nil, nil), 401)
	status(t, request(t, f.a.Public(), "GET", url, nil, browser(t, f, f.bob)), 403)
	status(t, request(t, f.a.Public(), "GET", "/api/v1/nodes/archive", nil, headers), 400)
	status(t, request(t, f.a.Public(), "GET", "/api/v1/nodes/archive?id="+one.ID+"&id="+one.ID, nil, headers), 400)
	for i := 0; i < cap(f.a.downloadSlots); i++ {
		f.a.downloadSlots <- struct{}{}
	}
	status(t, request(t, f.a.Public(), "GET", url, nil, headers), 429)
	for i := 0; i < cap(f.a.downloadSlots); i++ {
		<-f.a.downloadSlots
	}
	// HEAD must not read selected file objects.
	must(t, f.a.Files.Disk.Root.Remove("objects/"+one.ID))
	head := request(t, f.a.Public(), "HEAD", url, nil, headers)
	status(t, head, 200)
	if head.Body.Len() != 0 || head.Header().Get("Content-Type") != "application/zip" {
		t.Fatal("bad selection HEAD")
	}
}

func TestSelectionArchiveAbortsOnPermissionLoss(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "shared")
	must(t, err)
	file := putFile(t, f, f.alice, folder.ID, "data.txt", "content")
	must(t, f.a.Access.SetGrant(f.ctx, f.alice.ID, folder.ID, access.Grant{PrincipalType: "user", PrincipalID: f.bob.ID, Role: access.Reader}))
	headers := browser(t, f, f.bob)
	w := &archiveHookWriter{ResponseRecorder: httptest.NewRecorder(), hookAt: 1}
	w.hook = func() {
		must(t, f.a.Access.SetGrant(f.ctx, f.alice.ID, folder.ID, access.Grant{PrincipalType: "user", PrincipalID: f.bob.ID, Role: 0}))
	}
	r := httptest.NewRequest("GET", "https://files.example/api/v1/nodes/archive?id="+file.ID, nil)
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	var aborted any
	func() { defer func() { aborted = recover() }(); f.a.Public().ServeHTTP(w, r) }()
	if err, ok := aborted.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
		t.Fatalf("stream was not aborted: %v", aborted)
	}
	if len(f.a.downloadSlots) != 0 {
		t.Fatal("selection archive leaked slot")
	}
}
