import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import CompressionView from './CompressionView.vue'
import {
  fetchCompressionStats,
  fetchCompressionSessions,
  SESSIONS_PAGE_SIZE_MAX,
} from '@/api/compression'
import { setLocale, locale } from '@/i18n'

/**
 * CompressionView 的不变量（2026-10-08，第六十九批）。
 *
 * ★ 钉住的九处「不能都渲染成同一个东西」：
 *   ① ★★★★ `count` 是 COUNT(DISTINCT) 的**真实总数** ⇒ 可据此翻页；
 *      但空 session_id 被丢弃而 count 仍算它 ⇒ count > items.length 是可预期的。
 *   ② ★★★★ `count: 0` 是**二义的**（失败时后端返 200 + 空列表）。
 *   ③ ★★★ `compression_rate` 是 **0-1 比例**，展示要 ×100 并标「比例」。
 *   ④ ★★★ 选了自定义区间就**不能**再显示 hours 控件（后端会忽略它）。
 *   ⑤ ★★★ `hourly_series` 粒度随时间窗变，不能写「按小时」；
 *      空序列不能断言「没有流量」。
 *   ⑥ ★★★ 三个 token 估算字段缺键统一「未给出估算」，
 *      **绝不能**说成「没节省」。
 *   ⑦ ★★ `msg_reduction` 为 null ⇒「未知」；=0 且被夹 ⇒ 要提示。
 *   ⑧ ★★ `compression_strategy`/`sample_request_id` 是 MAX 的字典序最大值。
 *   ⑨ ★★★ 页面要标明 tenant_admin 只看成功请求。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/compression', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/compression')>()
  return { ...actual, fetchCompressionStats: vi.fn(), fetchCompressionSessions: vi.fn() }
})

const statsMock = fetchCompressionStats as unknown as ReturnType<typeof vi.fn>
const sessionsMock = fetchCompressionSessions as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

beforeEach(() => {
  statsMock.mockReset()
  sessionsMock.mockReset()
  statsMock.mockResolvedValue(statsFixture())
  sessionsMock.mockResolvedValue(sessionsFixture())
  setLocale('zh-CN')
})

afterEach(() => {
  for (const w of mountedList) w.unmount()
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  vi.restoreAllMocks()
})

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(CompressionView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>
/** 0=时间窗 1=stats 2=sessions */
function sectionOf(w: W, i: number) {
  return w.findAll('.cv__section')[i]!
}
function loadBtn(w: W, n: number) {
  return w.findAll('.cv__load')[n]!
}
async function clickLoad(w: W, n: number): Promise<void> {
  await loadBtn(w, n).trigger('click')
  await flushPromises()
  await flushPromises()
}

/** `<dl>` 里按 `<dt>` 精确文本取对应 `<dd>`（一区可能有多个 dl）。 */
function ddByLabel(w: W, i: number, label: string): string {
  const dls = sectionOf(w, i).findAll('.cv__kv')
  for (const dl of dls) {
    const ch = dl.element.children
    for (let k = 0; k < ch.length - 1; k += 1) {
      if (ch[k]!.tagName === 'DT' && ch[k]!.textContent === label) {
        return ch[k + 1]!.textContent ?? ''
      }
    }
  }
  throw new Error(`未找到标签为「${label}」的行`)
}

/* ── 夹具：逐字照抄后端 ─────────────────────────────────────────── */

/** compression_rate 是 0-1 比例（:187）；hourly 桶 rate 同样（:294）。 */
function statsFixture() {
  return {
    total_requests: 1000,
    compressed_total: 600,
    compression_rate: 0.6,
    strategy_distribution: { none: 400, p2c: 600 },
    token_band_below: 700,
    total_outbound_tokens: 480000,
    estimated_original_tokens: 960000,
    estimated_tokens_saved: 480000,
    summary_mode_rows: 12,
    hourly_series: [
      { hour: '2026-10-08T06:00:00Z', total: 300, compressed: 180, rate: 0.6 },
      { hour: '2026-10-08T12:00:00Z', total: 200, compressed: 120, rate: 0.5 },
    ],
  }
}

/** ★ 七个 omitempty 字段全缺（估算失败 / 值为 0 都长这样）。 */
function statsBareFixture() {
  return {
    total_requests: 0,
    compressed_total: 0,
    compression_rate: 0,
    strategy_distribution: {},
    hourly_series: [],
  }
}

