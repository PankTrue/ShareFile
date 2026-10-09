package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"sharefile/backend/internal/access"
)

func readArchive(t *testing.T, data []byte) map[string]string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	must(t, err)
	out := make(map[string]string)
	for _, entry := range z.File {
		if _, exists := out[entry.Name]; exists {
			t.Fatalf("duplicate ZIP entry %q", entry.Name)
		}
		if strings.HasPrefix(entry.Name, "/") || strings.Contains(entry.Name, "\\") || strings.Contains(entry.Name, "../") {
			t.Fatalf("unsafe ZIP path %q", entry.Name)
		}
		file, err := entry.Open()
		must(t, err)
		body, err := io.ReadAll(file) // Also verifies the ZIP entry's CRC.
		must(t, err)
		must(t, file.Close())
		if strings.HasSuffix(entry.Name, "/") && !entry.Mode().IsDir() {
			t.Fatalf("missing directory mode for %q", entry.Name)
		}
		out[entry.Name] = string(body)
	}
	return out
}

func TestFolderArchives(t *testing.T) {
	f := setup(t)
	root, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Поездка 日本語")
	must(t, err)
	nested, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, root.ID, "Фото и текст")
	must(t, err)
	empty, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, nested.ID, "Пустая")
	must(t, err)
	photo := testPNG(t)
	putFile(t, f, f.alice, root.ID, "заметки.txt", "корень")
	putFile(t, f, f.alice, nested.ID, "заметки.txt", "вложенный текст")
	putFile(t, f, f.alice, nested.ID, "фото.png", photo)
	putFile(t, f, f.alice, nested.ID, "нулевой.bin", "")
	deleted := putFile(t, f, f.alice, root.ID, "удалён.txt", "not in archive")
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, deleted.ID))
	putFile(t, f, f.alice, f.alice.HomeID, "личный.txt", "private")
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, root.ID, "", nil)
	must(t, err)
	must(t, f.a.Access.SetGrant(f.ctx, f.alice.ID, root.ID, access.Grant{PrincipalType: "user", PrincipalID: f.bob.ID, Role: access.Reader}))
	base := "/s/" + token
	headers := browser(t, f, f.bob)
	expectedNested := map[string]string{
		nested.Name + "/": "", nested.Name + "/" + empty.Name + "/": "",
		nested.Name + "/заметки.txt": "вложенный текст", nested.Name + "/фото.png": photo,
		nested.Name + "/нулевой.bin": "",
	}
	expected := map[string]string{root.Name + "/": "", root.Name + "/заметки.txt": "корень"}
	for name, body := range expectedNested {
		expected[root.Name+"/"+name] = body
	}
	for _, test := range []struct {
		url     string
		headers map[string]string
		name    string
		want    map[string]string
	}{
		{base + "/archive?limit=1&offset=99", nil, root.Name, expected},
		{base + "/nodes/" + root.ID + "/archive", nil, root.Name, expected},
		{base + "/nodes/" + nested.ID + "/archive", nil, nested.Name, expectedNested},
		{"/api/v1/nodes/" + root.ID + "/archive", headers, root.Name, expected},
	} {
		result := request(t, f.a.Public(), "GET", test.url, nil, test.headers)
		status(t, result, 200)
		if result.Header().Get("Content-Type") != "application/zip" || result.Header().Get("Content-Length") != "" || result.Header().Get("Accept-Ranges") != "none" {
			t.Fatalf("unexpected archive headers: %v", result.Header())
		}
		media, params, err := mime.ParseMediaType(result.Header().Get("Content-Disposition"))
		must(t, err)
		if media != "attachment" || params["filename"] != test.name+".zip" {
			t.Fatalf("incorrect download filename: %v", params)
		}
		if got := readArchive(t, result.Body.Bytes()); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("archive tree mismatch: got %v, want %v", got, test.want)
		}
	}
	for _, node := range []string{root.ID, nested.ID} {
		page := request(t, f.a.Public(), "GET", base+"/nodes/"+node, nil, nil)
		if !strings.Contains(page.Body.String(), "Скачать всё (ZIP)") || !strings.Contains(page.Body.String(), base+"/nodes/"+node+"/archive") {
			t.Fatal("folder archive action missing")
		}
	}
	emptyZIP := request(t, f.a.Public(), "GET", base+"/nodes/"+empty.ID+"/archive", nil, nil)
	status(t, emptyZIP, 200)
	if got := readArchive(t, emptyZIP.Body.Bytes()); !reflect.DeepEqual(got, map[string]string{empty.Name + "/": ""}) {
		t.Fatalf("empty folder lost: %v", got)
	}
	ranged := request(t, f.a.Public(), "GET", base+"/archive", nil, map[string]string{"Range": "bytes=0-3"})
	status(t, ranged, 200)
	if got := readArchive(t, ranged.Body.Bytes()); !reflect.DeepEqual(got, expected) {
		t.Fatal("Range must be ignored for an archive with unknown length")
	}
}

