import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import FreeDiscoveryView from './FreeDiscoveryView.vue'
import {
  fetchTemplates,
  fetchPresets,
  fetchTasks,
  fetchTaskById,
  fetchTaskResults,
  fetchScanSchedulerStatus,
  type FdTemplate,
  type FdPreset,
  type FdTask,
  type FdResult,
  type FdScanSchedulerStatus,
} from '@/api/freeDiscovery'
import { DRAWER_NAV } from '@/config/appNav'
import router from '@/router'
import { setLocale, locale } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { GO_ZERO_TIME } from '@/api/freeDiscovery'

/**
 * FreeDiscoveryView 的不变量（第四十八批，2026-10-07）。
 *
 * 1. ★★★★★★★★★★ 任务列表的 `updated_at` **恒为 Go 零值**（ListTasks 的 SELECT 漏列），
 *     而详情端点给真值 ⇒ 同字段两种渲染，列表不许显示它。
 * 2. ★★★★★★★★ 「空」有两种表示：`templates` 恒 `[]`（有 nil-guard）、
 *     `presets` 可为 `null`（无 nil-guard）⇒ 必须分别渲染，不许合并。
 * 3. ★★★★★★★ 租户范围：super_admin 看到的也只是 `default` 一个租户。
 * 4. ★★★★★★ 抽屉席是 admin 档（**不设** requiresRole）；
 *     第七条 scan-scheduler 是 superAdmin 档 ⇒ 不占席、单列 403。
 * 5. ★★★★ `limit` 超上界是**回落 50 而不是 clamp 到 200**。
 * 6. ★★★ `?status=` 未知取值不被拒 ⇒ 空结果分不出「真没有」与「过滤写错了」。
 * 7. ★★ 两个 omitempty 键（`last_scan_failure_at` / `auto_disabled_at`）缺失 ≠ null。
 * 8. ★★ 结果行里四个 0 全是二义；但 `free_type` 的 `''` **不是**二义。
 * 9. ★★ 抛错不许退化成「空列表」—— 旧数据必须被清空。
 *
 * ★ 所有期望文案都从 `zhCN` 词典**取值**，不手打（手打文案是本仓累计踩过 8 次的坑）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/freeDiscovery', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/freeDiscovery')>()
  return {
    ...actual,
    fetchTemplates: vi.fn(),
    fetchPresets: vi.fn(),
    fetchTasks: vi.fn(),
    fetchTaskById: vi.fn(),
    fetchTaskResults: vi.fn(),
    fetchScanSchedulerStatus: vi.fn(),
  }
})

const templatesMock = fetchTemplates as unknown as ReturnType<typeof vi.fn>
const presetsMock = fetchPresets as unknown as ReturnType<typeof vi.fn>
const tasksMock = fetchTasks as unknown as ReturnType<typeof vi.fn>
const taskDetailMock = fetchTaskById as unknown as ReturnType<typeof vi.fn>
const resultsMock = fetchTaskResults as unknown as ReturnType<typeof vi.fn>
const schedulerMock = fetchScanSchedulerStatus as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function wire<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** ★ 列表端点的任务：SELECT 漏了 updated_at ⇒ 零值时间。 */
function taskFromList(over: Record<string, unknown> = {}): FdTask {
  return wire({
    id: 7,
    tenant_id: 'acme',
    template_id: 12,
    provider_code: 'groq',
    status: 'success',
    trigger_type: 'manual',
    triggered_by: 'alice',
    started_at: '2026-09-09T02:00:01Z',
    completed_at: '2026-09-09T02:00:09Z',
    error_message: '',
    models_found: 14,
    models_imported: 14,
    created_at: '2026-09-09T02:00:00Z',
    updated_at: GO_ZERO_TIME,
    ...over,
  }) as FdTask
}

/** ★ 详情端点的任务：SELECT 有 updated_at ⇒ 真值。 */
function taskDetailRow(over: Record<string, unknown> = {}): FdTask {
  return wire({
    id: 7,
    tenant_id: 'acme',
    template_id: 12,
    provider_code: 'groq',
    status: 'success',
    trigger_type: 'manual',
    triggered_by: 'alice',
    started_at: '2026-09-09T02:00:01Z',
    completed_at: '2026-09-09T02:00:09Z',
    error_message: '',
    models_found: 14,
    models_imported: 14,
    created_at: '2026-09-09T02:00:00Z',
    updated_at: '2026-09-09T02:00:09Z',
    ...over,
  }) as FdTask
}

