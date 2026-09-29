import { Handle, Position, type NodeProps, type Node } from "@xyflow/react";
import type { CanvasNodeData } from "../graph";
import { t } from "../i18n";

type N = Node<CanvasNodeData, "inofyNode">;

// One canvas node per semantic node. Repeat bodies stay collapsed —
// the badge is the only place kind surfaces visually.
export function InofyNode({ data, selected }: NodeProps<N>) {
  const n = data.node;
  return (
    <div
      className={`inofy-node kind-${n.kind}${selected ? " selected" : ""}${data.unresolved ? " unresolved" : ""}`}
      data-testid={`node-${n.id}`}
    >
      {n.kind === "switch" && (
        <>
          {(n.cases ?? []).map((c) => (
            <Handle
              key={c.port}
              id={c.port}
              type="source"
              position={Position.Right}
              style={{ top: "auto", bottom: 4 + (n.cases ?? []).indexOf(c) * 12 }}
            />
          ))}
          {n.default_port && (
            <Handle
              id={n.default_port}
              type="source"
              position={Position.Right}
              style={{ top: "auto", bottom: 0 }}
            />
          )}
        </>
      )}
      {n.kind !== "switch" && <Handle type="source" position={Position.Right} />}
      <Handle type="target" position={Position.Left} />
      <strong>{n.id}</strong>
      <span className="kind">{n.kind}</span>
      {n.type && <span className="type">{n.type}</span>}
      {data.unresolved && (
        <span className="badge warn" role="note">
          {t("node.unresolved")}
        </span>
      )}
      {data.isExit && <span className="badge exit">{t("node.exit")}</span>}
    </div>
  );
}
