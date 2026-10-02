// usageTrend.ts — 全页用量趋势分析（2026-10-02 看板轮；8 语言同步）。
export default {
  pageTitle: 'Usage Trend Explorer',
  pageSub: 'Per-model usage over time; filter by provider / tenant / API key / model / metric',
  loadFailed: 'Failed to load',
  detailTimeoutHint: 'Filtering by API key scans raw request logs and can time out on wide ranges; try a shorter time range (e.g. within 7 days)',
  filterProvider: 'Provider',
  filterTenant: 'Tenant',
  filterApiKey: 'API Key',
  filterModel: 'Model',
  filterMetric: 'Metric',
  filterAll: 'All',
  shareByMetric: 'Share (metric)',
  sourceDetail: 'Request detail',
  // 2026-10-02 multi-select / clear-all / auto-refresh round
  clearAll: 'Clear all',
  autoRefresh: 'Auto refresh',
  autoRefreshOff: 'Off',
  autoRefresh30s: '30s',
  autoRefresh1m: '1 min',
  autoRefresh5m: '5 min',
}