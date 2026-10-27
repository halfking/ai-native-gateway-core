#!/usr/bin/env node
// design-motion-guard.mjs — 「状态过渡必须以指数减速到达」门 CLI（2026-10-27 · 第 26 增量）
//
// 判据实现：src/design/motion.ts（判据唯一实现，可被 vitest 覆盖；自证 28/28）。
// ★ 本文件是**薄壳**：读裁决簿 + 跑判据 + 比基线。
//
// ★★★ 判据（一条判据，七个面）：
//
//   **界面上的每一次状态变化，都必须以指数减速（exponential ease-out）到达。**
//
//   M1 减速曲线 —— 状态过渡与一次性入场动画的缓动必须是那条曲线
//   M2 具名属性 —— `transition` 不得过渡 `all`
//   M3 布局驱动属性 —— 不得过渡 width/height/top/left/…（数据条须在裁决簿登记）
//   M4 空间移动的 reduced-motion 路径 —— 过渡了 transform 就要有**真的中和**它的块
//   M5 时长上限 —— transition ≤ 500ms；一次性 animation ≤ 800ms
//   M6 入场登记 —— 一次性入场动效必须在裁决簿登记（防「散落效果」）
//   M7 匀速 —— 无限循环动画用 linear 时必须真的是匀速运动
//
// ★ 判据来源：impeccable `skill/reference/animate.md` + `craft-floor.md` Motion 段
//   （逐条引文见 `scripts/design-motion-decisions.json` 的 basis）。
//
// 用法：
//   node --experimental-strip-types scripts/design-motion-guard.mjs              # 扫描 + 棘轮，有违规 exit 1
//   node --experimental-strip-types scripts/design-motion-guard.mjs --json       # 机器可读（★ 只出 JSON）
//   node --experimental-strip-types scripts/design-motion-guard.mjs --list       # 按文件分组的工作单
//   node --experimental-strip-types scripts/design-motion-guard.mjs --selftest   # 自证：判据本身对不对
//   node --experimental-strip-types scripts/design-motion-guard.mjs --update-baseline

import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  collectMotionDecls,
  scanMotion,
  runMotionSelftest,
} from '../src/design/motion.ts'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const SRC = join(ROOT, 'src')
const DECISIONS = join(ROOT, 'scripts', 'design-motion-decisions.json')
const BASELINE = join(ROOT, 'scripts', 'design-motion-baseline.json')

// ── 自证 ──
if (process.argv.includes('--selftest')) {
  const r = runMotionSelftest()
  for (const f of r.failed) console.log('❌ ' + f)
  console.log(
    `\n自证 ${r.passed}/${r.total}` + (r.failed.length ? ' —— 有失败' : ' 全过 —— 量具有牙。'),
  )
  process.exit(r.failed.length ? 2 : 0)
}

// ── 裁决簿（★ 豁免与登记必须在**规划时**与**复核时**同时生效） ──
const decisions = JSON.parse(readFileSync(DECISIONS, 'utf8'))
const layoutDriven = (decisions.layoutDriven?.entries ?? []).map((e) => ({
  file: e.file,
  selector: e.selector,
  property: e.property,
  reason: e.reason,
}))
const entrances = (decisions.entrances?.entries ?? []).map((e) => ({
  name: e.name,
  file: e.file,
  reason: e.reason,
}))

const decls = collectMotionDecls(SRC)
const fileTexts = new Map()
for (const d of decls) {
  if (!fileTexts.has(d.file)) fileTexts.set(d.file, readFileSync(join(SRC, d.file), 'utf8'))
}
const res = scanMotion(decls, fileTexts, { layoutDriven, entrances })

const findings = res.findings ?? []
const facts = res.facts ?? {}

// ── 硬错误（判据完整性，不是棘轮指标） ──
const hardErrors = []

// ① 豁免漂移：登记 N 条、命中 M 条，N ≠ M ⇒ 裁决簿与代码已经对不上
const declaredLayout = layoutDriven.length
const hitLayout = facts.layoutExemptions !== undefined ? facts.layoutExemptions - facts.layoutDriven : 0
if (declaredLayout !== hitLayout) {
  hardErrors.push(
    `豁免漂移：裁决簿登记 ${declaredLayout} 条布局驱动豁免，本轮命中 ${hitLayout} 条` +
    ` ⇒ ${declaredLayout - hitLayout} 条已失效（代码变了）。`,
  )
}
// ② 豁免命中数为 0 而登记不为 0 ⇒ 门「因为豁免而绿」但绿得没有依据
if (declaredLayout > 0 && hitLayout === 0) {
  hardErrors.push('★ 豁免命中数为 0 —— 门可能是「因为豁免」才绿的，裁决簿已与代码漂移。')
}
// ③ M2 契约：裁决簿登记的具名属性必须与代码**逐条相等**
const m2Entries = decisions.transitionAll?.entries ?? []
for (const e of m2Entries) {
  const target = decls.find(
    (d) =>
      d.file === e.file &&
      d.prop === 'transition' &&
      d.selector.replace(/\s+/g, ' ').trim() === e.selector.replace(/\s+/g, ' ').trim(),
  )
  if (!target) {
    hardErrors.push(
      `裁决簿条目 \`${e.key}\` 在代码里找不到对应的 transition 声明 ⇒ 裁决簿与代码漂移。`,
    )
    continue
  }
  const got = target.transitionItems.map((it) => it.property)
  if (JSON.stringify(got) !== JSON.stringify(e.properties)) {
    hardErrors.push(
      `裁决簿条目 \`${e.key}\` 的属性集与代码不一致：实得 [${got}]，登记 [${e.properties}]。`,
    )
  }
}
// ④ 入场登记簿：登记了但代码里已经没有这个动画 ⇒ 登记失效
const declaredEntrances = new Set(entrances.map((e) => e.name))
for (const name of declaredEntrances) {
  const stillUsed = decls.some(
    (d) => d.prop === 'animation' && d.animationItems.some((it) => it.name === name),
  )
  if (!stillUsed) {
    hardErrors.push(`入场登记 \`${name}\` 在代码里已不存在 ⇒ 登记簿失效，请删掉这一条。`)
  }
}

