import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MaasCatalogView from './MaasCatalogView.vue'
import {
  fetchMaasPublicSettings,
  fetchMaasPublicModels,
  fetchMaasPublicPlans,
  fetchMaasPublicTopupPackages,
} from '@/api/maas'
import { setLocale, locale } from '@/i18n'

/**
 * MaasCatalogView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 档位是 **admin**（`/api/maas/**` = h.admin），**不是** superAdmin
 *    ⇒ 抽屉席不设 requiresRole。接线判据钉住「不许误设成 super_admin」。
 * 2. ★★★★★ `/api/maas/models` **只有 4 维** ⇒ 页面不许画七维。
 * 3. ★★★★ 模态的「盖章 / 猜测」在响应里分不出来 ⇒ 页面不许声称那是配置值。
 * 4. ★★★ settings 只有 3 键 ⇒ 页面不许渲染折扣 / `_in` 基价。
 * 5. ★★★ plans / topup 只列 enabled。
 * 6. ★★ 503 不许退化成「没有套餐」。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/maas', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/maas')>()
  return {
    ...actual,
    fetchMaasPublicSettings: vi.fn(),
    fetchMaasPublicModels: vi.fn(),
    fetchMaasPublicPlans: vi.fn(),
    fetchMaasPublicTopupPackages: vi.fn(),
  }
})

const sMock = fetchMaasPublicSettings as unknown as ReturnType<typeof vi.fn>
const mMock = fetchMaasPublicModels as unknown as ReturnType<typeof vi.fn>
const pMock = fetchMaasPublicPlans as unknown as ReturnType<typeof vi.fn>
const tpMock = fetchMaasPublicTopupPackages as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function publicSettings(over: Record<string, unknown> = {}) {
  return { cents_per_credit: 0.1, base_credits_per_1m: 10000, currency_display: 'CNY', ...over }
}

function publicModel(over: Record<string, unknown> = {}) {
  return {
    canonical_name: 'gpt-4o',
    display_name: 'GPT-4o',
    vendor: 'OpenAI',
    family: null,
    family_display_name: null,
    context_window: 128000,
    modality: 'multimodal',
    billing_mode: 'token',
    credits_per_1m_in: 10000,
    credits_per_1m_out: 30000,
    credits_per_1m_cache_in: 1000,
    credits_per_1m_cache_out: 1250,
    ...over,
  }
}

function plan(over: Record<string, unknown> = {}) {
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
  }
}

function topup(over: Record<string, unknown> = {}) {
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
  }
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(MaasCatalogView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  sMock.mockResolvedValue(publicSettings())
  mMock.mockResolvedValue({ items: [publicModel()] })
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
describe('★★★★★★ 档位是 admin，不是 superAdmin', () => {
  it('★★★★★★ 抽屉席**不许**误设成 super_admin', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'maas-catalog')
    expect(seat).toBeDefined()
    // ★ `/api/maas/**` 是 h.admin(...)（maas_handlers.go:26-30）
    //   ⇒ 设成 super_admin 会让 tenant_admin 点进来就 403。
    expect(seat?.requiresRole).toBeUndefined()
  })

  it('★★★★★ 钱包席同样是 admin 档', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'maas-wallet')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBeUndefined()
  })

  it('★★★★★ 四条端点都被调用（不是只调一条就渲染）', async () => {
    await mountView()
    expect(sMock).toHaveBeenCalledTimes(1)
    expect(mMock).toHaveBeenCalledTimes(1)
    expect(pMock).toHaveBeenCalledTimes(1)
    expect(tpMock).toHaveBeenCalledTimes(1)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 只有 4 维这件事必须渲染出来', () => {
  it('★★★★★★ 明说「图像/音频/视频三维的键根本不存在」', async () => {
    const w = await mountView()
    const warns = w.findAll('.mp__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('只有 4 维'))).toBe(true)
    expect(warns.some((x) => x.includes('不存在'))).toBe(true)
  })

  it('★★★★★★ ★ 页面上**只有 4 行**维度，绝不出现 image/audio/video', async () => {
    const w = await mountView()
    // 1 个模型 × 4 维
    expect(w.findAll('.mp__table tbody tr')).toHaveLength(4)
    const rows = w.findAll('.mp__table tbody tr').map((r) => r.text())
    for (const r of rows) {
      expect(r).not.toContain('图像')
      expect(r).not.toContain('音频')
      expect(r).not.toContain('视频')
    }
  })

  it('★★★★★ 页面**不许**说「这几维免费 / 是 0」', async () => {
    const w = await mountView()
    const warns = w.findAll('.mp__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('不是「免费」'))).toBe(true)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 模态不可验证', () => {
  it('★★★★ 明说「分不出是盖章还是猜的」', async () => {
    const w = await mountView()
    const notes = w.findAll('.mp__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('分不出'))).toBe(true)
  })

  it('★★★★★ 不许把模态标成「配置好的」', async () => {
    const w = await mountView()
    const notes = w.findAll('.mp__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('不要') && x.includes('配置好的模态'))).toBe(true)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ settings 只有 3 键', () => {
  it('★★★★ 明说「折扣和输入基价不在这条端点上」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只返回 3 个键')
    expect(w.text()).toContain('不在这条端点上')
  })

  it('★★★★★ 渲染的是**旧字段**基价并标出来', async () => {
    const w = await mountView()
    expect(w.text()).toContain('基价（旧字段）')
    expect(w.text()).toContain('可能不是生效基价')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 套餐与充值包', () => {
  it('★★★ 金额从**分**换算成元', async () => {
    const w = await mountView()
    // 9900 分 ⇒ ¥99.00
    expect(w.find('.mp__price').text()).toBe('¥99.00')
  })

  it('★★★ 积分为 0 时**不显示**单价（而不是 Infinity / NaN）', async () => {
    for (const ww of mountedList) ww.unmount()
    mountedList = []
    document.body.innerHTML = ''
    pMock.mockResolvedValue({ items: [plan({ monthly_credits: 0 })] })
    const w = await mountView()
    // ★ 按面板取值：`mp__unit` 是全页类名，充值包那边也会有一个，
    //   全页 exists() 判据会被**另一块面板的合法行**喂饱。
    const plansPanel = w.findAll('.mp__panel').find((p) => p.text().includes('订阅套餐'))!
    expect(plansPanel.exists()).toBe(true)
    expect(plansPanel.find('.mp__unit').exists()).toBe(false)
    // 对照：充值包那一块（积分为 1000）**仍然**有单价行
    const topupPanel = w.findAll('.mp__panel').find((p) => p.text().includes('充值包'))!
    expect(topupPanel.find('.mp__unit').exists()).toBe(true)
    expect(w.text()).not.toContain('Infinity')
    expect(w.text()).not.toContain('NaN')
  })

  it('★★★ 明说「只列已启用」', async () => {
    const w = await mountView()
    expect(w.findAll('.mp__note').filter((n) => n.text().includes('只列已启用')).length).toBeGreaterThan(0)
  })

  it('★★ 空清单 ⇒ 空态（**不是**「没配过价」）', async () => {
    pMock.mockResolvedValue({ items: [] })
    tpMock.mockResolvedValue({ items: [] })
    const w = await mountView()
    expect(w.text()).toContain('没有已启用的套餐')
    expect(w.text()).toContain('没有已启用的充值包')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 错误处理', () => {
  it('★★★ 503 ⇒ 明说「MaaS 没启用」，且**不渲染**任何面板', async () => {
    sMock.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    expect(w.text()).toContain('没有启用 MaaS')
    // ★ 整页进错误态：四个面板**一个都不许**留下
    expect(w.findAll('.mp__panel')).toHaveLength(1)
    expect(w.text()).not.toContain('订阅套餐')
    expect(w.text()).not.toContain('Pro 月付')
  })

  it('★★ 形状不符（抛错）也不许静默降级成空清单', async () => {
    mMock.mockRejectedValue(new Error('maas/public models 响应形状不符：…'))
    const w = await mountView()
    expect(w.text()).toContain('形状不符')
    expect(w.text()).not.toContain('GPT-4o')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 搜索', () => {
  it('★★ 搜索走本地（不发第二次请求）', async () => {
    const w = await mountView()
    await w.find('#mp-kw').setValue('gpt')
    await flushPromises()
    expect(mMock).toHaveBeenCalledTimes(1)
    expect(w.findAll('.mp__item').length).toBeGreaterThan(0)
  })
})
