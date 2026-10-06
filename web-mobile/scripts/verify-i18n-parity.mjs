// verify-i18n-parity.mjs — 中英词典键集守卫
//
// ## 为什么要有这道门
//
// `src/i18n/zh-CN.ts` 第 1 行写着「en-US.ts 与本文件**键集必须一致**
// （i18n 键集守卫见 specs）」—— 但 **specs 目录不存在、任何测试都没覆盖**。
// 后果在本专题实测过（2026-10-06 §11.20）：新增 `usage.costTrend` 等 9 个键时
// 被插进了 `models:` 词典（脚本按「第一个 `keys: {`」定位，而 models 排在 keys 之前），
// 运行时页面直接显示**字面量 `usage.costTrend`**（i18n 缺键回退键名）。
// 那次是靠 4 条**视图**测试偶然发现的 —— 只跑 API 层测试会完全漏过去。
//
// ⇒ 键集漂移是一个**该被静态门拦住、却完全靠运气**的缺陷类型。
//
// ## 守卫什么
//
// 1. **键集一致**：zh-CN 与 en-US 必须有完全相同的键路径集合（两侧各自多一个都算红）。
// 2. **无回退到键名**：i18n 缺键会回退成键名本身（见 i18n/index.ts 的说明）。
//    所以词典里任何值 === 它的键路径，就是「没翻译」——运行时会显示裸键名。
// 3. ★ 2026-10-07 新增 **无重复键**。此前 parseDict 用 `Map.set`，
//    同一段里写了两次 `heatmap:` 会被**静默覆盖** —— 键集一致、值也没问题，
//    门全程绿。实况是 2026-10-07 我把 `nav.heatmap` 插了两次，
//    只有 `vue-tsc` 的 TS1117 抓到。build 前置里 i18n 门跑在 vue-tsc **之前**，
//    既然它跑得更早，就应该由它先报出来并指出是哪个文件第几行。
//
// 用法：node scripts/verify-i18n-parity.mjs

import { readFileSync, existsSync } from 'node:fs'
import { join, dirname, resolve, sep as SEP } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const ZH = join(ROOT, 'src/i18n/zh-CN.ts')
const EN = join(ROOT, 'src/i18n/en-US.ts')

for (const p of [ZH, EN]) {
  if (!existsSync(p)) {
    console.error(`i18n 词典缺失：${p} —— 视为失败（扫不到文件却报 OK 的门比没有门更坏）`)
    process.exit(1)
  }
}

/**
 * 从词典源里抽出「键路径 → 字面值」。
 *
 * 刻意**不做 import**：那会依赖 tsx/编译链，而这个门要在 build 前置里跑。
 * 代价是要手写一小段解析，但词典文件是 SSOT、格式稳定，值都是单行字符串字面量。
 */
function parseDict(path) {
  const src = readFileSync(path, 'utf8')
  /** @type {Map<string, string>} */
  const out = new Map()
  /** @type {Map<string, number>} */  // 键路径 → 首次出现的行号（1-based）
  const firstSeen = new Map()
  /** @type {string[]} */
  const duplicates = []
  const stack = []
  const lines = src.split('\n')

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]
    // 开对象： `  foo: {` （排除数组/函数/字符串）
    const open = line.match(/^(\s*)([A-Za-z_$][\w$]*):\s*\{\s*$/)
    if (open) {
      stack.push(open[2])
      continue
    }
    // 叶子： `  key: 'value',` 或 `  key: 'value'`
    const leaf = line.match(/^(\s*)([A-Za-z_$][\w$]*):\s*'((?:[^'\\]|\\.)*)'\s*,?\s*$/)
    if (leaf && stack.length > 0) {
      const keyPath = [...stack, leaf[2]].join('.')
      // ★ 重复键检测：Map.set 会静默覆盖，必须在覆盖前记下来。
      //   同一段内重复（两行之间没有开/闭对象）才是真重复；
      //   不同段里同名（如两个词典各自都有 `title`）是正常的，按完整路径区分。
      if (firstSeen.has(keyPath)) {
        duplicates.push(`${keyPath}（第 ${firstSeen.get(keyPath)} 行与第 ${i + 1} 行）`)
      } else {
        firstSeen.set(keyPath, i + 1)
        out.set(keyPath, leaf[3])
      }
      continue
    }
    // 闭对象： `  },` / `  }` / **`} as const`**（本仓真实文件末尾就是后者，
    // 见 zh-CN.ts / en-US.ts 末行 `} as const`）。
    // ★ 初版只匹配前两种 ⇒ 栈永不弹空。真实文件因为恰好有 348 个叶子键所以
    //   「看起来能用」，但自测样本（末行纯 `}`）立刻解析出 0 键 ⇒ 门失效。
    //   这正是「门对真实样本恰好成立、对稍变样本就静默失效」的典型。
    if (/^\s*\},?\s*(as const)?\s*$/.test(line) && stack.length > 0) stack.pop()
  }
  return { out, duplicates }
}

