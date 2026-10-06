import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { req } from '@/api/client'
import {
  fetchLogStats,
  unwrapLogStats,
  logStatsNotEnabled,
  logStatsDirMissing,
  logStatsEmpty,
  logStatsTimeRange,
  logStatsArchiveRatio,
  fetchLogFiles,
  unwrapLogFiles,
  logFilesNotEnabled,
  logFilesEmpty,
  logFilesTotalDisagrees,
  fetchBodyCacheStats,
  unwrapBodyCacheStats,
  bodyCacheRateMeaningless,
  bodyCacheSaturated,
  LOG_STATS_KEYS,
  LOG_FILES_KEYS,
  LOG_FILE_INFO_KEYS,
  BODY_CACHE_STATS_KEYS,
} from '@/api/logOps'

/**
 * logOps API 的不变量（2026-10-08，第六十二批）。
 *
 * ★ 本族最该被钉住的：
 *   ① **同前缀两种权限档**（三条 admin 档 / config·archive·cleanup superAdmin 档）
 *      ⇒ 按前缀判权限会判错。
 *   ② **`LogFileInfoExt` 内嵌结构**（log_management.go:85-88）
 *      ⇒ Go json 编码**扁平化**，没有 `log_file_info` 嵌套层。
 *   ③ **三种「什么都没有」长得不一样**：未启用 / 目录不存在 / 目录里没文件。
 *   ④ **`disk_usage_pct` 与 `hit_rate` 的 0 是 Go 零值**，
 *      与「真的 0」不可分 ⇒ 靠兄弟键判，不能直接下结论。
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

/** admin/log_management.go:65-76，目录存在且有文件。 */
const STATS_NORMAL = {
  log_dir: '/var/log/llmgw',
  exists: true,
  total_files: 12,
  total_size_bytes: 1048576,
  total_size_human: '1.0 MB',
  archive_files: 3,
  archive_size: 262144,
  oldest_mtime: '2026-10-01T00:00:00Z',
  newest_mtime: '2026-10-08T00:00:00Z',
  disk_usage_pct: 12.5,
}

/** ★ `cur.File == ""` ⇒ 零值直接下发（:326-329），log_dir 与 exists 都是零值。 */
const STATS_NOT_ENABLED = {
  log_dir: '',
  exists: false,
  total_files: 0,
  total_size_bytes: 0,
  total_size_human: '',
  archive_files: 0,
  archive_size: 0,
  oldest_mtime: null,
  newest_mtime: null,
  disk_usage_pct: 0,
}

/** ★ 配了路径但目录不存在（:332-335）：log_dir 有值、exists=false。 */
const STATS_DIR_MISSING = { ...STATS_NOT_ENABLED, log_dir: '/var/log/llmgw' }

/** ★ 目录在但一个文件都没有 ⇒ 时间指针是 null。 */
const STATS_EMPTY = { ...STATS_DIR_MISSING, exists: true, total_size_human: '0 B' }

/** internal/logging/logging.go:399-406 + 外层 size_human（扁平化后平级）。 */
const FILES_NORMAL = {
  files: [
    {
      name: 'gateway.log',
      size_bytes: 2048,
      mod_time: '2026-10-08T02:00:00Z',
      is_current: true,
      is_compressed: false,
      is_archived: false,
      size_human: '2.0 KB',
    },
    {
      name: 'gateway.2026-10-01.log.gz',
      size_bytes: 1024,
      mod_time: '2026-10-01T02:00:00Z',
      is_current: false,
      is_compressed: true,
      is_archived: true,
      size_human: '1.0 KB',
    },
  ],
  total: 2,
  dir: '/var/log/llmgw',
}

/** ★ `ListFiles` 未启用时返空切片 + nil 错误，`dir` 也是空串。 */
const FILES_NOT_ENABLED = { files: [], total: 0, dir: '' }

const BODY_CACHE_NORMAL = { size: 142, hits: 1023, misses: 287, evictions: 5, hit_rate: 0.781, cap: 1024 }

/** ★ 分母为 0 ⇒ hit_rate 是 Go 零值 0，与「真的 0%」不可分。 */
const BODY_CACHE_NO_SAMPLE = { size: 0, hits: 0, misses: 0, evictions: 0, hit_rate: 0, cap: 1024 }

/* ═══════════════════════════════════════════════════════════════════════
 * A. logs/stats
 * ═══════════════════════════════════════════════════════════════════════ */

