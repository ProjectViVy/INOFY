// 登录卡：把 <state>/owner.token 里的一次性所有者令牌换成会话 cookie。
// 令牌只在本组件的一次提交里存在——不写 localStorage，不放进全局状态，
// 换来的会话是 HttpOnly cookie，界面此后不再接触它。

import { useState, type FormEvent } from "react";
import { TransportError } from "../transport";
import { Mk, Tag } from "./Ui";

export interface SessionAuth {
  login(token: string): Promise<void>;
}

interface Props {
  auth: SessionAuth;
  backendErr: string | null;
  onDone(): void;
}

export function Login({ auth, backendErr, onDone }: Props) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const value = token.trim();
    if (value === "" || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await auth.login(value);
      setToken("");
      onDone();
    } catch (e2) {
      if (e2 instanceof TransportError) {
        setErr(
          e2.status === 401 || e2.status === 403
            ? "令牌被拒绝：请核对 <state>/owner.token 的内容（首尾空白不计）。"
            : `${e2.code} · ${e2.message}`,
        );
      } else {
        setErr(e2 instanceof Error ? e2.message : String(e2));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={(e) => void submit(e)}>
        <div className="lc-head">
          <span className="wordmark">INOFY</span>
          <span className="plate-div" />
          <span className="plate-sub">Studio</span>
          <span className="grow" />
          <Tag soft>本机模式</Tag>
        </div>

        <div className="lc-body">
          <label className="in-row" style={{ border: 0, padding: "0 0 10px" }}>
            <span className="k" style={{ flex: "0 0 92px" }}>
              所有者令牌
            </span>
            <input
              className="in"
              style={{ flex: 1 }}
              type="password"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              value={token}
              aria-label="所有者令牌"
              placeholder="owner.token 的内容"
              onChange={(e) => setToken(e.target.value)}
            />
          </label>
          <div className="rowhint" style={{ padding: "0 0 4px" }}>
            <Mk shape="round" tone={backendErr ? "c-er" : "c-nr"} />
            <span>
              {backendErr
                ? "后端未就绪：登录会失败，请先启动 apps/inofy。"
                : "与工作台同源：会话 cookie 为 HttpOnly · SameSite=Strict"}
            </span>
          </div>
          {backendErr && (
            <div className="msg err" role="alert" style={{ marginTop: 8 }}>
              <span className="mk rd" />
              <span>
                <b>后端不可达</b>
              </span>
              <span className="txt">{backendErr}</span>
            </div>
          )}
          {err && (
            <div className="msg err" role="alert" style={{ marginTop: 8 }}>
              <span className="mk rd" />
              <span>
                <b>登录失败</b>
              </span>
              <span className="txt">{err}</span>
            </div>
          )}
        </div>

        <div className="lc-note">
          令牌由 App 首次启动时写入 <span className="mono">&lt;state&gt;/owner.token</span>
          ，仅用于本机回环地址。工作台不发出任何外部请求：目录、事件流与静态资源
          全部来自本机。
        </div>

        <div className="lc-act">
          <button
            type="submit"
            className="btn pri"
            disabled={busy || token.trim() === ""}
          >
            {busy ? "连接中…" : "连接工作台"}
          </button>
          <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
            POST /api/v1/session
          </span>
        </div>
      </form>
    </div>
  );
}
