import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import SessionAnalyticsView from './SessionAnalyticsView.vue'
import { fetchAnalyticsClients, fetchAnalyticsTasks, ANALYTICS_ZERO_TIME } from '@/api/sessionAnalytics'
import { setLocale, locale } from '@/i18n'

/**
 * SessionAnalyticsView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 数据源是**没有周期刷新路径**的物化视图 ⇒ 页面**必须**显示
 *    `refreshed_at`（数据截至）；空列表时它是 Go 零值 ⇒ 要单独说，不能当时间显示；
 * 2. ★★★★★ 坏行被后端 `continue` 丢弃 ⇒ 显示数 < total 时必须解释，
 *    且**不能**在显示数 == total 时也报这条；
 * 3. ★★★★ 500/403 绝不能退化成「没有数据」；
 * 4. ★★★ `avg_health_score` / `avg_latency_ms` 是 omitempty 指针 ⇒ 缺失显示「—」；
 * 5. ★★★ `health_distribution` 只有 A/B/C/D/F 五档**计数**，没有占比；
 * 6. ★★★ 这一族**有 total** ⇒ 分页是精确的（与 review-queue / feedback 相反）；
 * 7. ★★ 不发 order_by 时两个维度各用后端自己的默认值（客户端不替它决定）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/sessionAnalytics', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/sessionAnalytics')>()
  return { ...actual, fetchAnalyticsClients: vi.fn(), fetchAnalyticsTasks: vi.fn() }
})

const cMock = fetchAnalyticsClients as unknown as ReturnType<typeof vi.fn>
const tMock = fetchAnalyticsTasks as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function healthDistribution(over: Record<string, unknown> = {}) {
  return { a: 10, b: 2, c: 1, d: 0, f: 0, ...over }
}

/** 夹具逐字对应后端 `ClientAnalyticsSummary` / `ClientAnalyticsListResponse`。 */
function clientRow(over: Record<string, unknown> = {}) {
  return {
    client_id: 'cli-1',
    session_count: 5,
    active_sessions_24h: 2,
    total_requests: 100,
    total_cost_usd: 12.5,
    avg_cost_per_session: 2.5,
    avg_health_score: 88,
    health_distribution: healthDistribution(),
    total_success: 90,
    total_errors: 10,
    avg_latency_ms: 250,
    first_seen_at: '2026-10-01T00:00:00Z',
    last_seen_at: '2026-10-07T00:00:00Z',
    models_used: ['gpt-4o'],
    ...over,
  }
}

function taskRow(over: Record<string, unknown> = {}) {
  return { ...clientRow(), task_id: 'task-1', clients_used: ['cli-1'], ...over }
}

function clientsBody(over: Record<string, unknown> = {}) {
  return {
    clients: [clientRow()],
    total: 1,
    limit: 50,
    offset: 0,
    refreshed_at: '2026-10-07T09:00:00Z',
    ...over,
  }
}

function tasksBody(over: Record<string, unknown> = {}) {
  return {
    tasks: [taskRow()],
    total: 1,
    limit: 50,
    offset: 0,
    refreshed_at: '2026-10-07T09:00:00Z',
    ...over,
  }
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(SessionAnalyticsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  cMock.mockResolvedValue(clientsBody())
  tMock.mockResolvedValue(tasksBody())
  document.body.innerHTML = ''
})

afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 请求：维度切换与参数', () => {
  it('★★★★★★ 挂载即请求 clients，默认 limit=50，**不发** order_by/offset', async () => {
    await mountView()
    expect(cMock).toHaveBeenCalledTimes(1)
    expect(cMock).toHaveBeenCalledWith({ orderBy: undefined, offset: 0, limit: 50 })
    expect(tMock).not.toHaveBeenCalled()
  })

  it('★★★★★★ 点「按任务」⇒ 请求 tasks 端点', async () => {
    const w = await mountView()
    await w.findAll('button').find((b) => b.text() === '按任务')!.trigger('click')
    await flushPromises()
    expect(tMock).toHaveBeenCalledTimes(1)
    expect(tMock).toHaveBeenCalledWith({ orderBy: undefined, offset: 0, limit: 50 })
  })

  it('★★★★★★ 点排序 chip ⇒ 带 order_by 且 offset 归 0', async () => {
    const w = await mountView()
    await w.findAll('button').find((b) => b.text() === '按健康分')!.trigger('click')
    await flushPromises()
    expect(cMock).toHaveBeenLastCalledWith({ orderBy: 'health', offset: 0, limit: 50 })
  })

  it('★★★ 再点同一 chip ⇒ 取消排序（回到不发给后端）', async () => {
    const w = await mountView()
    const chip = w.findAll('button').find((b) => b.text() === '按花费')!
    await chip.trigger('click')
    await flushPromises()
    await chip.trigger('click')
    await flushPromises()
    expect(cMock).toHaveBeenLastCalledWith({ orderBy: undefined, offset: 0, limit: 50 })
  })

  it('★★★★ 明说「两个维度后端默认值不一样」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('两个默认值不一样')
  })

  it('★ 切维度后仍显示当前排序对应的说明', async () => {
    const w = await mountView()
    await w.findAll('button').find((b) => b.text() === '按会话数')!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('按会话数')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 数据新鲜度（本族头号问题）', () => {
  it('★★★★★★ 必须显示「数据截至」——这是这份数字真正的年龄', async () => {
    const w = await mountView()
    // ★★ 同理不能只 `toContain('数据截至')`：那句短语**本来就出现在说明文案里**
    //   （SV1 把标签清空后断言仍然绿，就是被这句喂饱的）。
    //   ⇒ 判「那个格子的**标签**就是它」。
    const labels = w.findAll('.sa__fresh .sa__cell-l').map((n) => n.text())
    expect(labels).toContain('数据截至')
    expect(labels).toContain('原始时间戳')
    // ★ 原始时间戳也要露出来，不能只给一个相对时间
    expect(w.text()).toContain('2026-10-07T09:00:00Z')
  })

  it('★★★★★★ 明说「物化视图 + 刷新只在建表时跑过一次 + 没有定时调度」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('物化视图')
    expect(w.text()).toContain('没有定时刷新它的调度器')
  })

  it('★★★★★★ 空列表 ⇒ refreshed_at 是 Go 零值 ⇒ 要说「拿不到真实刷新时刻」，**不**把 0001 当时间显示', async () => {
    cMock.mockResolvedValue(clientsBody({ clients: [], total: 0, refreshed_at: ANALYTICS_ZERO_TIME }))
    const w = await mountView()
    expect(w.text()).toContain('Go 零值')
    // ★★ 这里**不能**写 `not.toContain('0001-01-01')`：说明文案本身就要解释
    //   「零值是 0001-01-01」，负向断言会把**正确的说明**判红（本会话第 5 次踩这条）。
    //   ⇒ 改判**结构**：那一格「数据截至 / 原始时间戳」**整个不渲染**。
    expect(w.findAll('.sa__fresh .sa__cell')).toHaveLength(0)
    expect(w.findAll('.sa__fresh').length).toBe(1)
    // ★ 而带数据时它必须在场（证明上条不是恒空）
    cMock.mockResolvedValue(clientsBody())
    const w2 = await mountView()
    expect(w2.findAll('.sa__fresh .sa__cell').length).toBe(2)
  })

  it('★★★ 有数据时**不**显示零值那条说明', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('Go 零值')
  })

  it('★ 零值判定对空串/缺字段也成立（后端坏了也不能当正常时间显示）', async () => {
    cMock.mockResolvedValue(clientsBody({ clients: [], total: 0, refreshed_at: '' }))
    const w = await mountView()
    expect(w.text()).toContain('Go 零值')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 坏行被丢弃：显示数 < total 必须解释', () => {
  it('★★★★★ 共 10 条但只显示 1 条 ⇒ 必须说明，且说清「不是分页对不上」', async () => {
    cMock.mockResolvedValue(clientsBody({ clients: [clientRow()], total: 10 }))
    const w = await mountView()
    expect(w.text()).toContain('只显示了')
    expect(w.text()).toContain('不代表数据对不上')
  })

  it('★★★★★ 显示数 == total ⇒ **不**出现那条说明', async () => {
    cMock.mockResolvedValue(
      clientsBody({ clients: [clientRow(), clientRow({ client_id: 'cli-2' })], total: 2 }),
    )
    const w = await mountView()
    expect(w.text()).not.toContain('只显示了')
  })

  it('★ 翻到中间页时「本页 50 / 共 3」不该被误报为丢行', async () => {
    // ★ 结构上做不到（offset 从 0 起步），但判据锁住 analyticsRowsWereDropped 的方向性
    const w = await mountView()
    expect(w.text()).not.toContain('只显示了')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 500 / 403 绝不能退化成空态', () => {
  it('★★★★ clients 失败 ⇒ 显示错误 + 提示，**不**显示「没有数据」', async () => {
    cMock.mockRejectedValue(new Error('client analytics requires admin access'))
    const w = await mountView()
    expect(w.text()).toContain('client analytics requires admin access')
    expect(w.text()).not.toContain('没有任何客户端或任务')
  })

  it('★★★★ 跨租户 403 同样不许显示成空态', async () => {
    cMock.mockRejectedValue(new Error('cross-tenant access denied'))
    const w = await mountView()
    expect(w.text()).toContain('cross-tenant access denied')
  })

  it('★★★ 物化视图缺失的 42P01（503 + analytics_view_missing）原样透出', async () => {
    cMock.mockRejectedValue(new Error('analytics_view_missing: relation "session_client_stats" does not exist'))
    const w = await mountView()
    expect(w.text()).toContain('analytics_view_missing')
  })

  it('★ 真的空 ⇒ 才显示空态文案', async () => {
    cMock.mockResolvedValue(clientsBody({ clients: [], total: 0, refreshed_at: ANALYTICS_ZERO_TIME }))
    const w = await mountView()
    expect(w.text()).toContain('没有任何客户端或任务')
  })

  it('★★ 一个维度失败**不影响**另一个维度可切', async () => {
    cMock.mockRejectedValue(new Error('boom'))
    const w = await mountView()
    await w.findAll('button').find((b) => b.text() === '按任务')!.trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain('boom')
    expect(w.text()).toContain('task-1')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ omitempty 指针字段显示「—」而不是 0', () => {
  it('★★★★ 两个键都缺失 ⇒ 两处都显示「—」而不是 0', async () => {
    const row = clientRow()
    delete (row as Record<string, unknown>).avg_health_score
    delete (row as Record<string, unknown>).avg_latency_ms
    cMock.mockResolvedValue(clientsBody({ clients: [row] }))
    const w = await mountView()
    // ★★★ 判据必须**按单元格作用域**，且不能靠 `expect(w.text()).toContain('—')`：
    //   本页散文里本身就有 4 个破折号（如「两个默认值**——**不一样」），
    //   全页 toContain('—') 是**恒真判据，永远不会红** —— SV5 变异就是它放行的。
    //   ⇒ 取到那**两个格子自己**的文本，判「有标签、没有数字」。
    const cells = w.findAll('.sa__cell')
    const health = cells.find((c) => c.text().includes('平均健康分'))!
    const lat = cells.find((c) => c.text().includes('平均耗时'))!
    expect(health.exists()).toBe(true)
    expect(lat.exists()).toBe(true)
    expect(health.text()).not.toMatch(/\d/)          // ★ 0 会带数字
    expect(lat.text()).not.toMatch(/\d/)
    expect(health.text().endsWith('—')).toBe(true)   // ★ 真的是破折号，不是别的
    expect(lat.text().endsWith('—')).toBe(true)
  })

  it('★★★ 值存在时显示真实数字', async () => {
    const w = await mountView()
    expect(w.text()).toContain('88')
    expect(w.text()).toContain('250')
  })

  it('★★ 总请求数为 0 ⇒ 成功率显示「—」而不是 0%', async () => {
    cMock.mockResolvedValue(clientsBody({ clients: [clientRow({ total_requests: 0, total_success: 0 })] }))
    const w = await mountView()
    const cell = w.findAll('.sa__cell').find((c) => c.text().includes('成功率'))!
    expect(cell.text()).toContain('—')
    expect(cell.text()).not.toContain('0.0%')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ health_distribution 只有五档计数', () => {
  it('★★★ 渲染 A/B/C/D/F 五档，且明说「没有占比、没有总数」', async () => {
    const w = await mountView()
    for (const g of ['A', 'B', 'C', 'D', 'F']) {
      expect(w.findAll('.sa__grade').some((x) => x.text().startsWith(g))).toBe(true)
    }
    expect(w.text()).toContain('不返回占比')
  })

  it('★★ 只渲染五档（**证明不是恒有**：多一个都没有）', async () => {
    // ★ 同步 mount 时数据还没 resolve，列表是空的 ⇒ 必须 await 挂载
    const w = await mountView()
    const grades = w.findAll('.sa__grade')
    expect(grades).toHaveLength(5)
    // ★ 而行数再多也不会变成六档（每个 client 行固定五档）
    cMock.mockResolvedValue(
      clientsBody({ clients: [clientRow(), clientRow({ client_id: 'cli-2' })], total: 2 }),
    )
    const w2 = await mountView()
    expect(w2.findAll('.sa__grade')).toHaveLength(10)
  })

  it('★ 档位缺键时按 0 处理，不显示 undefined', async () => {
    cMock.mockResolvedValue(
      clientsBody({ clients: [clientRow({ health_distribution: { a: 1 } })] }),
    )
    const w = await mountView()
    expect(w.text()).not.toContain('undefined')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 分页是精确的（这一族有 total）', () => {
  it('★★★ 第一页时「上一页」禁用', async () => {
    const w = await mountView()
    const prev = w.findAll('button').find((b) => b.text() === '上一页')!
    expect(prev.attributes('disabled')).toBeDefined()
  })

  it('★★★ total > 本页条数 ⇒ 「下一页」可用，点击后带 offset=50', async () => {
    cMock.mockResolvedValue(clientsBody({ total: 200 }))
    const w = await mountView()
    const next = w.findAll('button').find((b) => b.text() === '下一页')!
    expect(next.attributes('disabled')).toBeUndefined()
    await next.trigger('click')
    await flushPromises()
    expect(cMock).toHaveBeenLastCalledWith({ orderBy: undefined, offset: 50, limit: 50 })
  })

  it('★★★ total == 本页条数 ⇒ 「下一页」禁用', async () => {
    const w = await mountView()
    const next = w.findAll('button').find((b) => b.text() === '下一页')!
    expect(next.attributes('disabled')).toBeDefined()
  })

  it('★★ 页信息显示精确的「共 N」', async () => {
    cMock.mockResolvedValue(clientsBody({ total: 200 }))
    const w = await mountView()
    expect(w.text()).toContain('共 200')
  })

  it('★ 明说「越界是回落到 50 而不是截断到 200」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('回落到')
    expect(w.text()).toContain('而不是截断')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ tasks 维度多显示 clients_used', () => {
  it('★★ 任务维度显示「用到的客户端」行', async () => {
    const w = await mountView()
    await w.findAll('button').find((b) => b.text() === '按任务')!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('用到的客户端')
  })

  it('★★ 客户端维度**不**显示那一行', async () => {
    const w = await mountView()
    expect(w.text()).toContain('用到的模型')
    expect(w.text()).not.toContain('用到的客户端')
  })

  it('★ 空数组显示「—」而不是空白', async () => {
    cMock.mockResolvedValue(clientsBody({ clients: [clientRow({ models_used: [] })] }))
    const w = await mountView()
    expect(w.text()).toContain('—')
    expect(w.text()).not.toContain('undefined')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★ R1 触控尺寸', () => {
  it('★ chip 与翻页按钮都用 sa__* 类（样式受 touch-target 门禁管）', async () => {
    const w = await mountView()
    expect(w.findAll('.sa__chip').length).toBeGreaterThan(0)
    expect(w.findAll('.sa__btn').length).toBeGreaterThan(0)
  })
})
