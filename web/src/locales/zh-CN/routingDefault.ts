// routingDefault.ts — 智能路由配置（默认路由）文案
export default {
  title: '智能路由配置',
  subtitle: '按任务类型配置主要 / 次级 / 托底模型；约 1 分钟内刷新生效。优先级：ban > pin > 本默认 > 隐式 tag > fallback。',
  scope: {
    platform: '平台',
  },
  rail: {
    all: '全部',
    allHint: '显示所有任务类型',
  },
  tiers: {
    primary: '主要',
    secondary: '次级',
    fallback: '托底',
  },
  profiles: {
    any: '通用',
    smart: '智能',
    speed_first: '速度',
    cost_first: '成本',
  },
  actions: {
    addModel: '+ 添加模型',
    detail: '明细',
    delete: '删除',
    save: '保存',
    saving: '保存中…',
    cancel: '取消',
    refresh: '刷新',
    loading: '加载中…',
  },
  fields: {
    model: '模型',
    profile: '特性优先',
    priority: '优先级',
    platform: '平台 / 租户',
    reason: '原因',
    expires: '过期时间',
    tier: '分组',
    taskType: '任务类型',
  },
  empty: {
    group: '该分组暂无模型，点击「添加模型」配置。',
    needTask: '请先在左侧选择一个任务类型，再添加模型。',
    none: '尚未配置默认路由。',
  },
  create: {
    title: '添加模型到「{tier}」',
    modelRequired: '请选择模型',
    taskRequired: '请先选择任务类型',
    submit: '添加',
    submitting: '添加中…',
  },
  detail: {
    title: '编辑默认路由 #{id}',
    clearExpires: '清除过期时间（永久有效）',
  },
  table: {
    deleteConfirm: '确认删除默认 #{id}（模型 {model}，任务 {task}）？',
    deleteFailed: '删除失败：',
    saveFailed: '保存失败：',
  },
  filter: {
    activeOnly: '仅活跃',
  },
}
