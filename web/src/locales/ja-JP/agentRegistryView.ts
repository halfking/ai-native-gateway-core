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
    placeholder: '名前・担当者・チーム・tenant_id で検索…',
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
  all: 'すべて',
  llm_endpoint: 'LLM エンドポイント',
  mcp_server: 'MCP サーバー',
  agent: 'エージェント',
  depends_on: 'depends_on（依存）',
  calls: 'calls（呼び出し）',
  similar_to: 'similar_to（代替）',
  loadFailed: 'Agent レジストリの読み込みに失敗しました。再試行してください。',
  healthy: "正常",
  degraded: '低下',
  down: 'ダウン',
  unknown: '不明',
  detailFailed: 'Agent 詳細の読み込みに失敗しました。再試行してください。',
  linkFailed: 'Agent の関連付けを作成できませんでした。再試行してください。',
  statsFailed: 'Agent 統計の読み込みに失敗しました。再試行してください。',
  topologyFailed: 'Agent トポロジの読み込みに失敗しました。再試行してください。',
  invalidTargetId: '有効なターゲットエージェント ID を入力してください',
}
