package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	inofy "github.com/ProjectViVy/inofy"
)

// postRun admits a run: an exact published revision
// (workflow+revision) or an exact draft-ETag snapshot
// (workflow+draft_etag). Idempotency-Key scopes dedup within
// (principal, workflow). The committed row carries the immutable
// artifact bytes — a later draft edit can never change the run.
func (h *handler) postRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Workflow  string          `json:"workflow"`
		Revision  uint64          `json:"revision"`
		DraftETag string          `json:"draft_etag"`
		Input     json.RawMessage `json:"input"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "bad body", nil)
		return
	}
	if body.Workflow == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "workflow required", nil)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Idempotency-Key required", nil)
		return
	}
	if len(body.Input) == 0 {
		body.Input = json.RawMessage(`{}`)
	}
	var source json.RawMessage
	var revision uint64
	switch {
	case body.Revision > 0:
		rev, err := h.d.Store.GetRevision(r.Context(), body.Workflow, body.Revision)
		if err != nil {
			mapErr(w, err)
			return
		}
		source, _ = json.Marshal(rev.Artifact)
		revision = rev.Revision
	case body.DraftETag != "":
		d, err := h.d.Store.GetDraft(r.Context(), body.Workflow)
		if err != nil {
			mapErr(w, err)
			return
		}
		if d.ETag != body.DraftETag {
			writeError(w, http.StatusPreconditionFailed, "revision_conflict", "draft etag mismatch", nil)
			return
		}
		source, _ = json.Marshal(d.Artifact)
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "revision or draft_etag required", nil)
		return
	}
	if h.d.Dispatch == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "dispatcher not wired", nil)
		return
	}
	runID, err := h.d.Dispatch.Admit(r.Context(), "owner", body.Workflow, revision, source, body.Input, key)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}

func (h *handler) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	items, next, err := h.d.Store.ListRuns(r.Context(), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		mapErr(w, err)
		return
	}
	out := map[string]any{"items": items}
	if next != "" {
		out["next_cursor"] = next
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, err := h.d.Store.Load(r.Context(), id)
	if err != nil {
		mapErr(w, err)
		return
	}
	out := map[string]any{
		"run_id": id, "status": st.Status,
		"epoch": st.Ref.Epoch,
	}
	if st.LatestCheckpoint != nil {
		out["generation"] = st.LatestCheckpoint.ContinuationGeneration
	}
	if len(st.Waits) > 0 {
		out["waits"] = st.Waits
	}
	writeJSON(w, http.StatusOK, out)
}

// getNodeOutput returns the committed protected output for a node —
// authorized by the session, never by knowledge of an operation key.
func (h *handler) getNodeOutput(w http.ResponseWriter, r *http.Request) {
	id, key := r.PathValue("id"), r.PathValue("key")
	out, err := h.d.Store.ProtectedOutput(r.Context(), id, key)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "node": key, "output": out})
}

// postCancel is idempotent: does not promise immediate termination.
func (h *handler) postCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.d.Dispatch == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "dispatcher not wired", nil)
		return
	}
	if err := h.d.Dispatch.Cancel(r.Context(), id); err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postResume continues a waiting run with the complete authorized
// answer set (§8.4 fenced resume).
func (h *handler) postResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Answers map[string]json.RawMessage `json:"answers"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.Answers) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "answers required", nil)
		return
	}
	st, err := h.d.Store.Load(r.Context(), id)
	if err != nil {
		mapErr(w, err)
		return
	}
	prog, err := h.d.Dispatch.ProgramFor(r.Context(), id)
	if err != nil {
		mapErr(w, err)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	res, err := prog.Run(r.Context(), inofy.RunRequest{
		Ref:    st.Ref,
		Resume: &inofy.ResumeRequest{IdempotencyKey: key, Answers: body.Answers},
	}, inofy.Bindings{})
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "status": res.Status})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

var _ = context.Background
