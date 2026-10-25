#!/usr/bin/env node
// i18n-dead-key-guard.mjs — 「跨语种独有的死键」门（2026-10-27）
//
// ⚠️ 为什么需要这道门（2026-10-27 实测）：
//   2026-10-23 那轮把 `routing.ts` 的一段 23 行复制块从 7 个非源语种里删掉后，
//   `i18n-key-parity` 一直是 PASS —— 但那时**同一批文件里还躺着 288 个键**
//   （7 个非源语种比 zh-CN 多出来的顶层键，如 `dashboard_analytics_task`、
//   `policy_weights_price`、`overview_backLink` …）。
//   ⇒ **parity 门看不见它们**：parity 比的是「各语种键集合是否一致」，
//     而这批键在**所有非源语种里都存在**（连成一片）⇒ 集合差异 = 0 ⇒ PASS。
//   ⇒ 于是它们成了「**7 个语种共有、源语种没有**」的孤儿：
//     · 全仓 0 引用（连动态拼接都覆盖不到）
//     · 用的还是**已被嵌套命名取代的旧扁平键名**（`a_b_c` vs 真正的 `a.b.c`）
//     · 其中 131 个**还带着没翻译的中文原文**
//        （实测 `zh-TW/routing.ts:471` 的 `dashboard_analytics_task: '任务'`
//          —— 繁体文件里的简体；而 zh-TW 的 `requestTrace` 甚至仍是英文原文）
//   ⇒ 它们撑大每个语言包、污染 `untranslated` / `same-as-source` 两个指标，
//     而且**一旦有人按旧扁平名接线，文案会取到「另一个语种的译文」**。
//
// 判据（三条同时成立才算死键）：
//   1) 该键在**源语种 zh-CN 的同一命名空间下不存在**
//      （命名空间 = 语言包模块名，如 `sessions` / `routing`）
//   2) 该键**全仓 0 引用**（字面量 t()/tc()/$t()/i18n.t() + helper 展开 +
//      te()/tm()/localeArray() 的字面量）
//   3) 该键**不在任何动态键前缀下**
//      （`t(`a.b.${x}`)` 这类静态扫描看不见的取键 ⇒ 一律不判死）
//
// 用法：
//   node scripts/i18n-dead-key-guard.mjs             # 检查；发现死键 ⇒ exit 1
//   node scripts/i18n-dead-key-guard.mjs --report f  # 导出 JSON 清单（供修复脚本消费）
//   node scripts/i18n-dead-key-guard.mjs --selftest  # 自证（违规→红 / 干净→绿）
//
// ⚠️ 已知边界（写在输出里，不藏）：
//   · 动态键的根无法静态判定时 ⇒ **不判死**（宁可漏报，不误报）。
//   · 「所有语种共有、但全仓 0 引用」的键是**另一族**，本门不判
//     （它的判据需要「全部语种都缺」这一更强条件，单列在输出里）。

