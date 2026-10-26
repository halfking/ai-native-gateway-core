import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'

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
  // ★ 批 149 收口：原 `boardOperational`（与 `dashboardBoard.ts` 同打 `/api/admin/dashboard/operational`），
  //   全仓 27 处外部 grep 0 命中（独有 42 个常量/判据自始未在生产路径被消费），
  //   仅 spec 自身引用 ⇒ 直接删除本模块 + spec，登记清单同步清空。
  // ⇒ 消解条件 = `web-mobile/src/api/boardOperational*.ts` 在工作树不存在 + 本清单不含这一行。
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

/**
 * ★★★★★★ **第三类登记：非页面型基础设施**（2026-10-15，第一百二十六批新增）。
 *
 * ## 为什么需要第三类
 *
 * 前两类隐含同一个前提：**孤儿 = 「做完了但用户点不到」的功能缺口**。
 * ⇒ 第一百二十六批撞上一个**前提不成立**的条目：`nativeTransport`。
 *
 * ### 逐字核过的事实
 *
 * ① **它自己没有任何端点。** 全文件 `'/api/'` 字面量计数 = **0**
 *    （导出是 `createNativeTransport` / `installNativeTransport` /
 *      `probeNativeTransport` / `reasonPhrase` / `NativeTransportError`，
 *      **没有一个是取数函数**）⇒ 它**不承载任何网关功能**。
 * ② ⇒ ★★ **「给它开一页」这个动作本身不成立** ——
 *    没有端点就没有数据，没有数据就没有可展示的面。
 *    开出来的页只能是装饰，而装饰面不是「把功能复制到移动端」。
 * ③ **它的能力在当前产品状态下不可能被执行**（自撤销的条件，见下面的自证）：
 *    - `web-mobile/package.json` 的依赖只有 `pinia` / `qrcode` / `vue` / `vue-router`
 *      ⇒ **连 `@capacitor/*` 都没装**，而五道门第 ① 道要的就是 `window.Capacitor`
 *      ⇒ 门① **恒不通过**；
 *    - 无 `capacitor.config.*`、无 `android/`、无 `ios/`；
 *    - `setGatewayBaseUrl` 在**生产代码里零调用**（只在 `nativeTransport.test.ts` 里调）
 *      ⇒ `gatewayBase` 恒为 `undefined` ⇒ 门⑤ **恒不通过**。
 * ④ **全仓无生产调用点** —— `installNativeTransport` / `probeNativeTransport` /
 *    `createNativeTransport` 在 `src/` 里除本文件与自己的测试外**零引用**。
 *
 * ### 与「重复件」的区别
 *
 * 重复件是**功能已可达、只是有两套实现**；本类是**功能压根不存在**。
 * ⇒ 两者都需要「登记 + 自证」，但登记时要说清消解条件不同。
 *
 * ## 这一类是**登记 + 自撤销**，不是**永久免罪**
 *
 * ★★★ 下面 `infrastructureIsProven()` 的两条腿**任一不成立，这条登记立刻红** ——
 *   也就是说：**一旦有人把壳装上、或在生产代码里注入了基址、或给它加了端点**，
 *   这条登记就会自己作废，逼着人重新决策（那时该做的是**接线**，不是继续挂起）。
 * ⇒ ★★ 这正是它与「万能借口」的区别：登记的前提被钉死在**可观测的事实**上，
 *   而不是钉在某个人的判断上。
 *
 * ### 消解条件
 *
 * 壳安装 + 在 bootstrap 里 `setGatewayBaseUrl(...)` + `void installNativeTransport()`
 * 之后，本模块**传递可达**（`main.ts` → 本模块），这一行就该从本清单删掉。
 */
const KNOWN_INFRASTRUCTURE: readonly string[] = [
  // UI规范 04 §7.2 ④「壳内 `CapacitorHttp` 反代」的前端侧安装器。
  // 零端点 + 零生产调用点 + 壳未安装 ⇒ **没有可复制的功能，也没有可开的页**。
  'nativeTransport',
]

/**
 * 某项登记为「非页面型基础设施」的**自证**：两条腿都要成立。
 *
 * **腿①（没有端点 ⇒ 没有 UI 入口可言）**：全文件 `'/api/'` 字面量计数 = 0。
 * **腿②（能力当前跑不起来）**：壳未安装 —— 即
 *   `package.json` 无任何 `@capacitor/*` 依赖，且 `setGatewayBaseUrl`
 *   在 `src/` 的**非测试**代码里零调用。
 *
 * ★ 腿②之所以要单独钉，而不是只看腿①：
 *   一个「零端点但已在别处被消费」的模块（`transport.ts` / `system.ts` / `gatewaySites.ts`
 *   都是这种）是**可达**的，本来就不在孤儿清单里 ——
 *   ⇒ 两条腿合起来才**唯一**命中 `nativeTransport`。
 */
function infrastructureIsProven(name: string): boolean {
  return infrastructureIsProvenGiven(moduleSource(name), shellIsNotInstalled())
}

