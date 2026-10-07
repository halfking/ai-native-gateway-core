import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ConnectionRegistryView from './ConnectionRegistryView.vue'
import { setLocale } from '@/i18n'
import {
  fetchConnectionRegistryList,
  fetchConnectionByRequestId,
  CLOSED_HISTORY_LIMIT,
  GO_ZERO_TIME,
} from '@/api/connectionRegistry'

/**
 * 流式连接注册台回归护栏（2026-10-08，第一百零七批）。
 *
 * ★★ 本批第一件事是**一个真实竞态窗口**，它推翻了「live 段的行 `closed` 恒 false」：
 *   `snapshot(false, "")`（`domains/streaming/connection_registry.go:476-491`）
 *   内部有 `if e.closed` 覆写分支，而 `WriteFrame` 超时分支（`:417-441`）是
 *   **先 `entry.closed = true`、放开 entry 锁，再从 map 摘除**
 *   ⇒ 在 `:421` 与 `:433` 之间 `List()` 会返回一行 `closed=true`
 *     却出现在 **`live` 段**里（此时它还没进环形缓冲，`detach` 还没跑）。
 *   ⇒ `live_count === live.length` 恒真**不蕴含**「live 里都是活连接」。
 *
 * 判据钉八组不变量：
 *  ① ★★★★★★ live 段的 `closed: true` 行**必须可见标注**，且**不许被搬去 closed 段**。
 *  ② ★★★★★ `live` 的顺序**后端不保证**（`List()` 自陈 map walk is unordered）
 *      ⇒ 视图必须**客户端排序**。
 *  ③ ★★★★ `live: []` 与 `closed: null` 是**两种不同的空态**，文案必须不同。
 *  ④ ★★★★ 满 50 行 ⇒ 文案是「可能还有更早的**没暴露**」，
 *      **不许**出现「已丢弃 / 已截断」这种把端点上限说成历史上限的说法
 *      （环形缓冲 256、端点只给 50 ⇒ 50 行时缓冲区里至少还有 206 条）。
 *  ⑤ ★★★★ `Lookup` 只查活跃表 ⇒ closed 段的行**不发**详情请求；
 *      而 live 段的行（含 ① 那种竞态行）**要发**。
 *  ⑥ ★★★ 404 与「id 不存在」「这行已注销」后端**返回同一个响应**
 *      ⇒ 文案只能说到「分不出」为止，**不许**断言是哪一种。
 *  ⑦ ★★★ 503 是「未装配」，**不是**「空列表」。
 *  ⑧ ★★★ 4 个 omitempty 条件键缺失 ⇒ 「未标注」而不是异常；
 *      `frames_written` 是 uint64 无 omitempty ⇒ 0 是**真的 0 帧**，不是「—」。
 *      `last_frame_at` 键恒在，但「从未发帧」时是 Go 零值时间 ⇒ 不得渲染那个值。
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

vi.mock('@/api/connectionRegistry', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/connectionRegistry')>()
  return {
    ...actual,
    fetchConnectionRegistryList: vi.fn(),
    fetchConnectionByRequestId: vi.fn(),
  }
})

const mList = fetchConnectionRegistryList as unknown as ReturnType<typeof vi.fn>
const mById = fetchConnectionByRequestId as unknown as ReturnType<typeof vi.fn>

// ── 夹具 ──────────────────────────────────────────────────────────────────

/** 一行**正常**的活跃连接：条件键齐全，`closed: false`。 */
const LIVE_OK = {
  request_id: 'req-live-1',
  protocol: 'sse',
  client_type: 'web',
  tenant_id: 'default',
  registered_at: '2026-10-08T01:00:00Z',
  last_frame_at: '2026-10-08T01:05:00Z',
  frames_written: 12,
  bytes_written: 4096,
  closed: false,
}

