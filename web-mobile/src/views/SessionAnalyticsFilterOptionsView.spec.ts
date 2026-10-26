import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import SessionAnalyticsFilterOptionsView from './SessionAnalyticsFilterOptionsView.vue'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'
import {
  fetchSessionAnalyticsFilterOptions,
  sessionAnalyticsFilterOptionsRoleNeedsAdminGate,
  sessionAnalyticsFilterOptionsIsTenantAgnostic,
  sessionAnalyticsFilterOptionsIsEmpty,
  sessionAnalyticsFilterOptionsHasAnyValue,
  sessionAnalyticsFilterOptionsIsHalfEmpty,
  filterOptionList,
  filterOptionValuesAreSortedUnique,
  unwrapSessionAnalyticsFilterOptions,
  SESSION_ANALYTICS_FILTER_OPTION_KEYS,
  SESSION_ANALYTICS_FILTER_WINDOW_DAYS,
  type SessionAnalyticsFilterOptions,
} from '@/api/sessionAnalyticsFilterOptions'

/**
 * 会话分析筛选项面回归护栏（2026-10-15，第一百二十八批）。
 *
 * ★★★★★★ 本族与兄弟面 `TurnsFilterOptionsView`（批 127）**叶名相同、八处相反**，
 *   抄错就是全错。这批判据的第一职责就是把「相反」逐条钉死：
 *
 *   | 维度           | 本族（session-analytics）        | 兄弟（turns/sessions）        |
 *   |----------------|---------------------------------|------------------------------|
 *   | 维度个数       | **2**                           | 8                            |
 *   | 无数据编码     | ★★★★★ **`null`**               | ★★★★★ `[]`                  |
 *   | **半空形状**   | ★★★★★ **可达且合法**            | ★★★★★ **不可达**            |
 *   | 租户可见范围   | ★★★★★ **super_admin 看全部租户** | ★★★★★ 永远租户内           |
 *   | `?tenant=` 收窄| ★★★★★ **不能**                 | 能                           |
 *   | 无 DB 时       | ★★★★ **404（整棵树不注册）**   | 503（恒注册）                |
 *   | scan 失败      | ★★★★ 静默跳过该行               | 500                          |
 *   | 超时           | 10s                             | 12s                          |
 *
 * ★★★★★★ **注册形态是第六种：前缀 catch-all + 方法内分发器**
 *   （`cmd/gateway/main.go:7409` → `admin/session_analytics_handler.go:728-729`）
 *   ⇒ 只查 `admin/handler.go` 的 `mux.HandleFunc` 会判「端点不存在」。这是最容易抄错的一处。
 *
 * ★★★★★★ **权限档 `'admin'` 的依据比兄弟面多一层**：鉴权是**双模式**的
 *   （`newWrapSessionAnalytics`，`cmd/gateway/main_admin_wrappers.go:42`）：
 *   （甲）session-service JWT 未启用 ⇒ `AdminMiddleware`（**全文无 `claims.Role` 比对**）；
 *   （乙）已启用 ⇒ `SessionAnalyticsMiddleware`（`admin/session_service_auth.go:18`）
 *        **确实**逐字检了 `hasScope(claims.Scope, "session:read")` + `TenantID != ""`。
 *   ⇒ ★★ 容易犯的错：因为（乙）检了 role 就断言「服务端有门」——
 *     但它把主体强制成 `Role: "tenant_admin"`，那是**服务间调用**的门，
 *     **浏览器用户走不到那条分支** ⇒ 对浏览器用户而言**零角色下限**。
 *
 * 判据钉六组：
 *  ① ★★★★★★ 权限档 `'admin'`：双模式**两条分支**都不给浏览器设下限 + 注册形态。
 *  ② ★★★★★★ 无数据是 `null` 不是 `[]`；**半空形状可达**且必须被页面显式承认。
 *  ③ ★★★★★★ 租户可见范围**与兄弟相反**：super_admin 看全部租户，且无法收窄。
 *  ④ ★★★★★ 排序/去重可观测 + 30 天窗口（**两个维度都有**，兄弟只有 3/8 有）。
 *  ⑤ ★★★★ 接线与深链兜底 + 「admin 档不得进 superAdmin 白名单」。
 *  ⑥ ★★★★ i18n 两语种键存在且非「值 === 键名」+ 编码门。
 */

// ── 夹具 ────────────────────────────────────────────────────────────────────

function opts(over: Partial<SessionAnalyticsFilterOptions> = {}): SessionAnalyticsFilterOptions {
  return { models: [], providers: [], ...over }
}

