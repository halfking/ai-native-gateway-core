import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import TuningProposalsView from './TuningProposalsView.vue'
import { fetchTuningProposals, TUNING_PROPOSALS_DEFAULT_LIMIT } from '@/api/autoRouteInsights'
import { setLocale, locale } from '@/i18n'

/**
 * TuningProposalsView 的四条不变量（2026-10-07）。
 *
 * 1. ★ **筛选器只能是 chip，不能是自由输入。**
 *    后端 status/category 是 allowlist（auto_route_tuning.go:188/193），
 *    非法值 400，而且**不做** TrimSpace / ToLower（对比 analytics 的 window
 *    用 strings.ToLower —— 两处不同）。用户手输 `Pending ` 必 400。
 *    判据：点了 `applied` chip 之后，发出去的参数必须正好是 `applied`。
 *
 * 2. ★★ **proposal / evidence 无 schema，取不到键就不显示。**
 *    写死 `p.model` 在 keyword_add 类型上就是 undefined，渲染出去是
 *    「该建议没有模型」—— 一个我们并不知道的结论。
 *
 * 3. ★ **陌生状态不给绿色。** 给看不懂的状态打 success 等于把
 *    「不知道」显示成「已生效」。
 *
 * 4. ★ limit 默认值必须与后端一致（50，不是拍脑袋的 100）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
});

vi.mock('@/api/autoRouteInsights', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/autoRouteInsights')>()
  return { ...actual, fetchTuningProposals: vi.fn() };
});

const proposalsMock = fetchTuningProposals as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(TuningProposalsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function chip(w: ReturnType<typeof mount>, label: string) {
  return w.findAll('button').find((b) => b.text() === label)
}

const WEIGHT_PROPOSAL = {
  id: 1,
  ts: '2026-10-07T10:00:00Z',
  category: 'weight_adjust',
  task_type: 'code_generation',
  proposal: { model: 'gpt-4o', from_weight: 3, to_weight: 5 },
  evidence: { sample_n: 42, success_rate: 0.81, reason: '权重偏低' },
  status: 'pending',
  reviewed_by: null,
  reviewed_at: null,
  applied_at: null,
  review_note: null,
}

/** ★ keyword_add 类型的建议结构**不同** —— 证明 UI 没有写死字段。 */
const KEYWORD_PROPOSAL = {
  id: 2,
  ts: '2026-10-06T10:00:00Z',
  category: 'keyword_add',
  task_type: null,
  proposal: { keyword: 'vue', mode: 'boost' },
  evidence: { observed: 7 },
  status: 'applied',
  reviewed_by: 'admin',
  reviewed_at: '2026-10-06T11:00:00Z',
  applied_at: '2026-10-06T11:05:00Z',
  review_note: '同意',
}

/** ★ 后端词表外的状态（DB 里被手改过就会这样）。 */
const WEIRD_STATUS = { ...KEYWORD_PROPOSAL, id: 3, status: 'archived_by_hand', proposal: {}, evidence: {} }

const EMPTY = { proposals: [], count: 0, filter: { status: '', category: '' } }

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  proposalsMock.mockResolvedValue({ proposals: [WEIGHT_PROPOSAL], count: 1, filter: { status: '', category: '' } })
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('入口与默认参数', () => {
  it('挂载即请求一次，且默认 limit = 50（后端 :197 的值）', async () => {
    await mountView()
    expect(proposalsMock).toHaveBeenCalledTimes(1)
    expect(proposalsMock.mock.calls[0]![0]).toMatchObject({ limit: TUNING_PROPOSALS_DEFAULT_LIMIT })
    // ★ 这条是为了防「默认值拍成 100」——数字写错了用户只会觉得少了几条
    expect(TUNING_PROPOSALS_DEFAULT_LIMIT).toBe(50)
  })

  it('初始不过滤（空串在 allowlist 内，客户端索性不发）', async () => {
    await mountView()
    const p = proposalsMock.mock.calls[0]![0] as Record<string, unknown>
    expect(p.status).toBe('')
    expect(p.category).toBe('')
  })
})

describe('★ 判据 1：筛选 chip 发出去的参数必须正好是后端 allowlist 里的字面值', () => {
  it('点「已生效」⇒ 发 status=applied', async () => {
    const w = await mountView()
    await chip(w, '已生效')!.trigger('click')
    await flushPromises()
    expect(proposalsMock.mock.calls[1]![0]).toMatchObject({ status: 'applied' })
  })

  it('点类别「加关键词」⇒ 发 category=keyword_add', async () => {
    const w = await mountView()
    await chip(w, '加关键词')!.trigger('click')
    await flushPromises()
    expect(proposalsMock.mock.calls[1]![0]).toMatchObject({ category: 'keyword_add' })
  })

  // ★ 没有自由输入框 —— 自由输入是这条 API 的头号死法（手输带空格/大小写错 → 400）
  it('页面上没有自由文本输入（只有 chip + limit 数字框）', async () => {
    const w = await mountView()
    const texts = w.findAll('input[type="text"]')
    expect(texts.length).toBe(0)
  })

  it('★ 状态与类别两个分组各有且仅有「全部」这一个复位入口', async () => {
    const w = await mountView()
    const alls = w.findAll('button').filter((b) => b.text() === '全部')
    expect(alls.length).toBe(2)
  })
})