function templateRow(over: Record<string, unknown> = {}): FdTemplate {
  return wire({
    id: 12,
    tenant_id: 'acme',
    provider_code: 'groq',
    display_name: 'Groq Cloud (Free Tier)',
    base_url: 'https://api.groq.com/openai/v1',
    api_type: 'openai-completions',
    api_key_env: '$GROQ_API_KEY',
    models_endpoint: '/models',
    quota_endpoint: '',
    tos_url: 'https://groq.com/terms-of-use/',
    tos_verdict: 'caution',
    tos_notes: 'Free tier documented',
    enabled: true,
    created_by: 'alice',
    created_at: '2026-09-09T02:00:00Z',
    updated_at: '2026-09-09T03:00:00Z',
    consecutive_scan_failures: 0,
    ...over,
  }) as FdTemplate
}

function presetRow(over: Record<string, unknown> = {}): FdPreset {
  return wire({
    provider_code: 'groq',
    display_name: 'Groq Cloud (Free Tier)',
    base_url: 'https://api.groq.com/openai/v1',
    api_type: 'openai-completions',
    api_key_env: '$GROQ_API_KEY',
    tos_verdict: 'caution',
    tos_notes: 'Free tier documented',
    ...over,
  }) as FdPreset
}

function resultRow(over: Record<string, unknown> = {}): FdResult {
  return wire({
    id: 91,
    task_id: 7,
    tenant_id: 'acme',
    provider_code: 'groq',
    model_id: 'llama-3.3-70b-versatile',
    display_name: 'Llama 3.3 70B Versatile',
    context_window: 131072,
    max_tokens: 32768,
    free_type: 'recurring-daily',
    monthly_tokens: 0,
    daily_tokens: 14400,
    pool_key: '',
    tos_verdict: 'caution',
    tos_notes: 'Free tier documented',
    import_status: 'pending',
    imported_at: null,
    created_at: '2026-09-09T02:00:05Z',
    ...over,
  }) as FdResult
}

function schedulerRow(over: Record<string, unknown> = {}): FdScanSchedulerStatus {
  return wire({
    enabled: true,
    started: true,
    interval: '6h0m0s',
    last_sweep_at: '2026-10-07T00:00:00Z',
    sweeps_total: 128,
    scans_total: 512,
    scans_failed: 3,
    scans_skipped: 40,
    cycles_failed: 1,
    ...over,
  }) as FdScanSchedulerStatus
}

/** ★★ typed-nil provider 分支（`bg/scan_scheduler.go:509-511`）。 */
function schedulerTypedNil(): FdScanSchedulerStatus {
  return wire({
    enabled: false,
    started: false,
    interval: '',
    last_sweep_at: GO_ZERO_TIME,
    sweeps_total: 0,
    scans_total: 0,
    scans_failed: 0,
    scans_skipped: 0,
    cycles_failed: 0,
  }) as FdScanSchedulerStatus
}

interface MockPlan {
  templates?: unknown
  presets?: unknown
  tasks?: unknown
  taskDetail?: unknown
  results?: unknown
  scheduler?: unknown
}

/** ★ helper 接受覆盖 + `instanceof Error` 分派（避免无条件覆盖造出恒真判据）。 */
function setAll(plan: MockPlan = {}): void {
  const pair = (m: ReturnType<typeof vi.fn>, v: unknown, fallback: unknown) => {
    if (v instanceof Error) m.mockRejectedValue(v)
    else m.mockResolvedValue(v === undefined ? fallback : v)
  }
  pair(templatesMock, plan.templates, { templates: [templateRow()] })
  pair(presetsMock, plan.presets, { presets: [presetRow()] })
  pair(tasksMock, plan.tasks, { tasks: [taskFromList()] })
  pair(taskDetailMock, plan.taskDetail, taskDetailRow())
  pair(resultsMock, plan.results, { results: [resultRow()] })
  pair(schedulerMock, plan.scheduler, schedulerRow())
}

async function mountView(plan: MockPlan = {}): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  setAll(plan)
  const w = mount(FreeDiscoveryView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>

/** ★ 按面板标题定位（标题取自词典，不手打）。 */
function panel(w: W, title: string) {
  return w
    .findAll('.fd__panel')
    .find((x) => (x.find('.fd__panel-title').exists() ? x.find('.fd__panel-title').text() : '') === title)
}

const P = {
  templates: () => zhCN.fd.templatesTitle,
  presets: () => zhCN.fd.presetsTitle,
  tasks: () => zhCN.fd.tasksTitle,
  detail: () => zhCN.fd.taskDetailTitle,
  scheduler: () => zhCN.fd.schedulerTitle,
}

function panelText(w: W, title: string): string {
  return panel(w, title)?.text() ?? ''
}

/** ★ 数据格的标签/取值（面板文本里含免责文案 ⇒ 一律按格作用域断言）。 */
function cellLabels(w: W, title: string): string[] {
  return (panel(w, title)?.findAll('.fd__cell-l') ?? []).map((n) => n.text())
}

function cellValues(w: W, title: string): string[] {
  return (panel(w, title)?.findAll('.fd__cell-v') ?? []).map((n) => n.text())
}

/** ★ 面板里某个节点集合的文本（用于精确措辞断言，避开跨面板误判）。 */
function nodeTexts(w: W, title: string, selector: string): string[] {
  return (panel(w, title)?.findAll(selector) ?? []).map((n) => n.text())
}

/**
 * ★★ 含 `{占位符}` 的词典值**不能整串匹配渲染文本** —— 渲染后占位符已被替换成实参，
 *   字面量对不上（本轮 8 条用例都栽在这）。这里取第一个 `{` 之前的**字面量部分**
 *   做前缀/子串断言，仍然不手打文案。
 */
function head(v: string): string {
  const i = v.indexOf('{')
  return (i < 0 ? v : v.slice(0, i)).trim()
}

async function clickPanelButton(w: W, title: string, label: string): Promise<void> {
  const btn = (panel(w, title)?.findAll('button') ?? []).find((b) => b.text() === label)
  expect(btn, `${title} 面板里找不到按钮「${label}」`).toBeTruthy()
  await btn!.trigger('click')
  await flushPromises()
  await flushPromises()
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
})

