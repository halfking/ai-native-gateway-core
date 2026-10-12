import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  // 取数
  fetchTemplates,
  fetchPresets,
  fetchTemplateById,
  fetchTasks,
  fetchTaskById,
  fetchTaskResults,
  fetchScanSchedulerStatus,
  // 解包
  unwrapTemplatesList,
  unwrapPresets,
  unwrapTemplate,
  unwrapTasksList,
  unwrapTask,
  unwrapResultsList,
  unwrapScanSchedulerStatus,
  // 路径
  FD_TEMPLATES_PATH,
  FD_PRESETS_PATH,
  FD_TASKS_PATH,
  FD_SCAN_SCHEDULER_PATH,
  fdTemplateByIdPath,
  fdTaskByIdPath,
  fdTemplatesPath,
  fdTasksPath,
  fdTaskResultsPath,
  // 键集合
  FD_TEMPLATE_REQUIRED_KEYS,
  FD_TEMPLATE_OPTIONAL_KEYS,
  FD_TEMPLATE_NEVER_KEY,
  FD_TASK_REQUIRED_KEYS,
  FD_RESULT_REQUIRED_KEYS,
  FD_PRESET_KEYS,
  FD_SCHEDULER_REQUIRED_KEYS,
  FD_SCHEDULER_OPTIONAL_KEYS,
  FD_TASK_LIMIT_DEFAULT,
  FD_TASK_LIMIT_MAX_LEGAL,
  FD_RESULT_AMBIGUOUS_ZERO_FIELDS,
  TEMPLATES_ENABLED_PARAM,
  // 判读
  templatesEnabledFilterApplies,
  fdTaskLimitEffective,
  taskLimitWasRewritten,
  resultsStatusParam,
  resultsStatusIsUnfiltered,
  resultsStatusIsKnown,
  resultsEmptyIsIndeterminate,
  GO_ZERO_TIME,
  isGoZeroTime,
  createdAtIsUnreadable,
  templateCredentialIsIndeterminate,
  templateAutoDisabled,
  templateManuallyDisabled,
  templateHasScanFailures,
  templateHasLastScanFailureAt,
  taskUpdatedAtIsUnreadable,
  taskListUpdatedAtIsAlwaysZero,
  taskTemplateDeleted,
  taskIsFinished,
  taskIsInFlight,
  taskHasErrorMessage,
  taskFoundNothing,
  resultFreeTypeUninferable,
  resultZeroFields,
  resultNotImported,
  resultSharesNoPool,
  schedulerIntervalUnset,
  schedulerNeverSwept,
  schedulerLastErrorMissing,
  schedulerProviderWasTypedNil,
  schedulerIdleCounters,
  schedulerEnabledButNotStarted,
  // 错误文案
  fdDepsMissingMessage,
  scanSchedulerMissingMessage,
  fdTemplatesInternalErrorMessage,
  fdRequestFailedMessage,
  invalidTemplateIdMessage,
  invalidTaskIdMessage,
  templateNotFoundMessage,
  taskNotFoundMessage,
  taskNotFoundId,
  tenantAdminWriteForbiddenMessage,
  schedulerAnswersWhileDepsAreDown,
  type FdTemplate,
  type FdTask,
  type FdResult,
  type FdPreset,
  type FdScanSchedulerStatus,
} from './freeDiscovery'

/**
 * 免费资源自动发现读面的契约测试（第四十八批，2026-10-07）。
 *
 * 后端逐条对应：
 *   admin/handler.go:1302-1317                          7 条只读 GET；★1316 是 superAdmin
 *   admin/free_discovery.go:83-89                       scheduler 探针（★唯一不查 fdDeps）
 *   admin/free_discovery.go:92-98                       fdDeps ⇒ 503
 *   admin/free_discovery.go:100-103                     fdTenant = EffectiveTenantID
 *   admin/free_discovery.go:108-146                     templates（nil-guard 在 :121-123）
 *   admin/free_discovery.go:149-198                     templates/{id}（ParseInt ⇒ 400）
 *   admin/free_discovery.go:201-235                     presets（★var out 无 nil-guard）
 *   admin/free_discovery.go:366-387                     tasks（★Atoi 的 error 被丢弃）
 *   admin/free_discovery.go:392-412                     tasks/{id}（404 带 id）
 *   admin/free_discovery.go:415-443                     tasks/{id}/results
 *   domains/freediscovery/types.go:85-107               ProviderTemplate（★2 个 omitempty）
 *   domains/freediscovery/types.go:149-164              DiscoveryTask（template_id 可空）
 *   domains/freediscovery/types.go:191-209              DiscoveryResult
 *   domains/freediscovery/template_manager.go:127-153   Get：404 sentinel 不带 id
 *   domains/freediscovery/template_manager.go:401-438   scanTemplate：Get 与 List 同列
 *   domains/freediscovery/discovery_engine.go:396-443   GetTask：SELECT **有** updated_at
 *   domains/freediscovery/discovery_engine.go:445-497   ListTasks：SELECT **漏** updated_at
 *   domains/freediscovery/discovery_engine.go:499-548   ListResults：SELECT **无** tenant_id
 *   bg/scan_scheduler.go:492-530                        ScanSchedulerStatus
 *   admin/context.go:60-65 / 79-88                      EffectiveTenantID / 按方法分档
 *   sql/migrations/084-freediscovery-schema.sql         CHECK 取值集合
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => (typeof body === 'string' ? body : JSON.stringify(body)),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastCall(): { url: string; init: unknown } {
  const c = fetchMock.mock.calls.at(-1)!
  return { url: String(c[0]), init: c[1] }
}

function lastUrl(): string {
  return lastCall().url
}

function lastMethod(): string {
  return String((lastCall().init as { method?: string } | undefined)?.method ?? '')
}

/** ★ 走一遍 JSON 序列化再回来 —— 后端发的就是 JSON，这一步不能省。 */
function wire<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

// ── 夹具：逐字照抄后端结构体的 json tag ───────────────────────────────

/**
 * ★ `scanTemplate` 的 21 个扫描位里，`api_key_encrypted` 是 `json:"-"`
 *   ⇒ 序列化后**没有**这个键（types.go:93）。
 * ★ `last_scan_failure_at` / `auto_disabled_at` 是 `*time.Time` + `omitempty`
 *   ⇒ SQL NULL 时**整个键缺失**（types.go:105-106）。
 */
