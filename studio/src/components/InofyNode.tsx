// 画布节点卡：面板材质的小卡片，嵌在暗屏上。形状+颜色双编码
// 表达重放语义；未解析类型与出口是卡片上的两枚状态标签。

import { Handle, Position, type NodeProps, type Node } from "@xyflow/react";
import type { CanvasNodeData } from "../graph";
import { nodeKind, replayLabel, replayOf } from "../labels";
import type { NodeDescriptor } from "../schema";
import { Mk, Tag } from "./Ui";

type N = Node<CanvasNodeData, "inofyNode">;

export function InofyNode({
  data,
  selected,
  descriptor,
}: NodeProps<N> & { descriptor?: NodeDescriptor }) {
  const n = data.node;
  const replay = descriptor ? replayOf(descriptor) : "unknown";
  const cls = [
    "node",
    selected ? "sel" : "",
    data.unresolved ? "unresolved" : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={cls} data-testid={`node-${n.id}`}>
      {n.kind === "switch" &&
        (n.cases ?? []).map((c, i) => (
          <Handle
            key={c.port}
            id={c.port}
            type="source"
            position={Position.Right}
            style={{ top: "auto", bottom: 6 + i * 12 }}
            title={`分支端口 ${c.port}`}
          />
        ))}
      {n.kind === "switch" && n.default_port && (
        <Handle
          id={n.default_port}
          type="source"
          position={Position.Right}
          style={{ top: "auto", bottom: 0 }}
          title={`默认端口 ${n.default_port}`}
        />
      )}
      {n.kind !== "switch" && (
        <Handle type="source" position={Position.Right} title="输出" />
      )}
      <Handle type="target" position={Position.Left} title="输入" />

      <div className="nh">
        <span className="nid">{n.id}</span>
        <span className="grow" />
        {data.isExit && (
          <Tag on title="出口：图的结果节点">
            出口
            <span className="mono" style={{ fontSize: 9 }}>
              exit
            </span>
          </Tag>
        )}
        {replay !== "unknown" && (
          <span
            className={replay === "pure" ? "c-ok" : "c-nr"}
            title={replayLabel(replay)}
          >
            <Mk shape={replay === "pure" ? "round" : "square"} />
          </span>
        )}
      </div>

      <div className="nb">
        <div className="kv">
          <span className="lbl">KIND</span>
          <code>{nodeKind(n.kind)}</code>
          <span className="mono" style={{ fontSize: 10, color: "#77786F" }}>
            {n.kind}
          </span>
        </div>
        {n.kind === "call" && (
          <div className="kv">
            <span className="lbl">TYPE</span>
            <code>{n.type ?? "—"}</code>
          </div>
        )}
        {data.unresolved && (
          <div className="bind" role="note" style={{ color: "#7C2A1E" }}>
            <Mk tone="c-er" />
            未知类型：目录中无 {n.type ?? "—"}
          </div>
        )}
        {data.isEntry && (
          <div className="bind">
            <Mk shape="round" tone="c-ok" />
            起始节点 · 无上游输入
          </div>
        )}
      </div>
      <span className="nvarsel" />
    </div>
  );
}
