// 属性面板侧的纯定义编辑：画布之外的每一处改动都走这里，
// 返回新的 artifact，绝不原地改写。画布自身的拖拽/连线仍由
// graph.ts 的 fromCanvas 负责。

import type { Artifact, Graph, Node } from "./schema";

// Eino 保留节点名：用它们作图节点 id 会在编译期失败。
const RESERVED_IDS = new Set(["start", "end"]);

export function patchNode(a: Artifact, node: Node): Artifact {
  return {
    ...a,
    definition: {
      ...a.definition,
      graph: {
        ...a.definition.graph,
        nodes: a.definition.graph.nodes.map((n) => (n.id === node.id ? node : n)),
      },
    },
  };
}

export function removeNode(a: Artifact, id: string): Artifact {
  return {
    ...a,
    definition: {
      ...a.definition,
      graph: {
        ...a.definition.graph,
        nodes: a.definition.graph.nodes.filter((n) => n.id !== id),
        edges: a.definition.graph.edges.filter((e) => e.from !== id && e.to !== id),
        exits: a.definition.graph.exits.filter((x) => x !== id),
        ...(a.definition.graph.outputs
          ? {
              outputs: Object.fromEntries(
                Object.entries(a.definition.graph.outputs).filter(
                  ([k, b]) => k !== id && b?.source !== id,
                ),
              ),
            }
          : {}),
      },
    },
    presentation: a.presentation?.layout?.positions
      ? {
          ...a.presentation,
          layout: {
            ...a.presentation.layout,
            positions: Object.fromEntries(
              Object.entries(a.presentation.layout.positions).filter(
                ([k]) => k !== id,
              ),
            ),
          },
        }
      : a.presentation,
  };
}

// 输出绑定指向节点的判定与 removeNode 一致：键名等于节点 id，
// 或 binding.source 指向该节点 —— 两者都算这个节点的具名输出。
function withoutNodeOutput(outputs: Graph["outputs"], id: string) {
  if (!outputs) return undefined;
  const kept = Object.fromEntries(
    Object.entries(outputs).filter(([k, b]) => k !== id && b?.source !== id),
  );
  return kept;
}

export function setExit(a: Artifact, id: string, on: boolean): Artifact {
  const exits = a.definition.graph.exits.filter((x) => x !== id);
  const previous = a.definition.graph.outputs;
  // 已有具名输出保留原名；新声明的出口默认以节点 id 暴露具名输出。
  const bound = exitBound(previous, id);
  const outputs = on && bound
    ? { ...previous }
    : { ...withoutNodeOutput(previous, id) };
  if (on && !bound) outputs[id] = { source: id };
  return {
    ...a,
    definition: {
      ...a.definition,
      graph: {
        ...a.definition.graph,
        exits: on ? [...exits, id] : exits,
        outputs,
      },
    },
  };
}

// 节点是否已有输出绑定（source 指向或同名键）。
function exitBound(outputs: Graph["outputs"], id: string) {
  if (!outputs) return false;
  return Object.entries(outputs).some(([k, b]) => k === id || b?.source === id);
}

// 出口节点的具名输出改名/清除：输出绑定名是 graph.outputs 的键，
// 改名即换键；清空名字即该出口不暴露具名输出（上游 schema 合法，
// 是否要求输出由宿主校验裁决）。
export function setExitOutputName(a: Artifact, id: string, name: string): Artifact {
  const outputs = { ...withoutNodeOutput(a.definition.graph.outputs, id) };
  const trimmed = name.trim();
  if (trimmed !== "") outputs[trimmed] = { source: id };
  return {
    ...a,
    definition: {
      ...a.definition,
      graph: {
        ...a.definition.graph,
        outputs,
      },
    },
  };
}

// 节点当前绑定的输出名：优先 source 指向，其次同名键。
export function exitOutputName(a: Artifact, id: string): string {
  const outputs = a.definition.graph.outputs ?? {};
  for (const [k, b] of Object.entries(outputs)) {
    if (b?.source === id) return k;
  }
  return Object.hasOwn(outputs, id) ? id : "";
}

export function nodeIDBase(typeID: string): string {
  const stripped = typeID.replace(/^inofy\./, "").replace(/@[^@]*$/, "");
  const slug = stripped.replace(/[^A-Za-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
  const base = slug === "" ? "node" : slug;
  return RESERVED_IDS.has(base) ? `${base}-node` : base;
}

export function nextNodeID(a: Artifact, typeID: string): string {
  const base = nodeIDBase(typeID);
  const taken = new Set(a.definition.graph.nodes.map((n) => n.id));
  if (!taken.has(base)) return base;
  for (let i = 2; ; i++) {
    const candidate = `${base}${i}`;
    if (!taken.has(candidate)) return candidate;
  }
}

export function newNode(typeID: string, id: string): Node {
  return { id, kind: "call", type: typeID, config: {} };
}

export function addNodeAt(
  a: Artifact,
  typeID: string,
  pos: { x: number; y: number },
): { artifact: Artifact; id: string } {
  const id = nextNodeID(a, typeID);
  const node = newNode(typeID, id);
  return {
    id,
    artifact: {
      ...a,
      definition: {
        ...a.definition,
        graph: {
          ...a.definition.graph,
          nodes: [...a.definition.graph.nodes, node],
        },
      },
      presentation: {
        ...a.presentation,
        layout: {
          ...a.presentation?.layout,
          positions: {
            ...a.presentation?.layout?.positions,
            [id]: { x: Math.round(pos.x), y: Math.round(pos.y) },
          },
        },
      },
    },
  };
}

// 校验器与运行期都按 JSON-Pointer 指路；属性面板的 JSON 编辑用
// 解析后的值替换节点的 config / inputs。
export function parseJSONOrError(text: string): { ok: true; value: unknown } | { ok: false; error: string } {
  try {
    return { ok: true, value: JSON.parse(text) as unknown };
  } catch (e) {
    return { ok: false, error: e instanceof Error ? e.message : String(e) };
  }
}