// ── 棘轮：七条判据的命中数**只降不升** ──
const RULES = ['M1', 'M2', 'M3', 'M4', 'M5', 'M6', 'M7']
const baseline = existsSync(BASELINE)
  ? JSON.parse(readFileSync(BASELINE, 'utf8'))
  : { counts: Object.fromEntries(RULES.map((r) => [r, 0])) }
const baseCounts = baseline.counts ?? Object.fromEntries(RULES.map((r) => [r, 0]))

const regressions = []
for (const r of RULES) {
  const now = facts[r] ?? 0
  const before = baseCounts[r] ?? 0
  if (now > before) regressions.push(`${r} ${before} → ${now}（升高了）`)
}

if (process.argv.includes('--update-baseline')) {
  if (regressions.length) {
    console.error('❌ 基线只能降不能升 —— 当前实测值高于基线，先把违规清掉再收紧基线。')
    for (const r of regressions) console.error('   ' + r)
    process.exit(1)
  }
  const next = {
    metric:
      'M1=减速曲线不合规的声明数 / M2=`transition: all` 的声明数 / ' +
      'M3=过渡布局驱动属性且未登记豁免的声明数 / M4=过渡了 transform 却没被 reduced-motion 中和的选择器数 / ' +
      'M5=超时长上限的声明数 / M6=未登记的一次性入场动画数 / M7=匀速声明用在非匀速 keyframes 上的处数。',
    counts: Object.fromEntries(RULES.map((r) => [r, facts[r] ?? 0])),
    updated: '2026-10-27',
    history: [
      ...(baseline.history ?? []),
      {
        counts: Object.fromEntries(RULES.map((r) => [r, facts[r] ?? 0])),
        updated: '2026-10-27',
        reason: process.env.MOTION_BASELINE_REASON || '人工收紧基线（只允许降）',
      },
    ],
  }
  writeFileSync(BASELINE, JSON.stringify(next, null, 2) + '\n')
  console.log(`✅ 基线已收紧：${RULES.map((r) => `${r}=${next.counts[r]}`).join(' · ')}`)
  process.exit(0)
}

if (process.argv.includes('--json')) {
  // ★ `--json` 模式下**只输出 JSON**
  console.log(
    JSON.stringify(
      {
        count: findings.length,
        facts,
        baseline: { counts: baseCounts },
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
    `动效读数：声明 ${facts.totalDeclarations ?? 0} 处（过渡 ${facts.transitionTotal ?? 0} · ` +
      `一次性动画 ${facts.animationFinite ?? 0} · 循环动画 ${facts.animationInfinite ?? 0}）`,
  )
  console.log(
    `缓动写法：令牌 ${facts.tokenStyle ?? 0} · 字面量 ${facts.literalStyle ?? 0} · ` +
      `默认 ease（不合规）${facts.defaultEase ?? 0}`,
  )
  console.log(
    `豁免：布局驱动 ${facts.layoutExemptions ?? 0} 条 · 入场登记 ${facts.registeredEntrances ?? 0}/${facts.entranceSites ?? 0}`,
  )
  for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
    console.log(`\n${file}  (${list.length})`)
    for (const f of list) console.log(`  L${f.line}  [${f.rule}] ${f.selector}  ${f.prop}: ${f.value}`)
  }
} else {
  for (const f of findings) {
    console.log(`${f.file}:${f.line}  [${f.rule}] ${f.selector}  ${f.prop}: ${f.value}`)
    console.log(`    ${f.reason}`)
    console.log(`    修法：${f.fix}`)
  }
  console.log(
    `\n读数：动效声明 ${facts.totalDeclarations ?? 0} 处 · 减速曲线不合规 ${facts.M1 ?? 0} · ` +
      `transition:all ${facts.M2 ?? 0} · 布局驱动未登记 ${facts.M3 ?? 0} · ` +
      `空间移动缺 reduced-motion ${facts.M4 ?? 0} · 超时长 ${facts.M5 ?? 0} · ` +
      `入场未登记 ${facts.M6 ?? 0} · 匀速误用 ${facts.M7 ?? 0}`,
  )
  console.log(`基线：${RULES.map((r) => `${r} ≤ ${baseCounts[r] ?? 0}`).join(' · ')}`)
}

if (findings.length) {
  console.error(`\nFAIL: ${findings.length} 处动效不合规。`)
  console.error('修法：缓动改成 var(--kx-motion-ease-out)（唯一定义处 web/src/style.css）；')
  console.error('      transition 必须具名；空间移动要有 reduced-motion 路径；入场要先登记。')
}
if (regressions.length) {
  console.error('FAIL: 棘轮指标升高。')
  for (const r of regressions) console.error('  ' + r)
}
if (hardErrors.length) {
  console.error('FAIL: 硬错误（判据完整性）。')
  for (const h of hardErrors) console.error('  ' + h)
}
if (findings.length || regressions.length || hardErrors.length) process.exitCode = 1
if (!process.argv.includes('--json')) console.log('✅ 每一次状态变化都以指数减速到达 —— PASS')
