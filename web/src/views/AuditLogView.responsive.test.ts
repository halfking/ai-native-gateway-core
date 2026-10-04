// AuditLogView.responsive.test.ts — H6 第四条垂直切片的门禁。
//
// 与前三条切片最大的不同：**本页的 loading / empty 原本长在表格里**
// （`<tr class="state-cell">` 两行，桌面刷新时表头照常显示，只是表内多一行字）。
// 若照搬 ApprovalListView 的写法把三态交给 `ResponsiveDataView` 裁定，
// 桌面刷新会让整张表连 `<thead>` 一起消失 —— 那是桌面观感回退。
// 所以本切片的关键门是「桌面三态仍然由表格自己出」，而不是「卡片能不能渲染」。
//
// 门禁清单：
// 1. 桌面零回归：5 列表头、徽章译名、目标拼装、详情截断、
//    loading/empty 两行 `.state-cell`、两条页码条、点行开抽屉
// 2. compact 连续加载：卡片、尾部控件、字段格式化、跨页去重、revision 闸门
// 3. 跨层契约：筛选单一真源、rowKey 用主键、两条路径不共享 ref、不新增端点、无硬编码中文
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import AuditLogView from './AuditLogView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'
import { useBreakpoint } from '../composables/useBreakpoint'

const source = readFileSync(resolve(process.cwd(), 'src/views/AuditLogView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

/**
 * ★ 满页 fixture 依赖组件里的 `COMPACT_PAGE_SIZE`，所以从源码取值而不是写死 50。
 *   锚点找不到就**抛错** —— 不抛的话「短页即无更多」会让尾部控件直接 exhausted，
 *   后面每条依赖「第 2 页」的用例都会静默作废（跑绿，但什么都没验）。
 */
const sizeAnchor = source.match(/const COMPACT_PAGE_SIZE = (\d+)/)
if (!sizeAnchor) {
  throw new Error('锚点 `const COMPACT_PAGE_SIZE = <数字>` 在 AuditLogView.vue 里不见了 —— 本文件的满页 fixture 与它绑定，请一并改这里')
}
const PAGE_SIZE = Number(sizeAnchor[1])

const { calls, ctrl, mockApi } = vi.hoisted(() => {
  type Entry = Record<string, unknown>
  let gen = 0
  const ctrl = {
    /** true 时请求挂起，直到测试手动放行 —— 制造「在途请求」。 */
    defer: false,
    resolvers: [] as Array<() => void>,
    /** 满页：否则「短页即无更多」会让尾部控件直接 exhausted，点不到。 */
    fullPage1: false,
    /** 空结果：量桌面空态与 compact 空态都要用。 */
    empty: false,
    /** 失败：验错误提示与可重试状态。 */
    fail: false,
    reset() {
      ctrl.defer = false
      ctrl.resolvers = []
      ctrl.fullPage1 = false
      ctrl.empty = false
      ctrl.fail = false
      // ★ `gen` 是闭包变量，不在 ctrl 上 —— 漏清它第 N 个用例就拿到 #g15，
      //   而 '#g1' 是 '#g15' 的子串，断言会两边一起错却照样绿。
      gen = 0
    },
  }

  /**
   * 四种动作，覆盖 `actionLabel` 的三条分支：
   * ① 直接有词条（`user.delete`）；② 走 `authentication.` → `auth.` 归一化后命中
   * （`authentication.logout`）；③ 完全无词条（`zzz.unknown`）—— 必须回落成原始码，
   * 而不是把 `auditLog.actions.zzz.unknown` 这种 key 甩到界面上。
   *
   * ② 不能省：少了它，归一化分支就没有 fixture 命中，
   * 「把归一化那行删掉」这个变异会全绿 —— 那是**看起来有门、实际没牙**。
   */
  const ACTIONS: Array<[string, string]> = [
    ['user.delete', '删除用户'],
    ['auth.login', '登录'],
    ['authentication.logout', '登出'],
    ['zzz.unknown', 'zzz.unknown'],
  ]

  // i 可以大于 23，所以走 Date.UTC（模板拼串会造出 T051: 非法时间）
  const mk = (i: number, generation: number): Entry => {
    const [action] = ACTIONS[(i - 1) % ACTIONS.length]
    return {
      id: 900 + i, // 与 i 一一对应但**不等于 i**：下标键会在这里撞车
      ts: new Date(Date.UTC(2026, 9, 5, i % 24, 15, 30)).toISOString(),
      actor: `u${i}#g${generation}`,
      action,
      target_type: i % 2 === 1 ? 'user' : undefined,
      target_id: 100 + i,
      after_json: { k: 'y'.repeat(120) }, // 故意超 80 字符，验详情截断
    }
  }

  const calls: Array<Record<string, unknown>> = []
  return {
    calls,
    ctrl,
    mockApi: () => ({
      getAuditLogs: vi.fn(async (params: Record<string, unknown> = {}) => {
        calls.push(params)
        const page = Number(params.page ?? 1)
        const generation = ++gen
        if (ctrl.fail) throw new Error('audit boom')
        let entries: Entry[]
        if (ctrl.empty) entries = []
        else if (ctrl.fullPage1) {
          entries =
            page === 1
              ? Array.from({ length: 50 }, (_, k) => mk(k + 1, generation))
              : [mk(51, generation), mk(52, generation)]
        } else entries = page === 1 ? [mk(1, generation), mk(2, generation), mk(3, generation), mk(4, generation)] : []
        if (ctrl.defer) await new Promise<void>((res) => ctrl.resolvers.push(res))
        return { entries, total: ctrl.empty ? 0 : 120 }
      }),
    }),
  }
})

vi.mock('../api', () => mockApi())

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: {
        pagination: { total: '共 {n} 条', pageOf: '第 {page} / {pages} 页', perPage: '每页', previous: '上一页', next: '下一页' },
        button: { search: '查询', clear: '重置', filter: '筛选' },
      },
      auditLog: {
        dash: '—',
        loadFailed: '加载审计日志失败',
        page: {
          title: '审计日志', desc: '记录用户管理与认证相关操作。',
          totalChip: '共 {n} 条', refresh: '刷新', refreshing: '刷新中…',
          emptyTitle: '暂无审计记录', emptyHint: '调整筛选条件或扩大时间范围后重试', loading: '加载中…',
        },
        filter: {
          actorLabel: '操作员', actorPlaceholder: '模糊匹配用户名',
          actionLabel: '动作', actionPlaceholder: '如 user.* 或 auth.*',
          fromLabel: '起始时间', toLabel: '截止时间',
        },
        table: {
          headers: { time: '时间', actor: '操作员', action: '动作', target: '目标', details: '详情' },
        },
        detail: {
          titleWithId: '审计详情 #{id}', close: '关闭',
          metaTime: '时间', metaActor: '操作员', metaAction: '动作', metaTarget: '目标',
          beforeTitle: '变更前', afterTitle: '变更后', noExtra: '无附加详情',
        },
        // ★ 故意**只给三条**动作词条：zzz.unknown 没有词条，必须回落成原始码。
        //   注意 `authentication.logout` 的词条键写的是归一化后的 `auth.logout` ——
        //   词条里并没有 `authentication.logout` 这一条。
        actions: {
          'user.delete': '删除用户',
          'auth.login': '登录',
          'auth.logout': '登出',
        },
      },
      hyper: {
        dataView: { table: '表格', cards: '卡片' },
        list: {
          empty: '暂无记录', allLoaded: '已全部加载 {count} 条', loadFailed: '加载失败，点击重试',
          retry: '重试', loadMore: '继续加载', loadingMore: '加载中…', refreshing: '正在刷新…',
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

/**
 * ★ `useBreakpoint` 是**模块级单例**，`bind()` 只在首次调用时跑一次。
 *   FilterBar 用它决定「<768 折叠面板」，而本页把筛选条交给 FilterBar ——
 *   所以本文件的 matchMedia 在第一次 bind 之前落在哪一档，之后**所有用例都固定**
 *   在那一档，后续 `mockWindowClass` 改不动它。
 *
 *   刻意绑在 **expanded**：FilterBar 的折叠是它自己的职责，不是本页的门禁对象；
 *   绑在 compact 就得每条筛选用例先点开折叠面板，噪声大且掩盖真正要验的东西。
 *   ★ 如实标注这条门禁的**盲区**：若哪天 FilterBar 的 <768 折叠坏了，
 *     本文件**测不到**。那是 FilterBar 自己该有的门禁。
 */
beforeAll(() => {
  mockWindowClass('expanded')
  useBreakpoint() // 触发一次 bind()，把单例钉在 expanded
})

async function factory() {
  const w = mount(AuditLogView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

/**
 * ★ 必须限定在 `[data-testid="card-list"]` 里取卡片。
 *   本页模板里有个 `<div class="card table-card">` —— 它也带 `card` 类，
 *   所以裸 `.card` 会把页面外壳一起选进来（第一版 4 条用例因此全部选错元素：
 *   cards[0] 是外壳、cards[1] 才是第一张卡）。
 */
function cardsOf(w: Awaited<ReturnType<typeof factory>>) {
  return w.findAll('[data-testid="card-list"] .card')
}

/**
 * 取第 `index` 张卡里、`<dt>` 文案为 `label` 的字段值。
 *
 * ★ 作用域必须是「那一张卡」。第一版只在整份列表里按 label 找字段，
 *   于是 cards[1] 的断言拿到的仍是 cards[0] 的值 —— 表现为
 *   「期望破折号、实得 user #101」，看起来像产品错了，实际是判据没定位到元素。
 */
function fieldValue(w: Awaited<ReturnType<typeof factory>>, index: number, label: string) {
  const card = cardsOf(w)[index]
  expect(card, `没有第 ${index} 张卡`).toBeTruthy()
  const hit = card!.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(hit, `卡片里找不到字段「${label}」，实际有：${card!.findAll('.card__field').map((f) => f.find('dt').text()).join('/')}`).toBeTruthy()
  return hit!.find('dd').text()
}

beforeEach(() => {
  _resetDataViewModeForTests()
  calls.length = 0
  ctrl.reset()
  mockWindowClass('expanded')
})

afterEach(() => vi.clearAllMocks())

describe('AuditLogView：桌面表格零回归', () => {
  it('5 列表头，文案与顺序不变', async () => {
    const w = await factory()
    const ths = w.findAll('table thead th')
    expect(ths).toHaveLength(5)
    expect(ths.map((th) => th.text())).toEqual(['时间', '操作员', '动作', '目标', '详情'])
  })

  it('徽章出译名，但 title 属性仍是原始动作码', async () => {
    const w = await factory()
    const badge = w.find('table tbody tr').find('.badge')
    expect(badge.text()).toBe('删除用户')
    expect(badge.attributes('title')).toBe('user.delete')
  })

  it('目标列由 target_type + target_id 拼装；无 target 时出破折号', async () => {
    const w = await factory()
    const cells = w.findAll('table tbody tr').map((tr) => tr.find('.col-target').text())
    // mk(1) 奇数 → user #101；mk(2) 偶数 → 无 target
    expect(cells[0]).toContain('user')
    expect(cells[0]).toContain('#101')
    expect(cells[1]).toBe('—')
  })

  it('详情超过 80 字符时截断并加省略号', async () => {
    const w = await factory()
    const detail = w.find('table tbody tr').find('.detail-preview')
    expect(detail.text().length).toBeLessThanOrEqual(80)
    expect(detail.text()).toMatch(/…$/)
  })

  /**
   * 桌面行内时间。
   *
   * ★ 不能断言「含 2026」—— `fmtDateCompact` 取的是**应用自己的** `localeRef`，
   *   不是测试 i18n 的 locale，默认 en-US 下出的是 `10/05, 09:15 AM`，没有年份。
   *   第一版写了 `toContain('2026')`，红的不是产品而是判据。
   *   真正的判别信号是「**不是裸 ISO 串**」：退回 `String(v)` 就会出现
   *   `2026-10-05T01:15:30.000Z` 这种带 `T` 和连字符的形态。
   */
  it('行内时间走本地化，不出现裸 ISO 串', async () => {
    const w = await factory()
    const cell = w.find('table tbody tr').find('.col-time').text()
    expect(cell).not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(cell).toMatch(/\d{1,2}[/:]\d{2}/)
  })

  /**
   * ★ 本切片的核心桌面门。
   *   loading 时表格**连表头一起在**，只是表内多一行居中文字。
   *   若把三态交给 `ResponsiveDataView`，桌面刷新会变成「只剩一个转圈」——
   *   那是肉眼可见的桌面观感回退，而且测试不会报任何错。
   */
  it('loading 时表格与表头仍在，状态行仍是表内那一行', async () => {
    ctrl.defer = true
    const w = await factory()
    expect(w.find('table thead').exists(), '桌面刷新不应把整张表连表头一起撤掉').toBe(true)
    expect(w.findAll('table thead th')).toHaveLength(5)
    const state = w.find('.state-cell')
    expect(state.exists()).toBe(true)
    expect(state.text()).toContain('加载中')
    expect(state.attributes('colspan')).toBe('5')
    // 三态没被容器接管
    expect(w.find('.responsive-data-view__state').exists()).toBe(false)
    expect(w.find('.app-empty-state').exists()).toBe(false)
  })

  it('empty 时仍是表内两段文字（标题 + 11px 灰字提示），不出 EmptyState', async () => {
    ctrl.empty = true
    const w = await factory()
    const state = w.find('.state-cell')
    expect(state.exists()).toBe(true)
    const ps = state.findAll('p')
    expect(ps).toHaveLength(2)
    expect(ps[0].text()).toBe('暂无审计记录')
    expect(ps[1].text()).toContain('调整筛选条件')
    expect(w.find('.app-empty-state').exists(), '桌面空态归表格，不归容器').toBe(false)
    // total 为 0 → 两条页码条都不出
    expect(w.findAll('.pagination-bar')).toHaveLength(0)
  })

  it('有数据时出**两条**页码条（上/下各一），且只请求第 1 页', async () => {
    const w = await factory()
    expect(w.findAll('.pagination-bar')).toHaveLength(2)
    expect(calls).toHaveLength(1)
    expect(calls[0].page).toBe(1)
    expect(calls[0].size).toBe(50)
  })

  it('点「下一页」按 page=2 重取，表格仍在', async () => {
    const w = await factory()
    const next = w.findAll('.pagination-bar')[0].findAll('.pagination-controls button')[1]
    await next.trigger('click')
    await flushPromises()
    expect(calls[1].page).toBe(2)
    expect(w.find('table thead').exists()).toBe(true)
  })

  it('桌面改筛选会带进请求，并回到第 1 页', async () => {
    const w = await factory()
    await w.findAll('input.filter-bar__control')[0].setValue('alice')
    await w.find('.filter-bar__search').trigger('click')
    await flushPromises()
    expect(calls).toHaveLength(2)
    expect(calls[1].actor).toBe('alice')
    expect(calls[1].page).toBe(1)
  })

  it('顶栏计数条显示服务端 total', async () => {
    const w = await factory()
    expect(w.find('.count-chip').text()).toContain('共 120 条')
  })

  it('点行打开详情抽屉', async () => {
    const w = await factory()
    expect(w.find('.drawer-panel').exists()).toBe(false)
    await w.find('table tbody tr.audit-row').trigger('click')
    expect(w.find('.drawer-panel').exists()).toBe(true)
    expect(w.find('#audit-detail-title').text()).toContain('901')
  })

  it('请求失败时仍出错误条且清空表格', async () => {
    ctrl.fail = true
    const w = await factory()
    expect(w.find('.alert-danger').text()).toContain('audit boom')
    expect(w.find('table tbody tr.audit-row').exists()).toBe(false)
  })

  it('桌面端不出连续加载尾部，也不出卡片', async () => {
    const w = await factory()
    expect(w.find('.hyper-load-more').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })
})

describe('AuditLogView：compact 连续加载路径', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出表格；不渲染页码条', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('table').exists()).toBe(false)
    expect(w.findAll('.pagination-bar')).toHaveLength(0)
    expect(w.find('.hyper-load-more').exists()).toBe(true)
  })

  it('卡头是动作译名，不是原始动作码', async () => {
    const w = await factory()
    const titles = w.findAll('.card__title').map((n) => n.text())
    // 四条动作：直接命中 / 直接命中 / 走 authentication.→auth. 归一化命中 / 无词条回落
    expect(titles).toEqual(['删除用户', '登录', '登出', 'zzz.unknown'])
  })

  it('卡片时间字段走 fmtTs：本地化形态，不出现裸 ISO 串', async () => {
    const w = await factory()
    const time = fieldValue(w, 0, '时间')
    expect(time, '卡面不应出现裸 ISO 时间戳').not.toMatch(/\d{4}-\d{2}-\d{2}T/)
    expect(time).toMatch(/\d{1,2}[/:]\d{2}/)
  })

  it('目标字段用整行拼装；无 target 时出破折号而不是 undefined', async () => {
    const w = await factory()
    // mk(1) 奇数 → user #101；mk(2) 偶数 → 无 target
    expect(fieldValue(w, 0, '目标')).toBe('user #101')
    expect(fieldValue(w, 1, '目标')).toBe('—')
    for (const c of cardsOf(w)) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })

  it('详情字段走截断（超过 80 字符加省略号）', async () => {
    const w = await factory()
    const detail = fieldValue(w, 0, '详情')
    expect(detail.length).toBeLessThanOrEqual(80)
    expect(detail).toMatch(/…$/)
  })

  it('顶栏计数条在 compact 下取连续加载带回来的服务端 total（不是页码 ref）', async () => {
    const w = await factory()
    // 页码 ref `total` 在 compact 下从不被写 —— 若接线漏了，这里会是「共 0 条」
    expect(w.find('.count-chip').text()).toContain('共 120 条')
  })

  it('empty 时出 EmptyState，且不再有表内状态行', async () => {
    ctrl.empty = true
    const w = await factory()
    expect(w.find('.app-empty-state').exists()).toBe(true)
    expect(w.find('.app-empty-state').text()).toContain('暂无审计记录')
    expect(w.find('.state-cell').exists()).toBe(false)
  })

  /**
   * ★ rowKey 的**行为**证明。
   *   满页 fixture 是必要的：否则「短页即无更多」让尾部控件直接 exhausted。
   *   第 2 页用 id 951/952（第 1 页是 901–950）—— 与第 1 页**下标相同**但主键不同，
   *   所以只有「按主键去重」的实现才拿得到 52 张卡。
   */
  it('跨页不去重：第 2 页两行不被第 1 页同下标行吃掉', async () => {
    ctrl.fullPage1 = true
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(PAGE_SIZE)
    await w.find('.hyper-load-more__btn--manual').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(PAGE_SIZE + 2)
  })

  /**
   * ★ revision 闸门的**行为**证明。
   *   `loadFirst()` 遇到第 1 页**在途**会提前 return（单飞）且**不**提升 revision ——
   *   所以没有显式 `invalidate()` 时，改筛选会被当成重复调用加入旧请求，
   *   **新筛选永远发不出去**（`calls` 停在 1，界面停在旧结果）。
   */
  it('compact 下改筛选会作废在途的第 1 页：既发出新请求，旧结果也不落地', async () => {
    ctrl.defer = true
    const w = mount(AuditLogView, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(calls).toHaveLength(1)

    await w.findAll('input.filter-bar__control')[0].setValue('alice')
    await w.find('.filter-bar__search').trigger('click')
    await flushPromises()
    expect(calls, '没有 invalidate 时第二次筛选会被单飞吞掉，calls 停在 1').toHaveLength(2)
    expect(calls[1].actor).toBe('alice')

    for (const r of ctrl.resolvers.splice(0)) r()
    await flushPromises()
    await flushPromises()
    const list = w.find('[data-testid="card-list"]').text()
    expect(list, '旧请求（gen 1）的结果不应落地').not.toMatch(/#g1(?!\d)/)
    expect(list, '新请求（gen 2）的结果应落地').toMatch(/#g2(?!\d)/)
  })

  it('请求失败时出错误条，且尾部控件转成可重试', async () => {
    ctrl.fail = true
    const w = await factory()
    expect(w.find('.alert-danger').text()).toContain('audit boom')
    // 失败必须被 createHyperPages 收得到才会转 failed；不抛的话会永远停在「加载中」
    expect(w.find('.hyper-load-more').attributes('data-state')).toBe('failed')
    expect(w.find('.hyper-load-more__btn').exists()).toBe(true)
  })

  /**
   * ★ 刷新钮的忙碌态必须跟着**两条路径各自的**在途信号走。
   *   桌面上 `busy === loading`（逐字不变）；compact 下 `loading` 恒为 false
   *   （`load()` 在 compact 下根本不会被调用），所以若把 busy 退回 `loading`，
   *   手机上取第 1 页时刷新钮永远可点、也永远不显示「刷新中…」。
   *   判据是渲染出来的 disabled 属性与文案，不是源码里的 busy 定义。
   */
  it('compact 下取第 1 页时刷新钮禁用且显示「刷新中…」', async () => {
    ctrl.defer = true
    const w = await factory()
    const btn = w.find('.header-actions button')
    expect(btn.attributes('disabled')).toBeDefined()
    expect(btn.text()).toContain('刷新中')
  })

  /** 反向对照：桌面下 busy 必须**等于** loading，不能各看各的。 */
  it('桌面下刷新钮的忙碌态与 loading 一致（在途时禁用、出「刷新中…」）', async () => {
    ctrl.defer = true
    const w = await factory()
    const btn = w.find('.header-actions button')
    expect(btn.attributes('disabled')).toBeDefined()
    expect(btn.text()).toContain('刷新中')
  })

  it('compact 下点卡片打开详情抽屉', async () => {
    const w = await factory()
    expect(w.find('.drawer-panel').exists()).toBe(false)
    await w.findAll('.card__head')[0].trigger('click')
    expect(w.find('.drawer-panel').exists()).toBe(true)
    expect(w.find('#audit-detail-title').text()).toContain('901')
  })
})

describe('AuditLogView：失败态不得显示空态（13 §7）', () => {
  /**
   * ★ 2026-10-06 实测补的（不是读码推断）。首屏取数失败时 `state === 'failed'`、
   *   `rows` 为空，而原判据 `:empty="isCompact && !compactBusy && rows.length === 0"`
   *   里 `compactBusy` **只认 `refreshing`** ⇒ failed 时它为假、取反为真
   *   ⇒ **错误横幅与「暂无审计记录」同时出现在屏上**。
   *   实测证据（修前）：compact + `getAuditLogs` reject ⇒
   *   `banner="audit boom"` 与 `empty="暂无审计记录"` 并存。
   *   ⇒ 处置是**改实现**（`:empty` 加上 `!compactFailed`），不是改门禁。
   */
  it('判据排除了 failed 态（compactFailed 存在且被 :empty 引用）', () => {
    expect(codeOnly).toContain("const compactFailed = computed(() => continuous.state.value === 'failed')")
    expect(codeOnly).toContain(':empty="isCompact && !compactBusy && !compactFailed && rows.length === 0"')
  })

  it('compactBusy 仍只认 refreshing（loadingNext 不该把刷新钮按成忙碌态）', () => {
    expect(codeOnly).toContain("const compactBusy = computed(() => continuous.state.value === 'refreshing')")
  })
})

describe('AuditLogView：跨层契约', () => {
  it('筛选条件只有一个真源（两条路径共用 filterBody）', () => {
    expect((codeOnly.match(/function filterBody\(/g) ?? []).length).toBe(1)
    // 定义 1 + load 1 + fetchPage 1 = 3
    expect((codeOnly.match(/filterBody\(\)/g) ?? []).length).toBeGreaterThanOrEqual(3)
  })

  it('filterBody 不含 page / size（那是加载方式，不是筛选条件）', () => {
    const start = codeOnly.indexOf('function filterBody(')
    const body = codeOnly.slice(start, start + 400)
    expect(body).not.toMatch(/^\s*page:/m)
    expect(body).not.toMatch(/^\s*size:/m)
  })

  /**
   * ★ 时间区间的 ISO 换算必须住在 `filterBody` 里。
   *   留在 `load()` 里的话，compact 路径会静默发出 `from=2026-01-01T00:00`
   *   这种非规范串 —— 服务端多半直接拒，于是「筛时间范围」在手机上整条失效，
   *   而桌面照常工作。 ⇒ 这类缺陷不会有任何前端报错。
   */
  it('datetime-local → ISO 的换算在 filterBody 里（两条路径都拿到）', () => {
    const start = codeOnly.indexOf('function filterBody(')
    const body = codeOnly.slice(start, start + 400)
    expect(body).toContain('parseLocalMinute')
    expect((codeOnly.match(/parseLocalMinute\(/g) ?? []).length).toBe(2)
  })

  it('rowKey 用后端主键 id，不是数组下标', () => {
    expect(codeOnly).toContain('rowKey: (e) => e.id')
    expect(codeOnly).not.toMatch(/rowKey:\s*\(.*\)\s*=>\s*i\b/)
  })

  it('页码与连续加载各自独立，不共享同一个 ref', () => {
    expect(codeOnly).toContain('const entries = ref<AuditLogEntry[]>([])')
    expect(codeOnly).toContain('const continuous = createHyperPages<AuditLogEntry>(')
    // `(?!=)` 是必需的：`compactBusy` 里写的是 `continuous.state.value === '...'`
    //，不带负向先行的话这条断言会匹配到 `===` 的第一个 `=` —— 然后永远红。
    expect(codeOnly).not.toMatch(/continuous\.(rows|hasMore|state)\.value\s*=(?!=)/)
    // 顶栏计数是第三条独立读数，不得回退去写页码的 total
    expect(codeOnly).not.toMatch(/continuousTotal\.value\s*=[^=]*r\.total[\s\S]{0,40}total\.value\s*=/)
  })

  it('页码条与连续加载尾部不同时出现（屏幕上不能有两个「加载更多」语义）', () => {
    expect(codeOnly).toContain('!isCompact.value && !loading.value && total.value > 0')
    expect(codeOnly).toMatch(/<HyperLoadMore[\s\S]{0,60}v-if="isCompact"/)
  })

  it('reload() 统一分派两条路径，且 compact 下先作废再重取', () => {
    expect(codeOnly).toMatch(
      /async function reload\(\)[\s\S]{0,320}continuous\.invalidate\(\)[\s\S]{0,80}continuous\.loadFirst\(\)/,
    )
  })

  /**
   * ★ 桌面三态必须**留**在表格里，所以容器的 `:loading` / `:empty` 都要带
   *   `isCompact` 前置。摘掉前置 = 桌面刷新时整张表连表头一起消失。
   */
  it('容器的 loading/empty 只在 compact 生效（桌面三态仍归表格）', () => {
    expect(codeOnly).toMatch(/:loading="isCompact\s*&&\s*compactBusy"/)
    // ★ 2026-10-06：这条原先只断到 `!compactBusy && rows.length === 0`，
    //   **把「失败态也显示空态」这个缺陷值钉住了**（见上面那个 describe）。
    //   现在加上 `!compactFailed` —— 是把判据改严，不是放松。
    expect(codeOnly).toMatch(/:empty="isCompact\s*&&\s*!compactBusy\s*&&\s*!compactFailed\s*&&\s*rows\.length\s*===\s*0"/)
  })

  it('表内两行 state-cell 仍在模板里（桌面三态的实现没被删）', () => {
    expect((codeOnly.match(/class="state-cell"/g) ?? []).length).toBe(2)
    expect(codeOnly).toMatch(/<tr\s+v-if="loading"/)
    expect(codeOnly).toMatch(/v-else-if="!entries\.length"/)
  })

  /**
   * ★ 不能出现两个横滚容器嵌套。
   *   `.table-wrap` 是**全局**类（`style.css` 里有），删掉本页的 div 之后
   *   横向滚动由 `ResponsiveDataView` 自带的 `overflow-x` 承担，只剩一层。
   */
  it('删掉本页的 .table-wrap（避免与容器自带的 overflow-x 嵌套出双滚动条）', () => {
    expect(codeOnly).not.toContain('table-wrap')
  })

  /**
   * ★ `table-min-width` 必须是 0px 而不是组件默认的 720px。
   *   本页原本**没有**表级 min-width（列宽由 `.col-*` 的 min/max 自己撑）。
   *   传 720 会在 1024–1200 的内容宽度上多出一条原本不存在的横滚动条。
   */
  it('table-min-width 显式传 0px，保持本页原有的列宽策略', () => {
    expect(codeOnly).toContain('table-min-width="0px"')
  })

  it('卡头走 titleFormat（否则裸动作码会被当标题甩给用户）', () => {
    expect(codeOnly).toContain(':title-format="cardTitle"')
    expect(codeOnly).toContain('title-key="action"')
    // 兜底：卡头取不到译名时回落到原始码，而不是空白
    expect(codeOnly).toMatch(/function cardTitle\(row: Record<string, unknown>\): string \{\s*return actionLabel\(/)
  })

  it('没有新增请求端点（仍走 api 层的 getAuditLogs）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('getAuditLogs')
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `模板里出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })
})
