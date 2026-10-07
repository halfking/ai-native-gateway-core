import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchFormatAnomalies,
  fetchFormatAnomalySummary,
  unwrapFormatAnomalies,
  unwrapFormatAnomalySummary,
  formatLimitIsSendable,
  formatOffsetIsSendable,
  formatHoursIsSendable,
  formatCountCoversWholeResult,
  formatHasMorePages,
  formatIsLastPage,
  formatProviderCodeIsUnmatchable,
  formatHasStructure,
  formatHasNumericValue,
  formatNumericValue,
  formatNumericValueIsZero,
  formatRequestIdIsEmpty,
  formatAnomalyTypeIsKnown,
  formatSeverityIsTableDefault,
  formatSummaryAffectedWithinCount,
  formatSummaryResolvedWithinCount,
  formatSummaryIsHourDesc,
  formatSummaryIsCountDescWithinHour,
  formatSummaryAvgIsAbsent,
  formatSummaryAtSqlCap,
  formatHoursWasRewritten,
  FORMAT_ANOMALY_LIMIT_DEFAULT,
  FORMAT_ANOMALY_LIMIT_MAX,
  FORMAT_ANOMALY_OFFSET_DEFAULT,
  FORMAT_SUMMARY_HOURS_DEFAULT,
  FORMAT_SUMMARY_HOURS_MAX,
  FORMAT_SUMMARY_SQL_LIMIT,
  FORMAT_CONTENT_SIZE_KEY,
  FORMAT_SEVERITY_TABLE_DEFAULT,
  FORMAT_ANOMALY_RECORD_ALWAYS_KEYS,
  FORMAT_ANOMALY_RECORD_OPTIONAL_KEYS,
  FORMAT_ANOMALY_RECORD_NUMERIC_OPTIONAL_KEYS,
  FORMAT_SUMMARY_ROW_ALWAYS_KEYS,
  FORMAT_SUMMARY_ROW_OPTIONAL_KEYS,
  FORMAT_ANOMALIES_KEYS,
  FORMAT_SUMMARY_KEYS,
  type FormatAnomalyRecord,
  type FormatAnomaliesResponse,
  type FormatAnomalySummaryRow,
  type FormatAnomalySummaryResponse,
} from './formatAnomalies'

/**
 * 响应格式异常（明细 + 汇总）的契约测试（2026-10-08，第八十二批）。
 *
 * 后端：`admin/handler.go:913/914`（两个都是 `h.superAdmin`）
 * → `admin/format_anomalies.go`（list `:51-192`、summary `:194-267`）。
 *
 * 重点是源文件头写明的十五件事 (1)…(15)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 */

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}
function lastUrl(): string {
  return String(fetchMock.mock.calls[0]![0])
}

// ── 夹具：七恒在键 + 若干条件键（逐字照抄 format_anomalies.go:14-35） ──

function row(over: Partial<FormatAnomalyRecord> = {}): FormatAnomalyRecord {
  return {
    id: 9001,
    detected_at: '2026-10-08T02:00:00Z',
    request_id: 'req-abc',
    anomaly_type: 'missing_usage_block',
    severity: 'medium',
    resolved: false,
    created_at: '2026-10-08T02:00:01Z',
    provider_id: 7,
    provider_code: 'openai',
    client_model: 'glm-5.2',
    outbound_model: 'glm-5.2',
    usage_source: 'stream',
    expected_tokens: 1024,
    actual_tokens: 0,
    content_size_bytes: 2048,
    response_structure: { finish_reason: 'length' },
    response_sample: 'finish_reason=length',
    tenant_id: 'default',
    ...over,
  }
}

/** 七个恒在键、没有任何条件键的最小行（DB 里那些列全是 NULL）。 */
function bare(over: Partial<FormatAnomalyRecord> = {}): FormatAnomalyRecord {
  return {
    id: 1,
    detected_at: '2026-10-08T01:00:00Z',
    request_id: 'req-1',
    anomaly_type: 'extraction_failed',
    severity: 'low',
    resolved: false,
    created_at: '2026-10-08T01:00:01Z',
    ...over,
  }
}

function lst(over: Partial<FormatAnomaliesResponse> = {}): FormatAnomaliesResponse {
  return {
    anomalies: [row()],
    count: 1,
    limit: 50,
    offset: 0,
    ...over,
  }
}

function srow(over: Partial<FormatAnomalySummaryRow> = {}): FormatAnomalySummaryRow {
  return {
    hour: '2026-10-08T02:00:00Z',
    anomaly_type: 'missing_usage_block',
    severity: 'medium',
    anomaly_count: 12,
    affected_requests: 8,
    resolved_count: 3,
    provider_code: 'openai',
    client_model: 'glm-5.2',
    avg_content_size: 2048,
    avg_expected_tokens: 1024,
    avg_actual_tokens: 0,
    ...over,
  }
}

function summ(over: Partial<FormatAnomalySummaryResponse> = {}): FormatAnomalySummaryResponse {
  return {
    summaries: [srow()],
    count: 1,
    hours: 24,
    ...over,
  }
}

function okList(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse(lst()))
}
function okSummary(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse(summ()))
}

// ═════════════════════════════════════════════════════════════════════════════
// URL 与参数
// ═════════════════════════════════════════════════════════════════════════════

