#!/usr/bin/env node
// design-primary-clarity-guard.mjs — 「主色必须唯一」门 CLI（2026-10-27，第 22 增量）
//
// 判据实现：src/design/primaryClarity.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**（与 scripts/design-primary-focus-guard.mjs 同一范式）：
//   判据要读 .vue 源码并做标签树解析 ⇒ 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// ★★★ 判据来源：
//   · impeccable skill/reference/distill.md：
//       "Clear hierarchy: ONE primary action, few secondary actions,
//        everything else tertiary or hidden"
//       "Clear next action: ONE obvious next action, not five competing ones"
//   · impeccable skill/reference/colorize.md：
//       "Keep the primary action easy to find; do not spend its color on decoration."
//   · 仓内 DESIGN.md §视觉世界：「状态用最小的颜色量表达、每个元素都承担信息职责」
//
// ★★★ 判据（唯一一条）：
//
//   **一个「动作区」里，最多一个实心主色。**
//
//   · 动作区 = **直接父容器**（一组并排按钮的共同父节点；不靠类名）
//   · 排除：焦点区域（模态 / 抽屉）内 · 数据行内（第 20 增量已治）
//
// ★ 棘轮：competingZones **只降不升**。
//
// 用法：
//   node --experimental-strip-types scripts/design-primary-clarity-guard.mjs              # 扫描 + 棘轮，有违规 exit 1
//   node --experimental-strip-types scripts/design-primary-clarity-guard.mjs --json       # 机器可读
//   node --experimental-strip-types scripts/design-primary-clarity-guard.mjs --list       # 按文件分组的工作单
//   node --experimental-strip-types scripts/design-primary-clarity-guard.mjs --selftest   # 自证：判据本身对不对
//   node --experimental-strip-types scripts/design-primary-clarity-guard.mjs --update-baseline

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'primaryClarity.ts')
const BASELINE = join(ROOT, 'scripts', 'design-primary-clarity-baseline.json')

const implUrl = pathToFileURL(IMPL).href

const body = `
import { scanPrimaryClarity, runPrimaryClaritySelftest, listVueFiles } from ${JSON.stringify(implUrl)}
const SRC = ${JSON.stringify(join(ROOT, 'src'))}
if (process.env.PC_MODE === 'selftest') {
  const r = runPrimaryClaritySelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const res = scanPrimaryClarity(listVueFiles(SRC), SRC)
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
    env: { ...process.env, PC_MODE: isSelftest ? 'selftest' : 'scan' },
  },
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
  : { competingZones: 0 }

// ── 棘轮：只降不升 ──
const regressions = []
if ((facts.competingZones ?? 0) > (baseline.competingZones ?? 0)) {
  regressions.push(`competingZones ${baseline.competingZones} → ${facts.competingZones}（升高了）`)
}

if (process.argv.includes('--update-baseline')) {
  if ((facts.competingZones ?? 0) > (baseline.competingZones ?? 0)) {
    console.error('❌ 基线只能降不能升 —— 当前实测值高于基线，先把违规清掉再收紧基线。')
    process.stderr.write('   ' + (regressions.join('\n   ') || '') + '\n')
    process.exit(1)
  }
  const next = {
    metric:
      'competingZones = 同一「动作区」（= 一组并排按钮的**直接父容器**）里并排出现 ≥2 个实心主色按钮的动作区数' +
      '（焦点区域与数据行内排除）',
    competingZones: facts.competingZones ?? 0,
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        competingZones: facts.competingZones ?? 0,
        updated: '2026-10-27',
        reason: process.env.PC_BASELINE_REASON || '人工收紧基线（只允许降）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`✅ 基线已收紧：competingZones=${next.competingZones}`)
  process.exit(0)
}

if (process.argv.includes('--json')) {
  console.log(
    JSON.stringify(
      {
        count: findings.length,
        facts,
        baseline: { competingZones: baseline.competingZones },
        regressions,
        findings,
      },
      null,
      2,
    ),
  )
} else if (process.argv.includes('--list')) {
  const byFile = new Map()
  for (const f of findings) {
    if (!byFile.has(f.file)) byFile.set(f.file, [])
    byFile.get(f.file).push(f)
  }
  console.log(
    `违规 ${findings.length} 个动作区 / ${byFile.size} 文件；涉及实心主色 ${facts.competingButtons ?? 0} 个` +
      `（全仓实心主色 ${facts.totalSolidPrimary ?? 0} · 焦点区域 ${facts.focusRegionSolidPrimary ?? 0} · 数据行内 ${facts.dataRowSolidPrimary ?? 0}）`,
  )
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) {
      console.log(`  动作区 <${f.zoneTag}${f.zoneClass ? ' class="' + f.zoneClass + '"' : ''}>  ${f.primaryCount} 个实心主色`)
      for (const b of f.buttons) console.log(`     L${b.line}  ${b.snippet}`)
      console.log(`     —— ${f.reason}`)
    }
  }
} else {
  for (const f of findings) {
    console.log(`${f.file}:${f.line}  动作区 <${f.zoneTag}>  ${f.primaryCount} 个实心主色\n    —— ${f.reason}`)
  }
  console.log(
    `\n读数：全仓实心主色 ${facts.totalSolidPrimary ?? 0} 个 · 焦点区域 ${facts.focusRegionSolidPrimary ?? 0} · ` +
      `数据行内 ${facts.dataRowSolidPrimary ?? 0} · 竞争动作区 ${facts.competingZones ?? 0}（涉及 ${facts.competingButtons ?? 0} 个按钮）`,
  )
  console.log(`基线：竞争动作区 ≤ ${baseline.competingZones}`)
}

if (findings.length) {
  console.error(`\nFAIL: ${findings.length} 个动作区里并排出现多个实心主色。`)
  console.error('修法：同一动作区里只留一个实心主色（那个「主动作」），其余降为次级样式（btn / btn-secondary）。')
  process.exit(1)
}
if (regressions.length) {
  console.error('\nFAIL: 棘轮指标升高。')
  console.error('  ' + regressions.join('\n  '))
  process.exit(1)
}
if ((facts.competingZones ?? 0) < (baseline.competingZones ?? 0)) {
  console.log('✅ 每个动作区只有一个实心主色 —— PASS（棘轮有余量，可用 --update-baseline 收紧基线）')
} else {
  console.log('✅ 每个动作区只有一个实心主色 —— PASS')
}