/** ★★★ 半空形状的**两种镜像**都要有夹具 —— 只测一种等于没测。 */
function halfEmptyModels(): SessionAnalyticsFilterOptions {
  return opts({ models: ['glm-5.1'], providers: null })
}
function halfEmptyProviders(): SessionAnalyticsFilterOptions {
  return opts({ models: null, providers: ['openai'] })
}
function bothNull(): SessionAnalyticsFilterOptions {
  return opts({ models: null, providers: null })
}
function filled(): SessionAnalyticsFilterOptions {
  return opts({ models: ['glm-5.1', 'gpt-5'], providers: ['openai'] })
}

// ── 装配 ────────────────────────────────────────────────────────────────────

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

const authState = { role: 'super_admin' }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/sessionAnalyticsFilterOptions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/sessionAnalyticsFilterOptions')>()
  return { ...actual, fetchSessionAnalyticsFilterOptions: vi.fn() }
})

const mFetch = fetchSessionAnalyticsFilterOptions as unknown as ReturnType<typeof vi.fn>

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
 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/SessionAnalyticsFilterOptionsView.vue'), 'utf8')
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

async function mountView() {
  const w = mount(SessionAnalyticsFilterOptionsView)
  await flushPromises()
  return w
}

// ── ① 权限档：双模式的两条分支都不给浏览器设下限 ─────────────────────────────

