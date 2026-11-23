import { describe, expect, it } from 'vitest'
import { statisticsHelpForPath } from './statisticsHelpRoutes'

describe('statisticsHelpForPath', () => {
  it.each([
    ['/usage', 'usage-cost', 'usage'],
    ['/usage-enhanced', 'usage-enhanced', 'usageEnhanced'],
    ['/usage-trend-series', 'usage-trends', 'usageTrendSeries'],
    ['/report-rollup', 'reconciliation', 'reportRollup'],
    ['/reconciliation', 'reconciliation', 'reconciliation'],
    ['/cache-state', 'usage-enhanced', 'cacheState'],
    ['/logs', 'request-logs', 'logs'],
    ['/session-audit', 'session-detail', 'sessionAudit'],
    ['/sessions-online', 'session-analytics-overview', 'sessionsOnline'],
    ['/session-analytics', 'session-analytics-overview', 'sessionAnalytics'],
    ['/request-detail/abc-123', 'request-detail', 'requestDetail'],
  ])('maps %s to page-specific help', (path, contentId, contextKey) => {
    expect(statisticsHelpForPath(path)).toEqual({ contentId, contextKey })
  })

  it('does not add the help action to unrelated pages', () => {
    expect(statisticsHelpForPath('/dashboard')).toBeNull()
  })
})