function template(over: Record<string, unknown> = {}): FdTemplate {
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
    tos_notes: 'Free tier documented (RPM/RPD/TPD limits)',
    enabled: true,
    created_by: 'alice',
    created_at: '2026-09-09T02:00:00Z',
    updated_at: '2026-09-09T03:00:00Z',
    consecutive_scan_failures: 0,
    ...over,
  }) as FdTemplate
}

/** ★ 带两个 `omitempty` 键的变体（曾经失败过 / 被自动停用）。 */
function templateWithHealth(over: Record<string, unknown> = {}): FdTemplate {
  return template({
    last_scan_failure_at: '2026-09-10T01:00:00Z',
    auto_disabled_at: '2026-09-10T01:05:00Z',
    enabled: false,
    consecutive_scan_failures: 5,
    ...over,
  })
}

/** ★ `presetView` 只有 7 个键（admin/free_discovery.go:209-217），**没有** `tos_url`。 */
function preset(over: Record<string, unknown> = {}): FdPreset {
  return wire({
    provider_code: 'groq',
    display_name: 'Groq Cloud (Free Tier)',
    base_url: 'https://api.groq.com/openai/v1',
    api_type: 'openai-completions',
    api_key_env: '$GROQ_API_KEY',
    tos_verdict: 'caution',
    tos_notes: 'Free tier documented (RPM/RPD/TPD limits); production proxy of free tier is a gray area — review before scale',
    ...over,
  }) as FdPreset
}

/** ★ 详情端点的任务：`GetTask` 的 SELECT **有** `updated_at` ⇒ 真值。 */
function taskDetail(over: Record<string, unknown> = {}): FdTask {
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

/**
 * ★★★ 列表端点的任务：`ListTasks` 的 SELECT **漏了 `updated_at`**
 *   （discovery_engine.go:460-464）⇒ `UpdatedAt` 停在 Go 零值。
 *   而 `started_at` / `completed_at` / `template_id` 都是 `*T` 且**无 omitempty**
 *   ⇒ 键恒存在，未跑的任务是 `null`。
 */
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

function result(over: Record<string, unknown> = {}): FdResult {
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

/** ★ `ScanSchedulerStatus` 的 9 个恒存在键；`last_error` 是 `omitempty`。 */
function scheduler(over: Record<string, unknown> = {}): FdScanSchedulerStatus {
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

/** ★★ typed-nil provider 分支（`bg/scan_scheduler.go:509-511`）：全零 + `interval:""`。 */
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

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// ══ templates ═════════════════════════════════════════════════════════

describe('free-discovery/templates 列表端点', () => {
  it('GET 合法信封解出模板数组', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ templates: [template(), templateWithHealth()] }))
    const r = await fetchTemplates()
    expect(r.templates).toHaveLength(2)
    expect(r.templates[0]!.provider_code).toBe('groq')
    // ★ omitempty 键缺失 ≠ null
    expect('last_scan_failure_at' in r.templates[0]!).toBe(false)
    expect('auto_disabled_at' in r.templates[0]!).toBe(false)
    expect(r.templates[1]!.auto_disabled_at).toBe('2026-09-10T01:05:00Z')
  })

  it('空列表是空数组（nil-guard 保证永不为 null）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ templates: [] }))
    const r = await fetchTemplates()
    expect(r.templates).toEqual([])
  })

  it('★ templates 为 null 抛错：后端有 nil-guard，null 不可能是合法响应', () => {
    expect(() => unwrapTemplatesList({ templates: null })).toThrow(/永不为 null/)
  })

  it('★ 同族四种「空」形态相反：presets 为 null 合法，templates 为 null 不合法', () => {
    expect(() => unwrapPresets({ presets: null })).not.toThrow()
    expect(() => unwrapTemplatesList({ templates: null })).toThrow()
  })

  it('把 presets 信封喂给 templates 解包器必须抛错', () => {
    expect(() => unwrapTemplatesList({ presets: [preset()] })).toThrow(/形状不符/)
  })

  it('把 templates 信封喂给 presets 解包器必须抛错', () => {
    expect(() => unwrapPresets({ templates: [template()] })).toThrow(/形状不符/)
  })

  it('非对象 / null / 裸数组都抛错', () => {
    expect(() => unwrapTemplatesList(null)).toThrow(/实得 null/)
    expect(() => unwrapTemplatesList([])).toThrow(/实得 array/)
    expect(() => unwrapTemplatesList('x')).toThrow(/实得 string/)
    expect(() => unwrapTemplatesList(7)).toThrow(/实得 number/)
    expect(() => unwrapTemplatesList({})).toThrow(/形状不符/)
  })

  it('★ 缺 17 个必填键中的任何一个都必须抛错', () => {
    for (const k of FD_TEMPLATE_REQUIRED_KEYS) {
      const item = { ...template() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapTemplatesList({ templates: [item] }), `缺 ${k} 时应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★ 两个 omitempty 键不在必填集合里（缺它们合法）', () => {
    for (const k of FD_TEMPLATE_OPTIONAL_KEYS) {
      expect(FD_TEMPLATE_REQUIRED_KEYS as readonly string[]).not.toContain(k)
      const item = { ...template() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapTemplatesList({ templates: [item] }), `缺 ${k} 合法`).not.toThrow()
    }
  })

  it('★ api_key_encrypted 是 json:"-" ⇒ 出现它就抛错', () => {
    const item = { ...template(), api_key_encrypted: 'AAAA' } as Record<string, unknown>
    expect(() => unwrapTemplatesList({ templates: [item] })).toThrow(/json:"-" 从不下发/)
  })

  it('类型不符（id 字符串 / enabled 缺失类型 / 失败计数非数）抛错', () => {
    expect(() => unwrapTemplatesList({ templates: [{ ...template(), id: '12' }] })).toThrow(/形状不符/)
    expect(() => unwrapTemplatesList({ templates: [{ ...template(), enabled: 'true' }] })).toThrow(/形状不符/)
    expect(() =>
      unwrapTemplatesList({ templates: [{ ...template(), consecutive_scan_failures: null }] }),
    ).toThrow(/形状不符/)
    expect(() => unwrapTemplatesList({ templates: [null] })).toThrow(/模板对象/)
    expect(() => unwrapTemplatesList({ templates: [[]] })).toThrow(/模板对象/)
  })

  it('★ enabled 只认字面 "true"：其余取值一律不过滤', () => {
    expect(templatesEnabledFilterApplies('true')).toBe(true)
    for (const v of ['1', 'TRUE', 'True', ' true', 'true ', 'yes', 'on', '', undefined]) {
      expect(templatesEnabledFilterApplies(v as string | undefined), `"${v}" 不该打开过滤`).toBe(false)
    }
  })

  it('只有 enabledOnly=true 才发查询参数', () => {
    expect(TEMPLATES_ENABLED_PARAM).toBe('true')
    expect(fdTemplatesPath({ enabledOnly: true })).toBe('/api/free-discovery/templates?enabled=true')
    expect(fdTemplatesPath({ enabledOnly: false })).toBe('/api/free-discovery/templates')
    expect(fdTemplatesPath()).toBe('/api/free-discovery/templates')
    expect(fdTemplatesPath({})).toBe('/api/free-discovery/templates')
  })

  it('enabledOnly=true 时真的把 enabled=true 发出去', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ templates: [] }))
    await fetchTemplates({ enabledOnly: true })
    expect(lastUrl()).toContain('enabled=true')
    expect(lastMethod()).toBe('GET')
  })
})

// ══ presets ═══════════════════════════════════════════════════════════

describe('free-discovery/templates/presets 预设端点', () => {
  it('★ presets 为 null 是合法响应，必须原样保留（降级成 [] 会丢掉这个区分）', () => {
    const r = unwrapPresets({ presets: null })
    expect(r.presets).toBeNull()
  })

  it('presets 数组正常解出', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ presets: [preset(), preset({ provider_code: 'openrouter' })] }),
    )
    const r = await fetchPresets()
    expect(r.presets).toHaveLength(2)
    expect(lastUrl()).toBe('/api/free-discovery/templates/presets')
  })

  it('★ presetView 只有 7 个键：tos_url 从不下发', () => {
    const r = unwrapPresets({ presets: [preset()] })
    expect(FD_PRESET_KEYS).toHaveLength(7)
    expect(r.presets![0]).not.toHaveProperty('tos_url')
    expect(Object.keys(r.presets![0]!).sort()).toEqual([...FD_PRESET_KEYS].sort())
  })

  it('★ 缺 7 个必填键中的任何一个都必须抛错', () => {
    for (const k of FD_PRESET_KEYS) {
      const item = { ...preset() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapPresets({ presets: [item] }), `缺 ${k} 时应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('presets 是对象 / 数字 / 无该键都抛错', () => {
    expect(() => unwrapPresets({ presets: {} })).toThrow(/形状不符/)
    expect(() => unwrapPresets({ presets: 3 })).toThrow(/形状不符/)
    expect(() => unwrapPresets({})).toThrow(/形状不符/)
    expect(() => unwrapPresets({ presets: [null] })).toThrow(/预设对象/)
  })
})

// ══ templates/{id} ════════════════════════════════════════════════════

describe('free-discovery/templates/{id} 详情端点', () => {
  it('★ 返回裸对象，没有信封', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(template()))
    const t = await fetchTemplateById(12)
    expect(t.id).toBe(12)
    expect(lastUrl()).toBe('/api/free-discovery/templates/12')
  })

  it('把列表信封喂给详情解包器必须抛错', () => {
    expect(() => unwrapTemplate({ templates: [template()] })).toThrow(/形状不符/)
  })

  it('把 presets 信封喂给详情解包器必须抛错', () => {
    expect(() => unwrapTemplate({ presets: [] })).toThrow(/形状不符/)
  })

  it('缺任一必填键抛错；api_key_encrypted 出现也抛错', () => {
    const noId = { ...template() } as Record<string, unknown>
    delete noId.provider_code
    expect(() => unwrapTemplate(noId)).toThrow(/缺 1 个/)
    expect(() => unwrapTemplate({ ...template(), api_key_encrypted: 'AA' })).toThrow(/json:"-"/)
  })

  it('路径对 id 做 encode', () => {
    expect(fdTemplateByIdPath('a b')).toBe('/api/free-discovery/templates/a%20b')
    expect(fdTemplateByIdPath('a/b')).toBe('/api/free-discovery/templates/a%2Fb')
  })
})

