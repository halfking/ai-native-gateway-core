// 全量跑时 175 个文件抢 CPU，每个文件的**第一个 mount 用例**要付
// 「动态 import + 整页渲染」的冷启动成本，实测会超过默认的 5s 超时。
// ⇒ 症状是「超时」而不是「断言失败」，很容易被当成产品回归。
// 这里给整份文件一个显式超时；判据本身不放宽。
// ProbeHealthDetailView.tabError.test.ts —— 价格页签的加载失败必须**显示**出来
//
// ## 挡住的是什么
//
// 2026-10-03 之前，`loadPricing` 的 catch 写的是
//
//     catch { setTabError('pricing', '价格数据不可用（需要 platform_ops 权限）') }
//
// 状态确实设了，模板也确实消费了它 —— 但消费写成
//
//     <EmptyState v-else-if="tabErrors.pricing" text="{{ tabErrors.pricing }}" />
//
// 花括号在双引号里是**字面量**，Vue 不做插值。于是失败时那行显示的是
// 一串 `{{ tabErrors.pricing }}`：既没告诉用户「价格数据不可用」，
// 又让页面看起来像模板写坏了。
//
// 这是「有状态、看起来也有消费者、实际消费者是坏的」——
// 只断言「错误被捕获」或「模板里出现过这个变量」都会放它过去。
// 判据因此断言**渲染出来的文本**，并显式排除字面量形态。
//
// 反向对照：把 `:text` 退回 `text="{{ … }}"`（撤掉本轮修复），两条用例红。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const reqMock = vi.fn()

// 组件直接用 `req()` 而不是某个具名 API 导出，桩必须挂在 **../api/_core**。
// 第一版的教训（见 filterMeta.silentcatch.test.ts 头注）：桩挂错模块时
// 正例与反例会**一起**红，因为成功路径压根没成功过。
vi.mock('../api/_core', () => ({ req: (...a: unknown[]) => reqMock(...a) }))
vi.mock('../api/routing', () => ({
  resolveRouting: () => Promise.resolve({ candidates: [] }),
  getDecisions: () => Promise.resolve({ items: [] }),
}))
vi.mock('../api/logs', () => ({ getRequestLogs: () => Promise.resolve({ items: [] }) }))
vi.mock('../api/credential-monitor', () => ({ getSlidingWindow: () => Promise.resolve({}) }))
vi.mock('../composables/useCredentialLabels', () => ({
  useCredentialLabels: () => ({ credentialDisplayName: (id: number) => `#${id}` }),
}))
vi.mock('../composables/useConfirmDialog', () => ({ confirmDialog: () => Promise.resolve(true) }))
vi.mock('../i18n', () => ({ localeRef: { value: 'zh-CN' } }))
vi.mock('../store', () => ({
  // 价格页签要求 super_admin，否则 v-if="!isAdmin" 先短路，断言会停在另一个分支上。
  store: { userInfo: { role: 'super_admin' } },
}))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: { model: 'gpt-test' } }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: { 'zh-CN': {} },
})

const PRICE_ERR = '价格数据不可用（需要 platform_ops 权限）'

async function openPricingTab() {
  const View = (await import('./ProbeHealthDetailView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  // 切到价格页签 —— 它是懒加载的（switchTab 首次才取数）
  const tab = w.findAll('button').find((b) => b.text().includes('价格管理'))
  expect(tab, '模板里找不到「价格管理」页签按钮').toBeTruthy()
  await tab!.trigger('click')
  await flushPromises()
  await flushPromises()
  return w
}

describe('ProbeHealthDetailView 价格页签：加载失败要显示原因', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('价格请求失败时，EmptyState 显示的是错误原因而不是字面量', async () => {
    reqMock.mockImplementation((method: string, url: string) => {
      if (url.includes('/api/pricing/table')) return Promise.reject(new Error('403'))
      return Promise.resolve({ rows: [], count: 0 })
    })

    const w = await openPricingTab()

    expect(reqMock.mock.calls.some(([, u]) => String(u).includes('/api/pricing/table')),
      '价格页签没有真的发请求 —— 断言量具停在了没触发的路径上').toBe(true)

    // ① 显示的是错误原因
    expect(w.text()).toContain(PRICE_ERR)
    // ② 且**不是**字面量形态（这一条才是本判据存在的唯一理由）
    expect(w.text()).not.toContain('{{')
    expect(w.text()).not.toContain('tabErrors')
  })

  it('价格请求成功但为空时，不显示错误原因（不把「真的没有」讲成「失败」）', async () => {
    reqMock.mockImplementation((method: string, url: string) => {
      if (url.includes('/api/pricing/table')) return Promise.resolve({ rows: [], count: 0 })
      return Promise.resolve({ rows: [], count: 0 })
    })

    const w = await openPricingTab()

    expect(w.text()).not.toContain(PRICE_ERR)
    expect(w.text()).toContain('暂无价格数据')
  })
})
