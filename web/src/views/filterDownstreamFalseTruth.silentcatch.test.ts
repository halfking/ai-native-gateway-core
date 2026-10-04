// filterDownstreamFalseTruth.silentcatch.test.ts —— 三个下拉/列表不得把「取不到」说成「没有」
//
// ## 挡住的是什么（本批 P2 / P3 / P4）
//
// 三处同一形状：`catch` 把数据源清空，页面于是给出一句**事实陈述**，
// 而真相是「没取到」。三处的载体不同，所以危害也不同：
//
// P2 `RequestLogsView.loadKeys`  → Key 下拉只剩「全部 Key」
//    用户按某个 Key 筛出零条日志 ⇒「这段时间没流量」（真相：Key 清单没加载）
//    ⚠️ 这一处外面**还包着一层** try/catch 打 console.error，但内层已吞 ⇒ 死代码，
//       读起来像「loadKeys 可能抛出」的保护，实际永不触发。
//    ⚠️ 也**不能**复用 filterOptionsError：loadCredentialOptions 开头会清零，
//       而它在 loadKeys **之后**执行 ⇒ 写进去必被抹掉。
//
// P3 `ChatView.refreshAvailableModels` → 模型 <select> 里有一条**硬编码**的
//    「自动路由 (auto)」（ChatComposer.vue:47）⇒ 清空后下拉不是空的，
//    它只剩「自动路由」一项 ⇒「网关只提供自动路由」，而用户据此发出去的
//    请求走了完全不同的链路。
//
// P4 `TurnsListView.loadChildOps` → 失败写 `childOpsMap[id] = []`，
//    而入口早退是 `if (childOpsMap.value[sessionId] || …) return` ——
//    **空数组是 truthy** ⇒ 一次失败后永久命中早退，展开再多次也不重试。
//    「取不到」被缓存成了「取到了，是空的」，两个错误合成一个永久的假。
//
// 反向对照见文末，均已实跑并断言变异确实发生。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import requestsZh from '../locales/zh-CN/requests'
import chatZh from '../locales/zh-CN/chat'

const getKeysMock = vi.fn()
const getProvidersMock = vi.fn()
const getProviderCredentialsMock = vi.fn()
const getCredentialMonitorSummaryMock = vi.fn()
const getAvailableModelsMock = vi.fn()
const fetchSessionTurnsTreeMock = vi.fn()

vi.mock('../api/keys', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../api/keys')
  return { ...actual, getKeys: (...a: unknown[]) => getKeysMock(...a) }
})
vi.mock('../api/providers', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../api/providers')
  return { ...actual, getProviders: (...a: unknown[]) => getProvidersMock(...a) }
})
vi.mock('../api/providerCredentials', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../api/providerCredentials')
  return { ...actual, getProviderCredentials: (...a: unknown[]) => getProviderCredentialsMock(...a) }
})
vi.mock('../api', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../api')
  return {
    ...actual,
    getCredentialMonitorSummary: (...a: unknown[]) => getCredentialMonitorSummaryMock(...a),
    getAvailableModels: (...a: unknown[]) => getAvailableModelsMock(...a),
  }
})
vi.mock('../api/sessionTurnsTree', () => ({ fetchSessionTurnsTree: (...a: unknown[]) => fetchSessionTurnsTreeMock(...a) }))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: { 'zh-CN': { requests: requestsZh, chat: chatZh } },
})

// ===== P2：RequestLogsView =====

async function renderLogs() {
  const View = (await import('./RequestLogsView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

describe('P2 RequestLogsView：Key 清单取不到，不得说成「按该 Key 筛出零条」', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getKeysMock.mockResolvedValue([{ id: 1, key_prefix: 'sk-aaa', application_code: 'app-1' }])
    getProvidersMock.mockResolvedValue([])
    getProviderCredentialsMock.mockResolvedValue([])
    getCredentialMonitorSummaryMock.mockResolvedValue({ credentials: [] })
  })

  it('P2-① Key 清单失败 → 筛选区出现失败说明', async () => {
    getKeysMock.mockRejectedValue(new Error('keys 500'))
    const w = await renderLogs()
    const errs = w.findAll('.filter-error')
    expect(errs.length, '必须至少有一条筛选区失败说明').toBeGreaterThan(0)
    expect(w.text()).toContain(requestsZh.list.filter.keysLoadFailed)
  })

  it('P2-② Key 清单成功 → 不出现那条说明（正向对照，防永远说失败）', async () => {
    const w = await renderLogs()
    expect(w.text()).not.toContain(requestsZh.list.filter.keysLoadFailed)
    // 前置：Key 确实渲染出来了，否则 ① 可能只是「什么都没渲染」
    expect(w.text()).toContain('sk-aaa')
  })
})

