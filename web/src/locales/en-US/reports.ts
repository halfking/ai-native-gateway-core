// reports.ts — Reconciliation report page (provider/internal dual view, R65 i18n sync).
// Keys mirror views/admin/ReconciliationReport.vue.
export default {
  // View switch
  providerView: 'Provider Reconciliation',
  internalView: 'Internal Reconciliation',
  // Toolbar actions
  exportExcel: 'Export Excel',
  rerun: 'Rerun End Day',
  rerunDone: 'Rerun completed',
  rerunFailed: 'Rerun failed',
  // Coverage / empty state
  daysCovered: 'days of snapshots',
  noSnapshots: 'No report snapshots in this range (the daily aggregation job generates the previous day in the early morning, or use "Rerun End Day" to backfill)',
  // Summary cards
  requests: 'Requests',
  totalTokens: 'Total tokens',
  in: 'In',
  out: 'Out',
  cacheRead: 'Cache read',
  cacheWrite: 'Cache write',
  providerCost: 'Provider cost',
  cacheHit: 'Cache hit',
  internalCredits: 'Internal credits',
  internalCost: 'Internal amount',
  // Section titles
  byProvider: 'By provider',
  byTenant: 'By tenant',
  byPerson: 'By person',
  byModel: 'By model',
  byDay: 'By day',
  // Column labels
  provider: 'Provider',
  tenant: 'Tenant',
  person: 'Person',
  model: 'Model',
  date: 'Date',
  success: 'Success',
  errors: 'Failures',
  cost: 'Cost',
  errorBreakdown: 'Error breakdown',
  qualityScore: 'Quality score',
  // ── 2026-09-29 multi-dimensional filter round ──────────────────────────
  // Filter bar
  filters: 'Filters',
  all: 'All',
  activeFilters: 'active',
  clearFilters: 'Clear filters',
  // New dimensions
  credential: 'Credential',
  apiKey: 'API key',
  byCredential: 'By credential',
  byApiKey: 'By API key',
  // Granularity
  summaryOnly: 'Summary',
  dailyDetail: 'Daily detail',
  // Tables
  columns: 'Columns',
  rows: 'rows',
  inputTokens: 'Input tokens',
  cacheTokens: 'Cache tokens',
  outputTokens: 'Output tokens',
  cacheRate: 'Cache rate',
  errorRate: 'Error rate',
  topError: 'Top error',
  noErrors: 'No failures',
  tokens: 'Token split',
  unassigned: 'Unassigned',
  qualityHint: 'Success rate × latency factor (lower P95 scores higher)',
  // Collapsible model list
  modelStats: 'Model statistics',
  modelStatsHint: 'Click to expand; pick a model name to filter by it',
  actions: 'Actions',
  filterBy: 'Filter',
  // Charts
  chartTrend: 'Request volume and error rate trend',
  chartByModel: 'Daily volume per model',
  chartErrors: 'Top 10 error types',
  chartOther: 'Other',
  chartFolded: 'Showing the top',
  chartFoldedRest: 'models by request count; the rest are folded into "Other"',
  noData: 'No data',
  // Snapshot caliber disclosure
  legacyCoverage: 'Days covered only by legacy snapshots:',
  legacyCoverageRest:
    ' — those days contribute to totals and the daily series only; credential/user/API-key dimensions appear after backfill (use "Rerun End Day" day by day)',

}
