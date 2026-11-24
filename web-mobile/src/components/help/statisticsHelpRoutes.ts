export interface StatisticsHelpRoute {
  contentId?: string
  contextKey: string
}

const routes: Array<{ match: (path: string) => boolean; value: StatisticsHelpRoute }> = [
  { match: p => p === '/usage', value: { contentId: 'usage-cost', contextKey: 'usage' } },
  { match: p => p === '/usage-enhanced', value: { contentId: 'usage-enhanced', contextKey: 'usageEnhanced' } },
  { match: p => p === '/usage-trend-series', value: { contentId: 'usage-trends', contextKey: 'usageTrendSeries' } },
  { match: p => p === '/report-rollup', value: { contentId: 'reconciliation', contextKey: 'reportRollup' } },
  { match: p => p === '/reconciliation', value: { contextKey: 'reconciliation' } },
  { match: p => p === '/cache-state', value: { contextKey: 'cacheState' } },
  { match: p => p === '/logs', value: { contentId: 'request-logs', contextKey: 'logs' } },
  { match: p => p === '/session-audit', value: { contentId: 'session-detail', contextKey: 'sessionAudit' } },
  { match: p => p === '/sessions-online', value: { contextKey: 'sessionsOnline' } },
  { match: p => p === '/session-analytics', value: { contentId: 'session-analytics-overview', contextKey: 'sessionAnalytics' } },
  { match: p => p === '/request-detail' || p.startsWith('/request-detail/'), value: { contentId: 'request-detail', contextKey: 'requestDetail' } },
]

export function statisticsHelpForPath(path: string): StatisticsHelpRoute | null {
  return routes.find(route => route.match(path))?.value ?? null
}
