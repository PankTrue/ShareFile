package httpapi

import (
	"net/http"
	"path/filepath"

	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
)

func (a *App) adminRoutes(mux *http.ServeMux) {
	a.configurationRoutes(mux)
	mux.HandleFunc("GET /api/v1/admin/users", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		out, err := a.Identity.Users(r.Context())
		respond(w, r, out, err)
	}))
	mux.HandleFunc("POST /api/v1/admin/users", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Admin    bool   `json:"admin"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Identity.CreateUser(r.Context(), in.Username, in.Password, in.Admin)
		if err != nil {
			fail(w, r, err)
			return
		}
		jsonReply(w, 201, out)
	}))
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		var in struct {
			Active   *bool  `json:"active"`
			Password string `json:"password"`
		}
		if !decode(w, r, &in) {
			return
		}
		noContent(w, r, a.Identity.UpdateUser(r.Context(), r.PathValue("id"), in.Active, in.Password))
	}))
	mux.HandleFunc("GET /api/v1/admin/groups", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		out, err := a.Access.Groups(r.Context())
		respond(w, r, out, err)
	}))
	mux.HandleFunc("POST /api/v1/admin/groups", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		var in struct {
			Name string `json:"name"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Access.CreateGroup(r.Context(), in.Name)
		if err != nil {
			fail(w, r, err)
			return
		}
		jsonReply(w, 201, out)
	}))
	mux.HandleFunc("GET /api/v1/admin/groups/{id}/members", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		rows, err := a.Store.DB.QueryContext(r.Context(), "SELECT user_id FROM memberships WHERE group_id=? ORDER BY user_id", r.PathValue("id"))
		if err != nil {
			fail(w, r, err)
			return
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				fail(w, r, err)
				return
			}
			ids = append(ids, id)
		}
		respond(w, r, ids, rows.Err())
	}))
	mux.HandleFunc("PUT /api/v1/admin/groups/{id}/members/{user}", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		noContent(w, r, a.Access.SetMember(r.Context(), r.PathValue("id"), r.PathValue("user"), true))
	}))
	mux.HandleFunc("DELETE /api/v1/admin/groups/{id}/members/{user}", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		noContent(w, r, a.Access.SetMember(r.Context(), r.PathValue("id"), r.PathValue("user"), false))
	}))
	mux.HandleFunc("GET /api/v1/admin/settings", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		var seconds int64 = 7 * 86400
		_ = a.Store.DB.QueryRowContext(r.Context(), "SELECT value FROM settings WHERE key='upload_ttl_seconds'").Scan(&seconds)
		jsonReply(w, 200, map[string]any{"upload_ttl_seconds": seconds, "chunk_bytes": a.Config.ChunkBytes, "upload_slots": a.Config.UploadSlots, "download_slots": a.Config.DownloadSlots, "reserve_bytes": a.Config.ReserveBytes, "import_roots": len(a.Config.ImportDirs)})
	}))
	mux.HandleFunc("PATCH /api/v1/admin/settings", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		var in struct {
			UploadTTL int64 `json:"upload_ttl_seconds"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.UploadTTL < 3600 || in.UploadTTL > 365*86400 {
			fail(w, r, metadata.ErrInvalid)
			return
		}
		_, err := a.Store.DB.ExecContext(r.Context(), "INSERT INTO settings VALUES('upload_ttl_seconds',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", in.UploadTTL)
		noContent(w, r, err)
	}))
	mux.HandleFunc("POST /api/v1/admin/import", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		var in struct {
			Root     int    `json:"root"`
			Path     string `json:"path"`
			FolderID string `json:"folder_id"`
			Name     string `json:"name"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.Root < 0 || in.Root >= len(a.Config.ImportDirs) || !filepath.IsLocal(in.Path) {
			fail(w, r, metadata.ErrInvalid)
			return
		}
		release := gate(w, r, a.uploadSlots)
		if release == nil {
			return
		}
		defer release()
		out, err := a.Files.Import(r.Context(), u.ID, in.FolderID, in.Name, a.Config.ImportDirs[in.Root], in.Path)
		if err != nil {
			fail(w, r, err)
			return
		}
		jsonReply(w, 201, out)
	}))
}
