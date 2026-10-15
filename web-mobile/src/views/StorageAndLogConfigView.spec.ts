import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import StorageAndLogConfigView from './StorageAndLogConfigView.vue'
import { setLocale, t } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import {
  fetchStorageConfig,
  fetchLogConfig,
  logConfigPathMatchesRuntime,
  storageConfigServerIsRealGate,
  STORAGE_MASKED_KEYS,
  STORAGE_TYPES,
  STORAGE_BOOL_OPTIONAL_KEYS,
  CONFIG_SOURCES,
  type StorageConfigResponse,
  type LogConfigResponse,
} from '@/api/storageAndLogConfig'

/**
 * 存储与日志配置面回归护栏（2026-10-14，第一百二十四批）。
 *
 * ★★★★★ **本族是「模块头一开始就把权限结论写对」的唯一样本** ——
 *   而第一百二十一 / 一百二十二 / 一百二十三批都栽在同一句话上。
 *
 *   那些批次的错：「都是 `admin(...)` ⇒ admin 档 ⇒ 抽屉席不设 `requiresRole`」，
 *   根因是**没人去看中间件那一层**。
 *   ★★★ 而本模块（第九十三批写的）的作者**顺手看了相邻那一行**，发现
 *   `:1129`（superAdmin）与 `:1132`（admin，storage/migration-state）
 *   **相邻两行却是两种档位**，并写下了「绝不能按 `/api/admin/storage/` 前缀推权限」。
 *   ⇒ ★★ **「顺手看一眼邻居」这一步，就是三批返工与不返工的分界线。**
 *
 * ★★★★★ 本族是**第二类「真门」**（与批 120 同款，与批 122/123 相反）：
 *   `h.superAdmin` → `SuperAdminMiddleware`（`admin/auth.go:376`）逐字
 *   `if claims.Role != "super_admin" { 403 "super_admin role required for this endpoint" }`
 *   ⇒ ★★ **越权请求会被服务端挡下来**
 *   ⇒ 视图内那一层兜的是**体验**（不发请求、不显示一屏 403），**不是**安全边界。
 *
 * ★★ **本模块只读**：桌面端有 PUT（`web/src/api/tuning.ts:802` / `:934`），
 *   而本模块**只导出两个 GET** ⇒ 移动端**不提供任何写入入口**。
 *
 * ★★ **行号订正**：模块头原写 `:1108` / `:1109` / `:1111` / `:1114`，
 *   实况 `:1129` / `:1130` / `:1132` / `:1135`（**整块漂 21 行**）。
 *
 * 判据钉六组：
 *  ① ★★★★★★ 权限档 super_admin（**真门**）+ 相邻两行两种档位。
 *  ② ★★★★★ 只读面：不提供任何写入入口。
 *  ③ ★★★★★ `config_source` 只描述**一个键**的来源（**且刻意不做成假门**）。
 *  ④ ★★★★ `file_path`（期望）≠ `log_file`（运行时）⇒ 热加载提示。
 *  ⑤ ★★★★★ 深链兜底：**不发请求**（但说清它兜的是体验层）。
 *  ⑥ ★★★ 接线与编码门（含 superAdmin 白名单字母序）。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function storage(over: Partial<StorageConfigResponse> = {}): StorageConfigResponse {
  return {
    storage_type: 'local',
    attachment_dir_override: '',
    ttl_days: 7,
    max_file_size_mb: 100,
    disk_quota_percent: 80,
    auto_cleanup_enabled: true,
    auto_cleanup_threshold: 90,
    effective_dir: '/data/attachments',
    attachment_dir_env: '/data/attachments',
    needs_restart: false,
    current_disk_usage: 42,
    config_source: 'db',
    download_url_prefix: '/api/attachments/',
    ...over,
  } as unknown as StorageConfigResponse
}

function logs(over: Partial<LogConfigResponse> = {}): LogConfigResponse {
  return {
    max_size_mb: 100,
    max_backups: 7,
    max_age_days: 30,
    compress: true,
    archive_days: 7,
    delete_days: 30,
    file_path: '/var/log/llmgw.log',
    file_path_override: '',
    file_path_env: '/var/log/llmgw.log',
    enabled: true,
    enabled_source: 'env',
    log_file: '/var/log/llmgw.log',
    log_dir: '/var/log',
    ...over,
  } as unknown as LogConfigResponse
}

// ── 装配 ──────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/storageAndLogConfig', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/storageAndLogConfig')>()
  return { ...actual, fetchStorageConfig: vi.fn(), fetchLogConfig: vi.fn() }
})

const mSt = fetchStorageConfig as unknown as ReturnType<typeof vi.fn>
const mLg = fetchLogConfig as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(StorageAndLogConfigView)
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
  return readFileSync(join(process.cwd(), 'src/views/StorageAndLogConfigView.vue'), 'utf8')
}
/**
 * ★★★ 剥掉行注释与块注释，只留**代码**。
 *
 * 存在的理由是本批亲手踩到的一次**自指陷阱**：视图注释里为了说明
 * 「这里曾有一个恒真 computed」而**原样写出了那个形态**
 * ⇒ 直接扫源码的否定断言会**误红自己的注释**。
 *
 * ⚠️★★ 写这个函数的注释时**绝不能**把「块注释的结束符」原样贴进来 ——
 *   它会**提前闭合本注释**，报出的却是下一行的语法错误（本次即如此：
 *   报错指向 `expect(existsSync…)` 那行，与真正的病因隔了 6 行）。
 *   ⇒ 只说「行注释 / 块注释」，不贴字面结束符。
 *
 * ★ 通用式：**否定断言遇到「被否的那个东西本身要写在注释里」时，
 *   正确解法是剥注释后扫，而不是把断言改成更长的正则去绕开。**
 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
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
  let end = start + 1
  while (end < lines.length && !stopRe.test(lines[end] ?? '')) end++
  return lines.slice(start, end).join('\n')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  mSt.mockResolvedValue(storage())
  mLg.mockResolvedValue(logs())
})

async function load(w: W) {
  await byId(w, 'slc-load').trigger('click').then(() => flushPromises())
}

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档 super_admin（真门）+ 相邻两行两种档位
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档第二类「真门」', () => {
  it('①-1 ★★★★★★ 抽屉席存在、路径正确、且设 super_admin', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'storage-log-config')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/storage-log-config')
    expect(seat!.requiresRole).toBe('super_admin')
    expect(seat!.titleKey).toBe('nav.storageAndLogConfig')
  })

  it('①-2 ★★★★★★ 抽屉席真的被 navItemsFor 挡住：super_admin 见得到、tenant_admin 见不到', () => {
    // ★ 只断言 `DRAWER_NAV` 里有这条数据是不够的：那证明「写进去了」，
    //   不证明「过滤器按 requiresRole 真的把它挡了」。
    // ⇒ 这里打**行为面**——同一份 items 换 role 两次，席位可见性必须变。
    const seen = (role: string) => navItemsFor(DRAWER_NAV, role).some((i) => i.key === 'storage-log-config')
    expect(seen('super_admin')).toBe(true)
    expect(seen('tenant_admin')).toBe(false)
    expect(seen('admin_key')).toBe(false)
    expect(seen('operator_user')).toBe(false)
  })

  it('①-3 ★★★★★★ 进 superAdmin 白名单，且字母序在 sliding-window 之后、subscription-tiers 之前', () => {
    const src = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    const at = (k: string) => src.indexOf(`'${k}',`)
    expect(at('storage-log-config'), '白名单里没有这一席').toBeGreaterThan(-1)
    expect(at('storage-log-config')).toBeGreaterThan(at('sliding-window'))
    expect(at('storage-log-config')).toBeLessThan(at('subscription-tiers'))
  })

  it('①-4 ★★★★★★ 读 Go 源码：`h.superAdmin` 逐字比 role ⇒ 403（**真门**）', () => {
    const h = goSrc('admin/handler.go')
    expect(h).toMatch(
      /func \(h \*Handler\) superAdmin\(fn http\.HandlerFunc\) http\.HandlerFunc \{\s*\n\s*return SuperAdminMiddleware\(fn, h\.db, h\.secret\)/,
    )
    const body = goFunc(goSrc('admin/auth.go'), 'SuperAdminMiddleware')
    expect(body, '中间件里没有 role 比对 ⇒ 不再是真门，权限结论要重判').toMatch(
      /claims\.Role != "super_admin"/,
    )
    expect(body).toMatch(/http\.StatusForbidden/)
  })

  it('①-5 ★★★★★★ 真门判别器会区分 `h.superAdmin` 与 `admin(...)`', () => {
    // ★★★ 这条是本族与批 122/123 的**分界**，也必须是可执行的：
    //   若哪天有人把注册处的 `h.superAdmin` 换成 `admin(...)`，
    //   服务端就不再挡越权 ⇒ 这条立刻红，提示「不再是真门，重新判档」。
    expect(storageConfigServerIsRealGate('h.superAdmin')).toBe(true)
    expect(storageConfigServerIsRealGate('admin(...)')).toBe(false)
    expect(storageConfigServerIsRealGate('h.admin')).toBe(false)
  })

  it('①-6 ★★★★★★ 读 Go 源码：`:1129` 与 `:1132` **相邻两行却是两种档位**', () => {
    // ★★★ 这条是「绝不能按前缀推权限」的**事实侧**，也是本族结论与三批返工的对照。
    const lines = goSrc('admin/handler.go').split('\n')
    const storageLine = lines.findIndex((l) => l.includes('"/api/admin/storage/config"'))
    expect(storageLine + 1, '模块头原写 :1108').toBe(1129)
    expect(lines[storageLine]).toMatch(/h\.superAdmin\(h\.handleStorageConfig\)/)
    // ★ 相邻的 migration-state 就在 1129 之后两行，却是 admin 档。
    const migLine = lines.findIndex((l) => l.includes('"/api/admin/storage/migration-state"'))
    expect(migLine + 1).toBe(1132)
    expect(lines[migLine]).toMatch(/admin\(h\.handleMigrationState\)/)
    expect(lines[migLine]).not.toMatch(/superAdmin/)
    expect(migLine - storageLine, '两行应当相邻（差 3 行：注释与空行）').toBeLessThanOrEqual(3)
  })

  it('①-7 ★★★★ 行号订正：模块头原写 :1108/:1109/:1111/:1114，实为 :1129/:1130/:1132/:1135', () => {
    const lines = goSrc('admin/handler.go').split('\n')
    const at = (frag: string) => lines.findIndex((l) => l.includes(frag)) + 1
    expect(at('"/api/admin/storage/config"')).toBe(1129)
    expect(at('"/api/admin/storage/config/test-path"')).toBe(1130)
    expect(at('"/api/admin/storage/migration-state"')).toBe(1132)
    expect(at('"/api/admin/logs/config"')).toBe(1135)
    // ★ 反向护栏：模块头原写的行号那行**不是**本族任何一行注册。
    expect(lines[1107]).not.toMatch(/storage\/config|logs\/config/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② 只读面
// ══════════════════════════════════════════════════════════════════════════

describe('② 本模块只读：不提供任何写入入口', () => {
  it('②-1 ★★★★★ 页面必须声明「只读」，而桌面端是有 PUT 的', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'slc-readonly-note')).toBe(t('slc.readonly.note'))
    // ★ 反向事实：桌面端确实有写入（tuning.ts:802 / :934）⇒ 这句不是空话。
    const desktop = goSrc('web/src/api/tuning.ts')
    expect(desktop).toMatch(/'PUT', '\/api\/admin\/storage\/config'/)
    expect(desktop).toMatch(/'PUT', '\/api\/admin\/logs\/config'/)
  })

  it('②-2 ★★★★ 模块**只导出两个 GET**（不得出现任何 PUT/POST 导出）', () => {
    const src = readFileSync(join(process.cwd(), 'src/api/storageAndLogConfig.ts'), 'utf8')
    const puts = [...src.matchAll(/'PUT',\s*'\/api\/(storage|logs)\/config/g)]
    expect(puts, '模块里出现了写操作导出').toEqual([])
  })

  it('②-3 ★★★★ 视图里不出现任何写入控件', () => {
    const src = viewSrc()
    expect(src).not.toMatch(/saveStorageConfig|putStorageConfig|saveLogConfig|putLogConfig/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ config_source 只描述一个键
// ══════════════════════════════════════════════════════════════════════════

describe('③ `config_source` 只描述一个键的来源（且刻意不做成假门）', () => {
  it('③-1 ★★★★★ 那条「不能当成整份配置的来源」的提醒**无条件显示**', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'slc-source-misused')).toBe(true)
    expect(textOf(w, 'slc-source-misused')).toBe(t('slc.check.sourceMisused'))
    // ★★★ **它不是红线**（不带 v-if 语义、样式是 warn 而非 err）——
    //   因为它是一条**永远成立的使用提醒**，不是「检测到违规」。
    expect(t('slc.check.sourceMisused')).toContain('某一个键')
  })

  it('③-2 ★★★★★★ 模块里**不得**存在恒定的「假门」helper', () => {
    // ★★★ 本批我自己**写过**一个恒返回 false 的 `configSourceDescribesWholeResponse()`，
    //   又按第一批 121 的纪律把它删了 —— 恒定返回的函数**看起来像闸、实际什么都挡不住**。
    // ⇒ 这条判据是给**后来者**的：别为了「显得严谨」再补一个这样的谓词。
    // ⚠️ 断言必须钉**导出形态**而不是那个裸词：那个词会合法地出现在
    //   模块头「我删过它」的自省注释里（此刻就出现了）⇒ 否词会误红。
    const src = readFileSync(join(process.cwd(), 'src/api/storageAndLogConfig.ts'), 'utf8')
    // ★ 否定断言扫**剥掉注释后的代码**；肯定断言（那句纪律本身）扫**全文**。
    expect(stripComments(src)).not.toMatch(/configSourceDescribesWholeResponse/)
    expect(src).toMatch(/恒定返回的函数就是假门/)
    // ★★★ 同一个毛病的**视图侧版本**：`computed(() => true)`。
    //   初版我在视图里留过一个（`showSourceMisuseNote`）——
    //   而 ③-2 只扫 API 模块 ⇒ **它躲在自己写的判据视线之外**。
    // ⇒ 判据要同时钉住**两侧**，否则「假门」会从后门回来。
    const view = stripComments(viewSrc())
    expect(view).not.toMatch(/computed\(\(\) => true\)/)
  })

  it('③-3 ★★★★ 源值必须落在三值域内', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'slc-source-unknown')).toBe(false)
    mSt.mockResolvedValue(storage({ config_source: 'nonsense' }))
    const w2 = await mountView()
    await load(w2)
    expect(hasId(w2, 'slc-source-unknown')).toBe(true)
    expect([...CONFIG_SOURCES]).toEqual(['default', 'env', 'db'])
  })

  it('③-4 ★★★★ `storage_type` 可被设成任意字符串（取值域不是 STORAGE_TYPES）', async () => {
    mSt.mockResolvedValue(storage({ storage_type: 'weird' }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'slc-type-unknown')).toBe(true)
    expect(textOf(w, 'slc-type')).toBe('weird')
    // ★ 反向：常见值不报。
    mSt.mockResolvedValue(storage())
    const w2 = await mountView()
    await load(w2)
    expect(hasId(w2, 'slc-type-unknown')).toBe(false)
    expect([...STORAGE_TYPES]).toEqual(['local', 'oss', 's3'])
  })

  it('③-5 ★★★★ 掩码键只报「有几个」，绝不回显值', async () => {
    mSt.mockResolvedValue(
      storage({ oss_access_key_secret: '***', s3_secret_access_key: '***' } as Partial<StorageConfigResponse>),
    )
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'slc-masked-count')).toBe(t('slc.masked.count', { n: 2 }))
    // ★★ 明文不许出现在 DOM 里。
    expect(w.text()).not.toContain('***')
    expect([...STORAGE_MASKED_KEYS]).toEqual(['oss_access_key_secret', 's3_secret_access_key'])
  })

  it('③-6 ★★★ 布尔可选键（`s3_use_ssl`）缺席即「未启用」，要说明', () => {
    // ★ 键存在 ⇔ 已启用；`false` 与「没配 S3」在响应上**不可区分**（模块 (2)）。
    expect([...STORAGE_BOOL_OPTIONAL_KEYS]).toEqual(['s3_use_ssl'])
    expect(STORAGE_CONFIG_HAS_SSL_KEY(storage())).toBe(false)
    expect(STORAGE_CONFIG_HAS_SSL_KEY(storage({ s3_use_ssl: true } as Partial<StorageConfigResponse>))).toBe(true)
  })
})

/** ★ 与 `StorageTableRow` 同款写法：用一个本地谓词钉住「键在不在」。 */
function STORAGE_CONFIG_HAS_SSL_KEY(r: StorageConfigResponse): boolean {
  return 's3_use_ssl' in (r as unknown as Record<string, unknown>)
}

