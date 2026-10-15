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
  const src = moduleSource(name)
  const endpointCount = (src.match(/['`]\/api\//g) ?? []).length
  if (endpointCount !== 0) return false // 腿①：有端点 ⇒ 是功能缺口，该开页
  return shellIsNotInstalled()
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

const KNOWN_ORPHANS: readonly string[] = [
  'sessionAnalyticsFilterOptions',
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
    expect(infrastructureIsProven('sessionAnalyticsFilterOptions')).toBe(false)
    expect(infrastructureIsProven('turnsFilterOptions')).toBe(false)
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
  })
})
