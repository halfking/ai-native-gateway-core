import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchBodyCacheStats,
  unwrapBodyCacheStats,
  fetchLogFiles,
  unwrapLogFiles,
  fetchLogStats,
  unwrapLogStats,
  fetchLogArchiveList,
  unwrapLogArchiveList,
  bodyCacheHitRate,
  bodyCacheHasNoTraffic,
  bodyCacheIsGenuinelyZeroRate,
  bodyCacheHitRateMatches,
  bodyCacheSizeExceedsCap,
  bodyCacheEvictionsArePlausible,
  logStatsLoggingDisabled,
  logStatsDirMissing,
  logStatsOk,
  logStatsExistsIsAmbiguous,
  logStatsHasNoFiles,
  logStatsTotalIncludingArchive,
  logStatsDiskUsageIsAmbiguous,
  logArchiveListIsDisabled,
  logArchiveListDirMissing,
  logArchiveListOk,
  logArchiveListState,
  logArchiveListAmbiguousZero,
  humanBytesMatches,
  bodyCacheNotInitialised,
  BODY_CACHE_STAT_KEYS,
  LOG_STATS_KEYS,
  type BodyCacheStats,
  type LogFilesList,
  type LogStats,
  type LogArchiveList,
} from './logsAdmin'

/**
 * 日志管理读面的契约测试（2026-10-08）。
 *
 * 后端逐条对应：
 *   admin/handler.go:959,1115,1116,1119   四条只读全是 admin(...)
 *   admin/handler.go:1114,1117,1118        config/archive/cleanup 是 superAdmin（不碰）
 *   admin/log_management.go:293-315        files
 *   admin/log_management.go:320-368        stats（三态同一形状）
 *   admin/log_management.go:567-615        archive/list（**异形端点**，dir/exists 条件存在）
 *   admin/logs_body_cache.go:123-146       body-cache-stats
 *   internal/logging/logging.go:399-406    LogFileInfo（**IsArchived 硬编码 false**）
 *   internal/logging/logging.go:424-455    scanLogDir（跳过目录 ⇒ 列表不含归档）
 *   admin/data_lifecycle_storage.go:502    humanBytes
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

/** 抄自 `admin/logs_body_cache.go:142-149` 的 map 字面量。 */
function cacheStatsOf(over: Record<string, unknown> = {}): BodyCacheStats {
  return {
    size: 142,
    hits: 1023,
    misses: 287,
    evictions: 5,
    hit_rate: 1023 / 1310,
    cap: 1024,
    ...over,
  } as BodyCacheStats
}

/** 抄自 `LogFilesListResponse`（admin/log_management.go:79-83）。 */
function filesOf(over: Record<string, unknown> = {}): LogFilesList {
  return {
    files: [
      {
        name: 'gateway.log',
        size_bytes: 524288,
        mod_time: '2026-10-08T12:00:00Z',
        is_current: true,
        is_compressed: false,
        is_archived: false,
        size_human: '512 KB',
      },
    ],
    total: 1,
    dir: '/var/log/gateway',
    ...over,
  } as LogFilesList
}

/** 抄自 `LogStatsResponse`（admin/log_management.go:65-76）。10 键齐全。 */
function statsOf(over: Record<string, unknown> = {}): LogStats {
  return {
    log_dir: '/var/log/gateway',
    exists: true,
    total_files: 7,
    total_size_bytes: 1048576,
    total_size_human: '1 MB',
    archive_files: 3,
    archive_size: 524288,
    oldest_mtime: '2026-09-01T00:00:00Z',
    newest_mtime: '2026-10-08T12:00:00Z',
    disk_usage_pct: 12.5,
    ...over,
  } as LogStats
}

/** 抄自 `archive/list` 的**正常态**（admin/log_management.go:609-614）。 */
function archiveListOk(over: Record<string, unknown> = {}): LogArchiveList {
  return {
    archives: [
      { name: 'gateway-20260901.log.gz', size_bytes: 2048, size_human: '2 KB', mod_time: '2026-09-02T00:00:00Z' },
    ],
    total: 1,
    dir: '/var/log/gateway/archive',
    exists: true,
    ...over,
  } as LogArchiveList
}