// ══ tasks ═════════════════════════════════════════════════════════════

describe('free-discovery/tasks 列表端点', () => {
  it('GET 合法信封解出任务数组', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ tasks: [taskFromList()] }))
    const r = await fetchTasks()
    expect(r.tasks).toHaveLength(1)
    expect(lastUrl()).toBe('/api/free-discovery/tasks')
  })

  it('tasks 为 null 抛错（有 nil-guard）', () => {
    expect(() => unwrapTasksList({ tasks: null })).toThrow(/永不为 null/)
  })

  it('把 tasks 信封喂给详情解包器必须抛错', () => {
    expect(() => unwrapTask({ tasks: [taskFromList()] })).toThrow(/形状不符/)
    // ★★ 报错文案里必须点明是**哪个端点**。变异 A9 把「详情解包」换成「先套列表信封再取第一项」，
    //   抛错时机一样、只是文案里的端点名从 `tasks/{id}` 变成 `tasks` ——
    //   没有这一条时它是**等价变异**（判据抓不到「解包器被换掉」）。
    expect(() => unwrapTask({ tasks: [taskFromList()] })).toThrow(/tasks\/\{id\}/)
  })

  it('★ 缺 14 个必填键中的任何一个都必须抛错', () => {
    for (const k of FD_TASK_REQUIRED_KEYS) {
      const item = { ...taskFromList() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapTasksList({ tasks: [item] }), `缺 ${k} 时应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★ template_id 为 null 是合法形状（模板被 ON DELETE SET NULL 删掉）', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ tasks: [taskFromList({ template_id: null, status: 'failed' })] }),
    )
    const r = await fetchTasks()
    expect(r.tasks[0]!.template_id).toBeNull()
    expect(taskTemplateDeleted(r.tasks[0]!)).toBe(true)
  })

  it('★ template_id 键缺失抛错，但值为 null 不抛错', () => {
    const missing = { ...taskFromList() } as Record<string, unknown>
    delete missing.template_id
    expect(() => unwrapTasksList({ tasks: [missing] })).toThrow(/缺 1 个/)
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), template_id: null }] })).not.toThrow()
    // ★ 按 typeof === 'number' 校验会把合法形状拒掉
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), template_id: '12' }] })).toThrow(
      /template_id:number \| null/,
    )
  })

  it('started_at / completed_at 键恒存在：可为 null，不可缺', () => {
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), started_at: null }] })).not.toThrow()
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), completed_at: null }] })).not.toThrow()
    for (const k of ['started_at', 'completed_at'] as const) {
      const item = { ...taskFromList() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapTasksList({ tasks: [item] }), `缺 ${k} 抛错`).toThrow(/缺 1 个/)
    }
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), started_at: 5 }] })).toThrow(/形状不符/)
  })

  it('计数类型不符抛错', () => {
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), models_found: '14' }] })).toThrow(/形状不符/)
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), models_imported: null }] })).toThrow(/形状不符/)
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), id: '7' }] })).toThrow(/形状不符/)
    expect(() => unwrapTasksList({ tasks: [{ ...taskFromList(), status: 7 }] })).toThrow(/形状不符/)
  })

  // ── ★★ 本批头号缺陷：ListTasks 的 SELECT 漏了 updated_at ──────────

  it('★★ 列表端点的 updated_at 恒为 Go 零值 —— 钉住 SELECT 漏列', () => {
    const t = taskFromList()
    expect(t.updated_at).toBe('0001-01-01T00:00:00Z')
    expect(isGoZeroTime(t.updated_at)).toBe(true)
    expect(taskUpdatedAtIsUnreadable(t)).toBe(true)
  })

  it('★★ 同一条任务的详情端点给的是真值 —— 两个端点两个值都 200', () => {
    const fromList = taskFromList()
    const fromDetail = taskDetail()
    expect(fromList.updated_at).not.toBe(fromDetail.updated_at)
    expect(taskUpdatedAtIsUnreadable(fromList)).toBe(true)
    expect(taskUpdatedAtIsUnreadable(fromDetail)).toBe(false)
  })

  it('★ 整个列表的 updated_at 全是零值 ⇒ 不能拿它排序', () => {
    const list = [taskFromList({ id: 1 }), taskFromList({ id: 2 }), taskFromList({ id: 3 })]
    expect(taskListUpdatedAtIsAlwaysZero(list)).toBe(true)
    expect(taskListUpdatedAtIsAlwaysZero([...list, taskDetail()])).toBe(false)
  })

  it('列表与详情解出的任务形状一致（字段集合相同）', () => {
    const fromList = unwrapTasksList({ tasks: [taskFromList()] }).tasks[0]!
    const fromDetail = unwrapTask(taskDetail())
    expect(Object.keys(fromList).sort()).toEqual(Object.keys(fromDetail).sort())
    expect(Object.keys(fromList).sort()).toEqual([...FD_TASK_REQUIRED_KEYS].sort())
  })

  // ── limit 语义 ───────────────────────────────────────────────────

  it('★ 缺省 / 空串 ⇒ 回落 50', () => {
    expect(FD_TASK_LIMIT_DEFAULT).toBe(50)
    expect(fdTaskLimitEffective()).toBe(50)
    expect(fdTaskLimitEffective(undefined)).toBe(50)
    expect(fdTaskLimitEffective(null)).toBe(50)
    expect(fdTaskLimitEffective('')).toBe(50)
  })

  it('★ 非数字 ⇒ Atoi 的 error 被丢弃 ⇒ 0 ⇒ 50', () => {
    for (const v of ['abc', '10.5', '1e3', '  ', '0x10', '５']) {
      expect(fdTaskLimitEffective(v), `"${v}" ⇒ 50`).toBe(50)
    }
  })

  it('★ <= 0 一律 50', () => {
    expect(fdTaskLimitEffective('0')).toBe(50)
    expect(fdTaskLimitEffective('-1')).toBe(50)
    expect(fdTaskLimitEffective('-999')).toBe(50)
    expect(fdTaskLimitEffective(0)).toBe(50)
    expect(fdTaskLimitEffective(-7)).toBe(50)
  })

  it('★ 1..200 原样生效', () => {
    expect(fdTaskLimitEffective('1')).toBe(1)
    expect(fdTaskLimitEffective('30')).toBe(30)
    expect(fdTaskLimitEffective(30)).toBe(30)
    expect(fdTaskLimitEffective('200')).toBe(200)
    expect(fdTaskLimitEffective(200)).toBe(200)
    expect(FD_TASK_LIMIT_MAX_LEGAL).toBe(200)
  })

  it('★★★ 超上界是**回落 50 而不是 clamp 到 200**：200 合法、201 变 50', () => {
    expect(fdTaskLimitEffective('201')).toBe(50)
    expect(fdTaskLimitEffective(201)).toBe(50)
    expect(fdTaskLimitEffective('9999')).toBe(50)
    expect(fdTaskLimitEffective(9999)).toBe(50)
    // ★ 注释写的是 "capped at 200"，实现不是 clamp
    expect(fdTaskLimitEffective('201')).not.toBe(200)
  })

  it('★ 请求值被静默改写时能看出来', () => {
    expect(taskLimitWasRewritten(undefined)).toBe(false)
    expect(taskLimitWasRewritten('')).toBe(false)
    expect(taskLimitWasRewritten('abc')).toBe(true)
    expect(taskLimitWasRewritten('0')).toBe(true)
    expect(taskLimitWasRewritten('-1')).toBe(true)
    expect(taskLimitWasRewritten('201')).toBe(true)
    expect(taskLimitWasRewritten('9999')).toBe(true)
    expect(taskLimitWasRewritten('30')).toBe(false)
    expect(taskLimitWasRewritten('200')).toBe(false)
  })

  it('limit 原样下发（改写发生在后端）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ tasks: [] }))
    await fetchTasks({ limit: 201 })
    expect(lastUrl()).toBe('/api/free-discovery/tasks?limit=201')
  })

  it('不传 limit 时不带查询串', () => {
    expect(fdTasksPath()).toBe('/api/free-discovery/tasks')
    expect(fdTasksPath({})).toBe('/api/free-discovery/tasks')
    expect(fdTasksPath({ limit: 30 })).toBe('/api/free-discovery/tasks?limit=30')
    expect(fdTasksPath({ limit: 'abc' })).toBe('/api/free-discovery/tasks?limit=abc')
  })

  // ── 任务侧判读 ───────────────────────────────────────────────────

  it('status 四态：finished 与 in-flight 互为补集', () => {
    const finished = ['success', 'failed'] as const
    const inFlight = ['pending', 'running'] as const
    for (const s of finished) expect(taskIsFinished(taskFromList({ status: s }))).toBe(true)
    for (const s of inFlight) expect(taskIsInFlight(taskFromList({ status: s }))).toBe(true)
    for (const s of finished) expect(taskIsInFlight(taskFromList({ status: s }))).toBe(false)
    for (const s of inFlight) expect(taskIsFinished(taskFromList({ status: s }))).toBe(false)
  })

  it('error_message 是 COALESCE(…,"") ⇒ 空串不是 null', () => {
    expect(taskHasErrorMessage(taskFromList())).toBe(false)
    expect(taskHasErrorMessage(taskFromList({ error_message: 'upstream 401' }))).toBe(true)
  })

  it('models_found 为 0 是二义：既可能是没量到也可能是一条都没扫', () => {
    expect(taskFoundNothing(taskFromList({ models_found: 0 }))).toBe(true)
    expect(taskFoundNothing(taskFromList({ models_found: 1 }))).toBe(false)
  })

  it('template_id 非 null ⇒ 模板还在', () => {
    expect(taskTemplateDeleted(taskFromList())).toBe(false)
    expect(taskTemplateDeleted(taskFromList({ template_id: null }))).toBe(true)
  })

  it('任务路径对 id 做 encode', () => {
    expect(fdTaskByIdPath(7)).toBe('/api/free-discovery/tasks/7')
    expect(fdTaskByIdPath('a b')).toBe('/api/free-discovery/tasks/a%20b')
  })
})

// ══ tasks/{id} ═════════════════════════════════════════════════════════

describe('free-discovery/tasks/{id} 详情端点', () => {
  it('★ 返回裸对象，没有信封', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(taskDetail({ template_id: null })))
    const t = await fetchTaskById(7)
    expect(t.id).toBe(7)
    expect(t.template_id).toBeNull()
    expect(lastUrl()).toBe('/api/free-discovery/tasks/7')
  })

  it('把 results 信封喂给任务解包器必须抛错', () => {
    expect(() => unwrapTask({ results: [result()] })).toThrow(/形状不符/)
  })

  it('把裸任务喂给列表解包器必须抛错', () => {
    expect(() => unwrapTasksList(taskDetail())).toThrow(/形状不符/)
  })
})

// ══ tasks/{id}/results ════════════════════════════════════════════════

describe('free-discovery/tasks/{id}/results 结果端点', () => {
  it('GET 合法信封解出结果数组', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ results: [result()] }))
    const r = await fetchTaskResults(7)
    expect(r.results).toHaveLength(1)
    expect(lastUrl()).toBe('/api/free-discovery/tasks/7/results')
  })

  it('results 为 null 抛错（有 nil-guard）', () => {
    expect(() => unwrapResultsList({ results: null })).toThrow(/永不为 null/)
  })

  it('★ 缺 17 个必填键中的任何一个都必须抛错', () => {
    for (const k of FD_RESULT_REQUIRED_KEYS) {
      const item = { ...result() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapResultsList({ results: [item] }), `缺 ${k} 时应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('imported_at 键恒存在：可为 null，不可缺；类型不符抛错', () => {
    expect(() => unwrapResultsList({ results: [{ ...result(), imported_at: null }] })).not.toThrow()
    expect(() => unwrapResultsList({ results: [{ ...result(), imported_at: '2026-09-10T00:00:00Z' }] })).not.toThrow()
    const missing = { ...result() } as Record<string, unknown>
    delete missing.imported_at
    expect(() => unwrapResultsList({ results: [missing] })).toThrow(/缺 1 个/)
    expect(() => unwrapResultsList({ results: [{ ...result(), imported_at: 7 }] })).toThrow(/形状不符/)
  })

  it('把 tasks 信封喂给结果解包器必须抛错', () => {
    expect(() => unwrapResultsList({ tasks: [taskFromList()] })).toThrow(/形状不符/)
  })

  // ── status 参数 ─────────────────────────────────────────────────

  it('★ 空 status 后端回落成 pending（所以不发这个参数而不是发 status=）', () => {
    expect(resultsStatusParam(undefined)).toBe('pending')
    expect(resultsStatusParam('')).toBe('pending')
    expect(resultsStatusParam('all')).toBe('all')
    expect(resultsStatusParam('imported')).toBe('imported')
    expect(fdTaskResultsPath(7)).toBe('/api/free-discovery/tasks/7/results')
    expect(fdTaskResultsPath(7, {})).toBe('/api/free-discovery/tasks/7/results')
    expect(fdTaskResultsPath(7, { status: '' })).toBe('/api/free-discovery/tasks/7/results')
    expect(fdTaskResultsPath(7, { status: 'all' })).toBe('/api/free-discovery/tasks/7/results?status=all')
  })

  it('★ 只有 "all" 不过滤（空串不可达，handler 已回落）', () => {
    expect(resultsStatusIsUnfiltered('all')).toBe(true)
    expect(resultsStatusIsUnfiltered('')).toBe(true)
    for (const s of ['pending', 'imported', 'skipped', 'conflict', 'bogus']) {
      expect(resultsStatusIsUnfiltered(s), `"${s}" 要过滤`).toBe(false)
    }
  })

  it('★★★ 未知 status 不被拒 ⇒ 空结果既可能是「真没有」也可能是「过滤写错了」', () => {
    expect(resultsStatusIsKnown('pending')).toBe(true)
    expect(resultsStatusIsKnown('imported')).toBe(true)
    expect(resultsStatusIsKnown('all')).toBe(true)
    expect(resultsStatusIsKnown('bogus')).toBe(false)
    expect(resultsStatusIsKnown('PENDING')).toBe(false)
    // 后端对未知值也返回 200 + 空数组，不返回 400
    expect(unwrapResultsList({ results: [] }).results).toEqual([])
    expect(resultsEmptyIsIndeterminate({ results: [] })).toBe(true)
    expect(resultsEmptyIsIndeterminate({ results: [result()] })).toBe(false)
  })

  // ── 结果侧判读 ──────────────────────────────────────────────────

  it('★ free_type 的空串**唯一**对应 SQL NULL（CHECK 不允许 ""）⇒ 这里不是二义', () => {
    expect(resultFreeTypeUninferable(result({ free_type: '' }))).toBe(true)
    expect(resultFreeTypeUninferable(result({ free_type: 'keyless' }))).toBe(false)
    expect(resultFreeTypeUninferable(result({ free_type: 'recurring-daily' }))).toBe(false)
  })

  it('★★ 四个配额/上下文 0 全是二义（COALESCE(…,0)）', () => {
    expect(FD_RESULT_AMBIGUOUS_ZERO_FIELDS).toEqual([
      'context_window',
      'max_tokens',
      'monthly_tokens',
      'daily_tokens',
    ])
    const allZero = result({
      context_window: 0,
      max_tokens: 0,
      monthly_tokens: 0,
      daily_tokens: 0,
    })
    expect(resultZeroFields(allZero)).toHaveLength(4)
    expect(resultZeroFields(result())).toEqual(['monthly_tokens'])
    expect(resultZeroFields(result({ monthly_tokens: 100 }))).toEqual([])
    // ★ 与 free_type 的对照：一个二义一个不二义
    expect(resultFreeTypeUninferable(allZero)).toBe(false)
  })

  it('imported_at 为 null ⇒ 还没导入', () => {
    expect(resultNotImported(result())).toBe(true)
    expect(resultNotImported(result({ imported_at: '2026-09-10T00:00:00Z' }))).toBe(false)
  })

  it('pool_key 空串不是 null（COALESCE(…,"")）', () => {
    expect(resultSharesNoPool(result())).toBe(true)
    expect(resultSharesNoPool(result({ pool_key: 'openrouter-free-pool' }))).toBe(false)
  })
})

// ══ scan-scheduler/status ══════════════════════════════════════════════

describe('free-discovery/scan-scheduler/status（superAdmin 档）', () => {
  it('★ 返回裸对象，没有信封', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(scheduler()))
    const s = await fetchScanSchedulerStatus()
    expect(s.started).toBe(true)
    expect(lastUrl()).toBe('/api/free-discovery/scan-scheduler/status')
  })

  it('★ 缺 9 个必填键中的任何一个都必须抛错', () => {
    for (const k of FD_SCHEDULER_REQUIRED_KEYS) {
      const item = { ...scheduler() } as Record<string, unknown>
      delete item[k]
      expect(() => unwrapScanSchedulerStatus(item), `缺 ${k} 时应抛错`).toThrow(/缺 1 个/)
    }
  })

  it('★ last_error 是 omitempty：键存在与缺失都合法，但类型必须是字符串', () => {
    expect(FD_SCHEDULER_OPTIONAL_KEYS).toEqual(['last_error'])
    expect(FD_SCHEDULER_REQUIRED_KEYS as readonly string[]).not.toContain('last_error')
    expect(() => unwrapScanSchedulerStatus(scheduler())).not.toThrow()
    expect(() => unwrapScanSchedulerStatus(scheduler({ last_error: 'upstream timeout' }))).not.toThrow()
    expect(() => unwrapScanSchedulerStatus(scheduler({ last_error: 7 }))).toThrow(/形状不符/)
  })

  it('布尔 / 字符串 / 计数类型不符都抛错', () => {
    expect(() => unwrapScanSchedulerStatus({ ...scheduler(), enabled: 'true' })).toThrow(/形状不符/)
    expect(() => unwrapScanSchedulerStatus({ ...scheduler(), started: 1 })).toThrow(/形状不符/)
    expect(() => unwrapScanSchedulerStatus({ ...scheduler(), interval: 3600 })).toThrow(/形状不符/)
    expect(() => unwrapScanSchedulerStatus({ ...scheduler(), last_sweep_at: null })).toThrow(/形状不符/)
    expect(() => unwrapScanSchedulerStatus({ ...scheduler(), sweeps_total: '128' })).toThrow(/形状不符/)
    expect(() => unwrapScanSchedulerStatus({ ...scheduler(), cycles_failed: null })).toThrow(/形状不符/)
    expect(() => unwrapScanSchedulerStatus({ presets: null })).toThrow(/形状不符/)
    expect(() => unwrapScanSchedulerStatus(null)).toThrow(/实得 null/)
  })

  it('★★ 两种「scheduler 不存在」是两种表示：503 与 200+interval:""', () => {
    // 一支：h.scanSchedulerStatus == nil ⇒ 503（这里只判文案，状态码由 client 抛出）
    expect(scanSchedulerMissingMessage('scan-scheduler is not available')).toBe(true)
    expect(fdDepsMissingMessage('scan-scheduler is not available')).toBe(false)
    // 另一支：接口里装着 typed-nil ⇒ 200 + 指纹
    const s = schedulerTypedNil()
    expect(schedulerProviderWasTypedNil(s)).toBe(true)
    expect(schedulerIntervalUnset(s)).toBe(true)
    expect(schedulerNeverSwept(s)).toBe(true)
    expect(schedulerLastErrorMissing(s)).toBe(true)
    expect(schedulerIdleCounters(s)).toBe(true)
  })

  it('真 scheduler 的 interval 至少是 "0s"，不会是空串', () => {
    expect(schedulerIntervalUnset(scheduler())).toBe(false)
    expect(schedulerIntervalUnset(scheduler({ interval: '0s' }))).toBe(false)
    expect(schedulerProviderWasTypedNil(scheduler())).toBe(false)
  })

  it('enabled 与 started 是两件事：env 开了 worker 未必在跑', () => {
    expect(schedulerEnabledButNotStarted(scheduler({ enabled: true, started: false }))).toBe(true)
    expect(schedulerEnabledButNotStarted(scheduler({ enabled: false, started: false }))).toBe(false)
    expect(schedulerEnabledButNotStarted(scheduler({ enabled: true, started: true }))).toBe(false)
    expect(schedulerEnabledButNotStarted(scheduler({ enabled: false, started: true }))).toBe(false)
  })

  it('last_error 键缺失与空串同义（都是「没有错误记录」）', () => {
    expect(schedulerLastErrorMissing(scheduler())).toBe(true)
    expect(schedulerLastErrorMissing(scheduler({ last_error: '' }))).toBe(true)
    expect(schedulerLastErrorMissing(scheduler({ last_error: 'boom' }))).toBe(false)
  })

  it('计数全 0 是二义（从没跑过 vs 跑了没产出）', () => {
    expect(schedulerIdleCounters(schedulerTypedNil())).toBe(true)
    expect(schedulerIdleCounters(scheduler({ sweeps_total: 1 }))).toBe(false)
    expect(schedulerIdleCounters(scheduler({ scans_total: 1 }))).toBe(false)
    expect(schedulerIdleCounters(scheduler({ scans_failed: 1 }))).toBe(false)
    expect(schedulerIdleCounters(scheduler({ scans_skipped: 1 }))).toBe(false)
    expect(schedulerIdleCounters(scheduler({ cycles_failed: 1 }))).toBe(false)
    expect(schedulerIdleCounters(scheduler())).toBe(false)
  })

  it('从没扫过时 last_sweep_at 是 Go 零值（time.Time 非指针，无 omitempty）', () => {
    expect(schedulerNeverSwept(scheduler({ last_sweep_at: GO_ZERO_TIME }))).toBe(true)
    expect(schedulerNeverSwept(scheduler())).toBe(false)
  })
})