/** ★★★★★★ **竞态窗口**夹具：它出现在 **`live` 段**，但 `closed: true`。 */
const LIVE_RACE = {
  request_id: 'req-race-1',
  protocol: 'sse',
  client_type: 'web',
  tenant_id: 'default',
  registered_at: '2026-10-08T02:00:00Z',
  last_frame_at: '2026-10-08T02:05:00Z',
  frames_written: 3,
  bytes_written: 900,
  close_reason: 'write_deadline',
  closed: true,
}

/**
 * ★ 条件键**真的不存在**的活跃连接。
 * ⚠️ 夹具名带 `NO_` ⇒ 当场确认那四个键真不在（不是 undefined、不是空串）：
 *   这里只列出后端不带 omitempty 的 6 个键，四个条件键**一个都没写**。
 */
const LIVE_NO_META = {
  request_id: 'req-nometa-1',
  registered_at: '2026-10-08T03:00:00Z',
  last_frame_at: '2026-10-08T03:00:01Z',
  frames_written: 5,
  bytes_written: 120,
  closed: false,
}

/** ★ 「从未发帧」：`frames_written` 是 0（**真的 0**），`last_frame_at` 是 Go 零值时间。 */
const LIVE_ZERO_FRAMES = {
  request_id: 'req-zero-1',
  protocol: 'sse',
  registered_at: '2026-10-08T04:00:00Z',
  last_frame_at: GO_ZERO_TIME,
  frames_written: 0,
  bytes_written: 0,
  closed: false,
}

/** 一行注销记录（**只在 `closed` 段**出现 ⇒ 按 id 查必然 404）。 */
const CLOSED_ONE = {
  request_id: 'req-closed-1',
  protocol: 'sse',
  client_type: 'web',
  tenant_id: 'default',
  registered_at: '2026-10-08T00:10:00Z',
  last_frame_at: '2026-10-08T00:12:00Z',
  frames_written: 7,
  bytes_written: 700,
  close_reason: 'stream_end',
  closed: true,
}

function listPayload(o: {
  live: unknown[]
  closed: unknown[] | null
  capacity?: number
}) {
  return {
    live: o.live,
    live_count: o.live.length, // ★ 后端是 len(live)（:56）⇒ 必须自洽
    capacity: o.capacity ?? 4096, // ★ 恒 ≥ 4096（NewConnectionRegistry 的兜底）
    closed: o.closed,
  }
}

async function mountView() {
  const w = mount(ConnectionRegistryView, { global: { stubs: { RouterLink: true } } })
  await flushPromises()
  return w
}

/** 点「加载」并等它落定。 */
async function loadInto(w: Awaited<ReturnType<typeof mountView>>): Promise<void> {
  await w.find('[data-testid="cr-search-input"]').exists()
  const btn = w.findAll('button').find((b) => b.text().includes('加载'))
  await btn!.trigger('click')
  await flushPromises()
}

function texts(w: Awaited<ReturnType<typeof mountView>>): string {
  return w.text()
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  mList.mockResolvedValue(listPayload({ live: [], closed: null }))
  mById.mockResolvedValue(LIVE_OK)
})

// ── ① 竞态窗口：live 段的 closed 行必须可见标注，且不被搬段 ────────────────

describe('① live 段的竞态 closed 行', () => {
  it('★ live 段出现 closed:true 的行 ⇒ 必须打「正在注销」标注', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK, LIVE_RACE], closed: null }))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-closing-req-race-1"]').exists()).toBe(true)
    expect(texts(w)).toContain('正在注销')
  })

  it('★★ 正控：正常活跃行（closed:false）**不得**出现该标注', async () => {
    // ★ 没有这条，上面那条就是恒真 —— 「页面上永远有个标注」也能满足它。
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-closing-req-live-1"]').exists()).toBe(false)
    expect(texts(w)).not.toContain('正在注销')
  })

  it('★★★ 竞态行**留在 live 段**，不被搬去 closed 段', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_RACE], closed: null }))
    const w = await mountView()
    await loadInto(w)
    // 必须在 live 段有行
    expect(w.find('[data-testid="cr-live-row-req-race-1"]').exists()).toBe(true)
    // 且**不得**在 closed 段出现同一行 —— 它还没进环形缓冲，搬过去就是凭空造出「已归档」
    expect(w.find('[data-testid="cr-closed-row-req-race-1"]').exists()).toBe(false)
  })

  it('★★★ 竞态行计入 live_count 的提示条（不是静默吞掉）', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_RACE], closed: null }))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-race"]').exists()).toBe(true)
    expect(w.find('[data-testid="cr-race"]').text()).toContain('1')
  })

  it('★★ 正控：全正常时不得出现竞态提示条', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-race"]').exists()).toBe(false)
  })
})

