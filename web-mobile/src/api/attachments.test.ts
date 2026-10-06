import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchAttachments,
  unwrapAttachments,
  fetchAttachmentStats,
  unwrapAttachmentStats,
  fetchAttachmentPolicy,
  unwrapAttachmentPolicy,
  previewAttachmentCleanup,
  unwrapAttachmentCleanupPreview,
  fetchAttachmentItem,
  unwrapAttachmentItem,
  fetchAttachmentFilesystemStats,
  unwrapAttachmentFilesystemStats,
  attachmentLimitClamped,
  attachmentOffsetClamped,
  attachmentLimitWasClamped,
  attachmentOffsetWasClamped,
  attachmentSinceWillBeUsed,
  attachmentUntilWillBeUsed,
  attachmentTimeWindowWasDropped,
  attachmentRawIsNull,
  attachmentItemIsArray,
  attachmentRowSetDiffers,
  attachmentCountIsDerived,
  attachmentStatsTotalsMatch,
  attachmentPolicyIsBuiltin,
  attachmentAutoCleanupEnabled,
  attachmentPreviewDaysEffective,
  attachmentPreviewDaysFellBack,
  attachmentPreviewIsAlwaysDryRun,
  attachmentFsIsEmpty,
  attachmentFsWarningLevel,
  attachmentFsWarningMatches,
  attachmentTenantFilterNeedsSuperAdmin,
  attachmentCrossTenantIsNotFound,
  ATTACHMENT_LIST_LIMIT_DEFAULT,
  ATTACHMENT_LIST_LIMIT_MAX,
  ATTACHMENT_LIST_OFFSET_MAX,
  ATTACHMENT_POLICY_MAX_SIZE_BYTES,
  ATTACHMENT_POLICY_RETENTION_DAYS,
  ATTACHMENT_PREVIEW_DAYS_DEFAULT,
  type AttachmentRow,
  type AttachmentList,
  type AttachmentStats,
  type AttachmentPolicy,
  type AttachmentItem,
  type AttachmentFilesystemStats,
} from './attachments'

/**
 * 附件留存读面的契约测试（2026-10-08）。
 *
 * 后端逐条对应：
 *   admin/handler.go:998-1008        六条全是 admin(...)，同前缀下混着两条 superAdmin
 *   admin/handler.go:1006            /api/admin/attachments/ → item 端点（admin 档）
 *   admin/data_lifecycle_attachments.go:51-58   attachmentRow（attachments 是 json.RawMessage）
 *   admin/data_lifecycle_attachments.go:154-155 limit/offset 走 clampInt
 *   admin/data_lifecycle_attachments.go:170      list  只要 attachments IS NOT NULL
 *   admin/data_lifecycle_attachments.go:182      list  的 attachments 无 COALESCE
 *   admin/data_lifecycle_attachments.go:202-205  scan 失败 warnRowSkip + continue
 *   admin/data_lifecycle_attachments.go:212      rows.Err() ⇒ 整个 500
 *   admin/data_lifecycle_attachments.go:216-221  {items, limit, offset, count}
 *   admin/data_lifecycle_attachments.go:242      stats 多一个 jsonb_typeof='array'
 *   admin/data_lifecycle_attachments.go:251-258  ★ 时间参数被**追加了两遍**（冗余，非行为 bug）
 *   admin/data_lifecycle_attachments.go:310-314  {breakdown, total_count, total_bytes}
 *   admin/data_lifecycle_attachments.go:319-332  policy 是**硬编码常量**且无 h.db 检查
 *   admin/data_lifecycle_attachments.go:337-382  preview **无方法门**（GET 也能调）
 *   admin/data_lifecycle_attachments.go:375-381  {older_than_days, affected_records, total_bytes, dry_run, action}
 *   admin/data_lifecycle_attachments.go:609-647  item：COALESCE(attachments::text,'[]')
 *   admin/data_lifecycle_attachments.go:631-633  ORDER BY ts DESC LIMIT 1 + 跨租户 404
 *   admin/data_lifecycle_attachments.go:650-662  clampInt（空/失败⇒def，两端 clamp）
 *   admin/data_lifecycle_attachments_filesystem.go:21-32  10 键，oldest_file_time 是无 omitempty 的指针
 *   admin/data_lifecycle_attachments_filesystem.go:117-122 75/90 分档
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

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

/** 抄自 `attachmentRow`（:51-58）。 */
function row(over: Record<string, unknown> = {}): AttachmentRow {
  return {
    request_id: 'req-1',
    ts: '2026-10-01T00:00:00Z',
    tenant_id: 'acme',
    client_model: 'gpt-4o',
    success: true,
    attachments: [{ type: 'image', size: 1024 }],
    ...over,
  } as AttachmentRow
}