// ══ 档位与接线 ═════════════════════════════════════════════════════════

describe('档位与接线', () => {
  it('★★ 抽屉席存在且**故意不设** requiresRole（六条只读全是 h.admin）', () => {
    const item = DRAWER_NAV.find((i) => i.key === 'free-discovery')
    expect(item).toBeTruthy()
    expect(item!.to).toBe('/free-discovery')
    expect(item!.titleKey).toBe('nav.freeDiscovery')
    // ★ 设成 super_admin 会把 admin 档页面挡在门外 —— 判据必须红
    expect(item!.requiresRole).toBeUndefined()
  })

  it('★ 抽屉席在 tenant_admin 下可见（admin 档不因角色被过滤）', () => {
    const superOnly = DRAWER_NAV.filter((i) => i.requiresRole === 'super_admin').map((i) => i.key)
    expect(superOnly).not.toContain('free-discovery')
  })

  it('路由注册在 /free-discovery，标题键取自词典', () => {
    const r = router.getRoutes().find((x) => x.name === 'free-discovery')
    expect(r).toBeTruthy()
    expect(r!.path).toBe('/free-discovery')
    const meta = r!.meta as { titleKey?: string; requiresAuth?: boolean } | undefined
    expect(meta?.titleKey).toBe('fd.title')
    expect(meta?.requiresAuth).toBe(true)
  })

  it('★★ 本页只读：没有任何写操作按钮', async () => {
    const w = await mountView()
    // ★★ 断言必须落到**按钮**上：只读说明那句话里本身就列了「触发扫描 / 批量导入 /
    //   删模板」这些词，全页 `not.toContain` 会被自己的免责声明喂饱 ⇒ 恒被判红。
    const btnTexts = w.findAll('button').map((b) => b.text())
    expect(btnTexts.length).toBeGreaterThan(0)
    for (const verb of ['创建', '新建', '删除', '移除', '扫描', '导入', '保存', '修改', '停用', '启用']) {
      expect(
        btnTexts.some((t) => t.includes(verb)),
        `不该有写操作按钮（含「${verb}」）：${btnTexts.join(' / ')}`,
      ).toBe(false)
    }
    // ★ 只读说明必须在
    expect(w.text()).toContain(zhCN.fd.readOnlyNote)
  })

  it('★ 七条端点在 api 模块里都存在（本页只调这些）', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    await clickPanelButton(w, P.presets(), zhCN.fd.presetsRun)
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    expect(templatesMock).toHaveBeenCalled()
    expect(presetsMock).toHaveBeenCalled()
    expect(tasksMock).toHaveBeenCalled()
    expect(schedulerMock).toHaveBeenCalled()
  })
})

// ══ 模板面板 ═══════════════════════════════════════════════════════════

