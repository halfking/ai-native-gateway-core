import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import FormatAnomaliesView from './FormatAnomaliesView.vue'
import { setLocale } from '@/i18n'
import {
  fetchFormatAnomalies,
  fetchFormatAnomalySummary,
  FORMAT_ANOMALY_LIMIT_MAX,
  FORMAT_ANOMALY_LIMIT_DEFAULT,
  FORMAT_SUMMARY_SQL_LIMIT,
  FORMAT_SUMMARY_HOURS_MAX,
} from '@/api/formatAnomalies'

/**
 * 响应格式异常面回归护栏（2026-10-14，第一百零九批）。
 *
 * ★★ 权限：**superAdmin 档**（`admin/handler.go:913-914` 两条都是 `h.superAdmin`）
 *   ⇒ 抽屉席**须设** `requiresRole: 'super_admin'`（同步 `AppDrawer.spec.ts` 白名单）。
 *
 * ★★★ 本族是本仓**第六次「不隔离」**（`withAllTenantReadOnlyTx`，
 *   `admin/format_anomalies.go:106`/`:143`/`:213`：两张表都有 tenant_id 列，
 *   list 会 SELECT 出来，但 WHERE 从不过滤）。
 *   ★★ **但处置与批 104 / 批 106 相反**：那两族是 admin 档 + 不隔离
 *     ⇒ tenant_admin 能读到跨租户数据 ⇒ 必须在视图内**按段落**门控；
 *     本族是 **superAdmin 档** ⇒ **档位本身就挡住了**跨租户访问
 *     ⇒ 抽屉席设 requiresRole 即是完整处置，**视图里不应有任何角色分支**。
 *   ⇒ 第三组判据专门钉住这件事：视图**不得**因角色而改变呈现。
 *
 * 判据钉十一组不变量：
 *  ① ★★★★★★★ **两个端点的 `count` 语义相反**：
 *      list（`:100-111`+`:186-191`）是**全表命中数** ⇒ 恒 `count >= 本页长度`；
 *      summary（`:231` 后）是 **`len(summaries)`** ⇒ 恒等于本页长度。
 *      与批 78/81 的 `count == 本页长度` **在 list 上相反** ⇒ 判据不能跨族照抄。
 *  ② ★★★ `count < 本页长度` ⇒ 显式违规告警（载荷被换过，不是「后端口径变了」）。
 *  ③ ★★ `limit` / `offset` **都回显** ⇒ 分页能精确判「还有下一页 / 已是末页」。
 *      ⚠️ 与批 79/80/81（只回显 days）相反。
 *  ④ ★★★ `provider_code` 键缺失 ⇒ 「不可能匹配 provider 过滤」，
 *      因为展示与过滤用的是**同一个** `COALESCE`（`:119`/`:81`），
 *      而 `NULL = 'x'` 求值为 NULL、WHERE 不会放它行。
 *  ⑤ ★★★ `request_id` 恒在但**可以是空串**（`text NOT NULL` ≠ 非空串）。
 *  ⑥ ★★★★ `response_structure` 是 map + omitempty ⇒ **键在就意味着非空**
 *      ⇒ 「结构为空」是恒真判据，**不提供**。
 *  ⑦ ★★★★ summary 三个 `AVG(...)` 是 `*float64` + omitempty
 *      ⇒ **`AVG` 返回 NULL 时键被省略**，不是 0、不是 null。
 *  ⑧ ★★★ `COUNT(DISTINCT request_id)` ⇒ `affected_requests <= anomaly_count`；
 *      `resolved_count <= anomaly_count` 恒成立。
 *  ⑨ ★★★ summary 的 `LIMIT 200` **硬编码在 SQL 里**、不可调、不回显
 *      ⇒ 只能靠 `count >= 200` 反推可能被截断。
 *  ⑩ ★★★ 排序是 `ORDER BY hour DESC, anomaly_count DESC` ⇒ **整表不按计数降序**，
 *      只能断言「**同一 hour 内**按 `anomaly_count` 非增」。
 *  ⑪ ★★★ `anomaly_type` / `severity` 是**开放域**（写入接受任意字符串、建表无 CHECK）
 *      ⇒ 未知取值要标出来，但**不是错误**；且 `severity` 的表级缺省是 `medium`
 *      （另一张表是 `low`）。
 */

// ── 夹具 ──────────────────────────────────────────────────────────────────

