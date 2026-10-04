// 全量跑时 175 个文件抢 CPU，每个文件的**第一个 mount 用例**要付
// 「动态 import + 整页渲染」的冷启动成本，实测会超过默认的 5s 超时。
// ⇒ 症状是「超时」而不是「断言失败」，很容易被当成产品回归。
// 这里给整份文件一个显式超时；判据本身不放宽。
// partialAggregate.silentcatch.test.ts —— 聚合类静默吞错：偏小的数字不得被当成全量
//
// ## 挡住的是什么
//
// 两个「部分成功」的场景，原来都把失败的那部分**当不存在**：
//
// 1. `ProbeHealthDetailView.loadMonitor`
//    逐凭据拉实时数据，catch 写的是 `// Skip credentials with no live data`。
//    但「这个凭据没有实时数据」与「这次请求失败」是两件事。
//    只要有一个凭据成功，`stats-row` 就照常显示**偏小**的总数与失败率，
//    而那个数字看上去是全量的 —— 失败率被算低，运维看不出来源不全。
//
// 2. `UsageTrendExplorer.loadFilterOptions`
//    两个 catch 都写「不阻塞主图」。主图确实不该被阻塞，但代价是
//    供应商/Key/租户下拉变空 ⇒「没有供应商可选」与「没加载出来」同形。
//    用户选了供应商 A 却查不到数据，结论会是「这段时间 A 没有用量」。
//
// ## 判据钉住的是什么
//
// 不是「多了一条提示」，而是**那句话不再出现 / 那个数字不再假装完整**：
//   ① 部分凭据失败 → 说出有几个（含 id），而不是把总数当全量
//   ② 全部成功     → 无提示（正向对照，防「永远有提示」）
//   ③ 筛选整体失败 → 横幅出现
//   ④ 租户失败     → 横幅出现，且**不与③同一句话**（两个来源要能分辨）
//
// 反向对照见文末，均已实跑并断言变异确实发生。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const reqMock = vi.fn()
const slidingMock = vi.fn()
const getProvidersMock = vi.fn()
const getKeysMock = vi.fn()
const getTenantsMock = vi.fn()

vi.mock('../api/_core', () => ({ req: (...a: unknown[]) => reqMock(...a) }))
vi.mock('../api/credential-monitor', () => ({
  getSlidingWindow: (...a: unknown[]) => slidingMock(...a),
}))
vi.mock('../api/routing', () => ({
  resolveRouting: () => Promise.resolve({ candidates: [] }),
  getDecisions: () => Promise.resolve({ items: [] }),
}))
vi.mock('../api/logs', () => ({ getRequestLogs: () => Promise.resolve({ items: [], count: 0 }) }))
vi.mock('../composables/useCredentialLabels', () => ({
  useCredentialLabels: () => ({ credentialDisplayName: (id: number) => `#${id}` }),
}))
vi.mock('../composables/useConfirmDialog', () => ({ confirmDialog: () => Promise.resolve(true) }))
vi.mock('../i18n', () => ({ localeRef: { value: 'zh-CN' } }))
// 两个视图都从 ../store 取东西，而它们要的不是同一批：
// 漏一个具名导出就会报 `No "x" export is defined` —— 本文件第一版就是这样
// 让 ③–⑤ 全红（症状像「修复没生效」，其实是桩不全）。
vi.mock('../store', () => ({
  store: { userInfo: { role: 'super_admin' } },
  isSuperAdmin: () => true,
  isDefaultTenant: () => true,
  isReadOnlyMode: () => false,
  getLocale: () => 'zh-CN',
}))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: { model: 'gpt-test' } }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}))