describe('模板面板', () => {
  it('加载成功后渲染模板行与数据格', async () => {
    const w = await mountView({ templates: { templates: [templateRow(), templateRow({ provider_code: 'zhipu', id: 13 })] } })
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    const p = panelText(w, P.templates())
    expect(p).toContain('groq')
    expect(p).toContain('zhipu')
    expect(cellLabels(w, P.templates())).toContain(zhCN.fd.apiType)
    expect(cellValues(w, P.templates())).toContain('openai-completions')
  })

  it('★ 勾上「只看启用项」后才把 enabledOnly=true 发出去', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(templatesMock).toHaveBeenLastCalledWith({ enabledOnly: false })
    await w.find('#fd-enabled').setValue(true)
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(templatesMock).toHaveBeenLastCalledWith({ enabledOnly: true })
  })

  it('只认字面 true 的免责说明常驻', async () => {
    const w = await mountView()
    expect(panelText(w, P.templates())).toContain(zhCN.fd.enabledLiteralNote)
  })

  it('503 时显示「依赖没起来」说明', async () => {
    const w = await mountView({ templates: new Error('free-discovery is not available (database disabled)') })
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(panelText(w, P.templates())).toContain(zhCN.fd.depsMissingNote)
  })

  it('★★ 500 时区分「这一族两个 500 文案」里的第一句', async () => {
    const w = await mountView({ templates: new Error('internal error (see server logs)') })
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    const p = panelText(w, P.templates())
    expect(p).toContain(zhCN.fd.templatesInternalErrorNote)
    expect(nodeTexts(w, P.templates(), '.fd__note')).not.toContain(zhCN.fd.requestFailedNote)
  })

  it('★★ 抛错必须清空旧数据（先成功→再失败）', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(panelText(w, P.templates())).toContain('groq')
    templatesMock.mockRejectedValueOnce(new Error('boom'))
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    const p = panelText(w, P.templates())
    expect(p).not.toContain('groq')
    expect(panel(w, P.templates())!.findAll('.fd__item')).toHaveLength(0)
  })

  it('空数组显示「确实没有模板」', async () => {
    const w = await mountView({ templates: { templates: [] } })
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(panelText(w, P.templates())).toContain(zhCN.fd.templatesEmptyNote)
  })

  it('★ api_key_env 为空 ⇒ 显示「密钥来源未能确定」', async () => {
    const w = await mountView({ templates: { templates: [templateRow({ api_key_env: '' })] } })
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(nodeTexts(w, P.templates(), '.fd__note')).toContain(zhCN.fd.credentialIndeterminateNote)
  })

  it('api_key_env 有值时**不**显示「未能确定」', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(nodeTexts(w, P.templates(), '.fd__note')).not.toContain(zhCN.fd.credentialIndeterminateNote)
  })

  it('★★ 自动停用与人工停用是两条不同的文案', async () => {
    const w = await mountView({
      templates: {
        templates: [
          templateRow({ id: 1, enabled: false, auto_disabled_at: '2026-09-10T01:05:00Z', consecutive_scan_failures: 5 }),
          templateRow({ id: 2, provider_code: 'zhipu', enabled: false }),
        ],
      },
    })
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    const notes = nodeTexts(w, P.templates(), '.fd__note')
    expect(notes.some((n) => n.includes(head(zhCN.fd.autoDisabledNote)))).toBe(true)
    expect(notes).toContain(zhCN.fd.manuallyDisabledNote)
  })

  it('★ 失败计数不为 0 但没有时间字段 ⇒ 单独一条说明（omitempty 键缺失 ≠ null）', async () => {
    const w = await mountView({ templates: { templates: [templateRow({ consecutive_scan_failures: 3 })] } })
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(nodeTexts(w, P.templates(), '.fd__note')).toContain(zhCN.fd.failureCountNoTimestampNote)
  })

  it('失败计数为 0 时**不**显示那条说明', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(nodeTexts(w, P.templates(), '.fd__note')).not.toContain(zhCN.fd.failureCountNoTimestampNote)
  })

  it('★ created_at 为零值时间 ⇒ 单独一条说明', async () => {
    const w = await mountView({ templates: { templates: [templateRow({ created_at: GO_ZERO_TIME })] } })
    await clickPanelButton(w, P.templates(), zhCN.fd.templatesRun)
    expect(nodeTexts(w, P.templates(), '.fd__note')).toContain(zhCN.fd.createdAtZeroNote)
  })
})

// ══ 预设面板 ═══════════════════════════════════════════════════════════

