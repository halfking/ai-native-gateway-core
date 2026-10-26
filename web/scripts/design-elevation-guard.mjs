#!/usr/bin/env node
// design-elevation-guard.mjs — 「浮起词汇归一」门 CLI（2026-10-27，第 18 增量）
//
// 判据实现：src/design/elevation.ts（可被 vitest 覆盖）。
// ★ 本文件是**薄壳**（与 scripts/design-ai-smell-guard.mjs 同一范式）：
//   判据要读 .vue 的 <style> 段与 .css 全文 ⇒ 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//
// 判据来源：
//   · 仓内 DESIGN.md §2「阴影 → 只用 --kx-shadow-sm/md」+ §5「阴影词汇」四级别表
//     + §5 Material Rule 3「描边优先于阴影」
//   · impeccable skill/reference/craft-floor.md codex 段：
//     "Declare elevation once, border or shadow."
//
//   E1 配方归一  抬升层必须来自 var(--kx-shadow-xs|sm|md|md-left)
//
// ★ 与第 17 增量的 ai-smell 门口径**不重叠**（可证伪）：
//   ai-smell 管「有没有抬升 / 几何 / 零偏移」，本门管「抬升怎么写的」。
//
// ★ 棘轮：两个指标都**只降不升** ——
//   nonTokenElevation      非规范抬升层数（本轮目标 = 0）
//   distinctElevationValues 全仓不同抬升取值数（本轮目标 = 4 = 四支 token）
//
// 用法：
//   node scripts/design-elevation-guard.mjs              # 扫描 + 棘轮，有违规 exit 1
//   node scripts/design-elevation-guard.mjs --json       # 机器可读
//   node scripts/design-elevation-guard.mjs --list       # 按文件分组的工作单
//   node scripts/design-elevation-guard.mjs --selftest   # 自证：判据本身对不对
//   node scripts/design-elevation-guard.mjs --update-baseline   # 人工收紧基线（只允许降）

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'elevation.ts')
const BASELINE = join(ROOT, 'scripts', 'design-elevation-baseline.json')

const implUrl = pathToFileURL(IMPL).href

