#!/usr/bin/env node
// design-primary-focus-guard.mjs — 「主色只留给主动作」门 CLI（2026-10-27，第 20 增量）
//
// 判据实现：src/design/primaryFocus.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**（与 scripts/design-elevation-guard.mjs 同一范式）：
//   判据要读 .vue 源码并做标签树解析 ⇒ 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// ★★★ 判据来源（三处，全部可引用）：
//   · impeccable skill/reference/operate.md：
//       "Accent color used for primary actions, current selection, and state indicators
//        only, not decoration."        ← rule:product-color-accent-only
//   · impeccable skill/reference/distill.md：
//       "Clear hierarchy: ONE primary action, few secondary actions,
//        everything else tertiary or hidden"
//       "Clear next action: ONE obvious next action, not five competing ones"
//   · impeccable skill/reference/colorize.md：
//       "Keep the primary action easy to find; do not spend its color on decoration."
//       "Let the strongest color own a deliberate region or role instead of
//        scattering tiny accents."
//   · 仓内 DESIGN.md §视觉世界：「状态用最小的颜色量表达、每个元素都承担信息职责」
//
// ★★★ 判据（唯一一条）：
//
//   **实心主色不得落在「数据行」内。**
//
//   · 数据行（4 种形态，穷举）：`<td>`/`<th>` · `<el-table-column>` ·
//     数据视图的行 slot（`<template #x="{ row }">`）· `<li>`/`<tr>`
//   · 豁免 ①：焦点区域（模态 / 抽屉）内 —— 那里主色属于「这个浮层的主动作」
//   · 豁免 ②：刻意区域卡（`v-for` 的块级项）且**该视图内没有别的实心主色**与之竞争
//
// ★ 棘轮：rowLevelSolidPrimary **只降不升**。
//
// 用法：
//   node --experimental-strip-types scripts/design-primary-focus-guard.mjs              # 扫描 + 棘轮，有违规 exit 1
//   node --experimental-strip-types scripts/design-primary-focus-guard.mjs --json       # 机器可读
//   node --experimental-strip-types scripts/design-primary-focus-guard.mjs --list       # 按文件分组的工作单
//   node --experimental-strip-types scripts/design-primary-focus-guard.mjs --selftest   # 自证：判据本身对不对
//   node --experimental-strip-types scripts/design-primary-focus-guard.mjs --update-baseline  # 人工收紧基线（只允许降）

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'primaryFocus.ts')
const BASELINE = join(ROOT, 'scripts', 'design-primary-focus-baseline.json')

const implUrl = pathToFileURL(IMPL).href

const body = `
import { scanPrimaryFocus, listVueFiles, runPrimaryFocusSelftest } from ${JSON.stringify(implUrl)}
const SRC = ${JSON.stringify(join(ROOT, 'src'))}
if (process.env.PF_MODE === 'selftest') {
  const r = runPrimaryFocusSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const res = scanPrimaryFocus(listVueFiles(SRC), SRC)
console.log(JSON.stringify(res))
`

const isSelftest = process.argv.includes('--selftest')

const r = spawnSync(
  process.execPath,
  ['--experimental-strip-types', '--no-warnings', '--input-type=module', '-e', body],
  {
    cwd: ROOT,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
    env: { ...process.env, PF_MODE: isSelftest ? 'selftest' : 'scan' },
  }
)

if (r.status !== 0 && r.stdout.trim() === '') {
  process.stderr.write(r.stderr || '')
  process.exit(r.status || 2)
}

if (isSelftest) {
  process.stdout.write(r.stdout)
  process.exit(r.status || 0)
}

let res = { findings: [], facts: {} }
try {
  res = JSON.parse(r.stdout.trim() || '{}')
} catch {
  process.stderr.write(r.stdout || '')
  process.stderr.write(r.stderr || '')
  process.exit(2)
}

const findings = res.findings ?? []
const facts = res.facts ?? {}

const baseline = existsSync(BASELINE)
  ? JSON.parse(readFileSync(BASELINE, 'utf8'))
  : { rowLevelSolidPrimary: 0 }

