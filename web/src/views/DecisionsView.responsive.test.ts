// DecisionsView.responsive.test.ts — H6 第十一条垂直切片的门禁。
//
// 本页形状与第七~十条**不同**：它**有**分页 API（`getDecisions` 收 limit+offset、回 total），
// 所以 compact 走**连续加载**（createHyperPages + HyperLoadMore），桌面的上下页码保持原样。
//
// 三态归属是**第一种形态（表内三态）**：桌面空态是 tbody 里的一行 `<td colspan="13">`，
// 而 `<table>` **永远渲染**（没有 v-if），加载提示是表格**下面**的独立一行。
// ⇒ `:loading` / `:empty` 必须带 `isCompact` 前缀，否则会改桌面的观感。
//
// 四条与前几条不同、且都在代码注释里写了理由的取舍：
// 1. `pageSize` 与 `fetchPage` 发出的 `limit` 必须是**同一个常量**（13 §7：短页判据读配置那份）
// 2. 桌面那个「每页条数」选择器在 compact 下**藏起来** —— 显示却不用是谎
// 3. 5s 自动刷新在 compact 下**只在还停在第 1 页时**才刷（否则把用户读的历史抽走）
// 4. `rowKey` 用 `request_id + ts`（对 13 §7「不要用时间戳」字面要求的有意偏离：重试会产生新决策行）
import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import DecisionsView from './DecisionsView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/views/DecisionsView.vue'), 'utf8')
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

const { ctrl, getDecisions } = vi.hoisted(() => {
  // ★ total 用 null 当「未设置」哨兵：用 0 会被 `0 ?? ALL.length` 判成 0
  //   （`??` 只接 null/undefined），total 恒为 0 ⇒ 页码条与「继续加载」全被干掉。
  const ctrl = { fail: false, failFirst: false, empty: false, total: null as number | null, calls: [] as Record<string, unknown>[] }
  const getDecisions = vi.fn()
  return { ctrl, getDecisions }
})

/** compact 每页 25 条（与 `COMPACT_PAGE_SIZE` 同源）。 */
const PAGE = 25

/**
 * 28 行：前 25 行是「满页」（`hasMore` 靠它保持 true，才能点「继续加载」），
 * 后 3 行是「短页」（第 2 页取回后 `hasMore` 转 false ⇒ 出现「已全部加载」）。
 * 前 3 行承载全部需要逐条断言的分支。
 */
function mkRow(i: number) {
  const base = {
    ts: `2026-10-05T0${(i % 9) + 1}:15:30.000Z`,
    request_id: `req-${i}`,
    idempotency_key: i === 0 ? null : `idem-${i}`,
    tenant_id: 'default',
    api_key_id: null,
    model: `model-${i}`,
    chosen_credential_id: i === 0 ? null : 7,
    chosen_provider_id: i === 0 ? null : 3,
    tier: i === 0 ? null : 1,
    candidates_tried: 2,
    latency_ms: i === 1 ? null : 120 + i,
    success: i !== 1,
    error_class: i === 1 ? 'UpstreamError' : null,
    prompt_tokens: i === 2 ? null : 100 + i,
    // ★ i===3 只有 completion 缺 —— 合并字段的「半缺」分支必须有 fixture 覆盖，
    //   否则「两项都缺才回 null」那条变异是行为等价的（台账上会是一条无牙）。
    completion_tokens: i === 2 || i === 3 ? null : 20 + i,
    cost_usd: i === 0 ? null : 0.0123456,
    request_bytes: 100,
    response_bytes: 200,
    client_model: `client-${i}`,
    resolved_raw_model: `raw-${i}`,
    outbound_model: i === 1 ? null : `outbound-${i}`,
    sticky_hit: false,
    client_profile: null,
    request_mode: null,
    identity_hash: null,
    transform_rule_id: null,
    egress_protocol: 'http',
    failure_stage: i === 1 ? 'upstream' : null,
    failure_detail_code: i === 1 ? 'E_UPSTREAM' : null,
    resolution_path: i === 1 ? null : 'exact',
    canonical_model: i === 1 ? null : `canon-${i}`,
    resolution_raw_models: i === 2 ? [] : [`raw-${i}`, `raw-${i}-alias`],
    decision_trace: {
      planned_candidates: i === 2 ? [] : [{ provider_id: 3, credential_id: 7, reason: 'ok' }],
      blocked_candidates: [],
    },
  }
  return base
}

