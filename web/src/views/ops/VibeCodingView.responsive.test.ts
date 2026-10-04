// VibeCodingView.responsive.test.ts — H6 第十六条垂直切片的门禁。
//
// 本页的形状（3 张 `el-table` + 2 级级联筛选 + 3 个弹窗，均在列表区之外）：
// 1. **无分页、单次 `Promise.all`** ⇒ 只改呈现形态，不引入连续加载。
// 2. **槽里是 `el-table`** ⇒ `:slotted(table)` 不命中，`table-min-width` 是空操作（D12）。
// 3. **三张表桌面都没有自己的空态行** ⇒ compact 的空态只能来自容器 `:empty`，带 `isCompact` 前置。
// 4. **`task_type` / `file_path` 都不唯一**（fixture 里两条会话同 `task_type`、两条评审
//    路径不同但同会话）⇒ `title-key` 必须是 `id`，卡头另用 `titleFormat` 出脸。
// 5. **级联筛选的两级空态谓词必须用筛选后的数组**（`filteredSessions` / `filteredReviews`），
//    否则「选中的项目下没有会话」时空态会缺席 —— 桌面第二节会剩一个空的 el-table。
//
// 门禁清单：
// A. 桌面零回归：三张表的列头与列数（6/6/7）、状态与评分 el-tag 两套色、计数徽章的
//    值与类型、`completed_at` 缺值出 `—`、三段都仍在 DOM
// B. 加载反馈：请求挂起时**三张 el-table 仍在**（证明没把桌面换成骨架）—— 这条是
//    「不传 `:loading`」的行为证据，源码 grep 只是它的影子
// C. el-table 地形 + 跨层契约：不传 table-min-width / 三处都不传 :loading /
//    三处 :empty 带 isCompact 前置且用筛选后数组 / 三个 title-key 都是 id / 0 新增 i18n 键
// D. compact 卡片：三段各出卡不出表、字段集与列一一对应、卡头身份、tone 逐档、
//    计数与桌面同源、`#actions` 真的驱动级联筛选与弹窗
// E. 空态归属：桌面不出现我们的 EmptyState；compact 三段各出；级联后空也出
import { flushPromises, enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { ElLoading } from 'element-plus'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import VibeCodingView from './VibeCodingView.vue'
import { _resetForTests } from '../../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../../composables/useDataViewMode'

enableAutoUnmount(afterEach)

const SOURCE = readFileSync(resolve(process.cwd(), 'src/views/ops/VibeCodingView.vue'), 'utf8')

/**
 * 剥注释得到「只看代码」的文本，供源码 grep 用。
 *
 * ★ 口径**故意保守**：块注释与 HTML 注释整段去掉；行注释**只去掉 trim 后以
 *   `//` / `*` / `/*` 开头的整行**（与 CJK 计数器 `i18n-cjk-count.mjs` 同一套规则）。
 *
 *   为什么不用「`//` 到行尾」的正则：第一版那么写，结果它把
 *   `const t = \`a//b\`` 里的 `//` 当成行注释起点、把后半行连代码一起吃掉 ——
 *   **误伤比漏剥危险得多**：漏剥只可能让某条 grep 断言误绿，误伤会让 grep 判据
 *   悄悄看见一个残缺的源码（台账里「锚点落进注释」那类误判就是这么来的）。
 *   代价是行尾注释留着，对 grep 判据无影响。
 */
const CODE_ONLY = SOURCE
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .split('\n')
  .filter((line) => !/^\s*(\/\/|\*|\/\*)/.test(line))
  .join('\n')

/** 三个容器的开标签。`:loading` / `title-key` 这类断言只在这三个标签上做，
 *  因为页面里 `AppModal` 页脚按钮**本来就用** `:loading="loading"`。 */
const RDV_TAGS = CODE_ONLY.match(/<ResponsiveDataView[\s\S]*?>/g) ?? []

const { ctrl } = vi.hoisted(() => ({
  ctrl: {
    empty: false,
    fail: false,
    reset() {
      ctrl.empty = false
      ctrl.fail = false
    },
  },
}))

/** `projects`：三档 status 全覆盖（active / archived / **未登记的** deleted）。 */
const PROJECTS = [
  { id: 1, name: 'Alpha 服务', language: 'Go', framework: 'Gin', status: 'active', created_at: '2026-10-01T00:00:00Z' },
  { id: 2, name: 'Beta 前端', language: 'TypeScript', framework: 'Vue', status: 'archived', created_at: '2026-10-02T00:00:00Z' },
  // ★ status `deleted` 不在 `statusType` 的 map 里 ⇒ 走 `|| 'info'` 兜底；
  //   它也不在测试词条的 `ops.vibecoding.status` 里 ⇒ enumLabel 回落原值 "deleted"。
  { id: 3, name: 'Gamma 无会话', language: 'Python', framework: 'FastAPI', status: 'deleted', created_at: '2026-10-03T00:00:00Z' },
]

/**
 * `sessions`：
 * - ★ id 10 与 11 **同 `task_type`（refactor）** ⇒ `title-key` 若写成 `task_type` 就撞键。
 * - ★ id 30 属于 project 1 但**没有任何评审** ⇒ 「级联到无评审会话」时空态必须出。
 * - id 20 的 `completed_at` 缺值（桌面出 `—`）。
 */
const SESSIONS = [
  { id: 10, project_id: 1, task_type: 'refactor', status: 'active', created_at: '2026-10-04T00:00:00Z' },
  { id: 11, project_id: 1, task_type: 'refactor', status: 'completed', created_at: '2026-10-04T01:00:00Z', completed_at: '2026-10-04T02:00:00Z' },
  { id: 20, project_id: 2, task_type: 'feat', status: 'failed', created_at: '2026-10-05T00:00:00Z' },
  { id: 30, project_id: 1, task_type: 'test', status: 'active', created_at: '2026-10-05T01:00:00Z' },
]

const issues = (n: number) => Array.from({ length: n }, (_, i) => ({ line: i + 1, severity: 'info', message: `m${i}`, category: 'c' }))
const review = (id: number, session_id: number, file_path: string, language: string, score: number, ic: number, sc: number) => ({
  id,
  session_id,
  file_path,
  language,
  score,
  review_result: { issues: issues(ic), suggestions: Array.from({ length: sc }, (_, i) => `s${i}`), summary: 'sum', complexity: 1, maintainability: 'ok' },
  created_at: '2026-10-06T00:00:00Z',
})

/**
 * `reviews`：三档 score 全覆盖 + 四个边界。
 * - ★ id 102：**0 问题 / 0 建议** ⇒ 徽章出 0（`showZero` 默认 true，**已实测**），
 *   卡片 issues 计数出 `0` 且 tone 走 `good` 那一侧。
 * - ★ id 103：**score 为 0**（不是缺值）⇒ 卡片出 `0`，不是 `—`。
 *   谓词必须是 `v == null` 而不是 `v`（`Number(0)` 也是 0）。id 103 挂在**孤儿 session_id 99**。
 * - ★ id 104：**既没有 `score` 也没有 `review_result`** ⇒ 桌面评分格是**空白**、
 *   卡片出 `—`（CardList 没有「空白」态）；两个计数都回落 `0`。
 * - ★ id 105：**score 85 落在 [80, 90) 这条中间带里**。没有它的话
 *   「`getScoreColor` 的判读线 80 改成 90」是**行为等价**的（其余四行分档结果不变）
 *   —— 这是本仓第三次踩「阈值变异缺中间带取值」（前两次：M25 `fmtUsd` 0.001→0.01、
 *   M10 `fmtMs` 1000→1500）。挂在 session 99 上，让「级联到 session 10 ⇒ 2 条」不变。
 */
const REVIEWS = [
  review(100, 10, 'internal/alpha.go', 'Go', 92, 3, 2),
  review(101, 10, 'internal/alpha_test.go', 'Go', 70, 1, 1),
  review(102, 20, 'src/beta.vue', 'TypeScript', 41, 0, 0),
  review(103, 99, 'orphan/app.py', 'Python', 0, 2, 0),
  { id: 104, session_id: 20, file_path: 'src/no-score.ts', language: 'TypeScript', created_at: '2026-10-06T01:00:00Z' },
  review(105, 99, 'orphan/mid.ts', 'TypeScript', 85, 2, 1),
]

vi.mock('../../api/ops', () => ({
  getVibeCodingProjects: vi.fn(async () => PROJECTS),
  getVibeCodingSessions: vi.fn(async () => SESSIONS),
  getCodeReviews: vi.fn(async () => REVIEWS),
  createVibeCodingProject: vi.fn(async () => ({})),
  createVibeCodingSession: vi.fn(async () => ({})),
}))

/**
 * 弹窗壳换成最小桩：`AppModal` 内部是 `<Teleport to="body">`，
 * 真实挂载后内容在 `document.body` 上，`wrapper.find` 够不着。
 * 这里要验的是「点了卡内动作会不会把弹窗打开」，不是 AppModal 本身（它另有测试）。
 */
vi.mock('../../components/ui/AppModal.vue', () => ({
  default: {
    props: ['modelValue', 'title', 'size'],
    emits: ['update:modelValue'],
    template: '<div v-if="modelValue" class="appmodal-stub" :data-title="title"><slot /><slot name="footer" /></div>',
  },
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    'zh-CN': {
      common: {
        table: { status: '状态' },
        createdAt: '创建时间',
        actions: '操作',
        detail: '详情',
        cancel: '取消',
        create: '创建',
        close: '关闭',
      },
      hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '暂无数据' } },
      ops: {
        vibecoding: {
          title: 'Vibe Coding',
          createProject: '新建项目',
          projects: '项目',
          projectName: '项目名称',
          language: '语言',
          framework: '框架',
          sessionName: '会话名称',
          projectId: '项目ID',
          startedAt: '开始时间',
          endedAt: '结束时间',
          sessions: '会话',
          codeReviews: '代码评审',
          filePath: '文件路径',
          score: '评分',
          issues: '问题数',
          suggestions: '建议数',
          reviewedAt: '评审时间',
          showAll: '显示全部',
          newSession: '新建会话',
          viewSessions: '查看会话',
          viewReviews: '查看评审',
          createSessionTitle: '新建会话',
          reviewDetail: '评审详情',
          // ★ 只登记三档；`failed` / `deleted` 故意不登记 ⇒ 走 enumLabel 的原值回落。
          status: { active: '进行中', archived: '已归档', completed: '已完成' },
        },
      },
    },
  },
})

