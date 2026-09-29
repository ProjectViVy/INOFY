// 属性面板：目录 schema 驱动的表单，schema 不支持时回落到
// JSON 直编。改动经 onChange 交回页面，面板本身不持有真相。
// JSON 编辑按失焦提交：输入过程中的中间态不是合法定义。

import { useMemo, useState } from "react";
import type { NodeDescriptor, Node, JsonValue } from "../schema";
import { t } from "../i18n";
import { nodeKind, replayLabel, replayOf, typeName } from "../labels";
import { Mk, Tag, Tbtn } from "./Ui";

interface Props {
  node: Node;
  descriptor?: NodeDescriptor;
  isExit: boolean;
  onChange(node: Node): void;
  onToggleExit(on: boolean): void;
  onDelete(): void;
}

type SchemaProp = { name: string; type: string; required: boolean };

// 只有纯标量属性的 object schema 才生成表单；更复杂的结构回落
// 到 JSON —— 后端始终是最终校验者。
function schemaProps(desc?: NodeDescriptor): SchemaProp[] | null {
  const s = desc?.config_schema as
    | {
        type?: string;
        properties?: Record<string, { type?: string }>;
        required?: string[];
      }
    | undefined;
  if (!s || s.type !== "object" || !s.properties) return null;
  const req = new Set(s.required ?? []);
  const props = Object.entries(s.properties).map(([name, p]) => ({
    name,
    type: p?.type ?? "string",
    required: req.has(name),
  }));
  return props.every((p) =>
    ["string", "number", "integer", "boolean"].includes(p.type),
  )
    ? props
    : null;
}

