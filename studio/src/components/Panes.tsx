// 跨页复用的右栏面板：受治理类型目录 / 运行环境。数据全部来自
// 真实接口（GET /node-types、GET /capabilities），不写死。

import type { NodeDescriptor } from "../schema";
import { Pane, Ph, Facts, FactRow, Tag, Mk, CapTag, Loading } from "./Ui";
import { capability, replayLabel, replayOf, typeName } from "../labels";
import type { ConnState } from "./Shell";

export function CatalogPane({
  nodeTypes,
  title = "受治理类型",
  note,
}: {
  nodeTypes: NodeDescriptor[] | null;
  title?: string;
  note?: string;
}) {
  return (
    <Pane
      title={title}
      note={note}
      right={
        <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
          {nodeTypes ? `${nodeTypes.length} 个` : "…"}
        </span>
      }
    >
      {!nodeTypes ? (
        <Loading />
      ) : nodeTypes.length === 0 ? (
        <div className="pnote">目录为空：宿主未注册任何节点类型。</div>
      ) : (
        <Facts>
          {nodeTypes.map((d) => {
            const replay = replayOf(d);
            return (
              <div key={d.type_id} className="cat-entry">
                <div className="l1">
                  <Mk shape={replay === "pure" ? "round" : "square"} tone={replay === "pure" ? "c-ok" : "c-nr"} />
                  <span className="nm">{typeName(d)}</span>
                  <span className="grow" />
                  {d.supports_wait && (
                    <Tag title="支持等待：可挂起等待人工应答">
                      <Mk shape="pause" tone="c-wa" />
                      等待
                    </Tag>
                  )}
                  <Tag title={replayLabel(replay)}>
                    <Mk shape={replay === "pure" ? "round" : "square"} tone={replay === "pure" ? "c-ok" : "c-nr"} />
                    {replay === "pure" ? "可重放" : replay === "non_replayable" ? "不可重放" : "未知"}
                  </Tag>
                </div>
                <span className="tid">{d.type_id}</span>
                <span className="impl">{d.implementation_id}</span>
              </div>
            );
          })}
        </Facts>
      )}
    </Pane>
  );
}

export function EnvPane({
  conn,
  features,
}: {
  conn: ConnState;
  features: string[];
}) {
  return (
    <Pane
      title="环境"
      right={
        <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
          本机模式
        </span>
      }
    >
      <Facts>
        <FactRow k="后端地址" v={conn.origin} />
        <FactRow
          k="监听范围"
          v={<span style={{ fontFamily: "var(--sans)" }}>仅回环地址</span>}
        />
        <FactRow
          k="连接"
          v={
            <span className={conn.ok ? "c-ok" : "c-er"}>
              {conn.ok ? "已连接" : "未连接"}
            </span>
          }
        />
        <FactRow
          k="契约版本"
          v={<span style={{ fontSize: 11 }}>{conn.schemaVersion ?? "—"}</span>}
        />
        <FactRow
          k="事件流"
          v={<span style={{ fontSize: 11 }}>SSE · Last-Event-ID</span>}
        />
        <FactRow
          k="存储"
          v={<span style={{ fontSize: 11 }}>本地 · 单写者</span>}
        />
      </Facts>
      <Ph
        title="能力"
        right={
          <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
            {features.length}
          </span>
        }
      />
      <div className="chiprow" style={{ padding: "8px 14px" }}>
        {features.length === 0 && (
          <span className="dim" style={{ fontSize: 11 }}>
            未声明能力
          </span>
        )}
        {features.map((f) => (
          <Tag key={f}>
            <Mk shape="round" tone="c-ok" />
            {capability(f)}
          </Tag>
        ))}
      </div>
    </Pane>
  );
}
