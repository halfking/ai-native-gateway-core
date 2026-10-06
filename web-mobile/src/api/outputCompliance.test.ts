import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchComplianceStats,
  fetchComplianceRecords,
  fetchComplianceReviewQueue,
  fetchComplianceFeedback,
  fetchCompliancePolicy,
  fetchComplianceKeywords,
  unwrapComplianceStats,
  unwrapComplianceRecords,
  unwrapComplianceQueue,
  unwrapCompliancePolicy,
  unwrapComplianceKeywords,
  unwrapComplianceFeedback,
  compliancePolicyIsSyntheticDefault,
  complianceFieldAlwaysZero,
  formatComplianceThreshold,
  COMPLIANCE_ALWAYS_ZERO_FIELDS,
  COMPLIANCE_STATS_COUNTS_BY,
  COMPLIANCE_ISSUE_TYPES,
  COMPLIANCE_TOTAL_CHECKS_MIRRORS_ISSUES,
  COMPLIANCE_THRESHOLD_FIELDS,
  COMPLIANCE_LIMIT_MAX,
  COMPLIANCE_RECORDS_DEFAULT_LIMIT,
  COMPLIANCE_QUEUE_DEFAULT_LIMIT,
  COMPLIANCE_QUEUE_STATUSES,
  COMPLIANCE_FEEDBACK_TYPES,
  type CompliancePolicy,
} from './outputCompliance'

