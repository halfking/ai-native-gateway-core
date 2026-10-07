import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  fetchApprovals,
  unwrapApprovals,
  approvalsPageEffective,
  approvalsPageSizeEffective,
  approvalsStatusDefault,
  approvalsTotalIsPageSize,
  approvalsHasNextPage,
  approvalRiskUnknown,
  approvalCountingDown,
  approvalOverdue,
  approvalDecided,
  fetchApprovalStats,
  unwrapApprovalStats,
  approvalTimeValid,
  approvalCountsDisagree,
  approvalAvgTimeMeaningless,
  APPROVAL_LIST_KEYS,
  APPROVAL_ITEM_KEYS,
  APPROVAL_STATS_KEYS,
  APPROVAL_STATUSES,
  APPROVAL_PAGE_SIZE_DEFAULT,
  APPROVAL_PAGE_SIZE_MAX,
} from '@/api/approvals'

/**
 * approvals API 的不变量（2026-10-08，第六十四批）。
 *
 * ★ 本族最该被钉住的：
 *   ① ★★★★★ **`total` 是本页条数，不是真实总数**（approval_handler.go:348-350
 *      `total := len(records)`，而 filter 里带着 Limit/Offset）。
 *      连带 `total_pages` 恒为 1 ⇒ 翻页判据**不能**用它。
 *   ② ★★★★ **`risk_level`/`trigger_type` 可以是空串** ——
 *      `buildListItem` 只在 `record.DetectResult != nil` 时才填（:546-549）
 *      ⇒ 空串 = 未检测，不是「低风险」。
 *   ③ ★★★ **`time_left` 带 omitempty**，只在 pending 且未过期时出现
 *      ⇒ pending + 无 time_left = 已逾期却还标着待审批。
 *   ④ ★★★ **`approved_by`/`approved_at`/`reason` 带 omitempty** ⇒ 键缺失 = 未审批。
 *   ⑤ ★★ **`start_time`/`end_time` 是 RFC3339 且格式错静默回落**（`if err == nil`）。
 *   ⑥ ★★ **`avg_approval_time_seconds` 的 0 是零值**，与「真的是 0 秒」不可分。
 *   ⑦ ★★ **`TodayTotal`/`TodayPending` 无视时间范围** ⇒ 同一份响应混两套口径。
 */

vi.mock('@/api/client', () => ({ req: vi.fn() }))

const reqMock = req as unknown as ReturnType<typeof vi.fn>

function urlOf(n = 0): string {
  return reqMock.mock.calls[n]![1] as string
}

