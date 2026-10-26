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
    placeholder: 'ابحث بالاسم / المالك / الفريق / tenant_id…',
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
  all: 'الكل',
  llm_endpoint: 'نقطة نهاية LLM',
  mcp_server: 'خادم MCP',
  agent: 'وكيل',
  depends_on: 'depends_on (تبعية)',
  calls: 'calls (استدعاء)',
  similar_to: 'similar_to (بديل)',
  loadFailed: 'تعذّر تحميل سجل الوكلاء. أعد المحاولة.',
  healthy: 'صحي',
  degraded: 'متضرر',
  down: 'غير متاح',
  unknown: 'غير معروف',
  detailFailed: 'تعذّر تحميل تفاصيل الوكيل. أعد المحاولة.',
  linkFailed: 'تعذّر إنشاء علاقة الوكيل. أعد المحاولة.',
  statsFailed: 'تعذّر تحميل إحصاءات الوكلاء. أعد المحاولة.',
  topologyFailed: 'تعذّر تحميل هيكل الوكلاء. أعد المحاولة.',
  invalidTargetId: 'يرجى إدخال معرف Agent هدف صالح',
}
