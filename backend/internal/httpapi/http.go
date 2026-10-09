// Package httpapi owns HTTP transport; administrative routes exist only on its private mux.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/config"
	"sharefile/backend/internal/files"
	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
	"sharefile/backend/internal/settings"
	"sharefile/backend/internal/shares"
)

type App struct {
	Config                     config.Config
	Store                      *metadata.Store
	Identity                   *identity.Service
	Access                     *access.Service
	Files                      *files.Service
	Shares                     *shares.Service
	Settings                   *settings.Manager
	uploadSlots, downloadSlots chan struct{}
	mu                         sync.Mutex
	attempts                   map[string]bucket
}
type bucket struct {
	tokens float64
	at     time.Time
}

func New(c config.Config, store *metadata.Store, disk *files.Disk) (*App, error) {
	passwords := security.NewPasswords()
	ident, err := identity.New(store, passwords)
	if err != nil {
		return nil, err
	}
	fs := &files.Service{Store: store, Disk: disk, ChunkBytes: c.ChunkBytes}
	app := &App{Config: c, Store: store, Identity: ident, Access: &access.Service{Store: store}, Files: fs, Shares: &shares.Service{Store: store, Files: fs, Passwords: passwords}, uploadSlots: make(chan struct{}, c.UploadSlots), downloadSlots: make(chan struct{}, c.DownloadSlots), attempts: make(map[string]bucket)}
	return app, nil
}

type authenticated func(http.ResponseWriter, *http.Request, identity.Actor)