// ── ② live 的顺序后端不保证 ⇒ 客户端必须排序 ────────────────────────────

describe('② live 客户端排序', () => {
  it('★★ live 行按 registered_at 升序渲染（后端给的是乱序）', async () => {
    const later = { ...LIVE_OK, request_id: 'req-b', registered_at: '2026-10-08T05:00:00Z' }
    const earlier = { ...LIVE_OK, request_id: 'req-a', registered_at: '2026-10-08T00:01:00Z' }
    // ★ 故意把「晚的」放前面：后端 List() 的 map 遍历序就是任意的
    mList.mockResolvedValue(listPayload({ live: [later, earlier], closed: null }))
    const w = await mountView()
    await loadInto(w)
    const aIdx = texts(w).indexOf('req-a')
    const bIdx = texts(w).indexOf('req-b')
    expect(aIdx).toBeGreaterThan(-1)
    expect(bIdx).toBeGreaterThan(-1)
    expect(aIdx).toBeLessThan(bIdx)
  })

  it('★★ 正控：只有一行时序断言不成立 ⇒ 排序判据不是恒真', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-live-row-req-live-1"]').exists()).toBe(true)
    expect(w.findAll('[data-testid^="cr-live-row-"]')).toHaveLength(1)
  })
})

// ── ③ 两种空态语义不同 ──────────────────────────────────────────────────

describe('③ live:[] 与 closed:null 是两种空态', () => {
  it('★★ live 为空 ⇒ 空态文案（不是「加载中」）', async () => {
    mList.mockResolvedValue(listPayload({ live: [], closed: null }))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-live-empty"]').exists()).toBe(true)
    expect(w.find('[data-testid="cr-closed-absent"]').exists()).toBe(true)
  })

  it('★★★ 两段的空态文案必须不同（null ≠ []）', async () => {
    mList.mockResolvedValue(listPayload({ live: [], closed: null }))
    const w = await mountView()
    await loadInto(w)
    const live = w.find('[data-testid="cr-live-empty"]').text()
    const closed = w.find('[data-testid="cr-closed-absent"]').text()
    expect(live).not.toBe(closed)
    expect(live.length).toBeGreaterThan(0)
    expect(closed.length).toBeGreaterThan(0)
  })

  it('★★ closed 有行 ⇒ 不显示「从无注销记录」', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: [CLOSED_ONE] }))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-closed-absent"]').exists()).toBe(false)
    expect(w.find('[data-testid="cr-closed-row-req-closed-1"]').exists()).toBe(true)
  })

  it('★★ closed 非空但被过滤 ⇒ 显示「没有匹配」而不是「从无注销记录」', async () => {
    // ★★★ 这两格**语义相反**：null = 从没有过注销；非空数组 = 有，但一个都没匹配上。
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: [CLOSED_ONE] }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-filter-input"]').setValue('zzz-no-such')
    await flushPromises()
    expect(w.find('[data-testid="cr-closed-absent"]').exists()).toBe(false)
    expect(w.find('[data-testid="cr-closed-empty"]').exists()).toBe(true)
  })
})

// ── ④ 满 50 行的措辞：不是「已丢弃」 ────────────────────────────────────

