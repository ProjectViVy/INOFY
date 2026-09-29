// AppTransport talks §11.4 over HTTP. Authentication rides the
// HttpOnly session cookie (browser) — callers exchange an owner
// token once via login(); the token itself is never stored in
// localStorage or app state beyond the exchange call.

import {
  TransportError,
  type EventSubscription,
  type StudioTransport,
} from "./transport";
import type {
  Artifact,
  ConnectionView,
  ApiError,
  DraftView,
  NodeDescriptor,
  PublishView,
  RevisionView,
  RunDetail,
  RunEvent,
  RunSummary,
  WorkflowSummary,
} from "./schema";

export class AppTransport implements StudioTransport {
  // fetch 必须以 Window 为接收者调用：存成实例属性再经 this 调用
  // 会抛「Illegal invocation」，所以在构造时先绑定。
  private readonly call: typeof fetch;

  constructor(private base: string, fetchImpl: typeof fetch = fetch) {
    this.call = fetchImpl.bind(globalThis);
  }

  private async req<T>(
    method: string,
    path: string,
    opts: {
      body?: unknown;
      headers?: Record<string, string>;
      accept?: string;
      raw?: boolean;
    } = {},
  ): Promise<T> {
    const res = await this.call(this.base + path, {
      method,
      credentials: "include",
      headers: {
        ...(opts.body !== undefined
          ? { "content-type": "application/json" }
          : {}),
        ...(opts.accept ? { accept: opts.accept } : {}),
        ...opts.headers,
      },
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
    });
    if (opts.raw) return res as unknown as T;
    const body = (await res.json().catch(() => null)) as
      | (ApiError & Record<string, unknown>)
      | null;
    if (!res.ok) {
      throw new TransportError(res.status, {
        code: body?.code ?? "http_error",
        message: body?.message ?? res.statusText,
        diagnostics: body?.diagnostics,
      });
    }
    return body as T;
  }

  async login(token: string): Promise<void> {
    await this.req("POST", "/api/v1/session", { body: { token } });
  }
  async logout(): Promise<void> {
    await this.req("DELETE", "/api/v1/session");
  }

  capabilities() {
    return this.req<Record<string, unknown>>("GET", "/api/v1/capabilities");
  }
  async nodeTypes() {
    const r = await this.req<{ types: NodeDescriptor[] }>(
      "GET",
      "/api/v1/node-types",
    );
    return r.types;
  }

  async listWorkflows(cursor?: string) {
    const r = await this.req<{ items: WorkflowSummary[] | null; next_cursor?: string }>(
      "GET",
      `/api/v1/workflows${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`,
    );
    return { items: r.items ?? [], next_cursor: r.next_cursor ?? null };
  }
  async loadDraft(id: string) {
    return this.req<DraftView>("GET", `/api/v1/workflows/${encodeURIComponent(id)}/draft`);
  }
  async saveDraft(id: string, artifact: Artifact, etag: string | null) {
    const headers: Record<string, string> =
      etag == null ? { "if-none-match": "*" } : { "if-match": etag };
    const r = await this.req<{ etag: string }>(
      "PUT",
      `/api/v1/workflows/${encodeURIComponent(id)}/draft`,
      { body: artifact, headers },
    );
    return { workflow: id, etag: r.etag, artifact };
  }
  async validate(id: string, etag: string) {
    return this.req<{ valid?: boolean; diagnostics?: ApiError["diagnostics"] }>(
      "POST",
      `/api/v1/workflows/${encodeURIComponent(id)}/validate`,
      { headers: { "if-match": etag } },
    );
  }
  async publish(id: string, etag: string) {
    return this.req<PublishView>(
      "POST",
      `/api/v1/workflows/${encodeURIComponent(id)}/publish`,
      { headers: { "if-match": etag } },
    );
  }
  async getRevision(id: string, revision: number) {
    return this.req<RevisionView>(
      "GET",
      `/api/v1/workflows/${encodeURIComponent(id)}/revisions/${revision}`,
    );
  }

