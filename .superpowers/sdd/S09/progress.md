# S09 — App auth, HTTP and catalog

## Verdict
Done. Authenticated `/api/v1` (session cookie + bearer + Origin/CSRF guards), §11.4 workflow/run/SSE endpoints over the S08 authority, governed node catalog (value/template/wait + OpenAI-compatible model via opaque connection IDs), explicit loopback serve lifecycle.

## Evidence
- `CGO_ENABLED=0 go test ./... -count=2` all green; root `go test -race` green; `go vet` clean.
- Auth: constant-time token compare, bounded idle-TTL sessions, HttpOnly SameSite=Strict cookie, session-carried mutations need same Origin (bearer exempt for CLI), token file must be 0600.
- Runs: same Idempotency-Key → same run ID; anonymous output GET 401 without value leak; events strictly monotone committed seq; Last-Event-ID replays tail only; SSE cursor = durable seq; wait→resume over HTTP works incl. identical authorized replay and conflicting-body rejection; cancel idempotent.
- Nodes: value/template pure; model resolves credential inside adapter only (missing secret → zero provider calls, no key echo); ambiguous timeout → `outcome_unknown`; committed ledger replay returns stored output; wait emits durable WaitRequest; catalog pins impl IDs + replay classes.

## Rulings
- Admission stores the immutable artifact bytes (`source_json`) + raw input (`input_json`) — §11.2 "immutable source snapshot/inputs"; a draft edit can never change an admitted run.
- Resume cpID re-stages under the NEW generation's ID (`RunID/gen`) — Eino's next suspend writes under the invoked ID, keeping `cpID == RunID/j.gen` (fixes re-wait-after-resume losing its payload; was latent since S06).
- `checkpoint_payload` may legitimately serialize empty — `Load` restores the envelope on `checkpoint_json` alone.
- Cancel: live runs get their ctx cancelled (engine commits `run_cancelled`); quiescent rows (queued/admitted/claimed/waiting/recovery_required) transition durably — never both.
- `node_executions` doubles as the effect ledger keyed by (run, path, attempt); a repeat NodeCall under the same key replays committed output.
- SSE is a committed-log poller per subscriber — no in-memory broadcaster, so slow consumers can never backpressure `Commit`; disconnect = client-side, replay via Last-Event-ID.
- Model dependency: `eino-ext/components/model/openai v0.1.13` (published 2026-04-16, eino ≥0.7.13 — compatible with pinned v0.9.13).
- Owner token auto-generated once into `owner.token` (0600) inside the 0700 state dir; non-loopback `-listen` rejected at startup.

## minor (deferred)
- POST /runs revision admission doesn't verify the run's principal against workflow ownership (single-owner App: acceptable for now).
- `ListRuns` cursor = run_id order, not recency.
- `postResume` blocks on the whole resumed execution — long resumes hold the request.
- `run_events.data` stored unredacted-copy of commit events — redaction contract currently relies on events already carrying protected references only.
