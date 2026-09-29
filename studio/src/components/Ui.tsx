// 仪器面板基础构件。全部为纯展示件，样式来自 styles.css 的令牌，
// 不在 JSX 里写颜色字面量。

import type { ButtonHTMLAttributes, ReactNode } from "react";

/** 状态标记：形状 + 颜色双编码。round=可重放 pause=支持等待 square=不可重放 */
export function Mk({
  shape = "square",
  tone,
  title,
}: {
  shape?: "round" | "square" | "pause" | "hollow";
  tone?: string;
  title?: string;
}) {
  return (
    <span
      className={`mk${shape === "round" ? " rd" : shape === "pause" ? " ps" : shape === "hollow" ? " hl" : ""}${tone ? " " + tone : ""}`}
      title={title}
      aria-hidden={title ? undefined : true}
    />
  );
}

export function Tag({
  children,
  on,
  soft,
  tone,
  title,
}: {
  children: ReactNode;
  on?: boolean;
  soft?: boolean;
  tone?: string;
  title?: string;
}) {
  return (
    <span
      className={`tag${on ? " on" : ""}${soft ? " soft" : ""}${tone ? " " + tone : ""}`}
      title={title}
    >
      {children}
    </span>
  );
}

type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  kind?: "plain" | "pri" | "deep";
  size?: "md" | "sm";
};

export function Btn({ kind = "plain", size = "md", className, ...rest }: BtnProps) {
  const cls = [
    "btn",
    kind === "pri" ? "pri" : kind === "deep" ? "deep" : "",
    size === "sm" ? "sm" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return <button type="button" className={cls} {...rest} />;
}

export function Tbtn({ className, ...rest }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button type="button" className={`tbtn ${className ?? ""}`} {...rest} />;
}

export function Hair({ style }: { style?: React.CSSProperties }) {
  return <div className="hair" style={style} />;
}

export function Ticks({ width, vertical }: { width?: number; vertical?: boolean }) {
  return vertical ? (
    <span className="ticks v" />
  ) : (
    <span className="ticks" style={width != null ? { width } : undefined} />
  );
}

/** 分区标题条：左标题 + 右侧注记 */
export function Ph({
  title,
  note,
  right,
  dark,
  children,
}: {
  title: ReactNode;
  note?: ReactNode;
  right?: ReactNode;
  dark?: boolean;
  children?: ReactNode;
}) {
  return (
    <div className={`ph${dark ? " on-dark" : ""}`}>
      <h2>{title}</h2>
      {note != null && <span className="note">{note}</span>}
      <span className="grow" />
      {right}
      {children}
    </div>
  );
}

export function Pane({
  title,
  note,
  right,
  children,
  flush,
}: {
  title: ReactNode;
  note?: ReactNode;
  right?: ReactNode;
  children: ReactNode;
  flush?: boolean;
}) {
  return (
    <section className={`pane${flush ? " flush" : ""}`}>
      <Ph title={title} note={note} right={right} />
      {children}
    </section>
  );
}

/** 事实行：键右对齐的值 */
export function Field({
  k,
  v,
  plain,
  mono,
}: {
  k: ReactNode;
  v: ReactNode;
  plain?: boolean;
  mono?: boolean;
}) {
  return (
    <div className="field">
      <span className="k">{k}</span>
      <span className={`v${plain ? " plain" : ""}`}>{v}</span>
    </div>
  );
}

export function Facts({
  variant,
  children,
  className,
}: {
  variant?: "two" | "three" | "nr" | "tight";
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={`facts${variant ? " " + variant : ""}${className ? " " + className : ""}`}>
      {children}
    </div>
  );
}

export function FactRow({ k, v }: { k: ReactNode; v: ReactNode }) {
  return (
    <div>
      <span className="k">{k}</span>
      <span className="v">{v}</span>
    </div>
  );
}

export function Chip({
  children,
  state,
  onClick,
  title,
}: {
  children: ReactNode;
  state?: "on" | "bad" | "warn";
  onClick?: () => void;
  title?: string;
}) {
  if (onClick) {
    return (
      <button type="button" className={`chip act${state ? " " + state : ""}`} onClick={onClick} title={title}>
        {children}
      </button>
    );
  }
  return (
    <span className={`chip${state ? " " + state : ""}`} title={title}>
      {children}
    </span>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function Loading({ children }: { children?: ReactNode }) {
  return <div className="loading">{children ?? "载入中…"}</div>;
}

/** 不可重放 / 支持等待等能力标记（来自目录 descriptor） */
export function CapTag({ capability }: { capability: string }) {
  switch (capability) {
    case "replay":
      return (
        <Tag title="可重放：纯函数，重放不产生新副作用">
          <Mk shape="round" tone="c-ok" />可重放
        </Tag>
      );
    case "non_replay":
      return (
        <Tag title="不可重放：调用外部世界，重放需处置">
          <Mk tone="c-nr" />不可重放
        </Tag>
      );
    case "wait":
      return (
        <Tag title="支持等待：可挂起等待人工应答">
          <Mk shape="pause" tone="c-wa" />支持等待
        </Tag>
      );
    default:
      return <Tag>{capability}</Tag>;
  }
}
