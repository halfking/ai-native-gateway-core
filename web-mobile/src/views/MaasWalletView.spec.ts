import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MaasWalletView from './MaasWalletView.vue'
import { fetchMaasWallet, type MaasWallet } from '@/api/maas'
import { setLocale, locale } from '@/i18n'

/**
 * MaasWalletView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 这条 GET **会写库**（`ensureWalletDirect` 建行）⇒ 页面必须说破。
 * 2. ★★★★ `total_available` 把**订阅额度**与**积分余额**（两种单位）相加。
 * 3. ★★★★ `balance_credits` 是**被兜底顶替**的值，不是原始列。
 * 4. ★★★ `subscription` **键整个不存在** 与「status 不是 active」是两回事。
 * 5. ★★★ 抛错不许退化成「余额 0」——那会让人以为账被清空了。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/maas', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/maas')>()
  return { ...actual, fetchMaasWallet: vi.fn() }
})

const wMock = fetchMaasWallet as unknown as ReturnType<typeof vi.fn>
const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** ★ 抄自 `WalletView`（maas/service.go）—— **裸对象**。 */
function wallet(over: Record<string, unknown> = {}): MaasWallet {
  return {
    tenant_id: 'acme',
    quota_remaining: 0,
    granted_balance: 1000,
    purchased_balance: 500,
    // ★ 后端发出来的样子（列值 0 时已被顶替成 1500）
    balance_credits: 1500,
    total_available: 1500,
    subscription: {
      plan_id: 1,
      plan_name: 'Pro 月付',
      status: 'active',
      period_start: '2026-10-01T00:00:00Z',
      period_end: '2026-11-01T00:00:00Z',
    },
    ...over,
  } as unknown as MaasWallet
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(MaasWalletView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  wMock.mockResolvedValue(wallet())
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ GET 会写库这件事要说破', () => {
  it('★★★★★★ 页面明说「虽然是 GET，但它会写库」', async () => {
    const w = await mountView()
    const notes = w.findAll('.mw__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('会写库') || x.includes('写库'))).toBe(true)
    expect(notes.some((x) => x.includes('ON CONFLICT') || x.includes('建行'))).toBe(true)
  })

  it('★★★★★ 明说只看**本租户**（与 superAdmin 跨租户相反）', async () => {
    const w = await mountView()
    const notes = w.findAll('.mw__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('本租户'))).toBe(true)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 总额把两种单位加在一起', () => {
  it('★★★★ ★ 有订阅额度时总额提示出现，并带上额度数值', async () => {
    wMock.mockResolvedValue(
      wallet({ quota_remaining: 1000, granted_balance: 1000, purchased_balance: 500, total_available: 2500 }),
    )
    const w = await mountView()
    const warns = w.findAll('.mw__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('两种不同单位') || x.includes('不同单位'))).toBe(true)
    expect(warns.some((x) => x.includes('1000'))).toBe(true)
  })

  it('★★★★★ quota = 0 ⇒ 不出该提示（纯积分余额，无混合）', async () => {
    const w = await mountView()
    const warns = w.findAll('.mw__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('不同单位'))).toBe(false)
  })

  it('★★★★ 总额渲染的是后端给的值', async () => {
    const w = await mountView()
    // fmtInt 带千分位分隔
    expect(w.find('.mw__total').text()).toBe('1,500')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ balance_credits 是兜底顶替值', () => {
  it('★★★★ 恰好等于「发放+购买」⇒ 提示可能被顶替', async () => {
    const w = await mountView()
    const notes = w.findAll('.mw__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('不保证是库里的原始值'))).toBe(true)
  })

  it('★★★ 不等于两数和 ⇒ 不出该提示', async () => {
    wMock.mockResolvedValue(wallet({ balance_credits: 9999 }))
    const w = await mountView()
    const notes = w.findAll('.mw__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('不保证是库里的原始值'))).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 「没有订阅」与「订阅不在 active」是两回事', () => {
  it('★★★ `subscription` 键不存在 ⇒ 显示「当前没有生效订阅」', async () => {
    const o = wallet() as unknown as Record<string, unknown>
    delete o.subscription
    wMock.mockResolvedValue(o as unknown as MaasWallet)
    const w = await mountView()
    expect(w.text()).toContain('当前没有生效订阅')
    // ★ 键整个不存在 vs status 不是 active：文案必须**不同**
    expect(w.text()).not.toContain('这两回事')
  })

  it('★★★ 键存在但 status=cancelled ⇒ 另一条文案，且仍显示订阅名', async () => {
    wMock.mockResolvedValue(
      wallet({
        subscription: {
          plan_id: 1,
          plan_name: 'Pro 月付',
          status: 'cancelled',
          period_start: '2026-10-01T00:00:00Z',
          period_end: '2026-11-01T00:00:00Z',
        },
      }),
    )
    const w = await mountView()
    const warns = w.findAll('.mw__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('cancelled'))).toBe(true)
    expect(warns.some((x) => x.includes('两回事'))).toBe(true)
    expect(w.text()).toContain('Pro 月付')
    expect(w.text()).not.toContain('当前没有生效订阅')
  })

  it('★★ status=active ⇒ 渲染订阅名与到期时间，不出警告', async () => {
    const w = await mountView()
    expect(w.find('.mw__title').text()).toBe('Pro 月付')
    expect(w.find('.mw__badge-t').text()).toBe('active')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 错误与只读', () => {
  it('★★★ 抛错**不许**退化成「余额 0」', async () => {
    wMock.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    expect(w.text()).toContain('没有启用 MaaS')
    // ★ 总额那一块**一个都不许**渲染出来
    expect(w.find('.mw__total').exists()).toBe(false)
    expect(w.text()).not.toContain('可用总额')
  })

  it('★★★ 形状不符 ⇒ 显示错误且不渲染钱包', async () => {
    wMock.mockRejectedValue(new Error('maas/public wallet 响应形状不符：…'))
    const w = await mountView()
    expect(w.text()).toContain('形状不符')
    expect(w.find('.mw__grid').exists()).toBe(false)
  })

  it('★★ 明说本页只读、不做 adjust/grant', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只读')
    expect(w.text()).toContain('adjust')
  })
})