func (a *App) Public() http.Handler  { return a.handler(false) }
func (a *App) Private() http.Handler { return a.handler(true) }
func (a *App) handler(private bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ui", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, 200, map[string]any{"admin_listener": private, "chunk_bytes": a.Config.ChunkBytes, "upload_slots": a.Config.UploadSlots})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, 200, map[string]any{"status": "alive", "api_version": "v1"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Files.Disk.Check(); err != nil {
			fail(w, r, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := a.Store.DB.PingContext(ctx); err != nil {
			fail(w, r, metadata.ErrUnavailable)
			return
		}
		jsonReply(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) { a.login(w, r, private) })
	mux.HandleFunc("GET /api/v1/me", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		jsonReply(w, 200, map[string]any{"user": u.User, "csrf_token": u.CSRF, "admin_listener": private})
	}))
	mux.HandleFunc("POST /api/v1/auth/logout", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		if u.Device {
			fail(w, r, metadata.ErrForbidden)
			return
		}
		if err := a.Identity.Logout(r.Context(), u); err != nil {
			fail(w, r, err)
			return
		}
		a.setSession(w, "", -1)
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /api/v1/device-requests", a.startDevice)
	mux.HandleFunc("POST /api/v1/device-requests/{id}/token", a.pollDevice)
	mux.HandleFunc("GET /api/v1/device-requests/{id}", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		if u.Device {
			fail(w, r, metadata.ErrForbidden)
			return
		}
		request, err := a.Identity.DeviceRequest(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, r, err)
			return
		}
		jsonReply(w, 200, request)
	}))
	mux.HandleFunc("POST /api/v1/device-requests/{id}/approve", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		if u.Device {
			fail(w, r, metadata.ErrForbidden)
			return
		}
		if err := a.Identity.ApproveDevice(r.Context(), u.ID, r.PathValue("id")); err != nil {
			fail(w, r, err)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /api/v1/devices", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		out, err := a.Identity.Devices(r.Context(), u.ID)
		respond(w, r, out, err)
	}))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		noContent(w, r, a.Identity.RevokeDevice(r.Context(), u.ID, r.PathValue("id")))
	}))
	mux.HandleFunc("GET /api/v1/folders", a.auth(false, a.listNodes))
	mux.HandleFunc("GET /api/v1/search", a.auth(false, a.searchNodes))
	mux.HandleFunc("POST /api/v1/folders", a.auth(false, a.createFolder))
	mux.HandleFunc("GET /api/v1/nodes/{id}", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		n, err := a.Files.Node(r.Context(), u.ID, r.PathValue("id"))
		if err != nil {
			fail(w, r, err)
			return
		}
		a.replyNode(w, r, u.ID, n, 200)
	}))
	mux.HandleFunc("PATCH /api/v1/nodes/{id}", a.auth(false, a.moveNode))
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		noContent(w, r, a.Files.Delete(r.Context(), u.ID, r.PathValue("id")))
	}))
	mux.HandleFunc("GET /api/v1/nodes/{id}/content", a.auth(false, a.download))
	mux.HandleFunc("GET /api/v1/nodes/{id}/preview", a.auth(false, a.preview))
	mux.HandleFunc("GET /api/v1/nodes/{id}/preview/content", a.auth(false, a.previewContent))
	mux.HandleFunc("GET /api/v1/nodes/{id}/archive", a.auth(false, a.downloadArchive))
	mux.HandleFunc("GET /api/v1/nodes/{id}/grants", a.auth(false, a.namedGrants))
	mux.HandleFunc("GET /api/v1/nodes/{id}/principals", a.auth(false, a.principals))
	mux.HandleFunc("PUT /api/v1/nodes/{id}/grants", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		var g access.Grant
		if !decode(w, r, &g) {
			return
		}
		noContent(w, r, a.Access.SetGrant(r.Context(), u.ID, r.PathValue("id"), g))
	}))
	mux.HandleFunc("GET /api/v1/trash", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		limit, offset, err := pagination(r)
		if err != nil {
			fail(w, r, err)
			return
		}
		out, err := a.Files.Trash(r.Context(), u.ID, limit, offset)
		respond(w, r, out, err)
	}))
	mux.HandleFunc("POST /api/v1/trash/{id}/restore", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		var in struct {
			ParentID string `json:"parent_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Files.Restore(r.Context(), u.ID, r.PathValue("id"), in.ParentID)
		respond(w, r, out, err)
	}))
	mux.HandleFunc("DELETE /api/v1/trash/{id}", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		noContent(w, r, a.Files.Purge(r.Context(), u.ID, r.PathValue("id")))
	}))
	mux.HandleFunc("/api/v1/uploads", a.auth(false, a.uploads))
	mux.HandleFunc("/api/v1/uploads/{id}", a.auth(false, a.upload))
	mux.HandleFunc("GET /api/v1/shares", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		limit, offset, err := pagination(r)
		if err != nil {
			fail(w, r, err)
			return
		}
		out, err := a.Shares.List(r.Context(), u.ID, r.URL.Query().Get("node_id"), limit, offset)
		respond(w, r, out, err)
	}))
	mux.HandleFunc("POST /api/v1/shares", a.auth(false, a.createShare))
	mux.HandleFunc("DELETE /api/v1/shares/{id}", a.auth(false, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		noContent(w, r, a.Shares.Revoke(r.Context(), u.ID, r.PathValue("id")))
	}))
	mux.HandleFunc("GET /s/{token}", a.guest)
	mux.HandleFunc("GET /s/{token}/nodes/{id}", a.guest)
	mux.HandleFunc("GET /s/{token}/nodes/{id}/view", a.guestView)
	mux.HandleFunc("GET /s/{token}/nodes/{id}/preview", a.guestPreview)
	mux.HandleFunc("GET /s/{token}/archive", a.guestArchive)
	mux.HandleFunc("GET /s/{token}/nodes/{id}/archive", a.guestArchive)
	mux.HandleFunc("POST /s/{token}/unlock", a.unlock)
	mux.HandleFunc("GET /share-assets/style.css", shareStyles)
	if private {
		a.adminRoutes(mux)
	}
	if a.Config.WebDir != "" {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/s/") {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			clean := filepath.Clean("/" + r.URL.Path)
			name := filepath.Join(a.Config.WebDir, clean)
			if info, err := os.Stat(name); err != nil || info.IsDir() {
				if filepath.Ext(clean) != "" {
					http.NotFound(w, r)
					return
				}
				name = filepath.Join(a.Config.WebDir, "index.html")
			}
			http.ServeFile(w, r, name)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				if err == http.ErrAbortHandler {
					panic(err)
				}
				slog.Error("HTTP handler panic", "route", r.Pattern)
				fail(w, r, errors.New("internal error"))
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/api/v1/uploads" || strings.HasPrefix(r.URL.Path, "/api/v1/uploads/") {
			tusHeaders(w)
			w.Header().Set("Sharefile-Chunk-Size", strconv.FormatInt(a.Config.ChunkBytes, 10))
			if override := r.Header.Get("X-HTTP-Method-Override"); override != "" {
				switch override {
				case "OPTIONS", "POST", "HEAD", "PATCH", "DELETE":
					r.Method = override
				default:
					fail(w, r, metadata.ErrInvalid)
					return
				}
			}
		}
		origin := a.Config.PublicURL
		if private {
			origin = a.Config.AdminURL
		}
		r = r.WithContext(context.WithValue(r.Context(), originKey{}, strings.TrimRight(origin, "/")))
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			if err := a.Files.Disk.Check(); err != nil {
				fail(w, r, err)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

type originKey struct{}

func checkOrigin(r *http.Request) bool {
	expected, _ := r.Context().Value(originKey{}).(string)
	origin := r.Header.Get("Origin")
	return (origin == "" || origin == expected) && r.Header.Get("Sec-Fetch-Site") != "cross-site"
}
func (a *App) auth(adminOnly bool, h authenticated) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := ""
		device := false
		if header := r.Header.Get("Authorization"); header != "" {
			if !strings.HasPrefix(header, "Bearer ") {
				jsonReply(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
			token = strings.TrimPrefix(header, "Bearer ")
			device = true
		} else if cookie, err := r.Cookie(a.sessionName()); err == nil {
			token = cookie.Value
		}
		u, err := a.Identity.Authenticate(r.Context(), token, device)
		if err != nil {
			jsonReply(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		if adminOnly && (!u.Admin || u.Device) {
			fail(w, r, metadata.ErrForbidden)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && !device {
			if !checkOrigin(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(u.CSRF)) != 1 {
				fail(w, r, metadata.ErrForbidden)
				return
			}
		}
		h(w, r, u)
	}
}
func (a *App) allow(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if len(a.attempts) >= 1024 {
		for k, b := range a.attempts {
			if now.Sub(b.at) > 15*time.Minute {
				delete(a.attempts, k)
			}
		}
		if _, ok := a.attempts[key]; !ok && len(a.attempts) >= 1024 {
			return false
		}
	}
	b, ok := a.attempts[key]
	if !ok {
		b = bucket{6, now}
	}
	b.tokens = min(6, b.tokens+now.Sub(b.at).Seconds()/12)
	b.at = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	a.attempts[key] = b
	return allowed
}
func peer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func limited(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "12")
	jsonReply(w, 429, map[string]string{"error": "try again later"})
}
func (a *App) sessionName() string {
	if a.Config.InsecureLocal {
		return "sharefile_dev"
	}
	return "__Host-sharefile"
}
func (a *App) setSession(w http.ResponseWriter, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: a.sessionName(), Value: token, Path: "/", MaxAge: age, HttpOnly: true, Secure: !a.Config.InsecureLocal, SameSite: http.SameSiteLaxMode})
}
func (a *App) login(w http.ResponseWriter, r *http.Request, private bool) {
	if !checkOrigin(r) {
		fail(w, r, metadata.ErrForbidden)
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !a.allow("login:"+peer(r)) || !a.allow("account:"+strings.ToLower(in.Username)) {
		limited(w, r)
		return
	}
	u, token, csrf, err := a.Identity.Login(r.Context(), in.Username, in.Password)
	if err != nil {
		jsonReply(w, 401, map[string]string{"error": "invalid credentials"})
		return
	}
	if private && !u.Admin {
		_ = a.Identity.Logout(r.Context(), identity.Actor{TokenHash: security.Digest(token)})
		fail(w, r, metadata.ErrForbidden)
		return
	}
	a.setSession(w, token, 86400)
	jsonReply(w, 200, map[string]any{"user": u, "csrf_token": csrf, "admin_listener": private})
}
func (a *App) startDevice(w http.ResponseWriter, r *http.Request) {
	if !a.allow("device:" + peer(r)) {
		limited(w, r)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := a.Identity.StartDevice(r.Context(), in.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	jsonReply(w, 201, map[string]any{"request": out, "verification_url": strings.TrimRight(a.Config.PublicURL, "/") + "/device/connect?request=" + out.ID, "interval": 12})
}
func (a *App) pollDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Secret string `json:"secret"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !a.allow("poll:" + r.PathValue("id")) {
		limited(w, r)
		return
	}
	token, err := a.Identity.PollDevice(r.Context(), r.PathValue("id"), in.Secret)
	if errors.Is(err, identity.ErrPending) {
		jsonReply(w, 202, map[string]string{"status": "pending"})
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	jsonReply(w, 200, map[string]any{"token": token, "token_type": "Bearer", "expires_in": 180 * 86400})
}
func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		jsonReply(w, 415, map[string]string{"error": "application/json required"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		fail(w, r, metadata.ErrInvalid)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(w, r, metadata.ErrInvalid)
		return false
	}
	return true
}
func jsonReply(w http.ResponseWriter, status int, out any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}
func respond(w http.ResponseWriter, r *http.Request, out any, err error) {
	if err != nil {
		fail(w, r, err)
		return
	}
	jsonReply(w, 200, out)
}
func noContent(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code := 500, "internal error"
	switch {
	case errors.Is(err, metadata.ErrNotFound):
		status, code = 404, "not found"
	case errors.Is(err, metadata.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, metadata.ErrInvalid):
		status, code = 400, "invalid request"
	case errors.Is(err, metadata.ErrConflict):
		status, code = 409, "conflict"
	case errors.Is(err, metadata.ErrSpace), errors.Is(err, syscall.ENOSPC), metadata.IsDiskFull(err):
		status, code = 507, "insufficient storage"
	case errors.Is(err, metadata.ErrUnavailable):
		status, code = 503, "storage unavailable"
	case errors.Is(err, shares.ErrPassword):
		status, code = 401, "share password required"
	case errors.Is(err, context.Canceled), errors.Is(err, io.ErrUnexpectedEOF):
		status, code = 400, "transfer interrupted"
	default:
		slog.Error("request failed", "route", r.Pattern, "error", err)
	}
	jsonReply(w, status, map[string]string{"error": code})
}
func pagination(r *http.Request) (int, int, error) {
	limit, offset := 100, 0
	var err error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil {
			return 0, 0, metadata.ErrInvalid
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.Atoi(v)
		if err != nil {
			return 0, 0, metadata.ErrInvalid
		}
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return 0, 0, metadata.ErrInvalid
	}
	return limit, offset, nil
}