/**
 * 输出合规模块读面的契约测试（2026-10-07）。
 *
 * 六条重点：
 * 1. ★★★★★ `stats` 里 `jailbreak_hits` / `avg_latency_ms` **恒为 0**（写死），
 *    `total_checks` **恒等于** `total_issues` —— 三者都不是「真值」；
 * 2. ★★★★ `stats` 只给三个 issue_type 计数，而取值域有五个；
 * 3. ★★★★★ `records` 的 `content_preview` **只在 redacted=true 时有内容**；
 * 4. ★★★★★ 没配策略时后端返回**合成的默认策略**（不是 404），靠 id===0 识别；
 * 5. ★★★ 同族默认条数不同（records 50 / queue 20），上限统一 200；
 * 6. ★★★ `status`/`type` 只发数据库 CHECK 允许的字面值。
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

function statsBody(over: Record<string, unknown> = {}) {
  return {
    total_issues: 3,
    blocked: 1,
    pending_reviews: 2,
    total_checks: 3,
    pii_hits: 2,
    secret_hits: 1,
    toxicity_hits: 0,
    jailbreak_hits: 0,
    avg_latency_ms: 0,
    last_updated: '2026-10-07T09:00:00Z',
    ...over,
  }
}

function policyBody(over: Record<string, unknown> = {}): CompliancePolicy {
  return {
    id: 7,
    policy_name: 'prod',
    enabled: true,
    enforcement_mode: 'enforce',
    llm_engine_id: null,
    check_pii: true,
    check_toxicity: true,
    check_bias: false,
    check_hallucination: false,
    check_secrets: true,
    check_internal_ip: true,
    check_jailbreak_response: false,
    check_instruction_injection_response: false,
    pii_threshold: 0.7,
    toxicity_threshold: 0.7,
    bias_threshold: 0.6,
    hallucination_threshold: 0.7,
    secrets_threshold: 0.7,
    internal_ip_threshold: 0.7,
    auto_redact: true,
    redact_email: true,
    redact_phone: true,
    redact_id_card: true,
    redact_credit_card: true,
    redact_bank_card: false,
    redact_jwt: true,
    redact_password: true,
    toxic_replacement: '[内容已过滤]',
    block_message: '响应因合规策略被阻断',
    strict_mode: false,
    whitelist_keywords: [],
    realtime_alert_enabled: false,
    alert_threshold_severity: 7,
    alert_aggregation_window_minutes: 5,
    sampling_rate: 1,
    auto_review_queue_enabled: false,
    feedback_loop_enabled: false,
    skill_generation_enabled: false,
    auto_threshold_tuning_enabled: false,
    retention_days: 90,
    total_detections: 3,
    total_blocks: 1,
    last_detection_at: '2026-10-07T09:00:00Z',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...over,
  } as CompliancePolicy
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  fetchMock.mockResolvedValue(jsonResponse(statsBody()))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('路径', () => {
  it('★★★ 六个只读端点各自独立', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statsBody()))
    await fetchComplianceStats()
    fetchMock.mockResolvedValueOnce(jsonResponse({ records: [], total: 0, limit: 50, offset: 0 }))
    await fetchComplianceRecords()
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [], status: 'pending', limit: 20, offset: 0 }))
    await fetchComplianceReviewQueue()
    fetchMock.mockResolvedValueOnce(jsonResponse({ feedback: [], limit: 20, offset: 0 }))
    await fetchComplianceFeedback()
    fetchMock.mockResolvedValueOnce(jsonResponse(policyBody()))
    await fetchCompliancePolicy()
    fetchMock.mockResolvedValueOnce(jsonResponse({ keywords: [] }))
    await fetchComplianceKeywords()
    const urls = fetchMock.mock.calls.map((c) => String(c[0]))
    for (const u of [
      '/api/admin/output-compliance/stats',
      '/api/admin/output-compliance/records',
      '/api/admin/output-compliance/review-queue',
      '/api/admin/output-compliance/feedback',
      '/api/admin/output-compliance/policy',
      '/api/admin/output-compliance/keywords',
    ]) {
      expect(urls.some((x) => x.includes(u))).toBe(true)
    }
  })
})

describe('★★★★★ 判据 1：stats 里三个「假数」', () => {
  it('★★★★★ 恒为 0 的字段清单就是这两个', () => {
    expect([...COMPLIANCE_ALWAYS_ZERO_FIELDS]).toEqual(['jailbreak_hits', 'avg_latency_ms'])
  })

  it('★★★ total_checks 恒等于 total_issues 是**契约**（不是巧合）', () => {
    expect(COMPLIANCE_TOTAL_CHECKS_MIRRORS_ISSUES).toBe(true)
    // 后端是把同一个值写了两遍，所以任何一组都必须相等
    for (const n of [0, 3, 999]) {
      const s = statsBody({ total_issues: n, total_checks: n })
      expect(s.total_checks).toBe(s.total_issues)
    }
  })

  it('★★ total_issues 可能 ≠ pii+secret+toxic（另两类没字段）', () => {
    // internal_ip / bias 的命中在 stats 里根本没有对应字段
    const s = statsBody({ total_issues: 10, pii_hits: 2, secret_hits: 1, toxicity_hits: 0 })
    expect(s.total_issues).toBeGreaterThan(s.pii_hits + s.secret_hits + s.toxicity_hits)
  })

  it('★★★ stats 只给三类计数，而取值域有五类', () => {
    expect([...COMPLIANCE_STATS_COUNTS_BY].sort()).toEqual(['pii', 'secret', 'toxic'])
    expect([...COMPLIANCE_ISSUE_TYPES].sort()).toEqual(['bias', 'internal_ip', 'pii', 'secret', 'toxic'])
  })

  it('★★ 每个恒假字段都判定为真，其它不', () => {
    for (const f of COMPLIANCE_ALWAYS_ZERO_FIELDS) expect(complianceFieldAlwaysZero(f)).toBe(true)
    for (const f of ['total_issues', 'blocked', 'pii_hits', 'last_updated']) expect(complianceFieldAlwaysZero(f)).toBe(false)
  })

  it('★ last_updated 空表时是**空字符串**（不是 null/缺键）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statsBody({ total_issues: 0, last_updated: '' })))
    const s = await fetchComplianceStats()
    expect(s.last_updated).toBe('')
    expect('last_updated' in s).toBe(true)
  })
})

describe('★★★★★ 判据 4：没配策略 ⇒ 合成默认策略，不是 404', () => {
  it('★★★★★ id===0 ⇒ 判定为「后端合成的默认策略」', () => {
    expect(compliancePolicyIsSyntheticDefault(policyBody({ id: 0 }))).toBe(true)
  })

  it('★★★★ created_at 为空串 ⇒ 同样判定为合成（两个信号是「或」）', () => {
    // ★ 默认策略两个信号同时为零（id=0 且时间为空串），所以任一成立即可判合成。
    expect(compliancePolicyIsSyntheticDefault(policyBody({ created_at: '' }))).toBe(true)
    // ★ 反向：id>0 且有 created_at ⇒ 一律判为真实行（哪怕其它字段全是默认值）
    expect(compliancePolicyIsSyntheticDefault(policyBody({ id: 5, created_at: '2026-01-01T00:00:00Z' }))).toBe(false)
  })

  it('★★★ 真实策略行（id>0 且有 created_at）⇒ 不判为合成', () => {
    expect(compliancePolicyIsSyntheticDefault(policyBody())).toBe(false)
  })

  it('★★★★ unwrap 收到数组 ⇒ **抛错**（handler 源码实测永不返回数组）', () => {
    expect(() => unwrapCompliancePolicy([policyBody()])).toThrow(/形状不符/)
    expect(() => unwrapCompliancePolicy([])).toThrow(/array\(len=0\)/)
  })

  it('★★ 真实 GET /policy 返回对象时原样放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(policyBody()))
    const p = await fetchCompliancePolicy()
    expect(p.id).toBe(7)
    expect(compliancePolicyIsSyntheticDefault(p)).toBe(false)
  })
})

describe('★★ 阈值与采样率量纲（0..1）', () => {
  it('★★★ 阈值字段清单是六个', () => {
    expect(COMPLIANCE_THRESHOLD_FIELDS.length).toBe(6)
    expect([...COMPLIANCE_THRESHOLD_FIELDS]).toContain('pii_threshold')
  })

  it('★★★ 0.7 ⇒ 70.00%（不是 0.7% 也不是 700%）', () => {
    expect(formatComplianceThreshold(0.7)).toBe('70.00%')
    expect(formatComplianceThreshold(0.7)).not.toContain('700')
  })

  it('★★ 非法值 ⇒ 「—」', () => {
    expect(formatComplianceThreshold(null)).toBe('—')
    expect(formatComplianceThreshold(undefined)).toBe('—')
    expect(formatComplianceThreshold(NaN)).toBe('—')
  })
})

describe('★★★ 判据 5：同族默认条数不同、上限统一 200', () => {
  it('★★★ records 默认 50，queue/feedback 默认 20（不同！）', () => {
    expect(COMPLIANCE_RECORDS_DEFAULT_LIMIT).toBe(50)
    expect(COMPLIANCE_QUEUE_DEFAULT_LIMIT).toBe(20)
    expect(COMPLIANCE_LIMIT_MAX).toBe(200)
  })

  it('★★ limit=201 不发（后端静默 clamp 到 200）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ records: [], total: 0, limit: 50, offset: 0 }))
    await fetchComplianceRecords({ limit: 201 })
    expect(lastUrl()).not.toContain('limit=')
  })

  it('★★ limit=200 发得出去（闭区间）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ records: [], total: 0, limit: 50, offset: 0 }))
    await fetchComplianceRecords({ limit: 200 })
    expect(lastUrl()).toContain('limit=200')
  })

  it('★★ limit ≤ 0 / NaN 不发（后端回落默认）', async () => {
    for (const n of [0, -1, NaN]) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ records: [], total: 0, limit: 50, offset: 0 }))
      await fetchComplianceRecords({ limit: n })
      expect(lastUrl()).not.toContain('limit=')
    }
  })

  it('★ offset=0 不发；offset>0 发', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ records: [], total: 0, limit: 50, offset: 0 }))
    await fetchComplianceRecords({ offset: 0 })
    expect(lastUrl()).not.toContain('offset=')
    await fetchComplianceRecords({ offset: 40 })
    expect(lastUrl()).toContain('offset=40')
  })
})

describe('★★★ 判据 6：枚举字面值只发 CHECK 允许的', () => {
  it('★★★ review-queue 的 status 只有三个合法值', () => {
    expect([...COMPLIANCE_QUEUE_STATUSES]).toEqual(['pending', 'approved', 'rejected'])
  })

  it('★★★ 三个合法 status 都发得出去', async () => {
    for (const s of COMPLIANCE_QUEUE_STATUSES) {
      fetchMock.mockResolvedValueOnce(jsonResponse({ items: [], status: s, limit: 20, offset: 0 }))
      await fetchComplianceReviewQueue({ status: s })
      expect(lastUrl()).toContain(`status=${s}`)
    }
  })

  it('★★★★ status=xxx **不发**（后端按值查，静默返空且不报错）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [], status: 'xxx', limit: 20, offset: 0 }))
    // ★ 用 `as never` 而不是 `@ts-expect-error`：后者的报错落在**实参那一行**，
    //   放在调用行上方会变成「未使用的指令」而报错（TS2578）。
    await fetchComplianceReviewQueue({ status: 'xxx' as never })
    expect(lastUrl()).not.toContain('status=')
  })

  it('★★ 不传 status 时**不发**（后端自己默认 pending）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [], status: 'pending', limit: 20, offset: 0 }))
    await fetchComplianceReviewQueue()
    expect(lastUrl()).not.toContain('status=')
  })

  it('★★★ feedback 的 type 只有三个合法值', () => {
    expect([...COMPLIANCE_FEEDBACK_TYPES]).toEqual(['false_positive', 'false_negative', 'correct'])
  })

  it('★★ feedback 非法 type 不发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ feedback: [], limit: 20, offset: 0 }))
    await fetchComplianceFeedback({ type: 'whatever' as never })
    expect(lastUrl()).not.toContain('type=')
  })
})

describe('★★★ 判据 3：records 的 preview 与过滤', () => {
  it('★★★ content_preview 原样透传（**不改写、不裁剪**，后端已截到 120）', async () => {
    const preview = '[email]->[REDACTED]'
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ records: [{ id: 1, redacted: true, content_preview: preview }], total: 1, limit: 50, offset: 0 }),
    )
    const r = await fetchComplianceRecords()
    expect(r.records[0]!.content_preview).toBe(preview)
  })

  it('★★★ redacted=false 的行 preview 是空串（不是 null、不是原文）', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        records: [{ id: 1, redacted: false, content_preview: '' }],
        total: 1,
        limit: 50,
        offset: 0,
      }),
    )
    const r = await fetchComplianceRecords()
    expect(r.records[0]!.content_preview).toBe('')
    expect(r.records[0]!.content_preview).not.toBeNull()
  })

  it('★★ check_type / hit_type 空白不发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ records: [], total: 0, limit: 50, offset: 0 }))
    await fetchComplianceRecords({ checkType: '  ', hitType: '' })
    expect(lastUrl()).not.toContain('=')
  })

  it('★★ check_type / hit_type 有值发出去并 trim', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ records: [], total: 0, limit: 50, offset: 0 }))
    await fetchComplianceRecords({ checkType: ' pii ', hitType: 'email' })
    expect(lastUrl()).toContain('check_type=pii')
    expect(lastUrl()).toContain('hit_type=email')
  })

  it('★ keywords 的 category 空白不发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ keywords: [] }))
    await fetchComplianceKeywords({ category: '   ' })
    expect(lastUrl()).not.toContain('category=')
  })
})

describe('形状不符抛错（含 500 不许退化成空清单）', () => {
  it('★★★ records 缺 records ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ total: 0 }))
    await expect(fetchComplianceRecords()).rejects.toThrow(/形状不符/)
  })

  it('★★★ review-queue 缺 items ⇒ 抛错（500 时绝不显示成「队列为空」）', async () => {
    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 500,
      json: async () => ({ error: 'Failed to scan review item' }),
      text: async () => JSON.stringify({ error: 'Failed to scan review item' }),
      headers: new Headers({ 'content-type': 'application/json' }),
    } as unknown as Response)
    await expect(fetchComplianceReviewQueue()).rejects.toThrow(/Failed to scan review item/)
  })

  it('★★★ keywords 缺 keywords ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({}))
    await expect(fetchComplianceKeywords()).rejects.toThrow(/形状不符/)
  })

  it('★★★ feedback 缺 feedback 键 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ limit: 20 }))
    await expect(fetchComplianceFeedback()).rejects.toThrow(/形状不符/)
  })

  it('★★ stats 缺 total_issues ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ blocked: 0 }))
    await expect(fetchComplianceStats()).rejects.toThrow(/形状不符/)
  })

  it('★ unwrap 可单测且文案说清实得类型', () => {
    expect(() => unwrapComplianceRecords(null)).toThrow(/实得 null/)
    expect(() => unwrapComplianceQueue('x')).toThrow(/实得 string/)
    expect(() => unwrapComplianceKeywords([])).toThrow(/实得 array/)
    expect(() => unwrapComplianceStats(null)).toThrow(/形状不符/)
  })
})
/**
 * ★★★★★★ 响应键必须**逐字**对得上后端 `writeJSON` 的 map 字面量。
 *
 * **为什么要有这一组**（本轮真实踩到）：
 * `unwrapComplianceFeedback` 第一版把键写成 `items`（后端实际是 `feedback`），
 * 而**当时的夹具也照着 `items` 写** ⇒ 三条用例全绿，
 * 对真后端却 **100% 抛错**，并且带着这个 bug 过了两次提交。
 *
 * ⇒ 夹具证明的是「**代码符合我对契约的理解**」，不是「代码符合真实契约」。
 * 所以这一组不复用任何 helper，**每个端点的响应体都是从后端源码抄的**，
 * 并在用例名里带上后端行号——后端改了行号或键名，这一组会先响。
 *
 * 后端逐条对应（`admin/output_compliance_handler.go`）：
 *   :208  writeJSON(w, 200, policy)                                  → policy **无包装键**
 *   :445  map[string]interface{}{"keywords": keywords}               → `keywords`
 *   :602  {"items":…, "status":…, "limit":…, "offset":…}             → `items`
 *   :711  {"feedback":…, "limit":…, "offset":…}                      → `feedback`  ★ 不是 items
 *   :799  {"total_issues":…, "blocked":…, …}                         → **扁平**，无包装键
 *   :924  {"records":…, "total":…, "limit":…, …}                     → `records` + `total`
 */
