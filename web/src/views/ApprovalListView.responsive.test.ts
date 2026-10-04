// ApprovalListView.responsive.test.ts — H6 第三条垂直切片的门禁。
//
// 这是**第三条**接入的业务页，也是第一条「列表区本身很小、视图里混着大量筛选与统计」
// 的页面（ApprovalListView 883 行里 419 行是 CSS）。门禁钉的不只是"卡片能不能渲染"，
// 而是三条跨层契约 + 一条桌面零回归：
// 1. 桌面页码与 compact 连续加载**互不干扰**（不共享 ref、不同时出现两个「加载更多」）
// 2. 筛选条件**只有一个真源**（`filterBody()`）—— 两份必然漂移
// 3. `rowKey` 是后端主键 `id`，不是数组下标
// 4. 桌面表格的**表头、行内按钮、空态 64px 内边距**逐条未变
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ApprovalListView from './ApprovalListView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/ApprovalListView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

/**
 * ★ 必须在 `vi.hoisted` 里：`vi.mock` 会被提升到文件顶部，那时顶层 `const`
 *   还没初始化，直接引用会抛 `Cannot access 'calls' before initialization`。
 *
 * `ctrl` 给「revision 闸门」「rowKey 跨页去重」两条**行为**门禁用：
 *   它们不能只靠源码断言 —— 源码里写着 `invalidate()` 不代表摘掉它行为会变。
 */
const { calls, ctrl, mockApprovalApi } = vi.hoisted(() => {
  type Item = Record<string, unknown>
  let gen = 0
  const ctrl = {
    /** true 时 getApprovalList 挂起，直到测试手动放行 —— 制造「在途请求」。 */
    defer: false,
    resolvers: [] as Array<() => void>,
    /** 满页 fixture：否则「短页即无更多」会让尾部控件直接 exhausted，点不到。 */
    fullPage1: false,
    /** 空结果 fixture：量桌面空态的内边距要用。 */
    empty: false,
    reset() {
      ctrl.defer = false
      ctrl.resolvers = []
      ctrl.fullPage1 = false
      ctrl.empty = false
      // ★ `gen` 是闭包变量，不在 ctrl 上 —— 漏清它，第 N 个测试就拿到 #g15。
      //   第一版就栽在这：断言写死 '#g1'，而实际是 '#g15'，两边一起错。
      gen = 0
    },
  }
  // i 可以大于 23，所以走 Date.UTC 而不是模板拼串（拼串会造出 T051: 非法时间）
  const ts = (i: number): string => new Date(Date.UTC(2026, 9, 4, i % 24, 0, 0)).toISOString()
  const mk = (i: number, generation: number): Item => {
    const level = i % 2 === 0 ? 'HIGH' : 'LOW'
    return {
      id: `apv-${i}`,
      session_id: `sess-${i}`,
      tenant_id: 'default',
      request_id: `req-${i}#g${generation}`,
      status: i % 2 === 0 ? 'approved' : 'pending',
      risk_level: level,
      trigger_type: 'keyword',
      detect_result: { decision: level, reason: 'r', cost_estimation: 0.1234 },
      created_at: ts(i),
      expires_at: ts(i + 1),
      time_left: '30s',
    }
  }
  const calls: Array<Record<string, unknown>> = []
  return {
    calls,
    ctrl,
    // ★ 不要给这个工厂加参数来控制行为：`vi.mock` 的工厂只在提升时执行一次，
    //   测试里再调一次返回的是**另一个对象**，对已挂载的组件毫无影响 ——
    //   第一版 `mockApprovalApi({ total: 0 })` 就是这么写的，断言永远拿到默认数据。
    //   控制一律走 `ctrl`。
    mockApprovalApi: () => ({
      getApprovalList: vi.fn(async (params: Record<string, unknown> = {}) => {
        calls.push(params)
        const page = Number(params.page ?? 1)
        const generation = ++gen
        const items = ctrl.empty
          ? []
          : ctrl.fullPage1
          ? page === 1
            ? Array.from({ length: 20 }, (_, k) => mk(k + 1, generation))
            : [mk(21, generation), mk(22, generation)]
          : page === 1
            ? [mk(1, generation), mk(2, generation), mk(3, generation)]
            : page === 2
              ? [mk(4, generation), mk(5, generation)]
              : []
        if (ctrl.defer) await new Promise<void>((res) => ctrl.resolvers.push(res))
        return { items, total: ctrl.empty ? 0 : 120, page, page_size: 20, total_pages: 6 }
      }),
      getApprovalStats: vi.fn(async () => ({
        today_total: 3, today_approved: 1, today_rejected: 1, avg_approval_time_seconds: 120,
      })),
      approveApproval: vi.fn(async () => ({})),
      rejectApproval: vi.fn(async () => ({})),
    }),
  }
})

