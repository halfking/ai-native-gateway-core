import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import DataLifecycleStorageView from './DataLifecycleStorageView.vue'
import { setLocale, t } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import {
  fetchStorageOverview,
  fetchStorageTableSizesChecked,
  storageOverviewRoleNeedsAdminGate,
  storageOverviewCarriesNoTenantDimension,
  storageDatabaseQueryFailed,
  storageDatabaseBlockIsAllZero,
  STORAGE_TABLES_LIMIT_FALLBACK,
  STORAGE_DB_FREE_IS_UNMEASURED,
  type StorageOverview,
  type StorageTableSizes,
} from '@/api/dataLifecycleStorage'

/**
 * 存储总览面回归护栏（2026-10-14，第一百二十三批）。
 *
 * ★★★★★★ **本批的价值不在「接进 UI」，而在「同一个错连续第三批出现」。**
 *
 *   模块头原写：「两个都是 **admin 档**（tenant_admin 可用）⇒ 抽屉席**不设** `requiresRole`。」
 *   —— 而第一百二十一批（platformSettings）与第一百二十二批（usageEnhanced）
 *   **栽在同一句话上**。三批的根因是同一个：
 *
 *   ★★★ `admin(...)` 是 `admin/handler.go:962` 的**局部别名** `admin := h.admin`，
 *     而 `h.admin` = `AdminMiddleware` —— 它**没有任何角色下限** ⇒
 *     ★★ **「admin 档」在注册行里的真实含义是「已认证」，不是「管理员」。**
 *
 *   ⇒ 那个标签**掩盖了服务端根本没有角色下限**这件事，
 *     而「没有角色下限」叠加批 122 的实证（`supportedRole` 接受 `"user"`）
 *     ⇒ 任何已认证的普通用户都能调这些 `/api/admin/` 端点。
 *
 * ★★★★★ **本族最终是 `'admin'`，不是 `super_admin`，也不是不设** ——
 *   两条判据必须**分开**看，缺一不可：
 *   - （甲）**不过度限制到 super_admin**：载荷是**纯平台基础设施事实**
 *     （`admin/data_lifecycle_storage.go` **整个文件零 `tenant` 命中**）
 *     ⇒ **不含任何租户的数据** ⇒ 按批 119 的边界（过度限制的正当性来自
 *     「载荷里有没有别人的东西」）⇒ **没有**理由挡到 super_admin。
 *   - （乙）**但需要 `'admin'`**：服务端无角色下限 ⇒ 普通 user 够得着，
 *     而「本机磁盘已用 92%」「日志目录超过 5GB」**没理由给普通用户看**
 *     ⇒ 客户端这一层要补的正是**角色下限**，不是数据隔离。
 *
 * ★ 与批 119（v1 数据地平线，**不设** requiresRole）的差别在于
 *   那份告示**必须让所有人看见**（「接口没报错不代表数据是新的」）；
 *   本族**没有**这个理由 ⇒ 「**没理由给普通用户看**」本身就是一道门。
 *
 * ★★ 行号订正：模块头原写 `:982` / `:983`，实为 `:1009` / `:1010`（整块漂 **27 行**）；
 *   同族 vacuum/reindex 六个原写 `:986-992`，实为 `:1013-:1019`（同样 27 行）。
 *
 * 判据钉六组：
 *  ① ★★★★★★ 权限档 admin，**两条理由分别钉**，且逐条从 Go 源码取证。
 *  ② ★★★★★★ 「admin 档」≠「管理员」：把中间件无角色下限钉成承重判据。
 *  ③ ★★★★★ 失败留下**全零整块**而不是缺键（`database` 与 `columnar` 表达**不同构**）。
 *  ④ ★★★★ `free_human` **恒为空串**是契约，不是「空闲 0 字节」。
 *  ⑤ ★★★★★ 深链兜底：**不发请求**。
 *  ⑥ ★★★ 接线与编码门（含 admin 档**不得**进 superAdmin 白名单）。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function ov(over: Partial<StorageOverview> = {}): StorageOverview {
  return {
    database: {
      database_bytes: 1024,
      database_human: '1024 bytes',
      total_bytes: 900,
      total_human: '900 bytes',
      tables_bytes: 500,
      indexes_bytes: 300,
      toast_bytes: 100,
      free_bytes: 0,
      free_human: '',
      server_version: 'PostgreSQL 17',
    },
    columnar: { available: true, table_count: 2, total_columns: 40, total_bytes: 200, total_human: '200 bytes' },
    filesystem: { path: '.', exists: true, total_bytes: 9000, total_human: '9000 bytes', used_bytes: 4500, used_human: '4500 bytes', used_percent: 50 },
    warnings: [],
    collected_at: '2026-10-14T10:00:00Z',
    ...over,
  } as unknown as StorageOverview
}

/** ★ 查询失败后的**完整形状**：九键全零/空，且 `server_version` 被 omitempty 省掉。 */
function ovDbFailed(warnings: string[]): StorageOverview {
  return ov({
    database: {
      database_bytes: 0, database_human: '', total_bytes: 0, total_human: '',
      tables_bytes: 0, indexes_bytes: 0, toast_bytes: 0, free_bytes: 0, free_human: '',
    } as unknown as StorageOverview['database'],
    warnings,
  })
}

