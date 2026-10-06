// verify-i18n-parity.selftest.mjs — 给 i18n 键集门自己搭前提
//
// 照 verify-touch-targets.selftest.mjs 的教训：一道只会放行的门，
// 与一个坏掉的门，在绿灯时完全无法区分。
//
// 本门要挡的缺陷在本专题真实发生过（2026-10-06 §11.20 / §11.29）：
//   · 键插错词典（`usage.costTrend` 落进 `models:`）→ 运行时显示字面量
//   · 两侧插到不同词典（`keys.errForbidden` zh 在 keys、en 在 home）
//     → **英文用户看到裸键名**，而中文用户完全正常 ⇒ 没有任何中文侧测试能发现
//   第二条尤其说明为什么必须有静态门：缺陷只在一侧存在，
//   靠「跑一遍页面看看」只能发现出问题的那一侧。
//
// 样本覆盖 5 组（正反两侧都有 —— 只测「该过的」是判据自证的陷阱）：
//   1. 正例：键集完全一致 → 放行
//   2. 反例：仅 zh 多一个键 → 拦下（并要报出键名）
//   3. 反例：仅 en 多一个键 → 拦下
//   4. ★ 反例：值 === 键名（漏翻译）→ 拦下
//   5. ★ 反例：词典文件缺失 / 解析出 0 键 → 报错而非静默放行
//
// 用法：node scripts/verify-i18n-parity.selftest.mjs

import { mkdtempSync, writeFileSync, rmSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

const HERE = dirname(fileURLToPath(import.meta.url))
const GATE = join(HERE, 'verify-i18n-parity.mjs')

let pass = 0
let fail = 0

/**
 * @param views 视图文件内容数组；`omitViews` 用来造「目录缺失」的反例。
 *
 * ★ 2026-10-07：新增 `src/views/` 是**必需**的。第 4 条判据要抽视图里的
 *   `t('...')` 键，而「目录扫不到」必须是失败而不是放行 ——
 *   扫不到却报 OK 的门比没有门更坏（与词典文件缺失同一判据）。
 *   ⇒ 自测样本若不建这个目录，第 6 组会**因为一个与被测行为无关的原因**
 *     失败（第一次加这条判据时就是这样：14 条里 8 条红了），
 *     报出来的问题还指向「重复键」而不是「目录缺失」。
 */
function makeRepo({ zh, en, omitZh = false, omitEn = false, views, omitViews = false } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'i18n-selftest-'))
  mkdirSync(join(root, 'src/i18n'), { recursive: true })
  mkdirSync(join(root, 'scripts'), { recursive: true })
  if (!omitViews) {
    mkdirSync(join(root, 'src/views'), { recursive: true })
    // ★ 默认给一个**不引用任何键**的视图。
    //   早先默认写 t('a.b')，而 [6]/[6b] 的样本词典里没有 a.b
    //   ⇒ 第 4 条判据在测「重复键」的用例里报「视图引用了不存在的键」，
    //   **报的 cause 与被测行为无关**（同一个坑：失败原因要指向真正的成因）。
    //   要测第 4 条的用例自己传 `views`。
    const files = views ?? ['<template><p>no i18n here</p></template>' + NL]
    files.forEach((src, i) => writeFileSync(join(root, 'src/views', `V${i}.vue`), src))
  }
  if (!omitZh) writeFileSync(join(root, 'src/i18n/zh-CN.ts'), zh ?? "export const zhCN = {\n  a: {\n    b: '值',\n  },\n} as const\n")
  if (!omitEn) writeFileSync(join(root, 'src/i18n/en-US.ts'), en ?? "export const enUS = {\n  a: {\n    b: 'value',\n  },\n} as const\n")
  writeFileSync(join(root, 'scripts/verify-i18n-parity.mjs'), execFileSync('cat', [GATE]))
  return root
}

function runGate(root) {
  try {
    return { code: 0, out: execFileSync('node', [join(root, 'scripts/verify-i18n-parity.mjs')], { encoding: 'utf8' }) }
  } catch (e) {
    return { code: e.status ?? 1, out: `${e.stdout ?? ''}${e.stderr ?? ''}` }
  }
}