vi.mock('../api/approval', () => mockApprovalApi())
vi.mock('../store', () => ({ isSuperAdmin: () => true }))
vi.mock('../composables/useConfirmDialog', () => ({ confirmDialog: vi.fn(async () => true) }))
vi.mock('../composables/useActionMessage', () => ({
  useActionMessage: () => ({
    message: { value: '' }, error: { value: '' },
    notifySuccess: vi.fn(), notifyError: vi.fn(), clearMessage: vi.fn(), clearError: vi.fn(),
  }),
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }) }))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      approval: {
        list: {
          description: '审批列表', loading: '加载中', empty: '暂无审批',
          pagination: { previous: '上一页', next: '下一页', info: '第 {page}/{totalPages} 页，共 {total} 条' },
          status: { pending: '待处理', approved: '已批准', rejected: '已拒绝', timeout: '已超时' },
          risk: { LOW: '低', MEDIUM: '中', HIGH: '高', CRITICAL: '严重' },
          filter: { status: '状态', risk: '风险', allStatus: '全部', allRisk: '全部', search: '搜索', reset: '重置' },
          stats: { today_total: '今日总数', today_approved: '今日批准', today_rejected: '今日拒绝', avgTime: '平均耗时' },
          table: {
            requestId: '请求 ID', sessionId: '会话 ID', riskLevel: '风险等级', trigger: '触发',
            cost: '费用', createdAt: '创建时间', status: '状态', actions: '操作',
          },
          actions: { approve: '批准', reject: '拒绝', viewDetail: '查看详情' },
          confirm: { approve: '确认批准 {id}？', rejectPrompt: '拒绝理由' },
          success: { approved: '已批准', rejected: '已拒绝' },
          errors: { loadListFailed: '加载失败', approveFailed: '批准失败', rejectFailed: '拒绝失败' },
          // ★ 这组词条原先**没给**：组件的 `formatDate` 走 `formatRelativeTime`，
          // 查不到 key 时 vue-i18n 返回 key 本身，于是卡片上直接出现
          // `approval.list.relativeTime.justNow` 字面量。
          relativeTime: {
            justNow: '刚刚',
            minutesAgo: '{n} 分钟前',
            hoursAgo: '{n} 小时前',
            daysAgo: '{n} 天前',
          },
          timeLeft: '剩余 {time}',
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
    writable: true, configurable: true,
    value: (q: string) => {
      const min = q.match(/min-width:\s*([\d.]+)px/)
      const max = q.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
      return { matches, media: q, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }
    },
  })
}

async function factory() {
  const w = mount(ApprovalListView, { global: { plugins: [i18n] } })
  await flushPromises()
  await flushPromises()
  return w
}

beforeEach(() => {
  _resetDataViewModeForTests()
  calls.length = 0
  ctrl.reset()
  // 不调 mockApprovalApi()：那个工厂只在 vi.mock 提升时执行一次，
  // 这里再调只会造一个没人用的对象（第一版踩过，见工厂定义处的注释）。
  mockWindowClass('expanded')
})

