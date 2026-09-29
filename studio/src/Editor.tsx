// 画布：artifact 的 React Flow 投影。画布是视图，不是真相——
// 每次画布改动都经 fromCanvas 合并回语义 artifact。视口（缩放/平移）
// 只存在于本地，不回写：位置属于作者意图，视口只是当下的看法。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  MarkerType,
  ReactFlow,
  ReactFlowProvider,
  applyEdgeChanges,
  applyNodeChanges,
  useReactFlow,
  type Connection,
  type EdgeChange,
  type NodeChange,
  type NodeProps,
  type Node,
  type Viewport,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import type { Artifact, NodeDescriptor } from "./schema";
import { fromCanvas, toCanvas, type Canvas, type CanvasNodeData } from "./graph";
import { InofyNode } from "./components/InofyNode";
import { addNodeAt } from "./edit";
import { Btn, Mk, Tag, Ticks } from "./components/Ui";

// 节点库 → 画布 的拖放契约：只传类型 id，不共享状态。
export const NODE_TYPE_MIME = "application/inofy-node-type";

// React Flow 把这些 props 逐次同步进内部 store：传新字面量会在每次
// 渲染里改写 store，触发「Maximum update depth」。一律用模块级常量。
// 署名内置角标会压住画布提示，改为在画布页脚以铭文列出。
const SNAP_GRID: [number, number] = [20, 20];
const DELETE_KEYS = ["Backspace", "Delete"];
const EDGE_OPTIONS = {
  markerEnd: {
    type: MarkerType.ArrowClosed,
    color: "#B4B2A8",
    width: 14,
    height: 14,
  },
};
const PRO_OPTIONS = { hideAttribution: true };

type NodeCompProps = NodeProps<Node<CanvasNodeData, "inofyNode">>;

interface Props {
  artifact: Artifact;
  catalog?: NodeDescriptor[];
  selected: string | null;
  onSelect(id: string | null): void;
  onArtifactChange?(a: Artifact): void;
}

export function Editor(props: Props) {
  return (
    <ReactFlowProvider>
      <Canvas {...props} />
    </ReactFlowProvider>
  );
}

// 选中态由页面持有（画布与属性面板共享一个选中）。React Flow 只认
// 自己内部的选中集，外部选中要同步进去；这类更新不回写 artifact。
function applySelection(c: Canvas, sel: string | null): Canvas {
  if (c.nodes.every((n) => Boolean(n.selected) === (n.id === sel))) return c;
  return {
    ...c,
    nodes: c.nodes.map((n) =>
      Boolean(n.selected) === (n.id === sel)
        ? n
        : { ...n, selected: n.id === sel },
    ),
  };
}