describe('URL 与参数：明细', () => {
  it('★ 无参数 ⇒ 裸路径', async () => {
    okList()
    await fetchFormatAnomalies()
    expect(lastUrl()).toBe('/api/admin/format-anomalies')
  })

  it('★ limit=50 与 offset=0 都发得出去', async () => {
    okList()
    await fetchFormatAnomalies({ limit: 50, offset: 0 })
    expect(lastUrl()).toContain('limit=50')
    expect(lastUrl()).toContain('offset=0')
  })

  it('★ limit=500 上界发得出去', () => {
    expect(formatLimitIsSendable(500)).toBe(true)
  })

  it('★ limit=501 不发（越界静默回落 50，不是 400）', () => {
    expect(formatLimitIsSendable(501)).toBe(false)
  })

  it('★ limit=0 不发（后端 limit <= 0 回落）', () => {
    expect(formatLimitIsSendable(0)).toBe(false)
  })

  it('★ limit=1 下界发得出去', () => {
    expect(formatLimitIsSendable(1)).toBe(true)
  })

  it('★ limit=-3 不发', () => {
    expect(formatLimitIsSendable(-3)).toBe(false)
  })

  it('★ limit=NaN 不发', () => {
    expect(formatLimitIsSendable(Number.NaN)).toBe(false)
  })

  it('★ limit=Infinity 不发', () => {
    expect(formatLimitIsSendable(Number.POSITIVE_INFINITY)).toBe(false)
  })

  it('★ limit=undefined / null 不发', () => {
    expect(formatLimitIsSendable(undefined)).toBe(false)
    expect(formatLimitIsSendable(null)).toBe(false)
  })

  it('★ limit=49.9 截断成 49 后发出', async () => {
    okList()
    await fetchFormatAnomalies({ limit: 49.9 })
    expect(lastUrl()).toContain('limit=49')
    expect(lastUrl()).not.toContain('49.9')
  })

  it('★ offset=0 发得出去', () => {
    expect(formatOffsetIsSendable(0)).toBe(true)
  })

  it('★ offset=-1 不发（后端 offset < 0 回落 0）', () => {
    expect(formatOffsetIsSendable(-1)).toBe(false)
  })

  it('★ ★ offset 没有上界：999999 发得出去', () => {
    // 后端 :68-71 只挡负数，`limit` 有上界而 `offset` 没有
    expect(formatOffsetIsSendable(999_999)).toBe(true)
  })

  it('★ offset=undefined / NaN 不发', () => {
    expect(formatOffsetIsSendable(undefined)).toBe(false)
    expect(formatOffsetIsSendable(Number.NaN)).toBe(false)
  })

  it('★ provider / model / anomaly_type 三个字符串过滤发得出去', async () => {
    okList()
    await fetchFormatAnomalies({ provider: 'openai', model: 'glm-5.2', anomalyType: 'missing_usage_block' })
    expect(lastUrl()).toContain('provider=openai')
    expect(lastUrl()).toContain('model=glm-5.2')
    expect(lastUrl()).toContain('anomaly_type=missing_usage_block')
  })

  it('★ 空串 provider 照发（后端 TrimSpace 后等价于不过滤）', async () => {
    okList()
    await fetchFormatAnomalies({ provider: '' })
    expect(lastUrl()).toContain('provider=')
  })

  it('★ unresolvedOnly=true 发的是 true 字面量', async () => {
    okList()
    await fetchFormatAnomalies({ unresolvedOnly: true })
    expect(lastUrl()).toContain('unresolved_only=true')
  })

  it('★ unresolvedOnly=false 也照发（queryBool 认假值）', async () => {
    okList()
    await fetchFormatAnomalies({ unresolvedOnly: false })
    expect(lastUrl()).toContain('unresolved_only=false')
  })

  it('★ unresolvedOnly 不传则不发该键', async () => {
    okList()
    await fetchFormatAnomalies({})
    expect(lastUrl()).not.toContain('unresolved_only')
  })
})

