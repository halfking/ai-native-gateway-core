import { describe, expect, it } from 'vitest'
import { statisticsHelpForPath } from './statisticsHelpRoutes'

describe('statisticsHelpForPath', () => {
  it.each([
    ['/usage', 'usage-cost', 'usage'],
    ['/usage-enhanced', 'usage-enhanced', 'usageEnhanced'],
    ['/usage-trend-series', 'usage-trends', 'usageTrendSeries'],
    ['/report-rollup', 'reconciliation', 'reportRollup'],
    ['/reconciliation', undefined, 'reconciliation'],
    ['/cache-state', undefined, 'cacheState'],
    ['/logs', 'request-logs', 'logs'],
    ['/session-audit', 'session-detail', 'sessionAudit'],
    ['/sessions-online', undefined, 'sessionsOnline'],
    ['/session-analytics', 'session-analytics-overview', 'sessionAnalytics'],
    ['/request-detail/abc-123', 'request-detail', 'requestDetail'],
  ])('maps %s to page-specific help', (path, contentId, contextKey) => {
    expect(statisticsHelpForPath(path)).toStrictEqual(contentId ? { contentId, contextKey } : { contextKey })
  })

  it('does not add the help action to unrelated pages', () => {
    expect(statisticsHelpForPath('/dashboard')).toBeNull()
  })
})