describe('★ 判据 2：proposal / evidence 无 schema，取不到就不显示', () => {
  it('weight_adjust 类型渲染 from_weight / to_weight', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('from_weight')
    expect(text).toContain('5')
    expect(text).toContain('sample_n')
  })

  // ★ 这条最关键：keyword_add 类型没有 model 字段。
  //   若 UI 写死 p.model，就会渲染出一行「model:（空）」——
  //   等于宣称「这条建议没有模型」，而真相是「这类建议本来就不带模型」。
  it('★ keyword_add 类型不显示 model / weight 行（取不到就不显示）', async () => {
    proposalsMock.mockResolvedValue({
      proposals: [KEYWORD_PROPOSAL],
      count: 1,
      filter: { status: '', category: '' },
    })
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('keyword')
    expect(text).not.toContain('from_weight')
    expect(text).not.toContain('to_weight')
  })

  // ★ 空 proposal 对象 ⇒ 整段 kv 不出现，而不是空壳
  it('proposal 为空对象 ⇒ 不渲染任何 kv 行', async () => {
    proposalsMock.mockResolvedValue({
      proposals: [{ ...WEIGHT_PROPOSAL, proposal: {}, evidence: {} }],
      count: 1,
      filter: { status: '', category: '' },
    })
    const w = await mountView()
    expect(w.text()).toContain('待审')
    expect(w.text()).not.toContain('from_weight')
  })
})

describe('★ 判据 3：陌生状态不得显示成「已生效」', () => {
  // ★ 断言必须**收窄到卡片上的状态标签**。
  //   整页 w.text() 必然含筛选 chip「已生效」（它是筛选项，不是这一条的状态），
  //   用整页断言会把「chip 在页面上」误判成「这条建议被显示成已生效」——
  //   判据量的不是它声称要量的那件事。
  it('词表外状态显示「未知状态」', async () => {
    proposalsMock.mockResolvedValue({
      proposals: [WEIRD_STATUS],
      count: 1,
      filter: { status: '', category: '' },
    })
    const w = await mountView()
    expect(w.find('.tp__item-status').text()).toBe('未知状态')
    // 反向：卡片里不许出现「已生效」
    expect(w.find('.tp__item').text()).not.toContain('已生效')
  })

  it('★ 陌生状态的状态点不得是 success 绿（StatusDot 不带 --success）', async () => {
    proposalsMock.mockResolvedValue({
      proposals: [WEIRD_STATUS],
      count: 1,
      filter: { status: '', category: '' },
    })
    const w = await mountView()
    expect(w.find('.status-dot--success').exists()).toBe(false)
  })

  it('已生效状态确实是绿的（证明上一条不是恒真）', async () => {
    proposalsMock.mockResolvedValue({
      proposals: [KEYWORD_PROPOSAL],
      count: 1,
      filter: { status: '', category: '' },
    })
    const w = await mountView()
    expect(w.find('.status-dot--success').exists()).toBe(true)
  })
})

describe('审核轨迹与空态', () => {
  it('已生效的建议显示审核人与审核时间', async () => {
    proposalsMock.mockResolvedValue({
      proposals: [KEYWORD_PROPOSAL],
      count: 1,
      filter: { status: '', category: '' },
    })
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('审核人')
    expect(text).toContain('admin')
    expect(text).toContain('同意')
  })

  it('待审的建议不显示审核轨迹（没审过就是没有，不留空行）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('审核人')
  })

  // ★ 后端显式初始化为 []（:238 注释说 nil 会让 .length 崩），
  //   但兜一层仍然要测 —— 崩在别处比显示空态难查得多。
  it('proposals 为 null ⇒ 显示空态而不是崩', async () => {
    proposalsMock.mockResolvedValue({ proposals: null, count: 0, filter: { status: '', category: '' } })
    const w = await mountView()
    expect(w.text()).toContain('没有建议')
  })

  it('回显后端实际用的过滤条件', async () => {
    proposalsMock.mockResolvedValue({ ...EMPTY, filter: { status: 'pending', category: 'keyword_add' } })
    const w = await mountView()
    expect(w.text()).toContain('pending')
    expect(w.text()).toContain('keyword_add')
  })

  it('403 ⇒ 报「仅超管」', async () => {
    proposalsMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    expect(w.text()).toContain('仅超管')
  })
})