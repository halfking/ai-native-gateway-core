// routingDefault.ts — RoutingDefaultsView 文案（M2，22 章 §22.6）。
// 显式默认路由：为 (task_type, profile, tenant_id) 指定首选/兜底模型。
export default {
  title: '默认路由',
  subtitle: '为 (任务类型, profile, 租户) 指定首选或兜底模型。约 1 分钟内通过 DefaultRoutingStore 刷新生效。优先级：ban > pin > 本默认 > 隐式 tag > fallback。',

  scope: {
    platform: '平台',
  },

  summary: {
    total: '活跃总数',
    primary: 'primary 层',
    tenantScoped: '租户级',
    expiring: '7 天内过期',
  },

  filter: {
    activeOnly: '仅活跃',
    taskType: '任务类型',
    taskTypePlaceholder: '如 code、reasoning',
    profile: 'Profile',
    all: '（全部）',
    refresh: '刷新',
    loading: '加载中…',
    newDefault: '+ 新建默认',
    cancel: '取消',
    audit: '审计日志',
  },

  create: {
    title: '新建默认路由',
    hint: '租户级行覆盖平台级。同一作用域下 profile 特定行覆盖通用（任意 profile）行。',
    taskType: '任务类型 *',
    taskTypePlaceholder: '如 code、reasoning、vision',
    profile: 'Profile',
    profileAny: '任意',
    tier: '层级',
    model: '标准模型 *',
    modelPlaceholder: '如 claude-sonnet-4.5',
    modelPickerTitle: '选择标准模型',
    tenantId: '租户 ID',
    tenantIdPlaceholder: '留空 = 平台默认',
    taskTypePickerTitle: '选择任务类型',
    taskTypeLoading: '加载任务类型…',
    tenantPickerTitle: '选择租户',
    tenantSearchPlaceholder: '搜索租户名称或编码…',
    tenantPlatformHint: '不绑定具体租户',
    tenantLoading: '加载租户列表…',
    tenantEmpty: '没有匹配的租户。',
    priority: '优先级',
    reason: '原因',
    expiresAt: '过期时间（可选）',
    submit: '创建',
    submitting: '创建中…',
    errors: {
      taskTypeRequired: '任务类型为必填项。',
      modelRequired: '标准模型为必填项。',
    },
  },

  table: {
    taskType: '任务类型',
    profile: 'Profile',
    tier: '层级',
    model: '模型',
    scope: '作用域',
    priority: '优先级',
    reason: '原因',
    expires: '过期',
    expired: '已过期',
    empty: '尚未配置默认路由。点击「+ 新建默认」添加。',
    deleteConfirm: '确认删除默认 #{id}（模型 {model}，任务 {task}）？',
    deleteFailed: '删除失败：',
  },

  audit: {
    title: '审计日志（最近 500 条）',
    refresh: '刷新',
    ts: '时间',
    action: '操作',
    routingId: '路由 ID',
    taskType: '任务类型',
    model: '模型',
    actor: '操作人',
    reason: '原因',
    empty: '暂无审计记录。',
  },
}