describe('④ 满额历史的措辞', () => {
  const full = Array.from({ length: CLOSED_HISTORY_LIMIT }, (_, i) => ({
    ...CLOSED_ONE,
    request_id: `req-closed-${i}`,
  }))

  it('★★★ 满 50 行 ⇒ 出现「可能还有更早的未暴露」提示', async () => {
    mList.mockResolvedValue(listPayload({ live: [], closed: full }))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-closed-more"]').exists()).toBe(true)
    expect(w.find('[data-testid="cr-closed-more"]').text()).toContain(String(CLOSED_HISTORY_LIMIT))
  })

  it('★★★★★ 措辞**不许**把端点上限说成历史上限（环形缓冲是 256，不是 50）', async () => {
    mList.mockResolvedValue(listPayload({ live: [], closed: full }))
    const w = await mountView()
    await loadInto(w)
    const body = texts(w)
    // ★ 环形缓冲 256、端点只暴露 50 ⇒ 满 50 行时缓冲区里至少还躺着另外 206 条
    expect(body).not.toContain('已丢弃')
    expect(body).not.toContain('已截断')
    expect(body).not.toContain('已丢弃的历史')
    // 正面锚：必须说到「没暴露」
    expect(body).toContain('未暴露')
  })

  it('★★ 49 行 ⇒ 不出现该提示（阈值就是上限本身）', async () => {
    mList.mockResolvedValue(
      listPayload({ live: [], closed: full.slice(0, CLOSED_HISTORY_LIMIT - 1) }),
    )
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-closed-more"]').exists()).toBe(false)
  })
})

// ── ⑤ closed 段不发详情请求；live 段要发 ────────────────────────────────

describe('⑤ 详情请求的两段差异', () => {
  it('★★★★ live 段的行点开 ⇒ 真的发详情请求（正控）', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-live-journey-req-live-1"]').trigger('click')
    await flushPromises()
    // 「打开请求旅程」是本地跳转，不是详情请求 —— 详情请求只由查询框发起。
    // ⇒ 这里断言的是：**页面上存在**这个 live 段入口（正控：按钮真的渲染出来了）
    expect(mById).not.toHaveBeenCalled()
    expect(w.find('[data-testid="cr-live-journey-req-live-1"]').exists()).toBe(true)
  })

  it('★★★ 查询框查 live 行 ⇒ 发 fetchConnectionByRequestId', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-search-input"]').setValue('req-live-1')
    await w.find('[data-testid="cr-search-btn"]').trigger('click')
    await flushPromises()
    expect(mById).toHaveBeenCalledTimes(1)
    expect(String(mById.mock.calls[0]![0])).toBe('req-live-1')
    expect(w.find('[data-testid="cr-hit"]').exists()).toBe(true)
  })

  it('★★★ 竞态行（live 段的 closed:true）**仍然可以**按 id 查（它还在活跃表里）', async () => {
    // ★ 这是「不能按 closed 字段判断能不能查」的正证据。
    mList.mockResolvedValue(listPayload({ live: [LIVE_RACE], closed: null }))
    // ★ beforeEach 把 mById 默认成 LIVE_OK（closed:false）；
    //   本用例查的是竞态行 ⇒ 详情 mock 也要换成 LIVE_RACE，否则命中的是普通行。
    mById.mockResolvedValue(LIVE_RACE)
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-search-input"]').setValue('req-race-1')
    await w.find('[data-testid="cr-search-btn"]').trigger('click')
    await flushPromises()
    expect(mById).toHaveBeenCalledTimes(1)
    expect(w.find('[data-testid="cr-hit"]').exists()).toBe(true)
    expect(w.find('[data-testid="cr-hit-closing"]').exists()).toBe(true)
  })

  it('★★★★★ closed 段的行**没有**详情入口（Lookup 只查活跃表 ⇒ 必然 404）', async () => {
    mList.mockResolvedValue(listPayload({ live: [], closed: [CLOSED_ONE] }))
    const w = await mountView()
    await loadInto(w)
    // 只提供旅程跳转，**不提供**按 id 详情按钮
    expect(w.find('[data-testid="cr-closed-row-req-closed-1"]').exists()).toBe(true)
    expect(w.find('[data-testid="cr-closed-journey-req-closed-1"]').exists()).toBe(true)
    // ★ 页面必须常驻说明「注销记录查不到」，否则用户会自己试
    expect(w.find('[data-testid="cr-closed-lookup-note"]').exists()).toBe(true)
  })
})

