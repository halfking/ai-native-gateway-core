import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import TenantsView from './TenantsView.vue'
import { fetchTenants, tenantUsageMayBeDegraded, type TenantInfo } from '@/api/tenants'
import { setLocale, locale } from '@/i18n'

/**
 * TenantsView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 抽屉席**必须** super_admin（admin/handler.go:926-927）。
 * 2. ★★★★★★ 「近 7 天用量」**可能不可信**（1.5s 独立预算 + 只 slog）
 *    ⇒ 文案必须说「读数不可信」，不能说「这段时间没用量」。
 * 3. ★★★★ 7 个聚合键带 omitempty ⇒ 缺键**不可下结论**。
 * 4. ★★ 响应是**裸数组**（页面不能假设 `{items}`）。
 */

const pushMock = vi.fn()
vi.mock('vue-router', () => ({ useRouter: () => ({ push: pushMock }) }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/tenants', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/tenants')>()
  return { ...actual, fetchTenants: vi.fn() }
})

const mMock = fetchTenants as unknown as ReturnType<typeof vi.fn>
const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function tenant(over: Record<string, unknown> = {}): TenantInfo {
  return {
    code: 'acme',
    name: 'Acme Corp',
    status: 'active',
    description: '',
    contact_email: 'ops@acme.test',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
    ...over,
  } as TenantInfo
}

/** ★★ 富化成功的样子：7 个聚合键**都在**。 */
function richTenant(over: Record<string, unknown> = {}): TenantInfo {
  return tenant({
    user_count: 3,
    api_key_count: 5,
    requests_7d: 100,
    tokens_7d: 3000,
    credits_7d: 500,
    cost_7d_usd: 1.5,
    total_requests: 1000,
    ...over,
  }) as TenantInfo
}

async function mountView(items: TenantInfo[] = [tenant()]): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  mMock.mockResolvedValue(items)
  const w = mount(TenantsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 抽屉席必须是 super_admin', () => {
  it('★★★★★★ /api/admin/tenants 两条注册都是 h.superAdmin', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'tenants')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBe('super_admin')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 7 天用量这一列可能不可信', () => {
  it('★★★★★★ 缺聚合键 ⇒ 明说「读数不可信」，不是「没用量」', async () => {
    const w = await mountView([tenant()])
    const warns = w.findAll('.tn__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('读数不可信'))).toBe(true)
    // ★★★ 不许说成「这段时间没有用量」—— 那是**错误结论**。
    //   注意：只能按**那一行**判读，不能整页 not.toContain('没用量') ——
    //   上面那条降级说明的文案里就带着「真没用量」，会把自己判红。
    const itemWarn = w.find('.tn__item').findAll('.tn__note--warn').map((n) => n.text())
    expect(itemWarn.some((x) => x.includes('不是「这段时间没用量」'))).toBe(true)
    for (const x of itemWarn) expect(x).not.toBe('这段时间没有用量')
  })

  it('★★★★★★ ★ 明说后端只给这一列留了 1.5 秒预算', async () => {
    const w = await mountView([tenant()])
    expect(w.text()).toContain('1.5 秒')
  })

  it('★★★★★ 聚合键齐全 ⇒ 不出该警告，改说「都齐」', async () => {
    const w = await mountView([richTenant()])
    const warns = w.findAll('.tn__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('读数不可信'))).toBe(false)
    expect(w.text()).toContain('都齐')
  })

  it('★★★★★ 部分缺（只有 credits）⇒ 仍判可能降级', async () => {
    const partial = tenant({ credits_7d: 500, cost_7d_usd: 1.5 })
    expect(tenantUsageMayBeDegraded(partial)).toBe(true)
    const w = await mountView([partial])
    const warns = w.findAll('.tn__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('读数不可信'))).toBe(true)
  })

  it('★★★ 混合：一行有、一行没有 ⇒ 只标那一行 + 表头给出总数', async () => {
    const w = await mountView([tenant({ code: 'ok', user_count: 1, api_key_count: 1, requests_7d: 5, tokens_7d: 5, credits_7d: 5, cost_7d_usd: 1, total_requests: 5 }), tenant({ code: 'bad' })])
    expect(w.text()).toContain('1 个租户')
    const items = w.findAll('.tn__item')
    expect(items).toHaveLength(2)
    expect(items[0]!.findAll('.tn__note--warn')).toHaveLength(0)
    expect(items[1]!.findAll('.tn__note--warn').length).toBeGreaterThan(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 缺键渲染成 0，但不下结论', () => {
  it('★★★★ 聚合键缺失 ⇒ 渲染成 0（不是空白、不是「—」）', async () => {
    const w = await mountView([tenant()])
    const cells = w.findAll('.tn__cell-v').map((c) => c.text())
    expect(cells).toEqual(['0', '0', '0', '0'])
  })

  it('★★★★ 聚合键齐全 ⇒ 渲染出真实值', async () => {
    const w = await mountView([richTenant()])
    const cells = w.findAll('.tn__cell-v').map((c) => c.text())
    expect(cells).toEqual(['3', '5', '100', '500'])
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 裸数组与筛选', () => {
  it('★★★ 明说「返回的是裸数组」，且键为 0 时会消失', async () => {
    const w = await mountView()
    expect(w.text()).toContain('裸数组')
  })

  it('★★★ 切状态筛选会重新请求并带上 status', async () => {
    const w = await mountView()
    await w.findAll('.tn__seg-btn')[1]!.trigger('click') // active
    await flushPromises()
    expect(mMock).toHaveBeenLastCalledWith({ status: 'active' })
  })

  it('★★ 搜索走本地（不发第二次请求）', async () => {
    const w = await mountView([tenant(), richTenant({ code: 'globex', name: 'Globex' })])
    await w.find('#tn-kw').setValue('globex')
    await flushPromises()
    expect(mMock).toHaveBeenCalledTimes(1)
    expect(w.findAll('.tn__item')).toHaveLength(1)
  })

  it('★★★ 503 ⇒ 明说「没启用」且不渲染名单', async () => {
    mMock.mockRejectedValue(new Error('database not configured'))
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(TenantsView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('没有启用 MaaS')
    expect(w.text()).not.toContain('Acme Corp')
    expect(w.find('.tn__list').exists()).toBe(false)
  })

  it('★★ 明说本页只读、不做创建/修改', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只读')
    expect(w.text()).toContain('PATCH')
  })
})