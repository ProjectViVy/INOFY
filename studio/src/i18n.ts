// 界面文案表：全中文。键名保持英文以便代码检索；未登记的键
// 原样返回，便于早期开发发现遗漏（生产文案全部登记在册）。

const table: Record<string, string> = {
  // 通用动作
  "action.save": "保存",
  "action.validate": "校验",
  "action.publish": "发布",
  "action.run": "运行",
  "action.cancel": "取消",
  "action.resume": "继续",
  "action.retry": "重试",
  "action.close": "关闭",
  "action.back": "返回",
  "action.copy": "复制",
  "action.copied": "已复制",
  "action.refresh": "刷新",
  "action.confirm": "确认",
  "action.discard": "放弃",

  // 节点
  "node.unresolved": "未知类型",
  "node.exit": "出口",
  "node.entry": "起始节点",
  "node.select": "选择节点以查看属性",

  // 属性
  "prop.node": "节点属性",
  "prop.config": "配置",
  "prop.config.raw": "配置（JSON）",
  "prop.inputs": "输入（JSON）",
  "prop.json.invalid": "JSON 无效",
  "prop.empty": "未选中节点",

  // 编辑器
  "editor.dirty": "未保存修改",
  "editor.saved": "已保存",
  "editor.conflict": "冲突",
  "editor.unsaved.hint": "发布需要不可变修订，运行需要可快照的 draft etag；请先保存草稿。",
  "editor.draft.etag": "草稿 etag",
  "editor.revision": "修订",
  "editor.no.revision": "尚无修订",

  // 运行
  "run.waiting": "等待中",
  "run.events": "事件账本",
  "run.output": "输出",
  "run.limit": "上限",
  "run.waits": "等待项",

  // 状态词
  "status.loading": "载入中",
  "status.empty": "暂无数据",
  "status.error": "出错",
  "status.offline": "未连接",
  "status.online": "已连接",
};

export function t(key: string): string {
  return table[key] ?? key;
}
