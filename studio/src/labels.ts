// 后端词表 → 中文。字符串与 Go 侧常量逐字对应（types.go §8），
// 不做本地化转换，只做呈现映射；未知值原样透出，不吞。

export type Tone = "ok" | "wa" | "er" | "nr" | "none";

export interface Term {
  zh: string;
  tone: Tone;
}

const toneClass: Record<Tone, string> = {
  ok: "c-ok",
  wa: "c-wa",
  er: "c-er",
  nr: "c-nr",
  none: "",
};

const scrToneClass: Record<Tone, string> = {
  ok: "s-ok",
  wa: "s-wa",
  er: "s-er",
  nr: "s-nr",
  none: "s-nr",
};

export function pTone(tone: Tone): string {
  return toneClass[tone];
}
export function sTone(tone: Tone): string {
  return scrToneClass[tone];
}

// --- 运行状态（RunStatus） ---

const RUN_STATUS: Record<string, Term> = {
  admitted: { zh: "已受理", tone: "nr" },
  running: { zh: "运行中", tone: "nr" },
  waiting: { zh: "等待中", tone: "wa" },
  succeeded: { zh: "成功", tone: "ok" },
  failed: { zh: "失败", tone: "er" },
  cancelled: { zh: "已取消", tone: "none" },
  recovery_required: { zh: "需恢复处置", tone: "er" },
};

export function runStatus(s: string | undefined): Term {
  if (!s) return { zh: "—", tone: "none" };
  return RUN_STATUS[s] ?? { zh: s, tone: "none" };
}

export const RUN_STATUS_TERMINAL = new Set(["succeeded", "failed", "cancelled"]);

// --- 节点状态（NodeState） ---

const NODE_STATE: Record<string, Term> = {
  not_started: { zh: "未开始", tone: "none" },
  running: { zh: "运行中", tone: "nr" },
  waiting: { zh: "等待中", tone: "wa" },
  completed: { zh: "已完成", tone: "ok" },
  degraded: { zh: "降级完成", tone: "wa" },
  failed: { zh: "失败", tone: "er" },
  cancelled: { zh: "已取消", tone: "none" },
  skipped: { zh: "已跳过", tone: "none" },
  unknown: { zh: "未知", tone: "none" },
};

export function nodeState(s: string | undefined): Term {
  if (!s) return { zh: "—", tone: "none" };
  return NODE_STATE[s] ?? { zh: s, tone: "none" };
}

// --- 事件类型（EventKind） ---

const EVENT_KIND: Record<string, Term> = {
  run_admitted: { zh: "运行受理", tone: "none" },
  run_started: { zh: "运行开始", tone: "nr" },
  run_waiting: { zh: "运行等待", tone: "wa" },
  run_resumed: { zh: "运行继续", tone: "nr" },
  run_succeeded: { zh: "运行成功", tone: "ok" },
  run_failed: { zh: "运行失败", tone: "er" },
  run_cancelled: { zh: "运行取消", tone: "none" },
  run_recovery_required: { zh: "需恢复处置", tone: "er" },
  node_attempt: { zh: "节点尝试", tone: "none" },
  node_started: { zh: "节点开始", tone: "nr" },
  node_completed: { zh: "节点完成", tone: "ok" },
  node_degraded: { zh: "节点降级", tone: "wa" },
  node_wait: { zh: "节点等待", tone: "wa" },
  node_failed: { zh: "节点失败", tone: "er" },
  switch_decision: { zh: "分支判定", tone: "none" },
  repeat_iteration: { zh: "循环迭代", tone: "none" },
};

export function eventKind(k: string): Term {
  return EVENT_KIND[k] ?? { zh: k, tone: "none" };
}

// --- 能力（capabilities.features） ---

const CAPABILITY: Record<string, string> = {
  draft: "草稿",
  publish: "发布",
  runs: "运行",
  sse: "事件流",
  resume: "等待恢复",
  cancel: "取消",
};

export function capability(k: string): string {
  return CAPABILITY[k] ?? k;
}

// --- 等待项类型（WaitRequest.kind） ---

const WAIT_KIND: Record<string, string> = {
  manual: "人工应答",
  approval: "人工审批",
};

export function waitKind(k: string | undefined): string {
  if (!k) return "人工应答";
  return WAIT_KIND[k] ?? k;
}

// --- 错误码（§11.4 envelope.code） ---

const ERROR_CODE: Record<string, string> = {
  revision_conflict: "修订冲突",
  invalid_artifact: "定义不合法",
  invalid_request: "请求不合法",
  validation_failed: "校验未通过",
  not_found: "对象不存在",
  unauthorized: "未认证",
  forbidden: "无权限",
  run_not_waiting: "运行不处于等待",
  wait_incomplete: "等待项未全部应答",
  idempotency_conflict: "幂等键冲突",
  definition_too_large: "定义超出体积上限",
  limit_exceeded: "超出引擎上限",
  cancelled: "已取消",
  recovery_required: "需恢复处置",
  http_error: "网络错误",
  internal: "服务内部错误",
};

export function errorCode(code: string): string {
  return ERROR_CODE[code] ?? code;
}

// 后端消息保持原文（诊断用），中文标题在前，原文随后。
export function errorHead(code: string): string {
  return ERROR_CODE[code] ?? code;
}

// --- 节点 kind ---

const NODE_KIND: Record<string, string> = {
  call: "调用",
  switch: "分支",
  select: "选择",
  repeat: "循环",
};

export function nodeKind(k: string | undefined): string {
  if (!k) return "—";
  return NODE_KIND[k] ?? k;
}

// --- 目录条目呈现 ---

// 目录自带的 display.title 是英文（由宿主注册），界面全中文：
// 按 type_id 给出中文名，未知类型回落到 display.title，再回落到
// type_id 本身。type_id 始终同屏显示，不隐藏权威标识。
const TYPE_NAME: Record<string, string> = {
  "inofy.value@1": "值",
  "inofy.template@1": "模板",
  "inofy.wait@1": "人工等待",
  "inofy.model.openai@1": "OpenAI 兼容模型",
};

export function typeName(d: {
  type_id: string;
  display?: { title?: string; subtitle?: string; [k: string]: unknown };
}): string {
  return TYPE_NAME[d.type_id] ?? d.display?.title ?? d.type_id;
}

// 重放语义：pure=可重放，non_replayable=不可重放。
export type ReplaySemantics = "pure" | "non_replayable" | "unknown";

export function replayOf(d: {
  replay?: string;
  capabilities?: string[];
}): ReplaySemantics {
  if (d.replay === "pure") return "pure";
  if (d.replay === "non_replayable") return "non_replayable";
  if (d.capabilities?.includes("replay")) return "pure";
  if (d.capabilities?.includes("non_replay")) return "non_replayable";
  return "unknown";
}

export function replayLabel(r: ReplaySemantics): string {
  switch (r) {
    case "pure":
      return "纯函数（可重放）";
    case "non_replayable":
      return "不可重放";
    default:
      return "重放语义未知";
  }
}
