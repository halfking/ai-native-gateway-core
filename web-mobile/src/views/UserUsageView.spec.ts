import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import UserUsageView from './UserUsageView.vue'
import { fetchManagedUsers } from '@/api/users'
import { fetchUserStats, fetchUserUsageSummary } from '@/api/userUsageStats'
import { createKey } from '@/api/keys'
import { fetchTenants, fetchTenantStats, fetchTenantUsers } from '@/api/tenants'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'

/**
 * 用户下创建 API Key 入口（2026-10-10，第二百零一批）。
 *
 * 任务原始需求：
 *   「要求能够通过租户、用户、apikey 查看其用量，可以在用户名下添加 apikey」
 *
 * 实现：UserUsageView 在用户详情页 toolbar 加「创建密钥」按钮 → AppSheet
 * 抽屉 → 复用 createKey + owner_user 字段。
 *
 * ★ 判据：以下任一被破，本组用例全红。
 *  (1) toolbar 看不到「创建密钥」按钮 —— 入口消失
 *  (2) 抽屉里「application_code」留空时，submit 拒绝本地校验（不调 API）
 *  (3) submit 时传给 createKey 的 payload **必须**带 `owner_user = 当前用户名`，
 *      否则后端这条 key 不会被绑到该用户身上 ⇒ 需求「在用户名下添加」落空
 *  (4) submit 后必须展示明文 `api_key`（13 §4：揭示结果不缓存，但本次必显示）
 *  (5) 成功后必须 reload 当前详情（loadDetail）让 `top_keys` 列表刷新
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/users', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/users')>()
  return { ...actual, fetchManagedUsers: vi.fn() }
})

vi.mock('@/api/userUsageStats', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/userUsageStats')>()
  return { ...actual, fetchUserStats: vi.fn(), fetchUserUsageSummary: vi.fn() }
})

vi.mock('@/api/keys', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/keys')>()
  return { ...actual, createKey: vi.fn() }
})

// 2026-10-13：默认窗口 30→7 的判据要能走到「租户列表 + 展开租户」那条路，
// 而 mountView() 落在 /users/:id（详情页）不经过 tenants.ts ⇒ 这里单独 mock。
vi.mock('@/api/tenants', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/tenants')>()
  return { ...actual, fetchTenants: vi.fn(), fetchTenantStats: vi.fn(), fetchTenantUsers: vi.fn() }
})

const MANAGED_USERS = [
  { id: 7, tenant_id: 'tenant-a', username: 'alice', display_name: 'Alice', email: 'a@x', role: 'user', enabled: true, must_change_password: false, last_login_at: null, created_at: '2026-10-01T00:00:00Z' },
]

const SUMMARY = { days: 30, items: [{ username: 'alice', requests: 10, tokens: 1000, credits: 0, last_active_at: null }] }

const DETAIL = {
  user_id: 7, username: 'alice', days: 30,
  kpi: { requests: 10, tokens: 1000, credits: 0, errors: 0, error_rate: 0.5, latency_p95_ms: null, cost_usd: 12.5 },
  daily: [],
  top_models: [],
  top_apps: [],
  top_keys: [],
  recent_requests: [],
  key_count: 0,
}

let mounted: Array<{ unmount(): void }> = []

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', name: 'home', component: { template: '<div />' } },
      { path: '/users', name: 'users', component: { template: '<div />' } },
      { path: '/users/:id', name: 'user-usage', component: { template: '<div />' } },
    ],
  })
}

async function mountView() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const router = makeRouter()
  await router.push('/users/7')
  await router.isReady()
  useAuthStore().userInfo = {
    id: 1, tenant_id: 'default', username: 'admin', display_name: 'A',
    email: 'a@x', role: 'super_admin', enabled: true,
  }
  const w = mount(UserUsageView, { attachTo: document.body, global: { plugins: [pinia, router] } })
  mounted.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  vi.mocked(fetchManagedUsers).mockResolvedValue(MANAGED_USERS)
  vi.mocked(fetchUserUsageSummary).mockResolvedValue(SUMMARY)
  vi.mocked(fetchUserStats).mockResolvedValue(DETAIL)
})

afterEach(() => {
  for (const m of mounted) m.unmount()
  mounted = []
  vi.clearAllMocks()
})

describe('UserUsageView 用户下创建 API Key 入口', () => {
  it('toolbar 显示「创建密钥」按钮（中文 locale）', async () => {
    const w = await mountView()
    const btnLabel = t('keys.create')
    const btns = w.findAll('button.btn--primary')
    const createBtn = btns.find((b) => b.text().includes(btnLabel))
    expect(createBtn, `toolbar 应有「${btnLabel}」按钮`).toBeDefined()
  })

  it('appCode 留空提交时**本地拒绝**，不调 createKey', async () => {
    const w = await mountView()
    const toolbarBtn = w.findAll('button.btn--primary').find((b) => b.text().includes(t('keys.create')))!
    await toolbarBtn.trigger('click')
    await flushPromises()
    // 抽屉 Teleport 到 body，wrapper.findAll 看不到；走原生 selector。
    const appInput = document.querySelector<HTMLInputElement>('.user-usage__create input[type="text"]')
    expect(appInput).toBeTruthy()
    appInput!.value = ''
    appInput!.dispatchEvent(new Event('input'))
    await flushPromises()
    const submitBtn = document.querySelector<HTMLButtonElement>('.user-usage__create button.btn--primary')!
    submitBtn.dispatchEvent(new Event('click'))
    await flushPromises()
    expect(vi.mocked(createKey)).not.toHaveBeenCalled()
    expect(document.body.textContent ?? '').toContain(t('keys.form.appNameRequired'))
  })

  it('submit 时必须把 owner_user 绑给当前用户', async () => {
    vi.mocked(createKey).mockResolvedValue({
      id: 99, api_key: 'sk-alice-new', key_prefix: 'sk-alice', application_code: 'mobile-debug', message: 'created',
    })
    const w = await mountView()
    const toolbarBtn = w.findAll('button.btn--primary').find((b) => b.text().includes(t('keys.create')))!
    await toolbarBtn.trigger('click')
    await flushPromises()
    // AppSheet 通过 Teleport to body 渲染，submit 按钮在抽屉里（不是 wrapper 子树）。
    // 直接走原生 selector：抽屉里的「创建」按钮在 .user-usage__create 内。
    const submitBtn = document.querySelector<HTMLButtonElement>('.user-usage__create button.btn--primary')
    expect(submitBtn, '抽屉里的「创建」按钮应可见').toBeTruthy()
    submitBtn!.dispatchEvent(new Event('click'))
    await flushPromises()
    await flushPromises()
    expect(vi.mocked(createKey)).toHaveBeenCalledTimes(1)
    const payload = vi.mocked(createKey).mock.calls[0]?.[0] as { owner_user?: string; tenant_id?: string } | undefined
    expect(payload?.owner_user).toBe('alice')
    expect(payload?.tenant_id).toBe('tenant-a')
  })

  it('成功后展示明文 api_key 与复制按钮', async () => {
    vi.mocked(createKey).mockResolvedValue({
      id: 99, api_key: 'sk-alice-new-very-secret', key_prefix: 'sk-alice', application_code: 'mobile-debug', message: 'created',
    })
    const w = await mountView()
    const toolbarBtn = w.findAll('button.btn--primary').find((b) => b.text().includes(t('keys.create')))!
    await toolbarBtn.trigger('click')
    await flushPromises()
    const submitBtn = document.querySelector<HTMLButtonElement>('.user-usage__create button.btn--primary')!
    submitBtn.dispatchEvent(new Event('click'))
    await flushPromises()
    await flushPromises()
    // 明文必须出现
    expect(document.body.textContent ?? '').toContain('sk-alice-new-very-secret')
    // 复制按钮可见（common.copy）
    const copyBtn = Array.from(document.body.querySelectorAll('button')).find((b) =>
      (b.textContent ?? '').includes(t('common.copy'))
    )
    expect(copyBtn).toBeTruthy()
  })

  it('创建后必须 reload 当前详情让 top_keys 刷新', async () => {
    vi.mocked(createKey).mockResolvedValue({
      id: 99, api_key: 'sk-x', key_prefix: 'sk-x', application_code: 'm', message: 'ok',
    })
    const callsBefore = vi.mocked(fetchUserStats).mock.calls.length
    const w = await mountView()
    const toolbarBtn = w.findAll('button.btn--primary').find((b) => b.text().includes(t('keys.create')))!
    await toolbarBtn.trigger('click')
    await flushPromises()
    const submitBtn = document.querySelector<HTMLButtonElement>('.user-usage__create button.btn--primary')!
    submitBtn.dispatchEvent(new Event('click'))
    await flushPromises()
    await flushPromises()
    expect(vi.mocked(fetchUserStats).mock.calls.length).toBeGreaterThan(callsBefore)
  })
})

describe('UserUsageView audit regressions', () => {
  it('renders a fractional error rate as a percentage and uses the complete-window cost', async () => {
    const w = await mountView()
    expect(w.text()).toContain('50.00%')
    expect(w.text()).toContain('12.50')
  })
  it('discards an older detail response after selecting a different user', async () => {
    let resolve!: (value: typeof DETAIL) => void
    vi.mocked(fetchUserStats).mockReturnValueOnce(new Promise((done) => { resolve = done }))
    const w = await mountView()
    vi.mocked(fetchUserStats).mockResolvedValueOnce({ ...DETAIL, user_id: 8, username: 'bob', kpi: { ...DETAIL.kpi, cost_usd: 99 } })
    await w.vm.$router.push('/users/8')
    await flushPromises()
    resolve(DETAIL)
    await flushPromises()
    expect(w.text()).toContain('99.00')
    expect(w.text()).not.toContain('12.50')
  })
  it('does not restore a secret after its creation sheet closes', async () => {
    let resolve!: (value: Awaited<ReturnType<typeof createKey>>) => void
    vi.mocked(createKey).mockReturnValueOnce(new Promise((done) => { resolve = done }))
    const w = await mountView()
    await w.findAll('button.btn--primary').find((b) => b.text().includes(t('keys.create')))!.trigger('click')
    await flushPromises()
    document.querySelector<HTMLButtonElement>('.user-usage__create button.btn--primary')!.click()
    await flushPromises()
    w.findComponent({ name: 'AppSheet' }).vm.$emit('update:modelValue', false)
    await flushPromises()
    resolve({ id: 99, api_key: 'sk-late-secret', key_prefix: 'sk-late', application_code: 'alice', message: 'ok' })
    await flushPromises()
    await w.findAll('button.btn--primary').find((b) => b.text().includes(t('keys.create')))!.trigger('click')
    await flushPromises()
    expect(document.body.textContent).not.toContain('sk-late-secret')
  })
})

/**
 * 默认统计窗口必须是 7 天（2026-10-13 实测修正的缺陷判据）。
 *
 * 缺陷本体：`days` 初始值是 30，而 30 天窗在真库上**确定性**失败——
 *   GET /api/admin/tenants/default/stats?days=30 → 504 / 10.09s / 10.10s
 *   GET …?days=7                                  → 200 / 2.74s / 4.27s
 * ⇒ 移动端「用户用量」页一进来展开任一租户，就得到一条红色超时提示，
 *   租户级 KPI（请求/Token/上游成本/积分）全部拿不到。
 *
 * 权威依据有三处，缺一不可：
 *   · 后端自身缺省 7（admin/tenants.go `days < 1 ⇒ 7`）
 *   · Web 端 TenantDetailView.statsDays = ref(7)
 *   · 端点能力边界：30/90 超预算后走既有 504 契约，提示「缩短统计天数后重试」
 *
 * ★ 钉住什么：默认档位是 7；展开租户时**真的**带着 days:7 去请求。
 * ★ 钉不住什么：钉不住「7 天一定快」——那取决于库上的数据量，
 *   所以另有端到端读数（见上）作第二重判据。单测不能替代它。
 */