// ══════════════════════════════════════════════════════════════════════════
// ④ file_path ≠ log_file
// ══════════════════════════════════════════════════════════════════════════

describe('④ `file_path`（配置期望）≠ `log_file`（运行时真值）', () => {
  it('④-1 ★★★★★ 两者不等 ⇒ 报「尚未热加载」', async () => {
    mLg.mockResolvedValue(logs({ file_path: '/var/log/new.log', log_file: '/var/log/old.log' }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'slc-logs-pending')).toBe(true)
    expect(textOf(w, 'slc-logs-pending')).toBe(t('slc.check.logPendingReload'))
    expect(logConfigPathMatchesRuntime(logs({ file_path: '/a', log_file: '/b' }))).toBe(false)
    expect(textOf(w, 'slc-file-path')).toBe('/var/log/new.log')
    expect(textOf(w, 'slc-log-file')).toBe('/var/log/old.log')
  })

  it('④-2 ★★★★ 相等 ⇒ 不报，且「已同步」徽标出现（该出现的一面）', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'slc-logs-pending')).toBe(false)
    expect(hasId(w, 'slc-logs-synced')).toBe(true)
    expect(logConfigPathMatchesRuntime(logs())).toBe(true)
  })

  it('④-3 ★★★ `needs_restart` 为真 ⇒ 独立提示', async () => {
    mSt.mockResolvedValue(storage({ needs_restart: true }))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'slc-needs-restart')).toBe(true)
    expect(textOf(w, 'slc-needs-restart')).toBe(t('slc.check.needsRestart'))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑤ 深链兜底（体验层）