// ── 棘轮：只降不升 ──
const regressions = []
if ((facts.rowLevelSolidPrimary ?? 0) > (baseline.rowLevelSolidPrimary ?? 0)) {
  regressions.push(
    `rowLevelSolidPrimary ${baseline.rowLevelSolidPrimary} → ${facts.rowLevelSolidPrimary}（升高了）`
  )
}

if (process.argv.includes('--update-baseline')) {
  if ((facts.rowLevelSolidPrimary ?? 0) > (baseline.rowLevelSolidPrimary ?? 0)) {
    console.error('❌ 基线只能降不能升 —— 当前实测值高于基线，先把违规清掉再收紧基线。')
    process.stderr.write('   ' + (regressions.join('\n   ') || '') + '\n')
    process.exit(1)
  }
  const next = {
    metric:
      'rowLevelSolidPrimary = 落在「数据行」内的实心主色按钮数（数据行 = td/th · el-table-column · ' +
      '数据视图行 slot · li/tr；焦点区域与「无竞争的刻意区域卡」豁免）',
    rowLevelSolidPrimary: facts.rowLevelSolidPrimary ?? 0,
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        rowLevelSolidPrimary: facts.rowLevelSolidPrimary ?? 0,
        updated: '2026-10-27',
        reason:
          process.env.PF_BASELINE_REASON || '人工收紧基线（只允许降）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`✅ 基线已收紧：rowLevelSolidPrimary=${next.rowLevelSolidPrimary}`)
  process.exit(0)
}

if (process.argv.includes('--json')) {
  console.log(
    JSON.stringify(
      {
        count: findings.length,
        facts,
        baseline: { rowLevelSolidPrimary: baseline.rowLevelSolidPrimary },
        regressions,
        findings,
      },
      null,
      2
    )
  )
} else if (process.argv.includes('--list')) {
  const byFile = new Map()
  for (const f of findings) {
    if (!byFile.has(f.file)) byFile.set(f.file, [])
    byFile.get(f.file).push(f)
  }
  console.log(
    `违规 ${findings.length} 处 / ${byFile.size} 文件；全仓实心主色 ${facts.totalSolidPrimary ?? 0} 个` +
      `（焦点区域豁免 ${facts.focusRegionSolidPrimary ?? 0} · 刻意区域豁免 ${facts.deliberateRegionSolidPrimary ?? 0}）`
  )
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) {
      console.log(`  ${f.line}:${f.col}  [${f.form}/${f.containerForm}]  ${f.snippet}  —— ${f.reason}`)
    }
  }
} else {
  for (const f of findings) {
    console.log(`${f.file}:${f.line}:${f.col}  [${f.form}/${f.containerForm}]  ${f.snippet}\n    —— ${f.reason}`)
  }
  console.log(
    `\n读数：全仓实心主色 ${facts.totalSolidPrimary ?? 0} 个 · 焦点区域豁免 ${facts.focusRegionSolidPrimary ?? 0} · ` +
      `刻意区域豁免 ${facts.deliberateRegionSolidPrimary ?? 0} · 数据行内 ${facts.rowLevelSolidPrimary ?? 0}`
  )
  console.log(`基线：数据行内实心主色 ≤ ${baseline.rowLevelSolidPrimary}`)
}

if (findings.length) {
  console.error(`\nFAIL: ${findings.length} 处实心主色落在数据行内。`)
  console.error(
    '修法：数据行里的动作改用次级样式（btn-ghost / btn）；主色只留给页面级主动作与「刻意区域」。'
  )
  process.exit(1)
}
if (regressions.length) {
  console.error('\nFAIL: 棘轮指标升高。')
  console.error('  ' + regressions.join('\n  '))
  process.exit(1)
}
if ((facts.rowLevelSolidPrimary ?? 0) < (baseline.rowLevelSolidPrimary ?? 0)) {
  console.log('✅ 数据行里没有实心主色 —— PASS（棘轮有余量，可用 --update-baseline 收紧基线）')
} else {
  console.log('✅ 数据行里没有实心主色 —— PASS')
}
