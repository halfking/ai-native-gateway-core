#!/usr/bin/env node
// design-semantic-color-guard.mjs — 「颜色只承载语义，不承载分组」门 CLI（2026-10-27，第 21 增量）
//
// 判据实现：src/design/semanticColor.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**（与 scripts/design-primary-focus-guard.mjs 同一范式）：
//   判据要读 .vue 源码并做标签树/CSS 解析 ⇒ 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// ★★★ 判据来源：
//   · impeccable skill/reference/colorize.md：
//       "Decoration without a relationship to hierarchy, state, content, or the
//        visual world is not a color strategy."
//       "Let the strongest color own a deliberate region or role instead of
//        scattering tiny accents."
//   · impeccable skill/reference/operate.md：
//       "Accent color used for primary actions, current selection, and state
//        indicators only, not decoration."        ← rule:product-color-accent-only
//   · 仓内 DESIGN.md §视觉世界：「状态用最小的颜色量表达、每个元素都承担信息职责」
//
// ★★★ 判据（唯一一条）：
//
//   **承载「一组控件」的容器，底色/边框色必须中性。**
//
//   · 控件组容器：直接子元素里有 ≥2 个可交互控件
//   · 着色：底色/边框色里**现场**混入裸颜色令牌（语义底色令牌 `--success-bg` 等不算）
//   · 豁免（状态载体）：容器自身带状态词（静态类名或动态绑定键）
//
// ★ 棘轮：tintedGroupContainers **只降不升**。
//
// 用法：
//   node --experimental-strip-types scripts/design-semantic-color-guard.mjs              # 扫描 + 棘轮，有违规 exit 1
//   node --experimental-strip-types scripts/design-semantic-color-guard.mjs --json       # 机器可读
//   node --experimental-strip-types scripts/design-semantic-color-guard.mjs --list       # 按文件分组的工作单
//   node --experimental-strip-types scripts/design-semantic-color-guard.mjs --selftest   # 自证：判据本身对不对
//   node --experimental-strip-types scripts/design-semantic-color-guard.mjs --update-baseline

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'semanticColor.ts')
const BASELINE = join(ROOT, 'scripts', 'design-semantic-color-baseline.json')
const EXEMPTIONS = join(ROOT, 'scripts', 'design-semantic-color-exemptions.json')

const implUrl = pathToFileURL(IMPL).href

const body = `
import { scanSemanticColor, runSemanticColorSelftest } from ${JSON.stringify(implUrl)}
import { listVueFiles } from ${JSON.stringify(pathToFileURL(join(ROOT, 'src', 'design', 'primaryFocus.ts')).href)}
import { readFileSync, existsSync } from 'node:fs'
const SRC = ${JSON.stringify(join(ROOT, 'src'))}
const EX = ${JSON.stringify(EXEMPTIONS)}
if (process.env.SC_MODE === 'selftest') {
  const r = runSemanticColorSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const exemptions = existsSync(EX) ? JSON.parse(readFileSync(EX, 'utf8')).exemptions ?? [] : []
const res = scanSemanticColor(listVueFiles(SRC), SRC, exemptions)
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
    env: { ...process.env, SC_MODE: isSelftest ? 'selftest' : 'scan' },
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
  : { tintedGroupContainers: 0 }

// ── 棘轮：只降不升 ──
const regressions = []
if ((facts.tintedGroupContainers ?? 0) > (baseline.tintedGroupContainers ?? 0)) {
  regressions.push(
    `tintedGroupContainers ${baseline.tintedGroupContainers} → ${facts.tintedGroupContainers}（升高了）`,
  )
}

if (process.argv.includes('--update-baseline')) {
  if ((facts.tintedGroupContainers ?? 0) > (baseline.tintedGroupContainers ?? 0)) {
    console.error('❌ 基线只能降不能升 —— 当前实测值高于基线，先把违规清掉再收紧基线。')
    process.stderr.write('   ' + (regressions.join('\n   ') || '') + '\n')
    process.exit(1)
  }
  const next = {
    metric:
      'tintedGroupContainers = 底色/边框色里现场混入裸颜色令牌、且直接包含 ≥2 个可交互控件的容器数' +
      '（状态载体豁免：容器自身带状态词；语义底色令牌 --success-bg 等不算「现场混入」）',
    tintedGroupContainers: facts.tintedGroupContainers ?? 0,
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        tintedGroupContainers: facts.tintedGroupContainers ?? 0,
        updated: '2026-10-27',
        reason: process.env.SC_BASELINE_REASON || '人工收紧基线（只允许降）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`✅ 基线已收紧：tintedGroupContainers=${next.tintedGroupContainers}`)
  process.exit(0)
}

if (process.argv.includes('--json')) {
  console.log(
    JSON.stringify(
      {
        count: findings.length,
        facts,
        baseline: { tintedGroupContainers: baseline.tintedGroupContainers },
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
    `违规 ${findings.length} 处 / ${byFile.size} 文件；全仓现场着色声明 ${facts.totalTintedDeclarations ?? 0} 条` +
      `（状态载体豁免 ${facts.stateCarrierExempt ?? 0} · 登记豁免 ${facts.registeredExempt ?? 0}）`,
  )
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) {
      console.log(`  ${f.line}  ${f.selector}  [${f.tokens.join('/')}]  ${f.declaration}`)
      console.log(`      容器: ${f.snippet}`)
      console.log(`      —— ${f.reason}`)
    }
  }
} else {
  for (const f of findings) {
    console.log(
      `${f.file}:${f.line}  ${f.selector}  [${f.tokens.join('/')}]  ${f.declaration}\n    —— ${f.reason}`,
    )
  }
  console.log(
    `\n读数：全仓现场着色声明 ${facts.totalTintedDeclarations ?? 0} 条 · 状态载体豁免 ${facts.stateCarrierExempt ?? 0} · ` +
      `登记豁免 ${facts.registeredExempt ?? 0} · 控件组容器被着色 ${facts.tintedGroupContainers ?? 0}`,
  )
  console.log(`基线：控件组容器被着色 ≤ ${baseline.tintedGroupContainers}`)
}

if (findings.length) {
  console.error(`\nFAIL: ${findings.length} 处「分组容器被着色」。`)
  console.error('修法：分组容器改用中性底色（--bg-subtle / --border）；颜色留给组内「被选中的那一个」。')
  process.exit(1)
}
if (regressions.length) {
  console.error('\nFAIL: 棘轮指标升高。')
  console.error('  ' + regressions.join('\n  '))
  process.exit(1)
}
if ((facts.tintedGroupContainers ?? 0) < (baseline.tintedGroupContainers ?? 0)) {
  console.log('✅ 分组容器没有被着色 —— PASS（棘轮有余量，可用 --update-baseline 收紧基线）')
} else {
  console.log('✅ 分组容器没有被着色 —— PASS')
}
