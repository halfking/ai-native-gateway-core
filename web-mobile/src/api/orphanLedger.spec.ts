import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

/**
 * ★★★★★★ **孤儿棘轮门**（2026-10-08，第一百批）。
 *
 * ## 为什么要有这道门
 *
 * 批 98/99 把 `/api/admin/task-profile` 与
 * `/api/admin/task-profile/corrections/stats` 的 API 层做完了
 * （128 + 214 条判据、87 条变异全有牙、文档 §11.134/§11.135），
 * **但两个模块都没有被任何 UI 引用**。
 * ⇒ 全仓 93 个 api 模块里有 **30 个**是「做完了但用户点不到」的孤儿。
 * ⇒ ★★★ **API 层做完 ≠ 功能复制到移动端。** 用户点不到的东西不算数。
 *
 * 这道门把孤儿清单**显式登记**下来，于是：
 * - 新增一个 api 模块却忘了接线 ⇒ 立刻红（清单多了一项）；
 * - 接上了一项 ⇒ 也红（清单少了一项），**逼着人手工删掉那一行** ——
 *   这正是我们要的：清单只能变小，且每次变小都是一次有记录的收口。
 *
 * ★ 与「现状盘点类数字必然腐烂」不冲突：那张说的是**把数字写进判据**。
 *   这里登记的是**一个集合**，且集合的每次变化都会经过一次显式的 diff ——
 *   「变红」正是它的工作方式，不是它的缺陷。
 *
 * ## 判据本身的口径：从 UI 出发的**传递**可达性
 *
 * 「孤儿」= `src/api/*.ts`（排除 `.test.ts` / `.spec.ts`）里，
 * **从 `src/` 下任何 UI 文件（视图 / 组件 / store / 配置 / 路由）出发，沿 import 边走不到**的那个模块。
 *
 * ★ 第一版只查**直接**引用，并且显式跳过 `src/api/` 整个目录。两处都错了：
 *
 * 1. **跳过整个 `src/api/` 是错的。** 第一百零二批消解 `nodeHealth.ts` / `nodeHealthTimeline.ts`
 *    这对副本时，靠的是**API 层内部的委托**（`nodeHealth.ts` 改成 import 厚的那个）。
 *    直接引用口径下，委托方在 `src/api/` 里 ⇒ 被跳过 ⇒ **计数一点没降**，
 *    而事实上重复契约已经合并、模块已经可达。
 * 2. **只看直接引用也是错的。** 第九十九批的 `taskTypeCorrectionStats` 从 `./taskProfile` 导入
 *    `unwrapCorrectionStat` —— 那是**同结构复用**，本身不是 UI 入口；
 *    但如果它自己也变成可达的，那它**传递可达**（UI → taskTypeCorrectionStats → taskProfile）。
 *    ⇒ 第一版靠「排除整个 api 目录」来避开这个假阳性，
 *    **代价是把所有 API 层内部委托一并排除了**。
 *
 * ⇒ ⇒ ★★★ **正确口径是传递可达性**：种子是「被 UI 文件直接 import 的模块」，
 *   再沿 api 模块之间的 import 边做闭包。
 * ⇒ 这样 api 内部互相引用**不会**凭空产生可达性（种子不从那里起），
 *   而真正的委托**会**被算进去 —— 两个问题一次性解决，不需要例外条款。
 */

