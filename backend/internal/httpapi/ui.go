package httpapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"sharefile/backend/internal/access"
	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
)

// Views describe available actions; each mutation still independently enforces access.
type nodeView struct {
	metadata.Node
	EffectiveRole int `json:"effective_role"`
}

func (a *App) viewNode(r *http.Request, user string, n metadata.Node) (nodeView, error) {
	role, err := access.Role(r.Context(), a.Store.DB, user, n.ID)
	return nodeView{n, role}, err
}
func (a *App) replyNode(w http.ResponseWriter, r *http.Request, user string, n metadata.Node, status int) {
	v, err := a.viewNode(r, user, n)
	if err != nil {
		fail(w, r, err)
		return
	}
	jsonReply(w, status, v)
}

type principal struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Only folder managers may search this bounded directory; no account details or memberships are exposed.
func (a *App) principals(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	id := r.PathValue("id")
	if err := access.Require(r.Context(), a.Store.DB, u.ID, id, access.Manager); err != nil {
		fail(w, r, err)
		return
	}
	n, err := metadata.GetNode(r.Context(), a.Store.DB, id)
	if err != nil || n.Kind != "folder" {
		if err == nil {
			err = metadata.ErrInvalid
		}
		fail(w, r, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	limit, offset, err := pagination(r)
	if err != nil || len(query) > 128 || !utf8.ValidString(query) {
		fail(w, r, metadata.ErrInvalid)
		return
	}
	rows, err := a.Store.DB.QueryContext(r.Context(), `SELECT type,id,name FROM (
	 SELECT 'user' AS type,id,username AS name FROM users WHERE active=1
	 UNION ALL SELECT 'group',id,name FROM groups
	) WHERE instr(lower(name),lower(?))>0 ORDER BY name,type,id LIMIT ? OFFSET ?`, query, limit, offset)
	if err != nil {
		fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []principal{}
	for rows.Next() {
		var p principal
		if err = rows.Scan(&p.Type, &p.ID, &p.Name); err != nil {
			fail(w, r, err)
			return
		}
		out = append(out, p)
	}
	respond(w, r, out, rows.Err())
}

func (a *App) namedGrants(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	id := r.PathValue("id")
	grants, err := a.Access.Grants(r.Context(), u.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	type namedGrant struct {
		access.Grant
		PrincipalName string `json:"principal_name"`
	}
	out := make([]namedGrant, 0, len(grants))
	for _, g := range grants {
		var name string
		query := "SELECT username FROM users WHERE id=?"
		if g.PrincipalType == "group" {
			query = "SELECT name FROM groups WHERE id=?"
		}
		if err := a.Store.DB.QueryRowContext(r.Context(), query, g.PrincipalID).Scan(&name); err != nil {
			fail(w, r, err)
			return
		}
		out = append(out, namedGrant{g, name})
	}
	jsonReply(w, 200, out)
}
