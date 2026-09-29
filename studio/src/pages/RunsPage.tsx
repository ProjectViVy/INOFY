// 运行中心：左列运行列表、中列已提交事件账本、右列运行详情与等待续答。
//
// 账本只渲染已提交事件（seq 单调、只追加）。光标停在某个 seq 时列表
// 定格回看，光标之后的条目压暗；跟随模式用 SSE 从 lastSeq 续订
// （Last-Event-ID 语义），断线可由光标手动重连。终态运行不再订阅：
// 事件流没有「结束帧」，定格是一等状态，不是错误。
//
// 节点的输出不在事件里——事件只带 seq/kind/path/attempt 与少量元数据；
// 完整输出存在单独的执行记录上，按节点路径取（GET /runs/{id}/nodes/{path}/output）。

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  RunDetail,
  RunEvent,
  RunLimits,
  RunSummary,
  WaitRequest,
} from "../schema";
import { TransportError, type EventSubscription, type StudioTransport } from "../transport";
import { Btn, Empty, FactRow, Facts, Loading, Mk, Tag, Tbtn, Ticks } from "../components/Ui";
import {
  eventKind,
  nodeState,
  pTone,
  runStatus,
  RUN_STATUS_TERMINAL,
  waitKind,
} from "../labels";
import { navigate } from "../router";
import { parseJSONOrError } from "../edit";
import { t } from "../i18n";

interface Props {
  transport: StudioTransport;
  runId?: string;
  // 运行列表由应用根持有并轮询：页面只消费与请求刷新。
  runs: RunSummary[] | null;
  listErr: string | null;
  onRefreshRuns(silent?: boolean): Promise<void>;
}

type LiveState = "live" | "paused" | "down";

interface NodeProj {
  path: string;
  state: string;
  attempts: number;
  lastSeq: number;
  lastKind: string;
}

// 运行级终态事件：账本自己也能看出运行已停。
const TERMINAL_EVENTS = new Set([
  "run_succeeded",
  "run_failed",
  "run_cancelled",
]);

// 账本比任何快照都权威：run 级终态事件一经提交，运行不可能再有新事件。
// 事件流没有结束帧（服务端不因终态关闭连接），是否订阅由这里裁决——
// 否则「运行在首屏快照之后、订阅之前就结束」会把连接空挂到天荒地老。
function ledgerStatus(evs: RunEvent[]): string {
  const last = evs[evs.length - 1];
  if (!last) return "";
  switch (last.kind) {
    case "run_succeeded":
      return "succeeded";
    case "run_failed":
      return "failed";
    case "run_cancelled":
      return "cancelled";
    default:
      return "";
  }
}

// 终态是吸收态：任一权威源说终态即为终态。等待中被取消的运行只改聚合
// 状态、不落终态事件（dispatch.Service.Cancel 的静默路径），只认账本会
// 让界面把已结束的运行一直显示成「等待中」。
function resolveStatus(
  ledger: string,
  detail: string | undefined,
  summary: string | undefined,
): string {
  const sources = [ledger, detail ?? "", summary ?? ""];
  return (
    sources.find((s) => RUN_STATUS_TERMINAL.has(s)) ??
    sources.find((s) => s !== "") ??
    ""
  );
}

const LIMIT_LABELS: [string, string][] = [
  ["max_nodes", "节点上限"],
  ["max_edges", "边上限"],
  ["parallelism", "并行度"],
  ["max_repeat_nesting", "循环嵌套"],
  ["max_iterations", "迭代上限"],
  ["max_activations", "激活上限"],
  ["max_attempts_per_call", "单调用尝试"],
  ["node_timeout_ms", "节点超时 ms"],
  ["run_timeout_ms", "运行超时 ms"],
  ["max_definition_bytes", "定义字节"],
  ["max_node_input_bytes", "输入字节"],
  ["max_node_output_bytes", "输出字节"],
  ["max_output_bytes_total", "累计输出字节"],
  ["max_checkpoint_bytes", "检查点字节"],
  ["max_predicate_depth", "断言深度"],
  ["max_concurrent_runs", "并发运行"],
  ["max_pending_admissions", "排队受理"],
];