function titleOf(v: JsonValue | undefined): string {
  if (v == null) return "—";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

export function NodeProperties({
  node,
  descriptor,
  isExit,
  onChange,
  onToggleExit,
  onDelete,
}: Props) {
  const props = useMemo(() => schemaProps(descriptor), [descriptor]);
  const config = (node.config ?? {}) as Record<string, JsonValue>;
  const replay = descriptor ? replayOf(descriptor) : "unknown";

  return (
    <aside className="pp" aria-label={t("prop.node")}>
      <div className="ph">
        <h2>属性</h2>
        <Tag on>
          选中{" "}
          <span className="mono" style={{ fontSize: 10 }}>
            {node.id}
          </span>
        </Tag>
        <span className="grow" />
        <Tbtn onClick={onDelete} title="从图中移除该节点">
          删除节点
        </Tbtn>
      </div>

      <div className="field">
        <span className="k">节点 id</span>
        <span className="v">{node.id}</span>
      </div>
      <div className="field">
        <span className="k">kind</span>
        <span className="v">
          {node.kind} · {nodeKind(node.kind)}
        </span>
      </div>
      <div className="field">
        <span className="k">类型</span>
        <span className="v">{node.type ?? "—"}</span>
      </div>
      {descriptor && (
        <div className="field">
          <span className="k">类型名</span>
          <span className="v plain">{typeName(descriptor)}</span>
        </div>
      )}
      <div className="field">
        <span className="k">重放语义</span>
        <span className="v plain" style={{ color: "var(--ink-2)" }}>
          <Mk
            shape={replay === "pure" ? "round" : "square"}
            tone={replay === "pure" ? "c-ok" : "c-nr"}
          />{" "}
          {descriptor ? replayLabel(replay) : "未知（目录缺失）"}
        </span>
      </div>
      <div className="field">
        <span className="k">能力</span>
        <span className="v plain" style={{ color: "var(--ink-3)" }}>
          {descriptor?.supports_wait ? "支持等待" : "无（不支持等待）"}
        </span>
      </div>
      <div className="field">
        <span className="k">出口</span>
        <span className="v plain">
          {isExit ? (
            <>
              <Mk shape="round" tone="c-sg" /> 是 · 结果节点
            </>
          ) : (
            "否"
          )}
        </span>
      </div>
      <div className="field">
        <span className="k">路径</span>
        <span className="v">/graph/nodes/{node.id}</span>
      </div>

      <div className="in-row" style={{ paddingTop: 8, paddingBottom: 8 }}>
        <span className="k">出口 exit</span>
        <label className="checkrow" style={{ border: 0, padding: 0, flex: 1 }}>
          <input
            type="checkbox"
            checked={isExit}
            onChange={(e) => onToggleExit(e.target.checked)}
          />
          <span style={{ fontSize: 11.5, color: "var(--ink-2)" }}>
            将本节点声明为结果节点（写入 graph.exits）
          </span>
        </label>
      </div>

      {node.kind === "call" ? (
        <>
          {props ? (
            <div>
              <div className="ph" style={{ height: 28, background: "var(--panel-lo)" }}>
                <h2>{t("prop.config")}</h2>
                <span className="grow" />
                <span className="mono" style={{ fontSize: 10 }}>
                  config · {descriptor?.type_id}
                </span>
              </div>
              {props.map((p) => (
                <label key={p.name} className="in-row">
                  <span className="k">
                    {p.name}
                    {p.required && <span className="req">*</span>}
                  </span>
                  {p.type === "boolean" ? (
                    <input
                      type="checkbox"
                      style={{ accentColor: "var(--sig)" }}
                      checked={config[p.name] === true}
                      onChange={(e) =>
                        onChange({
                          ...node,
                          config: { ...config, [p.name]: e.target.checked },
                        })
                      }
                    />
                  ) : (
                    <input
                      className="in"
                      type={p.type === "string" ? "text" : "number"}
                      placeholder={p.required ? "必填" : "可选"}
                      value={config[p.name] == null ? "" : String(config[p.name])}
                      onChange={(e) => {
                        const next = { ...config };
                        if (p.type === "string") {
                          next[p.name] = e.target.value;
                        } else if (e.target.value === "") {
                          delete next[p.name];
                        } else {
                          next[p.name] = Number(e.target.value);
                        }
                        onChange({ ...node, config: next });
                      }}
                    />
                  )}
                </label>
              ))}
              {node.config != null && (
                <div className="jsonbox">
                  <div className="jb-h">
                    <span>config 现值</span>
                    <span className="grow" />
                    <span>只读</span>
                  </div>
                  <pre>{JSON.stringify(node.config, null, 2)}</pre>
                </div>
              )}
            </div>
          ) : (
            <JsonField
              key={`config:${JSON.stringify(node.config ?? {})}`}
              label={t("prop.config.raw")}
              hint={`config · ${descriptor ? typeName(descriptor) : "目录缺失"}`}
              value={node.config ?? {}}
              onApply={(v) => onChange({ ...node, config: v as JsonValue })}
            />
          )}
          <JsonField
            key={`inputs:${JSON.stringify(node.inputs ?? {})}`}
            label={t("prop.inputs")}
            hint="inputs · 上游绑定"
            value={node.inputs ?? {}}
            onApply={(v) => onChange({ ...node, inputs: v as Node["inputs"] })}
          />
        </>
      ) : (
        <div className="jsonbox">
          <div className="jb-h">
            <span>{node.kind} 节点</span>
            <span className="grow" />
            <span>JSON 只读</span>
          </div>
          <pre data-testid="node-json">{JSON.stringify(node, null, 2)}</pre>
        </div>
      )}

      {node.retry?.max_attempts != null && (
        <div className="field">
          <span className="k">重试上限</span>
          <span className="v" style={{ fontSize: 11 }}>
            {node.retry.max_attempts}
          </span>
        </div>
      )}
      <div className="pnote">
        定义决定执行与摘要；路径用于定位诊断与节点输出。改动需保存草稿后
        才能发布或运行。
      </div>
    </aside>
  );
}

// JSON 字段：输入时校验、失焦时提交。无效文本不进入定义。
function JsonField({
  label,
  hint,
  value,
  onApply,
}: {
  label: string;
  hint: string;
  value: unknown;
  onApply(v: unknown): void;
}) {
  const [text, setText] = useState(() => JSON.stringify(value, null, 2));
  const [err, setErr] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);

  const check = (next: string) => {
    setText(next);
    setDirty(true);
    if (next.trim() === "") {
      setErr(null);
      return;
    }
    try {
      JSON.parse(next);
      setErr(null);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  const commit = () => {
    if (!dirty) return;
    if (err) return;
    try {
      const parsed = text.trim() === "" ? {} : (JSON.parse(text) as unknown);
      setDirty(false);
      onApply(parsed);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div className={`jsonbox${err ? " err" : ""}`}>
      <div className="jb-h">
        <span>{label}</span>
        <span className="grow" />
        <span
          className={err ? "s-er" : dirty ? "s-warn" : ""}
          style={{ color: err ? undefined : "var(--s-ok)" }}
        >
          {err ? "JSON 无效" : dirty ? "未提交 · 失焦生效" : "● 已解析 · 有效"}
        </span>
      </div>
      <textarea
        className="ta-dark"
        aria-label={label}
        aria-invalid={err ? true : undefined}
        spellCheck={false}
        value={text}
        onChange={(e) => check(e.target.value)}
        onBlur={commit}
      />
      {err ? (
        <div className="eline">
          <Mk tone="c-er" />
          <span>
            JSON 无效：{err} · 未通过解析的文本不会写入定义
          </span>
        </div>
      ) : (
        <div
          className="jb-h"
          style={{ borderTop: "1px solid var(--scr-line)", borderBottom: 0 }}
        >
          <span>{hint}</span>
          <span className="grow" />
          <span>{titleOf(value as JsonValue)}</span>
        </div>
      )}
    </div>
  );
}
