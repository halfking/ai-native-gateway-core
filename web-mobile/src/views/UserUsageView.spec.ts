import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createRouter, createMemoryHistory } from 'vue-router'
import UserUsageView from './UserUsageView.vue'
import { fetchManagedUsers } from '@/api/users'
import { fetchUserStats, fetchUserUsageSummary } from '@/api/userUsageStats'
import { createKey } from '@/api/keys'
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
