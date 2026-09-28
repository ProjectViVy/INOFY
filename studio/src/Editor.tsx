import { useCallback, useMemo, useState } from "react";
import {
  ReactFlow,
  ReactFlowProvider,
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type EdgeChange,
  type NodeChange,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import type { Artifact, NodeDescriptor } from "./schema";
import { fromCanvas, toCanvas, type CanvasNodeData } from "./graph";
import { InofyNode } from "./components/InofyNode";
import { NodeProperties } from "./components/NodeProperties";

const nodeTypes = { inofyNode: InofyNode };

interface Props {
  artifact: Artifact;
  catalog?: NodeDescriptor[];
  readOnly?: boolean;
  onArtifactChange?(a: Artifact): void;
}

// Editor renders the artifact as a React Flow projection. Every
// canvas mutation is merged back through fromCanvas so the semantic
// artifact — not the canvas — stays the single source of truth.
export function Editor({ artifact, catalog, readOnly, onArtifactChange }: Props) {
  const catalogIds = useMemo(() => catalog?.map((d) => d.type_id), [catalog]);
  const [canvas, setCanvas] = useState(() => toCanvas(artifact, catalogIds));
  const [sel, setSel] = useState<string | null>(null);

  const emit = useCallback(
    (next: typeof canvas) => {
      setCanvas(next);
      onArtifactChange?.(fromCanvas(next, artifact));
    },
    [artifact, onArtifactChange],
  );

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      const semantic = changes.filter(
        (c) => c.type === "position" || c.type === "remove",
      );
      const nodes = applyNodeChanges(
        changes,
        canvas.nodes,
      ) as typeof canvas.nodes;
      // Drop edges whose endpoints vanished with a removed node.
      const ids = new Set(nodes.map((n) => n.id));
      const edges = canvas.edges.filter(
        (e) => ids.has(e.source) && ids.has(e.target),
      );
      emit({ ...canvas, nodes, edges });
      void semantic;
    },
    [canvas, emit],
  );

  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) =>
      emit({ ...canvas, edges: applyEdgeChanges(changes, canvas.edges) }),
    [canvas, emit],
  );

  const onConnect = useCallback(
    (c: Connection) => {
      if (!c.source || !c.target) return;
      const edge = {
        id: `e-${c.source}-${c.sourceHandle ?? ""}-${c.target}`,
        source: c.source,
        target: c.target,
        ...(c.sourceHandle ? { sourceHandle: c.sourceHandle, label: c.sourceHandle } : {}),
        data: {
          semantic: {
            from: c.source,
            to: c.target,
            ...(c.sourceHandle ? { port: c.sourceHandle } : {}),
          },
        },
      };
      emit({ ...canvas, edges: [...canvas.edges, edge] });
    },
    [canvas, emit],
  );

  const selNode = canvas.nodes.find((n) => n.id === sel)?.data.node;
  const selDesc = catalog?.find(
    (d) => selNode?.kind === "call" && d.type_id === selNode.type,
  );

  return (
    <div className="editor" style={{ display: "flex", height: "100%" }}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <ReactFlow
          nodes={canvas.nodes}
          edges={canvas.edges}
          nodeTypes={nodeTypes}
          defaultViewport={canvas.viewport}
          nodesDraggable={!readOnly}
          nodesConnectable={!readOnly}
          elementsSelectable
          deleteKeyCode={readOnly ? null : ["Backspace", "Delete"]}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onConnect={onConnect}
          onSelectionChange={(s) =>
            setSel(s.nodes[0] ? String(s.nodes[0].id) : null)
          }
          fitView={canvas.viewport == null}
        />
      </div>
      {selNode && (
        <NodeProperties
          node={selNode}
          descriptor={selDesc}
          readOnly={readOnly}
          onChange={(next) => {
            const nodes = canvas.nodes.map((n) =>
              n.id === next.id
                ? { ...n, data: { ...n.data, node: next } as CanvasNodeData }
                : n,
            );
            emit({ ...canvas, nodes });
          }}
        />
      )}
    </div>
  );
}

export function EditorWithProvider(props: Props) {
  return (
    <ReactFlowProvider>
      <Editor {...props} />
    </ReactFlowProvider>
  );
}
