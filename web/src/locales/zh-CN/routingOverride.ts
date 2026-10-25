// routingOverride.ts — RoutingOverrideView 文案。
export default {
  title: '路由覆盖 (Routing Overrides)',
  subtitle: '为 (task_type, profile) 组合固定或禁用特定模型。变更约 1 分钟内通过 OverrideStore 刷新生效。可从关联分析页发现候选封禁对。',
  correlationsLink: '关联分析',
  summary: {
    total: '生效总数',
    bans: '封禁',
    pins: '固定',
    expiring: '7 天内到期',
  },
  filter: {
    activeOnly: '仅生效',
    taskType: '任务类型',
    taskTypePlaceholder: '如 code, reasoning',
    profile: 'Profile',
    all: '(全部)',
    refresh: '刷新',
    loading: '加载中…',
    newOverride: '+ 新建覆盖',
    cancel: '取消',
  },
  create: {
    title: '新建路由覆盖',
    hintPin: '固定：强制该模型排在候选首位（无视得分）。',
    hintBan: '封禁：从候选列表中完全排除该模型。',
    errors: {
      taskTypeRequired: 'task_type 为必填',
      reasonRequired: 'reason 为必填（审计留痕）',
      banRequiresModel: '封禁模式需要填写 model_chosen',
    },
  },
  table: {
    title: '覆盖列表 ({n})',
    empty: '没有匹配的覆盖。点击「+ 新建覆盖」创建。',
    deleteConfirm: '删除覆盖 #{id}（{mode} {model} / {task}）？\n\n这是软删除（将 expires_at 设为 1 秒前），下次刷新后 OverrideStore 会过滤掉。',
    deleteFailed: '删除失败：',
  },
}