function check(name, cond, detail = '') {
  if (cond) { pass++; console.log(`  ✓ ${name}`) }
  else { fail++; console.error(`  ✗ ${name}${detail ? `\n      ${String(detail).trim().slice(0, 300)}` : ''}`) }
}

// ★ 样本必须与真实词典**同形**：每行一个键、嵌套对象独立成行、末行 `} as const`。
//   解析器是按行工作的（刻意不 import，避免依赖编译链），
//   写成 `{ a: { b: 'x' } }` 这种单行紧凑形会解析出 0 键 ——
//   那不是门的 bug，是样本与被测格式不一致（见 §11.29 的教训）。
const NL = String.fromCharCode(10)
const doc = (body) => `export const D = {\n${body}\n} as const\n`

// ── 1. 正例 ─────────────────────────────────────────────────────────────
{
  console.log('\n[1] 正例：键集一致放行')
  const root = makeRepo({
    zh: doc("  nav: {\n    home: '首页',\n    more: '更多',\n  },\n  keys: {\n    requests: '请求数',\n    errForbidden: '无权限',\n  },"),
    en: doc("  nav: {\n    home: 'Home',\n    more: 'More',\n  },\n  keys: {\n    requests: 'Requests',\n    errForbidden: 'No permission',\n  },"),
  })
  const r = runGate(root)
  check('键集一致 → 放行', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 2. 仅 zh 多键 ───────────────────────────────────────────────────────
{
  console.log('\n[2] 反例：仅 zh-CN 多一个键')
  const root = makeRepo({
    zh: doc("  nav: {\n    home: '首页',\n  },\n  keys: {\n    errForbidden: '无权限',\n  },"),
    en: doc("  nav: {\n    home: 'Home',\n  },\n  keys: {\n    requests: 'Requests',\n  },"),
  })
  const r = runGate(root)
  check('仅 zh 多键 → 拦下', r.code === 1, r.out)
  check('报出了具体键名 keys.errForbidden', r.out.includes('keys.errForbidden'), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 3. 仅 en 多键 ───────────────────────────────────────────────────────
{
  console.log('\n[3] 反例：仅 en-US 多一个键（本专题真实发生过的形态）')
  const root = makeRepo({
    zh: doc("  keys: {\n    requests: '请求数',\n  },"),
    en: doc("  home: {\n    errForbidden: 'No permission',\n  },\n  keys: {\n    requests: 'Requests',\n  },"),
  })
  const r = runGate(root)
  check('仅 en 多键 → 拦下', r.code === 1, r.out)
  check('报出 home.errForbidden', r.out.includes('home.errForbidden'), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 4. 值 === 键名（漏翻译）────────────────────────────────────────────
{
  console.log('\n[4] 反例：值写成键名本身（运行时显示裸键）')
  const root = makeRepo({
    zh: doc("  nav: {\n    home: '首页',\n  },"),
    en: doc("  nav: {\n    home: 'home',\n  },"),
  })
  const r = runGate(root)
  check("值 === 键名 → 拦下", r.code === 1, r.out)
  check('报出未翻译提示', r.out.includes('未翻译'), r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 5. ★ 缺文件 / 解析出 0 键 ─────────────────────────────────────────
{
  console.log('\n[5] 反例：词典缺失不得静默放行')
  const root = makeRepo({ omitEn: true })
  const r = runGate(root)
  check('en-US 缺失 → 报错', r.code === 1, r.out)
  rmSync(root, { recursive: true, force: true })
}
{
  console.log('\n[5b] 反例：解析出 0 键（路径对但格式变了）')
  const root = makeRepo({ zh: 'export const zhCN = {}\n', en: 'export const enUS = {}\n' })
  const r = runGate(root)
  check('两侧都 0 键 → 报错', r.code === 1, r.out)
  rmSync(root, { recursive: true, force: true })
}

// ── 6. ★ 重复键（2026-10-07 新增判据）──────────────────────────────────
//   实况：本专题把 `nav.heatmap` 插了两次。键集一致、值也正常，
//   旧门**全程绿**（parseDict 用 Map.set，后者被静默覆盖）；
//   只有 vue-tsc 的 TS1117 抓到。build 前置里 i18n 门跑在 vue-tsc 之前，
//   所以它必须自己抓。
{
  console.log('\n[6] 反例：同一段内重复键必须拦下')
  const root = makeRepo({
    zh: doc("  nav: {\n    home: '首页',\n    heatmap: '热力图',\n    heatmap: '热力图',\n  },"),
    en: doc("  nav: {\n    home: 'Home',\n    heatmap: 'Heatmap',\n    heatmap: 'Heatmap',\n  },"),
  })
  const r = runGate(root)
  check('重复键 → 拦下', r.code === 1, r.out)
  check('报出重复键', r.out.includes('重复键'), r.out)
  check('报出了键名 nav.heatmap', r.out.includes('nav.heatmap'), r.out)
  check('报出了行号（否则定位不到人）', /第 \d+ 行与第 \d+ 行/.test(r.out), r.out)
  rmSync(root, { recursive: true, force: true })
}

{
  // ★ 反向锁定：**不同段**里同名不算重复。
  //   词典里几乎每个词典段都有 `title` / `empty`，按完整路径区分才不会误报。
  console.log('\n[6b] 反例：不同词典段里的同名键不算重复')
  const root = makeRepo({
    zh: doc("  nav: {\n    title: '导航',\n  },\n  logs: {\n    title: '日志',\n  },"),
    en: doc("  nav: {\n    title: 'Nav',\n  },\n  logs: {\n    title: 'Logs',\n  },"),
  })
  const r = runGate(root)
  check('nav.title 与 logs.title 不算重复', r.code === 0, r.out)
  rmSync(root, { recursive: true, force: true })
}


// --- 7. 第 4 条判据：视图引用的键必须存在（2026-10-07）-----------------
// 这一条挡的是**两侧一致地缺同一个键** —— 前三条判据全都看不出来。
// 实况：RouteMatrixView 写了 matrix.specified，两侧词典都没这个键，
// 门全程绿（键集一致 / 无未翻译），运行时显示裸键。
{
 console.log('' + NL + '[7] 正例：视图引用的键都存在 → 放行')
 const root = makeRepo({ views: ['<template><p>{{ t(\'a.b\') }}</p></template>' + NL] })
 const r = runGate(root)
 check('放行', r.code === 0, r.out)
 check('报出扫了几个文件', /扫了 1 个 [.][v]ue/.test(r.out), r.out)
}
{
 console.log('' + NL + '[8] 反例：视图引用了词典里不存在的键 → 拦下')
 const root = makeRepo({ views: ['<template><p>{{ t(\'a.b\') }}{{ t(\'nope.gone\') }}</p></template>' + NL] })
 const r = runGate(root)
 check('拦下', r.code !== 0, r.out)
 check('★ 报出缺的是哪个键 nope.gone', r.out.includes('nope.gone'), r.out)
 check('★ 报出这个缺陷是前三条看不出来的', /前三条判据看不出来/.test(r.out), r.out)
}
{
 console.log('' + NL + '[9] 反例：源码目录扫不到文件必须失败（扫不到却报 OK 的门比没有门更坏）')
 const root = makeRepo({ omitViews: true })
 const r = runGate(root)
 check('拦下', r.code !== 0, r.out)
 check('★ 报出是扫不到候选源文件', /没有任何可能引用/.test(r.out), r.out)
}
{
 console.log('' + NL + '[10] 反例：动态键前缀单列，不能静默跳过')
 const root = makeRepo({ views: ['<template><p>{{ t(\'a.\' + k) }}</p></template>' + NL] })
 const r = runGate(root)
 // a. 抽不出完整键 ⇒ 不算失败，但必须出现在输出里
 //（否则「已确认合格」这个说法会覆盖到根本没量过的地方）
 check('放行（动态键本身不是失败）', r.code === 0, r.out)
 check('★ 单列出动态键前缀', /动态键前缀/.test(r.out) && /a[.]/.test(r.out), r.out)
}

console.log('' + NL + 'i18n-parity selftest: ' + pass + ' passed, ' + fail + ' failed')
process.exit(fail === 0 ? 0 : 1)
