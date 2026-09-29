// 工作台首页：工作流列表 + 右栏目录/环境。列表项来自
// GET /workflows；新建走草稿 CAS（If-None-Match: *），成功后
// 直接进编辑器。没有 mock：空、错、载入都按真实状态渲染。

import { useMemo, useState } from "react";
import type { NodeDescriptor, WorkflowSummary } from "../schema";
import { TransportError, type StudioTransport } from "../transport";
import { blankArtifact, validateWorkflowID } from "../seed";
import { navigate } from "../router";
import { Btn, Empty, Tbtn, Ticks } from "../components/Ui";
import { CatalogPane, EnvPane } from "../components/Panes";
import type { ConnState } from "../components/Shell";
import { errorCode } from "../labels";

interface Props {
  transport: StudioTransport;
  workflows: WorkflowSummary[] | null;
  nodeTypes: NodeDescriptor[] | null;
  conn: ConnState;
  features: string[];
  error: string | null;
  onRefresh(): void;
}

export function WorkflowsPage({
  transport,
  workflows,
  nodeTypes,
  conn,
  features,
  error,
  onRefresh,
}: Props) {
  const [q, setQ] = useState("");
  const [creating, setCreating] = useState(false);
  const [newID, setNewID] = useState("");
  const [opening, setOpening] = useState(false);
  const [openID, setOpenID] = useState("");
  const [busy, setBusy] = useState(false);
  const [createErr, setCreateErr] = useState<string | null>(null);

  const rows = useMemo(() => {
    const list = workflows ?? [];
    const needle = q.trim().toLowerCase();
    const filtered = needle
      ? list.filter((w) => w.workflow_id.toLowerCase().includes(needle))
      : list;
    return [...filtered].sort(
      (a, b) =>
        (b.revision ?? 0) - (a.revision ?? 0) ||
        a.workflow_id.localeCompare(b.workflow_id),
    );
  }, [workflows, q]);

  const publishedCount = useMemo(
    () => (workflows ?? []).filter((w) => (w.revision ?? 0) > 0).length,
    [workflows],
  );

  const create = async () => {
    const id = newID.trim();
    const invalid = validateWorkflowID(id);
    if (invalid) {
      setCreateErr(invalid);
      return;
    }
    setBusy(true);
    setCreateErr(null);
    try {
      await transport.saveDraft(id, blankArtifact(id), null);
      setCreating(false);
      setNewID("");
      onRefresh();
      navigate({ name: "editor", id });
    } catch (e) {
      if (e instanceof TransportError) {
        setCreateErr(
          e.status === 412
            ? `工作流 ${id} 已有草稿：请直接打开，或另换一个 id。`
            : `${errorCode(e.code)} · ${e.message}`,
        );
      } else {
        setCreateErr(e instanceof Error ? e.message : String(e));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="grid-a">
      <div className="colL">
        <div className="a-tools">
          <div className="search">
            <span className="mono" style={{ color: "var(--ink-4)" }}>
              ⌕
            </span>
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="按 id 过滤工作流"
              aria-label="过滤工作流"
            />
          </div>
          <Btn kind="pri" onClick={() => setCreating((v) => !v)}>
            新建工作流
          </Btn>
          <Btn
            onClick={() => {
              setOpening((v) => !v);
              setCreating(false);
            }}
            title="列表只列出已发布修订的工作流；按 id 可打开只有草稿的工作流"
          >
            按 id 打开
          </Btn>
          <Btn onClick={onRefresh} title="重新拉取列表与目录">
            刷新
          </Btn>
        </div>

        {opening && (
          <div className="a-tools" style={{ gap: 8, background: "var(--panel-hi)" }}>
            <span className="td-title nowrap">按 id 打开</span>
            <input
              className="in"
              style={{ flex: 1, minWidth: 0 }}
              value={openID}
              autoFocus
              placeholder="已存在草稿或修订的工作流 id"
              aria-label="按 id 打开工作流"
              onChange={(e) => setOpenID(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") setOpening(false);
                if (e.key !== "Enter") return;
                const id = openID.trim();
                const invalid = validateWorkflowID(id);
                if (invalid) {
                  setCreateErr(invalid);
                  return;
                }
                setOpening(false);
                setOpenID("");
                navigate({ name: "editor", id });
              }}
            />
            <Btn onClick={() => setOpening(false)}>取消</Btn>
            <span className="mono nowrap" style={{ fontSize: 10.5, color: "var(--ink-4)" }}>
              直接进编辑器 · 尚不存在时以空白定义开局
            </span>
          </div>
        )}

        {creating && (
          <div className="a-tools" style={{ gap: 8, background: "var(--panel-hi)" }}>
            <span className="td-title nowrap">工作流 id</span>
            <input
              className="in"
              style={{ flex: 1, minWidth: 0 }}
              value={newID}
              autoFocus
              placeholder="例如 order-sync"
              aria-label="工作流 id"
              onChange={(e) => setNewID(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void create();
                if (e.key === "Escape") setCreating(false);
              }}
            />
            <Btn kind="pri" onClick={() => void create()} disabled={busy}>
              {busy ? "创建中…" : "创建草稿"}
            </Btn>
            <Btn onClick={() => setCreating(false)} disabled={busy}>
              取消
            </Btn>
            {createErr && (
              <span role="alert" className="c-er nowrap" style={{ fontSize: 11 }}>
                {createErr}
              </span>
            )}
          </div>
        )}

        <div className="tablewrap">
          {error && (
            <div className="msg err" role="alert">
              <span className="mk rd" />
              <span>
                <b>载入失败</b>
              </span>
              <span className="txt">{error}</span>
              <span className="kk">
                <Tbtn onClick={onRefresh}>重试</Tbtn>
              </span>
            </div>
          )}
          {workflows == null && !error && <div className="loading">载入工作流列表…</div>}
          {workflows != null && rows.length === 0 && (
            <Empty>
              {q.trim() ? (
                <>
                  没有匹配「{q.trim()}」的工作流。
                  <br />
                  <span className="mono">清除过滤条件或新建一个</span>
                </>
              ) : (
                <>
                  还没有已发布的工作流。
                  <br />
                  <span className="mono">
                    点「新建工作流」创建草稿：发布后才出现不可变修订
                  </span>
                </>
              )}
            </Empty>
          )}
          {rows.length > 0 && (
            <table className="t">
              <thead>
                <tr>
                  <th>工作流</th>
                  <th style={{ width: 96 }}>已发布修订</th>
                  <th style={{ width: 132 }} />
                </tr>
              </thead>
              <tbody>
                {rows.map((w) => (
                  <tr
                    key={w.workflow_id}
                    onDoubleClick={() =>
                      navigate({ name: "editor", id: w.workflow_id })
                    }
                  >
                    <td>
                      <span className="nm">{w.workflow_id}</span>
                      <span className="sub">
                        <span className="mono">GET /workflows/{w.workflow_id}/draft</span>
                      </span>
                    </td>
                    <td className="num">
                      {(w.revision ?? 0) > 0 ? (
                        <span>{w.revision}</span>
                      ) : (
                        <span className="dim" style={{ fontSize: 11 }}>
                          未发布
                        </span>
                      )}
                    </td>
                    <td>
                      <span className="acts">
                        <Tbtn
                          onClick={() =>
                            navigate({ name: "editor", id: w.workflow_id })
                          }
                        >
                          打开编辑器
                        </Tbtn>
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <div className="rowhint">
          <Ticks width={72} />
          <span>
            共 {rows.length} 项 · 已发布 {publishedCount} 项 · 双击行或点「打开编辑器」进入草稿
          </span>
          <span className="grow" />
          <span className="mono">
            列表仅含已发布过修订的工作流 · 草稿可反复覆盖 · 修订不可变
          </span>
        </div>
      </div>

      <div className="colR">
        <CatalogPane nodeTypes={nodeTypes} />
        <EnvPane conn={conn} features={features} />
        <section className="pane">
          <div className="ph">
            <h2>工作台约定</h2>
          </div>
          <div className="pnote" style={{ padding: "9px 14px" }}>
            Artifact = 定义 + 呈现。定义决定执行与摘要，呈现只影响画布外观，
            不参与执行。发布分配不可变修订；运行接受草稿 etag 快照或已发布修订号。
          </div>
        </section>
      </div>
    </div>
  );
}
