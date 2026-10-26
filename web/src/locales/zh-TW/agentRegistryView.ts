// agentRegistryView.ts — Agent Registry page.
export default {
  pageTitle: 'Agent Registry',
  autoRefresh: 'Auto refresh',
  refresh: 'Refresh',
  loading: 'Loading…',





  filter: {
    kindTitle: 'Kind',
    tenantTitle: 'Tenant',
    defaultTenant: 'Default tenant',
    placeholder: '依名稱 / 負責人 / 團隊 / tenant_id 搜尋…',
    clear: 'Clear',
    query: 'Query',
    totalCount: '{n} total',
  },

  stats: {
    total: 'Total',
    llm: 'LLM endpoints',
    mcp: 'MCP servers',
    healthy: 'Healthy',
    degradedOrDown: 'Degraded / down',
    loadError: 'Failed to load stats: {err}',
  },

  table: {
    id: 'ID',
    kind: 'Kind',
    name: 'Name',
    health: 'Health',
    version: 'Version',
    tenant: 'Tenant',
    owner: 'Owner',
    lastSeen: 'Last seen',
    actions: 'Actions',
  },

  pager: {
    perPage: 'Per page',
    pageInfo: '· Page {page} / {total}',
    prev: 'Previous',
    next: 'Next',
  },

  time: {
    secondsAgo: '{n}s ago',
    minutesAgo: '{n}m ago',
    hoursAgo: '{n}h ago',
    daysAgo: '{n}d ago',
  },

  detail: {
    title: 'Agent detail',
    owner: 'Owner',
    team: 'Team',
    tenant: 'Tenant',
    health: 'Health',
    version: 'Version',
    firstSeen: 'First seen',
    lastSeen: 'Last seen',
    metadata: 'Metadata',
    noRelations: 'No relations',
    addRelation: 'Add relation',
    showTopology: 'Show topology',
    close: 'Close',
  },

  link: {
    title: 'Add relation',
    sourceAgent: 'Source Agent',
    targetId: 'Target Agent ID',
    targetIdPlaceholder: 'Enter target Agent ref_id',
    relationType: 'Relation type',
    cancel: 'Cancel',
    submit: 'Create relation',
    creating: 'Creating…',
  },

  topology: {
    title: 'Topology (depth {depth})',
    upstream: 'Upstream ({n})',
    downstream: 'Downstream ({n})',
    depth: 'Depth',
    totalNodes: '{n} nodes',
  },

  empty: {
    noAgents: 'No agents yet',
  },


  // 扁平键（供 Vue 组件直接使用）
  all: '全部',
  llm_endpoint: 'LLM 端點',
  mcp_server: 'MCP 服務',
  agent: 'Agent',
  depends_on: 'depends_on（依賴）',
  calls: 'calls（呼叫）',
  similar_to: 'similar_to（取代）',
  loadFailed: 'Agent 註冊表沒能載入出來，請重試',
  healthy: '健康',
  degraded: '降級',
  down: '無法使用',
  unknown: '未知',
  detailFailed: 'Agent 詳情沒能載入出來，請重試',
  linkFailed: 'Agent 關聯沒能建立起來，請重試',
  statsFailed: 'Agent 統計沒能載入出來，請重試',
  topologyFailed: 'Agent 拓撲沒能載入出來，請重試',
  invalidTargetId: '請輸入有效的目標 Agent ID',
}
