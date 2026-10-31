import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import LiveView from './LiveView.vue'
import { setLocale } from '@/i18n'
import {
  fetchProvidersToday,
  fetchModelsToday,
  fetchClientsToday,
  liveStatRowName,
  liveStatRowSuccessRate,
} from '@/api/liveTopology'
import { fetchLiveStreamStats } from '@/api/liveStream'
import { fetchJourneyQueues } from '@/api/requestJourney'
import { useAuthStore } from '@/stores/auth'
import { createPinia, setActivePinia } from 'pinia'

/**
 * live 实时页回归护栏（2026-10-29）。
 *
 * 三条腿的契约（桌面 web/src/api/topology.ts 与 admin/topology_window_stats.go
 * 逐条对拍过；本页是它们的移动端消费面）：
 *  ① ★★★★ `degraded: true` = 聚合表未就绪 ⇒ 显示「统计不可用」横幅，
 *     **不许**把缺数据渲染成零行或零卡 —— 零会被人读成「没人用」。
 *  ② ★★★★ day 口径有 rollup 水位（≤1h 滞后）⇒ 水位时间必须显示；
 *     短窗口口径（5m/1h）没有水位键，不显示。
 *  ③ ★★★★ 区间标题以**响应回显**的 window 字段为准（后端把乱写静默落回 day）。
 *  ④ ★★★★ 成功率分母 0 ⇒ 「—」，不许 0% 冒充（liveStatRowSuccessRate 返 null）。
 *  ⑤ ★★★ 分组 tab 只切**显示**（三个信封同批已拉回），不重新发请求。
 *  ⑥ ★★★ 实时请求与统计分属两个来源（进程内快照 vs DB 日桶），各自独立降级：
 *     queues 挂了统计照常显示，反之亦然。
 *  ⑦ ★★★ Hub 脚注 503 = 未装配 ⇒ 「未启用」中性文案，不是报错。
 *  ⑧ ★★ 桌面全景入口只对 super_admin 渲染（桌面 /live 是 platformOps 面；
 *     admin 档显示一个点了 403 的入口违反「按 role 分档渲染」）。
 *  ⑨ ★★ 轮询在 document.hidden 时跳过（后台标签页不打端点）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const push = vi.fn()
vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return { ...actual, useRouter: () => ({ push }) }
})

vi.mock('@/api/liveTopology', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/liveTopology')>()
  return {
    ...actual,
    fetchProvidersToday: vi.fn(),
    fetchModelsToday: vi.fn(),
    fetchClientsToday: vi.fn(),
  }
})

vi.mock('@/api/liveStream', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/liveStream')>()
  return { ...actual, fetchLiveStreamStats: vi.fn() }
})

vi.mock('@/api/requestJourney', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/requestJourney')>()
  return { ...actual, fetchJourneyQueues: vi.fn() }
})

const mProviders = fetchProvidersToday as unknown as ReturnType<typeof vi.fn>
const mModels = fetchModelsToday as unknown as ReturnType<typeof vi.fn>
const mClients = fetchClientsToday as unknown as ReturnType<typeof vi.fn>
const mHub = fetchLiveStreamStats as unknown as ReturnType<typeof vi.fn>
const mQueue = fetchJourneyQueues as unknown as ReturnType<typeof vi.fn>

// ── 夹具 ──────────────────────────────────────────────────────────────────

const OK_ENVELOPE = {
  day_utc: '2026-10-29',
  rollup_cadence: '1h',
  items: [
    {
      display_name: 'MiniMax',
      requests: 300,
      success: 290,
      failure: 10,
      rate_limited: 0,
      prompt_tokens: 1000,
      completion_tokens: 500,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      credits_charged: 0,
      cost_usd: 1.5,
      share: 0.6,
    },
    {
      display_name: 'Zhipu',
      requests: 200,
      success: 200,
      failure: 0,
      rate_limited: 0,
      prompt_tokens: 1,
      completion_tokens: 1,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      credits_charged: 0,
      cost_usd: 0.5,
      share: 0.4,
    },
  ],
  totals: {
    requests: 500,
    success: 490,
    failure: 10,
    rate_limited: 0,
    prompt_tokens: 1001,
    completion_tokens: 501,
    cache_read_tokens: 0,
    cache_write_tokens: 0,
    total_tokens: 1502,
    credits_charged: 0,
    cost_usd: 2.0,
  },
}

const QUEUE_OK = {
  view: 'total',
  observation_status: 'observation_healthy',
  total_snapshot: {
    pending: 1,
    in_flight: 2,
    completed: 3,
    requests: [
      {
        request_id: 'jr-1',
        current_stage: 'forwarding',
        lifecycle_state: 'in_flight',
        resolved_model: 'glm-5.3-flash',
        updated_at: '2026-10-29T10:00:00Z',
      },
      {
        request_id: 'jr-2',
        current_stage: 'done',
        lifecycle_state: 'completed',
        resolved_model: 'minimax-m3',
        updated_at: '2026-10-29T09:59:00Z',
      },
    ],
  },
}

const HUB_OK = {
  active_clients: 2,
  active_scope_subscriptions: 2,
  total_connections: 10,
  total_disconnections: 8,
  auth_failures: 0,
  broadcast_count: 889,
  broadcast_drops: 0,
  cached_snapshot_baseline_present: 0,
  cached_snapshot_baseline_absent: 0,
  cached_snapshot_empty_skips: 0,
  cached_snapshot_evictions: 0,
  cached_snapshot_entries: 1,
  last_activity: '2026-10-29T10:00:00Z',
  seconds_since_activity: 1,
  lifecycle_actions_delivered: 0,
  lifecycle_action_scan_errors: 0,
  record_drops: 0,
}

function mountView(role = 'super_admin'): ReturnType<typeof mount> {
  const pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore().userInfo = {
    id: 1, tenant_id: 'default', username: 'u', display_name: 'U',
    email: 'e', role, enabled: true,
  }
  return mount(LiveView, { global: { plugins: [pinia] }, attachTo: document.body })
}

let mounted: Array<{ unmount(): void }> = []

beforeEach(() => {
  setLocale('zh-CN')
  for (const w of mounted) {
    try { w.unmount() } catch { /* ignore */ }
  }
  mounted = []
  document.body.innerHTML = ''
  vi.clearAllMocks()
  mProviders.mockResolvedValue(OK_ENVELOPE)
  mModels.mockResolvedValue(OK_ENVELOPE)
  mClients.mockResolvedValue(OK_ENVELOPE)
  mHub.mockResolvedValue(HUB_OK)
  mQueue.mockResolvedValue(QUEUE_OK)
})

