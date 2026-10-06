import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import MaasRatesView from './MaasRatesView.vue'
import { fetchMaasModelRates, type MaasModelRatesResponse } from '@/api/maas'
import { setLocale, locale } from '@/i18n'

/**
 * MaasRatesView 的不变量（2026-10-08）。
 *
 * 1. ★★★★★ 逐维标注生效价**来源**（自定义 / 全局已配 / 全局硬编码 10000）；
 * 2. ★★★★★ 「**开了手动但没生效**」与「**改了但没启用**」两件不同的事都要说破；
 * 3. ★★★★★ `global_discount` 配 0 ⇒ 明说「配 0 不等于全免」；
 * 4. ★★★ 硬编码 10000 不是「没配」；
 * 5. ★★★ 503（MaaS 没开）不许渲染成「没配过价」；
 * 6. ★★ 兜底字面量（「其他」/「text」）要标出来。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/maas', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/maas')>()
  return { ...actual, fetchMaasModelRates: vi.fn() }
})

const mMock = fetchMaasModelRates as unknown as ReturnType<typeof vi.fn>
const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function settings(over: Record<string, unknown> = {}) {
  return {
    cents_per_credit: 0.1,
    base_credits_per_1m: 0,
    base_credits_per_1m_in: 0,
    base_credits_per_1m_out: 0,
    base_credits_per_1m_cache_in: 0,
    base_credits_per_1m_cache_out: 0,
    global_discount: 1,
    currency_display: 'CNY',
    alipay_account: '',
    wechat_mch_id: '',
    stub_alipay_qr_url: '',
    stub_wechat_qr_url: '',
    ...over,
  }
}

function row(over: Record<string, unknown> = {}) {
  return {
    canonical_id: 1,
    canonical_name: 'gpt-4o',
    display_name: 'GPT-4o',
    vendor: 'OpenAI',
    family: null,
    modality: 'text',
    status: 'active',
    credits_per_1m_in: 10000,
    credits_per_1m_out: 10000,
    credits_per_1m_cache_in: 10000,
    credits_per_1m_cache_out: 10000,
    credits_per_1m_image_tokens: 10000,
    credits_per_1m_audio_tokens: 10000,
    credits_per_1m_video_tokens: 10000,
    manual_in: false,
    manual_out: false,
    manual_cache_in: false,
    manual_cache_out: false,
    manual_image: false,
    manual_audio: false,
    manual_video: false,
    is_custom: false,
    custom_credits_per_1m_in: null,
    custom_credits_per_1m_out: null,
    custom_credits_per_1m_cache_in: null,
    custom_credits_per_1m_cache_out: null,
    custom_credits_per_1m_image_tokens: null,
    custom_credits_per_1m_audio_tokens: null,
    custom_credits_per_1m_video_tokens: null,
    updated_at: null,
    ...over,
  }
}

function body(over: Record<string, unknown> = {}): MaasModelRatesResponse {
  return { settings: settings(), items: [row()], ...over } as MaasModelRatesResponse
}

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(MaasRatesView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  mMock.mockResolvedValue(body())
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★★ 逐维来源标注', () => {
  it('★★★★★★ 七个维度都渲染一行，且表头齐', async () => {
    const w = await mountView()
    expect(w.findAll('.ms__table tbody tr')).toHaveLength(7)
    expect(w.text()).toContain('生效价')
    expect(w.text()).toContain('来源')
  })

  it('★★★★★★ 全部走全局硬编码 ⇒ 每行来源都是「全局（硬编码 10000）」', async () => {
    const w = await mountView()
    const cells = w.findAll('.ms__src--global_hardcoded')
    expect(cells.length).toBe(7)
    expect(cells[0]?.text()).toBe('全局（硬编码 10000）')
  })

  it('★★★★★★ 手动且值 >0 ⇒ 该维来源变「自定义（原值）」，其余仍是全局', async () => {
    mMock.mockResolvedValue(
      body({
        settings: settings({ base_credits_per_1m_in: 7000 }),
        items: [row({ manual_in: true, custom_credits_per_1m_in: 300, is_custom: true })],
      }),
    )
    const w = await mountView()
    const srcs = w.findAll('.ms__table tbody tr').map((tr) => tr.findAll('td')[3]?.text())
    expect(srcs[0]).toBe('自定义（原值）')
    expect(srcs[1]).toBe('全局（已配）')
  })

  it('★★★★ 全局基价是配出来的 ⇒ 显式说明不是硬编码', async () => {
    mMock.mockResolvedValue(body({ settings: settings({ base_credits_per_1m_in: 7000 }) }))
    const w = await mountView()
    expect(w.text()).not.toContain('硬编码的 10000')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 「开了手动但没生效」', () => {
  it('★★★★★ manual=true 但 custom 为 null ⇒ 该维显示「★ 开了手动但没生效」', async () => {
    mMock.mockResolvedValue(
      body({ items: [row({ manual_in: true, custom_credits_per_1m_in: null, is_custom: true })] }),
    )
    const w = await mountView()
    expect(w.text()).toContain('开了手动但没生效')
  })

  it('★★★★★ manual=true 且 custom=300 ⇒ 该维显示「在用自定义」', async () => {
    mMock.mockResolvedValue(
      body({ items: [row({ manual_in: true, custom_credits_per_1m_in: 300, credits_per_1m_in: 300, is_custom: true })] }),
    )
    const w = await mountView()
    expect(w.text()).toContain('在用自定义')
    expect(w.text()).not.toContain('开了手动但没生效')
  })

  it('★★★★★ 全关 ⇒ 「跟随全局」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('跟随全局')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 「改了但没启用」', () => {
  it('★★★★★ manual=false 但 custom 有值 ⇒ 显式点破，不让它看起来自相矛盾', async () => {
    mMock.mockResolvedValue(
      body({ items: [row({ manual_in: false, custom_credits_per_1m_in: 300, credits_per_1m_in: 10000 })] }),
    )
    const w = await mountView()
    expect(w.text()).toContain('没开手动')
  })

  it('★ 没有休眠值 ⇒ 那句话**不**出现', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('没开手动')
  })

  it('★ is_custom=true ⇒ 明说「不等于整行都被定制」', async () => {
    mMock.mockResolvedValue(body({ items: [row({ is_custom: true })] }))
    const w = await mountView()
    expect(w.text()).toContain('不代表整行都被定制')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★★★ 折扣与硬编码兜底', () => {
  it('★★★★★ `global_discount = 0` ⇒ 明说「配 0 不等于全免」', async () => {
    mMock.mockResolvedValue(body({ settings: settings({ global_discount: 0 }) }))
    const w = await mountView()
    expect(w.text()).toContain('不等于')
    expect(w.text()).toContain('全免')
  })

  it('★★★★★ 折扣 > 1 ⇒ 同样归一成「不打折」，也点破', async () => {
    mMock.mockResolvedValue(body({ settings: settings({ global_discount: 1.5 }) }))
    const w = await mountView()
    expect(w.text()).toContain('不打折')
  })

  it('★★★★ 正常折扣 ⇒ 显示百分比 + 向上取整说明', async () => {
    mMock.mockResolvedValue(body({ settings: settings({ global_discount: 0.8 }) }))
    const w = await mountView()
    expect(w.text()).toContain('80.0%')
    expect(w.text()).toContain('向上取整')
  })

  it('★★★★★ 硬编码 10000 ⇒ 明说「不是你们配的」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('不是你们配的')
    expect(w.text()).toContain('硬编码的 10000')
  })

  it('★★★★ 明说「折扣只作用于全局价，自定义价不打折」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只作用于全局价')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★★ 503 与兜底字面量', () => {
  it('★★★ 503 ⇒ 显示「服务端没有启用 MaaS」，**不**显示「没配过价」', async () => {
    mMock.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    expect(w.text()).toContain('database not configured')
    expect(w.text()).toContain('没有启用 MaaS')
    expect(w.text()).not.toContain('Model rates')
  })

  it('★★ 503 时**不**出现任何价目行（结构判据）', async () => {
    mMock.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    expect(w.findAll('.ms__table tbody tr')).toHaveLength(0)
  })

  it('★★ `vendor` 是兜底值「其他」⇒ 打标签说明它不是真厂商', async () => {
    mMock.mockResolvedValue(body({ items: [row({ vendor: '其他' })] }))
    const w = await mountView()
    expect(w.text()).toContain('其他')
    expect(w.findAll('.ms__tag--warn').length).toBeGreaterThan(0)
  })

  it('★ `modality` 是兜底值 text ⇒ 打标签', async () => {
    const w = await mountView()
    expect(w.findAll('.ms__tag').length).toBeGreaterThan(0)
  })

  it('★ 真的没有 rate 行 ⇒ 明说「全部来自全局基价」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('没有对应行')
  })
})

// ────────────────────────────────────────────────────────────────────────
describe('★★ 没有分页 + 只列 active', () => {
  it('★★ 明说「没有分页」并给出本次条数', async () => {
    const w = await mountView()
    expect(w.text()).toContain('没有分页')
  })

  it('★★ 明说「只含 active 模型」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只含 active 模型')
  })

  it('★ 搜索过滤走本地（不发第二次请求）', async () => {
    const w = await mountView()
    await w.find('#ms-kw').setValue('gpt')
    await flushPromises()
    expect(mMock).toHaveBeenCalledTimes(1)
    expect(w.findAll('.ms__item')).toHaveLength(1)
  })

  it('★ 过滤不到 ⇒ 空态（**不是**「没配过价」）', async () => {
    const w = await mountView()
    await w.find('#ms-kw').setValue('zzz-no-match')
    await flushPromises()
    expect(w.text()).toContain('没有匹配的模型')
  })
})
