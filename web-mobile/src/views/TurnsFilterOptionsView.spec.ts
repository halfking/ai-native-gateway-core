import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import TurnsFilterOptionsView from './TurnsFilterOptionsView.vue'
import { t } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import {
  fetchTurnsFilterOptions,
  turnsFilterOptionsRoleNeedsAdminGate,
  turnsFilterOptionsIsTenantScoped,
  turnsFilterOptionsIsEmpty,
  turnsFilterOptionsHasAnyValue,
  turnsFilterOptionIsTruncated,
  statusCodeFilterValue,
  TURNS_FILTER_OPTION_KEYS,
  TURNS_FILTER_OPTION_LIMIT,
  type TurnsFilterOptions,
} from '@/api/turnsFilterOptions'

/**
 * 轮次筛选候选值面回归护栏（2026-10-15，第一百二十七批）。
 *
 * ★★★★★ 本族是**第四个**写下「admin 档 ⇒ 抽屉席不设 `requiresRole`」的模块
 *   —— 那句话让第一百二十一 / 一百二十二 / 一百二十三批连续返工。
 *   本模块（第九十六批写的）的原文是：
 *   「⇒ ★★ **admin 档**（`h.admin` = `AdminMiddleware`，`handler.go:962`）⇒ 抽屉席不设 `requiresRole`。」
 *
 * ⇒ ★★ 结论**正好相反**，理由是两条独立的：
 *   （甲）**需要 admin**：载荷是**租户级聚合清单** —— 8 个维度是这个租户
 *        **所有人**用过的项目/任务/负责人/客户端/标签/模型/供应商/状态码，
 *        不是某个用户自己的数据；
 *   （乙）**不过度限制到 super_admin**：handler 对每个维度都拼了 `tenantWhere`
 *        ⇒ 载荷是**租户内**的 ⇒ 按批 119 的边界不过度限制。
 *
 * ★★ **服务端这一层是唯一的门**（`admin(...)` = `AdminMiddleware` 全文无角色下限
 *   + `supportedRole` 接受 `"user"`）⇒ 深链兜底兜的是安全，不是体验。
 *
 * ★★ 本族最要紧的三条事实（都来自模块头，读 Go 源码逐字核过）：
 *   (1) **响应里没有 `api_keys`** ⇒ 桌面那把「API Key」下拉**永远是空的**
 *       （`TurnsFilterBar.vue:171` 的 `|| []` 就是作者打的补丁）
 *       ⇒ 移动端**不得**渲染它。
 *   (3) **文件头注释说「近 30 天」，但只有 3 个维度真有那个窗口**
 *       （models / providers / status_codes），其余五个是**全历史**里最近活跃的
 *       ⇒ 页面必须分开写，否则就是撒谎。
 *   (7) **`status_codes` 的元素是 `integer::text`**（`"200"`），
 *       而桌面把参数类型写成 `number` ⇒ **桌面的类型是错的**。
 *
 * 判据钉六组：
 *  ① ★★★★★★ 权限档 `'admin'`（服务端**没有**门）+ 租户过滤逐字。
 *  ② ★★★★★ 8 个维度、空维度是 `[]`、空态与「哪些维度空」。
 *  ③ ★★★★★★ 时间窗口：**只有 3 个维度**有 30 天窗口。
 *  ④ ★★★★★ `status_codes` 是数字串 + 转换 + 坏值不丢。
 *  ⑤ ★★★★ 截断（`LIMIT 20`，**正好 20 也算**）与 `api_keys` 不渲染。
 *  ⑥ ★★★★ 接线与编码门（含 admin 档**不得**进 superAdmin 白名单）。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function opts(over: Partial<TurnsFilterOptions> = {}): TurnsFilterOptions {
  return {
    projects: [],
    tasks: [],
    owners: [],
    clients: [],
    tags: [],
    models: [],
    providers: [],
    status_codes: [],
    ...over,
  } as TurnsFilterOptions
}

function filled(): TurnsFilterOptions {
  return opts({
    projects: ['proj-a'],
    tasks: ['task-a'],
    owners: ['alice'],
    clients: ['cli-1', 'app-1'],
    tags: ['prod'],
    models: ['glm-5.1'],
    providers: ['openai'],
    status_codes: ['200', '0'],
  })
}

// ── 装配 ────────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/turnsFilterOptions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/turnsFilterOptions')>()
  return { ...actual, fetchTurnsFilterOptions: vi.fn() }
})

const mFetch = fetchTurnsFilterOptions as unknown as ReturnType<typeof vi.fn>

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

/**
 * ★★★ 剥掉行注释与块注释，只留**代码**。
 *
 * ★★★ 否定断言若直接扫源码，会**误红自己的注释**（批 124 的教训）。
 * ⚠️★★ 本函数的注释里**不贴**「块注释结束符」的字面量：它会提前闭合本注释，
 *   报出的却是**下一行**的语法错误。
 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/TurnsFilterOptionsView.vue'), 'utf8')
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

type W = Awaited<ReturnType<typeof mountView>>
async function mountView() {
  const w = mount(TurnsFilterOptionsView)
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
async function click(w: W, id: string): Promise<void> {
  await byId(w, id).trigger('click')
  await flushPromises()
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.role = 'super_admin'
  mFetch.mockResolvedValue(filled())
})

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档 'admin'（服务端**没有**门 ⇒ 前端这一层是唯一的门）
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档「admin」（第四个写下错话的模块）', () => {
  it('①-1 ★★★★★★ 模块头那句「admin 档 ⇒ 抽屉席不设 requiresRole」**已被订正**', () => {
    // ★★★ 这是本族存在的理由：那四个字让前三批返工。
    const src = readFileSync(join(process.cwd(), 'src/api/turnsFilterOptions.ts'), 'utf8')
    const code = stripComments(src)
    // ★ 裸词会合法地出现在「我订正了它」的注释里 ⇒ 扫**剥掉注释后的代码**
    expect(code, '权限结论还写在模块头里，没被订正').not.toMatch(/admin 档.*不设 `requiresRole`/)
    expect(src, '订正说明应当保留在注释里（它是给后来者的）').toMatch(/原写的「⇒ 抽屉席不设/)
    expect(src, '订正说明里没点出这是第四个写下它的模块').toMatch(/第四个/)
  })

  it('①-2 ★★★★★★ 抽屉席存在、路径正确、且设 admin', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'turns-filter-options')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/turns-filter-options')
    expect(seat!.requiresRole).toBe('admin')
    expect(seat!.titleKey).toBe('nav.turnsFilterOptions')
  })

  it('①-3 ★★★★★★ 抽屉席真的被 navItemsFor 挡住：管理三档可见、user 不可见', () => {
    const seen = (role: string) => navItemsFor(DRAWER_NAV, role).some((i) => i.key === 'turns-filter-options')
    expect(seen('super_admin')).toBe(true)
    expect(seen('tenant_admin')).toBe(true)
    expect(seen('admin_key')).toBe(true)
    expect(seen('user')).toBe(false)
    expect(seen('operator_user')).toBe(false)
  })

  it('①-4 ★★★★★★ admin 档**不得**进 superAdmin 白名单', () => {
    const src = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    expect(src.indexOf("'turns-filter-options',"), 'admin 档进了 superAdmin 白名单 ⇒ 性质搞错了').toBe(-1)
  })

  it('①-5 ★★★★★★ 读 Go 源码：`admin(...)` 展开后**没有任何角色下限**', () => {
    const alias = goSrc('admin/handler.go').split('\n').find((l) => /^\s*admin := h\.admin\s*$/.test(l))
    expect(alias, '没找到 `admin := h.admin` 别名').toBeTruthy()
    const body = goFunc(goSrc('admin/auth.go'), 'AdminMiddleware')
    expect(body).toMatch(/func AdminMiddleware\(next http\.HandlerFunc/)
    // ★ 断言「没有」要否的是**角色比对这个形态**，而不是某个词
    expect(body).not.toMatch(/claims\.Role\s*(==|!=)/)
    const sup = goFunc(goSrc('pkg/identity/verifier.go'), 'supportedRole')
    expect(sup).toMatch(/case "user"/)
  })

  it('①-6 ★★★★★★ 读 Go 源码：注册行 `:1277` → `:1307`（批153）→ `:1342`（12h-audit 20261017）→ 实为 `:1349`（2026-10-21 views 行号订正轮）→ 实为 `:1352`（2026-10-26 行号漂移订正轮）且是 admin 档', () => {
    const lines = goSrc('admin/handler.go').split('\n')
    const i = lines.findIndex((l) => l.includes('"/api/admin/turns/sessions/filter-options"'))
    expect(i + 1, '模块头原写 :1277 → :1307 → :1342 → :1349 → 实为 :1352').toBe(1352)
    // ★★★ 逐字复核：这一行必须是 `admin(...)` 档，**不是** `superAdmin(...)`。
    expect(lines[i]).toMatch(/admin\(h\.handleTurnsFilterOptions\)/)
    expect(lines[i]).not.toMatch(/superAdmin/)
    // ★ 相邻的列表端点也是 admin 档（不是 superAdmin）⇒ 不能按前缀推
    const j = lines.findIndex((l) => l.includes('"/api/admin/turns/sessions"'))
    expect(lines[j]).toMatch(/admin\(h\.handleTurnsSessions\)/)
  })

  it('①-7 ★★★★★★ 读 Go 源码：每个维度都带租户条件 ⇒ 载荷是**租户内**的（2026-10-21 订正：turns 臂改传 tenantID 本身）', () => {
    // ★★ 这是「不过度限制到 super_admin」的**事实侧**（理由乙）。
    const src = goSrc('admin/turns_filter_options.go')
    const body = goFunc(src, 'handleTurnsFilterOptions')
    expect(body).toMatch(/if IsTenantAdmin\(r\) \{\s*\n\s*tenantID = GetTenantID\(r\)/)
    expect(body).toMatch(/tenantWhere = " AND ss\.tenant_id = \$1"/)
    // ★★ 2026-10-18 起 turns 臂不再拼 `turnTenantWhere` 串，而是**把 tenantID 本身**
    //   交给 `turnMergedSource(tenantID)`，由它在 SQL 里拼 ` AND tenant_id = $1`。
    //   ⇒ 租户条件**仍在**（只是拼法换了位置）⇒「载荷是租户内的」这条结论**不变**。
    expect(body).toMatch(/h\.turnMergedSource\(tenantID\)/)
    const turnSrc = goFunc(src, 'turnMergedSource')
    expect(stripComments(turnSrc), 'turns 臂的租户条件没了 ⇒ 「载荷是租户内的」这条结论不再成立').toMatch(
      /tc = " AND tenant_id = \$1"/,
    )
    // ⇒ 两个租户键都被拼进了查询，不存在「某个维度漏了租户条件」
    expect(body).not.toMatch(/lastActiveSource\(\s*"[^"]*"\s*,\s*joinDim,\s*"[^"]*"\s*\)/)
  })

  it('①-8 ★★★★★★ 租户覆盖被 `IsSuperAdminOrLegacy` 守着 ⇒ 不可提权', () => {
    // ★★ 模块头 (11) 的说法必须逐字为真，否则「永远不会跨租户」这句是空的。
    const body = goFunc(goSrc('admin/session_turns_v2.go'), 'tenantFromQueryOrContext')
    expect(body).toMatch(/if !IsSuperAdminOrLegacy\(r\) \{\s*\n\s*return GetTenantID\(r\)/)
    // ⇒ 只有超管/旧版管理员能读 ?tenant= / ?tenant_id= / X-Tenant-ID
    const after = body.slice(body.indexOf('IsSuperAdminOrLegacy'))
    expect(after).toMatch(/Query\(\)\.Get\("tenant"\)/)
    expect(after).toMatch(/Query\(\)\.Get\("tenant_id"\)/)
    expect(after).toMatch(/Header\.Get\("X-Tenant-ID"\)/)
  })

  it('①-9 ★★★★★★ 角色门随 role 变（不是恒真判据）', () => {
    expect(turnsFilterOptionsRoleNeedsAdminGate('user')).toBe(true)
    expect(turnsFilterOptionsRoleNeedsAdminGate('operator_user')).toBe(true)
    expect(turnsFilterOptionsRoleNeedsAdminGate(null)).toBe(true)
    expect(turnsFilterOptionsRoleNeedsAdminGate(undefined)).toBe(true)
    expect(turnsFilterOptionsRoleNeedsAdminGate('super_admin')).toBe(false)
    expect(turnsFilterOptionsRoleNeedsAdminGate('tenant_admin')).toBe(false)
    expect(turnsFilterOptionsRoleNeedsAdminGate('admin_key')).toBe(false)
  })

  it('①-10 ★★★★ 「租户内」判据随响应变（有租户键就说不过去）', () => {
    expect(turnsFilterOptionsIsTenantScoped(filled())).toBe(true)
    expect(turnsFilterOptionsIsTenantScoped(opts())).toBe(true)
    expect(turnsFilterOptionsIsTenantScoped(opts({ models: ['m'] }))).toBe(true)
    expect(turnsFilterOptionsIsTenantScoped({ ...filled(), tenant_id: 't1' })).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② 8 个维度 / 空维度是 [] / 空态
// ══════════════════════════════════════════════════════════════════════════

describe('② 8 个维度与空态', () => {
  it('②-1 ★★★★★★ 8 个维度**全部**渲染，一键不缺', async () => {
    const w = await mountView()
    await click(w, 'tfo-load')
    expect([...TURNS_FILTER_OPTION_KEYS]).toHaveLength(8)
    for (const k of TURNS_FILTER_OPTION_KEYS) {
      expect(hasId(w, `tfo-dim-${k}`), `缺维度 ${k}`).toBe(true)
      expect(hasId(w, `tfo-note-${k}`), `缺维度说明 ${k}`).toBe(true)
    }
  })

  it('②-2 ★★★★★★ 空维度是「有键但没值」⇒ 逐维度说「空」，不是整页空白', async () => {
    mFetch.mockResolvedValue(opts({ models: ['glm-5.1'] }))
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(hasId(w, 'tfo-none-models')).toBe(false)
    expect(textOf(w, 'tfo-item-models-0')).toBe('glm-5.1')
    for (const k of ['projects', 'tasks', 'owners', 'clients', 'tags', 'providers', 'status_codes']) {
      expect(hasId(w, `tfo-none-${k}`), `${k} 应该是空的`).toBe(true)
      expect(textOf(w, `tfo-none-${k}`)).toBe(t('tfo.dim.empty'))
    }
    expect(hasId(w, 'tfo-empty-dims')).toBe(true)
  })

  it('②-3 ★★★★★ 全空 ⇒ 空态提示，且不显示「有值」徽标', async () => {
    mFetch.mockResolvedValue(opts())
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(hasId(w, 'tfo-empty')).toBe(true)
    expect(hasId(w, 'tfo-nonempty')).toBe(false)
    expect(turnsFilterOptionsIsEmpty(opts())).toBe(true)
    expect(turnsFilterOptionsHasAnyValue(filled())).toBe(true)
    expect(turnsFilterOptionsHasAnyValue(opts())).toBe(false)
  })

  it('②-4 ★★★★ 非空但不是全空 ⇒ 两个提示都出（不是二选一）', async () => {
    // ⚠️ 首版误用了 `filled()`——它 8 个维度**全非空** ⇒ 「哪些维度为空」永远为空数组
    //   ⇒ 这条判据首跑就红（红得对：夹具与断言的意图不符）。
    mFetch.mockResolvedValue(opts({ models: ['glm-5.1'] }))
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(hasId(w, 'tfo-empty')).toBe(false)
    expect(hasId(w, 'tfo-nonempty')).toBe(true)
    expect(hasId(w, 'tfo-empty-dims')).toBe(true)
  })

  it('②-5 ★★★★ 取数失败 ⇒ 报错且不渲染任何维度', async () => {
    mFetch.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(hasId(w, 'tfo-error')).toBe(true)
    expect(hasId(w, 'tfo-dim-models')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ 时间窗口：**只有 3 个维度**有（模块头 (3)）
// ══════════════════════════════════════════════════════════════════════════

describe('③ 「近 30 天」这句话只对 3 个维度成立', () => {
  it('③-1 ★★★★★★ 页面上有窗口提醒，且说清是**部分**维度', async () => {
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(textOf(w, 'tfo-window-note')).toBe(t('tfo.windowNote', { days: 30 }))
    // ⚠️⚠️ 首版写 `expect(t('tfo.note.windowed')).toMatch(/30|window|窗口/i)`
    //   ⇒ **判据自己押了一种语言**（`t()` 取当前 locale，默认 en），
    //   而英文那条文案里既没有 `30`（它写 `{n}` 占位符）也没有 `window`
    //   ⇒ 首跑就红。★ 与批 125 的 ⑥-2 同一个坑 ⇒ 一律走 dictLookup。
    const zhWindowed = dictLookup(DICT['zh-CN'], 'tfo.note.windowed') as string
    const enWindowed = dictLookup(DICT['en-US'], 'tfo.note.windowed') as string
    expect(zhWindowed).toMatch(/窗口/)
    expect(enWindowed).toMatch(/last \{n\} days/i)
  })

  it('③-2 ★★★★★★ 三个「有窗口」维度用 windowed 说明，五个「全历史」用 allTime', async () => {
    const w = await mountView()
    await click(w, 'tfo-load')
    // ★★ 注意必须**带参**比较：视图渲染时传了 `{ n: windowDays }`
    //   ⇒ 未带参的 `t('tfo.note.windowed')` 里是字面 `{n}` ⇒ 这条判据当场
    //   抓出了「占位符没填」。（首版正是漏了这个参数而红。）
    for (const k of ['models', 'providers', 'status_codes']) {
      expect(textOf(w, `tfo-note-${k}`), `${k} 是有窗口的维度`).toBe(t('tfo.note.windowed', { n: 30 }))
      expect(textOf(w, `tfo-note-${k}`), `${k} 的说明漏掉了天数`).not.toContain('{n}')
    }
    // ★★ clients / tags 用的是**自己的** noteKey（要额外说明两件事），
    //   但它们同样**没有窗口** ⇒ 不变式是「说明里不得声称有窗口」，不是「文案逐字相同」。
    for (const k of ['projects', 'tasks', 'owners']) {
      expect(textOf(w, `tfo-note-${k}`), `${k} 没有窗口，说明写反了`).toBe(t('tfo.note.allTime'))
    }
    for (const k of ['clients', 'tags']) {
      const note = textOf(w, `tfo-note-${k}`)
      expect(note, `${k} 的说明声称了时间窗口（它没有）`).not.toMatch(/\{n\} days|\{n\} 天/)
      expect(note, `${k} 的说明没有点出「不受时间窗口约束」`).toMatch(
        /NOT time-windowed|不受时间窗口/,
      )
    }
  })

  it('③-3 ★★★★★★ 读 Go 源码：8 个维度现在**全部**受 30 天窗约束（2026-10-21 订正：turns 臂合并成一条 GROUPING SETS，ss 臂也统一加窗）', () => {
    // ★★★ 这条钉的是**事实侧**。
    // ★★★★★★ **2026-10-21 订正（Go 侧 2026-10-18 两次重构之后）**：
    //   旧结论「**只有 3 个维度**走带窗口的查询函数」**已不成立**：
    //   ① turns 臂的 3 个维度（models/providers/status_codes）合并成**一条**
    //      GROUPING SETS 查询（`h.turnMergedSource(tenantID)`）⇒
    //      `h.lastActiveTurnQuery(` 的调用点**归零**（函数本身仍在 `:246`，已无调用者）；
    //   ② ss 臂的 5 个维度也合并成一条（`h.ssMergedSource(tenantWhere)`），
    //      且经 `windowClause()` **统一加上了 30 天窗**（owner 裁定），
    //      tags 臂经 `lastActiveSource` 同样带窗
    //      ⇒ **8 个维度现在全部受 30 天窗约束**。
    //   ⇒ 维度归属（谁走哪条查询）改由**映射表**钉住，不再靠「数调用点」。
    // ⚠️ **与 ③-2 的矛盾（前端文案尚未跟上，待上游修）**：③-2 断言前端仍写
    //   「5 个维度不受时间窗口约束」—— 那是 `src/api/turnsFilterOptions.ts` 与
    //   i18n 文案的**现状**（不在本次 views 范围），但**与 Go 侧现状矛盾**。
    const src = goSrc('admin/turns_filter_options.go')
    expect(src).toMatch(/turnsFilterOptionsWindowDays\s*=\s*30/)
    const body = goFunc(src, 'handleTurnsFilterOptions')
    // 维度 → 查询臂的映射（结构性事实，不受「合并成一条」影响）
    expect(body).toMatch(/"1": &resp\.Models, "2": &resp\.Providers, "3": &resp\.StatusCodes/)
    expect(body).toMatch(/"1": &resp\.Projects, "2": &resp\.Tasks, "3": &resp\.Owners/)
    // ★★ 三条查询源各一份：ss 臂（5 维）/ tags 臂（1 维）/ turns 臂（3 维）
    expect(body.match(/h\.ssMergedSource\(/g) ?? []).toHaveLength(1)
    expect(body.match(/h\.turnMergedSource\(/g) ?? []).toHaveLength(1)
    expect(body.match(/h\.lastActiveSource\(/g) ?? []).toHaveLength(1)
    // ⇒ 三条源**都**带 30 天窗（ss 臂走 windowClause()，另两条走 INTERVAL 模板）
    const ssQ = goFunc(src, 'ssMergedSource')
    expect(ssQ).toMatch(/windowClause\(\)/)
    const turnQ = goFunc(src, 'turnMergedSource')
    expect(turnQ).toMatch(/INTERVAL '%d days'/)
    const tagQ = goFunc(src, 'lastActiveSource')
    expect(tagQ).toMatch(/INTERVAL '%d days'/)
    // ⇒ 旧形态（逐维调 `h.lastActiveTurnQuery(`）已归零
    expect(body.match(/h\.lastActiveTurnQuery\(/g) ?? []).toHaveLength(0)
    // ⇒ 而 `lastActiveQuery` 只是外层包装（`SELECT v FROM (…) t ORDER BY …`），
    //   时间条件**不在**它体内 —— 断言的是「窗在内层 source 里」。
    const wrapper = goFunc(src, 'lastActiveQuery')
    expect(wrapper).not.toMatch(/INTERVAL/)
  })

  it('③-4 ★★★★ `TURNS_TURN_DIMENSION_WINDOW_DAYS` 的注释也只说 3 个维度', () => {
    const src = readFileSync(join(process.cwd(), 'src/api/turnsFilterOptions.ts'), 'utf8')
    expect(src).toMatch(/只有 3 个维度用得到这个窗口/)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④ status_codes 是数字串（模块头 (7)）
// ══════════════════════════════════════════════════════════════════════════

describe('④ `status_codes` 是 integer::text，不是数字', () => {
  it('④-1 ★★★★★★ 元素按字符串渲染，不被当成数字排序/格式化', async () => {
    mFetch.mockResolvedValue(opts({ status_codes: ['200', '0', '404'] }))
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(textOf(w, 'tfo-item-status_codes-0')).toBe('200')
    expect(textOf(w, 'tfo-item-status_codes-1')).toBe('0')
    expect(textOf(w, 'tfo-item-status_codes-2')).toBe('404')
    expect(hasId(w, 'tfo-bad-status')).toBe(false)
  })

  it('④-2 ★★★★★★ 非法值**不丢**，单独标出来而不是悄悄丢掉', async () => {
    // ★★ `statusCodeFilterValue` 给 `null` 表示「不是合法状态码」
    //   ⇒ 调用方应当**忽略**它，而不是传 NaN ⇒ 页面上必须看得见。
    mFetch.mockResolvedValue(opts({ status_codes: ['200', '', '200abc', '0x10'] }))
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(hasId(w, 'tfo-bad-status')).toBe(true)
    expect(textOf(w, 'tfo-bad-status')).toContain('200abc')
    expect(textOf(w, 'tfo-bad-status')).toContain('0x10')
    // ★ 四个坏值一个都没被从 DOM 里抹掉
    expect(w.findAll('[data-testid^="tfo-item-status_codes-"]')).toHaveLength(4)
  })

  it('④-3 ★★★★★★ 转换规则：只认十进制整数字面量', () => {
    expect(statusCodeFilterValue('200')).toBe(200)
    expect(statusCodeFilterValue(' 0 ')).toBe(0)
    expect(statusCodeFilterValue('-1')).toBe(-1)
    // ★★★ 这四个都是「静默把坏值变成好值」的坑
    expect(statusCodeFilterValue('')).toBeNull()
    expect(statusCodeFilterValue('   ')).toBeNull()
    expect(statusCodeFilterValue('200abc')).toBeNull()
    expect(statusCodeFilterValue('1e3')).toBeNull()
    expect(statusCodeFilterValue('0x10')).toBeNull()
    expect(statusCodeFilterValue('200.5')).toBeNull()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑤ 截断 与 api_keys（模块头 (1)(6)）
// ══════════════════════════════════════════════════════════════════════════

describe('⑤ 截断提示与「不该渲染的那把下拉」', () => {
  it('⑤-1 ★★★★★★ 正好 20 个**也算**可能截断（`>=` 不是 `>`）', () => {
    expect(TURNS_FILTER_OPTION_LIMIT).toBe(20)
    expect(turnsFilterOptionIsTruncated(new Array(20).fill('x'))).toBe(true)
    expect(turnsFilterOptionIsTruncated(new Array(19).fill('x'))).toBe(false)
    expect(turnsFilterOptionIsTruncated([])).toBe(false)
  })

  it('⑤-2 ★★★★★ 页面逐维度显示截断徽标，且只给够长的维度', async () => {
    mFetch.mockResolvedValue(
      opts({ models: new Array(20).fill('m'), providers: new Array(3).fill('p') }),
    )
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(hasId(w, 'tfo-cut-models')).toBe(true)
    expect(hasId(w, 'tfo-cut-providers')).toBe(false)
    expect(hasId(w, 'tfo-cut-status_codes')).toBe(false)
  })

  it('⑤-3 ★★★★★★ 页面**明说**没有 `api_keys` 这把下拉（桌面那把永远是空的）', async () => {
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(hasId(w, 'tfo-no-apikeys')).toBe(true)
    expect(textOf(w, 'tfo-no-apikeys')).toBe(t('tfo.noApiKeys'))
    // ⚠️⚠️ 首版写 `expect(w.html()).not.toContain('api_keys')` —— 首跑就红：
    //   **我自己写的告示文案里就含 `api_keys` 这个词**（那是本条要说明的内容）。
    //   ⇒ 「否定断言要否的应是代码形态而不是词」的**又一次**同族踩坑。
    // ⇒ 正确形态：断言**没有承载 api_keys 的元素**（控件 / testid / 标签），而不是没有这个词。
    expect(w.findAll('[data-testid*="api-key"]')).toHaveLength(0)
    expect(w.findAll('[data-testid*="api_key"]')).toHaveLength(0)
    expect(w.findAll('select')).toHaveLength(0)
    // ⇒ 词只允许出现在那条告示段落里
    expect(w.findAll('[data-testid="tfo-no-apikeys"]')).toHaveLength(1)
    expect(w.html().split('api_keys').length - 1).toBe(1)
  })

  it('⑤-4 ★★★★★★ 即使响应里**多带了** `api_keys`，页面也不渲染它', async () => {
    // ★ 后端恒不返回这个键；万一某天返回了，**也不该**把它当一个有数据的筛选项
    mFetch.mockResolvedValue({
      ...filled(),
      api_keys: [{ id: 1, label: 'sk-xxx' }],
    } as unknown as TurnsFilterOptions)
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(hasId(w, 'tfo-no-apikeys')).toBe(true)
    expect(w.findAll('[data-testid*="api-key"]')).toHaveLength(0)
    expect(w.html(), '把后端从不返回的 api_keys 渲染出来了').not.toContain('sk-xxx')
  })

  it('⑤-5 ★★★★ 8 个后端键里**没有** api_keys（这是「恒假」的事实侧）', () => {
    expect([...TURNS_FILTER_OPTION_KEYS]).not.toContain('api_keys')
    expect([...TURNS_FILTER_OPTION_KEYS]).toEqual([
      'projects', 'tasks', 'owners', 'clients', 'tags', 'models', 'providers', 'status_codes',
    ])
  })

  it('⑤-6 ★★★★ clients 维度说明「混着两种来源」', async () => {
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(textOf(w, 'tfo-clients-note')).toContain('application_code')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 深链兜底 / 只读面 / 接线与编码门
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 深链兜底、只读面与接线', () => {
  it('⑥-1 ★★★★★★ 非管理员 ⇒ 显示 nope，且一次请求都不发', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(hasId(w, 'tfo-nope')).toBe(true)
    expect(hasId(w, 'tfo-load')).toBe(false)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('⑥-2 ★★★★★★ nope 页面说清「这一层是唯一的门」—— 后端不校验角色', async () => {
    authState.role = 'user'
    const w = await mountView()
    expect(textOf(w, 'tfo-nope-msg')).toBe(t('tfo.nope.msg'))
    expect(textOf(w, 'tfo-nope-why')).toBe(t('tfo.nope.why'))
    // ★★ 两个语种都要点明「后端只校验登录、不校验角色」这个**事实**
    const zhWhy = dictLookup(DICT['zh-CN'], 'tfo.nope.why') as string
    const enWhy = dictLookup(DICT['en-US'], 'tfo.nope.why') as string
    for (const [loc, v] of [['zh-CN', zhWhy], ['en-US', enWhy]] as const) {
      expect(v, `${loc} 的 nope 文案声称了 403（本族服务端并不挡）`).not.toContain('403')
    }
    expect(zhWhy).toMatch(/角色/)
    expect(enWhy).toMatch(/role/i)
  })

  it('⑥-3 ★★★★★ 主页面也说明「这一层是唯一的门」', async () => {
    const w = await mountView()
    await click(w, 'tfo-load')
    expect(textOf(w, 'tfo-gate-note')).toBe(t('tfo.gate.note'))
    expect(textOf(w, 'tfo-no-tenant')).toBe(t('tfo.tenant.scoped'))
  })

  it('⑥-4 ★★★★ 页面声明「只读」，且没有任何写控件', async () => {
    const w = await mountView()
    expect(textOf(w, 'tfo-readonly-note')).toBe(t('tfo.readonly.note'))
    for (const verb of ['POST', 'PUT', 'DELETE', 'PATCH']) {
      expect(w.html(), `页面里出现了 ${verb}`).not.toContain(verb)
    }
    expect(w.findAll('input')).toHaveLength(0)
    expect(w.findAll('form')).toHaveLength(0)
  })

  it('⑥-5 ★★★★★★ 路由存在，且**路由 meta 上的角色档**是 admin', () => {
    // ⚠️ 与抽屉席是**两道相互独立的门**（批 125 的教训），两处都要钉。
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toMatch(/path: '\/turns-filter-options'/)
    const i = src.indexOf("name: 'turns-filter-options'")
    expect(i).toBeGreaterThan(-1)
    const line = src.slice(i, src.indexOf('},', i)).split('\n').find((l) => l.includes('meta:')) ?? ''
    expect(line, '这一条路由没有 meta 行').not.toBe('')
    expect(line).toMatch(/requiresRole: 'admin'/)
    expect(line).toMatch(/requiresAuth: true/)
  })

  it('⑥-6 ★★★★ i18n 两个语种都有 tfo.* 全部键，且值不等于键名', () => {
    const keys = [
      'nav.turnsFilterOptions',
      'tfo.title',
      'tfo.nope.msg',
      'tfo.nope.why',
      'tfo.gate.note',
      'tfo.readonly.note',
      'tfo.load.title',
      'tfo.load.btn',
      'tfo.state.title',
      'tfo.empty',
      'tfo.emptyDims',
      'tfo.dim.empty',
      'tfo.dim.models',
      'tfo.dim.providers',
      'tfo.dim.statusCodes',
      'tfo.dim.projects',
      'tfo.dim.tasks',
      'tfo.dim.owners',
      'tfo.dim.clients',
      'tfo.dim.tags',
      'tfo.note.windowed',
      'tfo.note.allTime',
      'tfo.note.clients',
      'tfo.note.tags',
      'tfo.windowNote',
      'tfo.clientsNote',
      'tfo.noApiKeys',
      'tfo.tenant.scoped',
      'tfo.truncated',
      'tfo.badStatus',
      'tfo.badge.nonEmpty',
    ]
    for (const loc of ['zh-CN', 'en-US'] as const) {
      for (const k of keys) {
        const v = dictLookup(DICT[loc], k)
        expect(typeof v, `${loc} 缺 ${k}`).toBe('string')
        expect(v, `${loc} 的 ${k} 值等于键名`).not.toBe(k)
      }
    }
  })

  it('⑥-7 ★★★★ 孤儿棘轮：本族已移出「真缺口」清单', () => {
    const src = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    const i = src.indexOf('KNOWN_ORPHANS')
    const j = src.indexOf(']', i)
    expect(src.slice(i, j), '本族还在真缺口清单里').not.toContain('turnsFilterOptions')
  })

  it('⑥-8 ★★★★ 新触控控件 ≥48 CSS px（解析数值再比大小）', () => {
    const css = viewSrc()
    expect(css).toMatch(/\.tfo__btn\s*\{[^}]*min-height:\s*(\d+)px/)
    const minH = Number(/min-height:\s*(\d+)px/.exec(css)![1])
    expect(minH).toBeGreaterThanOrEqual(48)
  })

  it('⑥-9 ★★★ 编码门：视图、本文件与两个字典不得含 U+FFFD / NUL', () => {
    for (const p of [
      'src/views/TurnsFilterOptionsView.vue',
      'src/views/TurnsFilterOptionsView.spec.ts',
      'src/api/turnsFilterOptions.ts',
      'src/i18n/zh-CN.ts',
      'src/i18n/en-US.ts',
    ]) {
      const b = readFileSync(join(process.cwd(), p))
      const s = b.toString('utf8')
      // ★★ 被查的那个码点**绝不能**写成字面量：判据自己就被自己判红
      expect(s.includes(String.fromCharCode(0xfffd)), `${p} 含 U+FFFD`).toBe(false)
      expect(b.includes(0), `${p} 含 NUL`).toBe(false)
    }
  })

  it('⑥-10 ★★★ 视图里不得存在恒定的「假门」computed（剥注释后扫）', () => {
    const code = stripComments(viewSrc())
    expect(code).not.toMatch(/computed\(\(\) => true\)/)
    expect(code).not.toMatch(/computed\(\(\) => false\)/)
  })

  it('⑥-11 ★★★ 视图不出现「super_admin 档」的说法（档位已定为 admin）', () => {
    const code = stripComments(viewSrc())
    expect(code).not.toMatch(/requiresRole:\s*'super_admin'/)
    expect(t('tfo.gate.note')).not.toContain('super_admin')
  })

  it('⑥-12 ★★★ i18n 字典里的 tfo.dim 必须**一键一行**（门解析不了「一行多对」）', () => {
    for (const [name, p] of [
      ['zh-CN', 'src/i18n/zh-CN.ts'],
      ['en-US', 'src/i18n/en-US.ts'],
    ] as const) {
      const lines = readFileSync(join(process.cwd(), p), 'utf8').split('\n')
      const idx = lines.findIndex((l) => l.includes('tfo: {'))
      expect(idx, `${name} 里找不到 tfo 块`).toBeGreaterThan(-1)
      const body = lines.slice(idx, idx + 60).join('\n')
      expect(body, `${name} 的 tfo.dim 里有「一行多对」`).not.toMatch(/^\s*tfo\.dim:\s*\{[^\n]*,\s*\S+:/m)
    }
  })
})
