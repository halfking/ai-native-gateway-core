import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchModelIQNodeLatest,
  fetchModelIQHistory,
  fetchModelIQCatalog,
  unwrapModelIQNodeLatest,
  unwrapModelIQHistory,
  unwrapModelIQCatalog,
  modelIQDeriveStatus,
  modelIQHistoryStatusMatchesMetrics,
  modelIQStatusIsKnown,
  modelIQCatalogAggregatesMatchCount,
  modelIQCatalogHasStandardOrNodes,
  modelIQCatalogNodeAvgIsNull,
  modelIQCatalogStandardIqIsNull,
  modelIQNodeHasNoProvider,
  modelIQNodeHasProvider,
  modelIQNodeTestedAtIsNull,
  modelIQNodeTestedAtIsString,
  modelIQHistoryLimitIsAccepted,
  modelIQHistoryEffectiveLimit,
  modelIQHistoryStabilityIsZero,
  modelIQHistoryTestedAtIsRfc3339,
  modelIQCatalogWouldBeHidden,
  modelIQCatalogIncludesStatus,
  MODEL_IQ_DB_NOT_CONFIGURED,
  MODEL_IQ_STATUSES,
  MODEL_IQ_PROBE_KINDS,
  MODEL_IQ_PROBE_DEFAULT,
  MODEL_IQ_TRIGGER_DEFAULT,
  MODEL_IQ_CATALOG_STATUS_FILTER,
  MODEL_IQ_CANONICAL_STATUS_DOMAIN,
  MODEL_IQ_HISTORY_DEFAULT_LIMIT,
  MODEL_IQ_HISTORY_MAX_LIMIT,
  MODEL_IQ_NODE_LATEST_KEYS,
  MODEL_IQ_HISTORY_KEYS,
  MODEL_IQ_CATALOG_KEYS,
  MODEL_IQ_CATALOG_NULLABLE_KEYS,
  type ModelIQNodeLatest,
  type ModelIQHistoryPoint,
  type ModelIQCatalogRow,
} from './modelIQ'

/**
 * 节点智商三端点的契约测试（2026-10-08，第八十五批）。
 *
 * 后端：`admin/handler.go:1470-1472`（三个都 `h.superAdmin(...)`）
 * → `admin/model_iq.go`；status/写入域在 `domains/modelquality/dbstorage.go`。
 *
 * 重点是源文件头写明的十四件事 (1)…(14)。带 ★ 的自校验判据都能被变异打掉。
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

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ── 夹具：逐字照抄 `admin/model_iq.go` 的三个 struct ──

/** `nodeIQLatestRow`（`:27-41`）：13 键，10 个被 COALESCE 兜底。 */
function node(over: Partial<ModelIQNodeLatest> = {}): ModelIQNodeLatest {
  return {
    credential_id: 7,
    credential_label: 'cred-a',
    provider_id: 3,
    provider_name: 'openai',
    raw_model_name: 'gpt-4o',
    canonical_name: 'gpt-4o',
    overall_score: 88.5,
    grade: 'A',
    avg_score: 88.5,
    min_score: 85,
    max_score: 92,
    sample_count: 12,
    // ★ `*string` 无 omitempty；SQL 侧是 `to_char(... '"T"HH24:MI:SS"Z"')`
    tested_at: '2026-10-07T12:00:00Z',
    ...over,
  }
}

/** `iqHistoryPoint`（`:124-134`）：9 键。 */
function point(over: Partial<ModelIQHistoryPoint> = {}): ModelIQHistoryPoint {
  return {
    // ★ `time.Time` ⇒ RFC3339Nano（带小数或至少带 T）
    tested_at: '2026-10-07T12:00:00.123456789Z',
    overall_score: 88.5,
    grade: 'A',
    accuracy: 92.0,
    stability: 100,
    latency_p95: 820,
    probe_kind: 'gateway',
    trigger_kind: 'scheduled',
    // ★ 由 accuracy/stability 派生：accuracy>0 且 stability>=100 ⇒ success
    status: 'success',
    ...over,
  }
}

/** `catalogIQRow`（`:190-201`）：10 键，4 个可为裸 null。 */
function catalog(over: Partial<ModelIQCatalogRow> = {}): ModelIQCatalogRow {
  return {
    canonical_id: 11,
    canonical_name: 'gpt-4o',
    display_name: 'GPT-4o',
    family: 'gpt',
    standard_iq: 90.0,
    standard_iq_source: 'benchmark',
    // ★ node_count>0 ⇒ 三个聚合全非 null（见 (3)）
    node_avg_iq: 88.5,
    node_count: 12,
    max_node_iq: 92,
    min_node_iq: 85,
    ...over,
  }
}

// ══════════════════════════════════════════════════════════════════════════
// (1) 三个端点都是顶层裸数组
// ══════════════════════════════════════════════════════════════════════════

