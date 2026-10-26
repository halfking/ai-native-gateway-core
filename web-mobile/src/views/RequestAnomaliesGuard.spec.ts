import { describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import RequestAnomaliesView from './RequestAnomaliesView.vue'
import { fetchAnomalyList, fetchAnomalyCounts } from '@/api/requestAnomalies'
import { anomalyRoleIsSuperAdmin } from '@/api/requestAnomalies'
import { zhCN } from '@/i18n/zh-CN'
import { enUS } from '@/i18n/en-US'
import { DRAWER_NAV, navItemsFor } from '@/config/appNav'

/**
 * 请求侧异常页 superAdmin 档兜底的护栏（2026-10-15，第一百三十批）。
 *
 * ★★★★★ 本批**不是接孤儿**（孤儿真缺口在批 128 已归零），而是补一道
 *   **全族普查出来的漏网席位**：把全仓每一条带 `requiresRole` 的路由
 *   逐条核过去，**唯一**没有视图内兜底的就是 `/request-anomalies`。
 *   而壳层 `hyper-wiring.ts` 的 `beforeEach` **只查 `requiresAuth` 与
 *   `auth.isAuthenticated`，全仓无人消费 `meta.requiresRole`**（§11.166）
 *   ⇒ 深链直达时那一席既不会被路由挡下，也不会被视图挡下，
 *   用户只会看到一串 403 错误串。
 *
 * ★★★ 本族是 **superAdmin 档**，且**服务端是真门**（逐字核过 Go 源码）：
 *   `admin/handler.go:936-937` 两条都挂 `h.superAdmin(...)`，
 *   `SuperAdminMiddleware` 在 `admin/auth.go:383-384` **逐字比**
 *   `claims.Role != "super_admin"` ⇒ 403。
 *
 * ⇒ ★★ 所以本族的视图内兜底兜的是**体验层**，不是**安全层** ——
 *   这与批 121~128 那一族（`admin` 档，`AdminMiddleware` 只认证不检 role，
 *   前端那一层是**唯一的门**）**方向相反**。
 *   ⇒ 判据 ①-2 / ①-4 把这个方向差别钉死，否则下一个人照抄 admin 档的注释，
 *     会在这里写下「本页是唯一的门」这种**事实相反**的话。
 *
 * 判据分四组：
 *  ① ★★★★★★ 服务端是真门（读 Go 源码）+ 本层兜的是体验层。
 *  ② ★★★★★★ 深链兜底：无权限时**不发请求**，且只渲染 nope 区。
 *  ③ ★★★★★★★ 全族普查 + **召回自证**（③-2，只比数量）
 *     + **配对自证**（③-3，逐条比对；第一百三十一批补）——
 *     ★★ 数量对账 ≠ 配对正确，详见 ③-3 注释里实测到的那 3 条串线。
 *  ④ ★★★★ nope 文案两语种 + 编码门 + 抽屉席可见性。
 *
 * ⚠️ 本文件**不碰**既有 31 条业务判据（那些在 RequestAnomaliesView.spec.ts，
 *   按 super_admin 装配）—— 权限与业务不变量分开钉，免得互相拖红。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

// ⚠️ 这里 mock 掉整个 auth store（而不是像业务 spec 那样用真实 pinia 预置
//   `userInfo`）—— 因为本组要反复换角色，而 pinia 的 `role` 是 computed，
//   换 store 实例比换 mock 的普通对象更绕。
const authState = { role: 'super_admin' as string | null }
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/requestAnomalies', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/requestAnomalies')>()
  return { ...actual, fetchAnomalyList: vi.fn(), fetchAnomalyCounts: vi.fn() }
})

const mList = fetchAnomalyList as unknown as ReturnType<typeof vi.fn>
const mCounts = fetchAnomalyCounts as unknown as ReturnType<typeof vi.fn>

const DICT: Record<string, unknown> = { 'zh-CN': zhCN, 'en-US': enUS }

function dictLookup(root: unknown, path: string): unknown {
  let cur: unknown = root
  for (const seg of path.split('.')) {
    if (typeof cur !== 'object' || cur === null) return undefined
    cur = (cur as Record<string, unknown>)[seg]
  }
  return cur
}

/**
 * ★★★ 剥掉注释，只留代码。
 *   普查必须看**可执行代码**而非词 —— `requiresRole` 这个串在路由注释里
 *   出现过（都在解释「为什么这一席没服务端角色下限」），按词统计会虚增。
 *
 * ⚠️⚠️⚠️ 第一百三十二批修掉的**量具缺陷**：**剥离顺序不能反**。
 *
 *   旧写法（先块后行）：
 *     src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
 *
 *   块注释正则**不知道自己在不在注释里**。而 `config/appNav.ts` 的
 *   **行注释里写着 URL**，URL 里有块注释起点：
 *     // MaaS 租户运维面。**superAdmin 档**（/api/admin/maas/tenants/** 全 superAdmin）
 *     // MaaS 租户目录与价目。**admin 档**（/api/maas/** 是 h.admin(...)）
 *     // 不碰 /api/v1/approvals/* 的 approve/reject/resume
 *   ⇒ 三个 `/api/**` 各贡献一个块注释起点，块正则把它们当**块注释开头**，
 *     去找最近的**星号斜杠闭合标记**（在 685 行）⇒ **231~685 行的真实代码被整段删掉**：
 *     appNav.ts 30924 字节 → **6118 字节（只剩 19.8%）**，
 *     `'request-anomalies'` 从 2 次变成 **0 次** ⇒ 席位普查报「一个席都没有」。
 *
 *   ⇒ 修法：**先按行删 `//`，再删块注释**。行注释删完之后，
 *     剩下的块注释起点才都是真的，两类标记自然配平。
 *   实测：appNav.ts 30924 → 11698（37.8%），且先删行注释后
 *   块注释起点 5 个 / 闭合 5 个**配平**；`'request-anomalies'` 保持 2 次。
 *
 * ⚠️⚠️ 而 `router/index.ts` 之所以一直没被咬到，是**运气**：
 *   它里面块注释起点有 6 处（也在行注释的 URL 里），但**闭合标记一个都没有**
 *   ⇒ 块替换找不到闭合 ⇒ 空操作 ⇒ 幸存。
 *   ⇒ ★ **当前已发布的普查判据（③ 组）本身没被影响，但它不是「设计正确」，
 *     而是「碰巧没踩」。** 顺序必须按上面对的方式钉死。
 */
function stripComments(src: string): string {
  return src.replace(/^\s*\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '')
}

function goSrc(rel: string): string {
  const p = join(process.cwd(), '..', rel)
  expect(existsSync(p), `Go 源码路径不存在：${rel}`).toBe(true)
  return readFileSync(p, 'utf8')
}

function viewSrc(): string {
  return readFileSync(join(process.cwd(), 'src/views/RequestAnomaliesView.vue'), 'utf8')
}

async function mountAs(role: string | null) {
  authState.role = role
  mList.mockClear()
  mCounts.mockClear()
  const w = mount(RequestAnomaliesView)
  await flushPromises()
  await flushPromises()
  return w
}

// ── ① 服务端是真门 ──────────────────────────────────────────────────────────

describe('① ★★★★★★ 服务端是真门；本层兜的是体验层', () => {
  it('①-1 ★★★★★★ 读 Go 源码：注册挂 superAdmin，中间件逐字比 role', () => {
    const h = goSrc('admin/handler.go')
    // ★★ **两条**注册都要钉：`:936` 是列表端点，`:937` 是子路由
    //   （漏掉第二条的话，将来有人把子路由挪到 h.admin 下面不会被发现）
    expect(h).toMatch(
      /mux\.HandleFunc\("\/api\/admin\/request-anomalies", h\.superAdmin\(h\.handleRequestAnomalies\)\)/,
    )
    expect(h).toMatch(
      /mux\.HandleFunc\("\/api\/admin\/request-anomalies\/", h\.superAdmin\(h\.handleRequestAnomalySubrouter\)\)/,
    )
    // ★★ h.superAdmin 必须是 SuperAdminMiddleware，不能是 AdminMiddleware
    expect(stripComments(h)).toMatch(
      /func \(h \*Handler\) superAdmin\(fn http\.HandlerFunc\) http\.HandlerFunc \{\s*\n\s*return SuperAdminMiddleware\(fn, h\.db, h\.secret\)/,
    )
    // ★★★ 承重判据：中间件里逐字比 role ⇒ 它是**真门**
    const auth = goSrc('admin/auth.go')
    expect(
      auth.match(/if claims\.Role != "super_admin" \{[\s\S]{0,200}?writeError\(w, http\.StatusForbidden/),
      '没找到 SuperAdminMiddleware 里的 role 比对（正则失配会让这条恒绿）',
    ).toBeTruthy()
  })

  it('①-2 ★★★★★★ 对照：admin 档那一族**没有** role 比对（方向相反的证据）', () => {
    // ★ 这是本族与批 121~128 的**根本差别**：那边 AdminMiddleware 只认证
    //   不检 role，所以前端那一层是唯一的门；这边逐字检 ⇒ 前端只是体验层。
    const auth = goSrc('admin/auth.go')
    const adminFn = auth.slice(auth.indexOf('func AdminMiddleware('))
    const adminFnOnly = adminFn.slice(0, adminFn.indexOf('\nfunc '))
    expect(stripComments(adminFnOnly), 'AdminMiddleware 里出现了 role 比对 ⇒ 两族方向相同了').not.toMatch(
      /claims\.Role\s*!=|claims\.Role\s*==/,
    )
  })

  it('①-3 ★★★★★★ admin_key **进不来**（这一族只认 super_admin）', () => {
    expect(anomalyRoleIsSuperAdmin('super_admin')).toBe(true)
    // ★★ 这六个都必须 false —— 尤其 admin_key：
    //   在 `v1DataHorizon` 时代 admin_key 与 super_admin 同档，
    //   照抄那边的判据会把它错放进来。
    for (const r of ['admin_key', 'tenant_admin', 'user', '', null, undefined]) {
      expect(anomalyRoleIsSuperAdmin(r), `${String(r)} 不该被放行`).toBe(false)
    }
  })

  it('①-4 ★★★★★ 视图注释必须说清「体验层而非安全层」', () => {
    const src = viewSrc()
    // ★ 不判具体措辞，判**这三句话在不在** —— 措辞会改，结论不能丢：
    //   ① 本层兜的是体验层；② 与 admin 档方向相反；③ 服务端才是真门。
    expect(src).toMatch(/体验层/)
    expect(src).toMatch(/方向相反|相反/)
    expect(src).toMatch(/真门/)
    // ⚠️★★ 这里**故意不写** `not.toMatch(/本页是唯一的一?道?门/)` 那样的反向断言。
    //   本仓踩过两次：否定的对象是**词**，而那个词恰好合法地出现在
    //   「写出『本页是唯一的门』这种**事实相反**的话」这种注释里 ⇒ 反向断言必然自爆。
    //   ⇒ 要否就得否**可执行代码**（先 stripComments），而这句话只存在于注释中
    //   ⇒ 剥完注释再否就变成恒真的假门。**所以此处只能正向钉。**
  })

  it('①-5 ★★★★★ 路由与抽屉席都挂 super_admin', () => {
    const router = readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8')
    expect(router).toMatch(/path: '\/request-anomalies'/)
    expect(router).toMatch(/titleKey: 'anomalies\.title', requiresAuth: true, requiresRole: 'super_admin'/)

    const seat = DRAWER_NAV.find((n) => n.key === 'request-anomalies')
    expect(seat, '抽屉里没有这一席').toBeTruthy()
    expect(seat!.requiresRole).toBe('super_admin')

    // ★★ AppDrawer 的白名单是**排好序的字面量数组**（`tenant_admin 少掉的只有
    //   superAdmin 档那些` 那条），所以锚数组元素本身，不要锚偏移窗口。
    //   ⚠️ 本批实测：这一席早已在白名单里（AppDrawer.spec.ts:202）——
    //   本批的缺口只在**视图内兜底**，接线是现成的。
    const drawer = readFileSync(join(process.cwd(), 'src/components/shell/AppDrawer.spec.ts'), 'utf8')
    expect(drawer, 'AppDrawer.spec.ts 的 superAdmin 白名单里没有这一席（加新 superAdmin 席会红的那条）').toMatch(
      /^\s*'request-anomalies',?\s*$/m,
    )
  })
})

// ── ② 深链兜底 ──────────────────────────────────────────────────────────────

describe('② ★★★★★★ 深链兜底：不发请求且只渲染 nope 区', () => {
  it('②-1 ★★★★★★ 无权限时**一个请求都不发**（★ 兜底是「不发」不是「藏起来」）', async () => {
    const w = await mountAs('tenant_admin')
    // ★★★ 挂载即取数（视图 setup 尾部直接 `void loadList(); void loadCounts()`，
    //   RequestAnomaliesView.vue:165-166）⇒ 「不发请求」挂载后立刻可验，
    //   不需要点任何按钮。
    expect(mList, '无权限时竟然发了列表请求').not.toHaveBeenCalled()
    expect(mCounts, '无权限时竟然发了计数请求').not.toHaveBeenCalled()
    expect(w.find('[data-testid="ra-nope"]').exists()).toBe(true)
    expect(w.find('[data-testid="ra-nope-why"]').exists()).toBe(true)
  })

  it('②-2 ★★★★★★ 无权限时**不渲染业务区**', async () => {
    const w = await mountAs('admin_key')
    // nope 区自己有一个标题（`.ra__panel-title`），所以这条不能写成「没有标题」
    expect(w.find('.ra__panel-title').exists()).toBe(true)
    // ★ 模板里 `.ra__panel` 共 4 处：nope 区 1 + 业务区 3 ⇒ 无权限时只剩 1
    expect(w.findAll('.ra__panel').length).toBe(1)
    // 没漏出裸 i18n 键（说明 nope 区走的是真字典）。
    // ⚠️ 锚**具体键名**而不是 `anomalies.` 这个前缀 ——
    //   英文文案本身就写着 "request anomalies."，用前缀判必然误报（第一版就踩了）。
    expect(w.text()).not.toMatch(/anomalies\.(counts|unresolved|newToday|filter|clear|prev|next)\b/)
  })

  it('②-3 ★★★★★★ 有权限时正常取数，且 nope 区消失', async () => {
    mList.mockResolvedValue({ anomalies: [], count: 0 })
    mCounts.mockResolvedValue({ unresolved: 0, new_today: 0 })
    const w = await mountAs('super_admin')
    expect(mList, 'super_admin 没有发请求 ⇒ 守卫把它也挡了').toHaveBeenCalled()
    expect(mCounts, 'super_admin 没有发计数请求 ⇒ 守卫把它也挡了').toHaveBeenCalled()
    expect(w.find('[data-testid="ra-nope"]').exists()).toBe(false)
    expect(w.findAll('.ra__panel').length).toBeGreaterThan(1)
  })

  it('②-4 ★★★★★ 取数函数里两道早退都必须在（源码形态）', () => {
    const code = stripComments(viewSrc())
    // ★★ 两个取数函数各有一道 `if (!allowed.value) return`
    //   ⚠️ 只钉**数量**不够（一条可能写两遍、另一处压根没写）
    //   ⇒ 同时钉「在各自函数体内」。
    expect((code.match(/if \(!allowed\.value\) return/g) ?? []).length).toBe(2)
    expect(code).toMatch(/async function loadCounts\(\)[\s\S]{0,200}?if \(!allowed\.value\) return/)
    expect(code).toMatch(/async function loadList\(\)[\s\S]{0,200}?if \(!allowed\.value\) return/)
  })

  it('②-5 ★★★★ 早退必须在取数**之前**（不是请求发出后才丢弃结果）', () => {
    const code = stripComments(viewSrc())
    for (const fn of ['loadCounts', 'loadList']) {
      const body = code.slice(code.indexOf(`async function ${fn}(`), code.indexOf(`async function ${fn}(`) + 400)
      const guardAt = body.indexOf('if (!allowed.value) return')
      const callAt = body.search(/fetchAnomaly/)
      expect(guardAt, `${fn} 里没找到早退`).toBeGreaterThan(-1)
      expect(callAt, `${fn} 里没找到取数调用`).toBeGreaterThan(-1)
      // ★ 守卫位置必须**早于**发请求的位置
      expect(guardAt, `${fn} 的早退在发请求之后 ⇒ 兜底是假的（请求已经出去了）`).toBeLessThan(callAt)
    }
  })
})

// ── ③ ★★ 全族普查：本批真正的产出 ──────────────────────────────────────────

/**
 * ★★ 路由声明的匹配式：`path` → `component` → `requiresRole` 三者同属一条路由。
 *
 * ⚠️⚠️⚠️ 第一百三十一批修掉的**真缺陷**：原来两段窗口用的是裸 `[\s\S]{0,N}?`，
 * 它**不阻止跨越路由边界**。实测：
 *
 *   // `/pending-responses` 这条**没有** requiresRole，
 *   // 但它的 `path:` 后面 400 字符内就撞上了**下一条**路由的 component/requiresRole
 *   ⇒ 配出 ('/pending-responses', RequestAnomaliesView.vue, super_admin)
 *   ⇒ 而这一对**真正属于** '/request-anomalies'
 *
 * 15 条里**串了 3 条**（/pending-responses、/dispatch-journal、/data-horizon）。
 *
 * ★★ 而且这个错**恰好是 `stripComments` 引进来的**：剥掉注释后行距变短，
 * 原本够不着 400 字符的跨界匹配**够得着了** ⇒ 批 130 为了修「20 vs 15」
 * 而加的 stripComments，反过来制造了配对错误。
 *
 * ⇒ 修法：窗口里**不得再出现另一个 `path:`**（否定前瞻）。
 *   修后与「按块切分取真值」的独立实现**逐条完全一致**（见 ③-4）。
 */
const ROUTE_DECL =
  /path:\s*'([^']+)',(?:(?!path:)[\s\S]){0,400}?component:\s*\(\)\s*=>\s*import\('@\/views\/([^']+)'\),(?:(?!path:)[\s\S]){0,200}?requiresRole:\s*'([^']+)'/g

describe('③ ★★★★★★★ 全族普查：每一条 requiresRole 路由的视图都必须有兜底', () => {
  it('③-1 ★★★★★★★★ 逐条核对（★ 这条是本批真正的产出）', () => {
    // ⚠️★ 先 strip 注释再扫：路由注释里有 5 处散文 `requiresRole: 'admin'`
    //   （都在解释「为什么这一席没有服务端角色下限」），不剥就会把它们
    //   当成真声明，普查结论直接失真。
    const router = stripComments(readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8'))
    const routes = [...router.matchAll(ROUTE_DECL)].map((m) => ({ path: m[1]!, comp: m[2]!, role: m[3]! }))

    // ★★★ 先断言「真的扫到了东西」—— 否则下面那个 for 循环是**恒真**的
    expect(routes.length, '一条 requiresRole 路由都没扫到 ⇒ 这条判据形同虚设').toBeGreaterThan(10)

    const missing: string[] = []
    for (const r of routes) {
      const f = join(process.cwd(), 'src/views', r.comp)
      if (!existsSync(f)) {
        missing.push(`${r.path}(视图文件不存在)`)
        continue
      }
      const code = stripComments(readFileSync(f, 'utf8'))
      const hasGuardRender = /nope|allowed|denied|forbidden/.test(code)
      // ⚠️★★ 这里**故意不含** `RoleIsSuperAdmin`，也**故意不含** `useAuthStore`。
      //   变异台 M10 把这两层各踩了一次，都是「判据绿、被测物已坏」：
      //   ① 含 `RoleIsSuperAdmin` —— 本视图 import 的 helper 就叫
      //      `anomalyRoleIsSuperAdmin`，被它**顶替** ⇒ 不读 auth store 也绿；
      //   ② 只要求 `useAuthStore` —— 变异后 `import` 与 `useAuthStore()` 调用
      //      **都还在**，只是不读 `.role` 了 ⇒ 照样绿。
      // ⇒ 结论：「调用了 auth store」**不等于**「读了角色」；
      //   钉角色来源只能钉**属性读**，钉调用点是钉不住的。
      //   （全族 15 个视图实测都写 `auth.role` ⇒ 收窄后仍然全过，不是为这一席放宽。）
      const hasRoleSource = /\.role\b|authState\.role|RoleNeedsAdminGate|RoleIsAdminTier|RoleSeesAllTenants/.test(code)
      if (!(hasGuardRender && hasRoleSource)) {
        missing.push(`${r.path} → ${r.comp}（render=${hasGuardRender} role=${hasRoleSource}）`)
      }
    }
    expect(
      missing,
      `这些 requiresRole 路由的视图**没有兜底**（深链直达会裸奔）：\n  ${missing.join('\n  ')}`,
    ).toEqual([])
  })

  it('③-2 ★★★★★★★ 召回自证：正则不得漏掉任何一条真实声明', () => {
    // ★★★★★ 这条是本组的关键。③-1 只证明「扫到的都有兜底」，
    //   **不证明**「没漏扫」—— 而漏扫恰恰是普查类判据最常见的失效方式：
    //   将来有人把某条路由的 `component` 写成变量、或把 meta 拆行，
    //   正则会静默少匹配一条，③-1 照样全绿 ⇒ **缺口就此漏网且无人知晓**。
    //
    // ⇒ 解法：把「声明总数」与「匹配数」直接对账。
    const router = stripComments(readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8'))
    const declared = [...router.matchAll(/requiresRole:\s*'([^']+)'/g)].map((m) => m[1]!)
    const matched = [...router.matchAll(ROUTE_DECL)].map((m) => m[1]!)

    expect(declared.length, '连一条 requiresRole 声明都没扫到 ⇒ 判据量具坏了').toBeGreaterThan(10)
    expect(
      matched.length,
      `匹配到 ${matched.length} 条，但源码里有 ${declared.length} 处 requiresRole 声明\n` +
        `⇒ 正则漏扫了 ${declared.length - matched.length} 条，这些路由**不在普查范围内**。`,
    ).toBe(declared.length)
  })

  it('③-3 ★★★★★★★★ ★★ 配对自证：逐条比对，而不是只比数量（第一百三十一批）', () => {
    // ★★★★★★ **这条是 ③-2 抓不到的那一面。**
    //
    // ③-2 只对账**数量**（matched.length === declared.length）。而实测过的那个
    // 缺陷是**配对错**：path 来自上一条、component/requiresRole 来自下一条。
    // ⇒ 数量照样是 15===15，③-2 **照样绿**，而 ③-1 报出来的路径是错的 ——
    //   将来真有一席缺兜底，会把下一个人**指到错的路由**上。
    //
    // ⇒ 解法：换一个**独立实现**（按 `{ path:` 块切分，块内取 component/role），
    //   与正则结果**逐条**比对（比较集合，不只比长度）。
    const router = stripComments(readFileSync(join(process.cwd(), 'src/router/index.ts'), 'utf8'))

    // ── 独立实现：按「一条路由 = 一个 `{ path: … }` 对象块」切分 ──
    const truth: Array<[string, string, string]> = []
    for (const m of router.matchAll(/\{\s*path:\s*'([^']+)'([\s\S]{0,600}?)\n  \},/g)) {
      const comp = m[2]!.match(/component:\s*\(\)\s*=>\s*import\('@\/views\/([^']+)'\)/)
      const role = m[2]!.match(/requiresRole:\s*'([^']+)'/)
      if (role && comp) truth.push([m[1]!, comp[1]!, role[1]!])
    }

    const viaRegex = [...router.matchAll(ROUTE_DECL)].map((m): [string, string, string] => [m[1]!, m[2]!, m[3]!])

    expect(truth.length, '独立实现一条都没切出来 ⇒ 两边都坏了').toBeGreaterThan(10)

    const truthSet = new Set(truth.map((t) => t.join('|')))
    const regexSet = new Set(viaRegex.map((t) => t.join('|')))
    const wrongPairing = [...regexSet].filter((k) => !truthSet.has(k))
    const missed = [...truthSet].filter((k) => !regexSet.has(k))

    expect(
      wrongPairing,
      `这些 (path, 视图, 档位) 三元组是**串出来的**（path 与 component/role 不属于同一条路由）：\n  ${wrongPairing.join('\n  ')}`,
    ).toEqual([])
    expect(missed, `这些真实路由声明正则一条都没匹配到：\n  ${missed.join('\n  ')}`).toEqual([])
    // ★ 与 ③-2 的差别就在这：③-2 只断言 length 相等，这里断言**集合相等**。
    expect(regexSet.size).toBe(truthSet.size)
  })

  it('③-4 ★★★★ 普查判据对**改动前的这一席**确实会红（负控结论留档）', () => {
    // 这条留的是**结论**而不是行为：把普查判据的两条正则原样跑在本批
    // 改动之前的视图源码上（HEAD 版本由 §11.167 的负控实测给出），
    // 必须判成「无兜底」—— 否则 ③-1 只是一条恰好为真的装饰。
    //
    // ⚠️ 这里不能直接读 git HEAD（spec 跑在打包产物里，仓库未必在），
    //   所以只钉**判据自身的两个子句**：view 侧要 `nope|allowed` 且要
    //   `auth.role`。任何一席缺其中之一，③-1 就会把它列进 missing。
    const code = stripComments(viewSrc())
    expect(/nope|allowed|denied|forbidden/.test(code), '本视图连兜底渲染的影子都没有').toBe(true)
    expect(/auth\.role/.test(code), '本视图没有从 auth store 取角色 ⇒ 兜底恒真').toBe(true)
  })
})

// ── ④ i18n / 编码 / 抽屉席 ─────────────────────────────────────────────────

describe('④ ★★★★ nope 文案、编码门与抽屉席', () => {
  it('④-1 ★★★★ 两语种各有 nope.msg / nope.why，且值 ≠ 键名', () => {
    for (const loc of ['zh-CN', 'en-US'] as const) {
      const msg = dictLookup(DICT[loc], 'anomalies.nope.msg')
      const why = dictLookup(DICT[loc], 'anomalies.nope.why')
      expect(typeof msg === 'string' && msg.length > 0, `${loc} 缺 anomalies.nope.msg`).toBe(true)
      expect(typeof why === 'string' && why.length > 0, `${loc} 缺 anomalies.nope.why`).toBe(true)
      expect(msg).not.toBe('anomalies.nope.msg')
      expect(why).not.toBe('anomalies.nope.why')
    }
  })

  it('④-2 ★★★★ nope 文案必须说清「服务端会 403」（★ 那是本族的事实）', () => {
    // ★ 不押语种：两语种都要提到 403 这个可观测事实
    for (const loc of ['zh-CN', 'en-US'] as const) {
      expect(String(dictLookup(DICT[loc], 'anomalies.nope.why'))).toContain('403')
    }
  })

  it('④-3 ★★★★ 编码门：本批改的源文件不得含 U+FFFD / NUL', () => {
    // ⚠️ 必须按**完整三字节序列**匹配 EF BF BD —— 写成 includes(0xef)
    //   会把每一行正常中文都判成污染（第 129 批踩过）。
    const FFFD = Buffer.from([0xef, 0xbf, 0xbd])
    for (const p of [
      'src/views/RequestAnomaliesView.vue',
      'src/api/requestAnomalies.ts',
      'src/i18n/zh-CN.ts',
      'src/i18n/en-US.ts',
      'src/views/RequestAnomaliesView.spec.ts',
      'src/views/RequestAnomaliesGuard.spec.ts',
    ]) {
      const buf = readFileSync(join(process.cwd(), p))
      expect(buf.includes(FFFD), `${p} 含 U+FFFD 字节`).toBe(false)
      expect(buf.includes(0), `${p} 含 NUL 字节`).toBe(false)
    }
  })

  it('④-4 ★★★★ 抽屉席可见性：只有 super_admin 看得见这一席', () => {
    expect(navItemsFor(DRAWER_NAV, 'super_admin').map((n) => n.key)).toContain('request-anomalies')
    for (const r of ['tenant_admin', 'admin_key', 'user', '']) {
      expect(
        navItemsFor(DRAWER_NAV, r).map((n) => n.key),
        `${r} 看得见这一席`,
      ).not.toContain('request-anomalies')
    }
  })
})


// ── ⑤ ★★ 席档位 vs 视图兜底档位：逐条对账（第一百三十二批）──────────────────

/**
 * ★★★ 判据自身的前提：`stripComments` **不许毁掉真实代码**。
 *
 *   起因见上面 `stripComments` 的注释：`config/appNav.ts` 的行注释里写着
 *   `/api/admin/maas/tenants/**` 这种 URL，URL 里的 `/*` 被块注释正则当成
 *   块注释开头 ⇒ 231~685 行被整段删掉 ⇒ 席位普查报「一个席都没有」。
 *
 *   ⚠️ 这类错误**不报错、不红**，它只是**安安静静地给出「全都没有」的答案** ——
 *   而「全都没有」看起来像个正常结论。所以必须有一条判据盯住**剥离本身**。
 */
describe('⑤ ★★★★★★ 剥注释不许毁代码；席档位与视图兜底档位逐条一致', () => {
  const WEB = process.cwd()

  function seatEntries(navCode: string): Map<string, { key: string; role: string | null }> {
    const seg = navCode.slice(navCode.indexOf('export const DRAWER_NAV'))
    const out = new Map<string, { key: string; role: string | null }>()
    for (const m of seg.matchAll(/\{\s*key:\s*'([^']+)',([\s\S]{0,320}?)\n?\s*\}/g)) {
      const to = m[2]!.match(/to:\s*'([^']+)'/)
      if (!to) continue
      const rr = m[2]!.match(/requiresRole:\s*'([^']+)'/)
      out.set(to[1]!, { key: m[1]!, role: rr ? rr[1]! : null })
    }
    return out
  }

  it('⑤-1 ★★★★★★ 剥离前后，appNav.ts 的席 key 一个都不能少', () => {
    const raw = readFileSync(join(WEB, 'src/config/appNav.ts'), 'utf8')
    const stripped = stripComments(raw)
    const rawKeys = [...raw.matchAll(/\bkey:\s*'([^']+)'/g)].map((m) => m[1]!)
    expect(rawKeys.length, '连一个 key 都没扫到 ⇒ 判据量具坏了').toBeGreaterThan(40)
    const lost = [...new Set(rawKeys)].filter((k) => !stripped.includes(`key: '${k}'`))
    expect(
      lost,
      `剥注释后丢了 ${lost.length} 个席（说明 stripComments 把真实代码当注释删了）：\n  ${lost.join('\n  ')}`,
    ).toEqual([])
    // ★★ 再钉一条量级：剥完至少得保住三成以上。
    //   旧的「先块后行」写法在 appNav.ts 上只剩 **19.8%**，必然踩线。
    //
    // ⚠️⚠️ 但要如实说清：**这条不是抓住那个缺陷的那条**。变异台 P04 实测 ——
    //   把这个下限从 0.3 放宽到 0.05，在「剥离已改坏」的情况下 ⑤-1 **照样红**，
    //   因为上面那条「席 key 一个都不能少」先抓住了。
    // ⇒ 所以它防的是**另一种**失效（代码被删但恰好没删到任何 key），属次要防线，
    //   **不要把它当成 ⑤-1 的主牙**。主牙是上面那条逐 key 比对。
    expect(stripped.length / raw.length, '剥注释后剩下的代码量过少').toBeGreaterThan(0.3)
  })

  it('⑤-2 ★★★★★★ 席档位与路由 meta 的 requiresRole 必须一致', () => {
    const nav = stripComments(readFileSync(join(WEB, 'src/config/appNav.ts'), 'utf8'))
    const seats = seatEntries(nav)
    expect(seats.size, '一个席都没解析出来 ⇒ 量具坏了（先看 ⑤-1）').toBeGreaterThan(40)

    const router = stripComments(readFileSync(join(WEB, 'src/router/index.ts'), 'utf8'))
    const routes = [...router.matchAll(ROUTE_DECL)].map((m) => ({ path: m[1]!, role: m[3]! }))
    expect(routes.length).toBeGreaterThan(10)

    const problems: string[] = []
    for (const r of routes) {
      const seat = seats.get(r.path)
      if (!seat) {
        problems.push(`${r.path}：路由声明了 requiresRole:'${r.role}'，但抽屉里没有这一席`)
        continue
      }
      if ((seat.role ?? null) !== r.role) {
        problems.push(`${r.path}：路由 requiresRole='${r.role}' vs 抽屉席 requiresRole='${seat.role ?? '(缺省)'}（席 key=${seat.key}）`)
      }
    }
    expect(problems, `席与路由 meta 的档位对不上：\n  ${problems.join('\n  ')}`).toEqual([])
  })

  it('⑤-3 ★★★★★ ★★ 已知分歧：/sliding-window 的席比视图**严一档**（待属主拍板）', () => {
    // ⚠️⚠️ 这一条**不是**「已确认的 bug」，而是**登记在案的待决分歧**。
    //
    //   席：  config/appNav.ts        requiresRole: 'super_admin'
    //   视图：slidingWindowRoleIsPrivileged = ['super_admin','admin_key']
    //   服务端：admin/handler.go:1475 `monitorH.RegisterMonitorRoutes(mux, h.admin)`
    //          ⇒ h.admin = AdminMiddleware，**只认证不检 role** ⇒ 服务端**没有下限**
    //
    // ⇒ 所以 `admin_key` 深链进来时**服务端会放行**，页面**能用**，只是抽屉里没有入口。
    //   这不是「深链裸奔」，而是**三处口径互不相同**。
    //
    // ⇒ ★★ 而抽屉席那条的**立档理由**写的是：
    //   「任何通过认证的主体报出别的租户的 credential_id 就能读到逐次调用明细
    //     ⇒ 连 tenant_admin 都必须挡」
    //   按这句话的逻辑，`admin_key` 也**应该**被挡。
    // ⇒ ★★⇒ **这是口径决策，不是技术修复**：要么席放宽到 admin 档，
    //   要么视图收紧到只认 super_admin。**该由属主拍板，不该由本批偷偷改任一侧。**
    //   ⇒ 在此之前，本条把它**显式登记**为已知分歧（而不是让它悄悄存在）。
    //
    // ★ 登记的方式是「钉住现状 + 钉住理由」：
    //   下一个人想动它时，会先看到这段说明，而不是以为自己在修 bug。
    const nav = stripComments(readFileSync(join(WEB, 'src/config/appNav.ts'), 'utf8'))
    expect(seatEntries(nav).get('/sliding-window')?.role, '这一席的档位若已改，本条要重新拍板').toBe('super_admin')

    const viewSrc = stripComments(readFileSync(join(WEB, 'src/views/SlidingWindowView.vue'), 'utf8'))
    expect(
      viewSrc,
      '视图兜底若已改成只认 super_admin（口径已拍板），请连同本说明一起更新',
    ).toMatch(/\bslidingWindowRoleIsPrivileged\(/)

    // ★★★★★★★ **分歧的本体就在这里**，必须钉住角色集本身。
    // ⚠️ 第一版只钉了上面那句「视图调用了 helper」，**结果登记是假的**：
    //   变异台 P02 把 SLIDING_WINDOW_GLOBAL_ROLES 收紧成 ['super_admin']
    //   （也就是真去把这个分歧解决掉），⑤-3 **一声不吭地绿了** ——
    //   因为「视图还在调用那个 helper」在两种情况下都成立。
    // ⇒ ⇒ **钉「分歧存在」要钉分歧的那一处值，不能钉调用点。**
    const apiSrc = stripComments(readFileSync(join(WEB, 'src/api/slidingWindow.ts'), 'utf8'))
    const set = apiSrc.match(/SLIDING_WINDOW_GLOBAL_ROLES\s*=\s*\[([^\]]*)\]/)?.[1] ?? ''
    expect(
      set,
      '若 SLIDING_WINDOW_GLOBAL_ROLES 不再含 admin_key（分歧已解决），请连同本说明一起更新',
    ).toContain('admin_key')

    const h = goSrc('admin/handler.go')
    expect(
      h.match(/monitorH\.RegisterMonitorRoutes\(mux, h\.admin\)/),
      '若注册处已不是 h.admin，「服务端无下限」这个前提失效，本条要重判',
    ).toBeTruthy()
  })
})