function sessionsFixture() {
  return {
    items: [
      {
        gw_session_id: 'sess-a', compression_strategy: 'p2c', request_count: 8,
        first_ts: '2026-10-08T01:00:00.123456789Z', last_ts: '2026-10-08T01:30:00Z',
        outbound_msg_count: 4, outbound_token_est: 1200,
        estimated_original_msgs: 20, msg_reduction: 16,
        sample_request_id: 'req-0008',
      },
      {
        gw_session_id: 'sess-b', compression_strategy: 'none', request_count: 2,
        first_ts: '2026-10-08T00:10:00Z', last_ts: '2026-10-08T00:12:00Z',
        outbound_msg_count: null, outbound_token_est: 300,
        estimated_original_msgs: 9, msg_reduction: null,
        sample_request_id: 'req-0002',
      },
      {
        gw_session_id: 'sess-c', compression_strategy: 'summary', request_count: 1,
        first_ts: '2026-10-08T02:00:00Z', last_ts: '2026-10-08T02:00:00Z',
        // ★ outbound 30 > estimated 10 ⇒ 差值 -20 被夹到 0
        outbound_msg_count: 30, outbound_token_est: 900,
        estimated_original_msgs: 10, msg_reduction: 0,
        sample_request_id: 'req-0009',
      },
    ],
    count: 42,
  }
}

/* ═══════════════════════════════════════════════════════════════════════
 * ④⑨ 时间窗控件
 * ═══════════════════════════════════════════════════════════════════════ */

