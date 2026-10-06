import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchAnalyticsClients,
  fetchAnalyticsTasks,
  unwrapAnalyticsClients,
  unwrapAnalyticsTasks,
  analyticsRefreshedAtIsZero,
  analyticsRowsWereDropped,
  analyticsSuccessRate,
  ANALYTICS_LIMIT_DEFAULT,
  ANALYTICS_LIMIT_MAX,
  ANALYTICS_CLIENT_ORDER_BYS,
  ANALYTICS_TASK_ORDER_BYS,
  ANALYTICS_ZERO_TIME,
  type AnalyticsClientSummary,
  type AnalyticsTaskSummary,
} from './sessionAnalytics'

/**
 * 会话分析族（session-analytics）的契约测试（2026-10-08）。
 *
 * ★★ 本文件的夹具**逐字抄自后端 struct 的 json tag 与 writeJSON 的字面量**，
 *   不复用 helper、不为了迁就代码改形状。理由见 `promptInjection`/`outputCompliance`
 *   两次同源事故（夹具照着**错的**契约写 ⇒ 用例全绿 ⇒ 对真后端 100% 抛错）。
 *
 * 后端逐条对应：
 *   admin/session_analytics_clients.go:14-20  ClientAnalyticsListResponse
 *       ⇒ {clients, total, limit, offset, refreshed_at}
 *       :203-209  writeJSON(w, 200, &ClientAnalyticsListResponse{…})
 *   admin/session_analytics_tasks.go:14-20    TaskAnalyticsListResponse
 *       ⇒ {tasks, total, limit, offset, refreshed_at}
 *       :191-197  writeJSON(w, 200, &TaskAnalyticsListResponse{…})
 *   admin/dashboard_session_stats.go:25-32    HealthDistribution（**5 个键**）
 *       ⇒ {a, b, c, d, f}；**没有** total、**没有** *_percent
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

/** 抄自 `HealthDistribution`（admin 包那个，5 个键）。 */
function healthDistribution(over: Record<string, unknown> = {}) {
  return { a: 1, b: 2, c: 3, d: 4, f: 5, ...over }
}

/** 抄自 `ClientAnalyticsSummary`（session_analytics_clients.go:23-38）。 */
function clientRow(over: Record<string, unknown> = {}) {
  return {
    client_id: 'cli-1',
    session_count: 3,
    active_sessions_24h: 1,
    total_requests: 10,
    total_cost_usd: 1.5,
    avg_cost_per_session: 0.5,
    avg_health_score: 88,
    health_distribution: healthDistribution(),
    total_success: 9,
    total_errors: 1,
    avg_latency_ms: 250,
    first_seen_at: '2026-10-01T00:00:00Z',
    last_seen_at: '2026-10-07T00:00:00Z',
    models_used: ['gpt-4o'],
    ...over,
  }
}

/** 抄自 `TaskAnalyticsSummary`（session_analytics_tasks.go:23-39）——比 client 多 clients_used。 */
function taskRow(over: Record<string, unknown> = {}) {
  return {
    ...clientRow(),
    task_id: 'task-1',
    clients_used: ['cli-1'],
    ...over,
  }
}

function clientsBody(over: Record<string, unknown> = {}) {
  return { clients: [clientRow()], total: 1, limit: 50, offset: 0, refreshed_at: '2026-10-07T09:00:00Z', ...over }
}

function tasksBody(over: Record<string, unknown> = {}) {
  return { tasks: [taskRow()], total: 1, limit: 50, offset: 0, refreshed_at: '2026-10-07T09:00:00Z', ...over }
}

/** `noUncheckedIndexedAccess` 开着 ⇒ 取首元素要显式断言非空。 */
function firstClient(r: { clients: AnalyticsClientSummary[] }): AnalyticsClientSummary {
  const first = r.clients[0]
  if (!first) throw new Error('夹具缺第一行')
  return first
}

function firstTask(r: { tasks: Array<AnalyticsTaskSummary> }): AnalyticsTaskSummary {
  const first = r.tasks[0]
  if (!first) throw new Error('夹具缺第一行')
  return first
}

