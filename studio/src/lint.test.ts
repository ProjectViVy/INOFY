import { describe, expect, it } from "vitest";
import { lintArtifact } from "./lint";
import type { Graph } from "./schema";

const g = (nodes: object[], edges: object[], exits: string[], outputs?: object): Graph =>
  ({ nodes, edges, exits, outputs } as unknown as Graph);

const codes = (f: { code: string }[]) => f.map((x) => x.code).sort();

describe("lintArtifact", () => {
  it("accepts a simple chain with an exit", () => {
    const findings = lintArtifact(
      g([{ id: "a" }, { id: "b" }], [{ from: "a", to: "b" }], ["b"]),
    );
    expect(findings).toEqual([]);
  });

  it("flags a graph without exits", () => {
    // With no exits every node is also a dead end — mirrors the Go
    // decoder emitting both findings.
    const findings = lintArtifact(g([{ id: "a" }], [], []));
    expect(codes(findings)).toEqual(["no_exit", "nonexit_dead_end"]);
    expect(findings.find((f) => f.code === "no_exit")?.path).toBe("/graph/exits");
  });

  it("flags dead-end nodes that cannot reach an exit", () => {
    // d is a root (reachable) but no path leads to the exit b.
    const findings = lintArtifact(
      g(
        [{ id: "a" }, { id: "b" }, { id: "d" }],
        [{ from: "a", to: "b" }],
        ["b"],
      ),
    );
    expect(codes(findings)).toEqual(["nonexit_dead_end"]);
    expect(findings[0]?.path).toBe("/graph/nodes/d");
  });

  it("flags nodes fed only by a cycle as unreachable", () => {
    // Roots = in-degree 0. The a<->b component has no root, so c — fed
    // only by b — is unreachable while the cycle members themselves are
    // already covered by cycle_outside_repeat.
    const findings = lintArtifact(
      g(
        [{ id: "a" }, { id: "b" }, { id: "c" }],
        [
          { from: "a", to: "b" },
          { from: "b", to: "a" },
          { from: "b", to: "c" },
        ],
        ["c"],
      ),
    );
    expect(codes(findings)).toEqual(["cycle_outside_repeat", "unreachable_node"]);
    expect(findings[1]?.path).toBe("/graph/nodes/c");
  });

  it("flags authored cycles once per scope", () => {
    const findings = lintArtifact(
      g(
        [{ id: "a" }, { id: "b" }],
        [
          { from: "a", to: "b" },
          { from: "b", to: "a" },
        ],
        ["b"],
      ),
    );
    expect(codes(findings)).toEqual(["cycle_outside_repeat"]);
    expect(findings[0]?.path).toBe("/graph");
  });

  it("flags output bindings to non-exit nodes", () => {
    const findings = lintArtifact(
      g([{ id: "a" }, { id: "b" }], [{ from: "a", to: "b" }], ["b"], {
        answer: { source: "a" },
      }),
    );
    expect(codes(findings)).toEqual(["output_not_exit"]);
    expect(findings[0]?.path).toBe("/graph/outputs/answer");
  });

  it("flags dangling edge endpoints and binding sources", () => {
    const findings = lintArtifact(
      g(
        [{ id: "a", inputs: { q: { source: "ghost" } } }],
        [{ from: "a", to: "gone" }],
        ["a"],
      ),
    );
    expect(codes(findings)).toEqual(["unknown_node_ref", "unknown_node_ref"]);
  });

  it("flags unknown node types when a catalog is given", () => {
    const findings = lintArtifact(
      g([{ id: "a", kind: "call", type: "acme.task@1" }], [], ["a"]),
      { typeIDs: new Set(["vivy.child-task@1"]) },
    );
    expect(codes(findings)).toEqual(["unknown_node_type"]);
    expect(findings[0]?.check).toBe("capability");
  });

  it("walks repeat bodies as nested scopes", () => {
    const findings = lintArtifact(
      g(
        [
          {
            id: "loop",
            kind: "repeat",
            body: g([{ id: "step" }], [], []),
          },
        ],
        [],
        ["loop"],
      ),
    );
    expect(codes(findings)).toEqual(["no_exit", "nonexit_dead_end"]);
    expect(findings.find((f) => f.code === "no_exit")?.path).toBe(
      "/graph/nodes/loop/body/exits",
    );
  });

  it("skips reachability flags on cycle members", () => {
    // a<->b is an authored cycle; c is isolated and cannot reach the
    // exit a. Cycle members are already reported, so only c earns the
    // dead-end flag (c is itself a root, hence reachable).
    const findings = lintArtifact(
      g(
        [{ id: "a" }, { id: "b" }, { id: "c" }],
        [
          { from: "a", to: "b" },
          { from: "b", to: "a" },
        ],
        ["a"],
      ),
    );
    expect(codes(findings)).toEqual(["cycle_outside_repeat", "nonexit_dead_end"]);
    const flags = findings.filter((f) => f.code !== "cycle_outside_repeat");
    expect(flags.map((f) => f.path)).toEqual(["/graph/nodes/c"]);
  });
});
