#!/usr/bin/env node
/**
 * audit-flow-assertions.mjs —— 「flow 断言点 × 目标 URL 的现役渲染闭包」四方对拍
 * （2026-11-23 L14 轮：把 L14 的取证判据**工具化**，让它对**全部** flow 可复跑）
 *
 * ## 为什么要有这道工具
 *
 * ★★ L14 取证轮坐实的形态：上游 `95e44dc15` 把 `/admin/session-analytics/users`
 * 的 `component:` 由 `UserProfileListView` 改指 `SessionAnalyticsUsersView`
 * ⇒ ★ 上游 maestro `13-session-analytics-users.yaml` 的**列表页段恒红**
 * （★ 断言点在该 URL 的**现役渲染闭包**里 0 命中）。
 *
 * ★★ 而当时的存量工具都看不见它：
 *   · `audit-route-coverage.mjs` —— 判**路径**三方对拍（router × flow × README）
 *     ⇒ ★★ 同一个 URL 换了组件、断言随之失效，它**看不见**；
 *   · `navCoverage.test.ts` —— 判**路由**可达性 ⇒ ★★ 组件被 import 却没路由，它看不见；
 *   · `pageHelpRegistry.test.ts` —— 判**存在性** ⇒ ★★ 正文与页面形态是否一致不在判据里。
 *
 * ⇒⇒ **本工具补的是第四个维度：「这条 flow 断言的那个字/那个 testid，
 *      在它此刻打开的那个 URL 的现役渲染闭包里到底还在不在？」**
 *
 * ## 三档判定（★ **不判红绿**，只做事实登记）
 *
 *   `reachable`   —— 该断言点**在**目标页面的现役渲染闭包里可达（含壳层与 i18n 值）
 *   `not-found`   —— ★ **不在**闭包里，★ **但**该字面量**确实存在于仓内**
 *                    （locale 值或源码字面量）⇒ ⇒ **强候选：该页面渲染不出它**
 *   `undecidable` —— ★ 静态**判不了**：运行期数据（如登录用户的显示名）、
 *                    `${ENV}` 占位符、跨 token 的正则、或仓内根本不存在的字面量
 *                    ⇒ ⇒ **不作为候选**（★ 门自己的误报比没门更坏）
 *
 * ★ **退出码**：0 = 跑通（**不代表没有候选**）；1 = 工具自身出错。
 * ★ **升级为红门之前必须先人工过一遍 `not-found` 清单**。
 *
 * ## 用法
 *   node maestro/tools/audit-flow-assertions.mjs            # Markdown 报告
 *   node maestro/tools/audit-flow-assertions.mjs --json     # 机读
 *   node maestro/tools/audit-flow-assertions.mjs --flow 13   # 只看某个 flow（文件名前缀）
 */

