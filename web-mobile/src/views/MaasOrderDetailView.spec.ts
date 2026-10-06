import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MaasOrderDetailView from './MaasOrderDetailView.vue'
import { fetchMaasOrder, type MaasOrder } from '@/api/maas'
import { setLocale, locale } from '@/i18n'

/**
 * MaasOrderDetailView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★★ 404 **身兼两职**：「订单不存在」与「查询失败」后端都写 404
 *    ⇒ 页面不许只说「订单不存在」。
 * 2. ★★★★★ `payment_hint` / `stub_mode` **只有这里才有**
 *    ⇒ 这是列表页「没有支付信息」的正解所在。
 * 3. ★★★★ `stub_mode` 是 bool + omitempty ⇒ false 时**键整个不存在**，
 *    页面显示「关闭」是按「非 true」推断的，必须说破。
 * 4. ★★★ `paid_at` 是指针 + omitempty ⇒ 未支付时键不存在，显示「未支付」。
 * 5. ★★★ 有 id 但名字空串 = 关联已被删。
 * 6. ★★ id 非法 ⇒ 本地就拦，且说清是 400 而非 404。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const pushMock = vi.fn()
const routeParams = { id: '7' as string | undefined }
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { get id() { return routeParams.id } } }),
  useRouter: () => ({ push: pushMock }),
}))

vi.mock('@/api/maas', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/maas')>()
  return { ...actual, fetchMaasOrder: vi.fn() }
})

const mMock = fetchMaasOrder as unknown as ReturnType<typeof vi.fn>
const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** ★ 详情端点返回的是**裸对象**（writeJSON(w,200,order)），带 enrich 字段。 */
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
    // ★★ enrich 只在详情这一圈调 ⇒ 这两个键存在
    payment_hint: 'stub:alipay',
    stub_mode: false,
    ...over,
  } as MaasOrder
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(MaasOrderDetailView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  mMock.mockResolvedValue(order())
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 404 身兼两职', () => {
  it('★★★★★★ 404 时明说「也可能是查询失败」，不许只说订单不存在', async () => {
    mMock.mockRejectedValue(new Error('order not found'))
    const w = await mountView()
    const warns = w.findAll('.mo__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('不区分') && x.includes('查询失败'))).toBe(true)
  })

  it('★★★★★★ 404 时不渲染任何订单字段', async () => {
    mMock.mockRejectedValue(new Error('order not found'))
    const w = await mountView()
    expect(w.text()).not.toContain('ORD-0007')
    expect(w.find('.mo__amount-v').exists()).toBe(false)
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ enrich 字段只有详情有', () => {
  it('★★★★★★ 渲染 payment_hint', async () => {
    const w = await mountView()
    expect(w.text()).toContain('stub:alipay')
  })

  it('★★★★★★ 明说这是「只有详情接口返回」，列表看不到', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只有详情接口返回')
  })

  it('★★★★★★ stub_mode = false ⇒ 显示「关闭」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('关闭')
  })

  it('★★★★★★ ★★ stub_mode 键**缺失**时，必须说破「是推断不是读到 false」', async () => {
    // omitempty ⇒ false 时后端省掉键。两条 path 都渲染「关闭」，
    //   但**必须**区分「键缺失」与「显式 false」—— 否则读者以为读到了 false。
    const w = await mountView()
    // 夹具显式给了 false ⇒ 不出「键缺失」提示
    expect(w.text()).not.toContain('这个键**不存在**')

    for (const ww of mountedList) ww.unmount()
    mountedList = []
    document.body.innerHTML = ''

    const o = order() as unknown as Record<string, unknown>
    delete o.stub_mode
    mMock.mockResolvedValue(o as unknown as MaasOrder)
    const w2 = await mountView()
    expect(w2.text()).toContain('这个键**不存在**')
    expect(w2.text()).toContain('关闭')
  })

  it('★★★★ stub_mode = true ⇒ 显示「开启（stub）」且不再说键缺失', async () => {
    const o = order() as unknown as Record<string, unknown>
    delete o.stub_mode
    mMock.mockResolvedValue({ ...o, stub_mode: true } as unknown as MaasOrder)
    const w = await mountView()
    expect(w.text()).toContain('开启（stub）')
    expect(w.text()).not.toContain('这个键**不存在**')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★ 金额与时间', () => {
  it('★★★★ 1200 分 ⇒ ¥12.00', async () => {
    const w = await mountView()
    expect(w.find('.mo__amount-v').text()).toBe('¥12.00')
  })

  it('★★★ 已支付 ⇒ 渲染支付时间', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('未支付')
  })

  it('★★★ paid_at 键缺失（未支付）⇒ 显示「未支付」，不是空白', async () => {
    const o = order() as unknown as Record<string, unknown>
    delete o.paid_at
    mMock.mockResolvedValue(o as unknown as MaasOrder)
    const w = await mountView()
    expect(w.text()).toContain('未支付')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 孤儿关联名 + 只读声明', () => {
  it('★★★ plan_name 空串 ⇒ 说明「关联已被删」并点破 LEFT JOIN 未命中', async () => {
    const o = order() as unknown as Record<string, unknown>
    delete o.plan_name
    mMock.mockResolvedValue(o as unknown as MaasOrder)
    const w = await mountView()
    expect(w.text()).toContain('LEFT JOIN 没命中')
  })

  it('★★ 明说本页只读、不做 confirm', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只读')
    expect(w.text()).toContain('confirm')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 本地 id 拦截', () => {
  it('★★ 请求参数就是路由里的 id（正整数）', async () => {
    await mountView()
    expect(mMock).toHaveBeenCalledWith(7)
  })

  it('★★ 非法 id ⇒ 本地就拦，不发请求，并说清是 400', async () => {
    // ★★ 后端是 `strconv.ParseInt(parts[0], 10, 64)`，只吃**纯十进制数字**。
    // 而 `Number()` 会把下面这四种串**解析成整数** —— 若本地只用 Number()，
    // 它们就能过本地守卫，发出去后端必回 400 `invalid order id`。
    // 这条判据就是钉这个契约：四种都必须在本地就被拦下、且不发请求。
    for (const bad of ['1e3', '7.0', '0x10', '+7']) {
      for (const ww of mountedList) ww.unmount()
      mountedList = []
      document.body.innerHTML = ''
      routeParams.id = bad
      mMock.mockClear()

      const pinia = createPinia()
      setActivePinia(pinia)
      const w = mount(MaasOrderDetailView, { attachTo: document.body, global: { plugins: [pinia] } })
      mountedList.push(w)
      await flushPromises()
      await flushPromises()

      // 本地拦住 ⇒ **一个请求都不许发**
      expect(mMock, `id=${bad} 不该发请求`).not.toHaveBeenCalled()
      // 且要说清这是 400（invalid order id），不是 404
      const warns = w.findAll('.mo__note--warn').map((n) => n.text())
      expect(warns.some((x) => x.includes('400')), `id=${bad} 应说 400`).toBe(true)
      expect(w.text()).toContain('正整数')
    }
  })

  it('★★ 后端真回 400 invalid order id 时也归 badid（不误判成 404）', async () => {
    mMock.mockRejectedValue(new Error('invalid order id'))
    const w = await mountView()
    const warns = w.findAll('.mo__note--warn').map((n) => n.text())
    expect(warns.some((x) => x.includes('400'))).toBe(true)
    // ★ 不许被 404 那条文案吃掉
    expect(w.text()).not.toContain('也可能是查询失败')
  })

  it('★ 返回按钮回列表页', async () => {
    const w = await mountView()
    const btns = w.findAll('.mo__btn')
    await btns[0]!.trigger('click')
    expect(pushMock).toHaveBeenCalledWith('/maas-orders')
  })
})