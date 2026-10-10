package httpapi

import (
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

func (a *App) guestNode(w http.ResponseWriter, r *http.Request) (shares.Share, metadata.Node, bool) {
	token := r.PathValue("token")
	session := ""
	if c, err := r.Cookie(a.guestCookie(token)); err == nil {
		session = c.Value
	}
	sh, err := a.Shares.Authorize(r.Context(), token, session)
	if err == shares.ErrPassword {
		renderShare(w, r, http.StatusUnauthorized, sharePageData{Title: "Пароль ссылки", Base: "/s/" + token, Locked: true})
		return sh, metadata.Node{}, false
	}
	if err != nil {
		fail(w, r, err)
		return sh, metadata.Node{}, false
	}
	n, err := a.Shares.ResolveNode(r.Context(), sh, r.PathValue("id"))
	if err != nil {
		fail(w, r, err)
		return sh, n, false
	}
	return sh, n, true
}

func (a *App) guest(w http.ResponseWriter, r *http.Request) {
	sh, n, ok := a.guestNode(w, r)
	if !ok {
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
	data, err := a.sharePage(r, sh, n)
	if err != nil {
		fail(w, r, err)
		return
	}
	data.Nodes = out
	data.Previous, data.Next = offset > 0, len(out) == limit
	data.PreviousOffset, data.NextOffset, data.Limit = max(0, offset-limit), offset+limit, limit
	renderShare(w, r, http.StatusOK, data)
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