/** ★ 抄自「未启用」那一支（:574）—— **没有 `dir`、没有 `exists` 两个键**。 */
function archiveListDisabled(): LogArchiveList {
  return { archives: [], total: 0 }
}

/** ★ 抄自「归档目录读不到」那一支（:580）。 */
function archiveListDirMissing(): LogArchiveList {
  return { archives: [], total: 0, dir: '/var/log/gateway/archive', exists: false }
}

/** 走一遍 JSON 序列化再回来，模拟真实响应（会丢掉 `undefined` 的键）。 */
function overTheWire<T>(v: T): T {
  return JSON.parse(JSON.stringify(v)) as T
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('★★★★★★ 四条只读全是 admin 档 ⇒ 抽屉席不设 requiresRole', () => {
  it('★★★★★★ URL 形态', async () => {
    const urls: string[] = []
    fetchMock.mockResolvedValueOnce(jsonResponse(cacheStatsOf()))
    await fetchBodyCacheStats()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(filesOf()))
    await fetchLogFiles()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(statsOf()))
    await fetchLogStats()
    urls.push(lastUrl())

    fetchMock.mockResolvedValueOnce(jsonResponse(archiveListOk()))
    await fetchLogArchiveList()
    urls.push(lastUrl())

    expect(urls).toEqual([
      '/api/admin/logs/body-cache-stats',
      '/api/admin/logs/files',
      '/api/admin/logs/stats',
      '/api/admin/logs/archive/list',
    ])
  })

  it('★★★★★ 本批只碰这四条：superAdmin 那三条一个都没出现', () => {
    // config / archive / cleanup 都是 h.superAdmin（写操作 + 热加载）
    const src = JSON.stringify([BODY_CACHE_STAT_KEYS, LOG_STATS_KEYS])
    expect(src).not.toContain('log_config')
  })
})

describe('★★★★★★★ `hit_rate: 0` 是二义的', () => {
  it('★★★★★★★ 一次流量都没有 ⇒ 命中率为 0，但**不是** 0%', () => {
    const s = cacheStatsOf({ hits: 0, misses: 0, hit_rate: 0 })
    expect(bodyCacheHasNoTraffic(s)).toBe(true)
    expect(bodyCacheIsGenuinelyZeroRate(s)).toBe(false)
    expect(bodyCacheHitRate(0, 0)).toBe(0)
  })

  it('★★★★★★★ 有流量且全未命中 ⇒ 这才是真的 0%', () => {
    const s = cacheStatsOf({ hits: 0, misses: 7, hit_rate: 0 })
    expect(bodyCacheHasNoTraffic(s)).toBe(false)
    expect(bodyCacheIsGenuinelyZeroRate(s)).toBe(true)
  })

  it('★★★★★★ 两个 `hit_rate: 0` 响应，形状完全一样 ⇒ 只能靠派生量分开', () => {
    const idle = overTheWire(cacheStatsOf({ hits: 0, misses: 0, hit_rate: 0 }))
    const allMiss = overTheWire(cacheStatsOf({ hits: 0, misses: 7, hit_rate: 0 }))
    expect(idle.hit_rate).toBe(allMiss.hit_rate)
    // ⇒ 页面必须同时看 hits/misses，不许只看 hit_rate
    expect(bodyCacheHasNoTraffic(idle)).toBe(true)
    expect(bodyCacheIsGenuinelyZeroRate(allMiss)).toBe(true)
  })

  it('★★★★★ `hit_rate` 是派生值，可复算', () => {
    expect(bodyCacheHitRateMatches(cacheStatsOf())).toBe(true)
    expect(bodyCacheHitRateMatches(cacheStatsOf({ hit_rate: 0.9 }))).toBe(false)
  })

  it('★★★★★ `size > cap` 是后端异常', () => {
    expect(bodyCacheSizeExceedsCap(cacheStatsOf({ size: 2000, cap: 1024 }))).toBe(true)
    expect(bodyCacheSizeExceedsCap(cacheStatsOf())).toBe(false)
  })

  it('★★★★★ 淘汰数不应超过累计请求数', () => {
    expect(bodyCacheEvictionsArePlausible(cacheStatsOf({ hits: 0, misses: 1, evictions: 99 }))).toBe(false)
    expect(bodyCacheEvictionsArePlausible(cacheStatsOf())).toBe(true)
  })
})

