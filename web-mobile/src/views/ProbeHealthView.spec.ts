import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ProbeHealthView from './ProbeHealthView.vue'
import { fetchProbeSystemHealth, fetchProbeQueueSnapshot } from '@/api/probeHealth'
import { setLocale, locale } from '@/i18n'

/**
 * ProbeHealthView 的五条不变量（2026-10-07）。
 *
 * 这一页的全部价值在于**不读错源**。判据按危险程度排：
 *
 * 1. ★★★ 顶层 legacy 数字（572 行历史积压）**一个字都不许出现在主视图**；
 * 2. ★★★ 活动积压只由 unified 算，且 unified 缺失显示「未知」而不是 0；
 * 3. ★★ `success_rate_last_1h` 缺失显示「没有运行记录」而不是 0%；
 * 4. ★★ legacy 区块默认收起，且 `legacy_mode_safe=false` 要显式警告；
 * 5. ★ 两个端点**分别记错误** —— 一个挂了不等于整页挂了。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
});

vi.mock('@/api/probeHealth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/probeHealth')>()
  return { ...actual, fetchProbeSystemHealth: vi.fn(), fetchProbeQueueSnapshot: vi.fn() };
});

const healthMock = fetchProbeSystemHealth as unknown as ReturnType<typeof vi.fn>
const queueMock = fetchProbeQueueSnapshot as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(ProbeHealthView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  await flushPromises()
  return w
}

const UNIFIED_HEALTH = {
  total_credentials: 120,
  credentials_with_ursm: 115,
  credentials_no_ursm: 5,
  ursm_key_count: 118,
  queue_pending: 12,
  queue_in_flight: 3,
  queue_completed: 40,
  queue_failed: 2,
  queue_expired: 0,
  queue_total: 57,
  node_total: 30,
  node_healthy: 26,
  node_failing: 2,
  node_paused: 1,
  node_running: 1,
  node_due_now: 4,
  node_leased: 1,
  runs_last_1h: 18,
  runs_success_1h: 17,
  runs_failed_1h: 1,
  runs_last_at: '2026-10-07T10:00:00Z',
  success_rate_last_1h: 0.944,
  pseudo_success_count: 2,
  legacy: {
    total_nodes: 900,
    healthy_nodes: 880,
    failing_nodes: 20,
    suspicious_nodes: 0,
    probing_nodes: 0,
    urgent_queue_size: 572,
    ready_probes: 900,
    current_probing: 0,
    legacy_source: 'model_probe_state',
    legacy_mode_safe: false,
  },
  snapshot_at: '2026-10-07T10:05:00Z',
}

const UNIFIED_QUEUE = {
  queue_ready: 12,
  queue_running: 3,
  queue_finished: 40,
  queue_claims: 1,
  node_pending: 6,
  node_running: 1,
  node_paused: 1,
  node_due: 4,
  node_unclaimable: 2,
  stale_leases: 1,
  last_run_at: '2026-10-07T10:00:00Z',
  queue_size: 15,
  snapshot_at: '2026-10-07T10:05:00Z',
}

/** 与后端实际形状一致：顶层摊平着 legacy 字段。 */
const HEALTH_SHAPE = {
  // ↓ legacy 摊平在顶层（后端为兼容老客户端）
  total_nodes: 900,
  healthy_nodes: 880,
  urgent_queue_size: 572,
  ready_probes: 900,
  current_probing: 0,
  unified: UNIFIED_HEALTH,
  legacy: UNIFIED_HEALTH.legacy,
  legacy_mode_safe: false,
  snapshot_at: '2026-10-07T10:05:00Z',
}

