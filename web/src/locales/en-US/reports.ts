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

}