describe('★★★★★★★ `files` 里的 `is_archived` 永远是 false', () => {
  it('★★★★★★★ 后端是硬编码字面量 false，客户端不许据此说「没有归档」', async () => {
    const got = overTheWire(filesOf())
    expect(got.files.every((f) => f.is_archived === false)).toBe(true)
    // ★ `is_archived` 在**任何**响应里都为 false ⇒ 它不携带信息
    const allFalse = got.files.map((f) => f.is_archived)
    expect(new Set(allFalse).size).toBe(1)
  })

  it('★★★★★ 压缩判定是**真算的** `.gz` 后缀，与 `is_archived` 不同', () => {
    const gz = overTheWire(
      filesOf({
        files: [
          {
            name: 'gateway-20260901.log.gz',
            size_bytes: 10,
            mod_time: '2026-09-01T00:00:00Z',
            is_current: false,
            is_compressed: true,
            is_archived: false,
            size_human: '10 B',
          },
        ],
      }),
    )
    expect(gz.files[0]!.is_compressed).toBe(true)
    expect(gz.files[0]!.is_archived).toBe(false)
  })

  it('★★★★★★ 「文件日志未启用」在 files 里是 `dir === ""`（不是 files 为空）', () => {
    const off = overTheWire(filesOf({ files: [], total: 0, dir: '' }))
    expect(off.files).toEqual([])
    expect(off.dir).toBe('')
    // ★ 与「启用了但目录里没有 .log/.gz」两者的区别**只在 dir**
    const onButEmpty = overTheWire(filesOf({ files: [], total: 0, dir: '/var/log/gateway' }))
    expect(onButEmpty.files).toEqual([])
    expect(onButEmpty.dir).toBe('/var/log/gateway')
  })
})