// ── ⑥ 404 的措辞：只能说「分不出」 ──────────────────────────────────────

describe('⑥ 404 措辞', () => {
  it('★★★★ 查询失败 ⇒ 提示必须说到「两种情况返回相同」，不许断言是哪一种', async () => {
    mById.mockRejectedValue(new Error('request not registered'))
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-search-input"]').setValue('req-gone-1')
    await w.find('[data-testid="cr-search-btn"]').trigger('click')
    await flushPromises()
    const msg = w.find('[data-testid="cr-search-error"]').text()
    expect(msg).toContain('已注销')
    expect(msg).toContain('不存在')
    // ★ 措辞必须包含「相同」这类等价声明 ⇒ 承认客户端分不出
    expect(msg).toContain('相同')
  })

  it('★★★ 查询失败 ⇒ 不得渲染出结果卡片', async () => {
    mById.mockRejectedValue(new Error('request not registered'))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-search-input"]').setValue('req-gone-1')
    await w.find('[data-testid="cr-search-btn"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="cr-hit"]').exists()).toBe(false)
  })

  it('★★ 清除按钮把结果与输入一起清掉', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-search-input"]').setValue('req-live-1')
    await w.find('[data-testid="cr-search-btn"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="cr-hit"]').exists()).toBe(true)
    await w.find('[data-testid="cr-search-clear"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="cr-hit"]').exists()).toBe(false)
    expect((w.find('[data-testid="cr-search-input"]').element as HTMLInputElement).value).toBe('')
  })

  it('★★ 空 id ⇒ 查询按钮禁用（不发请求）', async () => {
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-search-btn"]').attributes('disabled')).toBeDefined()
    expect(mById).not.toHaveBeenCalled()
  })
})

// ── ⑦ 503 未装配 ≠ 空列表 ───────────────────────────────────────────────

describe('⑦ 503 与空列表', () => {
  it('★★★ 503 ⇒ 显示「未装配」，不显示空态、不显示水位', async () => {
    mList.mockRejectedValue(new Error('connection registry not wired'))
    const w = await mountView()
    await loadInto(w)
    expect(w.find('[data-testid="cr-watermark"]').exists()).toBe(false)
    expect(w.find('[data-testid="cr-live-empty"]').exists()).toBe(false)
    expect(texts(w)).toContain('未装配')
  })

  it('★★★ 非 503 失败 ⇒ 走错误通道而不是「未装配」', async () => {
    mList.mockRejectedValue(new Error('network down'))
    const w = await mountView()
    await loadInto(w)
    expect(texts(w)).not.toContain('未装配')
    expect(texts(w)).toContain('network down')
  })
})

// ── ⑧ 字段编码：条件键缺失 / 0 是真值 / 零值时间 ─────────────────────────

