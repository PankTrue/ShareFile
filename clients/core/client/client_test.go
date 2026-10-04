package client

import (
	"bytes"
	"context"
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
)

var testFolder = strings.Repeat("f", 43)
var testFile = strings.Repeat("n", 43)
var testPath = "/api/v1/uploads/" + strings.Repeat("u", 43)

func TestTrustedOriginAndResourcePaths(t *testing.T) {
	for _, raw := range []string{"http://example.org", "https://user:pass@example.org", "https://example.org/api", "https://example.org?secret=x", "https://example.org#x", "file:///tmp"} {
		if _, err := New(raw, "secret"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"https://example.org/", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := New(raw, ""); err != nil {
			t.Fatal(err)
		}
	}
	if path, err := UploadPath("https://untrusted.example" + testPath); err != nil || path != testPath {
		t.Fatal(path, err)
	}
	for _, raw := range []string{"//bad/path", "https://user:secret@x" + testPath, testPath + "?secret=x", testPath + "/..", testPath + "#x"} {
		if _, err := UploadPath(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

type fixture struct {
	mu                  sync.Mutex
	data                []byte
	size, offset, chunk int64
	created, patches    int
	key                 string
	authorized          bool
}

func (f *fixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.authorized || r.Header.Get("Authorization") != "Bearer test-device-token" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/nodes/" + testFolder:
			io.WriteString(w, `{"id":"`+testFolder+`","kind":"folder","effective_role":3}`)
		case "/api/v1/uploads":
			if r.Method != "POST" {
				t.Error("not POST")
			}
			if f.key != "" && f.key != r.Header.Get("Idempotency-Key") {
				t.Error("idempotency key changed")
			}
			f.key = r.Header.Get("Idempotency-Key")
			f.created++
			w.Header().Set("Location", "https://different-origin.example"+testPath)
			w.Header().Set("Sharefile-File-ID", testFile)
			w.WriteHeader(201)
		case testPath:
			w.Header().Set("Sharefile-File-ID", testFile)
			if r.Method == "POST" && r.Header.Get("X-HTTP-Method-Override") == "PATCH" {
				body, _ := io.ReadAll(r.Body)
				f.data = append(f.data, body...)
				f.offset += int64(len(body))
				f.patches++
				w.WriteHeader(204)
				return
			}
			if r.Method != "HEAD" {
				t.Error("unexpected request")
			}
			w.Header().Set("Upload-Offset", strconv64(f.offset))
			w.Header().Set("Upload-Length", strconv64(f.size))
			w.Header().Set("Sharefile-Chunk-Size", strconv64(f.chunk))
			state := "open"
			if f.offset == f.size {
				state = "complete"
			}
			w.Header().Set("Sharefile-Upload-State", state)
		default:
			t.Errorf("unrecognized path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
}
func strconv64(v int64) string { return fmt.Sprintf("%d", v) }

type lostResponse struct {
	base          http.RoundTripper
	create, patch bool
	mu            sync.Mutex
}

func (l *lostResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := l.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	drop := false
	if r.URL.Path == "/api/v1/uploads" && !l.create {
		l.create = true
		drop = true
	}
	if r.Header.Get("X-HTTP-Method-Override") == "PATCH" && !l.patch {
		l.patch = true
		drop = true
	}
	if drop {
		resp.Body.Close()
		return nil, errors.New("lost response")
	}
	return resp, nil
}
func jobFor(t *testing.T, payload []byte) Job {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Отчёт с пробелами 日本語.bin")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	j, err := Prepare(path, testFolder)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func TestLostCreationAndPatchResponsesResumeWithoutDuplicates(t *testing.T) {
	payload := bytes.Repeat([]byte("abc"), 5000)
	j := jobFor(t, payload)
	f := &fixture{size: int64(len(payload)), chunk: 4096, authorized: true}
	s := f.server(t)
	defer s.Close()
	c, _ := New(s.URL, "test-device-token")
	c.http.Transport = &lostResponse{base: c.http.Transport}
	if err := c.Upload(context.Background(), &j, func(Job) {}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.data, payload) || f.created != 2 || j.Offset != j.Size {
		t.Fatalf("wrong recovery: creates=%d offset=%d", f.created, j.Offset)
	}
	if j.Upload != testPath {
		t.Fatal("followed external Location")
	}
}
func TestCheckpointRestartAndChangedSource(t *testing.T) {
	payload := bytes.Repeat([]byte("s"), 20000)
	j := jobFor(t, payload)
	f := &fixture{size: j.Size, chunk: 4096, authorized: true}
	s := f.server(t)
	defer s.Close()
	c, _ := New(s.URL, "test-device-token")
	ctx, cancel := context.WithCancel(context.Background())
	err := c.Upload(ctx, &j, func(cp Job) {
		if cp.Offset >= 4096 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	checkpoint, _ := json.Marshal(j)
	var restored Job
	json.Unmarshal(checkpoint, &restored)
	if bytes.Contains(checkpoint, []byte("test-device-token")) {
		t.Fatal("credential in checkpoint")
	}
	if err = c.Upload(context.Background(), &restored, func(Job) {}); err != nil {
		t.Fatal(err)
	}
	if f.created != 1 || !bytes.Equal(payload, f.data) {
		t.Fatal("duplicate creation or corrupted resume")
	}
	os.WriteFile(j.Source, bytes.Repeat([]byte("x"), len(payload)), 0600)
	if err = c.Upload(context.Background(), &j, func(Job) {}); err == nil {
		t.Fatal("accepted modified source")
	}
}
func TestLargeSparseFileAndRevokedDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const size = int64(5<<30) + 3
	if err = file.Truncate(size); err != nil {
		t.Fatal(err)
	}
	file.WriteAt([]byte("end"), size-3)
	file.Close()
	j, err := Prepare(path, testFolder)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{size: size, offset: size - 3, chunk: 16 << 20, authorized: true}
	s := f.server(t)
	defer s.Close()
	c, _ := New(s.URL, "test-device-token")
	if err = c.Upload(context.Background(), &j, func(Job) {}); err != nil {
		t.Fatal(err)
	}
	if string(f.data) != "end" || j.Offset != size {
		t.Fatal("large offset truncated")
	}
	f.mu.Lock()
	f.authorized = false
	f.mu.Unlock()
	if _, err = c.Me(context.Background()); err == nil {
		t.Fatal("revoked token still accepted")
	}
}
func TestRedirectDoesNotForwardCredential(t *testing.T) {
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL+"/api/v1/me", 307) }))
	defer source.Close()
	c, _ := New(source.URL, "private-token")
	_, err := c.Me(context.Background())
	if err == nil || hit {
		t.Fatal("followed authenticated redirect")
	}
}
func TestShareIsNeverRetriedAndNoTransportSecretsInError(t *testing.T) {
	count := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count++; w.WriteHeader(503) }))
	defer s.Close()
	c, _ := New(s.URL, "secret")
	_, err := c.Share(context.Background(), testFile, ShareOptions{Days: 7})
	if err == nil || count != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatal(count, err)
	}
}

func TestMetadataCancellationAndTimeout(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer s.Close()
	c, _ := New(s.URL, "private-token")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Me(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "private-token") {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err = c.Me(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
