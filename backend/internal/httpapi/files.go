package httpapi

import (
	"encoding/base64"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
)

func (a *App) listNodes(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	limit, offset, err := pagination(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.Files.List(r.Context(), u.ID, r.URL.Query().Get("parent_id"), limit, offset)
	if err != nil {
		fail(w, r, err)
		return
	}
	views := make([]nodeView, 0, len(out))
	for _, n := range out {
		view, err := a.viewNode(r, u.ID, n)
		if err != nil {
			fail(w, r, err)
			return
		}
		views = append(views, view)
	}
	jsonReply(w, 200, views)
}
func (a *App) createFolder(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	var in struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := a.Files.CreateFolderOnce(r.Context(), u.ID, in.ParentID, in.Name, r.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.replyNode(w, r, u.ID, out, 201)
}
func (a *App) moveNode(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	var in struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	noContent(w, r, a.Files.Move(r.Context(), u.ID, r.PathValue("id"), in.ParentID, in.Name))
}
func gate(w http.ResponseWriter, r *http.Request, slots chan struct{}) func() {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }
	default:
		limited(w, r)
		return nil
	}
}
func serveFile(w http.ResponseWriter, r *http.Request, n metadata.Node, f interface {
	Read([]byte) (int, error)
	Seek(int64, int) (int64, error)
}) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": n.Name}))
	w.Header().Set("ETag", strconv.Quote(n.ID))
	http.ServeContent(&idleWriter{ResponseWriter: w}, r, n.Name, time.Unix(n.CreatedAt, 0), f)
}

type idleWriter struct{ http.ResponseWriter }

func (w *idleWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *idleWriter) Write(p []byte) (int, error) {
	_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	return w.ResponseWriter.Write(p)
}
func (a *App) download(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	release := gate(w, r, a.downloadSlots)
	if release == nil {
		return
	}
	defer release()
	n, f, err := a.Files.Open(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	defer f.Close()
	serveFile(w, r, n, f)
}
func tusHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.Header().Set("Tus-Version", "1.0.0")
	w.Header().Set("Tus-Extension", "creation,expiration,termination")
}

func uploadMetaHeader(name, folder, replace string) string {
	metadata := "filename " + base64.StdEncoding.EncodeToString([]byte(name)) + ",folder_id " + base64.StdEncoding.EncodeToString([]byte(folder))
	if replace != "" {
		metadata += ",replace_id " + base64.StdEncoding.EncodeToString([]byte(replace))
	}
	return metadata
}
func tusVersion(w http.ResponseWriter, r *http.Request) bool {
	tusHeaders(w)
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return false
	}
	if r.Header.Get("Tus-Resumable") != "1.0.0" {
		w.WriteHeader(412)
		return false
	}
	return true
}
func uploadMetadata(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		fields := strings.SplitN(strings.TrimSpace(entry), " ", 2)
		if fields[0] == "" {
			return nil, metadata.ErrInvalid
		}
		if _, ok := out[fields[0]]; ok {
			return nil, metadata.ErrInvalid
		}
		value := ""
		if len(fields) == 2 {
			b, err := base64.StdEncoding.DecodeString(fields[1])
			if err != nil {
				return nil, metadata.ErrInvalid
			}
			value = string(b)
		}
		switch fields[0] {
		case "filename", "folder_id", "replace_id":
		default:
			return nil, metadata.ErrInvalid
		}
		out[fields[0]] = value
	}
	return out, nil
}
func (a *App) uploads(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	w.Header().Set("Sharefile-Chunk-Size", strconv.FormatInt(a.Config.ChunkBytes, 10))
	if !tusVersion(w, r) {
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST, OPTIONS")
		w.WriteHeader(405)
		return
	}
	length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || length < 0 || r.Header.Get("Upload-Defer-Length") != "" {
		fail(w, r, metadata.ErrInvalid)
		return
	}
	if r.ContentLength != 0 {
		fail(w, r, metadata.ErrInvalid)
		return
	}
	meta, err := uploadMetadata(r.Header.Get("Upload-Metadata"))
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.Files.CreateUpload(r.Context(), u.ID, meta["folder_id"], meta["filename"], length, meta["replace_id"], r.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Location", strings.TrimRight(a.Config.PublicURL, "/")+"/api/v1/uploads/"+out.ID)
	w.Header().Set("Upload-Expires", time.Unix(out.ExpiresAt, 0).UTC().Format(http.TimeFormat))
	w.Header().Set("Sharefile-File-ID", out.FileID)
	w.WriteHeader(201)
}
func (a *App) upload(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	if !tusVersion(w, r) {
		return
	}
	id := r.PathValue("id")
	switch r.Method {
	case "HEAD":
		out, err := a.Files.Upload(r.Context(), u.ID, id)
		if err != nil {
			fail(w, r, err)
			return
		}
		w.Header().Set("Upload-Length", strconv.FormatInt(out.Length, 10))
		w.Header().Set("Upload-Metadata", uploadMetaHeader(out.Name, out.FolderID, out.ReplaceID))
		w.Header().Set("Upload-Offset", strconv.FormatInt(out.Offset, 10))
		w.Header().Set("Upload-Expires", time.Unix(out.ExpiresAt, 0).UTC().Format(http.TimeFormat))
		w.Header().Set("Sharefile-File-ID", out.FileID)
		w.Header().Set("Sharefile-Upload-State", out.State)
		w.WriteHeader(200)
	case "PATCH":
		if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
			w.WriteHeader(415)
			return
		}
		offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || offset < 0 {
			fail(w, r, metadata.ErrInvalid)
			return
		}
		if r.ContentLength > a.Config.ChunkBytes {
			jsonReply(w, 413, map[string]string{"error": "chunk exceeds Sharefile-Chunk-Size"})
			return
		}
		release := gate(w, r, a.uploadSlots)
		if release == nil {
			return
		}
		defer release()
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(15 * time.Minute))
		r.Body = http.MaxBytesReader(w, r.Body, a.Config.ChunkBytes+1)
		out, err := a.Files.Patch(r.Context(), u.ID, id, offset, r.Body)
		w.Header().Set("Upload-Offset", strconv.FormatInt(out.Offset, 10))
		w.Header().Set("Sharefile-File-ID", out.FileID)
		if err != nil {
			fail(w, r, err)
			return
		}
		w.Header().Set("Upload-Expires", time.Unix(out.ExpiresAt, 0).UTC().Format(http.TimeFormat))
		w.WriteHeader(204)
	case "DELETE":
		noContent(w, r, a.Files.Cancel(r.Context(), u.ID, id))
	default:
		w.Header().Set("Allow", "HEAD, PATCH, DELETE, OPTIONS")
		w.WriteHeader(405)
	}
}