describe('预设面板（null 与空数组是两种表示）', () => {
  it('★★ presets 为 null 显示「后端返回的是空值」，走 data-fd="presets-null"', async () => {
    const w = await mountView({ presets: { presets: null } })
    await clickPanelButton(w, P.presets(), zhCN.fd.presetsRun)
    expect(panel(w, P.presets())!.find('[data-fd="presets-null"]').exists()).toBe(true)
    expect(panel(w, P.presets())!.find('[data-fd="presets-empty"]').exists()).toBe(false)
    expect(panelText(w, P.presets())).toContain(zhCN.fd.presetsNullResult)
  })

  it('★★★ presets 为空数组显示「确实没有预设」，走 data-fd="presets-empty"', async () => {
    const w = await mountView({ presets: { presets: [] } })
    await clickPanelButton(w, P.presets(), zhCN.fd.presetsRun)
    expect(panel(w, P.presets())!.find('[data-fd="presets-empty"]').exists()).toBe(true)
    expect(panel(w, P.presets())!.find('[data-fd="presets-null"]').exists()).toBe(false)
    expect(panelText(w, P.presets())).toContain(zhCN.fd.presetsEmptyNote)
  })

  it('★ null 与空数组的两条文案互不含对方', async () => {
    const wNull = await mountView({ presets: { presets: null } })
    await clickPanelButton(wNull, P.presets(), zhCN.fd.presetsRun)
    expect(panelText(wNull, P.presets())).not.toContain(zhCN.fd.presetsEmptyNote)
  })

  it('★ 预设条目渲染，但条款地址那个字段拿不到（7 键结构体）', async () => {
    const w = await mountView({ presets: { presets: [presetRow()] } })
    await clickPanelButton(w, P.presets(), zhCN.fd.presetsRun)
    expect(panelText(w, P.presets())).toContain('groq')
    // ★「条款地址」是模板的字段标签，预设结构体里没有这个键
    expect(cellLabels(w, P.presets())).not.toContain(zhCN.fd.tosUrl)
    expect(panelText(w, P.presets())).toContain(zhCN.fd.presetsNoTosUrlNote)
  })

  it('503 时显示「依赖没起来」', async () => {
    const w = await mountView({ presets: new Error('free-discovery is not available (database disabled)') })
    await clickPanelButton(w, P.presets(), zhCN.fd.presetsRun)
    expect(panelText(w, P.presets())).toContain(zhCN.fd.depsMissingNote)
  })

  it('★★ 先成功→再失败：预设回到「未加载」，不许把失败显示成 null 态', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.presets(), zhCN.fd.presetsRun)
    expect(panelText(w, P.presets())).toContain('groq')
    presetsMock.mockRejectedValueOnce(new Error('boom'))
    await clickPanelButton(w, P.presets(), zhCN.fd.presetsRun)
    const p = panel(w, P.presets())!
    expect(p.find('[data-fd="presets-null"]').exists()).toBe(false)
    expect(p.find('[data-fd="presets-empty"]').exists()).toBe(false)
    expect(panelText(w, P.presets())).not.toContain('groq')
  })
})

// ══ 任务列表面板 ═══════════════════════════════════════════════════════

describe('任务列表面板（updated_at 读不出来）', () => {
  it('★★★★★★★★★ 每行都显示「最后更新读不出来」，绝不显示那个零值时间', async () => {
    const w = await mountView({ tasks: { tasks: [taskFromList({ id: 1 }), taskFromList({ id: 2 })] } })
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(panelText(w, P.tasks())).toContain(zhCN.fd.tasksUpdatedAtNote)
    const marks = panel(w, P.tasks())!.findAll('[data-fd="updated-at-unreadable"]')
    expect(marks).toHaveLength(2)
    // ★★ 零值时间**不许**出现在任何数据格里。
    //   注意断言必须落到 `.fd__cell-v`：免责文案 `tasksUpdatedAtNote` 本身
    //   就写了「（0001-01-01）」，全面板 `not.toContain` 会被自己喂饱 ⇒ 恒被判红。
    expect(cellValues(w, P.tasks()).filter((v) => v.includes('0001-01-01'))).toEqual([])
    expect(panel(w, P.tasks())!.findAll('.fd__item')[0]!.findAll('.fd__cell-v').map((n) => n.text())).not.toContain(
      GO_ZERO_TIME,
    )
  })

  it('★★ 任务列表里 updated_at 是真值时也不显示（那是详情端的形态）', async () => {
    const w = await mountView({ tasks: { tasks: [taskFromList({ updated_at: '2026-09-09T02:00:09Z' })] } })
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    // 本批缺陷是「列表端恒为零值」；这里注入真值，页面仍按列表语义不给「最后更新」栏位
    expect(cellLabels(w, P.tasks())).not.toContain(zhCN.fd.updatedAt)
    expect(panel(w, P.tasks())!.findAll('[data-fd="updated-at-unreadable"]')).toHaveLength(0)
  })

  it('★★★ limit 填 201 ⇒ 提示被改写成 50（回落不是 clamp）', async () => {
    const w = await mountView()
    await w.find('#fd-limit').setValue('201')
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    const notes = nodeTexts(w, P.tasks(), '.fd__note')
    expect(notes.some((n) => n.includes(head(zhCN.fd.limitRewrittenNote)))).toBe(true)
    expect(tasksMock).toHaveBeenLastCalledWith({ limit: '201' })
  })

  it('★ limit 填 200 不提示改写（200 合法）', async () => {
    const w = await mountView()
    await w.find('#fd-limit').setValue('200')
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(nodeTexts(w, P.tasks(), '.fd__note').some((n) => n.includes(head(zhCN.fd.limitRewrittenNote)))).toBe(
      false,
    )
  })

  it('limit 留空 ⇒ 不带查询参数，实际生效 50', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(tasksMock).toHaveBeenLastCalledWith(undefined)
    expect(panelText(w, P.tasks())).toContain('50')
  })

  it('limit 填非数字 ⇒ 也提示被改写', async () => {
    const w = await mountView()
    await w.find('#fd-limit').setValue('abc')
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(nodeTexts(w, P.tasks(), '.fd__note').some((n) => n.includes(head(zhCN.fd.limitRewrittenNote)))).toBe(true)
  })

  it('503 与另一种 500 文案各有各的说明', async () => {
    const wDown = await mountView({ tasks: new Error('free-discovery is not available (database disabled)') })
    await clickPanelButton(wDown, P.tasks(), zhCN.fd.tasksRun)
    expect(panelText(wDown, P.tasks())).toContain(zhCN.fd.depsMissingNote)

    const wErr = await mountView({ tasks: new Error('free discovery request failed') })
    await clickPanelButton(wErr, P.tasks(), zhCN.fd.tasksRun)
    const p = panelText(wErr, P.tasks())
    expect(p).toContain(zhCN.fd.requestFailedNote)
    expect(p).not.toContain(zhCN.fd.templatesInternalErrorNote)
  })

  it('★★ 抛错必须清空旧任务（先成功→再失败）', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(panel(w, P.tasks())!.findAll('.fd__item').length).toBeGreaterThan(0)
    tasksMock.mockRejectedValueOnce(new Error('boom'))
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(panel(w, P.tasks())!.findAll('.fd__item')).toHaveLength(0)
  })

  it('template_id 为 null 时显示「模板已删」，否则显示数字', async () => {
    const w = await mountView({
      tasks: { tasks: [taskFromList({ id: 1, template_id: null }), taskFromList({ id: 2, template_id: 12 })] },
    })
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(panelText(w, P.tasks())).toContain(zhCN.fd.templateDeleted)
    expect(cellValues(w, P.tasks())).toContain('12')
  })

  it('任务报错时显示报错行', async () => {
    const w = await mountView({ tasks: { tasks: [taskFromList({ error_message: 'upstream 401' })] } })
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(nodeTexts(w, P.tasks(), '.fd__note').some((n) => n.includes(head(zhCN.fd.taskErrorLine)))).toBe(true)
  })

  it('无报错时不显示报错行', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    expect(nodeTexts(w, P.tasks(), '.fd__note').some((n) => n.includes(head(zhCN.fd.taskErrorLine)))).toBe(false)
  })
})

