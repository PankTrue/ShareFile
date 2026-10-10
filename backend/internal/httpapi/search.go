package httpapi

import (
	"net/http"
	"strconv"

	"sharefile/backend/internal/files"
	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
)

func (a *App) searchNodes(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	limit, offset, err := pagination(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	values := r.URL.Query()
	o := files.SearchOptions{Query: values.Get("query"), Type: values.Get("type"), View: values.Get("view"), Limit: limit, Offset: offset}
	for _, filter := range []struct {
		name  string
		value **int64
	}{{"created_after", &o.CreatedAfter}, {"created_before", &o.CreatedBefore}, {"min_size", &o.MinSize}, {"max_size", &o.MaxSize}} {
		if raw := values.Get(filter.name); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v < 0 {
				fail(w, r, metadata.ErrInvalid)
				return
			}
			*filter.value = &v
		}
	}
	out, err := a.Files.Search(r.Context(), u.ID, o)
	respond(w, r, out, err)
}