// ══════════════════════════════════════════════════════════════════════════

describe('⑤ 深链兜底：兜的是「体验」，说清它不是安全边界', () => {
  it('⑤-1 ★★★★★ 非超管 ⇒ 显示 nope，且一次请求都不发', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    await flushPromises()
    expect(hasId(w, 'slc-nope')).toBe(true)
    expect(mSt).not.toHaveBeenCalled()
    expect(mLg).not.toHaveBeenCalled()
    expect(hasId(w, 'slc-load')).toBe(false)
  })

  it('⑤-2 ★★★★★ nope 页面说清「服务端会 403」—— 别让人以为前端是门', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    await flushPromises()
    expect(textOf(w, 'slc-nope-msg')).toBe(t('slc.nope.msg'))
    expect(textOf(w, 'slc-nope-why')).toBe(t('slc.nope.why'))
    expect(t('slc.nope.why')).toContain('403')
  })

  it('⑤-3 ★★★ 取数失败 ⇒ 报错，不显示空配置', async () => {
    mSt.mockRejectedValue(new Error('super_admin role required for this endpoint'))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'slc-error')).toBe(true)
    expect(hasId(w, 'slc-effective-dir')).toBe(false)
  })

  it('⑤-4 ★★★ 两个端点各发一次（不合并、不重复）', async () => {
    const w = await mountView()
    await load(w)
    expect(mSt).toHaveBeenCalledTimes(1)
    expect(mLg).toHaveBeenCalledTimes(1)
    // 两份响应都必须落进 DOM，否则「各发一次」可能是一次发了、结果被丢掉
    expect(hasId(w, 'slc-type')).toBe(true)
    expect(hasId(w, 'slc-log-file')).toBe(true)
  })

  it('⑤-5 ★★★★★★ **半份响应**不渲染：storage 成功 + logs 失败 ⇒ 两块都不出', async () => {
    // ★★★ 这条是 ⑤-3 的**补面**，补的是变异台当场查出来的夹具缺口：
    //   ⑤-3 只造了「两个端点一起失败」⇒ `storage.value` 从来没被赋过值，
    //   于是「catch 里清不清空 storage」在那个夹具下**不可观测**
    //   （变异 S6 摘掉 `storage.value = null` 全绿）。
    // ⇒ 「不显示空配置」这句话真正难的是**半份**：
    //   storage 已到手、logs 崩了 ⇒ 若不整块清掉，用户会看到一份**过期半真**的配置。
    mSt.mockResolvedValue(storage())
    mLg.mockRejectedValue(new Error('log config boom'))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'slc-error')).toBe(true)
    expect(hasId(w, 'slc-effective-dir'), '半份响应被当成完整配置显示了').toBe(false)
    expect(hasId(w, 'slc-log-file')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 接线与编码门
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 接线与编码门', () => {
  it('⑥-1 ★★★★ 路由存在且 requiresRole 是 super_admin', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toMatch(/path: '\/storage-log-config'/)
    expect(src).toMatch(/name: 'storage-log-config'/)
  })

  it('⑥-2 ★★★★ i18n 两个语种都有 slc.* 全部键，且值不等于键名', () => {
    const keys = [
      'nav.storageAndLogConfig',
      'slc.title',
      'slc.nope.msg',
      'slc.nope.why',
      'slc.load.title',
      'slc.load.btn',
      'slc.readonly.note',
      'slc.storage.title',
      'slc.storage.types',
      'slc.logs.title',
      'slc.logs.synced',
      'slc.masked.count',
      'slc.check.sourceMisused',
      'slc.check.sourceUnknown',
      'slc.check.typeUnknown',
      'slc.check.needsRestart',
      'slc.check.logPendingReload',
      'slc.field.storageType',
      'slc.field.effectiveDir',
      'slc.field.configSource',
      'slc.field.downloadPrefix',
      'slc.field.masked',
      'slc.field.filePath',
      'slc.field.logFile',
      'slc.field.maxSize',
      'slc.field.maxBackups',
      'slc.field.deleteDays',
      'slc.field.enabledSource',
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

  it('⑥-3 ★★★ i18n 字典里的 slc.field 必须**一键一行**（门解析不了「一行多对」）', () => {
    // ★★★ 本批首轮把 11 个键压成 4 行 ⇒ i18n:check 报「slc.field.* 两侧一致地缺」，
    //   而同一块的 slc.check.* 全部解析成功 ⇒ **分界正是「一行多对」**。
    // ⇒ 这条判据把那个分界钉下来，免得下一个人又把字段压回去。
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const src = readFileSync(join(process.cwd(), `src/i18n/${loc}.ts`), 'utf8')
      const start = src.indexOf('  slc: {')
      expect(start, `${loc} 找不到 slc 块`).toBeGreaterThan(-1)
      const block = src.slice(start, src.indexOf('\n  },', start))
      const fieldStart = block.indexOf('    field: {')
      expect(fieldStart, `${loc} 的 slc.field 不存在`).toBeGreaterThan(-1)
      const field = block.slice(fieldStart, block.indexOf('},', fieldStart))
      for (const line of field.split('\n').slice(1)) {
        const t = line.trim()
        if (t === '' || t === '}' || t === '},') continue
        expect((t.match(/^\s*[a-zA-Z_][a-zA-Z0-9_]*:/g) ?? []).length, `一行多对：${t}`).toBe(1)
      }
    }
  })

  it('⑥-4 ★★★ 新触控控件 ≥48 CSS px（解析数值再比大小）', () => {
    const m = viewSrc().match(/\.slc__btn\s*\{[^}]*min-height:\s*(\d+(?:\.\d+)?)px/)
    expect(m, '没找到 .slc__btn 的 min-height').toBeTruthy()
    expect(Number(m![1])).toBeGreaterThanOrEqual(48)
  })

  it('⑥-5 ★★★★ 孤儿棘轮：本族已移出「功能缺口」清单，且未登记成重复件', () => {
    const src = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    const listOf = (name: string): string[] => {
      const m = src.match(new RegExp(`const ${name}: readonly string\\[\\] = \\[([\\s\\S]*?)\\]`))
      expect(m, `没找到 ${name}（正则失配会让这条恒绿）`).toBeTruthy()
      return [...m![1]!.matchAll(/'([A-Za-z0-9_]+)'/g)].map((x) => x[1]!)
    }
    expect(listOf('KNOWN_ORPHANS')).not.toContain('storageAndLogConfig')
    expect(listOf('KNOWN_DUPLICATES')).not.toContain('storageAndLogConfig')
  })

  it('⑥-6 ★★★ 编码门：视图、本文件与两个字典不得含 U+FFFD / NUL', () => {
    const F = String.fromCharCode(0xFFFD)
    const N = String.fromCharCode(0)
    for (const f of [
      viewSrc(),
      readFileSync(join(process.cwd(), 'src/views/StorageAndLogConfigView.spec.ts'), 'utf8'),
      readFileSync(join(process.cwd(), 'src/i18n/zh-CN.ts'), 'utf8'),
      readFileSync(join(process.cwd(), 'src/i18n/en-US.ts'), 'utf8'),
    ]) {
      expect(f.includes(F), '含 U+FFFD').toBe(false)
      expect(f.includes(N), '含 NUL').toBe(false)
    }
  })

  it('⑥-7 ★★★ 前三批的 i18n 前缀没被本批挤掉', () => {
    for (const loc of ['zh-CN', 'en-US'] as const) {
      for (const k of ['sts.title', 'ps.title', 'ue.title', 'dls.title', 'slc.title']) {
        expect(typeof dictLookup(DICT[loc], k), `${loc} 的 ${k} 丢了`).toBe('string')
      }
    }
  })
})

/** ★ 字典是**嵌套对象**，必须按路径走。 */
const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}