/** 当前已登记的孤儿模块（**只能减少**）。接线一个就删一行。 */
/**
 * ★★★★★★ **第二类登记：重复件**（2026-10-14，第一百二十批新增）。
 *
 * ## 为什么要有这一类
 *
 * 本清单一直隐含一个前提：**孤儿 = 「做完了但用户点不到」的功能缺口**。
 * ⇒ 棘轮逼着人接线，接完减一。
 *
 * ★★★ 但第一百二十批撞上一个前提**不成立**的条目：`boardOperational`。
 *   逐字核过的事实：
 *   · `boardOperational.ts` 与 `dashboardBoard.ts` 打的是**同一个端点**
 *     `GET /api/admin/dashboard/operational`；
 *   · `DashboardOpsView.vue` 从 **`@/api/dashboardBoard`** 导入 `fetchDashboardOperational`
 *     并已经在展示那两个芯片 ⇒ **功能早已可达**；
 *   · 两模块的导出**零交集**（前者 42 个、后者 28 个，各自全独有）
 *     ⇒ 它不是「少写了几行」，而是**同一契约的两套并行实现**。
 *
 * ⇒ 照老规矩给它新开一页 = **同一份数据两个页面**，那是装饰面，不是复制功能。
 * ⇒ 而直接合并（把 42 个独有导出并入在用模块）又会**改变在用路径的失败行为**
 *   （前者校验更严，畸形响应会从「能显示」变成「抛错」）——
 *   第一百零四批正是为此**明确推迟**过的。
 *
 * ## 这一类是**登记**，不是**免罪**
 *
 * ★★★ 登记成重复件**必须自证**，否则例外条款会变成万能借口
 *   （「我觉得它重复」就能把任何孤儿永久挂起）。所以下面两条判据：
 *   ① 它**确实仍在**孤儿集合里（不许悄悄变成可达）；
 *   ② 它**确实与某个已可达模块打同一个端点**（`duplicateIsProven`）。
 * ⇒ 两条任一不成立，那条登记立刻红。
 */
const KNOWN_DUPLICATES: readonly string[] = [
  // 与 `dashboardBoard.ts` 同端点（`/api/admin/dashboard/operational`），
  // 而后者已被 `DashboardOpsView.vue` 消费 ⇒ 功能已可达，本项是**重复件**不是缺口。
  // 消解条件（写下来是为了为了让「什么时候能删」有答案）：
  // 把这 42 个独有判据/常量逐条核实后并入 `dashboardBoard.ts`，删掉本模块。
  'boardOperational',
]

/**
 * 某项登记为重复件的**自证**：它打的端点 URL 与某个**已从 UI 可达**的模块相同。
 *
 * ★ 判别口径必须是「**已可达**的模块」，而不是「另一个孤儿」——
 *   两个都没人点的模块打同一个端点，那是两个缺口，不是一个重复件。
 */