// ══ 模板侧判读 ═════════════════════════════════════════════════════════

describe('模板侧判读', () => {
  it('★ api_key_env 空串是二义：无认证 vs 有密文但没 env 引用', () => {
    // 后端 HasCredential() 还看 len(APIKeyEncrypted)>0，而密文 json:"-" 从不下发
    expect(templateCredentialIsIndeterminate(template({ api_key_env: '' }))).toBe(true)
    expect(templateCredentialIsIndeterminate(template({ api_key_env: '$GROQ_API_KEY' }))).toBe(false)
  })

  it('auto_disabled_at 是 omitempty ⇒ 键存在才谈得上自动停用', () => {
    expect(templateAutoDisabled(template())).toBe(false)
    expect(templateAutoDisabled(templateWithHealth())).toBe(true)
    expect(templateAutoDisabled(template({ auto_disabled_at: '' }))).toBe(false)
    expect(templateHasLastScanFailureAt(template())).toBe(false)
    expect(templateHasLastScanFailureAt(templateWithHealth())).toBe(true)
  })

  it('手动停用与自动停用分得开', () => {
    expect(templateManuallyDisabled(template({ enabled: false }))).toBe(true)
    expect(templateManuallyDisabled(templateWithHealth())).toBe(false)
    expect(templateManuallyDisabled(template({ enabled: true }))).toBe(false)
    expect(templateAutoDisabled(template({ enabled: false }))).toBe(false)
  })

  it('失败计数与是否停用是两个独立信号', () => {
    expect(templateHasScanFailures(template({ consecutive_scan_failures: 0 }))).toBe(false)
    expect(templateHasScanFailures(template({ consecutive_scan_failures: 3 }))).toBe(true)
    // 有失败但没到停用阈值：enabled 仍为 true，且 auto_disabled_at 不存在
    const t = template({ consecutive_scan_failures: 2, enabled: true })
    expect(templateHasScanFailures(t)).toBe(true)
    expect(templateAutoDisabled(t)).toBe(false)
    expect(templateManuallyDisabled(t)).toBe(false)
  })

  it('created_at 也可能是 Go 零值（sql.NullTime 为 NULL）', () => {
    expect(createdAtIsUnreadable(GO_ZERO_TIME)).toBe(true)
    expect(createdAtIsUnreadable('2026-09-09T02:00:00Z')).toBe(false)
    expect(isGoZeroTime(null)).toBe(false)
    expect(isGoZeroTime(undefined)).toBe(false)
    expect(GO_ZERO_TIME).toBe('0001-01-01T00:00:00Z')
  })
})

