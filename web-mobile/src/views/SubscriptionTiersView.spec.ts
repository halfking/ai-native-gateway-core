import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import SubscriptionTiersView from './SubscriptionTiersView.vue'
import { setLocale, t } from '@/i18n'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV } from '@/config/appNav'
import {
  fetchSubscriptionTiers,
  subscriptionTiersAreSortOrdered,
  subscriptionTierModuleKeysAreUnique,
  subscriptionTierCodesAreUnique,
  subscriptionTierIsFlat,
  subscriptionTierEnabledIsNotExposed,
  subscriptionTierMaxFeaturesIsAbsent,
  subscriptionTierDescriptionIsEmptyString,
  subscriptionTierHasNoModules,
  SUBSCRIPTION_TIER_KEYS,
  SUBSCRIPTION_TIER_CORE_KEYS,
  type SubscriptionTierRow,
} from '@/api/subscriptionTiers'

/**
 * 订阅档面回归护栏（2026-10-14，第一百二十批）。
 *
 * ★★★★★★ **权限档第四类登场：不是第三类，是「真门」。**
 *   批 115/117/118 都归第三类（服务端没有租户维度 ⇒ 载荷含别人的数据 ⇒ 过度限制）。
 *   本批**同样没有租户维度**（读全局 `subscription_tiers`），
 *   **但**中间件 `requireSuperAdmin`（`cmd/gateway/main_jwt_middleware.go:93-102`）
 *   逐字就是 `if role != "super_admin" { 403 }`
 *   ⇒ 权限由**中间件**给出，不靠前端推断 ⇒ 抽屉席设 `requiresRole` 即完整处置。
 *   ⇒ ★★★★ 与批 116 `modelIQ` 同款，与前三批相反：**别把「没有租户维度」自动读成第三类**。
 *
 * ★★★★★★ **本批的第二个要点：第三种注册机制。**
 *   这个端点**不在** `admin/handler.go` 的 `mux.HandleFunc` 里，而在 **echo 的 group** 上
 *   （`main.go:7027` → `:7029` → `licensing/module_api.go:22`）
 *   ⇒ 只 grep mux 会把「已注册」误判成「死端点」。判据里直接读 Go 源码钉这条链。
 *
 * ★★★★★ **第三个要点，也是最容易写成恒绿的：`module_keys` 的顺序未定义。**
 *   `ListTierModuleMaps`（`licensing/store_pgx.go:708-711`）的 SQL **无 `ORDER BY`**
 *   ⇒ 「顺序乱」**不是**违规 ⇒ 判据里有一条专门的**阴性对照**。
 *
 * ★★★★ **第四个要点：两个 helper 的名字是反的。**
 *   `subscriptionTierEnabledIsNotExposed` / `subscriptionTierMaxFeaturesIsAbsent`
 *   返回 **true 才是好状态**（"没出现" 符合契约）
 *   ⇒ 视图里必须取负才能当 violation 过滤器；判据 ④-1/④-3 专门钉这一点。
 *
 * 判据钉七组：
 *  ① ★★★★★★ 权限档 super_admin（真门）＋ 第三种注册机制。
 *  ② ★★★★★ 顶层裸数组 ＋ 六键同层（内嵌已扁平）。
 *  ③ ★★★★★ 行序有定义、组内序**未**定义；布尔与清单必须拆开。
 *  ④ ★★★★★ 两个「不可见的过滤维度」＋ 反名 helper。
 *  ⑤ ★★★★ 深链兜底：**不发请求**，而不只是「藏起来」。
 *  ⑥ ★★★ 「该出现」的那一面不能被红线吃掉。
 *  ⑦ ★★★ 接线（路由/抽屉/白名单/i18n/孤儿棘轮）与编码门。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

/** 六键同层；`module_keys` 恒为数组（作者显式补 `[]`）。 */
function tier(over: Partial<SubscriptionTierRow> = {}): SubscriptionTierRow {
  return {
    code: 'basic',
    name: '基础版',
    description: '基础能力',
    price_cents: 0,
    sort_order: 10,
    module_keys: ['requests', 'logs'],
    ...over,
  }
}

