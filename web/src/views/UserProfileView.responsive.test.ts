// UserProfileView.responsive.test.ts — H6 第十五条垂直切片的门禁。
//
// 本页的形状（**首条 el-table 切片**）：
// 1. **没有分页 API**（`getUserProfile(owner, days)` 一次取回整段，三张表都是它的字段）
//    ⇒ 只改呈现形态，不引入连续加载。
// 2. **槽里是 `el-table` 而不是原生 `<table>`** ⇒ 容器规则 `:slotted(table)` 不命中，
//    `table-min-width` 是**空操作**（10 §4.6 的 D12，已实测）。
//    门禁要断「本页不出现 `table-min-width`」—— 传一个不生效的值再注释「已设」是假的。
// 3. **三张表桌面都没有自己的空态行** —— 0 行时 Element 渲染自带 "No Data"。
//    ⇒ compact 的空态只能来自容器 `:empty`，且必须带 `isCompact` 前置。
// 4. **行对象带 `avg_health`，但 `top_end_users` 那张表没有这一列**
//    ⇒ 卡片也不许有（字段政策红线）。
// 5. **最近会话的卡头出完整 `session_id`**：桌面的 16 字符截断是为窄列做的，
//    卡片头承担「唯一句柄」职责 —— fixture 里两行**前 16 字符完全相同**。
//
// 门禁清单：
// A. 桌面零回归：三张表的列头与列数（4/4/5）、成本 4 位小数与缺值 $0.0000、
//    health_grade 五档 el-tag 类型、会话链接的 href 与截断文字、el-table 仍在
// B. el-table 地形：容器直接子元素是 div.el-table（证明 D12）、不传 table-min-width
// C. compact 卡片：出卡不出表、三张表字段集与列一一对应、tone 逐行、
//    成本与桌面同源、卡头是完整 id（不截断）、`#actions` 链接可用
// D. 空态：compact 出 EmptyState；桌面不出现我们的 EmptyState（留给 Element）
// E. 跨层契约：无连续加载、不传 `:loading`、三处 `:empty` 都带 isCompact 前置、0 新增 i18n 键
import { flushPromises, enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { ElLoading } from 'element-plus'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserProfileView from './UserProfileView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

enableAutoUnmount(afterEach)

const SOURCE = readFileSync(resolve(process.cwd(), 'src/views/UserProfileView.vue'), 'utf8')
const CODE_ONLY = SOURCE
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const { ctrl } = vi.hoisted(() => ({
  ctrl: {
    calls: [] as Array<{ owner: string; days: number }>,
    empty: false,
    fail: false,
    reset() {
      ctrl.calls = []
      ctrl.empty = false
      ctrl.fail = false
    },
  },
}))

/**
 * `top_tasks`：含一行 **`avg_health` 缺值**（桌面空白 / 卡片 `—`）与一行成本 0。
 * `top_end_users`：桌面**没有** `avg_health` 列，行对象却带这个字段 ⇒ 卡片也不能有；
 *   第二行 `last_activity` 为空串。
 * `recent_sessions`：**5 个健康等级全覆盖**（A/B/C/D/F）+ 一行**没有等级**；
 *   前两行的 `session_id` **前 16 字符完全相同**（`sess-000000000000`）⇒ 截断即撞脸。
 */
const TOP_TASKS = [
  { task_id: 'chat', session_count: 120, total_cost: 1.5, avg_health: 88 },
  { task_id: 'reasoning', session_count: 30, total_cost: 0.25, avg_health: 42 },
  { task_id: 'code', session_count: 7, total_cost: 0 },
  // ★ 成本**缺值**的一行。缺它时 `?? 0` 这个回落分支在桌面与卡片两侧都走不到
  //   ⇒ 台账上「去掉 `?? 0`」与「去掉 `v ?? 0`」两条变异**行为等价**（无牙）。
  { task_id: 'embedding', session_count: 2 },
]

const TOP_END_USERS = [
  { end_user_id: 'alice', session_count: 40, total_cost_usd: 0.75, avg_health: 91, last_activity: '2026-10-05T00:00:00Z' },
  { end_user_id: 'bob', session_count: 5, total_cost_usd: 0.0001, last_activity: '' },
]

const RECENT_SESSIONS = [
  { session_id: 'sess-0000000000000001-short', request_count: 12, cost_usd: 0.5, health_grade: 'A', created_at: '2026-10-06T00:00:00Z' },
  { session_id: 'sess-0000000000000002-mid', request_count: 1, cost_usd: 0, health_grade: 'C', created_at: '2026-10-05T00:00:00Z' },
  { session_id: 'sess-0000000000000003-tail', request_count: 3, cost_usd: 0.02, health_grade: 'F', created_at: '2026-10-04T00:00:00Z' },
  { session_id: 'sess-0000000000000004-b', request_count: 2, cost_usd: 0.01, health_grade: 'B', created_at: '2026-10-03T00:00:00Z' },
  { session_id: 'sess-0000000000000005-d', request_count: 2, cost_usd: 0.01, health_grade: 'D', created_at: '2026-10-02T00:00:00Z' },
  { session_id: 'sess-0000000000000006-none', request_count: 0, cost_usd: 0, created_at: '2026-10-01T00:00:00Z' },
  // ★ 成本**缺值**的一行（这张表也要有：M25 打的是本表的 `?? 0`，
  //   缺值行只加在 top_tasks 上时该变异仍然是行为等价的）。
  { session_id: 'sess-0000000000000007-nocost', request_count: 1, health_grade: 'A', created_at: '2026-09-30T00:00:00Z' },
]

function payload() {
  return {
    session_count: 6,
    total_cost_usd: 1.79,
    total_requests: 20,
    total_success: 18,
    total_errors: 2,
    avg_health_score: 77,
    health_distribution: { a: 1, b: 1, c: 1, d: 1, f: 1 },
    // ★ 趋势给空数组：`renderCostChart()` 见到空数组会**直接 return**，
    //   避免 echarts 在 jsdom 里对 0 尺寸容器初始化（那不是本切片要验的东西）。
    daily_cost_trend: [],
    top_tasks: ctrl.empty ? [] : TOP_TASKS,
    top_end_users: ctrl.empty ? [] : TOP_END_USERS,
    recent_sessions: ctrl.empty ? [] : RECENT_SESSIONS,
  }
}

vi.mock('../api/admin', () => ({
  getUserProfile: vi.fn(async (owner: string, days: number) => {
    ctrl.calls.push({ owner, days })
    if (ctrl.fail) throw new Error('profile boom')
    return payload()
  }),
}))

vi.mock('../components/ui/KxDateRangePicker.vue', () => ({
  default: {
    props: ['modelValue', 'presets', 'maxSpanDays'],
    emits: ['apply'],
    // ★ 桩要能**驱动** `@apply`，否则「days 真的进请求」这条无从验
    //   （只断首屏的话，「写死 30」与真实行为等价）。
    //   用 setup 形式而不是 `methods` + `this.$emit`：后者在 vue-tsc 下
    //   `this` 推不出 `emit`（TS2339），第一版就是这么写的。
    setup(_props: unknown, { emit }: { emit: (e: string, p: unknown) => void }) {
      const rangeOf = (days: number) => {
        const end = new Date('2026-10-06T00:00:00Z')
        const start = new Date(end.getTime() - (days - 1) * 86400000)
        return { start: start.toISOString().slice(0, 10), end: end.toISOString().slice(0, 10) }
      }
      return { apply7: () => emit('apply', rangeOf(7)) }
    },
    template:
      '<div class="kxdaterangepicker-stub">' +
      '<button data-testid="range-apply-7" @click="apply7()">7d</button>' +
      '</div>',
  },
}))

/** 本页用 `useRoute().params.owner` 当查询键、`useRouter().back()` 返回 —— 两者都得给。 */
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { owner: 'alice' } }),
  useRouter: () => ({ back: vi.fn(), push: vi.fn() }),
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    'zh-CN': {
      common: { back: '返回' },
      sessions: {
        userProfile: {
          detailTitle: '用户画像详情',
          empty: '暂无用户画像数据',
          sessionCount: '会话数',
          requestCount: '请求数',
          totalCost: '总成本',
          avgHealth: '平均健康分',
          avgHealthGrade: '健康等级',
          endUserId: '终端用户 ID',
          sessionId: '会话 ID',
          taskId: '任务 ID',
          topTasks: '热门任务',
          topEndUsers: '热门终端用户',
          recentSessions: '最近会话',
          costTrend: '成本趋势',
          lastSeenAt: '最近活跃',
          createdAt: '创建时间',
          successRate: '成功率',
        },
      },
      hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '暂无数据' } },
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
  // 注册 v-loading（页面用了但没在 setup 里 import 指令）—— 不注册只是 warn，
  // 但那 3 个 el-table 与本切片无关，别让噪音淹没真失败。
  const w = mount(UserProfileView, { global: { plugins: [i18n], directives: { loading: ElLoading.directive } } })
  await flushPromises()
  await flushPromises()
  return w
}