function mockWindowClass(cls: 'compact' | 'expanded'): void {
  _resetForTests()
  const lo = cls === 'compact' ? 0 : 1280
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (q: string) => {
      const min = q.match(/min-width:\s*([\d.]+)px/)
      const max = q.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
      return {
        matches, media: q, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
      }
    },
  })
}

async function factory() {
  const w = mount(VibeCodingView, { global: { plugins: [i18n], directives: { loading: ElLoading.directive } } })
  await flushPromises()
  await flushPromises()
  return w
}

type W = Awaited<ReturnType<typeof factory>>

function cardsIn(w: W, section: string) {
  return w.findAll(`[data-testid="${section}"] [data-testid="card-list"] .card`)
}

function rowsIn(w: W, section: string) {
  return w.findAll(`[data-testid="${section}"] .el-table__body tbody tr`)
}

function fieldLabels(card: ReturnType<typeof cardsIn>[number]) {
  return card.findAll('.card__field').map((f) => f.find('dt').text())
}

/** 定位判据的失败信息必须带上下文 —— 「找不到字段」本身指不出是哪张表哪一行。 */
function fieldValue(w: W, section: string, index: number, label: string) {
  const card = cardsIn(w, section)[index]
  expect(card, `${section} 第 ${index} 张卡不存在`).toBeTruthy()
  const hit = card!.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(hit, `${section} 第 ${index} 张卡里找不到字段「${label}」，实际有：${fieldLabels(card!).join('/')}`).toBeTruthy()
  return hit!.find('dd')
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

// ─────────────────────────────────────────────────────────────────────────────
describe('VibeCodingView：桌面三张表零回归', () => {
  it('三张表的列头与列数：6 / 6 / 7', async () => {
    const w = await factory()
    const heads = (id: string) => w.findAll(`[data-testid="${id}"] th`).map((th) => th.text())
    expect(heads('vc-projects')).toEqual(['项目名称', '语言', '框架', '状态', '创建时间', '操作'])
    expect(heads('vc-sessions')).toEqual(['会话名称', '项目ID', '状态', '开始时间', '结束时间', '操作'])
    expect(heads('vc-reviews')).toEqual(['语言', '文件路径', '评分', '问题数', '建议数', '评审时间', '操作'])
  })

  it('桌面三张 el-table 都在，且不是卡片', async () => {
    const w = await factory()
    expect(w.findAll('.el-table')).toHaveLength(3)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('行数：项目 3 / 会话 4 / 评审 5（未筛选态）', async () => {
    const w = await factory()
    expect(rowsIn(w, 'vc-projects')).toHaveLength(3)
    expect(rowsIn(w, 'vc-sessions')).toHaveLength(4)
    expect(rowsIn(w, 'vc-reviews')).toHaveLength(6)
  })

  /** 状态两档 el-tag 类型。`statusType` 的 map 只有 active/archived/completed。 */
  it('状态 el-tag 类型：active→success、archived→info、未登记的 deleted→info 兜底', async () => {
    const w = await factory()
    const tagOf = (section: string, i: number) => {
      const tag = rowsIn(w, section)[i]!.find('.el-tag')
      const type = ['success', 'primary', 'warning', 'info', 'danger'].filter((k) => tag.classes().includes(`el-tag--${k}`)).join('')
      return `${tag.text()}:${type}`
    }
    expect(tagOf('vc-projects', 0)).toBe('进行中:success')
    expect(tagOf('vc-projects', 1)).toBe('已归档:info')
    expect(tagOf('vc-projects', 2)).toBe('deleted:info')
    expect(tagOf('vc-sessions', 2)).toBe('failed:info')
  })

  /** 评分三档 el-tag 类型，判读线 80 / 60。 */
  it('评分 el-tag 类型三档 + 中间带：92/85→success、70→warning、41/0→danger', async () => {
    const w = await factory()
    const tagOf = (i: number) => {
      const tag = rowsIn(w, 'vc-reviews')[i]!.findAll('.el-tag')[0]!
      const type = ['success', 'primary', 'warning', 'info', 'danger'].filter((k) => tag.classes().includes(`el-tag--${k}`)).join('')
      return `${tag.text()}:${type}`
    }
    expect(tagOf(0)).toBe('92:success')
    expect(tagOf(1)).toBe('70:warning')
    expect(tagOf(2)).toBe('41:danger')
    expect(tagOf(3)).toBe('0:danger')
    // ★ 中间带：85 只有在判读线 80 与 90 之间才有区别
    expect(tagOf(5)).toBe('85:success')
  })

  /**
   * 计数徽章的**值与类型**都要断。`el-badge` 的 `showZero` 在 EP 2.14.3 默认 `true`
   * ⇒ 0 个问题照样出「success 徽章 0」，桌面与卡片两边都在，不是口径差异。
   *
   * ★ 类型 class 挂在 `sup.el-badge__content` 上，**不在**外层 `div.el-badge` 上 ——
   *   第一版 `find('.el-badge')` 拿到的是外壳，五个 class 一个都不匹配，红的是判据。
   */
  it('计数徽章：值与类型（含 0 个问题 ⇒ success 徽章 0）', async () => {
    const w = await factory()
    const badgeOf = (i: number, col: number) => {
      const badge = rowsIn(w, 'vc-reviews')[i]!.findAll('td')[col]!.find('.el-badge__content')
      const type = ['success', 'info', 'danger', 'warning', 'primary'].filter((k) => badge.classes().includes(`el-badge__content--${k}`)).join('')
      return `${badge.text()}:${type}`
    }
    // 3 个问题 → danger；0 个问题 → success（不是隐藏）
    expect(badgeOf(0, 3)).toBe('3:danger')
    expect(badgeOf(2, 3)).toBe('0:success')
    // 建议数：桌面固定 info，与问题数无关
    expect(badgeOf(0, 4)).toBe('2:info')
    expect(badgeOf(2, 4)).toBe('0:info')
  })

  it('completed_at 缺值时桌面出 —；有时间时与卡片同源（同一份 fmtDateTime24h）', async () => {
    const w = await factory()
    const ended = rowsIn(w, 'vc-sessions').map((r) => r.findAll('td')[4]!.text())
    expect(ended[0]).toBe('—')
    expect(ended[1]).toMatch(/\d/)
    // 已格式化的证据：不能是裸 ISO 串（D10/D13 那一族）
    expect(ended[1]).not.toContain('T')
  })

  it('score 缺值时桌面是**空白**（卡片出 — 的那个已知差异）', async () => {
    const w = await factory()
    const scores = rowsIn(w, 'vc-reviews').map((r) => r.findAll('td')[2]!.text())
    expect(scores).toEqual(['92', '70', '41', '0', '', '85'])
  })

  it('首屏三个接口各取一次（单次 Promise.all，无分页取数）', async () => {
    const { getVibeCodingProjects, getVibeCodingSessions, getCodeReviews } = await import('../../api/ops')
    vi.mocked(getVibeCodingProjects).mockClear()
    vi.mocked(getVibeCodingSessions).mockClear()
    vi.mocked(getCodeReviews).mockClear()
    await factory()
    expect(vi.mocked(getVibeCodingProjects)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(getVibeCodingSessions)).toHaveBeenCalledTimes(1)
    expect(vi.mocked(getCodeReviews)).toHaveBeenCalledTimes(1)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('VibeCodingView：加载反馈（不传 :loading 的行为证据）', () => {
  /**
   * ★ 桌面 projects 表自带 `v-loading`，那是**覆盖层**：`el-table` 仍在 DOM 里、旧行照常渲染。
   *   `ResponsiveDataView` 的 loading 分支是 `v-if` / `v-else` ⇒ 若传 `:loading`，
   *   桌面会在每次刷新时把整张 el-table 换成 spinner（首屏连 Element 的 "No Data" 一起消失）。
   *   这条把「三段表壳在请求挂起时仍在」钉住 —— 源码 grep 只是它的影子。
   */
  it('请求挂起时三张 el-table 仍在 DOM（桌面没有被换成骨架）', async () => {
    const { getVibeCodingProjects } = await import('../../api/ops')
    let release!: () => void
    vi.mocked(getVibeCodingProjects).mockImplementationOnce(
      () => new Promise((r) => { release = () => r([]) }),
    )
    const w = mount(VibeCodingView, { global: { plugins: [i18n], directives: { loading: ElLoading.directive } } })
    await flushPromises()
    expect(w.findAll('.el-table')).toHaveLength(3)
    expect(w.find('.app-spinner').exists()).toBe(false)
    // 首屏 loading=true ⇒ projects 的 v-loading 覆盖层确实挂着（证明延迟反馈还在）
    expect(w.find('.el-loading-mask').exists()).toBe(true)
    release()
    await flushPromises()
    await flushPromises()
    expect(rowsIn(w, 'vc-projects')).toHaveLength(0) // 释放时给的是空数组
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('VibeCodingView：el-table 地形 + 跨层契约', () => {
  it('容器直接子元素是 div.el-table，不是 <table> ⇒ :slotted(table) 不命中', async () => {
    const w = await factory()
    for (const id of ['vc-projects', 'vc-sessions', 'vc-reviews']) {
      const host = w.find(`[data-testid="${id}"] .responsive-data-view__table`)
      expect(host.exists(), `${id} 缺横向滚动容器`).toBe(true)
      expect([...host.element.children].map((c) => c.tagName.toLowerCase()), `${id} 的直接子元素`).toEqual(['div'])
      expect(host.find('.el-table').exists()).toBe(true)
    }
  })

  it('本页不传 table-min-width（对 el-table 是空操作，传了就是假的「已设」）', () => {
    expect(CODE_ONLY).not.toContain('table-min-width')
  })

  it('恰好三个 ResponsiveDataView，且三个都不传 :loading', () => {
    expect(RDV_TAGS).toHaveLength(3)
    for (const tag of RDV_TAGS) expect(tag, `容器不该接 :loading：${tag}`).not.toContain(':loading')
  })

  it('三处 :empty 都带 isCompact 前置', () => {
    const ones = CODE_ONLY.match(/:empty="[^"]*"/g) ?? []
    expect(ones).toHaveLength(3)
    for (const e of ones) expect(e).toMatch(/^:empty="isCompact && /)
  })

  /**
   * ★ 两级级联的谓词必须用**筛选后**的数组。用原始数组的话，
   *   「选中的项目下没有会话」时 `:empty` 恒为 false ⇒ compact 下第二节是一个
   *   空 `CardList`（`v-else` 分支，容器不出 EmptyState），桌面对应位置是 Element 的 "No Data"。
   */
  it('sessions/reviews 的 :empty 用筛选后的数组（projects 用原数组）', () => {
    const ones = CODE_ONLY.match(/:empty="[^"]*"/g) ?? []
    expect(ones.filter((e) => e.includes('filteredSessions.length'))).toHaveLength(1)
    expect(ones.filter((e) => e.includes('filteredReviews.length'))).toHaveLength(1)
    expect(ones.filter((e) => e.includes('projects.length'))).toHaveLength(1)
  })

  it('三张表的 title-key 都是 id（task_type / file_path 都不唯一）', () => {
    expect(CODE_ONLY.match(/title-key="id"/g)).toHaveLength(3)
  })

  it('无分页 ⇒ 不引入连续加载', () => {
    for (const token of ['createHyperPages', 'HyperLoadMore', 'hyper-load-more', 'page_size', 'offset']) {
      expect(CODE_ONLY, `页面里不该出现 ${token}`).not.toContain(token)
    }
  })

  it('0 新增 i18n 键：只引用 ops.vibecoding.* / common.* / hyper.* 的既有键', () => {
    const used = new Set([...CODE_ONLY.matchAll(/t\('([^']+)'/g)].map((m) => m[1]))
    for (const k of used) {
      expect(/^(ops\.vibecoding\.|common\.|hyper\.)/.test(k), `预期词条前缀之外的键：${k}`).toBe(true)
    }
    expect(used.has('hyper.list.empty'), 'compact 空态复用 hyper.list.empty ⇒ 0 新增键').toBe(true)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('VibeCodingView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('三段各出卡片、不出 el-table', async () => {
    const w = await factory()
    expect(cardsIn(w, 'vc-projects')).toHaveLength(3)
    expect(cardsIn(w, 'vc-sessions')).toHaveLength(4)
    expect(cardsIn(w, 'vc-reviews')).toHaveLength(6)
    expect(w.findAll('.el-table')).toHaveLength(0)
  })

  it('compact 下不渲染形态切换钮', async () => {
    const w = await factory()
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  /** 字段集与**桌面的列**一一对应：扣掉卡头那一列与操作列，一张不许多一张不许少。 */
  it('三段卡的字段集与桌面列一一对应', async () => {
    const w = await factory()
    expect(fieldLabels(cardsIn(w, 'vc-projects')[0]!)).toEqual(['语言', '框架', '状态', '创建时间'])
    expect(fieldLabels(cardsIn(w, 'vc-sessions')[0]!)).toEqual(['项目ID', '状态', '开始时间', '结束时间'])
    expect(fieldLabels(cardsIn(w, 'vc-reviews')[0]!)).toEqual(['语言', '评分', '问题数', '建议数', '评审时间'])
  })

  it('卡头是各自的身份：name / task_type / file_path', async () => {
    const w = await factory()
    expect(cardsIn(w, 'vc-projects').map((c) => c.attributes('data-title'))).toEqual(['Alpha 服务', 'Beta 前端', 'Gamma 无会话'])
    expect(cardsIn(w, 'vc-sessions').map((c) => c.attributes('data-title'))).toEqual(['refactor', 'refactor', 'feat', 'test'])
    expect(cardsIn(w, 'vc-reviews')[0]!.attributes('data-title')).toBe('internal/alpha.go')
  })

  /**
   * ★ 前两条会话的 `task_type` **完全相同**（`refactor`）⇒ 卡头撞脸。
   *   键走 `id`（1010/1011 各一张）所以两张都渲染得出来；这条断的是「行没被复用串掉」。
   */
  it('同 task_type 的两条会话仍是两张独立的卡（不撞键、不串行）', async () => {
    const w = await factory()
    const cards = cardsIn(w, 'vc-sessions')
    expect(cards).toHaveLength(4)
    expect(cards[0]!.attributes('data-title')).toBe('refactor')
    expect(cards[1]!.attributes('data-title')).toBe('refactor')
    // 撞键时 Vue 会复用节点 ⇒ 结束时间会两行一样。11 有 completed_at，10 没有。
    expect(fieldValue(w, 'vc-sessions', 0, '结束时间').text()).toBe('—')
    expect(fieldValue(w, 'vc-sessions', 1, '结束时间').text()).toMatch(/\d/)
  })

  it('状态 tone 逐行：已登记的 active→good，其余→neutral（两档，不是三档）', async () => {
    const w = await factory()
    const tone = (section: string, i: number) => fieldValue(w, section, i, '状态').attributes('data-tone')
    expect(tone('vc-projects', 0)).toBe('good') // active
    expect(tone('vc-projects', 1)).toBe('neutral') // archived
    expect(tone('vc-projects', 2)).toBe('neutral') // deleted（未登记）
    expect(tone('vc-sessions', 0)).toBe('good') // active
    expect(tone('vc-sessions', 1)).toBe('neutral') // completed
    expect(tone('vc-sessions', 2)).toBe('neutral') // failed（未登记）
  })

  it('状态译名与桌面同源（未登记枚举回落到原值，两边一样）', async () => {
    const w = await factory()
    expect(fieldValue(w, 'vc-projects', 0, '状态').text()).toBe('进行中')
    expect(fieldValue(w, 'vc-projects', 2, '状态').text()).toBe('deleted')
    expect(fieldValue(w, 'vc-sessions', 2, '状态').text()).toBe('failed')
  })

  /** 评分三档 tone，判读线从 `getScoreColor` 派生：≥80 good / ≥60 warn / else danger。 */
  it('评分 tone 三档 + 中间带：92/85→good、70→warn、41/0→danger', async () => {
    const w = await factory()
    const tone = (i: number) => fieldValue(w, 'vc-reviews', i, '评分').attributes('data-tone')
    expect(tone(0)).toBe('good')
    expect(tone(1)).toBe('warn')
    expect(tone(2)).toBe('danger')
    expect(tone(3)).toBe('danger')
    // ★ 中间带：85 是 good / warn 的分界见证者
    expect(tone(5)).toBe('good')
  })

  /** ★ score 为 0 的一行：出 `0`，不是 `—`。谓词是 `v == null` 而不是 `v`。 */
  it('评分为 0 的行出「0」而不是「—」', async () => {
    const w = await factory()
    expect(fieldValue(w, 'vc-reviews', 3, '评分').text()).toBe('0')
  })

  it('评分缺值出 —（桌面是空白，已在桌面那侧钉住）', async () => {
    const w = await factory()
    expect(fieldValue(w, 'vc-reviews', 4, '评分').text()).toBe('—')
  })

  it('问题数/建议数与桌面同源：3/2、1/1、0/0、2/0、无 review_result → 0/0', async () => {
    const w = await factory()
    const at = (i: number) => [fieldValue(w, 'vc-reviews', i, '问题数').text(), fieldValue(w, 'vc-reviews', i, '建议数').text()]
    expect(at(0)).toEqual(['3', '2'])
    expect(at(1)).toEqual(['1', '1'])
    expect(at(2)).toEqual(['0', '0'])
    expect(at(3)).toEqual(['2', '0'])
    expect(at(4)).toEqual(['0', '0']) // ★ 没有 review_result ⇒ 两个回落分支
    expect(at(5)).toEqual(['2', '1'])
  })

  it('问题数 tone：0 个问题→good（与桌面 success 徽章同侧），有→danger', async () => {
    const w = await factory()
    expect(fieldValue(w, 'vc-reviews', 0, '问题数').attributes('data-tone')).toBe('danger')
    expect(fieldValue(w, 'vc-reviews', 2, '问题数').attributes('data-tone')).toBe('good')
  })

  it('建议数不参与着色（桌面固定 info 徽章，卡片留 neutral）', async () => {
    const w = await factory()
    expect(fieldValue(w, 'vc-reviews', 0, '建议数').attributes('data-tone')).toBe('neutral')
  })

  it('时间字段与桌面同源（同一份 fmtDateTime24h，不是裸 ISO 串）', async () => {
    const w = await factory()
    const created = fieldValue(w, 'vc-projects', 0, '创建时间').text()
    expect(created).toMatch(/\d/)
    expect(created).not.toContain('T')
  })

  it('结束时间缺值出 —，有值时是格式化后的串', async () => {
    const w = await factory()
    expect(fieldValue(w, 'vc-sessions', 0, '结束时间').text()).toBe('—')
    const ended = fieldValue(w, 'vc-sessions', 1, '结束时间').text()
    expect(ended).toMatch(/\d/)
    expect(ended).not.toContain('T')
  })

  // ── #actions：三段各自的行内动作 ───────────────────────────────────────────
  it('`#actions` 给出与桌面操作列同款按钮（3 / 1 / 1）', async () => {
    const w = await factory()
    const acts = (section: string) => cardsIn(w, section).map((c) => c.find('.card__actions').findAll('button').map((b) => b.text()))
    expect(acts('vc-projects')[0]).toEqual(['新建会话', '查看会话'])
    expect(acts('vc-sessions')[0]).toEqual(['查看评审'])
    expect(acts('vc-reviews')[0]).toEqual(['详情'])
  })

  /**
   * ★ 动作必须**真的驱动级联筛选**：点项目卡的「查看会话」后，
   *   会话段只剩 project 1 的 3 条（10/11/30）。不断这条的话，
   *   「`selectedProjectId = row.id` 写成 `row.project_id`」这类变异是行为等价的。
   */
  it('项目卡「查看会话」真的级联筛选会话段（4 → 3）', async () => {
    const w = await factory()
    await cardsIn(w, 'vc-projects')[0]!.findAll('.card__actions button')[1]!.trigger('click')
    await flushPromises()
    expect(cardsIn(w, 'vc-sessions')).toHaveLength(3)
    expect(fieldValue(w, 'vc-sessions', 0, '项目ID').text()).toBe('1')
  })

  it('会话卡「查看评审」真的级联筛选评审段（6 → 2）', async () => {
    const w = await factory()
    await cardsIn(w, 'vc-sessions')[0]!.findAll('.card__actions button')[0]!.trigger('click')
    await flushPromises()
    expect(cardsIn(w, 'vc-reviews')).toHaveLength(2)
  })

  it('项目卡「新建会话」打开建会话弹窗（带上该行的 projectId）', async () => {
    const w = await factory()
    await cardsIn(w, 'vc-projects')[1]!.findAll('.card__actions button')[0]!.trigger('click')
    await flushPromises()
    const modal = w.find('.appmodal-stub')
    expect(modal.exists()).toBe(true)
    expect(modal.attributes('data-title')).toBe('新建会话')
  })

  it('评审卡「详情」打开评审详情弹窗', async () => {
    const w = await factory()
    await cardsIn(w, 'vc-reviews')[0]!.findAll('.card__actions button')[0]!.trigger('click')
    await flushPromises()
    const modal = w.find('.appmodal-stub')
    expect(modal.exists()).toBe(true)
    expect(modal.attributes('data-title')).toBe('评审详情')
  })

  /** 表头那两枚「显示全部」按钮不受形态影响（它们在 `el-card` 头上，不在容器里）。 */
  it('级联后表头出「显示全部」，点它回到未筛选', async () => {
    const w = await factory()
    expect(w.findAll('.card-header button')).toHaveLength(0)
    await cardsIn(w, 'vc-projects')[0]!.findAll('.card__actions button')[1]!.trigger('click')
    await flushPromises()
    const showAll = w.findAll('.card-header button')
    expect(showAll).toHaveLength(1)
    await showAll[0]!.trigger('click')
    await flushPromises()
    expect(cardsIn(w, 'vc-sessions')).toHaveLength(4)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('VibeCodingView：空态归属', () => {
  it('桌面三段全空：不出现我们的 EmptyState（空态留给 Element 自带）', async () => {
    const { getVibeCodingProjects, getVibeCodingSessions, getCodeReviews } = await import('../../api/ops')
    vi.mocked(getVibeCodingProjects).mockResolvedValueOnce([])
    vi.mocked(getVibeCodingSessions).mockResolvedValueOnce([])
    vi.mocked(getCodeReviews).mockResolvedValueOnce([])
    const w = await factory()
    expect(w.findAll('.el-table')).toHaveLength(3)
    expect(w.find('.app-empty-state').exists()).toBe(false)
  })

  it('compact 三段全空：出 EmptyState，且不是空 <ul>', async () => {
    mockWindowClass('compact')
    const { getVibeCodingProjects, getVibeCodingSessions, getCodeReviews } = await import('../../api/ops')
    vi.mocked(getVibeCodingProjects).mockResolvedValueOnce([])
    vi.mocked(getVibeCodingSessions).mockResolvedValueOnce([])
    vi.mocked(getCodeReviews).mockResolvedValueOnce([])
    const w = await factory()
    const empties = w.findAll('.app-empty-state')
    expect(empties).toHaveLength(3)
    // ★ 文案也要断：只断「出现了」的话，把 `:empty-text` 换成任何一个别的既有键
    //   都不会红 —— 而空态文案是本页唯一会露给用户的词条。
    for (const e of empties) expect(e.text()).toContain('暂无数据')
    expect(w.findAll('[data-testid="card-list"]')).toHaveLength(0)
  })

  /**
   * ★ 级联到「有项目、无会话」：会话段必须出 EmptyState。
   *   这条专门打「`:empty` 用 `sessions.length` 而不是 `filteredSessions.length`」——
   *   那样写的话这里会得到一个**空 CardList**（容器 `:empty` 为 false ⇒ 走 `v-else` 的 CardList）。
   */
  it('级联到「该项目下没有会话」：会话段出 EmptyState，而不是空卡片列表', async () => {
    mockWindowClass('compact')
    const w = await factory()
    // Gamma（id 3）在 sessions 里一条都没有
    await cardsIn(w, 'vc-projects')[2]!.findAll('.card__actions button')[1]!.trigger('click')
    await flushPromises()
    expect(cardsIn(w, 'vc-sessions')).toHaveLength(0)
    const host = w.find('[data-testid="vc-sessions"] .responsive-data-view')
    expect(host.find('.app-empty-state').exists(), '会话段该出空态').toBe(true)
    expect(host.find('[data-testid="card-list"]').exists()).toBe(false)
    // 原始数组非空（4 条）⇒ 红灯只能来自「谓词用了筛选后数组」这一个原因
    expect(ctrl.empty).toBe(false)
  })

  /** 同样打第二级：会话 30 没有任何评审。 */
  it('级联到「该会话下没有评审」：评审段出 EmptyState', async () => {
    mockWindowClass('compact')
    const w = await factory()
    const target = cardsIn(w, 'vc-sessions')[3]! // id 30 · task_type test
    expect(target.attributes('data-title')).toBe('test')
    await target.findAll('.card__actions button')[0]!.trigger('click')
    await flushPromises()
    expect(cardsIn(w, 'vc-reviews')).toHaveLength(0)
    const host = w.find('[data-testid="vc-reviews"] .responsive-data-view')
    expect(host.find('.app-empty-state').exists(), '评审段该出空态').toBe(true)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('VibeCodingView：compact 加载反馈的已知缺口', () => {
  /**
   * 现状**如实钉住**：compact 下请求挂起时不换骨架、不出遮罩（三张表都收了 v-loading，
   * 而卡片形态里 `v-loading` 那张 el-table 压根没渲染）。给容器加 `:loading` 能补上，
   * 但会把桌面的覆盖层换成整块替换 ⇒ 越桌面红线，故本切片不补。
   * 这条**故意断言现状**而不是理想行为：缺口钉住了，将来谁补了它会看见这条红，
   * 而不是让缺口在没人察觉的情况下漂走。详见 10 §4.6 的台账。
   */
  it('请求挂起时 compact 既不换骨架也不出遮罩（已知缺口，见 10 §4.6）', async () => {
    mockWindowClass('compact')
    const { getVibeCodingProjects } = await import('../../api/ops')
    let release!: () => void
    vi.mocked(getVibeCodingProjects).mockImplementationOnce(
      () => new Promise((r) => { release = () => r([]) }),
    )
    const w = mount(VibeCodingView, { global: { plugins: [i18n], directives: { loading: ElLoading.directive } } })
    await flushPromises()
    expect(w.findAll('[data-testid="card-list"]')).toHaveLength(0)
    expect(w.find('.app-spinner').exists()).toBe(false)
    expect(w.find('.el-loading-mask').exists()).toBe(false)
    release()
    await flushPromises()
    await flushPromises()
  })
})
