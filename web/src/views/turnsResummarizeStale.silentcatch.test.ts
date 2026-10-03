// turnsResummarizeStale.silentcatch.test.ts —— 「触发了」≠「刷新出来了」
//
// ## 为什么单独一份（它一开始没有判据）
//
// 第六批修了 TurnsListView：`refreshSessionSummary` 原来 `catch { /* keep existing */ }`，
// 而调用方 `resummarize` 无条件显示「已触发重新归纳」。
// 触发确实成功了，但**屏幕上那份归纳还是旧的**——用户会以为重新归纳没生效，
// 或者更糟：以为新归纳就是这个样子。
//
// 修法是让 `refreshSessionSummary` 返回布尔，调用方据此分两句说。
// 修完就登记进量具的「跨作用域已处理」豁免表（B 返回值即信号），
// 然后**量具的反向对照把这条例外打回来了**：把 `refreshed ?` 换成 `true ?`，
// 本文件当时**一条判据都没有**（原先登记的判据文件根本不含 TurnsListView）。
//
// ⇒ 这就是本文件存在的理由：**「产出正确」和「产出被守住」是两件事**。
// 每条修复配反向对照，不是流程上的仪式，而是会在两批之后咬人。
//
// ## 判据钉住的是什么
//
// ① 刷新没取回来（列表里找不到该 session）→ 说的是「已触发但没取回来」
// ② 刷新请求直接失败              → 同上
// ③ 刷新成功                      → 说的是「已触发重新归纳」（正向对照）
//
// ①③ 一起证明这条判据能回答**两个方向**：否则 ①② 恒绿可能只是因为
// 锚点根本读不到东西。
//
// ★ 用 `toBe` 精确比，不用 `toContain`：「已触发重新归纳」是
//   「已触发重新归纳，但新归纳没取回来（当前显示的是旧内容）」的**子串**。
//   `not.toContain('已触发重新归纳')` 在这句话出现时**照样会红**——
//   那是判据错，不是产品错。本轮已经在这个坑上栽过一次（见审计文档 §36）。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const listTurnsSessionsMock = vi.fn()
const listTurnsFilterOptionsMock = vi.fn()
const triggerInstantSummaryMock = vi.fn()
const fetchSessionTurnsTreeMock = vi.fn()

