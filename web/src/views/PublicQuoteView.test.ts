// PublicQuoteView.test.ts — 客户版报价落地页判据（2026-11-08 W2 客户版轮）。
//
// 钉四件事：
//   ① 正常态：报价要素表（模型/量/单价/折扣/小计 + 合计）与水印在场，
//      响应体类型（publicQuote.ts）不含成本/毛利/供应商轴 —— 客户版
//      「渲染隔离」的展示面（物理隔离由后端编译期锁 + 响应体断言钉）；
//   ② 不可用态（404/过期/撤销/429）：统一「链接不可用」，不区分后端语义；
//   ③ expired=true：过期横幅在场；
//   ④ 打印按钮真实驱动 window.print（打印样式兜底 PDF 的入口）。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import PublicQuoteView from './PublicQuoteView.vue'
import { fetchPublicQuote } from '../api/publicQuote'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/publicQuote', () => ({
  PublicQuoteUnavailableError: class extends Error {
    constructor() {
      super('public quote unavailable')
      this.name = 'PublicQuoteUnavailableError'
    }
  },
  fetchPublicQuote: vi.fn(),
}))

const mockedFetch = vi.mocked(fetchPublicQuote)
const viewMode = vi.hoisted(() => ({ value: 'table' }))
vi.mock('../composables/useDataViewMode', async () => {
  const { computed } = await import('vue')
  return { useDataViewMode: () => ({ effective: computed(() => viewMode.value), canSwitch: computed(() => false), setMode: vi.fn() }) }
})

const QUOTE = {
  customer_name: '客户甲',
  title: '11月推理用量报价',
  valid_until: '2026-11-30T00:00:00Z',
  expired: false,
  tier: 'standard',
  sale_total_usd: 388.9,
  items: [
    {
      canonical_name: 'model-a',
      display_name: 'Model A',
      est_tokens_in: 100_000_000,
      est_tokens_out: 20_000_000,
      volume_note: '',
      rate_in: 100,
      rate_out: 200,
      discount_pct: 0,
      subtotal_usd: 388.9,
    },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
  viewMode.value = 'table'
})

afterEach(() => {
  vi.restoreAllMocks()
})

async function mountAt(token: string) {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/public/quote/:token', component: PublicQuoteView }],
  })
  router.push(`/public/quote/${token}`)
  await router.isReady() // 路由就绪后再挂载：route.params.token 必须在场
  return mount(PublicQuoteView, {
    global: { plugins: [testI18n, router] },
  })
}

describe('PublicQuoteView', () => {
  it('compact cards retain every quote field, total, watermark and print action', async () => {
    viewMode.value = 'cards'
    mockedFetch.mockResolvedValueOnce({ ...QUOTE, items: [{ ...QUOTE.items[0], volume_note: 'Annual usage', discount_pct: 10 }] })
    const w = await mountAt('tok-compact')
    await flushPromises()
    expect(w.find('table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    for (const value of ['Model A', 'Annual usage', '100M', '20M', '100', '200', '10%', '$388.90']) {
      expect(w.text()).toContain(value)
    }
    expect(w.find('.pq-total-row').text()).toContain('$388.90')
    expect(w.find('.pq-watermark').text()).toContain('客户甲')
    expect(w.find('.pq-print-btn').exists()).toBe(true)
  })
  it('正常态：要素表 + 合计 + 水印在场；类型层钉死无成本/毛利/供应商轴', async () => {
    mockedFetch.mockResolvedValueOnce(QUOTE)
    const w = await mountAt('tok-abc')
    await flushPromises()
    const text = w.text()
    expect(text).toContain('Model A')
    expect(text).toContain('100')       // 单价-入
    expect(text).toContain('$388.90')   // 小计/合计
    expect(text).toContain('客户甲')     // 水印（客户名）
    // 类型层钉死：PublicQuote 类型没有 cost/margin/supplier 字段 ——
    // 若有人往类型与页面加成本轴，这行编译期即红（TS 多余属性检查在 mock
    // 赋值处生效），运行期再断言页面不含成本列头。
    expect(text).not.toContain('成本')
    expect(text).not.toContain('毛利')
    expect(text).not.toContain('供应商')
  })

  it('不可用态（404/过期/撤销/429）：统一「链接不可用」，不区分后端语义', async () => {
    mockedFetch.mockRejectedValueOnce(new (await import('../api/publicQuote')).PublicQuoteUnavailableError())
    const w = await mountAt('dead-token')
    await flushPromises()
    const text = w.text()
    expect(text).toContain('不可用')
    expect(text).toContain('过期')
    expect(text).not.toContain('$')
  })

  it('expired=true：过期横幅在场（价格仅供参考）', async () => {
    mockedFetch.mockResolvedValueOnce({ ...QUOTE, expired: true })
    const w = await mountAt('tok-old')
    await flushPromises()
    expect(w.text()).toContain('已过有效期')
  })

  it('打印按钮真实驱动 window.print（打印样式兜底 PDF 入口）', async () => {
    mockedFetch.mockResolvedValueOnce(QUOTE)
    const printSpy = vi.spyOn(window, 'print').mockImplementation(() => {})
    const w = await mountAt('tok-abc')
    await flushPromises()
    const btn = w.findAll('button').find((b) => b.text().includes('PDF'))
    expect(btn).toBeTruthy()
    await btn!.trigger('click')
    expect(printSpy).toHaveBeenCalledTimes(1)
    printSpy.mockRestore()
  })
})
