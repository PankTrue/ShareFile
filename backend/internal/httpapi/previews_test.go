package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"sharefile/backend/internal/access"
)

func TestAccountPreviewFormatsAndSafeText(t *testing.T) {
	f := setup(t)
	f.a.Files.ChunkBytes = 1024 * 1024
	headers := browser(t, f, f.alice)
	for _, tc := range []struct {
		name, data, kind, media string
	}{
		{"photo.txt", testPNG(t), "image", "image/png"},
		{"notes.html", `<script>window.bad=true</script>`, "text", "text/plain; charset=utf-8"},
		{"unsafe.svg", `<svg onload="alert(1)"></svg>`, "text", "text/plain; charset=utf-8"},
		{"empty.txt", "", "text", "text/plain; charset=utf-8"},
		{"large.txt", strings.Repeat("a", textPreviewBytes) + "Я", "text", "text/plain; charset=utf-8"},
		{"document.bin", "%PDF-1.7\n%%EOF", "pdf", "application/pdf"},
		{"song.mp3", "ID3\x04\x00\x00\x00\x00\x00\x00", "audio", "audio/mpeg"},
		{"song.wav", "RIFF\x04\x00\x00\x00WAVEfmt ", "audio", "audio/wave"},
		{"song.opus", "OggS\x00rest", "audio", "audio/ogg"},
		{"movie.ogv", "OggS\x00rest", "video", "video/ogg"},
		{"movie.webm", "\x1a\x45\xdf\xa3\x00", "video", "video/webm"},
		{"song.weba", "\x1a\x45\xdf\xa3\x00", "audio", "audio/webm"},
		{"movie.mp4", "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isommp42", "video", "video/mp4"},
		{"song.m4a", "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isommp42", "audio", "audio/mp4"},
		{"isom.mp4", "\x00\x00\x00\x18ftypisom\x00\x00\x00\x00isomiso2", "video", "video/mp4"},
		{"isom.m4a", "\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00isomiso2", "audio", "audio/mp4"},
		{"song.flac", "fLaC\x00\x00\x00\x22", "audio", "audio/flac"},
		{"song.aac", "\xff\xf1\x50\x80", "audio", "audio/aac"},
		{"raw.mp3", "\xff\xfb\x90\x64", "audio", "audio/mpeg"},
		{"fake.pdf", "<html>unsafe</html>", "unsupported", ""},
		{"fake.mp4", "\x00\x01\x02", "unsupported", ""},
		{"binary.txt", "\x00\x01\x02", "unsupported", "text/plain; charset=utf-8"},
		{"legacy.txt", "\xff\xfehello", "unsupported", "text/plain; charset=utf-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := putFile(t, f, f.alice, f.alice.HomeID, tc.name, tc.data)
			url := "/api/v1/nodes/" + n.ID + "/preview"
			response := request(t, f.a.Public(), "GET", url, nil, headers)
			status(t, response, 200)
			var p nodePreview
			must(t, json.Unmarshal(response.Body.Bytes(), &p))
			if p.Kind != tc.kind || p.MediaType != tc.media {
				t.Fatalf("unexpected descriptor: %+v", p)
			}
			if tc.kind == "text" {
				want := tc.data
				if len(want) > textPreviewBytes {
					want = want[:textPreviewBytes]
				}
				if p.Text != want || p.Truncated != (len(tc.data) > textPreviewBytes) {
					t.Fatal("text or truncation changed")
				}
			}
			if tc.kind == "text" || tc.kind == "unsupported" {
				status(t, request(t, f.a.Public(), "GET", url+"/content", nil, headers), 400)
				return
			}
			inline := request(t, f.a.Public(), "GET", url+"/content", nil, headers)
			status(t, inline, 200)
			if inline.Body.String() != tc.data || inline.Header().Get("Content-Type") != tc.media || !strings.HasPrefix(inline.Header().Get("Content-Disposition"), "inline") {
				t.Fatal("incorrect inline bytes or headers")
			}
			if inline.Header().Get("Cache-Control") != "no-store" || inline.Header().Get("X-Content-Type-Options") != "nosniff" || inline.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" || !strings.Contains(inline.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
				t.Fatal("private content security headers missing")
			}
			rangeHeaders := map[string]string{"Cookie": headers["Cookie"], "Range": "bytes=1-3"}
			ranged := request(t, f.a.Public(), "GET", url+"/content", nil, rangeHeaders)
			status(t, ranged, 206)
			if ranged.Body.String() != tc.data[1:4] {
				t.Fatal("incorrect range")
			}
			head := request(t, f.a.Public(), "HEAD", url+"/content", nil, headers)
			status(t, head, 200)
			if head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
				t.Fatal("HEAD should return length without bytes")
			}
			status(t, request(t, f.a.Public(), "GET", url+"/content", nil, map[string]string{"Cookie": headers["Cookie"], "If-None-Match": inline.Header().Get("ETag")}), 304)
			download := request(t, f.a.Public(), "GET", "/api/v1/nodes/"+n.ID+"/content", nil, headers)
			status(t, download, 200)
			if !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment") || download.Body.String() != tc.data {
				t.Fatal("download behavior changed")
			}
		})
	}
	var count int
	must(t, f.a.Store.DB.QueryRowContext(f.ctx, "SELECT count(*) FROM shares").Scan(&count))
	if count != 0 {
		t.Fatal("account preview must not create public shares")
	}
}

