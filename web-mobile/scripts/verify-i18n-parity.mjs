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
//
// 用法：node scripts/verify-i18n-parity.mjs

import { readFileSync, existsSync } from 'node:fs'
import { join, dirname, resolve } from 'node:path'
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
      out.set(keyPath, leaf[3])
      continue
    }
    // 闭对象： `  },` / `  }` / **`} as const`**（本仓真实文件末尾就是后者，
    // 见 zh-CN.ts / en-US.ts 末行 `} as const`）。
    // ★ 初版只匹配前两种 ⇒ 栈永不弹空。真实文件因为恰好有 348 个叶子键所以
    //   「看起来能用」，但自测样本（末行纯 `}`）立刻解析出 0 键 ⇒ 门失效。
    //   这正是「门对真实样本恰好成立、对稍变样本就静默失效」的典型。
    if (/^\s*\},?\s*(as const)?\s*$/.test(line) && stack.length > 0) stack.pop()
  }
  return out
}

const zh = parseDict(ZH)
const en = parseDict(EN)

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

let bad = false
if (onlyZh.length || onlyEn.length) {
  bad = true
  console.error('\ni18n 键集不一致：')
  if (onlyZh.length) console.error(`  仅 zh-CN 有（${onlyZh.length}）：\n    ${onlyZh.join('\n    ')}`)
  if (onlyEn.length) console.error(`  仅 en-US 有（${onlyEn.length}）：\n    ${onlyEn.join('\n    ')}`)
}
if (untranslated.length) {
  bad = true
  console.error(`\ni18n 未翻译（值 === 键名，运行时显示裸键）：\n  ${untranslated.join('\n  ')}`)
}

if (bad) {
  console.error('\n修法：两侧键集必须完全一致；每个键都要有真实文案。\n')
  process.exit(1)
}

console.log(`i18n parity OK: zh-CN / en-US 各 ${zh.size} 键，键集一致，无未翻译项`)
