import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AnnotationsView from './AnnotationsView.vue'
import { fetchAnnotationStats, fetchSamples, fetchFirstTurnSamples } from '@/api/annotations'
import { setLocale, locale } from '@/i18n'

/**
 * AnnotationsView 的不变量（2026-10-07，第六十三批）。
 *
 * ★ 钉住的四处「不能都渲染成同一个东西」：
 *   ① **稀疏键**：`FirstTurnSample` 5 个标注字段带 `omitempty`
 *      （handler.go:113-120）⇒ 未标注时后端**根本不写这些键**。
 *      缺键 ⇒ 「未标注」，不是 0、不是空串、也不是「标注正确」。
 *   ② **零标注时 stats 整条 500** ⇒ 不能渲染成「统计为零」。
 *   ③ **`accuracy_percent` 的 0 与「无意义」不可分** ⇒ 看 `total_annotations`。
 *   ④ **`strategy` 是条件键** ⇒ 键缺失 = recent；且 first-turn 段没有这个键。
 * 外加：**三个分布块都要渲染**（漏一块会让人以为「没人标注」）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/annotations', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/annotations')>()
  return { ...actual, fetchAnnotationStats: vi.fn(), fetchSamples: vi.fn(), fetchFirstTurnSamples: vi.fn() }
})

const statsMock = fetchAnnotationStats as unknown as ReturnType<typeof vi.fn>
const samplesMock = fetchSamples as unknown as ReturnType<typeof vi.fn>
const firstTurnMock = fetchFirstTurnSamples as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(AnnotationsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>

function sectionOf(w: W, index: number) {
  return w.findAll('.an__section')[index]!
}

/**
 * ★ 按 `<dt>` **精确**文本找行。
 * 直接用 `r.text().includes('人工判定')` 会先撞上「人工判定模型」那一行 ——
 * 子串匹配在标签互为前缀时是典型的样本选歪。
 */
function rowByLabel(w: W, index: number, label: string) {
  const row = sectionOf(w, index).findAll('.an__kv-row').find((r) => r.find('dt').text() === label)
  if (!row) throw new Error(`未找到标签为「${label}」的行`)
  return row
}

function loadBtn(w: W, n: number) {
  return w.findAll('.an__load')[n]!
}
async function clickLoad(w: W, n: number): Promise<void> {
  await loadBtn(w, n).trigger('click')
  await flushPromises()
  await flushPromises()
}

/* ── 夹具：逐字照抄后端 ─────────────────────────────────────────────── */

const STATS_FULL = {
  overall: {
    total_annotations: 120, correct_count: 100, incorrect_count: 20,
    accuracy_percent: 83.33, num_annotators: 4,
    first_annotation_at: '2026-10-01T02:00:00Z', last_annotation_at: '2026-10-08T02:00:00Z',
  },
  by_provider: [
    { provider: 'openai', total_predictions: 80, correct_predictions: 70, incorrect_predictions: 10, accuracy_percent: 87.5, avg_confidence: 0.9 },
  ],
  by_annotator: [
    {
      annotator: 'alice', total_annotations: 60, correct_count: 50, incorrect_count: 10,
      accuracy_percent: 83.33, first_annotation_at: '2026-10-01T02:00:00Z',
      last_annotation_at: '2026-10-07T02:00:00Z', hours_span: 140,
    },
  ],
  by_reason: [{ reason: 'wrong_model', count: 12, percentage: 60 }],
}

/** ★ 零标注（total=0）⇒ 百分数无意义。 */
const STATS_ZERO = {
  ...STATS_FULL,
  overall: {
    total_annotations: 0, correct_count: 0, incorrect_count: 0, accuracy_percent: 0,
    num_annotators: 0, first_annotation_at: null, last_annotation_at: null,
  },
  by_provider: [], by_annotator: [], by_reason: [],
}

/** ★ overall 是指针字段 ⇒ 可以整个块是 null。 */
const STATS_NULL_OVERALL = { ...STATS_FULL, overall: null }

/** ★ 计数器自相矛盾。 */
const STATS_CONTRADICT = {
  ...STATS_FULL,
  overall: { ...STATS_FULL.overall, incorrect_count: 40 },
}

