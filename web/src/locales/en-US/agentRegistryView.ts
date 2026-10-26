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
    placeholder: 'Search by name / owner / team / tenant_id…',
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
  all: 'All',
  llm_endpoint: 'LLM endpoint',
  mcp_server: 'MCP server',
  agent: 'Agent',
  depends_on: 'depends_on',
  calls: 'calls',
  similar_to: 'similar_to',
  loadFailed: 'The Agent registry could not be loaded. Please retry.',
  healthy: 'Healthy',
  degraded: 'Degraded',
  down: 'Down',
  unknown: 'Unknown',
  detailFailed: 'The Agent detail could not be loaded. Please retry.',
  linkFailed: 'The Agent relation could not be created. Please retry.',
  statsFailed: 'The Agent stats could not be loaded. Please retry.',
  topologyFailed: 'The Agent topology could not be loaded. Please retry.',
  invalidTargetId: 'Please enter a valid target Agent ID',
}