/**
 * ★★★ 与 `shellNotInstalledGiven` 同理**取参数**（第一百二十八批）。
 *
 * ⇒ 起因：负控⑤原先拿 `sessionAnalyticsFilterOptions` 当「有端点但不可达」的样本，
 *   而那个模块在本批被接线 ⇒ 样本**当场失效**（负控会变成僵尸断言，全绿但没测任何东西）。
 *   ⇒ ★★ 与 `boardOperational` 那类仓库里的活样本相比，
 *   **喂字符串**的样本不会随接线腐烂，也不会让下一个人再换一轮名字。
 *
 * @param src      被检模块的源码（腿①：数 `/api/` 端点数）
 * @param noShell  壳是否未安装（腿②，由调用方从依赖清单现读）
 */
function infrastructureIsProvenGiven(src: string, noShell: boolean): boolean {
  const endpointCount = (src.match(/['`]\/api\//g) ?? []).length
  if (endpointCount !== 0) return false // 腿①：有端点 ⇒ 是功能缺口，该开页
  return noShell
}

/**
 * 壳是否**未安装** —— 腿②的判别式。
 *
 * ★ 判别条件是**可观测的仓库事实**（依赖清单 / 配置文件 / 基址注入调用点），
 *   不是「我记得还没装」⇒ 壳装上的那一刻它自动变 false ⇒ 登记作废。
 */
function shellIsNotInstalled(): boolean {
  const pkg = JSON.parse(readFileSync(join(process.cwd(), 'package.json'), 'utf8')) as {
    dependencies?: Record<string, string>
    devDependencies?: Record<string, string>
  }
  return shellNotInstalledGiven(pkg.dependencies ?? {}, pkg.devDependencies ?? {}, currentProdBlob())
}

function currentProdBlob(): string {
  return uiSourceBlob() + readFileSync(join(API_DIR, 'client.ts'), 'utf8')
}

/**
 * ★★★ 判别式**取参数**，而不是自己去读仓库。
 *
 * ⇒ ★★★ 这不是洁癖：本批的变异台查出「把依赖判据里的
 *   `devDependencies` 去掉」**不会让任何判据变红** ——
 *   因为今天两个分支里都没有 `@capacitor/*`，所以**两条腿都还成立**。
 *   ⇒ ★★ 「削弱断言」型变异天生无法被同一批判据抓住，
 *     **唯一的治法是给判据配负控**：喂它一个**反例输入**，要求它返回 `false`。
 *   ⇒ 参数化是负控的前提（不然喂不进去）。
 */
function shellNotInstalledGiven(
  dependencies: Record<string, string>,
  devDependencies: Record<string, string>,
  prod: string,
): boolean {
  const deps = { ...dependencies, ...devDependencies }
  if (Object.keys(deps).some((k) => k.startsWith('@capacitor/'))) return false
  // ④ 的门⑤要 `gatewayBase`，而它只能由 setGatewayBaseUrl 注入。
  // ⇒ 只要生产代码里还没人注入它，门⑤ 就恒不通过。
  return !/setGatewayBaseUrl\(/.test(prod)
}

// ★★★ 第一百二十八批：`KNOWN_ORPHANS`（真缺口）**已清空** —— 真缺口 2 → 0。
//
//   最后一项 `sessionAnalyticsFilterOptions` 在本批接进 UI（视图
//   `SessionAnalyticsFilterOptionsView.vue` + 路由 `/session-analytics-filter-options`
//   + 抽屉席 `session-analytics-filter-options`）。
//
//   ⚠️ 空清单不是「可以删掉这个门」：这条判据（当前孤儿集合 = 三类登记的并）
//   是**承重**的 —— 它逐个核对**全部**孤儿。清单空了，它照样守着：
//   任何一个 api 模块只要还没接进 UI，就既不在清单里、又出现在 `currentOrphans()` 里
//   ⇒ 这条立刻红。⇒ ★ 所以**留一个空数组**，不要删变量，也不要改成「无孤儿则跳过」。
const KNOWN_ORPHANS: readonly string[] = [
  // ★ 故意为空：当前没有「有 UI 入口才能算完成」的真缺口。
  //   （`nativeTransport` 在 KNOWN_INFRASTRUCTURE —— 零端点的非页面型基础设施。
  //   原 KNOWN_DUPLICATES 那条 `boardOperational` 在批 149 已合并删模块、清出本登记。）
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

// ══════════════════════════════════════════════════════════════════════════
// ★★★ 第一百三十三批：给「孤儿」这个量具本身加自证。
//
// 本判据把「可达」定义为 **import 可达**（UI 侧 import 图的传递闭包）。
// 而流水线的完成口径是「**可达 UI 入口**」—— 这两者**不是一回事**，
// 差别只有一处：**uiSourceBlob 把 src 下所有文件都当种子**，
// 所以一个 api 模块只要被**任何**文件 import 就算「可达」，
// **哪怕那个文件自己从没被任何视图或壳层挂载**（死面板）。
//
// ⇒ 这个代理口径目前是**对的**，但它是**碰巧对**的 ——
//   和第 132 批 `router/index.ts` 上 `stripComments` 空操作一个味道。
// ⇒ 下面三条把它钉住，让它从「碰巧对」变成「被看着」。
// ══════════════════════════════════════════════════════════════════════════

/**
 * ★ 第一百三十二批修好的剥离顺序：**先按行删 `//`，再删块注释**。
 *   顺序反了的话，`appNav.ts` 行注释里的 URL（`/api/maas/**`）会被当成
 *   块注释起点，把大段真实代码当注释删掉。
 */
function stripComments(src: string): string {
  return src.replace(/^\s*\/\/.*$/gm, '').replace(/\/\*[\s\S]*?\*\//g, '')
}

/** 剥掉注释后的 UI 源码拼块（用于「注释不得伪造可达性」那一条）。 */
function uiSourceBlobStripped(): string {
  return stripComments(uiSourceBlob())
}

/** 每次调用取新快照，不跨用例缓存；调用方可在单个同步判据内复用。 */
function uiSources(): Map<string, string> {
  const out = new Map<string, string>()
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name)
      if (entry.isDirectory()) {
        if (full === API_DIR) continue
        walk(full)
        continue
      }
      if (!/\.(ts|vue)$/.test(entry.name)) continue
      if (entry.name.endsWith('.spec.ts')) continue
      out.set(relative(ROOT, full), readFileSync(full, 'utf8'))
    }
  }
  walk(ROOT)
  return out
}

/** 直接从 UI 引到 API 的反向索引：每份源码只解析一次。 */
function uiImporterIndex(sources: ReadonlyMap<string, string>): Map<string, string[]> {
  const index = new Map<string, string[]>()
  for (const [file, source] of sources) {
    for (const name of new Set(importsOf(source))) {
      const files = index.get(name) ?? []
      files.push(file); index.set(name, files)
    }
  }
  return index
}

/** 未提供快照时保持原调用口径；批量查询应复用同一个局部索引。 */
function uiImportersOf(name: string, index = uiImporterIndex(uiSources())): string[] {
  return index.get(name) ?? []
}




describe('孤儿棘轮', () => {
  it('★ 作用面自证：扫描到的 api 模块数不为 0（塌缩到 0 或 1 必须红）', () => {
    expect(apiModules().length).toBeGreaterThan(50)
  })

  it('★ 作用面自证：UI 源码体量不为 0（万一拼接失败必须红）', () => {
    expect(uiSourceBlob().length).toBeGreaterThan(10000)
  })

  it('★ 当前孤儿集合 = 真缺口 ∪ 重复件 ∪ 非页面型基础设施（三类登记）', () => {
    // ★★★ 三份清单都排进数组再比 —— 两边都已在源码里按字母序书写。
    //   ★ 第三类（第一百二十六批新增）不是为了给清单减项，
    //     而是登记一类**「没有可复制的功能」**的条目（`nativeTransport` 零端点）。
    //   ⇒ 它必须与「真缺口」严格区分：真缺口要开页，本类开了也不成立。
    expect([...currentOrphans()].sort()).toEqual(
      [...KNOWN_ORPHANS, ...KNOWN_DUPLICATES, ...KNOWN_INFRASTRUCTURE].sort(),
    )
  })

  it('★★★ 非页面型基础设施**确实零端点**（否则它就是功能缺口，该开页而不是登记）', () => {
    // ★★★ 这是第三类登记的**承重判据**，没有它，「非页面型」就成了万能借口。
    expect(KNOWN_INFRASTRUCTURE.length).toBeGreaterThan(0)
    for (const name of KNOWN_INFRASTRUCTURE) {
      expect(currentOrphans(), `${name} 已不在孤儿集合里 ⇒ 这条登记该删了`).toContain(name)
      const urls = [...moduleSource(name).matchAll(/['`]\/api\//g)]
      // ★ `toHaveLength` 只能用在有 `.length` 的对象上；这里是数字 ⇒ 用 `toBe(0)`
      expect(urls.length, `${name} 有 /api/ 端点 ⇒ 它是功能缺口，不该登记为基础设施`).toBe(0)
      expect(
        infrastructureIsProven(name),
        `${name} 的登记前提不成立了（要么有了端点，要么壳已安装/基址已注入）⇒ 重新决策`,
      ).toBe(true)
    }
  })

  it('★★★★ 自证判据的**负控**：喂反例输入必须返回 false（否则它是恒真的）', () => {
    // ★★★★ 本批变异台的核心发现：「削弱断言」型变异不会被同一批判据抓住
    //   （把精确判据换成宽松判据，事实仍为真 ⇒ 全绿）。
    //   ⇒ 唯一治法是**负控**：构造反例输入，要求判据说「不成立」。
    //
    // 负控①：壳装了（依赖里出现 @capacitor/*）⇒ 必须不成立
    expect(shellNotInstalledGiven({ '@capacitor/core': '8' }, {}, '')).toBe(false)
    // 负控②：壳装成 **devDependency** 也必须被发现（这条正是 S10 变异删掉的那半）
    expect(shellNotInstalledGiven({}, { '@capacitor/android': '8' }, '')).toBe(false)
    // 负控③：基址已注入 ⇒ 必须不成立
    expect(shellNotInstalledGiven({}, {}, 'setGatewayBaseUrl("https://gw")')).toBe(false)
    // 负控④：普通依赖不得被误判成壳
    expect(shellNotInstalledGiven({ vue: '3', pinia: '3' }, { vitest: '4' }, '')).toBe(true)
    // 负控⑤：`infrastructureIsProven` 对**有端点**的模块必须返回 false
    //   （这条让 S3「判据恒真」这类变异有牙：恒真会让它对任何模块都返回 true）
    // ★ 第一百二十八批：原先这里喂 `sessionAnalyticsFilterOptions`，
    //   而它在本批被接线 ⇒ 样本当场失效。改为**直接喂字符串**，
    //   样本不再依赖仓库里「恰好仍未接线」的模块。
    expect(infrastructureIsProvenGiven("const P = '/api/admin/x'", false)).toBe(false)
    // ★ 同一函数的正控：零端点 + 壳未安装 ⇒ 必须成立（否则负控⑤就是恒假）
    expect(infrastructureIsProvenGiven('const N = 1', true)).toBe(true)
    // ★ 腿②的反向护栏：零端点但壳已安装 ⇒ 必须不成立
    expect(infrastructureIsProvenGiven('const N = 1', false)).toBe(false)
    // ★ 与仓库现状的一致性（防止参数化把真实调用链改坏）：
    //   对每个**真实**的基础设施条目，两种算法必须给出同一个答案。
    for (const name of KNOWN_INFRASTRUCTURE) {
      expect(infrastructureIsProvenGiven(moduleSource(name), shellIsNotInstalled())).toBe(
        infrastructureIsProven(name),
      )
    }
  })

  it('★★★★ 「零端点 + 不可达」那条**能分辨**而不是恒成立（负控）', () => {
    // ★ 与「削弱断言」型变异对抗：断言一个**故意写错**的集合必须不匹配。
    const seen = reachableFromUi()
    const zeroEndpointOrphans = apiModules().filter(
      (m) => (moduleSource(m).match(/['`]\/api\//g) ?? []).length === 0 && !seen.has(m),
    )
    expect(zeroEndpointOrphans.sort()).not.toEqual([...KNOWN_INFRASTRUCTURE, 'transport'].sort())
    expect(zeroEndpointOrphans.sort()).not.toEqual([...KNOWN_INFRASTRUCTURE, 'system'].sort())
    expect(zeroEndpointOrphans.sort()).not.toEqual([])
  })

  it('★★★★★ 负控：承重那条判据**必须逐字含集合相等断言**（专打 M23，负控的负控）', () => {
    // ★★★★★ 第一百二十八批变异台：M23 把
    //   `expect([...currentOrphans()].sort()).toEqual([...KNOWN_ORPHANS, ...].sort())`
    //   换成 `expect([...currentOrphans()].sort().length).toBeGreaterThanOrEqual(0)`
    //   ⇒ **事实仍为真**（真孤儿确实 ≥0 个）⇒ 整条**全绿**。
    //   ⇒ 与 M23 同型的还有「上一条能分辨负控」：它复算的是
    //   `reachableFromUi()`，**没有**检查承重判据本身有没有被换成恒真式。
    //
    // ⇒ ★★ 「削弱断言」型变异的第二层：上一批的负控只覆盖了
    //   「判据函数返回错值」，**没覆盖「判据被换成恒真表达式」**。
    //   ⇒ 治法是**钉源码形态**：承重那条必须逐字出现 `toEqual(` + 三份清单的展开。
    const self = readFileSync(join(__dirname, 'orphanLedger.spec.ts'), 'utf8')
    // ★★ 三份清单是**跨两行**书写的（`toEqual(` 后面换行才展开），
    //   所以要连着取 3 行，不能只看命中 `toEqual(` 的那一行。
    const lines = self.split('\n')
    const at = lines.findIndex((l) => l.includes('currentOrphans()') && l.includes('toEqual('))
    expect(at, '承重判据里的集合相等断言不见了 ⇒ 它可能被换成了恒真式').toBeGreaterThan(-1)
    const snippet = lines.slice(at, at + 3).join('\n')
    expect(snippet).toContain('currentOrphans()')
    expect(snippet).toContain('toEqual(')
    // ★★ 三份清单必须**都**出现（少一份就等于少覆盖一类登记）
    for (const name of ['KNOWN_ORPHANS', 'KNOWN_DUPLICATES', 'KNOWN_INFRASTRUCTURE']) {
      expect(snippet, `承重判据少了一类登记：${name}`).toContain(name)
    }
    // ★ 反向护栏：承重那条**不得**退化成「长度恒真」这类形式。
    // ⚠️⚠️ 只扫**断言行**（以 expect 开头），不能扫整段 ——
    //   这段文字里就包含「toBeGreaterThan(0)」这个字面量，
    //   扫全文会**自指误红**（本批真踩到了）。
    // ⚠️ 窗口必须停在**本用例闭合**处，不能按固定字符数切 ——
    //   固定 900 字符会把**下一条**用例的断言也扫进来，
    //   而下一条正好有 `toBeGreaterThan(0)` ⇒ 自指误红（本批真踩到了）。
    const start = self.indexOf('当前孤儿集合 = 真缺口')
    const end = self.indexOf('\n  })', start)
    expect(end, '没找到承重用例的闭合位置').toBeGreaterThan(start)
    const weightLines = self
      .slice(start, end)
      .split('\n')
      .filter((l) => l.trim().startsWith('expect('))
    expect(weightLines.length, '没扫到承重判据的断言行').toBeGreaterThan(0)
    for (const l of weightLines) {
      expect(l, '承重判据里出现了恒真式断言').not.toMatch(/toBeGreaterThan(?:OrEqual)?\(0\)/)
      expect(l, '承重判据里出现了只比长度、不比集合的断言').not.toMatch(/\.length\)\.toBe/)
    }
  })

  it('★★★★ 负控必须**钉住真判据的源码形态**，不能只复算（变异 S7 的教训）', () => {
    // ★★★★ 本条是本批最贵的教训：
    //   S7 把真判据里那行 `toEqual([...KNOWN_INFRASTRUCTURE].sort())`
    //   换成 `toBeGreaterThanOrEqual(1)` ⇒ **事实仍为真** ⇒ 全绿，
    //   而上面那条负控（它自己重新算了一遍集合）**照常通过**
    //   ⇒ 负控与真判据成了**两份断言**，真判据被削弱时无人报警。
    //
    // ⇒ ★★★★ **钉子必须写成「带转义的正则字面量」** —— 这是本批最贵的一课：
    //   我先后试过三种钉法，**每一种都被自己的自指打败**：
    //   ① `expect(真判据源码).toContain('那一行的字面量')`
    //      ⇒ 负控自己把那个字面量写成期望值 ⇒ 恒绿；
    //   ② 「裁到本测试之前」
    //      ⇒ 真判据可能写在本测试**之后** ⇒ 连真判据一起切掉 ⇒ 恒红；
    //   ③ 「挖掉本测试那一段」
    //      ⇒ **裁切锚点字符串本身就在 helper 里** ⇒ `indexOf` 先命中 helper 自己
    //        ⇒ 挖掉的是 helper，测试正文原封不动 ⇒ 又恒绿（这一层最隐蔽）。
    // ⇒ ✅ **正解：用 `/…/` 正则字面量做钉** ——
    //   它的文本形态带反斜杠转义，**在物理上不可能与被搜的那一行逐字相同**，
    //   于是「自己命中自己」这件事消失了，也就不需要任何裁切。
    // ⇒ ★★ 通用式：**源码形态负控的钉子要写成正则字面量，不要写成字符串。**
    const self = readFileSync(join(process.cwd(), 'src/api/orphanLedger.spec.ts'), 'utf8')
    expect(
      self,
      '真判据里的「零端点 + 不可达 = 已登记集合」被削弱成了更松的断言',
    ).toMatch(
      /expect\(zeroEndpointOrphans\.sort\(\)\)\.toEqual\(\[\.\.\.KNOWN_INFRASTRUCTURE\]\.sort\(\)\)/,
    )
  })

  it('★★★ 第三类登记是**自撤销**的：壳一旦装上 / 基址一旦注入，这条立刻红', () => {
    // ★★ 这条是本类与「永久免罪」的分界线。
    //   两个前提都可观测、可变化 ⇒ 前提一变，登记自动作废。
    expect(shellIsNotInstalled(), '壳已安装或基址已注入 ⇒ nativeTransport 的登记该作废了').toBe(true)
    // ⇒ 反向自证：判别式本身是活的（不是恒真）
    const pkg = JSON.parse(readFileSync(join(process.cwd(), 'package.json'), 'utf8')) as {
      dependencies?: Record<string, string>
    }
    const deps = Object.keys(pkg.dependencies ?? {})
    expect(deps.some((k) => k.startsWith('@capacitor/')), '判别式的输入已经变了，判据要重判').toBe(false)
  })

  it('★★ 三份清单**互不重叠**（同一模块不得登记成两类）', () => {
    for (const name of KNOWN_DUPLICATES) {
      expect(KNOWN_ORPHANS).not.toContain(name)
      expect(KNOWN_INFRASTRUCTURE).not.toContain(name)
    }
    for (const name of KNOWN_INFRASTRUCTURE) expect(KNOWN_ORPHANS).not.toContain(name)
  })

  it('★★★ 「零端点 + UI 不可达」在全仓**唯一**命中已登记的模块', () => {
    // ★★ 这条把「为什么偏偏是它」钉成可复算的读数，而不是一句话断言。
    //   全仓另有三个零端点模块（transport / system / gatewaySites），
    //   但它们都**可达** ⇒ 「零端点」单独并不够，两条腿合起来才唯一。
    const seen = reachableFromUi()
    const zeroEndpoint = apiModules().filter((m) => (moduleSource(m).match(/['`]\/api\//g) ?? []).length === 0)
    const zeroEndpointOrphans = zeroEndpoint.filter((m) => !seen.has(m))
    expect(zeroEndpoint.length).toBeGreaterThan(1)
    expect(zeroEndpointOrphans.sort()).toEqual([...KNOWN_INFRASTRUCTURE].sort())
  })

  it('★★ 每一项登记的重复件**确实与某个已可达模块打同一个端点**（否则例外条款是免罪）', () => {
    // ★★★ 这条是第二类登记的**承重判据**：没有它，「重复件」就成了万能借口。
    // ★ 批 149 收口：原 `boardOperational` 合并删模块后清单已清空
    //   ⇒ 改用 `for` 循环，0 项时整条 trivial 通过；新登记必须照样自证。
    for (const name of KNOWN_DUPLICATES) {
      expect(currentOrphans(), `${name} 已不在孤儿集合里 ⇒ 这条登记该删了`).toContain(name)
      expect(duplicateIsProven(name), `${name} 没有已可达模块与它同端点 ⇒ 它是功能缺口，不是重复件`).toBe(true)
    }
  })

  it('★★ 重复件**不进**真缺口清单（两份清单不得重叠）', () => {
    for (const name of KNOWN_DUPLICATES) expect(KNOWN_ORPHANS).not.toContain(name)
  })

  it('★★ 已消解的重复件要**移出**登记（合并/删模块后这条逐条校验）', () => {
    // ★ 消解条件写在 KNOWN_DUPLICATES 的注释里；这条逼着人手工删那一行 ——
    //   与「接线一个孤儿就删一行」同一套纪律，只是对象换了。
    // ★ 批 149 收口：`boardOperational` 合并 ⇒ 这条循环 0 项即通过；
    //   新登记后**任一项不**在可达集合里立刻红。
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
    //   该族是 **superAdmin 档**（admin/handler.go:1133，整个 `/api/admin/report-rollup/`
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
    //   权限成因是**第三类**：注册处 `admin/handler.go:1144` 是 `h.superAdmin(...)`
    //   （`SuperAdminMiddleware` 逐字比 role ⇒ 后端 tenant_admin **读不到**），
    //   且 `handleOpsOverview` 自己不检 role、11 条子查询**全是全局表**
    //   ⇒ 服务端**完全没有租户维度** ⇒ 前端只能对齐成 `requiresRole: 'super_admin'`，
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

  it('★★ 已接线的哨兵：dataHorizon 不在孤儿里（第一百一十九批；第 129 批改名）', () => {
    // ★ 流量数据地平线接上了抽屉席与路由 ⇒ 棘轮清单同步删掉那一行。
    //   该族是 **admin 档**（`admin/handler.go:1109` 的 `admin(...)`，AdminMiddleware
    //   只认证不检 role ⇒ 后端 tenant_admin 可读）
    //   ⇒ 席**不设** requiresRole ⇒ `AppDrawer.spec.ts` 的 superAdmin 白名单里
    //   **不该**出现 data-horizon（与 probe-queue / connection-registry 同一条纪律）。
    // ★★ 与批 115/117/118 三族**相反**而那三族并没错：本族虽无租户维度，
    //   但载荷是**平台级全局事实**（六个键无一带租户）⇒ 过度限制**不适用**。
    //   ⇒ 两条判据合起来才覆盖「接上了」+「档位挂对了」。
    // ⚠️ 第 129 批：模块随 r1033 的后端重写改名 `v1DataHorizon` → `dataHorizon`。
    //   ★★ 这条哨兵的**取样方式**与第 128 批判据同一个病：断言「仓库里恰好有
    //   一个已接线的模块」—— 模块改名它就红，而它红的原因与被测行为无关。
    //   ⇒ 真正的承重判据是上面那条「当前孤儿集合 = 三类登记并集」，
    //     它对**全部**孤儿逐个核对；这条哨兵只补一个「改名后仍可达」的实例。
    expect(currentOrphans()).not.toContain('dataHorizon')
    // ★ 旧名必须**不再存在**（否则改名就是复制而不是移动）
    expect(apiModules()).not.toContain('v1DataHorizon')
  })

  it('★★★ 反向护栏：api 内部的互相引用不得凭空产生可达性', () => {
    // ★ 种子只从 UI 侧起。若把 api 内部互相 import 也算成种子，
    //   第九十九批那个假阳性就会回来（taskTypeCorrectionStats 从 ./taskProfile 导入
    //   unwrapCorrectionStat 会被当成「taskProfile 已接上」）。
    const orphans = currentOrphans()
    // ★ 第一百二十七批订正：`turnsFilterOptions` 已接线并移出清单
    //   ⇒ 换成当时仍不可达的 `sessionAnalyticsFilterOptions`。
    // ★ 第一百二十八批：**它也接线了**（最后一个真缺口归零）⇒ 改用 `boardOperational`。
    //   ⇒ ★★★ 这已经是这条取样**第三次**要换名字，而每次都是同一个原因：
    //     孤儿被接进 UI ⇒ 样本失效。这不是巧合，是**取样方式错了** ——
    //     断言「仓库里恰好有一个未接线的模块」必然随流水线推进而腐烂。
    //   ⇒ 那条「登记与实际必须一致」的判据（★ 当前孤儿集合 = …）才是承重的：
    //     它对**全部**孤儿逐个核对，抽样的失效不会让它变绿。
    //   ⇒ 这里退一步只断言一个**结构性**事实：孤儿集合**非空**。
    //     真归零时（三个清单都空）这条自然也要改 —— 那时它该断言 `toHaveLength(0)`，
    //     而**不是**回头去找一个假孤儿来凑数。
    expect(orphans.length).toBeGreaterThan(0)
    // ★ 承重的那一条单独再钉一次：孤儿集合里的每一项都必须落在三个清单之内。
    for (const name of orphans) {
      expect(
        [...KNOWN_ORPHANS, ...KNOWN_DUPLICATES, ...KNOWN_INFRASTRUCTURE],
        `${name} 是实际孤儿却没被登记`,
      ).toContain(name)
    }
  })

  // ══════════════════════════════════════════════════════════════════════
  // ⑥ ★★★ 第 133 批：给「孤儿」这个**量具本身**加自证。
  //   本判据把「可达」当成 import 可达（UI 侧 import 图的传递闭包），
  //   而完成口径是「可达 UI 入口」。差别只有一处：
  //   uiSourceBlob 把 src 下**所有**文件都当种子 ⇒ 一个死面板被删到没人引用，
  //   但只要它自己还在树里、还 import 着那个 api 模块，判据就仍判「已接」。
  //   下面三条把「碰巧对」变成「被看着」。
  // ══════════════════════════════════════════════════════════════════════

  it('⑥-1 ★★★★★ 剥掉注释后可达性不得改变（★ 注释不得伪造可达性）', () => {
    // 判据扫的是原始源码（含注释）。若某个 UI 文件在**注释**里写了
    // `from '@/api/xxx'`，那个模块会被算成「已接」而其实没人 import。
    // ⇒ 用批 132 修好的剥离顺序再跑一遍，两边孤儿集合必须**完全一致**。
    const raw = currentOrphans().sort()
    const strippedAll = apiModules()
    const strippedOrphans: string[] = []
    const strippedSeen = new Set<string>()
    const all = new Set(apiModules())
    const queue = importsOf(uiSourceBlobStripped()).filter((n) => all.has(n))
    while (queue.length > 0) {
      const n = queue.shift() as string
      if (strippedSeen.has(n)) continue
      strippedSeen.add(n)
      for (const dep of importsOf(stripComments(moduleSource(n)))) {
        if (all.has(dep) && !strippedSeen.has(dep)) queue.push(dep)
      }
    }
    for (const m of strippedAll) if (!strippedSeen.has(m)) strippedOrphans.push(m)
    strippedOrphans.sort()

    expect(strippedOrphans, '注释里出现了 `from "@/api/x"` 形态的假 import ⇒ 某个真孤儿被藏起来了').toEqual(raw)
    expect(raw.length, '一条孤儿都没扫到 ⇒ 量具塌缩了').toBeGreaterThan(0)
  })

  it('⑥-2 ★★★★★ 「可达但无 views 直接引用」的模块必须逐个点名', () => {
    // 这 6 个都是**有正当理由**的例外，必须逐个写下来：
    //   · credentialWriteOps / mobileNode / nodeAudit —— 由共用面板引入
    //     （⑥-3 会验这些面板**确实被挂载**）
    //   · nodeHealthTimeline —— api→api 委托（nodeHealth 转发）
    //   · transport —— api→api 委托
    //   · featureCapabilities（2026-10-21 R72f）—— **经 store 引入**：
    //     能力位的唯一消费者是 `stores/capabilities.ts`，再由
    //     `hyper-wiring.ts`（路由守卫）与 `components/shell/AppDrawer.vue`
    //     （抽屉）使用。⇒ 视图**不直接**引用它 ⇒ 但它**确实在运行时路径上**
    //     （⑥-3 会验它的引用方 `stores/capabilities.ts` 确实被挂载）。
    //     ⚠️ 若有人新接了「经由面板/委托」而没进这张表，这条会红 ——
    //       那正是它该做的：例外必须被登记，不能悄悄存在。
    const seen = reachableFromUi()
    const EXPECTED = [
      'credentialWriteOps', 'featureCapabilities', 'mobileNode',
      'nodeAudit', 'nodeHealthTimeline', 'transport',
    ]
    const actual = apiModules()
      .filter((m) => seen.has(m))
      .filter((m) => uiImportersOf(m).every((f) => !f.startsWith('views/')))
      .sort()
    expect(actual, '这些模块没有任何视图直接引用（须逐个登记理由，或改成有视图入口）').toEqual(EXPECTED)
  })

  it('⑥-2a 局部反向索引与逐文件过滤等价，空源/重复import不伪造引用', () => {
    const sources = uiSources(), index = uiImporterIndex(sources)
    for (const name of apiModules()) {
      const direct = [...sources].filter(([, source]) => importsOf(source).includes(name)).map(([file]) => file)
      expect(uiImportersOf(name, index)).toEqual(direct)
    }
    const synthetic = new Map([
      ['views/Live.vue', 'import { a } from "@/api/live"; import { b } from "@/api/live"'],
      ['components/Panel.vue', 'import { c } from "@/api/other"'],
      ['views/Empty.vue', 'no imports'],
    ])
    const controlled = uiImporterIndex(synthetic)
    expect(uiImportersOf('live', controlled)).toEqual(['views/Live.vue'])
    expect(uiImportersOf('other', controlled)).toEqual(['components/Panel.vue'])
    expect(uiImportersOf('missing', controlled)).toEqual([])
    expect(uiImporterIndex(new Map()).size).toBe(0)
  })

  it('⑥-3 ★★★★★ 非视图引用方**确实被挂载**（死面板不得伪装成 UI 入口）', () => {
    // 批144实测20秒超时：原实现按每个API反复读全树；局部索引不改变扫描范围。
    const sources = uiSources(), importers = uiImporterIndex(sources)
    const stripped = stripComments([...sources.values()].join('\n'))
    // ★ 这是本组的关键一条：⑥-2 只保证「例外被点名」，
    //   而 ⑥-3 保证这些例外**真的能被人点到** ——
    //   即引用它们的那个面板/组件自己**不是死文件**（至少被一个非 spec 文件引用）。
    const seen = reachableFromUi()
    const noViewDirect = apiModules()
      .filter((m) => seen.has(m))
      .filter((m) => uiImportersOf(m, importers).every((f) => !f.startsWith('views/')))

    expect(noViewDirect.length, '一组都没扫到 ⇒ 判据量具坏了').toBeGreaterThan(0)

    const dead: string[] = []
    for (const m of noViewDirect) {
      for (const importer of uiImportersOf(m, importers)) {
        const base = importer.replace(/\.(ts|vue)$/, '')
        const stem = base.split('/').pop() as string
        // 谁引用了这个引用方？至少要被一个**非 spec** 文件提到。
        // ⚠️⚠️ 挂载方式**不只一种**，第一版只认 `import … from` ⇒ 误判：
        //   这三个面板都是通过**组件标签**被用的（Options API 的 components
        //   注册 / 模板里的 `<XxxSheet />`），没有对应的 import 语句。
        //   ⇒ 只按 import 判「有没有被引用」，会把**活着的**面板判成死的。
        const mounted =
          // ① import 语句（跨行 ⇒ 必须在整块上做多行正则）
          new RegExp(`import[\\s\\S]{0,300}?from\\s+['"][^'"]*${stem}['"]`).test(stripped) ||
          // ② 模板里的组件标签 <XxxSheet …>
          new RegExp(`<\\s*${stem}[\\s/>]`).test(stripped) ||
          // ③ Options API 的 `components: { XxxSheet }` 注册
          new RegExp(`components\\s*:\\s*\\{[^}]*\\b${stem}\\b`).test(stripped)
        if (!mounted) dead.push(`${m} ← ${importer}（这个引用方自己没人引用）`)
      }
    }
    expect(dead, `这些引用方是**死文件** ⇒ 判据把它们算成「已接 UI」，实际用户点不到：\n  ${dead.join('\n  ')}`).toEqual([])
  })

  it('⑥-4 ★★★★ 钉住 ⑥-1 / ⑥-2 的**集合比对形态**（★ 治「更弱但仍为真」的变异）', () => {
    // ★★★★ 变异台 Q02 / Q04 把两条的断言**换成更弱但当前仍为真**的表达式：
    //   ⑥-1  toEqual(raw)          → strippedOrphans.length >= raw.length
    //   ⑥-2  toEqual(EXPECTED)     → actual.length <= EXPECTED.length
    //   两条在当前数据上**都仍然为真** ⇒ 变异台判 NO_RED。
    //
    // ⇒ 这是第 129 批定下的那一族：**判据被削弱时无法察觉自己被削弱**。
    //   治法只有一个：**从源码把「强形态」钉住** —— 否则哪天有人顺手把它改成
    //   「长度不小于」，没人会看见，而集合比对（本次唯一的承重件）就没了。
    //
    // ⚠️ 本条的形状与本文件里已有的「钉判据自身」那几条同族，属**僵尸断言**：
    //   只在「自己被改」时才红。它的价值在于把「别悄悄削弱它」写成门。
    const self = readFileSync(join(API_DIR, 'orphanLedger.spec.ts'), 'utf8')
    // ⑥-1：必须是**集合**相等，不是长度比较
    expect(
      self,
      '⑥-1 的集合比对被削弱了 ⇒ 注释伪造可达性又会被放过',
    ).toMatch(/expect\(strippedOrphans,[^\n]*\)\.toEqual\(raw\)/)
    // ⑥-2：登记表必须**逐个相等**，不是长度上界
    expect(
      self,
      '⑥-2 的登记表比对被削弱了 ⇒ 例外可以名存实亡',
    ).toMatch(/expect\(actual,[^\n]*\)\.toEqual\(EXPECTED\)/)
  })
})
