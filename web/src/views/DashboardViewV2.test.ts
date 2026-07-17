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
  it('defaults to board tab in DashboardView', async () => {
    const source = await readViewSource('DashboardView.vue')
    expect(source).toContain("ref<DashboardTabId>('board')")
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

  it('opens request details locally after the trace page was replaced by a modal', async () => {
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
    // 请求日志列表的流程详情应直接挂载 modal。
    expect(logs).toContain('traceRequestId.value = requestId')
    expect(logs).toContain('<RequestTraceModal')
    expect(logs).not.toContain('/admin/request-trace')
  })
})