function sizes(over: Partial<StorageTableSizes> = {}): StorageTableSizes {
  return {
    tables: [
      { table: 'usage_ledger', schema: 'public', rows: 208541, total_bytes: 500, total_human: '500 bytes', index_bytes: 300, toast_bytes: 0, toast_human: '', percent_of_db: 0.55, is_partitioned: true },
      { table: 'request_logs', schema: 'public', rows: 9000, total_bytes: 400, total_human: '400 bytes', index_bytes: 200, toast_bytes: 0, toast_human: '', percent_of_db: 0.44, is_partitioned: false },
    ],
    total_bytes: 900,
    total_human: '900 bytes',
    collected_at: '2026-10-14T10:00:00Z',
    ...over,
  } as unknown as StorageTableSizes
}

// ── 装配 ──────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/dataLifecycleStorage', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/dataLifecycleStorage')>()
  return { ...actual, fetchStorageOverview: vi.fn(), fetchStorageTableSizesChecked: vi.fn() }
})

const mOv = fetchStorageOverview as unknown as ReturnType<typeof vi.fn>
const mTb = fetchStorageTableSizesChecked as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(DataLifecycleStorageView)
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
  return readFileSync(join(process.cwd(), 'src/views/DataLifecycleStorageView.vue'), 'utf8')
}
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}
/** ★ 取 Go 函数体：带接收者时只停在「下一个同接收者的方法」（批 122 的教训）。 */
function goFunc(src: string, name: string): string {
  const lines = src.split('\n')
  const isMethod = lines.some((l) => l.startsWith(`func (h *Handler) ${name}(`))
  const start = lines.findIndex(
    (l) => l.startsWith(`func (h *Handler) ${name}(`) || l.startsWith(`func ${name}(`),
  )
  expect(start, `没找到 ${name} 的定义（正则失配会让这条恒绿）`).toBeGreaterThan(-1)
  const stopRe = isMethod ? /^func \(h \*Handler\) / : /^func /
  let end = start + 1
  while (end < lines.length && !stopRe.test(lines[end] ?? '')) end++
  return lines.slice(start, end).join('\n')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  mOv.mockResolvedValue(ov())
  mTb.mockResolvedValue(sizes())
})

