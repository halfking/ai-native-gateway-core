// DataLifecycleView.tabDeepLink.test.ts —— 「数据生命周期」页：?tab= 深链必须落到对应页签
//
// ## 这道判据钉的是什么
//
// 首启向导的「重置数据」一步把用户送到
// `/admin/data-lifecycle?tab=data-init`（面板默认已全选 ⇒ 默认要重置数据）。
//
// ⇒ 这条深链是**两个页面之间的契约**：向导给出 URL，页面负责解析。
//   页面若不解析 query，深链会静默落到第一个页签（存储总览）⇒
//   用户被送到一个**看不到重置入口**的页面 ⇒ 契约断在链子中间，
//   而且断得毫无征兆（页面正常打开、只是页签不对）。
//
// 判据钉三件事：
//   ① ?tab=data-init ⇒ 落在「数据初始化」页签（深链真的生效）；
//   ② 认不出的 tab 值 ⇒ 回落到第一个页签（不留空白页签、不抛错）；
//   ③ 不带 tab 参数 ⇒ 仍是第一个页签（既有行为不变）。
//
// ★ 门自身的「有牙」证明：变异在**源码层**做（把 resolveTabFromQuery 改成
//   恒返回 'storage'），重跑本判据必须判红，随后还原。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const statsMock = vi.fn()
const cleanupPreviewMock = vi.fn()

// 本判据在 src/views/ ⇒ '../api' 指向 src/api（与被测视图同一个模块）。
vi.mock('../api', () => ({
  dataLifecycleStats: (...a: unknown[]) => statsMock(...a),
  dataLifecycleCleanupPreview: (...a: unknown[]) => cleanupPreviewMock(...a),
}))

const routeQuery: { tab?: string } = {}
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: routeQuery }),
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

async function renderView() {
  const View = (await import('./DataLifecycleView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: true })
  await flushPromises()
  await flushPromises()
  return w
}

function activeTabLabel(w: Awaited<ReturnType<typeof renderView>>): string {
  const active = w.findAll('.tabs .tab').find(t => t.classes().includes('active'))
  return active ? active.text() : ''
}

describe('数据生命周期页：?tab= 深链', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    delete routeQuery.tab
    vi.spyOn(console, 'error').mockImplementation(() => {})
    statsMock.mockResolvedValue({
      total_rows: 0,
      total_size_human: '0 B',
      hot_data: { rows: 0, size_human: '0 B', percent_of_total: 0 },
      warm_data: { rows: 0, size_human: '0 B', percent_of_total: 0 },
      cold_data: { rows: 0, size_human: '0 B', percent_of_total: 0 },
      expired_data: { rows: 0, size_human: '0 B', percent_of_total: 0 },
      compression_rate: 0,
      compression_rate_human: '0%',
      by_tenant: [],
      growth_trend: [],
      recent_logs: [],
    })
    cleanupPreviewMock.mockResolvedValue({ affected_rows: 0, estimated_freed_human: '0 B' })
  })

  it('① ?tab=data-init ⇒ 落在「数据初始化」页签', async () => {
    routeQuery.tab = 'data-init'
    const w = await renderView()
    expect(activeTabLabel(w)).toContain('dataLifecycle.dataInit.tabLabel')
  })

  it('② 认不出的 tab 值 ⇒ 回落到第一个页签（不留空白）', async () => {
    routeQuery.tab = 'not-a-real-tab'
    const w = await renderView()
    expect(activeTabLabel(w)).not.toContain('dataLifecycle.dataInit.tabLabel')
    expect(activeTabLabel(w)).not.toBe('')
  })

  it('③ 不带 tab 参数 ⇒ 仍是第一个页签（既有行为不变）', async () => {
    const w = await renderView()
    expect(activeTabLabel(w)).not.toContain('dataLifecycle.dataInit.tabLabel')
    expect(activeTabLabel(w)).not.toBe('')
  })
})
