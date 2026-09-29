// 编辑器页：一份草稿的权威操作面。服务端是权威——保存走 etag CAS
// （首发 If-None-Match: *，其后 If-Match），过期 etag 显式暴露为冲突，
// 且绝不静默丢弃用户本地改动。校验/发布/运行都作用在「已保存的草稿」
// 上，因此有未保存改动时三者锁定，而不是拿内存定义去猜服务端会看到什么。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  Artifact,
  DraftView,
  NodeDescriptor,
  PublishView,
} from "../schema";
import { TransportError, type StudioTransport } from "../transport";
import { Editor, NODE_TYPE_MIME } from "../Editor";
import { NodeProperties } from "../components/NodeProperties";
import { Btn, Empty, Loading, Mk, Tag, Tbtn } from "../components/Ui";
import { navigate, routeHref } from "../router";
import { addNodeAt, patchNode, removeNode, setExit } from "../edit";
import { blankArtifact } from "../seed";
import { replayLabel, replayOf, typeName } from "../labels";
import { t } from "../i18n";

interface Props {
  transport: StudioTransport;
  workflowId: string;
  catalog: NodeDescriptor[] | null;
  publishedRevision?: number;
  onWorkflowsChanged(): void;
}

// 后端 Diagnostic = {check, path, code, message}，没有 severity：
// 每条发现都是拒绝理由，所以这里不编造等级。
interface Diag {
  check: string;
  path: string;
  code: string;
  message: string;
}

const CHECK: Record<string, string> = {
  schema: "结构",
  topology: "拓扑",
  capability: "能力",
  budget: "预算",
  authority: "权限",
};

function checkLabel(c: string): string {
  return CHECK[c] ?? c;
}