import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs'
import { resolve, join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'
import { execFileSync } from 'node:child_process'

const HERE = dirname(fileURLToPath(import.meta.url))
const repoRoot = resolve(HERE, '..', '..')
const webRoot = join(repoRoot, 'web')

// ★ TypeScript 必须**显式指定解析位置**（沿用 maestro/tools/extract-routes.mjs 的
// ★ 同款范式与同款理由：仓内 typescript 装在 web/，从 maestro/tools/ 直接 import 会
// ★ ERR_MODULE_NOT_FOUND ⇒ **工具不可运行 ⇒ 产出不可复现**）。
const webRequire = createRequire(resolve(webRoot, 'package.json'))
const ts = webRequire('typescript')

const argv = process.argv.slice(2)
const JSON_OUT = argv.includes('--json')
const FLOW_FILTER = argv.includes('--flow') ? argv[argv.indexOf('--flow') + 1] : null

// ───────────────────────────────────────────────── 1. 路由表（TS AST）

function extractRoutes() {
  // ★ 路由表**复用既有工具** `extract-routes.mjs`（TS AST 提取，**已处理
  // ★ vue-router 的 children 扁平化**）⇒ ★ **单一真源**，本工具不重复实现路由提取
  // ★（★ 重复实现过一次 ⇒ 把 `/mcp/admin` 这类**子路由**误判成「无路由」⇒ ★ 噪音）。
  const raw = execFileSync(process.execPath, [join(HERE, 'extract-routes.mjs')], { encoding: 'utf8' })
  const parsed = JSON.parse(raw)

  const file = join(webRoot, 'src', 'router.ts')
  const src = readFileSync(file, 'utf8')
  const nameToFile = new Map()
  let m
  const importRe = /const\s+([A-Za-z0-9_]+)\s*=\s*\(\s*\)\s*=>\s*import\(\s*['"]([^'"]+)['"]\s*\)/g
  while ((m = importRe.exec(src))) nameToFile.set(m[1], m[2])
  const staticRe = /^import\s+([A-Za-z0-9_]+)\s+from\s+['"](\.[^'"]+)['"]/gm
  while ((m = staticRe.exec(src))) {
    if (nameToFile.has(m[1])) continue
    const p = join(webRoot, 'src', m[2])
    for (const cand of [p, p + '.ts', p + '.vue']) {
      if (existsSync(cand)) {
        nameToFile.set(m[1], './' + m[2].replace(/^\.\//, '').replace(/\.(ts|vue)$/, '') + (cand.endsWith('.vue') ? '.vue' : '.ts'))
        break
      }
    }
  }
  // ★ 函数式重定向（`redirect: (to) => ({ path: '/tenant/models', query: to.query })`）
  // ★ —— `extract-routes.mjs` **不导出**这类目标（它的输出里 redirect 为空）⇒ ★ 若不自己补，
  // ★ `/maas/*` `/catalog` 这类会被误判成「无组件」⇒ ★ 噪音。
  const redirectTargets = new Map()
  {
    const sf = ts.createSourceFile('router.ts', src, ts.ScriptTarget.Latest, true)
    const visit = (node) => {
      if (ts.isPropertyAssignment(node) && node.name.getText(sf).replace(/['"]/g, '') === 'redirect') {
        const init = node.initializer
        if (ts.isArrowFunction(init) || ts.isFunctionExpression(init)) {
          const grab = (n) => {
            if (ts.isPropertyAssignment(n) && n.name.getText(sf).replace(/['"]/g, '') === 'path') {
              const v = n.initializer
              if (ts.isStringLiteral(v) || ts.isNoSubstitutionTemplateLiteral(v)) {
                const obj = n.parent
                const owner = obj && obj.parent
                if (owner && ts.isPropertyAssignment(owner) && owner.name.getText(sf).replace(/['"]/g, '') === 'path') {
                  const p = owner.initializer
                  if (ts.isStringLiteral(p) || ts.isNoSubstitutionTemplateLiteral(p)) redirectTargets.set(p.text, v.text)
                }
              }
            }
            ts.forEachChild(n, grab)
          }
          grab(init)
        }
      }
      ts.forEachChild(node, visit)
    }
    visit(sf)
  }

  return parsed.map((r) => {
    const comp = r.component || null
    // ★ component 字段有两种形态：① 组件名（'CorrelationsView'）② 相对路径
    //   （'./views/CorrelationsView.vue'）⇒ ★ **两者都要转成绝对路径**（★ 只转前者
    //   ⇒ ★ 名字形态的路由全部变成「组件文件缺失」⇒ ★ 工具自己把 365 条判成异常）。
    const rel = comp ? (comp.startsWith('./') ? comp : (nameToFile.get(comp) || null)) : null
    const file = rel ? join(webRoot, 'src', rel.replace(/^\.\//, '')) : null
    return {
      path: r.path,
      component: comp,
      redirect: typeof r.redirect === 'string' ? r.redirect : (redirectTargets.get(r.path) || null),
      file: file && existsSync(file) ? file : null,
    }
  })
}

// ───────────────────────────────────────────────── 2. zh-CN locale 值表

function loadLocaleValues() {
  const dir = join(webRoot, 'src', 'locales', 'zh-CN')
  const values = new Map()
  if (!existsSync(dir)) return values
  for (const name of readdirSync(dir)) {
    if (!name.endsWith('.ts') || name === 'index.ts') continue
    const src = readFileSync(join(dir, name), 'utf8')
    let obj
    try {
      const js = ts.transpileModule(src, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 } }).outputText
      const module_ = { exports: {} }
      new Function('exports', 'module', js)(module_.exports, module_)
      obj = module_.exports && module_.exports.default ? module_.exports.default : module_.exports
    } catch {
      continue
    }
    const ns = name.replace(/\.ts$/, '')
    const walk = (node, prefix) => {
      if (!node || typeof node !== 'object') return
      for (const [k, v] of Object.entries(node)) {
        const key = prefix ? `${prefix}.${k}` : k
        if (typeof v === 'string') values.set(`${ns}.${key}`, v)
        else if (v && typeof v === 'object') walk(v, key)
      }
    }
    walk(obj, '')
  }
  return values
}

// ───────────────────────────────────────────────── 3. 渲染闭包（页面 + 壳层）

const IMPORT_RE = /(?:import\s+(?:[\w*{}\s,]+)\s+from\s+|import\s*\(\s*|require\(\s*)['"]([^'"]+)['"]/g

/**
 * ★★ **动态渲染检测**：页面若通过 `<component :is>` / `defineAsyncComponent` /
 * `import.meta.glob` / 组件注册表**在运行期决定渲染哪个子组件**，
 * ⇒ ★ **静态闭包天然不完整** ⇒ ★★ 「闭包里没有 ⇒ 页面渲染不出」这条推论**不成立**
 * ⇒ ⇒ ★ **这类页面的 not-found 一律降级为 undecidable**（★ 宁可漏报不可误报）。
 */
const DYNAMIC_MARKERS = [
  /defineAsyncComponent\s*\(/,
  /resolveComponent\s*\(/,
  /Vue\.component\s*\(/,
]

/**
 * ★ `import.meta.glob` **只有当它 glob 的是组件（.vue/.ts）时**才让闭包不完整；
 * ★ glob 的是 **markdown**（`*.md`）时**不**算（★ 页面帮助正文已按 registry 单篇取）。
 * ★ 同理 `<component :is=`：它的候选集就是同文件里静态 import 的那几个 ⇒ ★ 闭包仍完整。
 */
function hasComponentGlob(src) {
  for (const m of src.matchAll(/import\.meta\.glob\(\s*['"]([^'"]+)['"]/g)) {
    const pattern = m[1]
    // ★ glob 的是 **markdown** ⇒ 闭包不受影响（帮助正文已按 registry 单篇取）
    if (/\.md\b/.test(pattern)) continue
    return true
  }
  return false
}

function closureOf(seeds) {
  const seen = new Set()
  const stack = [...seeds]
  let dynamic = false
  while (stack.length) {
    const f = stack.pop()
    if (seen.has(f) || !existsSync(f)) continue
    seen.add(f)
    const src = readFileSync(f, 'utf8')
    for (const re of DYNAMIC_MARKERS) if (re.test(src)) { dynamic = true; break }
    if (hasComponentGlob(src)) dynamic = true
    IMPORT_RE.lastIndex = 0
    let m
    while ((m = IMPORT_RE.exec(src))) {
      const spec = m[1]
      if (!spec.startsWith('.')) continue
      const base = resolve(dirname(f), spec)
      for (const cand of [base, base + '.ts', base + '.vue', join(base, 'index.ts'), join(base, 'index.vue')]) {
        if (existsSync(cand) && statSync(cand).isFile()) { stack.push(cand); break }
      }
    }
  }
  return { files: seen, dynamic }
}

/** 全站壳层：任何路由页面都挂着它们（AppTopbar / 登录弹层 / 底部导航）。 */
const SHELL_FILES = [
  join(webRoot, 'src', 'components', 'shell', 'AppTopbar.vue'),
  join(webRoot, 'src', 'components', 'shell', 'UserMenuDropdown.vue'),
  join(webRoot, 'src', 'components', 'LoginModal.vue'),
].filter((f) => existsSync(f))

/** 闭包内可上屏的三类文本 + testid */
function textsOf(closure, localeValues, routePath) {
  const literals = new Set()
  const testids = new Set()
  const i18nValues = new Set()
  let hasHelpDrawer = false

  for (const f of closure) {
    // ★★ **locale 文件不进「可达文本」集合**（★ 否则整个 locale 包会被当成可达文本
    // ★ ⇒ 任何 locale 值都变成「页面渲染得出」⇒ ⇒ 工具恒绿、什么都报不出来）。
    // ★ locale 的**值**另走 i18nValues（只收闭包里真正 `t('key')` 引用到的那些键）。
    if (f.includes('/locales/')) {
      // 仍要收集该文件里被引用的 i18n 键（键名是字面量，与值无关）
      continue
    }
    const src = readFileSync(f, 'utf8')
    for (const m of src.matchAll(/data-testid\s*=\s*["']([^"']+)["']/g)) testids.add(m[1])
    if (f.endsWith('PageHelpDrawer.vue')) hasHelpDrawer = true
    for (const m of src.matchAll(/\bt\(\s*['"]([^'"]+)['"]/g)) {
      const v = localeValues.get(m[1])
      if (v !== undefined) i18nValues.add(v)
    }
    // ★★ 2026-11-23（L30 轮·补②⑨）：★ **i18n 函数的【别名】也要解析**。
    //
    //	★ 为什么必须补（★ 实测出来的第二个提取缺口）：
    //	★ 仓里大量组件把 `t` 包一层、**且带命名空间前缀**：
    //	★   `const { t } = useI18n()`
    //	★   `const pm = (k: string, params?) => t(`providers.${k}` as never, ...)`
    //	★ ⇒ ★★ 页面里写的是 **`pm('page.title')`** ⇒ **不是字面 `t('key')`**
    //	★ ⇒ ★★ 上一段（只认 `\bt\(`）**完全看不见这些文案** ⇒ 误判 not-found。
    //	★ ★★ 实测反例：★ `views/ProvidersView.vue:879` 渲染 `{{ pm('page.title') }}`
    //	★ 而 locale `providers.page.title` ＝ 「供应商管理」⇒ ★ 该页确实渲染得出。
    //	★ 全仓此类别名普遍存在（★ pm / mi / ps / pd …）。
    //
    //	★ 判据：★ 只认**形如「箭头函数体里调用了 t(」**的定义 ⇒ ★ 与 i18n 绑定；
    //	★ ⇒ ★ **不会**把 `const t = (ms: number) => new Date(...)` 这类同名局部变量
    //	★ 误当成 i18n 别名（★ 它的函数体里没有 `t(` 调用）。
    const aliasDefs = []
    for (const m of src.matchAll(/const\s+(\w+)\s*=\s*\(([^)]*)\)\s*(?::[^=]{0,80}?)?=>\s*([^\n;]{0,200})/g)) {
      const [, aliasName, params, body] = m
      // 形态①：带命名空间前缀 ⇒ t(`providers.${k}`)
      const nsMatch = body.match(/\bt\(\s*`([^`$]*)\$\{/)
      if (nsMatch) {
        aliasDefs.push({ name: aliasName, prefix: nsMatch[1] })
        continue
      }
      // 形态②：无前缀 ⇒ t(k) 且参数名与调用一致
      const firstParam = (params.split(',')[0] || '').split(':')[0].trim()
      const plainMatch = body.match(/\bt\(\s*(\w+)\s*\)/)
      if (plainMatch && firstParam && plainMatch[1] === firstParam) {
        aliasDefs.push({ name: aliasName, prefix: '' })
      }
    }
    for (const alias of aliasDefs) {
      const callRe = new RegExp(`\\b${alias.name}\\(\\s*['"\`]([^'"\`]+)['"\`]\\s*\\)`, 'g')
      for (const m of src.matchAll(callRe)) {
        const v = localeValues.get(alias.prefix + m[1])
        if (v !== undefined) i18nValues.add(v)
      }
    }
    for (const m of src.matchAll(/['"]([^'"\n]{2,80})['"]/g)) {
      const s = m[1]
      if (/[一-鿿]/.test(s) || /[A-Za-z]{4,}/.test(s)) literals.add(s)
    }
    // ★★ 2026-11-23（L30 轮·补②⑦）：★ **模板裸文本也要算「可达」**。
    //
    //	★ 为什么必须补这一段（★ 实测出来的工具缺陷、★ 不是推演）：
    //	★ 上一段只收**引号里的字符串字面量** ⇒ ★ 而 Vue 模板里绝大多数可见文案
    //	★ 是**裸文本节点**（★ `<h1>队列瀑布图</h1>` ★ 里一个字都没引号）
    //	★ ⇒ ★★ 于是「页面明明渲染得出、工具却判 not-found」。
    //	★ ★★ 实测反例（★ 2026-11-23）：★ `05-routing.yaml` 第 291 行断言
    //	★ 「队列瀑布图」、★ 页面 `/dispatch/waterfall` 的组件
    //	★ `views/DispatchWaterfallView.vue` **第 122 行就是 `<h1>队列瀑布图</h1>`**
    //	★ ⇒ 工具判它 not-found ⇒ ★ **纯工具误报**（★ 该页确实渲染得出）。
    //
    //	★ 口径：★ 只收 **标签之间的裸文本**（★ `>` 与 `<` 之间）、★ 长度 2–80、
    //	★ 含中日韩字符或 ≥4 位英文字母者；★ **先剥 HTML 注释**（★ 注释里的字
    //	★ 不上屏）；★ 不收属性值（★ 属性值已由上面的引号字面量那条覆盖）。
    const templateSrc = src.replace(/<!--[\s\S]*?-->/g, ' ')
    for (const m of templateSrc.matchAll(/>([^<>{}]{2,80})</g)) {
      const s = m[1].trim()
      if (!s) continue
      if (/[一-鿿]/.test(s) || /[A-Za-z]{4,}/.test(s)) literals.add(s)
    }
  }
  // 页面帮助正文：**只取该路由在 pageHelpRegistry 里登记的那一篇**
  // （★ 全量 page-help 文件都算「可达」⇒ 又一次把工具变成恒绿）
  const helpTexts = new Set()
  if (hasHelpDrawer && routePath) {
    const id = resolvePageHelpId(routePath)
    if (id) {
      const md = join(webRoot, 'content', 'page-help', `${id}.zh-CN.md`)
      if (existsSync(md)) {
        for (const line of readFileSync(md, 'utf8').split('\n')) {
          const s = line.replace(/^[#>*\-\s]+/, '').trim()
          if (s) helpTexts.add(s)
        }
      }
    }
  }
  return { literals, testids, i18nValues, helpTexts, all: new Set([...literals, ...i18nValues, ...helpTexts]) }
}

/** 路由 path ⇒ page-help 内容 id（读 pageHelpRegistry.ts，按最长前缀匹配）。 */
function loadPageHelpRegistry() {
  const file = join(webRoot, 'src', 'config', 'pageHelpRegistry.ts')
  const entries = []
  if (!existsSync(file)) return entries
  const src = readFileSync(file, 'utf8')
  for (const m of src.matchAll(/\{\s*match:\s*'([^']+)'\s*,\s*id:\s*'([^']+)'/g)) {
    entries.push({ match: m[1], id: m[2] })
  }
  entries.sort((a, b) => b.match.length - a.match.length)
  return entries
}
const PAGE_HELP_ENTRIES = loadPageHelpRegistry()
function resolvePageHelpId(routePath) {
  const p = routePath.replace(/\/+$/, '') || '/'
  for (const e of PAGE_HELP_ENTRIES) {
    if (e.match === p) return e.id
    if (p.startsWith(e.match.replace(/\/:[^/]+$/, '') + '/')) return e.id
  }
  return null
}

/** 全仓字面量集合（用于区分「仓内根本不存在」⇒ 运行期数据 ⇒ 不可判定）。 */
function repoWideLiterals(localeValues) {
  const set = new Set(localeValues.values())
  const walkDir = (dir) => {
    for (const entry of readdirSync(dir)) {
      const full = join(dir, entry)
      if (statSync(full).isDirectory()) { walkDir(full); continue }
      if (!/\.(vue|ts)$/.test(full)) continue
      if (/\.test\.ts$|\.spec\.ts$/.test(full)) continue
      const src = readFileSync(full, 'utf8')
      for (const m of src.matchAll(/['"]([^'"\n]{2,80})['"]/g)) {
        const s = m[1]
        if (/[一-鿿]/.test(s) || /[A-Za-z]{4,}/.test(s)) set.add(s)
      }
    }
  }
  walkDir(join(webRoot, 'src'))
  return set
}

/**
 * ★★ 「这个字面量仓内是否存在」必须用**包含**判据，不能用**相等**判据：
 * ★ 例：L14 的断言串 `request_logs_with_current_month` 在仓内只以**更长句子的一部分**存在
 * ★   （locale 值「…均来自网关 canonical 请求视图 request_logs_with_current_month：模型名 = …」）
 * ★ ⇒ ★ 相等判据会说「仓内不存在」⇒ ★ 判成不可判定 ⇒ ★ **L14 的信号被工具自己吃掉**。
 */
function repoKnows(core) {
  for (const t of repoTexts) if (t.includes(core)) return true
  return false
}

// ───────────────────────────────────────────────── 4. flow 解析

function normalizePath(raw) {
  let p = raw.replace(/^["']|["']$/g, '')
  p = p.replace(/\$\{BASE_URL\}/g, '')
  p = p.replace(/^https?:\/\/[^/]+/i, '')
  p = p.split('?')[0].split('#')[0]
  if (!p.startsWith('/')) p = '/' + p
  p = p.replace(/\/+$/, '')
  return p || '/'
}

function parseFlow(file) {
  const lines = readFileSync(file, 'utf8').split('\n')
  const steps = []
  let current = null
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    const open = line.match(/openLink:\s*(.+)$/)
    if (open) { current = normalizePath(open[1].trim()); steps.push({ kind: 'openLink', line: i + 1, path: current }); continue }
    const av = line.match(/^\s*-?\s*assertVisible:\s*['"](.+?)['"]\s*$/)
    if (av) { steps.push({ kind: 'assertVisible', line: i + 1, text: av[1], page: current }); continue }
    const anv = line.match(/^\s*-?\s*assertNotVisible:\s*['"](.+?)['"]\s*$/)
    if (anv) { steps.push({ kind: 'assertNotVisible', line: i + 1, text: anv[1], page: current }); continue }
    const tap = line.match(/^\s*-?\s*tapOn:\s*['"](.+?)['"]\s*$/)
    if (tap) { steps.push({ kind: 'tapOn', line: i + 1, text: tap[1], page: current }); continue }
    const blk = line.match(/^\s*(text|visible|id):\s*['"](.+?)['"]\s*$/)
    if (blk) {
      const ctx = lines.slice(Math.max(0, i - 4), i).join('\n')
      if (/tapOn|extendedWaitUntil|waitForAnimationToEnd/.test(ctx)) {
        steps.push({ kind: blk[1], line: i + 1, text: blk[2], page: current })
      }
    }
  }
  return steps
}

function matchRoute(path, routes) {
  const find = (p) => {
    const norm = p.replace(/\/+$/, '') || '/'
    const exact = routes.find((r) => r.path === norm)
    if (exact) return exact
    const segs = norm.split('/').filter(Boolean)
    for (const r of routes) {
      const rsegs = r.path.split('/').filter(Boolean)
      if (rsegs.length !== segs.length) continue
      let ok = true
      for (let i = 0; i < rsegs.length; i++) {
        if (rsegs[i].startsWith(':')) continue
        if (rsegs[i] !== segs[i]) { ok = false; break }
      }
      if (ok) return r
    }
    return null
  }
  let r = find(path)
  // ★ 重定向跟随（最多 3 跳）：`/maas/models` → `/tenant/models` ⇒ ★ 不跟就会把
  // ★ 「重定向路由」误判成「无路由」⇒ ★ 噪音。
  let hops = 0
  while (r && r.redirect && !r.file && hops < 3) {
    const target = r.redirect.replace(/\$\{[^}]*\}/g, 'x')
    const next = find(target)
    if (!next || next === r) break
    r = next
    hops++
  }
  return r
}

// ───────────────────────────────────────────────── 5. 判定

function classifySingle(pattern, texts, repoTexts) {
  if (/\$\{[^}]*\}/.test(pattern)) return 'undecidable'
  const core = pattern.replace(/^\.\*/, '').replace(/\.\*$/, '').trim()
  if (!core) return 'reachable'
  if (core.includes('.*')) {
    // 跨 token 正则：只有当某个候选文本整体匹配才算可达，否则静态不可判定
    let re
    try { re = new RegExp(pattern) } catch { return 'undecidable' }
    for (const t of texts.all) if (re.test(t)) return 'reachable'
    return 'undecidable'
  }
  if (/[\\^$[\](){}|+?*]/.test(core)) {
    let re
    try { re = new RegExp(pattern) } catch { return 'undecidable' }
    for (const t of texts.all) if (re.test(t)) return 'reachable'
    // ★★ 第 4 档：**字面量确实在页面上，但断言把它写成了正则** ⇒ ★ 该断言**恒红**
    //   （★ 例：visible: 'Auto-Route 关联分析 (Correlations)' ⇒ 正则里的 `(...)`
    //   ★ 是**分组** ⇒ 实际要匹配的是「Auto-Route 关联分析Correlations」（无空格无括号）
    //   ★ ⇒ 与页面真正渲染的「Auto-Route 关联分析 (Correlations)」**逐字不同**）
    for (const t of texts.all) if (t.includes(core)) return 'regex-mismatch'
    return repoKnows(core) ? 'not-found' : 'undecidable'
  }
  for (const t of texts.all) if (t.includes(core)) return 'reachable'
  return repoKnows(core) ? 'not-found' : 'undecidable'
}

function classify(pattern, texts, repoTexts) {
  const alts = pattern.split('|').map((s) => s.trim()).filter(Boolean)
  if (!alts.length) return 'undecidable'
  let sawUndecidable = false
  let sawMismatch = false
  for (const alt of alts) {
    const r = classifySingle(alt, texts, repoTexts)
    if (r === 'reachable') return 'reachable'
    if (r === 'undecidable') sawUndecidable = true
    if (r === 'regex-mismatch') sawMismatch = true
  }
  if (sawMismatch) return 'regex-mismatch'
  return sawUndecidable ? 'undecidable' : 'not-found'
}

// ───────────────────────────────────────────────── 6. 主流程

const routes = extractRoutes()
const localeValues = loadLocaleValues()
const repoTexts = repoWideLiterals(localeValues)
const flowsDir = join(repoRoot, 'maestro', 'flows')
const flows = readdirSync(flowsDir).filter((f) => f.endsWith('.yaml') || f.endsWith('.yml')).sort()

const cache = new Map()
function textsFor(componentFile, routePath) {
  const key = componentFile + '|' + routePath
  if (cache.has(key)) return cache.get(key)
  // ★ 页面自身闭包（决定「这个页面能不能渲染出它」）与壳层闭包（顶栏/登录弹层，恒在）
  // ★ **分开算** ⇒ ★ 动态性只看**页面自身**（★ 壳层本身就有动态导航 ⇒ 若混算则每页都
  // ★ 被判成「动态」⇒ ⇒ 工具恒不可判定 ⇒ 白做）。
  const pageClosure = closureOf([componentFile])
  const shellClosure = closureOf(SHELL_FILES)
  const merged = new Set([...pageClosure.files, ...shellClosure.files])
  const t = textsOf(merged, localeValues, routePath)
  t.dynamic = pageClosure.dynamic
  cache.set(key, t)
  return t
}

/** 全仓 data-testid 集合（用于区分「仓内根本没有这个 testid」⇒ 不可判定）。 */
const repoTestids = (() => {
  const set = new Set()
  const walkDir = (dir) => {
    for (const entry of readdirSync(dir)) {
      const full = join(dir, entry)
      if (statSync(full).isDirectory()) { walkDir(full); continue }
      if (!/\.vue$/.test(full)) continue
      const src = readFileSync(full, 'utf8')
      for (const m of src.matchAll(/data-testid\s*=\s*["']([^"']+)["']/g)) set.add(m[1])
    }
  }
  walkDir(join(webRoot, 'src'))
  return set
})()
function repoHasTestid(id) { return repoTestids.has(id) }

const report = []
let toolError = null
try {
  for (const flow of flows) {
    if (FLOW_FILTER && !flow.startsWith(FLOW_FILTER)) continue
    const steps = parseFlow(join(flowsDir, flow))
    const findings = []
    for (const st of steps) {
      if (st.kind === 'openLink' || !st.text) continue
      if (!st.page) continue
      const route = matchRoute(st.page, routes)
      if (!route) { findings.push({ ...st, verdict: 'no-route' }); continue }
      if (!route.file) { findings.push({ ...st, verdict: 'no-component' }); continue }
      const full = route.file
      if (!full || !existsSync(full)) { findings.push({ ...st, verdict: 'component-missing' }); continue }
      const texts = textsFor(full, route.path)
      let verdict = st.kind === 'id'
        ? (texts.testids.has(st.text) ? 'reachable' : (repoHasTestid(st.text) ? 'not-found' : 'undecidable'))
        : classify(st.text, texts, repoTexts)
      // ★ 页面闭包是**动态**的 ⇒ ★ 「渲染不出」这条推论**证据强度较低**
      // ★ ⇒ ★ **不降级为不可判定**（★ 降级会把 L14 本案也一起吃掉 ⇒ ★ 工具白做）
      // ★ ⇒ ★ 改用**分区呈现**：静态闭包页面的候选＝强候选；动态闭包页面的候选＝弱候选
      // ★   （★ 两者都要人工过一遍 ⇒ ★ 门自己的误报比没门更坏）
      findings.push({ ...st, verdict, pageDynamic: !!texts.dynamic, route: route.path, component: route.component })    }
    report.push({ flow, findings })
  }
} catch (e) {
  toolError = String(e && e.stack ? e.stack : e)
}

const pick = (v) => report.flatMap((r) => r.findings.filter((f) => f.verdict === v).map((f) => ({ flow: r.flow, ...f })))
const notFound = pick('not-found')
const regexMismatch = pick('regex-mismatch')
const undecidable = pick('undecidable')
const other = pick('no-route').concat(pick('no-component'), pick('component-missing'))

if (JSON_OUT) {
  console.log(JSON.stringify({ routes: routes.length, flows: report.length, notFound, regexMismatch, undecidable, other, toolError }, null, 2))
} else {
  console.log('# flow 断言点 × 现役渲染闭包 —— 四方对拍')
  console.log('')
  console.log(`路由数：${routes.length}　flow 数：${report.length}　zh-CN locale 键：${localeValues.size}　壳层文件：${SHELL_FILES.length}`)
  console.log('')
  const strong = notFound.filter((f) => !f.pageDynamic)
  const weak = notFound.filter((f) => f.pageDynamic)
  console.log(`## ★ 候选「断言点在现役页面不可达」（not-found）：${notFound.length} 条`)
  console.log(`### ★★ 强候选（页面闭包**静态可枚举** ⇒ 「渲染不出」是硬证据）：${strong.length} 条`)
  if (!strong.length) console.log('（无）')
  for (const f of strong) {
    console.log(`- \`${f.flow}\` 第 ${f.line} 行 · ${f.kind} · 页面 \`${f.page}\` → 路由 \`${f.route}\`（${f.component}）· 断言「${f.text}」`)
  }
  console.log('')
  console.log(`### ○ 弱候选（页面闭包**含动态组件** ⇒ 静态闭包不完整 ⇒ 需人工确认）：${weak.length} 条`)
  if (!weak.length) console.log('（无）')
  for (const f of weak) {
    console.log(`- \`${f.flow}\` 第 ${f.line} 行 · ${f.kind} · 页面 \`${f.page}\` → 路由 \`${f.route}\`（${f.component}）· 断言「${f.text}」`)
  }
  console.log('')
  console.log(`## ★★ 候选「断言写法错：字面量在页面上、但被写成了正则」（regex-mismatch）：${regexMismatch.length} 条`)
  if (!regexMismatch.length) console.log('（无）')
  for (const f of regexMismatch) {
    console.log(`- \`${f.flow}\` 第 ${f.line} 行 · ${f.kind} · 页面 \`${f.page}\` → 路由 \`${f.route}\`（${f.component}）· 断言「${f.text}」`)
  }
  console.log('')
  console.log(`## 静态不可判定（undecidable）：${undecidable.length} 条　★ 不作为候选`)
  if (!undecidable.length) console.log('（无）')
  for (const f of undecidable) {
    console.log(`- \`${f.flow}\` 第 ${f.line} 行 · ${f.kind} · 页面 \`${f.page}\` → \`${f.route}\`（${f.component}）· 断言「${f.text}」`)
  }
  console.log('')
  console.log(`## 其它异常（无路由 / 无组件 / 组件文件缺失）：${other.length} 条`)
  if (!other.length) console.log('（无）')
  for (const f of other) {
    console.log(`- \`${f.flow}\` 第 ${f.line} 行 · ${f.kind} · 页面 \`${f.page}\` · ${f.verdict} · 断言「${f.text || ''}」`)
  }
  console.log('')
  console.log('★ 口径：`not-found` ＝ 该字面量在仓内确实存在、但在该页现役渲染闭包里渲染不出来 ⇒ **强候选**')
  console.log('★ 口径：`regex-mismatch` ＝ 字面量**就在页面上**，但断言把它写成了正则（★ 括号/加号被当元字符）⇒ **该断言恒红**')
  console.log('★ `undecidable` ＝ 运行期数据 / `${ENV}` 占位符 / 跨 token 正则 / 仓内不存在的字面量 ⇒ **不作为候选**')
  console.log('★ **升级为红门之前必须先人工过一遍 `not-found` 清单**（门自己的误报比没门更坏）')
  if (toolError) console.log('\n★ 工具自身出错：\n' + toolError)
}

process.exit(toolError ? 1 : 0)
