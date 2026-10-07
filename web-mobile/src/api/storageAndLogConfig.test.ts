import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchStorageConfig,
  fetchLogConfig,
  unwrapStorageConfig,
  unwrapLogConfig,
  logConfigPathMatchesRuntime,
  logConfigPutKeyFor,
  STORAGE_CONFIG_PATH,
  LOG_CONFIG_PATH,
  ADMIN_CONFIG_ERROR_DETAILS,
  CONFIG_SOURCES,
  STORAGE_TYPES,
  STORAGE_DOWNLOAD_URL_PREFIX,
  LOG_CONFIG_DEFAULT_ARCHIVE_DAYS,
  LOG_CONFIG_DEFAULT_DELETE_DAYS,
  LOG_ENABLED_SOURCES,
  STORAGE_CONFIG_REQUIRED_KEYS,
  STORAGE_CONFIG_OPTIONAL_KEYS,
  STORAGE_BOOL_OPTIONAL_KEYS,
  STORAGE_MASKED_KEYS,
  LOG_CONFIG_KEYS,
  type StorageConfigResponse,
  type LogConfigResponse,
} from './storageAndLogConfig'

/**
 * 存储配置 + 日志轮转配置的契约测试（2026-10-08，第九十三批）。
 *
 * 后端：`admin/handler.go:1108` 与 `:1114`（**两行相邻、同一 mux**，
 * 但 `:1111` 的 `storage/migration-state` 却是 `admin` 档 ⇒ 绝不能按前缀推权限）
 * + `admin/storage_config.go:119-227` + `admin/log_management.go:41-62,117-183`。
 *
 * 重点是源文件头写明的十六件事 (1)…(16)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量。
 * ⚠️ 夹具的键数与键名逐字来自 Go 结构体，**不手数**（键数由脚本数出后写死在这里）。
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

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

// ── 夹具：逐字照抄 `admin/storage_config.go:32-70` 的 13 个恒在键 ──

function storageLocalOf(over: Partial<StorageConfigResponse> = {}): StorageConfigResponse {
  return {
    storage_type: 'local',
    attachment_dir_override: '',
    ttl_days: 30,
    max_file_size_mb: 20,
    disk_quota_percent: 80,
    auto_cleanup_enabled: false,
    auto_cleanup_threshold: 85,
    effective_dir: './data/attachments',
    attachment_dir_env: '',
    needs_restart: false,
    current_disk_usage: 42.5,
    config_source: 'default',
    download_url_prefix: '/api/attachments/',
    ...over,
  }
}

/** oss 段（`switch` 的 `case "oss"`，`:196-206`）：secret 是 `"***" + 后四位`。 */
function storageOssOf(over: Partial<StorageConfigResponse> = {}): StorageConfigResponse {
  return {
    ...storageLocalOf({ storage_type: 'oss' }),
    oss_endpoint: 'oss-cn-hangzhou.aliyuncs.com',
    oss_bucket: 'gw-attachments',
    oss_access_key_id: 'AKID-example',
    oss_access_key_secret: '***ab12',
    oss_base_path: 'tenant-a/',
    ...over,
  }
}

/** s3 段（`case "s3"`，`:208-218`）：★ `s3_use_ssl` 出现即 true。 */
function storageS3Of(over: Partial<StorageConfigResponse> = {}): StorageConfigResponse {
  return {
    ...storageLocalOf({ storage_type: 's3' }),
    s3_endpoint: 's3.us-east-1.amazonaws.com',
    s3_region: 'us-east-1',
    s3_bucket: 'gw-attachments',
    s3_access_key_id: 'AKIAEXAMPLE',
    s3_secret_access_key: '***cd34',
    s3_base_path: 'tenant-b/',
    s3_use_ssl: true,
    ...over,
  }
}

// ── 夹具：逐字照抄 `admin/log_management.go:41-62` 的 15 个恒在键 ──

function logConfigOf(over: Partial<LogConfigResponse> = {}): LogConfigResponse {
  return {
    max_size_mb: 100,
    max_backups: 5,
    max_age_days: 14,
    compress: true,
    archive_days: 7,
    delete_days: 30,
    file_path: '/var/log/llmgw/gateway.log',
    file_path_override: '',
    file_path_env: '/var/log/llmgw/gateway.log',
    enabled: true,
    enabled_source: 'env',
    log_file: '/var/log/llmgw/gateway.log',
    log_dir: '/var/log/llmgw',
    hot_reloadable: true,
    config_source: 'default',
    ...over,
  }
}

// ══════════════════════════════════════════════════════════════════════════
// storage/config 解包（13 恒在 + 13 omitempty）
// ══════════════════════════════════════════════════════════════════════════