describe('CompressionView / 时间窗', () => {
  it('进入页面不自动拉数据（按需加载）', async () => {
    await mountView()
    expect(statsMock).not.toHaveBeenCalled()
    expect(sessionsMock).not.toHaveBeenCalled()
  })

  it('④ 默认是 hours 口径，显示跨度选择器', async () => {
    const w = await mountView()
    expect(sectionOf(w, 0).find('select').exists()).toBe(true)
    expect(sectionOf(w, 0).text()).toContain('钳在 1 到 720 小时')
  })

  it('★★★★ 切到自定义区间 ⇒ 跨度选择器**整块消失**（后端会忽略它）', async () => {
    const w = await mountView()
    await sectionOf(w, 0).findAll('.cv__segBtn')[1]!.trigger('click')
    await flushPromises()
    expect(sectionOf(w, 0).find('select').exists()).toBe(false)
    expect(sectionOf(w, 0).findAll('input[type="datetime-local"]').length).toBe(2)
  })

  it('④ 并说明「两个都传时以起止时间为准」', async () => {
    const w = await mountView()
    await sectionOf(w, 0).findAll('.cv__segBtn')[1]!.trigger('click')
    await flushPromises()
    expect(sectionOf(w, 0).text()).toContain('以起止时间为准')
  })

  it('⑨ 页面标明 tenant_admin 只看成功请求', async () => {
    const w = await mountView()
    expect(sectionOf(w, 0).find('.cv__banner').text()).toContain('只有成功请求')
  })

  it('切到自定义区间会自动重新拉取（口径变了，数字不能留着旧的）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    statsMock.mockClear()
    await sectionOf(w, 0).findAll('.cv__segBtn')[1]!.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(statsMock).toHaveBeenCalled()
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ③⑤⑥ stats 口径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('CompressionView / 压缩统计', () => {
  it('③ 压缩率按百分数显示（后端给的是 0-1 比例）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(ddByLabel(w, 1, '压缩率（比例）')).toContain('60.0%')
  })

  it('③ 并标明单位不是百分数字段、不能与数据流页比', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('不是同一个单位')
  })

  it('③ total_requests=0 ⇒ 那个 0 判为「没有意义」而不是 0%', async () => {
    statsMock.mockResolvedValue(statsBareFixture())
    const w = await mountView()
    await clickLoad(w, 0)
    expect(ddByLabel(w, 1, '压缩率（比例）')).toContain('没有意义')
  })

  it('★★★ compressed_total 带「组级口径」标签（分子不是被压缩的行数）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(ddByLabel(w, 1, '计入已压缩')).toContain('组级口径')
  })

  it('策略名 none 渲染成「未标注策略」并解释来源', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('未标注策略')
  })

  it('策略分布为空时不崩', async () => {
    statsMock.mockResolvedValue(statsBareFixture())
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('没有可归类的请求')
  })

  it('★ 策略分布对不上总数时给出提示（策略名撞键会少算）', async () => {
    const f = { ...statsFixture(), strategy_distribution: { none: 400 } }
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('对不上总数')
  })

  it('策略分布与总数一致时不挂矛盾提示（负控）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).not.toContain('对不上总数')
  })

  it('★ 分档缺键显示「这一档没有出现」而不是 0', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('这一档没有出现')
  })

  it('⑤ 序列标题按 hours 标出真实粒度（24h ⇒ 每小时）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('时间序列（每小时）')
  })

  it('★★ 序列标题不能写「按小时」当粒度随窗口变', async () => {
    const w = await mountView()
    // 切到 720 小时 ⇒ 粒度应为「每天」
    await sectionOf(w, 0).find('select').setValue(720)
    await flushPromises()
    await flushPromises()
    expect(sectionOf(w, 1).text()).toContain('时间序列（每天）')
  })

  it('★ 序列为空时措辞点明「也可能是查询被跳过」，不断言没有流量', async () => {
    statsMock.mockResolvedValue(statsBareFixture())
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('也可能是这一段的查询被跳过了')
  })

  it('桶内 compressed > total 时给出矛盾提示', async () => {
    const f = JSON.parse(JSON.stringify(statsFixture())) as { hourly_series: { total: number; compressed: number }[] }
    f.hourly_series[0]!.compressed = 999
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('计数矛盾')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ⑥ token 估算的三种缺失
 * ═══════════════════════════════════════════════════════════════════════ */

describe('CompressionView / token 估算缺键', () => {
  it('★★★★ 估算字段有值时照常显示数字', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(ddByLabel(w, 1, '估算节省')).toContain('480,000')
  })

  it('★★★ 估算节省缺键 ⇒「未给出节省估算」，**不是**「没节省」', async () => {
    const f = { ...statsFixture() } as Record<string, unknown>
    delete f.estimated_tokens_saved
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    const cell = ddByLabel(w, 1, '估算节省')
    expect(cell).toContain('未给出节省估算')
    expect(cell).not.toContain('没节省')
  })

  it('★★ 估算原始 token 缺键 ⇒「未给出估算」', async () => {
    const f = { ...statsFixture() } as Record<string, unknown>
    delete f.estimated_original_tokens
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(ddByLabel(w, 1, '估算原始 token')).toContain('未给出估算')
  })

  it('★★ 摘要模式行数缺键 ⇒「未给出估算」（0 与失败都缺键）', async () => {
    const f = { ...statsFixture() } as Record<string, unknown>
    delete f.summary_mode_rows
    statsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(ddByLabel(w, 1, '摘要模式行数')).toContain('未给出估算')
  })

  it('并解释「没给出」既可能是算不出来、也可能是 0', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).text()).toContain('既可能是算不出来')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ①②⑦⑧ sessions
 * ═══════════════════════════════════════════════════════════════════════ */

describe('CompressionView / 压缩会话', () => {
  it('① 会话总数照 count 显示（它是真实总数，不是本页条数）', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    // count=42 是全量总数；页面上另有 3 行是本页条数 —— 两者本就不该相等
    expect(ddByLabel(w, 2, '会话总数')).toContain('42')
    expect(sectionOf(w, 2).findAll('.cv__row')).toHaveLength(3)
  })

  it('① 当前页与每页条数照实回显（不是被夹过的值）', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    expect(ddByLabel(w, 2, '当前页')).toContain('每页 50 条')
  })

  it('★ count 与本页条数对不上时说明「是可预期的」，不报成 bug', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 2).text()).toContain('是可预期的')
    expect(sectionOf(w, 2).text()).toContain('会话 id 为空的行会被后端丢掉')
  })

  it('★★ count=0 空态措辞是二义的（失败也返回 200 + 空列表）', async () => {
    sessionsMock.mockResolvedValue({ items: [], count: 0 })
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 2).text()).toContain('总数查询被跳过了')
    expect(sectionOf(w, 2).text()).toContain('和「真的没有」分不出来')
  })

  it('★ 有会话时**不挂**二义措辞（负控）', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 2).text()).not.toContain('和「真的没有」分不出来')
  })

  it('★ msg_reduction 为 null ⇒ 显示「未知」而不是 0', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const row = sectionOf(w, 2).findAll('.cv__row').find((r) => r.text().includes('sess-b'))!
    expect(row.text()).toContain('未知')
    expect(row.text()).not.toContain('减少 0')
  })

  it('★★ reduction=0 且 outbound>orig ⇒ 提示「后端按 0 记」', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const row = sectionOf(w, 2).findAll('.cv__row').find((r) => r.text().includes('sess-c'))!
    expect(row.text()).toContain('后端按 0 记')
  })

  it('reduction 为正时**不挂**夹值提示（负控）', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const row = sectionOf(w, 2).findAll('.cv__row').find((r) => r.text().includes('sess-a'))!
    expect(row.text()).toContain('16')
    expect(row.text()).not.toContain('后端按 0 记')
  })

  it('outbound_msg_count 为 null ⇒ 显示占位符而不是 undefined', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const row = sectionOf(w, 2).findAll('.cv__row').find((r) => r.text().includes('sess-b'))!
    expect(row.text()).not.toContain('undefined')
    expect(row.text()).toContain('出站消息数 —')
  })

  it('⑧ 策略标「取字典序最大值」而不是「这个会话的策略」', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const row = sectionOf(w, 2).findAll('.cv__row').find((r) => r.text().includes('sess-a'))!
    expect(row.text()).toContain('策略（取字典序最大值）：p2c')
  })

  it('⑧ 样本请求同样标「取字典序最大值」，不是最近一次', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const row = sectionOf(w, 2).findAll('.cv__row').find((r) => r.text().includes('sess-a'))!
    expect(row.text()).toContain('样本请求（取字典序最大值）：req-0008')
  })

  it('第一页时「上一页」禁用；未取满时「下一页」也禁用', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const pager = sectionOf(w, 2).findAll('.cv__page')
    expect(pager[0]!.attributes('disabled')).toBeDefined()
    expect(pager[1]!.attributes('disabled')).toBeDefined()
  })

  it('★ 本页取满时「下一页」可用（count 是真实总数，可据此翻页）', async () => {
    const f = sessionsFixture()
    // ⚠️ 阈值用的是**实际生效的每页条数**（缺省 50），所以要造 50 行；
    //   造 20 行时「下一页」禁用是**正确**行为（见下一条负控）。
    f.items = Array.from({ length: 50 }, () => f.items[0]!)
    sessionsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 1)
    const pager = sectionOf(w, 2).findAll('.cv__page')
    expect(pager[1]!.attributes('disabled')).toBeUndefined()
  })

  it('★ 把每页条数改成 20 且取满 20 行 ⇒ 「下一页」可用（阈值跟着请求值走）', async () => {
    const f = sessionsFixture()
    f.items = Array.from({ length: 20 }, () => f.items[0]!)
    sessionsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 1)
    // 先选 20（触发重载），再取满 20 行
    await sectionOf(w, 2).find('select').setValue(20)
    await flushPromises()
    await flushPromises()
    const pager = sectionOf(w, 2).findAll('.cv__page')
    expect(pager[1]!.attributes('disabled')).toBeUndefined()
  })

  it('未取满 ⇒ 「下一页」禁用（负控：20 行 vs 每页 50）', async () => {
    const f = sessionsFixture()
    f.items = Array.from({ length: 20 }, () => f.items[0]!)
    sessionsMock.mockResolvedValue(f)
    const w = await mountView()
    await clickLoad(w, 1)
    const pager = sectionOf(w, 2).findAll('.cv__page')
    expect(pager[1]!.attributes('disabled')).toBeDefined()
  })

  it('每页条数选择器只给后端认可的值（越界会静默回落 50）', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    const values = sectionOf(w, 2).find('select').findAll('option').map((o) => Number(o.element.value))
    expect(values).toEqual([20, 50, 100, SESSIONS_PAGE_SIZE_MAX])
    expect(values).not.toContain(0)
    expect(values).not.toContain(500)
  })

  it('★ 每页条数要标明越界会静默回落 50', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 2).text()).toContain('静默回落成 50')
  })

  it('★ stats 报错时把上一份数据清掉（不能留旧值冒充新值）', async () => {
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).find('.cv__kv').exists()).toBe(true)

    statsMock.mockRejectedValue(new Error('boom'))
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).find('.cv__msg--err').exists()).toBe(true)
    expect(sectionOf(w, 1).find('.cv__kv').exists()).toBe(false)
  })

  it('stats 首次加载即失败时显示错误（负控）', async () => {
    statsMock.mockRejectedValue(new Error('boom'))
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).find('.cv__msg--err').exists()).toBe(true)
  })

  it('sessions 报错时清空数据，不留旧值', async () => {
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 2).findAll('.cv__row').length).toBeGreaterThan(0)
    sessionsMock.mockRejectedValue(new Error('boom'))
    await clickLoad(w, 1)
    expect(sectionOf(w, 2).find('.cv__msg--err').exists()).toBe(true)
    expect(sectionOf(w, 2).findAll('.cv__row').length).toBe(0)
  })

  it('400 显示「时间格式不对」而不是后端原文', async () => {
    statsMock.mockRejectedValue(Object.assign(new Error('invalid from'), { status: 400 }))
    const w = await mountView()
    await clickLoad(w, 0)

    expect(sectionOf(w, 1).find('.cv__msg--err').text()).toContain('RFC3339')
  })
})