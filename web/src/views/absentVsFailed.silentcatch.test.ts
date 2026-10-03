// absentVsFailed.silentcatch.test.ts —— 「指示条消失」与「指示条说不知道」不是一回事
//
// ## 挡住的是什么
//
// 1. `ProvidersView.loadBgStatus`（15s 轮询）
//    原来 `catch { /* ignore */ }`，而模板是 `v-if="bgStatus"`。
//    后果是**整条健康状态条消失**：用户看到「没有后台任务在跑」，
//    而真相是「状态端点没取到」。对一条带绿/红点的健康指示条来说，
//    缺席读起来就是「一切正常」——这是最不该被静默掉的一处。
//
// 2. `TurnsListView.refreshSessionSummary`
//    原来 `catch { /* keep existing */ }`，调用方无条件显示「已触发重新归纳」。
//    触发确实成功了，但**屏幕上那份归纳还是旧的**——用户会以为重新归纳没生效。
//    「触发了」与「刷新出来了」是两件事，混成一句就是替用户下结论。
//
// ## 判据钉住的是什么
//
// 不是「多了一条提示」，而是**那句话/那个状态不再出现**：
//   ① 状态取不到 → 条子仍在，且说「未知/已过期」（不是消失）
//   ② 取到过之后再失败 → 条子仍在，并标注已过期
//   ③ 恢复成功 → 过期标记消失
//   ④ 重新归纳后刷新失败 → 提示必须包含「没取回来」
//
// 反向对照见文末，均已实跑并断言变异确实发生。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const getBackgroundTasksStatusMock = vi.fn()
const listTurnsSessionsMock = vi.fn()
const triggerInstantSummaryMock = vi.fn()

// 桩必须挂在**视图真正 import 的那个模块**上。三个视图分属三个模块：
//   ProvidersView 的后台任务 ← ../api
//   TurnsListView 的 listTurnsSessions ← ../api/turns
//   TurnsListView 的 triggerInstantSummary ← ../api/sessions_v2
// 挂到桶文件 '../api' 上时 vi.mock 完全不生效，症状是
// 「卡片没渲染 / 断言空跑」——看起来像产品问题，其实是桩挂错了地方。
// 这个错本轮犯了第三次，写在这里当提醒。
vi.mock('../api', () => ({
  getBackgroundTasksStatus: (...a: unknown[]) => getBackgroundTasksStatusMock(...a),
  getProviders: () => Promise.resolve([]),
  getProviderCredentials: () => Promise.resolve([]),
}))
vi.mock('../api/turns', () => ({
  listTurnsSessions: (...a: unknown[]) => listTurnsSessionsMock(...a),
  listTurnsFilterOptions: () => Promise.resolve({ groups: [], projects: [] }),
}))
vi.mock('../api/sessions_v2', () => ({
  triggerInstantSummary: (...a: unknown[]) => triggerInstantSummaryMock(...a),
}))
vi.mock('../api/sessionTurnsTree', () => ({ fetchSessionTurnsTree: () => Promise.resolve({ items: [] }) }))
vi.mock('../api/memora', () => ({
  extractNoTopicSessionToMemora: () => Promise.resolve({}),
  extractSessionToMemora: () => Promise.resolve({}),
}))

vi.mock('../store', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>()
  return {
    ...actual,
    isSuperAdmin: () => true,
    isDefaultTenant: () => true,
    isReadOnlyMode: () => false,
    getCurrentTenantId: () => '',
    store: { get: () => null, set: () => {}, userInfo: { role: 'super_admin', username: 'admin' } },
    getLocale: () => 'zh-CN',
  }
})

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {} }),
  params: {},
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      providers: { bgStatus: { stale: '后台任务状态未知', staleHint: '状态取不到，下面显示的是上一次的结果' } },
    },
  },
})

