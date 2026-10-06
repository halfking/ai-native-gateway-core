import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import CredentialStateView from './CredentialStateView.vue'
import { fetchCredState, type CredState } from '@/api/credentialState'
import { DRAWER_NAV } from '@/config/appNav'
import router from '@/router'
import { setLocale, locale } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { HTTP_ERROR_TRAILING_NEWLINE, GO_ZERO_TIME } from '@/api/credentialState'

/**
 * CredentialStateView 的不变量（第四十九批，2026-10-07）。
 *
 * 1. ★★★★★★ 这条是 **superAdmin 档** ⇒ 抽屉席**必须**设 requiresRole（与前几批相反）。
 * 2. ★★★★★★★★★★ `state` 可为 `null`（三层缓存全 miss）⇒ **200**，不是 404。
 * 3. ★★★★★★ 五条指标在两条 DB 分支里**根本没赋值** ⇒ 恒 0；缓存命中才有真值。
 * 4. ★★★★ 注释里的枚举不全：source / health_status 实际取值 ⊋ 注释。
 * 5. ★★★★ 错误是 **text/plain + 尾换行** ⇒ 页面判文案要容忍。
 * 6. ★★ 抛错不许退化成「没探测过」—— 旧结果必须清空。
 * 7. ★★ 三个写端点（真的会触发探测）一个入口都不提供。
 *
 * ★ 期望文案全部从 `zhCN` 取值；含 `{占位符}` 的一律走 `head()`
 *   （渲染后占位符被替换，整串匹配必然对不上 —— 第四十八轮 10 条失败里 8 条栽在这）。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/credentialState', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/credentialState')>()
  return { ...actual, fetchCredState: vi.fn() }
})

const fetchMock = fetchCredState as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

function wire<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

/** ★ 缓存命中：五条指标有真值。 */
function stateFromProbe(over: Record<string, unknown> = {}): CredState {
  return wire({
    credential_id: 12,
    model: 'llama-3.3-70b-versatile',
    available: true,
    health_status: 'healthy',
    success_rate: 0.87,
    avg_latency_ms: 420,
    p95_latency_ms: 1180,
    active_sessions: 3,
    concurrency_limit: 8,
    last_updated_at: '2026-10-07T00:00:00Z',
    consecutive_fails: 0,
    source: 'model_probe',
    last_success_at: '2026-10-07T00:00:00Z',
    ...over,
  }) as CredState
}

/** ★★ node_probe 分支：health_status 空串 + 五条指标全 0 + recover_at 必在。 */
function stateFromNodeProbeDb(over: Record<string, unknown> = {}): CredState {
  return wire({
    credential_id: 12,
    model: 'llama-3.3-70b-versatile',
    available: true,
    health_status: '',
    success_rate: 0,
    avg_latency_ms: 0,
    p95_latency_ms: 0,
    active_sessions: 0,
    concurrency_limit: 0,
    last_updated_at: '2026-10-07T09:00:00Z',
    consecutive_fails: 0,
    source: 'node_probe_db',
    recover_at: '2026-10-07T09:05:00Z',
    ...over,
  }) as CredState
}

/** ★★ legacy 分支：last_attempt_at 为 NULL ⇒ Go 零值时间。 */
function stateFromLegacyDb(over: Record<string, unknown> = {}): CredState {
  return wire({
    credential_id: 12,
    model: 'llama-3.3-70b-versatile',
    available: false,
    health_status: 'healthy_confirmed',
    success_rate: 0,
    avg_latency_ms: 0,
    p95_latency_ms: 0,
    active_sessions: 0,
    concurrency_limit: 0,
    last_updated_at: GO_ZERO_TIME,
    consecutive_fails: 3,
    source: 'db',
    recover_at: GO_ZERO_TIME,
    ...over,
  }) as CredState
}

/** ★ `http.Error` 的报文：纯文本 + 尾换行。 */
function plainErr(text: string): Error {
  return new Error(text + HTTP_ERROR_TRAILING_NEWLINE)
}