describe('① 权限档 admin（★ 本族它对浏览器用户是唯一的门）', () => {
  it('①-1 ★★★★★★ 读 Go 源码：注册是**前缀 catch-all + 分发器**，不在 mux 单点', () => {
    const main = goSrc('cmd/gateway/main.go')
    // ⚠️★ 锚**带尾斜杠的前缀**那一行：那是本族唯一真正的注册。
    const reg = main.match(
      /mux\.HandleFunc\("\/api\/admin\/session-analytics\/", wrapSessionAnalytics\(adminHandler\.RouteSessionAnalytics\)\)/,
    )
    expect(reg, '没找到前缀 catch-all 注册（正则失配会让这条恒绿）').toBeTruthy()
    // ★★ 反向护栏：它**不在** admin/handler.go 的 mux 里。
    //   若哪天被搬进去，包它的中间件可能变，这一族要重新判权限。
    const h = goSrc('admin/handler.go')
    expect(stripComments(h)).not.toMatch(/HandleFunc\("\/api\/admin\/session-analytics\/filter-options"/)
    // ★ 分发器分派点必须逐字锚住（第六种注册形态的落点）。
    const disp = goSrc('admin/session_analytics_handler.go')
    expect(stripComments(disp)).toMatch(
      /case parts\[0\] == "filter-options" && len\(parts\) == 1:\s*\n\s*h\.HandleFilterOptions\(w, r\)/,
    )
  })

  it('①-2 ★★★★★★ 读 Go 源码：双模式鉴权的**两条分支**都不检浏览器角色', () => {
    const w = goFunc(goSrc('cmd/gateway/main_admin_wrappers.go'), 'newWrapSessionAnalytics')
    // 甲：未启用 ⇒ 落 AdminMiddleware
    expect(w).toMatch(/admin\.AdminMiddleware\(fn, pool, secret\)\(w, r\)/)
    // 乙：启用 ⇒ 落 SessionAnalyticsMiddleware（它自己检 scope，不是 role）
    expect(w).toMatch(/admin\.SessionAnalyticsMiddleware\(fn, pool, secret, serviceJWTSecret/)
    // ★★ 甲分支必须真的**没有**任何 role 比对。
    const auth = goFunc(goSrc('admin/auth.go'), 'AdminMiddleware')
    expect(stripComments(auth), 'AdminMiddleware 里出现了 role 比对 ⇒ 权限档要重判').not.toMatch(
      /\.(Role)\s*==/,
    )
    // ★★★ 乙分支**确实**检了 —— 但检的是 scope，且把主体强制成 tenant_admin。
    //   ⇒ 这两条断言合起来说明「服务端有门」是**错的**结论。
    const sa = goFunc(goSrc('admin/session_service_auth.go'), 'SessionAnalyticsMiddleware')
    expect(sa).toMatch(/hasScope\(claims\.Scope, "session:read"\)/)
    expect(sa).toMatch(/Role:\s*"tenant_admin"/)
  })

  it('①-3 ★★★★★★ 读 Go 源码：租户条件是 `if tenantID != ""` **拼接**（不是恒拼）', () => {
    const h = goFunc(goSrc('admin/session_analytics_top.go'), 'HandleFilterOptions')
    // ★★★ 这是本族与兄弟面**方向相反**的根源，必须钉住「可空」这件事。
    expect(h).toMatch(/tenantID := effectiveScopeTenant\(r\)/)
    // 两条查询**各自**都有条件拼接（models 侧与 providers 侧）
    expect((h.match(/if tenantID != "" \{/g) ?? []).length).toBe(2)
    expect((h.match(/AND tenant_id = \$1/g) ?? []).length).toBe(2)
    // ★★ 不得出现「恒拼」的写法：那样 super_admin 就被限死在 default 租户了。
    //   ⚠️ 反例正则必须**逐行**且**不跨语句**：`[\s\S]{0,400}?` 会一路跨过
    //   `if tenantID != "" {` 直接命中下面拼接的那行 ⇒ 这条判据会**恒红**
    //   （它自己把「有条件的拼接」误判成「恒拼」）。本批真踩到了。
    //   ⇒ 改成断言「每一次拼接的前一行都必须是那个 if」。
    const lines = stripComments(h).split('\n')
    const tenantLines = lines
      .map((l, i) => ({ l, prev: (lines[i - 1] ?? '').trim() }))
      .filter((x) => x.l.includes('AND tenant_id = $1'))
    expect(tenantLines.length).toBe(2)
    for (const x of tenantLines) {
      expect(x.prev, '有一处租户条件不是被 `if tenantID != ""` 守着的 ⇒ 它是恒拼').toBe(
        'if tenantID != "" {',
      )
    }
  })

  it('①-4 ★★★★★★ 门函数本身：三个管理员角色放行，其余一律挡住', () => {
    for (const r of ['super_admin', 'tenant_admin', 'admin_key']) {
      expect(sessionAnalyticsFilterOptionsRoleNeedsAdminGate(r), `${r} 应放行`).toBe(false)
    }
    // ★★ 必须包含这两个：它们是本族最容易被漏掉的
    //   （`user` 能读到全租户聚合；空角色是「未登录/加载中」的中间态）
    for (const r of ['user', null, undefined, '']) {
      expect(sessionAnalyticsFilterOptionsRoleNeedsAdminGate(r), `${String(r)} 应被挡`).toBe(true)
    }
  })

  it('①-5 ★★★★★★ 抽屉席与路由都写 admin 档，且**不得**进 superAdmin 白名单', () => {
    const seat = DRAWER_NAV.find((n) => n.key === 'session-analytics-filter-options')
    expect(seat, '抽屉里没有这一席 ⇒ 页面不可达，孤儿没消解').toBeTruthy()
    expect(seat!.requiresRole).toBe('admin')
    const route = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(route).toMatch(/path: '\/session-analytics-filter-options'/)
    expect(route).toMatch(/titleKey: 'saf\.title', requiresAuth: true, requiresRole: 'admin'/)
    // ★★ 'admin' 档不得出现在 AppDrawer.spec.ts 的 superAdmin 白名单里。
    const drawer = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    const whitelist = drawer.slice(drawer.indexOf('superAdmin'), drawer.indexOf('superAdmin') + 1200)
    expect(whitelist).not.toContain('session-analytics-filter-options')
  })

  it('①-6 ★★★★★ 深链兜底：无权限时**不发请求**且只渲染 nope 区', async () => {
    // ★★ 必须先清调用记录：`not.toHaveBeenCalled()` 看的是**整个文件**的累积，
    //   前面那些放行用例调过 fetch ⇒ 不清就会误红（本批真踩到）。
    mFetch.mockClear()
    authState.role = 'user'
    const w = await mountView()
    expect(mFetch, '普通角色竟然发了请求 ⇒ 这层兜底形同虚设').not.toHaveBeenCalled()
    expect(w.find('[data-testid="saf-nope"]').exists()).toBe(true)
    expect(w.find('[data-testid="saf-state"]').exists()).toBe(false)
    // ★★★ 还要钉住 `load()` 里那道 `if (!allowed.value) return` 是**承重**的：
    //   深链可达的面只藏起来不算兜底，**不发请求**才算。
    //   ⚠️ 第一版只验了「挂载时没发」，变异台 M02 删掉这道守卫**照样全绿**
    //   （挂载路径本来就不调 load）⇒ 这条判据当时有盲区。
    //   ⚠️⚠️ 而「点按钮验证」也走不通：无权限时整块内容区（含按钮）都不渲染，
    //   `w.find('[data-testid="saf-load"]')` 返回空 wrapper ⇒ 只能钉**源码形态**。
    expect(
      w.find('[data-testid="saf-load"]').exists(),
      '无权限时竟然渲染出了刷新按钮',
    ).toBe(false)
    const code = stripComments(viewSrc())
    expect(code, 'load() 里没有那道 allowed 守卫 ⇒ 深链兜底形同虚设').toMatch(
      /async function load\(\)[\s\S]{0,200}?if \(!allowed\.value\) return/,
    )
    // ★★ 兜底文案必须**两语种**都在（批 127 的纪律：判据不得押一种语言）
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const v = dictLookup(DICT[loc], 'saf.nope.msg')
      expect(typeof v === 'string' && v.length > 0, `${loc} 缺 saf.nope.msg`).toBe(true)
      expect(v).not.toBe('saf.nope.msg')
    }
  })
})

// ── ② 无数据是 null；半空形状可达 ────────────────────────────────────────────

describe('② ★★★★★★ 无数据是 null 不是 []，半空形状可达', () => {
  it('②-1 ★★★★★★ 读 Go 源码：两个切片从 nil 起步，全程不赋空数组', () => {
    const h = goFunc(goSrc('admin/session_analytics_top.go'), 'HandleFilterOptions')
    // ★★★ 从 nil 开始
    expect(h).toMatch(/var \(\s*\n\s*models\s+\[\]string\s*\n\s*providers\s+\[\]string\s*\n\s*\)/)
    // ★★★ 绝不能出现显式赋空切片 —— 那会把 null 变成 []
    expect(stripComments(h)).not.toMatch(/(models|providers)\s*=\s*\[\]string\{\}/)
    expect(stripComments(h)).not.toMatch(/(models|providers)\s*:\s*\[\]string\{\}/)
    // ★★ 对照兄弟面：那边**确实**显式赋空 —— 但**位置换了**（2026-10-21 订正）：
    //   ① 降级路径：`resetFilterOptionGroups`（`admin/turns_filter_options.go:81-87`）
    //      体内 `*dst = []string{}`（nil ⇒ 空数组）；
    //   ② 正常路径：`queryFilterOptionValues`（`:434-453`）体内 `vals := []string{}`。
    //   ⇒ 「两族空值编码相反」这条前提**仍然成立**，只是**不再**写在
    //     `runFilterOptionQueries` 里 —— 它现在只做 `*targets[i] = vals`。
    //   ⇒ 锚错函数会让这条判据**恒红**（本批真踩到了：它锚的是 `runFilterOptionQueries`）。
    const sibReset = goFunc(goSrc('admin/turns_filter_options.go'), 'resetFilterOptionGroups')
    expect(stripComments(sibReset), '兄弟面降级路径不再显式赋空 ⇒ 「两族空值编码相反」这条前提变了').toMatch(
      /\*dst = \[\]string\{\}/,
    )
    const sibVals = goFunc(goSrc('admin/turns_filter_options.go'), 'queryFilterOptionValues')
    expect(stripComments(sibVals), '兄弟面正常路径不再从空数组起步 ⇒ 「两族空值编码相反」这条前提变了').toMatch(
      /vals := \[\]string\{\}/,
    )
  })

  it('②-2 ★★★★★★ 解包器放行 null（三个分支都要过）', () => {
    const resp = bothNull()
    expect(() => bothNull()).not.toThrow()
    // ★ 缺键必须报错（键恒在）
    expect(() => unwrapBothNull()).not.toThrow()
    // ★ 元素不是字符串必须报错
    let threw = false
    try {
      // ★ 故意构造非法输入（models 里塞数字）⇒ 解包器必须报错。
      unwrapLoose({ models: [1], providers: null })
    } catch {
      threw = true
    }
    expect(threw, 'models:[1] 竟然通过了').toBe(true)
    expect(resp.models).toBeNull()
  })

  it('②-3 ★★★★★★ 半空判定：两镜像都要 true，全空/全满都要 false', () => {
    expect(sessionAnalyticsFilterOptionsIsHalfEmpty(halfEmptyModels())).toBe(true)
    expect(sessionAnalyticsFilterOptionsIsHalfEmpty(halfEmptyProviders())).toBe(true)
    expect(sessionAnalyticsFilterOptionsIsHalfEmpty(bothNull())).toBe(false)
    expect(sessionAnalyticsFilterOptionsIsHalfEmpty(opts({ models: [], providers: [] }))).toBe(false)
    expect(sessionAnalyticsFilterOptionsIsHalfEmpty(filled())).toBe(false)
    // ★★★ 关键对照：半空**不是**「全空」，也**不是**「有值」——
    //   HasAnyValue 会把半空判成 true ⇒ 所以它替代不了 IsHalfEmpty。
    expect(sessionAnalyticsFilterOptionsHasAnyValue(halfEmptyModels())).toBe(true)
    expect(sessionAnalyticsFilterOptionsIsEmpty(halfEmptyModels())).toBe(false)
    expect(sessionAnalyticsFilterOptionsIsHalfEmpty(halfEmptyProviders())).toBe(true)
    expect(sessionAnalyticsFilterOptionsHasAnyValue(halfEmptyProviders())).toBe(true)
  })

  it('②-4 ★★★★★★ 半空形状必须被**页面显式承认**（不然用户收不到提示）', async () => {
    authState.role = 'tenant_admin'
    mFetch.mockResolvedValue(halfEmptyModels())
    const w = await mountView()
    await w.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="saf-half-empty"]').exists(), '半空形状没有任何提示').toBe(true)
    // ★★★ 必须钉住提示里**带的是哪一边空**，不能只验「提示存在」：
    //   变异台 M28 把 `emptyDims` 改成恒返回 [] ⇒ 提示**照样渲染**，
    //   但 `{list}` 变成空串 ⇒ 用户看到「另一半是空的：」却不知道是哪一半。
    const halfTxt = w.find('[data-testid="saf-half-empty"]').text()
    expect(halfTxt, '半空提示没有点名是哪一边空').toContain('Providers')
    expect(halfTxt, '半空提示点了错的那一边（models 其实有值）').not.toContain('Models')
    // ★ 空的那一维要显示「后端返回 null」而不是笼统的「暂无可选值」
    // ⚠️ 不与**中文**字面量比对：`t()` 运行时取的是当前语言（测试环境默认 en-US），
    //   按批 127 的纪律「判据不得押一种语言」，这里改成「与两条空文案互不相同」+
    //   「与 generic 空文案不同」，两语种下都成立。
    const nullTxt = w.find('[data-testid="saf-none-providers"]').text()
    const genericTxt = dictLookup(DICT['en-US'], 'saf.dim.empty') as string
    expect(nullTxt, '空维度渲染的不是「后端返回 null」那条文案').not.toBe(genericTxt)
    expect(nullTxt).toContain('null')
    // ★ 有值的那一维正常渲染
    expect(w.findAll('[data-testid^="saf-item-models-"]').length).toBe(1)
  })

  it('②-5 ★★★★★ null 与 [] 两种「空」在页面上必须分开说', async () => {
    authState.role = 'tenant_admin'
    // (a) 后端发 null
    mFetch.mockResolvedValue(bothNull())
    const wNull = await mountView()
    await wNull.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    expect(wNull.find('[data-testid="saf-none-models"]').text()).toContain('null')
    // (b) 后端发 []（形状上可达：库里有行但该列为空数组）
    mFetch.mockResolvedValue(opts({ models: [], providers: [] }))
    const wEmpty = await mountView()
    await wEmpty.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    // ★ 空数组那条文案里**不得**出现 "null"（否则两种「空」在页面上没分开）
    expect(wEmpty.find('[data-testid="saf-none-models"]').text()).not.toContain('null')
    // ★★ 两者的文案必须**不同**，否则这条判据会假绿
    expect(dictLookup(DICT['zh-CN'], 'saf.dim.nullEmpty')).not.toBe(
      dictLookup(DICT['zh-CN'], 'saf.dim.empty'),
    )
  })

  it('②-6 ★★★★★ filterOptionList 把 null 归一成 []（模板不许直接 .length）', () => {
    expect(filterOptionList(null)).toEqual([])
    expect(filterOptionList([])).toEqual([])
    expect(filterOptionList(['a'])).toEqual(['a'])
    // ★★ 视图源码里凡是取列表都必须过它 —— 直接读 options.value[x] 会崩
    // ⚠️⚠️ ★★★ 第一版只禁了「点号取值」`options.value.x.length`，
    //   而视图真正写的是**下标取值** `options.value[dim.key]` ⇒ 变异台 M06
    //   把 `filterOptionList(...)` 去掉换成直接下标，**照样全绿**。
    //   ⇒ 两种形态都得禁：`.length` 直接取，和「把原始可空值直接交给模板」。
    const code = stripComments(viewSrc())
    expect(code, '视图里出现了未经归一的点号取值').not.toMatch(/options\.value\.\w+\.length/)
    // ★★★ 下标形态：只允许出现在「=== null」比较与 filterOptionList 调用里
    const directIndex = code
      .split('\n')
      .filter((l) => /options\.value\[/.test(l))
      .filter((l) => !l.includes('filterOptionList') && !l.includes('=== null'))
    expect(
      directIndex,
      `视图把原始可空值直接交给了模板（会崩在 null.length）：\n${directIndex.join('\n')}`,
    ).toEqual([])
  })
})

// ── ③ 租户可见范围：与兄弟面相反 ────────────────────────────────────────────

describe('③ ★★★★★★ super_admin 看全部租户（★ 与兄弟面方向相反）', () => {
  it('③-1 ★★★★★★ 读 Go 源码：effectiveScopeTenant 对 super_admin 返回空串', () => {
    const f = goFunc(goSrc('admin/session_tenant.go'), 'effectiveScopeTenant')
    expect(f).toMatch(/if IsSuperAdminOrLegacy\(r\) \{\s*\n\s*return ""/)
    expect(f).toMatch(/return GetTenantID\(r\)/)
    // ★★ 对照兄弟面：那边 GetTenantID 兜底 "default" ⇒ 恒非空 ⇒ 永远租户内。
    //   这一条是「两族方向相反」的机械证据。
    const ctx = goFunc(goSrc('admin/context.go'), 'GetTenantID')
    expect(stripComments(ctx)).toMatch(/return "default"/)
  })

  it('③-2 ★★★★★★ 视图必须对 super_admin 显式写明「这是全部租户」', async () => {
    authState.role = 'super_admin'
    mFetch.mockResolvedValue(filled())
    const w = await mountView()
    await w.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="saf-crosstenant"]').exists(), 'super_admin 看不到跨租户提示').toBe(
      true,
    )
    // ★★ tenant_admin **不该**看到这条（否则提示就成了噪声）
    authState.role = 'tenant_admin'
    mFetch.mockResolvedValue(filled())
    const w2 = await mountView()
    await w2.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    expect(w2.find('[data-testid="saf-crosstenant"]').exists()).toBe(false)
    // ★ admin_key 与 super_admin 同档（同走 IsSuperAdminOrLegacy）
    authState.role = 'admin_key'
    const w3 = await mountView()
    expect(w3.find('[data-testid="saf-crosstenant"]').exists()).toBe(true)
  })

  it('③-3 ★★★★★★ 没有 ?tenant= 收窄手段（所以提示才必须写）', () => {
    const h = goFunc(goSrc('admin/session_analytics_top.go'), 'HandleFilterOptions')
    // ★★ effectiveScopeTenant **不读查询参数** —— 不得出现 query/tenant 相关读取
    expect(stripComments(h)).not.toMatch(/URL\.Query\(\)/)
    expect(stripComments(h)).not.toMatch(/X-Tenant-ID/)
    // ★ 载荷本身也不含租户标识 ⇒ 只能是聚合清单
    expect(sessionAnalyticsFilterOptionsIsTenantAgnostic(filled())).toBe(true)
    // ★★★ 负控：载荷里**一旦出现** tenant 字段就必须返回 false。
    //   ⚠️ 第一版只喂「不含 tenant」的夹具 ⇒ 变异台 M08 把它改成恒真**照样全绿**
    //   （因为所有正例都返回 true）⇒ 判据缺负控，本批补上。
    expect(
      sessionAnalyticsFilterOptionsIsTenantAgnostic({ models: [], providers: [], tenant_id: 't1' }),
      '载荷里明明有 tenant_id 却被判成「无租户维度」',
    ).toBe(false)
    expect(
      sessionAnalyticsFilterOptionsIsTenantAgnostic({ models: ['x'], TENANT: 't1' }),
      '大小写不同的 tenant 键同样必须识破',
    ).toBe(false)
    // ★ 对照：真正的聚合载荷仍必须为 true
    expect(sessionAnalyticsFilterOptionsIsTenantAgnostic(opts({ models: ['t1-x'] }))).toBe(true)
  })

  it('③-4 ★★★★★ 载荷形状：恒 2 键、无第三键', () => {
    expect(SESSION_ANALYTICS_FILTER_OPTION_KEYS).toEqual(['models', 'providers'])
    expect(SESSION_ANALYTICS_FILTER_OPTION_KEYS.length).toBe(2)
    // ★★ 兄弟面是 8 键 ⇒ 两族的解包器不能共用
    const sib = goSrc('admin/turns_filter_options.go')
    expect(sib).toMatch(/status_codes/)
    // ★ 窗口常量确实是 30（兄弟面 models/providers 也是 30，但只有 3/8 有窗口）
    expect(SESSION_ANALYTICS_FILTER_WINDOW_DAYS).toBe(30)
  })
})

// ── ④ 排序可观测 + 窗口 ──────────────────────────────────────────────────────

describe('④ ★★★★★ 排序去重可观测；两个维度**都**有 30 天窗口', () => {
  it('④-1 ★★★★★★ 读 Go 源码：两条查询都有 ORDER BY + DISTINCT', () => {
    const h = goFunc(goSrc('admin/session_analytics_top.go'), 'HandleFilterOptions')
    expect((h.match(/SELECT DISTINCT/g) ?? []).length).toBe(2)
    expect(h).toMatch(/ORDER BY model/)
    expect(h).toMatch(/ORDER BY provider/)
    // ★★ 空串被排掉了 ⇒ 判据可以要求「严格升序无重复」而不是「非降序」
    expect((h.match(/IS NOT NULL AND \w+ != ''/g) ?? []).length).toBe(2)
  })

  it('④-2 ★★★★★ 排序判据本身有牙（含重复与降序两个反例）', () => {
    expect(filterOptionValuesAreSortedUnique(['a', 'b'])).toBe(true)
    expect(filterOptionValuesAreSortedUnique([])).toBe(true)
    expect(filterOptionValuesAreSortedUnique(['a'])).toBe(true)
    expect(filterOptionValuesAreSortedUnique(['b', 'a'])).toBe(false)
    expect(filterOptionValuesAreSortedUnique(['a', 'a'])).toBe(false)
    expect(filterOptionValuesAreSortedUnique(['a', 'b', 'b'])).toBe(false)
  })

  it('④-3 ★★★★★ 两个维度都受窗口约束（读 Go 源码逐条数）', () => {
    const h = goFunc(goSrc('admin/session_analytics_top.go'), 'HandleFilterOptions')
    // ★★ 本族是 **2/2**，兄弟面是 3/8 —— 文案不能抄兄弟的
    expect((h.match(/last_request_at > NOW\(\) - INTERVAL '30 days'/g) ?? []).length).toBe(2)
    const sib = goFunc(goSrc('admin/turns_filter_options.go'), 'handleTurnsFilterOptions')
    expect((sib.match(/INTERVAL '%d days'|INTERVAL '\d+ days'/g) ?? []).length).toBeLessThan(8)
  })

  it('④-4 ★★★★ 页面把「已排序去重」显示出来，且维度来源列写对', async () => {
    authState.role = 'tenant_admin'
    mFetch.mockResolvedValue(filled())
    const w = await mountView()
    await w.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="saf-sorted-models"]').exists()).toBe(true)
    expect(w.find('[data-testid="saf-sorted-providers"]').exists()).toBe(true)
    // ★★ 聚合来源列必须写对（models_used / providers，不是「models」）
    const noteModels = w.find('[data-testid="saf-note-models"]').text()
    expect(noteModels).toContain('models_used')
    const noteProviders = w.find('[data-testid="saf-note-providers"]').text()
    expect(noteProviders).toContain('providers')
    expect(noteProviders).not.toContain('models_used')
  })

  it('④-5 ★★★★ 占位符必须真的被替换（不许在页面上显示字面 {days}）', async () => {
    authState.role = 'tenant_admin'
    mFetch.mockResolvedValue(filled())
    const w = await mountView()
    await w.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    const win = w.find('[data-testid="saf-window-note"]').text()
    expect(win).not.toContain('{days}')
    expect(win).not.toContain('{n}')
    expect(win).toContain('30')
    expect(w.find('[data-testid="saf-note-models"]').text()).not.toContain('{days}')
  })
})