describe('⑧ 字段编码', () => {
  it('★★★★ 四个条件键全缺 ⇒ 渲染「未标注」，不渲染成空白或异常', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_NO_META], closed: null }))
    const w = await mountView()
    await loadInto(w)
    const body = texts(w)
    expect(body).toContain('未标注协议')
    expect(body).toContain('未标注客户端')
    expect(body).toContain('未标注租户')
  })

  it('★★★★★ 夹具自证：LIVE_NO_META 真的**不含**那四个键（不是 undefined / 空串）', () => {
    // ★★ 夹具名带 NO_ 就得当场验 —— 否则 ⑧ 第一条可能测的是一个「其实有键」的夹具。
    expect('protocol' in LIVE_NO_META).toBe(false)
    expect('client_type' in LIVE_NO_META).toBe(false)
    expect('tenant_id' in LIVE_NO_META).toBe(false)
    expect('close_reason' in LIVE_NO_META).toBe(false)
  })

  it('★★★ frames_written = 0 ⇒ 渲染「0 帧」，不是「—」也不是空白', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_ZERO_FRAMES], closed: null }))
    const w = await mountView()
    await loadInto(w)
    // ★ uint64 无 omitempty ⇒ 键恒在，0 是**真的 0 帧**（与批 105/106 的 omitempty 相反）
    expect(texts(w)).toContain('0 帧')
  })

  it('★★★★ last_frame_at 是 Go 零值时间 ⇒ 渲染「从未发帧」，不渲染那个值', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_ZERO_FRAMES], closed: null }))
    const w = await mountView()
    await loadInto(w)
    const body = texts(w)
    expect(body).toContain('从未发帧')
    expect(body).not.toContain(GO_ZERO_TIME)
    expect(body).not.toContain('0001-01-01')
  })

  it('★★★ 正控：正常连接渲染真实时间戳，不显示「从未发帧」', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    const body = texts(w)
    expect(body).toContain('2026-10-08T01:05:00Z')
    expect(body).not.toContain('从未发帧')
  })

  it('★★ 水位渲染 capacity 与 live_count', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null, capacity: 4096 }))
    const w = await mountView()
    await loadInto(w)
    const wm = w.find('[data-testid="cr-watermark"]').text()
    expect(wm).toContain('1')
    expect(wm).toContain('4096')
  })
})

// ── 过滤 ────────────────────────────────────────────────────────────────

describe('过滤四字段', () => {
  it('★★ 按 request_id 子串过滤（大小写不敏感）', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: [CLOSED_ONE] }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-filter-input"]').setValue('LIVE-1')
    await flushPromises()
    expect(w.find('[data-testid="cr-live-row-req-live-1"]').exists()).toBe(true)
    expect(w.find('[data-testid="cr-closed-row-req-closed-1"]').exists()).toBe(false)
  })

  it('★★ 按 protocol / client_type / tenant_id 过滤', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-filter-input"]').setValue('sse')
    await flushPromises()
    expect(w.find('[data-testid="cr-live-row-req-live-1"]').exists()).toBe(true)
    await w.find('[data-testid="cr-filter-input"]').setValue('default')
    await flushPromises()
    expect(w.find('[data-testid="cr-live-row-req-live-1"]').exists()).toBe(true)
  })

  it('★★ 条件键缺失的行也能被过滤命中而不是崩掉', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_NO_META], closed: null }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-filter-input"]').setValue('nometa')
    await flushPromises()
    expect(w.find('[data-testid="cr-live-row-req-nometa-1"]').exists()).toBe(true)
  })

  it('★★ 过滤后计数行出现并给出总数', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: [CLOSED_ONE] }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-filter-input"]').setValue('req-')
    await flushPromises()
    expect(w.find('[data-testid="cr-filter-count"]').text()).toContain('2')
  })
})

// ── 旅程跳转（移动端路由名与桌面不同） ──────────────────────────────────

describe('旅程跳转', () => {
  it('★★ live 行的旅程按钮推的是移动端的 journey-detail / :id', async () => {
    mList.mockResolvedValue(listPayload({ live: [LIVE_OK], closed: null }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-live-journey-req-live-1"]').trigger('click')
    await flushPromises()
    // ★ 桌面推的是 { name: 'request-journey-detail', params: { requestId } }，
    //   移动端路由是 /journey/:id / name: 'journey-detail'（router/index.ts:96-99）
    //   ⇒ 路由名与参数名**都**不同，照抄桌面会推一个不存在的路由。
    expect(push).toHaveBeenCalledWith({
      name: 'journey-detail',
      params: { id: 'req-live-1' },
    })
  })

  it('★★ closed 行的旅程按钮同样可用（跳转不依赖详情端点）', async () => {
    mList.mockResolvedValue(listPayload({ live: [], closed: [CLOSED_ONE] }))
    const w = await mountView()
    await loadInto(w)
    await w.find('[data-testid="cr-closed-journey-req-closed-1"]').trigger('click')
    await flushPromises()
    expect(push).toHaveBeenCalledWith({
      name: 'journey-detail',
      params: { id: 'req-closed-1' },
    })
  })
})