describe('★★★★★★★ stats 的三种状态共用同一个形状', () => {
  it('★★★★★★★ ① 未启用：整个零值响应，log_dir 与 exists 都是空/false', () => {
    const off = overTheWire(
      statsOf({
        log_dir: '',
        exists: false,
        total_files: 0,
        total_size_bytes: 0,
        total_size_human: '0 B',
        archive_files: 0,
        archive_size: 0,
        oldest_mtime: null,
        newest_mtime: null,
        disk_usage_pct: 0,
      }),
    )
    expect(logStatsLoggingDisabled(off)).toBe(true)
    expect(logStatsDirMissing(off)).toBe(false)
    expect(logStatsOk(off)).toBe(false)
  })

  it('★★★★★★★ ② 启用了但目录不存在：log_dir 有值、exists=false', () => {
    const missing = overTheWire(
      statsOf({
        log_dir: '/var/log/gateway',
        exists: false,
        total_files: 0,
        total_size_bytes: 0,
        total_size_human: '0 B',
        archive_files: 0,
        archive_size: 0,
        oldest_mtime: null,
        newest_mtime: null,
        disk_usage_pct: 0,
      }),
    )
    expect(logStatsLoggingDisabled(missing)).toBe(false)
    expect(logStatsDirMissing(missing)).toBe(true)
  })

  // ★★★ 这条是本批最核心的判据：① 与 ② 的 10 个键**完全一样**，
  //   唯一区别是 `log_dir` 空不空 ⇒ `exists` 单独看**分辨不出**。
  it('★★★★★★★★ ① 与 ② 的 `exists:false` 完全同形，只有 `log_dir` 能分开', () => {
    const off = overTheWire(statsOf({ log_dir: '', exists: false }))
    const missing = overTheWire(statsOf({ log_dir: '/var/log/gateway', exists: false }))
    expect(off.exists).toBe(missing.exists)
    expect(off.log_dir).not.toBe(missing.log_dir)
    expect(logStatsExistsIsAmbiguous(off)).toBe(true)
    expect(logStatsExistsIsAmbiguous(missing)).toBe(true)
    // ⇒ 判「未启用」**必须**看 log_dir
    expect(logStatsLoggingDisabled(off)).toBe(true)
    expect(logStatsLoggingDisabled(missing)).toBe(false)
  })

  it('★★★★★★★ `oldest_mtime: null` 是「一个非归档文件都没有」', () => {
    expect(logStatsHasNoFiles(overTheWire(statsOf({ oldest_mtime: null })))).toBe(true)
    expect(logStatsHasNoFiles(overTheWire(statsOf()))).toBe(false)
  })

  it('★★★★★★ `total_files` **不含**归档，两栏要分开', () => {
    const s = overTheWire(statsOf({ total_files: 7, archive_files: 3 }))
    expect(s.total_files).toBe(7)
    expect(logStatsTotalIncludingArchive(s)).toBe(10)
    // ★ 但后端**没有**这个合计字段 ⇒ 页面要自己说明口径
    expect('total_including_archive' in s).toBe(false)
  })

  // ★★★★ 变异 T11 逼出来的：原实现是 `oldest === null || newest === null`。
  //   退化成只判 `oldest` 时，原有用例**照样全绿** —— 因为它们总是两个一起置 null。
  it('★★★★★★ 只有 `newest_mtime` 是 null ⇒ 同样算「没有文件」', () => {
    const onlyNewest = overTheWire(statsOf({ oldest_mtime: '2026-09-01T00:00:00Z', newest_mtime: null }))
    expect(logStatsHasNoFiles(onlyNewest)).toBe(true)
  })

  it('★★★★★ `disk_usage_pct: 0` 分不出「0%」与「探测失败」', () => {
    expect(logStatsDiskUsageIsAmbiguous(overTheWire(statsOf({ disk_usage_pct: 0 })))).toBe(true)
    expect(logStatsDiskUsageIsAmbiguous(overTheWire(statsOf()))).toBe(false)
  })
})