function asDiags(v: unknown): Diag[] | null {
  if (v == null) return null;
  const items = Array.isArray(v) ? v : [v];
  return items.map((item) => {
    if (typeof item === "string") {
      return { check: "—", path: "", code: "", message: item };
    }
    if (item && typeof item === "object") {
      const o = item as Record<string, unknown>;
      return {
        check: typeof o.check === "string" ? o.check : "—",
        path: typeof o.path === "string" ? o.path : "",
        code: typeof o.code === "string" ? o.code : "",
        message:
          typeof o.message === "string" ? o.message : JSON.stringify(o),
      };
    }
    return { check: "—", path: "", code: "", message: String(item) };
  });
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function shortDigest(d: string | undefined): string {
  if (!d) return "—";
  return d.length > 16 ? `${d.slice(0, 16)}…` : d;
}

export function EditorPage({
  transport,
  workflowId,
  catalog,
  publishedRevision,
  onWorkflowsChanged,
}: Props) {
  const [draft, setDraft] = useState<DraftView | null>(null);
  const [artifact, setArtifact] = useState<Artifact | null>(null);
  const [dirty, setDirty] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [conflict, setConflict] = useState<{ code: string; message: string } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [diags, setDiags] = useState<Diag[] | null>(null);
  const [diagsAt, setDiagsAt] = useState<string | null>(null);
  const [diagOpen, setDiagOpen] = useState(true);
  const [published, setPublished] = useState<PublishView | null>(null);
  const past = useRef<Artifact[]>([]);
  const future = useRef<Artifact[]>([]);
  // 撤销栈需要「上一个 artifact」，但不能写在 setState 更新函数里
  // （StrictMode 会双调用更新函数，副作用会被执行两次）。
  const latest = useRef<Artifact | null>(null);
  latest.current = artifact;

  const load = useCallback(async () => {
    setLoading(true);
    setLoadErr(null);
    setErr(null);
    setConflict(null);
    setNotice(null);
    setSelected(null);
    past.current = [];
    future.current = [];
    try {
      const d = await transport.loadDraft(workflowId);
      setDraft(d);
      setArtifact(d.artifact);
      setDirty(false);
    } catch (e) {
      // 412 = 该工作流还没有草稿：以最小可校验定义开局，
      // 首次保存用 If-None-Match: * 真正创建它。
      if (e instanceof TransportError && e.status === 412) {
        const a = blankArtifact(workflowId);
        setDraft({ workflow: workflowId, etag: "", artifact: a });
        setArtifact(a);
        setDirty(true);
      } else {
        setDraft(null);
        setArtifact(null);
        setLoadErr(errText(e));
      }
    } finally {
      setLoading(false);
    }
  }, [transport, workflowId]);

  useEffect(() => {
    void load();
  }, [load]);

  // 最近一次发布的回执（修订号 + 定义摘要），只读展示。
  useEffect(() => {
    if (publishedRevision == null) {
      setPublished(null);
      return;
    }
    let live = true;
    transport
      .getRevision(workflowId, publishedRevision)
      .then((rv) => {
        if (live) setPublished(rv);
      })
      .catch(() => {
        if (live) setPublished({ workflow: workflowId, revision: publishedRevision });
      });
    return () => {
      live = false;
    };
  }, [transport, workflowId, publishedRevision]);

  const edit = useCallback((a: Artifact) => {
    const prev = latest.current;
    if (prev) {
      past.current.push(prev);
      if (past.current.length > 200) past.current.shift();
    }
    future.current = [];
    setArtifact(a);
    setDirty(true);
    setNotice(null);
  }, []);

  const undo = useCallback(() => {
    const prev = past.current.pop();
    const cur = latest.current;
    if (!prev || !cur) return;
    future.current.push(cur);
    setArtifact(prev);
    setDirty(true);
    setNotice(null);
  }, []);

  const redo = useCallback(() => {
    const next = future.current.pop();
    const cur = latest.current;
    if (!next || !cur) return;
    past.current.push(cur);
    setArtifact(next);
    setDirty(true);
    setNotice(null);
  }, []);

  const canRelease = draft != null && draft.etag !== "" && !dirty;

  const save = useCallback(async () => {
    const a = latest.current;
    if (!a || !draft || busy != null) return;
    setBusy("save");
    setErr(null);
    setConflict(null);
    try {
      const saved = await transport.saveDraft(
        workflowId,
        a,
        draft.etag === "" ? null : draft.etag,
      );
      setDraft(saved);
      setDirty(false);
      setNotice(`草稿已保存 · etag ${saved.etag}`);
    } catch (e) {
      if (e instanceof TransportError && (e.status === 412 || e.status === 409)) {
        setConflict({ code: e.code, message: e.message });
      } else {
        setErr(errText(e));
      }
    } finally {
      setBusy(null);
    }
  }, [busy, draft, transport, workflowId]);

  // 冲突的两条出路都必须显式选择：覆盖服务端，或丢弃本地。
  const overwriteWithLocal = async () => {
    const a = latest.current;
    if (!a || busy != null) return;
    setBusy("conflict");
    setErr(null);
    try {
      const fresh = await transport.loadDraft(workflowId);
      const saved = await transport.saveDraft(workflowId, a, fresh.etag);
      setDraft(saved);
      setDirty(false);
      setConflict(null);
      setNotice(`已用本地内容覆盖服务端草稿 · etag ${saved.etag}`);
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(null);
    }
  };

  const adoptServer = async () => {
    if (busy != null) return;
    setBusy("conflict");
    setErr(null);
    try {
      const fresh = await transport.loadDraft(workflowId);
      setDraft(fresh);
      setArtifact(fresh.artifact);
      setDirty(false);
      setConflict(null);
      setSelected(null);
      past.current = [];
      future.current = [];
      setNotice("已采用服务端草稿：本地未保存的改动已丢弃。");
    } catch (e) {
      setErr(errText(e));
    } finally {
      setBusy(null);
    }
  };

  const validate = async () => {
    if (!draft || draft.etag === "" || busy != null) return;
    setBusy("validate");
    setErr(null);
    setConflict(null);
    setNotice(null);
    try {
      const r = await transport.validate(workflowId, draft.etag);
      const list = asDiags(r.diagnostics) ?? [];
      setDiags(list);
      setDiagsAt(draft.etag);
      setDiagOpen(true);
      if (list.length === 0) setNotice("校验通过：定义满足全部检查。");
    } catch (e) {
      if (e instanceof TransportError) {
        const list = asDiags(e.diagnostics);
        setDiags(
          list ?? [{ check: "—", path: "", code: e.code, message: e.message }],
        );
        setDiagsAt(draft.etag);
        setDiagOpen(true);
        if (!list) setErr(`${e.code} · ${e.message}`);
      } else {
        setErr(errText(e));
      }
    } finally {
      setBusy(null);
    }
  };

  const publish = async () => {
    if (!draft || draft.etag === "" || busy != null) return;
    setBusy("publish");
    setErr(null);
    setConflict(null);
    setNotice(null);
    try {
      const rv = await transport.publish(workflowId, draft.etag);
      setPublished(rv);
      onWorkflowsChanged();
      setNotice(
        `已发布修订 r${rv.revision} · 定义摘要 ${shortDigest(rv.definition_digest)}`,
      );
    } catch (e) {
      if (e instanceof TransportError && (e.status === 412 || e.status === 409)) {
        setConflict({ code: e.code, message: e.message });
      } else if (e instanceof TransportError && e.diagnostics) {
        setDiags(asDiags(e.diagnostics) ?? null);
        setDiagsAt(draft.etag);
        setDiagOpen(true);
        setErr(`${e.code} · ${e.message}`);
      } else {
        setErr(errText(e));
      }
    } finally {
      setBusy(null);
    }
  };

  const run = async () => {
    if (!draft || draft.etag === "" || busy != null) return;
    setBusy("run");
    setErr(null);
    setConflict(null);
    try {
      const r = await transport.startRun({
        workflow: workflowId,
        draft_etag: draft.etag,
        input: {},
      });
      navigate({ name: "runs", id: r.run_id });
    } catch (e) {
      if (e instanceof TransportError && (e.status === 412 || e.status === 409)) {
        setConflict({ code: e.code, message: e.message });
      } else {
        setErr(errText(e));
      }
    } finally {
      setBusy(null);
    }
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey)) return;
      const k = e.key.toLowerCase();
      if (k === "s") {
        e.preventDefault();
        void save();
        return;
      }
      if (k !== "z") return;
      e.preventDefault();
      if (e.shiftKey) redo();
      else undo();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [save, undo, redo]);

  const selectedNode = useMemo(() => {
    if (!artifact || !selected) return null;
    return (
      artifact.definition.graph.nodes.find((n) => n.id === selected) ?? null
    );
  }, [artifact, selected]);

  const byType = useMemo(
    () => new Map((catalog ?? []).map((d) => [d.type_id, d])),
    [catalog],
  );

  const addByClick = (typeID: string) => {
    if (!artifact) return;
    const n = artifact.definition.graph.nodes.length;
    const { artifact: next, id } = addNodeAt(artifact, typeID, {
      x: 60 + (n % 3) * 280,
      y: 60 + Math.floor(n / 3) * 180,
    });
    edit(next);
    setSelected(id);
  };

  if (loading) {
    return (
      <div className="epage">
        <Loading>载入草稿 {workflowId}…</Loading>
      </div>
    );
  }

  if (loadErr != null || !draft || !artifact) {
    return (
      <div className="epage">
        <div className="statusband">
          <div className="msg err" role="alert">
            <span className="mk rd" />
            <span>
              <b>载入失败</b>
            </span>
            <span className="txt">{loadErr}</span>
            <span className="kk">
              <Tbtn onClick={() => void load()}>重试</Tbtn>
              <a className="tbtn rail" href={routeHref({ name: "workflows" })}>
                返回列表
              </a>
            </span>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="epage">
      <div className="edbar">
        <a className="tbtn rail" href={routeHref({ name: "workflows" })}>
          ← 工作流
        </a>
        <span className="sep" />
        <span className="wfn">{workflowId}</span>
        {published ? (
          <Tag
            on
            title={`最近发布修订 r${published.revision} · 定义摘要 ${published.definition_digest ?? "—"}`}
          >
            <Mk shape="round" tone="c-ok" />
            修订 r{published.revision}
          </Tag>
        ) : publishedRevision != null ? (
          <Tag title={`已发布修订 r${publishedRevision}`}>
            <Mk shape="round" tone="c-ok" />
            修订 r{publishedRevision}
          </Tag>
        ) : (
          <Tag soft title="尚未发布任何不可变修订">
            尚无修订
          </Tag>
        )}
        {dirty && (
          <Tag on title="本地有未保存的改动">
            <Mk tone="c-wa" />
            {t("editor.dirty")}
          </Tag>
        )}
        <span className="sep" />
        <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
          draft etag {draft.etag === "" ? "—（保存时创建）" : draft.etag}
        </span>
        <span className="grow" />
        <span className="note">
          {canRelease
            ? "草稿已落库：校验 / 发布 / 运行都作用在这份快照上"
            : "有未保存改动：发布与运行锁定在已保存草稿上"}
        </span>
        <Btn
          onClick={() => void save()}
          disabled={busy != null || !dirty}
          title="保存草稿（Ctrl+S）"
        >
          {busy === "save" ? "保存中…" : t("action.save")}
        </Btn>
        <Btn
          onClick={() => void validate()}
          disabled={busy != null || !canRelease}
          title={canRelease ? "编译草稿并列出全部诊断" : "请先保存草稿"}
        >
          {busy === "validate" ? "校验中…" : t("action.validate")}
        </Btn>
        <Btn
          kind="pri"
          onClick={() => void publish()}
          disabled={busy != null || !canRelease}
          title={canRelease ? "分配下一个不可变修订" : "请先保存草稿"}
        >
          {busy === "publish" ? "发布中…" : t("action.publish")}
        </Btn>
        <Btn
          kind="deep"
          onClick={() => void run()}
          disabled={busy != null || !canRelease}
          title={canRelease ? "以当前草稿 etag 快照启动一次运行" : "请先保存草稿"}
        >
          {busy === "run" ? "启动中…" : t("action.run")}
        </Btn>
      </div>

      {(conflict || err || dirty || notice) && (
        <div className="statusband">
          {conflict && (
            <div className="msg err" role="alert">
              <span className="mk rd" />
              <span>
                <b>修订冲突</b>
              </span>
              <span className="txt">
                <span className="mono">{conflict.code}</span> · {conflict.message}
              </span>
              <span className="kk">
                <Tbtn
                  onClick={() => void overwriteWithLocal()}
                  disabled={busy != null}
                  title="取服务端最新 etag，再用本地内容覆盖草稿"
                >
                  保留本地并覆盖
                </Tbtn>
                <Tbtn
                  onClick={() => void adoptServer()}
                  disabled={busy != null}
                  title="重新载入服务端草稿，丢弃本地未保存改动"
                >
                  放弃本地，采用服务端
                </Tbtn>
              </span>
            </div>
          )}
          {err && !conflict && (
            <div className="msg err" role="alert">
              <span className="mk rd" />
              <span>
                <b>出错</b>
              </span>
              <span className="txt">{err}</span>
              <span className="kk">
                <Tbtn onClick={() => setErr(null)}>关闭</Tbtn>
              </span>
            </div>
          )}
          {dirty && (
            <div className="msg warn">
              <span className="mk rd" />
              <span>
                <b>未保存</b>
              </span>
              <span className="txt">
                {draft.etag === ""
                  ? "该工作流还没有服务端草稿：首次保存会用 If-None-Match: * 创建它。"
                  : t("editor.unsaved.hint")}
              </span>
              <span className="kk">
                <Tbtn onClick={() => void save()} disabled={busy != null}>
                  保存草稿
                </Tbtn>
                <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
                  Ctrl+S
                </span>
              </span>
            </div>
          )}
          {notice && !conflict && !err && (
            <div className="msg ok">
              <span className="mk rd" />
              <span>
                <b>完成</b>
              </span>
              <span className="txt">{notice}</span>
              <span className="kk">
                <Tbtn onClick={() => setNotice(null)}>关闭</Tbtn>
              </span>
            </div>
          )}
        </div>
      )}

      <div className="gedit">
        <div className="col">
          <div className="ph">
            <h2>节点库</h2>
            <span className="grow" />
            <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
              {catalog ? `${catalog.length} 类` : "…"}
            </span>
          </div>
          <div className="collist">
            {!catalog ? (
              <Loading>载入目录…</Loading>
            ) : catalog.length === 0 ? (
              <div className="pnote">
                目录为空：宿主未注册节点类型，画布无法新增节点。
              </div>
            ) : (
              catalog.map((d) => {
                const replay = replayOf(d);
                return (
                  <button
                    key={d.type_id}
                    type="button"
                    className="lib"
                    draggable
                    title={`${d.type_id} · 拖到画布放置，点击追加到栅格`}
                    onDragStart={(e) => {
                      e.dataTransfer.setData(NODE_TYPE_MIME, d.type_id);
                      e.dataTransfer.effectAllowed = "copy";
                    }}
                    onClick={() => addByClick(d.type_id)}
                  >
                    <span className="grip" />
                    <span className="ln">
                      <span className="zh">{typeName(d)}</span>
                      <span className="grow" />
                      {d.supports_wait && <Mk shape="pause" tone="c-wa" title="支持等待" />}
                    </span>
                    <span className="tid">{d.type_id}</span>
                    <span className="caps">
                      <Tag title={replayLabel(replay)}>
                        <Mk
                          shape={replay === "pure" ? "round" : "square"}
                          tone={replay === "pure" ? "c-ok" : "c-nr"}
                        />
                        {replay === "pure"
                          ? "可重放"
                          : replay === "non_replayable"
                            ? "不可重放"
                            : "未知"}
                      </Tag>
                    </span>
                  </button>
                );
              })
            )}
          </div>
          <div className="libnote">
            <Mk shape="hollow" tone="c-sg" />
            拖到画布放置 · 点击追加
          </div>
        </div>

        <div className="col">
          <Editor
            artifact={artifact}
            catalog={catalog ?? undefined}
            selected={selected}
            onSelect={setSelected}
            onArtifactChange={edit}
          />
        </div>

        <div className="col">
          {selectedNode ? (
            <NodeProperties
              key={selectedNode.id}
              node={selectedNode}
              descriptor={byType.get(selectedNode.type ?? "")}
              isExit={artifact.definition.graph.exits.includes(selectedNode.id)}
              onChange={(n) => edit(patchNode(artifact, n))}
              onToggleExit={(on) => edit(setExit(artifact, selectedNode.id, on))}
              onDelete={() => {
                edit(removeNode(artifact, selectedNode.id));
                setSelected(null);
              }}
            />
          ) : (
            <aside className="pp" aria-label={t("prop.node")}>
              <div className="ph">
                <h2>属性</h2>
                <span className="grow" />
                <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
                  未选中
                </span>
              </div>
              <Empty>
                {t("node.select")}
                <br />
                <span className="mono">点击画布节点，或从左侧节点库拖入一个新节点</span>
              </Empty>
            </aside>
          )}
        </div>
      </div>

      <div className={`diag${diagOpen ? "" : " collapsed"}`}>
        <div className="dh">
          <Mk
            shape="round"
            tone={diags == null ? "c-nr" : diags.length > 0 ? "c-er" : "c-ok"}
          />
          <span
            style={{
              font: "600 11px/1 var(--sans)",
              letterSpacing: ".16em",
              color: "var(--ink-2)",
            }}
          >
            诊断
          </span>
          <span className="grow" />
          <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
            {diags == null
              ? "尚未校验"
              : diags.length === 0
                ? "无问题"
                : `${diags.length} 项`}
          </span>
          {diagsAt && (
            <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-4)" }}>
              etag {diagsAt}
            </span>
          )}
          <Tbtn onClick={() => setDiagOpen((v) => !v)}>
            {diagOpen ? "收起" : "展开"}
          </Tbtn>
        </div>
        {diagOpen && diags != null && (
          <div className="dbody">
            {diags.length === 0 ? (
              <div className="drow">
                <Mk shape="round" tone="c-ok" />
                <span className="sev">通过</span>
                <span className="txt">
                  结构 / 拓扑 / 能力 / 预算 / 权限 全部检查通过。
                </span>
                <span className="path">etag {diagsAt ?? "—"}</span>
              </div>
            ) : (
              diags.map((d, i) => (
                <div className="drow" key={`${d.path}-${d.code}-${i}`}>
                  <Mk tone="c-er" />
                  <span className="sev">{checkLabel(d.check)}</span>
                  <span className="txt">
                    {d.message}
                    {d.code && (
                      <>
                        {" "}
                        <span className="mono" style={{ color: "var(--ink-3)" }}>
                          {d.code}
                        </span>
                      </>
                    )}
                  </span>
                  <span className="path">{d.path}</span>
                </div>
              ))
            )}
          </div>
        )}
      </div>
    </div>
  );
}