describe('存储配置解包', () => {
  it('★ 满配 local 被放行（13 个恒在键、无任何可选键）', () => {
    expect(unwrapStorageConfig(storageLocalOf())).toBeTruthy()
  })

  it('★ ★★ 顶层是 null ⇒ 抛', () => {
    expect(() => unwrapStorageConfig(null)).toThrow(/存储配置 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ ★ 顶层是数组 ⇒ 抛「实得 array」', () => {
    expect(() => unwrapStorageConfig([] as never)).toThrow(/存储配置 响应形状不符：期望裸对象，实得 array/)
  })

  it('★ ★★ 十三个恒在键逐个都要检查', () => {
    for (const k of [
      'storage_type', 'attachment_dir_override', 'ttl_days', 'max_file_size_mb',
      'disk_quota_percent', 'auto_cleanup_enabled', 'auto_cleanup_threshold',
      'effective_dir', 'attachment_dir_env', 'needs_restart',
      'current_disk_usage', 'config_source', 'download_url_prefix',
    ]) {
      expect(() => unwrapStorageConfig(del(storageLocalOf() as never, k) as never)).toThrow(
        new RegExp(`存储配置 缺 1 个键（${k}）`),
      )
    }
  })

  it('★ ★★ 六个字符串恒在键逐个都要校验', () => {
    for (const k of [
      'storage_type', 'attachment_dir_override', 'effective_dir',
      'attachment_dir_env', 'config_source', 'download_url_prefix',
    ] as const) {
      expect(() => unwrapStorageConfig({ ...storageLocalOf(), [k]: 1 } as never)).toThrow(
        new RegExp(`存储配置 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ ★★ 五个数字恒在键逐个都要校验', () => {
    for (const k of [
      'ttl_days', 'max_file_size_mb', 'disk_quota_percent',
      'auto_cleanup_threshold', 'current_disk_usage',
    ] as const) {
      expect(() => unwrapStorageConfig({ ...storageLocalOf(), [k]: 'x' } as never)).toThrow(
        new RegExp(`存储配置 的 ${k} 不是数字`),
      )
    }
  })

  it('★ needs_restart 不是布尔 ⇒ 抛', () => {
    expect(() => unwrapStorageConfig({ ...storageLocalOf(), needs_restart: 1 } as never)).toThrow(
      /needs_restart 不是布尔/,
    )
  })

  it('★ auto_cleanup_enabled 不是布尔 ⇒ 抛', () => {
    expect(() => unwrapStorageConfig({ ...storageLocalOf(), auto_cleanup_enabled: 1 } as never)).toThrow(
      /auto_cleanup_enabled 不是布尔/,
    )
  })

  it('★ ★★ 十二个字符串可选键逐个都要校验（存在时的类型）', () => {
    for (const k of [
      'oss_endpoint', 'oss_bucket', 'oss_access_key_id', 'oss_access_key_secret', 'oss_base_path',
      's3_endpoint', 's3_region', 's3_bucket', 's3_access_key_id', 's3_secret_access_key',
      's3_base_path', 'migration_run_id',
    ] as const) {
      expect(() => unwrapStorageConfig({ ...storageLocalOf(), [k]: 1 } as never)).toThrow(
        new RegExp(`存储配置 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ ★★ s3_use_ssl 存在但不是布尔 ⇒ 抛', () => {
    expect(() => unwrapStorageConfig({ ...storageLocalOf(), s3_use_ssl: 'yes' } as never)).toThrow(
      /s3_use_ssl 不是布尔/,
    )
  })

  it('★ ★★ oss 形状被放行（5 个 oss_* 键带值，含脱敏 secret）', () => {
    expect(unwrapStorageConfig(storageOssOf())).toBeTruthy()
  })

  // ★ 这一条同时是 s3_use_ssl 的正向覆盖：它**出现**时就是 true（:216-218 只赋 true）。
  it('★ ★★ s3 形状被放行（7 个 s3_* 键带值，含唯一的布尔可选键）', () => {
    const c = unwrapStorageConfig(storageS3Of())
    expect(c.s3_use_ssl).toBe(true)
  })

  it('★ ★ migration_run_id 有值时被放行（PUT 触发了目录迁移才有）', () => {
    expect(unwrapStorageConfig(storageLocalOf({ migration_run_id: 'run-2026-10-07' }))).toBeTruthy()
  })

  it('★ ★★ 十三个可选键全部缺席也是合法形状（local 缺省的形状）', () => {
    expect(() => unwrapStorageConfig(storageLocalOf())).not.toThrow()
  })

  it('★ ★★ 空串值是合法的（★ 空串 ≠ 键缺）', () => {
    expect(() =>
      unwrapStorageConfig(storageLocalOf({ attachment_dir_override: '', attachment_dir_env: '' })),
    ).not.toThrow()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// logs/config 解包（15 键全部恒在，一个 omitempty 都没有）
// ══════════════════════════════════════════════════════════════════════════

describe('日志配置解包', () => {
  it('★ 满配配置被放行（15 键）', () => {
    expect(unwrapLogConfig(logConfigOf())).toBeTruthy()
  })

  it('★ 顶层是 null ⇒ 抛', () => {
    expect(() => unwrapLogConfig(null)).toThrow(/日志配置 响应形状不符：期望裸对象，实得 null/)
  })

  it('★ ★★ 十五个键逐个都要检查（★ 一个 omitempty 都没有，缺任一个都抛）', () => {
    for (const k of [
      'max_size_mb', 'max_backups', 'max_age_days', 'compress', 'archive_days', 'delete_days',
      'file_path', 'file_path_override', 'file_path_env', 'enabled', 'enabled_source',
      'log_file', 'log_dir', 'hot_reloadable', 'config_source',
    ]) {
      expect(() => unwrapLogConfig(del(logConfigOf() as never, k) as never)).toThrow(
        new RegExp(`日志配置 缺 1 个键（${k}）`),
      )
    }
  })

  it('★ ★★ 五个数字键逐个都要校验', () => {
    for (const k of ['max_size_mb', 'max_backups', 'max_age_days', 'archive_days', 'delete_days'] as const) {
      expect(() => unwrapLogConfig({ ...logConfigOf(), [k]: 'x' } as never)).toThrow(
        new RegExp(`日志配置 的 ${k} 不是数字`),
      )
    }
  })

  it('★ ★★ 三个布尔键逐个都要校验', () => {
    for (const k of ['compress', 'enabled', 'hot_reloadable'] as const) {
      expect(() => unwrapLogConfig({ ...logConfigOf(), [k]: 1 } as never)).toThrow(
        new RegExp(`日志配置 的 ${k} 不是布尔`),
      )
    }
  })

  it('★ ★ 七个字符串键逐个都要校验', () => {
    for (const k of [
      'file_path', 'file_path_override', 'file_path_env',
      'enabled_source', 'log_file', 'log_dir', 'config_source',
    ] as const) {
      expect(() => unwrapLogConfig({ ...logConfigOf(), [k]: 1 } as never)).toThrow(
        new RegExp(`日志配置 的 ${k} 不是字符串`),
      )
    }
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (2)(12) 可选键的可达性
// ══════════════════════════════════════════════════════════════════════════
// (9) 脱敏形态
// ══════════════════════════════════════════════════════════════════════════
// (4)(13) config_source 与 download_url_prefix
// ══════════════════════════════════════════════════════════════════════════
// (5)(6)(7)(8) logs/config 语义
describe('(8) file_path 与 log_file 的关系', () => {
  it('★ ★★ file_path 等于 log_file ⇒ 判为一致（未热加载的常态）', () => {
    expect(logConfigPathMatchesRuntime(logConfigOf())).toBe(true)
  })

  // ★★★ 本族**唯一有区分力**的语义判据：DB 覆盖了 log.file_path
  //   但运行时尚未热加载时，:174-177 只改 file_path、**不动 log_file**。
  it('★ ★★ file_path 与 log_file 不同 ⇒ 判为不一致（DB 覆盖了但尚未热加载）', () => {
    const r = logConfigOf({ file_path: '/new/path.log', log_file: '/old/path.log' })
    expect(logConfigPathMatchesRuntime(r)).toBe(false)
  })
})
// ══════════════════════════════════════════════════════════════════════════
// (14) PUT/GET 键名不对称
// ══════════════════════════════════════════════════════════════════════════

describe('(14) PUT 与 GET 的键名对照', () => {
  it('★ ★★★ delete_days 写回去要改名为 archive_delete_days', () => {
    expect(logConfigPutKeyFor('delete_days')).toBe('archive_delete_days')
  })

  it('★ ★★ archive_days 原样（★ 与 delete_days 各一条）', () => {
    expect(logConfigPutKeyFor('archive_days')).toBe('archive_days')
  })

  it('★ ★ max_size_mb 原样', () => {
    expect(logConfigPutKeyFor('max_size_mb')).toBe('max_size_mb')
  })

  it('★ ★ file_path 原样', () => {
    expect(logConfigPutKeyFor('file_path')).toBe('file_path')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ ★★ 存储配置打在 superAdmin 的路径上且不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(storageLocalOf()))
    await fetchStorageConfig()
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('/api/admin/storage/config')
    expect(u).not.toContain('?')
  })

  it('★ ★ 日志配置路径正确且不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(logConfigOf()))
    await fetchLogConfig()
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('/api/admin/logs/config')
    expect(u).not.toContain('?')
  })

  it('★ ★★ 端到端：存储配置缺一个键 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(del(storageLocalOf() as never, 'ttl_days')))
    await expect(fetchStorageConfig()).rejects.toThrow(/缺 1 个键/)
  })

  it('★ ★★ 端到端：日志配置缺一个键 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(del(logConfigOf() as never, 'delete_days')))
    await expect(fetchLogConfig()).rejects.toThrow(/缺 1 个键/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ ★ 两条路径不带尾斜杠', () => {
    expect(STORAGE_CONFIG_PATH).toBe('/api/admin/storage/config')
    expect(LOG_CONFIG_PATH).toBe('/api/admin/logs/config')
  })

  it('★ ★★ 两条错误文案与后端逐字一致', () => {
    expect([...Object.values(ADMIN_CONFIG_ERROR_DETAILS)]).toEqual([
      'method not allowed',
      'settings store unavailable',
    ])
  })

  it('★ ★ config_source 三值（★ 本族是真有第三个值的，与 enabled_source 不同）', () => {
    expect([...CONFIG_SOURCES]).toEqual(['default', 'env', 'db'])
  })

  it('★ ★★ enabled_source 只有两值（★ 注释里的 default 被排除了）', () => {
    expect([...LOG_ENABLED_SOURCES]).toEqual(['env', 'db'])
    expect(LOG_ENABLED_SOURCES).toHaveLength(2)
  })

  it('★ ★ storage_type 三值', () => {
    expect([...STORAGE_TYPES]).toEqual(['local', 'oss', 's3'])
  })

  it('★ ★★ 下载前缀常量', () => {
    expect(STORAGE_DOWNLOAD_URL_PREFIX).toBe('/api/attachments/')
  })

  it('★ ★ 归档与删除的硬编码缺省', () => {
    expect(LOG_CONFIG_DEFAULT_ARCHIVE_DAYS).toBe(7)
    expect(LOG_CONFIG_DEFAULT_DELETE_DAYS).toBe(30)
  })

  it('★ ★★ 十三个恒在键与十三个可选键（★ 键数从 Go 源码数出）', () => {
    expect(STORAGE_CONFIG_REQUIRED_KEYS).toHaveLength(13)
    expect(STORAGE_CONFIG_OPTIONAL_KEYS).toHaveLength(13)
  })

  it('★ ★★ 十三个恒在键逐个与后端一致', () => {
    expect([...STORAGE_CONFIG_REQUIRED_KEYS]).toEqual([
      'storage_type', 'attachment_dir_override', 'ttl_days', 'max_file_size_mb',
      'disk_quota_percent', 'auto_cleanup_enabled', 'auto_cleanup_threshold',
      'effective_dir', 'attachment_dir_env', 'needs_restart',
      'current_disk_usage', 'config_source', 'download_url_prefix',
    ])
  })

  it('★ ★★ 十三个可选键逐个与后端一致（含唯一的布尔 s3_use_ssl）', () => {
    expect([...STORAGE_CONFIG_OPTIONAL_KEYS]).toEqual([
      'oss_endpoint', 'oss_bucket', 'oss_access_key_id', 'oss_access_key_secret', 'oss_base_path',
      's3_endpoint', 's3_region', 's3_bucket', 's3_access_key_id', 's3_secret_access_key',
      's3_base_path', 's3_use_ssl', 'migration_run_id',
    ])
    expect([...STORAGE_BOOL_OPTIONAL_KEYS]).toEqual(['s3_use_ssl'])
  })

  it('★ ★★ 两个恒带掩码的键', () => {
    expect([...STORAGE_MASKED_KEYS]).toEqual(['oss_access_key_secret', 's3_secret_access_key'])
  })

  it('★ ★★ 十五个恒在键（★ 一个 omitempty 都没有）', () => {
    expect(LOG_CONFIG_KEYS).toHaveLength(15)
    expect([...LOG_CONFIG_KEYS]).toEqual([
      'max_size_mb', 'max_backups', 'max_age_days', 'compress', 'archive_days', 'delete_days',
      'file_path', 'file_path_override', 'file_path_env', 'enabled', 'enabled_source',
      'log_file', 'log_dir', 'hot_reloadable', 'config_source',
    ])
  })
})