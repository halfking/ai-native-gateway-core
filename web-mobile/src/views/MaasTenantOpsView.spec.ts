import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MaasTenantOpsView from './MaasTenantOpsView.vue'
import {
  fetchMaasTenantAccount,
  fetchMaasUsageSummary,
  fetchMaasConsumptionDetail,
  fetchMaasTenantLedger,
  type MaasTenantAccount,
  type MaasUsageSummary,
  type MaasConsumptionDetail,
  type MaasLedgerResponse,
  type MaasConsumptionRow,
  type MaasLedgerEntry,
  type MaasOrder,
  type MaasWallet,
} from '@/api/maas'
import { setLocale, locale } from '@/i18n'

/**
 * MaasTenantOpsView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ **days 决定读哪张物理表**（≤7 热表 / >7 当月合并表）
 *    ⇒ 页面必须显式标出，且跨过 7 时要点破「换了数据源」。
 * 2. ★★★★★ 抽屉席**必须**是 super_admin（这一族全 h.superAdmin）。
 * 3. ★★★★ 收入是**算出来的** ⇒ 复算对不上要标出来。
 * 4. ★★★★ 零收入 ⇒ 毛利率**无定义**，不许渲染成「零毛利」。
 * 5. ★★★★ `cost_usd` 键缺失（=0）⇒ 显示 0，不是「—」。
 * 6. ★★★ 「已计费但被客户端取消」非零要说破。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/maas', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/maas')>()
  return {
    ...actual,
    fetchMaasTenantAccount: vi.fn(),
    fetchMaasUsageSummary: vi.fn(),
    fetchMaasConsumptionDetail: vi.fn(),
    fetchMaasTenantLedger: vi.fn(),
  }
})

const aMock = fetchMaasTenantAccount as unknown as ReturnType<typeof vi.fn>
const sMock = fetchMaasUsageSummary as unknown as ReturnType<typeof vi.fn>
const dMock = fetchMaasConsumptionDetail as unknown as ReturnType<typeof vi.fn>
const lMock = fetchMaasTenantLedger as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function wallet(over: Record<string, unknown> = {}): MaasWallet {
  return {
    tenant_id: 'acme',
    quota_remaining: 0,
    granted_balance: 1000,
    purchased_balance: 500,
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

function order(): MaasOrder {
  return {
    id: 7,
    order_no: 'ORD-0007',
    tenant_id: 'acme',
    order_type: 'subscribe',
    status: 'paid',
    amount_cents: 1200,
    credits: 500,
    plan_id: 3,
    payment_channel: 'alipay',
    qr_payload: '',
    qr_url: '',
    paid_at: '2026-10-01T02:03:04Z',
    expires_at: '2026-11-01T00:00:00Z',
    note: '',
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T02:03:04Z',
    plan_name: 'Pro',
  } as unknown as MaasOrder
}

function account(): MaasTenantAccount {
  return {
    wallet: wallet(),
    recent_ledger: [ledgerEntry()],
    recent_orders: [order()],
  }
}

function ledgerEntry(over: Record<string, unknown> = {}): MaasLedgerEntry {
  return {
    id: 1,
    entry_type: 'charge',
    amount: -100,
    balance_after: 900,
    pool: 'purchased',
    ref_type: 'order',
    ref_id: 'ORD-7',
    note: '',
    created_at: '2026-10-01T00:00:00Z',
    ...over,
  } as unknown as MaasLedgerEntry
}

function summary(over: Record<string, unknown> = {}): MaasUsageSummary {
  return {
    days: 7,
    tenant_id: 'acme',
    total_requests: 100,
    total_credits: 5000,
    total_cost_usd: 12.5,
    by_model: [{ model: 'gpt-4o', requests: 100, credits: 5000, cost_usd: 12.5 }],
    trend: [{ date: '2026-10-01', requests: 100, credits: 5000, cost_usd: 12.5 }],
    ...over,
  } as unknown as MaasUsageSummary
}

function consumptionRow(over: Record<string, unknown> = {}): MaasConsumptionRow {
  return {
    tenant_id: 'acme',
    owner_user: 'alice',
    provider_id: 3,
    provider_name: 'OpenAI',
    credential_id: 9,
    credential_label: 'sk-…',
    canonical_id: 1,
    model: 'gpt-4o',
    requests: 100,
    prompt_tokens: 1000,
    completion_tokens: 2000,
    cache_read_tokens: 300,
    cache_write_tokens: 40,
    credits_charged: 5000,
    upstream_cost_usd: 10,
    // ★★ 与「回显的 cents_per_credit=0.1」自洽：5000 * 0.1 / 100 = 5
    tenant_revenue_usd: 5,
    gross_margin_usd: -5,
    gross_margin_rate: -1,
    cancelled_billed_requests: 2,
    ...over,
  } as unknown as MaasConsumptionRow
}

function detail(rows: MaasConsumptionRow[] = [consumptionRow()]): MaasConsumptionDetail {
  return { tenant_id: 'acme', days: 7, cents_per_credit: 0.1, rows }
}

function ledger(items: MaasLedgerEntry[] = [ledgerEntry()]): MaasLedgerResponse {
  return { items }
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(MaasTenantOpsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  // 默认租户码为空 ⇒ 首屏是「先填租户码」；统一填上再查一次
  await w.find('#mt-code').setValue('acme')
  await w.findAll('.mt__btn')[0]!.trigger('click')
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  aMock.mockResolvedValue(account())
  sMock.mockResolvedValue(summary())
  dMock.mockResolvedValue(detail())
  lMock.mockResolvedValue(ledger())
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
  it('★★★★★★ /maas/tenants/** 全是 h.superAdmin ⇒ 席必须设 requiresRole', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'maas-tenant-ops')
    expect(seat).toBeDefined()
    // ★★ 设成 admin/不设都会让 tenant_admin 点进来 403
    expect(seat?.requiresRole).toBe('super_admin')
  })

  it('★★★★★ 配置面席同样是 super_admin', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    expect(DRAWER_NAV.find((i) => i.key === 'maas-admin-catalog')?.requiresRole).toBe('super_admin')
  })

  it('★★ 对照：第三段那两条 admin 席**不许**被改成 super_admin', async () => {
    const { DRAWER_NAV } = await import('@/config/appNav')
    expect(DRAWER_NAV.find((i) => i.key === 'maas-catalog')?.requiresRole).toBeUndefined()
    expect(DRAWER_NAV.find((i) => i.key === 'maas-wallet')?.requiresRole).toBeUndefined()
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ days 决定读哪张表', () => {
  it('★★★★★★ days=7 ⇒ 明说读的是**热表**', async () => {
    const w = await mountView()
    expect(w.text()).toContain('request_logs_hot')
    expect(w.text()).not.toContain('request_logs_with_current_month')
    expect(sMock).toHaveBeenLastCalledWith('acme', { days: 7, limit: 50 })
  })

  it('★★★★★★ ★★ 切到 30 天 ⇒ 明说**换了当月合并表**，且说破「不只是窗口变长」', async () => {
    const w = await mountView()
    // 天数按钮：1 / 7 / 30 / 90 ⇒ 取索引 2（30）
    await w.findAll('.mt__seg')[0]!.findAll('button')[2]!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('request_logs_with_current_month')
    const warns = w.findAll('.mt__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('换了物理表') || x.includes('换了'))).toBe(true)
  })

  it('★★★★★ days=1 也仍是热表（边界是 >7 才换）', async () => {
    const w = await mountView()
    await w.findAll('.mt__seg')[0]!.findAll('button')[0]!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('request_logs_hot')
  })

  it('★★★★★ 明说三套限幅各不相同', async () => {
    const w = await mountView()
    expect(w.text()).toContain('三套限幅各不相同')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 收入是算出来的，要能核对', () => {
  it('★★★★ 服务端收入与复算一致时**不**报警', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('对不上')
  })

  it('★★★★★ ★ 服务端收入被改坏 ⇒ 复算发现不一致并标出', async () => {
    dMock.mockResolvedValue(detail([consumptionRow({ tenant_revenue_usd: 999 })]))
    const w = await mountView()
    expect(w.text()).toContain('对不上')
  })

  it('★★★★★ 单价回显被渲染出来（复算的依据）', async () => {
    const w = await mountView()
    expect(w.text()).toContain('每积分单价')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 零收入 ⇒ 毛利率无定义', () => {
  it('★★★★★ 零收入行显示「无定义」而不是「0%」', async () => {
    dMock.mockResolvedValue(
      detail([consumptionRow({ tenant_revenue_usd: 0, gross_margin_usd: 0, gross_margin_rate: 0 })]),
    )
    const w = await mountView()
    // ★★★ 按节点取值：整页 toContain('无定义') 会被**上方那条提示文案**
    //   （"毛利率是**无定义**"）喂饱 ⇒ 那是恒真判据，永远不会红。
    const item = w.find('.mt__item')
    expect(item.exists()).toBe(true)
    const cells = item.findAll('.mt__cell').map((c) => c.text())
    const marginCell = cells.find((x) => x.includes('毛利率'))
    expect(marginCell, '毛利率那一格必须存在').toBeDefined()
    expect(marginCell).toContain('无定义')
    expect(marginCell).not.toContain('0.0%')
    // 与「真·零毛利」互为对照的提示仍在
    const warns = w.findAll('.mt__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('没有收入'))).toBe(true)
  })

  it('★★★ 非零收入的行照常显示百分比', async () => {
    const w = await mountView()
    expect(w.text()).toContain('-100.0%')
    expect(w.text()).not.toContain('无定义')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ cost_usd 键缺失 ⇒ 显示 0 而不是「—」', () => {
  it('★★★ 模型聚合行的 cost 键缺失 ⇒ 渲染成 0.0000', async () => {
    sMock.mockResolvedValue(
      summary({ by_model: [{ model: 'm', requests: 1, credits: 0 }] }) as unknown as MaasUsageSummary,
    )
    const w = await mountView()
    // ★ 按那一行取值；全页 not.toContain('—') 会被**别的面板的合法文案**判红
    //   （本仓已经栽过三次，见「按节点作用域断言」那条纪律）
    const row = w.findAll('.mt__table tbody tr')[0]!
    const cells = row.findAll('.mt__td-v').map((c) => c.text())
    expect(cells).toEqual(['1', '0', '$0.0000'])
    // ★ 关键：最后一格是**真·零**，不是占位符
    expect(cells.at(-1)).not.toBe('—')
    expect(cells.at(-1)).not.toContain('NaN')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 已计费但被客户端取消', () => {
  it('★★★ 非零 ⇒ 明说是收入侧漏点', async () => {
    const w = await mountView()
    const warns = w.findAll('.mt__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('已扣积分但被客户端取消'))).toBe(true)
  })

  it('★★★ 为 0 ⇒ 不出该提示', async () => {
    dMock.mockResolvedValue(detail([consumptionRow({ cancelled_billed_requests: 0 })]))
    const w = await mountView()
    const warns = w.findAll('.mt__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('已扣积分但被客户端取消'))).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 租户为空 / 错误', () => {
  it('★★★ 租户码为空 ⇒ 不发请求并提示先填', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(MaasTenantOpsView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await flushPromises()
    // ★ 租户码留空直接点「查询」⇒ 本地守卫拦住，**一个请求都不许发**
    await w.findAll('.mt__btn')[0]!.trigger('click')
    await flushPromises()
    expect(w.text()).toContain('先填租户码')
    expect(aMock).not.toHaveBeenCalled()
    expect(sMock).not.toHaveBeenCalled()
    expect(dMock).not.toHaveBeenCalled()
    expect(lMock).not.toHaveBeenCalled()
  })

  it('★★★ 「tenant_id required」（500）⇒ 说清是服务端拒绝，不是「没数据」', async () => {
    aMock.mockRejectedValue(new Error('internal error (see server logs): tenant_id required'))
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(MaasTenantOpsView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await w.find('#mt-code').setValue('acme')
    await w.findAll('.mt__btn')[0]!.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('500')
  })

  it('★★★ 抛错时四个面板**一个都不许**留下', async () => {
    aMock.mockRejectedValue(new Error('boom'))
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(MaasTenantOpsView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await w.find('#mt-code').setValue('acme')
    await w.findAll('.mt__btn')[0]!.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('boom')
    expect(w.text()).not.toContain('gpt-4o')
    expect(w.find('.mt__table').exists()).toBe(false)
  })

  it('★★★★★ ★★ 先查成功、再查失败 ⇒ **旧数据必须被清掉**', async () => {
    // ★★★ 只验「首屏就失败」够不着「catch 里不清空」这条路径
    //   —— 首屏本来就是 null，删掉清空语句照样全绿。
    //   必须造「有数据 → 再失败」这个序列，否则这条判据是恒真的。
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o')

    aMock.mockRejectedValue(new Error('boom'))
    await w.findAll('.mt__btn')[0]!.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('boom')
    expect(w.text()).not.toContain('gpt-4o')
    expect(w.find('.mt__table').exists()).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 流水的 null 指针与只读声明', () => {
  it('★★ pool 为 null ⇒ 明说「键在值为 null」（不是空白、不是「未标记」）', async () => {
    lMock.mockResolvedValue(ledger([ledgerEntry({ pool: null, ref_type: null, ref_id: null })]))
    const w = await mountView()
    expect(w.text()).toContain('键在值为 null')
  })

  it('★★ 明说 account 那段条数写死、且与流水列表不是同一批', async () => {
    const w = await mountView()
    expect(w.text()).toContain('不是同一批数据')
  })

  it('★★ 明说本页只读、不做 adjust/grant', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只读')
    expect(w.text()).toContain('adjust')
  })
})