// ===== P3：ChatView =====

describe('P3 ChatView：模型清单取不到，下拉只剩「自动路由」不得被当真', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    // 形状照抄 `AvailableModelsResponse`（api/models.ts:41）：{ popular, families }。
    // 我第一版写成 `{ models: [...] }` ⇒ popular/families 全是 undefined ⇒
    // projectAvailableModels 返回 []，正向对照于是读到 0 条 —— 判据的错。
    getAvailableModelsMock.mockResolvedValue({ popular: [{ canonical_name: 'gpt-x', display_name: 'GPT-X' }], families: [] })
  })

  it('P3-① 模型清单失败 → 页面说清是取不到，且下拉里那条硬编码「自动路由」不是唯一解释', async () => {
    getAvailableModelsMock.mockRejectedValue(new Error('models 502'))
    const View = (await import('./ChatView.vue')).default
    const w = mount(View, { global: { plugins: [i18n], stubs: { RouterLink: true } }, shallow: false })
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain(chatZh.page.modelListFailed)
    // 防恒绿：这条说明不能由同页别处提供 —— 必须有一个专属标记节点。
    const alert = w.findAll('.chat-error').find((el) => el.text().includes(chatZh.page.modelListFailed))
    expect(alert, '失败说明必须在专属节点上，不能只是全页碰巧含有这句话').toBeTruthy()
  })

  it('P3-② 模型清单成功 → 不出现那条说明，且模型进了下拉（正向对照）', async () => {
    const View = (await import('./ChatView.vue')).default
    const w = mount(View, { global: { plugins: [i18n], stubs: { RouterLink: true } }, shallow: false })
    await flushPromises()
    await flushPromises()
    expect(w.text()).not.toContain(chatZh.page.modelListFailed)
    const composer = w.findComponent({ name: 'ChatComposer' })
    expect(composer.exists()).toBe(true)
    expect((composer.props('models') as unknown[]).length).toBeGreaterThan(0)
  })
})

// ===== P4：TurnsListView 子操作 =====

// 轮次形状照抄 `TurnGroupItem`（api/turns.ts:62）。第一版只写 `{ turn_no: 1 }`，
// 展开会话后模板读 `turn.cost_usd.toFixed(4)` → TypeError。
// 上一份 TurnsListView 判据没炸，是因为那里的卡片**始终折叠**，轮次区没渲染。
// ⇒ 夹具字段要对着**真正会渲染的那条分支**核对，不是对着「碰巧没走到」的分支。
const TURN = {
  turn_no: 1,
  ts: '2026-10-01T00:00:00Z',
  request_id: 'req-1',
  request_tokens: 10,
  response_tokens: 20,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  cost_usd: 0.002,
  model: 'm1',
  provider: 'p1',
  status_code: 200,
  success: true,
  submit_mode: 'sync',
  compression_applied: false,
  injection_verdict: 'pass',
  output_verdict: 'pass',
  attachment_count: 0,
  attempt_no: 1,
}
const SESSION = {
  session_id: 'sess-1',
  tenant_id: 't1',
  title: '一次普通会话',
  topic: '排查升级失败',
  status: 'active',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-03T00:00:00Z',
  start_time: '2026-10-01T00:00:00Z',
  total_turns: 1,
  total_tokens: 100,
  total_cost_usd: 0.01,
  models_used: ['m1'],
  failover_count: 0,
  error_count: 0,
  duration_ms: 1000,
  compression: { applied_count: 0, tokens_saved: 0, strategies: [] },
  turns: [TURN],
}
const PAGE = { items: [SESSION], has_more: false, next_cursor: '' }

type TurnsVm = {
  loadChildOps: (id: string) => Promise<void>
  toggleSession: (id: string) => void
  childOpsError: Record<string, string>
}

