#!/usr/bin/env node
// design-spacing-scale-guard.mjs — 「间距必须落在角色阶上」门 CLI（2026-10-27，第 24 增量）
//
// 判据实现：src/design/spacingScale.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**（与 scripts/design-type-scale-guard.mjs 同一范式）：
//   判据要读 .vue / .css 源码并做 CSS 解析 ⇒ 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// ★★★ 判据来源：
//   · 仓内 DESIGN.md §2 消费规则：间距 → `--kx-space-1..9`
//     （4 / 8 / 12 / 16 / 24 / 32 / 40 / 48 / 64；2026-10-27 从 6 档补到 9 档）
//     与「新组件一律消费令牌，禁止手写魔法数」
//   · impeccable skill/reference/layout.md：
//       "Rhythm: Do tight and generous intervals create a deliberate cadence,
//        or is one spacing value repeated until everything has equal weight?"
//       "Use a documented spacing scale rather than one-off values.
//        A 4-unit base usually provides the useful middle steps that an 8-only scale misses."
//       "Make optical corrections only after inspecting the rendered result."
//
// ★★★ 判据（唯一一条）：
//
//   **margin / padding / gap 的静态长度值必须落在已声明的间距阶上。**
//
//   · 「阶」= web/src/style.css 里 --kx-space-* 的**实际定义**（运行时解析，不硬编码）
//   · 不判：auto / none / inherit / % / em / vh / vw / calc() / 其它 var()（静态不可定值）
//   · 不判：0、负值（抵消形状）、≤3px（发丝 / 光学微调）—— ★ 豁免计数必须**可见**
//   · rem **要判**（根字号 16px ⇒ 可静态换算）；rem 与 px 并存是「同一阶两份真值」
//
// ★ 棘轮：offScaleSpacing 与 remSpacing **各自只降不升**。
//
// 用法：
//   node --experimental-strip-types scripts/design-spacing-scale-guard.mjs              # 扫描 + 棘轮，有违规 exit 1
//   node --experimental-strip-types scripts/design-spacing-scale-guard.mjs --json       # 机器可读（★ 只出 JSON）
//   node --experimental-strip-types scripts/design-spacing-scale-guard.mjs --list       # 按文件分组的工作单
//   node --experimental-strip-types scripts/design-spacing-scale-guard.mjs --selftest   # 自证：判据本身对不对
//   node --experimental-strip-types scripts/design-spacing-scale-guard.mjs --update-baseline

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'spacingScale.ts')
const BASELINE = join(ROOT, 'scripts', 'design-spacing-scale-baseline.json')
const DECISIONS = join(ROOT, 'scripts', 'design-spacing-scale-decisions.json')
const STYLESHEET = join(ROOT, 'src', 'style.css')

const implUrl = pathToFileURL(IMPL).href