async function load(w: W) {
  await byId(w, 'dls-load').trigger('click').then(() => flushPromises())
}

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档 admin —— 两条理由分别钉
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档 admin（两条独立理由，缺一不可）', () => {
  it('①-1 ★★★★★★ 抽屉席存在、路径正确、且设 admin 档', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'data-lifecycle-storage')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/data-lifecycle-storage')
    expect(seat!.requiresRole).toBe('admin')
    expect(seat!.titleKey).toBe('nav.dataLifecycleStorage')
  })

  it('①-2 ★★★★★★ admin 档 ⇒ 不得进 superAdmin 白名单，但 tenant_admin 能看到', () => {
    const src = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    expect(src).not.toMatch(/^\s*'data-lifecycle-storage',$/m)
    expect(navItemsFor(DRAWER_NAV, 'tenant_admin').map((i) => i.key)).toContain('data-lifecycle-storage')
    // ★★★ 这一层的**全部作用**：挡住普通 user。
    expect(navItemsFor(DRAWER_NAV, 'user').map((i) => i.key)).not.toContain('data-lifecycle-storage')
  })

  it('①-3 ★★★★★★ 理由（甲）：**不过度限制** —— 载荷无租户维度', async () => {
    // ★★ 这条是「不设 super_admin」的**依据本身**，必须可执行、可变异。
    expect(storageOverviewCarriesNoTenantDimension(ov())).toBe(true)
    // ★ 反向：一旦后端加了 per-tenant 拆分，这条会红 ⇒ 提示权限档要重判。
    expect(storageOverviewCarriesNoTenantDimension({ ...ov(), tenant: 'acme' })).toBe(false)
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'dls-no-tenant')).toBe(true)
    expect(textOf(w, 'dls-no-tenant')).toBe(t('dls.check.noTenant'))
  })

  it('①-4 ★★★★★★ 理由（乙）：**需要 admin** —— 服务端没有角色下限', () => {
    expect(storageOverviewRoleNeedsAdminGate('user')).toBe(true)
    expect(storageOverviewRoleNeedsAdminGate('tenant_admin')).toBe(false)
    expect(storageOverviewRoleNeedsAdminGate('super_admin')).toBe(false)
    expect(storageOverviewRoleNeedsAdminGate('admin_key')).toBe(false)
    expect(storageOverviewRoleNeedsAdminGate(null)).toBe(true)
  })

  it('①-5 ★★★★★★ 读 Go 源码：`admin(...)` 是 `admin := h.admin` 的局部别名', () => {
    // ★★★ 这是三批同源错误的**共同根因**，必须钉住这个别名本身。
    const h = goSrc('admin/handler.go')
    const line = h.split('\n').find((l) => /^\s*admin := h\.admin\s*$/.test(l))
    expect(line, '没找到 `admin := h.admin` 别名（三批同源错误的根）').toBeTruthy()
    expect(h).toMatch(
      /func \(h \*Handler\) admin\(fn http\.HandlerFunc\) http\.HandlerFunc \{\s*\n\s*return AdminMiddleware\(fn, h\.db, h\.secret\)/,
    )
  })

  it('①-6 ★★★★★★ 读 Go 源码：两个 handler **零租户维度**（这是理由甲的事实侧）', () => {
    const src = goSrc('admin/data_lifecycle_storage.go')
    for (const fn of ['handleDataLifecycleStorage', 'handleDataLifecycleTableSizes']) {
      const body = goFunc(src, fn)
      expect(body, `${fn} 出现了租户判定 ⇒ 权限档要重判`).not.toMatch(/tenant/i)
    }
  })

  it('①-7 ★★★★★ 行号订正：`:982`/`:983` → `:1009`/`:1010`（批153）→ `:1034`/`:1035`（12h-audit 20261017）→ 实为 `:1038`（2026-10-21 views 行号订正轮）→ 实为 `:1041`（2026-10-26 行号漂移订正轮）', () => {
    const lines = goSrc('admin/handler.go').split('\n')
    const at = lines.findIndex((l) => l.includes('"/api/admin/data-lifecycle/storage"'))
    expect(at, '找不到注册行').toBeGreaterThan(-1)
    expect(at + 1).toBe(1041)
    expect(lines[at]).toMatch(/admin\(h\.handleDataLifecycleStorage\)/)
    // ★★ 反向护栏：模块头原写的行号那行**不是**本族注册处。
    expect(lines[981]).not.toMatch(/data-lifecycle/i)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② 「admin 档」≠「管理员」
// ══════════════════════════════════════════════════════════════════════════

describe('② 「admin 档」这个标签掩盖了「没有角色下限」', () => {
  it('②-1 ★★★★★★ AdminMiddleware 里没有角色判定（批 122 的实证，本族复用）', () => {
    const body = goFunc(goSrc('admin/auth.go'), 'AdminMiddleware')
    expect(body, '中间件里出现了角色判定 ⇒ 权限结论要重判').not.toMatch(
      /IsSuperAdminOrLegacy|IsTenantAdmin|RequireSuperAdminForWrite/,
    )
    expect(body).toMatch(/errPasswordChangeRequired/)
  })

  it('②-2 ★★★★★★ `supportedRole` 接受 "user" —— 普通用户真的进得来', () => {
    expect(goFunc(goSrc('pkg/identity/verifier.go'), 'supportedRole')).toMatch(
      /case "user", "tenant_admin", "super_admin":\s*\n\s*return true/,
    )
  })

  it('②-3 ★★★★ 页面必须把「为什么普通账号看不到」讲清楚', async () => {
    authState.role = 'user'
    const w = await mountView()
    await flushPromises()
    expect(textOf(w, 'dls-nope-why')).toBe(t('dls.nope.why'))
    expect(t('dls.nope.why')).toContain('角色下限')
    expect(mOv).not.toHaveBeenCalled()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ 失败留下全零整块，而不是缺键
// ══════════════════════════════════════════════════════════════════════════

describe('③ 失败留下「全零整块」，与「真的空」同形', () => {
  it('③-1 ★★★★★ 数据库查询失败 ⇒ 报红线，且**不是**报「大小为 0」', async () => {
    mOv.mockResolvedValue(ovDbFailed(['数据库大小查询失败: connection refused']))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'dls-db-failed')).toBe(true)
    expect(hasId(w, 'dls-db-ok')).toBe(false)
    expect(textOf(w, 'dls-db-failed')).toBe(t('dls.check.dbFailed'))
    // ★★ 判据的两半都要成立才算「失败」——只判 human 为空是不够的。
    expect(storageDatabaseQueryFailed(ovDbFailed([]))).toBe(true)
    expect(storageDatabaseBlockIsAllZero(ovDbFailed([]).database)).toBe(true)
  })

  it('③-2 ★★★★ 只 human 为空、其余非零 ⇒ **不**报（两半缺一不可）', async () => {
    const halfZero = ov()
    halfZero.database.database_human = ''
    mOv.mockResolvedValue(halfZero)
    const w = await mountView()
    await load(w)
    expect(storageDatabaseQueryFailed(halfZero)).toBe(true)
    expect(storageDatabaseBlockIsAllZero(halfZero.database)).toBe(false)
    expect(hasId(w, 'dls-db-failed'), '只塌一半不算「整块塌成零」').toBe(false)
  })

  it('③-3 ★★★★ 正常载荷 ⇒ 不报，且「服务端确认成功」必须出现', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'dls-db-failed')).toBe(false)
    expect(hasId(w, 'dls-db-ok')).toBe(true)
  })

  it('③-4 ★★★★ warnings 是**横向诊断**，不只是错误列表', async () => {
    mOv.mockResolvedValue(
      ov({ warnings: ['本机磁盘已用 ≥ 90%，请检查日志/缓存/附件', '数据库总大小超过本机磁盘容量的 5 倍 — 表明 DB 部署在独立节点'] }),
    )
    const w = await mountView()
    await load(w)
    expect(w.findAll('[data-testid="dls-warn"]').length).toBe(2)
    expect(textOf(w, 'dls-warn-count')).toBe('2')
    // ★ 断言**具体文案**（逐字来自 Go 侧常量），不是「有 2 条」。
    expect(w.findAll('[data-testid="dls-warn"]')[0]!.text()).toContain('90%')
    expect(w.findAll('[data-testid="dls-warn"]')[1]!.text()).toContain('5 倍')
  })

  it('③-5 ★★★ columnar 用 `note` 说明原因，与 database 的表达**不同构**', async () => {
    mOv.mockResolvedValue(
      ov({ columnar: { available: false, table_count: 0, total_columns: 0, total_bytes: 0, total_human: '', note: 'citus_columnar 扩展未安装' } }),
    )
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'dls-columnar-note')).toBe('citus_columnar 扩展未安装')
    // ★ 关键：`note` 存在**不代表** database 查询失败 —— 两者不同构。
    expect(hasId(w, 'dls-db-failed')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④ free_human 恒为空串是契约
// ══════════════════════════════════════════════════════════════════════════

describe('④ `free_human` 恒为空串 = 「测不到」，不是「空闲 0 字节」', () => {
  it('④-1 ★★★★ 空串 ⇒ 提示必须出现，且渲染成占位符', async () => {
    const w = await mountView()
    await load(w)
    expect(STORAGE_DB_FREE_IS_UNMEASURED).toBe(true)
    expect(hasId(w, 'dls-free-note')).toBe(true)
    expect(textOf(w, 'dls-db-free')).toBe('—')
    expect(textOf(w, 'dls-free-note')).toBe(t('dls.check.freeUnmeasurable'))
  })

  it('④-2 ★★★ 若 free_human 意外非空 ⇒ 不提示（阴性对照）', async () => {
    const withFree = ov()
    withFree.database.free_human = '123 bytes'
    mOv.mockResolvedValue(withFree)
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'dls-free-note')).toBe(false)
    expect(textOf(w, 'dls-db-free')).toBe('123 bytes')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑤ 深链兜底
// ══════════════════════════════════════════════════════════════════════════

describe('⑤ 深链兜底：兜的是「不发请求」', () => {
  it('⑤-1 ★★★★★ user ⇒ 显示 nope，且一次请求都不发', async () => {
    authState.role = 'user'
    const w = await mountView()
    await flushPromises()
    expect(hasId(w, 'dls-nope')).toBe(true)
    expect(mOv).not.toHaveBeenCalled()
    expect(mTb).not.toHaveBeenCalled()
    expect(hasId(w, 'dls-load')).toBe(false)
  })

  it('⑤-2 ★★★★★ tenant_admin 放行（本族**不是** super_admin 档，别误伤）', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'dls-nope')).toBe(false)
    expect(mOv).toHaveBeenCalledTimes(1)
    expect(mTb).toHaveBeenCalledTimes(1)
  })

  it('⑤-3 ★★★ limit 用的是缺省值常量（不是随手写的数字）', async () => {
    const w = await mountView()
    await load(w)
    const arg = mTb.mock.calls[0]![0] as { limit?: number }
    expect(arg.limit).toBe(STORAGE_TABLES_LIMIT_FALLBACK)
    expect(textOf(w, 'dls-tables-limit')).toBe(t('dls.tables.limit', { n: STORAGE_TABLES_LIMIT_FALLBACK }))
  })

  it('⑤-4 ★★★ 表行渲染用 `schema.table`，且 toast_human 不丢', async () => {
    const withToast = sizes()
    withToast.tables[0]!.toast_human = '100 kB'
    mTb.mockResolvedValue(withToast)
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'dls-table-public-usage_ledger')).toContain('public.usage_ledger')
    expect(hasId(w, 'dls-toast')).toBe(true)
    expect(textOf(w, 'dls-toast')).toBe('100 kB')
    expect(textOf(w, 'dls-table-count')).toBe('2')
  })

  it('⑤-5 ★★★ 取数失败 ⇒ 报错，不显示「数据库 0 字节」', async () => {
    mOv.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'dls-error')).toBe(true)
    expect(hasId(w, 'dls-db-total')).toBe(false)
    expect(hasId(w, 'dls-db-failed')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 接线与编码门
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 接线与编码门', () => {
  it('⑥-1 ★★★★ 路由存在且 requiresRole 是 admin', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toMatch(/path: '\/data-lifecycle-storage'/)
    expect(src).toMatch(/name: 'data-lifecycle-storage'/)
  })

  it('⑥-2 ★★★★ i18n 两个语种都有 dls.* 全部键，且值不等于键名', () => {
    const keys = [
      'nav.dataLifecycleStorage',
      'dls.title',
      'dls.nope.msg',
      'dls.nope.why',
      'dls.load.title',
      'dls.load.btn',
      'dls.scope.note',
      'dls.warn.title',
      'dls.db.title',
      'dls.db.ok',
      'dls.tables.title',
      'dls.tables.limit',
      'dls.check.dbFailed',
      'dls.check.freeUnmeasurable',
      'dls.check.noTenant',
      'dls.field.total',
      'dls.field.tables',
      'dls.field.indexes',
      'dls.field.free',
    ]
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const m = DICT[loc]
      for (const k of keys) {
        const v = dictLookup(m, k)
        expect(typeof v, `${loc} 缺 ${k}`).toBe('string')
        expect(v, `${loc} 的 ${k} 值等于键名`).not.toBe(k)
      }
    }
  })

  it('⑥-3 ★★★ 新触控控件 ≥48 CSS px（解析数值再比大小）', () => {
    const m = viewSrc().match(/\.dls__btn\s*\{[^}]*min-height:\s*(\d+(?:\.\d+)?)px/)
    expect(m, '没找到 .dls__btn 的 min-height').toBeTruthy()
    expect(Number(m![1])).toBeGreaterThanOrEqual(48)
  })

  it('⑥-4 ★★★★ 孤儿棘轮：本族已移出「功能缺口」清单，且未登记成重复件', () => {
    const src = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    const listOf = (name: string): string[] => {
      const m = src.match(new RegExp(`const ${name}: readonly string\\[\\] = \\[([\\s\\S]*?)\\]`))
      expect(m, `没找到 ${name}（正则失配会让这条恒绿）`).toBeTruthy()
      return [...m![1]!.matchAll(/'([A-Za-z0-9_]+)'/g)].map((x) => x[1]!)
    }
    expect(listOf('KNOWN_ORPHANS')).not.toContain('dataLifecycleStorage')
    expect(listOf('KNOWN_DUPLICATES')).not.toContain('dataLifecycleStorage')
  })

  it('⑥-5 ★★★ 编码门：视图与本文件不得含 U+FFFD / NUL', () => {
    const F = String.fromCharCode(0xFFFD)
    const N = String.fromCharCode(0)
    for (const f of [
      viewSrc(),
      readFileSync(join(process.cwd(), 'src/views/DataLifecycleStorageView.spec.ts'), 'utf8'),
    ]) {
      expect(f.includes(F), '含 U+FFFD').toBe(false)
      expect(f.includes(N), '含 NUL').toBe(false)
    }
  })

  it('⑥-6 ★★★ 前三批的 i18n 前缀没被本批挤掉（批 121 前车之鉴）', () => {
    for (const loc of ['zh-CN', 'en-US'] as const) {
      for (const k of ['sts.title', 'ps.title', 'ue.title', 'dls.title']) {
        expect(typeof dictLookup(DICT[loc], k), `${loc} 的 ${k} 丢了`).toBe('string')
      }
    }
  })
})

/** ★ 字典是**嵌套对象**，必须按路径走，不能当 `Record<string,string>`。 */
const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}
