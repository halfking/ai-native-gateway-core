import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchStorageMigrationState,
  unwrapMigrationState,
  unwrapMigrationRun,
  effectiveMigrationRun,
  migrationStateIsEmpty,
  shouldPollMigration,
  migrationRunIsTerminal,
  migrationFileProgress,
  migrationByteProgress,
  migrationPurgeLooksComplete,
  migrationRunHasErrorDetail,
  migrationStateContainsRunId,
  MIGRATION_STATE_PATH,
  MIGRATION_STATE_KEYS,
  MIGRATION_RUN_KEYS,
  MIGRATION_RUN_OPTIONAL_KEYS,
  MIGRATION_STATUSES,
  MIGRATION_TERMINAL_STATUSES,
  type MigrationRun,
  type MigrationStateResponse,
} from './storageMigrationState'

/**
 * 附件存储目录迁移进度的契约测试（2026-10-08，第九十五批）。
 *
 * 后端：`admin/storage_migration.go:401-421`（handler）+ `:425-427`（分发入口）
 * + `:99-112`（getMigration）+ `:48-64`（migrationRun）+ `:40-45`（状态枚举），
 * 由 `admin/handler.go:1111` 用 **`admin(...)`** 挂载（★ **admin 档** ——
 * 与批 93 的 `storage/config`（`:1108`，superAdmin 档）**同前缀、相邻两行、两种档位**）。
 *
 * 重点是源文件头写明的十一件事 (1)…(11)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量。
 * ⚠️ 顶层是 Go map ⇒ `encoding/json` **按键名排序** ⇒ 夹具里 `latest` 写在 `running` 前。
 * ⚠️ run 内的键序照抄结构体声明顺序（`storage_migration.go:49-63`）。
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

// ═══════════════════════════════════════════════════════════════════════════
// 夹具：逐字照抄 `admin/storage_migration.go`
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★★ 复制进行中的一条 run（照抄 `:131-139` 建 run + `:203-242` 循环中途）。
 * 键序 = 结构体声明顺序；`finished_at` 缺失（只有 `finishMigration:292` 与
 * panic 分支 `:162` 会写）；`errors` 有一条（照抄 `:226` 的复制失败分支）。
 */
function runningRunOf(over: Partial<MigrationRun> = {}): Record<string, unknown> {
  return {
    run_id: 'migration-1759897200123456789',
    status: 'running',
    from_dir: '/data/attachments',
    to_dir: '/var/llm-gateway/attachments',
    started_at: '2026-10-08T03:00:00.123456789Z',
    heartbeat_at: '2026-10-08T03:00:41.987654321Z',
    files_total: 1200,
    files_copied: 480,
    bytes_total: 8123456789,
    bytes_copied: 3100000000,
    files_deleted: 0,
    old_dir_purged: false,
    errors: ['copy 2026/09/report.pdf: no such file or directory'],
    message: '正在复制 1200 个文件 (7.6 GiB)…',
    ...over,
  }
}

/**
 * ★★★★ (5) **「failed 但 `errors` 键缺失」** —— 照抄 `:179`
 * `finishMigration(run, migrationFailed, "收集文件失败: "+err.Error())`：
 * 该分支只写 `Message`、**从不动 `Errors`**（`append` 只在 `:160 :219 :226 :274`）。
 * 且 `FilesTotal` / `BytesTotal` / `FilesCopied` / `FilesDeleted` 全是零值。
 */
function collectFailedRun(): Record<string, unknown> {
  return {
    run_id: 'migration-1759897200987654321',
    status: 'failed',
    from_dir: '/data/attachments',
    to_dir: '/var/llm-gateway/attachments',
    started_at: '2026-10-08T04:10:00.000000000Z',
    heartbeat_at: '2026-10-08T04:10:00.000000000Z',
    finished_at: '2026-10-08T04:10:00.512000000Z',
    files_total: 0,
    files_copied: 0,
    bytes_total: 0,
    bytes_copied: 0,
    files_deleted: 0,
    old_dir_purged: false,
    message: '收集文件失败: permission denied',
  }
}