type W = ReturnType<typeof mount>

async function mountView(): Promise<W> {
  const pinia = createPinia()
  setActivePinia(pinia)
  fetchMock.mockResolvedValue({ credential_id: 12, model: 'llama-3.3-70b-versatile', state: stateFromProbe() })
  const w = mount(CredentialStateView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  return w
}

/** ★ 含 `{占位符}` 的词典值**不能整串匹配渲染文本** —— 取第一个 `{` 之前的字面量部分。 */
function head(v: string): string {
  const i = v.indexOf('{')
  return (i < 0 ? v : v.slice(0, i)).trim()
}

async function run(w: W, credId = '12', model = 'llama-3.3-70b-versatile'): Promise<void> {
  await w.find('#cs-cred').setValue(credId)
  await w.find('#cs-model').setValue(model)
  await w.find('button').trigger('click')
  await flushPromises()
  await flushPromises()
}

function cellLabels(w: W): string[] {
  return (w.findAll('.cs__cell-l') ?? []).map((n) => n.text())
}

function cellValues(w: W): string[] {
  return (w.findAll('.cs__cell-v') ?? []).map((n) => n.text())
}

function noteTexts(w: W): string[] {
  return (w.findAll('.cs__note') ?? []).map((n) => n.text())
}

function metaTexts(w: W): string[] {
  return (w.findAll('.cs__meta') ?? []).map((n) => n.text())
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

describe('档位与接线（★ 这一批与前几批方向相反）', () => {
  it('★★ 抽屉席**必须**设 requiresRole: super_admin', () => {
    const item = DRAWER_NAV.find((i) => i.key === 'credential-model-state')
    expect(item).toBeTruthy()
    expect(item!.to).toBe('/credential-model-state')
    expect(item!.titleKey).toBe('nav.credModelState')
    // ★ 去掉它会把超管档页面暴露给租户管理员 —— 判据必须红
    expect(item!.requiresRole).toBe('super_admin')
  })

  it('★ 相反方向的页面前几批都**故意不设** requiresRole（本批不能照抄那条）', () => {
    for (const key of ['free-discovery', 'log-admin', 'session-context']) {
      const it = DRAWER_NAV.find((i) => i.key === key)
      expect(it, key).toBeTruthy()
      expect(it!.requiresRole, `${key} 应不设 requiresRole`).toBeUndefined()
    }
  })

  it('租户管理员看不到本页；超管看得到', () => {
    expect(DRAWER_NAV.map((i) => i.key)).toContain('credential-model-state')
    expect(DRAWER_NAV.some((i) => i.key === 'credential-model-state' && i.requiresRole !== 'super_admin')).toBe(false)
  })

  it('路由注册在 /credential-model-state', () => {
    const r = router.getRoutes().find((x) => x.name === 'credential-model-state')
    expect(r).toBeTruthy()
    expect(r!.path).toBe('/credential-model-state')
    const meta = r!.meta as { titleKey?: string } | undefined
    expect(meta?.titleKey).toBe('cs.title')
  })

  it('★ 本页只读：没有任何写操作按钮', async () => {
    const w = await mountView()
    const btnTexts = w.findAll('button').map((b) => b.text())
    expect(btnTexts).toEqual([zhCN.cs.query])
    for (const verb of ['探测', '重测', '触发', '批量', '保存', '删除', '修改']) {
      expect(btnTexts.some((x) => x.includes(verb)), `不该有写操作按钮（含「${verb}」）`).toBe(false)
    }
    expect(w.text()).toContain(zhCN.cs.readOnlyNote)
  })

  it('★ 五条免责说明常驻', async () => {
    const w = await mountView()
    for (const note of [
      zhCN.cs.tierNote,
      zhCN.cs.nullStateNote,
      zhCN.cs.layerBlindNote,
      zhCN.cs.enumNote,
      zhCN.cs.plainTextErrorNote,
    ]) {
      expect(w.text(), note).toContain(note)
    }
  })
})

// ══ 输入校验 ═════════════════════════════════════════════════════════

describe('输入校验', () => {
  it('★★ 填 0 / 负数 / 非数字 ⇒ 提示且按钮禁用', async () => {
    const w = await mountView()
    for (const v of ['0', '-1', 'abc', '']) {
      await w.find('#cs-cred').setValue(v)
      await w.find('#cs-model').setValue('gpt-4o')
      await flushPromises()
      const disabled = w.find('button').attributes('disabled') !== undefined
      expect(disabled, `"${v}" 应禁用查询`).toBe(true)
      if (v !== '') expect(w.text()).toContain(zhCN.cs.badIdNote)
    }
  })

  it('正整数 + 模型名 ⇒ 按钮解禁', async () => {
    const w = await mountView()
    await w.find('#cs-cred').setValue('12')
    await w.find('#cs-model').setValue('gpt-4o')
    await flushPromises()
    expect(w.find('button').attributes('disabled')).toBeUndefined()
  })

  it('★ 模型名里带斜杠 ⇒ 提示会转义', async () => {
    const w = await mountView()
    await w.find('#cs-cred').setValue('12')
    await w.find('#cs-model').setValue('vendor/model')
    await flushPromises()
    expect(w.text()).toContain(zhCN.cs.encodingNote)
    await w.find('#cs-model').setValue('gpt-4o')
    await flushPromises()
    expect(w.text()).not.toContain(zhCN.cs.encodingNote)
  })
})

// ══ state 为 null ═════════════════════════════════════════════════════

describe('★★ state 为 null（200，不是 404）', () => {
  it('显示「从没探测过」并落在 data-cs="state-null"', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'llama-3.3-70b-versatile', state: null })
    await run(w)
    expect(w.find('[data-cs="state-null"]').exists()).toBe(true)
    expect(noteTexts(w)).toContain(zhCN.cs.neverProbed)
  })

  it('★ null 态**不**渲染任何数据格（不许把「没量过」显示成「可用」）', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'llama-3.3-70b-versatile', state: null })
    await run(w)
    expect(cellLabels(w)).toEqual([])
    expect(cellValues(w)).toEqual([])
  })

  it('★ null 态**不**显示「可用/否」那一格', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'llama-3.3-70b-versatile', state: null })
    await run(w)
    expect(cellValues(w)).not.toContain(zhCN.common.yes)
    expect(cellValues(w)).not.toContain(zhCN.common.no)
  })
})