beforeEach(() => {
  reqMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

/* ── 夹具：逐字照抄后端 ─────────────────────────────────────────────── */

/** 有检测结果的行。 */
const ITEM_DETECTED = {
  id: 'a1', session_id: 's1', tenant_id: 'default', request_id: 'r1',
  status: 'pending', detect_result: { decision: 'HIGH', reason: 'risk_rule' },
  risk_level: 'HIGH', trigger_type: 'risk_rule',
  created_at: '2026-10-08T01:00:00Z', expires_at: '2026-10-08T02:00:00Z',
  time_left: '58m',
}

/** ★ 无检测结果 ⇒ risk_level/trigger_type 是**空串**。 */
const ITEM_NO_DETECT = {
  id: 'a2', session_id: 's2', tenant_id: 'default', request_id: 'r2',
  status: 'pending', detect_result: null,
  risk_level: '', trigger_type: '',
  created_at: '2026-10-08T01:00:00Z', expires_at: '2026-10-08T02:00:00Z',
}

/** ★ 已审批 ⇒ 三个 omitempty 键出现。 */
const ITEM_APPROVED = {
  ...ITEM_DETECTED,
  status: 'approved',
  approved_by: 'alice',
  approved_at: '2026-10-08T01:30:00Z',
  reason: '看起来没问题',
}

const LIST_FULL = {
  items: [ITEM_DETECTED, ITEM_APPROVED],
  total: 2,
  page: 1,
  page_size: 2,
  total_pages: 1,
}

const STATS_FULL = {
  total: 120, pending: 20, approved: 80, rejected: 15, timeout: 5,
  avg_approval_time_seconds: 342.5,
  by_risk_level: { HIGH: 30, LOW: 90 },
  by_trigger_type: { risk_rule: 40 },
  today_total: 7, today_pending: 2,
}

/** ★ 四个状态加起来 > total ⇒ 口径问题。 */
const STATS_DISAGREE = { ...STATS_FULL, pending: 50 }

/** ★ 分母为 0 ⇒ avg 是 Go 零值。 */
const STATS_NO_DECISION = {
  ...STATS_FULL, approved: 0, rejected: 0, pending: 120,
  avg_approval_time_seconds: 0,
}

/* ═══════════════════════════════════════════════════════════════════════
 * A. approvals 列表
 * ═══════════════════════════════════════════════════════════════════════ */

describe('approvals 列表（cmd/gateway/main.go:7384）', () => {
  it('正常载荷解包通过', () => {
    const r = unwrapApprovals(LIST_FULL)
    expect(r.items.length).toBe(2)
    expect(r.items[0]!.risk_level).toBe('HIGH')
  })

  it('走裸 JSON 路径', async () => {
    reqMock.mockResolvedValue(LIST_FULL)
    await fetchApprovals()
    expect(urlOf()).toBe('/api/admin/approvals')
  })

  it('★★★ total 恒等于 items.length（它是本页条数，不是真实总数）', () => {
    // ★★ 这是本族最刺眼的一处：`total := len(records)` 而 records 已带 Limit/Offset
    const r = unwrapApprovals(LIST_FULL)
    expect(approvalsTotalIsPageSize(r)).toBe(true)
  })

  it('★ total 与 items.length 不一致 ⇒ 契约漂移', () => {
    expect(approvalsTotalIsPageSize(unwrapApprovals({ ...LIST_FULL, total: 999 }))).toBe(false)
  })

  it('★★ total_pages 恒为 1 时翻页判据仍要靠「本页取满」', () => {
    const full = { ...LIST_FULL, items: new Array(2).fill(ITEM_DETECTED), total_pages: 1 }
    expect(approvalsHasNextPage(unwrapApprovals(full))).toBe(true)
  })

  it('本页没取满 ⇒ 没有下一页', () => {
    const partial = { ...LIST_FULL, items: [ITEM_DETECTED], page_size: 50 }
    expect(approvalsHasNextPage(unwrapApprovals(partial))).toBe(false)
  })

  it('★ page 越界：非整数或 ≤0 ⇒ 静默回落 1', () => {
    expect(approvalsPageEffective(0)).toBe(1)
    expect(approvalsPageEffective(-3)).toBe(1)
    expect(approvalsPageEffective(1.5)).toBe(1)
    expect(approvalsPageEffective(4)).toBe(4)
    expect(approvalsPageEffective(undefined)).toBe(1)
  })

  it('★ page_size 越界：>200 或 ≤0 ⇒ 静默回落 50', () => {
    expect(approvalsPageEffective(0)).toBe(1)
    expect(approvalsPageSizeEffective(0)).toBe(APPROVAL_PAGE_SIZE_DEFAULT)
    expect(approvalsPageSizeEffective(201)).toBe(APPROVAL_PAGE_SIZE_DEFAULT)
    expect(approvalsPageSizeEffective(APPROVAL_PAGE_SIZE_MAX)).toBe(200)
    expect(approvalsPageSizeEffective(20)).toBe(20)
  })

  it('★★ status 缺省是 pending，不是「全部」', () => {
    expect(approvalsStatusDefault(undefined)).toBe('pending')
    expect(approvalsStatusDefault(null)).toBe('pending')
    expect(approvalsStatusDefault('approved')).toBe('approved')
  })

  it('★ 不传 status 时请求里没有这个键（由后端补 pending）', async () => {
    reqMock.mockResolvedValue(LIST_FULL)
    await fetchApprovals({ page: 2, pageSize: 20 })
    expect(urlOf()).not.toContain('status')
  })

  it('★ page_size 参数名带下划线（不是 size）', async () => {
    reqMock.mockResolvedValue(LIST_FULL)
    await fetchApprovals({ pageSize: 20 })
    const u = urlOf()
    expect(u).toContain('page_size=20')
    expect(u).not.toContain('&size=')
  })

  it('查询参数逐个发对', async () => {
    reqMock.mockResolvedValue(LIST_FULL)
    await fetchApprovals({
      status: 'approved', tenantId: 'acme', riskLevel: 'HIGH',
      page: 2, pageSize: 30, sortBy: 'created_at', sortOrder: 'desc',
    })
    const u = urlOf()
    expect(u).toContain('status=approved')
    expect(u).toContain('tenant_id=acme')
    expect(u).toContain('risk_level=HIGH')
    expect(u).toContain('page=2')
    expect(u).toContain('sort_by=created_at')
    expect(u).toContain('sort_order=desc')
  })

  it('★★ detect_result 为 null 合法，但必须是 null 或对象', () => {
    expect(() => unwrapApprovals({ ...LIST_FULL, items: [ITEM_NO_DETECT] })).not.toThrow()
    expect(() => unwrapApprovals({ ...LIST_FULL, items: [{ ...ITEM_DETECTED, detect_result: 'HIGH' }] }))
      .toThrow(/detect_result 不是对象也不是 null/)
  })

  it('★ risk_level 空串 ⇒ 判「未检测」而不是「低风险」', () => {
    const item = unwrapApprovals({ ...LIST_FULL, items: [ITEM_NO_DETECT] }).items[0]!
    expect(approvalRiskUnknown(item)).toBe(true)
    expect(approvalRiskUnknown(unwrapApprovals(LIST_FULL).items[0]!)).toBe(false)
  })

  it('★★ pending 但无 time_left ⇒ 判「已逾期」', () => {
    // ★ 这条与下一条是一组判别样本：两个都是 pending，只差 time_left 有无
    const overdue = unwrapApprovals({ ...LIST_FULL, items: [ITEM_NO_DETECT] }).items[0]!
    expect(approvalOverdue(overdue)).toBe(true)
    expect(approvalCountingDown(overdue)).toBe(false)
  })

  it('★ pending 且有 time_left ⇒ 倒计时中，不是逾期', () => {
    const live = unwrapApprovals({ ...LIST_FULL, items: [ITEM_DETECTED] }).items[0]!
    expect(approvalCountingDown(live)).toBe(true)
    expect(approvalOverdue(live)).toBe(false)
  })

  it('★★★ 已审批但仍带 time_left ⇒ 不得说「倒计时中」', () => {
    // ★★ #5 那条变异的**判别样本**：
    //   夹具里所有带 time_left 的行 status 都是 pending，
    //   所以把 `status === 'pending' &&` 从倒计时判据里去掉，一样全绿。
    //   但真实数据里「已审批 + 残留 time_left」是可能出现的
    //   （后端只在 buildListItem 时按当时状态填一次，不回填清理）。
    const decided = { ...ITEM_DETECTED, status: 'approved', approved_at: '2026-10-08T01:30:00Z' }
    const item = unwrapApprovals({ ...LIST_FULL, items: [decided] }).items[0]!
    expect(approvalCountingDown(item)).toBe(false)
    expect(approvalOverdue(item)).toBe(false)
  })

  it('★ 已审批的行：三个 omitempty 键出现', () => {
    const approved = unwrapApprovals(LIST_FULL).items[1]!
    expect(approvalDecided(approved)).toBe(true)
    expect(approved.approved_by).toBe('alice')
  })

  it('★ 未审批的行：键不存在 ⇒ 判「未审批」', () => {
    expect(approvalDecided(unwrapApprovals(LIST_FULL).items[0]!)).toBe(false)
  })

  it('缺 total_pages ⇒ 抛错', () => {
    const { total_pages, ...noTp } = LIST_FULL
    void total_pages
    expect(() => unwrapApprovals(noTp)).toThrow(/缺 1 个键（total_pages）/)
  })

  it('item 缺 risk_level ⇒ 抛错（虽可空串但键必须在）', () => {
    const { risk_level, ...noRisk } = ITEM_DETECTED
    void noRisk
    expect(() => unwrapApprovals({ ...LIST_FULL, items: [noRisk] }))
      .toThrow(/items\[0\] 缺 1 个键（risk_level）/)
  })

  it('items 不是数组 ⇒ 抛错', () => {
    expect(() => unwrapApprovals({ ...LIST_FULL, items: {} })).toThrow(/items 不是数组/)
  })

  it('items 元素为 null ⇒ 抛错', () => {
    expect(() => unwrapApprovals({ ...LIST_FULL, items: [null] })).toThrow(/items\[0\] 不是对象/)
  })

  it('拿到 dashboardapi 信封 ⇒ 报错', () => {
    expect(() => unwrapApprovals({ success: true, data: LIST_FULL, timestamp: 'x' }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('导出的键清单完整', () => {
    expect([...APPROVAL_LIST_KEYS]).toEqual(['items', 'total', 'page', 'page_size', 'total_pages'])
    expect(APPROVAL_ITEM_KEYS.length).toBe(10)
    expect([...APPROVAL_STATUSES]).toEqual(['pending', 'approved', 'rejected', 'timeout'])
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * B. approvals/stats
 * ═══════════════════════════════════════════════════════════════════════ */

describe('approvals/stats（cmd/gateway/main.go:7385）', () => {
  it('正常载荷解包通过', () => {
    const r = unwrapApprovalStats(STATS_FULL)
    expect(r.total).toBe(120)
    expect(r.by_risk_level.HIGH).toBe(30)
    expect(r.today_pending).toBe(2)
  })

  it('走裸 JSON 路径', async () => {
    reqMock.mockResolvedValue(STATS_FULL)
    await fetchApprovalStats()
    expect(urlOf()).toBe('/api/admin/approvals/stats')
  })

  it('★★ start_time 格式错 ⇒ 前端就拦下（后端会静默忽略）', async () => {
    await expect(fetchApprovalStats({ startTime: '2026-10-01' })).rejects.toThrow(/RFC3339/)
    expect(reqMock).not.toHaveBeenCalled()
  })

  it('end_time 格式错同样拦下', async () => {
    await expect(fetchApprovalStats({ endTime: '10/01/2026' })).rejects.toThrow(/RFC3339/)
    expect(reqMock).not.toHaveBeenCalled()
  })

  it('RFC3339 合法值放行', () => {
    expect(approvalTimeValid('2026-10-01T00:00:00Z')).toBe(true)
    expect(approvalTimeValid('2026-10-01T00:00:00.123Z')).toBe(true)
    expect(approvalTimeValid('2026-10-01T00:00:00+08:00')).toBe(true)
    expect(approvalTimeValid('2026-10-01')).toBe(false)
    expect(approvalTimeValid(undefined)).toBe(true)
  })

  it('★ 统计参数逐个发对', async () => {
    reqMock.mockResolvedValue(STATS_FULL)
    await fetchApprovalStats({ tenantId: 'acme', startTime: '2026-10-01T00:00:00Z', endTime: '2026-10-08T00:00:00Z' })
    const u = urlOf()
    expect(u).toContain('tenant_id=acme')
    expect(u).toContain('start_time=2026-10-01T00%3A00%3A00Z')
    expect(u).toContain('end_time=2026-10-08T00%3A00%3A00Z')
  })

  it('★★ 四个状态计数加起来 > total ⇒ 报警', () => {
    expect(approvalCountsDisagree(unwrapApprovalStats(STATS_DISAGREE))).toBe(true)
    expect(approvalCountsDisagree(unwrapApprovalStats(STATS_FULL))).toBe(false)
  })

  it('★★ 没有已审批/已拒绝时 avg 无意义（不是「0 秒审批」）', () => {
    expect(approvalAvgTimeMeaningless(unwrapApprovalStats(STATS_NO_DECISION))).toBe(true)
    expect(approvalAvgTimeMeaningless(unwrapApprovalStats(STATS_FULL))).toBe(false)
  })

  it('★★★ 全是已拒绝（approved=0 但分母非 0）⇒ avg 仍有意义', () => {
    // ★★ #14 那条变异的**判别样本**：
    //   STATS_NO_DECISION 是 approved=0 且 rejected=0，两种实现同返 true。
    //   只有「approved=0 但 rejected>0」这一种取值能让两种实现分叉。
    const allRejected = {
      ...STATS_FULL, approved: 0, rejected: 100, pending: 20, timeout: 0,
      avg_approval_time_seconds: 120,
    }
    expect(approvalAvgTimeMeaningless(unwrapApprovalStats(allRejected))).toBe(false)
  })

  it('★ by_risk_level 空 map 是 {} 不是 null', () => {
    const r = unwrapApprovalStats({ ...STATS_FULL, by_risk_level: {}, by_trigger_type: {} })
    expect(r.by_risk_level).toEqual({})
  })

  it('by_risk_level 为 null ⇒ 抛错', () => {
    expect(() => unwrapApprovalStats({ ...STATS_FULL, by_risk_level: null }))
      .toThrow(/by_risk_level 不是对象/)
  })

  it('缺 today_pending ⇒ 抛错', () => {
    const { today_pending, ...noToday } = STATS_FULL
    void noToday
    expect(() => unwrapApprovalStats(noToday)).toThrow(/缺 1 个键（today_pending）/)
  })

  it('拿到 dashboardapi 信封 ⇒ 报错', () => {
    expect(() => unwrapApprovalStats({ success: true, data: STATS_FULL, timestamp: 'x' }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('导出的必填键是十个', () => {
    expect(APPROVAL_STATS_KEYS.length).toBe(10)
    expect([...APPROVAL_STATS_KEYS]).toContain('avg_approval_time_seconds')
  })
})