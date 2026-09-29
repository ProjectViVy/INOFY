// 设置页：会话、接口、目录与工作台约定的只读事实面。这里不提供
// 任何「配置项」——引擎的契约来自后端，界面不发明开关。

import type { NodeDescriptor } from "../schema";
import { Btn, Facts, FactRow, Mk, Tag, Ticks } from "../components/Ui";
import { CatalogPane, EnvPane } from "../components/Panes";
import type { ConnState } from "../components/Shell";

interface Props {
  conn: ConnState;
  nodeTypes: NodeDescriptor[] | null;
  features: string[];
  caps: Record<string, unknown> | null;
  onLogout(): void;
  busy: boolean;
}

const CONVENTIONS: [string, string][] = [
  ["草稿", "每工作流一份，etag CAS 覆盖；首发 If-None-Match: * ，其后 If-Match"],
  ["修订", "发布时分配，单调递增，不可变；运行可精确指向某个修订"],
  ["运行", "受理即固定 artifact 字节；之后的草稿改动无法影响已受理的运行"],
  ["事件", "已提交事件按 seq 只追加；订阅从 Last-Event-ID 续传，不丢不乱"],
  ["等待", "挂起是持久状态；续答必须一次答完所有等待项，并由 answer_schema 校验"],
  ["诊断", "编译期发现无严重等级之分：每一条都拒绝该定义"],
];

export function SettingsPage({ conn, nodeTypes, features, caps, onLogout, busy }: Props) {
  return (
    <div className="settings">
      <div className="grid-a">
        <div className="colL">
          <section className="pane">
            <div className="ph">
              <h2>会话</h2>
              <span className="grow" />
              <Tag on={conn.ok}>
                <Mk shape="round" tone={conn.ok ? "c-ok" : "c-er"} />
                {conn.ok ? "已连接" : "未连接"}
              </Tag>
            </div>
            <div className="set-card">
              <div className="sc-h">
                <Mk shape="round" tone="c-ok" />
                当前会话
              </div>
              <div className="sc-b">
                所有者令牌已在会话建立时交换为 <span className="mono">HttpOnly</span> cookie；
                工作台不保存令牌本身。退出会话会在服务端吊销该会话。
              </div>
              <div className="lc-act" style={{ padding: "0 12px 12px" }}>
                <Btn onClick={onLogout} disabled={busy} title="吊销服务端会话并返回登录">
                  {busy ? "退出中…" : "退出会话"}
                </Btn>
                <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
                  DELETE /api/v1/session
                </span>
              </div>
            </div>
          </section>

          <section className="pane">
            <div className="ph">
              <h2>工作台约定</h2>
              <span className="grow" />
              <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
                S02 · S09 §11.4
              </span>
            </div>
            <div className="kvlist">
              {CONVENTIONS.map(([k, v]) => (
                <div key={k}>
                  <span className="k">{k}</span>
                  <span className="v" style={{ fontFamily: "var(--sans)", fontSize: 11.5 }}>
                    {v}
                  </span>
                </div>
              ))}
            </div>
          </section>

          <section className="pane">
            <div className="ph">
              <h2>契约能力</h2>
              <span className="grow" />
              <span className="mono" style={{ fontSize: 10.5, color: "var(--ink-3)" }}>
                GET /api/v1/capabilities
              </span>
            </div>
            <div className="set-card">
              <div className="kvlist">
                <div>
                  <span className="k">schema_version</span>
                  <span className="v">{conn.schemaVersion ?? "—"}</span>
                </div>
                {caps &&
                  Object.entries(caps)
                    .filter(([k]) => k !== "features" && k !== "schema_version")
                    .map(([k, v]) => (
                      <div key={k}>
                        <span className="k">{k}</span>
                        <span className="v">{JSON.stringify(v as unknown)}</span>
                      </div>
                    ))}
              </div>
            </div>
          </section>
        </div>

        <div className="colR">
          <EnvPane conn={conn} features={features} />
          <CatalogPane nodeTypes={nodeTypes} title="受治理类型" />
          <section className="pane">
            <div className="ph">
              <h2>关于</h2>
              <span className="grow" />
              <Ticks width={72} />
            </div>
            <Facts>
              <FactRow k="工作台" v="@inofy/studio" />
              <FactRow k="引擎" v="基于 Eino 的嵌入式工作流引擎" />
              <FactRow k="界面" v="仪器面板 · 全中文" />
              <FactRow
                k="外部依赖"
                v={
                  <span className="c-ok" style={{ fontFamily: "var(--sans)" }}>
                    零（仅回环地址）
                  </span>
                }
              />
            </Facts>
            <div className="pnote">
              本页显示的全部数值都来自后端接口与浏览器自身，没有任何演示数据。
              目录随宿主注册而变；上限取自每次运行的受理快照。
            </div>
          </section>
        </div>
      </div>
    </div>
  );
}
