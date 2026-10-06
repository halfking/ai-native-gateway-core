import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RequestLogsView from './RequestLogsView.vue'
import { fetchRequestLogs } from '@/api/requestLogs'
import type { RequestLogRow } from '@/api/requestLogs'
import { useAuthStore } from '@/stores/auth'

/**
 * 请求日志视图（2026-10-06）。
 *
 * 钉两条从后端源码读出来、**界面看不出来**的契约：
 *
 * ① ★ **时间窗按租户收窄**：后端 clampQueryWindowForTenant（logs.go:476/:1336-1341）
 *    对非 default 租户把 >72h 的查询静默改成 3 天，**响应里没有任何字段说这件事**。
 *    ⇒ 判据是「非 default 租户的界面上根本不出现 7 天/30 天选项」，
 *      且出现「窗口已收窄」提示。反向做（照渲染全部选项）会让用户以为
 *      「最近 7 天没请求」而实际是后端只给了 3 天。
 *
 * ② 分页用 `page`/`page_size`（logs.go:478-488）——**不是** model-integrity 的
 *    `limit`/`offset`。两个分页端点参数名不同，照抄会静默拿到第 1 页。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/requestLogs', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/requestLogs')>()
  return { ...actual, fetchRequestLogs: vi.fn() }
})

const PAGE_SIZE = 20

/**
 * ★ 等列表**彻底静止**。踩坑记录见 IntegrityView.spec 的 settle() 注释：
 *   ① loadNext 在 loading 态静默 return；
 *   ② autoFill 逐页补页且每轮之间 state 短暂回 idle ⇒ 纯 state 轮询会误判；
 *   ③ 用 mock.calls.length 判稳定同样有竞态（calls 非响应式）。
 *   本轮实测：只看 state 的版本 8 次里 4 次红，用 calls 的版本 10 次里 8 次红，
 *   改固定时长后 10 次全绿。⇒ 判据写法的选择本身就是这道题的答案之一。
 */
async function settle(ms = 60): Promise<void> {
  await flushPromises()
  await new Promise((r) => setTimeout(r, ms))
  await flushPromises()
}

function row(n: number, over: Partial<RequestLogRow> = {}): RequestLogRow {
  return {
    ts: '2026-10-06T13:00:00Z',
    request_id: `req-${n}`,
    api_key_id: 1, end_user_id: null,
    client_model: 'claude-sonnet-4-6', outbound_model: 'claude-sonnet-4-6',
    credential_id: 7, credential_label: 'main',
    provider_id: 3, provider_name: 'Anthropic', provider_code: 'anthropic',
    client_profile: 'roocode', request_mode: 'chat',
    prompt_tokens: 100, completion_tokens: 50, cache_read_tokens: 0, cache_write_tokens: 0,
    total_tokens: 150, cost_usd: 0.0123, cost_display: '0.0123', cost_currency: 'USD',
    latency_ms: 820, success: true, request_status: 'success',
    ...over,
  }
}

let mounted: Array<{ unmount(): void }> = []

async function mountAs(tenantId: string) {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().userInfo = {
    id: 1, tenant_id: tenantId, username: 'ops', display_name: 'Ops',
    email: 'o@x', role: 'tenant_admin', enabled: true,
  }
  const w = mount(RequestLogsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

describe('RequestLogsView 请求日志', () => {
  beforeEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
    document.body.innerHTML = ''
    vi.resetAllMocks()   // 必须 reset：clear 不清 once 队列（见文档 §11.13）
  })
  afterEach(() => {
    for (const w of mounted) {
      try { w.unmount() } catch { /* ignore */ }
    }
    mounted = []
  })

  it('★ 非 default 租户：界面上没有 7 天 / 30 天选项，并出现窗口收窄提示', async () => {
    ;(fetchRequestLogs as ReturnType<typeof vi.fn>).mockResolvedValue({
      items: [row(1)], count: 1, aggregate: {},
    })
    const w = await mountAs('tenant-a')
    await settle()

    const chips = w.findAll('.logs__chip').map((c) => c.text())
    expect(chips.join('|')).not.toContain('7d')
    expect(chips.join('|')).not.toContain('30d')
    // 72h 档仍可用（等于后端上限）
    expect(chips.join('|')).toContain('3d')
    // 明确告知窗口被收窄
    expect(w.text()).toContain('at most 72 hours')
  })

  it('default 租户：7 天 / 30 天可用，且不显示收窄提示', async () => {
    ;(fetchRequestLogs as ReturnType<typeof vi.fn>).mockResolvedValue({
      items: [row(1)], count: 1, aggregate: {},
    })
    const w = await mountAs('default')
    await settle()

    const chips = w.findAll('.logs__chip').map((c) => c.text())
    expect(chips.join('|')).toContain('7d')
    expect(chips.join('|')).toContain('30d')
    expect(w.text()).not.toContain('at most')
  })

  it('分页用 page/page_size（不是 limit/offset）', async () => {
    const m = fetchRequestLogs as ReturnType<typeof vi.fn>
    m.mockResolvedValue({ items: [row(1)], count: 500, aggregate: {} })
    const w = await mountAs('default')
    await settle()

    const p = m.mock.calls[0]?.[0] as Record<string, unknown>
    expect(p.page).toBe(1)
    expect(p.page_size).toBe(PAGE_SIZE)
    // ★ 反向判据：这两个键一旦出现，说明照抄了 model-integrity 的分页形态
    expect(p.limit).toBeUndefined()
    expect(p.offset).toBeUndefined()
    // 总数取 count（后端 COUNT），不是 items.length
    expect(w.text()).toContain('500')
  })

  it('成本字段 string 形态能正常显示（不为空串渲染 $0）', async () => {
    const m = fetchRequestLogs as ReturnType<typeof vi.fn>
    m.mockResolvedValue({
      items: [row(1, { cost_display: '0.0123' }), row(2, { cost_display: '' })],
      count: 2, aggregate: {},
    })
    const w = await mountAs('default')
    await settle()
    const text = w.text()
    expect(text).toContain('$0.0123')
    expect(text).not.toContain('NaN')
    expect(text).not.toContain('$0.0000')
  })

  it('换状态筛选必须重取（状态是服务端参数）', async () => {
    const m = fetchRequestLogs as ReturnType<typeof vi.fn>
    m.mockResolvedValue({ items: [row(1)], count: 1, aggregate: {} })
    const w = await mountAs('default')
    // ★ 先等首屏（含 autoFill 补页）全部落地，再清记录 ——
    //   否则清完之后补页请求才写入 mock.calls，at(-1) 取到的是 page=2。
    await settle()
    m.mockClear()
    m.mockResolvedValue({ items: [], count: 0, aggregate: {} })

    const chip = w.findAll('.logs__chip--sm').find((c) => c.text().includes('Failure'))
    if (!chip) throw new Error('失败筛选 chip 不存在')
    await chip.trigger('click')
    await flushPromises()
    await settle()

    expect(m.mock.calls.at(-1)?.[0]).toMatchObject({ request_status: 'failure' })
  })
})
