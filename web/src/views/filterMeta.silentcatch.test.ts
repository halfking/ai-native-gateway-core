// 2026-10-04：本文件此前**整文件没有任何 timeout 声明**，是 *.silentcatch.test.ts 一族里
// 唯一漏掉 { timeout: 20_000 } 的成员（对照 absentVsFailed / turnsResummarizeStale /
// wizardStatusUnknownVsNotActivated 等兄弟文件）。后果不是断言红，而是：
// `Error: Test timed out in 5000ms` —— 全量 187 文件并发时 jsdom 冷启动 +
// transform 把首个 mount 用例推过 vitest 的 5s 默认值；单跑本文件 7/7 全过（4.22s）。
//
// 按第五轮 §30.3 的处方：**只放宽 timeout，判据本身不放宽** —— 三态断言一个字没改。
// filterMeta.silentcatch.test.ts —— 筛选元数据加载失败必须自报家门
//
// ## 挡住的是什么
//
// 2026-10-03 之前，UsersView.loadTenants 与 ModelsView 的
// loadProviders / loadTags / loadFamilies 四处都是静默吞错：
//
//     try { families.value = await listModelFamilies() } catch (e) { /* ignore */ }
//
// 失败后页面**不报错**，而是把筛选下拉渲染成空的。用户看到的是
//「没有家族可选」「只有全部租户」，而真相是「没加载出来」——
// 前者是「知道没有」，后者是「不知道」。两者在页面上完全同形。
//
// ModelsView 更隐蔽一处：families 还喂给 modelVendorName()，
// 于是失败会**静默改写**列表里 vendor 列的取值（退化成无 vendor），
// 连下拉都看不出来。
//
// ## 判据钉住的是什么
//
// 不是「有提示就行」，而是三态可区分 + **失败时不得静默改数据**：
//   ① 加载成功且确实为空   → 无提示
//   ② 加载失败              → 提示，且下拉为空（范围未知要说出来）
//   ③ 加载成功且有数据      → 无提示
//
// 反向对照：把 catch 改回 `{ /* ignore */ }`（即撤掉本轮修复），
// 两条「加载失败」用例会红。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { createI18n } from 'vue-i18n'

const getTenantsAdminMock = vi.fn()
const getProvidersMock = vi.fn()
const listTagsMock = vi.fn()
const listModelFamiliesMock = vi.fn()

// 两个视图都从 '../api' **桶文件**导入（不是 '../api/admin'）。
//
// 第一版我把桩挂在 '../api/admin' 上，于是 vi.mock 完全没生效：
// 组件照常去调真实的桶导出，被 vitest 拦成
// `No "getTenantsAdmin" export is defined on the "../api" mock`。
// 症状是「失败用例红、成功用例也红」——
// 因为成功路径根本没成功过，它拿到的是桩缺失错误。
// ⇒ 量具挂错模块时，**正例与反例会一起红**；只看到反例红不要急着改产品。
vi.mock('../api', () => ({
  // UsersView
  getTenantsAdmin: (...a: unknown[]) => getTenantsAdminMock(...a),
  getUsers: () => Promise.resolve({ items: [], total: 0 }),
  getUserUsageSummary: () => Promise.resolve({ items: [] }),
  createUser: () => Promise.resolve({}),
  updateUser: () => Promise.resolve({}),
  deleteUser: () => Promise.resolve({}),
  resetUserPassword: () => Promise.resolve({}),
  // ModelsView
  listModels: () => Promise.resolve({ items: [], total: 0 }),
  listTags: (...a: unknown[]) => listTagsMock(...a),
  listModelFamilies: (...a: unknown[]) => listModelFamiliesMock(...a),
  getProviders: (...a: unknown[]) => getProvidersMock(...a),
  patchModelTags: () => Promise.resolve({}),
  getModel: () => Promise.resolve({}),
  getFeatured: () => Promise.resolve([]),
  getFeaturedModelsDynamic: () => Promise.resolve([]),
  getModelDiscoveryStatus: () => Promise.resolve({ latest: null, running: null }),
  listModelNameMappings: () => Promise.resolve([]),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))

// 模型清单那张卡挂在 activeTab === 'canonical' 下，而默认页签是
// isPlatformOpsView() ? 'canonical' : 'catalog'。不把 store 打成平台运维态，
// 组件根本不会渲染到那张卡 —— 于是「横幅没出现」看起来像修复没生效，
// 实际是断言量具停在了另一个页签上。
vi.mock('../store', () => ({
  isPlatformOpsView: () => true,
  isReadOnlyMode: () => false,
  store: { get: () => null, set: () => {} },
  clearApiKey: () => {},
  clearAll: () => {},
  authBearer: () => '',
  getLocale: () => 'zh-CN',
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      users: { error: { tenantsLoadFailed: '租户列表加载失败' } },
      models: { error: { filterMetaLoadFailed: '筛选条件加载失败' } },
    },
  },
})

