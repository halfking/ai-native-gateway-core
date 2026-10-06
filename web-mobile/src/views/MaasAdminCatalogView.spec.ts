import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MaasAdminCatalogView from './MaasAdminCatalogView.vue'
import {
  fetchMaasSettings,
  fetchMaasAdminPlans,
  fetchMaasAdminTopupPackages,
  type MaasSettings,
  type MaasPlan,
  type MaasTopupPackage,
} from '@/api/maas'
import { setLocale, locale } from '@/i18n'

/**
 * MaasAdminCatalogView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 抽屉席**必须** super_admin（/api/admin/maas/** 全 superAdmin）。
 * 2. ★★★★★ `global_discount` 配 0 ⇒ 明说「不打折」不是「全免」。
 * 3. ★★★★ 硬编码 10000 不是「没配」。
 * 4. ★★★ admin 档**含停用行**，且必须标出停用（租户那边根本列不出来）。
 * 5. ★★★ 明说折扣与基价是**租户看不到**的成本数据。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/maas', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/maas')>()
  return {
    ...actual,
    fetchMaasSettings: vi.fn(),
    fetchMaasAdminPlans: vi.fn(),
    fetchMaasAdminTopupPackages: vi.fn(),
  }
})

const sMock = fetchMaasSettings as unknown as ReturnType<typeof vi.fn>
const pMock = fetchMaasAdminPlans as unknown as ReturnType<typeof vi.fn>
const tpMock = fetchMaasAdminTopupPackages as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** ★ 抄自 `Settings`（12 键、无 omitempty）。 */
function settings(over: Record<string, unknown> = {}): MaasSettings {
  return {
    cents_per_credit: 0.1,
    base_credits_per_1m: 0,
    base_credits_per_1m_in: 0,
    base_credits_per_1m_out: 0,
    base_credits_per_1m_cache_in: 0,
    base_credits_per_1m_cache_out: 0,
    global_discount: 1,
    currency_display: 'CNY',
    alipay_account: 'ops@example.com',
    wechat_mch_id: '1900000109',
    stub_alipay_qr_url: '',
    stub_wechat_qr_url: '',
    ...over,
  } as unknown as MaasSettings
}

function plan(over: Record<string, unknown> = {}): MaasPlan {
  return {
    id: 1,
    code: 'pro-monthly',
    tier: 'pro',
    name: 'Pro 月付',
    price_cents: 9900,
    monthly_credits: 500000,
    enabled: true,
    sort_order: 10,
    ...over,
  } as unknown as MaasPlan
}

