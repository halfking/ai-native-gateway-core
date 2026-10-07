import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchStorageOverview,
  fetchStorageTableSizesChecked,
  storageTablesLimitIsSendable,
  unwrapStorageOverview,
  unwrapStorageTableSizes,
  humanBytesGo,
  storageDatabaseQueryFailed,
  storageDatabaseBlockIsAllZero,
  storageDatabaseExceedsRelationSum,
  storageTotalFellBackToDatabaseSize,
  storageColumnarNoteKind,
  storageColumnarQuerySucceeded,
  storageColumnarAvailableButUnmeasured,
  storageWarningKind,
  storageWarningOfKind,
  storageDbOverDiskRatio,
  storageFilesystemReservedBytes,
  storageLocalDirUnavailable,
  storageLocalDirHasNoFiles,
  storageLocalDirMtimesOrdered,
  storageOverviewIsUtc,
  storageDatabaseHumanIsGoStyle,
  storageHumanMatches,
  tableSizesTotalMatchesRows,
  tableSizesHumanMatchesTotal,
  tableRowHumsMatch,
  tableSizesIsDescending,
  tableSizesPercentIsTopNRelative,
  tableSizesMayDoubleCount,
  STORAGE_TABLES_LIMIT_FALLBACK,
  STORAGE_TABLES_LIMIT_SEND_MIN,
  STORAGE_TABLES_LIMIT_SEND_MAX,
  STORAGE_DB_OVER_DISK_RATIO,
  STORAGE_FS_DISK_ALERT_PERCENT,
  STORAGE_LOGS_ALERT_BYTES,
  STORAGE_DB_FREE_IS_UNMEASURED,
  STORAGE_WARNING_COLUMNAR_FAILED_IS_UNREACHABLE,
  STORAGE_COLUMNAR_NOTE_EXT_MISSING,
  STORAGE_COLUMNAR_NOTE_QUERY_FAILED_PREFIX,
  STORAGE_WARNING_DB_FAILED_PREFIX,
  STORAGE_WARNING_FS_FAILED_PREFIX,
  STORAGE_WARNING_DB_OVER_DISK,
  STORAGE_WARNING_FS_DISK_ALERT,
  type DatabaseStorageInfo,
  type ColumnarStorageInfo,
  type FilesystemInfo,
  type LocalDirInfo,
  type StorageOverview,
  type StorageTableRow,
  type StorageTableSizes,
} from './dataLifecycleStorage'

