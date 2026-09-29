// 端到端流程测试：会话门 → 草稿 CAS → 运行受理 → 运行账本。
// 传输层是唯一的假件；界面、路由与状态全部走真实实现。

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App } from "./App";
import { TransportError, type StudioTransport } from "./transport";
import type {
  Artifact,
  NodeDescriptor,
  RunEvent,
  RunSummary,
  WaitRequest,
} from "./schema";

const baseArtifact: Artifact = {
  definition: {
    schema_version: "inofy.workflow/v1",
    graph: {
      nodes: [{ id: "a", kind: "call", type: "inofy.value@1" }],
      edges: [],
      exits: ["a"],
    },
  },
};

const catalog: NodeDescriptor[] = [
  {
    type_id: "inofy.value@1",
    implementation_id: "inofy.value@1",
    replay: "pure",
    config_schema: {
      type: "object",
      properties: { value: { type: "string" } },
    },
  },
  {
    type_id: "inofy.wait@1",
    implementation_id: "inofy.wait@1",
    replay: "non_replayable",
    supports_wait: true,
  },
];

const auth = {
  login: vi.fn(async () => {}),
  logout: vi.fn(async () => {}),
};

function fakeTransport(overrides: Partial<StudioTransport> = {}): StudioTransport {
  return {
    capabilities: vi.fn(async () => ({
      schema_version: "inofy/v1",
      features: ["draft", "publish", "runs", "sse", "resume"],
    })),
    nodeTypes: vi.fn(async () => catalog),
    listWorkflows: vi.fn(async () => ({ items: [], next_cursor: null })),
    loadDraft: vi.fn(async () => ({
      workflow: "wf",
      etag: "e1",
      artifact: baseArtifact,
    })),
    saveDraft: vi.fn(async (id, artifact) => ({
      workflow: id,
      etag: "e2",
      artifact,
    })),
    validate: vi.fn(async () => ({ valid: true })),
    publish: vi.fn(async () => ({
      workflow: "wf",
      revision: 1,
      definition_digest: "digest-1",
    })),
    getRevision: vi.fn(async () => ({
      workflow: "wf",
      revision: 1,
      artifact: baseArtifact,
    })),
    startRun: vi.fn(async () => ({ run_id: "run-1" })),
    listRuns: vi.fn(async () => ({ items: [], next_cursor: null })),
    getRun: vi.fn(async () => ({ run_id: "run-1", status: "running" })),
    nodeOutput: vi.fn(async () => ({ output: { ok: true } })),
    cancelRun: vi.fn(async () => ({ ok: true })),
    resumeRun: vi.fn(async () => ({ run_id: "run-1", status: "running" })),
    events: vi.fn(async () => ({ events: [], next_cursor: null })),
    subscribeEvents: vi.fn(() => ({ close: vi.fn() })),
    ...overrides,
  };
}

const summary = (over: Partial<RunSummary> = {}): RunSummary => ({
  run_id: "run-1",
  status: "running",
  workflow_id: "wf",
  revision: 1,
  writer_epoch: 3,
  created_at: "2026-09-29T08:00:00Z",
  updated_at: "2026-09-29T08:00:02Z",
  ...over,
});

beforeEach(() => {
  window.location.hash = "";
  auth.login.mockClear();
  auth.logout.mockClear();
});

afterEach(() => {
  window.location.hash = "";
});