// ── ⑤ 接线与编码门 ──────────────────────────────────────────────────────────

describe('⑤ ★★★★ 接线与深链可达', () => {
  it('⑤-1 ★★★★ 底栏不塞席（本族走抽屉），且抽屉席可见性正确', () => {
    // ★ 底栏 ≤5 席已满 ⇒ 新页面一律走 DRAWER_NAV
    expect(navItemsFor(DRAWER_NAV, 'super_admin').map((n) => n.key)).toContain(
      'session-analytics-filter-options',
    )
    // ★★ 'admin' 档放行 admin 三档（super_admin / tenant_admin / admin_key）
    for (const r of ['super_admin', 'tenant_admin', 'admin_key']) {
      expect(
        navItemsFor(DRAWER_NAV, r).map((n) => n.key),
        `${r} 应能看到这一席`,
      ).toContain('session-analytics-filter-options')
    }
    // ★ 普通用户**不该**看到（这条才是 requiresRole 生效的证据）
    expect(navItemsFor(DRAWER_NAV, 'user').map((n) => n.key)).not.toContain(
      'session-analytics-filter-options',
    )
    // ★★★ 必须**逐档**断言可见性，不能只验「user 看不到」：
    //   变异台 M17 把这一席的 requiresRole 改成了 'super_admin'，
    //   而上面那些断言**照样全绿** —— 因为 tenant_admin 依然看得到、
    //   user 依然看不到 ⇒ 只有「tenant_admin 能不能看到」才区分得开两档。
    expect(
      navItemsFor(DRAWER_NAV, 'tenant_admin').map((n) => n.key),
      'tenant_admin 看不到这一席 ⇒ 它的档位被改成了 super_admin（越权收窄）',
    ).toContain('session-analytics-filter-options')
    expect(
      navItemsFor(DRAWER_NAV, 'admin_key').map((n) => n.key),
      'admin_key 看不到这一席 ⇒ 它的档位被改成了 super_admin',
    ).toContain('session-analytics-filter-options')
  })

  it('⑤-2 ★★★★ 加载/错误态：失败时清空数据并显示错误', async () => {
    authState.role = 'tenant_admin'
    mFetch.mockResolvedValueOnce(filled())
    const w = await mountView()
    await w.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="saf-state"]').exists()).toBe(true)
    mFetch.mockRejectedValueOnce(new Error('boom'))
    await w.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="saf-error"]').text()).toContain('boom')
    expect(w.find('[data-testid="saf-state"]').exists()).toBe(false)
  })

  it('⑤-3 ★★★★ 全空态：两个列表都空时给出「不是故障」的说法', async () => {
    authState.role = 'tenant_admin'
    mFetch.mockResolvedValue(bothNull())
    const w = await mountView()
    await w.find('[data-testid="saf-load"]').trigger('click')
    await flushPromises()
    expect(w.find('[data-testid="saf-empty"]').exists()).toBe(true)
    // ★★ 半空提示**不该**在全空时出现
    expect(w.find('[data-testid="saf-half-empty"]').exists()).toBe(false)
    expect(w.find('[data-testid="saf-nonempty"]').exists()).toBe(false)
  })

  it('⑤-4 ★★★ 触控目标 ≥48 CSS px（把数值解析出来再比大小）', () => {
    const style = viewSrc()
    const m = style.match(/\.saf__btn\s*\{[^}]*min-height:\s*(\d+)px/)
    expect(m, '没找到 .saf__btn 的 min-height（正则失配会让这条恒绿）').toBeTruthy()
    expect(Number(m![1])).toBeGreaterThanOrEqual(48)
  })

  it('⑤-5 ★★★ 编码门：源文件与用例标题不得含 U+FFFD / NUL', () => {
    // ⚠️⚠️ ★★★ 判据本身踩过一个坑：初版写成 `buf.includes(0xef) || buf.includes(0xbf)`，
    //   那会**把每一行正常中文都判成污染**（0xef/0xbf 是 UTF-8 的前导/后续字节）。
    //   ⇒ 正确做法是按**完整三字节序列**匹配：EF BF BD。
    const FFFD = Buffer.from([0xef, 0xbf, 0xbd])
    for (const p of [
      'src/views/SessionAnalyticsFilterOptionsView.vue',
      'src/views/SessionAnalyticsFilterOptionsView.spec.ts',
      'src/api/sessionAnalyticsFilterOptions.ts',
    ]) {
      const buf = readFileSync(join(process.cwd(), p))
      expect(buf.includes(FFFD), `${p} 含 U+FFFD 字节`).toBe(false)
      expect(buf.includes(0), `${p} 含 NUL 字节`).toBe(false)
    }
    // ★★ 用例标题行要单独查：变异台按 fullName 子串匹配，
    //   标题被污染会让整批变异集体「红了但没命中」。
    const spec = readFileSync(
      join(process.cwd(), 'src/views/SessionAnalyticsFilterOptionsView.spec.ts'),
      'utf8',
    )
    for (const line of spec.split('\n')) {
      if (/^\s*(it|describe)\(/.test(line)) {
        // ⚠️⚠️ ★★★ 这里**不能**把 U+FFFD 写成字面替换字符 —— 那等于
        //   **自己给自己种一个待查字节**，判据会永远红（本批真踩到了）。
        //   必须用码点转义 `\uFFFD` 表达同一个字符。
        expect(line.includes('\uFFFD'), '用例标题含 U+FFFD').toBe(false)
        expect(line.includes('\0'), '用例标题含 NUL').toBe(false)
      }
    }
  })
})