func TestArchiveIncludesMoreThanOneListingPageWithoutDiskCopies(t *testing.T) {
	f := setup(t)
	root, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Many")
	must(t, err)
	for i := 0; i < 105; i++ {
		putFile(t, f, f.alice, root.ID, fmt.Sprintf("%03d.txt", i), fmt.Sprint(i))
	}
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, root.ID, "", nil)
	must(t, err)
	before := storageNames(t, f)
	result := request(t, f.a.Public(), "GET", "/s/"+token+"/archive", nil, nil)
	status(t, result, 200)
	entries := readArchive(t, result.Body.Bytes())
	if len(entries) != 106 || entries["Many/104.txt"] != "104" {
		t.Fatal("archive stopped at a listing page boundary")
	}
	if after := storageNames(t, f); !reflect.DeepEqual(before, after) {
		t.Fatalf("archive created files on storage: before %v, after %v", before, after)
	}
}

func storageNames(t *testing.T, f *fixture) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{".", "objects", "uploads", "metadata"} {
		entries, err := os.ReadDir(f.a.Files.Disk.Path + "/" + dir)
		must(t, err)
		for _, e := range entries {
			out = append(out, dir+"/"+e.Name())
		}
	}
	return out
}

func TestArchiveAuthorizationAndHeaders(t *testing.T) {
	f := setup(t)
	root, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Protected")
	must(t, err)
	file := putFile(t, f, f.alice, root.ID, "secret.txt", "secret")
	sh, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, root.ID, testPassword, nil)
	must(t, err)
	base := "/s/" + token
	h := f.a.Public()
	urls := []string{base + "/archive", base + "/nodes/" + root.ID + "/archive"}
	for _, url := range urls {
		status(t, request(t, h, "GET", url, nil, nil), 401)
	}
	unlock := request(t, h, "POST", base+"/unlock", []byte("password="+testPassword), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	status(t, unlock, 303)
	cookie := unlock.Result().Cookies()[0]
	guest := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	for _, url := range urls {
		status(t, request(t, h, "GET", url, nil, guest), 200)
	}
	status(t, request(t, h, "GET", base+"/nodes/"+f.alice.HomeID+"/archive", nil, guest), 404)
	status(t, request(t, h, "GET", base+"/nodes/"+file.ID+"/archive", nil, guest), 400)
	status(t, request(t, h, "GET", "/api/v1/nodes/"+root.ID+"/archive", nil, nil), 401)
	status(t, request(t, h, "GET", "/api/v1/nodes/"+root.ID+"/archive", nil, browser(t, f, f.bob)), 403)
	for i := 0; i < cap(f.a.downloadSlots); i++ {
		f.a.downloadSlots <- struct{}{}
	}
	for _, url := range urls {
		status(t, request(t, h, "GET", url, nil, guest), 429)
	}
	status(t, request(t, h, "GET", "/api/v1/nodes/"+root.ID+"/archive", nil, browser(t, f, f.alice)), 429)
	for len(f.a.downloadSlots) > 0 {
		<-f.a.downloadSlots
	}
	// HEAD validates access but never opens file objects, even if one is missing.
	must(t, f.a.Files.Disk.Root.Remove("objects/"+file.ID))
	for _, url := range urls {
		head := request(t, h, "HEAD", url, nil, guest)
		status(t, head, 200)
		if head.Body.Len() != 0 || head.Header().Get("Content-Type") != "application/zip" || head.Header().Get("Content-Length") != "" {
			t.Fatal("incorrect archive HEAD")
		}
	}
	must(t, f.a.Shares.Revoke(f.ctx, f.alice.ID, sh.ID))
	for _, url := range urls {
		status(t, request(t, h, "GET", url, nil, guest), 404)
	}
	for _, mode := range []string{"expired", "permission", "deleted"} {
		sh, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, root.ID, "", nil)
		must(t, err)
		switch mode {
		case "expired":
			_, err = f.a.Store.DB.ExecContext(f.ctx, "UPDATE shares SET expires_at=? WHERE id=?", time.Now().Unix()-1, sh.ID)
		case "permission":
			err = f.a.Files.Move(f.ctx, f.admin.ID, root.ID, f.bob.HomeID, root.Name)
		case "deleted":
			err = f.a.Files.Delete(f.ctx, f.alice.ID, root.ID)
		}
		must(t, err)
		for _, suffix := range []string{"/archive", "/nodes/" + root.ID + "/archive"} {
			status(t, request(t, h, "GET", "/s/"+token+suffix, nil, nil), 404)
		}
		if mode == "permission" {
			must(t, f.a.Files.Move(f.ctx, f.admin.ID, root.ID, f.alice.HomeID, root.Name))
		}
	}
}