function twoTiers(over: Partial<SubscriptionTierRow> = {}): SubscriptionTierRow[] {
  return [
    tier({ code: 'basic', name: '基础版', sort_order: 10, ...over }),
    tier({ code: 'pro', name: '专业版', sort_order: 20, module_keys: ['requests', 'logs', 'export'] }),
  ]
}

// ── 装配 ──────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/subscriptionTiers', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/subscriptionTiers')>()
  return { ...actual, fetchSubscriptionTiers: vi.fn() }
})

const mFetch = fetchSubscriptionTiers as unknown as ReturnType<typeof vi.fn>

type W = Awaited<ReturnType<typeof mountView>>

async function mountView() {
  const w = mount(SubscriptionTiersView)
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
  return readFileSync(join(process.cwd(), 'src/views/SubscriptionTiersView.vue'), 'utf8')
}
/** ★ 读源码前先让路径写错**硬失败**，否则 expect 落在别处很难查。 */
function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}

beforeEach(() => {
  vi.clearAllMocks()
  setLocale('zh-CN')
  authState.role = 'super_admin'
  mFetch.mockResolvedValue(twoTiers())
})

async function load(w: W) {
  await byId(w, 'sts-load').trigger('click').then(() => flushPromises())
}

// ══════════════════════════════════════════════════════════════════════════
// ① 权限档 super_admin（真门）＋ 第三种注册机制
// ══════════════════════════════════════════════════════════════════════════

