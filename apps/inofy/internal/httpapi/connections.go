package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/ProjectViVy/inofy/apps/inofy/internal/nodes"
)

// --- connections (host-approved provider bindings) --------------

func (h *handler) listConnections(w http.ResponseWriter, _ *http.Request) {
	if h.d.Conns == nil {
		writeJSON(w, http.StatusOK, map[string]any{"connections": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": h.d.Conns.List()})
}

func (h *handler) putConnection(w http.ResponseWriter, r *http.Request) {
	if h.d.Conns == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no connection store", nil)
		return
	}
	var body struct {
		Kind    string `json:"kind"`
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		APIKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid body", nil)
		return
	}
	err := h.d.Conns.Upsert(r.PathValue("id"), nodes.Connection{
		Kind:    body.Kind,
		BaseURL: body.BaseURL,
		Model:   body.Model,
	}, body.APIKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *handler) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if h.d.Conns == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "no connection store", nil)
		return
	}
	if err := h.d.Conns.Delete(r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
