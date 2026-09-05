import { describe, expect, it } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

async function readViewSource(filename: string): Promise<string> {
  const candidates = [
    resolve(process.cwd(), 'web/src/views', filename),
    resolve(process.cwd(), 'src/views', filename),
  ]
  for (const path of candidates) {
    try {
      return await readFile(path, 'utf8')
    } catch {
      /* try next */
    }
  }
  throw new Error(`${filename} not found in candidate paths`)
}

describe('dashboard board tab contract', () => {
  it('defaults to stream tab in DashboardView', async () => {
    const source = await readViewSource('DashboardView.vue')
    expect(source).toContain("ref<DashboardTabId>('stream')")
    expect(source).toContain('readStoredTab')
  })

  it('renders BoardPanel only on board tab', async () => {
    const source = await readViewSource('DashboardViewV2.vue')
    expect(source).toContain("activeTab === 'board'")
    expect(source).toContain('BoardPanel')
    expect(source).not.toMatch(/<div class="stats-section"/)
  })

  it('loads SSE only when stream tab is mounted via v-if', async () => {
    const source = await readViewSource('DashboardViewV2.vue')
    expect(source).toMatch(/activeTab === 'stream'/)
    expect(source).toContain('LiveRequestStreamV2')
    const dash = await readViewSource('DashboardView.vue')
    expect(dash).not.toContain('useLiveStream')
  })

  it('board panel tolerates missing operational inject', async () => {
    const source = await readFile(
      resolve(process.cwd(), 'web/src/components/board/BoardPanel.vue'),
      'utf8',
    ).catch(() => readFile(resolve(process.cwd(), 'src/components/board/BoardPanel.vue'), 'utf8'))
    expect(source).toContain('operational?.value')
    expect(source).not.toContain('boardState.operational.value')
  })

  it('reuses one detail drawer without loading provider statistics', async () => {
    const [logs, drawerShell, unified] = await Promise.all([
      readViewSource('RequestLogsView.vue'),
      readFile(
        resolve(process.cwd(), 'web/src/components/RequestLogDrawer.vue'),
        'utf8',
      ).catch(() => readFile(resolve(process.cwd(), 'src/components/RequestLogDrawer.vue'), 'utf8')),
      readFile(
        resolve(process.cwd(), 'web/src/components/detail/UnifiedRequestSessionDrawer.vue'),
        'utf8',
      ).catch(() => readFile(resolve(process.cwd(), 'src/components/detail/UnifiedRequestSessionDrawer.vue'), 'utf8')),
    ])

    expect(logs).toContain('<RequestLogDrawer')
    expect(logs).not.toContain('getRequestLogDetail')
    expect(drawerShell).toContain('UnifiedRequestSessionDrawer')
    expect(unified).toContain('getRequestLogDetail')
    expect(unified).not.toContain('getProviderRequestStats')
    expect(unified).not.toContain('providerStats')
  })

  it('delegates drawer shell to UnifiedRequestSessionDrawer', async () => {
    const drawer = await readFile(
      resolve(process.cwd(), 'web/src/components/RequestLogDrawer.vue'),
      'utf8',
    ).catch(() => readFile(resolve(process.cwd(), 'src/components/RequestLogDrawer.vue'), 'utf8'))

    expect(drawer).toContain('UnifiedRequestSessionDrawer')
    expect(drawer).toContain(':request-id="requestId"')
  })

  it('opens request details via the request log drawer from live stream and request logs', async () => {
    // DashboardViewLegacy.vue was removed (P2-7 dead code, 2026-09-01):
    // V2 实时流点击走 <RequestLogDrawer>，drawer header 提供「打开全页」按钮
    // 接入全屏详情页（openRequestDetailPage）；请求日志列表直接跳全屏详情。
    const [v2, logs, unified] = await Promise.all([
      readViewSource('DashboardViewV2.vue'),
      readViewSource('RequestLogsView.vue'),
      readFile(
        resolve(process.cwd(), 'web/src/components/detail/UnifiedRequestSessionDrawer.vue'),
        'utf8',
      ).catch(() => readFile(resolve(process.cwd(), 'src/components/detail/UnifiedRequestSessionDrawer.vue'), 'utf8')),
    ])

    expect(v2).toContain('<RequestLogDrawer')
    expect(v2).toContain('@open-request="openDrawerRequestFullscreen"')
    expect(v2).not.toContain('/admin/request-trace')
    expect(unified).toContain('openRequestDetailPage(')
    expect(unified).toContain('打开全页')
    // 请求日志列表同样新开全屏详情；抽屉可保留兼容其它入口，但列表点击不再写 activeRequestId。
    expect(logs).toContain('openRequestDetailPage(requestId')
    expect(logs).not.toContain('<RequestTraceModal')
    expect(logs).not.toContain('/admin/request-trace')
  })

  it('stats tab shows overview panel before drilldown and uses KPI row', async () => {
    const v2 = await readViewSource('DashboardViewV2.vue')
    const statsPanel = await readFile(
      resolve(process.cwd(), 'web/src/components/SessionStatsPanel.vue'),
      'utf8',
    ).catch(() => readFile(resolve(process.cwd(), 'src/components/SessionStatsPanel.vue'), 'utf8'))

    const statsIdx = v2.indexOf('SessionStatsPanel')
    const drillIdx = v2.indexOf('SessionDrilldownPanel')
    expect(statsIdx).toBeGreaterThan(-1)
    expect(drillIdx).toBeGreaterThan(-1)
    expect(statsIdx).toBeLessThan(drillIdx)

    expect(statsPanel).toContain('DashboardStatsRow')
    expect(statsPanel).toContain('cost_stats')
    expect(statsPanel).toContain('compliance_stats')
    expect(statsPanel).toContain('model_usage')
  })

  it('opens request details from queue journey, node drawer, and stats drilldown', async () => {
    const [journey, nodeActions, drill] = await Promise.all([
      readFile(resolve(process.cwd(), 'web/src/components/RequestJourneyQueues.vue'), 'utf8').catch(() =>
        readFile(resolve(process.cwd(), 'src/components/RequestJourneyQueues.vue'), 'utf8')),
      readFile(resolve(process.cwd(), 'web/src/composables/useNodeDetailDrawerActions.ts'), 'utf8').catch(() =>
        readFile(resolve(process.cwd(), 'src/composables/useNodeDetailDrawerActions.ts'), 'utf8')),
      readFile(resolve(process.cwd(), 'web/src/components/session/SessionDrilldownPanel.vue'), 'utf8').catch(() =>
        readFile(resolve(process.cwd(), 'src/components/session/SessionDrilldownPanel.vue'), 'utf8')),
    ])
    expect(journey).toContain('openRequestDetailPage(requestId')
    expect(nodeActions).toContain('openRequestDetailPage(rid')
    expect(drill).toContain('openRequestDetailPage(payload.requestId')
    expect(drill).toContain('@open-request="openRequest"')
  })

})