function errLine(e: unknown): string {
  if (e instanceof TransportError) return `${e.code} · ${e.message}`;
  return e instanceof Error ? e.message : String(e);
}

function lastSeg(p: string | undefined): string {
  if (!p) return "—";
  const i = p.lastIndexOf("/");
  return i >= 0 ? p.slice(i + 1) : p;
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

function fmtTime(s: string | undefined): string {
  if (!s) return "—";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function fmtClock(s: string | undefined): string {
  if (!s) return "—";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function eventTone(kind: string): string {
  const tone = eventKind(kind).tone;
  if (tone === "ok") return " ok";
  if (tone === "er") return " er";
  if (tone === "wa") return " wa";
  if (tone === "nr") return " sig";
  return "";
}

function dataOf(e: RunEvent): Record<string, unknown> | null {
  const d = e.data;
  if (d && typeof d === "object" && !Array.isArray(d)) {
    return d as Record<string, unknown>;
  }
  return null;
}

function limitsOf(e: RunEvent): RunLimits | null {
  const d = dataOf(e);
  const lim = d?.limits;
  if (lim && typeof lim === "object" && !Array.isArray(lim)) {
    return lim as RunLimits;
  }
  return null;
}

function projectNodes(events: RunEvent[]): NodeProj[] {
  const byPath = new Map<string, NodeProj>();
  for (const e of events) {
    if (!e.path) continue;
    const cur = byPath.get(e.path) ?? {
      path: e.path,
      state: "not_started",
      attempts: 0,
      lastSeq: e.seq,
      lastKind: e.kind,
    };
    cur.lastSeq = e.seq;
    cur.lastKind = e.kind;
    switch (e.kind) {
      case "node_attempt":
        cur.attempts = Math.max(cur.attempts, e.attempt ?? 0);
        break;
      case "node_started":
        cur.state = "running";
        break;
      case "node_completed":
        cur.state = "completed";
        break;
      case "node_degraded":
        cur.state = "degraded";
        break;
      case "node_wait":
        cur.state = "waiting";
        break;
      case "node_failed":
        cur.state = "failed";
        break;
      default:
        break;
    }
    byPath.set(e.path, cur);
  }
  return [...byPath.values()].sort((a, b) => b.lastSeq - a.lastSeq);
}

function schemaDefault(schema: unknown): unknown {
  if (!schema || typeof schema !== "object" || Array.isArray(schema)) return {};
  const s = schema as Record<string, unknown>;
  switch (s.type) {
    case "string":
      return "";
    case "number":
    case "integer":
      return 0;
    case "boolean":
      return false;
    case "array":
      return [];
    case "object": {
      const props = s.properties;
      if (props && typeof props === "object" && !Array.isArray(props)) {
        const out: Record<string, unknown> = {};
        for (const k of Object.keys(props as Record<string, unknown>)) {
          out[k] = schemaDefault((props as Record<string, unknown>)[k]);
        }
        return out;
      }
      return {};
    }
    default:
      return {};
  }
}

function answerTemplate(w: WaitRequest): string {
  if (w.answer_schema == null) return "{}";
  return JSON.stringify(schemaDefault(w.answer_schema), null, 2);
}

function mkShape(status: string): "round" | "square" | "pause" {
  if (status === "waiting") return "pause";
  if (RUN_STATUS_TERMINAL.has(status)) return "round";
  return "square";
}

// 节点状态标记：完成=圆，等待=双竖，其余=方块。
function nodeShape(state: string): "round" | "square" | "pause" {
  if (state === "completed") return "round";
  if (state === "waiting") return "pause";
  return "square";
}

export function RunsPage({
  transport,
  runId,
  runs,
  listErr,
  onRefreshRuns,
}: Props) {
  const [q, setQ] = useState("");
  const [detail, setDetail] = useState<RunDetail | null>(null);
  const [events, setEvents] = useState<RunEvent[]>([]);
  const [evErr, setEvErr] = useState<string | null>(null);
  const [live, setLive] = useState<LiveState>("paused");
  const [cursor, setCursor] = useState<number | null>(null);
  const [selNode, setSelNode] = useState<string | null>(null);
  const [out, setOut] = useState<{ node: string; value: unknown } | null>(null);
  const [outErr, setOutErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [answers, setAnswers] = useState<Record<string, string>>({});

  const subRef = useRef<EventSubscription | null>(null);
  const lastSeqRef = useRef(0);
  const bodyRef = useRef<HTMLDivElement>(null);
  const atBottomRef = useRef(true);

  const loadDetail = useCallback(
    async (id: string): Promise<RunDetail | null> => {
      try {
        const d = await transport.getRun(id);
        setDetail(d);
        return d;
      } catch (e) {
        setActionErr(errLine(e));
        return null;
      }
    },
    [transport],
  );

  const loadEvents = useCallback(
    async (id: string): Promise<RunEvent[]> => {
      setEvErr(null);
      try {
        const page = await transport.events(id);
        setEvents(page.events);
        lastSeqRef.current = page.events.length
          ? (page.events[page.events.length - 1]?.seq ?? 0)
          : 0;
        return page.events;
      } catch (e) {
        setEvErr(errLine(e));
        return [];
      }
    },
    [transport],
  );

  const stopLive = useCallback((mode: LiveState) => {
    subRef.current?.close();
    subRef.current = null;
    setLive(mode);
  }, []);

  const startLive = useCallback(
    (id: string) => {
      subRef.current?.close();
      setLive("live");
      setActionErr(null);
      subRef.current = transport.subscribeEvents(
        id,
        lastSeqRef.current > 0 ? lastSeqRef.current : undefined,
        (e) => {
          if (e.seq <= lastSeqRef.current) return;
          lastSeqRef.current = e.seq;
          setEvents((prev) => {
            const next = [...prev, e];
            return next.length > 5000 ? next.slice(next.length - 5000) : next;
          });
          // 运行级事件改的是运行的聚合状态（状态、等待项）：重新取一次
          // 详情，等待项才会随 run_waiting / run_resumed 出现与清空。
          if (e.kind.startsWith("run_")) {
            void loadDetail(id);
            void onRefreshRuns(true);
          }
          if (TERMINAL_EVENTS.has(e.kind)) stopLive("paused");
        },
        () => setLive("down"),
      );
    },
    [loadDetail, onRefreshRuns, stopLive, transport],
  );

  // 切换运行：清空投影，先取快照与事件，再按状态决定是否跟随。
  useEffect(() => {
    subRef.current?.close();
    subRef.current = null;
    lastSeqRef.current = 0;
    setEvents([]);
    setEvErr(null);
    setDetail(null);
    setCursor(null);
    setSelNode(null);
    setOut(null);
    setOutErr(null);
    setActionErr(null);
    setAnswers({});
    setLive("paused");
    if (!runId) return;
    let alive = true;
    void (async () => {
      const d = await loadDetail(runId);
      const evs = await loadEvents(runId);
      if (!alive) return;
      const closed = ledgerStatus(evs) !== "";
      if (d && !RUN_STATUS_TERMINAL.has(d.status) && !closed) startLive(runId);
    })();
    return () => {
      alive = false;
      subRef.current?.close();
      subRef.current = null;
    };
  }, [runId, loadDetail, loadEvents, startLive]);

  const summary = useMemo(
    () => (runs ?? []).find((r) => r.run_id === runId) ?? null,
    [runs, runId],
  );

  const rows = useMemo(() => {
    const list = runs ?? [];
    const needle = q.trim().toLowerCase();
    const filtered = needle
      ? list.filter(
          (r) =>
            r.run_id.toLowerCase().includes(needle) ||
            (r.workflow_id ?? "").toLowerCase().includes(needle),
        )
      : list;
    return [...filtered].sort((a, b) =>
      (b.updated_at ?? "").localeCompare(a.updated_at ?? ""),
    );
  }, [runs, q]);

  const shown = useMemo(() => {
    if (cursor == null) return events;
    return events.filter((e) => e.seq <= cursor);
  }, [events, cursor]);

  const nodes = useMemo(() => projectNodes(shown), [shown]);

  // 跟随模式下始终贴住最新一行；回看时不动。
  useEffect(() => {
    if (cursor != null) return;
    const el = bodyRef.current;
    if (el && atBottomRef.current) el.scrollTop = el.scrollHeight;
  }, [shown, cursor]);

  const status = resolveStatus(ledgerStatus(events), detail?.status, summary?.status);
  const terminal = status !== "" && RUN_STATUS_TERMINAL.has(status);
  const st = runStatus(status || undefined);
  const waits = detail?.waits ?? [];
  const answersReady =
    waits.length > 0 &&
    waits.every((w) => {
      const text = (answers[w.request_id] ?? "").trim();
      return text !== "" && parseJSONOrError(text).ok;
    });

  const liveLabel =
    live === "live"
      ? "跟随中"
      : live === "down"
        ? "已断开"
        : terminal
          ? "已定格"
          : "已暂停";
  const liveClass =
    live === "live" ? "" : live === "down" ? " down" : terminal ? " end" : " paused";

  const toggleLive = () => {
    if (!runId) return;
    if (live === "live") stopLive("paused");
    else startLive(runId);
  };

  // 终态一旦确认（哪怕由详情或列表先于账本确认），订阅就该收：服务端
  // 不会为终态关流。少了这一手，静默取消会把连接空挂、角标停在「跟随中」。
  useEffect(() => {
    if (terminal && live === "live") stopLive("paused");
  }, [terminal, live, stopLive]);

  const loadOutput = async (path: string) => {
    if (!runId) return;
    setSelNode(path);
    setOut(null);
    setOutErr(null);
    setBusy("output");
    try {
      const r = await transport.nodeOutput(runId, path);
      setOut({ node: path, value: r.output });
    } catch (e) {
      setOutErr(errLine(e));
    } finally {
      setBusy(null);
    }
  };

  const resume = async () => {
    if (!runId || waits.length === 0) return;
    const parsed: Record<string, unknown> = {};
    const bad: string[] = [];
    for (const w of waits) {
      const r = parseJSONOrError(answers[w.request_id] ?? "");
      if (r.ok) parsed[w.request_id] = r.value;
      else bad.push(w.request_id);
    }
    if (bad.length > 0) {
      setActionErr(`应答不是合法 JSON：${bad.join("、")}`);
      return;
    }
    setBusy("resume");
    setActionErr(null);
    try {
      await transport.resumeRun(runId, parsed);
      setAnswers({});
      const d = await loadDetail(runId);
      const evs = await loadEvents(runId);
      if (d && !RUN_STATUS_TERMINAL.has(d.status) && ledgerStatus(evs) === "") {
        startLive(runId);
      }
      void onRefreshRuns(true);
    } catch (e) {
      setActionErr(errLine(e));
    } finally {
      setBusy(null);
    }
  };

  const cancel = async () => {
    if (!runId) return;
    setBusy("cancel");
    setActionErr(null);
    try {
      await transport.cancelRun(runId);
      const d = await loadDetail(runId);
      // 静默取消（等待中被取消）不落终态事件，账本停在被取消之前：
      // 冻结点就是账本的最后一笔，别再等一个永远不来的结束帧。
      if (d && RUN_STATUS_TERMINAL.has(d.status)) void loadEvents(runId);
      void onRefreshRuns(true);
    } catch (e) {
      setActionErr(errLine(e));
    } finally {
      setBusy(null);
    }
  };

  // 光标按事件序号走一步：键盘也能逐条回看，不必把每一行做成焦点。
  const stepCursor = (delta: number) => {
    if (events.length === 0) return;
    const base =
      cursor == null
        ? events.length - 1
        : Math.max(
            0,
            events.findIndex((e) => e.seq === cursor),
          );
    const next = Math.min(events.length - 1, Math.max(0, base + delta));
    setCursor(events[next]?.seq ?? null);
  };

  return (
    <div className="grid-c">
      <div className="col">
        <div className="a-tools">
          <div className="search">
            <span className="mono" style={{ color: "var(--ink-4)" }}>
              ⌕
            </span>
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="按 run id 或工作流过滤"
              aria-label="过滤运行"
            />
          </div>
          <Btn onClick={() => void onRefreshRuns(false)} title="重新拉取运行列表">
            刷新
          </Btn>
        </div>
        <div className="col-scroll">
          {listErr && (
            <div className="msg err" role="alert">
              <span className="mk rd" />
              <span>
                <b>列表载入失败</b>
              </span>
              <span className="txt">{listErr}</span>
              <span className="kk">
                <Tbtn onClick={() => void onRefreshRuns(false)}>重试</Tbtn>
              </span>
            </div>
          )}
          {runs == null && !listErr && <Loading />}
          {runs != null && rows.length === 0 && (
            <Empty>
              {q.trim() ? (
                <>没有匹配「{q.trim()}」的运行。</>
              ) : (
                <>
                  还没有运行记录。
                  <br />
                  <span className="mono">在编辑器里保存草稿后点「运行」</span>
                </>
              )}
            </Empty>
          )}
          {rows.map((r) => {
            const s = runStatus(r.status);
            const on = r.run_id === runId;
            return (
              <button
                key={r.run_id}
                type="button"
                className={`runitem${on ? " on" : ""}`}
                onClick={() => navigate({ name: "runs", id: r.run_id })}
              >
                <span className="ri-l1">
                  <Mk shape={mkShape(r.status)} tone={pTone(s.tone)} />
                  <span style={{ fontWeight: 600 }}>{s.zh}</span>
                  <span className="grow" />
                  <span className="mono" style={{ fontSize: 10.5 }}>
                    {fmtClock(r.updated_at)}
                  </span>
                </span>
                <span className="ri-id">{r.run_id}</span>
                <span className="ri-l3">
                  <span className="hi">{r.workflow_id ?? "—"}</span>
                  <span>
                    {(r.revision ?? 0) > 0 ? `修订 r${r.revision}` : "草稿快照"}
                  </span>
                  <span>epoch {r.writer_epoch ?? r.epoch ?? "—"}</span>
                </span>
              </button>
            );
          })}
        </div>
        <div className="rowhint">
          <Ticks width={72} />
          <span>
            共 {rows.length} 次运行 · 每 10s 自动刷新
          </span>
          <span className="grow" />
          <span className="mono">运行一经受理即不可变</span>
        </div>
      </div>

      <div className="col">
        <div className="ledger">
          <div className="lg-head">
            <span>{t("run.events")}</span>
            <span className="m">{runId ?? "未选择运行"}</span>
            <span className="grow" />
            <div className="cursorbox" title="光标：账本的查看位置，不改变服务端状态">
              <span className="ck">CURSOR</span>
              <span className="cv">{cursor ?? (lastSeqRef.current || "—")}</span>
            </div>
          </div>

          <div className="lg-cols">
            <span style={{ textAlign: "right" }}>SEQ</span>
            <span style={{ paddingLeft: 12 }}>类型</span>
            <span>路径</span>
            <span style={{ textAlign: "right" }}>尝试</span>
          </div>

          <div
            className="ev-body scroll-dark"
            ref={bodyRef}
            onScroll={(e) => {
              const el = e.currentTarget;
              atBottomRef.current =
                el.scrollHeight - el.scrollTop - el.clientHeight < 24;
            }}
          >
            {!runId && (
              <Empty>
                从左侧选择一次运行。
                <br />
                <span className="mono">事件账本按 seq 只追加，可从任意光标重放</span>
              </Empty>
            )}
            {runId && evErr && (
              <div className="msg err" role="alert" style={{ background: "#2A1B18" }}>
                <span className="mk rd" />
                <span>
                  <b>账本载入失败</b>
                </span>
                <span className="txt" style={{ color: "var(--scr-ink)" }}>
                  {evErr}
                </span>
                <span className="kk">
                  <Tbtn onClick={() => void loadEvents(runId)}>重试</Tbtn>
                </span>
              </div>
            )}
            {runId && !evErr && events.length === 0 && (
              <div className="loading" style={{ color: "var(--scr-ink-3)" }}>
                还没有已提交事件…
              </div>
            )}
            {events.map((e) => {
              const k = eventKind(e.kind);
              const beyond = cursor != null && e.seq > cursor;
              const atCursor = cursor != null && e.seq === cursor;
              const lim = e.kind === "run_admitted" ? limitsOf(e) : null;
              const d = dataOf(e);
              const prompt =
                e.kind === "node_wait" && typeof d?.prompt === "string"
                  ? d.prompt
                  : null;
              const cause =
                e.kind === "node_degraded" && typeof d?.cause === "string"
                  ? d.cause
                  : null;
              const admitted = e.kind === "run_admitted";
              return (
                <div key={e.seq}>
                  <div
                    className={`ev${eventTone(e.kind)}${atCursor ? " last" : ""}${admitted ? " adm" : ""}${beyond ? " dim" : ""}`}
                    onClick={() => setCursor(atCursor ? null : e.seq)}
                    title="点击把光标移到这一条（再看一次即回到最新）"
                  >
                    <span className="seq">{e.seq}</span>
                    <span className="kcell">
                      <span className="kz">{k.zh}</span>
                      <span className="ke">{e.kind}</span>
                    </span>
                    <span className="pth">
                      {e.path ? (
                        <>
                          <span style={{ color: "var(--scr-ink)" }}>
                            {lastSeg(e.path)}
                          </span>{" "}
                          {e.path}
                        </>
                      ) : (
                        <span style={{ color: "var(--scr-ink-3)" }}>—</span>
                      )}
                      {prompt && <span> · {prompt}</span>}
                      {cause && <span className="s-er"> · 降级原因 {cause}</span>}
                    </span>
                    <span className="att">{e.attempt ? `#${e.attempt}` : ""}</span>
                  </div>
                  {lim && (
                    <div className="limits">
                      <div className="lt">
                        本次运行生效的引擎上限（run_admitted 快照）
                      </div>
                      <div className="facts three">
                        {LIMIT_LABELS.filter(([key]) => lim[key] != null).map(
                          ([key, label]) => (
                            <div key={key}>
                              <span className="k">{label}</span>
                              <span className="v">{String(lim[key])}</span>
                            </div>
                          ),
                        )}
                      </div>
                    </div>
                  )}
                </div>
              );
            })}
          </div>

          {cursor != null && (
            <div className="tail">
              <span>
                光标 seq {cursor} · 定格 {shown.length} / {events.length} 条
              </span>
              <span className="grow" />
              <span>之后 {events.length - shown.length} 条压暗</span>
            </div>
          )}

          <div className="lg-foot">
            <div className="replay">
              <span>已提交</span>
              <span className="cur">{events.length}</span>
              <span>条 · 最新 seq {lastSeqRef.current || "—"}</span>
              <span className={`live${liveClass}`}>
                <Mk
                  shape={live === "live" ? "round" : "square"}
                  tone={
                    live === "live"
                      ? "c-ok"
                      : live === "down"
                        ? "c-er"
                        : terminal
                          ? "c-nr"
                          : "c-wa"
                  }
                />
                {liveLabel}
              </span>
            </div>
            <Btn
              size="sm"
              onClick={toggleLive}
              disabled={!runId || terminal}
              title={
                terminal
                  ? "运行已结束：事件流不会再有新条目"
                  : live === "live"
                    ? "暂停跟随，光标停在当前位置"
                    : "从最新 seq 续订事件流"
              }
            >
              {live === "live" ? "暂停" : live === "down" ? "重连" : "跟随"}
            </Btn>
            <Btn
              size="sm"
              onClick={() => void stepCursor(-1)}
              disabled={events.length === 0}
              title="光标上移一条"
            >
              ◀
            </Btn>
            <Btn
              size="sm"
              onClick={() => void stepCursor(1)}
              disabled={events.length === 0 || cursor == null}
              title="光标下移一条"
            >
              ▶
            </Btn>
            <Btn size="sm" onClick={() => setCursor(null)} disabled={cursor == null}>
              跳到最新
            </Btn>
            <Btn
              size="sm"
              onClick={() => runId && void loadEvents(runId)}
              disabled={!runId}
              title="丢弃本地事件缓存，重新按 seq 拉取"
            >
              重放
            </Btn>
          </div>
        </div>
      </div>

      <div className="col">
        <div className="col-scroll">
          {!runId && (
            <Empty>
              未选择运行
              <br />
              <span className="mono">选择后显示状态、等待项、节点投影与输出</span>
            </Empty>
          )}

          {runId && (
            <>
              <div className="ph">
                <h2>运行</h2>
                <Tag
                  on={!terminal}
                  title={status ? `RunStatus.${status}` : undefined}
                >
                  <Mk shape={mkShape(status)} tone={pTone(st.tone)} />
                  {st.zh}
                </Tag>
                <span className="grow" />
                <span className="mono" style={{ fontSize: 10.5 }}>
                  {liveLabel}
                </span>
              </div>

              <Facts>
                <FactRow
                  k="run id"
                  v={<span style={{ fontSize: 10.5 }}>{runId}</span>}
                />
                <FactRow k="工作流" v={summary?.workflow_id ?? "—"} />
                <FactRow
                  k="来源"
                  v={
                    summary && (summary.revision ?? 0) > 0
                      ? `修订 r${summary.revision}`
                      : "草稿快照（etag）"
                  }
                />
                <FactRow k="writer epoch" v={summary?.writer_epoch ?? detail?.epoch ?? "—"} />
                <FactRow
                  k="generation"
                  v={summary?.generation ?? detail?.generation ?? "—"}
                />
                <FactRow k="创建" v={<span style={{ fontSize: 10.5 }}>{fmtTime(summary?.created_at)}</span>} />
                <FactRow k="更新" v={<span style={{ fontSize: 10.5 }}>{fmtTime(summary?.updated_at)}</span>} />
                <FactRow
                  k="已提交事件"
                  v={`${events.length} 条 · 最新 seq ${lastSeqRef.current || "—"}`}
                />
                <FactRow k="节点投影" v={`${nodes.length} 个`} />
              </Facts>

              {actionErr && (
                <div className="msg err" role="alert" style={{ marginTop: 6 }}>
                  <span className="mk rd" />
                  <span>
                    <b>操作失败</b>
                  </span>
                  <span className="txt">{actionErr}</span>
                  <span className="kk">
                    <Tbtn onClick={() => setActionErr(null)}>关闭</Tbtn>
                  </span>
                </div>
              )}

              {waits.length > 0 && (
                <div className="waitcard">
                  <div className="wc-h">
                    <Mk shape="pause" tone="c-wa" />
                    {t("run.waits")}
                    <span className="grow" />
                    <Tag>{waits.length} 项</Tag>
                  </div>
                  {waits.map((w) => {
                    const text = answers[w.request_id] ?? "";
                    const blank = text.trim() === "";
                    // 空 ≠ 错：没写应答只是「未作答」，占位符给出 schema 的形状。
                    const parsed = blank ? null : parseJSONOrError(text);
                    return (
                      <div key={w.request_id}>
                        <div className="wc-p">
                          <span className="mono">{w.request_id}</span>
                          {` · ${waitKind(w.kind)}`}
                          {w.prompt ? <> · {w.prompt}</> : null}
                        </div>
                        <div className="wc-in">
                          <span className="lb">应答 JSON</span>
                          <textarea
                            className="ta"
                            aria-label={`应答 ${w.request_id}`}
                            spellCheck={false}
                            placeholder={answerTemplate(w)}
                            readOnly={terminal}
                            value={text}
                            onChange={(e) =>
                              setAnswers((prev) => ({
                                ...prev,
                                [w.request_id]: e.target.value,
                              }))
                            }
                          />
                          <span
                            className="hl"
                            style={parsed && !parsed.ok ? { color: "var(--err)" } : undefined}
                          >
                            {terminal
                              ? "运行已终态 · 应答不再受理"
                              : blank
                                ? "未作答 · 占位符是 answer_schema 的默认形状"
                                : parsed?.ok
                                  ? "已解析 · 与 answer_schema 的匹配由后端裁决"
                                  : `JSON 无效：${parsed?.error}`}
                          </span>
                        </div>
                      </div>
                    );
                  })}
                  <div className="wc-act">
                    <Btn
                      kind="pri"
                      onClick={() => void resume()}
                      disabled={busy != null || terminal || !answersReady}
                      title={
                        terminal
                          ? "运行已终态：等待项不再可应答"
                          : answersReady
                            ? undefined
                            : "尚有等待项未作答或不是合法 JSON"
                      }
                    >
                      {busy === "resume" ? "续答中…" : t("action.resume")}
                    </Btn>
                    <span className="note">
                      {terminal
                        ? "运行已终态 · 等待项只读"
                        : "全部等待项必须一起作答 · 缺项或不合 schema 会被整次拒绝"}
                    </span>
                  </div>
                </div>
              )}

              <div className="ph">
                <h2>节点投影</h2>
                <span className="grow" />
                <span className="mono" style={{ fontSize: 10.5 }}>
                  {cursor == null ? "随账本" : `光标 ${cursor} 之前`}
                </span>
              </div>
              {nodes.length === 0 ? (
                <div className="pnote">光标之前还没有节点事件。</div>
              ) : (
                <Facts>
                  {nodes.map((n) => {
                    const s = nodeState(n.state);
                    return (
                      <div
                        key={n.path}
                        className={`drow${n.path === selNode ? "" : ""}`}
                        style={
                          n.path === selNode
                            ? { background: "var(--panel-hi)" }
                            : undefined
                        }
                      >
                        <Mk shape={nodeShape(n.state)} tone={pTone(s.tone)} />
                        <span className="sev">{s.zh}</span>
                        <span className="txt">
                          <span className="mono" style={{ fontSize: 11 }}>
                            {lastSeg(n.path)}
                          </span>
                          <span className="path"> {n.path}</span>
                        </span>
                        <span className="path">
                          <Tbtn
                            onClick={() => void loadOutput(n.path)}
                            title="取该节点的已提交输出"
                          >
                            输出
                          </Tbtn>
                          {n.attempts > 1 ? ` #${n.attempts}` : ""}
                        </span>
                      </div>
                    );
                  })}
                </Facts>
              )}

              {selNode && (
                <>
                  <div className="ph">
                    <h2>{t("run.output")}</h2>
                    <span className="grow" />
                    <span className="mono" style={{ fontSize: 10.5 }}>
                      {selNode}
                    </span>
                    <Tbtn
                      onClick={() => {
                        setSelNode(null);
                        setOut(null);
                        setOutErr(null);
                      }}
                    >
                      关闭
                    </Tbtn>
                  </div>
                  {busy === "output" && <Loading>取输出…</Loading>}
                  {outErr && (
                    <div className="msg err" role="alert">
                      <span className="mk rd" />
                      <span>
                        <b>无输出</b>
                      </span>
                      <span className="txt">{outErr}</span>
                    </div>
                  )}
                  {out && (
                    <pre className="out-pre">{JSON.stringify(out.value, null, 2)}</pre>
                  )}
                </>
              )}
            </>
          )}
        </div>

        {runId && (
          <div className="lg-foot">
            <Btn
              onClick={() => void cancel()}
              disabled={busy != null || terminal}
              title={terminal ? "运行已结束" : "请求取消：不承诺立即终止"}
            >
              {busy === "cancel" ? "取消中…" : "取消运行"}
            </Btn>
            <span className="grow" />
            <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
              {terminal ? "终态运行只读" : "取消是请求，不是承诺"}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}
