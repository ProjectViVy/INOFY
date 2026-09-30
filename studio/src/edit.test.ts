import { describe, expect, it } from "vitest";
import { exitOutputName, setExit, setExitOutputName } from "./edit";
import type { Artifact } from "./schema";

const fixture: Artifact = {
  definition: {
    schema_version: "inofy.workflow/v1",
    graph: {
      nodes: [
        { id: "fetch", kind: "call", type: "inofy.value@1" },
        { id: "summarize", kind: "call", type: "inofy.value@1" },
      ],
      edges: [{ from: "fetch", to: "summarize" }],
      exits: [],
      outputs: {},
    },
  },
};

describe("exit output bindings", () => {
  it("declaring an exit binds its result under the node id by default", () => {
    const next = setExit(fixture, "summarize", true);
    expect(next.definition.graph.exits).toEqual(["summarize"]);
    expect(next.definition.graph.outputs).toEqual({ summarize: { source: "summarize", pointer: "" } });
    expect(exitOutputName(next, "summarize")).toBe("summarize");
  });

  it("un-declaring an exit drops its output bindings like node removal does", () => {
    const withExit = setExit(fixture, "summarize", true);
    const renamed = setExitOutputName(withExit, "summarize", "answer");
    expect(renamed.definition.graph.outputs).toEqual({ answer: { source: "summarize", pointer: "" } });
    const off = setExit(renamed, "summarize", false);
    expect(off.definition.graph.exits).toEqual([]);
    expect(off.definition.graph.outputs).toEqual({});
  });

  it("renaming moves the binding key; clearing the name unbinds the exit", () => {
    const withExit = setExit(fixture, "summarize", true);
    const renamed = setExitOutputName(withExit, "summarize", "answer");
    expect(renamed.definition.graph.outputs).toEqual({ answer: { source: "summarize", pointer: "" } });
    expect(exitOutputName(renamed, "summarize")).toBe("answer");
    const cleared = setExitOutputName(renamed, "summarize", "  ");
    expect(cleared.definition.graph.outputs).toEqual({});
    expect(exitOutputName(cleared, "summarize")).toBe("");
  });

  it("re-toggling an exit keeps a renamed output instead of resetting it", () => {
    const withExit = setExit(fixture, "summarize", true);
    const renamed = setExitOutputName(withExit, "summarize", "answer");
    const retoggled = setExit(renamed, "summarize", true);
    expect(retoggled.definition.graph.outputs).toEqual({ answer: { source: "summarize", pointer: "" } });
  });

  it("edits leave sibling bindings and other node facts untouched", () => {
    const seeded: Artifact = {
      ...fixture,
      definition: {
        ...fixture.definition,
        graph: {
          ...fixture.definition.graph,
          exits: ["fetch"],
          outputs: { fetched: { source: "fetch", pointer: "/raw" } },
        },
      },
    };
    const next = setExitOutputName(seeded, "summarize", "answer");
    expect(next.definition.graph.outputs).toEqual({
      fetched: { source: "fetch", pointer: "/raw" },
      answer: { source: "summarize", pointer: "" },
    });
    expect(next.definition.graph.exits).toEqual(["fetch"]);
    expect(next.definition.graph.nodes).toEqual(fixture.definition.graph.nodes);
  });
});