// ══ 任务详情面板 ═══════════════════════════════════════════════════════

describe('任务详情面板（updated_at 是真值）', () => {
  async function openDetail(w: W, id = '7'): Promise<void> {
    await w.find('#fd-task').setValue(id)
    await clickPanelButton(w, P.detail(), zhCN.fd.taskDetailRun)
  }

  it('★ 详情端点的 updated_at 显示真值，与列表形成对照', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.tasks(), zhCN.fd.tasksRun)
    await openDetail(w)
    const labels = cellLabels(w, P.detail())
    const values = cellValues(w, P.detail())
    expect(labels).toContain(zhCN.fd.updatedAt)
    expect(values).toContain('2026-09-09T02:00:09Z')
    // ★ 同一个页面上，列表那一侧不许出现真值，详情这一侧不许显示「读不出来」
    expect(panelText(w, P.tasks())).not.toContain('2026-09-09T02:00:09Z')
    expect(cellValues(w, P.detail())).not.toContain(zhCN.fd.updatedAtZero)
  })

  it('★ 详情里的 updated_at 也是零值时才显示「读不出来」', async () => {
    const w = await mountView({ taskDetail: taskDetailRow({ updated_at: GO_ZERO_TIME }) })
    await openDetail(w)
    expect(cellValues(w, P.detail())).toContain(zhCN.fd.updatedAtZero)
  })

  it('template_id 为 null ⇒ 详情显示「模板已删」', async () => {
    const w = await mountView({ taskDetail: taskDetailRow({ template_id: null }) })
    await openDetail(w)
    expect(cellValues(w, P.detail())).toContain(zhCN.fd.templateDeleted)
  })

  it('可空时间/空串渲染成占位符，不显示 null', async () => {
    const w = await mountView({
      taskDetail: taskDetailRow({ started_at: null, completed_at: null, triggered_by: '' }),
    })
    await openDetail(w)
    const values = cellValues(w, P.detail())
    expect(values).toContain('—')
    expect(panelText(w, P.detail())).not.toContain('null')
  })

  it('★ id 非数字是 400 ⇒ 显示专门的说明（不是「没找到」）', async () => {
    const w = await mountView({ taskDetail: new Error('invalid task id') })
    await openDetail(w)
    expect(nodeTexts(w, P.detail(), '.fd__note')).toContain(zhCN.fd.invalidTaskIdNote)
  })

  it('任务号为空时详情/结果两个按钮都禁用', async () => {
    const w = await mountView()
    const btns = panel(w, P.detail())!.findAll('button')
    const [detailBtn, resultsBtn] = btns
    expect(detailBtn!.attributes('disabled')).toBeDefined()
    expect(resultsBtn!.attributes('disabled')).toBeDefined()
  })

  it('任务号非空时两个按钮解禁', async () => {
    const w = await mountView()
    await w.find('#fd-task').setValue('7')
    for (const b of panel(w, P.detail())!.findAll('button')) {
      expect(b.attributes('disabled')).toBeUndefined()
    }
  })

  it('★★ 详情抛错必须清空（先成功→再失败）', async () => {
    const w = await mountView()
    await openDetail(w)
    expect(cellValues(w, P.detail())).toContain('2026-09-09T02:00:09Z')
    taskDetailMock.mockRejectedValueOnce(new Error('boom'))
    await clickPanelButton(w, P.detail(), zhCN.fd.taskDetailRun)
    expect(cellValues(w, P.detail())).not.toContain('2026-09-09T02:00:09Z')
  })
})

