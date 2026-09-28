// Package httpapi binds the App's authenticated /api/v1 surface
// (architecture §11.4): session exchange, capabilities/node-types,
// draft/validate/publish/revision workflow endpoints and durable run
// inspection. Errors use the {code,message,diagnostics?,run_id?}
// envelope; secrets never appear in any response.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/auth"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/dispatch"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
	"github.com/ProjectViVy/inofy/definitions"
)

// Dependencies wire the App composition into the handler.
type Dependencies struct {
	Auth        *auth.Service
	Store       *storage.Store
	Definitions *definitions.Service
	Dispatch    *dispatch.Service
	Catalog     inofy.Catalog
}

type handler struct {
	d Dependencies
	mux *http.ServeMux
}

// NewHandler returns the authenticated /api/v1 handler.
func NewHandler(d Dependencies) http.Handler {
	h := &handler{d: d, mux: http.NewServeMux()}
	h.routes()
	return d.Auth.RequireAuth(h.mux)
}

func (h *handler) routes() {
	h.mux.HandleFunc("POST /api/v1/session", h.postSession)
	h.mux.HandleFunc("DELETE /api/v1/session", h.deleteSession)
	h.mux.HandleFunc("GET /api/v1/capabilities", h.getCapabilities)
	h.mux.HandleFunc("GET /api/v1/node-types", h.getNodeTypes)
	h.mux.HandleFunc("GET /api/v1/workflows", h.listWorkflows)
	h.mux.HandleFunc("GET /api/v1/workflows/{id}/draft", h.getDraft)
	h.mux.HandleFunc("PUT /api/v1/workflows/{id}/draft", h.putDraft)
	h.mux.HandleFunc("POST /api/v1/workflows/{id}/validate", h.postValidate)
	h.mux.HandleFunc("POST /api/v1/workflows/{id}/publish", h.postPublish)
	h.mux.HandleFunc("GET /api/v1/workflows/{id}/revisions/{rev}", h.getRevision)
	h.mux.HandleFunc("POST /api/v1/runs", h.postRun)
	h.mux.HandleFunc("GET /api/v1/runs", h.listRuns)
	h.mux.HandleFunc("GET /api/v1/runs/{id}", h.getRun)
	h.mux.HandleFunc("GET /api/v1/runs/{id}/events", h.getRunEvents)
	h.mux.HandleFunc("GET /api/v1/runs/{id}/nodes/{key}/output", h.getNodeOutput)
	h.mux.HandleFunc("POST /api/v1/runs/{id}/cancel", h.postCancel)
	h.mux.HandleFunc("POST /api/v1/runs/{id}/resume", h.postResume)
}

// --- session ----------------------------------------------------------

func (h *handler) postSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "token required", nil)
		return
	}
	sid, err := h.d.Auth.Exchange(body.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "invalid token", nil)
		return
	}
	http.SetCookie(w, h.d.Auth.Cookie(sid))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("inofy_session"); err == nil {
		h.d.Auth.Revoke(c.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- capabilities/node-types -----------------------------------------

func (h *handler) getCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": inofy.SchemaVersionV1,
		"features":       []string{"draft", "publish", "runs", "sse", "resume", "cancel"},
	})
}

func (h *handler) getNodeTypes(w http.ResponseWriter, _ *http.Request) {
	types := make([]inofy.NodeDescriptor, 0, len(h.d.Catalog.Types()))
	for _, id := range h.d.Catalog.Types() {
		if d, ok := h.d.Catalog.Lookup(id); ok {
			types = append(types, d)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": types})
}

// --- helpers ----------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string, diags []inofy.Diagnostic) {
	env := map[string]any{"code": code, "message": msg}
	if len(diags) > 0 {
		env["diagnostics"] = diags
	}
	writeJSON(w, status, env)
}

// mapErr maps domain errors onto the §11.4 envelope/status.
func mapErr(w http.ResponseWriter, err error) {
	var ie *inofy.Error
	if errors.As(err, &ie) {
		switch ie.Code {
		case inofy.ErrRevisionConflict:
			writeError(w, http.StatusPreconditionFailed, string(ie.Code), ie.Message, nil)
		case inofy.ErrIdempotencyConflict:
			writeError(w, http.StatusConflict, string(ie.Code), ie.Message, nil)
		case inofy.ErrInvalidDefinition, inofy.ErrSchemaMismatch:
			writeError(w, http.StatusUnprocessableEntity, string(ie.Code), ie.Message, nil)
		case inofy.ErrUnsupportedFeature:
			writeError(w, http.StatusNotImplemented, string(ie.Code), ie.Message, nil)
		default:
			writeError(w, http.StatusBadRequest, string(ie.Code), ie.Message, nil)
		}
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", "internal error", nil)
}

var _ = context.Background
var _ = strings.TrimSpace