describe('logOps logs/stats（admin/log_management.go:1116）', () => {
  it('正常载荷逐键解包通过', () => {
    const r = unwrapLogStats(STATS_NORMAL)
    expect(r.log_dir).toBe('/var/log/llmgw')
    expect(r.total_files).toBe(12)
    expect(r.disk_usage_pct).toBeCloseTo(12.5)
  })

  it('走裸 JSON 路径', async () => {
    reqMock.mockResolvedValue(STATS_NORMAL)
    await fetchLogStats()
    expect(urlOf()).toBe('/api/admin/logs/stats')
  })

  it('★ 缺任一键 ⇒ 抛错', () => {
    const { disk_usage_pct, ...noPct } = STATS_NORMAL
    void disk_usage_pct
    expect(() => unwrapLogStats(noPct)).toThrow(/缺 1 个键（disk_usage_pct）/)
  })

  it('★★ 三种「什么都没有」必须彼此可分', () => {
    const notEnabled = unwrapLogStats(STATS_NOT_ENABLED)
    const dirMissing = unwrapLogStats(STATS_DIR_MISSING)
    const empty = unwrapLogStats(STATS_EMPTY)

    expect(logStatsNotEnabled(notEnabled)).toBe(true)
    expect(logStatsNotEnabled(dirMissing)).toBe(false)

    expect(logStatsDirMissing(dirMissing)).toBe(true)
    expect(logStatsDirMissing(notEnabled)).toBe(false)

    expect(logStatsEmpty(empty)).toBe(true)
    expect(logStatsEmpty(notEnabled)).toBe(false)
  })

  it('★ 正常的目录三者都不成立', () => {
    const r = unwrapLogStats(STATS_NORMAL)
    expect(logStatsNotEnabled(r)).toBe(false)
    expect(logStatsDirMissing(r)).toBe(false)
    expect(logStatsEmpty(r)).toBe(false)
  })

  it('★ 时间指针为 null 时时间范围算不出（返回 null 而不是「现在」）', () => {
    expect(logStatsTimeRange(unwrapLogStats(STATS_EMPTY))).toBeNull()
    expect(logStatsTimeRange(unwrapLogStats(STATS_NORMAL))).toEqual({
      from: '2026-10-01T00:00:00Z',
      to: '2026-10-08T00:00:00Z',
    })
  })

  it('★ 只有一端为 null 也算不出', () => {
    expect(logStatsTimeRange(unwrapLogStats({ ...STATS_NORMAL, newest_mtime: null }))).toBeNull()
    expect(logStatsTimeRange(unwrapLogStats({ ...STATS_NORMAL, oldest_mtime: null }))).toBeNull()
  })

  it('★ 归档占比：分母为 0 时返回 null（不是 0%）', () => {
    expect(logStatsArchiveRatio(unwrapLogStats(STATS_NORMAL))).toBeCloseTo(0.25)
    expect(logStatsArchiveRatio(unwrapLogStats(STATS_EMPTY))).toBeNull()
    expect(logStatsArchiveRatio(unwrapLogStats(STATS_NOT_ENABLED))).toBeNull()
  })

  it('★ disk_usage_pct 查失败时后端静默留 0 —— 类型是值不是指针', () => {
    // log_management.go:367-369 `if …; err == nil { resp.DiskUsagePct = pct }`
    // ⇒ 失败就是 0，与「真的是 0%」不可分。
    const r = unwrapLogStats({ ...STATS_NORMAL, disk_usage_pct: 0 })
    expect(r.disk_usage_pct).toBe(0)
    // 类型层面：不是 null
    expect(r.disk_usage_pct).not.toBeNull()
  })

  it('拿到 dashboardapi 信封 ⇒ 报错', () => {
    expect(() => unwrapLogStats({ success: true, data: STATS_NORMAL, timestamp: 'x' }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('数组载荷 ⇒ 抛错', () => {
    expect(() => unwrapLogStats([])).toThrow(/期望裸对象，实得 array/)
  })

  it('导出的必填键与 Go struct tag 一一对应（10 个）', () => {
    expect(LOG_STATS_KEYS.length).toBe(10)
    expect([...LOG_STATS_KEYS]).toContain('oldest_mtime')
    expect([...LOG_STATS_KEYS]).toContain('disk_usage_pct')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * B. logs/files
 * ═══════════════════════════════════════════════════════════════════════ */

describe('logOps logs/files（admin/log_management.go:1115）', () => {
  it('★ 内嵌结构被扁平化：六个内层字段与 size_human 平级', () => {
    const r = unwrapLogFiles(FILES_NORMAL)
    // ★ 没有 log_file_info 嵌套层
    expect(r.files[0]).not.toHaveProperty('log_file_info')
    expect(r.files[0]).toHaveProperty('name')
    expect(r.files[0]).toHaveProperty('size_human')
    expect(r.files[0]!.is_current).toBe(true)
    expect(r.files[1]!.is_archived).toBe(true)
    expect(r.files[1]!.is_compressed).toBe(true)
  })

  it('走裸 JSON 路径', async () => {
    reqMock.mockResolvedValue(FILES_NORMAL)
    await fetchLogFiles()
    expect(urlOf()).toBe('/api/admin/logs/files')
  })

  it('★★ 缺 size_human（写成了嵌套的 sizeHuman）⇒ 抛错', () => {
    const { size_human, ...noHuman } = FILES_NORMAL.files[0]!
    void size_human
    expect(() => unwrapLogFiles({ ...FILES_NORMAL, files: [noHuman] }))
      .toThrow(/files\[0\] 缺 1 个键（size_human）/)
  })

  it('★ 缺任一内层键 ⇒ 抛错', () => {
    const { is_archived, ...noArchived } = FILES_NORMAL.files[0]!
    void noArchived
    expect(() => unwrapLogFiles({ ...FILES_NORMAL, files: [noArchived] }))
      .toThrow(/files\[0\] 缺 1 个键（is_archived）/)
  })

  it('★★ 空列表要区分「未启用」与「启用了但没文件」', () => {
    const notEnabled = unwrapLogFiles(FILES_NOT_ENABLED)
    const empty = unwrapLogFiles({ files: [], total: 0, dir: '/var/log/llmgw' })
    expect(logFilesNotEnabled(notEnabled)).toBe(true)
    expect(logFilesEmpty(notEnabled)).toBe(false)
    expect(logFilesNotEnabled(empty)).toBe(false)
    expect(logFilesEmpty(empty)).toBe(true)
  })

  it('★ total 与 files.length 不一致 ⇒ 契约漂移', () => {
    expect(logFilesTotalDisagrees(unwrapLogFiles(FILES_NORMAL))).toBe(false)
    expect(logFilesTotalDisagrees(unwrapLogFiles({ ...FILES_NORMAL, total: 99 }))).toBe(true)
  })

  it('files 不是数组 ⇒ 抛错', () => {
    expect(() => unwrapLogFiles({ ...FILES_NORMAL, files: {} })).toThrow(/files 不是数组/)
  })

  it('files 元素为 null ⇒ 抛错', () => {
    expect(() => unwrapLogFiles({ ...FILES_NORMAL, files: [null] }))
      .toThrow(/files\[0\] 不是对象/)
  })

  it('导出的键清单完整', () => {
    expect([...LOG_FILES_KEYS]).toEqual(['files', 'total', 'dir'])
    expect(LOG_FILE_INFO_KEYS.length).toBe(7)
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * C. logs/body-cache-stats
 * ═══════════════════════════════════════════════════════════════════════ */

describe('logOps logs/body-cache-stats（admin/logs_body_cache.go:129）', () => {
  it('正常载荷解包通过', () => {
    const r = unwrapBodyCacheStats(BODY_CACHE_NORMAL)
    expect(r.hits).toBe(1023)
    expect(r.cap).toBe(1024)
    expect(r.hit_rate).toBeCloseTo(0.781)
  })

  it('走裸 JSON 路径', async () => {
    reqMock.mockResolvedValue(BODY_CACHE_NORMAL)
    await fetchBodyCacheStats()
    expect(urlOf()).toBe('/api/admin/logs/body-cache-stats')
  })

  it('★★ 分母为 0 ⇒ hit_rate 无意义（不是 0% 命中率）', () => {
    expect(bodyCacheRateMeaningless(unwrapBodyCacheStats(BODY_CACHE_NO_SAMPLE))).toBe(true)
    expect(bodyCacheRateMeaningless(unwrapBodyCacheStats(BODY_CACHE_NORMAL))).toBe(false)
  })

  it('★ 全是 miss 也不是「无意义」—— 分母非 0', () => {
    const allMiss = { ...BODY_CACHE_NORMAL, hits: 0, misses: 100, hit_rate: 0 }
    expect(bodyCacheRateMeaningless(unwrapBodyCacheStats(allMiss))).toBe(false)
  })

  it('★ 容量用满 ⇒ saturated', () => {
    expect(bodyCacheSaturated(unwrapBodyCacheStats(BODY_CACHE_NORMAL))).toBe(false)
    expect(bodyCacheSaturated(unwrapBodyCacheStats({ ...BODY_CACHE_NORMAL, size: 1024 }))).toBe(true)
  })

  it('cap 为 0 时不判饱和（不是「无限大」）', () => {
    expect(bodyCacheSaturated(unwrapBodyCacheStats({ ...BODY_CACHE_NORMAL, cap: 0, size: 0 }))).toBe(false)
  })

  it('缺任一键 ⇒ 抛错', () => {
    const { evictions, ...noEvict } = BODY_CACHE_NORMAL
    void noEvict
    expect(() => unwrapBodyCacheStats(noEvict)).toThrow(/缺 1 个键（evictions）/)
  })

  it('★ 缓存未初始化时后端返 503 —— 客户端必须让它以错误形态冒出来', async () => {
    reqMock.mockRejectedValue(Object.assign(new Error('body cache not initialized'), { status: 503 }))
    await expect(fetchBodyCacheStats()).rejects.toThrow('body cache not initialized')
  })

  it('拿到 dashboardapi 信封 ⇒ 报错', () => {
    expect(() => unwrapBodyCacheStats({ success: true, data: BODY_CACHE_NORMAL, timestamp: 'x' }))
      .toThrow(/dashboardapi 信封形状/)
  })

  it('导出的必填键是六个（手写 map[string]any，无 struct tag）', () => {
    expect([...BODY_CACHE_STATS_KEYS]).toEqual(['size', 'hits', 'misses', 'evictions', 'hit_rate', 'cap'])
  })
})
