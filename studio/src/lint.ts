// lintArtifact mirrors the cheap half of the Go decoder's topology and
// capability findings (internal/definition/topology.go) so an editor can
// flag structural problems locally, without a validate round-trip. It
// deliberately covers only what is computable from the artifact doc
// alone — schema shape, budgets, predicates and region wiring stay
// server-side; the decoder remains the single source of truth.
import type { Binding, Graph } from "./schema";

export interface LintFinding {
  check: "topology" | "capability";
  path: string;
  code: string;
  message: string;
}

export interface LintOptions {
  /** Catalog type ids; enables the unknown_node_type capability check. */
  typeIDs?: ReadonlySet<string>;
}

const esc = (s: string) => s.replace(/~/g, "~0").replace(/\//g, "~1");

export function lintArtifact(graph: Graph, opts: LintOptions = {}): LintFinding[] {
  const findings: LintFinding[] = [];
  lintScope(graph, "/graph", opts, findings);
  return findings;
}

function lintScope(g: Graph, path: string, opts: LintOptions, out: LintFinding[]) {
  const nodes = g.nodes ?? [];
  const edges = g.edges ?? [];
  const exits = new Set(g.exits ?? []);
  const ids = new Set(nodes.map((n) => n.id));

  // Edge endpoints and input binding sources must resolve in scope.
  for (const [i, e] of edges.entries()) {
    for (const k of ["from", "to"] as const) {
      const s = e[k];
      if (typeof s === "string" && !ids.has(s)) {
        out.push({
          check: "topology",
          path: `${path}/edges/${i}/${k}`,
          code: "unknown_node_ref",
          message: `edge ${k} ${JSON.stringify(s)} is not a node in this graph`,
        });
      }
    }
  }
  for (const n of nodes) {
    const np = `${path}/nodes/${esc(n.id)}`;
    if (n.inputs) {
      for (const k of Object.keys(n.inputs)) {
        const src = bindingSource(n.inputs[k]);
        if (src !== "" && src !== "input" && !ids.has(src)) {
          out.push({
            check: "topology",
            path: `${np}/inputs/${esc(k)}/source`,
            code: "unknown_node_ref",
            message: `binding source ${JSON.stringify(src)} is not a node in this graph`,
          });
        }
      }
    }
    if (opts.typeIDs && n.type && !opts.typeIDs.has(n.type)) {
      out.push({
        check: "capability",
        path: `${np}/type`,
        code: "unknown_node_type",
        message: `node type ${JSON.stringify(n.type)} is not in the catalog`,
      });
    }
  }

  // Adjacency among resolvable endpoints only.
  const fwd = new Map<string, string[]>();
  const rev = new Map<string, string[]>();
  const indeg = new Map<string, number>();
  for (const n of nodes) {
    fwd.set(n.id, []);
    rev.set(n.id, []);
    indeg.set(n.id, 0);
  }
  for (const e of edges) {
    if (!ids.has(e.from) || !ids.has(e.to)) continue;
    fwd.get(e.from)!.push(e.to);
    rev.get(e.to)!.push(e.from);
    indeg.set(e.to, (indeg.get(e.to) ?? 0) + 1);
  }

  const reach = bfs(nodes.map((n) => n.id).filter((id) => (indeg.get(id) ?? 0) === 0), fwd);
  const back = bfs([...exits].filter((id) => ids.has(id)), rev);

  // Authored cycle: a node with inbound edges some forward neighbor
  // reaches back to (mirrors checkAcyclicity).
  const cyclic = new Set<string>();
  const members: string[] = [];
  for (const n of nodes) {
    if ((indeg.get(n.id) ?? 0) === 0) continue;
    for (const m of fwd.get(n.id) ?? []) {
      if (reachTo(m, n.id, fwd)) {
        cyclic.add(n.id);
        members.push(n.id);
        break;
      }
    }
  }
  if (members.length > 0) {
    members.sort();
    out.push({
      check: "topology",
      path,
      code: "cycle_outside_repeat",
      message:
        `authored cycle through nodes ${members.join(", ")}` +
        "; repetition is only legal via a repeat body",
    });
  }

  for (const n of nodes) {
    if (cyclic.has(n.id)) continue;
    const np = `${path}/nodes/${esc(n.id)}`;
    if (!reach.has(n.id)) {
      out.push({
        check: "topology",
        path: np,
        code: "unreachable_node",
        message: `node ${JSON.stringify(n.id)} is not reachable from an implicit root`,
      });
    }
    if (!back.has(n.id)) {
      out.push({
        check: "topology",
        path: np,
        code: "nonexit_dead_end",
        message: `node ${JSON.stringify(n.id)} cannot reach a declared exit`,
      });
    }
  }
  if (exits.size === 0 && nodes.length > 0) {
    out.push({
      check: "topology",
      path: `${path}/exits`,
      code: "no_exit",
      message: "graph declares no exit nodes",
    });
  }

  if (g.outputs) {
    for (const k of Object.keys(g.outputs)) {
      const src = bindingSource(g.outputs[k]);
      if (src !== "" && src !== "input" && !exits.has(src)) {
        out.push({
          check: "topology",
          path: `${path}/outputs/${esc(k)}`,
          code: "output_not_exit",
          message: `output binding ${JSON.stringify(k)} names ${JSON.stringify(src)} which is not a declared exit`,
        });
      }
    }
  }

  // Repeat bodies are nested scopes with their own exits.
  for (const n of nodes) {
    if (n.kind === "repeat" && n.body) {
      lintScope(n.body, `${path}/nodes/${esc(n.id)}/body`, opts, out);
    }
  }
}

function bindingSource(b: Binding | undefined): string {
  if (!b || typeof b !== "object") return "";
  return typeof b.source === "string" ? b.source : "";
}

function bfs(seeds: string[], adj: Map<string, string[]>): Set<string> {
  const seen = new Set<string>(seeds);
  const queue = [...seeds];
  while (queue.length > 0) {
    const cur = queue.shift()!;
    for (const next of adj.get(cur) ?? []) {
      if (!seen.has(next)) {
        seen.add(next);
        queue.push(next);
      }
    }
  }
  return seen;
}

function reachTo(from: string, target: string, adj: Map<string, string[]>): boolean {
  return bfs([from], adj).has(target);
}