afterEach(() => {
  for (const w of mounted) {
    try { w.unmount() } catch { /* ignore */ }
  }
  mounted = []
  document.body.innerHTML = ''
})

describe('live 实时页', () => {
  it('① degraded: true ⇒ 显示「统计不可用」横幅，不渲染总量卡与零行', async () => {
    mProviders.mockResolvedValue({ ...OK_ENVELOPE, degraded: true, items: [], totals: undefined })
    const w = mountView()
    mounted.push(w)
    await flushPromises()
    const banner = w.find('[data-testid="live-watermark"]')
    // degraded 时水位行被横幅替代
    expect(w.text()).toContain('统计聚合表未就绪')
    // 总量卡没有可信分母 ⇒ 不渲染（App 的 grid 依赖 providers.totals）
    expect(w.text()).not.toContain('请求数')
    expect(banner.exists()).toBe(false)
  })

  it('② day 口径显示水位；5m 口径不显示', async () => {
    // ★ 契约形态：短窗口口径响应**不带** watermark 键且 window 回显窗口；
    //   day 口径带 watermark、无 window 键（topology_window_stats.go:24-36）。
    const DAY_ENVELOPE = { ...OK_ENVELOPE, source_high_watermark: '2026-10-29T09:00:00Z' }
    mProviders.mockImplementation((_w: string | undefined) =>
      Promise.resolve(_w ? { ...OK_ENVELOPE, window: _w } : DAY_ENVELOPE))
    mModels.mockImplementation((_w: string | undefined) =>
      Promise.resolve(_w ? { ...OK_ENVELOPE, window: _w } : DAY_ENVELOPE))
    mClients.mockImplementation((_w: string | undefined) =>
      Promise.resolve(_w ? { ...OK_ENVELOPE, window: _w } : DAY_ENVELOPE))
    const w = mountView()
    mounted.push(w)
    await flushPromises()
    // 默认 5m：响应回显 window=5m ⇒ 无水位行
    expect(w.find('[data-testid="live-watermark"]').exists()).toBe(false)
    // 切到「今天」（day）⇒ 带水位参数重拉，水位行出现
    await w.find('[data-testid="live-window-day"]').trigger('click')
    await flushPromises()
    expect(mProviders).toHaveBeenLastCalledWith(undefined, expect.anything())
    expect(w.find('[data-testid="live-watermark"]').exists()).toBe(true)
  })

  it('②b 5m/1h 传 window 参数', async () => {
    const w = mountView()
    mounted.push(w)
    await flushPromises()
    expect(mProviders).toHaveBeenLastCalledWith('5m', expect.anything())
    await w.find('[data-testid="live-window-1h"]').trigger('click')
    await flushPromises()
    expect(mProviders).toHaveBeenLastCalledWith('1h', expect.anything())
  })

  it('③ 区间回显：信封的 window 与请求不一致时显示「实际区间」', async () => {
    // 模拟「发了 5m 请求但后端落回 day 回显」的契约形态
    mProviders.mockResolvedValue({ ...OK_ENVELOPE, window: 'day' })
    mModels.mockResolvedValue({ ...OK_ENVELOPE, window: 'day' })
    mClients.mockResolvedValue({ ...OK_ENVELOPE, window: 'day' })
    const w = mountView()
    mounted.push(w)
    await flushPromises()
    expect(w.find('[data-testid="live-window-echo"]').exists()).toBe(true)
    expect(w.text()).toContain('实际区间：今天')
  })

  it('④ 成功率分母 0 ⇒ 「—」；正常行给百分比（liveStatRowSuccessRate 判据）', () => {
    const row = OK_ENVELOPE.items![0]!
    expect(liveStatRowSuccessRate({ ...row, requests: 0 })).toBeNull()
    expect(liveStatRowSuccessRate(OK_ENVELOPE.items![1]!)).toBe(100)
  })

  it('⑤ 分组 tab 只切显示、不重新发请求', async () => {
    const w = mountView()
    mounted.push(w)
    await flushPromises()
    const calls = mProviders.mock.calls.length
    await w.find('[data-testid="live-tab-models"]').trigger('click')
    await flushPromises()
    expect(mProviders.mock.calls.length).toBe(calls)
    // 模型行走 raw_model_name / canonical_name 展示链
    mModels.mockResolvedValue({
      ...OK_ENVELOPE,
      items: [{ ...OK_ENVELOPE.items![0]!, display_name: '', raw_model_name: 'glm-5.3-flash', canonical_name: '' }],
    })
    // tab 切换不重拉 ⇒ 展示链由 liveStatRowName 直接验证
    expect(liveStatRowName({ ...OK_ENVELOPE.items![0]!, display_name: '', raw_model_name: 'glm-5.3-flash' })).toBe('glm-5.3-flash')
  })

  it('⑥ queues 挂了统计照常显示（两来源独立降级）', async () => {
    mQueue.mockRejectedValue(Object.assign(new Error('boom'), { status: 500 }))
    const w = mountView()
    mounted.push(w)
    await flushPromises()
    expect(w.text()).toContain('实时请求暂不可用')
    // 统计腿正常渲染
    expect(w.text()).toContain('MiniMax')
  })

  it('⑦ Hub 503 = 未装配 ⇒ 中性文案（不是报错）', async () => {
    mHub.mockRejectedValue(Object.assign(new Error('not wired'), { status: 503 }))
    const w = mountView()
    mounted.push(w)
    await flushPromises()
    expect(w.find('[data-testid="live-hub"]').text()).toContain('未启用')
  })

  it('⑧ 桌面全景入口：super_admin 有、tenant_admin 无', async () => {
    const wSuper = mountView('super_admin')
    mounted.push(wSuper)
    await flushPromises()
    expect(wSuper.find('[data-testid="live-open-desktop"]').exists()).toBe(true)
    wSuper.unmount()
    mounted = mounted.filter((x) => x !== wSuper)

    const wTenant = mountView('tenant_admin')
    mounted.push(wTenant)
    await flushPromises()
    expect(wTenant.find('[data-testid="live-open-desktop"]').exists()).toBe(false)
  })

  it('⑨ document.hidden 时轮询跳过（后台不打端点）', async () => {
    vi.useFakeTimers()
    try {
      const w = mountView()
      mounted.push(w)
      await vi.advanceTimersByTimeAsync(0)
      const calls = mProviders.mock.calls.length
      Object.defineProperty(document, 'hidden', { configurable: true, value: true })
      await vi.advanceTimersByTimeAsync(21_000)
      expect(mProviders.mock.calls.length).toBe(calls)
      Object.defineProperty(document, 'hidden', { configurable: true, value: false })
      await vi.advanceTimersByTimeAsync(11_000)
      expect(mProviders.mock.calls.length).toBeGreaterThan(calls)
    } finally {
      vi.useRealTimers()
    }
  })

  it('★ 实时请求行可点进 /journey/:id', async () => {
    const w = mountView()
    mounted.push(w)
    await flushPromises()
    const rows = w.findAll('.live__req')
    expect(rows.length).toBe(2)
    await rows[0]!.trigger('click')
    expect(push).toHaveBeenCalledWith({ path: '/journey/jr-1' })
  })
})
