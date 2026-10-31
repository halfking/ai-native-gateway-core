#!/usr/bin/env node
// design-action-density-guard.mjs — 「动作区必须可扫读」门 CLI（2026-10-27，第 25 增量）
//
// 判据实现：src/design/actionDensity.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**（与 scripts/design-primary-clarity-guard.mjs 同一范式）：
//   判据要读 .vue 源码并做标签树解析 ⇒ 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// ★★★ 判据来源：
//   · impeccable skill/reference/distill.md：
//       "Clear hierarchy: ONE primary action, few secondary actions,
//        everything else tertiary or hidden"
//       "Clear next action: ONE obvious next action, not five competing ones"
//   · impeccable skill/reference/layout.md（Density）：
//       "Does the amount of information per region fit use frequency,
//        decision complexity, and visitor mode?"
//   · impeccable skill/reference/craft-floor.md（Verify · Coverage）：
//       "every brief requirement present and findable within seconds"
//
// ★★★ 判据（唯一一条）：
//
//   **一个「动作区」里，最坏情况下的可见动作数 ≤ 4。**
//
//   · 动作区 = **直接父容器**（沿用第 22 增量已验证的口径，不靠类名）
//   · 可见数按 **Vue 的相邻规则**折叠 `v-if / v-else-if / v-else` 互斥链
//     ⇒ 静态计数不得把互斥分支算成多个（真仓读数：KeysView 抽屉 5 → 实际 3）
//   · 排除：焦点区域 · 数据行 · 导航/过滤/批量区 · 浮层（Teleport）内
//
// ★ 棘轮：overloadedZones **只降不升**。
//
// 用法：
//   node --experimental-strip-types scripts/design-action-density-guard.mjs              # 扫描 + 棘轮，有违规 exit 1
//   node --experimental-strip-types scripts/design-action-density-guard.mjs --json       # 机器可读（只出 JSON）
//   node --experimental-strip-types scripts/design-action-density-guard.mjs --list       # 按文件分组的工作单
//   node --experimental-strip-types scripts/design-action-density-guard.mjs --selftest   # 自证：判据本身对不对
//   node --experimental-strip-types scripts/design-action-density-guard.mjs --update-baseline

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'actionDensity.ts')
const BASELINE = join(ROOT, 'scripts', 'design-action-density-baseline.json')

const implUrl = pathToFileURL(IMPL).href

const body = `
import { scanActionDensity, runActionDensitySelftest, listVueFiles } from ${JSON.stringify(implUrl)}
const SRC = ${JSON.stringify(join(ROOT, 'src'))}
if (process.env.AD_MODE === 'selftest') {
  const r = runActionDensitySelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const res = scanActionDensity(listVueFiles(SRC), SRC)
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
    env: { ...process.env, AD_MODE: isSelftest ? 'selftest' : 'scan' },
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
  : { overloadedZones: 0 }

// ── 棘轮：只降不升 ──
const regressions = []
if ((facts.overloadedZones ?? 0) > (baseline.overloadedZones ?? 0)) {
  regressions.push(`overloadedZones ${baseline.overloadedZones} → ${facts.overloadedZones}（升高了）`)
}

if (process.argv.includes('--update-baseline')) {
  if ((facts.overloadedZones ?? 0) > (baseline.overloadedZones ?? 0)) {
    console.error('❌ 基线只能降不能升 —— 当前实测值高于基线，先把违规清掉再收紧基线。')
    process.stderr.write('   ' + (regressions.join('\n   ') || '') + '\n')
    process.exit(1)
  }
  const next = {
    metric:
      'overloadedZones = 一个「动作区」（= 一组并列动作的**直接父容器**）里，最坏情况下的可见动作数 > 4 的动作区数' +
      '（可见数按 Vue 相邻规则折叠 v-if/v-else-if/v-else 互斥链；焦点区域 / 数据行 / 导航过滤批量区 / 浮层内排除）',
    overloadedZones: facts.overloadedZones ?? 0,
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        overloadedZones: facts.overloadedZones ?? 0,
        updated: '2026-10-27',
        reason: process.env.AD_BASELINE_REASON || '人工收紧基线（只允许降）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`✅ 基线已收紧：overloadedZones=${next.overloadedZones}`)
  process.exit(0)
}

if (process.argv.includes('--json')) {
  // ★ `--json` 模式**只出 JSON**（下游会 JSON.parse；多一行人话就解析失败）
  console.log(
    JSON.stringify(
      {
        count: findings.length,
        facts,
        baseline: { overloadedZones: baseline.overloadedZones },
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
  const hist = facts.histogram ?? {}
  console.log(
    `违规 ${findings.length} 个动作区 / ${byFile.size} 文件；` +
      `全仓动作控件 ${facts.totalActionControls ?? 0} 个 · 参与判定的动作区 ${facts.actionZones ?? 0} 个` +
      `（焦点区域 ${facts.excludedFocusRegion ?? 0} · 数据行 ${facts.excludedDataRow ?? 0} · ` +
      `导航过滤批量区 ${facts.excludedNonActionZone ?? 0} · 浮层 ${facts.excludedOverlay ?? 0}）`,
  )
  console.log(
    `动作数分布：` +
      Object.entries(hist)
        .sort((a, b) => Number(a[0]) - Number(b[0]))
        .map(([k, v]) => `${k}→${v}`)
        .join(' · '),
  )
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) {
      console.log(
        `  动作区 <${f.zoneTag}${f.zoneClass ? ' class="' + f.zoneClass + '"' : ''}>  ${f.visibleCount} 个可见动作`,
      )
      for (const s of f.slots) console.log(`     L${s.line}  ${s.label}${s.conditional ? '  (互斥链)' : ''}`)
      console.log(`     —— ${f.reason}`)
    }
  }
} else {
  for (const f of findings) {
    console.log(
      `${f.file}:${f.line}  动作区 <${f.zoneTag}>  ${f.visibleCount} 个可见动作\n    —— ${f.reason}`,
    )
  }
  console.log(
    `\n读数：全仓动作控件 ${facts.totalActionControls ?? 0} 个 · 参与判定的动作区 ${facts.actionZones ?? 0} 个 · ` +
      `超阈值动作区 ${facts.overloadedZones ?? 0}（焦点区域 ${facts.excludedFocusRegion ?? 0} · ` +
      `数据行 ${facts.excludedDataRow ?? 0} · 导航过滤批量区 ${facts.excludedNonActionZone ?? 0} · 浮层 ${facts.excludedOverlay ?? 0}）`,
  )
  console.log(`基线：超阈值动作区 ≤ ${baseline.overloadedZones}`)
}

if (findings.length) {
  console.error(`\nFAIL: ${findings.length} 个动作区的可见动作数超过 4。`)
  console.error(
    '修法：把低频动作收进「更多」溢出菜单（可见动作 ≤ 4）；' +
      '若两个入口做的是同一件事，直接删掉冗余的那个。',
  )
  process.exit(1)
}
if (regressions.length) {
  console.error('\nFAIL: 棘轮指标升高。')
  console.error('  ' + regressions.join('\n  '))
  process.exit(1)
}
if ((facts.overloadedZones ?? 0) < (baseline.overloadedZones ?? 0)) {
  console.log('✅ 每个动作区的可见动作数 ≤ 4 —— PASS（棘轮有余量，可用 --update-baseline 收紧基线）')
} else {
  console.log('✅ 每个动作区的可见动作数 ≤ 4 —— PASS')
}
