#!/usr/bin/env node
/**
 * audit-route-coverage.mjs —— 路由覆盖三方对拍（L10 收口轮的可复跑工具）
 *
 * ## 为什么要有这道工具
 *
 * 2026-11-20 的 L10 首版报告是**手工核对**的产物（读 router.ts + 逐个 flow
 * 点数），2026-11-23 复核时发现两个致命问题：
 *
 *   ① **不可复跑**：首版报告写「router.ts 实际路由总数 110 条」，而同一周
 *      上游新增路由后 README §1.1 的口径已经变成 125 / 130 ⇒ **报告的数
 *      字当场过期**，而报告本身没有任何「怎么重算」的说明 ⇒ 读者无法判断
 *      它现在是真是假。
 *   ② **量具自己跑不起来**：首版配套的 `extract-routes.mjs` 用
 *      `import ts from 'typescript'`，而仓内 typescript 装在 `web/`
 *      ⇒ 从仓根跑直接 `ERR_MODULE_NOT_FOUND` ⇒ ★ **工具不可运行 ⇒ 它的
 *      产出不可复现** ⇒ 「可重复运行」这个工具的核心价值当场失效。
 *
 * ⇒ 本工具把三件事一次做完，且**只依赖仓内现有资产**：
 *   ① 用 **TypeScript AST** 从 `web/src/router.ts` 提取路由（不数正则）
 *   ② 从 `maestro/flows/` 下的 yaml 的 `openLink:` 提取**真实被打开过**的路径
 *   ③ 从 `maestro/README.md` §3 提取**已登记**的路由
 *   然后三方对拍，产出：未覆盖路由 / README 未登记 / README 多记（幽灵路由）。
 *
 * ## 口径（重要：与 README §1.1 的关系）
 *
 * ★ 本工具数的是 **「flow 真的打开过的路由」**，README §1.1 数的是
 *   **「运行时路由总数」** ⇒ 两个数**本来就该不一样**（差值＝未覆盖量）。
 *   本工具**不重算** README §1.1 的口径（那套口径含「重定向路由的 meta 归属」
 *   等一整套人工判据，见 README §1.1 的计数口径说明）⇒ 两个口径各自独立、
 *   互为对照，**不合并**。
 *
 * ## 用法
 *
 *   node maestro/tools/audit-route-coverage.mjs           # Markdown 报告
 *   node maestro/tools/audit-route-coverage.mjs --json    # 机读 JSON
 *   node maestro/tools/audit-route-coverage.mjs --check   # ★ README 声明的数字 ↔ 当前树 对拍（漂移即红）
 *
 * 退出码：0 = 跑通（不代表「全覆盖」）；1 = 工具自身出错。
 * ★ 本工具**不判定红绿**——「未覆盖」是**事实登记**，不是缺陷（有些路由
 *   本来就不该由 Maestro 打开，见 README §3 的登记口径）。
 *
 * ★ `--check` 是**唯一的例外**：它判的是「README 声明的数与树实不符」
 *   ⇒ 退出码 0 = 全部一致；1 = 有漂移（逐条报出）；2 = 工具自身出错。
 *   ⇒ 理由：§1.1/§1.2/§1.3/§3.13 的数字**只有跟树对拍才有意义**，
 *     而「数字漂移」本身是**缺陷**（不是事实登记）⇒ 必须能判红。
 */

import { readFileSync, readdirSync, statSync } from 'fs'
import { fileURLToPath } from 'url'
import { dirname, resolve, join } from 'path'
import { createRequire } from 'module'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const repoRoot = resolve(__dirname, '../..')

/** ★ 与 extract-routes.mjs 同款：typescript 从 web/ 那份解析（ESM 不认 NODE_PATH）。 */
const webRequire = createRequire(resolve(repoRoot, 'web/package.json'))
const ts = webRequire('typescript')

// ────────────────────────────────────────────────────────────── 1. 路由侧（AST）

