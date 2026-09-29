// 应用外壳：铭牌 + 导航 + 舞台 + 图例页脚。外壳只表达状态，
// 不拥有数据；页脚图例是全站功能色编码的唯一出处。

import type { ReactNode } from "react";
import { Ticks, Mk } from "./Ui";
import type { Route } from "../router";
import { routeHref } from "../router";

export interface ConnState {
  ok: boolean;
  origin: string;
  schemaVersion?: string;
}

interface Props {
  route: Route;
  conn: ConnState;
  workflowCount: number | null;
  runCount: number | null;
  children: ReactNode;
}

export function Shell({ route, conn, workflowCount, runCount, children }: Props) {
  const tab = (r: Route, label: string, count: number | null) => {
    const on =
      route.name === r.name ||
      (r.name === "workflows" && route.name === "editor");
    return (
      <a
        className={`tab${on ? " on" : ""}`}
        href={routeHref(r)}
        aria-current={on ? "page" : undefined}
      >
        {label}
        {count != null && <span className="cnt">{count}</span>}
      </a>
    );
  };

  return (
    <div className="studio-app">
      <header className="appbar">
        <div className="nameplate">
          <span className="wordmark">INOFY</span>
          <span className="plate-div" />
          <span className="plate-sub">Studio</span>
        </div>
        <span className="tagline">基于 Eino 的嵌入式工作流引擎 · 可视化工作台</span>
        <span className="grow" />
        <div className="conn" title={conn.ok ? "后端可达，会话有效" : "后端不可达或会话已失效"}>
          <Mk shape="round" tone={conn.ok ? "c-ok" : "c-er"} />
          <span>{conn.ok ? "已连接" : "未连接"}</span>
          <span className="mono" style={{ fontSize: 11, color: "var(--ink)" }}>
            {conn.origin}
          </span>
        </div>
        <a className="btn" href={routeHref({ name: "settings" })}>
          设置
        </a>
      </header>

      <nav className="navbar">
        {tab({ name: "workflows" }, "工作流", workflowCount)}
        {tab({ name: "runs" }, "运行", runCount)}
        <span className="grow" />
        <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-4)" }}>
          {conn.schemaVersion ?? ""}
        </span>
      </nav>

      <main className="stage">{children}</main>

      <footer className="footbar">
        <Ticks width={92} />
        <span className="legend">
          <span>
            <Mk shape="round" tone="c-ok" />
            可重放 / 已提交
          </span>
          <span>
            <Mk tone="c-nr" />
            不可重放 / 需处置
          </span>
          <span>
            <Mk shape="pause" tone="c-wa" />
            支持等待
          </span>
          <span>
            <Mk tone="c-er" />
            失败 / 冲突
          </span>
        </span>
        <span className="grow" />
        <span>本机工作台 · 仅回环地址 · 零外部依赖</span>
      </footer>
    </div>
  );
}