// UsageTrendExplorer 的三个筛选 API 分别来自 ../api/providers、../api/keys、
// ../api/tenants（不是桶文件）。挂到桶上症状同型：桩不生效 ⇒ 正例反例一起红。
vi.mock('../api/providers', () => ({ getProviders: (...a: unknown[]) => getProvidersMock(...a) }))
vi.mock('../api/keys', () => ({ getKeys: (...a: unknown[]) => getKeysMock(...a) }))
vi.mock('../api/tenants', () => ({ getTenants: (...a: unknown[]) => getTenantsMock(...a) }))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      probeHealth: { monitorPartialFailed: '有 {count} 个凭据的实时数据没取到（{ids}），下面的统计偏小' },
      usageTrend: { filterProviderKeyFailed: '供应商/Key 筛选选项加载失败', filterTenantFailed: '租户筛选选项加载失败' },
    },
  },
})

const NODES = [
  { credential_id: 11, name: 'n1' },
  { credential_id: 22, name: 'n2' },
]

function routeReq() {
  return (method: string, url: string) => {
    if (String(url).includes('/nodes')) {
      return Promise.resolve({ nodes: NODES, total: NODES.length })
    }
    if (String(url).includes('/state-summary')) {
      return Promise.resolve({ state_distribution: { healthy: 2 } })
    }
    return Promise.resolve({ rows: [], count: 0 })
  }
}

async function openMonitorTab() {
  const View = (await import('./ProbeHealthDetailView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  const tab = w.findAll('button').find((b) => b.text().includes('请求监控'))
  expect(tab, '模板里找不到「请求监控」页签按钮').toBeTruthy()
  await tab!.trigger('click')
  await flushPromises()
  await flushPromises()
  return w
}

async function renderTrend() {
  const View = (await import('./admin/UsageTrendExplorer.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

describe('ProbeHealthDetailView 请求监控：部分凭据失败要说出来', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    reqMock.mockImplementation((m: string, u: string) => routeReq()(m, u))
    slidingMock.mockResolvedValue({ entries: [{ ts: '2026-10-03T00:00:00Z' }], stats: { success: 10, failed: 1, error_kinds: {} } })
  })

  it('① 一个凭据失败 → 提示说出有 1 个，而不是把总数当全量', async () => {
    slidingMock.mockImplementation((cred: number) =>
      cred === 22 ? Promise.reject(new Error('502')) : Promise.resolve({
        entries: [{ ts: '2026-10-03T00:00:00Z' }], stats: { success: 10, failed: 1, error_kinds: {} },
      }))
    const w = await openMonitorTab()

    expect(slidingMock).toHaveBeenCalled()
    const el = w.find('.load-error-banner')
    expect(el.exists()).toBe(true)
    expect(el.text()).toContain('1')
    expect(el.text()).toContain('22')
  })

  it('② 全部成功 → 无提示（正向对照）', async () => {
    const w = await openMonitorTab()
    expect(slidingMock).toHaveBeenCalledTimes(2)
    expect(w.find('.load-error-banner').exists()).toBe(false)
  })
})

describe('UsageTrendExplorer 筛选选项：加载失败要说出来', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('③ 供应商/Key 失败 → 横幅出现', async () => {
    getProvidersMock.mockRejectedValue(new Error('500'))
    getKeysMock.mockResolvedValue([])
    getTenantsMock.mockResolvedValue([])
    const w = await renderTrend()
    const el = w.find('.ute__filter-error')
    expect(el.exists()).toBe(true)
    expect(el.text()).toContain('供应商/Key 筛选选项加载失败')
  })

  it('④ 两个来源都失败 → 横幅列出两段，不吞掉租户那段', async () => {
    getProvidersMock.mockRejectedValue(new Error('500'))
    getKeysMock.mockRejectedValue(new Error('500'))
    getTenantsMock.mockRejectedValue(new Error('500'))
    const w = await renderTrend()
    const text = w.find('.ute__filter-error').text()
    expect(text).toContain('供应商/Key 筛选选项加载失败')
    expect(text).toContain('租户筛选选项加载失败')
  })

  it('⑤ 全部成功 → 无提示（正向对照）', async () => {
    getProvidersMock.mockResolvedValue([{ id: 1, display_name: '甲', catalog_code: 'a' }])
    getKeysMock.mockResolvedValue([])
    getTenantsMock.mockResolvedValue([])
    const w = await renderTrend()
    expect(w.find('.ute__filter-error').exists()).toBe(false)
  })
})
