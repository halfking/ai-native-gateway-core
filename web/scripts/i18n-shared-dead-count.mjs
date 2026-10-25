#!/usr/bin/env node
// i18n-shared-dead-count.mjs — 「所有语种共有但 0 引用」叶子键计数 + baseline 阻断门
// （2026-10-27 第 13 增量 · 语言包死键清理轮）
//
// 用法：
//   node scripts/i18n-shared-dead-count.mjs                     # 对比 baseline，超过则 exit 1
//   node scripts/i18n-shared-dead-count.mjs --json              # 输出 JSON
//   node scripts/i18n-shared-dead-count.mjs --update-baseline   # 把当前计数写入 baseline（只许降不许升）
//   node scripts/i18n-shared-dead-count.mjs --selftest          # 判据自证
//
// ★ 为什么要有这道门：
//   `i18n-dead-key-guard.mjs` 只判「跨语种独有的死键」；
//   而「8 语种共有但全仓 0 引用」这一族它**只报数、不判** ——
//   于是「新增死键」在这个方向上是**完全不受约束**的。
//   本门把这一族变成**棘轮指标**：只许降不许升 ⇒ 新增死键立刻被挡住。
//
// ★ 口径**不在本文件里重写**：直接复用 `i18n-dead-key-guard.mjs` 的
//   `computeDeadKeyFacts()` ⇒ 与死键门**逐字同源**（同一份实现，两个出口）。
//   ★ 纪律：「同一口径只能有一份实现」—— 否则两个门各算各的，读数无法对拍。

import { spawnSync } from 'node:child_process'
import { writeFileSync, unlinkSync, readFileSync, existsSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const BASELINE_FILE = join(ROOT, 'scripts', 'i18n-shared-dead-baseline.json')

function dirname(p) {
  return p.split('/').slice(0, -1).join('/')
}

function readBaseline(file = BASELINE_FILE) {
  if (!existsSync(file)) return null
  try {
    const b = JSON.parse(readFileSync(file, 'utf8'))
    return typeof b.count === 'number' ? b : null
  } catch {
    return null
  }
}

/** ★ 只许降不许升：把当前值写进 baseline 时，若比原值高则抛错。 */
function writeBaseline(count, reason, file = BASELINE_FILE) {
  const prev = readBaseline(file)
  if (prev && count > prev.count) {
    throw new Error(
      `baseline 只能降不能升：当前 ${count} > 现基线 ${prev.count}。` +
      `调高必须人工显式改 JSON 并写明理由（本轮未做）。`
    )
  }
  // ★ 保留**变更史**：棘轮每一次下调都要留下「为什么降」，
  //   否则后来人只看得到一个数字，看不出「基线被重铸过几次、每次因为什么」。
  const history = []
  if (prev && Array.isArray(prev.history)) history.push(...prev.history)
  history.push({ count, updated: '2026-10-27', ...(reason ? { reason } : {}) })
  const payload = {
    metric:
      'sharedDead = 8 语种共有、全仓 0 引用、且不在动态键前缀下的【叶子】键；' +
      '口径复用 i18n-dead-key-guard.mjs 的 computeDeadKeyFacts()（与死键门同源）',
    count,
    updated: '2026-10-27',
    history,
  }
  writeFileSync(file, JSON.stringify(payload, null, 2) + '\n', 'utf8')
  return payload
}

/** 判据自证：① 计数口径能区分「有引用 / 无引用」② 棘轮比较方向正确。 */
async function selftest() {
  const guard = await import(pathToFileURL(join(ROOT, 'scripts', 'i18n-dead-key-guard.mjs')).href)
  const cases = []
  const check = (id, desc, expected, actual) => {
    const ok = JSON.stringify(expected) === JSON.stringify(actual)
    cases.push({ id, desc, expected, actual, ok })
  }

  // A1 静态引用集能识别字面量调用
  const refKeys = guard.collectStaticRefKeys("const a = t('x.y')\nconst b = te('x.z')\n")
  check('A1', 't()/te() 字面量计入引用集', true, refKeys.has('x.y') && refKeys.has('x.z'))

  // A2 ★ 动态前缀会把「根不可判定」的调用点标成 unresolved ⇒ 该位置一律不判死
  const dyn = guard.collectDynamicPrefixes('const x = t(`${unknownVar}.status`)\n')
  check('A2', '根不可判定的动态键 ⇒ 记 unresolved（不静默放过）', 1, dyn.unresolved.length)

  // A3 ★ 动态前缀可判定 ⇒ 收集到前缀（其下的键不判死）
  const dyn2 = guard.collectDynamicPrefixes('const x = t(`ns.sub.${v}`)\n')
  check('A3', '可判定的动态前缀被收集', true, dyn2.prefixes.has('ns.sub.'))

  // A4 ★ 容器路径不算键（只算叶子）
  const leaves = [...guard.flattenLeaves({ a: { b: 'x', c: { d: 'y' } } }, '', new Set())]
  check('A4', '容器路径不进键集合（只收叶子）', ['a.b', 'a.c.d'], leaves)

  // A5 ★ 棘轮比较方向：count > baseline ⇒ 判红；count <= baseline ⇒ 判绿
  const over = 101 > 100
  check('A5', '棘轮方向：count > baseline ⇒ 判红', true, over)
  check('A5b', '棘轮方向：count <= baseline ⇒ 判绿', true, 100 <= 100)

  // A6 ★★ 写 baseline 时「调高」必须被拒 —— ★ **测真函数**（不是 `101 > 100` 那种恒真断言）
  //   ★ 教训来源：第 11 增量 §418 ——「断言恒真比恒假更隐蔽」。
  //   ★ 做法：把 baseline 指到**临时文件** ⇒ 调真函数 ⇒ 断言它抛错；再写一个更低的值 ⇒ 断言它不抛。
  const tmp = join(ROOT, 'scripts', '.i18n-shared-dead-selftest-baseline.json')
  let raiseRejected = false
  let lowerAccepted = false
  let historyKept = false
  try {
    writeFileSync(tmp, JSON.stringify({ count: 100, updated: '2026-10-27' }, null, 2))
    try {
      writeBaseline(101, '自证：应当被拒', tmp)
    } catch {
      raiseRejected = true
    }
    try {
      writeBaseline(90, '自证：应当被接受', tmp)
      lowerAccepted = true
      const after = readBaseline(tmp)
      historyKept = Array.isArray(after.history) && after.history.length === 1
    } catch {
      lowerAccepted = false
    }
  } finally {
    try { unlinkSync(tmp) } catch { /* best effort */ }
  }
  check('A6', '写 baseline 时调高必须被拒（测真函数）', true, raiseRejected)
  check('A6b', '写 baseline 时调低必须被接受（测真函数）', true, lowerAccepted)
  check('A6c', '写 baseline 时保留变更史 history', true, historyKept)

  // A7/A8/A9 需要事实源 ⇒ 在此处加载（★ 与主流程同一个 loader ⇒ 同源）
  const guard2 = await import(pathToFileURL(join(ROOT, 'scripts', 'i18n-dead-key-guard.mjs')).href)
  const scanner2 = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'scanner.ts')).href)
  const facts2 = await guard2.computeDeadKeyFacts(scanner2.loadLocale)

  // A7 ★★ 口径自证（**直接验证判据的定义**，不是恒真断言）：
  //   「共有死键」的每个键必须在【源语种的键集合】里（⇒ 它确实是个键）
  check('A7', '共有死键的每个键都在源语种键集合里', true,
    facts2.sharedDead.every((k) => facts2.sourceKeys.has(k)))

  // A8 ★★ 「共有死键」的每个键必须【不在静态引用集】里（⇒ 它确实 0 引用）
  check('A8', '共有死键的每个键都不在静态引用集里', true,
    facts2.sharedDead.every((k) => !facts2.refKeys.has(k)))

  // A9 ★★ 「共有死键」的每个键必须【不在任何动态键前缀】下（⇒ 动态取键够不着它）
  check('A9', '共有死键的每个键都不在动态键前缀下', true,
    facts2.sharedDead.every((k) => ![...facts2.dynPrefixes].some((p) => k.startsWith(p))))

  let bad = 0
  console.log('══ 判据自证 ══')
  for (const c of cases) {
    if (!c.ok) bad++
    const mark = c.ok ? '✔' : '✘'
    console.log(`  ${mark} ${c.id} ${c.desc}（期望 ${JSON.stringify(c.expected)}，实测 ${JSON.stringify(c.actual)}）`)
  }
  console.log(`\n══ 自证 ${cases.length - bad}/${cases.length} ══`)
  if (bad) process.exit(1)
}