// ══ 数据格渲染 ═══════════════════════════════════════════════════════

describe('数据格渲染', () => {
  it('成功渲染十个数据格', async () => {
    const w = await mountView()
    await run(w)
    expect(cellLabels(w)).toEqual([
      zhCN.cs.available,
      zhCN.cs.healthStatus,
      zhCN.cs.successRate,
      zhCN.cs.consecutiveFails,
      zhCN.cs.avgLatency,
      zhCN.cs.p95Latency,
      zhCN.cs.activeSessions,
      zhCN.cs.concurrencyLimit,
      zhCN.cs.source,
      zhCN.cs.lastUpdatedAt,
    ])
    expect(cellValues(w)).toContain('0.87')
    expect(cellValues(w)).toContain('model_probe')
  })

  it('★ health_status 空串渲染成「未赋值」，**不是**「健康」', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: stateFromNodeProbeDb({ model: 'm' }) })
    await run(w, '12', 'm')
    expect(cellValues(w)).toContain(zhCN.cs.healthUnset)
    expect(cellValues(w)).not.toContain('healthy')
  })

  it('★ last_updated_at 是 Go 零值 ⇒ 显示「读不出来」而不是那个时间', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: stateFromLegacyDb({ model: 'm' }) })
    await run(w, '12', 'm')
    expect(cellValues(w)).toContain(zhCN.cs.updatedAtUnreadable)
    // ★ 零值时间字符串不许出现在任何数据格里（免责文案里已引用过它）
    expect(cellValues(w).filter((v) => v.includes('0001-01-01'))).toEqual([])
  })

  it('★★ 两条自相矛盾的组合各自单列一条', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({
      credential_id: 12,
      model: 'm',
      state: stateFromProbe({ model: 'm', health_status: 'unreachable', available: true }),
    })
    await run(w, '12', 'm')
    expect(noteTexts(w)).toContain(zhCN.cs.signalsDisagreeNote)

    const w2 = await mountView()
    fetchMock.mockResolvedValueOnce({
      credential_id: 12,
      model: 'm',
      state: stateFromLegacyDb({ model: 'm' }),
    })
    await run(w2, '12', 'm')
    expect(noteTexts(w2)).toContain(zhCN.cs.availableFalseHealthyNote)
  })

  it('正常组合**不**显示那两条矛盾说明', async () => {
    const w = await mountView()
    await run(w)
    const notes = noteTexts(w)
    expect(notes).not.toContain(zhCN.cs.signalsDisagreeNote)
    expect(notes).not.toContain(zhCN.cs.availableFalseHealthyNote)
  })
})

