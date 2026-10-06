import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import SessionContextView from './SessionContextView.vue'
import {
  fetchExtractionStatus,
  fetchTitlesBatch,
  sessionTitleMapKey,
  type ExtractionStatus,
  type TitlesBatchResult,
} from '@/api/sessionContext'
import { setLocale, locale } from '@/i18n'

/**
 * SessionContextView 的不变量（2026-10-07）。
 *
 * 1. ★★★★★★ `extracted:false` 有**三种**成因且完全分不开（含 **DB 查询出错**）
 *     ⇒ 页面只能说「未能确定」，不许说「没抽过」。
 * 2. ★★★★★★ 异形端点：A 形只有 2 键、B 形 8 键 ⇒ 缺的字段**不能**当空值渲染。
 * 3. ★★★★★★★ map 键含**字面 NUL** ⇒ 只能拆开显示，不能按 taskId 直查。
 * 4. ★★★★ 批量结果的「空」有**四种**成因 ⇒ 分不开。
 * 5. ★★★★ 限幅是 `> 500`（正好 500 合法）。
 * 6. ★★ 抛错不许退化成「未抽取」。
 * 7. ★★ 抽屉席**必须不设** requiresRole（admin 档）。
 * 8. ★★ 四个写端点一个都不提供入口。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/sessionContext', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/sessionContext')>()
  return { ...actual, fetchExtractionStatus: vi.fn(), fetchTitlesBatch: vi.fn() }
})

const statusMock = fetchExtractionStatus as unknown as ReturnType<typeof vi.fn>
const titlesMock = fetchTitlesBatch as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

/** ★ A 形（后端 `admin/session_extract.go:241-244`）：**只有 2 个键**。 */
function statusNotExtracted(taskId = 'task-1'): ExtractionStatus {
  return JSON.parse(JSON.stringify({ task_id: taskId, extracted: false })) as ExtractionStatus
}

/** ★ B 形（`:267-276`）：8 个键齐全。 */
function statusExtracted(over: Record<string, unknown> = {}): ExtractionStatus {
  return JSON.parse(
    JSON.stringify({
      task_id: 'task-1',
      extracted: true,
      extracted_at: '2026-10-01T00:00:00Z',
      written: 12,
      skipped_noise: 3,
      skipped_duplicate: 1,
      status: 'done',
      detail: { turns: 20 },
      ...over,
    }),
  ) as ExtractionStatus
}

/** ★ 键形如 `task-1\u0000scoped-9`。 */
function titlesOf(pairs: Array<[string, string, string]> = [['task-1', 'scoped-9', '会话标题']]): TitlesBatchResult {
  const m: Record<string, string> = {}
  for (const [t, s, title] of pairs) m[sessionTitleMapKey(t, s)] = title
  return JSON.parse(JSON.stringify({ titles: m })) as TitlesBatchResult
}

/** ★ helper 接受覆盖 + `instanceof Error` 分派。 */
function setAll(o: { status?: unknown; titles?: unknown } = {}): void {
  if (o.status instanceof Error) statusMock.mockRejectedValue(o.status)
  else statusMock.mockResolvedValue(o.status ?? statusNotExtracted())
  if (o.titles instanceof Error) titlesMock.mockRejectedValue(o.titles)
  else titlesMock.mockResolvedValue(o.titles ?? titlesOf())
}

