#!/usr/bin/env node
// i18n-array-key-guard.mjs — 「数组型语言包键不得经 t() 消费」门（2026-10-27）
//
// ⚠️ 为什么需要这道门（2026-10-27 实测）：
//   vue-i18n 9.14.5 的 `t()` **不支持数组型消息** —— 实测（仓内最小复现）：
//     t('policy.tableHeaders') → 返回的是**键路径字符串** "policy.tableHeaders"（不是那个数组）
//     t('policy.title')        → 正常返回译文
//   ⇒ 模板里 `v-for="x in t('some.arrayKey')"` 会把**键名逐字符**渲染出来
//     （浏览器实测：表头单元格内容 = r / o / u / t / i / n / g / …）。
//   ⇒ 而**键存在性**门（i18n-audit）看不见它：数组被 collectLeafKeys 算作一个存在的键
//     ⇒ missing key = 0，页面却是坏的。
//   ⇒ 缺的是「键存在」与「键能被 t() 取到」之间的那一位 —— 本门补这一位。
//
// 用法：
//   node scripts/i18n-array-key-guard.mjs            # 检查；发现违规 ⇒ exit 1
//   node scripts/i18n-array-key-guard.mjs --selftest  # 自证（违规→红 / 干净→绿）
//
// ★ 判据（与 scanner.ts 的引用扫描同口径）：
//   1) 运行时遍历 8 个语种的**全部模块**，找��值是数组的叶子键；
//   2) 用 scanner.scanAll() 拿到源码里的**字面量键引用**；
//   3) 交集 = 违规（该数组键被 t('…') / tm('…') 消费）。
//   ⚠ 动态拼接的键（`t('x.' + k)`）静态扫描看不见 ⇒ 本门覆盖不到（已知边界，写在输出里）。

import { fileURLToPath, pathToFileURL } from 'node:url'
import { dirname, join, resolve } from 'node:path'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')

async function loadScanner() {
  const p = pathToFileURL(join(ROOT, 'src', 'i18n', 'scanner.ts')).href
  return await import(p)
}

/** 递归找出「值是数组」的叶子键（含模块名前缀），返回 Map<key, 数组长度> */
function collectArrayKeys(obj, prefix, out) {
  for (const [k, v] of Object.entries(obj || {})) {
    const key = prefix ? `${prefix}.${k}` : k
    if (Array.isArray(v)) {
      out.set(key, v.length)
    } else if (v && typeof v === 'object') {
      collectArrayKeys(v, key, out)
    }
  }
  return out
}

/** 找出「经 t()/tm() 消费了数组型键」的行 */
function findArrayConsumption(src, arrayKeys) {
  const hits = []
  const lines = src.split('\n')
  lines.forEach((line, i) => {
    // 只看字面量调用：t('...') / tm('...') / $t('...')
    const re = /\b(?:\$?t|tm)\(\s*'([^']+)'/g
    let m
    while ((m = re.exec(line)) !== null) {
      const key = m[1]
      if (arrayKeys.has(key)) hits.push({ line: i + 1, key, len: arrayKeys.get(key), text: line.trim().slice(0, 140) })
    }
  })
  return hits
}

/** 自证：夹具必须能区分「违规→红 / 干净→绿」 */
function selftest(findConsumption) {
  const cases = []
  const arr = new Map([['a.list', 3]])
  const src = [
    "const x = t('a.list')",                       // 违规：数组键经 t()
    "const y = t('a.text')",                       // 干净：标量键
    "const z = localeArray('a.list')",             // 干净：走消息树 helper
    "const w = t('a.' + name)",                    // 干净（动态键，本门看不见）
    "const v = tm('a.list')",                      // 违规：tm() 也是 t()
  ].join('\n')
  const hits = findConsumption(src, arr)
  const lines = hits.map((h) => h.line)
  cases.push({ id: 'A1', desc: '数组键经 t() 消费 ⇒ 报出', expected: true, actual: lines.includes(1), ok: lines.includes(1) === true })
  cases.push({ id: 'A2', desc: '标量键经 t() 消费 ⇒ 不报', expected: false, actual: lines.includes(2), ok: lines.includes(2) === false })
  cases.push({ id: 'A3', desc: '数组键走 localeArray() ⇒ 不报', expected: false, actual: lines.includes(3), ok: lines.includes(3) === false })
  cases.push({ id: 'A4', desc: '动态拼接键 ⇒ 不报（已知边界）', expected: false, actual: lines.includes(4), ok: lines.includes(4) === false })
  cases.push({ id: 'A5', desc: '数组键经 tm() 消费 ⇒ 报出', expected: true, actual: lines.includes(5), ok: lines.includes(5) === true })
  return cases
}

