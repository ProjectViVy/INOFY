import { useCallback, useEffect, useRef, useState } from "react";
import { TransportError, type StudioTransport } from "./transport";
import type { Artifact, DraftView, NodeDescriptor } from "./schema";
import { Editor } from "./Editor";
import { RunView } from "./RunView";
import { t, setLocale, getLocale, type Locale } from "./i18n";

interface Props {
  transport: StudioTransport;
  workflowId: string;
}

// One workflow's draft surface. The server stays the authority:
// save/publish ride the draft ETag; a stale ETag is surfaced as a
// conflict while the user's edited artifact is preserved untouched.
export function App({ transport, workflowId }: Props) {
  const [draft, setDraft] = useState<DraftView | null>(null);
  const [artifact, setArtifact] = useState<Artifact | null>(null);
  const [dirty, setDirty] = useState(false);
  const [conflict, setConflict] = useState<string | null>(null);
  const [diags, setDiags] = useState<unknown>(null);
  const [catalog, setCatalog] = useState<NodeDescriptor[]>([]);
  const [runId, setRunId] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const past = useRef<Artifact[]>([]);
  const future = useRef<Artifact[]>([]);
  // Editor owns its canvas while the user edits; undo swaps the
  // artifact, so bump this key to remount the canvas from it.
  const [canvasKey, setCanvasKey] = useState(0);

  useEffect(() => {
    void transport.nodeTypes().then(setCatalog);
    void transport.loadDraft(workflowId).then((d) => {
      setDraft(d);
      setArtifact(d.artifact);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workflowId]);

  const edit = useCallback(
    (a: Artifact) => {
      if (artifact) past.current.push(artifact);
      future.current = [];
      setArtifact(a);
      setDirty(true);
    },
    [artifact],
  );

  // Local undo/redo — never calls the server.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== "z") return;
      e.preventDefault();
      if (e.shiftKey) {
        const next = future.current.pop();
        if (next && artifact) {
          past.current.push(artifact);
          setArtifact(next);
          setCanvasKey((k) => k + 1);
        }
      } else {
        const prev = past.current.pop();
        if (prev && artifact) {
          future.current.push(artifact);
          setArtifact(prev);
          setCanvasKey((k) => k + 1);
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [artifact]);

  const save = async () => {
    if (!artifact || !draft) return;
    setErr(null);
    try {
      const first = draft.etag === "";
      const saved = await transport.saveDraft(
        workflowId,
        artifact,
        first ? null : draft.etag,
      );
      setDraft(saved);
      setDirty(false);
      setConflict(null);
    } catch (e) {
      // Stale ETag: keep the user's artifact, surface the conflict.
      if (e instanceof TransportError && (e.status === 412 || e.status === 409)) {
        setConflict(e.message);
      } else {
        setErr(e instanceof Error ? e.message : String(e));
      }
    }
  };

  const publish = async () => {
    if (!draft) return;
    setErr(null);
    try {
      await transport.publish(workflowId, draft.etag);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  const validate = async () => {
    if (!draft) return;
    try {
      const r = await transport.validate(workflowId, draft.etag);
      setDiags(r.diagnostics ?? null);
    } catch (e) {
      setDiags(
        e instanceof TransportError ? (e.diagnostics ?? e.message) : String(e),
      );
    }
  };

  const run = async () => {
    if (!draft) return;
    try {
      const rv = await transport.startRun({
        workflow: workflowId,
        draft_etag: draft.etag,
        input: {},
      });
      setRunId(rv.run_id);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  if (!draft || !artifact) return <p>loading…</p>;

  return (
    <div className="studio-app">
      <header className="bar">
        <strong>{workflowId}</strong>
        {dirty && <span className="dirty">{t("editor.dirty")}</span>}
        <button onClick={() => void save()}>{t("action.save")}</button>
        <button onClick={() => void validate()}>validate</button>
        <button onClick={() => void publish()} disabled={dirty}>
          {t("action.publish")}
        </button>
        <button onClick={() => void run()} disabled={dirty}>
          {t("action.run")}
        </button>
        <button
          aria-label="locale"
          onClick={() => {
            const next: Locale = getLocale() === "en" ? "zh" : "en";
            setLocale(next);
            // rerender via state poke
            setDiags((d: unknown) => (d === null ? undefined : d));
          }}
        >
          {getLocale() === "en" ? "中文" : "EN"}
        </button>
        {conflict && (
          <span role="alert" className="conflict">
            conflict: {conflict}
          </span>
        )}
        {err && <span role="alert">{err}</span>}
      </header>
      {diags != null && (
        <pre data-testid="diagnostics">{JSON.stringify(diags, null, 2)}</pre>
      )}
      <div className="main">
        <Editor
          key={canvasKey}
          artifact={artifact}
          catalog={catalog}
          onArtifactChange={edit}
        />
        {runId && <RunView transport={transport} runId={runId} />}
      </div>
    </div>
  );
}
