import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Editor } from "./Editor";
import { NodeProperties } from "./components/NodeProperties";
import type { Artifact, Node, NodeDescriptor } from "./schema";

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

const descriptor: NodeDescriptor = {
  type_id: "inofy.value@1",
  implementation_id: "inofy.value@1",
  replay: "pure",
  config_schema: {
    type: "object",
    properties: { limit: { type: "integer" }, label: { type: "string" } },
    required: ["limit"],
  },
};

function mountEditor(overrides: Partial<Parameters<typeof Editor>[0]> = {}) {
  const onSelect = vi.fn();
  const onArtifactChange = vi.fn();
  const utils = render(
    <div style={{ width: 800, height: 600 }}>
      <Editor
        artifact={artifact}
        catalog={[descriptor]}
        selected={null}
        onSelect={onSelect}
        onArtifactChange={onArtifactChange}
        {...overrides}
      />
    </div>,
  );
  return { ...utils, onSelect, onArtifactChange };
}

describe("Editor", () => {
  it("renders the artifact as nodes and flags unresolved types", () => {
    mountEditor();
    expect(screen.getByTestId("node-a")).toBeInTheDocument();
    expect(screen.getByTestId("node-u")).toHaveTextContent("未知类型");
    expect(screen.getByTestId("node-u")).toHaveTextContent("出口");
  });

  it("the canvas is a view: selection mirrors in without touching the artifact", () => {
    const { onArtifactChange } = mountEditor({ selected: "a" });
    expect(screen.getByTestId("node-a").className).toContain("sel");
    expect(screen.getByTestId("node-u").className).not.toContain("sel");
    // 选中只是看法，不回写定义。
    expect(onArtifactChange).not.toHaveBeenCalled();
  });

  it("reports the definition size from the semantic graph, not the view", () => {
    mountEditor();
    const foot = document.querySelector(".cvfoot");
    expect(foot?.textContent).toContain("节点 2");
    expect(foot?.textContent).toContain("边 1");
    expect(foot?.textContent).toContain("出口 1");
    expect(foot?.textContent).toContain("未知类型 1");
  });
});

describe("NodeProperties", () => {
  const node = artifact.definition.graph.nodes[0] as Node;

  it("drives the config form from the catalog schema", () => {
    const onChange = vi.fn();
    render(
      <NodeProperties
        node={node}
        descriptor={descriptor}
        isExit={false}
        outputName=""
        onSetExitOutput={vi.fn()}
        onChange={onChange}
        onToggleExit={vi.fn()}
        onDelete={vi.fn()}
      />,
    );
    const label = screen.getByLabelText(/label/) as HTMLInputElement;
    fireEvent.change(label, { target: { value: "hi" } });
    expect(onChange.mock.calls.at(-1)![0]).toMatchObject({
      id: "a",
      config: { label: "hi", limit: 1 },
    });
  });

  it("falls back to a JSON textarea when the schema is unsupported", () => {
    render(
      <NodeProperties
        node={node}
        descriptor={{ type_id: "inofy.value@1", implementation_id: "inofy.value@1" }}
        isExit={false}
        outputName=""
        onSetExitOutput={vi.fn()}
        onChange={vi.fn()}
        onToggleExit={vi.fn()}
        onDelete={vi.fn()}
      />,
    );
    expect(screen.getByLabelText("配置（JSON）")).toBeInTheDocument();
  });

  it("commits JSON on blur and rejects invalid text", () => {
    const onChange = vi.fn();
    render(
      <NodeProperties
        node={node}
        descriptor={{ type_id: "inofy.value@1", implementation_id: "inofy.value@1" }}
        isExit={false}
        outputName=""
        onSetExitOutput={vi.fn()}
        onChange={onChange}
        onToggleExit={vi.fn()}
        onDelete={vi.fn()}
      />,
    );
    const ta = screen.getByLabelText("配置（JSON）");
    fireEvent.change(ta, { target: { value: "{nope" } });
    fireEvent.blur(ta);
    expect(onChange).not.toHaveBeenCalled();
    fireEvent.change(ta, { target: { value: '{"k":1}' } });
    fireEvent.blur(ta);
    expect(onChange.mock.calls.at(-1)![0]).toMatchObject({ config: { k: 1 } });
  });

  it("declares an exit through the checkbox, not by rewriting the graph", () => {
    const onToggleExit = vi.fn();
    render(
      <NodeProperties
        node={node}
        descriptor={descriptor}
        isExit={false}
        outputName=""
        onSetExitOutput={vi.fn()}
        onChange={vi.fn()}
        onToggleExit={onToggleExit}
        onDelete={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("checkbox", { name: /结果节点/ }));
    expect(onToggleExit).toHaveBeenCalledWith(true);
  });
});