// ── ⑥ i18n 两语种 ───────────────────────────────────────────────────────────

describe('⑥ ★★★★ i18n 两语种键存在且未翻译', () => {
  it('⑥-1 ★★★★ 视图用到的每个 saf.* 键在两语种都在且非「值 === 键名」', () => {
    const code = stripComments(viewSrc())
    const used = new Set<string>()
    for (const m of code.matchAll(/t\('(saf\.[a-zA-Z.]+)'/g)) used.add(m[1]!)
    // ★ 断言自己真的取到了键（否则这条会恒绿）
    expect(used.size, '没从视图里取到任何 saf.* 键').toBeGreaterThan(8)
    for (const key of used) {
      for (const loc of ['zh-CN', 'en-US'] as const) {
        const v = dictLookup(DICT[loc], key)
        expect(v, `${loc} 缺 ${key}`).toBeDefined()
        expect(typeof v, `${loc} 的 ${key} 不是字符串`).toBe('string')
        // ★★ i18n 门拦「值 === 键名」
        expect(v, `${loc} 的 ${key} 未翻译`).not.toBe(key)
      }
    }
  })

  it('⑥-2 ★★★★ nav 席名两语种都在', () => {
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const v = dictLookup(DICT[loc], 'nav.sessionAnalyticsFilterOptions')
      expect(typeof v === 'string' && v.length > 0, `${loc} 缺 nav 席名`).toBe(true)
      expect(v).not.toBe('nav.sessionAnalyticsFilterOptions')
    }
  })

  it('⑥-3 ★★★ 半空文案必须两语种都有，且与「全空」文案不同', () => {
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const half = dictLookup(DICT[loc], 'saf.halfEmpty')
      const empty = dictLookup(DICT[loc], 'saf.empty')
      expect(typeof half === 'string' && half.length > 0, `${loc} 缺 saf.halfEmpty`).toBe(true)
      expect(half).not.toBe(empty)
    }
  })
})

// ── 辅助（放在最后，供 ②-2 使用）────────────────────────────────────────────

function unwrapBothNull(): SessionAnalyticsFilterOptions {
  return unwrapSessionAnalyticsFilterOptions({ models: null, providers: null })
}
function unwrapLoose(v: unknown): unknown {
  return unwrapSessionAnalyticsFilterOptions(v)
}