describe('★★★★★★ 响应键逐字对得上 writeJSON（后端行号钉死）', () => {
  // ── 正向：抄来的字面量必须被接受 ──
  it('★★★★★★ stats 的响应是**扁平**对象（:799），没有 `data`/`stats` 之类的包装键', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statsBody()))
    const s = await fetchComplianceStats()
    expect(s.total_issues).toBe(3)
    // ★ 若曾把它误当成 `{stats:{…}}`，这里会直接抛错而不是静默拿到 undefined。
    expect(Object.prototype.hasOwnProperty.call(s, 'data')).toBe(false)
  })

  it('★★★★★★ records 的键是 `records`，且带 `total`（:924）', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ records: [], total: 0, limit: 50, offset: 0 }),
    )
    const r = await fetchComplianceRecords()
    expect(r.records).toEqual([])
    expect(r.total).toBe(0)
  })

  it('★★★★★★ review-queue 的键是 `items`（:602），**没有** `feedback`', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ items: [], status: 'pending', limit: 20, offset: 0 }),
    )
    const q = await fetchComplianceReviewQueue()
    expect(q.items).toEqual([])
    expect(q.status).toBe('pending')
  })

  it('★★★★★★ feedback 的键是 `feedback`（:711），**不是** `items`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ feedback: [], limit: 20, offset: 0 }))
    const f = await fetchComplianceFeedback()
    expect(f.feedback).toEqual([])
    expect(f.limit).toBe(20)
    expect(f.offset).toBe(0)
  })

  it('★★★★★★ keywords 的键是 `keywords`（:445），不是 `items`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ keywords: [] }))
    const k = await fetchComplianceKeywords()
    expect(k.keywords).toEqual([])
  })

  it('★★★★★★ policy 是**裸对象**（:208），没有 `policy` 之类的包装键', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(policyBody()))
    const p = await fetchCompliancePolicy()
    expect(p.id).toBe(7)
    expect(Object.prototype.hasOwnProperty.call(p, 'policy')).toBe(false)
  })

  // ── 反向：同族兄弟的键**互不通用**（这一条就是本轮那个 bug 的守门人） ──
  it('★★★★★★ 把 review-queue 的 `items` 喂给 feedback ⇒ 必须抛错（:711 的键是 feedback）', async () => {
    // ★ 这正是第一版代码能过的那个形状：它当时认的就是 items。
    fetchMock.mockResolvedValueOnce(jsonResponse({ items: [], limit: 20, offset: 0 }))
    await expect(fetchComplianceFeedback()).rejects.toThrow(/feedback/)
  })

  it('★★★★★★ 把 feedback 的 `feedback` 喂给 review-queue ⇒ 必须抛错（:602 的键是 items）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ feedback: [], limit: 20, offset: 0 }))
    await expect(fetchComplianceReviewQueue()).rejects.toThrow(/形状不符/)
  })

  it('★★★★★★ `keywords` 与 `feedback` 的键也互不通用', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ feedback: [] }))
    await expect(fetchComplianceKeywords()).rejects.toThrow(/形状不符/)
    fetchMock.mockResolvedValueOnce(jsonResponse({ keywords: [] }))
    await expect(fetchComplianceFeedback()).rejects.toThrow(/形状不符/)
  })

  // ── 反向：错误的包装形态一律不认（不是「宽容解包」） ──
  it('★★★★★★ 包一层 `{data:{…}}` 一律抛错（后端没有这一层）', async () => {
    for (const body of [
      { data: { feedback: [], limit: 20, offset: 0 } },
      { result: { feedback: [], limit: 20, offset: 0 } },
    ]) {
      fetchMock.mockResolvedValueOnce(jsonResponse(body))
      await expect(fetchComplianceFeedback()).rejects.toThrow(/形状不符/)
    }
  })

  it('★★★★★★ 顶层是数组时抛错（后端永不含裸数组）', async () => {
    for (const unwrap of [unwrapComplianceFeedback, unwrapComplianceKeywords]) {
      expect(() => unwrap([])).toThrow(/形状不符/)
      expect(() => unwrap(null)).toThrow(/形状不符/)
      expect(() => unwrap('x')).toThrow(/形状不符/)
    }
  })

  it('★★ 抛错文案说清**期望的键名**，便于线上定位', () => {
    // ★ 文案里必须出现后端真实的键名，否则线上报错会指向错的字段。
    expect(() => unwrapComplianceFeedback({ items: [] })).toThrow(/\{feedback:\[…\], limit, offset\}/)
    expect(() => unwrapComplianceKeywords({ items: [] })).toThrow(/\{keywords:\[…\]?\}/)
  })
})
