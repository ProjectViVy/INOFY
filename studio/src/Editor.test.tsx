import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Editor } from "./Editor";
import type { Artifact, NodeDescriptor } from "./schema";

const artifact: Artifact = {
  definition: {
    schema_version: "inofy.workflow/v1",
    graph: {
      nodes: [
        { id: "a", kind: "call", type: "inofy.value@1", config: { limit: 1 } },
        { id: "u", kind: "call", type: "vendor.unknown@9" },
      ],
      edges: [{ from: "a", to: "u" }],
      exits: ["u"],
    },
  },
};

const catalog: NodeDescriptor[] = [
  {
    type_id: "inofy.value@1",
    implementation_id: "inofy.value@1",
    config_schema: {
      type: "object",
      properties: { limit: { type: "integer" }, label: { type: "string" } },
      required: ["limit"],
    },
  },
];

describe("Editor", () => {
  it("renders nodes and flags unresolved types", () => {
    render(
      <div style={{ width: 800, height: 600 }}>
        <Editor artifact={artifact} catalog={catalog} />
      </div>,
    );
    expect(screen.getByTestId("node-a")).toBeInTheDocument();
    expect(screen.getByTestId("node-u")).toHaveTextContent("unknown type");
    expect(screen.getByTestId("node-u")).toHaveTextContent("exit");
  });

  it("catalog schema drives the config form; edit updates artifact", () => {
    const onChange = vi.fn();
    render(
      <div style={{ width: 800, height: 600 }}>
        <Editor
          artifact={artifact}
          catalog={catalog}
          onArtifactChange={onChange}
        />
      </div>,
    );
    fireEvent.click(screen.getByTestId("node-a"));
    const label = screen.getByLabelText(/label/i) as HTMLInputElement;
    fireEvent.change(label, { target: { value: "hi" } });
    expect(onChange).toHaveBeenCalled();
    const next = onChange.mock.calls.at(-1)![0] as Artifact;
    const nodeA = next.definition.graph.nodes.find((n) => n.id === "a");
    expect(nodeA?.config).toMatchObject({ label: "hi" });
  });

  it("falls back to a JSON textarea when the schema is unsupported", () => {
    const noSchema: NodeDescriptor[] = [
      { type_id: "inofy.value@1", implementation_id: "inofy.value@1" },
    ];
    render(
      <div style={{ width: 800, height: 600 }}>
        <Editor artifact={artifact} catalog={noSchema} />
      </div>,
    );
    fireEvent.click(screen.getByTestId("node-a"));
    expect(
      screen.getByLabelText("Config (JSON)", { exact: false }),
    ).toBeInTheDocument();
  });
});
