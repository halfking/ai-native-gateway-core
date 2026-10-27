#!/usr/bin/env node
// design-type-scale-guard.mjs — 「字号必须落在角色阶上」门 CLI（2026-10-27，第 23 增量）
//
// 判据实现：src/design/typeScale.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**（与 scripts/design-primary-clarity-guard.mjs 同一范式）：
//   判据要读 .vue / .css 源码并做 CSS 解析 ⇒ 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// ★★★ 判据来源：
//   · 仓内 DESIGN.md §4 Typography：已声明 9 档角色级字号阶
//     （--kx-text-2xs 11 / xs 12 / sm 13 / md 14 / lg 16 / xl 20 / 2xl 24 / 3xl 30 / 4xl 38）
//     与 §2 消费规则「字号 → --kx-text-*（见 §4）」「新组件一律消费令牌，禁止手写魔法数」
//   · impeccable skill/reference/typeset.md：
//       "Scale and consistency: Is there a deliberate role scale,
//        or a collection of arbitrary values?"
//       "Are adjacent sizes or weights too close to carry different jobs?"
//       "Keep repeated roles consistent across screens and states."
//
// ★★★ 判据（唯一一条）：
//
//   **font-size 的静态长度值必须落在已声明的角色阶上。**
//
//   · 「阶」= web/src/style.css 里 --kx-text-* 的**实际定义**（运行时解析，不硬编码）
//   · 不判：em / clamp() / calc() / var() / inherit（静态不可定值 ⇒ 单独计数）
//   · rem **要判**（根字号 16px ⇒ 可静态换算）；rem 与 px 并存是「同一阶两份真值」
//
// ★ 棘轮：offScaleFontSizes **只降不升**。
//
// 用法：
//   node --experimental-strip-types scripts/design-type-scale-guard.mjs              # 扫描 + 棘轮，有违规 exit 1
//   node --experimental-strip-types scripts/design-type-scale-guard.mjs --json       # 机器可读
//   node --experimental-strip-types scripts/design-type-scale-guard.mjs --list       # 按文件分组的工作单
//   node --experimental-strip-types scripts/design-type-scale-guard.mjs --selftest   # 自证：判据本身对不对
//   node --experimental-strip-types scripts/design-type-scale-guard.mjs --update-baseline

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'typeScale.ts')
const BASELINE = join(ROOT, 'scripts', 'design-type-scale-baseline.json')
const DECISIONS = join(ROOT, 'scripts', 'design-type-scale-decisions.json')
const STYLESHEET = join(ROOT, 'src', 'style.css')

const implUrl = pathToFileURL(IMPL).href