// 键集按 TurnsListView 的 import 语句逐个列全。漏一个具名导出会报
// `No "x" export is defined on the mock`；而那个抛点若正好落在被测的
// try 里，成功用例会误走失败分支。
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))
vi.mock('../api/memora', () => ({
  extractNoTopicSessionToMemora: vi.fn(),
  extractSessionToMemora: vi.fn(),
}))
vi.mock('../api/sessionTurnsTree', () => ({ fetchSessionTurnsTree: fetchSessionTurnsTreeMock }))
vi.mock('../api/sessions_v2', () => ({ triggerInstantSummary: (...a: unknown[]) => triggerInstantSummaryMock(...a) }))
vi.mock('../api/turns', () => ({
  listTurnsSessions: (...a: unknown[]) => listTurnsSessionsMock(...a),
  listTurnsFilterOptions: (...a: unknown[]) => listTurnsFilterOptionsMock(...a),
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: { 'zh-CN': {} },
})

// 形状照抄 `TurnsSessionGroup`（api/turns.ts:95）。第一版漏了 `compression`，
// 报错出现在**渲染期**（`session.compression.applied_count` 读 undefined）
// 而不是取数时 —— 这是「mock 少一层」的典型信号。
const SESSION = {
  session_id: 'sess-1',
  tenant_id: 't1',
  title: '一次普通会话',
  topic: '排查升级失败',
  intent: '查日志',
  status: 'active',
  created_at: '2026-10-01T00:00:00Z',
  updated_at: '2026-10-03T00:00:00Z',
  start_time: '2026-10-01T00:00:00Z',
  total_turns: 2,
  total_tokens: 100,
  total_cost_usd: 0.01,
  models_used: ['m1'],
  failover_count: 0,
  error_count: 0,
  duration_ms: 1000,
  compression: { applied_count: 0, tokens_saved: 0, strategies: [] },
  turns: [{ turn_no: 1, content: '旧内容' }],
}
const PAGE = { items: [SESSION], has_more: false, next_cursor: '' }
const STALE_MSG = '已触发重新归纳，但新归纳没取回来（当前显示的是旧内容）'
const OK_MSG = '已触发重新归纳'

async function renderPage() {
  const View = (await import('./TurnsListView.vue')).default
  const w = mount(View, {
    global: {
      plugins: [i18n],
      stubs: { TurnsFilterBar: true }, // 筛选栏与本判据无关；卡片**不桩**（要读它的 prop）
    },
    shallow: false,
  })
  await flushPromises()
  await flushPromises()
  return w
}

/** 前置断言：会话卡真的渲染出来了，否则下面的 prop 断言会静默读到 undefined。 */
function sessionCard(w: Awaited<ReturnType<typeof renderPage>>) {
  const card = w.findComponent({ name: 'TurnsSessionCard' })
  expect(card.exists(), '会话卡必须真的渲染（没被桩掉）').toBe(true)
  return card
}

/**
 * 直接调组件方法，不点按钮。
 *
 * 两层原因：① 点「重新归纳」要穿过卡片内部的 emit → 列表页 handler →
 * async 链，桩件失败会表现成「产品断言红」；② `w.vm` 在运行时能拿到
 * `<script setup>` 的内部绑定，但 vue-tsc 不认（没 defineExpose）⇒ 必须转型，
 * 否则 TS2339。转型集中在这一个函数里，四处用例不重复写。
 */
function resummarize(w: Awaited<ReturnType<typeof renderPage>>) {
  return (w.vm as unknown as { resummarize: (s: unknown) => Promise<void> }).resummarize(SESSION)
}

describe('TurnsListView 重新归纳：触发成功 ≠ 新归纳取回来了', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    listTurnsFilterOptionsMock.mockResolvedValue({ api_keys: [] })
    triggerInstantSummaryMock.mockResolvedValue({ ok: true })
    fetchSessionTurnsTreeMock.mockResolvedValue({ turns: [] })
  })

  it('① 刷新回来的列表里没有这一条 → 不得说「已触发重新归纳」', async () => {
    // 第一次是首屏，第二次是 resummarize 内部的刷新：返回空列表 ⇒ 找不到该 session。
    listTurnsSessionsMock.mockResolvedValueOnce(PAGE).mockResolvedValue({ items: [], has_more: false, next_cursor: '' })
    const w = await renderPage()
    const card = sessionCard(w)
    // 前置：卡片拿到的就是这条会话。prop 名是 `session`（整个对象），
    // 我第一版写 `sessionId` 拿到 undefined —— **前置断言当场把自己拦下了**，
    // 下面的 assetMsg 断言本来会静默读到 undefined 然后报一个看似产品的红。
    expect(card.props('session').session_id).toBe('sess-1')

    await resummarize(w)
    await flushPromises()

    // 精确比，不是包含：「已触发重新归纳」是上面那句的子串。
    expect(card.props('assetMsg')).toBe(STALE_MSG)
    expect(card.props('assetMsg')).not.toBe(OK_MSG)
  })

  it('② 刷新请求直接失败 → 同样不得说「已触发重新归纳」', async () => {
    listTurnsSessionsMock.mockResolvedValueOnce(PAGE).mockRejectedValue(new Error('刷新接口 500'))
    const w = await renderPage()
    const card = sessionCard(w)

    await resummarize(w)
    await flushPromises()

    expect(card.props('assetMsg')).toBe(STALE_MSG)
  })

  it('③ 刷新成功 → 才说「已触发重新归纳」（正向对照：证明 ①② 的判据能答两个方向）', async () => {
    listTurnsSessionsMock.mockResolvedValue(PAGE)
    const w = await renderPage()
    const card = sessionCard(w)

    await resummarize(w)
    await flushPromises()

    expect(card.props('assetMsg')).toBe(OK_MSG)
    expect(triggerInstantSummaryMock).toHaveBeenCalled()
  })

  it('④ 触发本身失败 → 说的是失败原因，不是「已触发」（反向：别把触发失败也归到刷新那支）', async () => {
    listTurnsSessionsMock.mockResolvedValue(PAGE)
    triggerInstantSummaryMock.mockRejectedValue(new Error('归纳服务不可用'))
    const w = await renderPage()
    const card = sessionCard(w)

    await resummarize(w)
    await flushPromises()

    expect(card.props('assetMsg')).toBe('归纳服务不可用')
    expect(card.props('assetMsg')).not.toBe(OK_MSG)
  })
})
