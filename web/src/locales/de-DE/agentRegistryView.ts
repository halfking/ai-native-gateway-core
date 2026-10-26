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
    placeholder: 'Nach Name / Owner / Team / tenant_id suchen…',
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
  all: 'Alle',
  llm_endpoint: 'LLM-Endpunkt',
  mcp_server: 'MCP-Server',
  agent: 'Agent',
  depends_on: 'depends_on (Abhängigkeit)',
  calls: 'calls (Aufruf)',
  similar_to: 'similar_to (Ersatz)',
  loadFailed: 'Agent-Registry konnte nicht geladen werden. Bitte erneut versuchen.',
  healthy: 'Gesund',
  degraded: 'Beeinträchtigt',
  down: 'Nicht verfügbar',
  unknown: 'Unbekannt',
  detailFailed: 'Agent-Details konnten nicht geladen werden. Bitte erneut versuchen.',
  linkFailed: 'Agent-Verknüpfung konnte nicht erstellt werden. Bitte erneut versuchen.',
  statsFailed: 'Agent-Statistik konnte nicht geladen werden. Bitte erneut versuchen.',
  topologyFailed: 'Agent-Topologie konnte nicht geladen werden. Bitte erneut versuchen.',
  invalidTargetId: 'Bitte geben Sie eine gültige Ziel-Agent-ID ein',
}
