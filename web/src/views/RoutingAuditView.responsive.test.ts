// RoutingAuditView.responsive.test.ts — H6 第十条垂直切片的门禁。
//
// 本页形状是**第四种三态归属**（前三条见 TenantModelsView.responsive.test.ts 头部）：
// 桌面是 `<p v-if="!loading && !entries.length" class="empty">` + `<table v-else>`。
// 拆开看有**两个**空档，两档都要求「整块不出现」：
//   ① 空态（!loading 且无行）      → 出 `.empty`，无表无卡
//   ② 首载中（loading 且无行）    → **既不出空态也不出表**，卡片区只剩标题
//   ③ 刷新中（loading 但有旧行）  → 表格**不消失**（旧数据仍在屏上）
// ⇒ `v-else` 挂在 `ResponsiveDataView` 上（不是表格上），两档共用页面自己的 `.empty`，
// **不传 `:empty` / `:loading`** —— 传了就是死代码，而且会给桌面凭空加一个空态块。
//
// 门禁清单：
// 1. 桌面零回归：8 列表头与顺序、汇总卡四张与配色、动作徽章三支 class、
//    override 链接与缺省破折号、三个标签、模型截断、reason/actor 缺省、
//    展开行的 colspan=8 与两行 diff
// 2. 三个空档（空态 / 首载 / 刷新中保留旧表）逐个钉住
// 3. compact：出卡片不出表、无切换钮、6 个字段走 format、动作 tone 逐行、
//    任务/Profile/模式合并、#actions 的链接与展开钮（≤2）
// 4. 跨层契约：v-else 形态、`:empty`/`:loading` 都不传、`table-min-width="0px"`、
//    **title-key 必须是唯一主键**、不引入连续加载、格式化只有一份、无硬编码中文
import { flushPromises, mount, RouterLinkStub } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import RoutingAuditView from './RoutingAuditView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'
import { fmtDateTime24h } from '../i18n/useFormat'

const source = readFileSync(resolve(process.cwd(), 'src/views/RoutingAuditView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const { ctrl, ENTRIES } = vi.hoisted(() => {
  const ctrl = {
    fail: false,
    pending: false,
    empty: false,
    reset() {
      ctrl.fail = false
      ctrl.pending = false
      ctrl.empty = false
    },
  }
  /**
   * 6 行覆盖：
   * - 三个动作各一支（insert/update/delete）⇒ tone 逐行可断
   * - override_id 有 / 无（无 ⇒ 破折号且卡片里无链接）
   * - 任务/Profile/模式 三段齐 / 只一段 / 全缺（⇒ 出 '—'）
   * - model_chosen 28 字（截断）/ 18 字（不截断）/ 19 字（截断）
   *   ★ 18 与 19 落在 `length > 18` 阈值**两侧**：缺任一条，「阈值被挪」那条变异就是行为等价的
   * - expires_at 两者齐 / 只有一个 / 都没有（都没有 ⇒ 不出展开钮）
   * - reason / actor 为 null（⇒ 桌面走 `?? '—'` / `?? 'system'`，卡片走 '—'）
   */
  const ENTRIES = [
    {
      id: 101, ts: '2026-10-05T01:15:30.000Z', action: 'insert',
      override_id: 42, task_type: 'chat', profile: 'default', mode: 'auto',
      model_chosen: 'gpt-4o-2024-08-06-model-extra',
      reason: '封禁违规模型', actor: 'admin',
      old_expires_at: '2026-09-01T00:00:00Z', expires_at: '2026-11-01T00:00:00Z',
    },
    {
      id: 102, ts: '2026-10-05T02:15:30.000Z', action: 'delete',
      override_id: 7, model_chosen: 'claude-sonnet',
      reason: null, actor: null,
    },
    {
      id: 103, ts: '2026-10-05T03:15:30.000Z', action: 'update',
      override_id: null, task_type: 'vision',
      model_chosen: undefined, reason: '调整优先级', actor: 'ops',
      expires_at: '2026-12-01T00:00:00Z',
    },
    {
      id: 104, ts: '2026-10-05T04:15:30.000Z', action: 'insert',
      model_chosen: 'm12345678901234567', reason: '恢复', actor: 'admin',
    },
    {
      id: 105, ts: '2026-10-05T05:15:30.000Z', action: 'update',
      override_id: 9, task_type: 'chat', profile: 'p1', mode: 'manual',
      model_chosen: 'm123456789012345678', reason: '换绑', actor: 'admin',
    },
    {
      id: 106, ts: '2026-10-05T06:15:30.000Z', action: 'insert',
      override_id: 11, model_chosen: 'm-1', reason: null, actor: null,
    },
  ]
  return { ctrl, ENTRIES }
})