beforeEach(() => {
  // ★ stub 必须放在 beforeEach 里：afterEach 的 unstubAllGlobals 会把它摘掉，
  //   放在模块顶层只有第一条用例能跑到假 fetch（后面全走真 fetch ⇒ URL 解析失败）。
  vi.stubGlobal('fetch', fetchMock)
  vi.clearAllMocks()
  fetchMock.mockResolvedValue(jsonResponse(clientsBody()))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

// ────────────────────────────────────────────────────────────────────────
describe('路径与响应键（逐字对 writeJSON）', () => {
  it('★★★★★★ clients 打 `/session-analytics/clients`，键是 `clients`（:14-20 / :203）', async () => {
    const r = await fetchAnalyticsClients()
    expect(lastUrl()).toContain('/api/admin/session-analytics/clients')
    expect(r.clients).toHaveLength(1)
    expect(r.total).toBe(1)
  })

  it('★★★★★★ tasks 打 `/session-analytics/tasks`，键是 **`tasks`** 不是 `clients`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(tasksBody()))
    const r = await fetchAnalyticsTasks()
    expect(lastUrl()).toContain('/api/admin/session-analytics/tasks')
    expect(r.tasks).toHaveLength(1)
  })

  it('★★★★★★ 把 clients 的形状喂给 tasks ⇒ 必须抛错（键不同名）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(clientsBody()))
    await expect(fetchAnalyticsTasks()).rejects.toThrow(/\{tasks:\[…\]/)
  })

  it('★★★★★★ 把 tasks 的形状喂给 clients ⇒ 必须抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(tasksBody()))
    await expect(fetchAnalyticsClients()).rejects.toThrow(/\{clients:\[…\]/)
  })

  it('★★★★★★ 包一层 `{data:{…}}` / `{result:{…}}` 一律抛错（后端没有这一层）', async () => {
    for (const body of [
      { data: clientsBody() },
      { result: tasksBody() },
    ]) {
      fetchMock.mockResolvedValueOnce(jsonResponse(body))
      await expect(fetchAnalyticsClients()).rejects.toThrow(/形状不符/)
    }
  })

  it('★★★★★★ 顶层是数组/null/字符串 ⇒ 抛错（后端永不含裸数组）', async () => {
    for (const bad of [[], null, 'x']) {
      expect(() => unwrapAnalyticsClients(bad)).toThrow(/形状不符/)
      expect(() => unwrapAnalyticsTasks(bad)).toThrow(/形状不符/)
    }
  })

  it('★★★★★★ 合法的**空**清单必须被接受（真的空，不是错）', async () => {
    expect(unwrapAnalyticsClients(clientsBody({ clients: [] })).clients).toEqual([])
    expect(unwrapAnalyticsTasks(tasksBody({ tasks: [] })).tasks).toEqual([])
  })

  it('★★★★★★ 缺 `total` ⇒ 抛错（列表在但总数不在，形状就不对）', async () => {
    // ★ 这条是 SA5 变异逼出来的：原先只喂了数组/null/字符串，
    //   全都先被 Array.isArray 挡掉，**「total 缺失」这一支从来没被跑到过**
    //   ⇒ 那是「判据没覆盖到分支」，不是「判据无牙」。
    const { total: _dropped, ...noTotal } = clientsBody()
    expect(() => unwrapAnalyticsClients(noTotal)).toThrow(/形状不符/)

    const t = tasksBody()
    delete (t as Record<string, unknown>).total
    expect(() => unwrapAnalyticsTasks(t)).toThrow(/形状不符/)
  })

  it('★ `refreshed_at` 是**必填键**（空列表时也是键，只是值为 Go 零值）', async () => {
    // ★ 后端 struct 的 tag 是 `json:"refreshed_at"` 且**没有** omitempty ⇒ 键一定在。
    const r = unwrapAnalyticsClients(clientsBody({ clients: [], refreshed_at: ANALYTICS_ZERO_TIME }))
    expect(Object.prototype.hasOwnProperty.call(r, 'refreshed_at')).toBe(true)
    expect(r.refreshed_at).toBe(ANALYTICS_ZERO_TIME)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 分页：越界是**回落 50**，不是 clamp', () => {
  it('★★★★★★ limit=201 ⇒ 客户端就回落 50，**不发** 201', async () => {
    await fetchAnalyticsClients({ limit: 201 })
    expect(lastUrl()).toContain('limit=50')
    expect(lastUrl()).not.toContain('201')
  })

  it('★★★★★★ limit=0 / 负数 ⇒ 同样回落 50', async () => {
    for (const bad of [0, -1, -100]) {
      await fetchAnalyticsClients({ limit: bad })
      expect(lastUrl()).toContain('limit=50')
    }
  })

  it('★★★★★★ limit=200 是闭区间上界，发得出去', async () => {
    await fetchAnalyticsClients({ limit: ANALYTICS_LIMIT_MAX })
    expect(lastUrl()).toContain('limit=200')
  })

  it('★★★★★★ limit=1 发得出去（不是被回落成 50）', async () => {
    await fetchAnalyticsClients({ limit: 1 })
    expect(lastUrl()).toContain('limit=1')
  })

  it('★★★★ NaN/Infinity 不发 limit（宁可不发，也不要发会被后端改写的值）', async () => {
    for (const bad of [Number.NaN, Number.POSITIVE_INFINITY]) {
      await fetchAnalyticsClients({ limit: bad })
      expect(lastUrl()).not.toContain('limit=')
    }
  })

  it('★★★★ 非整数 12.9 ⇒ 先截断成 12 再发', async () => {
    await fetchAnalyticsClients({ limit: 12.9 })
    expect(lastUrl()).toContain('limit=12')
  })

  it('★★★ offset>0 才发；offset=0/负数不发', async () => {
    for (const bad of [0, -5]) {
      await fetchAnalyticsClients({ offset: bad })
      expect(lastUrl()).not.toContain('offset=')
    }
    await fetchAnalyticsClients({ offset: 50 })
    expect(lastUrl()).toContain('offset=50')
  })

  it('★ 常量：默认 50 / 上界 200', () => {
    expect(ANALYTICS_LIMIT_DEFAULT).toBe(50)
    expect(ANALYTICS_LIMIT_MAX).toBe(200)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ order_by：三个列表默认值不同，switch 无 default', () => {
  it('★★★★★★ 不传 order_by ⇒ 不发这个参数（让后端用它自己的默认）', async () => {
    // ★ 关键：clients 默认 cost、tasks 默认 sessions。若客户端替后端决定默认值，
    //   两者的排序就会**静默被改成一样**。
    await fetchAnalyticsClients()
    expect(lastUrl()).not.toContain('order_by=')
    fetchMock.mockResolvedValueOnce(jsonResponse(tasksBody()))
    await fetchAnalyticsTasks()
    expect(lastUrl()).not.toContain('order_by=')
  })

  it('★★★★★★ 三个合法值都发得出去', async () => {
    for (const v of ANALYTICS_CLIENT_ORDER_BYS) {
      await fetchAnalyticsClients({ orderBy: v })
      expect(lastUrl()).toContain(`order_by=${v}`)
    }
    for (const v of ANALYTICS_TASK_ORDER_BYS) {
      fetchMock.mockResolvedValueOnce(jsonResponse(tasksBody()))
      await fetchAnalyticsTasks({ orderBy: v })
      expect(lastUrl()).toContain(`order_by=${v}`)
    }
  })

  it('★★★★★★ switch 不认的值（`xxx`）⇒ 不发（后端会静默落回默认且**不报错**）', async () => {
    await fetchAnalyticsClients({ orderBy: 'xxx' as never })
    expect(lastUrl()).not.toContain('order_by=')
  })

  it('★ 常量清单：两边都是 cost/sessions/health 三个', () => {
    expect([...ANALYTICS_CLIENT_ORDER_BYS]).toEqual(['cost', 'sessions', 'health'])
    expect([...ANALYTICS_TASK_ORDER_BYS]).toEqual(['cost', 'sessions', 'health'])
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 租户参数', () => {
  it('★★★★★★ 传 tenant_id ⇒ 原样发（后端会校验跨租户 403）', async () => {
    await fetchAnalyticsClients({ tenantId: 'tenant-a' })
    expect(lastUrl()).toContain('tenant_id=tenant-a')
  })

  it('★★★★ 空白 tenant_id 不发', async () => {
    for (const blank of ['', '   ']) {
      await fetchAnalyticsClients({ tenantId: blank })
      expect(lastUrl()).not.toContain('tenant_id=')
    }
  })

  it('★★★★ tenant_id 会被 trim 后再发', async () => {
    await fetchAnalyticsClients({ tenantId: '  tenant-b  ' })
    expect(lastUrl()).toContain('tenant_id=tenant-b')
    expect(lastUrl()).not.toContain('%20')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 数据源与新鲜度（本族头号问题）', () => {
  it('★★★★★★ `refreshed_at` 是 Go 零值 ⇒ 这一页是空的，且拿不到刷新时间', () => {
    expect(analyticsRefreshedAtIsZero(ANALYTICS_ZERO_TIME)).toBe(true)
    expect(analyticsRefreshedAtIsZero('0001-01-01T00:00:00Z')).toBe(true)
    expect(analyticsRefreshedAtIsZero('')).toBe(true)
    expect(analyticsRefreshedAtIsZero(undefined)).toBe(true)
    expect(analyticsRefreshedAtIsZero(null)).toBe(true)
  })

  it('★★★★★★ 正常时间戳不能被误判成零值', () => {
    expect(analyticsRefreshedAtIsZero('2026-10-07T09:00:00Z')).toBe(false)
    expect(analyticsRefreshedAtIsZero('0002-01-01T00:00:00Z')).toBe(false)
  })

  it('★★★★★★ 显示数 < total 时必须**报出来**（坏行被 continue 丢掉）', () => {
    expect(analyticsRowsWereDropped(3, 10)).toBe(true)
    expect(analyticsRowsWereDropped(10, 10)).toBe(false)
    // ★ 翻到中间页时「本页 50 / 共 10」不该被误报
    expect(analyticsRowsWereDropped(10, 3)).toBe(false)
  })

  it('★ 成功率分母为 0 ⇒ null（页面显示「—」而不是 0%）', () => {
    expect(analyticsSuccessRate({ total_requests: 0, total_success: 0 })).toBeNull()
    expect(analyticsSuccessRate({ total_requests: 10, total_success: 9 })).toBe(90)
  })

  it('★★ 成功率按**总请求数**算，不用 success+errors 当分母', () => {
    // success=9 errors=1 但 total_requests=20 ⇒ 45%，不是 90%
    expect(analyticsSuccessRate({ total_requests: 20, total_success: 9 })).toBe(45)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ health_distribution 只有 5 个键（同名类型陷阱）', () => {
  it('★★★★ `health_distribution` 没有 `total`、没有 `*_percent`', () => {
    // ★ `admin` 包的 HealthDistribution 只有 a/b/c/d/f；
    //   `admin/dashboardapi` 包的**同名**类型才有 total + 百分比。
    const hd = healthDistribution()
    expect(Object.keys(hd).sort()).toEqual(['a', 'b', 'c', 'd', 'f'])
    expect(Object.prototype.hasOwnProperty.call(hd, 'total')).toBe(false)
    expect(Object.prototype.hasOwnProperty.call(hd, 'a_percent')).toBe(false)
  })

  it('★★★★ 行里带着 dashboard 形状的多余键时**照样能收**（后端要多给也不拦）', () => {
    const row = clientRow({ health_distribution: healthDistribution({ total: 15, a_percent: 6.7 }) })
    const r = unwrapAnalyticsClients(clientsBody({ clients: [row as AnalyticsClientSummary] }))
    expect(firstClient(r).health_distribution.a).toBe(1)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ avg_health_score / avg_latency_ms 是 omitempty 指针', () => {
  it('★★★★★ 两个键**可能整个不存在**（不是值为 null）', () => {
    const row = clientRow()
    delete (row as Record<string, unknown>).avg_health_score
    delete (row as Record<string, unknown>).avg_latency_ms
    const r = unwrapAnalyticsClients(clientsBody({ clients: [row as AnalyticsClientSummary] }))
    // ★ 判据是「键不存在」而不是「值为 null」——这两者在 JS 里表现不同。
    expect(Object.prototype.hasOwnProperty.call(firstClient(r), 'avg_health_score')).toBe(false)
    expect(Object.prototype.hasOwnProperty.call(firstClient(r), 'avg_latency_ms')).toBe(false)
  })

  it('★ 存在时照常取值', () => {
    const r = unwrapAnalyticsClients(clientsBody())
    expect(firstClient(r).avg_health_score).toBe(88)
    expect(firstClient(r).avg_latency_ms).toBe(250)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ tasks 比 clients 多一个 clients_used', () => {
  it('★★★ task 行带 `clients_used` 数组', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(tasksBody()))
    const r = await fetchAnalyticsTasks()
    expect(Array.isArray(firstTask(r).clients_used)).toBe(true)
    expect(firstTask(r).clients_used).toEqual(['cli-1'])
  })

  it('★★ task 行有 `task_id`，client 行有 `client_id`（各用自己的主键）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(tasksBody()))
    const t = firstTask(await fetchAnalyticsTasks())
    expect(t.task_id).toBe('task-1')
    expect(clientRow().client_id).toBe('cli-1')
  })
})