const body = `
import { scanTypeScale, runTypeScaleSelftest, listStyleFiles, parseCanonicalScale } from ${JSON.stringify(implUrl)}
import { readFileSync as __rf } from 'node:fs'
import { readFileSync } from 'node:fs'
const SRC = ${JSON.stringify(join(ROOT, 'src'))}
const SHEET = ${JSON.stringify(STYLESHEET)}
if (process.env.TS_MODE === 'selftest') {
  const r = runTypeScaleSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const steps = parseCanonicalScale(readFileSync(SHEET, 'utf8'))
const scalePx = [...new Set(steps.map((s) => s.px))].sort((a, b) => a - b)
// ★ 人工图标豁免与迁移脚本、裁决簿**同一份**（★ 同一口径只能有一份实现）
const __dec = JSON.parse(__rf(${JSON.stringify(DECISIONS)}, 'utf8'))
const __manual = new Set((__dec.manualIconExemptions?.entries ?? []).map((e) => e.class))
const res = scanTypeScale(listStyleFiles(SRC), SRC, scalePx, __manual)
res.facts.steps = steps
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
    env: { ...process.env, TS_MODE: isSelftest ? 'selftest' : 'scan' },
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
const remFindings = res.remFindings ?? []
const facts = res.facts ?? {}

const baseline = existsSync(BASELINE)
  ? JSON.parse(readFileSync(BASELINE, 'utf8'))
  : { offScaleFontSizes: 0, remFontSizes: 0 }

// ── 棘轮：只降不升 ──
const regressions = []
if ((facts.offScale ?? 0) > (baseline.offScaleFontSizes ?? 0)) {
  regressions.push(`offScaleFontSizes ${baseline.offScaleFontSizes} → ${facts.offScale}（升高了）`)
}
if ((facts.remFontSizes ?? 0) > (baseline.remFontSizes ?? 0)) {
  regressions.push(`remFontSizes ${baseline.remFontSizes} → ${facts.remFontSizes}（升高了）`)
}

if (process.argv.includes('--update-baseline')) {
  if (
    (facts.offScale ?? 0) > (baseline.offScaleFontSizes ?? 0) ||
    (facts.remFontSizes ?? 0) > (baseline.remFontSizes ?? 0)
  ) {
    console.error('❌ 基线只能降不能升 —— 当前实测值高于基线，先把违规清掉再收紧基线。')
    process.stderr.write('   ' + (regressions.join('\n   ') || '') + '\n')
    process.exit(1)
  }
  const next = {
    metric:
      'offScaleFontSizes = 静态 font-size 声明里，值**不落在** DESIGN.md §4 声明的角色阶' +
      '（--kx-text-* 的实际取值集合）上的声明数；em / clamp() / calc() / var() / inherit 不计（静态不可定值）',
    offScaleFontSizes: facts.offScale ?? 0,
    remFontSizes: facts.remFontSizes ?? 0,
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        offScaleFontSizes: facts.offScale ?? 0,
        remFontSizes: facts.remFontSizes ?? 0,
        updated: '2026-10-27',
        reason: process.env.TS_BASELINE_REASON || '人工收紧基线（只允许降）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(
    `✅ 基线已收紧：offScaleFontSizes=${next.offScaleFontSizes} · remFontSizes=${next.remFontSizes}`,
  )
  process.exit(0)
}

const scaleStr = (facts.scalePx ?? []).join('/')

if (process.argv.includes('--json')) {
  console.log(
    JSON.stringify(
      {
        count: findings.length,
        facts,
        baseline: { offScaleFontSizes: baseline.offScaleFontSizes },
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
    `离阶 ${findings.length} 处 / ${byFile.size} 文件；总声明 ${facts.totalDeclarations ?? 0} 处` +
      `（在阶 ${facts.onScale ?? 0} · 令牌写法 ${facts.tokenStyle ?? 0} · 静态不可定值 ${facts.dynamic ?? 0}）`,
  )
  console.log(`角色阶：${scaleStr}`)
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) {
      const tie = f.tie !== null && f.tie !== undefined ? ` ↔${f.tie}` : ''
      console.log(
        `  L${f.line}  ${f.selector}  ${f.value} → 就近 ${f.nearest}px(Δ${f.distance})${tie}`,
      )
    }
  }
} else {
  for (const f of findings) {
    console.log(`${f.file}:${f.line}  ${f.selector}  font-size: ${f.value}`)
  }
  for (const f of remFindings) {
    console.log(`${f.file}:${f.line}  ${f.selector}  font-size: ${f.value}  （rem 写法：同一阶的第二份真值）`)
  }
  console.log(
    `\n读数：font-size 声明 ${facts.totalDeclarations ?? 0} 处（在阶 ${facts.onScale ?? 0} · ` +
      `令牌写法 ${facts.tokenStyle ?? 0} · 静态不可定值 ${facts.dynamic ?? 0}）· ` +
      `离阶 ${facts.offScale ?? 0}（${facts.offScaleFiles ?? 0} 个文件）· rem 写法 ${facts.remFontSizes ?? 0}`,
  )
  console.log(`角色阶：${scaleStr}`)
  console.log(`基线：离阶字号 ≤ ${baseline.offScaleFontSizes} · rem 写法 ≤ ${baseline.remFontSizes ?? 0}`)
}

if (findings.length) {
  console.error(`\nFAIL: ${findings.length} 处字号落在角色阶之外。`)
  console.error('修法：收到就近的角色阶值（10px/9px/8px/7px 一律收进 11px —— 差 1px 的两档不可区分）。')
}
if (remFindings.length) {
  console.error(`\nFAIL: ${remFindings.length} 处字号用 rem 表达（同一阶的第二份真值）。`)
  console.error('修法：换成 px（数值逐字不变，零视觉变化）。')
}
if (regressions.length) {
  console.error('FAIL: 棘轮指标升高。')
  console.error('  ' + regressions.join('\n  '))
}
if (findings.length || remFindings.length || regressions.length) process.exit(1)
// ★ `--json` 模式下**只输出 JSON** —— 追加一行人话就会让下游 `JSON.parse` 失败
//   （实测踩过：变异臂读数取不到，误以为门没跑）。
if (!process.argv.includes('--json')) console.log('✅ 每个字号都落在角色阶上 —— PASS')
