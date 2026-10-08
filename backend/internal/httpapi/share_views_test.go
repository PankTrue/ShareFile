package httpapi

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"sharefile/backend/internal/access"
)

func testPNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 100, G: 150, B: 200, A: 255})
	var buf bytes.Buffer
	must(t, png.Encode(&buf, img))
	return buf.String()
}

func TestPublicFolderPreviewNavigation(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Общая папка")
	must(t, err)
	nested, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, folder.ID, "Фото и текст")
	must(t, err)
	text := putFile(t, f, f.alice, nested.ID, "заметки.html", "<script>alert('test')</script>\nПривет, мир!")
	photo := putFile(t, f, f.alice, nested.ID, "фото.png", testPNG(t))
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, folder.ID, "", nil)
	must(t, err)
	base := "/s/" + token
	h := f.a.Public()
	page := request(t, h, "GET", base, nil, nil)
	status(t, page, 200)
	if !strings.Contains(page.Body.String(), base+"/nodes/"+nested.ID) {
		t.Fatal("nested folder navigation missing")
	}
	page = request(t, h, "GET", base+"/nodes/"+nested.ID+"?limit=1", nil, nil)
	status(t, page, 200)
	if !strings.Contains(page.Body.String(), "limit=1&amp;offset=1") {
		t.Fatal("pagination lost the requested page size")
	}
	page = request(t, h, "GET", base+"/nodes/"+nested.ID, nil, nil)
	status(t, page, 200)
	for _, node := range []string{text.ID, photo.ID} {
		if !strings.Contains(page.Body.String(), base+"/nodes/"+node+"/view") || !strings.Contains(page.Body.String(), "aria-label=\"Скачать ") {
			t.Fatal("file preview/download links missing")
		}
	}
	page = request(t, h, "GET", base+"/nodes/"+text.ID+"/view", nil, nil)
	status(t, page, 200)
	if !strings.Contains(page.Body.String(), "Привет, мир!") || !strings.Contains(page.Body.String(), "&lt;script&gt;") || strings.Contains(page.Body.String(), "<script>") {
		t.Fatal("text must be displayed and HTML escaped")
	}
	if !strings.Contains(page.Body.String(), "Вернуться в папку") || strings.Contains(page.Body.String(), f.alice.HomeID) || strings.Contains(page.Body.String(), ">alice<") {
		t.Fatal("breadcrumbs missing or disclose ancestors above the shared root")
	}
	if strings.Contains(page.Header().Get("Content-Security-Policy"), "unsafe-inline") || page.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatal("public page security headers changed")
	}
	page = request(t, h, "GET", base+"/nodes/"+photo.ID+"/view", nil, nil)
	status(t, page, 200)
	if !strings.Contains(page.Body.String(), "<img src=\""+base+"/nodes/"+photo.ID+"/preview\"") {
		t.Fatal("image preview missing")
	}
	status(t, request(t, h, "GET", base+"/nodes/"+text.ID+"/preview", nil, nil), 400)
	status(t, request(t, h, "GET", base+"/nodes/"+folder.ID+"/preview", nil, nil), 400)
	redirect := request(t, h, "GET", base+"/nodes/"+nested.ID+"/view", nil, nil)
	status(t, redirect, 303)
	if redirect.Header().Get("Location") != base+"/nodes/"+nested.ID {
		t.Fatal("folder view should navigate to the folder")
	}
	style := request(t, h, "GET", "/share-assets/style.css", nil, nil)
	status(t, style, 200)
	if !strings.HasPrefix(style.Header().Get("Content-Type"), "text/css") {
		t.Fatal("stylesheet not served as CSS")
	}
}

func TestPublicImagePreviewStreamsAndKeepsDownloads(t *testing.T) {
	f := setup(t)
	data := testPNG(t)
	// The content, not an untrusted extension, supplies the inline MIME type.
	photo := putFile(t, f, f.alice, f.alice.HomeID, "image.txt", data)
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, photo.ID, "", nil)
	must(t, err)
	base := "/s/" + token
	preview := base + "/nodes/" + photo.ID + "/preview"
	h := f.a.Public()
	response := request(t, h, "GET", preview, nil, nil)
	status(t, response, 200)
	if response.Body.String() != data || response.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "inline") {
		t.Fatal("image should stream intact with an explicit safe MIME type")
	}
	ranged := request(t, h, "GET", preview, nil, map[string]string{"Range": "bytes=1-3"})
	status(t, ranged, 206)
	if ranged.Body.String() != data[1:4] {
		t.Fatal("incorrect image range")
	}
	head := request(t, h, "HEAD", preview, nil, nil)
	status(t, head, 200)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatal("image HEAD should return headers without a body")
	}
	status(t, request(t, h, "GET", preview, nil, map[string]string{"If-None-Match": response.Header().Get("ETag")}), 304)
	for _, url := range []string{base, base + "/nodes/" + photo.ID} {
		download := request(t, h, "GET", url, nil, nil)
		status(t, download, 200)
		if download.Body.String() != data || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment") {
			t.Fatal("existing direct file downloads changed")
		}
	}
}

