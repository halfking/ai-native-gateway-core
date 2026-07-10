// agentRegistryView.ts — Agent Registry page.
export default {
  pageTitle: 'Agent Registry',
  autoRefresh: 'Auto refresh',
  refresh: 'Refresh',
  loading: 'Loading…',

  kind: {
    all: 'All',
    llm_endpoint: 'LLM endpoint',
    mcp_server: 'MCP server',
    agent: 'Agent',
  },

  kindLabel: {
    llm_endpoint: 'LLM',
    mcp_server: 'MCP',
    agent: 'Agent',
  },

  health: {
    healthy: 'Healthy',
    degraded: 'Degraded',
    down: 'Down',
    unknown: 'Unknown',
  },

  relationType: {
    depends_on: 'depends_on',
    calls: 'calls',
    similar_to: 'similar_to',
  },

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
    basicInfo: 'Basic info',
    refId: 'ref_id',
    kind: 'Kind',
    name: 'Name',
    owner: 'Owner',
    team: 'Team',
    tenant: 'Tenant',
    health: 'Health',
    version: 'Version',
    firstSeen: 'First seen',
    lastSeen: 'Last seen',
    metadata: 'Metadata',
    capabilities: 'Capabilities',
    relations: 'Relations',
    noRelations: 'No relations',
    upstream: 'Upstream',
    downstream: 'Downstream',
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
    submitting: 'Submitting…',
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

  error: {
    loadFailed: 'Failed to load',
    detailFailed: 'Failed to load detail',
    linkFailed: 'Failed to create relation',
    statsFailed: 'Failed to load stats',
    topologyFailed: 'Failed to load topology',
    invalidTargetId: 'Please enter a valid target Agent ID',
  },

  // 扁平键（供 Vue 组件直接使用）
  all: 'Alle',
  llm_endpoint: 'LLM-Endpunkt',
  mcp_server: 'MCP-Server',
  agent: 'Agent',
  depends_on: 'depends_on (Abhängigkeit)',
  calls: 'calls (Aufruf)',
  similar_to: 'similar_to (Ersatz)',
  loadFailed: 'Laden fehlgeschlagen',
  healthy: 'Gesund',
  degraded: 'Beeinträchtigt',
  down: 'Nicht verfügbar',
  unknown: 'Unbekannt',
  detailFailed: 'Detail laden fehlgeschlagen',
  linkFailed: 'Beziehung erstellen fehlgeschlagen',
  statsFailed: 'Statistik laden fehlgeschlagen',
  topologyFailed: 'Topologie laden fehlgeschlagen',
  invalidTargetId: 'Bitte geben Sie eine gültige Ziel-Agent-ID ein',
}