/** ★ provider 层矛盾。 */
const STATS_PROVIDER_CONTRADICT = {
  ...STATS_FULL,
  by_provider: [{ ...STATS_FULL.by_provider[0]!, incorrect_predictions: 30 }],
}

const SAMPLES_FULL = {
  samples: [
    { request_id: 'r1', chosen_model: 'gpt-4o', task_type: 'code' },
    { request_id: 'r2', chosen_model: 'claude', task_type: 'chat' },
  ],
  total: 2,
}

/** ★ 未标注 ⇒ 5 个 omitempty 键**全部不存在**。 */
const FIRST_TURN_UNANNOTATED = {
  samples: [{
    session_id: 's1', request_id: 'r1', ts: '2026-10-08T02:00:00Z',
    title: null, client: null, task_type: 'code', chosen_model: 'gpt-4o',
    confidence: 0.9, status_code: 200, success: true, latency_ms: 120,
    total_turns: 4,
  }],
  total: 1,
}

/** ★ 已标注 ⇒ 那些键才出现。 */
const FIRST_TURN_ANNOTATED = {
  samples: [{
    ...FIRST_TURN_UNANNOTATED.samples[0],
    human_task_type: 'coding', human_model: 'claude', human_provider: 'anthropic',
    is_correct: false, reason: 'wrong_model', annotator: 'alice',
    annotated_at: '2026-10-08T03:00:00Z',
  }],
  total: 1,
}

beforeEach(() => {
  setLocale('zh-CN')
  statsMock.mockReset()
  samplesMock.mockReset()
  firstTurnMock.mockReset()
})

