// 新工作流的起始定义：单节点、单出口，最小可校验图。
// 节点 id 避开 "start"/"end"——Eino 保留这两个名字，用作节点 id
// 会在编译期失败（compose.START / compose.END）。

import type { Artifact } from "./schema";

export function blankArtifact(title: string): Artifact {
  return {
    definition: {
      schema_version: "inofy.workflow/v1",
      graph: {
        nodes: [
          {
            id: "begin",
            kind: "call",
            type: "inofy.value@1",
            config: { value: { message: title } },
          },
        ],
        edges: [],
        exits: ["begin"],
      },
    },
    presentation: {
      title,
      layout: {
        positions: { begin: { x: 80, y: 80 } },
        viewport: { x: 0, y: 0, zoom: 1 },
      },
    },
  };
}

// 工作流 id 作为地址与主键使用：不做后端未声明的额外约束，
// 只挡住必然失败的输入（空白、路径分隔符、保留游标分隔符）。
export function validateWorkflowID(id: string): string | null {
  if (id.trim() === "") return "工作流 id 不能为空";
  if (id !== id.trim()) return "工作流 id 首尾不能有空白";
  if (/[/\\?#:]/.test(id)) return "工作流 id 不能包含 / \\ ? # :";
  if (id.length > 64) return "工作流 id 不超过 64 字符";
  return null;
}