describe('UserUsageView 默认统计窗口 = 7 天', () => {
  const TENANTS = [
    { code: 'default', name: '默认租户', status: 'active', description: '', contact_email: '', created_at: '', updated_at: '', user_count: 15 },
  ]

  async function mountList() {
    const pinia = createPinia()
    setActivePinia(pinia)
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/users', component: { template: '<div/>' } }],
    })
    await router.push('/users')
    await router.isReady()
    useAuthStore().userInfo = {
      id: 1, tenant_id: 'default', username: 'admin', display_name: 'A',
      email: 'a@x', role: 'super_admin', enabled: true,
    }
    vi.mocked(fetchTenants).mockResolvedValue(TENANTS as never)
    vi.mocked(fetchTenantStats).mockResolvedValue({
      days: 7, total_requests: 5, total_tokens: 500, total_credits: 3, total_cost_usd: 1.5,
      unique_keys: 1, unique_models: 1, unique_apps: 1, input_tokens: 1, output_tokens: 2,
      cache_read_tokens: 0, cache_write_tokens: 0, avg_latency_ms: 10,
      by_model: [], by_application: [], daily: [],
    } as never)
    vi.mocked(fetchTenantUsers).mockResolvedValue([] as never)
    const w = mount(UserUsageView, { attachTo: document.body, global: { plugins: [pinia, router] } })
    mounted.push(w)
    await flushPromises()
    await flushPromises()
    return w
  }

  it('下拉框落在 7，而不是 30', async () => {
    const w = await mountList()
    const sel = w.find('select').element as HTMLSelectElement
    expect(sel.value, '默认统计窗口应为 7').toBe('7')
    // 防「只改了 value 没真绑上 v-model」：7 必须在选项表里存在
    expect([...sel.options].map((o) => o.value)).toContain('7')
  })

  it('展开租户时请求必须带 days=7', async () => {
    const w = await mountList()
    const head = w.find('.user-usage__tenant-head')
    expect(head.exists(), '租户分组应渲染').toBe(true)
    await head.trigger('click')
    await flushPromises()
    expect(vi.mocked(fetchTenantStats)).toHaveBeenCalled()
    expect(vi.mocked(fetchTenantStats).mock.calls[0]?.[1]).toEqual({ days: 7 })
  })
})