function extractRoutesFromRouter() {
  // ★ 可用 `--router=<path>` 指向别处的 router.ts（复核历史口径时用：
  //   「上次报告里的数字」要与「同一工具在当时的输入上跑出来的数字」比，
  //   而不是拿现在的口径去套旧数字）。
  const argRouter = process.argv.find((a) => a.startsWith('--router='))
  const routerPath = argRouter ? resolve(argRouter.slice('--router='.length)) : join(repoRoot, 'web/src/router.ts')
  const source = readFileSync(routerPath, 'utf-8')
  const sourceFile = ts.createSourceFile(
    'router.ts',
    source,
    ts.ScriptTarget.Latest,
    true,
  )

  const routes = []

  const stringValue = (node) => {
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text
    if (ts.isTemplateExpression(node)) return node.getText()
    return null
  }

  const componentName = (node) => {
    if (ts.isIdentifier(node)) return node.text
    const text = node.getText()
    const m = text.match(/import\(['"](.+)['"]\)/)
    if (m) return m[1]
    if (text.includes('externalMaintainRedirect')) return 'externalMaintainRedirect'
    return null
  }

  const extractMeta = (node) => {
    const meta = {}
    if (!ts.isObjectLiteralExpression(node)) return meta
    for (const prop of node.properties) {
      if (!ts.isPropertyAssignment(prop)) continue
      const name = prop.name.text
      if (name === 'public') meta.public = prop.initializer.kind === ts.SyntaxKind.TrueKeyword
      else if (name === 'requiresAuth') meta.requiresAuth = prop.initializer.kind === ts.SyntaxKind.TrueKeyword
      else if (name === 'requiresSuper') meta.requiresSuper = prop.initializer.kind === ts.SyntaxKind.TrueKeyword
      else if (name === 'requiresPlatformOps') meta.requiresPlatformOps = prop.initializer.kind === ts.SyntaxKind.TrueKeyword
      else if (name === 'requiresProviderConsole') meta.requiresProviderConsole = prop.initializer.kind === ts.SyntaxKind.TrueKeyword
    }
    return meta
  }

  const extract = (obj) => {
    const route = {}
    for (const prop of obj.properties) {
      if (!ts.isPropertyAssignment(prop)) continue
      const name = prop.name.text
      if (name === 'path') route.path = stringValue(prop.initializer)
      else if (name === 'redirect') {
        // ★ 只要**有** redirect 属性就算「纯 redirect 路由」（README §1.2 的口径），
        //   哪怕目标是箭头函数算出来的 —— 否则那 6 条（/catalog、/maas/* 族）
        //   会整段漏掉，README §1.2 的 19 条就被数成 13 条。
        route.hasRedirect = true
        if (ts.isStringLiteral(prop.initializer)) route.redirect = prop.initializer.text
        else if (ts.isObjectLiteralExpression(prop.initializer)) {
          const p = prop.initializer.properties.find(
            (x) => ts.isPropertyAssignment(x) && x.name.text === 'path',
          )
          if (p) route.redirect = stringValue(p.initializer)
        } else if (
          ts.isArrowFunction(prop.initializer) ||
          ts.isFunctionExpression(prop.initializer)
        ) {
          // `redirect: (to) => ({ path: '/models', ... })` —— 取字面量 path；
          // 取不到就留空（effective meta 退回自身 meta）。
          const body = prop.initializer.body
          if (
            ts.isParenthesizedExpression(body) &&
            ts.isObjectLiteralExpression(body.expression)
          ) {
            const p = body.expression.properties.find(
              (x) => ts.isPropertyAssignment(x) && x.name.text === 'path',
            )
            if (p) route.redirect = stringValue(p.initializer)
          }
        }
      } else if (name === 'meta') route.meta = extractMeta(prop.initializer)
      else if (name === 'component') route.component = componentName(prop.initializer)
      else if (name === 'children' && ts.isArrayLiteralExpression(prop.initializer)) {
        route.children = prop.initializer.elements
          .filter((c) => ts.isObjectLiteralExpression(c))
          .map((c) => extract(c))
      }
    }
    return route
  }

  /**
   * 从一个数组字面量的元素里抽出全部路由对象。
   *
   * ★ 为什么要处理「展开元素」：`router.ts` 里有两类路由**不是**直接的
   *   对象字面量元素：
   *   ① `...(import.meta.env.DEV ? [{ path: '/dev/xxx', ... }] : [])`
   *      —— dev-only 的 3 条（生产构建不注册，但**路由表里有**）；
   *   ② `externalMaintainRedirect('/ops/licenses', '/maintain/ops/licenses')`
   *      —— 5 条 `/ops/*`，它造的是「component: {render: () => null} +
   *      beforeEnter 里 window.location.replace」的路由记录。
   *   ⇒ 只认「直接对象字面量」会把这 8 条漏掉 ⇒ README §3 里登记了它们
   *     ⇒ 工具就会把它们误报成「README 有、router 没有」的幽灵路由。
   */
  const collectFromArray = (arrayLiteral) => {
    for (const el of arrayLiteral.elements) {
      if (ts.isObjectLiteralExpression(el)) {
        const r = extract(el)
        if (r.path) routes.push(r)
        continue
      }
      // ★ 类型坑：数组字面量里的 `...x` 是 **SpreadElement**，
      //   而对象字面量里的 `...x` 才是 **SpreadAssignment**
      //   （TS AST 的两个不同节点类型）⇒ 只判 SpreadAssignment 会把
      //   dev-only 的三条条件展开路由整段漏掉。
      if (ts.isSpreadElement(el) || ts.isSpreadAssignment(el)) {
        let expr = el.expression
        // ★ 又一层：TS 会把 `(cond ? a : b)` 保留成 **ParenthesizedExpression**
        //   节点 ⇒ 不解包就判不出条件表达式 ⇒ dev-only 三条继续漏。
        while (ts.isParenthesizedExpression(expr)) expr = expr.expression
        if (ts.isConditionalExpression(expr)) {
          for (const branch of [expr.whenTrue, expr.whenFalse]) {
            if (ts.isArrayLiteralExpression(branch)) collectFromArray(branch)
          }
        } else if (ts.isArrayLiteralExpression(expr)) {
          collectFromArray(expr)
        }
        continue
      }
      if (ts.isCallExpression(el) && el.expression.getText() === 'externalMaintainRedirect') {
        const args = el.arguments
        if (args.length >= 2 && ts.isStringLiteral(args[0]) && ts.isStringLiteral(args[1])) {
          routes.push({
            path: args[0].text,
            redirect: args[1].text,
            component: 'externalMaintainRedirect',
            meta: {},
          })
        }
      }
    }
  }

  const visit = (node) => {
    if (
      ts.isPropertyAssignment(node) &&
      ts.isIdentifier(node.name) &&
      node.name.text === 'routes' &&
      ts.isArrayLiteralExpression(node.initializer)
    ) {
      collectFromArray(node.initializer)
    }
    ts.forEachChild(node, visit)
  }
  visit(sourceFile)

  const flat = []
  const walk = (list, parentPath) => {
    for (const r of list) {
      const full = r.path
        ? (parentPath ? `${parentPath}/${r.path}` : r.path).replace(/\/+/g, '/')
        : parentPath
      flat.push({
        path: full,
        redirect: r.redirect ?? null,
        hasRedirect: r.hasRedirect === true,
        component: r.component ?? null,
        meta: r.meta ?? {},
      })
      if (r.children) walk(r.children, full)
    }
  }
  walk(routes, '')
  flat.sort((a, b) => a.path.localeCompare(b.path))
  return flat
}

// ────────────────────────────────────────────────────────────── 1b. §1.1 统计口径

/**
 * 按 `maestro/README.md` §1.1 的**公布口径**复算统计表。
 *
 * ★ 口径要点（与 README §1.1 的「计数口径」说明一一对应）：
 *   - `path:` 条目 ＝ 路由对象数（**含** dev-only 的 3 条条件展开路由，
 *     因为它们在 `routes:` 数组里是字面量）；
 *   - 运行时路由总数 ＝ path 条目 + `externalMaintainRedirect(...)` 造的 5 条；
 *   - 纯 redirect ＝ 自己没有 component、只跳走；
 *   - 「不需要登录」＝ `meta.public === true` ∪ 「自身无 meta、但重定向到
 *     公开页」（vue-router 先跑 `handleRedirectRecord()` 再跑守卫，
 *     所以 redirect 路由由**目标页**的 meta 决定要不要登录）；
 *   - 权限分档 ＝ 按 `router.ts` 的 `beforeEach`：
 *     无 meta 或 `requiresAuth` ⇒ 任意登录用户；否则按 super / platformOps /
 *     providerConsole 归档。
 *
 * ★ 这套口径**不是本工具发明的**：它逐条对应 README §1.1 已公布的数字。
 *   本工具的价值是**能在任何一棵树（含历史提交）上复算同一张表**——
 *   只要在基准提交上复算出与 README 公布值逐项相等的数，
 *   当前树上的新数就是可信的（而不是「另一个口径的另一组数」）。
 */
function computeStats(routes) {
  const byPath = new Map(routes.map((r) => [r.path, r]))
  const externalCount = routes.filter((r) => r.component === 'externalMaintainRedirect').length
  // ★ path 条目＝**路由对象**数（不含 externalMaintainRedirect 造的 5 条）
  //   —— README §1.1 的「125 + 5 = 130」正是这个口径。
  const pathEntries = routes.length - externalCount
  const runtimeTotal = routes.length

  // ★ 纯 redirect ＝ **数组内带 `redirect:` 的条目**（README §1.2 的口径，
  //   含 externalMaintainRedirect 造的 5 条 —— 它们同样只跳走、无自有视图）。
  const pureRedirect = routes.filter((r) => r.hasRedirect).length
  // ★ 动态段**不含** glob 兜底（`:pathMatch(.*)*` 单列一行，见 README §1.1）。
  const dynamic = routes.filter((r) =>
    r.path.split('/').some((s) => s.startsWith(':') && !s.startsWith(':pathMatch')),
  ).length
  const glob = routes.filter((r) => r.path.includes(':pathMatch')).length

  /** 该路由最终判定的 meta（redirect 路由沿用目标页的 meta）。 */
  const effectiveMeta = (route, seen = new Set()) => {
    if (!route.redirect) return route.meta
    const target = route.redirect.split('?')[0].split('#')[0].replace(/\/+$/, '') || '/'
    if (seen.has(target)) return route.meta
    seen.add(target)
    const t = byPath.get(target)
    return t ? effectiveMeta(t, seen) : route.meta
  }

  let publicCount = 0
  const tiers = { anyUser: 0, super: 0, platformOps: 0, providerConsole: 0 }
  for (const r of routes) {
    // ① 公开与否按 **effective meta**：vue-router 的 `handleRedirectRecord()`
    //    在导航守卫**之前**执行 ⇒ 一条 redirect 路由的登录要求由**目标页**
    //    的 meta 决定 ⇒ 「自身无 meta、但重定向到公开页」那 5 条自动落进 public。
    if (effectiveMeta(r).public === true) {
      publicCount++
      continue
    }
    // ② 权限分档按**自身 meta**（README §1.1 已公布口径）：
    //    任何一条 meta 权限位都没有 ⇒ 归「任意登录用户」。
    const m = r.meta
    if (m.requiresSuper) tiers.super++
    else if (m.requiresPlatformOps) tiers.platformOps++
    else if (m.requiresProviderConsole) tiers.providerConsole++
    else tiers.anyUser++
  }

  return {
    pathEntries,
    externalCount,
    runtimeTotal,
    pureRedirect,
    dynamic,
    glob,
    publicCount,
    authCount: runtimeTotal - publicCount,
    tiers,
  }
}

// ────────────────────────────────────────────────────────────── 2. flow 侧

function listYamlFiles(dir) {
  const out = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) out.push(...listYamlFiles(full))
    else if (entry.endsWith('.yaml') || entry.endsWith('.yml')) out.push(full)
  }
  return out
}

/**
 * 提取 flow 里**真的打开过**的路径。
 *
 * ★ 口径：只认 `openLink:`（Maestro web flow 里唯一的「打开页面」动作）。
 *   `evalScript` 里的 `location.href = ...` 也算打开，但 273 处 evalScript
 *   绝大多数是数据读取 ⇒ 若把它们算进来会**虚高覆盖率** ⇒ 这里只认
 *   `openLink:`，并在报告里写明该口径的边界。
 */
function extractFlowOpens() {
  const flowsDir = join(repoRoot, 'maestro/flows')
  const files = listYamlFiles(flowsDir)
  const opens = []
  for (const file of files) {
    const text = readFileSync(file, 'utf-8')
    const lines = text.split('\n')
    lines.forEach((line, i) => {
      const m = line.match(/^\s*-\s*openLink:\s*(.+?)\s*$/)
      if (!m) return
      let raw = m[1].trim()
      if (/^["'].*["']$/.test(raw)) raw = raw.slice(1, -1)
      const rel = file.slice(repoRoot.length + 1)
      opens.push({ flow: rel, line: i + 1, raw, path: normalizeFlowPath(raw) })
    })
  }
  return opens
}

/** flow 里的 URL → 站内路径（去 origin / 去 query / 去 hash / 去尾斜杠）。 */
function normalizeFlowPath(raw) {
  let p = raw
  p = p.replace(/^["']|["']$/g, '')
  p = p.replace(/\$\{BASE_URL\}/g, '')
  p = p.replace(/^https?:\/\/[^/]+/i, '')
  p = p.split('?')[0].split('#')[0]
  if (!p.startsWith('/')) p = '/' + p
  p = p.replace(/\/+$/, '')
  return p || '/'
}

// ────────────────────────────────────────────────────────────── 3. README §3 侧

function extractReadmeSection3() {
  const readme = readFileSync(join(repoRoot, 'maestro/README.md'), 'utf-8')
  const start = readme.indexOf('## 3. 逐条路由核对表')
  if (start < 0) return { paths: new Set(), text: '' }
  const end = readme.indexOf('\n## 4.', start)
  let text = readme.slice(start, end < 0 ? undefined : end)
  // ★ §3.13「已知缺口」登记的是**没被 flow 打开**的路由 —— 它是**缺口清单**，
  //   不是**覆盖登记** ⇒ 若把它算进「§3 已登记」，工具会自己把缺口「登记掉」
  //   ⇒ 未覆盖数被自己掩盖 ⇒ 口径失真 ⇒ 这里在 §3.13 处截断。
  const gapStart = text.indexOf('### 3.13')
  if (gapStart >= 0) text = text.slice(0, gapStart)
  const paths = new Set()
  // ★ 只取**表格行**里的反引号路径：
  //   §3 的正文散文里也会出现 `/customer/*`、`/tenant/*` 这类**通配写法**
  //   （那是「这一族」的说明，不是单条路由）⇒ 把它当路由会造出幽灵条目。
  for (const line of text.split('\n')) {
    if (!line.trim().startsWith('|')) continue
    for (const m of line.matchAll(/`(\/[^`\s]*)`/g)) {
      const raw = m[1]
      if (raw.includes('*')) continue // 通配写法（族说明），不是单条路由
      const p = raw.split('?')[0].split('#')[0].replace(/\/+$/, '') || '/'
      paths.add(p)
    }
  }
  return { paths, text }
}

// ────────────────────────────────────────────────────────────── 4. 对拍

/** 路径归一：动态段（`:param` / `:pathMatch(.*)*` / `${VAR}` / 末段 id）统一成 `*`。 */
function shape(path) {
  return path
    .split('/')
    .map((seg) => {
      if (!seg) return seg
      if (seg.startsWith(':')) return '*'
      if (seg.includes('${')) return '*'
      return seg
    })
    .join('/')
}

const routerRoutes = extractRoutesFromRouter()
const flowOpens = extractFlowOpens()
const { paths: readmePaths } = extractReadmeSection3()

const flowShapes = new Map() // shape → {flow, raw}
for (const o of flowOpens) {
  const s = shape(o.path)
  if (!flowShapes.has(s)) flowShapes.set(s, o)
}

const covered = []
const uncovered = []
for (const r of routerRoutes) {
  const s = shape(r.path)
  const hit = flowShapes.get(s)
  if (hit) covered.push({ ...r, viaFlow: hit.flow, viaRaw: hit.raw })
  else uncovered.push(r)
}

const routerShapes = new Set(routerRoutes.map((r) => shape(r.path)))
const routerPaths = new Set(routerRoutes.map((r) => r.path))

const readmeNotInRouter = [...readmePaths].filter((p) => !routerShapes.has(shape(p)))
const routerNotInReadme = routerRoutes.filter((r) => !readmePaths.has(r.path) && !readmePaths.has(shape(r.path)))

// ────────────────────────────────────────────────────────────── 5. 输出

const stats = computeStats(routerRoutes)

/**
 * ── 5.1 `--check` 模式：README §1.1/§1.2/§1.3/§3.13 声明的数字 ↔ 当前树实测
 *
 * ★ **为什么要有这个模式**：本工具的前身是「手工核对的报告」⇒ **数字会静默过期**
 *   （2026-11-23 复核轮实测：上游 `41fb572f3` 加了 2 条路由 ⇒ README §1.1 的
 *   129/134/22/117/54 全部过期，而**没有任何一处会红**）。
 *   ⇒ 有了 `--check`，「README 的数字与树不一致」就变成**一条会红的判据**，
 *     而不是「等下次有人想起来再手抄一遍」。
 *
 * ★ 判据分两类：
 *   ① **声明值 vs 实测值**：README 里写的数必须等于工具量出来的数；
 *   ② **标题数 vs 表内行数**：README 各小节的标题声明与它自己的表必须自洽
 *     （★ 这一类能抓「只改了表、忘了改标题」的**半回填**）。
 *
 * 退出码：0 = 全部一致；1 = 有漂移（逐条报出）；2 = 工具自身出错（读不到 README 等）。
 */
function readmeDeclaredNumbers() {
  const readme = readFileSync(join(repoRoot, 'maestro/README.md'), 'utf-8')

  // ★ 取某节正文：从标题行到下一个同级标题行（`### `）。
  const section = (headingRe) => {
    const m = readme.match(headingRe)
    if (!m) return null
    const start = m.index
    const rest = readme.slice(start + m[0].length)
    const next = rest.search(/\n### |\n## /)
    return next < 0 ? rest : rest.slice(0, next)
  }

  const num = (text, re) => {
    const m = text.match(re)
    return m ? Number(m[1]) : null
  }

  const s11 = section(/### 1\.1 实测统计/)
  const s12 = section(/### 1\.2 纯 redirect 路由/)
  const s13 = section(/### 1\.3 带命名动态段的路由/)
  const s313 = section(/### 3\.13 已知缺口/)

  // ★ 表格行数（去掉表头行与分隔行）。
  const tableRows = (sec) => {
    if (!sec) return null
    const rows = sec.split('\n').filter((l) => l.trim().startsWith('|'))
    return Math.max(0, rows.length - 2)
  }

  // ★ §3.13 的表里有「一行多路由」的合并行（`/operations/market-prices` /
  //   `/operations/marketing` / `/operations/sales` 写在同一行）⇒ 不能数行，
  //   要数**第一列里去重后的路径字面量**。
  const gapPaths = (sec) => {
    if (!sec) return null
    const set = new Set()
    for (const line of sec.split('\n')) {
      if (!line.trim().startsWith('|')) continue
      const firstCell = line.slice(1, line.indexOf('|', 1))
      for (const m of firstCell.matchAll(/`([^`]+)`/g)) {
        if (m[1].startsWith('/')) set.add(m[1])
      }
    }
    return set
  }

  const d = {
    pathEntries: num(s11 || '', /\| `path:` 条目 \| \*\*(\d+)\*\* \|/),
    externalCount: num(s11 || '', /\| `externalMaintainRedirect\(\.\.\.\)` 调用 \| \*\*(\d+)\*\* \|/),
    runtimeTotal: num(s11 || '', /\| \*\*运行时路由总数\*\* \| \*\*(\d+)\*\* \|/),
    pureRedirect: num(s11 || '', /\| 其中纯 redirect（没有自己的组件，只跳走） \| \*\*(\d+)\*\* \|/),
    dynamic: num(s11 || '', /\| 其中带命名动态段（`:param`，不含 glob） \| \*\*(\d+)\*\* \|/),
    glob: num(s11 || '', /\| 其中 glob 兜底（`:pathMatch\(\.\*\)\*`） \| \*\*(\d+)\*\* \|/),
    publicCount: num(s11 || '', /\| \*\*不需要登录\*\* \| \*\*(\d+)\*\* \|/),
    authCount: num(s11 || '', /\| \*\*需要登录\*\* \| \*\*(\d+)\*\* \|/),
    anyUser: num(s11 || '', /\| 任意登录用户（[^|]*） \| \*\*(\d+)\*\* \|/),
    super: num(s11 || '', /\| `requiresSuper`（[^|]*） \| \*\*(\d+)\*\* \|/),
    platformOps: num(s11 || '', /\| `requiresPlatformOps`（[^|]*） \| \*\*(\d+)\*\* \|/),
    providerConsole: num(s11 || '', /\| `requiresProviderConsole`（[^|]*） \| \*\*(\d+)\*\* \|/),
    s12Heading: num(readme, /### 1\.2 纯 redirect 路由（(\d+) 条）/),
    s12Rows: tableRows(s12),
    s13Heading: num(readme, /### 1\.3 带命名动态段的路由（(\d+) 条）/),
    s13Rows: tableRows(s13),
    s313Heading: num(readme, /### 3\.13 已知缺口：\*\*未被任何 flow 的 `openLink` 直接打开\*\*的 (\d+) 条路由/),
    s313Paths: gapPaths(s313),
  }
  d.s313PathCount = d.s313Paths ? d.s313Paths.size : null
  return d
}

function runCheck() {
  const declared = readmeDeclaredNumbers()
  const rows = []
  const eq = (name, declaredValue, actualValue, note) => {
    rows.push({
      name,
      declared: declaredValue,
      actual: actualValue,
      pass: declaredValue !== null && declaredValue === actualValue,
      note: note || '',
    })
  }

  eq('§1.1 path 条目', declared.pathEntries, stats.pathEntries)
  eq('§1.1 externalMaintainRedirect 调用', declared.externalCount, stats.externalCount)
  eq('§1.1 运行时路由总数', declared.runtimeTotal, stats.runtimeTotal)
  eq('§1.1 纯 redirect', declared.pureRedirect, stats.pureRedirect)
  eq('§1.1 带命名动态段', declared.dynamic, stats.dynamic)
  eq('§1.1 glob 兜底', declared.glob, stats.glob)
  eq('§1.1 不需要登录', declared.publicCount, stats.publicCount)
  eq('§1.1 需要登录', declared.authCount, stats.authCount)
  eq('§1.1 权限档 anyUser', declared.anyUser, stats.tiers.anyUser)
  eq('§1.1 权限档 super', declared.super, stats.tiers.super)
  eq('§1.1 权限档 platformOps', declared.platformOps, stats.tiers.platformOps)
  eq('§1.1 权限档 providerConsole', declared.providerConsole, stats.tiers.providerConsole)
  eq('§1.2 标题条数 = 纯 redirect 实测', declared.s12Heading, stats.pureRedirect)
  eq('§1.2 表内行数 = 标题条数', declared.s12Rows, declared.s12Heading)
  eq('§1.3 标题条数 = 动态段实测', declared.s13Heading, stats.dynamic)
  eq('§1.3 表内行数 = 标题条数', declared.s13Rows, declared.s13Heading)
  eq('§3.13 标题条数 = 未覆盖实测', declared.s313Heading, report_uncoveredCount())
  eq('§3.13 表内路径数 = 标题条数', declared.s313PathCount, declared.s313Heading)

  const failed = rows.filter((r) => !r.pass)
  return { rows, failed }
}

function report_uncoveredCount() {
  // ★ 复用主流程已算出的「未被任何 flow 打开」条数（§3.13 的口径）。
  return uncovered.length
}

const report = {
  routerRouteCount: routerRoutes.length,
  stats,
  flowOpenCount: flowOpens.length,
  readmeListedCount: readmePaths.size,
  coveredCount: covered.length,
  uncoveredCount: uncovered.length,
  uncovered: uncovered.map((r) => ({
    path: r.path,
    redirect: r.redirect,
    component: r.component,
    meta: r.meta,
  })),
  readmeNotInRouter,
  routerNotInReadme: routerNotInReadme.map((r) => r.path),
  covered: covered.map((r) => ({ path: r.path, viaFlow: r.viaFlow })),
}

if (process.argv.includes('--check')) {
  // ★ 「README 声明的数字」与「当前树实测」逐项对拍 ⇒ 漂移即红。
  let out
  try {
    out = runCheck()
  } catch (e) {
    console.error('工具错误：' + e.message)
    process.exit(2)
  }
  const L = []
  L.push('# README 路由口径 ↔ 当前树 对拍（--check）')
  L.push('')
  L.push('| 判据 | README 声明 | 树实测 | |')
  L.push('|---|---|---|---|')
  for (const r of out.rows) {
    L.push(`| ${r.name} | ${r.declared === null ? '（读不到）' : r.declared} | ${r.actual} | ${r.pass ? '✔' : '❌'} |`)
  }
  L.push('')
  L.push(`RESULT: ${out.failed.length === 0 ? 'PASS' : 'FAIL'}（${out.rows.length - out.failed.length} / ${out.rows.length} 条一致）`)
  console.log(L.join('\n'))
  process.exit(out.failed.length === 0 ? 0 : 1)
} else if (process.argv.includes('--json')) {
  console.log(JSON.stringify(report, null, 2))
} else {
  const L = []
  L.push('# 路由覆盖审计（AST × flow × README §3）')
  L.push('')
  L.push('| 指标 | 数值 |')
  L.push('|---|---|')
  L.push(`| router.ts 路由条目（AST 提取，含子路由展开） | ${report.routerRouteCount} |`)
  L.push(`| flow 里 openLink 步数（全部 flow） | ${report.flowOpenCount} |`)
  L.push(`| README §3 登记的路由数 | ${report.readmeListedCount} |`)
  L.push(`| **已被 flow 打开过的路由** | **${report.coveredCount}** |`)
  L.push(`| **未被任何 flow 打开的路由** | **${report.uncoveredCount}** |`)
  L.push('')
  L.push('## 未覆盖路由')
  L.push('')
  L.push('| 路由 | 形态 |')
  L.push('|---|---|')
  for (const r of uncovered) {
    const kind = r.redirect ? `重定向 → ${r.redirect}` : r.component ? '页面' : '其它'
    L.push(`| \`${r.path}\` | ${kind} |`)
  }
  L.push('')
  L.push('## README §3 已登记但 router.ts 里没有')
  L.push('')
  for (const p of report.readmeNotInRouter) L.push(`- \`${p}\``)
  L.push('')
  L.push('## router.ts 有但 README §3 未登记')
  L.push('')
  for (const p of report.routerNotInReadme) L.push(`- \`${p}\``)
  console.log(L.join('\n'))
}