describe('(1) 顶层裸数组', () => {
  it('★ node-latest 顶层是数组 ⇒ 放行（不是 envelope）', () => {
    expect(unwrapModelIQNodeLatest([node()])).toHaveLength(1)
  })

  it('★ ★ node-latest 空数组 ⇒ 放行（out := []T{} 初始化，不是 null）', () => {
    expect(unwrapModelIQNodeLatest([])).toEqual([])
  })

  it('★ history 顶层是数组 ⇒ 放行', () => {
    expect(unwrapModelIQHistory([point()])).toHaveLength(1)
  })

  it('★ ★ history 空数组 ⇒ 放行', () => {
    expect(unwrapModelIQHistory([])).toEqual([])
  })

  it('★ catalog 顶层是数组 ⇒ 放行', () => {
    expect(unwrapModelIQCatalog([catalog()])).toHaveLength(1)
  })

  it('★ ★★ catalog 空数组 ⇒ 放行', () => {
    expect(unwrapModelIQCatalog([])).toEqual([])
  })

  it('★ ★★★ node-latest 顶层是对象 ⇒ 抛（**不是** {data:…} 信封）', () => {
    expect(() => unwrapModelIQNodeLatest({ data: [node()] })).toThrow(
      /节点智商最新 响应形状不符：期望顶层裸数组，实得 object/,
    )
  })

  it('★ history 顶层是对象 ⇒ 抛', () => {
    expect(() => unwrapModelIQHistory({ items: [] })).toThrow(
      /智商历史 响应形状不符：期望顶层裸数组，实得 object/,
    )
  })

  it('★ catalog 顶层是 null ⇒ 抛并报 null', () => {
    expect(() => unwrapModelIQCatalog(null)).toThrow(/期望顶层裸数组，实得 null/)
  })

  it('★ 顶层是字符串 ⇒ 抛并报 string', () => {
    expect(() => unwrapModelIQCatalog('x')).toThrow(/期望顶层裸数组，实得 string/)
  })

  it('★ ★ 数组里的元素不是对象 ⇒ 抛并点名下标', () => {
    expect(() => unwrapModelIQNodeLatest(['x'])).toThrow(/节点智商最新\[0\] 响应形状不符：期望裸对象，实得 string/)
  })

  it('★ ★ 数组里的元素是 null ⇒ 抛并报 null', () => {
    expect(() => unwrapModelIQCatalog([null])).toThrow(/智商目录\[0\] 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ ★★★ 数组元素本身是数组 ⇒ 抛（`typeof` 是 object，只有 Array.isArray 能拦）', () => {
    expect(() => unwrapModelIQHistory([[]])).toThrow(/智商历史\[0\] 响应形状不符：期望裸对象，实得 array/)
  })

  it('★ 第二项元素出错时点名下标 1', () => {
    expect(() => unwrapModelIQHistory([point(), null])).toThrow(/智商历史\[1\]/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5) node-latest 字段校验
// ══════════════════════════════════════════════════════════════════════════

describe('(5) node-latest 字段校验', () => {
  it('★ 缺一个键 ⇒ 抛并点名（13 键恒在）', () => {
    const g = del(node() as unknown as Record<string, unknown>, 'sample_count')
    expect(() => unwrapModelIQNodeLatest([g])).toThrow(/节点智商最新\[0\] 缺 1 个键（sample_count）/)
  })

  it('★ ★ 缺三个键 ⇒ 抛并报数量与键名顺序', () => {
    let g = node() as unknown as Record<string, unknown>
    g = del(g, 'credential_id')
    g = del(g, 'grade')
    g = del(g, 'max_score')
    expect(() => unwrapModelIQNodeLatest([g])).toThrow(
      /节点智商最新\[0\] 缺 3 个键（credential_id, grade, max_score）/,
    )
  })

  it('★ credential_id 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelIQNodeLatest([node({ credential_id: '7' as unknown as number })])).toThrow(
      /\[0\] 的 credential_id 不是数字/,
    )
  })

  it('★ ★ provider_id 是 0 ⇒ **放行**（COALESCE 兜底，不是非法值）', () => {
    expect(() => unwrapModelIQNodeLatest([node({ provider_id: 0 })])).not.toThrow()
  })

  it('★ ★★ min_score 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelIQNodeLatest([node({ min_score: 'x' as unknown as number })])).toThrow(
      /\[0\] 的 min_score 不是数字/,
    )
  })

  it('★ avg_score 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelIQNodeLatest([node({ avg_score: null as unknown as number })])).toThrow(
      /\[0\] 的 avg_score 不是数字/,
    )
  })

  it('★ sample_count 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelIQNodeLatest([node({ sample_count: '12' as unknown as number })])).toThrow(
      /\[0\] 的 sample_count 不是数字/,
    )
  })

  it('★ ★ credential_label 是空串 ⇒ **放行**（COALESCE 兜底）', () => {
    expect(() => unwrapModelIQNodeLatest([node({ credential_label: '' })])).not.toThrow()
  })

  it('★ ★ canonical_name 是空串 ⇒ **放行**（LEFT JOIN 未命中）', () => {
    expect(() => unwrapModelIQNodeLatest([node({ canonical_name: '' })])).not.toThrow()
  })

  it('★ ★★★ canonical_name 不是字符串 ⇒ 抛（专打「把它移出字符串检查」那条变异）', () => {
    expect(() => unwrapModelIQNodeLatest([node({ canonical_name: 1 as unknown as string })])).toThrow(
      /\[0\] 的 canonical_name 不是字符串/,
    )
  })

  it('★ provider_name 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapModelIQNodeLatest([node({ provider_name: 3 as unknown as string })])).toThrow(
      /\[0\] 的 provider_name 不是字符串/,
    )
  })

  it('★ grade 不是字符串 ⇒ 抛（它是 COALESCE 的空串兜底，不是数字）', () => {
    expect(() => unwrapModelIQNodeLatest([node({ grade: 90 as unknown as string })])).toThrow(
      /\[0\] 的 grade 不是字符串/,
    )
  })

  it('★ ★ tested_at 是字符串 ⇒ 放行', () => {
    expect(() => unwrapModelIQNodeLatest([node({ tested_at: '2026-10-07T12:00:00Z' })])).not.toThrow()
  })

  it('★ ★★ tested_at 是裸 null ⇒ 放行（*string 无 omitempty）', () => {
    expect(() => unwrapModelIQNodeLatest([node({ tested_at: null })])).not.toThrow()
  })

  it('★ ★★ tested_at 是数字 ⇒ 抛（不是字符串也不是 null）', () => {
    expect(() => unwrapModelIQNodeLatest([node({ tested_at: 123 as unknown as string | null })])).toThrow(
      /\[0\] 的 tested_at 不是字符串也不是 null/,
    )
  })

  it('★ ★★★★ tested_at 是空串 ⇒ IsNull 为 false、IsString 为 true（真值判断会两头都错）', () => {
    // ★ 后端 `to_char` 不会返回空串，但手写夹具能造 ⇒ 这正是
    //   「`=== null` vs `!!x`」与「`typeof` vs `!!x`」唯一能区分的那一格。
    const r = node({ tested_at: '' })
    expect(modelIQNodeTestedAtIsNull(r)).toBe(false)
    expect(modelIQNodeTestedAtIsString(r)).toBe(true)
  })

  it('★ ★ 满配行（13 键齐全）⇒ 放行', () => {
    expect(() => unwrapModelIQNodeLatest([node()])).not.toThrow()
  })

  it('★ ★★★ provider_id=0 的行两种判据互斥（一个真、一个假）', () => {
    const r = node({ provider_id: 0 })
    expect(modelIQNodeHasNoProvider(r)).toBe(true)
    expect(modelIQNodeHasProvider(r)).toBe(false)
  })

  it('★ ★ provider_id>0 ⇒ HasProvider 为 true（反向）', () => {
    expect(modelIQNodeHasProvider(node({ provider_id: 3 }))).toBe(true)
    expect(modelIQNodeHasNoProvider(node({ provider_id: 3 }))).toBe(false)
  })

  it('★ tested_at 是 null ⇒ IsNull 为 true', () => {
    expect(modelIQNodeTestedAtIsNull(node({ tested_at: null }))).toBe(true)
    expect(modelIQNodeTestedAtIsString(node({ tested_at: null }))).toBe(false)
  })

  it('★ ★ tested_at 是字符串 ⇒ IsString 为 true（反向）', () => {
    expect(modelIQNodeTestedAtIsString(node())).toBe(true)
    expect(modelIQNodeTestedAtIsNull(node())).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (4)(8)(9) history 字段校验与 status 反推
// ══════════════════════════════════════════════════════════════════════════

describe('(4)(8)(9) history 字段校验', () => {
  it('★ 缺一个键 ⇒ 抛并点名', () => {
    const g = del(point() as unknown as Record<string, unknown>, 'probe_kind')
    expect(() => unwrapModelIQHistory([g])).toThrow(/智商历史\[0\] 缺 1 个键（probe_kind）/)
  })

  it('★ 缺两个键 ⇒ 抛并报数量', () => {
    let g = point() as unknown as Record<string, unknown>
    g = del(g, 'accuracy')
    g = del(g, 'status')
    expect(() => unwrapModelIQHistory([g])).toThrow(/智商历史\[0\] 缺 2 个键（accuracy, status）/)
  })

  it('★ accuracy 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelIQHistory([point({ accuracy: 'x' as unknown as number })])).toThrow(
      /\[0\] 的 accuracy 不是数字/,
    )
  })

  it('★ ★★ stability 是 0 ⇒ **放行**（COALESCE 的 NULL 也变成 0）', () => {
    expect(() => unwrapModelIQHistory([point({ stability: 0 })])).not.toThrow()
  })

  it('★ ★★★ stability 不是数字 ⇒ 抛（专打「把它移出数字检查」那条变异）', () => {
    expect(() => unwrapModelIQHistory([point({ stability: 'x' as unknown as number })])).toThrow(
      /\[0\] 的 stability 不是数字/,
    )
  })

  it('★ latency_p95 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelIQHistory([point({ latency_p95: 'x' as unknown as number })])).toThrow(
      /\[0\] 的 latency_p95 不是数字/,
    )
  })

  it('★ tested_at 不是字符串 ⇒ 抛（time.Time 恒发，不是可空键）', () => {
    expect(() => unwrapModelIQHistory([point({ tested_at: null as unknown as string })])).toThrow(
      /\[0\] 的 tested_at 不是字符串/,
    )
  })

  it('★ status 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapModelIQHistory([point({ status: 0 as unknown as string })])).toThrow(
      /\[0\] 的 status 不是字符串/,
    )
  })

  it('★ probe_kind 不是字符串 ⇒ 抛', () => {
    expect(() => unwrapModelIQHistory([point({ probe_kind: 1 as unknown as string })])).toThrow(
      /\[0\] 的 probe_kind 不是字符串/,
    )
  })

  it('★ ★ 满配行（9 键齐全）⇒ 放行', () => {
    expect(() => unwrapModelIQHistory([point()])).not.toThrow()
  })
})