const zhRes = parseDict(ZH)
const enRes = parseDict(EN)
const zh = zhRes.out
const en = enRes.out
const duplicates = [
  ...zhRes.duplicates.map((d) => `zh-CN ${d}`),
  ...enRes.duplicates.map((d) => `en-US ${d}`),
]

if (zh.size === 0 || en.size === 0) {
  console.error(`i18n 解析异常：zh=${zh.size} 键 / en=${en.size} 键（任一为 0 视为失败）`)
  process.exit(1)
}

const onlyZh = [...zh.keys()].filter((k) => !en.has(k)).sort()
const onlyEn = [...en.keys()].filter((k) => !zh.has(k)).sort()

// 值 === 键路径的末段 ⇒ 值被写成了键名本身（漏翻译）
const untranslated = []
for (const [key, value] of zh) {
  const leaf = key.split('.').pop()
  if (value === leaf || value === key) untranslated.push(`zh-CN ${key} = ${value}`)
}
for (const [key, value] of en) {
  const leaf = key.split('.').pop()
  if (value === leaf || value === key) untranslated.push(`en-US ${key} = ${value}`)
}

// 4. ★ 2026-10-07 新增 **视图引用的键必须存在**。
//    判据 1/2/3 全部只看两侧词典**互相**对齐，于是漏掉整整一类：
//    **两侧一致地缺同一个键**。i18n 缺键回退成键名本身（见 i18n/index.ts），
//    运行时用户看到的是字面量 `matrix.specified`。
//    实况：写 RouteMatrixView 时把两个键只加了注释没加值，两侧都没这个键，
//    i18n 门全程绿（677 键 / 键集一致 / 无未翻译），
//    是视图测试顺带撞出来的。
//    ⇒ 门必须从**调用侧**抽键。只抽 `t('a.b.c')` 的字面量形式；
//      `t(prefix + x)` 抽不出完整键，**单列出来**而不是静默跳过 ——
//      「测不到」混进「已确认合格」会让报表主动隐藏真缺口。
import { readdirSync } from 'node:fs'