func TestPublicPreviewsRespectShareSecurity(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "Public")
	must(t, err)
	photo := putFile(t, f, f.alice, folder.ID, "photo.png", testPNG(t))
	outside := putFile(t, f, f.alice, f.alice.HomeID, "private.png", testPNG(t))
	sh, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, folder.ID, testPassword, nil)
	must(t, err)
	base := "/s/" + token
	h := f.a.Public()
	urls := []string{base, base + "/nodes/" + photo.ID + "/view", base + "/nodes/" + photo.ID + "/preview"}
	for _, url := range urls {
		response := request(t, h, "GET", url, nil, nil)
		status(t, response, 401)
		if response.Header().Get("Referrer-Policy") != "same-origin" {
			t.Fatal("password form must preserve its same-origin POST Origin")
		}
		if strings.Contains(response.Body.String(), "photo.png") {
			t.Fatal("locked share disclosed the filename")
		}
	}
	unlock := request(t, h, "POST", base+"/unlock", []byte("password="+testPassword), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	status(t, unlock, 303)
	cookie := unlock.Result().Cookies()[0]
	headers := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	for _, url := range urls {
		status(t, request(t, h, "GET", url, nil, headers), 200)
	}
	for _, node := range []string{outside.ID, f.alice.HomeID} {
		for _, suffix := range []string{"", "/view", "/preview"} {
			status(t, request(t, h, "GET", base+"/nodes/"+node+suffix, nil, headers), 404)
		}
	}
	must(t, f.a.Shares.Revoke(f.ctx, f.alice.ID, sh.ID))
	for _, url := range urls {
		status(t, request(t, h, "GET", url, nil, headers), 404)
	}
	// Expiration, permission loss, and deletion apply to every new endpoint too.
	for _, mode := range []string{"expired", "permission", "deleted"} {
		sh, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, folder.ID, "", nil)
		must(t, err)
		switch mode {
		case "expired":
			_, err = f.a.Store.DB.ExecContext(f.ctx, "UPDATE shares SET expires_at=? WHERE id=?", time.Now().Unix()-1, sh.ID)
		case "permission":
			must(t, f.a.Files.Move(f.ctx, f.admin.ID, folder.ID, f.bob.HomeID, folder.Name))
		case "deleted":
			must(t, f.a.Files.Delete(f.ctx, f.alice.ID, photo.ID))
		}
		must(t, err)
		for _, suffix := range []string{"/view", "/preview"} {
			status(t, request(t, h, "GET", "/s/"+token+"/nodes/"+photo.ID+suffix, nil, nil), 404)
		}
		if mode == "permission" {
			must(t, f.a.Files.Move(f.ctx, f.admin.ID, folder.ID, f.alice.HomeID, folder.Name))
		}
	}
	// A reader cannot publish someone else's folder.
	must(t, f.a.Access.SetGrant(f.ctx, f.admin.ID, f.bob.HomeID, access.Grant{PrincipalType: "user", PrincipalID: f.alice.ID, Role: access.Reader}))
	status(t, request(t, h, "POST", "/api/v1/shares", map[string]string{"node_id": f.bob.HomeID}, browser(t, f, f.alice)), 403)
}

func TestTextPreviewLimitsAndFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, body, kind, text string
		truncated              bool
	}{
		{"empty.txt", "", "text", "", false},
		{"notes.txt", "\xef\xbb\xbfПривет\n", "text", "Привет\n", false},
		{"README", "Plain text without extension", "text", "Plain text without extension", false},
		{"unsafe.svg", `<svg onload="alert(1)"></svg>`, "text", `<svg onload="alert(1)"></svg>`, false},
		{"binary.txt", "\x00\x01\x02", "", "", false},
		{"legacy.txt", "\xff\xfehello", "", "", false},
		{"archive.zip", "PK\x03\x04\x00\x00\x00\x00", "", "", false},
		{"large.txt", strings.Repeat("a", textPreviewBytes-1) + "Я" + "hidden", "text", strings.Repeat("a", textPreviewBytes-1), true},
		{"invalid-large.txt", strings.Repeat("a", textPreviewBytes-1) + "\xffhidden", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "preview")
			must(t, err)
			defer f.Close()
			_, err = f.WriteString(tc.body)
			must(t, err)
			_, err = f.Seek(0, 0)
			must(t, err)
			preview, err := readPreview(f, tc.name)
			must(t, err)
			if preview.Kind != tc.kind || preview.Text != tc.text || preview.Truncated != tc.truncated {
				t.Fatalf("unexpected preview: kind=%q text length=%d truncated=%t", preview.Kind, len(preview.Text), preview.Truncated)
			}
		})
	}
}

func TestPublicPreviewTransferLimit(t *testing.T) {
	f := setup(t)
	photo := putFile(t, f, f.alice, f.alice.HomeID, "photo.png", testPNG(t))
	_, token, err := f.a.Shares.Create(f.ctx, f.alice.ID, photo.ID, "", nil)
	must(t, err)
	for i := 0; i < cap(f.a.downloadSlots); i++ {
		f.a.downloadSlots <- struct{}{}
	}
	for _, suffix := range []string{"/view", "/preview"} {
		status(t, request(t, f.a.Public(), http.MethodGet, "/s/"+token+"/nodes/"+photo.ID+suffix, nil, nil), 429)
	}
}
