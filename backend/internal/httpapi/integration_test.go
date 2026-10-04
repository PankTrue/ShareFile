package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/config"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/maintenance"
	"sharefile/backend/internal/metadata"
)

const testPassword = "test-password-123!"

type fixture struct {
	a                 *App
	ctx               context.Context
	admin, alice, bob identity.User
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	must(t, files.Initialize(dir, "test-volume"))
	disk, err := files.OpenDisk(dir, "test-volume", 0)
	must(t, err)
	t.Cleanup(func() { disk.Close() })
	store, err := metadata.Open(filepath.Join(dir, "metadata/sharefile.db"))
	must(t, err)
	t.Cleanup(func() { store.Close() })
	c := config.Config{DataDir: dir, VolumeID: "test-volume", PublicURL: "https://files.example", AdminURL: "https://admin.example", ChunkBytes: 64, UploadSlots: 2, DownloadSlots: 2, InsecureLocal: true}
	a, err := New(c, store, disk)
	must(t, err)
	f := &fixture{a: a, ctx: context.Background()}
	f.admin, err = a.Identity.CreateUser(f.ctx, "admin", testPassword, true)
	must(t, err)
	f.alice, err = a.Identity.CreateUser(f.ctx, "alice", testPassword, false)
	must(t, err)
	f.bob, err = a.Identity.CreateUser(f.ctx, "bob", testPassword, false)
	must(t, err)
	return f
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func request(t *testing.T, h http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if raw, ok := body.([]byte); ok {
		reader = bytes.NewReader(raw)
	} else if body != nil {
		data, err := json.Marshal(body)
		must(t, err)
		reader = bytes.NewReader(data)
	}
	r := httptest.NewRequest(method, "https://files.example"+path, reader)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func status(t *testing.T, w *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if w.Code != expected {
		t.Fatalf("status %d, want %d: %s", w.Code, expected, w.Body.String())
	}
}
func browser(t *testing.T, f *fixture, u identity.User) map[string]string {
	t.Helper()
	_, token, csrf, err := f.a.Identity.Login(f.ctx, u.Username, testPassword)
	must(t, err)
	return map[string]string{"Cookie": f.a.sessionName() + "=" + token, "X-CSRF-Token": csrf}
}
func putFile(t *testing.T, f *fixture, u identity.User, parent, name, data string) metadata.Node {
	t.Helper()
	upload, err := f.a.Files.CreateUpload(f.ctx, u.ID, parent, name, int64(len(data)), "", "")
	must(t, err)
	for upload.State != "complete" {
		chunk := min(int64(len(data))-upload.Offset, f.a.Files.ChunkBytes)
		upload, err = f.a.Files.Patch(f.ctx, u.ID, upload.ID, upload.Offset, strings.NewReader(data[upload.Offset:upload.Offset+chunk]))
		must(t, err)
	}
	n, err := f.a.Files.Node(f.ctx, u.ID, upload.FileID)
	must(t, err)
	return n
}
func TestPrivateRoutesAndCSRF(t *testing.T) {
	f := setup(t)
	headers := browser(t, f, f.admin)
	status(t, request(t, f.a.Public(), "GET", "/api/v1/admin/users", nil, headers), 404)
	status(t, request(t, f.a.Private(), "GET", "/api/v1/admin/users", nil, headers), 200)
	noCSRF := map[string]string{"Cookie": headers["Cookie"]}
	in := map[string]any{"username": "newuser", "password": testPassword}
	status(t, request(t, f.a.Private(), "POST", "/api/v1/admin/users", in, noCSRF), 403)
	evil := map[string]string{"Cookie": headers["Cookie"], "X-CSRF-Token": headers["X-CSRF-Token"], "Origin": "https://evil.example"}
	status(t, request(t, f.a.Private(), "POST", "/api/v1/admin/users", in, evil), 403)
	status(t, request(t, f.a.Private(), "POST", "/api/v1/admin/users", in, headers), 201)
	status(t, request(t, f.a.Private(), "GET", "/api/v1/admin/users", nil, browser(t, f, f.alice)), 403)
	status(t, request(t, f.a.Public(), "GET", "/api/v1/folders", nil, nil), 401)
	login := request(t, f.a.Public(), "POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": testPassword}, nil)
	status(t, login, 200)
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe session cookie")
	}
}