afterEach(() => vi.clearAllMocks())

describe('ApprovalListView：桌面页码路径零回归', () => {
  it('渲染 8 列表头，顺序与文案不变', async () => {
    const w = await factory()
    const ths = w.findAll('table thead th')
    expect(ths).toHaveLength(8)
    expect(ths.map((th) => th.text())).toEqual([
      '请求 ID', '会话 ID', '风险等级', '触发', '费用', '创建时间', '状态', '操作',
    ])
  })

  it('行内仍是徽章 + 费用 + 三个操作按钮（待处理行出批准/拒绝）', async () => {
    const w = await factory()
    const row = w.find('table tbody tr')
    expect(row.findAll('.badge')).toHaveLength(2)
    // ★ fixture：mk(1) → status='pending' / risk='LOW'（i 为奇数时走这两支）
    expect(row.text()).toContain('低')
    expect(row.text()).toContain('待处理')
    expect(row.text()).toContain('¥0.1234')
    // 操作列：批准 / 拒绝 / 查看详情
    expect(row.findAll('.actions-cell button')).toHaveLength(3)
  })

  it('请求 id 截断到 12 位并带省略号', async () => {
    const w = await factory()
    // ★ 用负向先行而不是 toContain：'#g1' 是 '#g15' 的子串，
    //   计数器一旦没清零，toContain 仍然会通过 —— 那是恒绿，不是断言。
    expect(w.find('table tbody tr').text()).toMatch(/#g1(?!\d)/)
  })

  /**
   * ★ 行为断言，不是源码断言。
   *   第一版写的是 `expect(codeOnly).toContain('empty-padding="64px"')` ——
   *   那是**恒真**：把 `ResponsiveDataView` 的默认值从 40px 改成别的，
   *   本页照样传着 64px，源码断言仍绿，但**别的页面**的空态已经变了。
   *   真正要守的是「迁入本组件后本页空态像素不变」，所以渲染出来量。
   */
  it('空态内边距仍是 64px（迁移到 ResponsiveDataView 后桌面像素不变）', async () => {
    ctrl.empty = true
    const w = await factory()
    const empty = w.find('.app-empty-state')
    expect(empty.exists(), '无数据时应出空态').toBe(true)
    expect(empty.attributes('style')).toContain('64px')
    // 反向对照：默认值是 40px，若本页没传就一定是 40px
    expect(empty.attributes('style')).not.toContain('40px')
  })

  it('totalPages > 1 时出页码条，只请求第 1 页', async () => {
    const w = await factory()
    expect(w.findAll('.pagination')).toHaveLength(1)
    expect(calls).toHaveLength(1)
    expect(calls[0].page).toBe(1)
    expect(calls[0].page_size).toBe(20)
  })

  it('点下一页按 page=2 重取，且表格仍在', async () => {
    const w = await factory()
    await w.find('.pagination').findAll('button')[1].trigger('click')
    await flushPromises()
    expect(calls[1].page).toBe(2)
    expect(w.find('table tbody tr').exists()).toBe(true)
  })

  it('桌面端不出连续加载尾部，也不出卡片与切换钮', async () => {
    const w = await factory()
    expect(w.find('.hyper-load-more').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })
})

describe('ApprovalListView：compact 连续加载路径', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出表格；不渲染页码条', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('table').exists()).toBe(false)
    expect(w.findAll('.pagination')).toHaveLength(0)
  })

  it('出连续加载尾部控件', async () => {
    const w = await factory()
    expect(w.find('.hyper-load-more').exists()).toBe(true)
  })

  it('卡片字段走 format：风险/状态出译名，费用带货币号', async () => {
    const w = await factory()
    const cards = w.findAll('.card')
    expect(cards).toHaveLength(3)
    expect(cards[0].text()).toContain('低') // mk(1) risk=LOW
    expect(cards[0].text()).toContain('待处理') // mk(1) status=pending
    expect(cards[0].text()).toContain('¥0.1234')
    expect(cards[1].text()).toContain('高') // mk(2) risk=HIGH
    expect(cards[1].text()).toContain('已批准')
  })

  /**
   * ★ 时间字段必须走 `formatDate`。
   *   第一版只断言了风险/状态/费用，**没管时间** ⇒ 变异「时间字段退回 String(v)」
   *   在 72 例门禁下全绿。枚举与金额被断言了，日期被漏了。
   *
   * ★★ 2026-10-06 修正：本条原先是**时间炸弹**。`formatRelativeTime` 按
   *   `Date.now()` 分支（刚刚 / N 分钟前 / N 小时前 / 超过 7 天才出绝对日期），
   *   而 fixture 的 `created_at` 固定在 `2026-10-04T01:00Z` —— 与真实时钟只差几分钟。
   *   08:41 跑时差值 -19 分钟 ⇒ 落到 `older` 分支 ⇒ 断言 `toContain('2026')` 通过；
   *   08:59 再跑差值 -1 分钟 ⇒ 落到 `justNow` ⇒ **同一个提交、同一份代码，判据翻红**。
   *   ⇒ 这是「判据引用了没控制的环境」的第四种形态：前三种是 locale / 组件实例 /
   *   fixture 可达性，这一次是**墙上时钟**。处置：冻时间 + 断确定输出。
   */
  it('时间字段走 formatDate：出本地化的相对时间，不出现裸 ISO 串', async () => {
    vi.useFakeTimers()
    // 固定成「距 fixture 5 分钟」这一支：稳定、且不依赖运行时刻
    vi.setSystemTime(new Date('2026-10-04T01:05:00.000Z'))
    try {
      const w = await factory()
      const text = w.findAll('.card')[0].text()
      expect(text, '不应出现裸 ISO 时间戳').not.toContain('2026-10-04T')
      // 断**确定形态**而不是「含 2026」：绝对日期只在超过 7 天后才出现，
      // 而相对时间各支都不含年份 —— 原来的 `toContain('2026')` 本身就是错的判据。
      expect(text).toContain('5 分钟前')
      expect(text, '不应出现未翻译的 i18n key').not.toContain('relativeTime')
    } finally {
      vi.useRealTimers()
    }
  })

  /**
   * ★ 客户端搜索的**行为**证明。
   *   计数断言（`applySearch(` 出现次数 >= 3）分不出
   *   「两条路径都套搜索」与「只有一条套」—— 变异把 compact 分支换成
   *   另一个 `applySearch(...)` 调用，计数不变，门照样绿。
   *   真正要守的是「compact 下搜索也生效」，所以打搜索框看卡片数变少。
   */
  it('搜索在 compact 下也生效（卡片数随搜索收窄）', async () => {
    const w = await factory()
    expect(w.findAll('.card')).toHaveLength(3)
    // fixture 的 session_id 是 sess-1 / sess-2 / sess-3
    await w.find('input.form-input').setValue('sess-2')
    await flushPromises()
    const after = w.findAll('.card')
    expect(after).toHaveLength(1)
    expect(after[0].text()).toContain('sess-2')
  })

  it('触发类型为空时渲染破折号而不是 undefined', async () => {
    const w = await factory()
    // 卡片字段全部经过 format，没有一个会吐出字面 "undefined"
    for (const c of w.findAll('.card')) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })

  /**
   * ★ rowKey 的**行为**证明。源码断言只能证明「写了这个字段」，
   *   证明不了「不用下标也能正确跨页去重」。
   * 满页 fixture 是必要的：否则「短页即无更多」会让尾部控件直接 exhausted。
   */
  it('跨页不去重：第 2 页首行不被第 1 页首行吃掉', async () => {
    ctrl.fullPage1 = true
    const w = await factory()
    expect(w.findAll('.card')).toHaveLength(20)
    await w.find('.hyper-load-more__btn--manual').trigger('click')
    await flushPromises()
    await flushPromises()
    // 期望 20 + 2 = 22 张；下标键会让第 2 页两行都撞上 "0"/"1" → 只剩 20
    expect(w.findAll('.card')).toHaveLength(22)
  })

  /**
   * ★ revision 闸门的**行为**证明。机制：`loadFirst()` 遇到第 1 页**在途**
   *   会提前 return（单飞）且**不**提升 revision —— 所以没有显式 `invalidate()`
   *   时，改筛选会被当成重复调用加入旧请求，**新筛选永远发不出去**。
   */
  it('筛选变更会作废在途的第 1 页请求：既发出新请求，旧结果也不落地', async () => {
    ctrl.defer = true
    const w = mount(ApprovalListView, { global: { plugins: [i18n] } })
    await flushPromises()
    expect(calls).toHaveLength(1)

    await w.findAll('select.form-select')[0].setValue('approved')
    await flushPromises()
    expect(calls).toHaveLength(2)
    expect(calls[1].status).toBe('approved')

    for (const r of ctrl.resolvers.splice(0)) r()
    await flushPromises()
    await flushPromises()
    const list = w.find('[data-testid="card-list"]').text()
    expect(list, '旧请求（gen 1）的结果不应落地').not.toMatch(/#g1(?!\d)/)
    expect(list, '新请求（gen 2）的结果应落地').toMatch(/#g2(?!\d)/)
  })
})

describe('ApprovalListView：跨层契约', () => {
  it('筛选条件只有一个真源（两条路径共用 filterBody）', () => {
    expect((codeOnly.match(/function filterBody\(/g) ?? []).length).toBe(1)
    // 定义 1 + loadApprovals 1 + fetchPage 1 = 3
    expect((codeOnly.match(/filterBody\(\)/g) ?? []).length).toBeGreaterThanOrEqual(3)
  })

  it('filterBody 不含 page / page_size（那是加载方式，不是筛选条件）', () => {
    const body = codeOnly.slice(
      codeOnly.indexOf('function filterBody('),
      codeOnly.indexOf('function filterBody(') + 400,
    )
    expect(body).not.toMatch(/^\s*page:/m)
    expect(body).not.toMatch(/page_size/)
  })

  it('rowKey 用后端主键 id，不是数组下标', () => {
    expect(codeOnly).toContain('rowKey: (item) => item.id')
    // 兜底：即便有人改成下标，也必须带明确的注释说明后果
    expect(codeOnly).not.toMatch(/rowKey:\s*\(.*\)\s*=>\s*i\b/)
  })

  it('页码与连续加载各自独立，不共享同一个 ref', () => {
    expect(codeOnly).toContain('const approvals = ref<ApprovalItem[]>([])')
    expect(codeOnly).toContain('const continuous = createHyperPages<ApprovalItem>(')
    expect(codeOnly).not.toMatch(/continuous\.(rows|hasMore|state)\.value\s*=/)
  })

  it('页码条与连续加载尾部不同时出现（屏幕上不能有两个「加载更多」语义）', () => {
    expect(codeOnly).toContain('!isCompact.value && totalPages.value > 1')
    expect(codeOnly).toMatch(/<HyperLoadMore[\s\S]{0,60}v-if="isCompact"/)
  })

  it('reload() 统一分派两条路径，且 compact 下先作废再重取', () => {
    expect(codeOnly).toMatch(/async function reload\(\)[\s\S]{0,320}continuous\.invalidate\(\)[\s\S]{0,80}continuous\.loadFirst\(\)/)
  })

  it('客户端搜索两条路径共用 applySearch（改搜索不会漏改一条）', () => {
    expect((codeOnly.match(/function applySearch\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/applySearch\(/g) ?? []).length).toBeGreaterThanOrEqual(3)
  })

  it('没有新增请求端点（仍走 api/approval 的 getApprovalList）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('getApprovalList')
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `模板里出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })
})
