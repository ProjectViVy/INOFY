// Minimal en/zh string table. One dictionary, one lookup — no
// framework for a two-locale editor.

export type Locale = "en" | "zh";

const table: Record<string, { en: string; zh: string }> = {
  "node.unresolved": { en: "unknown type", zh: "未知类型" },
  "node.exit": { en: "exit", zh: "出口" },
  "prop.config": { en: "Config", zh: "配置" },
  "prop.config.raw": { en: "Config (JSON)", zh: "配置（JSON）" },
  "prop.inputs": { en: "Inputs (JSON)", zh: "输入（JSON）" },
  "prop.json.invalid": { en: "Invalid JSON", zh: "JSON 无效" },
  "prop.node": { en: "Node", zh: "节点" },
  "editor.dirty": { en: "unsaved changes", zh: "未保存修改" },
  "run.waiting": { en: "waiting", zh: "等待中" },
  "action.save": { en: "Save", zh: "保存" },
  "action.publish": { en: "Publish", zh: "发布" },
  "action.run": { en: "Run", zh: "运行" },
  "action.cancel": { en: "Cancel", zh: "取消" },
  "action.resume": { en: "Resume", zh: "继续" },
};

let locale: Locale = "en";

export function setLocale(l: Locale) {
  locale = l;
}
export function getLocale(): Locale {
  return locale;
}
export function t(key: string): string {
  return table[key]?.[locale] ?? key;
}