function listOf(items: AttachmentRow[] = [row()]): AttachmentList {
  return { items, limit: 50, offset: 0, count: items.length }
}

/** 抄自 `writeJSON(w,200,{breakdown,total_count,total_bytes})`（:310-314）。 */
function statsOf(over: Record<string, unknown> = {}): AttachmentStats {
  return {
    breakdown: [
      { type: 'image', content_type: 'image/png', count: 3, total_bytes: 3000 },
      { type: 'file', content_type: 'application/pdf', count: 1, total_bytes: 5000 },
    ],
    total_count: 4,
    total_bytes: 8000,
    ...over,
  } as AttachmentStats
}

/** 抄自 :320-331 的**硬编码** policy。 */
function policyOf(over: Record<string, unknown> = {}): AttachmentPolicy {
  return {
    policy: {
      retention_days: 30,
      max_size_bytes: 20971520,
      auto_cleanup: false,
      delete_filesystem: false,
      description: '附件元数据保留 30 天；默认不自动清理，不删除文件系统实体文件。通过 cleanup/preview + execute 手动操作。',
      ...over,
    },
    note: '策略为内置默认值，暂不支持动态配置。可通过环境变量 LLM_GATEWAY_ATTACHMENT_DISABLED=1 完全关闭附件捕获。',
  } as AttachmentPolicy
}

/** 抄自 :640-647 的 item 裸对象。 */
function itemOf(over: Record<string, unknown> = {}): AttachmentItem {
  return {
    request_id: 'req-1',
    ts: '2026-10-01T00:00:00Z',
    tenant_id: 'acme',
    client_model: 'gpt-4o',
    success: true,
    attachments: [],
    ...over,
  } as AttachmentItem
}