async function renderTurns() {
  const View = (await import('./TurnsListView.vue')).default
  const w = mount(View, { global: { plugins: [i18n], stubs: { TurnsFilterBar: true } }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}
const tVm = (w: Awaited<ReturnType<typeof renderTurns>>) => w.vm as unknown as TurnsVm

describe('P4 TurnsListView：子操作取不到 ≠ 这一轮没有子请求', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    const { listTurnsSessionsMock, listTurnsFilterOptionsMock } = mocks
    listTurnsSessionsMock.mockResolvedValue(PAGE)
    listTurnsFilterOptionsMock.mockResolvedValue({ api_keys: [] })
    fetchSessionTurnsTreeMock.mockResolvedValue({ turns: [] })
  })

  it('P4-① 取不到 → 卡片上出现失败标记（不是沉默）', async () => {
    fetchSessionTurnsTreeMock.mockRejectedValue(new Error('tree 503'))
    const w = await renderTurns()
    // ★ 必须走 toggleSession（真实用户路径：展开才触发加载），
    //   因为轮次区在 `v-if="expanded"` 里。直接调 loadChildOps 的话
    //   数据到了但整块没渲染 —— 我第一版就这么写的，
    //   报错是「失败标记必须真的渲染出来 expected +0 to be +1」。
    tVm(w).toggleSession('sess-1')
    await flushPromises()
    const card = w.findComponent({ name: 'TurnsSessionCard' })
    expect(card.props('childOpsError')).toBe('tree 503')
    const marker = w.findAll('.child-ops-error')
    expect(marker.length, '失败标记必须真的渲染出来').toBe(1)
    expect(marker[0].text()).toContain('tree 503')
  })

  it('P4-② 失败后 map 里不得留下这一项（否则永不重试）', async () => {
    fetchSessionTurnsTreeMock.mockRejectedValue(new Error('tree 503'))
    const w = await renderTurns()
    tVm(w).toggleSession('sess-1')
    await flushPromises()
    const card = w.findComponent({ name: 'TurnsSessionCard' })
    // ⚠️ 这是本条的核心：`[]` 是 truthy，早退会永久命中。
    expect(card.props('childOps')).toBeUndefined()

    // 端点恢复后再调一次，必须真的重新发请求（而不是被早退挡掉）
    fetchSessionTurnsTreeMock.mockResolvedValue({
      turns: [{ turn_number: 1, child_requests: [{ request_id: 'r1', request_type: 'embedding', status: 'ok' }] }],
    })
    await tVm(w).loadChildOps('sess-1')
    await flushPromises()
    expect(fetchSessionTurnsTreeMock, '失败后必须还能重试').toHaveBeenCalledTimes(2)
    expect(card.props('childOpsError'), '成功后错误标记要清掉').toBe('')
    expect((card.props('childOps') as unknown[])?.length).toBeGreaterThan(0)
  })

  it('P4-③ 成功取到空的子操作 → 那是「确实没有」，不该出现失败标记（正向对照）', async () => {
    fetchSessionTurnsTreeMock.mockResolvedValue({ turns: [{ turn_number: 1, child_requests: [] }] })
    const w = await renderTurns()
    tVm(w).toggleSession('sess-1')
    await flushPromises()
    const card = w.findComponent({ name: 'TurnsSessionCard' })
    expect(card.props('childOpsError')).toBe('')
    expect(w.findAll('.child-ops-error')).toHaveLength(0)
    expect(card.props('childOps'), '成功取到空列表：这是「确实为空」，要缓存住').toEqual([
      { turn_number: 1, child_requests: [] },
    ])
  })
})

// TurnsListView 自己的 api 桩在文件上方统一声明（vi.mock 是提升的，
// 但 mock 工厂里引用的局部变量必须在调用前初始化 —— 提前到顶部）。
const mocks = {
  listTurnsSessionsMock: vi.fn(),
  listTurnsFilterOptionsMock: vi.fn(),
}
vi.mock('../api/turns', () => ({
  listTurnsSessions: (...a: unknown[]) => mocks.listTurnsSessionsMock(...a),
  listTurnsFilterOptions: (...a: unknown[]) => mocks.listTurnsFilterOptionsMock(...a),
}))
vi.mock('../api/memora', () => ({
  extractNoTopicSessionToMemora: vi.fn(),
  extractSessionToMemora: vi.fn(),
}))
vi.mock('../api/sessions_v2', () => ({ triggerInstantSummary: vi.fn() }))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))
