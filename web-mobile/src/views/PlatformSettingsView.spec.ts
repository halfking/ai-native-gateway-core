import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import PlatformSettingsView from './PlatformSettingsView.vue'
import { setLocale, t } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchSettingsList,
  fetchSettingHistory,
  settingsAuditCarriesOperatorIdentity,
  settingsHistoryIsCrossTenant,
  settingsWriteNeedsSuperAdmin,
  settingsDangerLevelName,
  settingsValueIsNullIffTenantScope,
  SETTINGS_DANGER_SUPER_ADMIN_THRESHOLD,
  SETTINGS_AUDIT_OPERATOR_KEYS,
  type SettingsListResponse,
  type SettingsListItem,
  type SettingsHistoryResponse,
} from '@/api/platformSettings'

/**
 * 平台设置面回归护栏（2026-10-14，第一百二十一批）。
 *
 * ★★★★★★ **本批的核心不是「接进 UI」，是「订正一个错的权限结论」。**
 *
 *   `api/platformSettings.ts` 的 (1) 原先写着：
 *   「三个端点的权限档位是同一个：都是 `h.admin(...)` ⇒ admin 档
 *   （tenant_admin 可用，抽屉席**不设** `requiresRole`）」。
 *
 *   ★ 错在**只读了注册处那一行**。而注册处是：
 *     `admin/settings.go:24` `mux.HandleFunc("/api/admin/settings/", h.admin(h.settingsRouter))`
 *     —— 它包的是**一个会再分发到四个 handler 的 router**，不是叶子 handler。
 *
 *   ⇒ ★★★★ `mux.HandleFunc(prefix, h.admin(h.someRouter))` 这种形态，
 *     **权限必须逐个子端点读**；`h.admin` 与 URL 前缀**都推不出来**。
 *
 *   逐字读完 `settingsRouter`（`settings.go:96-114`）与四个 handler 的结果：
 *
 *   | 子端点 | 分发 | handler 里的 role 检查 |
 *   |---|---|---|
 *   | GET {key}            | :104 | ★★ **零** |
 *   | PUT {key}            | :106 | ★ `settings.go:236` 仅当 `DangerLevel >= Dangerous` |
 *   | GET {key}/history    | :108 | ★★ **零** |
 *   | POST {key}/rollback  | :110 | ★★★ **零**（连那道 DangerLevel 门都没有）|
 *
 *   `h.admin` = `AdminMiddleware`（`handler.go:894` → `auth.go:85`）**只认证不检 role**
 *   ⇒ 「三个 GET 后端零 role 门」这半句是对的；**错的是「所以抽屉席不设 requiresRole」。**
 *
 * ★★★★★ **过度限制到 `super_admin` 的两条独立理由**（都不是「有没有 tenant 条件」）：
 *   ① **载荷含别人的身份数据**：history 传 `tid=""`，而 `operator_user` /
 *      `operator_role` 是**恒在键** ⇒ tenant_admin 能读到**别的操作者**的身份；
 *   ② **写路径提权**：rollback 无 DangerLevel 门 ⇒ 能 PUT 不了的档能直接 rollback。
 *   ⇒ ⇒ 这两条都是**后端缺陷**（本批不动后端，已登记待办）；
 *     **前端这一层是兜底不是修复** —— 但与批 116/120 不同，
 *     这里在服务端补门之前，它**就是**唯一挡在 tenant_admin 与别人身份数据之间的东西。
 *
 * 判据钉七组：
 *  ① ★★★★★★ 权限档 super_admin，且**逐个子端点**从 Go 源码钉「零 role 门」。
 *  ② ★★★★★ payload 恒带操作者身份 ＋ history 跨租户。
 *  ③ ★★★★ `value === null` ⟺ tenant 作用域（双向不变量）。
 *  ④ ★★★★ `danger_level >= 2` 的档必须被标出来。
 *  ⑤ ★★★★★ 深链兜底：**不发请求**。
 *  ⑥ ★★★ 历史的合法形态（`items: null` 不是错）与排序/上限。
 *  ⑦ ★★★ 接线（路由/抽屉/白名单/i18n/孤儿棘轮）与编码门。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function item(over: Partial<SettingsListItem> = {}): SettingsListItem {
  return {
    key: 'ratelimit_enabled',
    type: 'bool',
    scope: 'platform',
    category: 'ratelimit',
    value: true,
    source: 'default',
    danger_level: 0,
    ...over,
  } as SettingsListItem
}

function list(items: SettingsListItem[]): SettingsListResponse {
  return { items }
}

function history(over: Partial<SettingsHistoryResponse> = {}): SettingsHistoryResponse {
  return {
    items: [
      {
        setting_key: 'ratelimit_enabled',
        action: 'update',
        operator_user: 'alice',
        operator_role: 'super_admin',
        created_at: '2026-10-14T10:00:00Z',
      },
      {
        setting_key: 'ratelimit_enabled',
        action: 'rollback',
        operator_user: 'bob',
        operator_role: 'tenant_admin',
        created_at: '2026-10-13T09:00:00Z',
      },
    ],
    ...over,
  }
}

// ── 装配 ──────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/platformSettings', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/platformSettings')>()
  return { ...actual, fetchSettingsList: vi.fn(), fetchSettingHistory: vi.fn() }
})

const mList = fetchSettingsList as unknown as ReturnType<typeof vi.fn>
const mHist = fetchSettingHistory as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(PlatformSettingsView)
  await flushPromises()
  return w
}
function byId(w: W, id: string) {
  return w.find(`[data-testid="${id}"]`)
}
function hasId(w: W, id: string): boolean {
  return w.findAll(`[data-testid="${id}"]`).length > 0
}
/** ★★ 文本断言一律用 `text()`，**不用** `html()` —— 后者会序列化模板注释。 */
function textOf(w: W, id: string): string {
  return byId(w, id).text()
}
function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/PlatformSettingsView.vue'), 'utf8')
}
/** ★ 读源码前先让路径写错**硬失败**，否则 expect 落在别处很难查。 */
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}
/**
 * ★ 取 Go 函数体：从定义行起，直到遇到下一个顶层 `func `。
 * ★★ **两种定义形态都要认**：方法 `func (h *Handler) x(` 与
 *    普通函数 `func x(` —— `AdminMiddleware` 是后者，只认方法会永远找不到它。
 */