func TestAccountPreviewAuthorization(t *testing.T) {
	f := setup(t)
	folder, err := f.a.Files.CreateFolder(f.ctx, f.alice.ID, f.alice.HomeID, "photos")
	must(t, err)
	n := putFile(t, f, f.alice, folder.ID, "photo.png", testPNG(t))
	url := "/api/v1/nodes/" + n.ID + "/preview"
	alice, bob := browser(t, f, f.alice), browser(t, f, f.bob)
	for _, suffix := range []string{"", "/content"} {
		status(t, request(t, f.a.Public(), "GET", url+suffix, nil, nil), 401)
		status(t, request(t, f.a.Public(), "GET", url+suffix, nil, bob), 403)
		status(t, request(t, f.a.Public(), "GET", url+suffix, nil, alice), 200)
		status(t, request(t, f.a.Private(), "GET", url+suffix, nil, browser(t, f, f.admin)), 200)
		status(t, request(t, f.a.Public(), "GET", "/api/v1/nodes/"+folder.ID+"/preview"+suffix, nil, alice), 400)
		status(t, request(t, f.a.Public(), "GET", "/api/v1/nodes/missing/preview"+suffix, nil, alice), 404)
	}
	grant := access.Grant{PrincipalType: "user", PrincipalID: f.bob.ID, Role: access.Reader}
	must(t, f.a.Access.SetGrant(f.ctx, f.alice.ID, folder.ID, grant))
	for _, suffix := range []string{"", "/content"} {
		status(t, request(t, f.a.Public(), "GET", url+suffix, nil, bob), 200)
	}
	grant.Role = 0
	must(t, f.a.Access.SetGrant(f.ctx, f.alice.ID, folder.ID, grant))
	for _, suffix := range []string{"", "/content"} {
		status(t, request(t, f.a.Public(), "GET", url+suffix, nil, bob), 403)
	}
	must(t, f.a.Files.Delete(f.ctx, f.alice.ID, folder.ID))
	for _, suffix := range []string{"", "/content"} {
		status(t, request(t, f.a.Public(), "GET", url+suffix, nil, alice), 404)
	}
}

func TestAccountPreviewTransferLimit(t *testing.T) {
	f := setup(t)
	n := putFile(t, f, f.alice, f.alice.HomeID, "photo.png", testPNG(t))
	headers := browser(t, f, f.alice)
	for i := 0; i < cap(f.a.downloadSlots); i++ {
		f.a.downloadSlots <- struct{}{}
	}
	for _, suffix := range []string{"", "/content"} {
		status(t, request(t, f.a.Public(), http.MethodGet, "/api/v1/nodes/"+n.ID+"/preview"+suffix, nil, headers), 429)
	}
}
