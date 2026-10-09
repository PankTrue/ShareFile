package httpapi

import (
	"errors"
	"net/http"

	"sharefile/backend/internal/config"
	"sharefile/backend/internal/identity"
	"sharefile/backend/internal/metadata"
	"sharefile/backend/internal/settings"
)

func (a *App) configurationRoutes(mux *http.ServeMux) {
	available := func(w http.ResponseWriter, r *http.Request) bool {
		if a.Settings == nil {
			fail(w, r, metadata.ErrUnavailable)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/v1/admin/config", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		if available(w, r) {
			jsonReply(w, 200, a.Settings.Snapshot())
		}
	}))
	mux.HandleFunc("PUT /api/v1/admin/config", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		if !available(w, r) {
			return
		}
		var in struct {
			Config   config.Config `json:"config"`
			Revision string        `json:"revision"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := a.Settings.Save(in.Config, in.Revision)
		if err != nil {
			configurationError(w, r, err)
			return
		}
		jsonReply(w, 200, out)
	}))
	mux.HandleFunc("DELETE /api/v1/admin/config", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		if !available(w, r) {
			return
		}
		var in struct {
			Revision string `json:"revision"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := a.Settings.Discard(in.Revision); err != nil {
			configurationError(w, r, err)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /api/v1/admin/config/apply", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		if !available(w, r) {
			return
		}
		var in struct {
			Revision string `json:"revision"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := a.Settings.RequestApply(in.Revision); err != nil {
			configurationError(w, r, err)
			return
		}
		jsonReply(w, 202, map[string]string{"status": "applying"})
	}))
	mux.HandleFunc("GET /api/v1/admin/volumes", a.auth(true, func(w http.ResponseWriter, r *http.Request, u identity.Actor) {
		jsonReply(w, 200, settings.Volumes(a.Config.DataDir))
	}))
}

func configurationError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, metadata.ErrConflict) {
		jsonReply(w, 409, map[string]string{"error": "Настройки изменились. Обновите страницу и повторите действие."})
		return
	}
	if errors.Is(err, metadata.ErrInvalid) {
		fail(w, r, err)
		return
	}
	// These routes are private and browser-admin-only; validation details help fix paths.
	jsonReply(w, 400, map[string]string{"error": err.Error()})
}
