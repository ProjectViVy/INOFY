// 应用根：会话门 → 外壳 → 页面。数据在根上持有一份（工作流列表、
// 运行列表、目录），页面只消费与请求刷新；路由是纯哈希，地址即状态，
// 刷新与深链都由浏览器兜底。

import { useCallback, useEffect, useMemo, useState } from "react";
import type {
  NodeDescriptor,
  RunSummary,
  WorkflowSummary,
} from "./schema";
import { TransportError, type StudioTransport } from "./transport";
import { Shell, type ConnState } from "./components/Shell";
import { Login, type SessionAuth } from "./components/Login";
import { Loading } from "./components/Ui";
import { WorkflowsPage } from "./pages/WorkflowsPage";
import { EditorPage } from "./pages/EditorPage";
import { RunsPage } from "./pages/RunsPage";
import { SettingsPage } from "./pages/SettingsPage";
import { useRoute } from "./router";

export interface SessionAuthFull extends SessionAuth {
  logout(): Promise<void>;
}

interface Props {
  transport: StudioTransport;
  auth?: SessionAuthFull;
}

type Session = "checking" | "in" | "out";

function errLine(e: unknown): string {
  if (e instanceof TransportError) return `${e.code} · ${e.message}`;
  return e instanceof Error ? e.message : String(e);
}

function unauthenticated(e: unknown): boolean {
  return e instanceof TransportError && (e.status === 401 || e.status === 403);
}

export function App({ transport, auth }: Props) {
  const route = useRoute();
  const [session, setSession] = useState<Session>("checking");
  const [caps, setCaps] = useState<Record<string, unknown> | null>(null);
  const [backendErr, setBackendErr] = useState<string | null>(null);
  const [nodeTypes, setNodeTypes] = useState<NodeDescriptor[] | null>(null);
  const [workflows, setWorkflows] = useState<WorkflowSummary[] | null>(null);
  const [workflowsErr, setWorkflowsErr] = useState<string | null>(null);
  const [runs, setRuns] = useState<RunSummary[] | null>(null);
  const [runsErr, setRunsErr] = useState<string | null>(null);
  const [loggingOut, setLoggingOut] = useState(false);

  const origin = useMemo(() => {
    const o = window.location.origin;
    return o === "null" || o === "" ? "本机" : o;
  }, []);

  const schemaVersion =
    typeof caps?.schema_version === "string" ? caps.schema_version : undefined;
  const features = useMemo(() => {
    const f = caps?.features;
    return Array.isArray(f) ? f.filter((x): x is string => typeof x === "string") : [];
  }, [caps]);

  const conn: ConnState = {
    ok: session === "in",
    origin,
    ...(schemaVersion ? { schemaVersion } : {}),
  };

  const refreshWorkflows = useCallback(
    async (silent = false) => {
      try {
        const page = await transport.listWorkflows();
        setWorkflows(page.items);
        setWorkflowsErr(null);
      } catch (e) {
        if (unauthenticated(e)) {
          setSession("out");
          return;
        }
        if (!silent) setWorkflowsErr(errLine(e));
      }
    },
    [transport],
  );

  const refreshRuns = useCallback(
    async (silent = false) => {
      try {
        const page = await transport.listRuns();
        setRuns(page.items);
        setRunsErr(null);
      } catch (e) {
        if (unauthenticated(e)) {
          setSession("out");
          return;
        }
        if (!silent) setRunsErr(errLine(e));
      }
    },
    [transport],
  );

  const refreshCatalog = useCallback(async () => {
    try {
      const types = await transport.nodeTypes();
      setNodeTypes(types);
    } catch (e) {
      if (!unauthenticated(e)) setWorkflowsErr(errLine(e));
      else setSession("out");
    }
  }, [transport]);

  const bootstrap = useCallback(async () => {
    setSession("checking");
    setBackendErr(null);
    try {
      const c = await transport.capabilities();
      setCaps(c);
      setSession("in");
    } catch (e) {
      setCaps(null);
      // 未认证只是「还没登录」，不是后端故障。
      setBackendErr(unauthenticated(e) ? null : errLine(e));
      setSession("out");
    }
  }, [transport]);

  useEffect(() => {
    void bootstrap();
  }, [bootstrap]);

  useEffect(() => {
    if (session !== "in") return;
    void refreshCatalog();
    void refreshWorkflows();
    void refreshRuns();
    const id = window.setInterval(() => void refreshRuns(true), 10_000);
    return () => window.clearInterval(id);
  }, [session, refreshCatalog, refreshWorkflows, refreshRuns]);

  const logout = async () => {
    if (!auth) return;
    setLoggingOut(true);
    try {
      await auth.logout();
    } catch {
      // 会话可能在服务端已失效：本地一样回到登录门。
    } finally {
      setLoggingOut(false);
      setCaps(null);
      setWorkflows(null);
      setRuns(null);
      setNodeTypes(null);
      setSession("out");
    }
  };

  if (session === "checking") {
    return (
      <div className="login-wrap">
        <Loading>连接本机后端…</Loading>
      </div>
    );
  }

  if (session === "out") {
    if (!auth) {
      return (
        <div className="login-wrap">
          <div className="login-card">
            <div className="lc-head">
              <span className="wordmark">INOFY</span>
              <span className="plate-div" />
              <span className="plate-sub">Studio</span>
            </div>
            <div className="lc-body" role="alert">
              会话不可用：{backendErr ?? "宿主未提供登录方式"}。
            </div>
          </div>
        </div>
      );
    }
    return <Login auth={auth} backendErr={backendErr} onDone={() => void bootstrap()} />;
  }

  const page = (() => {
    switch (route.name) {
      case "editor":
        return (
          <EditorPage
            transport={transport}
            workflowId={route.id}
            catalog={nodeTypes}
            publishedRevision={workflows?.find((w) => w.workflow_id === route.id)?.revision}
            onWorkflowsChanged={() => void refreshWorkflows(true)}
          />
        );
      case "runs":
        return (
          <RunsPage
            transport={transport}
            runId={route.id}
            runs={runs}
            listErr={runsErr}
            onRefreshRuns={refreshRuns}
          />
        );
      case "settings":
        return (
          <SettingsPage
            conn={conn}
            nodeTypes={nodeTypes}
            features={features}
            caps={caps}
            transport={transport}
            onLogout={() => void logout()}
            busy={loggingOut}
          />
        );
      default:
        return (
          <WorkflowsPage
            transport={transport}
            workflows={workflows}
            nodeTypes={nodeTypes}
            conn={conn}
            features={features}
            error={workflowsErr}
            onRefresh={() => {
              void refreshWorkflows();
              void refreshCatalog();
            }}
          />
        );
    }
  })();

  return (
    <Shell
      route={route}
      conn={conn}
      workflowCount={workflows?.length ?? null}
      runCount={runs?.length ?? null}
    >
      {page}
    </Shell>
  );
}
