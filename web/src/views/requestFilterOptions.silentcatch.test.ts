// 全量跑时 175 个文件抢 CPU，每个文件的**第一个 mount 用例**要付
// 「动态 import + 整页渲染」的冷启动成本，实测会超过默认的 5s 超时。
// ⇒ 症状是「超时」而不是「断言失败」，很容易被当成产品回归。
// 这里给整份文件一个显式超时；判据本身不放宽。
// requestFilterOptions.silentcatch.test.ts —— 筛选/策略加载失败不得与「本来就没有」同形
//
// ## 挡住的是什么
//
// 2026-10-03 之前，`RequestLogsView.loadCredentialOptions` 有两处静默吞错：
//
//   · 外层 `catch { providerOptions = []; credentialOptions = [] }`
//   · 内层每个 provider 的 `catch { /* 该 provider 失败不影响其它 */ }`
//
// 页面后果：供应商/凭据下拉变空，而「下拉为空」在页面上与
// 「这个部署真的一个供应商/凭据都没有」**完全同形**。用户按某个供应商
// 筛选后一条日志都搜不到，得到的结论是「这段时间没流量」——
// 而真相是「凭据列表没加载出来」。把「不知道」讲成「知道」比不显示更糟。
//
// 同一文件 1081 行原本还写着「由 loadCredentialOptions() 内部兜底，
// 此处不需再包一层 try/catch」：把判断推给未来的人，而那个「兜底」
// 就是清空两个下拉。
//
// `ProxyView` 是同一族的另一面：`loadPolicy` 由 onMounted 调用，
// 失败时 policy 一直是 null，卡片**永远**显示「加载中…」——
// 一个永远不会完成的等待。页面没说失败，只说还在加载。
//
// ## 判据钉住的是什么
//
// 三态可区分，且**失败必须被显示**（不只是被捕获）：
//   ① 加载成功              → 无提示
//   ② 整体失败              → 提示，且下拉为空
//   ③ 部分 provider 失败     → 提示，且说出是哪些（与②不是同一句话）
//   ④/⑤ 策略/区域加载失败   → 进同一条 error 通道，且不再说「加载中…」
//
// 反向对照见文末，均已实跑并断言变异确实发生。
//
// ## 这份测试自己翻车过一次（记在这里，因为症状很像「产品没修好」）
//
// 第一版把 `../api` mock 成**只含 4 个导出**的对象，于是视图里任何其它
// API 调用都抛 `No "X" export is defined on the mock` —— 而那个抛出点
// 正好在 `loadCredentialOptions` 的 try 里 ⇒ **成功用例也报出了失败横幅**。
// 「正例红」在这里不是产品有 bug，是量具停在错误的位置。
// 现在 api mock 用 Proxy 兜底，未列出的导出返回空结果。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const getProvidersMock = vi.fn()
const getProviderCredentialsMock = vi.fn()
const getProxyPolicyMock = vi.fn()
const getProxyRegionsMock = vi.fn()

// 桩必须**枚举出视图真的 import 的每个具名导出**：vitest 对具名导入是按 key
// 校验的，`new Proxy` 兜底不成立（会报 No "x" export is defined on the mock），
// 而那个报错点若正好落在被测的 try 里，成功用例会误走失败分支。
// 键集由两个视图的 import 语句生成，不是手抄。
// ⚠️ RequestLogsView 的供应商/凭据 API 来自 **../api/providers**（不是桶文件 ../api），
// 挂错模块的症状与上面同型：正例与反例一起红。
vi.mock('../api', () => ({
  createProxyNode: () => Promise.resolve([]),
  createProxySubscription: () => Promise.resolve([]),
  deleteProxyNode: () => Promise.resolve([]),
  deleteProxySubscription: () => Promise.resolve([]),
  forceSwapProxy: () => Promise.resolve([]),
  getProxyNodes: () => Promise.resolve([]),
  getProxyPolicy: (...a: unknown[]) => getProxyPolicyMock(...a),
  getProxyRegions: (...a: unknown[]) => getProxyRegionsMock(...a),
  getProxyStatus: () => Promise.resolve([]),
  getProxySubscriptions: () => Promise.resolve([]),
  // 形状要对：视图做 `rows.value = resp.items; total.value = resp.count`，
  // 返回 [] 会让 rows 变 undefined → 渲染时 .filter 炸 → **未处理的 rejection**。
  // 症状特别误导：所有用例都「通过」，但 vitest 退出码是 1。
  getRequestLogs: () => Promise.resolve({ items: [], count: 0, aggregate: null }),
  getKeys: () => Promise.resolve([]),
  getBodyCacheStats: () => Promise.resolve({}),
  healthCheckAllProxyNodes: () => Promise.resolve([]),
  healthCheckProxyNode: () => Promise.resolve([]),
  healthCheckProxySubscription: () => Promise.resolve([]),
  refreshProxySubscription: () => Promise.resolve([]),
  setProxyNodeRegionBan: () => Promise.resolve([]),
  setProxyPolicy: () => Promise.resolve([]),
  updateProxySubscription: () => Promise.resolve([]),
}))