async function mountView(o: Parameters<typeof setAll>[0] = {}): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  setAll(o)
  const w = mount(SessionContextView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

/** ★ 按面板作用域取文本（`.sc__item` 只在批量面板，全页取也安全但仍按面板更稳）。 */
function panelText(w: ReturnType<typeof mount>, title: string): string {
  const p = w
    .findAll('.sc__panel')
    .find((x) => (x.find('.sc__panel-title').exists() ? x.find('.sc__panel-title').text() : '') === title)
  return p?.text() ?? ''
}

async function setTaskId(w: ReturnType<typeof mount>, v: string): Promise<void> {
  await w.find('#sc-task').setValue(v)
}

async function setIds(w: ReturnType<typeof mount>, v: string): Promise<void> {
  await w.find('#sc-ids').setValue(v)
}

/** ★ 批量面板的「查标题」按钮（第 2 个）。 */
async function clickTitles(w: ReturnType<typeof mount>): Promise<void> {
  const btns = w.findAll('button')
  await btns[btns.length - 1]!.trigger('click')
  await flushPromises()
  await flushPromises()
}

/** ★ 抽取面板的按钮（第 1 个）。 */
async function clickStatus(w: ReturnType<typeof mount>): Promise<void> {
  await w.findAll('button')[0]!.trigger('click')
  await flushPromises()
  await flushPromises()
}

/**
 * ★★ 数据格里的**标签**与**取值**。
 *   面板文本里包含标题与免责文案 —— 例如面板标题「记忆抽取状态」本身就含子串
 *   「抽取状态」，免责文案里也含「没能确定」⇒ 面板级 `not.toContain` **恒被自己的文案判红**。
 *   ⇒ 断言一律落到 `.sc__cell-l` / `.sc__cell-v` 这个作用域上。
 */
function cellLabels(w: ReturnType<typeof mount>, panelTitle: string): string[] {
  const p = w
    .findAll('.sc__panel')
    .find((x) => (x.find('.sc__panel-title').exists() ? x.find('.sc__panel-title').text() : '') === panelTitle)
  return (p?.findAll('.sc__cell-l') ?? []).map((n) => n.text())
}

function cellValues(w: ReturnType<typeof mount>, panelTitle: string): string[] {
  const p = w
    .findAll('.sc__panel')
    .find((x) => (x.find('.sc__panel-title').exists() ? x.find('.sc__panel-title').text() : '') === panelTitle)
  return (p?.findAll('.sc__cell-v') ?? []).map((n) => n.text())
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

describe('★★★★★★ 抽屉席**必须不设** requiresRole（admin 档）', () => {
  it('★★★★★★ session-context 席存在且没有 requiresRole', async () => {
    const { DRAWER_NAV, navItemsFor } = await import('@/config/appNav')
    const seat = DRAWER_NAV.find((i) => i.key === 'session-context')
    expect(seat).toBeDefined()
    expect(seat?.requiresRole).toBeUndefined()
    expect(navItemsFor(DRAWER_NAV, 'tenant_admin').some((i) => i.key === 'session-context')).toBe(true)
  })
})

describe('★★★★★★★★ `extracted:false` 三种成因分不开 ⇒ 只说「未能确定」', () => {
  it('★★★★★★★ 未抽取 ⇒ 显示「未能确定」，**不说**「没抽过」', async () => {
    const w = await mountView({ status: statusNotExtracted() })
    await setTaskId(w, 'task-1')
    await clickStatus(w)
    // ★★ 面板级 `toContain('未能确定')` 会被**我自己写的免责末句**喂饱
    //   （statusIndeterminateNote 结尾就是「所以下面只能说「未能确定」」）⇒ 判据无牙。
    //   ⇒ 必须按**结果节点 + 具体措辞**判：indeterminateResult 那句以「未能确定：」开头。
    const resultNotes = w
      .findAll('.sc__panel')
      .filter((x) => (x.find('.sc__panel-title').exists() ? x.find('.sc__panel-title').text() : '') === '记忆抽取状态')[0]!
      .findAll('.sc__note--warn')
      .map((n) => n.text())
    expect(resultNotes.some((t) => t.startsWith('未能确定：'))).toBe(true)
    // 「是否已抽取」这一格的取值必须落在 `cellValues` 里判；
    //   面板文本里我自己写的免责文案也含「没抽过」「未能确定」等词，
    //   面板级 not.toContain 会被它们判红。
    const vals = cellValues(w, '记忆抽取状态')
    expect(vals).toContain('未能确定')
    expect(vals.filter((v) => v === '是' || v === '否')).toHaveLength(0)
    expect(vals.filter((v) => v.includes('抽过'))).toHaveLength(0)
  })

  it('★★★★★★★ 页面明说这是三种成因（含 DB 出错）且返回同形', async () => {
    const w = await mountView()
    expect(panelText(w, '记忆抽取状态')).toContain('数据库查询出错')
    expect(panelText(w, '记忆抽取状态')).toContain('逐字节相同')
  })

  it('★★★★★ 已抽取 ⇒ 显示「是」与各字段', async () => {
    const w = await mountView({ status: statusExtracted() })
    await setTaskId(w, 'task-1')
    await clickStatus(w)
    const vals = cellValues(w, '记忆抽取状态')
    expect(vals).toContain('是')
    expect(vals).toContain('done')
    expect(vals).toContain('12')
    expect(vals).toContain('3')
    expect(vals).not.toContain('未能确定')
  })

  it('★★★★★ ★ task_id 叫 `titles` ⇒ 提示会被后端特判截胡', async () => {
    const w = await mountView()
    await setTaskId(w, 'titles')
    await w.vm.$nextTick()
    expect(panelText(w, '记忆抽取状态')).toContain('截胡')
    await setTaskId(w, 'task-1')
    await w.vm.$nextTick()
    expect(panelText(w, '记忆抽取状态')).not.toContain('截胡')
  })
})

describe('★★★★★★ A 形缺的字段不能当空值渲染', () => {
  it('★★★★★★ 未抽取时**不出现**状态/计数/详情那几个字段', async () => {
    const w = await mountView({ status: statusNotExtracted() })
    await setTaskId(w, 'task-1')
    await clickStatus(w)
    // ★ 判**标签列表**而不是面板文本：面板标题「记忆抽取状态」本身就含子串「抽取状态」
    const labels = cellLabels(w, '记忆抽取状态')
    expect(labels).toContain('是否已抽取')
    expect(labels).not.toContain('抽取状态')
    expect(labels).not.toContain('写入条数')
    expect(labels).not.toContain('跳过（噪声）')
    expect(labels).not.toContain('抽取时间')
    expect(labels).not.toContain('详情')
    // 但要说清「字段不存在」这件事。★ 断言串从**实际 i18n 文案**里挑、且不含 `**`：
    //   lacksFieldsNote = 「…只有任务号与「未抽取」两个字段，其余字段**不存在**（不是空值）。」
    expect(panelText(w, '记忆抽取状态')).toContain('其余字段')
  })

  it('★★★★★ 已抽取时这些字段**都在**', async () => {
    const w = await mountView({ status: statusExtracted() })
    await setTaskId(w, 'task-1')
    await clickStatus(w)
    const labels = cellLabels(w, '记忆抽取状态')
    expect(labels).toContain('抽取状态')
    expect(labels).toContain('写入条数')
    expect(labels).toContain('跳过（噪声）')
    expect(labels).toContain('抽取时间')
    expect(labels).toContain('详情')
    // ★ 已抽取时那句「字段不存在」**不该**出现（我第一版把它无条件渲染了）
    expect(panelText(w, '记忆抽取状态')).not.toContain('其余字段')
  })

  it('★★★★★★ `detail` 是 JSON 标量 null ⇒ 折叠成「JSON 空值」而不是 `null` 字样', async () => {
    const w = await mountView({ status: statusExtracted({ detail: null }) })
    await setTaskId(w, 'task-1')
    await clickStatus(w)
    const p = panelText(w, '记忆抽取状态')
    expect(p).toContain('JSON 空值')
    expect(p).not.toContain('"detail":null')
  })

  it('★★★★★ detail 是对象时原样显示', async () => {
    const w = await mountView({ status: statusExtracted({ detail: { turns: 20 } }) })
    await setTaskId(w, 'task-1')
    await clickStatus(w)
    expect(panelText(w, '记忆抽取状态')).toContain('turns')
  })
})

describe('★★★★★★★★★ map 键含 NUL ⇒ 拆开显示', () => {
  it('★★★★★★★ 显示的是**拆开后的** taskId 与 scopedSessionId', async () => {
    const w = await mountView({ titles: titlesOf([['task-1', 'scoped-9', '标题甲']]) })
    await setIds(w, 'task-1')
    await clickTitles(w)
    const p = panelText(w, '会话标题（批量）')
    expect(p).toContain('标题甲')
    expect(p).toContain('task-1')
    expect(p).toContain('scoped-9')
    // ★ 页面上**不该**出现裸 NUL 字符
    expect(p.includes('\u0000')).toBe(false)
  })

  it('★★★★★ scoped 为空 ⇒ 显示占位而不是空白', async () => {
    const w = await mountView({ titles: titlesOf([['task-1', '', '标题乙']]) })
    await setIds(w, 'task-1')
    await clickTitles(w)
    expect(panelText(w, '会话标题（批量）')).toContain('会话作用域 ID：—')
  })

  it('★★★★★ 页面明说「按任务 ID 直查永远查不到」', async () => {
    const w = await mountView()
    expect(panelText(w, '会话标题（批量）')).toContain('永远查不到')
  })
})

describe('★★★★★★ 批量结果的「空」有四种成因', () => {
  it('★★★★★★★ 返回空 map ⇒ 提示「证明不了没有标题」', async () => {
    const w = await mountView({ titles: titlesOf([]) })
    await setIds(w, 'task-1')
    await clickTitles(w)
    const p = panelText(w, '会话标题（批量）')
    expect(p).toContain('证明不了')
    expect(p).toContain('数据库查询出错')
  })

  it('★★★★★ 有结果 ⇒ 不出那条提示（不能恒真）', async () => {
    const w = await mountView({ titles: titlesOf([['task-1', 's', 'T']]) })
    await setIds(w, 'task-1')
    await clickTitles(w)
    expect(panelText(w, '会话标题（批量）')).not.toContain('证明不了')
  })

  it('★★★★★★ 少回来的键被逐个点出', async () => {
    const w = await mountView({ titles: titlesOf([['task-1', '', 'T']]) })
    await setIds(w, 'task-1\ntask-2')
    await clickTitles(w)
    const p = panelText(w, '会话标题（批量）')
    expect(p).toContain('问了 2 个，只回来 1 个')
  })

  it('★★★★★ 页面明说「键缺失 ≠ 空串标题」', async () => {
    const w = await mountView()
    // ★ 实际文案是「…**整个省略**的，不会有空字符串这种值。」—— 我先前两次都凭印象手打
    expect(panelText(w, '会话标题（批量）')).toContain('不会有空字符串这种值')
  })

  it('★★★★★ 页面明说它是 POST-only', async () => {
    const w = await mountView()
    expect(panelText(w, '会话标题（批量）')).toContain('只能用 POST')
  })
})

describe('★★★★★★ 输入归一与限幅', () => {
  it('★★★★★★ 去空 + 去重 + trim 后计数', async () => {
    const w = await mountView()
    await setIds(w, ' task-1 \n\n  task-1 \n task-2 ')
    await w.vm.$nextTick()
    expect(panelText(w, '会话标题（批量）')).toContain('去空去重后共 2 个')
  })

  // ★★★ 这条是**变异 V9 逼出来的**：把 `.split(/[\n,;\s]+/)` 换成 `.split(/\n/)` 时，
  //   原来的用例**照样全绿** —— 因为喂的输入**全是换行分隔**的。
  //   ⇒ 缺口是「逗号/分号/空格分隔」这一侧**从没喂过**。
  it('★★★★★★★ 逗号 / 分号 / 空格分隔也要能切（否则整串会被当成一个 id，全 miss）', async () => {
    const w = await mountView()
    await setIds(w, 'task-1, task-2; task-3   task-4')
    await w.vm.$nextTick()
    expect(panelText(w, '会话标题（批量）')).toContain('去空去重后共 4 个')
  })

  it('★★★★★★★ 正好 500 个**合法**（后端是 `> 500` 才拒）', async () => {
    const w = await mountView()
    const ids = Array.from({ length: 500 }, (_, i) => `t${i}`).join('\n')
    await setIds(w, ids)
    await w.vm.$nextTick()
    const p = panelText(w, '会话标题（批量）')
    expect(p).toContain('去空去重后共 500 个')
    expect(p).not.toContain('超了')
    // ★ 按钮不禁用
    const btn = w.findAll('button')[w.findAll('button').length - 1]!
    expect(btn.attributes('disabled')).toBeUndefined()
  })

  it('★★★★★★ 501 个 ⇒ 提示超限且按钮禁用', async () => {
    const w = await mountView()
    const ids = Array.from({ length: 501 }, (_, i) => `t${i}`).join('\n')
    await setIds(w, ids)
    await w.vm.$nextTick()
    const p = panelText(w, '会话标题（批量）')
    expect(p).toContain('超了')
    expect(p).toContain('正好 500 个是合法的')
    const btn = w.findAll('button')[w.findAll('button').length - 1]!
    expect(btn.attributes('disabled')).toBeDefined()
  })

  // ★★★ 与抽取侧同一个形状的缺口：首屏就失败证明不了「出错时必须清空旧结果」。
  it('★★★★★★★★ 批量：先成功、再失败 ⇒ 旧结果必须消失', async () => {
    const w = await mountView({ titles: titlesOf([['task-1', '', '旧标题']]) })
    await setIds(w, 'task-1')
    await clickTitles(w)
    expect(panelText(w, '会话标题（批量）')).toContain('旧标题')

    titlesMock.mockRejectedValue(new Error('later boom'))
    await clickTitles(w)
    await flushPromises()

    expect(panelText(w, '会话标题（批量）')).not.toContain('旧标题')
    expect(panelText(w, '会话标题（批量）')).toContain('later boom')
  })

  it('★★★★★ 没有 id 时按钮禁用', async () => {
    const w = await mountView()
    await setIds(w, '   \n  ')
    await w.vm.$nextTick()
    const btn = w.findAll('button')[w.findAll('button').length - 1]!
    expect(btn.attributes('disabled')).toBeDefined()
  })
})

describe('★★★★★ 错误态', () => {
  // ★ 「出错」与「未抽取」在页面上是两回事 —— 后端把 DB 故障也变成 200 + 未抽取，
  //   但真正的 HTTP 失败（503/400/405）必须显示错误条，且**不许**退化。
  it('★★★★★★ 抽取请求失败 ⇒ 显示错误，且**不**显示「未能确定」', async () => {
    const w = await mountView({ status: new Error('database not configured') })
    await setTaskId(w, 'task-1')
    await clickStatus(w)
    const p = panelText(w, '记忆抽取状态')
    expect(p).toContain('database not configured')
    expect(p).toContain('没有配置数据库')
    // ★ 判数据格：出错时**没有**结果区，就不该有「是否已抽取」这一格
    expect(cellLabels(w, '记忆抽取状态')).not.toContain('是否已抽取')
  })

  // ★★★ 这是**变异 V2 逼出来的**：把 catch 里的 `status.value = null` 删掉，
  //   原来的用例**照样全绿** —— 因为视图**首屏不自动加载**，
  //   首屏就失败时根本没有「旧结果」可留。
  //   ⇒ 必须造「先成功 → 再失败」序列，才能证明出错时**清空**了。
  it('★★★★★★★ 先成功、再失败 ⇒ 旧结果必须消失（不是留在屏幕上）', async () => {
    const w = await mountView({ status: statusExtracted() })
    await setTaskId(w, 'task-1')
    await clickStatus(w)
    // 先确认成功态确实渲染出来了
    expect(cellLabels(w, '记忆抽取状态')).toContain('写入条数')
    expect(cellValues(w, '记忆抽取状态')).toContain('done')

    statusMock.mockRejectedValue(new Error('later boom'))
    await clickStatus(w)
    await flushPromises()

    // ★ 关键：不是「字段变成空」，而是**整个结果区连同数据格一起消失**
    expect(cellLabels(w, '记忆抽取状态')).not.toContain('写入条数')
    expect(cellValues(w, '记忆抽取状态')).not.toContain('done')
    expect(panelText(w, '记忆抽取状态')).toContain('later boom')
  })

  it('★★★★★ 批量请求失败 ⇒ 只影响批量面板', async () => {
    const w = await mountView({ titles: new Error('too many keys (max 500)') })
    await setIds(w, 'task-1')
    await clickTitles(w)
    expect(panelText(w, '会话标题（批量）')).toContain('too many keys')
    expect(panelText(w, '记忆抽取状态')).not.toContain('too many keys')
  })
})

describe('★★★★★ 只读页：四个写端点一个都不提供', () => {
  it('★★★★★★ 按钮只有「查抽取状态」和「批量查标题」两个', async () => {
    const w = await mountView()
    const labels = w.findAll('button').map((b) => b.text())
    expect(labels).toHaveLength(2)
    expect(labels[0]).toContain('查询抽取状态')
    expect(labels[1]).toContain('批量查标题')
  })

  it('★★★★★ 页面明说四个写操作都没碰', async () => {
    const w = await mountView()
    expect(w.text()).toContain('抽取到记忆库')
    expect(w.text()).toContain('调模型')
  })
})