  async startRun(request: {
    workflow: string;
    revision?: number;
    draft_etag?: string;
    input?: unknown;
  }) {
    return this.req<{ run_id: string }>("POST", "/api/v1/runs", {
      body: request,
      headers: { "idempotency-key": crypto.randomUUID() },
    });
  }
  async listRuns(cursor?: string) {
    const r = await this.req<{ items: RunSummary[] | null; next_cursor?: string }>(
      "GET",
      `/api/v1/runs${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ""}`,
    );
    return { items: r.items ?? [], next_cursor: r.next_cursor ?? null };
  }
  getRun(id: string) {
    return this.req<RunDetail>("GET", `/api/v1/runs/${encodeURIComponent(id)}`);
  }
  async nodeOutput(id: string, node: string) {
    return this.req<{ output?: unknown }>(
      "GET",
      `/api/v1/runs/${encodeURIComponent(id)}/nodes/${encodeURIComponent(node)}/output`,
    );
  }
  cancelRun(id: string) {
    return this.req<{ ok: boolean }>(
      "POST",
      `/api/v1/runs/${encodeURIComponent(id)}/cancel`,
      { headers: { "idempotency-key": crypto.randomUUID() } },
    );
  }
  async resumeRun(id: string, answers: Record<string, unknown>) {
    return this.req<{ run_id: string; status: string }>(
      "POST",
      `/api/v1/runs/${encodeURIComponent(id)}/resume`,
      { body: { answers }, headers: { "idempotency-key": crypto.randomUUID() } },
    );
  }
  async events(id: string, afterSeq?: number) {
    const r = await this.req<{ events: RunEvent[] | null }>(
      "GET",
      `/api/v1/runs/${encodeURIComponent(id)}/events${afterSeq != null ? `?after=${afterSeq}` : ""}`,
    );
    const events = r.events ?? [];
    return {
      events,
      next_cursor: events.length ? String(events[events.length - 1]!.seq) : null,
    };
  }

  async listConnections() {
    const r = await this.req<{ connections?: ConnectionView[] }>(
      "GET",
      "/api/v1/connections",
    );
    return r.connections ?? [];
  }
  async putConnection(
    id: string,
    body: { kind: string; base_url: string; model: string; api_key?: string },
  ) {
    return this.req<{ ok: boolean }>(
      "PUT",
      `/api/v1/connections/${encodeURIComponent(id)}`,
      { body },
    );
  }
  async deleteConnection(id: string) {
    return this.req<{ ok: boolean }>(
      "DELETE",
      `/api/v1/connections/${encodeURIComponent(id)}`,
    );
  }

  // SSE subscriber over fetch streaming: replays committed events
  // after `afterSeq` (Last-Event-ID semantics), then streams. close()
  // aborts; the caller resumes by re-subscribing at the last seq.
  subscribeEvents(
    id: string,
    afterSeq: number | undefined,
    onEvent: (e: RunEvent) => void,
    onError?: (err: unknown) => void,
  ): EventSubscription {
    const ac = new AbortController();
    const go = async () => {
      try {
        const res = await this.call(
          `${this.base}/api/v1/runs/${encodeURIComponent(id)}/events${afterSeq != null ? `?after=${afterSeq}` : ""}`,
          {
            credentials: "include",
            headers: { accept: "text/event-stream" },
            signal: ac.signal,
          },
        );
        if (!res.ok || !res.body) throw new Error(`sse ${res.status}`);
        const reader = res.body.getReader();
        const dec = new TextDecoder();
        let buf = "";
        let dataLines: string[] = [];
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          const frames = buf.split("\n\n");
          buf = frames.pop() ?? "";
          for (const f of frames) {
            dataLines = [];
            for (const line of f.split("\n")) {
              if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
            }
            if (dataLines.length) {
              try {
                onEvent(JSON.parse(dataLines.join("")) as RunEvent);
              } catch {
                /* non-JSON frame (heartbeat) — skip */
              }
            }
          }
        }
      } catch (err) {
        if (!ac.signal.aborted) onError?.(err);
      }
    };
    void go();
    return { close: () => ac.abort() };
  }
}
