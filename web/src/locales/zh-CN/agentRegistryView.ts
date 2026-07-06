// agentRegistryView.ts — Agent Registry 页(AgentRegistryView)文案。
export default {
  pageTitle: 'Agent Registry',
  autoRefresh: '自动刷新',
  refresh: '刷新',
  loading: '加载中…',

  kind: {
    all: '全部',
    llm_endpoint: 'LLM 端点',
    mcp_server: 'MCP 服务',
    agent: 'Agent',
  },

  kindLabel: {
    llm_endpoint: 'LLM',
    mcp_server: 'MCP',
    agent: 'Agent',
  },

  health: {
    healthy: '健康',
    degraded: '降级',
    down: '不可用',
    unknown: '未知',
  },

  relationType: {
    depends_on: 'depends_on（依赖）',
    calls: 'calls（调用）',
    similar_to: 'similar_to（替代）',
  },

  filter: {
    kindTitle: '类型',
    tenantTitle: '租户',
    defaultTenant: '默认租户',
    placeholder: '按名称 / owner / team / tenant_id 搜索…',
    clear: '清除',
    query: '查询',
    totalCount: '共 {n} 个',
  },

  stats: {
    total: '总数',
    llm: 'LLM 端点',
    mcp: 'MCP 服务',
    healthy: '健康',
    degradedOrDown: '降级/下线',
    loadError: '统计加载失败: {err}',
  },

  table: {
    id: 'ID',
    kind: '类型',
    name: '名称',
    health: '健康',
    version: '版本',
    tenant: '租户',
    owner: 'Owner',
    lastSeen: '最近活跃',
    actions: '操作',
  },

  pager: {
    perPage: '每页',
    pageInfo: '· 第 {page} / {total} 页',
    prev: '上一页',
    next: '下一页',
  },

  time: {
    secondsAgo: '{n}秒前',
    minutesAgo: '{n}分钟前',
    hoursAgo: '{n}小时前',
    daysAgo: '{n}天前',
  },

  detail: {
    title: 'Agent 详情',
    basicInfo: '基本信息',
    refId: 'ref_id',
    kind: '类型',
    name: '名称',
    owner: 'Owner',
    team: 'Team',
    tenant: '租户',
    health: '健康',
    version: '版本',
    firstSeen: '首次发现',
    lastSeen: '最近活跃',
    metadata: '元数据',
    capabilities: '能力清单',
    relations: '关联关系',
    noRelations: '暂无关联',
    upstream: '上游依赖',
    downstream: '下游被调用',
    addRelation: '添加关联',
    showTopology: '查看拓扑',
  },

  link: {
    title: '添加关联',
    sourceAgent: '源 Agent',
    targetId: '目标 Agent ID',
    targetIdPlaceholder: '输入目标 Agent 的 ref_id',
    relationType: '关联类型',
    cancel: '取消',
    submit: '创建关联',
    submitting: '提交中…',
    creating: '创建中…',
  },

  topology: {
    title: '拓扑 (深度 {depth})',
    upstream: '上游 ({n})',
    downstream: '下游 ({n})',
    depth: '深度',
    totalNodes: '共 {n} 个节点',
  },

  empty: {
    noAgents: '暂无 Agent',
  },

  error: {
    loadFailed: '加载失败',
    detailFailed: '加载详情失败',
    linkFailed: '创建关联失败',
    statsFailed: '加载统计失败',
    topologyFailed: '加载拓扑失败',
    invalidTargetId: '请输入有效的目标 Agent ID',
  },
}