afterEach(() => {
  mountedList.forEach((w) => w.unmount())
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

/* ═══════════════════════════════════════════════════════════════════════
 * ① 稀疏键：未标注 ≠ 0
 * ═══════════════════════════════════════════════════════════════════════ */

describe('AnnotationsView ① 稀疏键', () => {
  it('★★★ 未标注时缺键渲染成「未标注」，不是 0 也不是「正确」', async () => {
    firstTurnMock.mockResolvedValue(FIRST_TURN_UNANNOTATED)
    const w = await mountView()
    await clickLoad(w, 2)
    const sec = sectionOf(w, 2)

    const humanModelRow = rowByLabel(w, 2, '人工判定模型')
    const verdictRow = rowByLabel(w, 2, '人工判定')
    expect(humanModelRow.find('dd').element.textContent).toBe('未标注')
    expect(verdictRow.find('dd').element.textContent).toBe('未标注')
    // ★ 绝不能是「正确」—— 未标注 ≠ 标注正确
    expect(sec.text()).not.toContain('正确')
    expect(sec.text()).not.toContain('错误')
  })

  it('★ 缺键位置带独立 class（判据锚在原因上而非字形）', async () => {
    firstTurnMock.mockResolvedValue(FIRST_TURN_UNANNOTATED)
    const w = await mountView()
    await clickLoad(w, 2)
    for (const label of ['人工判定模型', '人工判定', '原因']) {
      expect(rowByLabel(w, 2, label).find('dd').classes()).toContain('an__nodata')
    }
  })

  it('★★★ 已标注 ⇒ 键出现，且判错要说「错误」', async () => {
    // ★ 这是上一条的判别样本：同一条渲染路径，两种取值必须分叉
    firstTurnMock.mockResolvedValue(FIRST_TURN_ANNOTATED)
    const w = await mountView()
    await clickLoad(w, 2)
    const verdictRow = rowByLabel(w, 2, '人工判定')
    expect(verdictRow.find('dd').element.textContent).toBe('错误')
    expect(verdictRow.find('dd').classes()).not.toContain('an__nodata')

    const humanModelRow = rowByLabel(w, 2, '人工判定模型')
    expect(humanModelRow.find('dd').element.textContent).toBe('claude')
  })

  it('★ 未标注的行显示「未标注」徽标，已标注的不显示', async () => {
    firstTurnMock.mockResolvedValue(FIRST_TURN_UNANNOTATED)
    const w1 = await mountView()
    await clickLoad(w1, 2)
    expect(sectionOf(w1, 2).findAll('.an__badge').length).toBe(1)
    w1.unmount()
    mountedList = []

    firstTurnMock.mockReset()
    firstTurnMock.mockResolvedValue(FIRST_TURN_ANNOTATED)
    const w2 = await mountView()
    await clickLoad(w2, 2)
    expect(sectionOf(w2, 2).findAll('.an__badge').length).toBe(0)
  })

  it('★ is_correct = true ⇒ 说「正确」', async () => {
    firstTurnMock.mockResolvedValue({
      samples: [{ ...FIRST_TURN_ANNOTATED.samples[0]!, is_correct: true }], total: 1,
    })
    const w = await mountView()
    await clickLoad(w, 2)
    const row = rowByLabel(w, 2, '人工判定')
    expect(row.find('dd').element.textContent).toBe('正确')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ② 零标注 500 / overall 为 null
 * ═══════════════════════════════════════════════════════════════════════ */

describe('AnnotationsView ② 统计端的 500 与 null', () => {
  it('★★ 零标注导致的 500 ⇒ 明说「不能读成没人标注」', async () => {
    // ★★★ 本批最关键的一条：stats 端在零标注时整条 500
    //   （annotation/stats.go 查无聚合单行汇总表 ⇒ ErrNoRows）
    //   ⇒ 页面**不得**把它渲染成「统计为零」。
    statsMock.mockRejectedValue(Object.assign(new Error('failed to get overall stats'), { status: 500 }))
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).toContain('查询统计失败')
    expect(txt).toContain('不能读成')
    // 且不得渲染任何 KPI（否则等于把 500 画成「0 条标注」）
    expect(sectionOf(w, 0).findAll('.an__kpi').length).toBe(0)
  })

  it('★ overall 为 null ⇒ 说「统计块不可用」而不是零标注', async () => {
    statsMock.mockResolvedValue(STATS_NULL_OVERALL)
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).toContain('统计块不可用')
    expect(txt).not.toContain('无标注')
    expect(sectionOf(w, 0).findAll('.an__kpi').length).toBe(0)
  })

  it('零标注（200 且 total=0）⇒ 准确率显示「无标注」而不是 0.00%', async () => {
    statsMock.mockResolvedValue(STATS_ZERO)
    const w = await mountView()
    await clickLoad(w, 0)
    const txt = sectionOf(w, 0).text()
    expect(txt).toContain('无标注（准确率无意义）')
    expect(txt).not.toContain('0.00%')
  })

  it('★★★ 三个分布块全空 ⇒ 说「还没有任何可聚合的标注」', async () => {
    statsMock.mockResolvedValue(STATS_ZERO)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('还没有任何可聚合的标注')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ③ 准确率与矛盾
 * ═══════════════════════════════════════════════════════════════════════ */

describe('AnnotationsView ③ 准确率与自相矛盾', () => {
  it('有标注 ⇒ 显示真实准确率', async () => {
    statsMock.mockResolvedValue(STATS_FULL)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('83.33%')
  })

  it('★ 计数器矛盾 ⇒ 报警', async () => {
    statsMock.mockResolvedValue(STATS_CONTRADICT)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('计数器自相矛盾')
  })

  it('正常数据不报矛盾', async () => {
    statsMock.mockResolvedValue(STATS_FULL)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).not.toContain('计数器自相矛盾')
  })

  it('★ provider 层矛盾逐行报警', async () => {
    statsMock.mockResolvedValue(STATS_PROVIDER_CONTRADICT)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('大于')
  })

  it('★★★ by_annotator 块必须渲染（漏一块会让人以为没人标注）', async () => {
    statsMock.mockResolvedValue(STATS_FULL)
    const w = await mountView()
    await clickLoad(w, 0)
    const sec = sectionOf(w, 0)
    expect(sec.text()).toContain('按标注人')
    expect(sec.text()).toContain('alice')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ④ strategy 条件键 + 参数口径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('AnnotationsView ④ samples 参数口径', () => {
  it('★ strategy 键缺失 ⇒ 回显 recent，不是「未知」', async () => {
    samplesMock.mockResolvedValue(SAMPLES_FULL)
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('策略 recent')
  })

  it('★ strategy 下发时回显该值', async () => {
    samplesMock.mockResolvedValue({ ...SAMPLES_FULL, strategy: 'disagreement' })
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('策略 disagreement')
  })

  it('★★ size 越界 ⇒ 回显后端实际生效值（钳位到 200）', async () => {
    samplesMock.mockResolvedValue(SAMPLES_FULL)
    const w = await mountView()
    const sizeInput = sectionOf(w, 1).findAll('.an__input')[1]!
    await sizeInput.setValue('999')
    await flushPromises()
    expect(sectionOf(w, 1).text()).toContain('每页 200 条')
  })

  it('★ per_strata 越界 ⇒ 回显钳位到 20', async () => {
    samplesMock.mockResolvedValue(SAMPLES_FULL)
    const w = await mountView()
    const strataInput = sectionOf(w, 1).findAll('.an__input')[2]!
    await strataInput.setValue('99')
    await flushPromises()
    expect(sectionOf(w, 1).text()).toContain('每层 20 个')
  })

  it('★ 非分层策略时提示「每层配额不生效」', async () => {
    samplesMock.mockResolvedValue(SAMPLES_FULL)
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('每层配额不生效')
  })

  it('★ 切到分层策略后该提示消失', async () => {
    // 这条是上一条的判别样本
    samplesMock.mockResolvedValue(SAMPLES_FULL)
    const w = await mountView()
    const select = sectionOf(w, 1).find('select')!
    await select.setValue('stratified')
    await flushPromises()
    expect(sectionOf(w, 1).text()).not.toContain('每层配额不生效')
  })

  it('★ 翻页把新 page 真发出去', async () => {
    samplesMock.mockResolvedValue({
      samples: new Array(50).fill({ request_id: 'r' }), total: 120,
    })
    const w = await mountView()
    await clickLoad(w, 1)
    const sec = sectionOf(w, 1)
    await sec.findAll('.an__pager-btn')[1]!.trigger('click')
    await flushPromises()
    await flushPromises()
    expect((samplesMock.mock.calls[1]![0] as Record<string, unknown>).page).toBe(2)
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * first-turn 的缺省日期语义
 * ═══════════════════════════════════════════════════════════════════════ */

describe('AnnotationsView first-turn 日期语义', () => {
  it('★ 日期留空 ⇒ 明说「只看今天」而不是「全部时间」', async () => {
    firstTurnMock.mockResolvedValue(FIRST_TURN_UNANNOTATED)
    const w = await mountView()
    const sec = sectionOf(w, 2)
    expect(sec.text()).toContain('只看今天')
  })

  it('★ 填了日期后该提示消失', async () => {
    firstTurnMock.mockResolvedValue(FIRST_TURN_UNANNOTATED)
    const w = await mountView()
    const dateInput = sectionOf(w, 2).findAll('.an__input')[0]!
    await dateInput.setValue('2026-10-01')
    await flushPromises()
    expect(sectionOf(w, 2).text()).not.toContain('只看今天')
  })

  it('★ 日期留空 ⇒ 请求里不带 start_date（后端自己取今天）', async () => {
    firstTurnMock.mockResolvedValue(FIRST_TURN_UNANNOTATED)
    const w = await mountView()
    await clickLoad(w, 2)
    const q = firstTurnMock.mock.calls[0]![0] as Record<string, unknown>
    expect(q.startDate).toBeUndefined()
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * 加载与失败路径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('AnnotationsView 加载与失败路径', () => {
  it('★ 失败 ⇒ 清掉旧数据', async () => {
    samplesMock.mockResolvedValueOnce(SAMPLES_FULL)
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).findAll('.an__item').length).toBe(2)

    samplesMock.mockRejectedValueOnce(new Error('boom'))
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('boom')
    expect(sectionOf(w, 1).findAll('.an__item').length).toBe(0)
  })

  it('403 ⇒ 无权文案', async () => {
    statsMock.mockRejectedValue(Object.assign(new Error('Forbidden'), { status: 403 }))
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('无权查看标注工作台')
  })

  it('★ 三段互不串扰', async () => {
    statsMock.mockResolvedValue(STATS_FULL)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).findAll('.an__item').length).toBe(0)
    expect(sectionOf(w, 2).findAll('.an__item').length).toBe(0)
  })
})