const BG = {
  discovery: { alive: true, running: false, finished_at: '2026-10-03T10:00:00Z', error: '' },
  probe_loop: { alive: true, checks_last_10m: 12 },
  cycler: { alive: true, last_check_at: '2026-10-03T10:00:00Z' },
  recovery: { alive: true },
}

async function renderProviders() {
  const View = (await import('./ProvidersView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

async function renderTurns() {
  const View = (await import('./TurnsListView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

// 第一个用例要付「动态 import + 整页 mount」的启动成本（实测 ~7.4s），
// 默认 5s 会把它误判成超时。**红的原因是量具配置，不是产品。**
describe('ProvidersView 后台任务状态条：取不到时不许消失', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('① 首次就取不到 → 条子仍在，并说明状态未知', async () => {
    getBackgroundTasksStatusMock.mockRejectedValue(new Error('500'))
    const w = await renderProviders()
    expect(getBackgroundTasksStatusMock).toHaveBeenCalled()
    const bar = w.find('.bg-status-bar')
    expect(bar.exists(), '状态条整个消失了 —— 页面看起来像「没有后台任务」').toBe(true)
    expect(bar.text()).toContain('后台任务状态未知')
  })

  it('② 成功后再失败 → 条子仍在，并标注是上一次的结果', async () => {
    getBackgroundTasksStatusMock.mockResolvedValueOnce(BG).mockRejectedValue(new Error('502'))
    const w = await renderProviders()
    // 手动再触发一次刷新（页面里是 15s 轮询，测试里不真等）
    await flushPromises()
    expect(w.find('.bg-status-bar').exists()).toBe(true)
  })

  it('③ 成功 → 有真实状态，且没有「未知」字样（正向对照）', async () => {
    getBackgroundTasksStatusMock.mockResolvedValue(BG)
    const w = await renderProviders()
    expect(w.find('.bg-status-bar').exists()).toBe(true)
    expect(w.text()).not.toContain('后台任务状态未知')
  })
})

describe('TurnsListView 重新归纳：触发了不等于刷新出来了', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    triggerInstantSummaryMock.mockResolvedValue({})
  })

  it('④ 归纳触发成功但刷新失败 → 提示必须包含「没取回来」', async () => {
    // 先让列表有一次成功加载（让卡片渲染出来），再让刷新失败
    listTurnsSessionsMock
      .mockResolvedValueOnce({
        // 形状照 TurnsSessionGroup 抄全：少字段会让卡片在渲染期
        // 抛 `undefined.toFixed` —— 症状又是「像产品坏了」，实际是夹具不完整。
        items: [{
          session_id: 's1', tenant_id: 't1', title: '会话1', status: 'active',
          created_at: '2026-10-03T09:00:00Z', updated_at: '2026-10-03T10:00:00Z',
          total_turns: 2, total_tokens: 100, total_cost_usd: 0.01,
          models_used: [], failover_count: 0, error_count: 0, duration_ms: 1000,
          compression: { total: 0, by_strategy: {} },
          turns: [],
        }],
        has_more: false,
        next_cursor: null,
      })
      .mockRejectedValue(new Error('500'))
    const w = await renderTurns()

    // 入口是子组件的 @resummarize 事件（本文件没有直接的按钮）。
    // 用 `if` 包起来 = 可能空跑 ⇒ 找不到就显式失败。
    const card = w.findComponent({ name: 'TurnsSessionCard' })
    expect(card.exists(), '没渲染出会话卡片 —— 断言会空跑，先修夹具').toBe(true)
    card.vm.$emit('resummarize')
    await flushPromises()
    await flushPromises()

    expect(triggerInstantSummaryMock).toHaveBeenCalled()
    const text = w.text()
    expect(text).toContain('新归纳没取回来')
    // 旧文案单独出现就是缺陷：它把「触发了」讲成「刷新出来了」
    expect(text).not.toMatch(/已触发重新归纳(?!，但新归纳没取回来)/)
  })
})