// ══ 五条指标 ═════════════════════════════════════════════════════════

describe('★★ 五条指标在两条 DB 分支里恒为 0', () => {
  it('全 0 时逐个点名，并说清 0 不等于「量出来就是 0」', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: stateFromNodeProbeDb({ model: 'm' }) })
    await run(w, '12', 'm')
    const hit = noteTexts(w).find((n) => n.includes(head(zhCN.cs.metricsZeroNote)))
    expect(hit).toBeTruthy()
    for (const f of ['success_rate', 'avg_latency_ms', 'p95_latency_ms', 'active_sessions', 'concurrency_limit']) {
      expect(hit, `应点名 ${f}`).toContain(f)
    }
  })

  it('有真值时**不**显示「没赋值」那条', async () => {
    const w = await mountView()
    await run(w)
    expect(noteTexts(w).some((n) => n.includes(head(zhCN.cs.metricsZeroNote)))).toBe(false)
  })

  it('★★ 只有成功率是 0（其余有值）⇒ 显示另一种更弱的说明', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({
      credential_id: 12,
      model: 'm',
      state: stateFromProbe({ model: 'm', success_rate: 0, avg_latency_ms: 420, concurrency_limit: 8 }),
    })
    await run(w, '12', 'm')
    const notes = noteTexts(w)
    expect(notes).toContain(zhCN.cs.successRateZeroNote)
    expect(notes.some((n) => n.includes(head(zhCN.cs.metricsZeroNote)))).toBe(false)
  })
})

// ══ 枚举超集 ═════════════════════════════════════════════════════════

describe('枚举的实际取值超出注释', () => {
  it('★★ source 出现注释外的值 ⇒ 单列一条，不当异常隐藏', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: stateFromNodeProbeDb({ model: 'm' }) })
    await run(w, '12', 'm')
    const notes = noteTexts(w)
    expect(notes.some((n) => n.includes(head(zhCN.cs.sourceUndocumentedNote)))).toBe(true)
    expect(cellValues(w)).toContain('node_probe_db')
  })

  it('★★ health_status 出现注释外的值（healthy_confirmed）⇒ 单列一条', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: stateFromLegacyDb({ model: 'm' }) })
    await run(w, '12', 'm')
    expect(noteTexts(w).some((n) => n.includes(head(zhCN.cs.healthUndocumentedNote)))).toBe(true)
    expect(cellValues(w)).toContain('healthy_confirmed')
  })

  it('取值都在注释里时**不**显示那两条', async () => {
    const w = await mountView()
    await run(w)
    const notes = noteTexts(w)
    expect(notes.some((n) => n.includes(head(zhCN.cs.sourceUndocumentedNote)))).toBe(false)
    expect(notes.some((n) => n.includes(head(zhCN.cs.healthUndocumentedNote)))).toBe(false)
  })

  it('★★★ node_probe 分支的「最后更新」是查询时刻 ⇒ 单列一条', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: stateFromNodeProbeDb({ model: 'm' }) })
    await run(w, '12', 'm')
    expect(noteTexts(w)).toContain(zhCN.cs.updatedAtIsQueryTimeNote)
  })

  it('legacy 分支**不**显示「查询时刻」那条', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: stateFromLegacyDb({ model: 'm' }) })
    await run(w, '12', 'm')
    expect(noteTexts(w)).not.toContain(zhCN.cs.updatedAtIsQueryTimeNote)
  })
})

