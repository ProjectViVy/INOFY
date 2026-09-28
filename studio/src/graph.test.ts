import { describe, expect, it } from "vitest";
import { fromCanvas, toCanvas } from "./graph";
import type { Artifact } from "./schema";

const fixture: Artifact = {
  definition: {
    schema_version: "inofy.workflow/v1",
    inputs_schema: { type: "object" },
    graph: {
      nodes: [
        {
          id: "search",
          kind: "call",
          type: "inofy.value@1",
          config: { limit: 3 },
          inputs: { query: { source: "input", pointer: "/query" } },
        },
        {
          id: "route",
          kind: "switch",
          inputs: { hits: { source: "search", pointer: "/hits" } },
          cases: [
            {
              port: "have",
              when: {
                op: "gt",
                left: { source: "input", pointer: "/hits" },
                right: { literal: 0 },
              },
            },
          ],
          default_port: "none",
          join: "pick",
        },
        { id: "expand", kind: "call", type: "inofy.value@1" },
        { id: "empty", kind: "call", type: "inofy.value@1" },
        {
          id: "pick",
          kind: "select",
          switch: "route",
          candidates: [
            { source: "expand", pointer: "/out" },
            { source: "empty", pointer: "/out" },
          ],
          fallback: { literal: "none" },
        },
        {
          id: "loop",
          kind: "repeat",
          initial: { acc: { literal: 0 } },
          state_schema: { type: "object" },
          body: {
            nodes: [{ id: "step", kind: "call", type: "inofy.value@1" }],
            edges: [],
            exits: ["step"],
            outputs: { acc: { source: "step", pointer: "/acc" } },
          },
          max_iterations: 8,
          until: {
            op: "gte",
            left: { source: "input", pointer: "/acc" },
            right: { literal: 5 },
          },
        },
      ],
      edges: [
        { from: "search", to: "route" },
        { from: "route", to: "expand", port: "have" },
        { from: "route", to: "empty", port: "none" },
        { from: "expand", to: "pick" },
        { from: "empty", to: "pick" },
        { from: "pick", to: "loop" },
      ],
      exits: ["loop", "pick"],
      outputs: { answer: { source: "pick", pointer: "/answer" } },
    },
    limits: { max_iterations: 8 },
  },
  presentation: {
    title: "demo",
    layout: {
      positions: { search: { x: 10, y: 20 }, route: { x: 10, y: 120 } },
      viewport: { x: 0, y: 0, zoom: 1 },
      collapsed: [],
    },
    custom_ui_hint: { color: "blue" },
  },
};

describe("graph projection round-trip", () => {
  it("preserves the full fixture through toCanvas/fromCanvas", () => {
    const canvas = toCanvas(fixture);
    const back = fromCanvas(canvas, fixture);
    expect(back).toEqual(fixture);
  });

  it("carries switch case ports onto canvas edges", () => {
    const canvas = toCanvas(fixture);
    const have = canvas.edges.find(
      (e) => e.source === "route" && e.target === "expand",
    );
    expect(have?.sourceHandle).toBe("have");
    const none = canvas.edges.find(
      (e) => e.source === "route" && e.target === "empty",
    );
    expect(none?.sourceHandle).toBe("none");
  });

  it("keeps repeat body opaque and verbatim", () => {
    const canvas = toCanvas(fixture);
    const loop = canvas.nodes.find((n) => n.id === "loop");
    expect(loop?.data.node.kind).toBe("repeat");
    const back = fromCanvas(canvas, fixture);
    const loopBack = back.definition.graph.nodes.find((n) => n.id === "loop");
    expect(loopBack?.body).toEqual(fixture.definition.graph.nodes[5]?.body);
  });

  it("round-trips unicode, numeric and null binding values", () => {
    const art: Artifact = {
      definition: {
        schema_version: "inofy.workflow/v1",
        graph: {
          nodes: [
            {
              id: "a",
              kind: "call",
              type: "inofy.value@1",
              inputs: {
                名前: { literal: "值🙂" },
                num: { literal: 9007199254740991 },
                nul: { literal: null },
              },
            },
          ],
          edges: [],
          exits: ["a"],
        },
      },
    };
    const back = fromCanvas(toCanvas(art), art);
    const node = back.definition.graph.nodes[0];
    expect(node?.inputs?.["名前"]).toEqual({ literal: "值🙂" });
    expect(node?.inputs?.num).toEqual({ literal: 9007199254740991 });
    expect(node?.inputs?.nul).toEqual({ literal: null });
  });

  it("flags call nodes whose type is not in the catalog", () => {
    const canvas = toCanvas(fixture, ["inofy.value@1"]);
    const odd: Artifact = {
      definition: {
        schema_version: "inofy.workflow/v1",
        graph: {
          nodes: [{ id: "x", kind: "call", type: "vendor.unknown@9" }],
          edges: [],
          exits: ["x"],
        },
      },
    };
    const c2 = toCanvas(odd, ["inofy.value@1"]);
    expect(c2.nodes[0]?.data.unresolved).toBe(true);
    // unknown node still round-trips untouched
    expect(fromCanvas(c2, odd)).toEqual(odd);
    expect(canvas.nodes.every((n) => !n.data.unresolved)).toBe(true);
  });

  it("layout-only edits never touch the semantic definition", () => {
    const canvas = toCanvas(fixture);
    const moved = {
      ...canvas,
      nodes: canvas.nodes.map((n) =>
        n.id === "search" ? { ...n, position: { x: 500, y: 600 } } : n,
      ),
      viewport: { x: 7, y: 8, zoom: 1.5 },
    };
    const back = fromCanvas(moved, fixture);
    expect(back.definition).toEqual(fixture.definition);
    expect(back.presentation?.layout?.positions?.search).toEqual({
      x: 500,
      y: 600,
    });
    expect(back.presentation?.layout?.viewport).toEqual({
      x: 7,
      y: 8,
      zoom: 1.5,
    });
    expect(back.presentation?.custom_ui_hint).toEqual({ color: "blue" });
  });

  it("canvas node/edge membership edits map to semantic nodes/edges", () => {
    const canvas = toCanvas(fixture);
    const edited = {
      ...canvas,
      nodes: [
        ...canvas.nodes,
        {
          id: "extra",
          position: { x: 1, y: 2 },
          data: {
            node: {
              id: "extra",
              kind: "call" as const,
              type: "inofy.value@1",
            },
            unresolved: false,
            isExit: false,
            positionAuthored: false,
            fallbackPos: { x: 1, y: 2 },
          },
          type: "inofyNode",
        },
      ],
      edges: [
        ...canvas.edges,
        {
          id: "e-extra",
          source: "loop",
          target: "extra",
          data: { semantic: { from: "loop", to: "extra" } },
        },
      ],
    };
    const back = fromCanvas(edited, fixture);
    const ids = back.definition.graph.nodes.map((n) => n.id);
    expect(ids).toContain("extra");
    const ge = back.definition.graph.edges;
    expect(ge[ge.length - 1]).toEqual({ from: "loop", to: "extra" });
    // membership removal
    const removed = fromCanvas(
      {
        ...canvas,
        nodes: canvas.nodes.filter((n) => n.id !== "empty"),
        edges: canvas.edges.filter(
          (e) => e.source !== "empty" && e.target !== "empty",
        ),
      },
      fixture,
    );
    expect(removed.definition.graph.nodes.map((n) => n.id)).not.toContain(
      "empty",
    );
    expect(
      removed.definition.graph.edges.every(
        (e) => e.from !== "empty" && e.to !== "empty",
      ),
    ).toBe(true);
  });
});