describe('URL 与参数：汇总', () => {
  it('★ 无参数 ⇒ 裸路径', async () => {
    okSummary()
    await fetchFormatAnomalySummary()
    expect(lastUrl()).toBe('/api/admin/format-anomaly-summary')
  })

  it('★ hours=24 发得出去', async () => {
    okSummary()
    await fetchFormatAnomalySummary({ hours: 24 })
    expect(lastUrl()).toContain('hours=24')
  })

  it('★ hours=720 上界发得出去（24*30）', () => {
    expect(formatHoursIsSendable(720)).toBe(true)
  })

  it('★ ★ hours=721 不发（上界是 720 小时不是 30 天）', () => {
    expect(formatHoursIsSendable(721)).toBe(false)
  })

  it('★ hours=0 不发（后端 hours <= 0 回落 24）', () => {
    expect(formatHoursIsSendable(0)).toBe(false)
  })

  it('★ hours=1 下界发得出去', () => {
    expect(formatHoursIsSendable(1)).toBe(true)
  })

  it('★ hours=-24 不发', () => {
    expect(formatHoursIsSendable(-24)).toBe(false)
  })

  it('★ hours=undefined / NaN 不发', () => {
    expect(formatHoursIsSendable(undefined)).toBe(false)
    expect(formatHoursIsSendable(Number.NaN)).toBe(false)
  })

  it('★ 汇总没有 limit 参数（上限 200 硬编码在 SQL 里）', async () => {
    okSummary()
    await fetchFormatAnomalySummary({ hours: 24 })
    expect(lastUrl()).not.toContain('limit=')
  })

  it('后端常量：limit 50/500、offset 0、hours 24/720、SQL 上限 200', () => {
    expect(FORMAT_ANOMALY_LIMIT_DEFAULT).toBe(50)
    expect(FORMAT_ANOMALY_LIMIT_MAX).toBe(500)
    expect(FORMAT_ANOMALY_OFFSET_DEFAULT).toBe(0)
    expect(FORMAT_SUMMARY_HOURS_DEFAULT).toBe(24)
    expect(FORMAT_SUMMARY_HOURS_MAX).toBe(720)
    expect(FORMAT_SUMMARY_SQL_LIMIT).toBe(200)
  })

  it('后端常量：内容体积的键名带 _bytes 后缀', () => {
    expect(FORMAT_CONTENT_SIZE_KEY).toBe('content_size_bytes')
  })

  it('后端常量：这张表的 severity 建表缺省是 medium', () => {
    expect(FORMAT_SEVERITY_TABLE_DEFAULT).toBe('medium')
  })

  it('后端常量：七恒在键 / 十三条件键 / 四整型条件键', () => {
    expect([...FORMAT_ANOMALY_RECORD_ALWAYS_KEYS]).toEqual([
      'id',
      'detected_at',
      'request_id',
      'anomaly_type',
      'severity',
      'resolved',
      'created_at',
    ])
    expect(FORMAT_ANOMALY_RECORD_OPTIONAL_KEYS).toHaveLength(13)
    expect([...FORMAT_ANOMALY_RECORD_NUMERIC_OPTIONAL_KEYS]).toEqual([
      'provider_id',
      'expected_tokens',
      'actual_tokens',
      'content_size_bytes',
    ])
    expect(FORMAT_SUMMARY_ROW_ALWAYS_KEYS).toHaveLength(6)
    expect(FORMAT_SUMMARY_ROW_OPTIONAL_KEYS).toHaveLength(5)
    expect([...FORMAT_ANOMALIES_KEYS]).toEqual(['anomalies', 'count', 'limit', 'offset'])
    expect([...FORMAT_SUMMARY_KEYS]).toEqual(['summaries', 'count', 'hours'])
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 形状校验：明细
// ═════════════════════════════════════════════════════════════════════════════

describe('明细形状校验', () => {
  it('★ 响应不是裸对象 ⇒ 抛错', () => {
    expect(() => unwrapFormatAnomalies(null)).toThrow(/格式异常明细 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ 响应是数组 ⇒ 抛错（点明实得 array）', () => {
    expect(() => unwrapFormatAnomalies([])).toThrow(/实得 array/)
  })

  it('★ 顶层缺 offset ⇒ 抛错并点名 offset', () => {
    const d = { ...lst() } as Record<string, unknown>
    delete d['offset']
    expect(() => unwrapFormatAnomalies(d)).toThrow(/缺 1 个键（offset）/)
  })

  it('★ 顶层缺 limit ⇒ 抛错并点名 limit', () => {
    const d = { ...lst() } as Record<string, unknown>
    delete d['limit']
    expect(() => unwrapFormatAnomalies(d)).toThrow(/缺 1 个键（limit）/)
  })

  it('★ anomalies 是空数组是合法的', () => {
    expect(unwrapFormatAnomalies(lst({ anomalies: [], count: 0 })).anomalies).toEqual([])
  })

  it('★ anomalies 是 null ⇒ 抛错（不是 []，是 null）', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: null as unknown as FormatAnomalyRecord[] })),
    ).toThrow(/anomalies 不是数组/)
  })

  it('★ count 不是数字 ⇒ 抛错点名 count', () => {
    expect(() => unwrapFormatAnomalies(lst({ count: '1' as unknown as number }))).toThrow(
      /的 count 不是数字/,
    )
  })

  it('★ 行缺 request_id ⇒ 抛错并点名 request_id（它是恒在键）', () => {
    const r = { ...row() } as Record<string, unknown>
    delete r['request_id']
    expect(() => unwrapFormatAnomalies(lst({ anomalies: [r as unknown as FormatAnomalyRecord] }))).toThrow(
      /anomalies\[0\] 缺 1 个键（request_id）/,
    )
  })

  it('★ 行缺 created_at ⇒ 抛错并点名 created_at', () => {
    const r = { ...row() } as Record<string, unknown>
    delete r['created_at']
    expect(() => unwrapFormatAnomalies(lst({ anomalies: [r as unknown as FormatAnomalyRecord] }))).toThrow(
      /缺 1 个键（created_at）/,
    )
  })

  it('★ ★ created_at 键齐全但类型错 ⇒ 抛错点名 created_at（类型检查那一支）', () => {
    // ★ 这条才是「删掉两个 time 字段的类型检查」能打掉的：
    //   缺键那条被 requireKeys 兜住 ⇒ 删掉类型检查后仍红 ⇒ 锚点必须指这条。
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), created_at: 1757000000 as unknown as string }] })),
    ).toThrow(/anomalies\[0\] 的 created_at 不是字符串/)
  })

  it('★ detected_at 键齐全但类型错 ⇒ 抛错点名 detected_at', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), detected_at: null as unknown as string }] })),
    ).toThrow(/anomalies\[0\] 的 detected_at 不是字符串/)
  })

  it('★ id 是字符串 ⇒ 抛错点名 id', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), id: '1' as unknown as number }] })),
    ).toThrow(/anomalies\[0\] 的 id 不是数字/)
  })

  it('★ request_id 是数字 ⇒ 抛错点名 request_id（类型检查那一支）', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), request_id: 7 as unknown as string }] })),
    ).toThrow(/anomalies\[0\] 的 request_id 不是字符串/)
  })

  it('★ resolved 是 0 ⇒ 抛错点名 resolved', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), resolved: 0 as unknown as boolean }] })),
    ).toThrow(/anomalies\[0\] 的 resolved 不是布尔/)
  })

  it('★ provider_id 是字符串 ⇒ 抛错点名 provider_id', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), provider_id: '7' as unknown as number }] })),
    ).toThrow(/anomalies\[0\] 的 provider_id 不是数字/)
  })

  it('★ content_size_bytes 键缺**不抛错**（条件键缺 = DB 里是 NULL）', () => {
    // ★★ 这是最容易犯的错：Go 字段叫 ContentSize，JSON 键却叫 content_size_bytes。
    //   但它是**条件键** ⇒ 解包器不要求它存在 ⇒ 写错键名不会抛错，只是取不到值。
    const r = { ...bare() } as Record<string, unknown>
    delete r['content_size_bytes']
    expect(() => unwrapFormatAnomalies(lst({ anomalies: [r as unknown as FormatAnomalyRecord] }))).not.toThrow()
  })

  it('★ ★ content_size_bytes 键在但类型错 ⇒ 抛错点名它（整型条件键那一支）', () => {
    // ★ 这条才是「从整型条件键列表里删掉 content_size_bytes」能打掉的：
    //   缺键那条是合法形状（DB 里 NULL）⇒ 解包器本来就不该抛。
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), content_size_bytes: '2048' as unknown as number }] })),
    ).toThrow(/anomalies\[0\] 的 content_size_bytes 不是数字/)
  })

  it('★ ★ 写错键名（content_size）⇒ 静默 undefined，解包器毫无察觉', () => {
    const r = { ...row(), content_size: 2048 } as Record<string, unknown>
    delete r['content_size_bytes']
    const parsed = unwrapFormatAnomalies(lst({ anomalies: [r as unknown as FormatAnomalyRecord] }))
    expect((parsed.anomalies[0] as unknown as Record<string, unknown>)['content_size']).toBe(2048)
    expect(formatNumericValue(parsed.anomalies[0]!, 'content_size_bytes')).toBeUndefined()
  })

  it('★ 走取值入口能正确取到 2048（键名写对的那一格）', () => {
    expect(formatNumericValue(row(), 'content_size_bytes')).toBe(2048)
  })

  it('★ tenant_id 是数字 ⇒ 抛错点名 tenant_id', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), tenant_id: 3 as unknown as string }] })),
    ).toThrow(/anomalies\[0\] 的 tenant_id 不是字符串/)
  })

  it('★ response_structure 是数组 ⇒ 抛错（它是 map，只可能是对象）', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), response_structure: [] as unknown as Record<string, unknown> }] })),
    ).toThrow(/response_structure 不是对象/)
  })

  it('★ 第二行错类型时报出下标 1', () => {
    expect(() =>
      unwrapFormatAnomalies(lst({ anomalies: [row(), { ...row(), id: 'x' as unknown as number }] })),
    ).toThrow(/anomalies\[1\] 的 id 不是数字/)
  })

  it('★ 十三个条件键全缺是合法的', () => {
    const r = unwrapFormatAnomalies(lst({ anomalies: [bare()], count: 1 }))
    expect('provider_code' in r.anomalies[0]!).toBe(false)
    expect('response_structure' in r.anomalies[0]!).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 形状校验：汇总
// ═════════════════════════════════════════════════════════════════════════════

describe('汇总形状校验', () => {
  it('★ 响应不是裸对象 ⇒ 抛错', () => {
    expect(() => unwrapFormatAnomalySummary(null)).toThrow(
      /格式异常汇总 响应形状不符：期望裸对象，实得 null/,
    )
  })

  it('★ 顶层缺 hours ⇒ 抛错并点名 hours', () => {
    const d = { ...summ() } as Record<string, unknown>
    delete d['hours']
    expect(() => unwrapFormatAnomalySummary(d)).toThrow(/缺 1 个键（hours）/)
  })

  it('★ summaries 是空数组是合法的', () => {
    expect(unwrapFormatAnomalySummary(summ({ summaries: [], count: 0 })).summaries).toEqual([])
  })

  it('★ summaries 是 null ⇒ 抛错', () => {
    expect(() =>
      unwrapFormatAnomalySummary(summ({ summaries: null as unknown as FormatAnomalySummaryRow[] })),
    ).toThrow(/summaries 不是数组/)
  })

  it('★ 行缺 affected_requests ⇒ 抛错并点名它', () => {
    const s = { ...srow() } as Record<string, unknown>
    delete s['affected_requests']
    expect(() =>
      unwrapFormatAnomalySummary(summ({ summaries: [s as unknown as FormatAnomalySummaryRow] })),
    ).toThrow(/summaries\[0\] 缺 1 个键（affected_requests）/)
  })

  it('★ 行缺 resolved_count ⇒ 抛错并点名它', () => {
    const s = { ...srow() } as Record<string, unknown>
    delete s['resolved_count']
    expect(() =>
      unwrapFormatAnomalySummary(summ({ summaries: [s as unknown as FormatAnomalySummaryRow] })),
    ).toThrow(/缺 1 个键（resolved_count）/)
  })

  it('★ hour 是数字 ⇒ 抛错点名 hour', () => {
    expect(() =>
      unwrapFormatAnomalySummary(summ({ summaries: [{ ...srow(), hour: 1 as unknown as string }] })),
    ).toThrow(/summaries\[0\] 的 hour 不是字符串/)
  })

  it('★ anomaly_count 是字符串 ⇒ 抛错点名 anomaly_count', () => {
    expect(() =>
      unwrapFormatAnomalySummary(
        summ({ summaries: [{ ...srow(), anomaly_count: '12' as unknown as number }] }),
      ),
    ).toThrow(/anomaly_count 不是数字/)
  })

  it('★ avg_content_size 是字符串 ⇒ 抛错点名 avg_content_size', () => {
    expect(() =>
      unwrapFormatAnomalySummary(
        summ({ summaries: [{ ...srow(), avg_content_size: '2048' as unknown as number }] }),
      ),
    ).toThrow(/summaries\[0\] 的 avg_content_size 不是数字/)
  })

  it('★ avg_expected_tokens 是字符串 ⇒ 抛错点名它', () => {
    expect(() =>
      unwrapFormatAnomalySummary(
        summ({ summaries: [{ ...srow(), avg_expected_tokens: 'x' as unknown as number }] }),
      ),
    ).toThrow(/avg_expected_tokens 不是数字/)
  })

  it('★ 汇总行的 provider_code 是数字 ⇒ 抛错点名它', () => {
    expect(() =>
      unwrapFormatAnomalySummary(summ({ summaries: [{ ...srow(), provider_code: 7 as unknown as string }] })),
    ).toThrow(/summaries\[0\] 的 provider_code 不是字符串/)
  })

  it('★ 三个 AVG 键全缺是合法的（AVG 返回 NULL）', () => {
    const s = srow()
    delete (s as unknown as Record<string, unknown>)['avg_content_size']
    delete (s as unknown as Record<string, unknown>)['avg_expected_tokens']
    delete (s as unknown as Record<string, unknown>)['avg_actual_tokens']
    expect(unwrapFormatAnomalySummary(summ({ summaries: [s] })).summaries[0]!.avg_content_size).toBeUndefined()
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (1)(2)：count 是全量、limit/offset 回显
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★★ (1) count 是全表命中数而不是本页长度', () => {
  it('★ count 大于本页长度 ⇒ 覆盖判据成立', () => {
    const r = lst({ anomalies: [row(), row({ id: 2 })], count: 137, limit: 2 })
    expect(formatCountCoversWholeResult(r)).toBe(true)
  })

  it('★ count 等于本页长度 ⇒ 也成立（恰好装完）', () => {
    expect(formatCountCoversWholeResult(lst())).toBe(true)
  })

  it('★ count 小于本页长度 ⇒ 不成立（这条在真实后端不可能出现）', () => {
    expect(formatCountCoversWholeResult(lst({ anomalies: [row(), row({ id: 2 })], count: 1 }))).toBe(false)
  })

  it('★ ★ 与批 78/81 的语义相反：count===length 在这里不恒真', () => {
    const r = lst({ anomalies: [row()], count: 137, limit: 1 })
    expect(r.count === r.anomalies.length).toBe(false)
    expect(formatCountCoversWholeResult(r)).toBe(true)
  })
})

describe('★★★★ (2) limit/offset 回显 ⇒ 分页判定', () => {
  it('★ 还有下一页 ⇒ 成立', () => {
    const r = lst({ anomalies: [row(), row({ id: 2 })], count: 137, limit: 2, offset: 0 })
    expect(formatHasMorePages(r)).toBe(true)
  })

  it('★ 最后一页 ⇒ 不成立（反向）', () => {
    const r = lst({ anomalies: [row()], count: 137, limit: 50, offset: 136 })
    expect(formatHasMorePages(r)).toBe(false)
  })

  it('★ ★ 中间页：offset>0 时才算得出来（offset=0 的那格区分不开）', () => {
    // ★ 这条才是「漏掉 r.offset」能打掉的：
    //   offset=0 时 `0 + len < count` 与 `len < count` 完全相同 ⇒ 指不到牙。
    const r = lst({ anomalies: [row(), row({ id: 2 })], count: 137, limit: 2, offset: 50 })
    expect(formatHasMorePages(r)).toBe(true)
    const noOffset = lst({ anomalies: [row(), row({ id: 2 })], count: 5, limit: 2, offset: 50 })
    expect(formatHasMorePages(noOffset)).toBe(false)
  })

  it('★ 恰好装完 ⇒ 最后一页（offset + length === count）', () => {
    const r = lst({ anomalies: [row(), row({ id: 2 })], count: 2, limit: 50, offset: 0 })
    expect(formatIsLastPage(r)).toBe(true)
    expect(formatHasMorePages(r)).toBe(false)
  })

  it('★ 空结果且 count=0 ⇒ 最后一页', () => {
    expect(formatIsLastPage(lst({ anomalies: [], count: 0 }))).toBe(true)
  })

  it('★ ★ offset 越过总数 ⇒ 空数组且判「没有更多」（不是「还有」）', () => {
    // 后端 offset 没有上界 ⇒ offset=999999 会拿到空数组，但 count 仍是全量 5000。
    // ⇒ offset + 0 = 999999 >= 5000 ⇒ 已在末尾之外 ⇒ **没有更多**（不是「还有」）。
    const r = lst({ anomalies: [], count: 5000, limit: 50, offset: 999_999 })
    expect(r.anomalies).toEqual([])
    expect(formatHasMorePages(r)).toBe(false)
    expect(formatIsLastPage(r)).toBe(true)
  })

  it('★ offset 未越界但本页为空 ⇒ 仍判「还有下一页」', () => {
    // 这一格才区分得开：offset 在总数之内却拿到空数组（数据被并发删掉等情况）
    const r = lst({ anomalies: [], count: 5000, limit: 50, offset: 4000 })
    expect(formatHasMorePages(r)).toBe(true)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (3)(4)(5)(6)：COALESCE / map / request_id / 键名
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (3) provider_code 缺失 ⇔ 匹配不了 provider 过滤', () => {
  it('★ provider_code 键缺 ⇒ 判为不可匹配', () => {
    expect(formatProviderCodeIsUnmatchable(bare())).toBe(true)
  })

  it('★ provider_code 有值 ⇒ 不判（反向）', () => {
    expect(formatProviderCodeIsUnmatchable(row())).toBe(false)
  })

  it('★ provider_code 是空串 ⇒ **不算**不可匹配（有键就有值）', () => {
    // ★ 换成真值判断的实现会把空串吞掉 ⇒ 这一格是它的牙
    expect(formatProviderCodeIsUnmatchable({ ...row(), provider_code: '' })).toBe(false)
  })
})

describe('★★★★ (4) response_structure 是 map + omitempty', () => {
  it('★ 键在 ⇒ 判有结构', () => {
    expect(formatHasStructure(row())).toBe(true)
  })

  it('★ 键缺 ⇒ 判无结构（反向）', () => {
    expect(formatHasStructure(bare())).toBe(false)
  })


  it('★ ★ response_structure 键在但值是 null ⇒ 键判定为真、真值判定为假', () => {
    // ★ 这一格才是「把 in 换成 !!」的牙：{} 的真值也是 true，区分不开。
    //   （真实后端不会发这个形状，但它是唯一能区分两种实现的输入。）
    const r = { ...row(), response_structure: null as unknown as Record<string, unknown> }
    expect(formatHasStructure(r)).toBe(true)
    expect(Boolean(r.response_structure)).toBe(false)
  })
  it('★ 空对象也是「键在」（map + omitempty 下键在就意味着非空）', () => {
    // ★★ 推论：Go 的 omitempty 对空 map 生效 ⇒ 空 map 会**键缺**而不是发 `{}`
    //   ⇒ 所以「键在」已经蕴含「非空」，「结构为空」这个判据是恒真的，按纪律不提供。
    expect(formatHasStructure({ ...row(), response_structure: {} })).toBe(true)
  })
})

describe('★★★★ (5) request_id 是非指针 string：恒在但可为空串', () => {
  it('★ 空串 ⇒ 判为空请求号', () => {
    expect(formatRequestIdIsEmpty({ ...row(), request_id: '' })).toBe(true)
  })

  it('★ 非空 ⇒ 不判（反向）', () => {
    expect(formatRequestIdIsEmpty(row())).toBe(false)
  })

  it('★ 空串仍然照发（NOT NULL 只保证不是 NULL）', () => {
    const r = unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), request_id: '' }] }))
    expect('request_id' in r.anomalies[0]!).toBe(true)
    expect(r.anomalies[0]!.request_id).toBe('')
  })

  it('★ ★ 与批 81 的 RequestID *string 相反：那里键缺，这里恒在', () => {
    expect('request_id' in row()).toBe(true)
  })
})