describe('★★★★★★★★ archive/list 是异形端点：dir/exists 条件存在', () => {
  it('★★★★★★★★ 「未启用」那一支**没有** dir 与 exists 两个键', () => {
    const off = overTheWire(archiveListDisabled())
    expect('dir' in off).toBe(false)
    expect('exists' in off).toBe(false)
    expect(off.archives).toEqual([])
    expect(logArchiveListIsDisabled(off)).toBe(true)
    expect(logArchiveListState(off)).toBe('disabled')
  })

  it('★★★★★★★★ 「归档目录读不到」那一支：有 dir、exists=false', () => {
    const miss = overTheWire(archiveListDirMissing())
    expect(miss.dir).toBe('/var/log/gateway/archive')
    expect(miss.exists).toBe(false)
    expect(logArchiveListDirMissing(miss)).toBe(true)
    expect(logArchiveListState(miss)).toBe('dir-missing')
  })

  it('★★★★★★★ ①与②的 archives/total 完全一样，只有「键在不在」能分开', () => {
    const off = overTheWire(archiveListDisabled())
    const miss = overTheWire(archiveListDirMissing())
    expect(off.archives).toEqual(miss.archives)
    expect(off.total).toBe(miss.total)
    expect(logArchiveListAmbiguousZero(off)).toBe(true)
    expect(logArchiveListAmbiguousZero(miss)).toBe(true)
    // ⇒ 分开它们的**唯一**办法是键的有无
    expect(logArchiveListState(off)).not.toBe(logArchiveListState(miss))
  })

  // ★★★ 这一条就是「同一条件、三个端点三个判据」的核心
  it('★★★★★★★ 同一个「未启用」在三个端点上是三个判据，不能复用', () => {
    const disabledFiles = overTheWire(filesOf({ files: [], total: 0, dir: '' }))
    const disabledStats = overTheWire(statsOf({ log_dir: '', exists: false }))
    const disabledArch = overTheWire(archiveListDisabled())
    // files  ⇒ 空串
    expect(disabledFiles.dir).toBe('')
    // stats  ⇒ 空串
    expect(disabledStats.log_dir).toBe('')
    // archive/list ⇒ **键整个不存在**
    expect('dir' in disabledArch).toBe(false)
    expect(disabledArch.dir).toBeUndefined()
  })

  // ★★★★ 这条是**变异 T2 逼出来的**：把 `!('dir' in r)` 换成
  //   `r.dir === undefined || r.dir === ''` 时，原来所有用例**照样全绿** ——
  //   因为后端要么不写这个键、要么写一个非空路径，从没写过空串。
  //   ⇒ 缺口是「键在但值为空」这种输入**从没喂过**。契约上按**键在不在**判。
  it('★★★★★★ `dir` 键在但值是空串 ⇒ 仍算「键在」，不是「未启用」', () => {
    const r = overTheWire({ archives: [], total: 0, dir: '', exists: false })
    expect('dir' in r).toBe(true)
    expect(logArchiveListIsDisabled(r)).toBe(false)
    expect(logArchiveListDirMissing(r)).toBe(true)
    expect(logArchiveListState(r)).toBe('dir-missing')
  })

  // ★★★★ 变异 T19 逼出来的：`archives.length === 0` 与 `&& total === 0` 不可分辨
  it('★★★★★★ `archives` 空但 `total` 非 0 ⇒ 不算「两个都是 0」', () => {
    const r = overTheWire({ archives: [], total: 5, dir: '/a', exists: true })
    expect(logArchiveListAmbiguousZero(r)).toBe(false)
  })

  // ★★★★ 变异 T20 逼出来的：`'dir' in r` 与 `exists === true` 不可分辨
  it('★★★★★★ `dir` 在但 `exists` 键整个缺失 ⇒ 不算「读到了」', () => {
    const r = overTheWire({ archives: [], total: 0, dir: '/a' })
    expect('exists' in r).toBe(false)
    expect(logArchiveListOk(r)).toBe(false)
    expect(logArchiveListState(r)).toBe('dir-missing')
  })

  it('★★★★★ 正常态 exists===true 才是「读到了归档列表」', () => {
    const ok = overTheWire(archiveListOk())
    expect(logArchiveListOk(ok)).toBe(true)
    expect(logArchiveListState(ok)).toBe('ok')
  })
})