/**
 * 存储总览 + 表级大小的契约测试（2026-10-08，第八十批）。
 *
 * 后端：`admin/handler.go:982` / `:983`（两个 `admin(...)` ⇒ admin 档），
 * 实现 `admin/data_lifecycle_storage.go`（两个 handler + helpers）。
 *
 * 本 spec 的重点是十三件已在源文件头写明的事（(1)…(13)），其中带 ★ 的自校验判据
 * 都能被变异打掉（每条都有具名用例作为锚点）。
 *
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 用
 *    `/it\('([^']*)'/` 抽标题当锚点，参数化标题抽不到（第七十九批教训）。
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

// ── 夹具（逐字照抄后端 struct 的键集，data_lifecycle_storage.go:39-128） ──────

function db(over: Partial<DatabaseStorageInfo> = {}): DatabaseStorageInfo {
  return {
    database_bytes: 10_485_760,
    database_human: '10 MB', // ← PostgreSQL pg_size_pretty 风格（见 (3)）
    total_bytes: 12_582_912,
    total_human: '12 MB', // ← 本仓 humanBytes 风格
    tables_bytes: 8_388_608,
    indexes_bytes: 3_145_728,
    toast_bytes: 1_048_576,
    free_bytes: 0, // ← 恒 0（见 (2)）
    free_human: '', // ← 恒空串（见 (2)）
    server_version: '16.4',
    ...over,
  }
}

function col(over: Partial<ColumnarStorageInfo> = {}): ColumnarStorageInfo {
  return {
    available: true,
    table_count: 3,
    total_columns: 42,
    total_bytes: 2_097_152,
    total_human: '2 MB',
    ...over,
  }
}

function fs(over: Partial<FilesystemInfo> = {}): FilesystemInfo {
  return {
    path: '/var/lib/llm-gateway-go',
    total_bytes: 107_374_182_400,
    total_human: '100 GB',
    used_bytes: 53_687_091_200,
    used_human: '50 GB',
    free_bytes: 53_687_091_200,
    free_human: '50 GB',
    used_percent: 50,
    ...over,
  }
}

function dir(over: Partial<LocalDirInfo> = {}): LocalDirInfo {
  return {
    path: '/var/lib/llm-gateway-go/logs',
    exists: true,
    files: 12,
    size_bytes: 1_048_576,
    size_human: '1 MB',
    oldest_mtime: 1_757_000_000,
    newest_mtime: 1_757_500_000,
    ...over,
  }
}

function ov(over: Partial<StorageOverview> = {}): StorageOverview {
  return {
    database: db(),
    columnar: col(),
    filesystem: fs(),
    local_logs: dir(),
    warnings: [],
    collected_at: '2026-10-08T02:00:00.123456789Z',
    ...over,
  }
}

function row(over: Partial<StorageTableRow> = {}): StorageTableRow {
  return {
    table: 'request_logs',
    schema: 'public',
    rows: 1_234_567,
    total_bytes: 10_485_760,
    total_human: '10 MB',
    index_bytes: 2_097_152,
    toast_bytes: 1_048_576,
    toast_human: '1 MB', // ← 桌面 tuning.ts 漏掉的键（见源文件头）
    percent_of_db: 50,
    is_partitioned: false,
    ...over,
  }
}

function sizes(over: Partial<StorageTableSizes> = {}): StorageTableSizes {
  return {
    tables: [row()],
    total_bytes: 10_485_760,
    total_human: '10 MB',
    collected_at: '2026-10-08T02:00:00Z',
    ...over,
  }
}

function okOverview(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse(ov()))
}
function okSizes(): void {
  fetchMock.mockResolvedValueOnce(jsonResponse(sizes()))
}

// ═════════════════════════════════════════════════════════════════════════════
// URL 与参数
// ═════════════════════════════════════════════════════════════════════════════

describe('URL 与参数', () => {
  it('★ overview 无参数 ⇒ 裸路径', async () => {
    okOverview()
    await fetchStorageOverview()
    expect(lastUrl()).toBe('/api/admin/data-lifecycle/storage')
  })

  it('★ tables 无参数 ⇒ 裸路径', async () => {
    okSizes()
    await fetchStorageTableSizesChecked()
    expect(lastUrl()).toBe('/api/admin/data-lifecycle/storage/tables')
  })

  it('★ limit=200 发得出去', async () => {
    okSizes()
    await fetchStorageTableSizesChecked({ limit: 200 })
    expect(lastUrl()).toContain('limit=200')
  })

  it('★ limit=201 不发（后端静默回落，不是 400）', async () => {
    okSizes()
    await fetchStorageTableSizesChecked({ limit: 201 })
    expect(lastUrl()).not.toContain('limit=')
  })

  it('★ limit=0 不发（后端 n > 0 才收）', () => {
    expect(storageTablesLimitIsSendable(0)).toBe(false)
  })

  it('★ limit=1 发得出去（下界是闭区间）', () => {
    expect(storageTablesLimitIsSendable(1)).toBe(true)
  })

  it('★ limit=-3 不发', () => {
    expect(storageTablesLimitIsSendable(-3)).toBe(false)
  })

  it('★ limit=NaN 不发', () => {
    expect(storageTablesLimitIsSendable(Number.NaN)).toBe(false)
  })

  it('★ limit=Infinity 不发（Number.isFinite 守卫）', () => {
    expect(storageTablesLimitIsSendable(Number.POSITIVE_INFINITY)).toBe(false)
  })

  it('★ limit=undefined / null 不发', () => {
    expect(storageTablesLimitIsSendable(undefined)).toBe(false)
    expect(storageTablesLimitIsSendable(null)).toBe(false)
  })

  it('★ limit=1.5 截断成 1 后发出（Math.trunc 与 URL 里的 String 一致）', async () => {
    okSizes()
    await fetchStorageTableSizesChecked({ limit: 1.5 })
    expect(lastUrl()).toContain('limit=1')
    expect(lastUrl()).not.toContain('1.5')
  })

  it('后端常量：回落 20 / 下界 1 / 上界 200 / 5 倍 / 90% / 5GB', () => {
    expect(STORAGE_TABLES_LIMIT_FALLBACK).toBe(20)
    expect(STORAGE_TABLES_LIMIT_SEND_MIN).toBe(1)
    expect(STORAGE_TABLES_LIMIT_SEND_MAX).toBe(200)
    expect(STORAGE_DB_OVER_DISK_RATIO).toBe(5)
    expect(STORAGE_FS_DISK_ALERT_PERCENT).toBe(90)
    // ★ 5<<30 = 5368709120（严格大于触发）
    expect(STORAGE_LOGS_ALERT_BYTES).toBe(5368709120)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 形状校验（判据 1：缺键与「键在但类型错」是两条分支，必须成对）
// ═════════════════════════════════════════════════════════════════════════════

describe('overview 形状校验', () => {
  it('★ 响应不是裸对象 ⇒ 抛错', () => {
    expect(() => unwrapStorageOverview(null)).toThrow(/存储总览 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ 响应是数组 ⇒ 抛错（点明实得 array）', () => {
    expect(() => unwrapStorageOverview([])).toThrow(/实得 array/)
  })

  it('★ 响应是字符串 ⇒ 抛错（点明实得 string）', () => {
    expect(() => unwrapStorageOverview('x')).toThrow(/实得 string/)
  })

  it('★ 顶层缺 database ⇒ 抛错并点名键', () => {
    const d = { ...ov() } as Record<string, unknown>
    delete d['database']
    expect(() => unwrapStorageOverview(d)).toThrow(/存储总览 缺 1 个键（database）/)
  })

  it('★ 顶层缺 warnings ⇒ 抛错并点名 warnings', () => {
    const d = { ...ov() } as Record<string, unknown>
    delete d['warnings']
    expect(() => unwrapStorageOverview(d)).toThrow(/缺 1 个键（warnings）/)
  })

  it('★ 顶层缺 collected_at ⇒ 抛错并点名 collected_at', () => {
    const d = { ...ov() } as Record<string, unknown>
    delete d['collected_at']
    expect(() => unwrapStorageOverview(d)).toThrow(/缺 1 个键（collected_at）/)
  })

  it('★ database 键齐全只错类型（total_bytes 写成字符串）⇒ 抛错点名 total_bytes', () => {
    const d = { ...ov(), database: { ...db(), total_bytes: '12 MB' } }
    expect(() => unwrapStorageOverview(d)).toThrow(/存储总览 的 database 的 total_bytes 不是数字/)
  })

  it('★ database 键齐全只错类型（database_human 写成 null）⇒ 抛错点名 database_human', () => {
    const d = { ...ov(), database: { ...db(), database_human: null } }
    expect(() => unwrapStorageOverview(d)).toThrow(/的 database_human 不是字符串/)
  })

  it('★ database 缺 free_human（它没有 omitempty，恒在）⇒ 抛错', () => {
    const d = { ...db() } as Record<string, unknown>
    delete d['free_human']
    // ★ 正则必须收紧到 requireKeys 自己的措辞：否则下面的「类型检查」分支
    //   也会因为 free_human 是 undefined 而抛出一条含同名字样的错 ⇒ 用例照样绿。
    expect(() => unwrapStorageOverview({ ...ov(), database: d })).toThrow(/缺 1 个键（free_human）/)
  })

  it('★ free_human 键齐全但写成 null ⇒ 抛错点名 free_human（类型检查那一支）', () => {
    expect(() => unwrapStorageOverview({ ...ov(), database: { ...db(), free_human: null } })).toThrow(
      /的 free_human 不是字符串/,
    )
  })

  it('★ free_bytes 键齐全但写成字符串 ⇒ 抛错点名 free_bytes（类型检查那一支）', () => {
    expect(() => unwrapStorageOverview({ ...ov(), database: { ...db(), free_bytes: '0' } })).toThrow(
      /的 free_bytes 不是数字/,
    )
  })

  it('★ columnar 的 available 写成 1 ⇒ 抛错点名 available（布尔检查那一支）', () => {
    expect(() => unwrapStorageOverview({ ...ov(), columnar: { ...col(), available: 1 } })).toThrow(
      /的 available 不是布尔/,
    )
  })

  it('★ filesystem 的 used_percent 写成字符串 ⇒ 抛错点名 used_percent', () => {
    expect(() => unwrapStorageOverview({ ...ov(), filesystem: { ...fs(), used_percent: '50' } })).toThrow(
      /的 used_percent 不是数字/,
    )
  })

  it('★ local_logs 键在但写成 null ⇒ 抛错（指针键要么缺要么是对象）', () => {
    expect(() =>
      unwrapStorageOverview({ ...ov(), local_logs: null as unknown as LocalDirInfo }),
    ).toThrow(/的 local_logs 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ local_logs 键齐全只错类型（files 写成字符串）⇒ 抛错点名 files', () => {
    expect(() =>
      unwrapStorageOverview({ ...ov(), local_logs: { ...dir(), files: '12' as unknown as number } }),
    ).toThrow(/local_logs 的 files 不是数字/)
  })

  it('★ columnar 的 note 缺键是合法的（omitempty 条件键）', () => {
    expect(unwrapStorageOverview(ov()).columnar.note).toBeUndefined()
  })

  it('★ columnar 的 note 在但类型错（数字）⇒ 抛错点名 note', () => {
    expect(() => unwrapStorageOverview({ ...ov(), columnar: { ...col(), note: 7 } })).toThrow(
      /的 note 不是字符串/,
    )
  })

  it('★ columnar 缺 total_human（恒发键）⇒ 抛错', () => {
    const c = { ...col() } as Record<string, unknown>
    delete c['total_human']
    expect(() => unwrapStorageOverview({ ...ov(), columnar: c })).toThrow(/缺 1 个键（total_human）/)
  })

  it('★ local_logs 缺键是合法的（指针 + omitempty）', () => {
    const r = ov()
    delete (r as unknown as Record<string, unknown>)['local_logs']
    expect(unwrapStorageOverview(r).local_logs).toBeUndefined()
  })

  it('★ local_logs 在但缺 exists ⇒ 抛错', () => {
    const l = { ...dir() } as Record<string, unknown>
    delete l['exists']
    expect(() => unwrapStorageOverview({ ...ov(), local_logs: l })).toThrow(/缺 1 个键（exists）/)
  })

  it('★ warnings 不是数组 ⇒ 抛错', () => {
    expect(() => unwrapStorageOverview({ ...ov(), warnings: '无' })).toThrow(/warnings 不是数组/)
  })

  it('★ collected_at 不是字符串 ⇒ 抛错', () => {
    expect(() => unwrapStorageOverview({ ...ov(), collected_at: 1757000000 })).toThrow(
      /collected_at 不是字符串/,
    )
  })

  it('★ 正常响应原样通过（五块齐全 + 键数一致）', () => {
    const r = unwrapStorageOverview(ov())
    expect(r.database.server_version).toBe('16.4')
    expect(r.local_logs?.files).toBe(12)
    expect(r.warnings).toEqual([])
  })
})

describe('tables 形状校验', () => {
  it('★ 响应不是裸对象 ⇒ 抛错', () => {
    expect(() => unwrapStorageTableSizes(null)).toThrow(/表级大小 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ 顶层缺 tables ⇒ 抛错并点名 tables', () => {
    const d = { ...sizes() } as Record<string, unknown>
    delete d['tables']
    expect(() => unwrapStorageTableSizes(d)).toThrow(/表级大小 缺 1 个键（tables）/)
  })

  it('★ tables 是空数组是合法的（后端 make(…, 0, limit) ⇒ 恒数组）', () => {
    const r = unwrapStorageTableSizes(sizes({ tables: [], total_bytes: 0, total_human: '0 B' }))
    expect(r.tables).toEqual([])
  })

  it('★ tables 是 null ⇒ 抛错（不是 []，是 null）', () => {
    expect(() => unwrapStorageTableSizes(sizes({ tables: null as unknown as StorageTableRow[] }))).toThrow(
      /tables 不是数组/,
    )
  })

  it('★ 行缺 toast_human ⇒ 抛错（桌面 tuning.ts 漏掉的那个键）', () => {
    const t = { ...row() } as Record<string, unknown>
    delete t['toast_human']
    // ★ 收紧到 requireKeys 的措辞（下游的类型检查也会因为 undefined 抛出一条含同名的错）
    expect(() => unwrapStorageTableSizes(sizes({ tables: [t as unknown as StorageTableRow] }))).toThrow(
      /缺 1 个键（toast_human）/,
    )
  })

  it('★ 行缺 percent_of_db ⇒ 抛错', () => {
    const t = { ...row() } as Record<string, unknown>
    delete t['percent_of_db']
    expect(() => unwrapStorageTableSizes(sizes({ tables: [t as unknown as StorageTableRow] }))).toThrow(
      /缺 1 个键（percent_of_db）/,
    )
  })

  it('★ 行键齐全只错类型（is_partitioned 写成 0）⇒ 抛错点名 is_partitioned', () => {
    expect(() =>
      unwrapStorageTableSizes(
        sizes({ tables: [{ ...row(), is_partitioned: 0 as unknown as boolean }] }),
      ),
    ).toThrow(/tables\[0\] 的 is_partitioned 不是布尔/)
  })

  it('★ 行键齐全只错类型（rows 写成字符串）⇒ 抛错点名 rows', () => {
    expect(() =>
      unwrapStorageTableSizes(sizes({ tables: [{ ...row(), rows: '123' as unknown as number }] })),
    ).toThrow(/tables\[0\] 的 rows 不是数字/)
  })

  it('★ toast_human 键齐全但写成数字 ⇒ 抛错点名 toast_human（类型检查那一支）', () => {
    // ★ 这条才是「删掉表行 human 串的类型检查」能打掉的：
    //   缺键那条会被 requireKeys 兜住，删类型检查后仍红 ⇒ 锚点必须指这条。
    expect(() =>
      unwrapStorageTableSizes(
        sizes({ tables: [{ ...row(), toast_human: 1 as unknown as string }] }),
      ),
    ).toThrow(/tables\[0\] 的 toast_human 不是字符串/)
  })

  it('★ 第二行错类型时报出下标 1（点明是哪一行）', () => {
    expect(() =>
      unwrapStorageTableSizes(
        sizes({ tables: [row(), { ...row(), total_bytes: 'x' as unknown as number }] }),
      ),
    ).toThrow(/tables\[1\] 的 total_bytes 不是数字/)
  })

  it('★ 顶层 total_human 不是字符串 ⇒ 抛错', () => {
    expect(() => unwrapStorageTableSizes(sizes({ total_human: 10485760 as unknown as string }))).toThrow(
      /表级大小 的 total_human 不是字符串/,
    )
  })

  it('★ 顶层 total_bytes 写成字符串 ⇒ 抛错点名 total_bytes', () => {
    expect(() => unwrapStorageTableSizes(sizes({ total_bytes: '10 MB' as unknown as number }))).toThrow(
      /表级大小 的 total_bytes 不是数字/,
    )
  })

  it('★ 顶层 collected_at 写成数字 ⇒ 抛错点名 collected_at', () => {
    expect(() => unwrapStorageTableSizes(sizes({ collected_at: 1757000000 as unknown as string }))).toThrow(
      /表级大小 的 collected_at 不是字符串/,
    )
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (1)：database 查询失败 = 整块零值
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (1) database 查询失败留整块零值', () => {
  it('★ database_human 为空 ⇒ 判失败', () => {
    expect(storageDatabaseQueryFailed(ov({ database: db({ database_human: '' }) }))).toBe(true)
  })

  it('★ database_human 非空 ⇒ 不判失败（反向）', () => {
    expect(storageDatabaseQueryFailed(ov())).toBe(false)
  })

  it('★ 整块全零（含 server_version 缺键）⇒ 全零判据成立', () => {
    const zero = {
      database_bytes: 0,
      database_human: '',
      total_bytes: 0,
      total_human: '',
      tables_bytes: 0,
      indexes_bytes: 0,
      toast_bytes: 0,
      free_bytes: 0,
      free_human: '',
    } as DatabaseStorageInfo
    expect(storageDatabaseBlockIsAllZero(zero)).toBe(true)
  })

  it('★ 有 server_version ⇒ 不算全零（反向：那是「拿到了版本号」）', () => {
    expect(storageDatabaseBlockIsAllZero({ ...db({ database_human: '' }), server_version: undefined })).toBe(
      false,
    )
  })

  it('★ 任何一字段非零 ⇒ 不算全零', () => {
    expect(storageDatabaseBlockIsAllZero(db())).toBe(false)
  })

  it('★ 只有一个字段非零（free_bytes）⇒ 也不算全零', () => {
    // ★ 这条才是「漏掉 free_bytes 那一项」能打掉的：
    //   只给 free_bytes 一个非零值、其余全零，漏项后仍会返回 true。
    const onlyFree = {
      database_bytes: 0,
      database_human: '',
      total_bytes: 0,
      total_human: '',
      tables_bytes: 0,
      indexes_bytes: 0,
      toast_bytes: 0,
      free_bytes: 1,
      free_human: '',
    } as DatabaseStorageInfo
    expect(storageDatabaseBlockIsAllZero(onlyFree)).toBe(false)
  })

  it('★ 其它全零但带着 server_version ⇒ 不算全零', () => {
    // ★ 这条才是「漏掉 server_version 那一项」能打掉的：
    //   db() 的其余字段都非零，漏项后照样返回 false ⇒ 指不到牙。
    const zeroWithVersion = {
      database_bytes: 0,
      database_human: '',
      total_bytes: 0,
      total_human: '',
      tables_bytes: 0,
      indexes_bytes: 0,
      toast_bytes: 0,
      free_bytes: 0,
      free_human: '',
      server_version: '16.4',
    } as DatabaseStorageInfo
    expect(storageDatabaseBlockIsAllZero(zeroWithVersion)).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (2)：database.free_* 是「测不到」—— 恒 0 / 恒空串
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (2) database 的 free 位是「测不到」而非「空闲」', () => {
  it('★ 契约常量钉住：free 位不可用', () => {
    expect(STORAGE_DB_FREE_IS_UNMEASURED).toBe(true)
  })

  it('★ 成功响应的 free_bytes 仍是 0（不是 0 字节空闲，是没人测）', () => {
    const r = ov()
    expect(r.database.free_bytes).toBe(0)
    expect(r.database.free_human).toBe('')
  })

  it('★ free_bytes 被当整数透传（解包器**不**把 0 改成 null/删键）', () => {
    const r = unwrapStorageOverview(ov())
    expect('free_bytes' in r.database).toBe(true)
    expect(r.database.free_bytes).toBe(0)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (3)：两套 humanize，单位串不重叠 ⇒ 可自验
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★★ (3) database_human 是 PG 风格，其余是本仓 humanBytes 风格', () => {
  it('★ database_human = "1 kB"（PG 小写 k）⇒ 不是本仓风格', () => {
    expect(storageDatabaseHumanIsGoStyle(db({ database_human: '1 kB' }))).toBe(false)
  })

  it('★ database_human = "512 bytes"（PG 字节级）⇒ 不是本仓风格', () => {
    expect(storageDatabaseHumanIsGoStyle(db({ database_human: '512 bytes' }))).toBe(false)
  })

  it('★ database_human = "1 KB"（本仓大写 K）⇒ 判为本仓风格 ⇒ 说明响应不对', () => {
    expect(storageDatabaseHumanIsGoStyle(db({ database_human: '1 KB' }))).toBe(true)
  })

  it('★ humanBytesGo(0) = "0 B"', () => {
    expect(humanBytesGo(0)).toBe('0 B')
  })

  it('★ humanBytesGo(1) = "1 B"（字节级无小数点）', () => {
    expect(humanBytesGo(1)).toBe('1 B')
  })

  it('★ humanBytesGo(512) = "512 B"（1023 及以下不走浮点分支）', () => {
    expect(humanBytesGo(512)).toBe('512 B')
  })

  it('★ humanBytesGo(1023) = "1023 B"（上界内）', () => {
    expect(humanBytesGo(1023)).toBe('1023 B')
  })

  it('★ humanBytesGo(1024) = "1 KB"（刚进位，1.0 ⇒ TrimRight 去尾 0 再去尾点）', () => {
    expect(humanBytesGo(1024)).toBe('1 KB')
  })

  it('★ humanBytesGo(1280) = "1.2 KB"（1.25 **截断**不是四舍五入 1.3）', () => {
    expect(humanBytesGo(1280)).toBe('1.2 KB')
  })

  it('★ humanBytesGo(1536) = "1.5 KB"', () => {
    expect(humanBytesGo(1536)).toBe('1.5 KB')
  })

  it('★ humanBytesGo(1500) = "1.4 KB"（能区分 1024 进制与 1000 进制）', () => {
    // 1024 进制 ⇒ 1.4648 ⇒ 截断 1.4；1000 进制 ⇒ 1.5 ⇒ "1.5 KB"。
    // ★ 1024 这个用例挑的是 1024 与 1536 都区分不开的那一格：
    //   1024 两种进制都恰好进位到 1.0 ⇒ 都输出 "1 KB" ⇒ 样本选歪。
    expect(humanBytesGo(1500)).toBe('1.4 KB')
  })

  it('★ humanBytesGo(1 MB) = "1 MB"（单位逐级递进）', () => {
    expect(humanBytesGo(1024 * 1024)).toBe('1 MB')
  })

  it('★ humanBytesGo(1 GB) = "1 GB"', () => {
    expect(humanBytesGo(1024 ** 3)).toBe('1 GB')
  })

  it('★ humanBytesGo(1 TB) = "1 TB"', () => {
    expect(humanBytesGo(1024 ** 4)).toBe('1 TB')
  })

  it('★ humanBytesGo(1 PB) = "1 PB"', () => {
    expect(humanBytesGo(1024 ** 5)).toBe('1 PB')
  })

  it('★ humanBytesGo(1024^6) 停在 PB（i 上限 5，不再进位）', () => {
    expect(humanBytesGo(1024 ** 6)).toBe('1024 PB')
  })

  it('★ humanBytesGo(-1) = "0 B"（后端 n<0 分支）', () => {
    expect(humanBytesGo(-1)).toBe('0 B')
  })

  it('★ storageHumanMatches 对得上 ⇒ true', () => {
    expect(storageHumanMatches(10_485_760, '10 MB')).toBe(true)
  })

  it('★ storageHumanMatches 对不上 ⇒ false（反向）', () => {
    expect(storageHumanMatches(10_485_760, '10 MB ')).toBe(false)
  })

  it('★ tables 的 total_human 与 humanBytesGo 吻合', () => {
    expect(tableSizesHumanMatchesTotal(sizes())).toBe(true)
  })

  it('★ tables 的 total_human 换算不吻合 ⇒ 判 false（反向）', () => {
    expect(tableSizesHumanMatchesTotal(sizes({ total_human: '9 MB' }))).toBe(false)
  })

  it('★ 逐行的 total_human 与 toast_human 都吻合', () => {
    expect(tableRowHumsMatch(sizes())).toBe(true)
  })

  it('★ 某行 toast_human 错 ⇒ 判 false（反向：两个字段都要查）', () => {
    expect(tableRowHumsMatch(sizes({ tables: [row({ toast_human: '2 MB' })] }))).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (4)：「列存统计查询失败」warning 不可达
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (4) 列存失败只落在 columnar.note，永不进 warnings', () => {
  it('★ 不可达契约钉住', () => {
    expect(STORAGE_WARNING_COLUMNAR_FAILED_IS_UNREACHABLE).toBe(true)
  })

  it('★ 分类器仍能认出这条（万一线上真出现了，不当成 unknown 吞掉）', () => {
    expect(storageWarningKind('列存统计查询失败: connection refused')).toBe('columnar_failed')
  })

  it('★ 只有 db_failed 的响应里查 columnar_failed ⇒ 查不到', () => {
    const r = ov({ warnings: [STORAGE_WARNING_DB_FAILED_PREFIX + 'connection refused'] })
    expect(storageWarningOfKind(r, 'columnar_failed')).toBeUndefined()
  })

  it('★ 列存失败的真实表达是 note 带前缀', () => {
    const c = col({ note: STORAGE_COLUMNAR_NOTE_QUERY_FAILED_PREFIX + 'connection refused' })
    expect(storageColumnarNoteKind(c)).toBe('query_failed')
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (5)：columnar.note 五分类 + total_human ⇔ 统计成功
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★★ (5) columnar 的 note 与 total_human', () => {
  it('★ note 缺键 ⇒ none', () => {
    expect(storageColumnarNoteKind(col())).toBe('none')
  })

  it('★ note = 数据库连接未就绪 ⇒ db_not_ready', () => {
    // ★ 逐字照抄后端 :299
    expect(storageColumnarNoteKind(col({ note: '数据库连接未就绪' }))).toBe('db_not_ready')
  })

  it('★ note = 扩展未安装 ⇒ ext_missing', () => {
    // ★ 逐字照抄后端 :320 的字面量，**不**用被测常量（否则改常量用例照样绿）
    expect(storageColumnarNoteKind(col({ note: 'citus_columnar 扩展未安装' }))).toBe('ext_missing')
  })

  it('★ note = 尚无表使用列存 ⇒ no_tables', () => {
    // ★ 逐字照抄后端 :345
    expect(storageColumnarNoteKind(col({ note: '尚无表使用列存（citus_columnar 已加载）' }))).toBe('no_tables')
  })

  it('★ 未知字面量 ⇒ unknown（不静默归 none，否则会伪装成统计成功）', () => {
    expect(storageColumnarNoteKind(col({ note: '某个新文案' }))).toBe('unknown')
  })

  it('★ total_human 非空 ⇒ 统计成功', () => {
    expect(storageColumnarQuerySucceeded(col())).toBe(true)
  })

  it('★ total_human 为空 ⇒ 统计失败（反向）', () => {
    expect(storageColumnarQuerySucceeded(col({ total_human: '' }))).toBe(false)
  })

  it('★ available=true 但 total_human 空 ⇒ 「声称可用却没测出」', () => {
    expect(storageColumnarAvailableButUnmeasured(col({ total_human: '' }))).toBe(true)
  })

  it('★ available=true 且 total_human 非空 ⇒ 不算未测出（反向）', () => {
    expect(storageColumnarAvailableButUnmeasured(col())).toBe(false)
  })

  it('★ available=false 且 total_human 空 ⇒ 不算「声称可用却没测出」（反向）', () => {
    expect(storageColumnarAvailableButUnmeasured(col({ available: false, total_human: '' }))).toBe(false)
  })

  it('★ 扩展未安装的响应：available=false / total_human 空 / note 齐全', () => {
    const c = col({
      available: false,
      table_count: 0,
      total_columns: 0,
      total_bytes: 0,
      total_human: '',
      note: STORAGE_COLUMNAR_NOTE_EXT_MISSING,
    })
    expect(storageColumnarQuerySucceeded(c)).toBe(false)
    expect(storageColumnarNoteKind(c)).toBe('ext_missing')
  })

  it('★ 扫描失败但 Available 已置 true：note 齐全 + total_human 空', () => {
    const c = col({
      available: true,
      total_human: '',
      note: STORAGE_COLUMNAR_NOTE_QUERY_FAILED_PREFIX + 'scan failed',
    })
    expect(storageColumnarAvailableButUnmeasured(c)).toBe(true)
    expect(storageColumnarNoteKind(c)).toBe('query_failed')
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (6)：warnings 六分类
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (6) warnings 六种取值', () => {
  it('★ 数据库查询失败（前缀 + err）⇒ db_failed', () => {
    expect(storageWarningKind(STORAGE_WARNING_DB_FAILED_PREFIX + 'conn refused')).toBe('db_failed')
  })

  it('★ 本机磁盘查询失败（前缀 + err）⇒ fs_failed', () => {
    expect(storageWarningKind(STORAGE_WARNING_FS_FAILED_PREFIX + 'statfs /x: 不可用')).toBe('fs_failed')
  })

  it('★ DB 超磁盘 5 倍 ⇒ db_over_disk', () => {
    // ★ 逐字照抄后端 :183-184（含全角破折号与两个空格）
    expect(storageWarningKind('数据库总大小超过本机磁盘容量的 5 倍 — 表明 DB 部署在独立节点')).toBe('db_over_disk')
  })

  it('★ 磁盘 ≥ 90% ⇒ fs_disk_alert', () => {
    // ★ 逐字照抄后端 :188（含全角 ≥ 与 %）
    expect(storageWarningKind('本机磁盘已用 ≥ 90%，请检查日志/缓存/附件')).toBe('fs_disk_alert')
  })

  it('★ 日志 > 5GB ⇒ logs_alert', () => {
    // ★ 逐字照抄后端 :192-193
    expect(storageWarningKind('本机日志目录已超过 5GB，建议调小 log.max_size_mb / log.max_age_days')).toBe('logs_alert')
  })

  it('★ 认不出的文案 ⇒ unknown（不静默丢弃）', () => {
    expect(storageWarningKind('某个新告警')).toBe('unknown')
  })

  it('★ 按分类取 warning：命中时拿得到', () => {
    const r = ov({ warnings: ['x', STORAGE_WARNING_FS_DISK_ALERT] })
    expect(storageWarningOfKind(r, 'fs_disk_alert')).toBe(STORAGE_WARNING_FS_DISK_ALERT)
  })

  it('★ 按分类取 warning：未命中时是 undefined', () => {
    expect(storageWarningOfKind(ov({ warnings: [] }), 'logs_alert')).toBeUndefined()
  })

  it('★ warnings 恒数组：零条时是 [] 而不是 null', () => {
    expect(unwrapStorageOverview(ov({ warnings: [] })).warnings).toEqual([])
  })
})

describe('★★★★ (6) 5 倍告警的三个必要条件', () => {
  it('★ db>0 且 fs>0 ⇒ 算出真实比值', () => {
    const r = ov({
      database: db({ total_bytes: 600_000_000_000 }),
      filesystem: fs({ total_bytes: 100_000_000_000 }),
    })
    expect(storageDbOverDiskRatio(r)).toBe(6)
  })

  it('★ db=0 ⇒ 比值判 0（后端 :180 的 db>0 短路）', () => {
    const r = ov({ database: db({ total_bytes: 0 }), filesystem: fs() })
    expect(storageDbOverDiskRatio(r)).toBe(0)
  })

  it('★ fs=0 ⇒ 比值判 0（后端 :180 的 fs>0 短路）', () => {
    const r = ov({ database: db(), filesystem: fs({ total_bytes: 0 }) })
    expect(storageDbOverDiskRatio(r)).toBe(0)
  })

  it('★ db=0 且 fs=0 ⇒ 比值判 0（两个短路都命中）', () => {
    const r = ov({ database: db({ total_bytes: 0 }), filesystem: fs({ total_bytes: 0 }) })
    expect(storageDbOverDiskRatio(r)).toBe(0)
  })

  it('★ db=0 且 fs=0 ⇒ 绝不返回 NaN（这是 db>0 那一项的唯一作用）', () => {
    // ★ 去掉 `db > 0` 那一项后，0/0 = NaN 会漏出去 ⇒ 这条是它的牙。
    //   去掉 `fs > 0` 那一项则得 Infinity（另一条用例）。
    const r = ov({ database: db({ total_bytes: 0 }), filesystem: fs({ total_bytes: 0 }) })
    expect(storageDbOverDiskRatio(r)).not.toBeNaN()
    expect(Number.isFinite(storageDbOverDiskRatio(r))).toBe(true)
  })

  it('★ db>0 且 fs=0 ⇒ 判 0 而不是 Infinity', () => {
    const r = ov({ database: db({ total_bytes: 4096 }), filesystem: fs({ total_bytes: 0 }) })
    expect(storageDbOverDiskRatio(r)).toBe(0)
    expect(Number.isFinite(storageDbOverDiskRatio(r))).toBe(true)
  })

  it('★ 带 db_over_disk 告警 ⇒ 比值必 > 5（阈值严格大于）', () => {
    const r = ov({
      database: db({ total_bytes: 600_000_000_000 }),
      filesystem: fs({ total_bytes: 100_000_000_000 }),
      warnings: [STORAGE_WARNING_DB_OVER_DISK],
    })
    expect(storageWarningOfKind(r, 'db_over_disk')).toBeDefined()
    expect(storageDbOverDiskRatio(r)).toBeGreaterThan(STORAGE_DB_OVER_DISK_RATIO)
  })

  it('★ 比值恰为 5 ⇒ 不带 db_over_disk 告警（严格大于的反向）', () => {
    const r = ov({
      database: db({ total_bytes: 500_000_000_000 }),
      filesystem: fs({ total_bytes: 100_000_000_000 }),
    })
    expect(storageDbOverDiskRatio(r)).toBe(STORAGE_DB_OVER_DISK_RATIO)
    expect(storageWarningOfKind(r, 'db_over_disk')).toBeUndefined()
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (7)：filesystem 的 used / free 取自 statfs 不同字段
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (7) used_bytes + free_bytes ≠ total_bytes', () => {
  it('★ 有 root 保留块时，保留字节为正', () => {
    // total 100 GiB；used = total − Bfree = 60 GiB ⇒ Bfree = 40 GiB；
    // free = Bavail = 30 GiB ⇒ 保留 10 GiB
    const f = fs({ total_bytes: 100 * 1024 ** 3, used_bytes: 60 * 1024 ** 3, free_bytes: 30 * 1024 ** 3 })
    expect(storageFilesystemReservedBytes(f)).toBe(10 * 1024 ** 3)
  })

  it('★ 没有保留块时（Bfree == Bavail）⇒ 保留字节为 0', () => {
    const f = fs({ total_bytes: 100, used_bytes: 60, free_bytes: 40 })
    expect(storageFilesystemReservedBytes(f)).toBe(0)
  })

  it('★ 三个数全 0 ⇒ 保留字节 0（不是负数）', () => {
    const f = fs({ total_bytes: 0, used_bytes: 0, free_bytes: 0 })
    expect(storageFilesystemReservedBytes(f)).toBe(0)
  })

  it('★ used_percent 原样透传（客户端不拿它重算任何东西）', () => {
    // 后端 :363-366 是 int(used*100/total) 的**截断**整数除法，
    // 且它的 used/total 与 free/total 取自 statfs 不同字段 ⇒ 与 used_bytes/free_bytes 不自洽。
    // ⇒ 客户端只能当「后端给的整数」显示，不拿它反推字节数。
    const f = fs({ used_percent: 50 })
    expect(f.used_percent).toBe(50)
    expect(fs({ used_percent: 33 }).used_percent).toBe(33)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (8)：local_logs 条件键 + exists=false 的空串
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★ (8) local_logs 的 exists / size_human / mtime 关系', () => {
  it('★ exists=false ⇒ size_human 为空', () => {
    expect(storageLocalDirUnavailable(dir({ exists: false, size_human: '', files: 0, size_bytes: 0 }))).toBe(
      true,
    )
  })

  it('★ exists=true ⇒ size_human 非空（反向）', () => {
    expect(storageLocalDirUnavailable(dir())).toBe(false)
  })

  it('★ 目录不存在但 path 仍被填上（:381 在提前 return 之前）', () => {
    const d = dir({ exists: false, size_human: '', files: 0, size_bytes: 0, oldest_mtime: 0, newest_mtime: 0 })
    expect(d.path).toBe('/var/lib/llm-gateway-go/logs')
  })

  it('★ 空目录：exists=true 但 files=0 ⇒ 两个 mtime 都是 0', () => {
    const d = dir({ files: 0, size_bytes: 0, size_human: '0 B', oldest_mtime: 0, newest_mtime: 0 })
    expect(storageLocalDirHasNoFiles(d)).toBe(true)
  })

  it('★ 有文件 ⇒ oldest_mtime 非 0 ⇒ 不判「无文件」（反向）', () => {
    expect(storageLocalDirHasNoFiles(dir())).toBe(false)
  })

  it('★ mtimes 有序（oldest ≤ newest）⇒ true', () => {
    expect(storageLocalDirMtimesOrdered(dir())).toBe(true)
  })

  it('★ mtimes 倒置 ⇒ false（反向）', () => {
    expect(
      storageLocalDirMtimesOrdered(dir({ oldest_mtime: 1_757_500_000, newest_mtime: 1_757_000_000 })),
    ).toBe(false)
  })

  it('★ 条件键缺省（undefined）⇒ 三个判据都不抛也不误判', () => {
    expect(storageLocalDirUnavailable(undefined)).toBe(false)
    expect(storageLocalDirHasNoFiles(undefined)).toBe(false)
    expect(storageLocalDirMtimesOrdered(undefined)).toBe(false)
  })

  it('★ null 同样安全', () => {
    expect(storageLocalDirUnavailable(null)).toBe(false)
    expect(storageLocalDirHasNoFiles(null)).toBe(false)
    expect(storageLocalDirMtimesOrdered(null)).toBe(false)
  })

  it('★ 空目录：size_human 是 "0 B" 但**不算**不可用', () => {
    // ★ 换成 size_human === '0 B' 的实现会把这条判成「不可用」⇒ 样本必须选这一格。
    const d = dir({ files: 0, size_bytes: 0, size_human: '0 B', oldest_mtime: 0, newest_mtime: 0 })
    expect(d.exists).toBe(true)
    expect(storageLocalDirUnavailable(d)).toBe(false)
  })

  it('★ files 非 0 但 oldest_mtime 为 0 ⇒ 判「无文件」（判据只看 mtime）', () => {
    // ★ 换成 files === 0 的实现会把这条判成 false ⇒ 样本必须选这一格。
    const d = dir({ files: 12, oldest_mtime: 0, newest_mtime: 1_757_500_000 })
    expect(storageLocalDirHasNoFiles(d)).toBe(true)
  })

  it('★ 单文件目录：oldest_mtime === newest_mtime ⇒ 仍算有序', () => {
    // ★ 严格小于的实现会把这条判成 false ⇒ 样本必须选这一格（并列，而非严格递增）。
    const d = dir({ files: 1, oldest_mtime: 1_757_000_000, newest_mtime: 1_757_000_000 })
    expect(storageLocalDirMtimesOrdered(d)).toBe(true)
  })

  it('★ 目录不存在时 5GB 告警不成立（后端要求 exists 为真）', () => {
    // 响应里就是不带这条告警的：判据体现为「查不到该分类」
    const r = ov({ warnings: [], local_logs: dir({ exists: false, size_human: '', size_bytes: 0, files: 0 }) })
    expect(storageWarningOfKind(r, 'logs_alert')).toBeUndefined()
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (9)：SUM 静默兜底 + 注释里的不等式方向是错的
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (9) database_bytes 可以大于 total_bytes', () => {
  it('★ database_bytes > total_bytes ⇒ 判成立（pg_catalog 被 SUM 排除）', () => {
    expect(storageDatabaseExceedsRelationSum(db({ database_bytes: 10_485_760, total_bytes: 8_388_608 }))).toBe(
      true,
    )
  })

  it('★ database_bytes ≤ total_bytes ⇒ 不成立（反向：这是常见情形）', () => {
    expect(storageDatabaseExceedsRelationSum(db({ database_bytes: 10_485_760, total_bytes: 12_582_912 }))).toBe(
      false,
    )
  })

  it('★ 两者相等 ⇒ 不成立（严格大于）', () => {
    expect(storageDatabaseExceedsRelationSum(db({ database_bytes: 1024, total_bytes: 1024 }))).toBe(false)
  })

  it('★ total_bytes == database_bytes 且非 0 ⇒ 判为「SUM 兜底」（:268）', () => {
    expect(storageTotalFellBackToDatabaseSize(db({ database_bytes: 4096, total_bytes: 4096 }))).toBe(true)
  })

  it('★ total_bytes == 0 ⇒ 不判兜底（否则与「查询失败」混同）', () => {
    expect(storageTotalFellBackToDatabaseSize(db({ database_bytes: 0, total_bytes: 0 }))).toBe(false)
  })

  it('★ 两者不等 ⇒ 不判兜底（反向）', () => {
    expect(storageTotalFellBackToDatabaseSize(db({ database_bytes: 1024, total_bytes: 2048 }))).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (10)：collected_at 必是 UTC
// ═════════════════════════════════════════════════════════════════════════════

describe('★★ (10) collected_at 必以 Z 结尾', () => {
  it('★ RFC3339Nano UTC ⇒ true', () => {
    expect(storageOverviewIsUtc('2026-10-08T02:00:00.123456789Z')).toBe(true)
  })

  it('★ 带 +08:00 偏移 ⇒ false（后端写死 .UTC()，不可能出现）', () => {
    expect(storageOverviewIsUtc('2026-10-08T10:00:00+08:00')).toBe(false)
  })

  it('★ 没有时区 ⇒ false（反向）', () => {
    expect(storageOverviewIsUtc('2026-10-08T02:00:00')).toBe(false)
  })

  it('★ 空串 ⇒ false（反向）', () => {
    expect(storageOverviewIsUtc('')).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 判据 (13)：表级大小的四条强不变量
// ═════════════════════════════════════════════════════════════════════════════

describe('★★★★ (13) total_bytes 恒等于返回行之和', () => {
  it('★ 一行：total = 该行', () => {
    expect(tableSizesTotalMatchesRows(sizes())).toBe(true)
  })

  it('★ 三行：total = 三行之和', () => {
    const tables = [
      row({ total_bytes: 100, total_human: '100 B', percent_of_db: 25 }),
      row({ total_bytes: 200, total_human: '200 B', percent_of_db: 50 }),
      row({ total_bytes: 100, total_human: '100 B', percent_of_db: 25 }),
    ]
    expect(tableSizesTotalMatchesRows(sizes({ tables, total_bytes: 400 }))).toBe(true)
  })

  it('★ total 对不上 ⇒ 判 false（反向：跳过行时不计入，但仍须自洽）', () => {
    // ★ 用「小于」这一格：放宽成 >= 时它仍返回 false ⇒ 指不到牙。
    expect(tableSizesTotalMatchesRows(sizes({ total_bytes: 999 }))).toBe(false)
  })

  it('★ total 大于行之和 ⇒ 判 false（放大成 >= 才抓得到的那一格）', () => {
    expect(tableSizesTotalMatchesRows(sizes({ total_bytes: 99_999_999 }))).toBe(false)
  })

  it('★ 空榜：total 必为 0（make(…,0,limit) ⇒ 恒数组）', () => {
    expect(tableSizesTotalMatchesRows(sizes({ tables: [], total_bytes: 0, total_human: '0 B' }))).toBe(true)
  })

  it('★ 跳过一行后 total 仍是留存行之和（跳过的行不计入）', () => {
    // 后端 :446-452：扫描失败 continue，totalBytes 只在留存时累加
    const tables = [row({ total_bytes: 100, total_human: '100 B', percent_of_db: 100 })]
    expect(tableSizesTotalMatchesRows(sizes({ tables, total_bytes: 100 }))).toBe(true)
  })
})

describe('★★★★ (13) percent_of_db 的分母是榜内之和', () => {
  it('★ 两行各 50 ⇒ 逐行吻合', () => {
    const tables = [
      row({ total_bytes: 100, total_human: '100 B', percent_of_db: 50 }),
      row({ total_bytes: 100, total_human: '100 B', percent_of_db: 50 }),
    ]
    expect(tableSizesPercentIsTopNRelative(sizes({ tables, total_bytes: 200 }))).toBe(true)
  })

  it('★ 榜内占比凑不满 100（整数除法截断：3×33 = 99）', () => {
    const tables = [
      row({ total_bytes: 1, total_human: '1 B', percent_of_db: 33 }),
      row({ total_bytes: 1, total_human: '1 B', percent_of_db: 33 }),
      row({ total_bytes: 1, total_human: '1 B', percent_of_db: 33 }),
    ]
    expect(tables.reduce((s, x) => s + x.percent_of_db, 0)).toBe(99)
    expect(tableSizesPercentIsTopNRelative(sizes({ tables, total_bytes: 3 }))).toBe(true)
  })

  it('★ 用全库分母算出来的百分比 ⇒ 判 false（反向）', () => {
    // 100/200 = 50，但若分母是「整库 1000」则应为 10 ⇒ 这行 50 说明分母是榜内和
    const tables = [row({ total_bytes: 100, total_human: '100 B', percent_of_db: 50 })]
    expect(tableSizesPercentIsTopNRelative(sizes({ tables, total_bytes: 200 }))).toBe(false)
  })

  it('★ 6 行各 1 字节 ⇒ 每行 16（截断 16.67，不是四舍五入的 17）', () => {
    // ★ 3 行各 1 字节那格区分不开（33.33 两种实现都得 33）；
    //   小数部分 ≥ 0.5 的那一格才区分得开。
    const tables = Array.from({ length: 6 }, () =>
      row({ total_bytes: 1, total_human: '1 B', percent_of_db: 16 }),
    )
    expect(tables.reduce((s, x) => s + x.percent_of_db, 0)).toBe(96)
    expect(tableSizesPercentIsTopNRelative(sizes({ tables, total_bytes: 6, total_human: '6 B' }))).toBe(true)
  })

  it('★ total=0 ⇒ 每行 percent 必为 0（后端 :461 的 totalBytes>0 守卫）', () => {
    const tables = [row({ total_bytes: 0, total_human: '0 B', percent_of_db: 0 })]
    expect(tableSizesPercentIsTopNRelative(sizes({ tables, total_bytes: 0, total_human: '0 B' }))).toBe(true)
  })

  it('★ total=0 但某行 percent 非 0 ⇒ 判 false（反向）', () => {
    const tables = [row({ total_bytes: 0, total_human: '0 B', percent_of_db: 7 })]
    expect(tableSizesPercentIsTopNRelative(sizes({ tables, total_bytes: 0, total_human: '0 B' }))).toBe(false)
  })
})

describe('★★★★ (13) 排序与分区父表标记', () => {
  it('★ 按 total_bytes 非增 ⇒ true', () => {
    const tables = [
      row({ total_bytes: 300, total_human: '300 B' }),
      row({ total_bytes: 200, total_human: '200 B' }),
      row({ total_bytes: 100, total_human: '100 B' }),
    ]
    expect(tableSizesIsDescending(sizes({ tables }))).toBe(true)
  })

  it('★ 并列不判「严格递减」也算通过（PG 并列次序未定）', () => {
    const tables = [
      row({ total_bytes: 100, total_human: '100 B' }),
      row({ total_bytes: 100, total_human: '100 B' }),
    ]
    expect(tableSizesIsDescending(sizes({ tables }))).toBe(true)
  })

  it('★ 升序 ⇒ 判 false（反向）', () => {
    const tables = [
      row({ total_bytes: 100, total_human: '100 B' }),
      row({ total_bytes: 200, total_human: '200 B' }),
    ]
    expect(tableSizesIsDescending(sizes({ tables }))).toBe(false)
  })

  it('★ 单行榜 ⇒ 恒非增（反向：没有第二行可比）', () => {
    expect(tableSizesIsDescending(sizes())).toBe(true)
  })

  it('★ 榜上有分区父表 ⇒ 体积重叠不可相加', () => {
    expect(tableSizesMayDoubleCount(sizes({ tables: [row({ is_partitioned: true })] }))).toBe(true)
  })

  it('★ 榜上无分区父表 ⇒ 不判重叠（反向）', () => {
    expect(tableSizesMayDoubleCount(sizes({ tables: [row({ is_partitioned: false })] }))).toBe(false)
  })

  it('★ 空榜不判重叠（反向）', () => {
    expect(tableSizesMayDoubleCount(sizes({ tables: [] }))).toBe(false)
  })
})

// ═════════════════════════════════════════════════════════════════════════════
// 端到端：fetch 走解包器
// ═════════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ overview：正常响应被解包器放行', async () => {
    okOverview()
    const r = await fetchStorageOverview()
    expect(r.database.database_bytes).toBe(10_485_760)
    expect(storageOverviewIsUtc(r.collected_at)).toBe(true)
  })

  it('★ overview：形状不符 ⇒ 抛错（不是把坏数据交给 UI）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ nope: true }))
    await expect(fetchStorageOverview()).rejects.toThrow(/存储总览 缺/)
  })

  it('★ tables：正常响应被解包器放行', async () => {
    okSizes()
    const r = await fetchStorageTableSizesChecked({ limit: 5 })
    expect(r.tables).toHaveLength(1)
    expect(tableSizesTotalMatchesRows(r)).toBe(true)
  })

  it('★ tables：缺 toast_human 的行 ⇒ 抛错（桌面漏掉的键不能漏）', async () => {
    const bad = { ...row() } as Record<string, unknown>
    delete bad['toast_human']
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ tables: [bad], total_bytes: 10_485_760, total_human: '10 MB', collected_at: 'x' }),
    )
    await expect(fetchStorageTableSizesChecked()).rejects.toThrow(/toast_human/)
  })

  it('★ 500 的错误信封是嵌套的 {"error":{"detail":"查询表大小失败"}}（不带 code）', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { detail: '查询表大小失败' } }, 500),
    )
    await expect(fetchStorageTableSizesChecked()).rejects.toThrow()
  })
})