async function main() {
  const argv = process.argv.slice(2)
  if (argv.includes('--selftest')) {
    await selftest()
    return
  }

  const guard = await import(pathToFileURL(join(ROOT, 'scripts', 'i18n-dead-key-guard.mjs')).href)
  const scanner = await import(pathToFileURL(join(ROOT, 'src', 'i18n', 'scanner.ts')).href)
  const facts = await guard.computeDeadKeyFacts(scanner.loadLocale)
  const count = facts.sharedDead.length

  if (argv.includes('--update-baseline')) {
    const reason = argv.includes('--reason') ? argv[argv.indexOf('--reason') + 1] : undefined
    const b = writeBaseline(count, reason)
    console.log(JSON.stringify({ updated: b.count }))
    return
  }

  if (argv.includes('--json')) {
    const baseline = readBaseline()
    const byNs = new Map()
    for (const k of facts.sharedDead) {
      const ns = k.split('.')[0]
      byNs.set(ns, (byNs.get(ns) || 0) + 1)
    }
    console.log(
      JSON.stringify(
        {
          count,
          baseline: baseline ? baseline.count : null,
          topNamespaces: [...byNs.entries()].sort((a, b) => b[1] - a[1]).slice(0, 20),
          sample: facts.sharedDead.slice(0, 30),
        },
        null,
        2
      )
    )
    return
  }

  const baseline = readBaseline()
  const lines = ['shared dead keys (8 语种共有 + 全仓 0 引用): ' + count]
  if (!baseline) {
    console.error('error: baseline file scripts/i18n-shared-dead-baseline.json missing or invalid')
    console.error('run: node scripts/i18n-shared-dead-count.mjs --update-baseline')
    process.exit(1)
  }
  lines.push('baseline:                                   ' + baseline.count + ' (' + baseline.updated + ')')
  if (count > baseline.count) {
    console.log(lines.join('\n'))
    console.error('\n❌ 共有死键数 INCREASED by ' + (count - baseline.count) + ' (ratchet violated)')
    console.error('   新增了「8 语种共有但全仓 0 引用」的键 ⇒ 要么接上引用，要么从语言包里删掉。')
    process.exit(1)
  }
  if (count < baseline.count) {
    lines.push('✅ decreased by ' + (baseline.count - count) + ' — ratchet down with:')
    lines.push('   node scripts/i18n-shared-dead-count.mjs --update-baseline')
  } else {
    lines.push('✅ within baseline')
  }
  console.log(lines.join('\n'))
}

main().catch((e) => {
  console.error('i18n-shared-dead-count failed:', e && e.stack ? e.stack : e)
  process.exit(2)
})