vi.mock('../api', () => ({
  getRoutingAudit: vi.fn(async (params: Record<string, unknown>) => {
    if (ctrl.fail) throw new Error('audit boom')
    if (ctrl.pending) return new Promise(() => {})
    if (ctrl.empty) return { entries: [], count: 0 }
    // 服务端筛选语义：action 只在非空时收窄
    const want = String(params.action ?? '')
    return { entries: want ? ENTRIES.filter((e) => e.action === want) : ENTRIES, count: ENTRIES.length }
  }),
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      routingAudit: {
        title: '路由覆盖审计',
        subtitle: '说明文案',
        summary: { total: '总计', inserts: '新增', updates: '更新', deletes: '删除' },
        filter: {
          action: '动作', all: '(全部)', actor: '操作者', actorPlaceholder: '管理员用户名',
          overrideId: '覆盖 ID', overrideIdPlaceholder: '如 42', window: '时间窗口',
          limit: '条数限制', refresh: '刷新', loading: '加载中…',
          days: { d1: '1 天', d7: '7 天', d30: '30 天', d90: '90 天' },
          limits: { l50: '50', l200: '200', l500: '500', l1000: '1000' },
        },
        actions: { insert: '新增', update: '更新', delete: '删除' },
        table: {
          title: '审计记录 ({n})', empty: '没有匹配的审计记录。', details: '详情',
          headers: {
            when: '时间', action: '动作', override: '覆盖',
            taskProfileMode: '任务 / Profile / 模式', model: '模型', reason: '原因', actor: '操作者',
          },
        },
        expand: { oldExpires: '变更前 expires_at', newExpires: '变更后 expires_at', noDiff: '此操作无差异字段' },
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
  const w = mount(RoutingAuditView, {
    global: { plugins: [i18n], stubs: { RouterLink: RouterLinkStub } },
  })
  await flushPromises()
  await flushPromises()
  return w
}

function cardsOf(w: Awaited<ReturnType<typeof factory>>) {
  return w.findAll('[data-testid="card-list"] .card')
}

function fieldValue(card: ReturnType<typeof cardsOf>[number], label: string) {
  const hit = card.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(
    hit,
    `卡片里找不到字段「${label}」，实际有：${card.findAll('.card__field').map((f) => f.find('dt').text()).join('/')}`,
  ).toBeTruthy()
  return hit!.find('dd')
}

/** 按 `id` 取卡（不用下标：渲染序一旦变化，下标会指错行而红的是判据）。 */
function cardById(w: Awaited<ReturnType<typeof factory>>, id: number) {
  const idx = ENTRIES.findIndex((e) => e.id === id)
  expect(idx, `fixture 里没有 id=${id}`).toBeGreaterThan(-1)
  return cardsOf(w)[idx]!
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

describe('RoutingAuditView：桌面审计表零回归', () => {
  it('8 列表头，文案与顺序不变（末列无标题）', async () => {
    const w = await factory()
    const ths = w.findAll('.audit-table thead th')
    expect(ths).toHaveLength(8)
    expect(ths.map((th) => th.text())).toEqual([
      '时间', '动作', '覆盖', '任务 / Profile / 模式', '模型', '原因', '操作者', '',
    ])
  })

  it('汇总卡四张：总计 + 三支动作数（且只在有行时出现）', async () => {
    const w = await factory()
    const cards = w.findAll('.summary-card')
    expect(cards).toHaveLength(4)
    expect(cards.map((c) => c.find('.summary-value').text())).toEqual(['6', '3', '2', '1'])
    // 桌面原样：insert 绿 / update 强调色 / delete 红，都走内联 var
    expect(cards[1]!.find('.summary-value').attributes('style')).toContain('var(--success)')
    expect(cards[2]!.find('.summary-value').attributes('style')).toContain('var(--accent)')
    expect(cards[3]!.find('.summary-value').attributes('style')).toContain('var(--danger)')
  })

  it('汇总卡三支计数按动作分类（把分类条件换掉能被抓住）', async () => {
    const w = await factory()
    const vals = w.findAll('.summary-card').map((c) => c.find('.summary-value').text())
    // insert=101/104/106 → 3；update=103/105 → 2；delete=102 → 1
    expect(vals).toEqual(['6', '3', '2', '1'])
  })

  it('动作徽章三支 class 与译名', async () => {
    const w = await factory()
    const rows = w.findAll('.audit-table tbody tr.audit-row')
    expect(rows).toHaveLength(6)
    const badge = (i: number) => rows[i]!.find('.action-badge')
    expect(badge(0)!.classes().join(' ')).toContain('action-insert')
    expect(badge(0)!.text()).toBe('新增')
    expect(badge(1)!.classes().join(' ')).toContain('action-delete')
    expect(badge(1)!.text()).toBe('删除')
    expect(badge(2)!.classes().join(' ')).toContain('action-update')
    expect(badge(2)!.text()).toBe('更新')
  })

  it('覆盖列：有值出 router-link，缺值出破折号且无链接', async () => {
    const w = await factory()
    const rows = w.findAll('.audit-table tbody tr.audit-row')
    const link = rows[0]!.findComponent(RouterLinkStub)
    expect(link.props('to')).toBe('/routing/overrides#42')
    expect(rows[0]!.findAll('td')[2]!.text()).toBe('#42')
    // id=103 的 override_id 为 null
    expect(rows[2]!.find('a').exists()).toBe(false)
    expect(rows[2]!.findAll('td')[2]!.text()).toBe('—')
  })

  it('任务 / Profile / 模式：三个标签各自条件渲染', async () => {
    const w = await factory()
    const rows = w.findAll('.audit-table tbody tr.audit-row')
    // ★ 作用域写进选择器：`.tag` 会连模型格的 `.tag-model` 一起吃进来
    //   （第一版就因此多出一个 'gpt-4o-...'），所以按**列**取，不按类名取。
    const tags = (i: number) => rows[i]!.findAll('td')[3]!.findAll('.tag').map((s) => s.text())
    expect(tags(0)).toEqual(['chat', 'default', 'auto'])
    expect(tags(2)).toEqual(['vision'])      // 只有 task_type
    expect(tags(1)).toEqual([])              // 三个全缺
  })

  it('模型列走 shortModel：28 字截断、18 字不截断、19 字截断', async () => {
    const w = await factory()
    const rows = w.findAll('.audit-table tbody tr.audit-row')
    const cell = (i: number) => rows[i]!.findAll('td')[4]!.text()
    expect(cell(0)).toBe('gpt-4o-2024-08-...')   // 28 字 → 15 字 + 省略
    expect(cell(3)).toBe('m12345678901234567')    // 18 字 → 原样
    expect(cell(4)).toBe('m12345678901234...')    // 19 字 → 取前 15 字 + 省略
    expect(cell(2)).toBe('—')                     // model_chosen 缺 ⇒ 破折号
  })

  it('原因与操作者：null 时分别回落破折号与 system', async () => {
    const w = await factory()
    const rows = w.findAll('.audit-table tbody tr.audit-row')
    expect(rows[1]!.findAll('td')[5]!.text()).toBe('—')
    expect(rows[1]!.find('.actor').text()).toBe('system')
    expect(rows[0]!.findAll('td')[5]!.text()).toBe('封禁违规模型')
    expect(rows[0]!.find('.actor').text()).toBe('admin')
  })

  it('展开行：colspan=8，两行 diff（变更前 / 变更后）', async () => {
    const w = await factory()
    const btn = w.findAll('.audit-table tbody tr.audit-row')[0]!.find('.btn-expand')
    expect(btn.text()).toBe('+')
    await btn.trigger('click')
    await flushPromises()
    const exp = w.find('.audit-table tbody tr.expand-row')
    expect(exp.exists()).toBe(true)
    expect(exp.find('td').attributes('colspan')).toBe('8')
    expect(exp.findAll('.diff-field')).toHaveLength(2)
    expect(exp.text()).toContain('2026-09-01T00:00:00Z')
    expect(exp.text()).toContain('2026-11-01T00:00:00Z')
    // 再点一次收起
    await w.findAll('.audit-table tbody tr.audit-row')[0]!.find('.btn-expand').trigger('click')
    await flushPromises()
    expect(w.find('.audit-table tbody tr.expand-row').exists()).toBe(false)
  })

  it('只有一个 expires_at 时 diff 只出一行', async () => {
    const w = await factory()
    const rows = w.findAll('.audit-table tbody tr.audit-row')
    await rows[2]!.find('.btn-expand').trigger('click')
    await flushPromises()
    const exp = w.find('.audit-table tbody tr.expand-row')
    expect(exp.findAll('.diff-field')).toHaveLength(1)
    expect(exp.text()).toContain('2026-12-01T00:00:00Z')
  })

  it('两个 expires 都没有的行不出展开钮（`noDiff` 分支因此不可达）', async () => {
    const w = await factory()
    const rows = w.findAll('.audit-table tbody tr.audit-row')
    // id=101/103 有到期时间，102/104/105/106 没有
    const withBtn = rows.filter((r) => r.find('.btn-expand').exists()).length
    expect(withBtn).toBe(2)
    expect(rows[1]!.find('.btn-expand').exists()).toBe(false)
  })

  it('筛选栏：动作 4 选项 / 时间窗 4 选项 / 条数 4 选项 / 刷新钮', async () => {
    const w = await factory()
    const sels = w.findAll('.filter-bar select')
    expect(sels).toHaveLength(3)
    expect(sels[0]!.findAll('option').map((o) => o.text())).toEqual(['(全部)', 'insert', 'update', 'delete'])
    expect(sels[1]!.findAll('option').map((o) => o.text())).toEqual(['1 天', '7 天', '30 天', '90 天'])
    expect(sels[2]!.findAll('option').map((o) => o.text())).toEqual(['50', '200', '500', '1000'])
    expect(w.find('.filter-bar button').text()).toBe('刷新')
  })

  it('请求失败：错误文案出在筛选卡内，表格不挂载', async () => {
    ctrl.fail = true
    const w = await factory()
    expect(w.find('.error').text()).toContain('audit boom')
    expect(w.find('.audit-table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('桌面不出卡片，也不出现连续加载尾部', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(false)
  })
})

describe('RoutingAuditView：三个空档（第四种三态归属）', () => {
  it('空态：出 .empty，表壳被撤掉', async () => {
    ctrl.empty = true
    const w = await factory()
    expect(w.find('.empty').text()).toBe('没有匹配的审计记录。')
    expect(w.find('.audit-table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    // 汇总卡桌面原本就是 v-if="entries.length > 0" ⇒ 空态时不出
    expect(w.find('.summary-cards').exists()).toBe(false)
  })

  /**
   * ★ 这一档我第一版读反了（以为「首载时两档都空」），门禁当场把错读抓出来：
   *   `v-if="!loading && !entries.length"` 在 loading 时为**假** ⇒ 走 `v-else`
   *   ⇒ **出的是空表壳**（表头在、没有行），不是「什么都不出」。
   *   ⇒ 本页是三态归属的**第一种形态（表内三态）**，不是第四种。
   */
  it('首载中：走 v-else 出空表壳（表头在、无行），不出 .empty', async () => {
    ctrl.pending = true
    const w = await factory()
    expect(w.find('.empty').exists()).toBe(false)
    expect(w.find('.audit-table').exists()).toBe(true)
    expect(w.findAll('.audit-table thead th')).toHaveLength(8)
    expect(w.findAll('.audit-table tbody tr.audit-row')).toHaveLength(0)
  })

  it('首载中 compact：0 张卡的卡片列表（与桌面空表壳同形，不是加载动画）', async () => {
    mockWindowClass('compact')
    ctrl.pending = true
    const w = await factory()
    expect(w.find('.empty').exists()).toBe(false)
    expect(cardsOf(w)).toHaveLength(0)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
  })

  it('刷新中且有旧行：表格不消失（桌面现状）', async () => {
    const w = await factory()
    expect(w.findAll('.audit-table tbody tr.audit-row')).toHaveLength(6)
    ctrl.pending = true
    await w.find('.filter-bar button').trigger('click')
    await flushPromises()
    expect(w.findAll('.audit-table tbody tr.audit-row')).toHaveLength(6)
  })
})

describe('RoutingAuditView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出表；不渲染视图切换钮', async () => {
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(6)
    expect(w.find('.audit-table').exists()).toBe(false)
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  /**
   * ★ 卡头**不是**时间。第一版拿 `fmtDateTime24h(e.ts)` 当卡头，门禁当场报
   *   「6 张卡卡头全是 10/5/2026」—— 那个 helper 的 `Intl.DateTimeFormat`
   *   没有任何日期/时间选项，按规范默认**只输出年月日，没有时分**。
   *   审计行常在同一天 ⇒ 6 个卡头全同，compact 直接不可用。
   *   共享 helper 有 27 个调用点、跨 10 页 ⇒ 不在本切片改，已登记为存量缺陷。
   *   ⇒ 卡头改用这一行的**唯一句柄**：`#覆盖ID`，没有覆盖 ID 时退回动作译名。
   */
  it('卡头是这一行的唯一句柄：有覆盖 ID 出 #N，没有时退回动作译名', async () => {
    const w = await factory()
    const head = (id: number) => cardById(w, id).find('.card__title').text()
    expect(head(101)).toBe('#42')
    expect(head(102)).toBe('#7')
    expect(head(105)).toBe('#9')
    // 103 / 104 没有 override_id ⇒ 退回动作译名
    expect(head(103)).toBe('更新')
    expect(head(104)).toBe('新增')
  })

  it('时间仍是字段，且不出裸 ISO 串（与表格同一份 fmtDateTime24h）', async () => {
    const w = await factory()
    // ★ 比「同一个函数的结果」而不是字面量：该 helper 读 app 级 `localeRef`
    //   与本地时区，比字面量就是「永远红」的那类判据。
    expect(fieldValue(cardById(w, 101), '时间').text()).toBe(fmtDateTime24h(ENTRIES[0]!.ts))
    expect(cardById(w, 101).text()).not.toContain('2026-10-05T01:15:30.000Z')
  })

  it('动作是带色徽章，tone 逐行（insert=good / update=neutral / delete=danger）', async () => {
    const w = await factory()
    const tone = (id: number) => fieldValue(cardById(w, id), '动作')
    expect(tone(101).text()).toBe('新增')
    expect(tone(101).attributes('data-tone')).toBe('good')
    expect(tone(102).text()).toBe('删除')
    expect(tone(102).attributes('data-tone')).toBe('danger')
    expect(tone(103).text()).toBe('更新')
    expect(tone(103).attributes('data-tone')).toBe('neutral')
  })

  /**
   * ★ 覆盖 ID 已经是**卡头**，再列成一个字段就是同一行字出现两次。
   *   桌面那格还是链接，卡片侧由 `#actions` 的「详情」链接承担同一意图。
   */
  it('卡头已是覆盖 ID ⇒ 字段里不再出现「覆盖」这一行（不重复展示）', async () => {
    const w = await factory()
    for (const c of cardsOf(w)) {
      const labels = c.findAll('.card__field').map((f) => f.find('dt').text())
      expect(labels, `卡片里不该再有「覆盖」字段：${labels.join('/')}`).not.toContain('覆盖')
    }
    expect(cardsOf(w)[0]!.findAll('.card__field')).toHaveLength(6)
  })

  it('任务 / Profile / 模式合并成一个字段（三段 / 一段 / 全缺）', async () => {
    const w = await factory()
    expect(fieldValue(cardById(w, 101), '任务 / Profile / 模式').text()).toBe('chat · default · auto')
    expect(fieldValue(cardById(w, 103), '任务 / Profile / 模式').text()).toBe('vision')
    expect(fieldValue(cardById(w, 102), '任务 / Profile / 模式').text()).toBe('—')
  })

  it('模型字段与表格同一份 shortModel（含缺值破折号）', async () => {
    const w = await factory()
    expect(fieldValue(cardById(w, 101), '模型').text()).toBe('gpt-4o-2024-08-...')
    expect(fieldValue(cardById(w, 104), '模型').text()).toBe('m12345678901234567')
    expect(fieldValue(cardById(w, 105), '模型').text()).toBe('m12345678901234...')
    expect(fieldValue(cardById(w, 103), '模型').text()).toBe('—')
  })

  it('原因 / 操作者：null 出破折号（不是 "null" 字面量）', async () => {
    const w = await factory()
    expect(fieldValue(cardById(w, 102), '原因').text()).toBe('—')
    expect(fieldValue(cardById(w, 102), '操作者').text()).toBe('—')
    expect(fieldValue(cardById(w, 101), '原因').text()).toBe('封禁违规模型')
  })

  it('卡片共 6 个字段：动作 / 时间 / 任务三合一 / 模型 / 原因 / 操作者', async () => {
    const w = await factory()
    const labels = cardsOf(w)[0]!.findAll('.card__field').map((f) => f.find('dt').text())
    expect(labels).toEqual(['动作', '时间', '任务 / Profile / 模式', '模型', '原因', '操作者'])
  })

  it('#actions：有 override_id 出「详情」链接，无则不出', async () => {
    const w = await factory()
    const linkOf = (id: number) => cardById(w, id).findComponent(RouterLinkStub)
    expect(linkOf(101).props('to')).toBe('/routing/overrides#42')
    expect(linkOf(101).text()).toBe('详情')
    expect(linkOf(103).exists(), 'override_id 为 null 的行不该有链接').toBe(false)
  })

  it('#actions：展开钮与桌面同一条件，展开后 diff 同样两行', async () => {
    const w = await factory()
    expect(cardById(w, 101).find('.btn-expand').exists()).toBe(true)
    expect(cardById(w, 102).find('.btn-expand').exists()).toBe(false)
    await cardById(w, 101).find('.btn-expand').trigger('click')
    await flushPromises()
    const diff = cardById(w, 101).find('.diff')
    expect(diff.exists()).toBe(true)
    expect(diff.findAll('.diff-field')).toHaveLength(2)
    expect(diff.text()).toContain('2026-11-01T00:00:00Z')
  })

  it('每张卡的动作数不超过 2（链接 + 展开钮）', async () => {
    const w = await factory()
    for (const c of cardsOf(w)) {
      // ★ VTU 2.4 的 DOMWrapper 没有 children()，按元素类型数（.diff 是展开态内容，不算动作）
      const acts = c.findAll('.card__actions a, .card__actions button')
      expect(acts.length, `卡片动作过多：${acts.map((a) => a.text()).join('/')}`).toBeLessThanOrEqual(2)
    }
  })

  /**
   * ★ `CardList` 的 `:key` 取自 `titleKey`（`keyOf`）。桌面那一栏是**动作**
   *   （insert/update/delete 只有三个取值）—— 真拿它当键就有 3 组重复键。
   *   静态契约由「title-key 是唯一主键 id」那条断；这里断**渲染出来的卡头**，
   *   因为它同时是「这 6 张卡在屏上能分辨」的可用性底线。
   */
  it('6 张卡的卡头两两不同（否则同屏分不出哪张是哪条）', async () => {
    const w = await factory()
    const titles = cardsOf(w).map((c) => c.find('.card__title').text())
    expect(titles).toHaveLength(6)
    expect(new Set(titles).size, `卡头撞车了：${titles.join(' / ')}`).toBe(6)
  })

  it('卡片里不出现 undefined / null 字面量', async () => {
    const w = await factory()
    for (const c of cardsOf(w)) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })

  it('筛选在卡片侧同样生效（动作筛选出 3 条 insert）', async () => {
    const w = await factory()
    await w.findAll('.filter-bar select')[0]!.setValue('insert')
    await flushPromises()
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(3)
  })
})

describe('RoutingAuditView：跨层契约', () => {
  it('第四种三态归属：v-else 挂在容器上，桌面空态排在容器之前', () => {
    const rdvAt = codeOnly.indexOf('<ResponsiveDataView')
    expect(rdvAt, '没找到 <ResponsiveDataView —— 下面的断言会变成查空串').toBeGreaterThan(-1)
    const tag = codeOnly.slice(rdvAt, codeOnly.indexOf('>', rdvAt) + 1)
    expect(tag, 'v-else 必须挂在容器上（空态与首载都撤掉整块）').toContain('v-else')
    const emptyAt = codeOnly.indexOf('v-if="!loading && entries.length === 0"')
    expect(emptyAt, '桌面空态必须存在').toBeGreaterThan(-1)
    expect(emptyAt, '空态分支必须排在 v-else 链之前').toBeLessThan(rdvAt)
  })

  it('不传 :empty 也不传 :loading（容器不挂载，传了是死代码）', () => {
    expect(codeOnly).not.toMatch(/<ResponsiveDataView[\s\S]{0,400}?\s:empty=/)
    expect(codeOnly).not.toMatch(/<ResponsiveDataView[\s\S]{0,400}?\s:loading=/)
  })

  it('table-min-width 传 0px（.audit-table 本页没有 min-width，传默认 720 会凭空出横滚）', () => {
    expect(codeOnly).toContain('table-min-width="0px"')
    expect(codeOnly).toMatch(/\.audit-table\s*\{[^}]*\}/)
    expect(codeOnly).not.toMatch(/\.audit-table\s*\{[^}]*min-width/)
  })

  /**
   * ★ `CardList` 的 `:key` 取自 `titleKey`（`keyOf`）。`action` 只有三个取值 ——
   *   拿它当键，同一列表里**所有行都是重复键**，Vue 告警且复用整片 DOM。
   *   `AuditLogView` 现在用的正是 `title-key="action"`（已单独登记）。
   *   这里必须钉住「键用后端主键、脸用 titleFormat」。
   */
  it('title-key 是唯一主键 id（不是 action —— action 三个取值不唯一）', () => {
    expect(codeOnly).toContain('title-key="id"')
    expect(codeOnly).not.toContain('title-key="action"')
    expect(codeOnly).toContain(':title-format="cardTitle"')
  })

  it('卡片里两个动作的触控目标都 ≥48px（Android 控件基线）', () => {
    // ★ 补这条是因为变异 M24 无牙：撤掉 `.card-link` 的 `min-height: 48px` 全绿。
    //   桌面 `.btn-expand` 是 24×24 的小方钮（贴着表格行），**直接搬到卡片上不够 48px**
    //   —— 卡片的展开钮由 `CardList` 的 `.card__actions :deep(button)` 兜住，
    //   但它是**页面自己**的 CSS，只能由本页门禁守。
    expect(codeOnly).toMatch(/\.card-link\s*\{[^}]*min-height:\s*48px;/)
  })

  it('本页不引入连续加载（getRoutingAudit 一次取回整段）', () => {
    expect(codeOnly).not.toContain('HyperLoadMore')
    expect(codeOnly).not.toContain('createHyperPages')
  })

  it('表格与卡片共用同一份格式化/译名函数（不写第二份）', () => {
    expect((codeOnly.match(/function shortModel\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function actionLabel\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function actionTone\(/g) ?? []).length).toBe(1)
    expect((codeOnly.match(/function cardTitle\(/g) ?? []).length).toBe(1)
    // 表格单元格与卡片字段都调它们
    expect(codeOnly).toContain('{{ shortModel(e.model_chosen) }}')
    expect(codeOnly).toContain('format: (v) => shortModel(v == null ? undefined : String(v))')
    expect(codeOnly).toContain('{{ actionLabel(e.action) }}')
    expect(codeOnly).toContain('format: (v) => actionLabel(String(v ?? \'\')) || null')
    // 时间只有一份格式化，表格与卡片字段共用
    expect(codeOnly).toContain('{{ fmtDateTime24h(e.ts) }}')
    expect(codeOnly).toContain('format: (v) => (v == null ? null : fmtDateTime24h(String(v)))')
  })

  it('卡片字段标签全部复用既有表头词条（本次切片 0 新增 i18n 键）', () => {
    const labels = [...codeOnly.matchAll(/t\('routingAudit\.table\.headers\.(\w+)'\)/g)].map((m) => m[1])
    // 表头 7 个 + 卡片 6 个 = 13 次引用（覆盖只出现在表头，卡片侧折进卡头）
    expect(labels.length).toBe(13)
    expect(new Set(labels)).toEqual(
      new Set(['when', 'action', 'override', 'taskProfileMode', 'model', 'reason', 'actor']),
    )
    // 链接标签用的是此前从未被引用的 details 键
    expect(codeOnly).toContain("t('routingAudit.table.details')")
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })

  it('没有新增请求端点（仍走 api 层的 getRoutingAudit）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
    expect(codeOnly).toContain('getRoutingAudit')
  })
})
