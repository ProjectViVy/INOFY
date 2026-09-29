// 连接面板：宿主批准的 provider 绑定。key 写入后只经 env-name
// 间接寻址，界面从不回读它；列表只显示 has_secret 事实。

import { useCallback, useEffect, useState } from "react";

import type { ConnectionView } from "../schema";
import type { StudioTransport } from "../transport";
import { Btn, Mk, Pane, Tag } from "./Ui";

function errLine(e: unknown): string {
  if (e && typeof e === "object" && "message" in e) return String((e as Error).message);
  return String(e);
}

export function ConnsPane({ transport }: { transport: StudioTransport }) {
  const [items, setItems] = useState<ConnectionView[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState({ id: "", kind: "openai", base_url: "", model: "", api_key: "" });

  const refresh = useCallback(async () => {
    try {
      setItems(await transport.listConnections());
      setErr(null);
    } catch (e) {
      setErr(errLine(e));
    }
  }, [transport]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const submit = async () => {
    if (!form.id.trim() || !form.base_url.trim() || !form.model.trim()) return;
    setBusy(true);
    try {
      await transport.putConnection(form.id.trim(), {
        kind: form.kind,
        base_url: form.base_url.trim(),
        model: form.model.trim(),
        ...(form.api_key ? { api_key: form.api_key } : {}),
      });
      setForm({ id: "", kind: "openai", base_url: "", model: "", api_key: "" });
      await refresh();
    } catch (e) {
      setErr(errLine(e));
    } finally {
      setBusy(false);
    }
  };

  const del = async (id: string) => {
    setBusy(true);
    try {
      await transport.deleteConnection(id);
      await refresh();
    } catch (e) {
      setErr(errLine(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Pane
      title="连接"
      right={
        <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
          /api/v1/connections
        </span>
      }
    >
      <div style={{ padding: "4px 14px 10px" }}>
        {err && (
          <div className="c-er" style={{ fontSize: 11, padding: "4px 0" }}>
            {err}
          </div>
        )}
        {items == null && !err && <div className="dim" style={{ fontSize: 11 }}>读取中…</div>}
        {items != null && items.length === 0 && (
          <div className="dim" style={{ fontSize: 11, padding: "4px 0" }}>
            尚无连接——模型节点需要一条 OpenAI 兼容绑定。
          </div>
        )}
        {(items ?? []).map((c) => (
          <div key={c.id} className="field" style={{ alignItems: "center" }}>
            <span className="k mono" style={{ fontSize: 11 }}>
              {c.id}
            </span>
            <span className="v" style={{ fontFamily: "var(--sans)", fontSize: 11 }}>
              {c.model} · {c.base_url}
              <span style={{ marginLeft: 6 }}>
                <Tag on={c.has_secret}>
                  <Mk shape="round" tone={c.has_secret ? "c-ok" : "c-er"} />
                  {c.has_secret ? "密钥就绪" : "缺密钥"}
                </Tag>
              </span>
              {c.source === "file" && (
                <span className="mono" style={{ marginLeft: 6, fontSize: 10, color: "var(--ink-3)" }}>
                  file
                </span>
              )}
            </span>
            <span className="grow" />
            <Btn size="sm" disabled={busy} onClick={() => void del(c.id)}>
              删除
            </Btn>
          </div>
        ))}

        <div className="ph" style={{ height: 26, background: "var(--panel-lo)", margin: "8px -14px 0" }}>
          <h2>新建 / 更新</h2>
        </div>
        <label className="in-row">
          <span className="k">id<span className="req">*</span></span>
          <input
            className="in"
            placeholder="如 sensetime"
            value={form.id}
            onChange={(e) => setForm({ ...form, id: e.target.value })}
          />
        </label>
        <label className="in-row">
          <span className="k">base_url<span className="req">*</span></span>
          <input
            className="in"
            placeholder="https://token.sensenova.cn/v1"
            value={form.base_url}
            onChange={(e) => setForm({ ...form, base_url: e.target.value })}
          />
        </label>
        <label className="in-row">
          <span className="k">model<span className="req">*</span></span>
          <input
            className="in"
            placeholder="sensenova-6.8-flash-lite"
            value={form.model}
            onChange={(e) => setForm({ ...form, model: e.target.value })}
          />
        </label>
        <label className="in-row">
          <span className="k">api_key</span>
          <input
            className="in"
            type="password"
            placeholder="留空 = 沿用已存密钥"
            autoComplete="off"
            value={form.api_key}
            onChange={(e) => setForm({ ...form, api_key: e.target.value })}
          />
        </label>
        <div style={{ padding: "6px 0 2px" }}>
          <Btn
            kind="pri"
            disabled={busy || !form.id.trim() || !form.base_url.trim() || !form.model.trim()}
            onClick={() => void submit()}
          >
            {busy ? "写入中…" : "保存连接"}
          </Btn>
          <span className="mono" style={{ marginLeft: 10, fontSize: 10.5, color: "var(--ink-3)" }}>
            key 只落服务端 secrets.json(0600),列表不回读
          </span>
        </div>
      </div>
    </Pane>
  );
}