// ══ 结果面板 ═══════════════════════════════════════════════════════════

describe('发现结果面板', () => {
  async function openResults(w: W, status = 'pending'): Promise<void> {
    await w.find('#fd-task').setValue('7')
    await w.find('#fd-status').setValue(status)
    await clickPanelButton(w, P.detail(), zhCN.fd.resultsRun)
  }

  it('★ 默认过滤值是 pending（留空时后端回落）', async () => {
    const w = await mountView()
    await openResults(w)
    expect(panelText(w, P.detail())).toContain('pending')
  })

  it('★ 未知过滤值在**请求之前**就先提示（后端不会拒它）', async () => {
    const w = await mountView()
    await w.find('#fd-task').setValue('7')
    await w.find('#fd-status').setValue('bogus')
    expect(nodeTexts(w, P.detail(), '.fd__note').some((n) => n.includes(head(zhCN.fd.statusUnknownNote)))).toBe(true)
  })

  it('★ all 表示不过滤 ⇒ 显示不过滤说明、且不显示未知提示', async () => {
    const w = await mountView()
    await openResults(w, 'all')
    const notes = nodeTexts(w, P.detail(), '.fd__note')
    expect(notes).toContain(zhCN.fd.statusUnfilteredNote)
    expect(notes.some((n) => n.includes(head(zhCN.fd.statusUnknownNote)))).toBe(false)
  })

  it('★★★ 空结果显示「证明不了没有」，不是「没有」', async () => {
    const w = await mountView({ results: { results: [] } })
    await openResults(w)
    expect(nodeTexts(w, P.detail(), '.fd__note')).toContain(zhCN.fd.resultsEmptyNote)
  })

  it('★ free_type 为空串 ⇒ 确定的「推断不出」（不是二义）', async () => {
    const w = await mountView({ results: { results: [resultRow({ free_type: '' })] } })
    await openResults(w)
    expect(cellValues(w, P.detail())).toContain(zhCN.fd.freeTypeUnknown)
  })

  it('free_type 有值时显示取值，不显示「推断不出」', async () => {
    const w = await mountView()
    await openResults(w)
    expect(cellValues(w, P.detail())).toContain('recurring-daily')
    expect(cellValues(w, P.detail())).not.toContain(zhCN.fd.freeTypeUnknown)
  })

  it('★★ 为 0 的字段逐个点名（上下文/上限/月度/每日）', async () => {
    const w = await mountView({
      results: {
        results: [resultRow({ context_window: 0, max_tokens: 0, monthly_tokens: 0, daily_tokens: 0 })],
      },
    })
    await openResults(w)
    const notes = nodeTexts(w, P.detail(), '.fd__note')
    const hit = notes.find((n) => n.includes(head(zhCN.fd.zeroAmbiguousNote)))
    expect(hit).toBeTruthy()
    for (const f of ['context_window', 'max_tokens', 'monthly_tokens', 'daily_tokens']) {
      expect(hit, `应点名 ${f}`).toContain(f)
    }
  })

  it('没有 0 字段时**不**显示二义说明', async () => {
    const w = await mountView({
      results: { results: [resultRow({ monthly_tokens: 100, daily_tokens: 100 })] },
    })
    await openResults(w)
    expect(nodeTexts(w, P.detail(), '.fd__note').some((n) => n.includes(head(zhCN.fd.zeroAmbiguousNote)))).toBe(false)
  })

  it('imported_at 为 null ⇒ 显示未导入', async () => {
    const w = await mountView()
    await openResults(w)
    expect(nodeTexts(w, P.detail(), '.fd__note')).toContain(zhCN.fd.notImportedNote)
  })

  it('已导入时不显示未导入说明', async () => {
    const w = await mountView({ results: { results: [resultRow({ imported_at: '2026-09-10T00:00:00Z' })] } })
    await openResults(w)
    expect(nodeTexts(w, P.detail(), '.fd__note')).not.toContain(zhCN.fd.notImportedNote)
  })

  it('pool_key 为空串时提示不共享额度池', async () => {
    const w = await mountView()
    await openResults(w)
    expect(panelText(w, P.detail())).toContain(zhCN.fd.noPoolNote)
  })

  it('★★ 结果抛错必须清空（先成功→再失败）', async () => {
    const w = await mountView()
    await openResults(w)
    expect(panelText(w, P.detail())).toContain('llama-3.3-70b-versatile')
    resultsMock.mockRejectedValueOnce(new Error('boom'))
    await clickPanelButton(w, P.detail(), zhCN.fd.resultsRun)
    expect(panelText(w, P.detail())).not.toContain('llama-3.3-70b-versatile')
  })
})