const body = `
import { scanSpacingScale, runSpacingSelftest, listStyleFiles, parseCanonicalSpaceScale } from ${JSON.stringify(implUrl)}
import { readFileSync as __rf } from 'node:fs'
import { readFileSync } from 'node:fs'
const SRC = ${JSON.stringify(join(ROOT, 'src'))}
const SHEET = ${JSON.stringify(STYLESHEET)}
if (process.env.SS_MODE === 'selftest') {
  const r = runSpacingSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const steps = parseCanonicalSpaceScale(readFileSync(SHEET, 'utf8'))
const scalePx = [...new Set(steps.map((s) => s.px))].sort((a, b) => a - b)
// ★ 人工裁决与迁移脚本**同一份**（★ 同一口径只能有一份实现）
const __dec = JSON.parse(__rf(${JSON.stringify(DECISIONS)}, 'utf8'))
const __ex = (__dec.manualExemptions?.entries ?? []).map((e) => e.key)
const res = scanSpacingScale(listStyleFiles(SRC), SRC, scalePx, { exemptions: __ex })
res.facts.steps = steps
res.facts.decisions = __dec.decisions ?? {}
res.facts.manualExemptions = __dec.manualExemptions?.entries ?? []
res.facts.declaredExemptions = __ex.length
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
    env: { ...process.env, SS_MODE: isSelftest ? 'selftest' : 'scan' },
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

// ── 硬错误（**不是**棘轮指标）：它们判的是「本门有没有盲区」──
//   ① 扫描器盲区：声明值里有没被切分的数值令牌 ⇒ 本门对那条声明是瞎的。
//   ② 豁免漂移：裁决簿登记了 N 条豁免，但本轮**一条都没命中**
//      ⇒ 裁决簿与代码已经对不上 ⇒ 门会「因为豁免而绿」但绿得没有依据。
//      ★ 「豁免命中数为 0」是危险信号，不是「干净」。
const hardErrors = []
if ((facts.missedTokenDecls ?? 0) > 0) {
  hardErrors.push(
    `扫描器盲区 ${facts.missedTokenDecls} 处 —— 声明值里有没被切分的数值令牌 ⇒ 本门对那些声明是盲的。`,
  )
}
if ((facts.declaredExemptions ?? 0) > 0 && (facts.exempted ?? 0) === 0) {
  hardErrors.push(
    `豁免漂移：裁决簿登记了 ${facts.declaredExemptions} 条人工豁免，但本轮**一条都没命中** ⇒ 裁决簿已与代码漂移。`,
  )
}
if ((facts.declaredExemptions ?? 0) !== (facts.exempted ?? 0)) {
  hardErrors.push(
    `豁免部分失效：登记 ${facts.declaredExemptions} 条，命中 ${facts.exempted ?? 0} 条 ⇒ 有 ${(facts.declaredExemptions ?? 0) - (facts.exempted ?? 0)} 条已失效（代码变了）。`,
  )
}

const baseline = existsSync(BASELINE)
  ? JSON.parse(readFileSync(BASELINE, 'utf8'))
  : { offScaleSpacing: 0, remSpacing: 0 }

// ── 棘轮：只降不升 ──
const regressions = []
if ((facts.offScale ?? 0) > (baseline.offScaleSpacing ?? 0)) {
  regressions.push(`offScaleSpacing ${baseline.offScaleSpacing} → ${facts.offScale}（升高了）`)
}
if ((facts.remSpacing ?? 0) > (baseline.remSpacing ?? 0)) {
  regressions.push(`remSpacing ${baseline.remSpacing} → ${facts.remSpacing}（升高了）`)
}

if (process.argv.includes('--update-baseline')) {
  if (
    (facts.offScale ?? 0) > (baseline.offScaleSpacing ?? 0) ||
    (facts.remSpacing ?? 0) > (baseline.remSpacing ?? 0)
  ) {
    console.error('❌ 基线只能降不能升 —— 当前实测值高于基线，先把违规清掉再收紧基线。')
    process.stderr.write('   ' + (regressions.join('\n   ') || '') + '\n')
    process.exit(1)
  }
  const next = {
    metric:
      'offScaleSpacing = margin / padding / gap 声明里，值**不落在** DESIGN.md §2 声明的间距阶' +
      '（--kx-space-* 的实际取值集合）上的声明数；auto / none / inherit / % / em / vh / vw / calc() / 其它 var() 不计' +
      '（静态不可定值）；0、负值（抵消形状）、≤3px（发丝 / 光学微调）不计。' +
      'remSpacing = 用 rem 表达的间距声明数（同一阶的第二份真值）。',
    offScaleSpacing: facts.offScale ?? 0,
    remSpacing: facts.remSpacing ?? 0,
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        offScaleSpacing: facts.offScale ?? 0,
        remSpacing: facts.remSpacing ?? 0,
        updated: '2026-10-27',
        reason: process.env.SS_BASELINE_REASON || '人工收紧基线（只允许降）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(
    `✅ 基线已收紧：offScaleSpacing=${next.offScaleSpacing} · remSpacing=${next.remSpacing}`,
  )
  process.exit(0)
}

const scaleStr = (facts.scalePx ?? (facts.steps ?? []).map((s) => s.px).join('/')).join?.('/') ??
  (facts.steps ?? []).map((s) => s.px).join('/')

if (process.argv.includes('--json')) {
  // ★ `--json` 模式下**只输出 JSON** —— 追加一行人话就会让下游 `JSON.parse` 失败
  console.log(
    JSON.stringify(
      {
        count: findings.length,
        facts,
        baseline: { offScaleSpacing: baseline.offScaleSpacing, remSpacing: baseline.remSpacing },
        regressions,
        hardErrors,
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
    `离阶 ${findings.length} 处 / ${byFile.size} 文件；总声明 ${facts.totalDeclarations ?? 0} 处` +
      `（在阶 ${facts.onScale ?? 0} · 令牌写法 ${facts.tokenStyle ?? 0} · 静态不可定值 ${facts.dynamic ?? 0}` +
      ` · 0 ${facts.zero ?? 0} · 发丝/微调 ${facts.hairline ?? 0} · 抵消 ${facts.negative ?? 0}` +
      ` · 人工豁免 ${facts.exempted ?? 0}）`,
  )
  console.log(`间距阶：${scaleStr}`)
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) {
      const parts = f.parts
        .map((p) =>
          p.kind === 'rem'
            ? `${p.raw}→${p.px}px(rem)`
            : `${p.raw}→${p.nearest}px${p.tie !== null && p.tie !== undefined ? ` ↔${p.tie}` : ''}`,
        )
        .join(' ')
      console.log(`  L${f.line}  ${f.selector}  ${f.property}: ${f.value}  ⇒ ${parts}`)
    }
  }
} else {
  for (const f of findings) {
    console.log(`${f.file}:${f.line}  ${f.selector}  ${f.property}: ${f.value}`)
  }
  console.log(
    `\n读数：间距声明 ${facts.totalDeclarations ?? 0} 处（在阶 ${facts.onScale ?? 0} · ` +
      `令牌写法 ${facts.tokenStyle ?? 0} · 静态不可定值 ${facts.dynamic ?? 0} · 0 ${facts.zero ?? 0} · ` +
      `发丝/微调 ${facts.hairline ?? 0} · 抵消 ${facts.negative ?? 0} · 人工豁免 ${facts.exempted ?? 0}）· ` +
      `离阶 ${facts.offScale ?? 0}（${facts.offScaleFiles ?? 0} 个文件）· rem 写法 ${facts.remSpacing ?? 0}`,
  )
  console.log(`间距阶：${scaleStr}`)
  console.log(`基线：离阶间距 ≤ ${baseline.offScaleSpacing} · rem 写法 ≤ ${baseline.remSpacing ?? 0}`)
}

if (findings.length) {
  console.error(`\nFAIL: ${findings.length} 处间距落在角色阶之外。`)
  console.error('修法：收到就近的间距阶值（平局取下界 —— 运维台要密度，不要均匀的松）。')
}
if (regressions.length) {
  console.error('FAIL: 棘轮指标升高。')
  console.error('  ' + regressions.join('\n  '))
}
if (hardErrors.length) {
  console.error('FAIL: 硬错误（判据完整性）。')
  console.error('  ' + hardErrors.join('\n  '))
}
// ★ 用 `exitCode` 而不是 `process.exit(1)` ⇒ stdout 会被完整 flush
//   （`process.exit()` 在管道上会截断大输出 ⇒ 下游 `JSON.parse` 会读到半个 JSON）
if (findings.length || regressions.length || hardErrors.length) process.exitCode = 1
// ★ `--json` 模式下**只输出 JSON**（追加一行人话会让下游 `JSON.parse` 失败）
if (!process.argv.includes('--json')) console.log('✅ 每个间距都落在角色阶上 —— PASS')
