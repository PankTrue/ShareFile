package httpapi

import (
	"net/http"

	"sharefile/backend/internal/identity"
)

func (a *App) batchNodes(w http.ResponseWriter, r *http.Request, u identity.Actor) {
	var in struct {
		Action   string   `json:"action"`
		NodeIDs  []string `json:"node_ids"`
		ParentID string   `json:"parent_id"`
		Name     string   `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	folder, err := a.Files.Batch(r.Context(), u.ID, in.Action, in.NodeIDs, in.ParentID, in.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Action == "collect" {
		a.replyNode(w, r, u.ID, folder, http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}