/**
 * ★ 51 行不是随手取的：第 2 页必须是**满页**（25 行），
 *   2×25=50 < 51 ⇒ `hasMore` 仍为 true、state 仍是 `idle`。
 *   第一版用 28 行，第 2 页是短页 ⇒ state 变 `exhausted` ⇒
 *   **「state 守卫」把「翻过页守卫」挡住了**，M21 那条变异因此无牙。
 *   两个守卫必须能互相独立地被抓到，所以 fixture 要让它们不互相遮蔽。
 */
const ALL = Array.from({ length: 51 }, (_, i) => mkRow(i))

vi.mock('../api', () => ({
  getDecisions: (params: Record<string, unknown> = {}) => {
    ctrl.calls.push({ ...params })
    if (ctrl.fail) return Promise.reject(new Error('decisions boom'))
    if (ctrl.failFirst && ctrl.calls.length === 1) return Promise.reject(new Error('first boom'))
    if (ctrl.empty) return Promise.resolve({ decisions: [], total: 0 })
    const limit = Number(params.limit ?? 50)
    const offset = Number(params.offset ?? 0)
    const total = ctrl.total ?? ALL.length
    // 返回的行数也受 total 约束：只改 total 不裁行会得到「声称 3 条却回 25 行」的自相矛盾
    const src = ALL.slice(0, total)
    return Promise.resolve({ decisions: src.slice(offset, offset + limit), total })
  },
}))

vi.mock('../components/ModelPicker.vue', () => ({
  default: {
    props: ['modelValue', 'placeholder', 'title'],
    emits: ['update:modelValue'],
    template: '<input class="model-picker-stub" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
  },
}))

