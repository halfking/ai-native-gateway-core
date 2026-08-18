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
    expect(source).toContain("saved === 'board'")
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
    const [logs, drawer] = await Promise.all([
      readViewSource('RequestLogsView.vue'),
      readFile(
        resolve(process.cwd(), 'web/src/components/RequestLogDrawer.vue'),
        'utf8',
      ).catch(() => readFile(resolve(process.cwd(), 'src/components/RequestLogDrawer.vue'), 'utf8')),
    ])

    expect(logs).toContain('<RequestLogDrawer')
    expect(logs).not.toContain('getRequestLogDetail')
    expect(drawer).toContain('getRequestLogDetail')
    expect(drawer).not.toContain('getProviderRequestStats')
    expect(drawer).not.toContain('providerStats')
  })

  it('guards the shared drawer against stale detail and session writes', async () => {
    const drawer = await readFile(
      resolve(process.cwd(), 'web/src/components/RequestLogDrawer.vue'),
      'utf8',
    ).catch(() => readFile(resolve(process.cwd(), 'src/components/RequestLogDrawer.vue'), 'utf8'))

    expect(drawer).toContain('let detailLoadSeq = 0')
    expect(drawer).toContain('const loadSeq = ++detailLoadSeq')
    expect(drawer).toContain('if (loadSeq !== detailLoadSeq || props.requestId !== id) return')
    expect(drawer).toContain('function isCurrentDetail(requestId: string, loadSeq: number): boolean')
    expect(drawer).toContain('if (!isCurrentDetail(requestId, loadSeq)) return')
  })

  it('opens request details locally after the trace page was replaced by an inline panel', async () => {
    const [v2, legacy, logs] = await Promise.all([
      readViewSource('DashboardViewV2.vue'),
      readViewSource('DashboardViewLegacy.vue'),
      readViewSource('RequestLogsView.vue'),
    ])

    // 首页 V1/V2 实时流点击应打开本地抽屉，而非跳转到已删除的 trace 路由。
    expect(v2).toContain('activeRequestId.value = id')
    expect(legacy).toContain('activeRequestId.value = id')
    expect(v2).not.toContain('/admin/request-trace')
    expect(legacy).not.toContain('/admin/request-trace')
    // 请求日志列表与首页实时流复用同一个请求详情抽屉；流程详情入口
    // 通过抽屉的 initialTraceOpen prop 控制内嵌面板，而非维护第二套详情实现。
    expect(logs).toContain("activeRequestId.value = requestId")
    expect(logs).toContain("openDetailWithTrace.value = true")
    expect(logs).toContain('<RequestLogDrawer')
    expect(logs).toContain('mode="request-logs"')
    expect(logs).toContain(':initial-trace-open="openDetailWithTrace"')
    expect(logs).not.toContain('<RequestTraceModal')
    expect(logs).not.toContain('/admin/request-trace')
  })
})