describe('★★★ 整型条件键的 0 是合法值', () => {
  it('★ actual_tokens=0 ⇒ 键在且值为 0', () => {
    expect(formatHasNumericValue({ ...row(), actual_tokens: 0 }, 'actual_tokens')).toBe(true)
  })

  it('★ actual_tokens 键缺 ⇒ 判没有值', () => {
    expect(formatHasNumericValue(bare(), 'actual_tokens')).toBe(false)
  })

  it('★ actual_tokens=0 ⇒ 是零值', () => {
    expect(formatNumericValueIsZero({ ...row(), actual_tokens: 0 }, 'actual_tokens')).toBe(true)
  })

  it('★ actual_tokens=1024 ⇒ 不是零值（反向）', () => {
    expect(formatNumericValueIsZero({ ...row(), actual_tokens: 1024 }, 'actual_tokens')).toBe(false)
  })

  it('★ 夹具里 actual_tokens 默认就是 0（zero_completion 这类异常的典型值）', () => {
    expect(formatNumericValueIsZero(row(), 'actual_tokens')).toBe(true)
  })

  it('★ 键缺 ⇒ 既不是「有值」也不是「零值」', () => {
    const r = bare()
    expect(formatHasNumericValue(r, 'actual_tokens')).toBe(false)
    expect(formatNumericValueIsZero(r, 'actual_tokens')).toBe(false)
  })

  it('★ content_size_bytes=0 ⇒ 零值（键名带 _bytes）', () => {
    expect(formatNumericValueIsZero({ ...row(), content_size_bytes: 0 }, 'content_size_bytes')).toBe(true)
  })

  it('★ ★ 陷阱对照：真值判断会把真实的 0 当成「没有」', () => {
    const r = { ...row(), actual_tokens: 0 }
    expect(Boolean(r.actual_tokens)).toBe(false)
    expect(formatHasNumericValue(r, 'actual_tokens')).toBe(true)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (7)(8)：开放域枚举
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★ (7) anomaly_type / severity 是开放域', () => {
  it('★ missing_usage_block 已知', () => {
    expect(formatAnomalyTypeIsKnown(row())).toBe(true)
  })

  it('★ tools_restore_failed 已知（另一份常量文件里的）', () => {
    expect(formatAnomalyTypeIsKnown({ ...row(), anomaly_type: 'tools_restore_failed' })).toBe(true)
  })

  it('★ 未知取值 ⇒ 不判已知，但解包器**不抛错**', () => {
    const r = unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), anomaly_type: 'brand_new_kind' }] }))
    expect(formatAnomalyTypeIsKnown(r.anomalies[0]!)).toBe(false)
  })

  it('★ ★ 大写变体也不判已知（大小写敏感是契约的一部分）', () => {
    // ★ 这条才是「改成 toLowerCase」能打掉的：
    //   用一个**小写形态命中不了**但大写形态与白名单大小写不同的值 ⇒ 两边都 false ⇒ 指不到牙。
    expect(formatAnomalyTypeIsKnown({ ...row(), anomaly_type: 'MISSING_USAGE_BLOCK' })).toBe(false)
  })

  it('★ 未知 severity 也照样放行', () => {
    const r = unwrapFormatAnomalies(lst({ anomalies: [{ ...row(), severity: 'catastrophic' }] }))
    expect(r.anomalies[0]!.severity).toBe('catastrophic')
  })

  it('★ ★ 与批 81 相反：那里 anomaly_type 被 SQL 硬编码可严格校验，这里不行', () => {
    // 批 81 的 fingerprint-drift 端点 WHERE 写死了 anomaly_type ⇒ 每行必然等于它
    // 这里 WHERE 不限定 anomaly_type ⇒ 值由写入端决定 ⇒ 只能是开放域
    expect(formatAnomalyTypeIsKnown(row())).toBe(true)
  })

  it('★ (8) severity=medium ⇒ 等于这张表的建表缺省', () => {
    expect(formatSeverityIsTableDefault(row())).toBe(true)
  })

  it('★ (8) severity=low ⇒ **不是**这张表的缺省（另一张表才是 low）', () => {
    expect(formatSeverityIsTableDefault({ ...row(), severity: 'low' })).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (9)(10)(11)(12)：汇总的 AVG / 去重 / 排序 / 硬编码上限
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★ (9) AVG 为 NULL 时键被省略', () => {
  it('★ avg_content_size 键缺 ⇒ 判「无均值」', () => {
    const s = srow({ anomaly_count: 0 })
    delete (s as unknown as Record<string, unknown>)['avg_content_size']
    expect(formatSummaryAvgIsAbsent(s, 'avg_content_size')).toBe(true)
  })

  it('★ avg_content_size 有值 ⇒ 不判（反向）', () => {
    expect(formatSummaryAvgIsAbsent(srow(), 'avg_content_size')).toBe(false)
  })

  it('★ avg_expected_tokens 键缺 ⇒ 判「无均值」', () => {
    const s = srow()
    delete (s as unknown as Record<string, unknown>)['avg_expected_tokens']
    expect(formatSummaryAvgIsAbsent(s, 'avg_expected_tokens')).toBe(true)
  })

  it('★ avg_actual_tokens 为 0 ⇒ **不算**缺（0 是合法均值）', () => {
    expect(formatSummaryAvgIsAbsent({ ...srow(), avg_actual_tokens: 0 }, 'avg_actual_tokens')).toBe(false)
  })
})