// ★ 2026-10-07：扫描范围是**整个 src/**，不是只有 views/*.vue。
//   初版只扫 views，漏掉了 API 层里的字面量键 —— `taskLabel()` 在
//   `src/api/autoRouteMatrix.ts` 里写死了 `t('matrix.specified')` 的键名，
//   那个键被误删时门全程绿（因为 views 里没有任何地方**字面量**引用它）。
//   ⇒ 「判据的绿只覆盖它量到的那件事」：扫描范围也是判据的一部分。
//   排除 `*.spec.ts` / `*.test.ts` —— 那些文件里的 t() 键是**被测样本**，
//   不是产品引用，拿它们当契约会把「故意写错的样本」报成缺陷。
const SRC_DIR = join(ROOT, 'src')
const SKIP = /\.(spec|test)\.ts$/
// ★ 末尾的 `.?` 不是可有可无：真实代码写的是 `t('matrix.row.' + r)`，
//   那个尾点会让「要求 `'` 紧跟标识符」的老正则**整条失配**
//   —— 于是既没进字面量桶、也没进动态桶，**两桶都漏**。
//   实况：加了这条判据后自测 [10] 就是这么红的。
const T_LITERAL = /\bt\(\s*'([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*\.?)'/g

function walk(dir) {
  const out = []
  let entries = []
  try {
    entries = readdirSync(dir, { withFileTypes: true })
  } catch {
    return out
  }
  for (const e of entries) {
    const full = join(dir, e.name)
    if (e.isDirectory()) {
      if (e.name === 'node_modules' || e.name === 'dist') continue
      out.push(...walk(full))
    } else if (/\.(vue|ts)$/.test(e.name) && !SKIP.test(e.name)) {
      out.push(full)
    }
  }
  return out
}

function collectVueKeys() {
  /** @type {Set<string>} */
  const literal = new Set()
  /** @type {Set<string>} */
  const dynamicPrefixes = new Set()
  let files = []
  try {
    files = walk(SRC_DIR)
  } catch {
    files = []
  }
  // ★ 守卫判的是「扫到过**可能引用 t() 键的**源文件」，不是「扫到过文件」。
  //   词典本身也是 src/ 下的 .ts，早先只数总文件数时，[9] 那组自测
  //   （只建词典、不建视图）会因两个词典文件而被判成「扫到了」⇒ 门放行。
  const candidates = files.filter((f) => !f.includes(`${SEP}i18n${SEP}`))
  if (candidates.length === 0) {
    // 扫不到候选源文件必须当失败：扫不到却报 OK 的门比没有门更坏
    console.error(
      `源码目录里没有任何可能引用 t() 键的源文件（只找到 ${files.length} 个词典文件）—— ` +
        `视为失败（扫不到文件却报 OK 的门比没有门更坏）`,
    )
    process.exit(1)
  }
  for (const full of files) {
    const f = full.slice(ROOT.length + 1)
    const src = readFileSync(full, 'utf8')
    for (const m of src.matchAll(T_LITERAL)) {
      const after = src.slice(m.index + m[0].length)
      if (/^\s*\+/.test(after)) {
        // `t('a.b' + x)`：抽不出完整键，单列出来而不是当成已确认的键
        dynamicPrefixes.add(`${f}: '${m[1]}'`)
        continue
      }
      // 尾点是动态形态的残留，不该作为完整键（`a.b.` 永远不是真键）
      if (m[1].endsWith('.')) {
        dynamicPrefixes.add(`${f}: '${m[1]}'`)
        continue
      }
      literal.add(m[1])
    }
  }
  return { literal, dynamicPrefixes, scanned: candidates.length }
}

const { literal: usedKeys, dynamicPrefixes, scanned: candidatesScanned } = collectVueKeys()
const missingInViews = [...usedKeys].filter((k) => !zh.has(k) && !en.has(k)).sort()

let bad = false
if (onlyZh.length || onlyEn.length) {
  bad = true
  console.error('\ni18n 键集不一致：')
  if (onlyZh.length) console.error(`  仅 zh-CN 有（${onlyZh.length}）：\n    ${onlyZh.join('\n    ')}`)
  if (onlyEn.length) console.error(`  仅 en-US 有（${onlyEn.length}）：\n    ${onlyEn.join('\n    ')}`)
}
if (duplicates.length) {
  bad = true
  console.error(`\ni18n 重复键（后者被静默覆盖，运行时会取到最后一个）：\n  ${duplicates.join('\n  ')}`)
}
if (untranslated.length) {
  bad = true
  console.error(`\ni18n 未翻译（值 === 键名，运行时显示裸键）：\n  ${untranslated.join('\n  ')}`)
}
if (missingInViews.length) {
  bad = true
  console.error(
    `\n视图引用了词典里不存在的键（两侧一致地缺 ⇒ 前三条判据看不出来；\n` +
      `运行时 i18n 回退成键名本身，用户看到裸键）：\n  ${missingInViews.join('\n  ')}`,
  )
}
if (dynamicPrefixes.size) {
  // ★ 不算失败，但**必须列出来**：这些键门抽不出完整路径，等于「测不到」。
  //   静默跳过会让「已确认合格」这个说法覆盖到没量过的地方。
  console.log(
    `i18n 动态键前缀（本门未覆盖，由 src/i18n/dynamicKeys.spec.ts 覆盖，需人工确认，${dynamicPrefixes.size} 处）：\n  ${[...dynamicPrefixes].sort().join('\n  ')}`,
  )
}

if (bad) {
  console.error('\n修法：两侧键集必须完全一致；每个键都要有真实文案。\n')
  process.exit(1)
}

console.log(
  `i18n parity OK: zh-CN / en-US 各 ${zh.size} 键，键集一致，无未翻译项；` +
    `源码字面量键 ${usedKeys.size} 个全部存在（扫了 ${candidatesScanned} 个 .vue/.ts）`,
)
