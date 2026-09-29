package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/definitions"
)

func (h *handler) listWorkflows(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("cursor")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	page, err := h.d.Store.List(r.Context(), cursor, limit)
	if err != nil {
		mapErr(w, err)
		return
	}
	out := map[string]any{"items": page.Revisions}
	if page.NextCursor != "" {
		out["next_cursor"] = page.NextCursor
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) getDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, err := h.d.Store.GetDraft(r.Context(), id)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workflow": id, "etag": d.ETag, "artifact": d.Artifact,
	})
}

// putDraft saves a draft under ETag CAS: If-None-Match: * creates,
// If-Match: <etag> updates. The returned ETag snapshots the draft.
func (h *handler) putDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, diags, err := inofy.DecodeArtifact(readBody(r))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_definition", err.Error(), diags)
		return
	}
	etag := definitions.ETagAbsent
	if m := strings.TrimSpace(r.Header.Get("If-Match")); m != "" {
		etag = m
	} else if r.Header.Get("If-None-Match") == "*" {
		etag = definitions.ETagAbsent
	} else {
		// No CAS header → require explicit create sentinel.
		writeError(w, http.StatusBadRequest, "invalid_request", "If-Match or If-None-Match required", nil)
		return
	}
	saved, err := h.d.Definitions.SaveDraft(r.Context(), id, etag, a)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"etag": saved.ETag})
}

// postValidate compiles the draft against the frozen catalog and
// returns its diagnostics verbatim — never secrets.
func (h *handler) postValidate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	etag := strings.TrimSpace(r.Header.Get("If-Match"))
	diags, err := h.d.Definitions.ValidateDraft(r.Context(), id, etag, h.d.Catalog)
	if err != nil {
		if _, ok := err.(*inofy.Error); ok {
			mapErr(w, err)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "invalid_definition", "definition failed validation", diags)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "diagnostics": diags})
}

// postPublish allocates the next immutable revision for the draft.
func (h *handler) postPublish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	etag := strings.TrimSpace(r.Header.Get("If-Match"))
	if etag == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "If-Match required", nil)
		return
	}
	rev, err := h.d.Definitions.Publish(r.Context(), id, etag, h.d.Catalog)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workflow": rev.WorkflowID, "revision": rev.Revision,
		"definition_digest": rev.DefinitionDigest,
	})
}

func (h *handler) getRevision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rev, err := strconv.ParseUint(r.PathValue("rev"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "bad revision", nil)
		return
	}
	v, err := h.d.Store.GetRevision(r.Context(), id, rev)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workflow": id, "revision": v.Revision, "artifact": v.Artifact,
		"definition_digest":   v.DefinitionDigest,
		"used_catalog_digest": v.UsedCatalogDigest,
	})
}

func readBody(r *http.Request) []byte {
	defer r.Body.Close()
	var buf [8 << 20]byte
	n, _ := r.Body.Read(buf[:])
	b := buf[:n]
	for {
		m, err := r.Body.Read(buf[:])
		if m == 0 || err != nil {
			break
		}
		b = append(b, buf[:m]...)
	}
	return b
}