// ══ 错误文案 ══════════════════════════════════════════════════════════

describe('错误文案判读（信封 {"error":{"detail":msg}}）', () => {
  it('★ 六条 deps 端点的 503 文案', () => {
    const m = 'free-discovery is not available (database disabled)'
    expect(fdDepsMissingMessage(m)).toBe(true)
    expect(scanSchedulerMissingMessage(m)).toBe(false)
    expect(fdDepsMissingMessage('scan-scheduler is not available')).toBe(false)
    expect(fdDepsMissingMessage('free discovery request failed')).toBe(false)
  })

  it('★ scheduler 探针的 503 是另一句话', () => {
    const m = 'scan-scheduler is not available'
    expect(scanSchedulerMissingMessage(m)).toBe(true)
    expect(fdDepsMissingMessage(m)).toBe(false)
  })

  it('★★ 同族两个 500 文案：只有 templates GET 用这一句', () => {
    const internal = 'internal error (see server logs)'
    const failed = 'free discovery request failed'
    expect(fdTemplatesInternalErrorMessage(internal)).toBe(true)
    expect(fdRequestFailedMessage(internal)).toBe(false)
    expect(fdRequestFailedMessage(failed)).toBe(true)
    expect(fdTemplatesInternalErrorMessage(failed)).toBe(false)
    expect(fdTemplatesInternalErrorMessage('  internal error (see server logs)')).toBe(false)
  })

  it('★ id 非数字是 400 不是 404，且两条任务端点共用同一句', () => {
    expect(invalidTemplateIdMessage('invalid template id')).toBe(true)
    expect(invalidTemplateIdMessage('invalid task id')).toBe(false)
    expect(invalidTaskIdMessage('invalid task id')).toBe(true)
    expect(invalidTaskIdMessage('invalid template id')).toBe(false)
    expect(templateNotFoundMessage('invalid task id')).toBe(false)
    expect(taskNotFoundMessage('invalid task id')).toBe(false)
  })

  it('★ 两条 404 的 detail 形状不同：模板不带 id，任务带 (id N)', () => {
    expect(templateNotFoundMessage('freediscovery: provider template not found')).toBe(true)
    expect(taskNotFoundMessage('freediscovery: task not found (id 42)')).toBe(true)
    // ★ 交叉确认互不为真
    expect(taskNotFoundMessage('freediscovery: provider template not found')).toBe(false)
    expect(templateNotFoundMessage('freediscovery: task not found (id 42)')).toBe(false)
    // ★ 任务那条不带 id 就匹配不上
    expect(taskNotFoundMessage('freediscovery: task not found')).toBe(false)
  })

  it('能从任务 404 文案取回 id，模板那条取不回', () => {
    expect(taskNotFoundId('freediscovery: task not found (id 42)')).toBe('42')
    expect(taskNotFoundId('freediscovery: task not found (id -7)')).toBe('-7')
    expect(taskNotFoundId('freediscovery: provider template not found')).toBeNull()
    expect(taskNotFoundId('freediscovery: task not found')).toBeNull()
  })

  it('同路径写操作的 403 文案（解释这页为什么只读）', () => {
    expect(
      tenantAdminWriteForbiddenMessage(
        'tenant_admin has read-only access; write operations require super_admin',
      ),
    ).toBe(true)
    expect(tenantAdminWriteForbiddenMessage('free discovery request failed')).toBe(false)
  })

  it('★ scheduler 是全族唯一不查 fdDeps 的 ⇒ 六条 503 与它 200 可以同时出现', () => {
    expect(
      schedulerAnswersWhileDepsAreDown(true, 'free-discovery is not available (database disabled)'),
    ).toBe(true)
    expect(schedulerAnswersWhileDepsAreDown(false, 'free-discovery is not available (database disabled)')).toBe(false)
    expect(schedulerAnswersWhileDepsAreDown(true, 'scan-scheduler is not available')).toBe(false)
    expect(schedulerAnswersWhileDepsAreDown(true, null)).toBe(false)
  })
})