async function main() {
  const argv = process.argv.slice(2)
  if (argv.includes('--selftest')) {
    const cases = selftest(findArrayConsumption)
    let bad = 0
    for (const c of cases) {
      const mark = c.ok ? '✓' : '✗'
      if (!c.ok) bad++
      console.log(`  ${mark} ${c.id} ${c.desc}（期望 ${c.expected}，实测 ${c.actual}）`)
    }
    console.log(`\n══ 自证 ${cases.length - bad}/${cases.length} ══`)
    process.exit(bad ? 1 : 0)
  }

  const scanner = await loadScanner()
  const { loadLocale, scanAll } = scanner
  const LOCALES = ['ar-SA', 'de-DE', 'en-US', 'es-ES', 'fr-FR', 'ja-JP', 'zh-CN', 'zh-TW']

  // ① 全部语种的数组型键（并集；并断言各语种对同一键的数组长度一致）
  const perLocale = new Map()
  for (const loc of LOCALES) {
    const mod = loadLocale(loc, join(ROOT, 'src', 'locales'))
    perLocale.set(loc, collectArrayKeys(mod, '', new Map()))
  }
  const arrayKeys = new Map()
  const shapeConflicts = []
  for (const [loc, m] of perLocale) {
    for (const [k, len] of m) {
      if (arrayKeys.has(k) && arrayKeys.get(k) !== len) shapeConflicts.push(`${k}: ${loc} len=${len} vs ${arrayKeys.get(k)}`)
      else arrayKeys.set(k, len)
    }
  }

  // ② 源码里经 t()/tm() 消费的数组型键
  const { references, files } = scanAll(join(ROOT, 'src'))
  const refKeys = new Set(references.map((r) => r.key))
  const consumed = [...refKeys].filter((k) => arrayKeys.has(k)).sort()

  // ③ 定位到行（便于修）
  const { readFileSync, readdirSync } = await import('node:fs')
  const lines = []
  const walk = (dir) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name)
      if (e.isDirectory()) { walk(p); continue }
      // Windows join 产生反斜杠 ⇒ '/locales/' 永不命中 ⇒ locale 文件被当源码扫
      // （与 src/i18n/scanner.ts 的 toSlash 同一课）。
      if (!/\.(vue|ts)$/.test(e.name) || p.replace(/\\/g, '/').includes('/locales/')) continue
      const src = readFileSync(p, 'utf8')
      for (const h of findArrayConsumption(src, arrayKeys)) lines.push({ file: p.replace(ROOT + '/', ''), ...h })
    }
  }
  walk(join(ROOT, 'src'))

  console.log(`数组型语言包键：${arrayKeys.size} 个（8 语种并集）`)
  console.log(`其中被 t()/tm() 消费的：${consumed.length} 个`)
  if (shapeConflicts.length) {
    console.log('\n⚠ 各语种数组长度不一致（键形状分歧）：')
    for (const c of shapeConflicts) console.log('   ' + c)
  }
  if (!lines.length) {
    console.log('\n✅ 没有「数组型键经 t() 消费」—— PASS')
    process.exit(0)
  }
  console.log('\n✗ 发现违规（数组型键经 t()/tm() 消费 ⇒ 运行时会渲染出键路径）：')
  for (const l of lines) {
    console.log(`   ${l.file}:${l.line}  ${l.key}（数组 ${l.len} 项）`)
    console.log(`      ${l.text}`)
  }
  console.log('\n   修法：① 短列表 → 改成标量键；② 长列表 → 用 i18n.ts 的 localeArray()（从消息树直取）。')
  process.exit(1)
}

main().catch((e) => {
  console.error('FATAL: ' + (e && e.stack ? e.stack : e))
  process.exit(2)
})