import { fileURLToPath, pathToFileURL } from 'node:url'
import { dirname, join, resolve } from 'node:path'
import { readFileSync, readdirSync, existsSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']
const SOURCE = 'zh-CN'

async function loadScanner() {
  const p = pathToFileURL(join(ROOT, 'src', 'i18n', 'scanner.ts')).href
  return await import(p)
}

// ---------------------------------------------------------------------------
// 动态键前缀
// ---------------------------------------------------------------------------

/**
 * ★★ 取键函数的**全部**写法（2026-10-27 第 13 增量实测枚举）。
 *
 *   ⚠️ 原实现只认 `t/tc/$t/i18n.t` 的**紧邻字面量** ⇒ 仓内实际存在的 6 种形态里
 *      **有 5 种它看不见** ⇒ 那些键被误判成死键（本轮实测误删了 **63 个活键**）：
 *        ① t('a.b') / tc / $t / i18n.t ………… 唯一被认的
 *        ② te('a.b') / tm('a.b') / **TE('a.b')（大写别名）** ……… 大写别名不命中
 *        ③ **localeArray<string[]>('a.b')（带泛型参数）** ……… 泛型参数挡在中间
 *        ④ **t(cond ? 'a.b' : 'c.d')（三元表达式）** ……… 字面量不在 `(` 之后
 *        ⑤ **t(变量)（运行时字符串）** ………… 键路径以字面量形式出现在**别处**
 *        ⑥ **错误前缀判定 `err.startsWith('a.b.')`** ……… 前缀字面量不是取键调用
 *
 *   ⇒ 修法（**只往保守方向修**）：
 *      · 动态前缀：模板串（全部调用形态）+ **以 `.` 结尾的点分字面量**
 *      · 静态引用：全部调用形态 + **所有「点分键路径形态」的字符串字面量**
 *   ⇒ 代价：会多判一些「活键」（保守 ⇒ 不误删）；收益：不再误删活键。
 */
const CALL_RE = String.raw`\$?t|tc|te|tm|TE|TM|TC|localeArray|i18n\.t`
/** 取键调用（允许泛型参数，如 `localeArray<string[]>(...)`） */
const CALL_CALL_RE = new RegExp(String.raw`\b(?:${CALL_RE})(?:<[^>]*>)?\s*\(\s*(['"\`])([A-Za-z][A-Za-z0-9_.\-]*)\1`, 'g')
/** 取键调用 + 模板串（动态前缀用） */
const CALL_TMPL_RE = new RegExp(String.raw`\b(?:${CALL_RE})(?:<[^>]*>)?\s*\(\s*\`([^\`]*)\``, 'g')
/** 点分键路径形态的字面量（`ns.path`） */
const KEYLIKE_LIT_RE = /(['"`])((?:[A-Za-z0-9_-]+\.)+[A-Za-z0-9_-]+)\1/g
/** 以 `.` 结尾的点分字面量（动态前缀） */
const PREFIX_LIT_RE = /(['"`])((?:[A-Za-z0-9_-]+\.)+)\1/g

/** 扫描源码里的「动态取键」，返回可静态判定的根前缀集合。 */
export function collectDynamicPrefixes(src) {
  const prefixes = new Set()
  const unresolved = []
  // 同一文件里的 `const X = '字面量'` —— 用来还原「变量在键头部」的情形
  const consts = new Map()
  for (const m of src.matchAll(/\bconst\s+([A-Za-z_$][\w$]*)\s*=\s*(['"`])([^'"`]*)\2/g)) {
    consts.set(m[1], m[3])
  }
  // ★★ 形态 ⑥：**以 `.` 结尾的点分字面量**就是动态前缀
  //   （实测：`error.startsWith('clientConfigDialog.error.')` ⇒ 其下的键全是活的）
  for (const m of src.matchAll(PREFIX_LIT_RE)) prefixes.add(m[2])
  // t(`...`) / tc(`...`) / $t(`...`) / i18n.t(`...`) / te / tm / TE / localeArray(`...`)
  for (const m of src.matchAll(CALL_TMPL_RE)) {
    const raw = m[1]
    if (!raw.includes('${')) { prefixes.add(raw); continue }
    const head = raw.slice(0, raw.indexOf('${'))
    if (head) { prefixes.add(head); continue }
    // 头部是变量 ⇒ 找同文件里的 const 字面量
    const varName = raw.slice(2, raw.indexOf('}'))
    const tail = raw.slice(raw.indexOf('}') + 1)
    const lit = consts.get(varName)
    if (lit !== undefined) prefixes.add(`${lit}${tail}`)
    else unresolved.push(raw)
  }
  return { prefixes, unresolved }
}

/** 把「源码里出现过的取键调用」全部收集成静态引用键集。 */
export function collectStaticRefKeys(src) {
  const keys = new Set()
  const add = (k) => { if (k) keys.add(k) }
  // t()/tc()/$t()/i18n.t()/te()/tm()/TE()/localeArray() 的字面量
  //   ★ 允许**泛型参数**（`localeArray<string[]>(...)`）与**大写别名**（`TE(...)`）
  for (const m of src.matchAll(CALL_CALL_RE)) add(m[2])
  // ★★ 形态 ④⑤：**所有「点分键路径形态」的字符串字面量**一律计入引用集。
  //   · 形态 ④（三元 `t(c ? 'a.b' : 'c.d')`）：字面量不在 `(` 之后 ⇒ 靠这条覆盖
  //   · 形态 ⑤（`t(变量)`）：键路径以字面量形式出现在赋值处 ⇒ 靠这条覆盖
  //   ⇒ **只往保守方向加**（多判活键 ⇒ 少删 ⇒ 不会误删）
  for (const m of src.matchAll(KEYLIKE_LIT_RE)) add(m[2])
  // ★ 属性承载的 i18n 键（labelKey / titleKey / hintKey）—— 与 scanner.ts 的 KEY_PROP_RE 保持一致。
  //   这些键在运行时被 `navLabel(item.labelKey, …)` 取用，值是配置里的**字面量** ⇒ 静态可解析。
  //   ★ 收录它们只会**减少**「判死」的数量（更保守），不会造成误删。
  for (const m of src.matchAll(/\b(?:labelKey|titleKey|hintKey)\s*:\s*(['"])([A-Za-z][A-Za-z0-9_.\-]*)\1/g)) add(m[2])
  // helper 展开：const XX = (k) => t('prefix.' + k)
  const helpers = new Map()
  for (const m of src.matchAll(/\bconst\s+(\w+)\s*=\s*\(\s*k\s*(?::\s*string)?\s*(?:,\s*[^)]*)?\)\s*:\s*string\s*=>\s*\w+\(\s*(['"`])((?:[^'"`\\]|\\.)*?)\1\s*\+\s*k\b/g)) {
    helpers.set(m[1], m[2].replace(/\.+$/, ''))
  }
  for (const m of src.matchAll(/\bconst\s+(\w+)\s*=\s*\(\s*k\s*(?::\s*string)?\s*(?:,\s*[^)]*)?\)\s*:\s*string\s*=>\s*\w+\(\s*`([^`]+)\$\{k\}`/g)) {
    helpers.set(m[1], m[2].replace(/\.+$/, ''))
  }
  for (const [name, prefix] of helpers) {
    const re = new RegExp(`\\b${name}\\s*\\(\\s*(['"])([A-Za-z][A-Za-z0-9_.\\-]*)\\1`, 'g')
    for (const m of src.matchAll(re)) add(`${prefix}.${m[2]}`)
  }
  return keys
}

// ---------------------------------------------------------------------------
// 键树
// ---------------------------------------------------------------------------

/**
 * ★ 死键口径的**唯一实现**（2026-10-27 第 13 增量提取为导出函数）。
 *
 *   为什么要提取：★ 新增的 `i18n-shared-dead-count.mjs`（棘轮门）要**同一份口径**，
 *   而「同一口径只能有一份实现」—— 否则两个门各算各的，
 *   就会出现「死键门说 3582、棘轮门说 3400」这种无法对拍的读数。
 *
 *   口径（与本门原先内联的完全一致）：
 *     ① 8 语种的**叶子**键集合（命名空间 = 模块名；容器路径不算键）
 *     ② 源码静态引用集（t/tc/$t/i18n.t/te/tm/localeArray 字面量 + 属性承载键 + helper 展开）
 *     ③ 动态键前缀（根不可判定的调用点一律不判死）
 *     ④ 判死：跨语种独有的 = 源语种没有 + 全仓 0 引用 + 不在动态前缀下
 *     ⑤ sharedDead：8 语种**共有** + 全仓 0 引用 + 不在动态前缀下
 */
export async function computeDeadKeyFacts(loadLocale) {
  // ① 8 语种的键集合（命名空间 = 模块名）
  //    ⚠️ 死键判定用**叶子**口径：容器路径不是「键」（没人会 t() 一个对象），
  //       且「非源语种独有的容器」⇒ 其整棵子树都是非源独有 ⇒ 叶子口径照样能整块删。
  const perLocale = new Map()
  const perLocaleValues = new Map()
  const nsToFile = new Map()
  for (const loc of LOCALES) {
    const mod = loadLocale(loc, join(ROOT, 'src', 'locales'))
    perLocale.set(loc, flattenLeaves(mod, '', new Set()))
    perLocaleValues.set(loc, flattenValues(mod, '', new Map()))
    for (const ns of Object.keys(mod)) {
      if (!nsToFile.has(ns)) {
        // index.ts 里的别名（public: publicPortal）⇒ 命名空间与文件名可能不同
        const guess = join(ROOT, 'src', 'locales', loc, `${ns}.ts`)
        nsToFile.set(ns, existsSync(guess) ? `${ns}.ts` : null)
      }
    }
  }
  // 别名修正：public → publicPortal.ts
  for (const loc of LOCALES) {
    const idx = join(ROOT, 'src', 'locales', loc, 'index.ts')
    if (!existsSync(idx)) continue
    const src = readFileSync(idx, 'utf8')
    for (const m of src.matchAll(/^\s*(\w+)\s*:\s*(\w+)\s*,?\s*$/gm)) {
      if (m[1] !== m[2] && !nsToFile.get(m[1])) {
        const f = `${m[2]}.ts`
        if (existsSync(join(ROOT, 'src', 'locales', loc, f))) nsToFile.set(m[1], f)
      }
    }
  }
  const sourceKeys = perLocale.get(SOURCE)

  // ② 源码静态引用集 + 动态键前缀
  const refKeys = new Set()
  const dynPrefixes = new Set()
  const unresolvedDyn = []
  const walk = (dir) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name)
      if (e.isDirectory()) { if (e.name === 'locales' || e.name === 'node_modules') continue; walk(p); continue }
      if (!/\.(vue|ts|tsx|js|mjs)$/.test(e.name)) continue
      // ★★ 2026-10-27 第 13 增量：**测试文件也必须进引用集**。
      //   原实现跳过 `*.test.ts` / `*.spec.ts` ⇒ 「只被测试引用」的键被判死
      //   ⇒ 实测 3 个键（`public.support.emailPlaceholder` / `nav.item.pluginSessions` /
      //   `routing.overview.loading`）被删 ⇒ **测试直接编译不过**。
      //   ⇒ 测试里引用的键也是**活键**（删了会打断构建）⇒ 必须进引用集。
      const src = readFileSync(p, 'utf8')
      for (const k of collectStaticRefKeys(src)) refKeys.add(k)
      const { prefixes, unresolved } = collectDynamicPrefixes(src)
      for (const p of prefixes) dynPrefixes.add(p)
      for (const u of unresolved) unresolvedDyn.push({ file: p.replace(ROOT + '/', ''), raw: u })
    }
  }
  walk(join(ROOT, 'src'))

  // ③ 判死（跨语种独有）
  const dead = []
  for (const loc of LOCALES) {
    if (loc === SOURCE) continue
    for (const key of perLocale.get(loc)) {
      if (sourceKeys.has(key)) continue
      if (refKeys.has(key)) continue
      let dyn = false
      for (const p of dynPrefixes) if (key.startsWith(p)) { dyn = true; break }
      if (dyn) continue
      dead.push({ locale: loc, key, ns: key.split('.')[0] })
    }
  }
  dead.sort((a, b) => (a.key === b.key ? a.locale.localeCompare(b.locale) : a.key.localeCompare(b.key)))

  // ④ 另一族：所有语种共有、但全仓 0 引用
  //    ⚠️ 同样要套动态键前缀保护，否则虚高；
  //    ⚠️ 且用**叶子**口径 —— 含容器时这个数会虚高（`common` / `common.button`
  //       这类结构节点灌的水）。
  const sharedDead = [...perLocale.get(SOURCE)].filter((k) => {
    for (const loc of LOCALES) if (loc !== SOURCE && !perLocale.get(loc).has(k)) return false
    if (refKeys.has(k)) return false
    for (const p of dynPrefixes) if (k.startsWith(p)) return false
    return true
  })

  return { perLocale, perLocaleValues, nsToFile, sourceKeys, refKeys, dynPrefixes, unresolvedDyn, dead, sharedDead }
}

/** 把运行时消息树摊平成「命名空间.路径」→ 叶子键集合。
 *  ⚠️ **只收叶子**（值是标量或数组）—— 容器路径（`common`、`common.button` …）
 *     不是「键」：没有任何代码会 `t('common.button')`（vue-i18n 对对象消息也取不到）。
 *     ⇒ 把容器算进键集合 ⇒ 「0 引用的键」会被容器路径灌水
 *     （实测：含容器时 4600，**只算叶子时 0** ⇒ 那一族根本不存在，是量具口径的伪影）。 */
export function flattenLeaves(obj, prefix, out) {
  for (const [k, v] of Object.entries(obj || {})) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      flattenLeaves(v, key, out)
    } else {
      out.add(key)
    }
  }
  return out
}

/** 把运行时消息树摊平成「命名空间.路径」→ 全部路径（含容器） */
/**
 * ★ 与 flattenLeaves 同口径的**值表**（2026-10-27 第 13 增量）。
 *   为什么需要：死键判定用的键集合来自本门（`flattenLeaves`），
 *   而 `localeValueQuality.ts` 的 loader **不收数组** ⇒ 两个口径的键集合不同
 *   （实测 `sessions.list.tableHeaders` 是数组：守门算它是叶子，loader 说没有这个键）
 *   ⇒ 删除动作必须用**同一份**口径，否则会出现「删不掉的键」与「8 语种结构不一致」。
 */
export function flattenValues(obj, prefix, out = new Map()) {
  for (const [k, v] of Object.entries(obj || {})) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      flattenValues(v, key, out)
    } else {
      out.set(key, v)
    }
  }
  return out
}

export function flattenTree(obj, prefix, out) {
  for (const [k, v] of Object.entries(obj || {})) {
    const key = prefix ? `${prefix}.${k}` : k
    if (v && typeof v === 'object' && !Array.isArray(v)) {
      out.add(key)
      flattenTree(v, key, out)
    } else {
      out.add(key)
    }
  }
  return out
}

// ---------------------------------------------------------------------------
// 自证
// ---------------------------------------------------------------------------

function selftest() {
  const cases = []
  // ⚠️ 比较必须是**深比较**：数组用 `===` 是引用相等 ⇒ 两个内容相同的数组会被判成「不等」
  //    （实测踩到过：A10 的期望/实测打印出来一模一样却判红 ⇒ 差点以为工具坏了）
  const deepEq = (a, b) => {
    if (a === b) return true
    if (typeof a !== typeof b) return false
    if (a === null || b === null || typeof a !== 'object') return false
    return JSON.stringify(a) === JSON.stringify(b)
  }
  const check = (id, desc, expected, actual) => cases.push({ id, desc, expected, actual, ok: deepEq(expected, actual) })

  // 判据函数（与 main 里同一套）
  const isDead = ({ key, sourceKeys, refKeys, dynPrefixes }) => {
    if (sourceKeys.has(key)) return false
    if (refKeys.has(key)) return false
    for (const p of dynPrefixes) if (key.startsWith(p)) return false
    return true
  }
  const base = { sourceKeys: new Set(['a.keep']), refKeys: new Set(['a.used']), dynPrefixes: ['a.dyn.'] }

  // A1 源语种没有 + 0 引用 + 不在动态前缀下 ⇒ 死键
  check('A1', '源语种没有 + 0 引用 + 非动态 ⇒ 判死', true,
    isDead({ ...base, key: 'a.ghost' }))
  // A2 源语种有 ⇒ 不判死
  check('A2', '源语种有该键 ⇒ 不判死', false,
    isDead({ ...base, key: 'a.keep' }))
  // A3 有静态引用 ⇒ 不判死
  check('A3', '有静态引用 ⇒ 不判死', false,
    isDead({ ...base, key: 'a.used' }))
  // A4 落在动态键前缀下 ⇒ 不判死
  check('A4', '动态键前缀下 ⇒ 不判死', false,
    isDead({ ...base, key: 'a.dyn.anything' }))
  // A5 动态前缀本身也要保护（`t(`a.dyn.`)`）
  check('A5', '动态前缀字面量本身 ⇒ 不判死', false,
    isDead({ ...base, key: 'a.dyn.' }))
  // A6 动态前缀提取：变量在头部且同文件有 const 字面量 ⇒ 可还原
  const src = "const tip = 'dashboard.liveStream.tooltip'\nconst x = t(`${tip}.status`)\n"
  const { prefixes, unresolved } = collectDynamicPrefixes(src)
  check('A6', '变量头部 + 同文件 const 字面量 ⇒ 根可还原', true,
    prefixes.has('dashboard.liveStream.tooltip.status'))
  // A7 动态前缀提取：根不可还原 ⇒ 记为 unresolved（宁可漏报）
  const src2 = "const x = t(`${unknownVar}.status`)\n"
  const r2 = collectDynamicPrefixes(src2)
  check('A7', '根不可还原 ⇒ 记 unresolved 不静默放过', true,
    r2.unresolved.length === 1)
  // A8 静态引用收集：te()/tm()/localeArray() 的字面量也要算引用
  const refKeys = collectStaticRefKeys("const a = te('x.y')\nconst b = tm('x.z')\nconst c = localeArray('x.w')\n")
  check('A8', 'te/tm/localeArray 字面量计入引用集', true,
    refKeys.has('x.y') && refKeys.has('x.z') && refKeys.has('x.w'))
  // A9 夹具自带命中数声明：死键判据在「全绿夹具」上必须 0 命中
  const cleanKeys = ['a.keep', 'a.used', 'a.dyn.foo']
  const hits = cleanKeys.filter((k) => isDead({ ...base, key: k }))
  check('A9', '干净夹具上死键命中数 = 0', 0, hits.length)

  // A10 容器路径**不算键**（没人会 t() 一个对象；实测含容器时「共有 0 引用键」
  //      是 4600，只算叶子时是 0 ⇒ 那一族并不存在，是量具口径的伪影）
  const leafKeys = [...flattenLeaves({ a: { b: 'x', c: { d: 'y' } } }, '', new Set())]
  check('A10', '容器路径不进键集合（只收叶子）', ['a.b', 'a.c.d'], leafKeys)
  const treeKeys = [...flattenTree({ a: { b: 'x', c: { d: 'y' } } }, '', new Set())]
  check('A10b', '全路径口径仍含容器（供对照）', ['a', 'a.b', 'a.c', 'a.c.d'], treeKeys)

  // A11 夹具必须能抓住「自证助手自己写错」—— 用两个内容相同但不同实例的数组
  check('A11', '自证助手对数组用深比较（不是引用相等）', true,
    deepEq(['a', 'b'], ['a', 'b']))

  // ── B 组：2026-10-27 第 13 增量新增（★ 「取键形态」实测枚举出来的 5 个盲区）
  // B1 ★ 大写别名 `TE('x.y')`（vue-i18n 的 `te` 在仓内被别名成大写）
  check('B1', '大写别名 TE() 的字面量计入引用集', true,
    collectStaticRefKeys("const a = TE('x.y')\n").has('x.y'))
  // B2 ★ 泛型参数：`localeArray<string[]>('x.y')`
  check('B2', '带泛型参数的 localeArray<string[]>() 计入引用集', true,
    collectStaticRefKeys("const a = localeArray<string[]>('x.y')\n").has('x.y'))
  // B3 ★ 三元表达式：`t(cond ? 'x.y' : 'x.z')`
  check('B3', '三元表达式里的字面量计入引用集', true,
    collectStaticRefKeys("const a = t(c ? 'x.y' : 'x.z')\n").has('x.y') &&
    collectStaticRefKeys("const a = t(c ? 'x.y' : 'x.z')\n").has('x.z'))
  // B4 ★ 运行时字符串：`const e = 'x.y'; t(e)` —— 键路径字面量在别处出现
  check('B4', '运行时字符串里的键路径字面量计入引用集', true,
    collectStaticRefKeys("const e = 'x.y'\nt(e)\n").has('x.y'))
  // B5 ★ 错误前缀判定：`err.startsWith('x.error.')` ⇒ 其下的键全是活的
  const p5 = collectDynamicPrefixes("const ok = err.startsWith('x.error.')\n")
  check('B5', '以 . 结尾的点分字面量被收成动态前缀', true, p5.prefixes.has('x.error.'))
  // B6 ★ 大写别名的模板串：`TE(\`x.byStatus.${status}\`)` ⇒ 前缀 x.byStatus.
  const p6 = collectDynamicPrefixes("const a = TE(`x.byStatus.${status}`)\n")
  check('B6', '大写别名的模板串前缀被收集', true, p6.prefixes.has('x.byStatus.'))
  // B7 ★★ 反向自证：这些形态下的键**必须不判死**
  const base2 = { sourceKeys: new Set(), refKeys: collectStaticRefKeys(
    "const e = 'x.y'\nconst a = TE('x.b')\nconst c = localeArray<string[]>('x.c')\nconst d = t(k ? 'x.d' : 'x.e')\nconst ok = e.startsWith('x.f.')\n"),
    dynPrefixes: [...collectDynamicPrefixes("const ok = e.startsWith('x.f.')\nconst g = TE(`x.g.${v}`)\n").prefixes] }
  const mustLive = ['x.y', 'x.b', 'x.c', 'x.d', 'x.e', 'x.f.any', 'x.g.any']
  const wronglyDead = mustLive.filter((k) => isDead({ ...base2, key: k }))
  check('B7', '六种取键形态下的键一律不判死', [], wronglyDead)

  return cases
}

// ---------------------------------------------------------------------------

async function main() {
  const argv = process.argv.slice(2)
  if (argv.includes('--selftest')) {
    const cases = selftest()
    let bad = 0
    for (const c of cases) {
      const mark = c.ok ? '✓' : '✗'
      if (!c.ok) bad++
      console.log(`  ${mark} ${c.id} ${c.desc}（期望 ${JSON.stringify(c.expected)}，实测 ${JSON.stringify(c.actual)}）`)
    }
    console.log(`\n══ 自证 ${cases.length - bad}/${cases.length} ══`)
    process.exit(bad ? 1 : 0)
  }

  const scanner = await loadScanner()
  const { loadLocale } = scanner

  const facts = await computeDeadKeyFacts(loadLocale)
  const { perLocale, nsToFile, sourceKeys, refKeys, dynPrefixes, unresolvedDyn, dead, sharedDead } = facts

  console.log('口径：源语种 = zh-CN；命名空间 = 语言包模块名；引用集 = 字面量 t/tc/$t/i18n.t/te/tm/localeArray + helper 展开；动态键前缀下的键一律不判死')
  console.log(`静态引用键：${refKeys.size} 个；动态键前缀：${dynPrefixes.size} 个`)
  if (unresolvedDyn.length) {
    console.log(`⚠ 根不可判定的动态键 ${unresolvedDyn.length} 处（这些位置一律不判死）：`)
    for (const u of unresolvedDyn) console.log(`   ${u.file}  ${u.raw}`)
  }
  console.log(`\n跨语种独有的死键：${dead.length} 个`)
  console.log(`（另：所有语种共有但 0 引用的【叶子】键 ${sharedDead.length} 个 —— 本门不判；若该数为 0，说明「共有死键」这一族并不存在）`)

  const reportIdx = argv.indexOf('--report')
  if (reportIdx >= 0 && argv[reportIdx + 1]) {
    const out = argv[reportIdx + 1]
    const { writeFileSync } = await import('node:fs')
    writeFileSync(out, JSON.stringify({ dead, sharedDead, source: SOURCE, locales: LOCALES, nsToFile: Object.fromEntries(nsToFile) }, null, 2))
    console.log(`\n清单已导出：${out}`)
  }

  if (!dead.length) {
    console.log('\n✅ 没有「跨语种独有的死键」—— PASS')
    process.exit(0)
  }
  console.log('\n✗ 发现死键（按语言包模块分组）：')
  const byNs = new Map()
  for (const d of dead) {
    const ns = d.key.split('.')[0]
    if (!byNs.has(ns)) byNs.set(ns, [])
    byNs.get(ns).push(d)
  }
  for (const [ns, rows] of byNs) {
    const locs = [...new Set(rows.map((r) => r.locale))].sort()
    console.log(`\n  ${ns}  (${rows.length} 处 / 涉及 ${locs.length} 个语种: ${locs.join(', ')})`)
    const show = [...new Set(rows.map((r) => r.key))].sort()
    for (const k of show.slice(0, 8)) console.log(`      ${k}`)
    if (show.length > 8) console.log(`      … 共 ${show.length} 个键路径`)
  }
  console.log('\n   修法：这些键在源语种不存在、且全仓 0 引用 ⇒ 直接从对应语言包里删除。')
  process.exit(1)
}

// ★ 只有「直接运行时」才跑 main；被 import 时**只导出函数**，供其它门复用
//   （2026-10-27 第 13 增量：新增 i18n-shared-dead-count.mjs 复用本门的
//    collectStaticRefKeys / collectDynamicPrefixes —— 但 import 旧版会直接跑 main
//    并 process.exit(0) ⇒ 把复用方的脚本整个掐掉。）
const isDirectRun = !!process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href
if (isDirectRun) {
  main().catch((e) => {
    console.error('FATAL: ' + (e && e.stack ? e.stack : e))
    process.exit(2)
  })
}