/**
 * ★★★★ (9) **空目录成功迁移**（照抄 `:182-201`）：
 * `FilesTotal = 0`、`BytesTotal = 0`、`FilesCopied` **从未赋值**（保持 0），
 * 终态仍是 `succeeded`（`:200`），`message = "迁移完成（空目录）"`。
 * `oldDirPurged` 取 `purged` 参数：true = 走了 `:197-198` 的 `os.RemoveAll`。
 */
function emptyDirRunOf(oldDirPurged: boolean): Record<string, unknown> {
  return {
    run_id: 'migration-1759897200555555555',
    status: 'succeeded',
    from_dir: '/data/attachments',
    to_dir: '/var/llm-gateway/attachments',
    started_at: '2026-10-08T05:20:00.000000000Z',
    heartbeat_at: '2026-10-08T05:20:00.000000000Z',
    finished_at: '2026-10-08T05:20:00.244000000Z',
    files_total: 0,
    files_copied: 0,
    bytes_total: 0,
    bytes_copied: 0,
    files_deleted: 0,
    old_dir_purged: oldDirPurged,
    message: '迁移完成（空目录）',
  }
}

/**
 * ★★★ (7) **非空目录迁移成功且已清理**（照抄 `:272-284`）：
 * `purged` 为 true ⇒ `run.FilesDeleted = run.FilesTotal`（`:277`，**赋的是总数不是实际删除数**）。
 * `message` 是 `:280-283` 拼的 `"迁移完成：%d 文件 (%s)"` + `"，%d 个警告"`。
 * ★ 全部文件都复制成功 ⇒ **`bytes_copied === bytes_total`**（`:237` 逐个累加全部成功项）。
 */
function purgedRun(): Record<string, unknown> {
  return {
    run_id: 'migration-1759897200111111111',
    status: 'succeeded',
    from_dir: '/data/attachments',
    to_dir: '/var/llm-gateway/attachments',
    started_at: '2026-10-08T06:30:00.000000000Z',
    heartbeat_at: '2026-10-08T06:33:12.000000000Z',
    finished_at: '2026-10-08T06:33:41.880000000Z',
    files_total: 1200,
    files_copied: 1200,
    bytes_total: 8123456789,
    bytes_copied: 8123456789,
    files_deleted: 1200,
    old_dir_purged: true,
    errors: ['copy 2026/09/report.pdf: no such file or directory'],
    message: '迁移完成：1200 文件 (7.6 GiB)，1 个警告',
  }
}

/** ★★ (1) 只有 `running` 有载荷的 state（照抄 `:414-415`）。 */
function stateWithRunning(run: Record<string, unknown> = runningRunOf()): Record<string, unknown> {
  return { latest: null, running: run }
}

/** ★★ (1) 只有 `latest` 有载荷的 state（照抄 `:416-418`）。 */
function stateWithLatest(run: Record<string, unknown> = purgedRun()): Record<string, unknown> {
  return { latest: run, running: null }
}

/** ★★ (1) 两个槽位都是裸 `null`（照抄 `:409-412` 的初值，`run == nil`）。 */
function emptyState(): Record<string, unknown> {
  return { latest: null, running: null }
}