async function renderUsers() {
  const View = (await import('./UsersView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

async function renderModels() {
  const View = (await import('./ModelsView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

describe('UsersView 租户下拉：三态可区分', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.spyOn(console, 'warn').mockImplementation(() => {})
  })

  it('① 加载成功且确实没有租户 → 无失败提示（下拉为空是事实，不是故障）', async () => {
    getTenantsAdminMock.mockResolvedValue([])
    const w = await renderUsers()
    expect(w.find('p.filter-error').exists()).toBe(false)
  })

  it('② 加载失败 → 必须出现提示，且带原始错误信息', async () => {
    // 反向对照：把 catch 退回 `{ /* ignore */ }` 之后，这条会红
    // （页面上不会有任何 .filter-error，而下拉依然是空的）。
    getTenantsAdminMock.mockRejectedValue(new Error('tenants boom'))
    const w = await renderUsers()
    expect(w.find('p.filter-error').exists()).toBe(true)
    expect(w.find('p.filter-error').text()).toContain('tenants boom')
  })

  it('③ 加载成功且有数据 → 无失败提示', async () => {
    getTenantsAdminMock.mockResolvedValue([{ code: 'acme', name: 'Acme', status: 'active' }])
    const w = await renderUsers()
    expect(w.find('p.filter-error').exists()).toBe(false)
  })
})

describe('ModelsView 筛选元数据：三态可区分', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.spyOn(console, 'warn').mockImplementation(() => {})
  })

  it('① 全部成功 → 无失败提示', async () => {
    getProvidersMock.mockResolvedValue([{ id: 1, name: 'p', code: 'p' }])
    listTagsMock.mockResolvedValue({ namespaces: [] })
    listModelFamiliesMock.mockResolvedValue({ items: [] })
    const w = await renderModels()
    expect(w.find('.alert-warning').exists()).toBe(false)
  })

  it('② 家族列表失败 → 出现提示（不是「没有家族可选」）', async () => {
    getProvidersMock.mockResolvedValue([])
    listTagsMock.mockResolvedValue({ namespaces: [] })
    listModelFamiliesMock.mockRejectedValue(new Error('families boom'))
    const w = await renderModels()
    expect(w.find('.alert-warning').exists()).toBe(true)
    expect(w.find('.alert-warning').text()).toContain('families boom')
  })

  it('③ 标签列表失败 → 出现提示', async () => {
    getProvidersMock.mockResolvedValue([])
    listTagsMock.mockRejectedValue(new Error('tags boom'))
    listModelFamiliesMock.mockResolvedValue({ items: [] })
    const w = await renderModels()
    expect(w.find('.alert-warning').exists()).toBe(true)
    expect(w.find('.alert-warning').text()).toContain('tags boom')
  })

  it('④ 供应商列表失败 → 出现提示', async () => {
    getProvidersMock.mockRejectedValue(new Error('providers boom'))
    listTagsMock.mockResolvedValue({ namespaces: [] })
    listModelFamiliesMock.mockResolvedValue({ items: [] })
    const w = await renderModels()
    expect(w.find('.alert-warning').exists()).toBe(true)
    expect(w.find('.alert-warning').text()).toContain('providers boom')
  })
})