describe('★★★ (10) affected_requests ≤ anomaly_count', () => {
  it('★ 去重数小于总数 ⇒ 成立', () => {
    expect(formatSummaryAffectedWithinCount(srow())).toBe(true)
  })

  it('★ 两者相等 ⇒ 成立（每个请求各一条异常）', () => {
    expect(formatSummaryAffectedWithinCount(srow({ affected_requests: 12, anomaly_count: 12 }))).toBe(true)
  })

  it('★ 去重数大于总数 ⇒ 不成立（反向，后端不可能产出）', () => {
    expect(formatSummaryAffectedWithinCount(srow({ affected_requests: 13, anomaly_count: 12 }))).toBe(false)
  })

  it('★ resolved_count ≤ anomaly_count 同样成立', () => {
    expect(formatSummaryResolvedWithinCount(srow({ resolved_count: 3, anomaly_count: 12 }))).toBe(true)
  })

  it('★ resolved_count 大于总数 ⇒ 不成立（反向）', () => {
    expect(formatSummaryResolvedWithinCount(srow({ resolved_count: 99, anomaly_count: 12 }))).toBe(false)
  })

  it('★ 零异常桶：三个数都是 0', () => {
    const s = srow({ anomaly_count: 0, affected_requests: 0, resolved_count: 0 })
    expect(formatSummaryAffectedWithinCount(s)).toBe(true)
    expect(formatSummaryResolvedWithinCount(s)).toBe(true)
  })
})