describe('(4) status 从 accuracy/stability 反推', () => {
  it('★ ★ accuracy>0 且 stability=100 ⇒ 派生 success', () => {
    expect(modelIQDeriveStatus(92, 100)).toBe('success')
  })

  it('★ ★ accuracy>0 且 stability=99 ⇒ 派生 partial', () => {
    expect(modelIQDeriveStatus(92, 99)).toBe('partial')
  })

  it('★ ★★ accuracy<=0 且 stability<=0 ⇒ 派生 failed（覆盖 partial）', () => {
    // ★ 这条钉住「第三条判定在最后且覆盖前两条」：stability=0<100 本会落 partial，
    //   但 accuracy<=0 && stability<=0 让它变 failed。
    expect(modelIQDeriveStatus(0, 0)).toBe('failed')
  })

  it('★ ★★ stability 恰好 100 ⇒ **不**是 partial（边界：`stability < 100` 才 partial）', () => {
    expect(modelIQDeriveStatus(50, 100)).toBe('success')
  })

  it('★ accuracy=0 但 stability=50 ⇒ 派生 partial（failed 条件不成立）', () => {
    expect(modelIQDeriveStatus(0, 50)).toBe('partial')
  })

  it('★ accuracy=50 但 stability=0 ⇒ 派生 partial（failed 条件要两个都 <=0）', () => {
    expect(modelIQDeriveStatus(50, 0)).toBe('partial')
  })

  it('★ ★★★ status 与 accuracy/stability 自洽 ⇒ true', () => {
    expect(modelIQHistoryStatusMatchesMetrics(point({ accuracy: 92, stability: 100, status: 'success' }))).toBe(true)
  })

  it('★ ★★★ 自洽判据在 partial 行上成立', () => {
    expect(modelIQHistoryStatusMatchesMetrics(point({ accuracy: 92, stability: 99, status: 'partial' }))).toBe(true)
  })

  it('★ ★★★ 自洽判据在 failed 行上成立', () => {
    expect(modelIQHistoryStatusMatchesMetrics(point({ accuracy: 0, stability: 0, status: 'failed' }))).toBe(true)
  })

  it('★ ★★ 自洽判据不一致 ⇒ false', () => {
    expect(modelIQHistoryStatusMatchesMetrics(point({ accuracy: 92, stability: 100, status: 'failed' }))).toBe(false)
  })

  it('★ ★★ 自洽判据：partial 被写成 success ⇒ false', () => {
    expect(modelIQHistoryStatusMatchesMetrics(point({ accuracy: 92, stability: 99, status: 'success' }))).toBe(false)
  })

  it('★ ★★ 自洽判据：failed 被写成 partial ⇒ false（覆盖关系钉住）', () => {
    expect(modelIQHistoryStatusMatchesMetrics(point({ accuracy: 0, stability: 0, status: 'partial' }))).toBe(false)
  })

  it('★ status 是三值之一 ⇒ StatusIsKnown 为 true', () => {
    expect(modelIQStatusIsKnown(point({ status: 'success' }))).toBe(true)
    expect(modelIQStatusIsKnown(point({ status: 'partial' }))).toBe(true)
    expect(modelIQStatusIsKnown(point({ status: 'failed' }))).toBe(true)
  })

  it('★ ★ 第四个 status 值 ⇒ StatusIsKnown 为 false', () => {
    expect(modelIQStatusIsKnown(point({ status: 'error' }))).toBe(false)
  })

  it('★ ★★★★ 长度 >10 但不含 T ⇒ 不是 RFC3339（专打「只看长度」那条变异）', () => {
    expect(modelIQHistoryTestedAtIsRfc3339(point({ tested_at: '2026-10-07 00:00:00' }))).toBe(false)
  })

  it('★ ★ stability 是 0 ⇒ StabilityIsZero 为 true', () => {
    expect(modelIQHistoryStabilityIsZero(point({ stability: 0 }))).toBe(true)
  })

  it('★ ★ stability 非 0 ⇒ StabilityIsZero 为 false（反向）', () => {
    expect(modelIQHistoryStabilityIsZero(point({ stability: 50 }))).toBe(false)
  })

  it('★ tested_at 是 RFC3339（含 T、长度 >10）⇒ true', () => {
    expect(modelIQHistoryTestedAtIsRfc3339(point())).toBe(true)
  })

  it('★ ★★★ 10 位纯日期 ⇒ 不是 RFC3339（后端是 time.Time，不会这样）', () => {
    expect(modelIQHistoryTestedAtIsRfc3339(point({ tested_at: '2026-10-07' }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (2)(3)(10) catalog 字段校验与不变式
// ══════════════════════════════════════════════════════════════════════════

describe('(2)(3) catalog 字段校验', () => {
  it('★ 缺一个键 ⇒ 抛并点名（10 键恒在）', () => {
    const g = del(catalog() as unknown as Record<string, unknown>, 'standard_iq_source')
    expect(() => unwrapModelIQCatalog([g])).toThrow(/智商目录\[0\] 缺 1 个键（standard_iq_source）/)
  })

  it('★ ★ 缺两个键 ⇒ 抛并报数量与顺序', () => {
    let g = catalog() as unknown as Record<string, unknown>
    g = del(g, 'family')
    g = del(g, 'min_node_iq')
    expect(() => unwrapModelIQCatalog([g])).toThrow(/智商目录\[0\] 缺 2 个键（family, min_node_iq）/)
  })

  it('★ canonical_id 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelIQCatalog([catalog({ canonical_id: 'x' as unknown as number })])).toThrow(
      /\[0\] 的 canonical_id 不是数字/,
    )
  })

  it('★ node_count 不是数字 ⇒ 抛', () => {
    expect(() => unwrapModelIQCatalog([catalog({ node_count: null as unknown as number })])).toThrow(
      /\[0\] 的 node_count 不是数字/,
    )
  })

  it('★ family 不是字符串 ⇒ 抛（COALESCE 的空串兜底，类型仍是 string）', () => {
    expect(() => unwrapModelIQCatalog([catalog({ family: 1 as unknown as string })])).toThrow(
      /\[0\] 的 family 不是字符串/,
    )
  })

  it('★ ★ standard_iq 是数字 ⇒ 放行', () => {
    expect(() => unwrapModelIQCatalog([catalog({ standard_iq: 90 })])).not.toThrow()
  })

  it('★ ★★ standard_iq 是裸 null ⇒ **放行**（*float64 无 omitempty）', () => {
    expect(() => unwrapModelIQCatalog([catalog({ standard_iq: null })])).not.toThrow()
  })

  it('★ ★★ node_avg_iq 是裸 null ⇒ **放行**', () => {
    expect(() =>
      unwrapModelIQCatalog([catalog({ node_avg_iq: null, node_count: 0, max_node_iq: null, min_node_iq: null })]),
    ).not.toThrow()
  })

  it('★ ★★ standard_iq 是字符串 ⇒ 抛（不是数字也不是 null）', () => {
    expect(() => unwrapModelIQCatalog([catalog({ standard_iq: '90' as unknown as number | null })])).toThrow(
      /\[0\] 的 standard_iq 不是数字也不是 null/,
    )
  })

  it('★ ★ max_node_iq 是字符串 ⇒ 抛（另一个可空键）', () => {
    expect(() => unwrapModelIQCatalog([catalog({ max_node_iq: 'x' as unknown as number | null })])).toThrow(
      /\[0\] 的 max_node_iq 不是数字也不是 null/,
    )
  })

  it('★ ★ min_node_iq 是字符串 ⇒ 抛（第三个可空键）', () => {
    expect(() => unwrapModelIQCatalog([catalog({ min_node_iq: {} as unknown as number | null })])).toThrow(
      /\[0\] 的 min_node_iq 不是数字也不是 null/,
    )
  })

  it('★ ★ 满配行（10 键齐全）⇒ 放行', () => {
    expect(() => unwrapModelIQCatalog([catalog()])).not.toThrow()
  })
})

describe('(3) node_count 与三个聚合互为充要', () => {
  it('★ ★★★ node_count=0 且三个聚合全 null ⇒ 成立', () => {
    const r = catalog({ node_count: 0, node_avg_iq: null, max_node_iq: null, min_node_iq: null })
    expect(modelIQCatalogAggregatesMatchCount(r)).toBe(true)
  })

  it('★ ★★ node_count>0 且三个聚合全非 null ⇒ 成立', () => {
    expect(modelIQCatalogAggregatesMatchCount(catalog({ node_count: 12 }))).toBe(true)
  })

  it('★ ★★ node_count=0 但聚合非 null ⇒ **不成立**（count(*)=0 时聚合必 NULL）', () => {
    const r = catalog({ node_count: 0, node_avg_iq: 88.5, max_node_iq: 92, min_node_iq: 85 })
    expect(modelIQCatalogAggregatesMatchCount(r)).toBe(false)
  })

  it('★ ★★★ node_count>0 但 node_avg_iq 是 null ⇒ **不成立**', () => {
    const r = catalog({ node_count: 12, node_avg_iq: null, max_node_iq: 92, min_node_iq: 85 })
    expect(modelIQCatalogAggregatesMatchCount(r)).toBe(false)
  })

  it('★ node_count>0 但 max_node_iq 是 null ⇒ 不成立（另一个聚合）', () => {
    const r = catalog({ node_count: 12, node_avg_iq: 88.5, max_node_iq: null, min_node_iq: 85 })
    expect(modelIQCatalogAggregatesMatchCount(r)).toBe(false)
  })

  it('★ ★★★★ node_count=0 但**只有** min_node_iq 非 null ⇒ 不成立', () => {
    // ★ 专格：其余聚合全为「不触发值（null）」、只有被测项触发。
    const r = catalog({ node_count: 0, node_avg_iq: null, max_node_iq: null, min_node_iq: 85 })
    expect(modelIQCatalogAggregatesMatchCount(r)).toBe(false)
  })

  it('★ node_count>0 但 min_node_iq 是 null ⇒ 不成立（第三个聚合）', () => {
    const r = catalog({ node_count: 12, node_avg_iq: 88.5, max_node_iq: 92, min_node_iq: null })
    expect(modelIQCatalogAggregatesMatchCount(r)).toBe(false)
  })

  it('★ ★★★★ node_count=0 但**只有** node_avg_iq 非 null ⇒ 不成立', () => {
    const r = catalog({ node_count: 0, node_avg_iq: 88.5, max_node_iq: null, min_node_iq: null })
    expect(modelIQCatalogAggregatesMatchCount(r)).toBe(false)
  })

  it('★ ★★ node_count=0 但 max_node_iq 非 null ⇒ 不成立（只有它非 null）', () => {
    const r = catalog({ node_count: 0, node_avg_iq: null, max_node_iq: 92, min_node_iq: null })
    expect(modelIQCatalogAggregatesMatchCount(r)).toBe(false)
  })
})

describe('(3) standard_iq 与 node_count 至少一个成立', () => {
  it('★ ★★ standard_iq 非 null ⇒ 成立（哪怕 node_count=0）', () => {
    const r = catalog({ standard_iq: 90, node_count: 0, node_avg_iq: null, max_node_iq: null, min_node_iq: null })
    expect(modelIQCatalogHasStandardOrNodes(r)).toBe(true)
  })

  it('★ ★ standard_iq 是 null 但 node_count>0 ⇒ 成立', () => {
    expect(modelIQCatalogHasStandardOrNodes(catalog({ standard_iq: null, node_count: 3 }))).toBe(true)
  })

  it('★ ★★★ standard_iq 是 null 且 node_count=0 ⇒ **不成立**（WHERE 保证不会出现）', () => {
    const r = catalog({ standard_iq: null, node_count: 0, node_avg_iq: null, max_node_iq: null, min_node_iq: null })
    expect(modelIQCatalogHasStandardOrNodes(r)).toBe(false)
  })

  it('★ node_avg_iq 是 null ⇒ NodeAvgIsNull 为 true', () => {
    expect(modelIQCatalogNodeAvgIsNull(catalog({ node_avg_iq: null, node_count: 0 }))).toBe(true)
  })

  it('★ ★ node_avg_iq 非 null ⇒ NodeAvgIsNull 为 false（反向）', () => {
    expect(modelIQCatalogNodeAvgIsNull(catalog())).toBe(false)
  })

  it('★ ★★★ node_avg_iq 是 0 ⇒ NodeAvgIsNull 为 false（0 不是 null，真值判断会误判）', () => {
    expect(modelIQCatalogNodeAvgIsNull(catalog({ node_avg_iq: 0, node_count: 3 }))).toBe(false)
  })

  it('★ standard_iq 是 null ⇒ StandardIqIsNull 为 true', () => {
    expect(modelIQCatalogStandardIqIsNull(catalog({ standard_iq: null }))).toBe(true)
  })

  it('★ ★ standard_iq 是 0 ⇒ StandardIqIsNull 为 false（0 不是 null）', () => {
    expect(modelIQCatalogStandardIqIsNull(catalog({ standard_iq: 0 }))).toBe(false)
  })
})

describe('(10) hidden 永远不出现在 catalog', () => {
  it('★ ★★ status=hidden ⇒ WouldBeHidden 为 true', () => {
    expect(modelIQCatalogWouldBeHidden('hidden')).toBe(true)
  })

  it('★ ★ status 其它值 ⇒ WouldBeHidden 为 false（反向）', () => {
    expect(modelIQCatalogWouldBeHidden('active')).toBe(false)
    expect(modelIQCatalogWouldBeHidden('disabled')).toBe(false)
    expect(modelIQCatalogWouldBeHidden('deprecated')).toBe(false)
  })

  it('★ ★★ 三个放行状态 ⇒ IncludesStatus 为 true', () => {
    expect(modelIQCatalogIncludesStatus('active')).toBe(true)
    expect(modelIQCatalogIncludesStatus('disabled')).toBe(true)
    expect(modelIQCatalogIncludesStatus('deprecated')).toBe(true)
  })

  it('★ ★★★ hidden ⇒ IncludesStatus 为 false（端点过滤掉它）', () => {
    expect(modelIQCatalogIncludesStatus('hidden')).toBe(false)
  })

  it('★ ★ 端点放行集是表 CHECK 域的真子集（少了 hidden）', () => {
    const filter = MODEL_IQ_CATALOG_STATUS_FILTER as readonly string[]
    const domain = MODEL_IQ_CANONICAL_STATUS_DOMAIN as readonly string[]
    expect(domain.filter((s) => !filter.includes(s))).toEqual(['hidden'])
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (7) limit 静默回落
// ══════════════════════════════════════════════════════════════════════════

describe('(7) limit 静默回落', () => {
  it('★ ★★ limit=50 会被采纳', () => {
    expect(modelIQHistoryLimitIsAccepted(50)).toBe(true)
  })

  it('★ ★ limit=1 会被采纳（下界是 `n > 0`）', () => {
    expect(modelIQHistoryLimitIsAccepted(1)).toBe(true)
  })

  it('★ ★★ limit=500 会被采纳（边界：`n <= 500`）', () => {
    expect(modelIQHistoryLimitIsAccepted(500)).toBe(true)
  })

  it('★ ★★ limit=501 **不**被采纳（超上界，静默回落）', () => {
    expect(modelIQHistoryLimitIsAccepted(501)).toBe(false)
  })

  it('★ ★ limit=0 **不**被采纳（`n > 0` 是严格不等号）', () => {
    expect(modelIQHistoryLimitIsAccepted(0)).toBe(false)
  })

  it('★ limit=-1 不被采纳', () => {
    expect(modelIQHistoryLimitIsAccepted(-1)).toBe(false)
  })

  it('★ ★★ 非法 limit 的实际生效值恒为缺省 50', () => {
    expect(modelIQHistoryEffectiveLimit(501)).toBe(50)
    expect(modelIQHistoryEffectiveLimit(0)).toBe(50)
    expect(modelIQHistoryEffectiveLimit(-1)).toBe(50)
  })

  it('★ ★ 合法 limit 的生效值就是它自己', () => {
    expect(modelIQHistoryEffectiveLimit(20)).toBe(20)
    expect(modelIQHistoryEffectiveLimit(500)).toBe(500)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch URL 拼装
// ══════════════════════════════════════════════════════════════════════════

describe('fetch URL 拼装', () => {
  it('★ node-latest 无参数 ⇒ 不带问号', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQNodeLatest()
    expect(lastUrl()).toContain('/api/admin/model-iq/node-latest')
    expect(lastUrl()).not.toContain('?')
  })

  it('★ ★ node-latest 带 provider_id 与 canonical_id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQNodeLatest({ providerId: 3, canonicalId: 11 })
    expect(lastUrl()).toContain('provider_id=3')
    expect(lastUrl()).toContain('canonical_id=11')
  })

  it('★ ★ 只给 provider_id 时不发 canonical_id', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQNodeLatest({ providerId: 3 })
    expect(lastUrl()).toContain('provider_id=3')
    expect(lastUrl()).not.toContain('canonical_id')
  })

  it('★ ★★ providerId=0 也会发出（后端 `<= 0` 会 400，客户端不预判）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQNodeLatest({ providerId: 0 })
    expect(lastUrl()).toContain('provider_id=0')
  })

  it('★ catalog 无参数 ⇒ 路径固定、不带问号', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQCatalog()
    expect(lastUrl()).toContain('/api/admin/model-iq/catalog')
    expect(lastUrl()).not.toContain('?')
  })

  it('★ ★ history 两个必填参数都发', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQHistory({ credentialId: 7, rawModelName: 'gpt-4o' })
    expect(lastUrl()).toContain('credential_id=7')
    expect(lastUrl()).toContain('raw_model_name=gpt-4o')
  })

  it('★ ★ history 不给 limit 时不发（缺省由后端定 50）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQHistory({ credentialId: 7, rawModelName: 'gpt-4o' })
    expect(lastUrl()).not.toContain('limit=')
  })

  it('★ ★ history 给 limit 时透传', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQHistory({ credentialId: 7, rawModelName: 'gpt-4o', limit: 20 })
    expect(lastUrl()).toContain('limit=20')
  })

  it('★ ★★ rawModelName 含空格时按 URL 编码发出（后端 lower() 匹配大小写不敏感）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQHistory({ credentialId: 7, rawModelName: 'gpt 4o' })
    expect(lastUrl()).toContain('raw_model_name=gpt+4o')
  })

  it('★ ★ rawModelName 大小写原样发出（**客户端不做小写折叠**）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQHistory({ credentialId: 7, rawModelName: 'GPT-4o' })
    expect(lastUrl()).toContain('raw_model_name=GPT-4o')
    expect(lastUrl()).not.toContain('raw_model_name=gpt-4o')
  })

  it('★ ★★ history 的 limit=0 也透传（静默回落由后端做，不在客户端拦）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await fetchModelIQHistory({ credentialId: 7, rawModelName: 'gpt-4o', limit: 0 })
    expect(lastUrl()).toContain('limit=0')
  })
})

describe('fetch 端到端', () => {
  it('★ node-latest 满配响应被解包器放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([node()]))
    const r = await fetchModelIQNodeLatest()
    expect(r).toHaveLength(1)
  })

  it('★ ★ node-latest 空数组响应被放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    expect(await fetchModelIQNodeLatest()).toEqual([])
  })

  it('★ history 满配响应被解包器放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([point()]))
    expect(await fetchModelIQHistory({ credentialId: 7, rawModelName: 'gpt-4o' })).toHaveLength(1)
  })

  it('★ catalog 满配响应被解包器放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([catalog()]))
    expect(await fetchModelIQCatalog()).toHaveLength(1)
  })

  it('★ ★ catalog 里 standard_iq 为 null 的行被放行', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse([catalog({ standard_iq: null, node_avg_iq: 88.5, node_count: 3 })]),
    )
    const r = await fetchModelIQCatalog()
    expect(modelIQCatalogStandardIqIsNull(r[0]!)).toBe(true)
  })

  it('★ ★★ history 响应里的 status 自洽（写入侧同源算出）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([point({ accuracy: 92, stability: 99, status: 'partial' })]))
    const r = await fetchModelIQHistory({ credentialId: 7, rawModelName: 'gpt-4o' })
    expect(modelIQHistoryStatusMatchesMetrics(r[0]!)).toBe(true)
  })

  it('★ ★★ 顶层是对象（后端换形状）⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ data: [] }))
    await expect(fetchModelIQCatalog()).rejects.toThrow(/期望顶层裸数组/)
  })

  it('★ node-latest 缺键 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([del(node() as unknown as Record<string, unknown>, 'grade')]))
    await expect(fetchModelIQNodeLatest()).rejects.toThrow(/缺 1 个键（grade）/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值（逐条对齐后端字面量）
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ MODEL_IQ_DB_NOT_CONFIGURED === model_iq.go:48 的字面量', () => {
    expect(MODEL_IQ_DB_NOT_CONFIGURED).toBe('database not configured')
  })

  it('★ ★ MODEL_IQ_DB_NOT_CONFIGURED **没有** available（批 84 那条有）', () => {
    expect(MODEL_IQ_DB_NOT_CONFIGURED).not.toContain('available')
  })

  it('★ MODEL_IQ_STATUSES 三值与 dbstorage.go:80-87 一致', () => {
    expect([...MODEL_IQ_STATUSES]).toEqual(['success', 'partial', 'failed'])
  })

  it('★ MODEL_IQ_PROBE_KINDS 三值与 benchmark.go:27-29 一致', () => {
    expect([...MODEL_IQ_PROBE_KINDS]).toEqual(['gateway', 'direct', 'mock'])
  })

  it('★ MODEL_IQ_PROBE_DEFAULT === dbstorage.go:121 的缺省', () => {
    expect(MODEL_IQ_PROBE_DEFAULT).toBe('direct')
  })

  it('★ MODEL_IQ_TRIGGER_DEFAULT === dbstorage.go:95 的缺省', () => {
    expect(MODEL_IQ_TRIGGER_DEFAULT).toBe('scheduled')
  })

  it('★ MODEL_IQ_CATALOG_STATUS_FILTER 三值与 SQL 的 IN 列表一致', () => {
    expect([...MODEL_IQ_CATALOG_STATUS_FILTER]).toEqual(['active', 'disabled', 'deprecated'])
  })

  it('★ MODEL_IQ_CANONICAL_STATUS_DOMAIN 四值与建表 CHECK 一致', () => {
    expect([...MODEL_IQ_CANONICAL_STATUS_DOMAIN]).toEqual(['active', 'disabled', 'deprecated', 'hidden'])
  })

  it('★ MODEL_IQ_HISTORY_DEFAULT_LIMIT === model_iq.go:155 的 50', () => {
    expect(MODEL_IQ_HISTORY_DEFAULT_LIMIT).toBe(50)
  })

  it('★ MODEL_IQ_HISTORY_MAX_LIMIT === model_iq.go:157 的 500', () => {
    expect(MODEL_IQ_HISTORY_MAX_LIMIT).toBe(500)
  })

  it('★ MODEL_IQ_NODE_LATEST_KEYS 恰是 13 个恒在键', () => {
    expect(MODEL_IQ_NODE_LATEST_KEYS.length).toBe(13)
  })

  it('★ MODEL_IQ_HISTORY_KEYS 恰是 9 个恒在键', () => {
    expect(MODEL_IQ_HISTORY_KEYS.length).toBe(9)
  })

  it('★ MODEL_IQ_CATALOG_KEYS 恰是 10 个恒在键', () => {
    expect(MODEL_IQ_CATALOG_KEYS.length).toBe(10)
  })

  it('★ ★ MODEL_IQ_CATALOG_NULLABLE_KEYS 四个可空键都在 catalog 恒在键内', () => {
    const all = MODEL_IQ_CATALOG_KEYS as readonly string[]
    for (const k of MODEL_IQ_CATALOG_NULLABLE_KEYS) expect(all).toContain(k)
  })

  it('★ ★ MODEL_IQ_CATALOG_NULLABLE_KEYS 里**不含** node_count（它是 int 不是指针）', () => {
    expect(MODEL_IQ_CATALOG_NULLABLE_KEYS as readonly string[]).not.toContain('node_count')
  })

  it('★ ★ MODEL_IQ_HISTORY_KEYS 里**不含** benchmark_type（后端不返回该列）', () => {
    expect(MODEL_IQ_HISTORY_KEYS as readonly string[]).not.toContain('benchmark_type')
  })

  it('★ ★ MODEL_IQ_NODE_LATEST_KEYS 里**不含** filter（后端没这概念）', () => {
    expect(MODEL_IQ_NODE_LATEST_KEYS as readonly string[]).not.toContain('filter')
  })
})
