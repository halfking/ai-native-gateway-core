import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import StorageMigrationStateView from './StorageMigrationStateView.vue'
import { t } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import {
  fetchStorageMigrationState,
  migrationStateRoleNeedsAdminGate,
  migrationStateCarriesNoTenantDimension,
  shouldPollMigration,
  migrationStateIsEmpty,
  MIGRATION_POLL_INTERVAL_MS,
  MIGRATION_POLL_MAX,
  type MigrationStateResponse,
  type MigrationRun,
} from '@/api/storageMigrationState'

/**
 * 附件目录迁移进度面回归护栏（2026-10-15，第一百二十五批）。
 *
 * ★★★★★ 本族与第一百二十四批**结论相反，而且只隔一行** ——
 *   这是九批以来第一次出现「同一族两个模块，档位由注册处那一行决定」。
 *
 * | 行 | 端点 | 档位 | 前端那一层是什么 |
 * |---|---|---|---|
 * | `:1129` | `storage/config`（批 124）| `h.superAdmin` ⇒ **服务端是真门** | **体验兜底** |
 * | `:1132` | `storage/migration-state`（**本族**）| `admin(...)` ⇒ **服务端没有门** | **唯一的门** |
 *
 * ⇒ ★★ 「相邻两行两种档位」这件事在批 124 只是**警示**，
 *   在本族是**本族结论的直接依据**。
 *
 * ★★★★ `'admin'` 档的**两条独立理由**（缺一不可）：
 *   （甲）**不过度限制**：`admin/storage_migration.go` 整个文件 `grep -c tenant` = 0
 *        ⇒ 载荷是本机单例的迁移进度，不含任何租户的数据。
 *   （乙）**需要 admin**：`AdminMiddleware` 全文无任何 `claims.Role` 比对
 *        + `pkg/identity` 的 `supportedRole` **接受 `"user"`**
 *        ⇒ 任何已认证的普通用户都能调到这个端点。
 *
 * ★★★ 本族最要紧的三件事（都来自模块头，读 Go 源码逐字核过）：
 *   (9) **进度比必须处理除零** —— 空目录迁移是「成功但零进度」的**合法形状**，
 *       `0/0 = NaN` 喂给进度条会渲染成空白 / `NaN%`。
 *   (7) **`old_dir_purged` 才是权威** —— `files_deleted === files_total` 推不出
 *       「已清理」（空目录时 `0 === 0` 成立但目录没删）。
 *   (10) **没有「卡住」信号** —— 心跳停更在本响应里查不出来
 *       ⇒ 轮询**必须有上限**，这是协议缺口的唯一补丁。
 *
 * 判据钉六组：
 *  ① ★★★★★★ 权限档 `'admin'`（服务端**没有**门）+ 相邻两行两种档位。
 *  ② ★★★★★ 有效 run 的取法（`running` 优先）+ 空态。
 *  ③ ★★★★★ 进度比除零（空目录成功态）。
 *  ④ ★★★★ 清理判定：`old_dir_purged` 权威，`files_deleted` 不作数。
 *  ⑤ ★★★★★ 轮询：三种「必须停」+ 卸载清理。
 *  ⑥ ★★★★ 接线与编码门（含 admin 档**不得**进 superAdmin 白名单）。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function run(over: Partial<MigrationRun> = {}): MigrationRun {
  return {
    run_id: 'migration-1700000000000000000',
    status: 'succeeded',
    from_dir: '/data/attachments',
    to_dir: '/mnt/big/attachments',
    started_at: '2026-10-15T01:00:00.123456789Z',
    finished_at: '2026-10-15T01:00:12Z',
    heartbeat_at: '2026-10-15T01:00:11Z',
    files_total: 100,
    files_copied: 100,
    bytes_total: 1000,
    bytes_copied: 1000,
    files_deleted: 100,
    old_dir_purged: true,
    ...over,
  } as unknown as MigrationRun
}

function state(over: Partial<MigrationStateResponse> = {}): MigrationStateResponse {
  return { running: null, latest: null, ...over } as MigrationStateResponse
}

// ── 装配 ────────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/storageMigrationState', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/storageMigrationState')>()
  return { ...actual, fetchStorageMigrationState: vi.fn() }
})

const mFetch = fetchStorageMigrationState as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(StorageMigrationStateView)
  await flushPromises()
  return w
}
function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}
function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}
function textOf(w: W, id: string): string {
  return byId(w, id).text()
}
function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/StorageMigrationStateView.vue'), 'utf8')
}
/**
 * ★★★ 剥掉行注释与块注释，只留**代码**。
 *
 * ★★★ 本批沿用上一批的教训：否定断言若直接扫源码，会**误红自己的注释** ——
 *   视图注释里为了说明「某个恒真 computed 被删过」而原样写出了那个形态。
 * ⚠️★★ 本函数的注释里**不贴**「块注释结束符」的字面量：
 *   它会提前闭合本注释，报出的却是**下一行**的语法错误。
 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}
const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

/** ★ 逐段取值；`toHaveProperty('a.b')` 会被当成一个带点的**单段**键而失配。 */
function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}
/** ★ 带接收者时只停在「下一个同接收者的方法」（批 122 的教训）。 */
function goFunc(src: string, name: string): string {
  const lines = src.split('\n')
  const isMethod = lines.some((l) => l.startsWith(`func (h *Handler) ${name}(`))
  const start = lines.findIndex(
    (l) => l.startsWith(`func (h *Handler) ${name}(`) || l.startsWith(`func ${name}(`),
  )
  expect(start, `没找到 ${name} 的定义（正则失配会让这条恒绿）`).toBeGreaterThan(-1)
  const stopRe = isMethod ? /^func \(h \*Handler\) / : /^func /
  let stop = lines.length
  for (let i = start + 1; i < lines.length; i += 1) {
    if (stopRe.test(lines[i] ?? '')) {
      stop = i
      break
    }
  }
  return lines.slice(start, stop).join('\n')
}
async function click(w: W, id: string): Promise<void> {
  await byId(w, id).trigger('click')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.useRealTimers()
  authState.role = 'super_admin'
  mFetch.mockResolvedValue(state({ latest: run() }))
})

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档 'admin'：服务端**没有**门，前端这一层是唯一的门
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档「admin」（服务端无角色下限 ⇒ 前端是唯一的门）', () => {
  it('①-1 ★★★★★★ 抽屉席存在、路径正确、且设 admin', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'storage-migration-state')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/storage-migration-state')
    expect(seat!.requiresRole).toBe('admin')
    expect(seat!.titleKey).toBe('nav.storageMigrationState')
  })

  it('①-2 ★★★★★★ 抽屉席真的被 navItemsFor 挡住：三种管理档可见、`user` 不可见', () => {
    // ★ 只断言「DRAWER_NAV 里有这条数据」只证明**写进去了**，
    //   不证明**过滤器按 requiresRole 真的把它挡了** ⇒ 打行为面。
    const seen = (role: string) =>
      navItemsFor(DRAWER_NAV, role).some((i) => i.key === 'storage-migration-state')
    expect(seen('super_admin')).toBe(true)
    expect(seen('tenant_admin')).toBe(true)
    expect(seen('admin_key')).toBe(true)
    // ★★★ 这一条是本族与批 124 的**分界**：批 124 的席 `user` 看不见是因为
    //   **服务端也会挡**；本族服务端**不挡** ⇒ 抽屉席这一挡是**唯一的**。
    expect(seen('user')).toBe(false)
    expect(seen('operator_user')).toBe(false)
  })

  it('①-3 ★★★★★★ admin 档**不得**进 superAdmin 白名单（那是 tenant_admin 掉的那批）', () => {
    const src = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    const at = (k: string) => src.indexOf(`'${k}',`)
    expect(at('storage-migration-state'), 'admin 档进了 superAdmin 白名单 ⇒ 性质搞错了').toBe(-1)
    // ★ 对照：批 124 的 super_admin 席**在**白名单里。
    expect(at('storage-log-config')).toBeGreaterThan(-1)
  })

  it('①-4 ★★★★★★ 读 Go 源码：`admin(...)` 展开后**没有任何角色下限**', () => {
    // ★★★ 这一条是本族**最要紧**的事实侧：若哪天 AdminMiddleware 加了 role 比对，
    //   「前端是唯一的门」这句话就不成立，权限档要重判。
    const alias = goSrc('admin/handler.go').split('\n').find((l) => /^\s*admin := h\.admin\s*$/.test(l))
    expect(alias, '没找到 `admin := h.admin` 别名').toBeTruthy()

    const body = goFunc(goSrc('admin/auth.go'), 'AdminMiddleware')
    expect(body).toMatch(/func AdminMiddleware\(next http\.HandlerFunc/)
    // ★★ 断言「没有」要否的是**角色比对**这个形态，而不是某个词
    //   （`Role: p.Role` 这种赋值是合法存在的，写进注释也是合法的）。
    expect(body).not.toMatch(/claims\.Role\s*(==|!=)/)
    expect(body).not.toMatch(/StatusForbidden.*super_admin/)
    // ⇒ supportedRole 接受 "user" ⇒ 已认证的普通用户就能过 AdminMiddleware。
    const sup = goFunc(goSrc('pkg/identity/verifier.go'), 'supportedRole')
    expect(sup).toMatch(/case "user"/)
  })

  it('①-5 ★★★★★★ 读 Go 源码：`:1129`（superAdmin）与 `:1132`（admin）相邻两行两种档位', () => {
    // ★★★ 本族结论的直接依据：档位由**那一行**决定，不由前缀决定。
    const lines = goSrc('admin/handler.go').split('\n')
    const mig = lines.findIndex((l) => l.includes('"/api/admin/storage/migration-state"'))
    expect(mig + 1, '模块头原写 :1111（整块漂 21 行）').toBe(1132)
    expect(lines[mig]).toMatch(/admin\(h\.handleMigrationState\)/)
    const cfg = lines.findIndex((l) => l.includes('"/api/admin/storage/config"'))
    expect(lines[cfg]).toMatch(/h\.superAdmin\(h\.handleStorageConfig\)/)
    // ★ 相邻：1129 与 1132 之间只隔着 test-path 那一行
    expect(cfg).toBeLessThan(mig)
    expect(mig - cfg).toBeLessThanOrEqual(3)
  })

  it('①-6 ★★★★★★ 零租户维度 = 「不过度限制」的**依据**，不是「结论」', () => {
    // ★★★ 这条不是恒真的装饰：判别条件是「响应里没有租户键」，
    //   哪天后端给迁移状态加了 per-tenant 拆分，这条会红 ⇒ 权限档要重判。
    expect(migrationStateCarriesNoTenantDimension(state({ latest: run() }))).toBe(true)
    expect(
      migrationStateCarriesNoTenantDimension(state({ running: run({ status: 'running' }) })),
    ).toBe(true)
    expect(migrationStateCarriesNoTenantDimension(state({ latest: { tenant_id: 't1' } as never }))).toBe(false)
  })

  it('①-7 ★★★★★★ 角色门判据随 role 变（不是恒真判据）', () => {
    expect(migrationStateRoleNeedsAdminGate('user')).toBe(true)
    expect(migrationStateRoleNeedsAdminGate('operator_user')).toBe(true)
    expect(migrationStateRoleNeedsAdminGate(null)).toBe(true)
    expect(migrationStateRoleNeedsAdminGate(undefined)).toBe(true)
    expect(migrationStateRoleNeedsAdminGate('super_admin')).toBe(false)
    expect(migrationStateRoleNeedsAdminGate('tenant_admin')).toBe(false)
    expect(migrationStateRoleNeedsAdminGate('admin_key')).toBe(false)
  })

  it('①-8 ★★★★★★ Go 源文件里 tenant 命中数 = 0（甲条理由的字面证据）', () => {
    const src = goSrc('admin/storage_migration.go')
    const hits = (src.match(/tenant/gi) ?? []).length
    expect(hits, '迁移状态里出现了 tenant ⇒ 载荷含别人的数据 ⇒ 该考虑 super_admin 了').toBe(0)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② 有效 run 的取法 + 空态
// ══════════════════════════════════════════════════════════════════════════

describe('② 「当前有效的那个」run 与空态', () => {
  it('②-1 ★★★★★★ `running` 槽位优先（只读 latest 的客户端会漏掉正在跑的迁移）', async () => {
    mFetch.mockResolvedValue(
      state({
        running: run({ status: 'running', run_id: 'migration-RUN' }),
        latest: run({ status: 'succeeded', run_id: 'migration-LATEST' }),
      }),
    )
    const w = await mountView()
    await click(w, 'sms-load')
    expect(textOf(w, 'sms-run-id')).toBe('migration-RUN')
    expect(hasId(w, 'sms-running')).toBe(true)
  })

  it('②-2 ★★★★★★ 两个槽位都为 null ⇒ 空态，且不渲染任何 run 字段', async () => {
    mFetch.mockResolvedValue(state())
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-empty')).toBe(true)
    expect(textOf(w, 'sms-empty')).toBe(t('sms.empty'))
    expect(hasId(w, 'sms-run-id')).toBe(false)
    expect(hasId(w, 'sms-from')).toBe(false)
  })

  it('②-3 ★★★★★ `latest` 有值但没有 `running` ⇒ 正常显示终态，不显示「运行中」', async () => {
    mFetch.mockResolvedValue(state({ latest: run({ status: 'succeeded' }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-running')).toBe(false)
    expect(hasId(w, 'sms-terminal')).toBe(true)
    expect(textOf(w, 'sms-status')).toBe('succeeded')
  })

  it('②-4 ★★★★★ `migrationStateIsEmpty` 直测：两槽位都 null 才真（真查表）', () => {
    // ★ 与 ②-2 的区别：②-2 钉的是**页面**，这条钉的是 **helper 本身**。
    //   只从页面反推的话，helper 被改成恒真时页面那几条仍可能绿
    //   （因为空态那侧本来就只会在「都 null」时渲染）。
    expect(migrationStateIsEmpty(state())).toBe(true)
    expect(migrationStateIsEmpty(state({ running: run({ status: 'running' }) }))).toBe(false)
    expect(migrationStateIsEmpty(state({ latest: run() }))).toBe(false)
  })

  it('②-5 ★★★★ 空态与「有值」是互斥的（v-if 不能同时成立）', async () => {
    mFetch.mockResolvedValue(state())
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-empty')).toBe(true)
    expect(hasId(w, 'sms-purged')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ 进度比除零（模块头 (9)）
// ══════════════════════════════════════════════════════════════════════════

describe('③ 进度比必须处理除零', () => {
  it('③-1 ★★★★★★ 空目录成功态（0/0）⇒ 0%，绝不是 NaN%', async () => {
    // ★★★ 这是模块头 (9) 钉死的**合法成功形状**：
    //   `files_total === 0` 且 `files_copied` 从未赋值（保持 0）而终态是 succeeded。
    mFetch.mockResolvedValue(
      state({
        latest: run({
          status: 'succeeded',
          files_total: 0,
          files_copied: 0,
          bytes_total: 0,
          bytes_copied: 0,
          files_deleted: 0,
          old_dir_purged: false,
        }),
      }),
    )
    const w = await mountView()
    await click(w, 'sms-load')
    expect(textOf(w, 'sms-file-pct')).toBe('0%')
    expect(textOf(w, 'sms-byte-pct')).toBe('0%')
    // ★★★ 进度条宽度不能是 NaN%——那会让 CSS 失效、条子消失或撑破容器
    const bar = w.find('.sms__bar')
    expect(bar.attributes('style')).not.toMatch(/NaN|Infinity/)
    expect(w.html()).not.toContain('NaN')
  })

  it('③-2 ★★★★ 部分进度 ⇒ 整数百分比，且宽度与它一致', async () => {
    mFetch.mockResolvedValue(
      state({
        latest: run({
          status: 'running',
          files_total: 200,
          files_copied: 50,
          bytes_total: 1000,
          bytes_copied: 250,
        }),
      }),
    )
    const w = await mountView()
    await click(w, 'sms-load')
    expect(textOf(w, 'sms-file-pct')).toBe('25%')
    expect(textOf(w, 'sms-byte-pct')).toBe('25%')
    expect(textOf(w, 'sms-file-counts')).toBe('50 / 200')
    const styles = w.findAll('.sms__bar').map((b) => b.attributes('style'))
    expect(styles.every((s) => s?.includes('width: 25%'))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④ 清理判定：old_dir_purged 权威（模块头 (7)）
// ══════════════════════════════════════════════════════════════════════════

describe('④ 「旧目录已清理」的权威判据', () => {
  it('④-1 ★★★★★★ `files_deleted === files_total` 但 `old_dir_purged === false` ⇒ 显示「未清理」', async () => {
    // ★★★★ 模块头 (7) 的核心：**0 === 0 成立但目录没删**。
    //   空目录 + 迁移期间有新写入 ⇒ `:193-196` 跳过删除。
    mFetch.mockResolvedValue(
      state({
        latest: run({
          status: 'succeeded',
          files_total: 0,
          files_copied: 0,
          files_deleted: 0,
          old_dir_purged: false,
        }),
      }),
    )
    const w = await mountView()
    await click(w, 'sms-load')
    expect(textOf(w, 'sms-purged')).toBe(t('sms.purge.no'))
    expect(textOf(w, 'sms-purge-note')).toBe(t('sms.purge.note'))
  })

  it('④-2 ★★★★★ `old_dir_purged === true` ⇒ 显示「已清理」', async () => {
    mFetch.mockResolvedValue(state({ latest: run({ old_dir_purged: true, files_deleted: 100 }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(textOf(w, 'sms-purged')).toBe(t('sms.purge.yes'))
  })

  it('④-3 ★★★★ `files_deleted` 照样展示，但页面上说清它不作数', async () => {
    // ★ 不藏字段（那是替用户做决定），但必须说清权威在 `old_dir_purged`。
    mFetch.mockResolvedValue(state({ latest: run({ files_deleted: 7, files_total: 100, old_dir_purged: true }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(textOf(w, 'sms-files-deleted')).toBe('7')
    expect(t('sms.purge.note')).toMatch(/old_dir_purged/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑤ 轮询：三种「必须停」+ 卸载清理
// ══════════════════════════════════════════════════════════════════════════

describe('⑤ 轮询', () => {
  it('⑤-1 ★★★★★★ 从未迁移过 ⇒ **不轮询**（否则在一个没迁移过的网关上永远转圈）', async () => {
    vi.useFakeTimers()
    mFetch.mockResolvedValue(state())
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-polling')).toBe(false)
    const before = mFetch.mock.calls.length
    await vi.advanceTimersByTimeAsync(MIGRATION_POLL_INTERVAL_MS * 5)
    expect(mFetch.mock.calls.length).toBe(before)
    vi.useRealTimers()
  })

  it('⑤-2 ★★★★★ 终态 ⇒ **不轮询**（重试循环不会把 latest 变回 running）', async () => {
    vi.useFakeTimers()
    mFetch.mockResolvedValue(state({ latest: run({ status: 'succeeded' }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-polling')).toBe(false)
    const before = mFetch.mock.calls.length
    await vi.advanceTimersByTimeAsync(MIGRATION_POLL_INTERVAL_MS * 5)
    expect(mFetch.mock.calls.length).toBe(before)
    vi.useRealTimers()
  })

  it('⑤-3 ★★★★★ 运行中 ⇒ 轮询，并显示「轮询中」徽标', async () => {
    vi.useFakeTimers()
    mFetch.mockResolvedValue(state({ running: run({ status: 'running' }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-polling')).toBe(true)
    const before = mFetch.mock.calls.length
    await vi.advanceTimersByTimeAsync(MIGRATION_POLL_INTERVAL_MS * 3)
    expect(mFetch.mock.calls.length).toBeGreaterThan(before)
    vi.useRealTimers()
  })

  it('⑤-4 ★★★★★★ 轮询有**次数上限**，打满后停并报「可能卡住」（模块头 (10)）', async () => {
    // ★★★ 上限不是装饰：卡死时 `status` 仍是 `running`、
    //   `shouldPollMigration` 永远为真 ⇒ 没有上限就是永久后台请求。
    vi.useFakeTimers()
    mFetch.mockResolvedValue(state({ running: run({ status: 'running' }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    await vi.advanceTimersByTimeAsync(MIGRATION_POLL_INTERVAL_MS * (MIGRATION_POLL_MAX + 2))
    expect(hasId(w, 'sms-poll-timeout')).toBe(true)
    expect(hasId(w, 'sms-polling')).toBe(false)
    const after = mFetch.mock.calls.length
    await vi.advanceTimersByTimeAsync(MIGRATION_POLL_INTERVAL_MS * 5)
    expect(mFetch.mock.calls.length, '打满后还在轮询').toBe(after)
    vi.useRealTimers()
  })

  it('⑤-5 ★★★★★★ 卸载必须清掉定时器（否则手机上永远发请求）', async () => {
    vi.useFakeTimers()
    mFetch.mockResolvedValue(state({ running: run({ status: 'running' }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(vi.getTimerCount()).toBeGreaterThan(0)
    w.unmount()
    expect(vi.getTimerCount(), '卸载后还留着轮询定时器').toBe(0)
    vi.useRealTimers()
  })

  it('⑤-6 ★★★★ 轮询途中的一次失败**不抹掉**已有状态（否则用户以为迁移凭空消失）', async () => {
    vi.useFakeTimers()
    mFetch.mockResolvedValueOnce(state({ running: run({ status: 'running' }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-running')).toBe(true)
    mFetch.mockRejectedValue(new Error('network down'))
    await vi.advanceTimersByTimeAsync(MIGRATION_POLL_INTERVAL_MS * 2)
    expect(hasId(w, 'sms-running'), '一次网络抖动把「正在迁移」抹成了空态').toBe(true)
    expect(hasId(w, 'sms-empty')).toBe(false)
    vi.useRealTimers()
  })

  it('⑤-7 ★★★★ 首次取数失败才报错（此前没有任何数据可显示）', async () => {
    mFetch.mockRejectedValue(new Error('boom'))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-error')).toBe(true)
    expect(hasId(w, 'sms-empty')).toBe(false)
  })

  it('⑤-8 ★★★★ `shouldPollMigration` 三种输入三种输出（真查表，不是恒真）', () => {
    expect(shouldPollMigration(state())).toBe(false)
    expect(shouldPollMigration(state({ latest: run({ status: 'succeeded' }) }))).toBe(false)
    expect(shouldPollMigration(state({ latest: run({ status: 'failed' }) }))).toBe(false)
    expect(shouldPollMigration(state({ running: run({ status: 'running' }) }))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 深链兜底 / 只读面 / 接线与编码门
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 深链兜底、只读面与接线', () => {
  it('⑥-1 ★★★★★★ 非管理员 ⇒ 显示 nope，且一次请求都不发', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(hasId(w, 'sms-nope')).toBe(true)
    expect(hasId(w, 'sms-load')).toBe(false)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('⑥-2 ★★★★★★ nope 页面说清「这一层是唯一的门」—— 后端并不挡', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(textOf(w, 'sms-nope-msg')).toBe(t('sms.nope.msg'))
    expect(textOf(w, 'sms-nope-why')).toBe(t('sms.nope.why'))
    // ★★★ 与批 124 的 nope 文案**方向相反**：
    //   批 124 说「服务端会 403」（那一层是体验兜底），
    //   本族必须说「后端不挡、我们挡」⇒ **文案里出现 403 就是错的**。
    //
    // ⚠️★★ 首跑就红在这里：我写成 `expect(t(...)).toMatch(/后端|服务端/)`，
    //   而 `t()` 取的是**当前 locale**（默认 en）⇒ **判据自己押了一种语言**。
    //   ⇒ 正确写法：**两个语种各查各的关键词**，且「不得出现 403」对两侧都成立。
    const zhWhy = dictLookup(DICT['zh-CN'], 'sms.nope.why') as string
    const enWhy = dictLookup(DICT['en-US'], 'sms.nope.why') as string
    for (const [loc, v] of [['zh-CN', zhWhy], ['en-US', enWhy]] as const) {
      expect(v, `${loc} 的 nope 文案声称了 403（本族服务端并不挡）`).not.toContain('403')
    }
    expect(zhWhy).toMatch(/后端/)
    expect(enWhy).toMatch(/backend/i)
    // ★★ 两条都要点明「后端不校验角色」这个**事实**，而不只是「我们挡了」。
    expect(zhWhy).toMatch(/角色/)
    expect(enWhy).toMatch(/role/i)
  })

  it('⑥-3 ★★★★★ 主页面也说明「这一层是唯一的门」（别让人以为后端也在挡）', async () => {
    const w = await mountView()
    await click(w, 'sms-load')
    expect(textOf(w, 'sms-gate-note')).toBe(t('sms.gate.note'))
  })

  it('⑥-3b ★★★★ 页面把「不过度限制」的依据摆出来（零租户维度）', async () => {
    // ★★ 这条不是装饰：`sms-no-tenant` 是本族**不是 superAdmin 席**的可见依据
    //   （批 123 的同款做法）。它由随响应变化的 helper 驱动，不是恒真。
    const w = await mountView()
    await click(w, 'sms-load')
    expect(textOf(w, 'sms-no-tenant')).toBe(t('sms.tenant.none'))
    expect(t('sms.tenant.none')).toMatch(/租户|tenant/i)
  })

  it('⑥-4 ★★★★ 页面声明「只读」：本页没有任何写控件', async () => {
    const w = await mountView()
    expect(textOf(w, 'sms-readonly-note')).toBe(t('sms.readonly.note'))
    const html = w.html()
    for (const verb of ['POST', 'PUT', 'DELETE', 'PATCH']) {
      expect(html, `页面里出现了 ${verb}`).not.toContain(verb)
    }
    expect(w.findAll('input')).toHaveLength(0)
    expect(w.findAll('form')).toHaveLength(0)
  })

  it('⑥-5 ★★★★★★ 失败细节：`errors` 整个键缺失时只显示 `message`，并说清原因', async () => {
    // ★★★ 模块头 (5)：8 条 finishMigration 分支**只写 message、从不动 errors**
    //   ⇒ `errors` 缺失**不代表没有失败**。
    mFetch.mockResolvedValue(
      state({
        latest: run({
          status: 'failed',
          errors: undefined,
          message: '空间不足: no space left on device',
        }),
      }),
    )
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-failed')).toBe(true)
    expect(hasId(w, 'sms-errors')).toBe(false)
    expect(textOf(w, 'sms-message')).toBe('空间不足: no space left on device')
    expect(t('sms.failed.note')).toMatch(/message/)
  })

  it('⑥-6 ★★★★ `errors` 有内容时逐条列出', async () => {
    mFetch.mockResolvedValue(
      state({
        latest: run({ status: 'failed', errors: ['rel failed: a', 'copy failed: b'], message: 'x' }),
      }),
    )
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-errors')).toBe(true)
    expect(w.findAll('[data-testid="sms-errors"] li')).toHaveLength(2)
  })

  it('⑥-7 ★★★★ 非 failed 时不渲染失败区块', async () => {
    mFetch.mockResolvedValue(state({ latest: run({ status: 'succeeded', errors: ['x'] }) }))
    const w = await mountView()
    await click(w, 'sms-load')
    expect(hasId(w, 'sms-failed')).toBe(false)
  })

  it('⑥-8 ★★★★★★ 路由存在，且**路由 meta 上的角色档**是 admin', () => {
    // ⚠️★★ 这条是变异台 S6 补出来的：**路由 meta 是与抽屉席相互独立的第二道**，
    //   而初版只断言了 `path` 与 `name`
    //   ⇒ 变异把 `meta.requiresRole` 整条摘掉时全绿。
    // ⇒ ★★ 通用式：**「配了某个字段」与「配在哪个载体上」要分别钉** ——
    //   抽屉席一份、路由 meta 一份，少钉一份就等于没配。
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toMatch(/path: '\/storage-migration-state'/)
    expect(src).toMatch(/name: 'storage-migration-state'/)
    const i = src.indexOf("name: 'storage-migration-state'")
    expect(i).toBeGreaterThan(-1)
    // ★ 取该路由的 meta 行（**行内**出现才算是这一条路由的）
    const line = src.slice(i, src.indexOf('},', i)).split('\n').find((l) => l.includes('meta:')) ?? ''
    expect(line, '这一条路由没有 meta 行').not.toBe('')
    expect(line).toMatch(/requiresRole: 'admin'/)
    expect(line, '路由 meta 缺 requiresAuth').toMatch(/requiresAuth: true/)
  })

  it('⑥-9 ★★★★ i18n 两个语种都有 sms.* 全部键，且值不等于键名', () => {
    const keys = [
      'nav.storageMigrationState',
      'sms.title',
      'sms.nope.msg',
      'sms.nope.why',
      'sms.gate.note',
      'sms.readonly.note',
      'sms.load.title',
      'sms.load.btn',
      'sms.state.title',
      'sms.tenant.none',
      'sms.empty',
      'sms.status.running',
      'sms.status.terminal',
      'sms.polling.on',
      'sms.polling.timeout',
      'sms.field.status',
      'sms.field.runId',
      'sms.field.from',
      'sms.field.to',
      'sms.field.startedAt',
      'sms.field.fileProgress',
      'sms.field.byteProgress',
      'sms.field.purge',
      'sms.field.filesDeleted',
      'sms.purge.yes',
      'sms.purge.no',
      'sms.purge.note',
      'sms.failed.title',
      'sms.failed.note',
    ]
    for (const loc of ['zh-CN', 'en-US'] as const) {
      for (const k of keys) {
        const v = dictLookup(DICT[loc], k)
        expect(typeof v, `${loc} 缺 ${k}`).toBe('string')
        expect(v, `${loc} 的 ${k} 值等于键名`).not.toBe(k)
      }
    }
  })

  it('⑥-10 ★★★ i18n 字典里的 sms.field 必须**一键一行**（门解析不了「一行多对」）', () => {
    // ★★ 这条是本会话真踩过的：i18n 门的键展平器解析不了 `a: 'x', b: 'y'`，
    //   会报「两侧一致地缺」⇒ 字典字段必须一键一行。
    for (const [name, src] of [
      ['zh-CN', readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8')],
      ['en-US', readFileSync(join(process.cwd(), 'src/i18n/en-US.ts'), 'utf8')],
    ] as const) {
      const lines = src.split('\n')
      const idx = lines.findIndex((l) => l.includes('sms: {'))
      expect(idx, `${name} 里找不到 sms 块`).toBeGreaterThan(-1)
      const body = lines.slice(idx, idx + 40).join('\n')
      expect(body).not.toMatch(/^\s*sms\.field:\s*\{[^\n]*,\s*\S+:/m)
    }
  })

  it('⑥-11 ★★★★ 孤儿棘轮：本族已移出「功能缺口」清单，且未登记成重复件', () => {
    const src = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    const orphanList = src.slice(src.indexOf('KNOWN_ORPHANS'), src.indexOf(']', src.indexOf('KNOWN_ORPHANS')))
    expect(orphanList, '本族还在孤儿清单里').not.toContain('storageMigrationState')
    expect(src, '本族被登记成 KNOWN_DUPLICATES 了').not.toMatch(/KNOWN_DUPLICATES[\s\S]{0,200}storageMigrationState/)
  })

  it('⑥-12 ★★★★ 新触控控件 ≥48 CSS px（解析数值再比大小）', () => {
    const css = viewSrc()
    expect(css).toMatch(/\.sms__btn\s*\{[^}]*min-height:\s*(\d+)px/)
    const minH = Number(/min-height:\s*(\d+)px/.exec(css)![1])
    expect(minH).toBeGreaterThanOrEqual(48)
  })

  it('⑥-13 ★★★ 编码门：视图、本文件与两个字典不得含 U+FFFD / NUL', () => {
    for (const p of [
      'src/views/StorageMigrationStateView.vue',
      'src/views/StorageMigrationStateView.spec.ts',
      'src/api/storageMigrationState.ts',
      'src/i18n/zh-CN.ts',
      'src/i18n/en-US.ts',
    ]) {
      const b = readFileSync(join(process.cwd(), p))
      // ★★ 被查的那个码点**绝不能**写成字面量：上一版的判据里
      //   真的躺着一个 U+FFFD 字面量 ⇒ 这条判据自己就被自己判红。
      //   ⇒ 一律用 fromCharCode 构造。
      const s = b.toString('utf8')
      expect(s.includes(String.fromCharCode(0xfffd)), `${p} 含 U+FFFD`).toBe(false)
      expect(b.includes(0), `${p} 含 NUL`).toBe(false)
    }
  })

  it('⑥-14 ★★★ 视图里不得存在恒定的「假门」computed（两侧都扫）', () => {
    // ★★★ 上一批亲手踩到：判据只扫 API 模块，而我在**视图**里留了
    //   `computed(() => true)` ⇒ 因为不在扫描面里而一直绿。
    // ⇒ 否定断言扫**剥掉注释后的代码**，否则会误红「我删过它」的自省注释。
    const code = stripComments(viewSrc())
    expect(code).not.toMatch(/computed\(\(\) => true\)/)
    expect(code).not.toMatch(/computed\(\(\) => false\)/)
  })

  it('⑥-15 ★★★ 视图不出现「本族有租户维度」或「super_admin 档」的说法', () => {
    // ★ 档位结论已经定了（'admin'），页面文案不许漂到别的档位上去。
    const code = stripComments(viewSrc())
    expect(code).not.toMatch(/requiresRole:\s*'super_admin'/)
    expect(t('sms.gate.note')).not.toContain('super_admin')
  })
})