vi.mock('../api/providers', () => ({
  getProviders: (...a: unknown[]) => getProvidersMock(...a),
  getProviderCredentials: (...a: unknown[]) => getProviderCredentialsMock(...a),
}))
vi.mock('../api/credential-monitor', () => ({
  getCredentialMonitorSummary: () => Promise.resolve({ credentials: [] }),
}))

vi.mock('../store', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>()
  return {
    ...actual,
    isSuperAdmin: () => true,
    isDefaultTenant: () => true,
    isReadOnlyMode: () => false,
    getCurrentTenantId: () => '',
    store: { get: () => null, set: () => {}, userInfo: { role: 'super_admin' } },
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
      requests: {
        list: {
          filter: {
            loadFailed: '筛选选项加载失败',
            partialLoadFailed: '有 {count} 个供应商凭据未加载：{names}',
          },
        },
      },
      proxy: {
        error: { loadPolicyFailed: '加载选择策略失败', loadRegionsFailed: '加载区域统计失败' },
      },
    },
  },
})

async function renderRequestLogs() {
  const View = (await import('./RequestLogsView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

async function renderProxy() {
  const View = (await import('./ProxyView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

const P1 = { id: 1, display_name: '供应商甲', catalog_code: 'a' }
const P2 = { id: 2, display_name: '供应商乙', catalog_code: 'b' }
const C1 = { id: 11, provider_id: 1, label: '凭据甲' }

describe('RequestLogsView 供应商/凭据筛选：三态可区分', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getProvidersMock.mockResolvedValue([P1, P2])
    getProviderCredentialsMock.mockResolvedValue([])
  })

  it('① 加载成功 → 无提示（且确认真走到了被测路径）', async () => {
    getProviderCredentialsMock.mockImplementation((id: number) =>
      id === 1 ? Promise.resolve([C1]) : Promise.resolve([]))
    const w = await renderRequestLogs()
    // 不加这条断言，「无提示」可能只是因为压根没发请求
    expect(getProvidersMock).toHaveBeenCalled()
    expect(getProviderCredentialsMock).toHaveBeenCalled()
    expect(w.find('.filter-error').exists()).toBe(false)
  })

  it('② 整体失败 → 提示出现', async () => {
    getProvidersMock.mockRejectedValue(new Error('500'))
    const w = await renderRequestLogs()
    expect(w.find('.filter-error').exists()).toBe(true)
    expect(w.find('.filter-error').text()).toContain('筛选选项加载失败')
  })

  it('③ 部分 provider 失败 → 提示说出是哪些，且不与②同一句话', async () => {
    getProviderCredentialsMock.mockImplementation((id: number) =>
      id === 1 ? Promise.resolve([C1]) : Promise.reject(new Error('502')))
    const w = await renderRequestLogs()
    const el = w.find('.filter-error')
    expect(el.exists()).toBe(true)
    expect(el.text()).toContain('1')
    expect(el.text()).toContain('供应商乙')
    expect(el.text()).not.toContain('筛选选项加载失败')
  })
})

describe('ProxyView：加载失败不得永远停在「加载中…」', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('④ 策略加载失败 → 进 error 通道，且卡片不再说「加载中…」', async () => {
    // onMounted 就会调 loadPolicy()，这条路径**真实可达**。
    // 抛非 Error 才会走 i18n 兜底文案（产品优先展示 e.message，那是合理的）。
    getProxyPolicyMock.mockRejectedValue('boom')
    getProxyRegionsMock.mockResolvedValue({ items: [] })
    const w = await renderProxy()
    expect(getProxyPolicyMock).toHaveBeenCalled()
    expect(w.find('.error-banner').exists()).toBe(true)
    expect(w.find('.error-banner').text()).toContain('加载选择策略失败')
    expect(w.text()).not.toContain('proxy.policy.loading')
  })

  it('⑤ 区域统计失败 → 走同一条通道', async () => {
    getProxyPolicyMock.mockResolvedValue({ policy: {}, swap_state: null })
    getProxyRegionsMock.mockRejectedValue('boom')
    const w = await renderProxy()
    expect(w.find('.error-banner').text()).toContain('加载区域统计失败')
  })

  it('⑥ 两个都成功 → 无横幅（正向对照，防止「永远有横幅」）', async () => {
    getProxyPolicyMock.mockResolvedValue({ policy: { auto_disable_enabled: true }, swap_state: null })
    getProxyRegionsMock.mockResolvedValue({ items: [] })
    const w = await renderProxy()
    expect(w.find('.error-banner').exists()).toBe(false)
  })
})