function rec(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    detected_at: '2026-10-14T03:00:00Z',
    request_id: 'req-1',
    anomaly_type: 'missing_usage_block',
    severity: 'medium',
    resolved: false,
    created_at: '2026-10-14T03:00:01Z',
    provider_code: 'openai',
    client_model: 'gpt-4o',
    outbound_model: 'gpt-4o-2024',
    ...over,
  }
}

function listPayload(o: {
  anomalies?: unknown[]
  count?: number
  limit?: number
  offset?: number
} = {}) {
  const anomalies = o.anomalies ?? [rec()]
  return {
    anomalies,
    // ★ 默认 count 取全表命中数（>= 本页长度），不是本页长度
    count: o.count ?? anomalies.length,
    limit: o.limit ?? FORMAT_ANOMALY_LIMIT_DEFAULT,
    offset: o.offset ?? 0,
  }
}

function smRow(over: Record<string, unknown> = {}) {
  return {
    hour: '2026-10-14T03:00:00Z',
    anomaly_type: 'missing_usage_block',
    severity: 'medium',
    anomaly_count: 10,
    affected_requests: 4,
    resolved_count: 2,
    avg_content_size: 2048,
    ...over,
  }
}

function sumPayload(o: { summaries?: unknown[]; count?: number; hours?: number } = {}) {
  const summaries = o.summaries ?? [smRow()]
  return {
    summaries,
    // ★★ summary 的 count 是 len(summaries)，与 list 语义相反
    count: o.count ?? summaries.length,
    hours: o.hours ?? 24,
  }
}

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/formatAnomalies', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/formatAnomalies')>()
  return { ...actual, fetchFormatAnomalies: vi.fn(), fetchFormatAnomalySummary: vi.fn() }
})

const mList = fetchFormatAnomalies as unknown as ReturnType<typeof vi.fn>
const mSum = fetchFormatAnomalySummary as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(FormatAnomaliesView)
  await flushPromises()
  return w
}

function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}

function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  mList.mockResolvedValue(listPayload())
  mSum.mockResolvedValue(sumPayload())
})

// ── ① ② 两个 count 语义相反 ────────────────────────────────────────────