// ═══════════════════════════════════════════════════════════════════════════
// (1) 顶层形状：2 键裸对象，不是 envelope / 不是数组 / 不是单主键
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移状态 · 顶层形状 (1)', () => {
  it('顶层是数组时抛错（storage_migration.go:409 map 而非 slice）', () => {
    expect(() => unwrapMigrationState([])).toThrow(/响应形状不符：期望裸对象，实得 array/)
  })

  it('顶层是 null 时抛错', () => {
    expect(() => unwrapMigrationState(null)).toThrow(/响应形状不符：期望裸对象，实得 null/)
  })

  it('顶层是字符串时抛错', () => {
    expect(() => unwrapMigrationState('running')).toThrow(/响应形状不符：期望裸对象，实得 string/)
  })

  it('顶层是数字时抛错', () => {
    expect(() => unwrapMigrationState(42)).toThrow(/响应形状不符：期望裸对象，实得 number/)
  })

  // ★★ 下面两条是 #25 那条变异的**区分格**：`v === null` 换成 `!v` 时，
  //   只有「falsy 但不是 null」的那一格（0 / '' / false）才分得开。
  //   ⇒ 只测 42 与 null 是不够的 —— 那两条对两种实现**完全一致**。
  it('顶层是 0 时报 number 而不是 null', () => {
    expect(() => unwrapMigrationState(0)).toThrow(/响应形状不符：期望裸对象，实得 number/)
  })

  it('顶层是空串时报 string 而不是 null', () => {
    expect(() => unwrapMigrationState('')).toThrow(/响应形状不符：期望裸对象，实得 string/)
  })

  it('两槽位皆裸 null 时通过（storage_migration.go:409-412 初值）', () => {
    const s = unwrapMigrationState(emptyState())
    expect(s.running).toBeNull()
    expect(s.latest).toBeNull()
  })

  it('只有 running 有载荷时通过（storage_migration.go:414-415）', () => {
    const s = unwrapMigrationState(stateWithRunning())
    expect(s.running).not.toBeNull()
    expect(s.latest).toBeNull()
  })

  it('只有 latest 有载荷时通过（storage_migration.go:416-418）', () => {
    const s = unwrapMigrationState(stateWithLatest())
    expect(s.latest).not.toBeNull()
    expect(s.running).toBeNull()
  })

  it('路径是 /api/admin/storage/migration-state（handler.go:1111）', () => {
    expect(MIGRATION_STATE_PATH).toBe('/api/admin/storage/migration-state')
  })

  it('顶层两个键恒在（storage_migration.go:410-411）', () => {
    expect(MIGRATION_STATE_KEYS).toEqual(['running', 'latest'])
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (1) 顶层缺键：两条判据的消息必须不同（变异只改一处时才能分辨）
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移状态 · 顶层缺键', () => {
  it('缺 running 时抛错并点名 running', () => {
    const bad = del(emptyState(), 'running')
    expect(() => unwrapMigrationState(bad)).toThrow(/迁移状态 缺 1 个键（running）/)
  })

  it('缺 latest 时抛错并点名 latest', () => {
    const bad = del(emptyState(), 'latest')
    expect(() => unwrapMigrationState(bad)).toThrow(/迁移状态 缺 1 个键（latest）/ as never as never)
  })

  it('两个键都缺时抛错并一次点名两个', () => {
    expect(() => unwrapMigrationState({})).toThrow(/迁移状态 缺 2 个键（running, latest）/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (1) 槽位类型：null 或 run 对象，二选一；两个槽位的判据消息必须不同
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移状态 · 槽位类型 (1)', () => {
  it('running 是数字时抛错并点名 running', () => {
    expect(() => unwrapMigrationState({ latest: null, running: 42 })).toThrow(
      /迁移状态 的 running 响应形状不符：期望裸对象，实得 number/,
    )
  })

  it('latest 是字符串时抛错并点名 latest', () => {
    expect(() => unwrapMigrationState({ latest: 'none', running: null })).toThrow(
      /迁移状态 的 latest 响应形状不符：期望裸对象，实得 string/,
    )
  })

  it('running 是数组时抛错并点名 running', () => {
    expect(() => unwrapMigrationState({ latest: null, running: [] })).toThrow(
      /迁移状态 的 running 响应形状不符：期望裸对象，实得 array/,
    )
  })

  it('latest 是嵌套数组时抛错并点名 latest', () => {
    expect(() => unwrapMigrationState({ latest: [[]], running: null })).toThrow(
      /迁移状态 的 latest 响应形状不符：期望裸对象，实得 array/,
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (4) migrationRun 的 11 个恒在键
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移 run · 11 个恒在键 (4)', () => {
  it('恒在键清单是 11 个（storage_migration.go:49-63 脚本数出）', () => {
    expect(MIGRATION_RUN_KEYS).toHaveLength(11)
    expect(MIGRATION_RUN_KEYS).toEqual([
      'run_id',
      'status',
      'from_dir',
      'to_dir',
      'started_at',
      'files_total',
      'files_copied',
      'bytes_total',
      'bytes_copied',
      'files_deleted',
      'old_dir_purged',
    ])
  })

  it('omitempty 键清单是 4 个', () => {
    expect(MIGRATION_RUN_OPTIONAL_KEYS).toHaveLength(4)
    expect(MIGRATION_RUN_OPTIONAL_KEYS).toEqual(['finished_at', 'heartbeat_at', 'errors', 'message'])
  })

  it('完整的 run 原样通过（storage_migration.go:49-63）', () => {
    const r = unwrapMigrationRun(runningRunOf(), 'r')
    expect(r.run_id).toBe('migration-1759897200123456789')
    expect(r.files_total).toBe(1200)
    expect(r.old_dir_purged).toBe(false)
  })

  it('缺 run_id 时抛错并点名 run_id', () => {
    expect(() => unwrapMigrationRun(del(runningRunOf(), 'run_id'), 'r')).toThrow(/r 缺 1 个键（run_id）/)
  })

  it('缺 old_dir_purged 时抛错并点名 old_dir_purged', () => {
    expect(() => unwrapMigrationRun(del(runningRunOf(), 'old_dir_purged'), 'r')).toThrow(
      /r 缺 1 个键（old_dir_purged）/,
    )
  })

  it('缺 files_total 时抛错并点名 files_total', () => {
    expect(() => unwrapMigrationRun(del(runningRunOf(), 'files_total'), 'r')).toThrow(
      /r 缺 1 个键（files_total）/,
    )
  })

  it('缺 started_at 时抛错并点名 started_at', () => {
    expect(() => unwrapMigrationRun(del(runningRunOf(), 'started_at'), 'r')).toThrow(
      /r 缺 1 个键（started_at）/,
    )
  })

  it('同时缺 status 与 to_dir 时抛错并一次点名两个', () => {
    const bad = del(del(runningRunOf(), 'status'), 'to_dir')
    expect(() => unwrapMigrationRun(bad, 'r')).toThrow(/r 缺 2 个键（status, to_dir）/)
  })

  it('缺 finished_at 等 4 个 omitempty 键时仍通过（storage_migration.go:54-55 62-63）', () => {
    const bare = runningRunOf()
    for (const k of ['finished_at', 'heartbeat_at', 'errors', 'message']) delete bare[k]
    const r = unwrapMigrationRun(bare, 'r')
    expect(r.run_id).toBe('migration-1759897200123456789')
  })

  it('running 槽位里的 run 坏掉时错误消息点名 running', () => {
    expect(() => unwrapMigrationState(stateWithRunning(del(runningRunOf(), 'run_id')))).toThrow(
      /迁移状态 的 running 缺 1 个键（run_id）/,
    )
  })

  it('latest 槽位里的 run 坏掉时错误消息点名 latest', () => {
    expect(() => unwrapMigrationState(stateWithLatest(del(purgedRun(), 'run_id')))).toThrow(
      /迁移状态 的 latest 缺 1 个键（run_id）/,
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (4) 恒在键的类型校验：夹具「键齐全、只错类型」
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移 run · 恒在键类型 (4)', () => {
  it('status 是数字时抛错并点名 status', () => {
    expect(() => unwrapMigrationRun(runningRunOf({ status: 1 as never }), 'r')).toThrow(
      /r 的 status 不是字符串/,
    )
  })

  it('run_id 是数字时抛错并点名 run_id', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), run_id: 1759897200 }, 'r')).toThrow(
      /r 的 run_id 不是字符串/,
    )
  })

  it('from_dir 是数组时抛错并点名 from_dir', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), from_dir: [] }, 'r')).toThrow(
      /r 的 from_dir 不是字符串/,
    )
  })

  it('to_dir 是 null 时抛错并点名 to_dir', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), to_dir: null }, 'r')).toThrow(
      /r 的 to_dir 不是字符串/,
    )
  })

  it('started_at 是布尔时抛错并点名 started_at', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), started_at: true }, 'r')).toThrow(
      /r 的 started_at 不是字符串/,
    )
  })

  it('files_total 是字符串时抛错并点名 files_total', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), files_total: '1200' }, 'r')).toThrow(
      /r 的 files_total 不是数字/,
    )
  })

  it('files_copied 是 null 时抛错并点名 files_copied', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), files_copied: null }, 'r')).toThrow(
      /r 的 files_copied 不是数字/,
    )
  })

  it('bytes_total 是字符串时抛错并点名 bytes_total', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), bytes_total: '8123456789' }, 'r')).toThrow(
      /r 的 bytes_total 不是数字/,
    )
  })

  it('bytes_copied 是布尔时抛错并点名 bytes_copied', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), bytes_copied: false }, 'r')).toThrow(
      /r 的 bytes_copied 不是数字/,
    )
  })

  it('files_deleted 是字符串时抛错并点名 files_deleted', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), files_deleted: '0' }, 'r')).toThrow(
      /r 的 files_deleted 不是数字/,
    )
  })

  it('old_dir_purged 是字符串 false 时抛错并点名 old_dir_purged', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), old_dir_purged: 'false' }, 'r')).toThrow(
      /r 的 old_dir_purged 不是布尔/,
    )
  })

  it('old_dir_purged 是数字 0 时抛错并点名 old_dir_purged', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), old_dir_purged: 0 }, 'r')).toThrow(
      /r 的 old_dir_purged 不是布尔/,
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (4) 4 个 omitempty 键：两个分支（不存在 / 存在但类型错）
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移 run · omitempty 键的两分支 (4)', () => {
  it('errors 是字符串时抛错并点名 errors', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), errors: 'boom' }, 'r')).toThrow(
      /r 的 errors 不是数组/,
    )
  })

  it('errors 是对象时抛错并点名 errors', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), errors: { a: 1 } }, 'r')).toThrow(
      /r 的 errors 不是数组/,
    )
  })

  it('errors 元素是数字时抛错并点名 errors[0]', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), errors: [42] }, 'r')).toThrow(
      /r 的 errors\[0\] 不是字符串/,
    )
  })

  it('errors 第二个元素是 null 时抛错并点名 errors[1]', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), errors: ['ok', null] }, 'r')).toThrow(
      /r 的 errors\[1\] 不是字符串/,
    )
  })

  it('message 是数字时抛错并点名 message', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), message: 7 }, 'r')).toThrow(
      /r 的 message 不是字符串/,
    )
  })

  it('finished_at 是数字时抛错并点名 finished_at', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), finished_at: 1759897200 }, 'r')).toThrow(
      /r 的 finished_at 不是字符串/,
    )
  })

  it('heartbeat_at 是 null 时抛错并点名 heartbeat_at', () => {
    expect(() => unwrapMigrationRun({ ...runningRunOf(), heartbeat_at: null }, 'r')).toThrow(
      /r 的 heartbeat_at 不是字符串/,
    )
  })

  it('4 个 omitempty 键全部在场且类型正确时通过', () => {
    const r = unwrapMigrationRun(purgedRun(), 'r')
    expect(r.finished_at).toBe('2026-10-08T06:33:41.880000000Z')
    expect(r.heartbeat_at).toBe('2026-10-08T06:33:12.000000000Z')
    expect(r.errors).toHaveLength(1)
    expect(r.message).toBe('迁移完成：1200 文件 (7.6 GiB)，1 个警告')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// fetch
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移状态 · fetch', () => {
  it('发 GET 到 /api/admin/storage/migration-state 且不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(stateWithRunning()))
    await fetchStorageMigrationState()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/admin/storage/migration-state')
    expect(url).not.toContain('?')
    expect(init.method).toBe('GET')
  })

  it('响应里的 latest 槽位被原样解出', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(stateWithLatest()))
    const s = await fetchStorageMigrationState()
    expect(s.latest?.run_id).toBe('migration-1759897200111111111')
    expect(s.running).toBeNull()
  })

  it('响应形状不符时 Promise reject（storage_migration.go:409 恒为裸对象）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await expect(fetchStorageMigrationState()).rejects.toThrow(/响应形状不符/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (1)(2) 槽位选择：running 优先于 latest
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移状态 · 有效 run 的选取 (1)(2)', () => {
  it('只有 running 有值时取到 running 那一条', () => {
    const s = unwrapMigrationState(stateWithRunning())
    expect(effectiveMigrationRun(s)?.run_id).toBe('migration-1759897200123456789')
  })

  it('只有 latest 有值时取到 latest 那一条', () => {
    const s = unwrapMigrationState(stateWithLatest())
    expect(effectiveMigrationRun(s)?.run_id).toBe('migration-1759897200111111111')
  })

  it('两槽位皆 null 时取到 null（storage_migration.go:111 return nil）', () => {
    expect(effectiveMigrationRun(unwrapMigrationState(emptyState()))).toBeNull()
  })

  it('空 state 判定为 true（storage_migration.go:409-412 初值）', () => {
    expect(migrationStateIsEmpty(unwrapMigrationState(emptyState()))).toBe(true)
  })

  it('有 running 时空 state 判定为 false', () => {
    expect(migrationStateIsEmpty(unwrapMigrationState(stateWithRunning()))).toBe(false)
  })

  it('只有 latest 时空 state 判定为 false（不能只看 running）', () => {
    expect(migrationStateIsEmpty(unwrapMigrationState(stateWithLatest()))).toBe(false)
  })

  // ★ 这条同时是**类型级**判据：两个槽位的类型必须是 `MigrationRun | null`
  //   （storage_migration.go:410-411 的初值就是 nil）而不是 `MigrationRun`。
  //   写成 `const running: MigrationRun` 会让 `vue-tsc` 直接报错 —— 那就是它的牙。
  it('解包结果的两个槽位类型是 MigrationRun | null', () => {
    const s: MigrationStateResponse = unwrapMigrationState(stateWithRunning())
    const running: MigrationRun | null = s.running
    const latest: MigrationRun | null = s.latest
    expect(running?.status).toBe('running')
    expect(latest).toBeNull()
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 轮询策略
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移状态 · 轮询策略', () => {
  it('从未迁移过（两槽位皆 null）时不轮询（否则永远转圈）', () => {
    expect(shouldPollMigration(unwrapMigrationState(emptyState()))).toBe(false)
  })

  it('复制中时继续轮询（storage_migration.go:133 status=running）', () => {
    expect(shouldPollMigration(unwrapMigrationState(stateWithRunning()))).toBe(true)
  })

  it('最新一次已成功时不轮询（storage_migration.go:200 finishMigration succeeded）', () => {
    expect(shouldPollMigration(unwrapMigrationState(stateWithLatest(purgedRun())))).toBe(false)
  })

  it('最新一次已失败时不轮询（storage_migration.go:179 finishMigration failed）', () => {
    expect(shouldPollMigration(unwrapMigrationState(stateWithLatest(collectFailedRun())))).toBe(false)
  })

  it('running 槽位的 run 终态判定为 false（storage_migration.go:133）', () => {
    expect(migrationRunIsTerminal(unwrapMigrationState(stateWithRunning()).running!)).toBe(false)
  })

  it('succeeded 槽位的 run 终态判定为 true', () => {
    expect(migrationRunIsTerminal(unwrapMigrationState(stateWithLatest()).latest!)).toBe(true)
  })

  it('failed 槽位的 run 终态判定为 true', () => {
    const s = unwrapMigrationState(stateWithLatest(collectFailedRun()))
    expect(migrationRunIsTerminal(s.latest!)).toBe(true)
  })

  it('终态取值清单是 2 个（storage_migration.go:43-44）', () => {
    expect(MIGRATION_TERMINAL_STATUSES).toEqual(['succeeded', 'failed'])
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (3) status 的可达取值只有 3 个
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移 run · status 可达域 (3)', () => {
  it('可达取值清单是 3 个且不含 idle（storage_migration.go:41 idle 无赋值点）', () => {
    expect(MIGRATION_STATUSES).toHaveLength(3)
    expect(MIGRATION_STATUSES).toEqual(['running', 'succeeded', 'failed'])
    expect(MIGRATION_STATUSES as readonly string[]).not.toContain('idle')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (9) 进度比：total === 0 时给 0，不是 NaN
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移 run · 进度比 (9)', () => {
  it('复制中 480/1200 得到 0.4（storage_migration.go:203 239）', () => {
    const s = unwrapMigrationState(stateWithRunning())
    expect(migrationFileProgress(s.running!)).toBeCloseTo(0.4, 10)
  })

  it('★ 空目录成功迁移 files_total 为 0 时进度是 0 而不是 NaN（storage_migration.go:184）', () => {
    const s = unwrapMigrationState(stateWithLatest(emptyDirRunOf(true)))
    const p = migrationFileProgress(s.latest!)
    expect(p).toBe(0)
    expect(Number.isNaN(p)).toBe(false)
  })

  it('★ 零文件的 run 一律给出有限的进度比（storage_migration.go:184-185）', () => {
    const s = unwrapMigrationState(stateWithLatest(emptyDirRunOf(false)))
    expect(Number.isFinite(migrationFileProgress(s.latest!))).toBe(true)
    expect(Number.isFinite(migrationByteProgress(s.latest!))).toBe(true)
  })

  it('一个文件都还没复制时进度是 0', () => {
    const s = unwrapMigrationState(stateWithRunning(runningRunOf({ files_copied: 0 })))
    expect(migrationFileProgress(s.running!)).toBe(0)
  })

  it('全部复制完成时进度是 1', () => {
    const s = unwrapMigrationState(stateWithLatest(purgedRun()))
    expect(migrationFileProgress(s.latest!)).toBe(1)
  })

  it('字节进度 3100000000/8123456789（storage_migration.go:204 237）', () => {
    const s = unwrapMigrationState(stateWithRunning())
    expect(migrationByteProgress(s.running!)).toBeCloseTo(3100000000 / 8123456789, 12)
  })

  it('★ 空目录 bytes_total 为 0 时字节进度是 0 而不是 NaN（storage_migration.go:185）', () => {
    const s = unwrapMigrationState(stateWithLatest(emptyDirRunOf(true)))
    const p = migrationByteProgress(s.latest!)
    expect(p).toBe(0)
    expect(Number.isNaN(p)).toBe(false)
  })

  it('字节全部复制完成时进度是 1', () => {
    const s = unwrapMigrationState(stateWithLatest(purgedRun()))
    expect(migrationByteProgress(s.latest!)).toBe(1)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (7) 清理完成的判断：files_deleted === files_total 推不出「已删」
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移 run · 清理判断 (7)', () => {
  it('★ 空目录 + 迁移期间有新写入 ⇒ files_deleted === files_total 但目录没删（storage_migration.go:193-196）', () => {
    // ★★★ 这一格就是「files_deleted === files_total 推不出已清理」的反例：
    //   FilesTotal=0、FilesDeleted 从未赋值仍是 0、OldDirPurged 保持 false。
    const s = unwrapMigrationState(stateWithLatest(emptyDirRunOf(false)))
    const run = s.latest!
    expect(run.files_deleted).toBe(run.files_total)
    expect(run.old_dir_purged).toBe(false)
    expect(migrationPurgeLooksComplete(run)).toBe(false)
  })

  it('非空目录且已清理时弱信号为 true（storage_migration.go:277）', () => {
    const s = unwrapMigrationState(stateWithLatest(purgedRun()))
    expect(migrationPurgeLooksComplete(s.latest!)).toBe(true)
  })

  it('空目录且已删除时弱信号为 false —— 计数器给不出信息（storage_migration.go:197-198）', () => {
    const s = unwrapMigrationState(stateWithLatest(emptyDirRunOf(true)))
    expect(s.latest!.old_dir_purged).toBe(true)
    expect(s.latest!.files_deleted).toBe(0)
    expect(migrationPurgeLooksComplete(s.latest!)).toBe(false)
  })

  // ★ 按纪律删掉的两条恒真用例（原先在此）：
  //   「复制中 files_deleted 为 0 时弱信号为 false」
  //   「非空目录未清理时 files_deleted 保持 0」
  //   ⇒ `files_deleted > 0` 只在 `:277` 的 `purged == true` 分支里被赋成 `FilesTotal`，
  //     而同一个分支把 `OldDirPurged` 也设成 true ⇒ ⇒
  //     **「files_deleted > 0 但 old_dir_purged 为 false」在真后端上不可达** ⇒
  //     两种实现对所有可达输入完全一致 ⇒ 保留它们只会给人「这条有牙」的错觉。
  //   ⇒ 对应的变异 #62（只看计数器、丢掉权威标志）**同属可证等价，已从变异表删除**。
})

// ═══════════════════════════════════════════════════════════════════════════
// (5) failed 的 run 可能没有 errors 键
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移 run · 失败细节 (5)', () => {
  it('★ 收集文件失败时 errors 键缺失、细节只在 message（storage_migration.go:179）', () => {
    const s = unwrapMigrationState(stateWithLatest(collectFailedRun()))
    const run = s.latest!
    expect(run.status).toBe('failed')
    expect('errors' in run).toBe(false)
    expect(migrationRunHasErrorDetail(run)).toBe(false)
  })

  it('★ 失败但有 errors 键时细节可用（storage_migration.go:226 复制失败会 append）', () => {
    const copyFailed = {
      ...runningRunOf(),
      status: 'failed',
      finished_at: '2026-10-08T03:01:02.000000000Z',
      errors: ['copy 2026/09/report.pdf: no such file or directory'],
      message: '复制失败率过高 (100.0%, 1200/1200)',
    }
    const s = unwrapMigrationState(stateWithLatest(copyFailed))
    expect(migrationRunHasErrorDetail(s.latest!)).toBe(true)
  })

  it('★ 校验失败也没有 errors 键（storage_migration.go:257 同样只写 Message）', () => {
    // ★ 显式标注 Record：对象字面量展开后 TS 只保留写出的那几个键的精确类型，
    //   `delete obj['errors']` 会因此报 TS7053（★ 类型门在编译期就抓到了它）。
    const verifyFailed: Record<string, unknown> = { ...purgedRun(), status: 'failed', old_dir_purged: false, files_deleted: 0 }
    delete verifyFailed['errors']
    const s = unwrapMigrationState(stateWithLatest(verifyFailed))
    expect('errors' in s.latest!).toBe(false)
    expect(migrationRunHasErrorDetail(s.latest!)).toBe(false)
  })

  it('空间不足提前终止时 errors 有内容（storage_migration.go:228-230）', () => {
    const noSpace = {
      ...runningRunOf(),
      status: 'failed',
      finished_at: '2026-10-08T03:00:12.000000000Z',
      errors: ['copy 2026/09/big.iso: no space left on device'],
      message: '目标磁盘空间不足',
    }
    const s = unwrapMigrationState(stateWithLatest(noSpace))
    expect(migrationRunHasErrorDetail(s.latest!)).toBe(true)
    expect(s.latest!.message).toBe('目标磁盘空间不足')
  })

  it('★ 复制中的 run 也有 errors 键（storage_migration.go:226 边跑边 append）', () => {
    const s = unwrapMigrationState(stateWithRunning())
    expect(migrationRunHasErrorDetail(s.running!)).toBe(true)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 上游 migration_run_id 关联
// ═══════════════════════════════════════════════════════════════════════════

describe('迁移状态 · 上游 run_id 关联', () => {
  it('latest 的 run_id 命中上游 migration_run_id 时关联成功（storage_config.go:346）', () => {
    const s = unwrapMigrationState(stateWithLatest())
    expect(migrationStateContainsRunId(s, 'migration-1759897200111111111')).toBe(true)
  })

  it('running 的 run_id 命中时关联成功', () => {
    const s = unwrapMigrationState(stateWithRunning())
    expect(migrationStateContainsRunId(s, 'migration-1759897200123456789')).toBe(true)
  })

  it('run_id 不同则关联失败', () => {
    const s = unwrapMigrationState(stateWithLatest())
    expect(migrationStateContainsRunId(s, 'migration-9999999999999999999')).toBe(false)
  })

  it('★ 两槽位皆 null 时关联失败而不是误判为 true（storage_migration.go:111）', () => {
    const s = unwrapMigrationState(emptyState())
    expect(migrationStateContainsRunId(s, 'migration-1759897200111111111')).toBe(false)
  })
})