vi.mock('../composables/useCredentialLabels', () => ({
  credentialDisplayName: (id: number | null | undefined) => (id == null ? '—' : `凭据 #${id}`),
  useCredentialLabels: () => ({ labelRevision: { value: 0 } }),
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      decisions: {
        dash: '—', msUnit: 'ms', costUnit: '$', bytesUnit: 'B', latencyUnit: 'ms',
        stickyHitOk: '命中', stickyHitNo: '未命中', successOk: '成功', successFail: '失败',
      },
      decisionsView: {
        title: '路由决策日志', autoRefresh: '每 5 秒自动刷新',
        filter: {
          status: '状态', statusAll: '全部', statusSuccess: '成功', statusFailed: '失败',
          timeRange: '时间范围', time10m: '10分钟', time30m: '30分钟', time1h: '1小时',
          time6h: '6小时', time24h: '24小时',
          limit: '条数', limit20: '20条', limit50: '50条', limit100: '100条', limit200: '200条',
          refresh: '刷新', totalCount: '共 {n} 条', modelLabel: '模型（可选）',
          modelPlaceholder: '选择模型…', modelTitle: '筛选路由决策模型',
        },
        pagination: { summary: '共 {total} 条，当前 {start} - {end}', prev: '← 上一页', next: '下一页 →' },
        table: {
          time: '时间', status: '状态', model: '模型', interpretation: '解析', usage: 'Usage',
          latency: '延迟', provider: '供应商', outboundModel: '出站模型', cost: '费用',
          candidateChain: '候选链', blockReason: '拦截原因', error: '错误',
          loading: '加载中…', noData: '暂无决策记录',
        },
        loading: '加载中…',
        detail: {
          title: '决策详情', close: '✕ 关闭', basicInfo: '基本信息', time: '时间', status: '状态',
          latency: '延迟', clientModel: '客户端模型', outboundModel: '出站模型', protocol: '协议',
          modelResolution: '模型解析', routingDecision: '路由决策', providerId: '供应商 ID',
          credentialId: '凭据 ID', candidatesCount: '候选数', usage: 'Usage', costCalc: '费用计算',
          trace: '决策跟踪', errorInfo: '错误信息',
        },
      },
      hyper: {
        dataView: { table: '表格', cards: '卡片' },
        list: {
          empty: '暂无数据', allLoaded: '已全部加载 {count} 条', loadFailed: '加载失败，点击重试',
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

async function factory() {
  // 抽屉在 <Teleport to="body"> 里，wrapper.find 看不到；沿用本仓既有写法（RequestJourneyQueues）
  const w = mount(DecisionsView, { global: { plugins: [i18n], stubs: { Teleport: true } } })
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

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.fail = false
  ctrl.failFirst = false
  ctrl.empty = false
  ctrl.total = null
  ctrl.calls = []
  getDecisions.mockClear()
  mockWindowClass('expanded')
  localStorage.clear()
})

// 本页挂载即排一个 5s 真实 interval（自动刷新）。不卸载就是 §2.1 那条病灶的形状。
enableAutoUnmount(afterEach)
afterEach(() => {
  vi.useRealTimers()
  vi.clearAllMocks()
})

describe('DecisionsView：桌面决策流水表零回归', () => {
  it('14 列表头，文案与顺序不变', async () => {
    const w = await factory()
    const ths = w.findAll('.data-table thead th')
    expect(ths).toHaveLength(14)
    expect(ths.map((th) => th.text())).toEqual([
      '时间', '状态', '模型', '解析', 'Tier', '延迟', '供应商', '出站模型',
      'prompt_t', 'comp_t', '费用', '候选链', '拦截原因', '错误',
    ])
  })

  it('状态徽章两支 class 与译名（成功 / 失败）', async () => {
    const w = await factory()
    const rows = w.findAll('.data-table tbody tr')
    expect(rows[0]!.find('.badge-ok').text()).toBe('命中')
    expect(rows[1]!.find('.badge-err').text()).toBe('未命中')
    expect(rows[1]!.classes()).toContain('row-fail')
    expect(rows[0]!.classes()).not.toContain('row-fail')
  })

  it('解析列两行：path / canonical + raw_models 逗号串', async () => {
    const w = await factory()
    const cells = w.findAll('.data-table tbody tr').map((tr) => tr.findAll('td')[3]!.text())
    expect(cells[0]).toBe('exact / canon-0raw-0, raw-0-alias')
    expect(cells[1]).toBe('— / —raw-1, raw-1-alias')   // path 与 canonical 都是 null
    expect(cells[2]).toBe('exact / canon-2—')            // raw_models 空数组 ⇒ 破折号
  })

  it('tier / 延迟 / 供应商 / 出站模型：缺值回落破折号，延迟带 ms 单位', async () => {
    const w = await factory()
    const rows = w.findAll('.data-table tbody tr')
    const td = (i: number, n: number) => rows[i]!.findAll('td')[n]!.text()
    expect(td(0, 4)).toBe('—')          // tier null
    expect(td(0, 6)).toBe('—')          // provider null
    expect(td(0, 7)).toBe('outbound-0')  // ★ outbound 的空值行是第 1 行，不是第 0 行
    expect(td(1, 5)).toBe('—')          // latency 的空值行同样是第 1 行
    expect(td(1, 7)).toBe('—')          // outbound 同理
    expect(td(0, 5)).toBe('120ms')      // 第 0 行 latency = 120 + 0
    expect(td(2, 4)).toBe('1')          // tier 有值
    expect(td(2, 5)).toBe('122ms')      // latency 120+2
    expect(td(2, 6)).toBe('3')
    expect(td(2, 7)).toBe('outbound-2')
  })

  it('token 两列与费用 5 位小数（缺值破折号）', async () => {
    const w = await factory()
    const rows = w.findAll('.data-table tbody tr')
    const td = (i: number, n: number) => rows[i]!.findAll('td')[n]!.text()
    expect(td(2, 8)).toBe('—')                       // prompt_tokens null
    expect(td(2, 9)).toBe('—')                       // completion_tokens null
    expect(td(0, 10)).toBe('—')                      // ★ cost 的空值行是第 0 行
    expect(td(0, 8)).toBe('100')
    expect(td(0, 9)).toBe('20')
    expect(td(2, 10)).toBe('$0.01235')
    // ★ 5 位不是 6 位：抽屉里是 toFixed(6)，表格是 toFixed(5)，两处本来就不同
  })

  it('候选链 / 拦截链走 traceList（对象数组拼接；空数组出破折号）', async () => {
    const w = await factory()
    const rows = w.findAll('.data-table tbody tr')
    expect(rows[0]!.findAll('td')[11]!.text()).toBe('p3/凭据 #7 ok')
    expect(rows[0]!.findAll('td')[12]!.text()).toBe('—')
    // 第 3 行 planned_candidates 是空数组
    expect(rows[2]!.findAll('td')[11]!.text()).toBe('—')
  })

  it('错误列回落链：failure_detail_code → error_class → 空串', async () => {
    const w = await factory()
    const rows = w.findAll('.data-table tbody tr')
    expect(rows[1]!.findAll('td')[13]!.text()).toBe('E_UPSTREAM')
    expect(rows[0]!.findAll('td')[13]!.text()).toBe('')
  })

  it('行可点：打开决策详情抽屉，含全量 decision_trace JSON', async () => {
    const w = await factory()
    await w.findAll('.data-table tbody tr')[1]!.trigger('click')
    await flushPromises()
    const panel = w.find('.drawer-panel')
    expect(panel.exists()).toBe(true)
    expect(panel.text()).toContain('决策详情')
    expect(panel.find('.trace-json').text()).toContain('"planned_candidates"')
    await panel.find('button').trigger('click')
    await flushPromises()
    expect(w.find('.drawer-panel').exists()).toBe(false)
  })

  it('筛选栏：状态 3 选项 / 时间窗 5 选项 / 条数 4 选项 / 模型选择器 / 刷新钮', async () => {
    const w = await factory()
    const sels = w.findAll('.cf-select')
    expect(sels[0]!.findAll('option').map((o) => o.text())).toEqual(['全部', '成功', '失败'])
    expect(sels[1]!.findAll('option').map((o) => o.text())).toEqual(['10分钟', '30分钟', '1小时', '6小时', '24小时'])
    expect(sels[2]!.findAll('option').map((o) => o.text())).toEqual(['20条', '50条', '100条', '200条'])
    expect(w.find('.model-picker-stub').exists()).toBe(true)
    expect(w.find('.cf-row button').text()).toBe('刷新')
  })

  it('上下两个页码条都在；首屏时上页禁用、下页可用（total 51 > limit 50）', async () => {
    const w = await factory()
    // ★ 按**按钮文案**定位，不按「恰好两个 button 的 .card」—— 后者会连别的卡片一起吃进来
    //   （第一版就是这么数出 3 的：红的是判据）。
    const pagerButtons = (t: string) => w.findAll('button').filter((b) => b.text().includes(t))
    const prevs = pagerButtons('上一页')
    const nexts = pagerButtons('下一页')
    expect(prevs).toHaveLength(2)
    expect(nexts).toHaveLength(2)
    for (const b of prevs) {
      expect(b.attributes('disabled'), 'offset=0 ⇒ 上页必须禁用').toBeDefined()
    }
    for (const b of nexts) {
      expect(b.attributes('disabled'), 'offset 0 + limit 50 < total 51 ⇒ 下页应可点').toBeUndefined()
    }
    expect(w.text()).toContain('共 51 条')
    // 点下一页真的翻到第 2 页（offset 0 → 50）
    await nexts[0]!.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(ctrl.calls.at(-1)).toMatchObject({ offset: 50, limit: 50 })
    expect(w.findAll('.data-table tbody tr')).toHaveLength(1)
    expect(pagerButtons('上一页')[0]!.attributes('disabled')).toBeUndefined()
    expect(pagerButtons('下一页')[0]!.attributes('disabled')).toBeDefined()
  })

  it('自动刷新勾选框存在且默认勾上', async () => {
    const w = await factory()
    const cb = w.find('input[type="checkbox"]')
    expect((cb.element as HTMLInputElement).checked).toBe(true)
    expect(w.text()).toContain('每 5 秒自动刷新')
  })

  it('桌面不出卡片、不出连续加载尾部', async () => {
    const w = await factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(false)
  })
})

describe('DecisionsView：空态 / 加载态（第一种三态归属）', () => {
  it('有数据时 tbody 里不出空态行', async () => {
    const w = await factory()
    expect(w.text()).not.toContain('暂无决策记录')
  })

  it('空态：无行且非加载 ⇒ tbody 里出「暂无决策记录」行', async () => {
    ctrl.empty = true
    const w = await factory()
    const empty = w.findAll('.data-table tbody tr').filter((tr) => tr.text() === '暂无决策记录')
    expect(empty).toHaveLength(1)
    // 空态时汇总条也不出（`v-if="showPager"` 要求 total > 0）
    expect(w.findAll('button').filter((b) => /上一页|下一页/.test(b.text()))).toHaveLength(0)
  })

  /**
   * ★ 存量 off-by-one：**表头是 14 列，空态行却写 `colspan="13"`**。
   *   本切片**不改桌面**，但把当前值钉住 —— 将来修它必须是一次可见的桌面变更。
   */
  it('【存量缺陷】空态行的 colspan 是 13，而表头是 14 列', async () => {
    expect(codeOnly).toMatch(/<td colspan="13"/)
    const thCount = (codeOnly.match(/<th>/g) ?? []).length
    expect(thCount, '表头列数变了，这条 off-by-one 的说明也要改').toBe(14)
  })

  it('compact 无数据时走 EmptyState，不出空表壳', async () => {
    mockWindowClass('compact')
    ctrl.empty = true
    const w = await factory()
    expect(w.find('.data-table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.find('.app-empty-state').exists()).toBe(true)
    expect(w.find('.app-empty-state').text()).toBe('暂无决策记录')
  })

  it('首屏失败：出错误横幅且**不出空态**（13 §7「失败态不显示空态」）', async () => {
    mockWindowClass('compact')
    ctrl.failFirst = true
    const w = await factory()
    expect(w.find('.error-banner').text()).toContain('first boom')
    expect(w.find('.app-empty-state').exists(), '失败态不得显示空态').toBe(false)
    expect(w.find('.hyper-load-more').exists()).toBe(true)
  })
})

describe('DecisionsView：compact 卡片形态', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('出卡片不出表；无页码条；无「每页条数」选择器；出继续加载', async () => {
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(PAGE)
    expect(w.find('.data-table').exists()).toBe(false)
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
    // 桌面的两个页码条在 compact 下都撤掉
    expect(w.findAll('button').filter((b) => /上一页|下一页/.test(b.text()))).toHaveLength(0)
    // ★ 「每页条数」是页码语义的控件，连续加载里没有「每页」⇒ 不显示（显示却不用是谎）
    expect(w.findAll('.cf-select')).toHaveLength(2)
    expect(w.find('.hyper-load-more').exists()).toBe(true)
  })

  it('卡头是时间（与表格同一份 fmtTs），8 个字段', async () => {
    const w = await factory()
    const card = cardsOf(w)[0]!
    expect(card.find('.card__title').text()).toMatch(/^\d{1,2}:\d{2}:\d{2}$/)
    expect(card.text()).not.toContain('2026-10-05T01:15:30.000Z')
    expect(card.findAll('.card__field')).toHaveLength(8)
    expect(card.findAll('.card__field').map((f) => f.find('dt').text())).toEqual([
      '状态', '模型', '模型解析', '延迟', '供应商', '出站模型', 'Usage', '费用',
    ])
  })

  it('状态是带色徽章，tone 逐行（good / danger）', async () => {
    const w = await factory()
    const cards = cardsOf(w)
    expect(fieldValue(cards[0]!, '状态').text()).toBe('命中')
    expect(fieldValue(cards[0]!, '状态').attributes('data-tone')).toBe('good')
    expect(fieldValue(cards[1]!, '状态').text()).toBe('未命中')
    expect(fieldValue(cards[1]!, '状态').attributes('data-tone')).toBe('danger')
  })

  it('解析字段把两段合成一段；缺值回落破折号', async () => {
    const w = await factory()
    const cards = cardsOf(w)
    expect(fieldValue(cards[0]!, '模型解析').text()).toBe('exact / canon-0 · raw-0, raw-0-alias')
    expect(fieldValue(cards[1]!, '模型解析').text()).toBe('— / — · raw-1, raw-1-alias')
    expect(fieldValue(cards[2]!, '模型解析').text()).toBe('exact / canon-2 · —')
  })

  it('延迟带 ms、费用 5 位小数、token 两列合并（缺值破折号）', async () => {
    const w = await factory()
    const cards = cardsOf(w)
    expect(fieldValue(cards[1]!, '延迟').text()).toBe('—')   // 空值行是第 1 张
    expect(fieldValue(cards[0]!, '延迟').text()).toBe('120ms')
    expect(fieldValue(cards[2]!, '延迟').text()).toBe('122ms')
    expect(fieldValue(cards[0]!, '费用').text()).toBe('—')    // 空值行是第 0 张
    expect(fieldValue(cards[2]!, '费用').text()).toBe('$0.01235')
    // 两项都缺 ⇒ 单个破折号（format 返 null，CardList 渲染成 —），不是 '— / —'
    expect(fieldValue(cards[2]!, 'Usage').text()).toBe('—')
    expect(fieldValue(cards[0]!, 'Usage').text()).toBe('100 / 20')
    // ★ 只缺 completion ⇒ 保留斜杠，另一侧照常显示（「— / —」会把「有一个数」也吃掉）
    expect(fieldValue(cards[3]!, 'Usage').text()).toBe('103 / —')
  })

  it('整卡可点的可访问名绑定到既有词条（变异 M20 无牙后补的）', () => {
    // CardList 在 clickableLabel 为空时会回落到卡头文本（这里是时间），
    // 行为退化不明显 ⇒ 补一条源码门禁钉住「绑的是一个真实词条」。
    expect(codeOnly).toContain(`:clickable-label="t('decisionsView.detail.title')"`)
  })

  it('整卡可点：打开同一个详情抽屉', async () => {
    const w = await factory()
    await cardsOf(w)[1]!.find('.card__head').trigger('click')
    await flushPromises()
    expect(w.find('.drawer-panel').exists()).toBe(true)
    expect(w.find('.drawer-panel .trace-json').text()).toContain('"planned_candidates"')
  })

  it('点「继续加载」逐页累积；最后一页短 ⇒ 转「已全部加载」', async () => {
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(PAGE)
    await w.find('.hyper-load-more__btn').trigger('click')
    await flushPromises()
    await flushPromises()
    // 第 2 页是满页 ⇒ 还能继续（不是 exhausted）
    expect(cardsOf(w)).toHaveLength(PAGE * 2)
    expect(w.find('.hyper-load-more').text()).not.toContain('已全部加载')
    await w.find('.hyper-load-more__btn').trigger('click')
    await flushPromises()
    await flushPromises()
    // 第 3 页只剩 1 行 ⇒ 短页 ⇒ 到头
    expect(cardsOf(w)).toHaveLength(ALL.length)
    expect(w.find('.hyper-load-more').text()).toContain('已全部加载')
  })

  it('卡片里不出现 undefined / null 字面量', async () => {
    const w = await factory()
    for (const c of cardsOf(w)) {
      expect(c.text()).not.toContain('undefined')
      expect(c.text()).not.toContain('null')
    }
  })

  it('筛选在卡片侧同样生效（筛成功只剩 27 行里成功的那些）', async () => {
    const w = await factory()
    await w.findAll('.cf-select')[0]!.setValue('true')
    await flushPromises()
    await flushPromises()
    expect(ctrl.calls.at(-1)).toMatchObject({ success: true, offset: 0 })
  })
})

describe('DecisionsView：5s 自动刷新（compact 与累积页的冲突）', () => {
  it('桌面：5s 后重新取数', async () => {
    vi.useFakeTimers()
    const w = await factory()
    const before = ctrl.calls.length
    await vi.advanceTimersByTimeAsync(5000)
    for (let i = 0; i < 10 && ctrl.calls.length === before; i++) {
      await vi.advanceTimersByTimeAsync(0)
    }
    expect(ctrl.calls.length, '桌面自动刷新没触发').toBeGreaterThan(before)
    void w
  })

  it('compact 且还停在第 1 页：5s 后刷新头部', async () => {
    mockWindowClass('compact')
    vi.useFakeTimers()
    await factory()
    const before = ctrl.calls.length
    await vi.advanceTimersByTimeAsync(5000)
    for (let i = 0; i < 10 && ctrl.calls.length === before; i++) {
      await vi.advanceTimersByTimeAsync(0)
    }
    expect(ctrl.calls.length, 'compact 第 1 页应被自动刷新').toBeGreaterThan(before)
    expect(ctrl.calls.at(-1)).toMatchObject({ limit: PAGE, offset: 0 })
  })

  /**
   * ★ 守卫一（翻过页）：翻一页后 state **仍是 idle**（第 2 页是满页），
   *   所以这一条只可能被 `loadedPages > 1` 那个守卫挡住。
   *   第一版把两个守卫写进同一条测试、fixture 又让第 2 页是短页（state=exhausted）
   *   ⇒ 「state 守卫」把「翻过页守卫」遮住，变异 M21 无牙。
   */
  it('★ 守卫一：compact 已翻过页（state 仍 idle）⇒ 连拍 3 拍（15s）不再发请求', async () => {
    mockWindowClass('compact')
    vi.useFakeTimers()
    const w = await factory()
    await w.find('.hyper-load-more__btn').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(cardsOf(w)).toHaveLength(PAGE * 2)
    // 前置自证：此刻 state 不是 exhausted，否则这条测的不是目标守卫
    expect(w.find('.hyper-load-more').text()).not.toContain('已全部加载')
    const before = ctrl.calls.length
    for (let round = 0; round < 3; round++) {
      await vi.advanceTimersByTimeAsync(5000)
      for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(0)
    }
    expect(ctrl.calls.length, '翻过页后自动刷新把累积页抽走了').toBe(before)
    expect(cardsOf(w)).toHaveLength(PAGE * 2)
  })

  /**
   * ★ 守卫二（state）：首屏就取完（total ≤ pageSize）⇒ loadedPages 仍是 1，
   *   但 state 已是 `exhausted`。这一条只可能被 `state !== 'idle'` 那个守卫挡住。
   */
  it('★ 守卫二：compact 首屏就取完（state=exhausted）⇒ 自动刷新不再发请求', async () => {
    mockWindowClass('compact')
    ctrl.total = 3          // ≤ 25 ⇒ 第 1 页就是短页
    vi.useFakeTimers()
    const w = await factory()
    expect(cardsOf(w)).toHaveLength(3)
    expect(w.find('.hyper-load-more').text()).toContain('已全部加载')
    const before = ctrl.calls.length
    for (let round = 0; round < 3; round++) {
      await vi.advanceTimersByTimeAsync(5000)
      for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(0)
    }
    expect(ctrl.calls.length, 'exhausted 后不该再自动刷新').toBe(before)
  })

  it('关掉自动刷新：不再发请求', async () => {
    vi.useFakeTimers()
    const w = await factory()
    const cb = w.find('input[type="checkbox"]')
    await cb.setValue(false)
    const before = ctrl.calls.length
    for (let round = 0; round < 3; round++) {
      await vi.advanceTimersByTimeAsync(5000)
      for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(0)
    }
    expect(ctrl.calls.length).toBe(before)
  })
})

describe('DecisionsView：跨层契约', () => {
  it('第一种三态归属：`:loading` / `:empty` 都带 isCompact 前置（桌面像素不变）', () => {
    expect(codeOnly).toContain(':loading="isCompact && compactBusy"')
    // ★ `:empty` 必须**再**排除 failed —— 首屏失败时 rows 为空，
    //   只判 busy 会让空态与错误横幅同时出现（违反 13 §7）。
    expect(codeOnly).toContain(':empty="isCompact && !compactBusy && !compactFailed && displayRows.length === 0"')
  })

  it('空态文案复用既有词条（0 新增 i18n 键）', () => {
    expect(codeOnly).toContain(`:empty-text="t('decisionsView.table.noData')"`)
  })

  it('table-min-width 传 1500px（页面自带 min-width:1500px，传 0 会改小）', () => {
    expect(codeOnly).toContain('table-min-width="1500px"')
    expect(codeOnly).toMatch(/<table class="data-table" style="min-width:1500px">/)
  })

  it('★ `pageSize` 与 `fetchPage` 发出的 limit 同源（13 §7：短页判据读配置那份）', () => {
    expect(codeOnly).toMatch(/const COMPACT_PAGE_SIZE = 25/)
    expect(codeOnly).toContain('pageSize: COMPACT_PAGE_SIZE')
    expect(codeOnly).toContain('limit: COMPACT_PAGE_SIZE')
    // 请求 offset 必须由同一个数推出来，不能再写一份字面量
    expect(codeOnly).toContain('offset: (p - 1) * COMPACT_PAGE_SIZE')
    expect(codeOnly).not.toMatch(/offset:\s*\(p - 1\)\s*\*\s*\d/)
  })

  it('rowKey 用 request_id + ts（重试会产出新决策行，只用 request_id 会丢行）', () => {
    expect(codeOnly).toContain('rowKey: (r) => `${r.request_id}#${r.ts}`')
    // 表格自己的 :key 也要是同一个口径
    expect(codeOnly).toContain(':key="r.request_id + r.ts"')
  })

  it('fetchPage 失败继续抛出（否则 state 停在 idle，UI 会撒谎）', () => {
    const seg = codeOnly.slice(codeOnly.indexOf('fetchPage: async (p) =>'), codeOnly.indexOf('const displayRows'))
    expect(seg).toContain('throw e')
  })

  it('筛选条件单一真源：两条路径都走 filterBody()', () => {
    expect(codeOnly).toMatch(/function filterBody\(\): Record<string, unknown>/)
    // 桌面路径
    expect(codeOnly).toContain('const resp = await getDecisions({ ...filterBody(), limit: limit.value, offset: offset.value })')
    // 连续加载路径
    expect(codeOnly).toContain('const resp = await getDecisions({\n        ...filterBody(),')
    // 不许再有第二处内联拼筛选
    expect(codeOnly).not.toMatch(/qs\.set|new URLSearchParams/)
  })

  it('compact 刷新先 invalidate 再 loadFirst（顺序反了旧请求会落在新请求之后）', () => {
    const seg = codeOnly.slice(codeOnly.indexOf('async function reload()'), codeOnly.indexOf('async function tick()'))
    expect(seg).toMatch(/continuous\.invalidate\(\)[\s\S]{0,120}await continuous\.loadFirst\(\)/)
  })

  it('页码条仅桌面（showPager 带 !isCompact）', () => {
    expect(codeOnly).toMatch(/const showPager = computed\(\(\) => !isCompact\.value && total\.value > 0\)/)
    expect((codeOnly.match(/v-if="showPager"/g) ?? []).length).toBe(2)
  })

  it('「每页条数」选择器在 compact 下隐藏（显示却不用是谎）', () => {
    expect(codeOnly).toContain('<select v-if="!isCompact" v-model="limit"')
  })

  it('连续加载尾部只在 compact 出现', () => {
    expect(codeOnly).toMatch(/<HyperLoadMore\s+v-if="isCompact"/)
    expect(codeOnly).toContain('@load-more="continuous.loadNext()"')
    expect(codeOnly).toContain('@retry="continuous.retry()"')
  })

  it('没有新增请求端点（仍走 api 层的 getDecisions）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest']) {
      expect(codeOnly, `不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
  })

  it('页面没有硬编码中文文案（全部走 i18n）', () => {
    const cjk = codeOnly.match(/[一-鿿]/g)
    expect(cjk, `出现了硬编码中文：${cjk?.join('')}`).toBeNull()
  })
})