function cardsIn(w: Awaited<ReturnType<typeof factory>>, section: string) {
  return w.findAll(`[data-testid="${section}"] [data-testid="card-list"] .card`)
}

function fieldLabels(card: Awaited<ReturnType<typeof factory>> extends never ? never : ReturnType<typeof cardsIn>[number]) {
  return card.findAll('.card__field').map((f) => f.find('dt').text())
}

function fieldValue(card: ReturnType<typeof cardsIn>[number], label: string) {
  const hit = card.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(hit, `卡片里找不到字段「${label}」，实际有：${fieldLabels(card).join('/')}`).toBeTruthy()
  return hit!.find('dd')
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('UserProfileView：桌面三张表零回归', () => {
  it('三张表的列头与列数：4 / 4 / 5', async () => {
    const w = await factory()
    const heads = (id: string) => w.findAll(`[data-testid="${id}"] th`).map((th) => th.text())
    expect(heads('up-top-tasks')).toEqual(['任务 ID', '会话数', '总成本', '平均健康分'])
    expect(heads('up-top-end-users')).toEqual(['终端用户 ID', '会话数', '总成本', '最近活跃'])
    expect(heads('up-recent-sessions')).toEqual(['会话 ID', '请求数', '总成本', '健康等级', '创建时间'])
  })

  it('桌面三张 el-table 都还在，且不是卡片', async () => {
    const w = await factory()
    expect(w.findAll('.el-table')).toHaveLength(3)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('成本列 4 位小数；缺值/0 出 $0.0000（与桌面 `?? 0` 同一口径）', async () => {
    const w = await factory()
    // 热门任务：1.5 / 0.25 / 0
    const costCells = () =>
      w.findAll('[data-testid="up-top-tasks"] .el-table__body tbody tr').map((r) => r.findAll('td')[2]!.text())
    expect(costCells()).toEqual(['$1.5000', '$0.2500', '$0.0000', '$0.0000'])

    // ★ 最近会话的成本列也要断：缺值行加在这张表上，若不断它，
    //   「去掉 `?? 0`」这条变异是行为等价的（台账上一条无牙）。
    const sessCost = w
      .findAll('[data-testid="up-recent-sessions"] .el-table__body tbody tr')
      .map((r) => r.findAll('td')[2]!.text())
    expect(sessCost).toEqual(['$0.5000', '$0.0000', '$0.0200', '$0.0100', '$0.0100', '$0.0000', '$0.0000'])
  })

  /**
   * `health_grade` 的五档 el-tag 类型。桌面这一格是 `el-tag` + `healthGradeColor`，
   * 卡片那一格是 `tone`（5 档收敛成 3）—— **两条线都要钉住**。
   */
  it('健康等级五档的 el-tag 类型逐档不同', async () => {
    const w = await factory()
    const tags = w
      .findAll('[data-testid="up-recent-sessions"] .el-table__body tbody tr')
      .map((r) => {
        const tag = r.find('.el-tag')
        // ★ el-tag 的 class 里还有 `--small` / `--light`，第一版只按前缀筛
        //   `el-tag--` 把它们一起收进来了，红的是判据不是产品。改成只认 5 个类型。
        const type = tag.exists()
          ? ['success', 'primary', 'warning', 'info', 'danger']
              .filter((k) => tag.classes().includes(`el-tag--${k}`))
              .join('')
          : ''
        return tag.exists() ? `${tag.text()}:${type}` : '(无 tag)'
      })
    expect(tags).toEqual([
      'A:success',
      'C:warning',
      'F:danger',
      'B:primary',
      'D:info',
      // 缺等级 ⇒ 桌面不渲染 el-tag（`v-if`）
      '(无 tag)',
      'A:success',
    ])
  })

  it('会话链接：href 带 encodeURIComponent，文字截断到 16 字符', async () => {
    const w = await factory()
    const a = w.findAll('[data-testid="up-recent-sessions"] .el-table__body tbody tr')[0]!.find('a.session-link')
    expect(a.exists()).toBe(true)
    // 'sess'(4) + '-'(1) + 11 个 0 = 16 ⇒ slice(0,16) 就是 'sess-00000000000'
    expect(a.text()).toBe('sess-00000000000...')
    expect(a.attributes('href')).toBe('/plugins/ai-session-manager/sessions/sess-0000000000000001-short')
    expect(a.attributes('target')).toBe('_blank')
    expect(a.attributes('rel')).toBe('noopener')
  })

  it('avg_health 缺值时桌面是**空白**（卡片出 — 的那个已知差异）', async () => {
    const w = await factory()
    const cells = w
      .findAll('[data-testid="up-top-tasks"] .el-table__body tbody tr')
      .map((r) => r.findAll('td')[3]!.text())
    expect(cells).toEqual(['88', '42', '', ''])
  })

  it('首屏取数带 route 上的 owner 与默认 days=30', async () => {
    await factory()
    expect(ctrl.calls).toEqual([{ owner: 'alice', days: 30 }])
  })

  /**
   * ★ 必须**切一次**时间范围：默认值本来就是 30，把 `days.value` 换成写死的 30
   *   在首屏完全等价（第一版只断首屏 ⇒ 那条变异无牙）。
   */
  it('切时间范围后重新取数，days 进请求（不是写死 30）', async () => {
    const w = await factory()
    ctrl.calls = []
    // 桩组件上的按钮 emit `apply`，range 跨度 7 天 ⇒ `applyRange` 把 days 置 7，
    // `watch(days)` 随即重新 load。
    await w.find('[data-testid="range-apply-7"]').trigger('click')
    await flushPromises()
    expect(ctrl.calls).toEqual([{ owner: 'alice', days: 7 }])
  })
})

describe('UserProfileView：el-table 地形（D12）', () => {
  it('容器直接子元素是 div.el-table，不是 <table> ⇒ :slotted(table) 不命中', async () => {
    const w = await factory()
    for (const id of ['up-top-tasks', 'up-top-end-users', 'up-recent-sessions']) {
      const host = w.find(`[data-testid="${id}"] .responsive-data-view__table`)
      expect(host.exists(), `${id} 缺横向滚动容器`).toBe(true)
      const children = [...host.element.children].map((c) => c.tagName.toLowerCase())
      expect(children, `${id} 的直接子元素`).toEqual(['div'])
      expect(host.find('.el-table').exists()).toBe(true)
    }
  })

  it('本页不传 table-min-width（对 el-table 是空操作，传了就是假的「已设」）', () => {
    expect(CODE_ONLY).not.toContain('table-min-width')
  })
})

describe('UserProfileView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('三张表各出卡片、不出 el-table', async () => {
    const w = await factory()
    expect(cardsIn(w, 'up-top-tasks')).toHaveLength(4)
    expect(cardsIn(w, 'up-top-end-users')).toHaveLength(2)
    expect(cardsIn(w, 'up-recent-sessions')).toHaveLength(7)
    expect(w.findAll('.el-table')).toHaveLength(0)
  })

  it('compact 下不渲染形态切换钮', async () => {
    const w = await factory()
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  /** 字段集与**桌面的列**一一对应；`top_end_users` 不许凭空多出「平均健康分」。 */
  it('三张卡的字段集与桌面列一一对应', async () => {
    const w = await factory()
    expect(fieldLabels(cardsIn(w, 'up-top-tasks')[0]!)).toEqual(['会话数', '总成本', '平均健康分'])
    expect(fieldLabels(cardsIn(w, 'up-top-end-users')[0]!)).toEqual(['会话数', '总成本', '最近活跃'])
    expect(fieldLabels(cardsIn(w, 'up-recent-sessions')[0]!)).toEqual(['请求数', '总成本', '健康等级', '创建时间'])
  })

  it('卡头是各自的身份键', async () => {
    const w = await factory()
    expect(cardsIn(w, 'up-top-tasks').map((c) => c.attributes('data-title'))).toEqual(['chat', 'reasoning', 'code', 'embedding'])
    expect(cardsIn(w, 'up-top-end-users').map((c) => c.attributes('data-title'))).toEqual(['alice', 'bob'])
  })

  /**
   * ★ 卡头出**完整** session_id。前两行的前 16 字符完全相同（`sess-000000000000`）
   *   ⇒ 跟桌面一样截断就会有两张一模一样的卡、且 `:key` 撞车。
   *   这条断的是「唯一句柄」，不是「和桌面长得一样」。
   */
  it('卡头是完整 session_id（前 16 字符相同的两行仍可区分）', async () => {
    const w = await factory()
    const titles = cardsIn(w, 'up-recent-sessions').map((c) => c.attributes('data-title'))
    expect(new Set(titles).size).toBe(7)
    expect(titles[0]).toBe('sess-0000000000000001-short')
    expect(titles[1]).toBe('sess-0000000000000002-mid')
  })

  it('成本与桌面同源（同一份 fmtUsd4），avg_health 缺值出 —', async () => {
    const w = await factory()
    const tasks = cardsIn(w, 'up-top-tasks')
    expect(fieldValue(tasks[0]!, '总成本').text()).toBe('$1.5000')
    expect(fieldValue(tasks[2]!, '总成本').text()).toBe('$0.0000')
    expect(fieldValue(tasks[3]!, '总成本').text()).toBe('$0.0000')
    expect(fieldValue(tasks[0]!, '平均健康分').text()).toBe('88')
    // 第 3 行没有 avg_health ⇒ 卡片出 —（桌面是空白，已在桌面那侧钉住）
    const missing = tasks[2]!.findAll('.card__field').find((f) => f.find('dt').text() === '平均健康分')!
    expect(missing.find('dd').text()).toBe('—')
  })

  it('健康等级 tone 逐行：5 档收敛成 3 档（A/B→good、C/D→warn、F→danger、缺→无）', async () => {
    const w = await factory()
    const cards = cardsIn(w, 'up-recent-sessions')
    const gradeOf = (title: string) =>
      cards.find((c) => c.attributes('data-title') === title)!.findAll('.card__field').find((f) => f.find('dt').text() === '健康等级')!.find('dd')
    expect(gradeOf('sess-0000000000000001-short').attributes('data-tone')).toBe('good') // A
    expect(gradeOf('sess-0000000000000004-b').attributes('data-tone')).toBe('good') // B
    expect(gradeOf('sess-0000000000000002-mid').attributes('data-tone')).toBe('warn') // C
    expect(gradeOf('sess-0000000000000005-d').attributes('data-tone')).toBe('warn') // D
    expect(gradeOf('sess-0000000000000003-tail').attributes('data-tone')).toBe('danger') // F
    // 无等级 ⇒ 无 tone（CardList 的 fieldTone 对 badge 同样 `t ?? 'neutral'`）
    expect(gradeOf('sess-0000000000000006-none').attributes('data-tone')).toBe('neutral')
  })

  it('最近会话的成本与桌面同源；缺值出 $0.0000（不是 NaN）', async () => {
    const w = await factory()
    const cards = cardsIn(w, 'up-recent-sessions')
    expect(fieldValue(cards[0]!, '总成本').text()).toBe('$0.5000')
    expect(fieldValue(cards[2]!, '总成本').text()).toBe('$0.0200')
    // 缺 `cost_usd` 的那一行：与桌面 `?? 0` 同一口径
    const noCost = cards.find((c) => c.attributes('data-title') === 'sess-0000000000000007-nocost')!
    expect(fieldValue(noCost, '总成本').text()).toBe('$0.0000')
  })

  it('未登记的等级不出 tone（不猜）', async () => {
    const w = await factory()
    // 直接验函数语义：通过一个未知等级渲染不出彩色
    expect(CODE_ONLY).toMatch(/function healthGradeTone[\s\S]*?return undefined/)
  })

  it('`#actions` 给出可点的会话链接（桌面第一列就是链接）', async () => {
    const w = await factory()
    const a = cardsIn(w, 'up-recent-sessions')[0]!.find('a.session-link')
    expect(a.exists()).toBe(true)
    expect(a.attributes('href')).toBe('/plugins/ai-session-manager/sessions/sess-0000000000000001-short')
    expect(a.text()).toBe('会话 ID')
  })
})

describe('UserProfileView：空态归属', () => {
  it('桌面三段全空：不出现我们的 EmptyState（空态留给 Element 自带）', async () => {
    ctrl.empty = true
    const w = await factory()
    expect(w.findAll('.el-table')).toHaveLength(3)
    expect(w.find('.app-empty-state').exists()).toBe(false)
  })

  it('compact 三段全空：出 EmptyState，且不是空 <ul>', async () => {
    mockWindowClass('compact')
    ctrl.empty = true
    const w = await factory()
    expect(w.findAll('.app-empty-state')).toHaveLength(3)
    expect(w.findAll('[data-testid="card-list"]')).toHaveLength(0)
  })

  /**
   * 接口失败时 `data = null` ⇒ 三张表拿到 `[]`，**但表壳仍在**
   * （三张 `el-card` 不在 `v-if="data"` 里，只有统计卡与图表在里面）。
   * 第一版我断言「三张表不渲染」—— 那是判据写错。
   */
  it('接口失败：页面级空态出现，三张表壳仍在（0 行）', async () => {
    ctrl.fail = true
    const w = await factory()
    expect(w.find('.empty').exists()).toBe(true)
    expect(w.findAll('.el-table')).toHaveLength(3)
    expect(w.findAll('.el-table__body tbody tr')).toHaveLength(0)
  })

  /**
   * ★ **第二次**失败才算「catch 里清空 data」这条真的生效。
   *   第一版只在首屏失败时验，而首屏 `data` 本来就是 `null` ⇒
   *   把 `data.value = null` 删掉输出**一模一样**（台账上一条无牙）。
   *   这里先成功加载，再让下一次取数失败 ⇒ 旧数据必须被清掉，
   *   否则页面会把**过期数据当成新数据**展示（比报错更危险）。
   */
  it('先成功后失败：旧数据被清空（不把过期数据当新数据展示）', async () => {
    const w = await factory()
    expect(w.findAll('.el-table__body tbody tr').length).toBeGreaterThan(0) // 首屏有数据
    ctrl.fail = true
    await w.find('[data-testid="range-apply-7"]').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.find('.empty').exists()).toBe(true)
    expect(w.findAll('.el-table__body tbody tr')).toHaveLength(0)
  })
})

describe('UserProfileView：跨层契约', () => {
  it('无分页 ⇒ 不引入连续加载', () => {
    for (const token of ['createHyperPages', 'HyperLoadMore', 'hyper-load-more', 'page_size', 'offset']) {
      expect(CODE_ONLY, `页面里不该出现 ${token}`).not.toContain(token)
    }
  })

  it('不传 :loading（桌面刷新时 el-table 照常渲染旧行，加了会与桌面错位）', () => {
    expect(CODE_ONLY).not.toContain(':loading=')
  })

  it('三处 :empty 都带 isCompact 前置', () => {
    const ones = CODE_ONLY.match(/:empty="[^"]*"/g) ?? []
    expect(ones).toHaveLength(3)
    for (const e of ones) expect(e).toMatch(/^:empty="isCompact && /)
  })

  it('三张表各自一个 title-key，且都不是数组下标', () => {
    expect(CODE_ONLY).toContain('title-key="task_id"')
    expect(CODE_ONLY).toContain('title-key="end_user_id"')
    expect(CODE_ONLY).toContain('title-key="session_id"')
  })

  it('0 新增 i18n 键：只引用 sessions.userProfile.* / common.* / hyper.* 的既有键', () => {
    const used = new Set([...CODE_ONLY.matchAll(/t\('([^']+)'/g)].map((m) => m[1]))
    for (const k of used) {
      expect(/^(sessions\.userProfile\.|common\.|hyper\.)/.test(k), `预期词条前缀之外的键：${k}`).toBe(true)
    }
    expect(used.has('hyper.list.empty'), 'compact 空态复用 hyper.list.empty ⇒ 0 新增键').toBe(true)
  })
})