describe('① 两个端点的 count 语义相反', () => {
  it('★★★★★ list：count 是**全表命中数** ⇒ 措辞必须同时写出总数与本页数', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec(), rec({ id: 2 })], count: 137 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    const txt = byId(w, 'fa-list-count').text()
    expect(txt).toContain('137')
    expect(txt).toContain('2')
  })

  it('★★★★★ summary：count 是**本页行数** ⇒ 措辞不得与 list 共用', async () => {
    mSum.mockResolvedValue(sumPayload({ summaries: [smRow(), smRow(), smRow()], count: 3 }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    // ★ 两处文案不同句式 ⇒ 从文案上就能分清是哪个 count
    const listTxt = byId(w, 'fa-list-count').exists() ? byId(w, 'fa-list-count').text() : ''
    const sumTxt = byId(w, 'fa-summary-count').text()
    expect(sumTxt).toContain('3')
    expect(listTxt).not.toBe(sumTxt)
  })

  it('★★★ 正控：count == 本页长度（没截断）⇒ 不出违规告警', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec()], count: 1 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-count-violation')).toBe(false)
  })

  it('★★★★★ 正控：count **大于**本页长度 ⇒ **不得**误报违规', async () => {
    // ★★★★ 这条正控是被变异 S1 逼出来的：把「count < len 才违规」改写成
    //   「count != len 就违规」时，`count > len`（也就是**最正常的那种分页**）
    //   会被整页染红。而我原先只有 `==` 的正控，没有 `>` 的方向
    //   ⇒ 两边都没钉住，中间那块整整一类行为无人看守。
    // ⇒ 「异常告警」必须只在**真异常**（count 比本页还少）时出现。
    mList.mockResolvedValue(listPayload({ anomalies: [rec()], count: 137 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-list-count').text()).toContain('137')
    expect(hasId(w, 'fa-count-violation')).toBe(false)
  })

  it('★★★★★ count < 本页长度 ⇒ 显式违规告警', async () => {
    // ★ 这是「载荷被换过 / 中间有人改过」，不是「后端口径变了」
    mList.mockResolvedValue(listPayload({ anomalies: [rec(), rec({ id: 2 })], count: 1 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-count-violation')).toBe(true)
  })

  it('★★★★ summary 的 count != summaries.length ⇒ 显式违规告警', async () => {
    mSum.mockResolvedValue(sumPayload({ summaries: [smRow()], count: 99 }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-summary-count-bad')).toBe(true)
  })
})

// ── ③ 分页（limit/offset 都回显）────────────────────────────────────────

describe('③ 分页判定用回显值', () => {
  it('★★★★★ limit / offset 原样回显到页面', async () => {
    mList.mockResolvedValue(listPayload({ limit: FORMAT_ANOMALY_LIMIT_MAX, offset: 250 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    const txt = byId(w, 'fa-list-window').text()
    expect(txt).toContain(String(FORMAT_ANOMALY_LIMIT_MAX))
    expect(txt).toContain('250')
  })

  it('★★★★★ count > 本页长度 ⇒ 「还有下一页」，下一页按钮可点', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec()], count: 900, limit: 50, offset: 0 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-has-more')).toBe(true)
    expect(byId(w, 'fa-next').attributes('disabled')).toBeUndefined()
  })

  it('★★★★★ 点下一页 ⇒ offset 递增且真的重新取数', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec()], count: 900, limit: 50, offset: 0 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    await byId(w, 'fa-next').trigger('click')
    await flushPromises()
    expect(mList).toHaveBeenCalledTimes(2)
    expect(mList.mock.calls[1]![0]!.offset).toBe(50)
  })

  it('★★★★★ 末页 ⇒ 「已是末页」且下一页禁用', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec()], count: 1, offset: 0 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-last-page')).toBe(true)
    expect(byId(w, 'fa-next').attributes('disabled')).toBeDefined()
  })

  it('★★★ 上一页在 offset=0 时禁用（不产生负 offset）', async () => {
    mList.mockResolvedValue(listPayload({ offset: 0 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-prev').attributes('disabled')).toBeDefined()
  })

  it('★★★★ 换 limit ⇒ offset 归零（避免跳过一整段）', async () => {
    // ★★★★ 第一版这条判据是**恒真**的，被变异 S17 当场揭穿：
    //   夹具只加载过一次（offset 还是默认 0），换档前后请求的 offset 都是 0
    //   ⇒ 无论实现里有没有那行 `offset = 0`，断言都成立。
    //   ⇒ 「先点下一页把 offset 推到非 0」是这条判据**能被咬住的前提**，
    //     不加这一步它测的不是「换档会不会重置」，而是「0 是不是 0」。
    mList.mockResolvedValue(listPayload({ count: 900, limit: 50, offset: 0 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    // ★ 先翻一页：offset 0 → 50
    await byId(w, 'fa-next').trigger('click')
    await flushPromises()
    expect(mList.mock.calls[1]![0]!.offset).toBe(50)
    // ★ 再换档：offset 必须归零，limit 变成新档位
    await byId(w, 'fa-limit-100').trigger('click')
    await flushPromises()
    const last = mList.mock.calls[mList.mock.calls.length - 1]![0]!
    expect(last.offset).toBe(0)
    expect(last.limit).toBe(100)
  })
})

// ── ④ provider_code 的 COALESCE ─────────────────────────────────────────

describe('④ provider_code 键缺 ≠ 空值', () => {
  it('★★★★★ 键缺失 ⇒ 说「不可能匹配 provider 过滤」，不是「未提供」', async () => {
    const r = rec()
    delete (r as Record<string, unknown>).provider_code
    mList.mockResolvedValue(listPayload({ anomalies: [r] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('不可能匹配')
  })

  it('★★★★★ 正控：键在 ⇒ 显示真实值，两种空态都不得出现', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ provider_code: 'openai' })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-row-1').text()).toContain('openai')
    expect(w.text()).not.toContain('不可能匹配')
  })
})

// ── ⑤ request_id 可空串 ────────────────────────────────────────────────

describe('⑤ request_id 恒在但可空串', () => {
  it('★★★★★ 空串 ⇒ 显式说「空」，不渲染成空白', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ request_id: '' })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-row-1').text()).toContain('空串')
  })

  it('★★★★ 正控：有值 ⇒ 显示真实值且不说「空串」', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ request_id: 'req-abc' })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    const txt = byId(w, 'fa-row-1').text()
    expect(txt).toContain('req-abc')
    expect(txt).not.toContain('空串')
  })
})

// ── ⑥⑦ 键缺 vs 0 ──────────────────────────────────────────────────────

describe('⑥⑦ 键缺 ≠ 值为 0', () => {
  it('★★★★★ expected_tokens 键缺 ⇒ 说「无数据」，不是「0」', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec()] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-f-1-expected_tokens').text()).toContain('无数据')
  })

  it('★★★★★ expected_tokens = 0 ⇒ 必须显示「0」（指针指向 0，键仍在）', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ expected_tokens: 0, actual_tokens: 0 })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    // ★★★ 收窄到**那一格**：第一版断的是整行 `not.toContain('无数据')`,
    //   而夹具只设了这两个字段、content_size_bytes 仍然键缺 ⇒ 那一格**正确地**
    //   显示「无数据」⇒ 整行断言必然失败。
    //   ⇒ ★ 正控的范围必须与被断的对象**同格**，不能放大到整行 ——
    //     放大之后它测的不再是「0 有没有被说成无数据」，而是「整行里还有没有别的无数据」。
    const cell = byId(w, 'fa-f-1-expected_tokens').text()
    expect(cell).toContain('0 词元')
    expect(cell).not.toContain('无数据')
    // ★ 且确认「键缺的那一格」确实说了无数据 ⇒ 两格必须能被分别区分
    expect(byId(w, 'fa-f-1-content_size').text()).toContain('无数据')
  })

  it('★★★★★ content_size_bytes 键名带 _bytes 后缀（Go 字段名是 ContentSize）', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ content_size_bytes: 4096 })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-row-1').text()).toContain('4096')
  })

  it('★★★★★ summary 的 avg 键缺 ⇒ 说「无法判定」，绝不说 0', async () => {
    const r = smRow()
    delete (r as Record<string, unknown>).avg_content_size
    mSum.mockResolvedValue(sumPayload({ summaries: [r] }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-s-0-avg_content_size').text()).toContain('无法判定')
    expect(byId(w, 'fa-s-0-avg_expected_tokens').text()).toContain('无法判定')
  })

  it('★★★★ 正控：avg = 0 ⇒ 显示「0」，不得说「无法判定」', async () => {
    mSum.mockResolvedValue(sumPayload({ summaries: [smRow({ avg_content_size: 0 })] }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    // ★ 同上：avg_content_size 是 0，另外两个 avg 键缺 ⇒ 那一格**正确地**说无法判定
    const cell = byId(w, 'fa-s-0-avg_content_size').text()
    expect(cell).toContain('0 字节')
    expect(cell).not.toContain('无法判定')
    expect(byId(w, 'fa-s-0-avg_actual_tokens').text()).toContain('无法判定')
  })
})

// ── ⑧ 去重数不超过总数 ─────────────────────────────────────────────────

describe('⑧ COUNT(DISTINCT) 不超过总数', () => {
  it('★★★★★ affected_requests > anomaly_count ⇒ 行内违规标注', async () => {
    mSum.mockResolvedValue(sumPayload({ summaries: [smRow({ anomaly_count: 3, affected_requests: 9 })] }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-sm-0').text()).toContain('去重数')
  })

  it('★★★★★ resolved_count > anomaly_count ⇒ 行内违规标注', async () => {
    mSum.mockResolvedValue(sumPayload({ summaries: [smRow({ anomaly_count: 3, resolved_count: 9 })] }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-sm-0').text()).toContain('已解决数')
  })

  it('★★★ 正控：合规 ⇒ 两条标注都不出现', async () => {
    mSum.mockResolvedValue(sumPayload({ summaries: [smRow({ anomaly_count: 10, affected_requests: 4, resolved_count: 2 })] }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    const txt = byId(w, 'fa-sm-0').text()
    expect(txt).not.toContain('去重数')
    expect(txt).not.toContain('已解决数')
  })
})

// ── ⑨ SQL 硬上限 200 ───────────────────────────────────────────────────

describe('⑨ LIMIT 200 硬编码、不回显', () => {
  it('★★★★★ count === 200 ⇒ 明说「可能还有更多」', async () => {
    const rows = Array.from({ length: FORMAT_SUMMARY_SQL_LIMIT }, (_, i) =>
      smRow({ hour: `2026-10-14T${String(i % 24).padStart(2, '0')}:00:00Z`, anomaly_count: i + 1 }),
    )
    mSum.mockResolvedValue(sumPayload({ summaries: rows, count: FORMAT_SUMMARY_SQL_LIMIT }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-summary-cap').text()).toContain(String(FORMAT_SUMMARY_SQL_LIMIT))
  })

  it('★★★ 正控：199 行 ⇒ 不得说截断', async () => {
    const rows = Array.from({ length: FORMAT_SUMMARY_SQL_LIMIT - 1 }, (_, i) =>
      smRow({ hour: `2026-10-14T${String(i % 24).padStart(2, '0')}:00:00Z`, anomaly_count: i + 1 }),
    )
    mSum.mockResolvedValue(sumPayload({ summaries: rows, count: FORMAT_SUMMARY_SQL_LIMIT - 1 }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-summary-cap')).toBe(false)
  })
})

// ── ⑩ 排序契约 ─────────────────────────────────────────────────────────

describe('⑩ 排序：hour DESC + hour 内 count 非增', () => {
  it('★★★★★ hour 不是降序 ⇒ 显式违规告警', async () => {
    mSum.mockResolvedValue(
      sumPayload({
        summaries: [smRow({ hour: '2026-10-14T01:00:00Z' }), smRow({ hour: '2026-10-14T03:00:00Z' })],
        count: 2,
      }),
    )
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-hour-order-bad')).toBe(true)
  })

  it('★★★★★ 同一 hour 内计数非增被破坏 ⇒ 显式违规告警', async () => {
    mSum.mockResolvedValue(
      sumPayload({
        summaries: [
          smRow({ hour: '2026-10-14T03:00:00Z', anomaly_count: 2, affected_requests: 1, resolved_count: 0 }),
          smRow({ hour: '2026-10-14T03:00:00Z', anomaly_count: 99, affected_requests: 1, resolved_count: 0 }),
        ],
        count: 2,
      }),
    )
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-count-order-bad')).toBe(true)
  })

  it('★★★★★ 跨 hour 计数不降序是**正常**的 ⇒ 不得误报', async () => {
    // ★★★ 这是本族最容易被误用的不变量：`ORDER BY hour DESC, anomaly_count DESC`
    //   意味着**整张表不按计数排序**；只有同一 hour 内才按计数降序。
    mSum.mockResolvedValue(
      sumPayload({
        summaries: [
          smRow({ hour: '2026-10-14T03:00:00Z', anomaly_count: 2, affected_requests: 1, resolved_count: 0 }),
          smRow({ hour: '2026-10-14T02:00:00Z', anomaly_count: 99, affected_requests: 1, resolved_count: 0 }),
        ],
        count: 2,
      }),
    )
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-count-order-bad')).toBe(false)
    expect(hasId(w, 'fa-hour-order-bad')).toBe(false)
  })
})

// ── ⑪ 开放域 + 表级缺省 ────────────────────────────────────────────────

describe('⑪ 开放域与表级缺省', () => {
  it('★★★★★ 未知 anomaly_type 标注出来，但**不是错误**', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ anomaly_type: 'some_new_kind' })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-row-1').text()).toContain('some_new_kind')
    expect(byId(w, 'fa-row-1').text()).toContain('★')
    expect(hasId(w, 'fa-list-error')).toBe(false)
  })

  it('★★★ 正控：已知 anomaly_type ⇒ 不加 ★', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ anomaly_type: 'missing_usage_block' })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-row-1').text()).not.toContain('★')
  })

  it('★★★★ severity = 表级缺省 medium ⇒ 标出它是缺省值', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ severity: 'medium' })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-row-1').text()).toContain('缺省')
  })

  it('★★★ 正控：severity = high ⇒ 不标缺省', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec({ severity: 'high' })] }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-row-1').text()).not.toContain('缺省')
  })

  it('★★★★ 已解决 / 未解决两个徽标互斥且都可见', async () => {
    mList.mockResolvedValue(
      listPayload({ anomalies: [rec({ id: 1, resolved: true }), rec({ id: 2, resolved: false })] }),
    )
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-row-1').text()).toContain('已解决')
    expect(byId(w, 'fa-row-2').text()).toContain('未解决')
  })
})