const QUEUE_SHAPE = {
  unified: UNIFIED_QUEUE,
  // ↓ 顶层 queues/total 也是 legacy 的（含 572 行历史积压）
  queues: [
    { probe_priority: 'P1', state: 'pending', queue_size: 572, ready_now: 572, ready_1min: 0, ready_5min: 0 },
  ],
  total: 572,
  legacy: {
    queues: [
      { probe_priority: 'P1', state: 'pending', queue_size: 572, ready_now: 572, ready_1min: 0, ready_5min: 0 },
    ],
    total: 572,
    legacy: true,
    legacy_mode_safe: false,
    legacy_source: 'model_probe_state',
  },
  snapshot_at: '2026-10-07T10:05:00Z',
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  healthMock.mockResolvedValue(HEALTH_SHAPE)
  queueMock.mockResolvedValue(QUEUE_SHAPE)
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('入口：两个端点各发一次', () => {
  it('挂载后两个端点各调一次', async () => {
    await mountView()
    expect(healthMock).toHaveBeenCalledTimes(1)
    expect(queueMock).toHaveBeenCalledTimes(1)
  })

  it('403 ⇒ 报「没有权限」', async () => {
    healthMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    queueMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    expect(w.text()).toContain('没有查看探测系统的权限')
  })
})

describe('★★★ 判据 1：legacy 的 572 绝不出现在主视图', () => {
  it('★ 主视图里没有 572（它是历史积压，不是当前积压）', async () => {
    const w = await mountView()
    // 572 在响应里（HEALTH_SHAPE.urgent_queue_size / QUEUE_SHAPE.total）
    expect(HEALTH_SHAPE.urgent_queue_size).toBe(572)
    expect(QUEUE_SHAPE.total).toBe(572)
    // legacy 区块默认收起 ⇒ 主视图里不该出现
    expect(w.text()).not.toContain('572')
  })

  it('★ 活动积压显示 25（12+3+6+4，由 unified 算）而不是 572', async () => {
    const w = await mountView()
    const hero = w.findAll('.ph__hero-value')[0]!
    expect(hero.text()).toBe('25')
  })

  it('★ 展开 legacy 后才看得到 572，且带「已不权威」标注', async () => {
    const w = await mountView()
    await w.find('.ph__legacy-toggle').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('572')
    expect(w.text()).toContain('已不权威')
  })
})

describe('★★★ 判据 2：unified 缺失显示「未知」而不是 0', () => {
  it('★ queue 的 unified 缺失 ⇒ 活动积压显示未知，不是 0', async () => {
    queueMock.mockResolvedValue({ queues: [], total: 572, legacy: QUEUE_SHAPE.legacy })
    const w = await mountView()
    // 队列卡片整个不渲染（没有 unified 就没有当前状况）
    expect(w.text()).not.toContain('活动积压')
  })

  it('两个 unified 都缺失 ⇒ 空态', async () => {
    healthMock.mockResolvedValue({})
    queueMock.mockResolvedValue({})
    const w = await mountView()
    expect(w.text()).toContain('拿不到探测系统状态')
  })
})

describe('★★ 判据 3：success_rate_last_1h 三态', () => {
  it('有值 ⇒ 显示百分比', async () => {
    const w = await mountView()
    expect(w.text()).toContain('94.4%')
  })

  // ★ Go 侧 `*float64` + omitempty ⇒ 字段整个不存在
  it('★ 字段缺失 ⇒ 显示「这一小时没有运行记录」，不是 0.0%', async () => {
    const noField = { ...UNIFIED_HEALTH }
    delete (noField as Record<string, unknown>).success_rate_last_1h
    healthMock.mockResolvedValue({ ...HEALTH_SHAPE, unified: noField })
    const w = await mountView()
    expect(w.text()).toContain('这一小时没有运行记录')
    expect(w.text()).not.toContain('0.0%')
  })

  it('★ 真的是 0（跑了且全失败）⇒ 显示 0.0%，不显示「没有运行记录」', async () => {
    healthMock.mockResolvedValue({ ...HEALTH_SHAPE, unified: { ...UNIFIED_HEALTH, success_rate_last_1h: 0 } })
    const w = await mountView()
    expect(w.text()).toContain('0.0%')
    expect(w.text()).not.toContain('这一小时没有运行记录')
  })
})

describe('★★ 判据 4：legacy 区块默认收起 + 显式警告', () => {
  it('★ 默认收起（aria-expanded=false）', async () => {
    const w = await mountView()
    expect(w.find('.ph__legacy-toggle').attributes('aria-expanded')).toBe('false')
  })

  // ★ legacy_mode_safe=false 是后端的显式警告，必须说清「不代表当前状况」
  it('★ legacy_mode_safe=false ⇒ 显示「不权威」警告', async () => {
    const w = await mountView()
    expect(w.text()).toContain('不权威')
    expect(w.text()).toContain('不代表当前状况')
  })

  // ★★ system-health 的 legacy 查询**软失败**（只 slog.Warn，响应仍 200，
  //   legacy 保持零值），而且后端注释声称会回报错误、代码并没有。
  //   ⇒ legacy 全 0 时无法区分「真 0」与「没加载」。
  //   ★ 这条测的是**视图消费 legacySectionMayBeUnloaded 的那条分支** ——
  //   API 层测了 helper 本身，但没人量过视图拿到 true 时显示什么。
  it('★ legacy 全 0 ⇒ 显示「可能未加载」，不显示那四个 0', async () => {
    healthMock.mockResolvedValue({
      ...HEALTH_SHAPE,
      legacy: {
        total_nodes: 0,
        healthy_nodes: 0,
        failing_nodes: 0,
        suspicious_nodes: 0,
        probing_nodes: 0,
        urgent_queue_size: 0,
        ready_probes: 0,
        current_probing: 0,
        legacy_source: 'model_probe_state',
        legacy_mode_safe: false,
      },
      unified: { ...UNIFIED_HEALTH, legacy: { total_nodes: 0, healthy_nodes: 0, failing_nodes: 0, suspicious_nodes: 0, probing_nodes: 0, urgent_queue_size: 0, ready_probes: 0, current_probing: 0, legacy_source: 'model_probe_state', legacy_mode_safe: false } },
    })
    const w = await mountView()
    await w.find('.ph__legacy-toggle').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('无法判断')
    // ★ 四个 0 一个都不许渲染成统计值
    expect(w.findAll('.ph__cell').every((c) => c.find('dd').text() !== '0')).toBe(true)
  })

  // ★ 反向锁定：legacy 有值时不显示「可能未加载」（证明上一条不是恒真）
  it('★ legacy 有值 ⇒ 不显示「可能未加载」', async () => {
    const w = await mountView()
    await w.find('.ph__legacy-toggle').trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain('无法判断')
    expect(w.text()).toContain('900')
  })

  it('★ 展开后可见 legacy 明细行', async () => {
    const w = await mountView()
    await w.find('.ph__legacy-toggle').trigger('click')
    await flushPromises()
    expect(w.findAll('.ph__lrow').length).toBe(1)
    expect(w.text()).toContain('model_probe_state')
  })
})

describe('★ 判据 5：两个端点分别记错误', () => {
  // ★ 合并成一个 error 会把「一段挂了」显示成「整页都挂了」
  it('queue-snapshot 挂了、system-health 正常 ⇒ 只报队列这一段，且健康数据仍在', async () => {
    queueMock.mockRejectedValue(new Error('boom'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('队列')
    expect(text).toContain('boom')
    // ★ system-health 的 unified 仍然渲染出来了。
    //   115 是 credentials_with_ursm，视图渲染的是覆盖率 115/120 = 95.8%，
    //   以及 credentials_no_ursm = 5 —— 断言要对准**实际渲染的量**。
    expect(text).toContain('URSM 可路由覆盖率')
    expect(text).toContain('95.8%')
    expect(text).toContain('缺 URSM 键5')
  })

  it('system-health 挂了、queue 正常 ⇒ 只报健康这一段，队列数据仍在', async () => {
    healthMock.mockRejectedValue(new Error('kaput'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('系统健康')
    expect(text).toContain('kaput')
    // 队列的 unified 仍然渲染
    expect(text).toContain('活动积压')
  })
})

describe('租约异常三项', () => {
  it('★ 三项都渲染，且异常项标红', async () => {
    const w = await mountView()
    const items = w.findAll('.ph__anom-item')
    expect(items.length).toBe(3)
    // node_unclaimable=2, stale_leases=1, queue_expired=0
    expect(items[0]!.find('dd').text()).toBe('2')
    expect(items[1]!.find('dd').text()).toBe('1')
    expect(items[2]!.find('dd').text()).toBe('0')
    expect(items[0]!.find('dd').classes()).toContain('ph__bad')
    expect(items[2]!.find('dd').classes()).not.toContain('ph__bad')
  })

  it('★ 三项全 0 ⇒ 不标红、异常区不报警（证明上一条不是恒真）', async () => {
    queueMock.mockResolvedValue({ ...QUEUE_SHAPE, unified: { ...UNIFIED_QUEUE, node_unclaimable: 0, stale_leases: 0 } })
    healthMock.mockResolvedValue({ ...HEALTH_SHAPE, unified: { ...UNIFIED_HEALTH, queue_expired: 0 } })
    const w = await mountView()
    expect(w.find('.ph__anom--bad').exists()).toBe(false)
  })
})