describe('★★★★★ (11) 排序是 hour DESC 然后 count DESC', () => {
  it('★ hour 全局非增', () => {
    const r = summ({
      summaries: [
        srow({ hour: '2026-10-08T03:00:00Z' }),
        srow({ hour: '2026-10-08T02:00:00Z' }),
        srow({ hour: '2026-10-08T01:00:00Z' }),
      ],
      count: 3,
    })
    expect(formatSummaryIsHourDesc(r)).toBe(true)
  })

  it('★ hour 升序 ⇒ 判 false（反向）', () => {
    const r = summ({
      summaries: [srow({ hour: '2026-10-08T01:00:00Z' }), srow({ hour: '2026-10-08T03:00:00Z' })],
      count: 2,
    })
    expect(formatSummaryIsHourDesc(r)).toBe(false)
  })

  it('★ 同一 hour 内按 anomaly_count 非增 ⇒ 成立', () => {
    const r = summ({
      summaries: [
        srow({ hour: '2026-10-08T02:00:00Z', anomaly_count: 30 }),
        srow({ hour: '2026-10-08T02:00:00Z', anomaly_count: 20 }),
        srow({ hour: '2026-10-08T02:00:00Z', anomaly_count: 10 }),
      ],
      count: 3,
    })
    expect(formatSummaryIsCountDescWithinHour(r)).toBe(true)
  })

  it('★ 同一 hour 内计数升序 ⇒ 判 false（反向）', () => {
    const r = summ({
      summaries: [
        srow({ hour: '2026-10-08T02:00:00Z', anomaly_count: 10 }),
        srow({ hour: '2026-10-08T02:00:00Z', anomaly_count: 20 }),
      ],
      count: 2,
    })
    expect(formatSummaryIsCountDescWithinHour(r)).toBe(false)
  })

  it('★ ★ 不同 hour 之间不比较计数（第二排序键只在同 hour 内生效）', () => {
    const r = summ({
      summaries: [
        // 新 hour 但计数更小 —— 这是合法的，hour 是第一排序键
        srow({ hour: '2026-10-08T03:00:00Z', anomaly_count: 1 }),
        srow({ hour: '2026-10-08T02:00:00Z', anomaly_count: 100 }),
      ],
      count: 2,
    })
    expect(formatSummaryIsHourDesc(r)).toBe(true)
    expect(formatSummaryIsCountDescWithinHour(r)).toBe(true)
  })

  it('★ hour 相邻相等且计数相等 ⇒ 两个判据都成立', () => {
    const r = summ({
      summaries: [
        srow({ hour: '2026-10-08T02:00:00Z', anomaly_count: 5 }),
        srow({ hour: '2026-10-08T02:00:00Z', anomaly_count: 5 }),
      ],
      count: 2,
    })
    expect(formatSummaryIsHourDesc(r)).toBe(true)
    expect(formatSummaryIsCountDescWithinHour(r)).toBe(true)
  })
})