func TestVisibleTrashAndSharesPagination(t *testing.T) {
	f := setup(t)
	a := putFile(t, f, f.alice, f.alice.HomeID, "alice.txt", "a")
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, a.ID))
	b := putFile(t, f, f.bob, f.bob.HomeID, "bob.txt", "b")
	must(t, f.a.Files.Delete(f.ctx, f.bob.ID, b.ID))
	// Ordering puts an inaccessible object first, but it must not consume a page.
	_, err := f.a.Store.DB.Exec("UPDATE nodes SET deleted_at=deleted_at+10 WHERE id=?", b.ID)
	must(t, err)
	out, err := f.a.Files.Trash(f.ctx, f.alice.ID, 1, 0)
	must(t, err)
	if len(out) != 1 || out[0].ID != a.ID {
		t.Fatalf("visible page: %#v", out)
	}
	live := putFile(t, f, f.alice, f.alice.HomeID, "live.txt", "c")
	for i := 0; i < 3; i++ {
		_, _, err = f.a.Shares.Create(f.ctx, f.alice.ID, live.ID, "", nil)
		must(t, err)
	}
	first, err := f.a.Shares.List(f.ctx, f.alice.ID, "", 1, 0)
	must(t, err)
	second, err := f.a.Shares.List(f.ctx, f.alice.ID, "", 1, 1)
	must(t, err)
	if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Fatal("shares pagination repeats entries")
	}
}

func TestRevocationAndPurgeAcrossBatches(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "many")
	must(t, err)
	for i := 0; i < 140; i++ {
		name := fmt.Sprintf("file-%03d", i)
		n := putFile(t, f, f.alice, folder.ID, name, "")
		_, _, err = f.a.Shares.Create(f.ctx, f.alice.ID, n.ID, "", nil)
		must(t, err)
	}
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, folder.ID))
	var remaining int
	must(t, f.a.Store.DB.QueryRow("SELECT count(*) FROM shares WHERE revoked=0").Scan(&remaining))
	if remaining != 0 {
		t.Fatal("links after first batch were not revoked")
	}
	must(t, f.a.Files.Purge(f.ctx, f.alice.ID, folder.ID))
	must(t, f.a.Store.DB.QueryRow("SELECT count(*) FROM nodes WHERE parent_id=?", folder.ID).Scan(&remaining))
	if remaining != 0 {
		t.Fatal("objects after first batch survived purge")
	}
}
func TestInheritedGroupsAndPermanentRevocation(t *testing.T) {
	f := setup(t)
	shared, err := f.a.Files.CreateFolder(f.ctx, f.admin.ID, "", "Shared")
	must(t, err)
	group, err := f.a.Access.CreateGroup(f.ctx, "team")
	must(t, err)
	must(t, f.a.Access.SetGrant(f.ctx, f.admin.ID, shared.ID, access.Grant{PrincipalType: "group", PrincipalID: group.ID, Role: access.Manager}))
	must(t, f.a.Access.SetMember(f.ctx, group.ID, f.alice.ID, true))
	child, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, shared.ID, "nested")
	must(t, err)
	n := putFile(t, f, f.alice, child.ID, "hello.txt", "hello")
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, n.ID, "", nil)
	must(t, err)
	_, err = f.a.Shares.Authorize(f.ctx, token, "")
	must(t, err)
	_, err = f.a.Files.Node(f.ctx, f.bob.ID, n.ID)
	if !errors.Is(err, metadata.ErrForbidden) {
		t.Fatalf("private access: %v", err)
	}
	must(t, f.a.Access.SetMember(f.ctx, group.ID, f.alice.ID, false))
	_, err = f.a.Shares.Authorize(f.ctx, token, "")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatalf("link was not revoked: %v", err)
	}
	must(t, f.a.Access.SetMember(f.ctx, group.ID, f.alice.ID, true))
	_, err = f.a.Shares.Authorize(f.ctx, token, "")
	if !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("revoked link revived")
	}
	role, err := access.Role(f.ctx, f.a.Store.DB, f.alice.ID, n.ID)
	must(t, err)
	if role != access.Manager {
		t.Fatalf("role %d", role)
	}
	must(t, f.a.Files.Move(f.ctx, f.admin.ID, n.ID, f.bob.HomeID, n.Name))
	if _, err = f.a.Files.Node(f.ctx, f.alice.ID, n.ID); !errors.Is(err, metadata.ErrForbidden) {
		t.Fatal("old ancestor rights survived move")
	}
}