describe('★★★★★★ 形状判据与兄弟端点互喂', () => {
  it('★★★★★★ 四个形状互不包含 ⇒ 互喂抛错', () => {
    expect(() => unwrapBodyCacheStats(filesOf())).toThrow(/形状不符/)
    expect(() => unwrapLogFiles(cacheStatsOf())).toThrow(/形状不符/)
    expect(() => unwrapLogStats(archiveListOk())).toThrow(/形状不符/)
    expect(() => unwrapLogArchiveList(statsOf())).toThrow(/形状不符/)
  })

  it('★★★★★ 各自的合法响应不抛错', () => {
    expect(() => unwrapBodyCacheStats(cacheStatsOf())).not.toThrow()
    expect(() => unwrapLogFiles(filesOf())).not.toThrow()
    expect(() => unwrapLogStats(statsOf())).not.toThrow()
    expect(() => unwrapLogArchiveList(archiveListDisabled())).not.toThrow()
    expect(() => unwrapLogArchiveList(archiveListOk())).not.toThrow()
  })

  it('★★★★★ null / 裸数组 / 空对象 ⇒ 抛错', () => {
    for (const u of [unwrapBodyCacheStats, unwrapLogFiles, unwrapLogStats, unwrapLogArchiveList]) {
      expect(() => u(null)).toThrow(/形状不符/)
      expect(() => u([])).toThrow(/形状不符/)
      expect(() => u({})).toThrow(/形状不符/)
    }
  })

  // ★★★ 形状互喂**抓不到同一形状内部的字段缺失**
  //   ⇒ 两个「无信封」端点（6 键 / 10 键）必须为**每个必填键**各钉一条。
  it.each([...BODY_CACHE_STAT_KEYS])('★★★★★★★ body-cache-stats 缺 `%s` 必抛错', (k) => {
    const s = { ...cacheStatsOf() }
    delete (s as Record<string, unknown>)[k]
    expect(() => unwrapBodyCacheStats(s)).toThrow(/形状不符/)
  })

  it.each([...LOG_STATS_KEYS])('★★★★★★★ stats 缺 `%s` 必抛错', (k) => {
    const s = { ...statsOf() }
    delete (s as Record<string, unknown>)[k]
    expect(() => unwrapLogStats(s)).toThrow(/形状不符/)
  })

  // ★ archive/list 的 `dir` / `exists` 是**允许缺**的 ⇒ 不能钉这两条
  it('★★★★★★ archive/list 缺 `dir` / `exists` **不**抛错（那是合法的一支）', () => {
    expect(() => unwrapLogArchiveList(archiveListDisabled())).not.toThrow()
    expect(() => unwrapLogArchiveList(archiveListDirMissing())).not.toThrow()
  })

  it('★★★★★ 缺 `archives` / `total` ⇒ 抛错', () => {
    const a = { ...archiveListOk() }
    delete (a as Record<string, unknown>).archives
    expect(() => unwrapLogArchiveList(a)).toThrow(/形状不符/)
    const b = { ...archiveListOk() }
    delete (b as Record<string, unknown>).total
    expect(() => unwrapLogArchiveList(b)).toThrow(/形状不符/)
  })

  it('★★★★★ 缺 `files` / `total` / `dir` ⇒ 抛错', () => {
    for (const k of ['files', 'total', 'dir'] as const) {
      const f = { ...filesOf() }
      delete (f as Record<string, unknown>)[k]
      expect(() => unwrapLogFiles(f)).toThrow(/形状不符/)
    }
  })
})

describe('★★★ 错误态与派生值', () => {
  it('★★★ 503 `body cache not initialized`（代码用 initialized，注释写 initialised）', async () => {
    const msg = 'body cache not initialized'
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: msg } }, 503))
    await expect(fetchBodyCacheStats()).rejects.toThrow(msg)
    expect(bodyCacheNotInitialised(msg)).toBe(true)
    expect(bodyCacheNotInitialised('body cache not initialised')).toBe(true)
    expect(bodyCacheNotInitialised('database not configured')).toBe(false)
  })

  it('★★★ 405 `method not allowed`（四条都有方法门）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'method not allowed' } }, 405))
    await expect(fetchLogFiles()).rejects.toThrow(/method not allowed/)
  })

  it('★★★ 500：`files` 走 os.ReadDir，失败会 500（同族另两条是静默跳过）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ error: { detail: 'list log files' } }, 500))
    await expect(fetchLogFiles()).rejects.toThrow(/list log files/)
  })

  it('★★ humanBytes 可复算（派生值一致性核对）', () => {
    expect(humanBytesMatches(0, '0 B')).toBe(true)
    expect(humanBytesMatches(512, '512 B')).toBe(true)
    expect(humanBytesMatches(1024, '1 KB')).toBe(true)
    expect(humanBytesMatches(1048576, '1 MB')).toBe(true)
    expect(humanBytesMatches(1536, '1.5 KB')).toBe(true)
    expect(humanBytesMatches(1024, '1.0 KB')).toBe(false)
    // ★ 负数后端返 "0 B"
    expect(humanBytesMatches(-1, '0 B')).toBe(true)
  })
})
