import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MaasOrdersView from './MaasOrdersView.vue'
import { fetchMaasOrders, type MaasOrder } from '@/api/maas'
import { setLocale, locale } from '@/i18n'

/**
 * MaasOrdersView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 页面**没有**「下一页」：这条端点只有 limit，没有 offset/cursor。
 *    排满时只能说「可能还有更老的，调大 limit」，不是「点下一页」。
 * 2. ★★★★★ 列表**恒无** `payment_hint` / `stub_mode`
 *    ⇒ 文案必须说「这是端点差异，去详情页看」，**不能**说「这单没有支付信息」。
 * 3. ★★★★ `amount_cents` 单位是**分** ⇒ 渲染成元（1200 分 ⇒ ¥12.00）。
 * 4. ★★★ 跨租户要���破（后端 tenantID 传空串）。
 * 5. ★★★ 有 id 但名字是空串 = 关联已被删（LEFT JOIN 未命中）。
 * 6. ★★ 503（MaaS 没开）不许渲染成「没有订单」。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('@/api/maas', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/maas')>()
  return { ...actual, fetchMaasOrders: vi.fn() }
})

const mMock = fetchMaasOrders as unknown as ReturnType<typeof vi.fn>
const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** ★ 照抄 `maas/orders.go` `BillingOrder` + ListOrders 那条 SQL 的扫描顺序。 */
function order(over: Record<string, unknown> = {}): MaasOrder {
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
    // ★ payment_hint / stub_mode **故意不给**：ListOrders 那一圈不 enrich
    ...over,
  } as MaasOrder
}

