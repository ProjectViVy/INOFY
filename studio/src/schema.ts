// Mirrors the wire shapes of inofy.workflow/v1 (S02) and the App API
// bodies (S09 §11.4). Field names stay snake_case on purpose — these
// objects serialize straight to the backend digests; never rename.

export type JsonValue =
  | string
  | number
  | boolean
  | null
  | JsonValue[]
  | { [k: string]: JsonValue };

export interface Artifact {
  definition: Definition;
  presentation?: Presentation;
}

export interface Definition {
  schema_version: string;
  inputs_schema?: JsonValue;
  outputs_schema?: JsonValue;
  graph: Graph;
  limits?: Limits;
}

export interface Graph {
  nodes: Node[];
  edges: Edge[];
  exits: string[];
  outputs?: Record<string, Binding>;
}

export interface Edge {
  from: string;
  to: string;
  port?: string;
}

export type NodeKind = "call" | "switch" | "select" | "repeat";

export interface Node {
  id: string;
  kind: NodeKind;
  type?: string;
  config?: JsonValue;
  inputs?: Record<string, Binding>;
  timeout_ms?: number;
  retry?: { max_attempts?: number; backoff_ms?: number };
  on_error?: { mode: string; value?: JsonValue };
  // switch
  cases?: SwitchCase[];
  default_port?: string;
  join?: string;
  // select
  switch?: string;
  candidates?: SelectCandidate[];
  fallback?: Binding;
  // repeat
  initial?: Record<string, Binding>;
  state_schema?: JsonValue;
  body?: Graph;
  max_iterations?: number;
  until?: Predicate;
  // forward-compatible: unknown authored fields ride along untouched
  [extra: string]: unknown;
}

export interface SwitchCase {
  port: string;
  when: Predicate;
}

export interface SelectCandidate {
  source: string;
  pointer?: string;
}

export interface Predicate {
  op: string;
  left?: Binding;
  right?: Binding;
  args?: Binding[];
}

export interface Binding {
  literal?: JsonValue;
  source?: string;
  pointer?: string;
}

export interface Limits {
  max_repeat_nesting?: number;
  max_iterations?: number;
  max_active_nodes?: number;
  max_pending_waits?: number;
  max_nodes?: number;
  max_edges?: number;
  [extra: string]: unknown;
}

export interface Presentation {
  title?: string;
  description?: string;
  layout?: Layout;
  [extra: string]: unknown;
}

export interface Layout {
  positions?: Record<string, { x: number; y: number }>;
  viewport?: { x: number; y: number; zoom: number };
  collapsed?: string[];
  [extra: string]: unknown;
}

// --- catalog ---

export interface NodeDescriptor {
  type_id: string;
  implementation_id: string;
  config_schema?: JsonValue;
  input_schema?: JsonValue;
  output_schema?: JsonValue;
  display?: { title?: string; subtitle?: string; [k: string]: unknown };
  capabilities?: string[];
  replay?: string;
  supports_wait?: boolean;
}

// --- App API bodies (S09 §11.4) ---

export interface ApiError {
  code: string;
  message: string;
  diagnostics?: JsonValue;
  run_id?: string;
}

export interface DraftView {
  workflow: string;
  etag: string;
  artifact: Artifact;
}

export interface RevisionView {
  workflow: string;
  revision: number;
  etag: string;
  artifact: Artifact;
}

export interface RunView {
  run_id: string;
  workflow: string;
  revision: number;
  status: string;
  writer_epoch?: number;
  generation?: number;
  created_at?: string;
  updated_at?: string;
}

export interface RunEvent {
  seq: number;
  kind: string;
  path?: string;
  attempt?: number;
  data?: JsonValue;
}