function Canvas({ artifact, catalog, selected, onSelect, onArtifactChange }: Props) {
  const catalogIds = useMemo(() => catalog?.map((d) => d.type_id), [catalog]);
  // 目录随类型注入节点卡：重放语义标记要按类型查目录。
  const nodeTypes = useMemo(() => {
    const byType = new Map((catalog ?? []).map((d) => [d.type_id, d]));
    return {
      inofyNode: (p: NodeCompProps) => (
        <InofyNode {...p} descriptor={byType.get(p.data.node.type ?? "")} />
      ),
    };
  }, [catalog]);
  const [canvas, setCanvas] = useState<Canvas>(() => toCanvas(artifact, catalogIds));
  const initialVP = useMemo(() => canvas.viewport ?? { x: 0, y: 0, zoom: 1 }, []);
  const [zoom, setZoom] = useState(initialVP.zoom);
  const lastEmitted = useRef<Artifact | null>(null);
  const selectedRef = useRef<string | null>(selected);
  selectedRef.current = selected;
  const { screenToFlowPosition, zoomIn, zoomOut, fitView } = useReactFlow();

  // 外部改动（撤销、属性面板、重新载入）→ 重建投影。
  // 自己发出的改动会原样回来，用身份比较跳过，避免打断拖拽。
  useEffect(() => {
    if (lastEmitted.current === artifact) return;
    setCanvas(applySelection(toCanvas(artifact, catalogIds), selectedRef.current));
  }, [artifact, catalogIds]);

  useEffect(() => {
    setCanvas((c) => applySelection(c, selected));
  }, [selected]);

  const emit = useCallback(
    (next: Canvas) => {
      const produced = fromCanvas(next, artifact);
      // 投影相同（例如仅 dimensions 变化）不触碰 artifact：
      // 否则挂载即变脏，撤销栈也会被空事件塞满。
      if (JSON.stringify(produced) === JSON.stringify(artifact)) return;
      lastEmitted.current = produced;
      onArtifactChange?.(produced);
    },
    [artifact, onArtifactChange],
  );

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      const nodes = applyNodeChanges(changes, canvas.nodes) as Canvas["nodes"];
      const ids = new Set(nodes.map((n) => n.id));
      const edges = canvas.edges.filter(
        (e) => ids.has(e.source) && ids.has(e.target),
      );
      const next = { ...canvas, nodes, edges };
      setCanvas(next);
      // 只在对语义有影响的改动上回写：落点（dragging 结束）与删除。
      const semantic = changes.some(
        (c) =>
          c.type === "remove" ||
          (c.type === "position" && c.dragging === false),
      );
      if (semantic) emit(next);
    },
    [canvas, emit],
  );

  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      const edges = applyEdgeChanges(changes, canvas.edges);
      const next = { ...canvas, edges };
      setCanvas(next);
      if (changes.some((c) => c.type === "remove")) emit(next);
    },
    [canvas, emit],
  );

  const onConnect = useCallback(
    (c: Connection) => {
      if (!c.source || !c.target) return;
      const edge = {
        id: `e-${c.source}-${c.sourceHandle ?? ""}-${c.target}`,
        source: c.source,
        target: c.target,
        ...(c.sourceHandle
          ? { sourceHandle: c.sourceHandle, label: c.sourceHandle }
          : {}),
        data: {
          semantic: {
            from: c.source,
            to: c.target,
            ...(c.sourceHandle ? { port: c.sourceHandle } : {}),
          },
        },
      };
      const next = { ...canvas, edges: [...canvas.edges, edge] };
      setCanvas(next);
      emit(next);
    },
    [canvas, emit],
  );

  const onDrop = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault();
      const type = e.dataTransfer.getData(NODE_TYPE_MIME);
      if (!type) return;
      const pos = screenToFlowPosition({ x: e.clientX, y: e.clientY });
      const { artifact: next, id } = addNodeAt(artifact, type, {
        x: pos.x - 112,
        y: pos.y - 20,
      });
      setCanvas(applySelection(toCanvas(next, catalogIds), id));
      lastEmitted.current = next;
      onArtifactChange?.(next);
      onSelect(id);
    },
    [artifact, catalogIds, onArtifactChange, onSelect, screenToFlowPosition],
  );

  const onSelectionChange = useCallback(
    (s: { nodes: Array<{ id: string }> }) => {
      onSelect(s.nodes[0] ? String(s.nodes[0].id) : null);
    },
    [onSelect],
  );

  const onMove = useCallback((_e: unknown, vp: Viewport) => {
    setZoom((z) => (z === vp.zoom ? z : vp.zoom));
  }, []);

  const g = artifact.definition.graph;
  const unresolved = canvas.nodes.filter((n) => n.data.unresolved).length;

  return (
    <div className="cvwrap">
      <div className="cvhead">
        <span
          style={{
            font: "600 11px/1 var(--sans)",
            letterSpacing: ".14em",
            color: "var(--ink-2)",
          }}
        >
          画布
        </span>
        <span style={{ width: 0, height: 14, borderLeft: "1px solid var(--rule)" }} />
        <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
          {artifact.presentation?.title ? (
            <>
              <span style={{ color: "var(--ink-2)" }}>presentation.title</span>{" "}
              {artifact.presentation.title}
            </>
          ) : (
            "未命名呈现"
          )}
        </span>
        <span className="grow" />
        <Tag>
          <Mk shape="round" tone="c-ok" />
          定义视图
        </Tag>
        <div className="zoom">
          <button
            title="缩小"
            aria-label="缩小"
            onClick={() => void zoomOut({ duration: 120 })}
          >
            −
          </button>
          <span className="z" data-testid="zoom">
            {Math.round(zoom * 100)}%
          </span>
          <button
            title="放大"
            aria-label="放大"
            onClick={() => void zoomIn({ duration: 120 })}
          >
            ＋
          </button>
        </div>
        <Btn size="sm" onClick={() => void fitView({ duration: 160, padding: 0.12 })}>
          适应视图
        </Btn>
      </div>

      <div
        className="canvas"
        onDragOver={(e) => {
          if (e.dataTransfer.types.includes(NODE_TYPE_MIME)) {
            e.preventDefault();
            e.dataTransfer.dropEffect = "copy";
          }
        }}
        onDrop={onDrop}
      >
        <div className="cv-tick" />
        <ReactFlow
          nodes={canvas.nodes}
          edges={canvas.edges}
          nodeTypes={nodeTypes}
          defaultViewport={initialVP}
          minZoom={0.25}
          maxZoom={2}
          snapToGrid
          snapGrid={SNAP_GRID}
          nodesConnectable
          elementsSelectable
          deleteKeyCode={DELETE_KEYS}
          defaultEdgeOptions={EDGE_OPTIONS}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onConnect={onConnect}
          onSelectionChange={onSelectionChange}
          onMove={onMove}
          proOptions={PRO_OPTIONS}
        />
        <span className="cvhint">
          拖动节点重排 · 滚轮缩放 · 空白处平移 · 网格 20 / 100 · 拖入左侧类型以添加
        </span>
      </div>

      <div className="cvfoot">
        <Ticks width={92} />
        <span className="legend">
          <span>
            <Mk shape="round" tone="c-ok" />
            可重放
          </span>
          <span>
            <Mk tone="c-nr" />
            不可重放
          </span>
          <span>
            <Mk shape="pause" tone="c-wa" />
            支持等待
          </span>
          <span>
            <Mk tone="c-sg" />
            出口 exit
          </span>
        </span>
        <span className="grow" />
        <span className="credit">画布引擎 React Flow · MIT</span>
        <span className="mono">
          节点 {g.nodes.length} · 边 {g.edges.length} · 出口 {g.exits.length}
          {g.outputs && Object.keys(g.outputs).length > 0
            ? ` · 输出绑定 ${Object.keys(g.outputs).length}`
            : ""}
          {unresolved > 0 ? ` · 未知类型 ${unresolved}` : ""}
        </span>
        <span className="sr-only" aria-live="polite">
          {selected ? `已选中节点 ${selected}` : "未选中节点"}
        </span>
      </div>
    </div>
  );
}