const body = `
import { scanElevation, runElevationSelftest } from ${JSON.stringify(implUrl)}
if (process.env.ELEV_MODE === 'selftest') {
  const r = runElevationSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log('\\n自证 ' + r.passed + '/' + r.total + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'))
  process.exit(r.failed.length ? 2 : 0)
}
const res = scanElevation(${JSON.stringify(join(ROOT, 'src'))}, ${JSON.stringify(ROOT)})
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
    env: { ...process.env, ELEV_MODE: isSelftest ? 'selftest' : 'scan' },
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

let res = { findings: [], facts: { nonTokenElevation: 0, distinctElevationValues: 0, elevationLayers: 0 }, files: 0 }
try {
  res = JSON.parse(r.stdout.trim() || '{}')
} catch {
  process.stderr.write(r.stdout || '')
  process.stderr.write(r.stderr || '')
  process.exit(2)
}

const findings = res.findings ?? []
const facts = res.facts ?? { nonTokenElevation: 0, distinctElevationValues: 0, elevationLayers: 0 }

const baseline = existsSync(BASELINE)
  ? JSON.parse(readFileSync(BASELINE, 'utf8'))
  : { nonTokenElevation: 0, distinctElevationValues: 0 }

// ── 棘轮：只降不升 ──
const regressions = []
if (facts.nonTokenElevation > baseline.nonTokenElevation) {
  regressions.push(`nonTokenElevation ${baseline.nonTokenElevation} → ${facts.nonTokenElevation}（升高了）`)
}
if (facts.distinctElevationValues > baseline.distinctElevationValues) {
  regressions.push(`distinctElevationValues ${baseline.distinctElevationValues} → ${facts.distinctElevationValues}（升高了）`)
}

if (process.argv.includes('--update-baseline')) {
  if (facts.nonTokenElevation > baseline.nonTokenElevation || facts.distinctElevationValues > baseline.distinctElevationValues) {
    console.error('❌ 基线只能降不能升 —— 当前实测值高于基线，先把违规清掉再收紧基线。')
    console.error('   ' + (regressions.join('\n   ') || ''))
    process.exit(1)
  }
  const next = {
    metric:
      'nonTokenElevation = 非规范抬升层数（抬升层必须来自 var(--kx-shadow-xs|sm|md|md-left)）；' +
      'distinctElevationValues = 全仓不同抬升取值数（跨文件去重）',
    nonTokenElevation: facts.nonTokenElevation,
    distinctElevationValues: facts.distinctElevationValues,
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        nonTokenElevation: facts.nonTokenElevation,
        distinctElevationValues: facts.distinctElevationValues,
        updated: '2026-10-27',
        reason: '第 18 增量：抬升配方归一（69 处抬升 / 34 种手写取值 → 四支规范 token）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(
    `✅ 基线已收紧：nonTokenElevation=${next.nonTokenElevation}  distinctElevationValues=${next.distinctElevationValues}`
  )
  process.exit(0)
}

// ★★ R87 轮（★ 掏空验证查出来的真洞）：★ 基线**不可用** ⇒ 必须红。
//
// ★ 为什么必须有：★ 下面两条棘轮都是「实测值 > 基线值 ⇒ 升高了」
// ⇒ ⇒ 基线字段一旦缺失，`baseline.nonTokenElevation` 就是 `undefined`
// ⇒ ⇒ `0 > undefined` 恒假 ⇒ ⇒ **棘轮静默失效**⇒ ⇒ 而门照样报「✅ PASS」
// ⇒ ⇒ **假绿**（★ 而「掏空基线 / 删掉基线字段」正是绕过这道门最省事的办法）。
// ★ ⇒ ⇒ 基线不可用 ⇒ 必须红（★ 红的理由要说清「棘轮没有上限」）。
//
// ★ `--update-baseline` 分支在上面就 return 了 ⇒ ⇒ 这里红**不会**挡住「重建基线」。
const baselineUsable =
  typeof baseline?.nonTokenElevation === 'number' &&
  typeof baseline?.distinctElevationValues === 'number'
if (!baselineUsable) {
  console.error('❌ 基线缺失或非法：scripts/design-elevation-baseline.json')
  console.error('   ⇒ 棘轮没有上限 ⇒ ⇒ 这道门此刻等于没设门槛 ⇒ ⇒ 必须红。')
  console.error('   修法：node scripts/design-elevation-guard.mjs --update-baseline')
  process.exit(1)
}

if (process.argv.includes('--json')) {
  console.log(
    JSON.stringify(
      {
        count: findings.length,
        facts,
        baseline: {
          nonTokenElevation: baseline.nonTokenElevation,
          distinctElevationValues: baseline.distinctElevationValues,
        },
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
    `违规 ${findings.length} 处 / ${byFile.size} 文件；抬升层 ${facts.elevationLayers} 层，不同取值 ${facts.distinctElevationValues} 种`
  )
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) console.log(`  ${f.line}  [${f.rule}]  ${f.selector}  「${f.text}」  —— ${f.reason}`)
  }
} else {
  for (const f of findings) {
    console.log(`${f.file}:${f.line}  [${f.rule}]  ${f.selector}  「${f.text}」  —— ${f.reason}`)
  }
  console.log(
    `\n读数：抬升层 ${facts.elevationLayers} 层 · 不同取值 ${facts.distinctElevationValues} 种 · 非规范配方 ${facts.nonTokenElevation} 处`
  )
  console.log(
    `基线：非规范配方 ≤ ${baseline.nonTokenElevation} · 不同取值 ≤ ${baseline.distinctElevationValues}`
  )
}

if (findings.length) {
  console.error(`\nFAIL: ${findings.length} 处手写抬升配方。`)
  console.error('修法：抬升层级只能来自 var(--kx-shadow-xs|sm|md|md-left)（DESIGN.md §5 阴影词汇表）。')
  process.exit(1)
}
if (regressions.length) {
  console.error(`\nFAIL: 棘轮指标升高。`)
  console.error('  ' + regressions.join('\n  '))
  process.exit(1)
}
if (
  facts.nonTokenElevation < baseline.nonTokenElevation ||
  facts.distinctElevationValues < baseline.distinctElevationValues
) {
  console.log('✅ 浮起配方全部走规范 token —— PASS（棘轮有余量，可用 --update-baseline 收紧基线）')
} else {
  console.log('✅ 浮起配方全部走规范 token —— PASS')
}