// ══ 档位与路径常量 ════════════════════════════════════════════════════

describe('路径常量与档位', () => {
  it('路径逐条对上 admin/handler.go:1302-1316', () => {
    expect(FD_TEMPLATES_PATH).toBe('/api/free-discovery/templates')
    expect(FD_PRESETS_PATH).toBe('/api/free-discovery/templates/presets')
    expect(FD_TASKS_PATH).toBe('/api/free-discovery/tasks')
    expect(FD_SCAN_SCHEDULER_PATH).toBe('/api/free-discovery/scan-scheduler/status')
  })

  it('★ presets 路径更具体，不会被 templates/{id} 遮蔽（Go 1.22 方法限定路由）', () => {
    // 两个常量必须不同，且 presets 不是以 {id} 结尾的形态
    expect(FD_PRESETS_PATH).not.toBe(FD_TEMPLATES_PATH)
    expect(FD_PRESETS_PATH).toBe('/api/free-discovery/templates/presets')
  })

  it('scheduler 探针走的是 superAdmin 那条注册（不是 /api/admin/ 前缀）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(scheduler()))
    await fetchScanSchedulerStatus()
    expect(lastUrl()).toBe('/api/free-discovery/scan-scheduler/status')
    expect(lastUrl().startsWith('/api/admin/')).toBe(false)
  })

  it('每条 fetch 都用 GET 且不带请求体', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ templates: [] }))
    await fetchTemplates()
    expect(lastMethod()).toBe('GET')
    expect((lastCall().init as { body?: unknown }).body).toBeUndefined()
  })

  it('★ presets 端点没有 enabled/limit 之类的查询参数', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ presets: [] }))
    await fetchPresets()
    expect(lastUrl()).not.toContain('?')
  })
})