// ══ 可选键与错误记录 ═════════════════════════════════════════════════

describe('omitempty 键与错误记录', () => {
  it('★ 四个可选键的缺失清单按节点列出（缺失 ≠ 空值）', async () => {
    const w = await mountView()
    await run(w)
    // ★ 默认夹具**有** last_success_at ⇒ 它不该出现在缺失清单里
    const txt = w.find('[data-cs="missing-optional"]').text()
    for (const k of ['last_failure_at', 'recover_at', 'last_error']) {
      expect(txt, `应点名缺失的 ${k}`).toContain(k)
    }
    expect(txt).not.toContain('last_success_at')
  })

  it('夹具缺 last_success_at 时它**才**进缺失清单', async () => {
    const w = await mountView()
    const { last_success_at: _drop, ...without } = stateFromProbe()
    void _drop
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: wire(without) })
    await run(w, '12', 'm')
    const txt = w.find('[data-cs="missing-optional"]').text()
    expect(txt).toContain('last_success_at')
  })

  it('没有 last_error ⇒ 显示「没有错误记录」', async () => {
    const w = await mountView()
    await run(w)
    expect(metaTexts(w)).toContain(zhCN.cs.noLastErrorNote)
    expect(noteTexts(w).some((n) => n.includes(head(zhCN.cs.lastErrorLine)))).toBe(false)
  })

  it('有 last_error ⇒ 显示错误行，**不**显示「没有错误记录」', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({
      credential_id: 12,
      model: 'm',
      state: stateFromProbe({ model: 'm', last_error: 'upstream 401' }),
    })
    await run(w, '12', 'm')
    expect(noteTexts(w).some((n) => n.includes(head(zhCN.cs.lastErrorLine)))).toBe(true)
    expect(metaTexts(w)).not.toContain(zhCN.cs.noLastErrorNote)
  })

  it('consecutive_fails > 0 ⇒ 显示计数说明', async () => {
    const w = await mountView()
    fetchMock.mockResolvedValueOnce({ credential_id: 12, model: 'm', state: stateFromLegacyDb({ model: 'm' }) })
    await run(w, '12', 'm')
    expect(noteTexts(w).some((n) => n.includes(head(zhCN.cs.hasFailsNote)))).toBe(true)
  })

  it('consecutive_fails 为 0 ⇒ 不显示计数说明', async () => {
    const w = await mountView()
    await run(w)
    expect(noteTexts(w).some((n) => n.includes(head(zhCN.cs.hasFailsNote)))).toBe(false)
  })
})

// ══ 错误报文（text/plain + 尾换行）═════════════════════════════════════