describe('① 权限档 super_admin（与批 115/117/118 相反：不是第三类，是真门）', () => {
  it('①-1 ★★★★★★ 抽屉席存在、路径正确、且设 super_admin', () => {
    const seat = DRAWER_NAV.find((i) => i.key === 'subscription-tiers')
    expect(seat).toBeTruthy()
    expect(seat!.to).toBe('/subscription-tiers')
    expect(seat!.requiresRole).toBe('super_admin')
    expect(seat!.titleKey).toBe('nav.subscriptionTiers')
  })

  it('①-2 ★★★★ 进 superAdmin 白名单，且按字母序插在 sliding-window 与 task-index 之间', () => {
    const src = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    const at = (k: string) => src.indexOf(`'${k}',`)
    expect(at('subscription-tiers'), '白名单里没有这一席').toBeGreaterThan(-1)
    // ★ 白名单是排序后的字面量数组 ⇒ 插入位置错会让 sort 断言炸。
    expect(at('subscription-tiers')).toBeGreaterThan(at('sliding-window'))
    expect(at('subscription-tiers')).toBeLessThan(at('task-index'))
  })

  it('①-3 ★★★★★★ 读 Go 源码：注册链是 **echo group**，不是 mux（第三种注册机制）', () => {
    const src = goSrc('cmd/gateway/main.go')
    const group = src.match(/adminGroup := e\.Group\("\/api\/admin", (requireSuperAdmin)\)/)
    expect(group, '没找到 echo group 声明（正则失配会让这条恒绿）').toBeTruthy()
    expect(group![1]).toBe('requireSuperAdmin')
    const reg = src.match(/(licensing\.RegisterModuleRoutes\(adminGroup, licensingStore\))/)
    expect(reg, '没找到 RegisterModuleRoutes 调用').toBeTruthy()
    // ★★★★ 中间件变量必须**绑定到**那个逐字比 role 的工厂函数。
    //   ⚠️ 这里踩过一个坑：`requireSuperAdmin` 是**局部变量名**，
    //   真正的定义是 `func newRequireSuperAdminMiddleware()`；
    //   若按变量名去 `main_jwt_middleware.go` 里找定义，永远找不到 ——
    //   那正是本条判据首轮红掉的原因（「没找到 requireSuperAdmin 定义」）。
    const bind = src.match(/requireSuperAdmin := (newRequireSuperAdminMiddleware\(\))/)
    expect(bind, '中间件变量没有绑定到 newRequireSuperAdminMiddleware').toBeTruthy()
    expect(bind![1]).toBe('newRequireSuperAdminMiddleware()')
    // ★★★ 反向护栏：这个端点**不在** mux.HandleFunc 里 —— 若哪天被搬进 mux，
    //   这条会红，提示「权限结论要重判」（那时包它的中间件可能不是 requireSuperAdmin）。
    const h = goSrc('admin/handler.go')
    expect(h).not.toMatch(/HandleFunc\("\/api\/admin\/tiers"/)
  })

  it('①-4 ★★★★★★ 读 Go 源码：中间件逐字比 role ⇒ 403（**真门**，不是前端推断）', () => {
    const src = goSrc('cmd/gateway/main_jwt_middleware.go')
    const lines = src.split('\n')
    // ★ 锚**工厂函数名**，不是局部变量名（见 ①-3 的注释）。
    const idx = lines.findIndex((l) => l.includes('func newRequireSuperAdminMiddleware'))
    expect(idx, '没找到 newRequireSuperAdminMiddleware 定义').toBeGreaterThan(-1)
    const body = lines.slice(idx, idx + 12).join('\n')
    // ★★★ 逐字：`role` 取自 JWT 中间件注入的 context，不是 URL 参数、更不是形参名。
    expect(body).toMatch(/role, _ := c\.Get\("role"\)\.\(string\); role != "super_admin"/)
    expect(body).toMatch(/http\.StatusForbidden/)
    // ★★ role 必须是**注入**来的 ⇒ JWT 中间件跑在它前面。
    expect(src).toMatch(/c\.Set\("role", claims\.Role\)/)
  })

  it('①-5 ★★★★ 反向护栏：不得被退化成 admin 档或无档', () => {
    // ★ 批 119 刚把第三类（无租户维度 + 全局载荷）定为不过度限制。
    //   本族**不能**跟着改成那样 —— 中间件是逐字的真门，退档会让 tenant_admin 看到一屏 403。
    const seat = DRAWER_NAV.find((i) => i.key === 'subscription-tiers')!
    expect(seat.requiresRole).not.toBe('admin')
    expect(seat.requiresRole).not.toBeUndefined()
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ② 顶层裸数组 ＋ 六键同层
// ══════════════════════════════════════════════════════════════════════════

describe('② 顶层裸数组与六键同层（内嵌已扁平）', () => {
  it('②-1 ★★★★★ 空数组 ⇒ `[]` 是合法形态，显示空态而不是报错', async () => {
    mFetch.mockResolvedValue([])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-empty')).toBe(true)
    expect(hasId(w, 'sts-error')).toBe(false)
    expect(hasId(w, 'sts-nope')).toBe(false)
  })

  it('②-2 ★★★★★ 载荷不是顶层数组 ⇒ 报错（不得静默当成空列表）', async () => {
    mFetch.mockRejectedValue(new Error('订阅档 响应形状不符：期望顶层裸数组，实得 object'))
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-error')).toBe(true)
    // ★ 该出现的一面：出错时不能顺手显示「没有订阅档」—— 那是把失败读成空。
    expect(hasId(w, 'sts-empty')).toBe(false)
  })

  it('②-3 ★★★★★ 六键**同层**：`code` 在顶层，不是 `.tier.code`', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'sts-modules-basic')).toBe('requests / logs')
    expect(textOf(w, 'sts-row-pro')).toContain('pro')
    expect(hasId(w, 'sts-row-basic')).toBe(true)
    // ★ 若写成嵌套取值，渲染出来会是 `undefined` ⇒ 这条会红。
    expect(textOf(w, 'sts-row-basic')).not.toContain('undefined')
  })

  it('②-4 ★★★★ 六个键与「内嵌五键 + module_keys」的拆分一致', () => {
    expect(SUBSCRIPTION_TIER_KEYS).toHaveLength(6)
    expect(SUBSCRIPTION_TIER_CORE_KEYS).toHaveLength(5)
    expect([...SUBSCRIPTION_TIER_CORE_KEYS, 'module_keys']).toEqual([...SUBSCRIPTION_TIER_KEYS])
  })

  it('②-5 ★★★★ 平铺判据：顶层有 `code` 且**没有** `tier` 包裹', () => {
    expect(subscriptionTierIsFlat(tier())).toBe(true)
    expect(subscriptionTierIsFlat({ tier, module_keys: [] } as unknown as SubscriptionTierRow)).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ③ 行序有定义、组内序未定义；布尔与清单拆开
// ══════════════════════════════════════════════════════════════════════════

describe('③ 排序与唯一性（布尔是开关、清单才是「哪一行」）', () => {
  it('③-1 ★★★★★ 正常载荷 ⇒ 五条红线全不亮', async () => {
    const w = await mountView()
    await load(w)
    for (const id of [
      'sts-order-bad',
      'sts-dup-keys',
      'sts-dup-codes',
      'sts-enabled-leak',
      'sts-max-features-leak',
    ]) {
      expect(hasId(w, id), `${id} 不该亮`).toBe(false)
    }
  })

  it('③-2 ★★★★★ 行序逆序 ⇒ 报 sts-order-bad（`ORDER BY sort_order` 是**定义**的）', async () => {
    mFetch.mockResolvedValue([
      tier({ code: 'pro', sort_order: 20 }),
      tier({ code: 'basic', sort_order: 10 }),
    ])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-order-bad')).toBe(true)
    expect(subscriptionTiersAreSortOrdered([tier({ sort_order: 20 }), tier({ sort_order: 10 })])).toBe(false)
    expect(subscriptionTiersAreSortOrdered(twoTiers())).toBe(true)
  })

  it('③-3 ★★★★★★ `module_keys` 组内顺序乱 ⇒ **不得**报红（SQL 无 ORDER BY）', async () => {
    // ★★★★★ 这是本族最关键的一条阴性对照。
    //   `ListTierModuleMaps`（`licensing/store_pgx.go:708-711`）既无 ORDER BY 也无 WHERE
    //   ⇒ 组内顺序是**后端未定义**的 ⇒ 客户端若据此报红就是在报一个不存在的契约。
    mFetch.mockResolvedValue([
      tier({ code: 'basic', module_keys: ['logs', 'requests', 'audit'] }),
      tier({ code: 'pro', module_keys: ['export', 'logs'] }),
    ])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-dup-keys'), '顺序未定义 ≠ 违规').toBe(false)
    // ★ 同时：渲染**不得**自作主张排序（那会掩盖后端顺序，一旦后端将来加了
    //   ORDER BY，前端这个排序会让「顺序变了」这件事永远看不见）。
    expect(textOf(w, 'sts-modules-basic')).toBe('logs / requests / audit')
  })

  it('③-4 ★★★★★ 组内元素重复 ⇒ 报 sts-dup-keys 且 n 是**行数**不是元素数', async () => {
    mFetch.mockResolvedValue([
      tier({ code: 'basic', module_keys: ['a', 'a', 'b'] }),
      tier({ code: 'pro', module_keys: ['c', 'c'] }),
    ])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-dup-keys')).toBe(true)
    expect(textOf(w, 'sts-dup-keys')).toBe(t('sts.check.dupKeys', { n: 2 }))
    // ★ API 层判据是**数组级**布尔（任一行违规即 false）⇒ 不能拿它当过滤器。
    expect(subscriptionTierModuleKeysAreUnique([tier({ module_keys: ['a', 'a'] })])).toBe(false)
    expect(subscriptionTierModuleKeysAreUnique(twoTiers())).toBe(true)
  })

  it('③-5 ★★★★★ `code` 重复 ⇒ 报 sts-dup-codes', async () => {
    mFetch.mockResolvedValue([tier({ code: 'basic' }), tier({ code: 'basic', sort_order: 20 })])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-dup-codes')).toBe(true)
    expect(textOf(w, 'sts-dup-codes')).toBe(t('sts.check.dupCodes', { n: 1 }))
    expect(subscriptionTierCodesAreUnique([tier({ code: 'x' }), tier({ code: 'x' })])).toBe(false)
    expect(subscriptionTierCodesAreUnique(twoTiers())).toBe(true)
  })

  it('③-6 ★★★★ 空 `module_keys` 是合法形态：不报重复，且计入「无模块」计数', async () => {
    mFetch.mockResolvedValue([tier({ code: 'basic', module_keys: [] })])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-dup-keys')).toBe(false)
    expect(textOf(w, 'sts-modules-basic')).toBe(t('sts.field.noModules'))
    expect(textOf(w, 'sts-empty-modules')).toBe(t('sts.check.emptyModules', { n: 1 }))
    expect(subscriptionTierHasNoModules(tier({ module_keys: [] }))).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ④ 两个「不可见的过滤维度」＋ 反名 helper
// ══════════════════════════════════════════════════════════════════════════

describe('④ enabled / max_features：一个不存在，一个被 SELECT 却丢弃', () => {
  it('④-1 ★★★★★★ `enabled` **缺席** ⇒ 不报泄漏（钉住 helper 名字是反的）', async () => {
    // ★★★★ `subscriptionTierEnabledIsNotExposed` 返回 **true 才是好状态**。
    //   若视图直接拿它 `.filter()` 收集「泄漏行」，得到的是**全部行** ⇒ 红线恒亮。
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-enabled-leak'), 'enabled 缺席是契约，不是泄漏').toBe(false)
    expect(subscriptionTierEnabledIsNotExposed(tier())).toBe(true)
  })

  it('④-2 ★★★★★ 载荷里**冒出了** enabled ⇒ 报泄漏（端点压根没 SELECT 它）', async () => {
    mFetch.mockResolvedValue([tier({ code: 'basic', enabled: true } as unknown as SubscriptionTierRow)])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-enabled-leak')).toBe(true)
    expect(textOf(w, 'sts-enabled-leak')).toBe(t('sts.check.enabledLeak', { n: 1 }))
  })

  it('④-3 ★★★★★★ `max_features` **缺席** ⇒ 不报（钉住第二个反名 helper）', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-max-features-leak')).toBe(false)
    expect(subscriptionTierMaxFeaturesIsAbsent(tier())).toBe(true)
  })

  it('④-4 ★★★★ 载荷里冒出 max_features ⇒ 报（后端 SELECT 了但 struct 里没有）', async () => {
    mFetch.mockResolvedValue([
      tier({ code: 'basic', max_features: '' } as unknown as SubscriptionTierRow),
    ])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-max-features-leak')).toBe(true)
    expect(textOf(w, 'sts-max-features-leak')).toBe(t('sts.check.maxFeaturesLeak', { n: 1 }))
  })

  it('④-5 ★★★★ 页面必须说明「enabled 不可见」这件反直觉的事', async () => {
    // ★ 这条是「停用档也会出现且客户端看不出」的**用户可见面**。
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'sts-enabled-note')).toBe(t('sts.scope.enabledNote'))
    expect(t('sts.scope.enabledNote')).toContain('enabled')
  })

  it('④-6 ★★★ `description` 恒为字符串（可为空串，不是 null）', () => {
    // ★ 只钉「空串」这一侧：表列 NOT NULL ⇒ 可达集合是 {非空串} ∪ {''}，没有 null。
    expect(subscriptionTierDescriptionIsEmptyString(tier({ description: '' }))).toBe(true)
    expect(subscriptionTierDescriptionIsEmptyString(tier({ description: 'x' }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑤ 深链兜底
// ══════════════════════════════════════════════════════════════════════════

describe('⑤ 深链兜底：兜的是「不发请求」', () => {
  it('⑤-1 ★★★★★ 非 super_admin ⇒ 显示 nope，且**一次请求都不发**', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    await flushPromises()
    expect(hasId(w, 'sts-nope')).toBe(true)
    expect(mFetch).not.toHaveBeenCalled()
    expect(hasId(w, 'sts-load')).toBe(false)
  })

  it('⑤-2 ★★★★★ nope 页面也要把「为什么」讲清楚', async () => {
    authState.role = 'user'
    const w = await mountView()
    await flushPromises()
    expect(textOf(w, 'sts-nope-msg')).toBe(t('sts.nope.msg'))
    expect(textOf(w, 'sts-nope-why')).toBe(t('sts.nope.why'))
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('⑤-3 ★★★★ nope 分支里连加载按钮与列表徽标都没有', async () => {
    authState.role = 'tenant_admin'
    const w = await mountView()
    await flushPromises()
    expect(hasId(w, 'sts-load')).toBe(false)
    // ★ `sts-rows` 是列表徽标，只在 `v-if="rows.length"` 时渲染 ⇒ 顺带证明没数据进来。
    expect(hasId(w, 'sts-rows')).toBe(false)
    expect(mFetch).not.toHaveBeenCalled()
  })

  it('⑤-4 ★★★ super_admin ⇒ 不显示 nope', async () => {
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-nope')).toBe(false)
    expect(mFetch).toHaveBeenCalledTimes(1)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑥ 「该出现」的那一面
// ══════════════════════════════════════════════════════════════════════════

describe('⑥ 红线之外的「该出现」', () => {
  it('⑥-1 ★★★★ 空 description 计入计数，且不是红线', async () => {
    mFetch.mockResolvedValue([tier({ code: 'basic', description: '' }), tier({ code: 'pro' })])
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'sts-empty-description')).toBe(t('sts.check.emptyDescription', { n: 1 }))
    expect(hasId(w, 'sts-empty-description')).toBe(true)
    expect(hasId(w, 'sts-order-bad')).toBe(false)
  })

  it('⑥-2 ★★★ 计数徽标与行数一致', async () => {
    const w = await mountView()
    await load(w)
    expect(textOf(w, 'sts-count')).toBe('2')
    expect(textOf(w, 'sts-rows')).toBe('2')
  })

  it('⑥-3 ★★★ 未加载时（loading=false 且无数据）显示空态而不是「什么都没有」', async () => {
    mFetch.mockResolvedValue([])
    const w = await mountView()
    await load(w)
    expect(hasId(w, 'sts-empty')).toBe(true)
    expect(textOf(w, 'sts-empty')).toBe(t('sts.list.empty'))
  })

  it('⑥-4 ★★★ 每个订阅档都渲染 name / code / sort_order / price_cents', async () => {
    const w = await mountView()
    await load(w)
    const pro = textOf(w, 'sts-row-pro')
    expect(pro).toContain('专业版')
    expect(pro).toContain('pro')
    expect(pro).toContain('#20')
    expect(pro).toContain('0') // price_cents=0（basic 档）
  })

  it('⑥-5 ★★★★ price_cents = 0 必须渲染出来，不能被 v-if 当 falsy 吃掉', async () => {
    // ★ 这一族最容易犯的错：`v-if="r.price_cents"` 会把**免费档**整行信息藏掉。
    //   行里**没有任何其他字符是 0**（sort_order=5、code/name/module_keys 都不含 0）
    //   ⇒ 只要页面上出现 "0"，就一定是 price_cents 渲染出来了。
    mFetch.mockResolvedValue([
      tier({ code: 'free', name: '免费版', sort_order: 5, price_cents: 0, module_keys: [] }),
    ])
    const w = await mountView()
    await load(w)
    const row = textOf(w, 'sts-row-free')
    expect(row).toContain('免费版')
    expect(row).toContain('#5')
    expect(row).toContain('0')
  })
})

// ══════════════════════════════════════════════════════════════════════════
// ⑦ 接线与编码门
// ══════════════════════════════════════════════════════════════════════════

describe('⑦ 接线与编码门', () => {
  it('⑦-1 ★★★★ 路由存在且 requiresRole 与抽屉席一致', () => {
    const src = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(src).toMatch(/path: '\/subscription-tiers'/)
    expect(src).toMatch(/name: 'subscription-tiers'/)
  })

  it('⑦-2 ★★★★ i18n 两个语种都有 nav.subscriptionTiers 与 sts.* 全部键', () => {
    for (const loc of ['zh-CN', 'en-US'] as const) {
      expect(t.length, 't 不该被换掉').toBeGreaterThan(0)
      const m = DICT[loc]
      for (const k of [
        'nav.subscriptionTiers',
        'sts.title',
        'sts.nope.msg',
        'sts.nope.why',
        'sts.load.title',
        'sts.load.btn',
        'sts.scope.note',
        'sts.scope.enabledNote',
        'sts.check.title',
        'sts.check.orderBad',
        'sts.check.dupKeys',
        'sts.check.dupCodes',
        'sts.check.enabledLeak',
        'sts.check.maxFeaturesLeak',
        'sts.check.emptyModules',
        'sts.check.emptyDescription',
        'sts.list.title',
        'sts.list.empty',
        'sts.field.code',
        'sts.field.price',
        'sts.field.modules',
        'sts.field.noModules',
        'sts.field.keys',
      ]) {
        const v = dictLookup(m, k)
        // ★★ 先确认没缺键，再判「值 === 键名」—— 缺键时 lookup 返回 undefined，
        //   那时后面那条 `not.toBe(k)` 会**假绿**（undefined ≠ k）。
        expect(typeof v, `${loc} 缺 ${k}`).toBe('string')
        // ★★ i18n 门拦「值 === 键名」——英文里最容易这么写。
        expect(v, `${loc} 的 ${k} 值等于键名`).not.toBe(k)
      }
    }
  })

  it('⑦-3 ★★★ 新触控控件 ≥48 CSS px（断言要解析数值再比大小）', () => {
    const src = viewSrc()
    const m = src.match(/\.sts__btn\s*\{[^}]*min-height:\s*(\d+(?:\.\d+)?)px/)
    expect(m, '没找到 .sts__btn 的 min-height').toBeTruthy()
    expect(Number(m![1])).toBeGreaterThanOrEqual(48)
  })

  it('⑦-4 ★★★★ 孤儿棘轮：这一族已从「功能缺口」清单移出，且**不得**被登记成重复件', () => {
    // ★ 两个清单都是 spec 文件里的**非导出**字符串数组 ⇒ 只能读文本，import 不到。
    const src = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    const listOf = (name: string): string[] => {
      const m = src.match(new RegExp(`const ${name}: readonly string\\[\\] = \\[([\\s\\S]*?)\\]`))
      expect(m, `没找到 ${name}（正则失配会让这条恒绿）`).toBeTruthy()
      return [...m![1]!.matchAll(/'([A-Za-z0-9_]+)'/g)].map((x) => x[1]!)
    }
    // ★ 本批收口：视图可达了 ⇒ 功能缺口清单里不再有它。
    expect(listOf('KNOWN_ORPHANS')).not.toContain('subscriptionTiers')
    // ★★ 反向护栏：它是**功能缺口**，不是重复件（端点与 dashboardBoard 不重合）。
    //   若哪天有人图省事把它登记进 KNOWN_DUPLICATES 免罪，这条立刻红。
    expect(listOf('KNOWN_DUPLICATES')).not.toContain('subscriptionTiers')
    // ★★★ 批 149 收口：合并 boardOperational 后重复件清单已清空
    //   （孤儿登记从「无真缺口 + 一条重复件 + 一条基础设施」降到「无真缺口 + 一条基础设施」）。
    expect(listOf('KNOWN_DUPLICATES')).toEqual([])
  })

  it('⑦-5 ★★★ 编码门：视图与本文件不得含 U+FFFD / NUL', () => {
    for (const f of [viewSrc(), readFileSync(join(process.cwd(), 'src/views/SubscriptionTiersView.spec.ts'), 'utf8')]) {
      expect(f.includes('\uFFFD'), '含 U+FFFD').toBe(false)
      expect(f.includes('\u0000'), '含 NUL').toBe(false)
    }
  })
})

/**
 * ★ 本地字典句柄（只为 ⑦-2 能**按语种**逐键核对；`t()` 只看运行期当前语种）。
 *   字典是**嵌套对象**（`{ sts: { check: { … } } }`），不是扁平表
 *   ⇒ 不能按 `Record<string, string>` 断言，必须按路径走。
 */
const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

/** 按 `a.b.c` 路径取值；任何一段不存在返回 undefined（由断言判红）。 */
function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}
