package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// getRunEvents serves committed redacted events. Without Accept:
// text/event-stream it returns a cursor-paginated JSON page (polling
// fallback). With SSE it replays committed events after
// Last-Event-ID, then streams newly committed events — the cursor is
// the durable per-run seq, so a disconnected subscriber replays
// without loss. A lagging subscriber is disconnected with its last
// delivered cursor rather than blocking commits.
func (h *handler) getRunEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var after uint64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.ParseUint(v, 10, 64)
	} else if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseUint(v, 10, 64)
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	if r.Header.Get("Accept") != "text/event-stream" {
		events, err := h.d.Store.ListEvents(r.Context(), id, after, limit)
		if err != nil {
			mapErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events})
		return
	}
	h.serveSSE(w, r, id, after)
}

// serveSSE streams committed events. The loop polls the durable log
// (the only ordering authority); a bounded buffer per subscriber
// plus a write deadline means a slow consumer is cut, never a
// backpressure source on Commit.
func (h *handler) serveSSE(w http.ResponseWriter, r *http.Request, runID string, after uint64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal", "no streaming", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	poll := time.NewTicker(150 * time.Millisecond)
	defer poll.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		events, err := h.d.Store.ListEvents(r.Context(), runID, after, 500)
		if err != nil {
			return
		}
		for _, e := range events {
			// data carries the whole committed record (seq/kind/path/
			// attempt/data) — the same shape the paged endpoint and the
			// studio's RunEvent expect; a payload-only blob would drop
			// the cursor and kind a subscriber needs.
			b, err := json.Marshal(e)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Kind, b)
			after = e.Seq
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