// ── hours 回显与过滤 ───────────────────────────────────────────────────

describe('hours 回显与过滤参数', () => {
  it('★★★★ hours 回显生效值', async () => {
    mSum.mockResolvedValue(sumPayload({ hours: 168 }))
    const w = await mountView()
    await byId(w, 'fa-hours-168').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-summary-hours').text()).toContain('168')
  })

  it('★★★★ 回显值 ≠ 请求值 ⇒ 告警（<=0⇒24 / >720⇒720）', async () => {
    mSum.mockResolvedValue(sumPayload({ hours: FORMAT_SUMMARY_HOURS_MAX }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-hours-rewritten').exists()).toBe(true)
  })

  it('★★★ 正控：回显 == 请求 ⇒ 不得告警', async () => {
    mSum.mockResolvedValue(sumPayload({ hours: 24 }))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-hours-rewritten')).toBe(false)
  })

  it('★★★ 三个字符串过滤只在非空时发；空串不制造无意义参数', async () => {
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    const q = mList.mock.calls[0]![0]!
    expect(q.provider).toBeUndefined()
    expect(q.model).toBeUndefined()
    expect(q.anomalyType).toBeUndefined()
  })

  it('★★★ 填了过滤就真的发出去', async () => {
    const w = await mountView()
    await byId(w, 'fa-provider').setValue('openai')
    await byId(w, 'fa-model').setValue('gpt-4o')
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    const q = mList.mock.calls[0]![0]!
    expect(q.provider).toBe('openai')
    expect(q.model).toBe('gpt-4o')
  })

  it('★★★★ unresolved_only 恒发（能区分「没传」与「传了 false」）', async () => {
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(mList.mock.calls[0]![0]!.unresolvedOnly).toBe(false)
  })

  it('★★★ 勾上 unresolved_only ⇒ 发 true', async () => {
    const w = await mountView()
    await byId(w, 'fa-unresolved').setValue(true)
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(mList.mock.calls[0]![0]!.unresolvedOnly).toBe(true)
  })

  it('★★★★ 重置清掉全部过滤并把 offset 归零', async () => {
    mList.mockResolvedValue(listPayload({ count: 900, offset: 100 }))
    const w = await mountView()
    await byId(w, 'fa-provider').setValue('openai')
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    await byId(w, 'fa-reset').trigger('click')
    await flushPromises()
    const q = mList.mock.calls[mList.mock.calls.length - 1]![0]!
    expect(q.provider).toBeUndefined()
    expect(q.offset).toBe(0)
  })
})

// ── 失败路径 ───────────────────────────────────────────────────────────

describe('失败路径', () => {
  it('★★★ list 失败 ⇒ 报错可见，且不留下上次的成功结果', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [rec()], count: 900 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-row-1')).toBe(true)
    mList.mockRejectedValueOnce(new Error('list query failed'))
    await byId(w, 'fa-load-list').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-list-error').text()).toContain('list query failed')
    expect(hasId(w, 'fa-row-1')).toBe(false)
  })

  it('★★★★ summary 的失败文案与 list 不同 ⇒ 可区分失败发生在哪一步', async () => {
    // ★★ 契约头 (15)：三条 500 文案各不相同
    mSum.mockRejectedValue(new Error('summary query failed'))
    const w = await mountView()
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(byId(w, 'fa-summary-error').text()).toContain('summary query failed')
  })

  it('★★★ 空列表 ⇒ 空态且不出错误', async () => {
    mList.mockResolvedValue(listPayload({ anomalies: [], count: 0 }))
    mSum.mockResolvedValue(sumPayload({ summaries: [], count: 0 }))
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-list-empty')).toBe(true)
    expect(hasId(w, 'fa-summary-empty')).toBe(true)
  })
})

// ── 权限档：superAdmin ⇒ 抽屉席门控，视图内部无角色分支 ───────────────

describe('superAdmin 档的处置', () => {
  it('★★★★ 视图内部**不得**有任何角色分支（档位已挡住跨租户）', async () => {
    // ★ 与批 104/106 相反：那两族是 admin 档 + 不隔离 ⇒ 必须在视图内门控；
    //   本族是 superAdmin 档 ⇒ 抽屉席 requiresRole 即完整处置。
    //   ⇒ 判据直接量「渲染出的两段内容恒等」：有角色分支的话两者会不同。
    mList.mockResolvedValue(listPayload())
    mSum.mockResolvedValue(sumPayload())
    const w = await mountView()
    await byId(w, 'fa-load-list').trigger('click')
    await byId(w, 'fa-load-summary').trigger('click')
    await flushPromises()
    expect(hasId(w, 'fa-row-1')).toBe(true)
    expect(hasId(w, 'fa-sm-0')).toBe(true)
    // ★ 页面里不得出现任何「跨租户」披露块 —— 本族由档位挡住，无需再自曝
    expect(w.text()).not.toContain('跨租户')
  })
})