/** 抄自 `AttachmentFilesystemStatsResponse`（:21-32）。 */
function fsOf(over: Record<string, unknown> = {}): AttachmentFilesystemStats {
  return {
    attachment_dir: '/var/lib/llmgw/attachments',
    total_files: 12,
    total_size_bytes: 4096,
    total_size_human: '4.0 KiB',
    oldest_file_time: '2026-01-01T00:00:00Z',
    disk_total_bytes: 1000,
    disk_used_bytes: 250,
    disk_avail_bytes: 750,
    disk_usage_percent: 25,
    disk_warning_level: 'safe',
    ...over,
  } as AttachmentFilesystemStats
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('★★★★★★ 六条端点都在 admin 档，且形状两两不同', () => {
  it('★★★★★★ URL 清单', async () => {
    const urls: string[] = []
    fetchMock.mockResolvedValueOnce(jsonResponse(listOf()))
    await fetchAttachments()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(statsOf()))
    await fetchAttachmentStats()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(policyOf()))
    await fetchAttachmentPolicy()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(
      jsonResponse({ older_than_days: 30, affected_records: 5, total_bytes: 100, dry_run: true, action: 'x' }),
    )
    await previewAttachmentCleanup()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(itemOf()))
    await fetchAttachmentItem('req-1')
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(fsOf()))
    await fetchAttachmentFilesystemStats()
    urls.push(lastUrl())

    expect(urls).toEqual([
      '/api/admin/attachments',
      '/api/admin/attachments/stats',
      '/api/admin/attachments/policy',
      '/api/admin/attachments/cleanup/preview',
      '/api/admin/attachments/req-1',
      '/api/admin/attachments/filesystem/stats',
    ])
  })

  it('★★★★★★ 五种形状互不包含 ⇒ 互喂必须抛错', () => {
    const listShape = listOf()
    const statShape = statsOf()
    const polShape = policyOf()
    const prevShape = { older_than_days: 30, affected_records: 5, total_bytes: 100, dry_run: true, action: 'x' }
    const itemShape = itemOf()
    const fsShape = fsOf()

    // list
    expect(() => unwrapAttachmentStats(listShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentPolicy(listShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentCleanupPreview(listShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentItem(listShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentFilesystemStats(listShape)).toThrow(/形状不符/)
    // stats
    expect(() => unwrapAttachments(statShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentPolicy(statShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentItem(statShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentFilesystemStats(statShape)).toThrow(/形状不符/)
    // policy
    expect(() => unwrapAttachments(polShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentStats(polShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentItem(polShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentFilesystemStats(polShape)).toThrow(/形状不符/)
    // preview
    expect(() => unwrapAttachments(prevShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentStats(prevShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentItem(prevShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentFilesystemStats(prevShape)).toThrow(/形状不符/)
    // item
    expect(() => unwrapAttachments(itemShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentStats(itemShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentPolicy(itemShape)).toThrow(/形状不符/)
    // fs
    expect(() => unwrapAttachments(fsShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentStats(fsShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentPolicy(fsShape)).toThrow(/形状不符/)
    expect(() => unwrapAttachmentItem(fsShape)).toThrow(/形状不符/)
  })

  it('★★★★★★ ★★ list 的信封缺 `offset` / `count` / `limit` ⇒ 必须抛错', () => {
    // ★★★ 只靠「互喂别的形状」抓不到这三个：别的形状**都没有** items，
    //   把 offset/count 从解包条件里删掉，测试照样全绿。
    const full = listOf()
    expect(() => unwrapAttachments({ items: [], limit: 50, count: 0 })).toThrow(/形状不符/)
    expect(() => unwrapAttachments({ items: [], limit: 50, offset: 0 })).toThrow(/形状不符/)
    expect(() => unwrapAttachments({ items: [], offset: 0, count: 0 })).toThrow(/形状不符/)
    expect(() => unwrapAttachments(full)).not.toThrow()
  })

  it('★★★★★ request_id 必须 encode', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(itemOf()))
    await fetchAttachmentItem('a/b c')
    expect(lastUrl()).toBe('/api/admin/attachments/a%2Fb%20c')
  })
})

describe('★★★★★★★ list 的 `attachments` 可以是 JSON 标量 `null`', () => {
  it('★★★★★★★ `attachments: null` 是**合法行**，解包器不许抛错', () => {
    // ★ 后端 `attachments::text` 无 COALESCE；18k+ 行存的就是 JSON null
    const resp = listOf([row({ attachments: null })])
    expect(() => unwrapAttachments(resp)).not.toThrow()
    expect(attachmentRawIsNull(unwrapAttachments(resp).items[0]!)).toBe(true)
  })

  it('★★★★★★ ★★ 但 **item 端点保证是数组**（后端 COALESCE 成空数组）⇒ 同一字段两处语义不同', () => {
    const empty = itemOf({ attachments: [] })
    expect(attachmentItemIsArray(empty)).toBe(true)
    // ★ 而 list 侧同一条数据可能是 null
    const l = unwrapAttachments(listOf([row({ attachments: null })]))
    expect(attachmentRawIsNull(l.items[0]!)).toBe(true)
    expect(attachmentItemIsArray(l.items[0] as unknown as AttachmentItem)).toBe(false)
  })

  it('★★★★★ list 的 `attachments` 也能是**对象**或字符串（原样透传，不解析）', () => {
    const obj = unwrapAttachments(listOf([row({ attachments: { weird: true } })]))
    expect(obj.items[0]!.attachments).toEqual({ weird: true })
    const str = unwrapAttachments(listOf([row({ attachments: 'oops' })]))
    expect(str.items[0]!.attachments).toBe('oops')
  })

  it('★★★★★ `client_model` 是空串而不是 null（COALESCE）', () => {
    const l = unwrapAttachments(listOf([row({ client_model: '' })]))
    expect(l.items[0]!.client_model).toBe('')
  })
})

describe('★★★★★★★ stats 的行集合是 list 的真子集', () => {
  it('★★★★★★★ list 的 `IS NOT NULL` vs stats 的多一个 `jsonb_typeof=\'array\'`', () => {
    // 客户端只能观察到「条数对不上」，口径差异由后端注释保证
    expect(attachmentRowSetDiffers(10, 4)).toBe(true)
    expect(attachmentRowSetDiffers(4, 4)).toBe(false)
  })

  it('★★★★★ 客户端独立复算 stats 的两个汇总值', () => {
    expect(attachmentStatsTotalsMatch(statsOf())).toBe(true)
    expect(attachmentStatsTotalsMatch(statsOf({ total_count: 99 }))).toBe(false)
    expect(attachmentStatsTotalsMatch(statsOf({ total_bytes: 99 }))).toBe(false)
  })

  it('★★★★★ `breakdown` 为空数组时汇总也是 0', () => {
    const s = statsOf({ breakdown: [], total_count: 0, total_bytes: 0 })
    expect(attachmentStatsTotalsMatch(s)).toBe(true)
  })
})

describe('★★★★★★ limit / offset 是**两端 clamp**（不是回落）', () => {
  it('★★★★★★ `99999` ⇒ 200（不是回落 50）', () => {
    expect(attachmentLimitClamped(99999)).toBe(ATTACHMENT_LIST_LIMIT_MAX)
    expect(attachmentLimitClamped(99999)).not.toBe(ATTACHMENT_LIST_LIMIT_DEFAULT)
  })

  it('★★★★★★ `0` / 负数 ⇒ 1（夹到下界，不是回落 50）', () => {
    expect(attachmentLimitClamped(0)).toBe(1)
    expect(attachmentLimitClamped(-9)).toBe(1)
  })

  it('★★★★★★ 不传 ⇒ 50（默认值）', () => {
    expect(attachmentLimitClamped(undefined)).toBe(50)
    expect(attachmentLimitClamped(null)).toBe(50)
    expect(ATTACHMENT_LIST_LIMIT_DEFAULT).toBe(50)
    expect(ATTACHMENT_LIST_LIMIT_MAX).toBe(200)
  })

  it('★★★★★★ offset：负数 ⇒ 0，超大 ⇒ 100000', () => {
    expect(attachmentOffsetClamped(-5)).toBe(0)
    expect(attachmentOffsetClamped(999999)).toBe(ATTACHMENT_LIST_OFFSET_MAX)
    expect(attachmentOffsetClamped(undefined)).toBe(0)
  })

  it('★★★★★ 非法输入 ⇒ 默认值（非数字回默认，不是 clamp）', () => {
    expect(attachmentLimitClamped('abc')).toBe(50)
    expect(attachmentLimitClamped(Number.NaN)).toBe(50)
    expect(attachmentOffsetClamped('abc')).toBe(0)
  })

  it('★★★★★ 「被夹过」标志与生效值一致', () => {
    expect(attachmentLimitWasClamped(99999)).toBe(true)
    expect(attachmentLimitWasClamped(50)).toBe(false)
    expect(attachmentLimitWasClamped(undefined)).toBe(false)
    expect(attachmentOffsetWasClamped(-5)).toBe(true)
    expect(attachmentOffsetWasClamped(0)).toBe(false)
  })

  it('★★★★★ URL 里带上 limit/offset 原样发（clamp 由服务端做）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(listOf()))
    await fetchAttachments({ limit: 99999, offset: -5 })
    expect(lastUrl()).toBe('/api/admin/attachments?limit=99999&offset=-5')
  })
})

describe('★★★★★★★ 时间参数解析失败被**静默丢弃**', () => {
  it('★★★★★★★ 只有严格 RFC3339 才会被采纳', () => {
    expect(attachmentSinceWillBeUsed('2026-10-01T00:00:00Z')).toBe(true)
    expect(attachmentSinceWillBeUsed('2026-10-01T00:00:00+08:00')).toBe(true)
    expect(attachmentSinceWillBeUsed('2026-10-01')).toBe(false)
    expect(attachmentSinceWillBeUsed('2026-13-45T00:00:00Z')).toBe(false)
    expect(attachmentSinceWillBeUsed('garbage')).toBe(false)
    expect(attachmentSinceWillBeUsed('')).toBe(false)
    expect(attachmentSinceWillBeUsed(undefined)).toBe(false)
    expect(attachmentUntilWillBeUsed('nope')).toBe(false)
  })

  it('★★★★★★★★ ★ 形状对但**取值越界**也算不合法（Go 会因月份 13 拒绝）', () => {
    // ★★ 只用 `\d{2}` 的形状正则会把这些**误判成有效** ⇒ 页面会提示
    //   「窗口生效」，而后端其实**静默丢弃**了 ⇒ 提示反过来误导人。
    expect(attachmentSinceWillBeUsed('2026-13-45T00:00:00Z')).toBe(false) // 月 13
    expect(attachmentSinceWillBeUsed('2026-00-01T00:00:00Z')).toBe(false) // 月 0
    expect(attachmentSinceWillBeUsed('2026-01-32T00:00:00Z')).toBe(false) // 日 32
    expect(attachmentSinceWillBeUsed('2026-01-01T24:00:00Z')).toBe(false) // 时 24
    expect(attachmentSinceWillBeUsed('2026-01-01T00:60:00Z')).toBe(false) // 分 60
    expect(attachmentSinceWillBeUsed('2026-01-01T00:00:61Z')).toBe(false) // 秒 61
    expect(attachmentSinceWillBeUsed('2026-01-01T00:00:00+25:00')).toBe(false) // 时区 25
    expect(attachmentSinceWillBeUsed('2026-01-01T00:00:00+08:60')).toBe(false) // 时区分 60
  })

  it('★★★★★ 秒 60（闰秒）Go 允许 ⇒ 判有效', () => {
    expect(attachmentSinceWillBeUsed('2016-12-31T23:59:60Z')).toBe(true)
  })

  it('★★★★★★★ 用户填了但不合法 ⇒ 「窗口没生效」标志为真', () => {
    expect(attachmentTimeWindowWasDropped('garbage')).toBe(true)
    expect(attachmentTimeWindowWasDropped(undefined, 'nope')).toBe(true)
    expect(attachmentTimeWindowWasDropped('2026-10-01T00:00:00Z')).toBe(false)
    expect(attachmentTimeWindowWasDropped()).toBe(false)
    expect(attachmentTimeWindowWasDropped('')).toBe(false)
  })

  it('★★★★★ 不合法的时间**照样发**（客户端不能替后端偷偷丢掉，得让页面能提示）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(listOf()))
    await fetchAttachments({ since: 'garbage' })
    expect(lastUrl()).toBe('/api/admin/attachments?since=garbage')
  })
})

describe('★★★★★★ `count` 是派生值', () => {
  it('★★★★★ `count === items.length`', () => {
    expect(attachmentCountIsDerived(listOf())).toBe(true)
    expect(attachmentCountIsDerived(listOf([]))).toBe(true)
    expect(attachmentCountIsDerived({ items: [], limit: 50, offset: 0, count: 3 })).toBe(false)
  })
})

describe('★★★★★★ policy 是硬编码常量', () => {
  it('★★★★★★ 核对内置默认值', () => {
    expect(attachmentPolicyIsBuiltin(policyOf())).toBe(true)
    expect(attachmentPolicyIsBuiltin(policyOf({ retention_days: 7 }))).toBe(false)
    expect(ATTACHMENT_POLICY_RETENTION_DAYS).toBe(30)
    expect(ATTACHMENT_POLICY_MAX_SIZE_BYTES).toBe(20971520)
  })

  it('★★★★★ `auto_cleanup` 恒 false ⇒ 现在什么都不自动删', () => {
    expect(attachmentAutoCleanupEnabled(policyOf())).toBe(false)
  })

  it('★★★★★ `note` 必须原样带出来（它说明只能整体关掉）', () => {
    const p = policyOf()
    expect(p.note).toContain('LLM_GATEWAY_ATTACHMENT_DISABLED')
  })
})

describe('★★★★★★ preview：无方法门 + 越界回落 30', () => {
  it('★★★★★★ ★★ `older_than_days` 越界 / ≤0 ⇒ **回落 30**（不是 clamp）', () => {
    expect(attachmentPreviewDaysEffective(0)).toBe(ATTACHMENT_PREVIEW_DAYS_DEFAULT)
    expect(attachmentPreviewDaysEffective(-4)).toBe(ATTACHMENT_PREVIEW_DAYS_DEFAULT)
    expect(attachmentPreviewDaysEffective(undefined)).toBe(ATTACHMENT_PREVIEW_DAYS_DEFAULT)
    // ★ 注意：这里**没有上界**，10 万就是 10 万（与 list 的 limit 语义不同）
    expect(attachmentPreviewDaysEffective(100000)).toBe(100000)
  })

  it('★★★★★ 合法值原样透传', () => {
    expect(attachmentPreviewDaysEffective(7)).toBe(7)
    expect(attachmentPreviewDaysFellBack(-4)).toBe(false)
    expect(attachmentPreviewIsAlwaysDryRun()).toBe(true)
  })

  it('★★★★★ preview 走 GET（handler 里**没有**方法门）', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ older_than_days: 30, affected_records: 0, total_bytes: 0, dry_run: true, action: 'x' }),
    )
    await previewAttachmentCleanup({ olderThanDays: 30 })
    expect(String(fetchMock.mock.calls.at(-1)![1]!.method)).toBe('GET')
  })

  it('★★★★★ `dry_run` 是 boolean 且恒 true', () => {
    expect(
      unwrapAttachmentCleanupPreview({
        older_than_days: 30,
        affected_records: 0,
        total_bytes: 0,
        dry_run: true,
        action: 'x',
      }).dry_run,
    ).toBe(true)
    // ★ 缺 dry_run ⇒ 抛错
    expect(() =>
      unwrapAttachmentCleanupPreview({ older_than_days: 30, affected_records: 0, total_bytes: 0 }),
    ).toThrow(/形状不符/)
  })
})

describe('★★★★★★ filesystem/stats 的裸对象', () => {
  it('★★★★★★ `oldest_file_time` 是**无** omitempty 的指针：键在值可 null', () => {
    const empty = fsOf({ oldest_file_time: null, total_files: 0 })
    expect(() => unwrapAttachmentFilesystemStats(empty)).not.toThrow()
    expect(attachmentFsIsEmpty(unwrapAttachmentFilesystemStats(empty))).toBe(true)
    expect(attachmentFsIsEmpty(fsOf())).toBe(false)
  })

  it('★★★★★★ 键整个不存在 ⇒ 抛错（null 与缺键是两回事）', () => {
    const noKey = JSON.parse(JSON.stringify(fsOf())) as Record<string, unknown>
    delete noKey.oldest_file_time
    expect(() => unwrapAttachmentFilesystemStats(noKey)).toThrow(/形状不符/)
  })

  it('★★★★★★ 磁盘告警分档 >=90 danger、>=75 warning、否则 safe', () => {
    expect(attachmentFsWarningLevel(25)).toBe('safe')
    expect(attachmentFsWarningLevel(74.9)).toBe('safe')
    expect(attachmentFsWarningLevel(75)).toBe('warning')
    expect(attachmentFsWarningLevel(89.9)).toBe('warning')
    expect(attachmentFsWarningLevel(90)).toBe('danger')
    expect(attachmentFsWarningLevel(100)).toBe('danger')
  })

  it('★★★★★ 后端给的 level 与复算不一致时判 false', () => {
    expect(attachmentFsWarningMatches(fsOf())).toBe(true)
    expect(attachmentFsWarningMatches(fsOf({ disk_usage_percent: 95 }))).toBe(false)
  })
})

describe('★★ 两处权限/存在性事实', () => {
  it('★★ `tenant_id` 对 tenant_admin 静默无效', () => {
    expect(attachmentTenantFilterNeedsSuperAdmin()).toBe(true)
  })

  it('★★ 跨租户是 **404 不是 403**（源码明说是为避免泄漏存在性）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'request not found' } }, 404))
    await expect(fetchAttachmentItem('req-other')).rejects.toThrow(/request not found/)
    expect(attachmentCrossTenantIsNotFound()).toBe(true)
  })
})

describe('★★ 错误态透出', () => {
  it('★★ 503 `database unavailable`（h.db == nil）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'database unavailable' } }, 503))
    await expect(fetchAttachments()).rejects.toThrow(/database unavailable/)
  })

  it('★★ 400 `missing request_id`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'missing request_id' } }, 400))
    await expect(fetchAttachmentItem('')).rejects.toThrow(/missing request_id/)
  })

  it('★★ 405 `method not allowed`（filesystem/stats 有方法门）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'method not allowed' } }, 405))
    await expect(fetchAttachmentFilesystemStats()).rejects.toThrow(/method not allowed/)
  })

  it('★★★ 503 `analytics_view_missing`（rows.Err() 是缺关系时）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'analytics_view_missing' } }, 503))
    await expect(fetchAttachments()).rejects.toThrow(/analytics_view_missing/)
  })

  it('★★ 500 `query failed`', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'query failed' } }, 500))
    await expect(fetchAttachments()).rejects.toThrow(/query failed/)
  })
})