// ══ 调度器面板（superAdmin 档） ════════════════════════════════════════

describe('扫描调度器面板（superAdmin 档，单列 403）', () => {
  it('★ 成功渲染九个数据格', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    const labels = cellLabels(w, P.scheduler())
    for (const l of [
      zhCN.fd.schedEnabled,
      zhCN.fd.schedStarted,
      zhCN.fd.schedInterval,
      zhCN.fd.schedLastSweep,
      zhCN.fd.schedSweeps,
      zhCN.fd.schedScans,
      zhCN.fd.schedScansFailed,
      zhCN.fd.schedScansSkipped,
      zhCN.fd.schedCyclesFailed,
    ]) {
      expect(labels, `缺数据格 ${l}`).toContain(l)
    }
  })

  it('★★ 403（租户管理员打超管档）⇒ 显示专门的说明', async () => {
    const w = await mountView({
      scheduler: new Error('tenant_admin has read-only access; write operations require super_admin'),
    })
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    const notes = nodeTexts(w, P.scheduler(), '.fd__note')
    expect(notes.some((n) => n.includes(zhCN.fd.schedulerForbiddenNote))).toBe(true)
    // ★ 与 deps 的 503 说明不是同一条
    expect(notes).not.toContain(zhCN.fd.depsMissingNote)
  })

  it('★★ 503（调度器没接上）是另一句话，不等于五个面板的 503', async () => {
    const w = await mountView({ scheduler: new Error('scan-scheduler is not available') })
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    const notes = nodeTexts(w, P.scheduler(), '.fd__note')
    expect(notes).toContain(zhCN.fd.schedulerMissingNote)
    expect(notes).not.toContain(zhCN.fd.depsMissingNote)
  })

  it('★★★ typed-nil 分支：成功但全空 ⇒ 显示「两种表示」那条', async () => {
    const w = await mountView({ scheduler: schedulerTypedNil() })
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    expect(nodeTexts(w, P.scheduler(), '.fd__note')).toContain(zhCN.fd.schedulerTypedNilNote)
  })

  it('正常调度器**不**显示 typed-nil 那条', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    expect(nodeTexts(w, P.scheduler(), '.fd__note')).not.toContain(zhCN.fd.schedulerTypedNilNote)
  })

  it('★ enabled 开但 started 关 ⇒ 单独一条（两个字段是两件事）', async () => {
    const w = await mountView({ scheduler: schedulerRow({ enabled: true, started: false }) })
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    expect(nodeTexts(w, P.scheduler(), '.fd__note')).toContain(zhCN.fd.schedulerEnabledNotStartedNote)
  })

  it('last_error 存在 ⇒ 显示错误行；缺省时不显示', async () => {
    const wErr = await mountView({ scheduler: schedulerRow({ last_error: 'upstream timeout' }) })
    await clickPanelButton(wErr, P.scheduler(), zhCN.fd.schedulerRun)
    expect(
      nodeTexts(wErr, P.scheduler(), '.fd__note').some((n) => n.includes(head(zhCN.fd.schedulerLastErrorLine))),
    ).toBe(true)

    const wOk = await mountView()
    await clickPanelButton(wOk, P.scheduler(), zhCN.fd.schedulerRun)
    expect(
      nodeTexts(wOk, P.scheduler(), '.fd__note').some((n) => n.includes(head(zhCN.fd.schedulerLastErrorLine))),
    ).toBe(false)
  })

  it('★★ 抛错必须清空（先成功→再失败）', async () => {
    const w = await mountView()
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    expect(cellValues(w, P.scheduler())).toContain('6h0m0s')
    schedulerMock.mockRejectedValueOnce(new Error('boom'))
    await clickPanelButton(w, P.scheduler(), zhCN.fd.schedulerRun)
    expect(cellValues(w, P.scheduler())).not.toContain('6h0m0s')
  })

  it('★ 这一条不查数据库依赖的说明常驻（六条 503 时它仍可能 200）', async () => {
    const w = await mountView()
    expect(panelText(w, P.scheduler())).toContain(zhCN.fd.schedulerSkipsDepsNote)
  })
})