function topup(over: Record<string, unknown> = {}): MaasTopupPackage {
  return {
    id: 2,
    code: 'pack-1k',
    tier: 'basic',
    name: '1000 积分包',
    price_cents: 1000,
    credits_amount: 1000,
    enabled: true,
    sort_order: 20,
    ...over,
  } as unknown as MaasTopupPackage
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(MaasAdminCatalogView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  sMock.mockResolvedValue(settings())
  pMock.mockResolvedValue({ items: [plan()] })
  tpMock.mockResolvedValue({ items: [topup()] })
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
  it('★★★★★★ 席必须设 requiresRole: super_admin', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    expect(DRAWER_NAV.find((i) => i.key === 'maas-admin-catalog')?.requiresRole).toBe('super_admin')
  })

  it('★★ 三条端点都被调用', async () => {
    await mountView()
    expect(sMock).toHaveBeenCalledTimes(1)
    expect(pMock).toHaveBeenCalledTimes(1)
    expect(tpMock).toHaveBeenCalledTimes(1)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 折扣 0 不是全免', () => {
  it('★★★★★ global_discount = 0 ⇒ 明说「配 0 不等于全免」', async () => {
    sMock.mockResolvedValue(settings({ global_discount: 0 }))
    const w = await mountView()
    const warns = w.findAll('.mac__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('不等于全免') || x.includes('全免'))).toBe(true)
  })

  it('★★★★★ global_discount = 1 ⇒ 不出该提示', async () => {
    const w = await mountView()
    const warns = w.findAll('.mac__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('全免'))).toBe(false)
  })

  it('★★★ global_discount = 1.5（越界）⇒ 同样归一成「不打折」', async () => {
    sMock.mockResolvedValue(settings({ global_discount: 1.5 }))
    const w = await mountView()
    const warns = w.findAll('.mac__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('全免'))).toBe(true)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 硬编码基价不是「没配」', () => {
  it('★★★ 两个基价键都是 0 ⇒ 明说回落到了硬编码值', async () => {
    const w = await mountView()
    const warns = w.findAll('.mac__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('硬编码'))).toBe(true)
  })

  it('★★★ 明说硬编码末位是 10000', async () => {
    const w = await mountView()
    expect(w.text()).toContain('10000')
  })

  it('★★ 配了 `_in` ⇒ 不出「硬编码」警告', async () => {
    sMock.mockResolvedValue(settings({ base_credits_per_1m_in: 12000 }))
    const w = await mountView()
    const warns = w.findAll('.mac__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('硬编码'))).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ admin 档含停用行，且必须标出来', () => {
  it('★★★ 停用行照常渲染 + 打「已停用」标签 + 整行压暗', async () => {
    pMock.mockResolvedValue({ items: [plan(), plan({ id: 9, code: 'legacy', name: '旧套餐', enabled: false })] })
    const w = await mountView()
    const tags = w.findAll('.mac__tag--warn')
    expect(tags.length).toBeGreaterThan(0)
    expect(tags.some((t) => t.text() === '已停用')).toBe(true)
    expect(w.findAll('.mac__item--off').length).toBeGreaterThan(0)
  })

  it('★★★ 明说「含停用行」并给出停用条数', async () => {
    pMock.mockResolvedValue({ items: [plan(), plan({ id: 9, code: 'legacy', enabled: false })] })
    const w = await mountView()
    const warns = w.findAll('.mac__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('含停用行'))).toBe(true)
    expect(warns.some((x) => x.includes('租户那边根本列不出来'))).toBe(true)
  })

  it('★★ 全部启用 ⇒ 停用计数为 0', async () => {
    const w = await mountView()
    const warns = w.findAll('.mac__note--warn').map((n) => n.text())
    // 默认夹具是 1 条套餐 + 1 条充值包 ⇒ 两块都应是「1 条里 0 条已停用」
    expect(warns.filter((x) => x.includes('1 条里 0 条已停用')).length).toBe(2)
  })

  it('★★ 充值包那一侧同样标停用', async () => {
    tpMock.mockResolvedValue({ items: [topup({ id: 3, code: 'old', enabled: false })] })
    const w = await mountView()
    expect(w.findAll('.mac__tag--warn').length).toBeGreaterThan(0)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 租户看不到的成本数据 + 支付配置', () => {
  it('★★ 明说折扣与基价租户看不到', async () => {
    const w = await mountView()
    expect(w.text()).toContain('租户那边看不到')
  })

  it('★★ 沙箱二维码区分「已配置 / 未配置」', async () => {
    const w = await mountView()
    const cells = w.findAll('.mac__cell-v').map((c) => c.text())
    expect(cells.filter((x) => x === '未配置').length).toBe(2)

    for (const ww of mountedList) ww.unmount()
    mountedList = []
    document.body.innerHTML = ''
    sMock.mockResolvedValue(
      settings({ stub_alipay_qr_url: 'https://qr.example/a', stub_wechat_qr_url: 'https://qr.example/w' }),
    )
    const w2 = await mountView()
    expect(w2.findAll('.mac__cell-v').filter((c) => c.text() === '已配置').length).toBe(2)
  })

  it('★★ 明说本页只读、不做 settings PUT', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只读')
    expect(w.text()).toContain('PUT /api/admin/maas/settings')
  })

  it('★★★ 503 ⇒ 明说「MaaS 没启用」且不渲染面板内容', async () => {
    sMock.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    expect(w.text()).toContain('没有启用 MaaS')
    expect(w.text()).not.toContain('Pro 月付')
  })
})