function goFunc(src: string, name: string): string {
  const lines = src.split('\n')
  const start = lines.findIndex(
    (l) => l.startsWith(`func (h *Handler) ${name}(`) || l.startsWith(`func ${name}(`),
  )
  expect(start, `没找到 ${name} 的定义（正则失配会让这条恒绿）`).toBeGreaterThan(-1)
  let end = start + 1
  // ★ 用可选链而不是 `lines[end].startsWith`：本仓开了 noUncheckedIndexedAccess，
  //   越界索引的类型是 `string | undefined`，TS 不会因为 `end < lines.length` 就收窄。
  while (end < lines.length && !lines[end]?.startsWith('func ')) end++
  return lines.slice(start, end).join('\n')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  mList.mockResolvedValue(list([item()]))
  mHist.mockResolvedValue(history())
})

async function load(w: W) {
  await byId(w, 'ps-load').trigger('click').then(() => flushPromises())
}
async function openHistory(w: W, key = 'ratelimit_enabled') {
  await byId(w, `ps-history-${key}`).trigger('click').then(() => flushPromises())
}

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档 super_admin —— 且逐个子端点从 Go 源码钉「零 role 门」
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档第三类（且理由比前三席都硬）', () => {
  it('①-1 ★★★★★★ 抽屉席存在、路径正确、且设 super_admin', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'platform-settings')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/platform-settings')
    expect(seat!.requiresRole).toBe('super_admin')
    expect(seat!.titleKey).toBe('nav.platformSettings')
  })

  it('①-2 ★★★★★★ 读 Go 源码：注册处包的是 **router**，不是叶子 handler', () => {
    // ★★★ 这是本批最重要的**形态**判据：按这行读会读出「admin 档」，而那是错的。
    const src = goSrc('admin/settings.go')
    const m = src.match(
      /mux\.HandleFunc\("\/api\/admin\/settings\/", h\.admin\(h\.settingsRouter\)\)/,
    )
    expect(m, '注册处形状变了；权限结论必须重判').toBeTruthy()
    const router = goFunc(src, 'settingsRouter')
    // ★★ 分发到**四个**子端点：少一个都说明后端变了，结论要重判。
    for (const call of ['h.settingsGet(', 'h.settingsPut(', 'h.settingsHistory(', 'h.settingsRollback(']) {
      expect(router, `settingsRouter 不再分发 ${call}`).toContain(call)
    }
  })

  it('①-3 ★★★★★★ 读 Go 源码：get 与 history **全文零 role 检查**', () => {
    const src = goSrc('admin/settings.go')
    // ★ 这两条正是「按注册处读会读错」的根源：它们自己根本不检 role。
    expect(goFunc(src, 'settingsGet')).not.toMatch(/IsSuperAdminOrLegacy|IsTenantAdmin|super_admin/)
    expect(goFunc(src, 'settingsHistory')).not.toMatch(/IsSuperAdminOrLegacy|IsTenantAdmin|super_admin/)
    // ★★ 对照组：PUT **确实**有门，而且是按 setting 的 DangerLevel 分档的。
    const put = goFunc(src, 'settingsPut')
    expect(put).toMatch(/sp\.DangerLevel >= settings\.Dangerous && !IsSuperAdminOrLegacy\(r\)/)
  })

  it('①-4 ★★★★★★ 读 Go 源码：rollback **连那道 DangerLevel 门都没有**（后端缺陷）', () => {
    // ★★★ 回滚路径比写入路径权限更大：能 PUT 不了的档能直接 rollback。
    //   这是本批新登记的后端缺陷②，前端严于后端**只是兜底，不是修复**。
    const rb = goFunc(goSrc('admin/settings.go'), 'settingsRollback')
    expect(rb).not.toMatch(/IsSuperAdminOrLegacy|IsTenantAdmin|super_admin/)
    expect(rb).not.toMatch(/DangerLevel/)
  })

  it('①-5 ★★★★★ `h.admin` = AdminMiddleware：只认证，不检 role', () => {
    const h = goSrc('admin/handler.go')
    expect(h).toMatch(/func \(h \*Handler\) admin\(fn http\.HandlerFunc\) http\.HandlerFunc \{\s*\n\s*return AdminMiddleware\(fn, h\.db, h\.secret\)/)
    // ★★ 中间件本体里唯一的 403 是**强制改密码**，不是 role 门。
    const mw = goSrc('admin/auth.go')
    const body = goFunc(mw, 'AdminMiddleware')
    expect(body).toMatch(/errPasswordChangeRequired/)
    expect(body).not.toMatch(/super_admin/)
  })

  it('①-6 ★★★★ 进 superAdmin 白名单（字母序在 work-types 之前）', () => {
    const src = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    const at = (k: string) => src.indexOf(`'${k}',`)
    expect(at('platform-settings'), '白名单里没有这一席').toBeGreaterThan(-1)
    expect(at('platform-settings')).toBeLessThan(at('work-types'))
  })

  it('①-7 ★★★★ 反向护栏：不得退化成 admin 档或无档', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'platform-settings')!
    expect(seat.requiresRole).not.toBe('admin')
    expect(seat.requiresRole).not.toBeUndefined()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② payload 含别人的身份 ＋ history 跨租户
// ══════════════════════════════════════════════════════════════════════════

describe('② 为什么必须过度限制：载荷里有别人的身份', () => {
  it('②-1 ★★★★★ 审计项恒带 operator_user / operator_role（恒在键）', () => {
    const e = history().items![0]!
    expect(settingsAuditCarriesOperatorIdentity(e)).toBe(true)
    // ★★ 逐键钉住，避免哪天改名后「载荷里有没有别人的东西」悄悄变 false。
    for (const k of ['operator_user', 'operator_role']) {
      expect(e, `${k} 不该是可选键`).toHaveProperty(k)
    }
    expect(SETTINGS_AUDIT_OPERATOR_KEYS).toContain('operator_user')
    expect(SETTINGS_AUDIT_OPERATOR_KEYS).toContain('operator_role')
  })

  it('②-2 ★★★★★ 少一个身份键 ⇒ 判据转 false（判别力不是恒真）', () => {
    const e = history().items![0]!
    expect(settingsAuditCarriesOperatorIdentity({ ...e, operator_role: undefined as never })).toBe(false)
    expect(settingsAuditCarriesOperatorIdentity({ ...e, operator_user: undefined as never })).toBe(false)
  })

  it('②-3 ★★★★★ history 跨租户 ⇒ 页面必须把它当红线说出来', async () => {
    const w = await mountView()
    await load(w)
    await openHistory(w)
    expect(settingsHistoryIsCrossTenant(history())).toBe(true)
    expect(hasId(w, 'ps-history-cross-tenant')).toBe(true)
    expect(textOf(w, 'ps-history-cross-tenant')).toBe(t('ps.check.historyCrossTenant'))
  })

  it('②-4 ★★★★ 带 tenant_id 的历史就不算跨租户（阴性对照）', () => {
    expect(
      settingsHistoryIsCrossTenant(history({ items: [{ ...history().items![0]!, tenant_id: 'acme' }] })),
    ).toBe(false)
  })

  it('②-5 ★★★★★ 页面把操作者身份**显示出来**（这正是要挡的东西）', async () => {
    const w = await mountView()
    await load(w)
    await openHistory(w)
    const rows = w.findAll('[data-testid="ps-history-item"]')
    expect(rows.length).toBe(2)
    expect(w.findAll('[data-testid="ps-history-operator"]')[0]!.text()).toBe('alice')
    expect(w.findAll('[data-testid="ps-history-role"]')[1]!.text()).toBe('tenant_admin')
    expect(textOf(w, 'ps-history-operators')).toBe(t('ps.check.operators', { n: 2 }))
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ value === null ⟺ tenant 作用域（双向）
// ══════════════════════════════════════════════════════════════════════════

describe('③ value 与 scope 的双向不变量', () => {
  it('③-1 ★★★★★ platform 作用域 ⇒ value 非 null 且 source 非空', () => {
    expect(settingsValueIsNullIffTenantScope(item({ scope: 'platform', value: true, source: 'default' }))).toBe(true)
  })

  it('③-2 ★★★★★ tenant 作用域 ⇒ value 为 null 且 source 为空串', () => {
    const it = item({ scope: 'tenant', value: null, source: '' })
    expect(settingsValueIsNullIffTenantScope(it)).toBe(true)
  })

  it('③-3 ★★★★ 不变量被打破 ⇒ 报红线（scopeBad / sourceBad 各自独立触发）', async () => {
    // ★★★ 两个夹具要**分别**打中两条线，且**计数必须不对称**，否则判据没有判别力。
    //   · a1 / a2：platform 作用域却 value=null ⇒ 只有 scope 不变量破（**两条**）
    //   · b     ：tenant 作用域、value=null **但 source 非空串** ⇒ 只有 source 不变量破
    //   ⇒ ★ 若两类各只放一条，把 scopeMismatch 的取负去掉后计数**恰好不变**
    //     （原来 [a] 变成 [b]），断言照样绿 —— 变异台 S3 首轮就是这么「红了没命中」的。
    //     两类计数不同，极性才可辨。
    //   ⇒ 另：视图的 sourceMismatch 额外依赖 `settingsValueIsNullForTenantScope`
    //     （要求 value === null），所以 b 的 value **必须是 null**，
    //     否则它会被 scopeMismatch 吃掉、source-bad 永不亮。
    mList.mockResolvedValue(
      list([
        item({ key: 'a1', scope: 'platform', value: null, source: 'default' }),
        item({ key: 'a2', scope: 'platform', value: null, source: 'default' }),
        item({ key: 'b', scope: 'tenant', value: null, source: 'default' }),
      ]),
    )
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ps-scope-bad')).toBe(true)
    expect(textOf(w, 'ps-scope-bad')).toBe(t('ps.check.scopeBad', { n: 2 }))
    expect(hasId(w, 'ps-source-bad')).toBe(true)
    expect(textOf(w, 'ps-source-bad')).toBe(t('ps.check.sourceBad', { n: 1 }))
  })

  it('③-4 ★★★★ 正常载荷 ⇒ 两条都不报（阴性对照）', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ps-scope-bad')).toBe(false)
    expect(hasId(w, 'ps-source-bad')).toBe(false)
  })

  it('③-5 ★★★ source 为空串时渲染占位符，不留空白格', async () => {
    mList.mockResolvedValue(list([item({ key: 'a', scope: 'tenant', value: null, source: '' })]))
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'ps-src-a')).toBe('—')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④ danger_level 分档
// ══════════════════════════════════════════════════════════════════════════

describe('④ danger_level：危险档必须被标出来', () => {
  it('④-1 ★★★★ 阈值是 2（dangerous 起）', () => {
    expect(SETTINGS_DANGER_SUPER_ADMIN_THRESHOLD).toBe(2)
    expect(settingsWriteNeedsSuperAdmin(1)).toBe(false)
    expect(settingsWriteNeedsSuperAdmin(2)).toBe(true)
    expect(settingsWriteNeedsSuperAdmin(3)).toBe(true)
  })

  it('④-2 ★★★★ 危险档带 data-danger=yes，且计数出现在自检里', async () => {
    mList.mockResolvedValue(
      list([item({ key: 'safe1', danger_level: 0 }), item({ key: 'danger1', danger_level: 2 })]),
    )
    const w = await mountView()
    await load(w)
    expect(byId(w, 'ps-danger-danger1').attributes('data-danger')).toBe('yes')
    expect(byId(w, 'ps-danger-safe1').attributes('data-danger')).toBe('no')
    expect(textOf(w, 'ps-gated')).toBe(t('ps.check.gated', { n: 1 }))
  })

  it('④-3 ★★★ 档位名可读，且未知档位不炸', () => {
    expect(settingsDangerLevelName(0)).toBe('safe')
    expect(settingsDangerLevelName(3)).toBe('breaking')
    expect(settingsDangerLevelName(9)).toBe('unknown')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑤ 深链兜底
// ══════════════════════════════════════════════════════════════════════════

describe('⑤ 深链兜底：兜的是「不发请求」', () => {
  it('⑤-1 ★★★★★ tenant_admin ⇒ 显示 nope，且**一次请求都不发**', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    await flushPromises()
    expect(hasId(w, 'ps-nope')).toBe(true)
    expect(mList).not.toHaveBeenCalled()
    expect(hasId(w, 'ps-load')).toBe(false)
  })

  it('⑤-2 ★★★★★ nope 页面把「为什么」讲清楚（含跨租户身份数据）', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    await flushPromises()
    expect(textOf(w, 'ps-nope-msg')).toBe(t('ps.nope.msg'))
    expect(textOf(w, 'ps-nope-why')).toBe(t('ps.nope.why'))
    expect(t('ps.nope.why')).toContain('跨租户')
    expect(mList).not.toHaveBeenCalled()
  })

  it('⑤-3 ★★★★ nope 分支里连清单与历史徽标都没有', async () => {
    authState.role = 'user'
    const w = await mountView()
    await flushPromises()
    expect(hasId(w, 'ps-count')).toBe(false)
    expect(hasId(w, 'ps-history-sec')).toBe(false)
  })

  it('⑤-4 ★★★ super_admin ⇒ 不显示 nope，且正常发请求', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ps-nope')).toBe(false)
    expect(mList).toHaveBeenCalledTimes(1)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 历史的合法形态
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 历史：`items: null` 是合法形态，不是错', () => {
  it('⑥-1 ★★★★★ `items: null` ⇒ 显示空态，**不报任何红线**', async () => {
    mHist.mockResolvedValue(history({ items: null }))
    const w = await mountView()
    await load(w)
    await openHistory(w)
    expect(hasId(w, 'ps-history-empty')).toBe(true)
    expect(hasId(w, 'ps-history-order-bad')).toBe(false)
    expect(hasId(w, 'ps-history-limit-bad')).toBe(false)
    expect(textOf(w, 'ps-history-empty')).toBe(t('ps.history.empty'))
  })

  it('⑥-2 ★★★★ 顺序逆序 ⇒ 报 order-bad（无 tiebreak，只能断言非升序）', async () => {
    mHist.mockResolvedValue(history({ items: [...history().items!].reverse() }))
    const w = await mountView()
    await load(w)
    await openHistory(w)
    expect(hasId(w, 'ps-history-order-bad')).toBe(true)
  })

  it('⑥-3 ★★★★ 超过 50 条 ⇒ 报 limit-bad（后端写死的上限）', async () => {
    const many = Array.from({ length: 51 }, (_, i) => ({
      setting_key: 'k',
      action: 'update',
      operator_user: `u${i}`,
      operator_role: 'super_admin',
      created_at: `2026-10-14T${String(10 - Math.floor(i / 60)).padStart(2, '0')}:${String(i % 60).padStart(2, '0')}:00Z`,
    }))
    mHist.mockResolvedValue(history({ items: many }))
    const w = await mountView()
    await load(w)
    await openHistory(w)
    expect(hasId(w, 'ps-history-limit-bad')).toBe(true)
  })

  it('⑥-4 ★★★ history 请求失败 ⇒ 报历史错，且**不**把清单也清空', async () => {
    mHist.mockRejectedValue(new Error('query failed'))
    const w = await mountView()
    await load(w)
    await openHistory(w)
    expect(hasId(w, 'ps-history-error')).toBe(true)
    expect(hasId(w, 'ps-error')).toBe(false)
    expect(hasId(w, 'ps-count')).toBe(true)
  })

  it('⑥-5 ★★★ 列表请求失败 ⇒ 报错，不显示「没有设置项」', async () => {
    mList.mockRejectedValue(new Error('settings registry not initialised'))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'ps-error')).toBe(true)
    expect(hasId(w, 'ps-count')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑦ 接线与编码门
// ══════════════════════════════════════════════════════════════════════════

describe('⑦ 接线与编码门', () => {
  it('⑦-1 ★★★★ 路由存在', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toMatch(/path: '\/platform-settings'/)
    expect(src).toMatch(/name: 'platform-settings'/)
  })

  it('⑦-2 ★★★★ i18n 两个语种都有 ps.* 全部键，且值不等于键名', () => {
    const keys = [
      'nav.platformSettings',
      'ps.title',
      'ps.nope.msg',
      'ps.nope.why',
      'ps.load.title',
      'ps.load.btn',
      'ps.scope.note',
      'ps.scope.warn',
      'ps.check.scopeBad',
      'ps.check.sourceBad',
      'ps.check.maybeIncomplete',
      'ps.check.gated',
      'ps.check.historyOrderBad',
      'ps.check.historyLimitBad',
      'ps.check.historyCrossTenant',
      'ps.check.operators',
      'ps.list.title',
      'ps.list.historyBtn',
      'ps.history.title',
      'ps.history.empty',
      'ps.field.scope',
      'ps.field.source',
      'ps.field.operator',
      'ps.field.role',
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

  it('⑦-3 ★★★ 新触控控件 ≥48 CSS px（断言要解析数值再比大小）', () => {
    const src = viewSrc()
    const m = src.match(/\.ps__btn\s*\{[^}]*min-height:\s*(\d+(?:\.\d+)?)px/)
    expect(m, '没找到 .ps__btn 的 min-height').toBeTruthy()
    expect(Number(m![1])).toBeGreaterThanOrEqual(48)
  })

  it('⑦-4 ★★★★ 孤儿棘轮：本族已移出「功能缺口」清单，且未被登记成重复件', () => {
    const src = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    const listOf = (name: string): string[] => {
      const m = src.match(new RegExp(`const ${name}: readonly string\\[\\] = \\[([\\s\\S]*?)\\]`))
      expect(m, `没找到 ${name}（正则失配会让这条恒绿）`).toBeTruthy()
      return [...m![1]!.matchAll(/'([A-Za-z0-9_]+)'/g)].map((x) => x[1]!)
    }
    expect(listOf('KNOWN_ORPHANS')).not.toContain('platformSettings')
    // ★ 它是**功能缺口**不是重复件：三个端点与任何已可达模块都不重合。
    expect(listOf('KNOWN_DUPLICATES')).not.toContain('platformSettings')
  })

  it('⑦-5 ★★★ 编码门：视图与本文件不得含 U+FFFD / NUL', () => {
    const F = String.fromCharCode(0xFFFD)
    const N = String.fromCharCode(0)
    for (const f of [
      viewSrc(),
      readFileSync(join(process.cwd(), 'src/views/PlatformSettingsView.spec.ts'), 'utf8'),
    ]) {
      expect(f.includes(F), '含 U+FFFD').toBe(false)
      expect(f.includes(N), '含 NUL').toBe(false)
    }
  })

  it('⑦-6 ★★★ ★ 批 120 的 sts 族**没被本批的编辑挤掉**（前车之鉴）', () => {
    // ★★★ 本批接线时用整块替换改 i18n，一度把 sts.* 整块删掉（靠 i18n:check 才发现）。
    //   ⇒ 这条判据是给**下一个接线批次**立的：改 i18n 前先钉住上一批的前缀还在。
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const m = DICT[loc]
      expect(typeof dictLookup(m, 'sts.title'), `${loc} 的 sts 块被弄丢了`).toBe('string')
      expect(typeof dictLookup(m, 'ps.title'), `${loc} 的 ps 块`).toBe('string')
      expect(typeof dictLookup(m, 'nav.subscriptionTiers'), `${loc} 的 nav.subscriptionTiers`).toBe('string')
    }
  })
})

/** ★ 字典是**嵌套对象**，不能按 `Record<string,string>` 断言，必须按路径走。 */
const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}