// ══ 键集合自身的形状 ══════════════════════════════════════════════════

describe('键集合与后端结构体对齐', () => {
  it('模板：17 必填 + 2 条件存在 + 1 永不下发', () => {
    expect(FD_TEMPLATE_REQUIRED_KEYS).toHaveLength(17)
    expect(FD_TEMPLATE_OPTIONAL_KEYS).toEqual(['last_scan_failure_at', 'auto_disabled_at'])
    expect(FD_TEMPLATE_NEVER_KEY).toBe('api_key_encrypted')
    // 三者互不重叠
    const all = [...FD_TEMPLATE_REQUIRED_KEYS, ...FD_TEMPLATE_OPTIONAL_KEYS, FD_TEMPLATE_NEVER_KEY]
    expect(new Set(all).size).toBe(all.length)
  })

  it('任务 14 键全恒存在（含可空但无 omitempty 的 template_id/started_at/completed_at）', () => {
    expect(FD_TASK_REQUIRED_KEYS).toHaveLength(14)
    for (const k of ['template_id', 'started_at', 'completed_at']) {
      expect(FD_TASK_REQUIRED_KEYS as readonly string[]).toContain(k)
    }
    // ★ updated_at 也在里面 —— 问题不在键，在列表端点不给真值
    expect(FD_TASK_REQUIRED_KEYS as readonly string[]).toContain('updated_at')
  })

  it('结果 17 键（imported_at 恒存在可为空；tenant_id 由 Go 回填）', () => {
    expect(FD_RESULT_REQUIRED_KEYS).toHaveLength(17)
    expect(FD_RESULT_REQUIRED_KEYS as readonly string[]).toContain('imported_at')
    expect(FD_RESULT_REQUIRED_KEYS as readonly string[]).toContain('tenant_id')
  })

  it('scheduler 9 必填 + 1 条件存在', () => {
    expect(FD_SCHEDULER_REQUIRED_KEYS).toHaveLength(9)
    expect(FD_SCHEDULER_OPTIONAL_KEYS).toHaveLength(1)
    expect(new Set([...FD_SCHEDULER_REQUIRED_KEYS, ...FD_SCHEDULER_OPTIONAL_KEYS]).size).toBe(10)
  })
})