type archiveHookWriter struct {
	*httptest.ResponseRecorder
	hook           func()
	writeErr       error
	writes, hookAt int
}

func (w *archiveHookWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.writes++
	if w.hook != nil && w.writes == max(1, w.hookAt) {
		hook := w.hook
		w.hook = nil
		hook()
	}
	if w.writeErr != nil {
		return n, w.writeErr
	}
	return n, err
}

func TestArchiveStreamsBeforeReadingAndAbortsFailures(t *testing.T) {
	for _, mode := range []string{"cancel", "revoke", "move", "missing", "network", "invalid-name"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			root, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Stream")
			must(t, err)
			selected, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, root.ID, "Selected")
			must(t, err)
			file := putFile(t, f, f.alice, selected.ID, "data.txt", "must not appear in failed archive")
			sh, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, root.ID, "", nil)
			must(t, err)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			w := &archiveHookWriter{ResponseRecorder: httptest.NewRecorder()}
			w.hook = func() {
				// First bytes arrive before the archive tries to open this object.
				if !bytes.HasPrefix(w.Body.Bytes(), []byte("PK\x03\x04")) {
					t.Fatal("stream did not begin with a ZIP local header")
				}
				switch mode {
				case "cancel":
					cancel()
				case "revoke":
					must(t, f.a.Shares.Revoke(f.ctx, f.alice.ID, sh.ID))
				case "move":
					must(t, f.a.Files.Move(f.ctx, f.alice.ID, selected.ID, f.alice.HomeID, selected.Name))
				case "missing":
					must(t, f.a.Files.Disk.Root.Remove("objects/"+file.ID))
				case "network":
					w.writeErr = io.ErrClosedPipe
				case "invalid-name":
					_, err := f.a.Store.DB.Exec("UPDATE nodes SET name='../escape' WHERE id=?", file.ID)
					must(t, err)
				}
			}
			r := httptest.NewRequest("GET", "/s/"+token+"/nodes/"+selected.ID+"/archive", nil).WithContext(ctx)
			var aborted any
			func() {
				defer func() { aborted = recover() }()
				f.a.Public().ServeHTTP(w, r)
			}()
			if aborted != http.ErrAbortHandler {
				t.Fatalf("failed stream must abort the HTTP response, got %v", aborted)
			}
			if len(f.a.downloadSlots) != 0 {
				t.Fatal("failed archive leaked its transfer slot")
			}
			if bytes.Contains(w.Body.Bytes(), []byte(`{"error"`)) {
				t.Fatal("JSON error appended to partial ZIP")
			}
			if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); !errors.Is(err, zip.ErrFormat) {
				t.Fatalf("failed archive was finalized: %v", err)
			}
		})
	}
}

func TestArchiveCancellationWhileCopyingFile(t *testing.T) {
	f := setup(t)
	f.a.Files.ChunkBytes = 1024 * 1024
	root, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Large")
	must(t, err)
	data := make([]byte, 256*1024)
	_, err = rand.Read(data)
	must(t, err)
	putFile(t, f, f.alice, root.ID, "random.bin", string(data))
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, root.ID, "", nil)
	must(t, err)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	w := &archiveHookWriter{ResponseRecorder: httptest.NewRecorder(), hook: cancel, hookAt: 2}
	var aborted any
	func() {
		defer func() { aborted = recover() }()
		f.a.Public().ServeHTTP(w, httptest.NewRequest("GET", "/s/"+token+"/archive", nil).WithContext(ctx))
	}()
	if aborted != http.ErrAbortHandler || w.writes < 2 || w.Body.Len() >= len(data) || len(f.a.downloadSlots) != 0 {
		t.Fatalf("copy must stop on cancellation and release the slot: abort=%v, writes=%d, bytes=%d", aborted, w.writes, w.Body.Len())
	}
}