describe('★★★ (12) SQL 里硬编码的 LIMIT 200', () => {
  it('★ count 拿满 200 ⇒ 判可能被截断', () => {
    expect(formatSummaryAtSqlCap(summ({ count: 200 }))).toBe(true)
  })

  it('★ 少于 200 ⇒ 不判（反向）', () => {
    expect(formatSummaryAtSqlCap(summ())).toBe(false)
  })

  it('★ count 大于 200 ⇒ 也判（后端不可能，但判据不挑）', () => {
    expect(formatSummaryAtSqlCap(summ({ count: 500 }))).toBe(true)
  })
})

describe('★★★ (13) hours 回显生效值', () => {
  it('★ 请求 24 拿到 24 ⇒ 没被改写', () => {
    expect(formatHoursWasRewritten(summ(), 24)).toBe(false)
  })

  it('★ 请求 721 拿到 720 ⇒ 被改写', () => {
    expect(formatHoursWasRewritten(summ({ hours: 720 }), 721)).toBe(true)
  })

  it('★ 请求 0 拿到 24 ⇒ 被改写', () => {
    expect(formatHoursWasRewritten(summ(), 0)).toBe(true)
  })

  it('★ 没传 requestedHours ⇒ 不判', () => {
    expect(formatHoursWasRewritten(summ())).toBe(false)
    expect(formatHoursWasRewritten(summ(), undefined)).toBe(false)
  })

  it('★ 请求 720 拿到 720 ⇒ 没被改写', () => {
    expect(formatHoursWasRewritten(summ({ hours: 720 }), 720)).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ═════════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ 明细正常响应被解包器放行', async () => {
    okList()
    const r = await fetchFormatAnomalies()
    expect(r.limit).toBe(50)
    expect(r.anomalies).toHaveLength(1)
    expect(formatCountCoversWholeResult(r)).toBe(true)
  })

  it('★ 明细形状不符 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ nope: true }))
    await expect(fetchFormatAnomalies()).rejects.toThrow(/格式异常明细 缺/)
  })

  it('★ 汇总正常响应被解包器放行', async () => {
    okSummary()
    const r = await fetchFormatAnomalySummary()
    expect(r.hours).toBe(24)
    expect(formatSummaryAffectedWithinCount(r.summaries[0]!)).toBe(true)
  })

  it('★ 汇总形状不符 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ nope: true }))
    await expect(fetchFormatAnomalySummary()).rejects.toThrow(/格式异常汇总 缺/)
  })

  it('★ 缺 created_at 的行 ⇒ 抛错点名该行下标', async () => {
    const r = { ...row() } as Record<string, unknown>
    delete r['created_at']
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ anomalies: [r], count: 1, limit: 50, offset: 0 }),
    )
    await expect(fetchFormatAnomalies()).rejects.toThrow(/anomalies\[0\] 缺 1 个键（created_at）/)
  })

  it('★ 越界的 limit/offset/hours 都不发', async () => {
    okList()
    await fetchFormatAnomalies({ limit: 501, offset: -1 })
    expect(lastUrl()).not.toContain('limit=')
    expect(lastUrl()).not.toContain('offset=')
  })

  it('★ 超大的 offset 照发（后端无上界）', async () => {
    okList()
    await fetchFormatAnomalies({ offset: 999_999 })
    expect(lastUrl()).toContain('offset=999999')
  })
})