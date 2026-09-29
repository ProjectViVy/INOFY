// StudioTransport is the single seam between the editor UI and any
// host (App HTTP, ViVy RPC). The UI never calls fetch itself; hosts
// supply one implementation. All methods return typed promises and
// surface backend errors as TransportError carrying the §11.4
// envelope verbatim.

import type {
  Artifact,
  ApiError,
  DraftView,
  NodeDescriptor,
  RevisionView,
  RunEvent,
  RunView,
} from "./schema";

export class TransportError extends Error {
  readonly code: string;
  readonly diagnostics?: ApiError["diagnostics"];
  readonly status: number;

  constructor(status: number, body: ApiError) {
    super(body.message);
    this.status = status;
    this.code = body.code;
    this.diagnostics = body.diagnostics;
  }
}

export interface EventPage {
  events: RunEvent[];
  next_cursor: string | null;
}

// EventSubscription streams committed run events. close() detaches;
// callers resume by re-calling events() with the last seq seen.
export interface EventSubscription {
  close(): void;
}

export interface StudioTransport {
  capabilities(): Promise<Record<string, unknown>>;
  nodeTypes(): Promise<NodeDescriptor[]>;

  listWorkflows(cursor?: string): Promise<{ items: { workflow: string; revision?: number }[]; next_cursor: string | null }>;
  loadDraft(id: string): Promise<DraftView>;
  saveDraft(id: string, artifact: Artifact, etag: string | null): Promise<DraftView>;
  validate(id: string, etag: string): Promise<{ diagnostics?: ApiError["diagnostics"] }>;
  publish(id: string, etag: string): Promise<RevisionView>;
  getRevision(id: string, revision: number): Promise<RevisionView>;

  startRun(request: { workflow: string; revision?: number; draft_etag?: string; input?: unknown }): Promise<RunView>;
  listRuns(cursor?: string): Promise<{ items: RunView[]; next_cursor: string | null }>;
  getRun(id: string): Promise<RunView>;
  cancelRun(id: string): Promise<RunView>;
  resumeRun(id: string, answers: Record<string, unknown>): Promise<RunView>;
  events(id: string, afterSeq?: number): Promise<EventPage>;
  subscribeEvents(
    id: string,
    afterSeq: number | undefined,
    onEvent: (e: RunEvent) => void,
    onError?: (err: unknown) => void,
  ): EventSubscription;
}