type brokenReader struct{ done bool }

func (r *brokenReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrUnexpectedEOF
	}
	r.done = true
	return copy(p, "abcd"), nil
}
func TestUploadInterruptionAndRecovery(t *testing.T) {
	f := setup(t)
	u, err := f.a.Files.CreateUpload(f.ctx, f.alice.ID, f.alice.HomeID, "resume.txt", 8, "", "retry-key")
	must(t, err)
	u, err = f.a.Files.Patch(f.ctx, f.alice.ID, u.ID, 0, &brokenReader{})
	if !errors.Is(err, io.ErrUnexpectedEOF) || u.Offset != 4 {
		t.Fatalf("checkpoint: offset %d, err %v", u.Offset, err)
	}
	if _, err = f.a.Files.Node(f.ctx, f.alice.ID, u.FileID); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("partial file published")
	}
	if _, err = f.a.Files.Upload(f.ctx, f.bob.ID, u.ID); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("foreign upload disclosed")
	}
	raw, err := f.a.Files.Disk.Root.OpenFile("uploads/"+u.ID, os.O_APPEND|os.O_WRONLY, 0600)
	must(t, err)
	_, err = raw.WriteString("uncommitted")
	must(t, err)
	raw.Close()
	must(t, f.a.Files.Recover(f.ctx))
	info, err := f.a.Files.Disk.Root.Stat("uploads/" + u.ID)
	must(t, err)
	if info.Size() != 4 {
		t.Fatal("uncommitted tail retained")
	}
	if _, err = f.a.Files.Patch(f.ctx, f.alice.ID, u.ID, 0, strings.NewReader("efgh")); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("wrong offset accepted")
	}
	u, err = f.a.Files.Patch(f.ctx, f.alice.ID, u.ID, 4, strings.NewReader("efgh"))
	must(t, err)
	if u.State != "complete" {
		t.Fatal("file not committed")
	}
	repeat, err := f.a.Files.CreateUpload(f.ctx, f.alice.ID, f.alice.HomeID, "resume.txt", 8, "", "retry-key")
	must(t, err)
	if repeat.ID != u.ID {
		t.Fatal("idempotency failed")
	}
	rangeHeader := browser(t, f, f.alice)
	rangeHeader["Range"] = "bytes=2-5"
	response := request(t, f.a.Public(), "GET", "/api/v1/nodes/"+u.FileID+"/content", nil, rangeHeader)
	status(t, response, 206)
	if response.Body.String() != "cdef" || response.Header().Get("Content-Range") != "bytes 2-5/8" {
		t.Fatal("invalid Range response")
	}
}
func TestRecoverAfterRenameBeforeCommit(t *testing.T) {
	f := setup(t)
	u, err := f.a.Files.CreateUpload(f.ctx, f.alice.ID, f.alice.HomeID, "recover.txt", 4, "", "")
	must(t, err)
	raw, err := f.a.Files.Disk.Root.OpenFile("uploads/"+u.ID, os.O_WRONLY, 0600)
	must(t, err)
	_, err = raw.WriteString("data")
	must(t, err)
	must(t, raw.Sync())
	raw.Close()
	_, err = f.a.Store.DB.ExecContext(f.ctx, "UPDATE uploads SET offset=4,state='committing' WHERE id=?", u.ID)
	must(t, err)
	must(t, f.a.Files.Disk.Root.Rename("uploads/"+u.ID, "objects/"+u.FileID))
	must(t, f.a.Files.Recover(f.ctx))
	u, err = f.a.Files.Upload(f.ctx, f.alice.ID, u.ID)
	must(t, err)
	if u.State != "complete" {
		t.Fatal("commit not recovered")
	}
	n, err := f.a.Files.Node(f.ctx, f.alice.ID, u.FileID)
	must(t, err)
	if n.Size != 4 {
		t.Fatal("invalid recovered file")
	}
}
func TestConcurrentChunksAndExpiry(t *testing.T) {
	f := setup(t)
	u, err := f.a.Files.CreateUpload(f.ctx, f.alice.ID, f.alice.HomeID, "race.txt", 8, "", "")
	must(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.a.Files.Patch(f.ctx, f.alice.ID, u.ID, 0, strings.NewReader("abcd"))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for e := range errs {
		if e == nil {
			success++
		} else if errors.Is(e, metadata.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	_, err = f.a.Store.DB.ExecContext(f.ctx, "UPDATE uploads SET updated_at=? WHERE id=?", time.Now().Add(-8*24*time.Hour).Unix(), u.ID)
	must(t, err)
	must(t, f.a.Files.Cleanup(f.ctx))
	if _, err = f.a.Files.Upload(f.ctx, f.alice.ID, u.ID); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("expired upload retained")
	}
	if _, err = f.a.Files.Disk.Root.Stat("uploads/" + u.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired data retained")
	}
}
func TestPublicLinksScopePasswordAndLiveFolder(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Public")
	must(t, err)
	node := putFile(t, f, f.alice, folder.ID, "safe.txt", "abcdef")
	outside := putFile(t, f, f.alice, f.alice.HomeID, "private.txt", "private")
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, folder.ID, "", nil)
	must(t, err)
	status(t, request(t, f.a.Public(), "GET", "/s/"+token+"/nodes/"+outside.ID, nil, nil), 404)
	rangeResponse := request(t, f.a.Public(), "GET", "/s/"+token+"/nodes/"+node.ID, nil, map[string]string{"Range": "bytes=1-3"})
	status(t, rangeResponse, 206)
	if rangeResponse.Body.String() != "bcd" {
		t.Fatal("wrong public range")
	}
	_ = putFile(t, f, f.alice, folder.ID, "<img onerror=alert(1)>.txt", "new")
	page := request(t, f.a.Public(), "GET", "/s/"+token, nil, nil)
	status(t, page, 200)
	if !strings.Contains(page.Body.String(), "&lt;img") || strings.Contains(page.Body.String(), "<img") {
		t.Fatal("unsafe or stale folder page")
	}
	sh, locked, err := f.a.Shares.Create(f.ctx, f.alice.ID, node.ID, testPassword, nil)
	must(t, err)
	status(t, request(t, f.a.Public(), "GET", "/s/"+locked, nil, nil), 401)
	unlock := request(t, f.a.Public(), "POST", "/s/"+locked+"/unlock", []byte("password="+testPassword), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	status(t, unlock, 303)
	cookie := unlock.Result().Cookies()[0]
	guest := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	status(t, request(t, f.a.Public(), "GET", "/s/"+locked, nil, guest), 200)
	must(t, f.a.Shares.Revoke(f.ctx, f.alice.ID, sh.ID))
	status(t, request(t, f.a.Public(), "GET", "/s/"+locked, nil, guest), 404)
}
func TestReplaceTrashRestoreAndPurgeRecovery(t *testing.T) {
	f := setup(t)
	old := putFile(t, f, f.alice, f.alice.HomeID, "file.txt", "old")
	duplicate := putFile(t, f, f.alice, f.alice.HomeID, "file.txt", "copy")
	if duplicate.Name != "file (1).txt" {
		t.Fatal(duplicate.Name)
	}
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, old.ID, "", nil)
	must(t, err)
	upload, err := f.a.Files.CreateUpload(f.ctx, f.alice.ID, f.alice.HomeID, "file.txt", 3, old.ID, "")
	must(t, err)
	upload, err = f.a.Files.Patch(f.ctx, f.alice.ID, upload.ID, 0, strings.NewReader("new"))
	must(t, err)
	if _, err = f.a.Shares.Authorize(f.ctx, token, ""); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("old share still active")
	}
	restored, err := f.a.Files.Restore(f.ctx, f.alice.ID, old.ID, "")
	must(t, err)
	if restored.Name != "file (2).txt" {
		t.Fatal(restored.Name)
	}
	if _, err = f.a.Shares.Authorize(f.ctx, token, ""); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("restoration revived share")
	}
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, restored.ID))
	_, err = f.a.Store.DB.ExecContext(f.ctx, "INSERT INTO purges VALUES(?)", old.ID)
	must(t, err)
	must(t, f.a.Files.Disk.Root.Remove("objects/"+old.ID))
	if _, err = f.a.Files.Restore(f.ctx, f.alice.ID, old.ID, ""); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("purging file restored")
	}
	must(t, f.a.Files.Recover(f.ctx))
	if _, err = metadata.GetNode(f.ctx, f.a.Store.DB, old.ID); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("purge not recovered")
	}
}
func TestDevicePairingAndAccountDisable(t *testing.T) {
	f := setup(t)
	requestInfo, err := f.a.Identity.StartDevice(f.ctx, "Desktop")
	must(t, err)
	if _, err = f.a.Identity.PollDevice(f.ctx, requestInfo.ID, requestInfo.Secret); !errors.Is(err, identity.ErrPending) {
		t.Fatal(err)
	}
	must(t, f.a.Identity.ApproveDevice(f.ctx, f.admin.ID, requestInfo.ID))
	token, err := f.a.Identity.PollDevice(f.ctx, requestInfo.ID, requestInfo.Secret)
	must(t, err)
	if _, err = f.a.Identity.PollDevice(f.ctx, requestInfo.ID, requestInfo.Secret); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("pairing replayed")
	}
	bearer := map[string]string{"Authorization": "Bearer " + token}
	status(t, request(t, f.a.Public(), "GET", "/api/v1/folders", nil, bearer), 200)
	status(t, request(t, f.a.Private(), "GET", "/api/v1/admin/users", nil, bearer), 403)
	n := putFile(t, f, f.alice, f.alice.HomeID, "owned.txt", "data")
	_, shareToken, err := f.a.Shares.Create(f.ctx, f.alice.ID, n.ID, "", nil)
	must(t, err)
	session := browser(t, f, f.alice)
	disabled := false
	must(t, f.a.Identity.UpdateUser(f.ctx, f.alice.ID, &disabled, ""))
	status(t, request(t, f.a.Public(), "GET", "/api/v1/folders", nil, session), 401)
	if _, err = f.a.Shares.Authorize(f.ctx, shareToken, ""); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("disabled user's link active")
	}
	active := true
	must(t, f.a.Identity.UpdateUser(f.ctx, f.alice.ID, &active, ""))
	if _, err = f.a.Shares.Authorize(f.ctx, shareToken, ""); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("reactivation revived link")
	}
	if err = f.a.Identity.UpdateUser(f.ctx, f.admin.ID, &disabled, ""); !errors.Is(err, metadata.ErrConflict) {
		t.Fatal("last admin disabled")
	}
}
func TestTusHTTPProtocol(t *testing.T) {
	f := setup(t)
	h := browser(t, f, f.alice)
	h["Tus-Resumable"] = "1.0.0"
	h["Upload-Length"] = "4"
	h["Upload-Metadata"] = "filename " + base64.StdEncoding.EncodeToString([]byte("protocol.txt")) + ",folder_id " + base64.StdEncoding.EncodeToString([]byte(f.alice.HomeID))
	create := request(t, f.a.Public(), "POST", "/api/v1/uploads", nil, h)
	status(t, create, 201)
	location := strings.TrimPrefix(create.Header().Get("Location"), f.a.Config.PublicURL)
	head := request(t, f.a.Public(), "HEAD", location, nil, h)
	status(t, head, 200)
	if head.Header().Get("Upload-Offset") != "0" {
		t.Fatal("incorrect initial offset")
	}
	if head.Header().Get("Upload-Metadata") != h["Upload-Metadata"] {
		t.Fatal("upload metadata missing")
	}
	h["Tus-Resumable"] = "2.0.0"
	status(t, request(t, f.a.Public(), "HEAD", location, nil, h), 412)
	h["Tus-Resumable"] = "1.0.0"
	h["Upload-Offset"] = "0"
	h["Content-Type"] = "application/offset+octet-stream"
	h["X-HTTP-Method-Override"] = "PATCH"
	status(t, request(t, f.a.Public(), "POST", location, []byte("data"), h), 204)
	delete(h, "X-HTTP-Method-Override")
	head = request(t, f.a.Public(), "HEAD", location, nil, h)
	status(t, head, 200)
	if head.Header().Get("Upload-Offset") != "4" || head.Header().Get("Sharefile-Upload-State") != "complete" {
		t.Fatal("completion not exposed")
	}
	status(t, request(t, f.a.Public(), "DELETE", location, nil, h), 204)
	status(t, request(t, f.a.Public(), "HEAD", location, nil, h), 404)
	status(t, request(t, f.a.Public(), "GET", "/api/v1/nodes/"+create.Header().Get("Sharefile-File-ID")+"/content", nil, h), 200)
}

