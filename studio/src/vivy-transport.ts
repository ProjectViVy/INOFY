// VivyTransport adapts StudioTransport onto the ViVy host RPC bridge
// (S11 contract). The bridge is injected — the editor package never
// imports ViVy types; a host supplies `call(method, params)` and an
// event source. Until S11 lands the bridge shape here is the seam
// S11 must satisfy; the App adapter (app-transport.ts) is the
// reference implementation.

import {
  TransportError,
  type EventPage,
  type EventSubscription,
  type StudioTransport,
} from "./transport";
import type {
  Artifact,
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

export interface HostBridge {
  call<T>(method: string, params?: unknown): Promise<T>;
  subscribe(
    channel: string,
    params: unknown,
    onEvent: (data: unknown) => void,
    onError?: (err: unknown) => void,
  ): { close(): void };
}

export class VivyTransport implements StudioTransport {
  constructor(private rpc: HostBridge) {}

  private async call<T>(method: string, params?: unknown): Promise<T> {
    try {
      return await this.rpc.call<T>(`inofy.${method}`, params);
    } catch (e) {
      const err = e as { code?: string; message?: string; data?: ApiError };
      throw new TransportError(0, {
        code: err.data?.code ?? "rpc_error",
        message: err.message ?? String(e),
        diagnostics: err.data?.diagnostics,
      });
    }
  }

  capabilities() {
    return this.call<Record<string, unknown>>("capabilities");
  }
  nodeTypes() {
    return this.call<NodeDescriptor[]>("nodeTypes");
  }
  listWorkflows(cursor?: string) {
    return this.call<{
      items: WorkflowSummary[];
      next_cursor: string | null;
    }>("listWorkflows", { cursor });
  }
  loadDraft(id: string) {
    return this.call<DraftView>("loadDraft", { workflow: id });
  }
  saveDraft(id: string, artifact: Artifact, etag: string | null) {
    return this.call<DraftView>("saveDraft", { workflow: id, artifact, etag });
  }
  validate(id: string, etag: string) {
    return this.call<{ valid?: boolean; diagnostics?: ApiError["diagnostics"] }>("validate", {
      workflow: id,
      etag,
    });
  }
  publish(id: string, etag: string) {
    return this.call<PublishView>("publish", { workflow: id, etag });
  }
  getRevision(id: string, revision: number) {
    return this.call<RevisionView>("getRevision", { workflow: id, revision });
  }
  startRun(request: { workflow: string; revision?: number; draft_etag?: string; input?: unknown }) {
    return this.call<{ run_id: string }>("startRun", request);
  }
  listRuns(cursor?: string) {
    return this.call<{ items: RunSummary[]; next_cursor: string | null }>(
      "listRuns",
      { cursor },
    );
  }
  getRun(id: string) {
    return this.call<RunDetail>("getRun", { run_id: id });
  }
  nodeOutput(id: string, node: string) {
    return this.call<{ output?: unknown }>("nodeOutput", { run_id: id, node });
  }
  cancelRun(id: string) {
    return this.call<{ ok: boolean }>("cancelRun", { run_id: id });
  }
  resumeRun(id: string, answers: Record<string, unknown>) {
    return this.call<{ run_id: string; status: string }>("resumeRun", { run_id: id, answers });
  }
  events(id: string, afterSeq?: number) {
    return this.call<EventPage>("events", { run_id: id, after: afterSeq });
  }
  listConnections() {
    return this.call<import("./schema").ConnectionView[]>("listConnections");
  }
  putConnection(id: string, body: { kind: string; base_url: string; model: string; api_key?: string }) {
    return this.call<{ ok: boolean }>("putConnection", { id, ...body });
  }
  deleteConnection(id: string) {
    return this.call<{ ok: boolean }>("deleteConnection", { id });
  }
  subscribeEvents(
    id: string,
    afterSeq: number | undefined,
    onEvent: (e: RunEvent) => void,
    onError?: (err: unknown) => void,
  ): EventSubscription {
    return this.rpc.subscribe(
      "inofy.events",
      { run_id: id, after: afterSeq },
      (d) => onEvent(d as RunEvent),
      onError,
    );
  }
}
