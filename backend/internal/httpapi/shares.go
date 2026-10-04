package httpapi

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/security"
	"sharefile/backend/internal/shares"
)

func (a *App) createShare(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	var in struct {
		NodeID    string `json:"node_id"`
		Password  string `json:"password"`
		ExpiresAt *int64 `json:"expires_at"`
		Never     bool   `json:"never_expires"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Never && in.ExpiresAt != nil {
		fail(w, r, metadata.ErrInvalid)
		return
	}
	if !in.Never && in.ExpiresAt == nil {
		expiry := time.Now().Add(7 * 24 * time.Hour).Unix()
		in.ExpiresAt = &expiry
	}
	out, token, err := a.Shares.Create(r.Context(), u.ID, in.NodeID, in.Password, in.ExpiresAt)
	if err != nil {
		fail(w, r, err)
		return
	}
	jsonReply(w, 201, map[string]any{"share": out, "url": strings.TrimRight(a.Config.PublicURL, "/") + "/s/" + token})
}
func (a *App) guestCookie(token string) string {
	prefix := "__Secure-sf-share-"
	if a.Config.InsecureLocal {
		prefix = "sf-share-"
	}
	return prefix + security.Digest(token)[:16]
}

var passwordPage = template.Must(template.New("password").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><title>ShareFile</title><h1>Пароль ссылки</h1><form method="post" action="{{.}}/unlock"><label>Пароль <input type="password" name="password" required maxlength="256" autocomplete="current-password"></label><button type="submit">Открыть</button></form></html>`))
var folderPage = template.Must(template.New("folder").Parse(`<!doctype html><html lang="ru"><meta charset="utf-8"><title>{{.Name}} — ShareFile</title><h1>{{.Name}}</h1><ul>{{range .Nodes}}<li><a href="{{$.Base}}/nodes/{{.ID}}">{{.Name}}</a>{{if eq .Kind "file"}} ({{.Size}} байт){{end}}</li>{{end}}</ul>{{if .Previous}}<a href="?offset={{.PreviousOffset}}">Назад</a>{{end}}{{if .Next}} <a href="?offset={{.NextOffset}}">Далее</a>{{end}}</html>`))

func (a *App) guest(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	session := ""
	if c, err := r.Cookie(a.guestCookie(token)); err == nil {
		session = c.Value
	}
	sh, err := a.Shares.Authorize(r.Context(), token, session)
	if err == shares.ErrPassword {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(401)
		_ = passwordPage.Execute(w, "/s/"+token)
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	n, err := a.Shares.ResolveNode(r.Context(), sh, r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if n.Kind == "file" {
		release := gate(w, r, a.downloadSlots)
		if release == nil {
			return
		}
		defer release()
		f, err := a.Files.Disk.Root.Open("objects/" + n.ID)
		if err != nil {
			fail(w, r, metadata.ErrUnavailable)
			return
		}
		defer f.Close()
		serveFile(w, r, n, f)
		return
	}
	limit, offset, err := pagination(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.Files.List(r.Context(), sh.UserID, n.ID, limit, offset)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct {
		Name, Base                 string
		Nodes                      []metadata.Node
		Previous, Next             bool
		PreviousOffset, NextOffset int
	}{n.Name, "/s/" + token, out, offset > 0, len(out) == limit, max(0, offset-limit), offset + limit}
	_ = folderPage.Execute(w, data)
}
func (a *App) unlock(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
	if !checkOrigin(r) {
		fail(w, r, metadata.ErrForbidden)
		return
	}
	token := r.PathValue("token")
	if !a.allow("unlock:" + peer(r) + ":" + security.Digest(token)) {
		limited(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		fail(w, r, metadata.ErrInvalid)
		return
	}
	session, err := a.Shares.Unlock(r.Context(), token, r.PostForm.Get("password"))
	if err != nil {
		fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: a.guestCookie(token), Value: session, Path: "/s/" + token, Secure: !a.Config.InsecureLocal, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	http.Redirect(w, r, "/s/"+token, http.StatusSeeOther)
}