func TestPurgingFolderIncludesPreviouslyDeletedChildren(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "folder")
	must(t, err)
	file := putFile(t, f, f.alice, folder.ID, "old.txt", "old")
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, file.ID))
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, folder.ID))
	must(t, f.a.Files.Purge(f.ctx, f.alice.ID, folder.ID))
	if _, err = metadata.GetNode(f.ctx, f.a.Store.DB, file.ID); !errors.Is(err, metadata.ErrNotFound) {
		t.Fatal("nested trash survived parent purge")
	}
}

func TestResumeAndRangeBeyondFourGiB(t *testing.T) {
	f := setup(t)
	const offset int64 = 5 << 30
	u, err := f.a.Files.CreateUpload(f.ctx, f.alice.ID, f.alice.HomeID, "large.bin", offset+3, "", "")
	if errors.Is(err, metadata.ErrSpace) {
		t.Skip("filesystem reports less than 5 GiB free")
	}
	must(t, err)
	// A sparse checkpoint exercises 64-bit offsets without writing 5 GiB of test data.
	file, err := f.a.Files.Disk.Root.OpenFile("uploads/"+u.ID, os.O_WRONLY, 0600)
	must(t, err)
	must(t, file.Truncate(offset))
	must(t, file.Sync())
	must(t, file.Close())
	_, err = f.a.Store.DB.Exec("UPDATE uploads SET offset=? WHERE id=?", offset, u.ID)
	must(t, err)
	u, err = f.a.Files.Patch(f.ctx, f.alice.ID, u.ID, offset, strings.NewReader("xyz"))
	must(t, err)
	if u.Offset != offset+3 || u.State != "complete" {
		t.Fatal("large offset was truncated")
	}
	h := browser(t, f, f.alice)
	h["Range"] = "bytes=5368709119-5368709122"
	w := request(t, f.a.Public(), "GET", "/api/v1/nodes/"+u.FileID+"/content", nil, h)
	status(t, w, 206)
	if w.Body.String() != string([]byte{0, 'x', 'y', 'z'}) {
		t.Fatal("incorrect range beyond 4 GiB")
	}
}
func TestStorageProtectionAndImport(t *testing.T) {
	f := setup(t)
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "regular.txt"), []byte("import"), 0600))
	must(t, os.Symlink(filepath.Join(outside, "regular.txt"), filepath.Join(outside, "link")))
	n, err := f.a.Files.Import(f.ctx, f.admin.ID, f.admin.HomeID, "imported.txt", outside, "regular.txt")
	must(t, err)
	if n.Size != 6 {
		t.Fatal("bad import")
	}
	if _, err = f.a.Files.Import(f.ctx, f.admin.ID, f.admin.HomeID, "symlink.txt", outside, "link"); !errors.Is(err, metadata.ErrInvalid) {
		t.Fatal("symlink imported")
	}
	if _, err = f.a.Files.Import(f.ctx, f.admin.ID, f.admin.HomeID, "escape.txt", outside, "../escape"); !errors.Is(err, metadata.ErrInvalid) {
		t.Fatal("escaped import accepted")
	}
	if _, err = f.a.Files.CreateUpload(f.ctx, f.alice.ID, f.alice.HomeID, "../escape", 1, "", ""); !errors.Is(err, metadata.ErrInvalid) {
		t.Fatal("unsafe name accepted")
	}
	f.a.Files.Disk.Reserve = ^uint64(0)
	if _, err = f.a.Files.CreateUpload(f.ctx, f.alice.ID, f.alice.HomeID, "full.txt", 1, "", ""); !errors.Is(err, metadata.ErrSpace) {
		t.Fatal("reserve ignored")
	}
	f.a.Files.Disk.Reserve = 0
	must(t, os.Rename(f.a.Config.DataDir, f.a.Config.DataDir+"-detached"))
	status(t, request(t, f.a.Public(), "GET", "/readyz", nil, nil), 503)
	if err = f.a.Files.Disk.Check(); !errors.Is(err, metadata.ErrUnavailable) {
		t.Fatal("detached volume accepted")
	}
	must(t, os.Rename(f.a.Config.DataDir+"-detached", f.a.Config.DataDir))
}
func TestOfflineBackupRestore(t *testing.T) {
	f := setup(t)
	node := putFile(t, f, f.alice, f.alice.HomeID, "backup.txt", "backup data")
	archive := filepath.Join(t.TempDir(), "backup.tar")
	_, err := f.a.Store.DB.ExecContext(f.ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	must(t, err)
	must(t, f.a.Store.Close())
	must(t, maintenance.Backup(f.ctx, f.a.Files.Disk, archive))
	restored := filepath.Join(t.TempDir(), "restored")
	must(t, maintenance.Restore(f.ctx, restored, "test-volume", archive))
	data, err := os.ReadFile(filepath.Join(restored, "objects", node.ID))
	must(t, err)
	if string(data) != "backup data" {
		t.Fatal("restore data mismatch")
	}
	db, err := metadata.Open(filepath.Join(restored, "metadata/sharefile.db"))
	must(t, err)
	defer db.Close()
	n, err := metadata.GetNode(f.ctx, db.DB, node.ID)
	must(t, err)
	if n.Name != node.Name || n.OwnerID != f.alice.ID {
		t.Fatal("restore metadata mismatch")
	}
	if err = maintenance.Restore(f.ctx, restored, "test-volume", archive); err == nil {
		t.Fatal("existing storage overwritten")
	}
}