function duplicateIsProven(name: string): boolean {
  const src = moduleSource(name)
  const urls = [...src.matchAll(/['`]\/api\/[a-z0-9/_{}$.-]+['`]/g)].map((m) => m[0].slice(1, -1))
  if (urls.length === 0) return false
  const seen = reachableFromUi()
  for (const other of seen) {
    if (other === name) continue
    const otherSrc = moduleSource(other)
    for (const u of urls) if (otherSrc.includes(`'${u}'`) || otherSrc.includes('`' + u + '`')) return true
  }
  return false
}

const KNOWN_ORPHANS: readonly string[] = [
  'dataLifecycleStorage',
  'nativeTransport',
  'sessionAnalyticsFilterOptions',
  'storageAndLogConfig',
  'storageMigrationState',
  'turnsFilterOptions',
]

/**
 * ★★★★★★ 这里**故意不写「当前 N 项」**（2026-10-08，第一百零七批）。
 *
 * ## 为什么删掉那个数字
 *
 * 它是本仓「现状盘点类数字必然腐烂」最贴身的一次实例，而且**腐烂了三批才被发现**：
 *
 * | 提交 | 清单真实项数 | 文件头注释写的 |
 * |---|---|---|
 * | `2442eddf3`（第一百零三批） | 26 | **27** ← 错误从这里开始 |
 * | `1c198d51a` / `1e7617002`（批 104） | 26 | **27** |
 * | `9c4bcee7e`（第一百零五批） | 25 | **26** |
 * | `74dead128`（第一百零六批） | 24 | **25** |
 *
 * 每一批都在做「上一批的数 −1」这个**推理**，**从来没重新数过** ——
 * 于是第一次的手滑（批 103 我自己写错了 1）被逐批放大，而**没有任何判据会红**：
 * 清单逐项对拍的那条断言（`toEqual([...KNOWN_ORPHANS])`）只管集合内容，
 * **不管长度**；而注释里的数字**压根不是判据**，它只是给人读的。
 * ★★ 于是「有人读了这个数字」变成了「有人相信这个数字」——
 *   没有人读，就没有人发现它是错的；没有判据，就没有人在乎。
 *
 * ⇒ ★★★ **处置不是把数字改对，是把数字删掉**：
 *   - 改对 = 把同一个错误推迟到下一批腐烂（而且下一个人又会照抄上一批的数字）；
 *   - 删掉 = 让 `KNOWN_ORPHANS` 这份清单成为**唯一**事实源，
 *     想知道有几项就跑 `currentOrphans()` / 数清单项数，**不要引用散文里的数字**。
 *
 * ★ 上面文件头里那句「30 个」是第一百批当时的**实测记录**，它带时刻、
 *   属于「历史陈述」而非「现状盘点」，所以保留 ——
 *   **引用它时要连时刻一起引用**（「第一百批那轮 30 个」）。
 */

const ROOT = join(process.cwd(), 'src')
const API_DIR = join(ROOT, 'api')

function apiModules(): string[] {
  return readdirSync(API_DIR)
    .filter((f) => f.endsWith('.ts') && !f.endsWith('.test.ts') && !f.endsWith('.spec.ts'))
    .map((f) => f.slice(0, -3))
    .sort()
}

/** 把 src 下除 src/api/ 之外的所有源码拼成一个字符串。 */
function uiSourceBlob(): string {
  const out: string[] = []
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) {
        if (full === API_DIR) continue // 种子必须从 UI 侧起，api 内部不产生可达性
        walk(full)
        continue
      }
      if (!/\.(ts|vue)$/.test(entry.name)) continue
      if (entry.name.endsWith('.spec.ts')) continue
      out.push(readFileSync(full, 'utf8'))
    }
  }
  walk(ROOT)
  return out.join('\n')
}

/** 某个模块自己的源码（用于找它 import 了谁）。 */
function moduleSource(name: string): string {
  return readFileSync(join(API_DIR, `${name}.ts`), 'utf8')
}

function importsOf(blob: string): string[] {
  // ★ 两类 import 形式都必须认：
  //   ① `@/api/<name>` / `…/api/<name>` —— UI 侧与少数 api 侧用这种；
  //   ② `./<name>` —— **api 模块之间**用的就是这种相对形式。
  //   ★★ 第一版只写了 ① ⇒ `nodeHealth.ts` 对 `./nodeHealthTimeline` 的委托**完全不可见**
  //   ⇒ 计数不降。**量具自己也有盲区，而盲区恰好落在本批要修的那件事上。**
  const abs = [...blob.matchAll(/from\s+['"][^'"]*api\/([A-Za-z0-9_]+)['"]/g)].map((m) => m[1] as string)
  const rel = [...blob.matchAll(/from\s+['"]\.\/(?![.])([A-Za-z0-9_]+)['"]/g)].map((m) => m[1] as string)
  return [...abs, ...rel]
}

/**
 * ★ 从 UI 出发做传递闭包。
 *   注意 `importsOf` 用的是**相对宽松**的 `…api/<name>` 模式，
 *   所以 `./foo`（api 内部引用）匹配不到 —— 这正是我们要的：
 *   api 内部的边只能**传递**可达性，不能**凭空**产生可达性。
 *   ⇒ 而 uiSourceBlob 里出现的都是 `@/api/<name>` 形式，匹配得到。
 */
function reachableFromUi(): Set<string> {
  const all = new Set(apiModules())
  const seen = new Set<string>()
  const queue = importsOf(uiSourceBlob()).filter((n) => all.has(n))
  while (queue.length > 0) {
    const name = queue.shift() as string
    if (seen.has(name)) continue
    seen.add(name)
    for (const dep of importsOf(moduleSource(name))) {
      if (all.has(dep) && !seen.has(dep)) queue.push(dep)
    }
  }
  return seen
}

function currentOrphans(): string[] {
  const seen = reachableFromUi()
  return apiModules().filter((m) => !seen.has(m))
}

describe('孤儿棘轮', () => {
  it('★ 作用面自证：扫描到的 api 模块数不为 0（塌缩到 0 或 1 必须红）', () => {
    expect(apiModules().length).toBeGreaterThan(50)
  })

  it('★ 作用面自证：UI 源码体量不为 0（万一拼接失败必须红）', () => {
    expect(uiSourceBlob().length).toBeGreaterThan(10000)
  })

  it('★ 当前孤儿集合 = 真缺口 ∪ 重复件（第二类登记，见 KNOWN_DUPLICATES）', () => {
    // ★ 两份清单都排进数组再比 —— 两边都已在源码里按字母序书写。
    expect([...currentOrphans()].sort()).toEqual([...KNOWN_ORPHANS, ...KNOWN_DUPLICATES].sort())
  })

  it('★★ 每一项登记的重复件**确实与某个已可达模块打同一个端点**（否则例外条款是免罪）', () => {
    // ★★★ 这条是第二类登记的**承重判据**：没有它，「重复件」就成了万能借口。
    expect(KNOWN_DUPLICATES.length).toBeGreaterThan(0)
    for (const name of KNOWN_DUPLICATES) {
      expect(currentOrphans(), `${name} 已不在孤儿集合里 ⇒ 这条登记该删了`).toContain(name)
      expect(duplicateIsProven(name), `${name} 没有已可达模块与它同端点 ⇒ 它是功能缺口，不是重复件`).toBe(true)
    }
  })

  it('★★ 重复件**不进**真缺口清单（两份清单不得重叠）', () => {
    for (const name of KNOWN_DUPLICATES) expect(KNOWN_ORPHANS).not.toContain(name)
  })

  it('★★ 已消解的重复件要**移出**登记（boardOperational 被合并后这条会红）', () => {
    // ★ 消解条件写在 KNOWN_DUPLICATES 的注释里；这条逼着人手工删那一行 ——
    //   与「接线一个孤儿就删一行」同一套纪律，只是对象换了。
    for (const name of KNOWN_DUPLICATES) expect(reachableFromUi().has(name)).toBe(false)
  })

  it('★ 登记清单本身无重复（防止清单写错掩盖变化）', () => {
    expect(new Set(KNOWN_ORPHANS).size).toBe(KNOWN_ORPHANS.length)
  })

  it('★ 登记的每一项都真的是 api 模块（防止写了已不存在的名字）', () => {
    const modules = new Set(apiModules())
    for (const m of KNOWN_ORPHANS) expect(modules.has(m)).toBe(true)
  })

  it('★ 已接线的哨兵：taskProfile / taskTypeCorrectionStats 不在孤儿里', () => {
    // ★ 这两条是本批接线的目标。若有人把抽屉席或路由删了，这条立刻红。
    const orphans = currentOrphans()
    expect(orphans).not.toContain('taskProfile')
    expect(orphans).not.toContain('taskTypeCorrectionStats')
  })

  it('★ 已接线的哨兵：routingPolicy 不在孤儿里（第一百零一批）', () => {
    // ★ 第一百零一批把路由策略配置面接上了 ⇒ 棘轮清单同步删掉那一行。
    expect(currentOrphans()).not.toContain('routingPolicy')
  })

  it('★★ 已接线的哨兵：nodeHealthTimeline 传递可达（第一百零二批）', () => {
    // ★ 它是被**委托**接上的：NodeHealthView → nodeHealth.ts → ./nodeHealthTimeline
    //   中间那一跳在 src/api/ 里且用相对导入 —— 两种口径都测得出这一点。
    expect(currentOrphans()).not.toContain('nodeHealthTimeline')
  })

  it('★★ 已接线的哨兵：workTypes 不在孤儿里（第一百零三批）', () => {
    // ★ 工作类型配置面接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   ★ 这一族是 h.superAdmin（admin/handler.go:1434），所以席的权限门控
    //   由 AppDrawer.spec.ts 的白名单把守，两条判据合起来才覆盖「接上了且挂对了档」。
    expect(currentOrphans()).not.toContain('workTypes')
  })

  it('★ 已接线的哨兵：probeTriStateTasks 不在孤儿里（第一百零五批）', () => {
    // ★ 凭据探测三态队列接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **admin 档**（admin/probe_dashboard.go:1907 的 adminWrap），
    //   所以它的席**不设** requiresRole ⇒ AppDrawer.spec.ts 的白名单里
    //   **不该**出现 probe-queue。两条判据合起来才覆盖「接上了且档位挂对了」。
    expect(currentOrphans()).not.toContain('probeTriStateTasks')
  })

  it('★ 已接线的哨兵：selfCheck 不在孤儿里（第一百零六批）', () => {
    // ★ self-check 席是 **admin 档** ⇒ 不进 AppDrawer.spec.ts 的白名单，
    //   跨租户门控在 SelfCheckView 内部按段落做。
    expect(currentOrphans()).not.toContain('selfCheck')
  })

  it('★★ 已接线的哨兵：connectionRegistry 不在孤儿里（第一百零七批）', () => {
    // ★ 流式连接注册台接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **admin 档**（admin/handler.go:1024-1025 两条都是 `admin(...)`），
    //   所以席**不设** requiresRole ⇒ AppDrawer.spec.ts 的白名单里
    //   **不该**出现 connection-registry（与 probe-queue 同一条纪律）。
    // ★ 这条负断言有牙的前提是上面那条 `toEqual([...KNOWN_ORPHANS])` 仍在把守：
    //   把本模块改回孤儿时集合会不等 ⇒ 那条先红 ⇒ 哨兵不是独自承担。
    expect(currentOrphans()).not.toContain('connectionRegistry')
  })

  it('★★ 已接线的哨兵：usageTrendSeries 不在孤儿里（第一百零八批）', () => {
    // ★ 用量趋势线接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **admin 档**（admin/handler.go:1267 的 h.admin(h.HandleUsageAdmin)），
    //   席**不设** requiresRole ⇒ AppDrawer.spec.ts 的白名单里不该出现 usage-trend-series。
    // ★ 这一族的租户口径是本仓第四种且是**安全**的那种：statsTenantScope
    //   （admin/stats.go:87-97）对非 super_admin 强制用 auth.TenantID 并忽略 tenant_id
    //   ⇒ tenant_admin 天然隔离 ⇒ 视图内部**不需要**任何角色分支。
    expect(currentOrphans()).not.toContain('usageTrendSeries')
  })

  it('★★ 已接线的哨兵：formatAnomalies 不在孤儿里（第一百零九批）', () => {
    // ★ 响应格式异常接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **superAdmin 档**（admin/handler.go:913-914）⇒ 席**要** requiresRole，
    //   所以 `AppDrawer.spec.ts` 的排序白名单里**必须**出现 format-anomalies。
    //   ⇒ 两条判据合起来才覆盖「接上了」+「档位挂对了」；
    //     只加抽屉席不改正那份白名单，「tenant_admin 少掉的只有这些」那条立刻红。
    expect(currentOrphans()).not.toContain('formatAnomalies')
  })

  it('★★ 已接线的哨兵：modelIntegrityDrift 不在孤儿里（第一百一十批）', () => {
    // ★ 指纹漂移接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **superAdmin 档**（admin/handler.go:942-943，整个前缀都是 h.superAdmin）
    //   ⇒ 席**要** requiresRole，所以 `AppDrawer.spec.ts` 的排序白名单里
    //   **必须**出现 model-integrity-drift。
    //   ⇒ 两条判据合起来才覆盖「接上了」+「档位挂对了」；
    //     只加抽屉席不改正那份白名单，「tenant_admin 少掉的只有这些」那条立刻红。
    // ★ 顺带记下一条**同前缀不同档**的事实：`integrity` 席（summary/events/resolve）
    //   与本席共用 `/api/admin/model-integrity/` 前缀却是**两张席** ——
    //   所以权限判据必须落到可执行注册处，不能按前缀判。
    expect(currentOrphans()).not.toContain('modelIntegrityDrift')
  })

  it('★★ 已接线的哨兵：reportRollup 不在孤儿里（第一百一十一批）', () => {
    // ★ 对账报表接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **superAdmin 档**（admin/handler.go:1086，整个 `/api/admin/report-rollup/`
    //   前缀带尾斜杠都是 h.superAdmin）⇒ 席**要** requiresRole，
    //   所以 `AppDrawer.spec.ts` 的排序白名单里**必须**出现 report-rollup。
    //   ⇒ 两条判据合起来才覆盖「接上了」+「档位挂对了」。
    // ★⚠️ 另记一条**命名陷阱**：`reconciliation` 席走的是 `costReconciliation`
    //   （费用对账），本席走 `reportRollup`（对账报表）——两张席都叫「对账」，
    //   接错模块这两条判据**都不会红** ⇒ 靠的是 nav 标题与哨兵注释里的显式区分。
    expect(currentOrphans()).not.toContain('reportRollup')
  })

  it('★★ 已接线的哨兵：dispatchJournal 不在孤儿里（第一百一十二批）', () => {
    // ★ 单请求调度轨迹接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **admin 档**（可执行注册处 cmd/gateway/main.go:7143-7144 传的是
    //   `requestJourneyWrapAdmin` → `admin.AdminMiddleware`，**不检查 role**）
    //   ⇒ 席**不设** requiresRole ⇒ `AppDrawer.spec.ts` 的 superAdmin 白名单里
    //   **不该**出现 dispatch-journal（与 probe-queue / connection-registry 同一条纪律）。
    expect(currentOrphans()).not.toContain('dispatchJournal')
  })

  it('★★ 已接线的哨兵：opsOverview 不在孤儿里（第一百一十七批）', () => {
    // ★ 运维总览接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   权限成因是**第三类**：注册处 `admin/handler.go:1091` 是 `admin(...)`
    //   （`AdminMiddleware` 只认证不检 role ⇒ 后端 tenant_admin **可读**），
    //   但 `handleOpsOverview` 自己不检 role、11 条子查询**全是全局表**
    //   ⇒ 服务端**完全没有租户维度** ⇒ 只能过度限制成 `requiresRole: 'super_admin'`，
    //   所以 `AppDrawer.spec.ts` 的排序白名单里**必须**出现 ops-overview。
    // ⇒ 两条判据合起来才覆盖「接上了」+「档位挂对了」；
    //   只加抽屉席不改正那份白名单，「tenant_admin 少掉的只有这些」那条立刻红。
    expect(currentOrphans()).not.toContain('opsOverview')
  })

  it('★★ 已接线的哨兵：requestActions 不在孤儿里（第一百一十八批）', () => {
    // ★ 请求动作时间线接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族归**第三类**：注册处 `admin/handler.go:1045` 是 `admin(...)`
    //   （`AdminMiddleware` 只认证不检 role ⇒ 后端 tenant_admin **可读**），
    //   但 `handleRequestActions` **完全不按租户隔离** —— 唯一过滤是 URL 里的
    //   `request_id`，而它由 crypto/rand 生成、**不含租户信息**；
    //   读同一个 store 的 `/api/admin/live-stream` 却**有**租户过滤
    //   ⇒ 后端不一致 ⇒ 只能过度限制成 `requiresRole: 'super_admin'`，
    //   所以 `AppDrawer.spec.ts` 的排序白名单里**必须**出现 request-actions。
    // ⇒ 两条判据合起来才覆盖「接上了」+「档位挂对了」。
    expect(currentOrphans()).not.toContain('requestActions')
  })

  it('★★ 已接线的哨兵：v1DataHorizon 不在孤儿里（第一百一十九批）', () => {
    // ★ v1 数据地平线接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **admin 档**（`admin/handler.go:1109` 的 `admin(...)`，AdminMiddleware
    //   只认证不检 role ⇒ 后端 tenant_admin 可读）
    //   ⇒ 席**不设** requiresRole ⇒ `AppDrawer.spec.ts` 的 superAdmin 白名单里
    //   **不该**出现 v1-data-horizon（与 probe-queue / connection-registry 同一条纪律）。
    // ★★ 与批 115/117/118 三族**相反**而那三族并没错：本族虽无租户维度，
    //   但载荷是**平台级全局事实**（七个键无一带租户）⇒ 过度限制**不适用**。
    //   ⇒ 两条判据合起来才覆盖「接上了」+「档位挂对了」。
    expect(currentOrphans()).not.toContain('v1DataHorizon')
  })

  it('★★★ 反向护栏：api 内部的互相引用不得凭空产生可达性', () => {
    // ★ 种子只从 UI 侧起。若把 api 内部互相 import 也算成种子，
    //   第九十九批那个假阳性就会回来（taskTypeCorrectionStats 从 ./taskProfile 导入
    //   unwrapCorrectionStat 会被当成「taskProfile 已接上」）。
    const orphans = currentOrphans()
    expect(orphans).toContain('turnsFilterOptions')
    expect(orphans).toContain('storageMigrationState')
  })
})
