// FreeDiscoveryView.responsive.test.ts — H6 第十七条垂直切片的门禁。
//
// 本页的形状（**首条 5 张原生表 + 3 个互斥 Tab + 2 张下钻表**）：
// 1. **无分页**：全文 `pageSize` / `offset` 一次都不出现（只有 `cursor: pointer` 的 CSS 命中）
//    ⇒ 只改呈现形态，不引入连续加载。
// 2. **两张下钻表**：T3 挂在 `v-if="selectedTask"` 下、T5 挂在 `v-if="historyDetailTask"`
//    且自带 `historyLoading` 的 `v-if`/`v-else` ⇒ 容器必须挂在同一分支上。
// 3. **`.table-wrap` 自带 `overflow-x: auto`** ⇒ 与容器嵌套 = 双横滚，已整类删除（含 CSS）；
//    而因为剩下的是**原生 `<table>`**，`:slotted(table)` 这次**真的命中**
//    ⇒ `table-min-width` 头一回不是空操作（前面那些 el-table 切片传它都无效，D12）。
// 4. **四种空态归属形态各出现两次**：T1–T4 是表内三态（`colspan` 空态行），
//    T5 **根本没有空态行** ⇒ 五张表的 `:empty` 一律带 `isCompact` 前置。
// 5. **badge 类名不带语义**：`*Class()` 只吐 CSS 类名，tone 必须从 `<style>` 里的
//    `color: var(--x)` 反查。本门禁**真的去做这个反查**，不是把映射表抄一遍。
//
// 门禁清单：
// A. 桌面零回归：三 Tab、五张表的列头与列数（8/10/8/8/4）、四张表内空态行、
//    badge 类名逐档、`x || 破折号` 四列、fmtNum 的 0、T3 表头全选
// B. 地形：`.table-wrap` 清零、容器直接子元素是 `<table>`、min-width 0px、不传 :loading
// C. badge → tone：**从 `<style>` 抽色值交叉校验映射表**
// D. compact 卡片：五段各出卡不出表、字段集与列一一对应、卡头、副标题、tone、
//    空串出 `—`、0 是合法值、`#actions` 四处
// E. 批量选择：逐行 checkbox 真的驱动 `selectedResultIds` 与导入按钮计数
// F. 空态归属 + 跨层契约
import { flushPromises, enableAutoUnmount, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import FreeDiscoveryView from './FreeDiscoveryView.vue'
import { _resetForTests } from '../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../composables/useDataViewMode'

enableAutoUnmount(afterEach)

const SOURCE = readFileSync(resolve(process.cwd(), 'src/views/FreeDiscoveryView.vue'), 'utf8')

/**
 * 保守版剥注释（与 CJK 计数器同口径）：块/HTML 注释整段去掉；行注释只去掉
 * trim 后整行以 `//` `*` `/*` 开头的。**不用「`//` 到行尾」那套正则** ——
 * 它会把反引号串里的 `//` 当成注释起点（切片十六自证时实测踩过）。
 */
const CODE_ONLY = SOURCE
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .split('\n')
  .filter((line) => !/^\s*(\/\/|\*|\/\*)/.test(line))
  .join('\n')

/** 五个容器的开标签。`:loading` / `table-min-width` 这类断言只在这五个上做。 */
const RDV_TAGS = CODE_ONLY.match(/<ResponsiveDataView[\s\S]*?>/g) ?? []

const SECTIONS = ['fd-templates', 'fd-tasks', 'fd-results', 'fd-history', 'fd-history-detail'] as const
type SectionId = (typeof SECTIONS)[number]

const TOS_CLASSES = ['tos-ok', 'tos-caution', 'tos-avoid', 'tos-ambiguous'] as const
const TASK_STATUS_CLASSES = ['st-success', 'st-failed', 'st-running', 'st-pending'] as const

// ── fixture ────────────────────────────────────────────────────────────────
// ★ 每个集合都覆盖**分档函数的每一支**，外加三个最容易漏的边界：
//   空串（`x || 破折号` 那几列）、0（`fmtNum` 明写「0 是合法值」）、未登记的枚举值。
const templates = [
  { id: 1, provider_code: 'groq', display_name: 'Groq Cloud', base_url: 'https://api.groq.com/openai/v1', api_type: 'openai-completions', api_key_env: '$GROQ_API_KEY', tos_verdict: 'ok', enabled: true, created_at: '2026-10-01T00:00:00Z' },
  // ★ api_key_env 为**空串**（keyless）⇒ 桌面出 `—`
  { id: 2, provider_code: 'cerebras', display_name: 'Cerebras', base_url: 'https://api.cerebras.ai/v1', api_type: 'openai-completions', api_key_env: '', tos_verdict: 'caution', enabled: false, created_at: '2026-10-02T00:00:00Z' },
  { id: 3, provider_code: 'unknown-vendor', display_name: 'Unknown Vendor', base_url: 'https://x.example/v1', api_type: 'anthropic', api_key_env: '$X_KEY', tos_verdict: 'avoid', enabled: true, created_at: '2026-10-03T00:00:00Z' },
  // ★ tos_verdict 落在 `tosClass` 四个显式分支之外 ⇒ 走 `return 'tos-ambiguous'` 兜底
  { id: 4, provider_code: 'mystery', display_name: 'Mystery', base_url: 'https://y.example/v1', api_type: 'anthropic', api_key_env: '$Y_KEY', tos_verdict: 'whatever', enabled: true, created_at: '2026-10-04T00:00:00Z' },
  // ★ display_name 为空串 ⇒ 卡头必须回落到 provider_code
  { id: 5, provider_code: 'nameless', display_name: '', base_url: 'https://z.example/v1', api_type: 'anthropic', api_key_env: '$Z_KEY', tos_verdict: 'ok', enabled: false, created_at: '2026-10-05T00:00:00Z' },
]

const tasks = [
  { id: 11, provider_code: 'groq', status: 'success', trigger_type: 'manual', triggered_by: 'alice', models_found: 12, models_imported: 10, error_message: '', created_at: '2026-10-06T00:00:00Z', completed_at: '2026-10-06T00:30:00Z' },
  { id: 12, provider_code: 'cerebras', status: 'failed', trigger_type: 'scheduled', triggered_by: 'bob', models_found: 0, models_imported: 0, error_message: 'boom', created_at: '2026-10-06T01:00:00Z', completed_at: null },
  // ★ `st-running` 那一档（`--primary`，CardTone 无 primary ⇒ 落 neutral）
  { id: 13, provider_code: 'groq', status: 'running', trigger_type: 'webhook', triggered_by: 'ci', models_found: 0, models_imported: 0, error_message: '', created_at: '2026-10-06T02:00:00Z', completed_at: null },
  // ★ `pending` 那一档：**桌面是 `--warning`（黄），不是 neutral** —— 最容易按名字猜错的一档
  { id: 14, provider_code: 'mystery', status: 'pending', trigger_type: 'manual', triggered_by: 'alice', models_found: 3, models_imported: 0, error_message: '', created_at: '2026-10-06T03:00:00Z', completed_at: null },
  { id: 15, provider_code: 'anthropic', status: 'success', trigger_type: 'webhook', triggered_by: 'alice', models_found: 5, models_imported: 5, error_message: '', created_at: '2026-10-06T04:00:00Z', completed_at: '2026-10-06T04:30:00Z' },
]

// ★ T3（下钻 task 11）与 T5（历史明细 task 15）**共用** `listFreeDiscoveryTaskResults`，
//   靠 taskId 区分 —— 这是读码得来的，第一版我以为有两个不同的 API。
const results = [
  { id: 101, task_id: 11, provider_code: 'groq', model_id: 'groq/llama-3-70b', display_name: 'Llama 3 70B', free_type: 'limited', monthly_tokens: 0, daily_tokens: 1000, tos_verdict: 'ok', tos_notes: '', import_status: 'pending', imported_at: null },
  // ★ display_name / free_type 都是**空串** ⇒ 桌面出 `—`，卡片必须也是 `—`（不是空白）
  { id: 102, task_id: 11, provider_code: 'groq', model_id: 'groq/llama-3-8b', display_name: '', free_type: '', monthly_tokens: 0, daily_tokens: 0, tos_verdict: 'caution', tos_notes: '', import_status: 'pending', imported_at: null },
  { id: 103, task_id: 11, provider_code: 'groq', model_id: 'groq/mixtral', display_name: 'Mixtral', free_type: 'unlimited', monthly_tokens: 0, daily_tokens: null, tos_verdict: 'avoid', tos_notes: 'x', import_status: 'imported', imported_at: '2026-10-06T05:00:00Z' },
  // ★ import_status 落 `importStatusClass` 的 else 兜底（`st-pending`）
  { id: 104, task_id: 11, provider_code: 'groq', model_id: 'groq/weird', display_name: 'Weird', free_type: 'limited', monthly_tokens: null, daily_tokens: 5, tos_verdict: 'nonsense', tos_notes: '', import_status: 'whatever', imported_at: null },
]

/** ★ T3 的另一个分支：三条**全部已导入** ⇒ `selectableResults.length === 0`。
 *  没有它的话，「`allSelectableChecked` 去掉 `length > 0` 前置」这条变异不可见
 *  （`[].every()` 返回 true 只在空数组上发生，而 fixture 永远非空）。 */
const resultsAllImported = [
  { id: 301, task_id: 13, provider_code: 'groq', model_id: 'groq/a', display_name: 'A', free_type: 'limited', monthly_tokens: 0, daily_tokens: 1, tos_verdict: 'ok', tos_notes: '', import_status: 'imported', imported_at: '2026-10-06T06:00:00Z' },
  { id: 302, task_id: 13, provider_code: 'groq', model_id: 'groq/b', display_name: 'B', free_type: 'limited', monthly_tokens: 0, daily_tokens: 1, tos_verdict: 'ok', tos_notes: '', import_status: 'imported', imported_at: '2026-10-06T06:01:00Z' },
  { id: 303, task_id: 13, provider_code: 'groq', model_id: 'groq/c', display_name: 'C', free_type: 'limited', monthly_tokens: 0, daily_tokens: 1, tos_verdict: 'ok', tos_notes: '', import_status: 'imported', imported_at: '2026-10-06T06:02:00Z' },
]

const historyDetail = [
  { id: 201, task_id: 15, provider_code: 'anthropic', model_id: 'anthropic/claude', display_name: 'Claude', free_type: 'limited', monthly_tokens: 0, daily_tokens: 1000, tos_verdict: 'ok', tos_notes: '', import_status: 'imported', imported_at: '2026-10-06T04:31:00Z' },
  // ★ import_status 走 `st-warning`（conflict）那一档；imported_at 为 null
  { id: 202, task_id: 15, provider_code: 'anthropic', model_id: 'anthropic/claude-2', display_name: 'Claude 2', free_type: 'unlimited', monthly_tokens: 0, daily_tokens: 0, tos_verdict: 'caution', tos_notes: '', import_status: 'conflict', imported_at: null },
]

const { ctrl } = vi.hoisted(() => ({
  ctrl: {
    templates: [] as unknown[],
    tasks: [] as unknown[],
    /** 按 taskId 分派扫描结果；查不到返回空数组（= 真实的空明细）。 */
    resultsByTask: {} as Record<number, unknown[]>,
    reset() {
      ctrl.templates = []
      ctrl.tasks = []
      ctrl.resultsByTask = {}
    },
  },
}))

vi.mock('../api', async (orig) => {
  const actual = (await orig()) as Record<string, unknown>
  return {
    ...actual,
    listFreeDiscoveryTemplates: vi.fn(async () => ({ templates: ctrl.templates })),
    listFreeDiscoveryTasks: vi.fn(async () => ({ tasks: ctrl.tasks })),
    listFreeDiscoveryTaskResults: vi.fn(async (taskId?: number) => ({
      results: (taskId != null && ctrl.resultsByTask[taskId]) || [],
    })),
    getFreeDiscoveryPresets: vi.fn(async () => ({ presets: [] })),
  }
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    'zh-CN': {
      freeDiscovery: {
        page: { title: '免费资源发现', desc: '说明' },
        common: {
          actions: '操作', empty: '暂无数据', enabled: '已启用', disabled: '已禁用',
          loading: '加载中', refresh: '刷新', hideForm: '收起',
        },
        tabs: { templates: '模板', tasks: '任务', history: '历史' },
        presets: { title: '预设', keyless: '无密钥', scannerPending: '扫描器待支持', create: '创建', exists: '已存在' },
        tpl: {
          listTitle: '模板（{n}）', name: '名称', baseUrl: '地址', apiType: '接口类型',
          keyEnv: '密钥变量', tos: '条款', enabled: '启用', createdAt: '创建时间',
          scan: '扫描', delete: '删除', disabledScanHint: '模板已禁用',
        },
        scan: { title: '扫描', pickTemplate: '选模板', start: '开始', running: '扫描中' },
        task: {
          listTitle: '任务（{n}）', provider: '供应商', status: '状态', trigger: '触发',
          found: '发现', imported: '已导入', by: '触发者', time: '时间', error: '错误', review: '审查',
        },
        res: {
          title: '结果', none: '暂无结果', model: '模型', displayName: '显示名', freeType: '免费类型',
          monthly: '月额度', daily: '日额度', tos: '条款', importStatus: '导入状态',
          pending: '仅待导入', all: '全部', policy: '冲突策略', policySkip: '跳过',
          policyOverwrite: '覆盖', policyMerge: '合并',
          importSelected: '导入所选（{n}）', importAllPending: '导入全部待导入',
        },
        hist: { title: '历史（{n}）', desc: '历史说明', none: '暂无历史', completed: '完成时间', detail: '明细', detailTitle: '明细 {id}', importedAt: '导入时间' },
        // ★ 键名照抄 `taskStatusKey` / `triggerKey` / `importStatusKey` 三个函数，
        //   不是我按语义编的 —— 第一版编成 `taskStatus.*`，结果 i18n 查不到、
        //   badge 文本吐的是键名本身（红的是我的 fixture，不是产品）。
        trigger: { manual: '手动', scheduled: '定时', webhook: 'Webhook' },
        status: {
          taskPending: '待处理', running: '运行中', success: '成功', failed: '失败',
          imported: '已导入', conflict: '冲突', skipped: '已跳过', review: '待审',
        },
        form: { show: '新建', providerCode: '供应商代码', displayName: '显示名', baseUrl: '地址', apiType: '类型', apiKeyEnv: '密钥', tosVerdict: '条款', apiKeyEnvHint: '提示', submit: '提交' },
        orbi: { show: 'Orbi 导入', hint: '提示', import: '导入', invalidJson: 'JSON 非法', done: '完成' },
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
 * Vue 警告收集器。
 *
 * ★ 为什么需要它：Vue 3 里**重复 `:key` 不产生任何 DOM 差异**，只有一个 dev
 *   warning。第一版门禁只看 DOM，于是「`title-key` 从 `id` 改成会撞键的 `api_type`」
 *   这条变异完全测不出来（实测 5 条模板里 3 条同值）。把 warning 接出来，
 *   撞键就成了可断言的不变量 —— 这与 D2 的结论不冲突：
 *   D2 说「重复 key 观测不到实际故障」，这里正是把**那个唯一的可见信号**用起来。
 */
const vueWarnings: string[] = []

async function factory() {
  vueWarnings.length = 0
  const w = mount(FreeDiscoveryView, {
    global: {
      plugins: [i18n],
      config: { warnHandler: (msg: string) => { vueWarnings.push(msg) } },
    },
  })
  await flushPromises()
  await flushPromises()
  return w
}

type W = Awaited<ReturnType<typeof factory>>

async function openTab(w: W, id: 'templates' | 'tasks' | 'history') {
  const idx = ['templates', 'tasks', 'history'].indexOf(id)
  await w.findAll('.tab-bar .tab-btn')[idx]!.trigger('click')
  await flushPromises()
}

function sectionOf(w: W, id: SectionId) {
  const host = w.find(`[data-testid="${id}"]`)
  expect(host.exists(), `找不到容器 ${id}`).toBe(true)
  return host!
}

function cardsOf(w: W, id: SectionId) {
  return sectionOf(w, id).findAll('[data-testid="card-list"] .card')
}

function fieldLabels(card: ReturnType<typeof cardsOf>[number]) {
  return card.findAll('.card__field').map((f) => f.find('dt').text())
}

/** 定位失败时把该段实际有的字段打出来 —— 「找不到字段」本身指不出是哪段哪一行。 */
function fieldValue(w: W, id: SectionId, index: number, label: string) {
  const card = cardsOf(w, id)[index]
  expect(card, `${id} 第 ${index} 张卡不存在（共 ${cardsOf(w, id).length} 张）`).toBeTruthy()
  const hit = card!.findAll('.card__field').find((f) => f.find('dt').text() === label)
  expect(hit, `${id} 第 ${index} 张卡里找不到字段「${label}」，实际有：${fieldLabels(card!).join('/')}`).toBeTruthy()
  return hit!.find('dd')
}

/** 打开 T3 下钻：点任务卡的「审查」。 */
async function drillIntoTask11(w: W) {
  const btn = cardsOf(w, 'fd-tasks')[0]!.findAll('.card__actions button')[0]!
  await btn.trigger('click')
  await flushPromises()
}

/**
 * 打开 T5 明细：点历史卡的「明细」。
 *
 * ★ **必须按卡头选，不能用下标** —— `historyTasks` 是
 *   `tasks.filter(models_imported > 0)`，本 fixture 里 **id 11 与 15 都满足**，
 *   顺序即 tasks 顺序 ⇒ 第 0 张是 groq/task 11，第 1 张才是 anthropic/task 15。
 *   我第一版写死下标 0，于是每次「打开明细」拿到的都是 task 11 的扫描结果，
 *   4 条断言全红 —— 红的是 fixture 的假设，不是产品。
 */
async function openHistoryDetail(w: W, title = 'anthropic') {
  const card = cardsOf(w, 'fd-history').find((c) => c.attributes('data-title') === title)
  expect(card, `历史段里找不到卡头为「${title}」的卡，实际有：${cardsOf(w, 'fd-history').map((c) => c.attributes('data-title')).join('/')}`).toBeTruthy()
  await card!.findAll('.card__actions button')[0]!.trigger('click')
  await flushPromises()
}

beforeEach(() => {
  _resetDataViewModeForTests()
  ctrl.reset()
  mockWindowClass('expanded')
  localStorage.clear()
})

afterEach(() => vi.clearAllMocks())

// ─────────────────────────────────────────────────────────────────────────────
describe('FreeDiscoveryView：桌面三 Tab 五张表零回归', () => {
  it('三 Tab 互斥：默认只出模板段', async () => {
    ctrl.templates = templates
    const w = await factory()
    expect(w.findAll('.tab-bar .tab-btn')).toHaveLength(3)
    expect(w.findAll('.tab-panel')).toHaveLength(1)
    expect(w.findAll('.data-table')).toHaveLength(1)
  })

  it('T1/T2/T4 的列头逐字未变（8 / 10 / 8 列）', async () => {
    ctrl.templates = templates
    ctrl.tasks = tasks
    const w = await factory()
    expect(w.findAll('.data-table')[0]!.findAll('th').map((h) => h.text())).toEqual([
      '名称', '地址', '接口类型', '密钥变量', '条款', '启用', '创建时间', '操作',
    ])

    await openTab(w, 'tasks')
    expect(w.findAll('.data-table')[0]!.findAll('th').map((h) => h.text())).toEqual([
      '#', '供应商', '状态', '触发', '发现', '已导入', '触发者', '时间', '错误', '操作',
    ])

    await openTab(w, 'history')
    expect(w.findAll('.data-table')[0]!.findAll('th').map((h) => h.text())).toEqual([
      '#', '供应商', '已导入', '发现', '触发', '触发者', '完成时间', '操作',
    ])
  })

  it('T3 扫描结果 8 列（含首列那个全选 checkbox）', async () => {
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results }
    const w = await factory()
    await openTab(w, 'tasks')
    expect(w.findAll('.data-table')).toHaveLength(1) // 未下钻时只有 T2
    // ★ 必须先点「审查」把下钻打开 —— 第一版忘了这步，`[1]` 是 undefined
    await w.findAll('.data-table')[0]!.findAll('tbody tr')[0]!.findAll('td')[9]!.find('button').trigger('click')
    await flushPromises()
    const t3 = w.findAll('.data-table')[1]!
    expect(t3.findAll('th').map((h) => h.text())).toEqual([
      '', '模型', '显示名', '免费类型', '月额度', '日额度', '条款', '导入状态',
    ])
  })

  it('T5 导入明细 4 列，且它桌面没有空态行', async () => {
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 15: historyDetail }
    const w = await factory()
    await openTab(w, 'history')
    // 桌面用「明细」按钮下钻（compact 下同一入口在卡片里）
    const detailBtn = w.findAll('.data-table')[0]!.findAll('tbody tr')[1]!.findAll('td')[7]!.find('button')
    await detailBtn.trigger('click')
    await flushPromises()
    expect(w.findAll('.data-table')).toHaveLength(2)
    const t5 = w.findAll('.data-table')[1]!
    expect(t5.findAll('th').map((h) => h.text())).toEqual(['模型', '条款', '导入状态', '导入时间'])
    // ★ 本切片没给 T5 造一个桌面空态行
    expect(t5.find('.empty-cell').exists()).toBe(false)
  })

  it('四张表的表内空态行仍在（colspan 8/10/8/8），且 T5 桌面没有空态行', async () => {
    const w = await factory()
    expect(w.findAll('.empty-cell')).toHaveLength(1)
    expect(w.find('.empty-cell').attributes('colspan')).toBe('8')

    await openTab(w, 'tasks')
    expect(w.findAll('.empty-cell')).toHaveLength(1)
    expect(w.find('.empty-cell').attributes('colspan')).toBe('10')

    await openTab(w, 'history')
    expect(w.findAll('.empty-cell')).toHaveLength(1)
    expect(w.find('.empty-cell').attributes('colspan')).toBe('8')
    // ★ T5（导入明细）桌面**本来就没有空态行** —— 钉住「本切片没给它加一个」
    expect(w.findAll('.data-table')).toHaveLength(1)
  })

  it('tos 四档 badge 类名：ok/caution/avoid + 未登记值走 ambiguous 兜底', async () => {
    ctrl.templates = templates
    const w = await factory()
    const rows = w.findAll('.data-table')[0]!.findAll('tbody tr')
    expect(rows).toHaveLength(5)
    // ★ 类名挂在 `<span class="badge …">` 上，不在 `<td>` 上 —— 第一版查了 td，红的是判据
    const badge = (r: (typeof rows)[number]) => r.findAll('td')[4]!.find('.badge')
    expect(rows.map((r) => badge(r).text())).toEqual(['ok', 'caution', 'avoid', 'whatever', 'ok'])
    expect(rows.map((r) => TOS_CLASSES.find((c) => badge(r).classes().includes(c)))).toEqual([
      'tos-ok', 'tos-caution', 'tos-avoid', 'tos-ambiguous', 'tos-ok',
    ])
  })

  it('任务状态四档 badge 类名逐档不同（pending 是 st-pending，不是 neutral）', async () => {
    ctrl.tasks = tasks
    const w = await factory()
    await openTab(w, 'tasks')
    const rows = w.findAll('.data-table')[0]!.findAll('tbody tr')
    const badgeOf = (r: (typeof rows)[number]) => {
      const b = r.findAll('td')[2]!.find('.badge')
      return `${b.text()}:${TASK_STATUS_CLASSES.find((c) => b.classes().includes(c))}`
    }
    expect(rows.slice(0, 4).map(badgeOf)).toEqual([
      '成功:st-success', '失败:st-failed', '运行中:st-running', '待处理:st-pending',
    ])
  })

  it('空串那几列在桌面出破折号（不是空白）', async () => {
    ctrl.templates = templates
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results }
    const w = await factory()
    // T1 第 2 行 api_key_env 为空串 ⇒ 第 4 列出 `—`
    const keyEnv = w.findAll('.data-table')[0]!.findAll('tbody tr').map((r) => r.findAll('td')[3]!.text())
    expect(keyEnv[1]).toBe('—')

    await openTab(w, 'tasks')
    // T2 第 1 行 error_message 为空串 ⇒ 第 9 列出 `—`
    const errs = w.findAll('.data-table')[0]!.findAll('tbody tr').map((r) => r.findAll('td')[8]!.text())
    expect(errs[0]).toBe('—')
    expect(errs[1]).toBe('boom')
  })

  it('fmtNum 的 0 是合法值（不被当成缺值）', async () => {
    ctrl.tasks = tasks
    const w = await factory()
    await openTab(w, 'tasks')
    const found = w.findAll('.data-table')[0]!.findAll('tbody tr').map((r) => r.findAll('td')[4]!.text())
    expect(found).toEqual(['12', '0', '0', '3', '5'])
  })

  it('T3 表头那枚「全选」checkbox 仍在桌面；逐行 checkbox 按 import_status 决定 disabled', async () => {
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results }
    const w = await factory()
    await openTab(w, 'tasks')
    await w.findAll('.data-table')[0]!.findAll('tbody tr')[0]!.findAll('td')[9]!.find('button').trigger('click')
    await flushPromises()
    const t3 = w.findAll('.data-table')[1]!
    expect(t3.find('thead input[type="checkbox"]').exists()).toBe(true)
    const rowBoxes = t3.findAll('tbody input[type="checkbox"]')
    expect(rowBoxes).toHaveLength(4)
    expect(rowBoxes[0]!.attributes('disabled')).toBeUndefined() // pending
    expect(rowBoxes[1]!.attributes('disabled')).toBeUndefined() // pending
    expect(rowBoxes[2]!.attributes('disabled')).toBeDefined() // imported
    expect(rowBoxes[3]!.attributes('disabled')).toBeDefined() // 未登记 ⇒ 非 pending
  })

  // ── 补门：这两条是台账查出「门未红」之后才补的 ──────────────────────
  // M44（selectableResults 判据改写）与 M47（`allSelectableChecked` 去掉 `length > 0`）
  // 在第一轮台账里都「门未红」—— 根因是 fixture 从不出现「0 个可选项」，
  // 且没有任何断言读表头 checkbox 的 checked 状态。
  it('★ 全部 pending 都勾上时，表头「全选」才变成选中（0 个可选项时不得为选中）', async () => {
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results }
    const w = await factory()
    await openTab(w, 'tasks')
    await w.findAll('.data-table')[0]!.findAll('tbody tr')[0]!.findAll('td')[9]!.find('button').trigger('click')
    await flushPromises()
    const headBox = () => w.findAll('.data-table')[1]!.find('thead input[type="checkbox"]')
    expect((headBox().element as HTMLInputElement).checked).toBe(false)

    // 结果集里有 2 条 pending（101/102）+ 1 条 imported（103）+ 1 条未登记（104）
    for (const i of [0, 1]) {
      await w.findAll('.data-table')[1]!.findAll('tbody tr')[i]!.find('input[type="checkbox"]').setValue(true)
      await flushPromises()
    }
    // ★ 两条 pending 都被勾上 ⇒ 全选成立
    expect((headBox().element as HTMLInputElement).checked).toBe(true)
  })

  it('★ 0 个可选项时表头「全选」不得为选中（`[].every()` 恒真那个坑）', async () => {
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 13: resultsAllImported }
    const w = await factory()
    await openTab(w, 'tasks')
    // 下钻 task 13（三条全部已导入 ⇒ selectableResults 为空）
    await w.findAll('.data-table')[0]!.findAll('tbody tr')[2]!.findAll('td')[9]!.find('button').trigger('click')
    await flushPromises()
    const t3 = w.findAll('.data-table')[1]!
    expect(t3.findAll('tbody tr')).toHaveLength(3)
    const headBox = t3.find('thead input[type="checkbox"]')
    expect((headBox.element as HTMLInputElement).checked, '0 个可选项时全选必须是 false').toBe(false)
    expect(headBox.attributes('disabled')).toBeDefined()
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('FreeDiscoveryView：表格地形（.table-wrap / table-min-width）', () => {
  it('`.table-wrap` 在代码里清零（模板 + <style>）—— 它自带 overflow-x，与容器嵌套会出双横滚', () => {
    // ★ 查 CODE_ONLY 而不是 SOURCE：本页的说明注释里**故意**提到了 `.table-wrap`
    //   （解释为什么要删它），拿原文去 grep 必然命中 —— 红的是判据。
    expect(CODE_ONLY).not.toContain('table-wrap')
    expect(SOURCE).not.toMatch(/^\.table-wrap\s*\{/m)
  })

  it('容器的直接子元素是原生 <table>（所以 table-min-width 这次真的命中）', async () => {
    ctrl.templates = templates
    const w = await factory()
    const host = w.find('.responsive-data-view__table')
    expect(host.exists()).toBe(true)
    expect([...host.element.children].map((c) => c.tagName.toLowerCase())).toEqual(['table'])
    expect(host.find('table.data-table').exists()).toBe(true)
  })

  it('五个容器都传 table-min-width="0px"（.data-table 没有 min-width，默认 720px 会凭空加横滚）', () => {
    expect(RDV_TAGS).toHaveLength(5)
    for (const tag of RDV_TAGS) {
      expect(tag, `容器缺 table-min-width：${tag}`).toContain('table-min-width="0px"')
    }
    // 反向证据：.data-table 真的没有 min-width
    expect(/\.data-table\s*\{[^}]*min-width/.test(SOURCE)).toBe(false)
  })

  it('五个容器都不传 :loading（本页 loading 只驱动刷新按钮文案，没有覆盖层）', () => {
    for (const tag of RDV_TAGS) expect(tag, `容器不该接 :loading：${tag}`).not.toContain(':loading')
  })

  /**
   * ★ 编码一条**写法约定**（不是「观测到故障」的守卫），并把这一点写在断言旁边。
   *
   * 事实：Vue 3 里重复 `:key` **在初次渲染时不产生任何 warning**（只有 update 阶段
   * 才发 "Duplicate keys found during update"），也不产生任何 DOM 差异
   * —— 这与 D2 的结论一致。所以 M01（`title-key` 从 `id` 改成会撞键的 `api_type`，
   * fixture 里 5 条有 3 条同值）**靠 DOM 与 warning 都抓不到**。
   * ⇒ 这里退一步断**结构**：五张表的卡头键统一是主键 `id`。
   * 它防的是那个具体脚枪（拿一个不唯一的字段当 `:key`），不是假装重复键会崩。
   */
  it('五张表的 title-key 统一是主键 id（写法约定：卡头键不许用不唯一的字段）', () => {
    expect(CODE_ONLY.match(/title-key="[^"]*"/g)).toEqual(Array<string>(5).fill('title-key="id"'))
  })

  it('五处 :empty 都带 isCompact 前置', () => {
    const ones = CODE_ONLY.match(/:empty="[^"]*"/g) ?? []
    expect(ones).toHaveLength(5)
    for (const e of ones) expect(e).toMatch(/^:empty="isCompact && /)
  })

  it('下钻容器挂在同一分支上：未选中任务 / 未打开明细时不挂载', async () => {
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results, 15: historyDetail }
    const w = await factory()
    await openTab(w, 'tasks')
    expect(w.find('[data-testid="fd-results"]').exists()).toBe(false)

    await openTab(w, 'history')
    expect(w.find('[data-testid="fd-history-detail"]').exists()).toBe(false)
  })

  it('无分页 ⇒ 不引入连续加载', () => {
    for (const token of ['createHyperPages', 'HyperLoadMore', 'hyper-load-more', 'page_size']) {
      expect(CODE_ONLY, `页面里不该出现 ${token}`).not.toContain(token)
    }
  })

  it('0 新增 i18n 键：只引用 freeDiscovery.* 的既有键', () => {
    const used = new Set([...CODE_ONLY.matchAll(/t\('([^']+)'/g)].map((m) => m[1]))
    for (const k of used) expect(/^freeDiscovery\./.test(k), `预期词条前缀之外的键：${k}`).toBe(true)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('FreeDiscoveryView：badge 类 → 卡片 tone（从 <style> 反查，不是抄映射表）', () => {
  /**
   * ★ 本块的价值：不把 `TONE_BY_BADGE_CLASS` 抄一遍，而是去 `<style>` 里抽每个
   *   badge 类真实的 `color: var(--x)`，再按 token 推期望 tone。
   *   改了徽章颜色却忘了改映射（或反之），这里会红。
   *   —— 这正是 D11（`color:check` 只扫 `<style>`、看不见 `<script>` 里的判定）
   *   在页面级的补法。
   */
  const TOKEN_TONE: Record<string, string> = {
    '--success': 'good',
    '--warning': 'warn',
    '--danger': 'danger',
    '--text-secondary': 'neutral',
    '--primary': 'neutral', // CardTone 没有 primary 档 ⇒ 落 neutral（不新造一档）
  }

  const BADGE_CLASSES = [
    'tos-ok', 'tos-caution', 'tos-avoid', 'tos-ambiguous',
    'st-success', 'st-failed', 'st-running', 'st-pending', 'st-warning', 'st-muted',
  ]

  /** 从 `<style>` 抽 `.cls { … color: var(--x) … }` 里的 token。 */
  function colorTokenOf(cls: string): string {
    const m = SOURCE.match(new RegExp(`\\.${cls}\\s*\\{([^}]*)\\}`))
    expect(m, `<style> 里找不到 .${cls}`).toBeTruthy()
    const c = m![1]!.match(/color:\s*var\((--[a-z-]+)\)/)
    expect(c, `.${cls} 没有 color: var(…)`).toBeTruthy()
    return c![1]!
  }

  it('10 个 badge 类的色值 token 全部落在已知的 5 个语义 token 上', () => {
    for (const cls of BADGE_CLASSES) {
      const token = colorTokenOf(cls)
      expect(TOKEN_TONE[token], `${cls} 的 color 是 ${token}，映射表里没有这一档`).toBeTruthy()
    }
  })

  it('映射表覆盖全部 10 个类，且每个类的 tone 与它的色值 token 一致', () => {
    const table = CODE_ONLY.match(/TONE_BY_BADGE_CLASS[^=]*=\s*\{([\s\S]*?)\n\}/)?.[1] ?? ''
    const parsed = new Map<string, string>()
    for (const m of table.matchAll(/'([a-z-]+)':\s*'([a-z]+)'/g)) parsed.set(m[1]!, m[2]!)
    expect(parsed.size, `映射表只解析出 ${parsed.size} 项`).toBe(BADGE_CLASSES.length)
    for (const cls of BADGE_CLASSES) {
      expect(parsed.get(cls), `${cls} 的 tone`).toBe(TOKEN_TONE[colorTokenOf(cls)])
    }
  })

  it('★ 两个反直觉档位：st-pending 是 warn（不是 neutral）、st-running 是 neutral（无 primary 档）', () => {
    expect(colorTokenOf('st-pending')).toBe('--warning')
    expect(colorTokenOf('st-running')).toBe('--primary')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('FreeDiscoveryView：compact 卡片形态', () => {
  beforeEach(() => {
    mockWindowClass('compact')
    ctrl.templates = templates
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results, 15: historyDetail }
  })

  it('T1 出 5 张卡不出表；T2 出 5 张卡；T4 只出 2 张（models_imported>0 的两条任务）', async () => {
    const w = await factory()
    expect(cardsOf(w, 'fd-templates')).toHaveLength(5)
    expect(w.findAll('.data-table')).toHaveLength(0)

    await openTab(w, 'tasks')
    expect(cardsOf(w, 'fd-tasks')).toHaveLength(5)

    await openTab(w, 'history')
    expect(cardsOf(w, 'fd-history')).toHaveLength(2)
  })

  it('compact 下不渲染形态切换钮', async () => {
    const w = await factory()
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  /** 字段集与桌面的列一一对应：扣掉卡头那一列与操作列，一张不许多一张不许少。 */
  it('字段集与桌面列一一对应（T1 五 / T2 七 / T3 六 / T5 三）', async () => {
    const w = await factory()
    expect(fieldLabels(cardsOf(w, 'fd-templates')[0]!)).toEqual(['地址', '接口类型', '密钥变量', '条款', '创建时间'])

    await openTab(w, 'tasks')
    expect(fieldLabels(cardsOf(w, 'fd-tasks')[0]!)).toEqual(['状态', '触发', '发现', '已导入', '触发者', '时间', '错误'])

    await drillIntoTask11(w)
    expect(fieldLabels(cardsOf(w, 'fd-results')[0]!)).toEqual(['显示名', '免费类型', '月额度', '日额度', '条款', '导入状态'])

    await openTab(w, 'history')
    await openHistoryDetail(w)
    expect(fieldLabels(cardsOf(w, 'fd-history-detail')[0]!)).toEqual(['条款', '导入状态', '导入时间'])
  })

  it('T1 卡头是显示名，provider_code 走卡头副标题；显示名为空时回落到 provider_code', async () => {
    const w = await factory()
    const cards = cardsOf(w, 'fd-templates')
    expect(cards.map((c) => c.attributes('data-title'))).toEqual([
      'Groq Cloud', 'Cerebras', 'Unknown Vendor', 'Mystery', 'nameless',
    ])
    expect(cards[0]!.find('.card__subtitle').text()).toContain('groq')
  })

  it('T2/T4 卡头是 provider_code；T3/T5 卡头是 model_id', async () => {
    const w = await factory()
    await openTab(w, 'tasks')
    expect(cardsOf(w, 'fd-tasks').map((c) => c.attributes('data-title'))).toEqual([
      'groq', 'cerebras', 'groq', 'mystery', 'anthropic',
    ])

    await drillIntoTask11(w)
    expect(cardsOf(w, 'fd-results').map((c) => c.attributes('data-title'))).toEqual([
      'groq/llama-3-70b', 'groq/llama-3-8b', 'groq/mixtral', 'groq/weird',
    ])

    await openTab(w, 'history')
    expect(cardsOf(w, 'fd-history').map((c) => c.attributes('data-title'))).toEqual(['groq', 'anthropic'])
    await openHistoryDetail(w)
    expect(cardsOf(w, 'fd-history-detail').map((c) => c.attributes('data-title'))).toEqual([
      'anthropic/claude', 'anthropic/claude-2',
    ])
  })

  it('★ 空串那几列出 `—` 而不是空白（CardList.text() 只认 null/undefined，不认空串）', async () => {
    const w = await factory()
    // T1 第 2 行 api_key_env 为空串
    expect(fieldValue(w, 'fd-templates', 1, '密钥变量').text()).toBe('—')

    await openTab(w, 'tasks')
    // T2 第 1 行 error_message 为空串
    expect(fieldValue(w, 'fd-tasks', 0, '错误').text()).toBe('—')

    await drillIntoTask11(w)
    // T3 第 2 行 display_name 与 free_type 都是空串
    expect(fieldValue(w, 'fd-results', 1, '显示名').text()).toBe('—')
    expect(fieldValue(w, 'fd-results', 1, '免费类型').text()).toBe('—')
  })

  it('★ fmtNum 的 0 在卡片上仍是 0（不是 `—`）；null 才出 `—`', async () => {
    const w = await factory()
    await openTab(w, 'tasks')
    expect(fieldValue(w, 'fd-tasks', 0, '发现').text()).toBe('12')
    expect(fieldValue(w, 'fd-tasks', 1, '发现').text()).toBe('0')
    expect(fieldValue(w, 'fd-tasks', 1, '已导入').text()).toBe('0')

    await drillIntoTask11(w)
    expect(fieldValue(w, 'fd-results', 0, '月额度').text()).toBe('0')
    expect(fieldValue(w, 'fd-results', 0, '日额度').text()).toBe('1000')
    expect(fieldValue(w, 'fd-results', 2, '日额度').text()).toBe('—') // null
    expect(fieldValue(w, 'fd-results', 3, '月额度').text()).toBe('—') // null
  })

  it('tone 逐档：tos 四档 + 任务状态四档（含 pending→warn、running→neutral）', async () => {
    const w = await factory()
    expect(fieldValue(w, 'fd-templates', 0, '条款').attributes('data-tone')).toBe('good') // ok
    expect(fieldValue(w, 'fd-templates', 1, '条款').attributes('data-tone')).toBe('warn') // caution
    expect(fieldValue(w, 'fd-templates', 2, '条款').attributes('data-tone')).toBe('danger') // avoid
    expect(fieldValue(w, 'fd-templates', 3, '条款').attributes('data-tone')).toBe('neutral') // 未登记

    await openTab(w, 'tasks')
    expect(fieldValue(w, 'fd-tasks', 0, '状态').attributes('data-tone')).toBe('good') // success
    expect(fieldValue(w, 'fd-tasks', 1, '状态').attributes('data-tone')).toBe('danger') // failed
    expect(fieldValue(w, 'fd-tasks', 2, '状态').attributes('data-tone')).toBe('neutral') // running → --primary
    expect(fieldValue(w, 'fd-tasks', 3, '状态').attributes('data-tone')).toBe('warn') // pending → --warning
  })

  it('导入状态 tone：imported→good、conflict→warn、pending/未登记→warn', async () => {
    const w = await factory()
    await openTab(w, 'tasks')
    await drillIntoTask11(w)
    expect(fieldValue(w, 'fd-results', 0, '导入状态').attributes('data-tone')).toBe('warn') // pending
    expect(fieldValue(w, 'fd-results', 2, '导入状态').attributes('data-tone')).toBe('good') // imported
    expect(fieldValue(w, 'fd-results', 3, '导入状态').attributes('data-tone')).toBe('warn') // 未登记 → st-pending

    await openTab(w, 'history')
    await openHistoryDetail(w)
    expect(fieldValue(w, 'fd-history-detail', 0, '导入状态').attributes('data-tone')).toBe('good') // imported
    expect(fieldValue(w, 'fd-history-detail', 1, '导入状态').attributes('data-tone')).toBe('warn') // conflict
  })

  it('状态译名与桌面同源（未登记枚举两边都回落到 taskPending / review）', async () => {
    const w = await factory()
    // tos_verdict 未登记 ⇒ 桌面直接渲染原值 whatever，卡片照抄
    expect(fieldValue(w, 'fd-templates', 3, '条款').text()).toBe('whatever')

    await openTab(w, 'tasks')
    await drillIntoTask11(w)
    // import_status 未登记 ⇒ importStatusKey 回落到 status.review ⇒ 两边都是「待审」
    expect(fieldValue(w, 'fd-results', 3, '导入状态').text()).toBe('待审')
  })

  it('触发方式译名与桌面同源（三档 + 未登记回落 manual）', async () => {
    const w = await factory()
    await openTab(w, 'tasks')
    expect(fieldValue(w, 'fd-tasks', 0, '触发').text()).toBe('手动')
    expect(fieldValue(w, 'fd-tasks', 1, '触发').text()).toBe('定时')
    expect(fieldValue(w, 'fd-tasks', 2, '触发').text()).toBe('Webhook')
  })

  it('`#actions`：T1 三枚（切换/扫描/删除）、T2 一枚、T3 一枚 checkbox、T5 零枚', async () => {
    const w = await factory()
    const t1 = cardsOf(w, 'fd-templates')[0]!.find('.card__actions')
    expect(t1.findAll('button').map((b) => b.text())).toEqual(['已启用', '扫描', '删除'])
    // ★ enabled 列在桌面**就是**那个 toggle 按钮 ⇒ 卡片也不出该字段（否则同一句话出现两次）
    expect(fieldLabels(cardsOf(w, 'fd-templates')[0]!)).not.toContain('启用')

    await openTab(w, 'tasks')
    expect(cardsOf(w, 'fd-tasks')[0]!.find('.card__actions').findAll('button').map((b) => b.text())).toEqual(['审查'])

    await drillIntoTask11(w)
    expect(cardsOf(w, 'fd-results')[0]!.find('.card__actions').find('input[type="checkbox"]').exists()).toBe(true)

    await openTab(w, 'history')
    await openHistoryDetail(w)
    expect(cardsOf(w, 'fd-history-detail')[0]!.find('.card__actions').exists()).toBe(false)
  })

  // ── 第一组：台账查出「门未红」之后补上的判据 ──────────────────────────
  // 这 6 条**不是**我一开始就想到的，是变异台账逐条查因之后补的：
  //   M01 撞键 / M13 空态文案 / M25 时间格式化 / M28 状态译名 / M44 全选判据 / M47 零可选项
  it('★ 五张表都不产生「重复 key」警告（撞键的唯一可见信号）', async () => {
    ctrl.templates = templates
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results, 15: historyDetail }
    const w = await factory()
    await openTab(w, 'tasks')
    await drillIntoTask11(w)
    await openTab(w, 'history')
    await openHistoryDetail(w)
    const dup = vueWarnings.filter((m) => /Duplicate keys|duplicate key/i.test(m))
    expect(dup, `出现重复 key 警告：${dup.join(' | ')}`).toEqual([])
  })

  it('★ T1 的创建时间是**格式化后**的串（不是裸 ISO 串 —— D13 那一族）', async () => {
    ctrl.templates = templates
    const w = await factory()
    const created = fieldValue(w, 'fd-templates', 0, '创建时间').text()
    expect(created).toMatch(/\d/)
    expect(created).not.toContain('T')
    expect(created).not.toContain('Z')
  })

  it('★ T2 状态列的译名与桌面同源（不是裸枚举值）', async () => {
    ctrl.tasks = tasks
    const w = await factory()
    await openTab(w, 'tasks')
    expect(fieldValue(w, 'fd-tasks', 0, '状态').text()).toBe('成功')
    expect(fieldValue(w, 'fd-tasks', 1, '状态').text()).toBe('失败')
    expect(fieldValue(w, 'fd-tasks', 2, '状态').text()).toBe('运行中')
    expect(fieldValue(w, 'fd-tasks', 3, '状态').text()).toBe('待处理')
  })

  it('★ T3 与 T5 的空态文案各自复用既有关键词条（不是共用一句）', async () => {
    mockWindowClass('compact')
    ctrl.tasks = tasks
    ctrl.resultsByTask = {}
    const w = await factory()
    await openTab(w, 'tasks')
    await drillIntoTask11(w)
    // T3 用 `freeDiscovery.res.none`（"暂无结果"），不是 `common.empty`（"暂无数据"）
    expect(sectionOf(w, 'fd-results').find('.app-empty-state').text()).toContain('暂无结果')

    await openTab(w, 'history')
    await openHistoryDetail(w, 'anthropic')
    // T5 复用 `freeDiscovery.common.empty`
    expect(sectionOf(w, 'fd-history-detail').find('.app-empty-state').text()).toContain('暂无数据')
  })

  it('★ T4 的空态文案是 hist.none（"暂无历史"），不是 common.empty', async () => {
    mockWindowClass('compact')
    ctrl.tasks = [] // ★ 外层 beforeEach 塞了 tasks ⇒ T4 非空；要断文案必须先清空
    const w = await factory()
    await openTab(w, 'history')
    expect(sectionOf(w, 'fd-history').find('.app-empty-state').text()).toContain('暂无历史')
  })

  it('T1 的「扫描」disabled 规则与桌面同源（模板未启用 ⇒ 不可扫）', async () => {
    const w = await factory()
    const scanOf = (i: number) => cardsOf(w, 'fd-templates')[i]!.findAll('.card__actions button')[1]!
    expect(scanOf(0).attributes('disabled')).toBeUndefined() // enabled
    expect(scanOf(1).attributes('disabled')).toBeDefined() // enabled=false
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('FreeDiscoveryView：批量选择（本页的核心交互）', () => {
  beforeEach(() => {
    mockWindowClass('compact')
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results }
  })

  async function drilled() {
    const w = await factory()
    await openTab(w, 'tasks')
    await drillIntoTask11(w)
    return w
  }

  it('逐行 checkbox 真的驱动 selectedResultIds（导入所选的计数与 disabled 跟着变）', async () => {
    const w = await drilled()
    const importBtn = () => w.findAll('.import-bar button').find((b) => b.text().includes('导入所选'))!
    expect(importBtn().text()).toBe('导入所选（0）')
    expect(importBtn().attributes('disabled')).toBeDefined()

    const box = cardsOf(w, 'fd-results')[0]!.find('.card__actions input[type="checkbox"]')
    expect(box.exists()).toBe(true)
    await box.setValue(true)
    await flushPromises()

    expect(importBtn().text()).toBe('导入所选（1）')
    expect(importBtn().attributes('disabled')).toBeUndefined()
  })

  it('非 pending 的行 checkbox 是 disabled（与桌面同一条规则）', async () => {
    const w = await drilled()
    const boxes = cardsOf(w, 'fd-results').map((c) => c.find('.card__actions input[type="checkbox"]'))
    expect(boxes[0]!.attributes('disabled')).toBeUndefined() // pending
    expect(boxes[1]!.attributes('disabled')).toBeUndefined() // pending
    expect(boxes[2]!.attributes('disabled')).toBeDefined() // imported
    expect(boxes[3]!.attributes('disabled')).toBeDefined() // 未登记 ⇒ 非 pending
  })

  it('★ compact 下没有「全选」表头，但「导入全部待导入」按钮仍在（两者功能等价）', async () => {
    const w = await drilled()
    expect(sectionOf(w, 'fd-results').find('thead').exists()).toBe(false)
    const allBtn = w.findAll('.import-bar button').find((b) => b.text().includes('导入全部待导入'))!
    expect(allBtn.exists()).toBe(true)
    expect(allBtn.attributes('disabled')).toBeUndefined()
  })
})

// ─────────────────────────────────────────────────────────────────────────────
describe('FreeDiscoveryView：空态归属', () => {
  it('桌面三段全空：仍出各自的 .empty-cell，不出现我们的 EmptyState', async () => {
    const w = await factory()
    expect(w.find('.empty-cell').exists()).toBe(true)
    expect(w.find('.app-empty-state').exists()).toBe(false)
    await openTab(w, 'tasks')
    expect(w.find('.empty-cell').exists()).toBe(true)
    expect(w.find('.app-empty-state').exists()).toBe(false)
    await openTab(w, 'history')
    expect(w.find('.empty-cell').exists()).toBe(true)
    expect(w.find('.app-empty-state').exists()).toBe(false)
  })

  it('compact 各段全空：出 EmptyState（复用本页既有关键词条），不是空 <ul>', async () => {
    mockWindowClass('compact')
    const w = await factory()
    const host = sectionOf(w, 'fd-templates')
    expect(host.find('.app-empty-state').text()).toContain('暂无数据')
    expect(host.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('★ T5 桌面本来就没有空态行，compact 下仍必须出 EmptyState', async () => {
    mockWindowClass('compact')
    // resultsByTask 故意**不**给 15 ⇒ 打开明细返回空数组
    // 用 task 15（anthropic）打开明细，但**不给 15 配 resultsByTask** ⇒ 返回空数组
    ctrl.tasks = tasks
    ctrl.resultsByTask = { 11: results }
    const w = await factory()
    await openTab(w, 'history')
    await openHistoryDetail(w, 'anthropic')
    const host = sectionOf(w, 'fd-history-detail')
    expect(cardsOf(w, 'fd-history-detail')).toHaveLength(0)
    expect(host.find('.app-empty-state').exists(), 'T5 该出空态').toBe(true)
    expect(host.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('T3 下钻打开但结果为空时，也出 EmptyState（不是空卡片列表）', async () => {
    mockWindowClass('compact')
    ctrl.tasks = tasks
    ctrl.resultsByTask = {}
    const w = await factory()
    await openTab(w, 'tasks')
    await drillIntoTask11(w)
    const host = sectionOf(w, 'fd-results')
    expect(host.find('.app-empty-state').exists(), 'T3 该出空态').toBe(true)
  })
})