describe('★ 错误报文：纯文本且带尾换行', () => {
  it('★★ 显示时把尾换行去掉（data-cs="err-raw"）', async () => {
    const w = await mountView()
    fetchMock.mockRejectedValueOnce(plainErr('state service not available'))
    await run(w)
    const shown = w.find('[data-cs="err-raw"]').text()
    expect(shown).toBe('state service not available')
    expect(shown.endsWith(HTTP_ERROR_TRAILING_NEWLINE)).toBe(false)
    // ★★ 必须读 **textContent 而不是 `.text()`**：vue-test-utils 的 `.text()` 会 `trim()`，
    //   ⇒ 尾换行在 DOM 断言里**根本看不见**（实测：把视图里的 strip 去掉，这条断言仍全绿）。
    //   读原始 textContent 才抓得到那个不可见字符。
    const rawText = w.find('[data-cs="err-raw"]').element.textContent ?? ''
    expect(rawText).toBe('state service not available')
    expect(rawText.endsWith(HTTP_ERROR_TRAILING_NEWLINE)).toBe(false)
  })

  it('并单独给出「原文有 N 个字符」那一行', async () => {
    const w = await mountView()
    fetchMock.mockRejectedValueOnce(plainErr('state service not available'))
    await run(w)
    const raw = w.find('[data-cs="err-stripped"]').text()
    expect(raw).toContain(String('state service not available'.length + 1))
  })

  it('★ 403（超管档）⇒ 显示专门的说明', async () => {
    const w = await mountView()
    fetchMock.mockRejectedValueOnce(plainErr('forbidden: super_admin role required'))
    await run(w)
    expect(noteTexts(w)).toContain(zhCN.cs.forbiddenNote)
  })

  it('★ 503 ⇒ 显示「状态服务没接上」，并说清它与 null 态不同', async () => {
    const w = await mountView()
    fetchMock.mockRejectedValueOnce(plainErr('state service not available'))
    await run(w)
    const notes = noteTexts(w)
    expect(notes).toContain(zhCN.cs.serviceMissingNote)
    // ★★ 503 时不许出现「从没探测过」
    expect(w.find('[data-cs="state-null"]').exists()).toBe(false)
    expect(notes).not.toContain(zhCN.cs.neverProbed)
  })

  it('★ 500 ⇒ 显示「查状态真的失败了」', async () => {
    const w = await mountView()
    fetchMock.mockRejectedValueOnce(plainErr('failed to get state'))
    await run(w)
    expect(noteTexts(w)).toContain(zhCN.cs.internalNote)
  })

  it('★ 服务端那条 400 ⇒ 显示说明', async () => {
    const w = await mountView()
    fetchMock.mockRejectedValueOnce(plainErr('invalid credential ID'))
    await run(w, '12', 'm')
    expect(noteTexts(w)).toContain(zhCN.cs.invalidIdServerNote)
  })

  it('未知报文 ⇒ 不硬套任何一种说明', async () => {
    const w = await mountView()
    fetchMock.mockRejectedValueOnce(plainErr('something else'))
    await run(w)
    const notes = noteTexts(w)
    expect(notes).not.toContain(zhCN.cs.forbiddenNote)
    expect(notes).not.toContain(zhCN.cs.serviceMissingNote)
    expect(notes).not.toContain(zhCN.cs.internalNote)
    expect(notes).not.toContain(zhCN.cs.invalidIdServerNote)
  })
})

// ══ 抛错必须清空 ═════════════════════════════════════════════════════

describe('★★ 抛错必须清空旧结果', () => {
  it('先成功 → 再失败：数据格与 null 态都不许残留', async () => {
    const w = await mountView()
    await run(w)
    expect(cellValues(w)).toContain('0.87')
    fetchMock.mockRejectedValueOnce(plainErr('failed to get state'))
    await run(w)
    expect(cellValues(w)).toEqual([])
    expect(cellLabels(w)).toEqual([])
    expect(w.find('[data-cs="state-null"]').exists()).toBe(false)
  })

  it('先成功 → 再失败：可选键清单节点也一起消失', async () => {
    const w = await mountView()
    await run(w)
    expect(w.find('[data-cs="missing-optional"]').exists()).toBe(true)
    fetchMock.mockRejectedValueOnce(plainErr('failed to get state'))
    await run(w)
    expect(w.find('[data-cs="missing-optional"]').exists()).toBe(false)
  })

  it('★ 失败**不**退化成「从没探测过」', async () => {
    const w = await mountView()
    await run(w)
    fetchMock.mockRejectedValueOnce(plainErr('state service not available'))
    await run(w)
    // ★ 服务没起来 与 从没探测过 是两回事：null 态节点不许出现
    expect(w.find('[data-cs="state-null"]').exists()).toBe(false)
    expect(w.find('[data-cs="err-raw"]').exists()).toBe(true)
  })
})