describe("会话门", () => {
  it("未认证时给出门禁；令牌换会话后才进入工作台，且令牌不落存储", async () => {
    let authed = false;
    const t = fakeTransport({
      capabilities: vi.fn(async () => {
        if (!authed) {
          throw new TransportError(401, {
            code: "unauthorized",
            message: "no session",
          });
        }
        return { schema_version: "inofy/v1", features: ["draft"] };
      }),
    });
    auth.login.mockImplementation(async () => {
      authed = true;
    });
    const persist = vi.spyOn(Storage.prototype, "setItem");

    render(<App transport={t} auth={auth} />);
    const input = await screen.findByLabelText("所有者令牌");
    fireEvent.change(input, { target: { value: "  s3cret  " } });
    fireEvent.click(screen.getByRole("button", { name: "连接工作台" }));

    await waitFor(() => expect(auth.login).toHaveBeenCalledWith("s3cret"));
    expect(await screen.findByRole("link", { name: /工作流/ })).toBeInTheDocument();
    // 令牌只在本组件的一次提交里存在：不落任何存储。
    expect(persist).not.toHaveBeenCalled();
    persist.mockRestore();
  });

  it("被拒的令牌留在门禁内，不进入应用", async () => {
    const t = fakeTransport({
      capabilities: vi.fn(async () => {
        throw new TransportError(401, {
          code: "unauthorized",
          message: "no session",
        });
      }),
    });
    auth.login.mockImplementation(async () => {
      throw new TransportError(403, { code: "forbidden", message: "bad token" });
    });

    render(<App transport={t} auth={auth} />);
    fireEvent.change(await screen.findByLabelText("所有者令牌"), {
      target: { value: "wrong" },
    });
    fireEvent.click(screen.getByRole("button", { name: "连接工作台" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("令牌被拒绝");
    expect(screen.queryByRole("link", { name: /工作流/ })).not.toBeInTheDocument();
  });
});

describe("草稿生命周期", () => {
  it("保存走 etag CAS；发布回执修订号；运行以草稿 etag 快照受理", async () => {
    window.location.hash = "#/workflows/wf";
    const t = fakeTransport();
    render(<App transport={t} auth={auth} />);

    await waitFor(() => expect(t.loadDraft).toHaveBeenCalledWith("wf"));
    // 草稿已落库且无本地改动：发布与运行直接作用在这份快照上，保存不可用。
    expect(await screen.findByRole("button", { name: "保存" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "运行" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "校验" })).toBeEnabled();

    // 从节点库追加一个节点 → 变脏 → 发布与运行锁定在已保存草稿上。
    fireEvent.click(screen.getByRole("button", { name: /inofy\.wait@1/ }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "保存" })).toBeEnabled(),
    );
    expect(screen.getByRole("button", { name: "运行" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(t.saveDraft).toHaveBeenCalled());
    const save = vi.mocked(t.saveDraft).mock.calls.at(-1)!;
    expect(save[0]).toBe("wf");
    expect(save[2]).toBe("e1");
    expect((save[1] as Artifact).definition.graph.nodes).toHaveLength(2);
    expect(await screen.findByText(/草稿已保存/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "发布" }));
    await waitFor(() => expect(t.publish).toHaveBeenCalledWith("wf", "e2"));
    expect(await screen.findByText(/已发布修订 r1/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "运行" }));
    await waitFor(() =>
      expect(t.startRun).toHaveBeenCalledWith({
        workflow: "wf",
        draft_etag: "e2",
        input: {},
      }),
    );
    expect(window.location.hash).toBe("#/runs/run-1");
  });

  it("冲突暴露两条出路，覆盖路径先取服务端 etag 再写回本地内容", async () => {
    window.location.hash = "#/workflows/wf";
    let loads = 0;
    const saveDraft = vi
      .fn<StudioTransport["saveDraft"]>()
      .mockRejectedValueOnce(
        new TransportError(412, {
          code: "revision_conflict",
          message: "etag stale",
        }),
      )
      .mockImplementation(async (id, artifact) => ({
        workflow: id,
        etag: "e9",
        artifact,
      }));
    const t = fakeTransport({
      saveDraft,
      loadDraft: vi.fn(async () => {
        loads += 1;
        return {
          workflow: "wf",
          etag: loads === 1 ? "e1" : "e5",
          artifact: baseArtifact,
        };
      }),
    });
    render(<App transport={t} auth={auth} />);

    fireEvent.click(await screen.findByRole("button", { name: /inofy\.wait@1/ }));
    fireEvent.click(screen.getByRole("button", { name: "保存" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("修订冲突");
    expect(screen.getByText("保留本地并覆盖")).toBeInTheDocument();
    expect(screen.getByText("放弃本地，采用服务端")).toBeInTheDocument();

    fireEvent.click(screen.getByText("保留本地并覆盖"));
    await waitFor(() => expect(loads).toBe(2));
    await waitFor(() => expect(saveDraft).toHaveBeenCalledTimes(2));
    expect(vi.mocked(saveDraft).mock.calls.at(-1)![2]).toBe("e5");
    expect(await screen.findByText(/已用本地内容覆盖服务端草稿/)).toBeInTheDocument();
  });
});

describe("运行中心", () => {
  const ledger: RunEvent[] = [
    { seq: 1, kind: "run_admitted", data: { limits: { max_nodes: 64 } } },
    { seq: 2, kind: "node_started", path: "/graph/nodes/a" },
    { seq: 3, kind: "node_completed", path: "/graph/nodes/a" },
    { seq: 4, kind: "run_succeeded" },
  ];

  it("终态运行已定格：不再订阅事件流", async () => {
    window.location.hash = "#/runs/run-1";
    const t = fakeTransport({
      listRuns: vi.fn(async () => ({
        items: [summary({ status: "succeeded", updated_at: "2026-09-29T08:00:09Z" })],
        next_cursor: null,
      })),
      getRun: vi.fn(async () => ({ run_id: "run-1", status: "succeeded" })),
      events: vi.fn(async () => ({ events: ledger, next_cursor: "4" })),
    });
    const { container } = render(<App transport={t} auth={auth} />);

    await waitFor(() => expect(container.querySelectorAll(".ev")).toHaveLength(4));
    expect(t.subscribeEvents).not.toHaveBeenCalled();
    expect(container.querySelector(".live")).toHaveTextContent("已定格");
    // run_admitted 自带的上限快照按中文标签铺开。
    expect(container.querySelector(".limits")).toHaveTextContent("节点上限");

    // 光标回看：只画到光标为止，之后的事件压暗。
    const rows = container.querySelectorAll<HTMLElement>(".ev");
    fireEvent.click(rows[2]!);
    await waitFor(() =>
      expect(container.querySelector(".tail")).toHaveTextContent("光标 seq 3"),
    );
    expect(container.querySelector(".tail")).toHaveTextContent("定格 3 / 4 条");
    // 光标之后的条目留在原位只压暗，回看不跳版。
    expect(container.querySelectorAll(".ev")).toHaveLength(4);
    expect(container.querySelectorAll(".ev.dim")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "跳到最新" }));
    await waitFor(() =>
      expect(container.querySelector(".tail")).toBeNull(),
    );
  });

  it("首屏快照落后于账本：账本已闭账就不再订阅，也不空挂连接", async () => {
    window.location.hash = "#/runs/run-1";
    // 运行可能在详情快照之后、订阅之前就结束——快照仍说 running，
    // 账本末条已是 run_succeeded。以账本为准。
    const t = fakeTransport({
      listRuns: vi.fn(async () => ({
        items: [summary({ status: "running", updated_at: "2026-09-29T08:00:09Z" })],
        next_cursor: null,
      })),
      getRun: vi.fn(async () => ({ run_id: "run-1", status: "running" })),
      events: vi.fn(async () => ({ events: ledger, next_cursor: "4" })),
    });
    const { container } = render(<App transport={t} auth={auth} />);

    await waitFor(() => expect(container.querySelectorAll(".ev")).toHaveLength(4));
    await waitFor(() =>
      expect(container.querySelector(".live")).toHaveTextContent("已定格"),
    );
    expect(t.subscribeEvents).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "跟随" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "取消运行" })).toBeDisabled();
    expect(container.querySelector(".col .tag")).toHaveTextContent("成功");
  });

  it("跟随中收到终态事件：立刻解订阅并定格", async () => {
    window.location.hash = "#/runs/run-1";
    let emit: ((e: RunEvent) => void) | null = null;
    const close = vi.fn();
    const t = fakeTransport({
      listRuns: vi.fn(async () => ({
        items: [summary({ status: "running" })],
        next_cursor: null,
      })),
      getRun: vi.fn(async () => ({ run_id: "run-1", status: "running" })),
      events: vi.fn(async () => ({ events: [ledger[0]!], next_cursor: "1" })),
      subscribeEvents: vi.fn((_id, _after, onEvent) => {
        emit = onEvent;
        return { close };
      }),
    });
    const { container } = render(<App transport={t} auth={auth} />);

    await waitFor(() => expect(t.subscribeEvents).toHaveBeenCalled());
    expect(container.querySelector(".live")).toHaveTextContent("跟随中");

    act(() => {
      emit?.({ seq: 2, kind: "run_succeeded" });
    });
    await waitFor(() =>
      expect(container.querySelector(".live")).toHaveTextContent("已定格"),
    );
    expect(close).toHaveBeenCalled();
  });

  it("跟随中转入等待：重新取详情，等待项随 run_waiting 出现", async () => {
    window.location.hash = "#/runs/run-1";
    let emit: ((e: RunEvent) => void) | null = null;
    let calls = 0;
    const t = fakeTransport({
      listRuns: vi.fn(async () => ({
        items: [summary({ status: "waiting" })],
        next_cursor: null,
      })),
      getRun: vi.fn(async () => {
        calls += 1;
        return calls === 1
          ? { run_id: "run-1", status: "running" }
          : {
              run_id: "run-1",
              status: "waiting",
              waits: [{ request_id: "approve", kind: "approval", prompt: "确认发布？" }],
            };
      }),
      events: vi.fn(async () => ({ events: [ledger[0]!], next_cursor: "1" })),
      subscribeEvents: vi.fn((_id, _after, onEvent) => {
        emit = onEvent;
        return { close: vi.fn() };
      }),
    });
    const { container } = render(<App transport={t} auth={auth} />);

    await waitFor(() => expect(t.subscribeEvents).toHaveBeenCalled());
    expect(container.querySelector(".waitcard")).toBeNull();

    act(() => {
      emit?.({ seq: 2, kind: "node_wait", path: "/graph/nodes/wait" });
      emit?.({ seq: 3, kind: "run_waiting" });
    });
    expect(await screen.findByText(/确认发布/)).toBeInTheDocument();
    expect(screen.getByLabelText("应答 approve")).toBeInTheDocument();
  });

  it("节点输出按路径单独取，不来自事件", async () => {
    window.location.hash = "#/runs/run-1";
    const t = fakeTransport({
      listRuns: vi.fn(async () => ({
        items: [summary({ status: "succeeded" })],
        next_cursor: null,
      })),
      getRun: vi.fn(async () => ({ run_id: "run-1", status: "succeeded" })),
      events: vi.fn(async () => ({ events: ledger, next_cursor: "4" })),
      nodeOutput: vi.fn(async () => ({ output: { message: "done" } })),
    });
    const { container } = render(<App transport={t} auth={auth} />);

    await waitFor(() =>
      expect(container.querySelector(".drow")?.textContent).toContain("/graph/nodes/a"),
    );
    fireEvent.click(screen.getByRole("button", { name: "输出" }));
    await waitFor(() =>
      expect(t.nodeOutput).toHaveBeenCalledWith("run-1", "/graph/nodes/a"),
    );
    expect(await screen.findByText(/"message": "done"/)).toBeInTheDocument();
  });

  it("等待运行：全部等待项一次作答，缺一不可", async () => {
    window.location.hash = "#/runs/run-1";
    const waits: WaitRequest[] = [
      { request_id: "w1", kind: "approval", prompt: "批准合并?" },
      { request_id: "w2", kind: "manual", prompt: "补充备注" },
    ];
    const suspendLedger: RunEvent[] = [
      { seq: 1, kind: "run_admitted" },
      { seq: 2, kind: "node_wait", path: "/graph/nodes/wait" },
      { seq: 3, kind: "run_waiting" },
    ];
    const t = fakeTransport({
      listRuns: vi.fn(async () => ({
        items: [summary({ status: "waiting" })],
        next_cursor: null,
      })),
      getRun: vi.fn(async () => ({
        run_id: "run-1",
        status: "waiting",
        waits,
      })),
      events: vi.fn(async () => ({ events: suspendLedger, next_cursor: "3" })),
      resumeRun: vi.fn(async () => ({ run_id: "run-1", status: "running" })),
    });
    render(<App transport={t} auth={auth} />);

    expect(await screen.findByText(/批准合并/)).toBeInTheDocument();
    const w1 = screen.getByLabelText("应答 w1") as HTMLTextAreaElement;
    const w2 = screen.getByLabelText("应答 w2") as HTMLTextAreaElement;
    // 未作答的项不算错，但「继续」要等全部项都给出合法 JSON。
    expect(screen.getByRole("button", { name: "继续" })).toBeDisabled();
    expect(screen.getAllByText(/未作答/)).toHaveLength(2);
    fireEvent.change(w1, { target: { value: '{"approved":true}' } });
    expect(screen.getByRole("button", { name: "继续" })).toBeDisabled();
    fireEvent.change(w2, { target: { value: '{"note":"ok"}' } });
    fireEvent.click(screen.getByRole("button", { name: "继续" }));

    await waitFor(() =>
      expect(t.resumeRun).toHaveBeenCalledWith("run-1", {
        w1: { approved: true },
        w2: { note: "ok" },
      }),
    );
  });

  it("静默取消：账本不带终态事件，靠聚合状态定格，等待项转只读", async () => {
    window.location.hash = "#/runs/run-1";
    let cancelled = false;
    const suspendLedger: RunEvent[] = [
      { seq: 1, kind: "run_admitted" },
      { seq: 2, kind: "run_waiting" },
    ];
    const t = fakeTransport({
      listRuns: vi.fn(async () => ({
        items: [summary({ status: "waiting" })],
        next_cursor: null,
      })),
      getRun: vi.fn(async () => ({
        run_id: "run-1",
        status: cancelled ? "cancelled" : "waiting",
        waits: [{ request_id: "approve", kind: "approval", prompt: "确认发布？" }],
      })),
      events: vi.fn(async () => ({ events: suspendLedger, next_cursor: "2" })),
      cancelRun: vi.fn(async () => {
        cancelled = true;
        return { ok: true };
      }),
    });
    const { container } = render(<App transport={t} auth={auth} />);

    expect(await screen.findByText(/确认发布/)).toBeInTheDocument();
    expect(container.querySelector(".live")).toHaveTextContent("跟随中");
    fireEvent.click(screen.getByRole("button", { name: "取消运行" }));

    await waitFor(() =>
      expect(container.querySelector(".live")).toHaveTextContent("已定格"),
    );
    // 等待中的运行被取消只改聚合状态，账本停在被取消之前：界面不伪造收尾行。
    expect(container.querySelectorAll(".ev")).toHaveLength(2);
    expect(container.querySelector(".col .tag")).toHaveTextContent("已取消");
    expect(
      (screen.getByLabelText("应答 approve") as HTMLTextAreaElement).readOnly,
    ).toBe(true);
    expect(screen.getByRole("button", { name: "继续" })).toBeDisabled();
    expect(screen.getByText(/运行已终态 · 等待项只读/)).toBeInTheDocument();
  });
});