async function mountView(items: MaasOrder[] = [order()]): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  mMock.mockResolvedValue({ items })
  const w = mount(MaasOrdersView, { attachTo: document.body, global: { plugins: [pinia] } })
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
describe('★★★★★★ 没有分页这件事本身要说破', () => {
  it('★★★★★★ 明说「没有分页」，且给出本次条数', async () => {
    const w = await mountView([order()])
    const notes = w.findAll('.mo__note').map((n) => n.text())
    expect(notes.some((x) => x.includes('没有分页'))).toBe(true)
    expect(notes.some((x) => x.includes('本次返回 1 条'))).toBe(true)
  })

  it('★★★★★★ ★★ 页面上**不存在**任何「下一页 / 加载更多」控件', async () => {
    const w = await mountView([order()])
    const clickable = w.findAll('button').map((b) => b.text()).join('|')
    expect(clickable).not.toContain('下一页')
    expect(clickable).not.toContain('加载更多')
    expect(clickable).not.toContain('next')
  })

  it('★★★★★★ 排满（条数 == limit）⇒ 只提示「可能还有更老的，调大 limit」', async () => {
    // 造 20 条（limit 默认 20）⇒ 正好排满
    const many = Array.from({ length: 20 }, (_, i) => order({ id: i + 1, order_no: `ORD-${i}` }))
    const w = await mountView(many)
    const warn = w.findAll('.mo__note--warn').map((n) => n.text())
    expect(warn.some((x) => x.includes('可能还有更老的'))).toBe(true)
    // ★ 提示必须指向「调大 limit」这条路，而不是「点下一页」
    expect(warn.some((x) => x.includes('把 limit 调大'))).toBe(true)
  })

  it('★★★★★ 没排满 ⇒ 不出「可能还有更多」的提示', async () => {
    const w = await mountView([order()])
    const warn = w.findAll('.mo__note--warn').map((n) => n.text())
    expect(warn.some((x) => x.includes('可能还有更老的'))).toBe(false)
  })

  it('★★★★ limit 选择器三个值都落在后端有效区间 1..100 内', async () => {
    const w = await mountView()
    const labels = w.findAll('.mo__seg-btn').map((b) => Number(b.text()))
    expect(labels).toEqual([20, 50, 100])
    // ★ 任何可选项都不得 <=0 或 >100（那会被后端悄悄改写成 20）
    for (const n of labels) {
      expect(n).toBeGreaterThanOrEqual(1)
      expect(n).toBeLessThanOrEqual(100)
    }
  })

  it('★★★★ 切 limit 会重新请求，且带上新值', async () => {
    const w = await mountView()
    await w.findAll('.mo__seg-btn')[2]!.trigger('click') // 100
    await flushPromises()
    expect(mMock).toHaveBeenLastCalledWith({ limit: 100 })
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 列表恒无 enrich 字段', () => {
  it('★★★★★★ 文案说「不是这单没有支付信息」而**不是**「没有支付信息」', async () => {
    const w = await mountView()
    const item = w.find('.mo__item')
    expect(item.exists()).toBe(true)
    expect(item.text()).toContain('不是「这单没有支付信息」')
    expect(item.text()).toContain('列表接口压根不返回')
  })

  it('★★★★★★ 全局一次提示：后端只在详情接口 enrich', async () => {
    const w = await mountView()
    expect(w.text()).toContain('enrich 支付信息')
    expect(w.text()).toContain('端点差异')
  })

  it('★★★★★ 若真给了 payment_hint，则不再说「列表不返回」', async () => {
    // 守「判据不是恒真」：把 enrich 字段塞进列表，那条提示就该消失
    const w = await mountView([order({ payment_hint: 'stub:alipay' })])
    expect(w.text()).not.toContain('列表接口压根不返回')
    expect(w.text()).not.toContain('本页所有订单都缺')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 金额单位是分', () => {
  it('★★★★★ 1200 分渲染成 ¥12.00，**不是** ¥1200', async () => {
    const w = await mountView([order({ amount_cents: 1200 })])
    const amt = w.find('.mo__amount-v')
    expect(amt.exists()).toBe(true)
    expect(amt.text()).toBe('¥12.00')
    expect(amt.text()).not.toContain('1200')
  })

  it('★★★ 0 分 ⇒ ¥0.00，不留空', async () => {
    const w = await mountView([order({ amount_cents: 0 })])
    expect(w.find('.mo__amount-v').text()).toBe('¥0.00')
  })

  it('★★★ 积分照原样渲染（不是分）', async () => {
    const w = await mountView([order({ credits: 500 })])
    expect(w.find('.mo__amount-u').text()).toContain('500')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 孤儿关联名', () => {
  it('★★★ 有 plan_id 但 plan_name 是空串 ⇒ 标「关联已删」，不显示空名', async () => {
    const w = await mountView([order({ plan_id: 3, plan_name: '' })])
    expect(w.find('.mo__tag--warn').exists()).toBe(true)
    // ★ 按节点取值：全页 toContain/not.toContain 会把标签前缀一起吞进来
    const nameV = w.find('.mo__name-v')
    expect(nameV.exists()).toBe(true)
    expect(nameV.text()).toBe('（关联已被删除）')
    expect(nameV.text()).not.toBe('')
  })

  it('★★★ plan_id 都没有 ⇒ 不算孤儿，不标', async () => {
    const w = await mountView([order({ plan_id: undefined, plan_name: undefined })])
    expect(w.find('.mo__tag--warn').exists()).toBe(false)
    expect(w.text()).not.toContain('关联已删')
  })

  it('★★★ topup 单看 package 侧：package 空名才是孤儿', async () => {
    const w = await mountView([order({ order_type: 'topup', package_id: 9, package_name: '' })])
    expect(w.find('.mo__tag--warn').exists()).toBe(true)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 跨租户', () => {
  it('★★★ 明说这条接口跨全部租户', async () => {
    const w = await mountView()
    expect(w.text()).toContain('跨全部租户')
  })

  it('★★★ 出现多个租户时给出租户个数', async () => {
    const w = await mountView([order({ tenant_id: 'acme' }), order({ id: 8, tenant_id: 'globex' })])
    expect(w.text()).toContain('涉及 2 个租户')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 枚举与错误', () => {
  it('★★★ 四个状态都映射成中文标签', async () => {
    for (const [s, label] of [
      ['pending', '待支付'],
      ['paid', '已支付'],
      ['cancelled', '已取消'],
      ['expired', '已过期'],
    ] as const) {
      const w = await mountView([order({ status: s })])
      expect(w.find('.mo__badge-t').text()).toBe(label)
      for (const ww of mountedList) ww.unmount()
      mountedList = []
      document.body.innerHTML = ''
    }
  })

  it('★★★ 枚举外的值**原样显示**，不猜也不吞', async () => {
    const w = await mountView([order({ status: 'refunded' })])
    expect(w.find('.mo__badge-t').text()).toBe('refunded')
  })

  it('★★★ 支付渠道映射：alipay/wechat/manual', async () => {
    for (const [c, label] of [
      ['alipay', '支付宝'],
      ['wechat', '微信'],
      ['manual', '手工'],
    ] as const) {
      const w = await mountView([order({ payment_channel: c })])
      expect(w.text()).toContain(label)
      for (const ww of mountedList) ww.unmount()
      mountedList = []
      document.body.innerHTML = ''
    }
  })

  it('★★★★ 503（MaaS 没开）⇒ 渲染成「没启用」，**不是**「没有订单」', async () => {
    mMock.mockRejectedValue(new Error('database not configured'))
    const pinia = createPinia()
    setActivePinia(pinia)
    const w = mount(MaasOrdersView, { attachTo: document.body, global: { plugins: [pinia] } })
    mountedList.push(w)
    await flushPromises()
    await flushPromises()
    expect(w.text()).toContain('没有启用 MaaS')
    expect(w.text()).not.toContain('没有匹配的订单')
    expect(w.find('.mo__list').exists()).toBe(false)
  })

  it('★★★ 过滤不到 ⇒ 空态', async () => {
    const w = await mountView()
    await w.find('#mo-kw').setValue('zzz-no-match')
    await flushPromises()
    expect(w.text()).toContain('没有匹配的订单')
  })

  it('★★ 搜索走本地（不发第二次请求）', async () => {
    const w = await mountView()
    await w.find('#mo-kw').setValue('ORD-0007')
    await flushPromises()
    expect(mMock).toHaveBeenCalledTimes(1)
    expect(w.findAll('.mo__item')).toHaveLength(1)
  })
})