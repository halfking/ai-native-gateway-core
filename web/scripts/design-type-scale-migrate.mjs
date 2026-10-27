#!/usr/bin/env node
// design-type-scale-migrate.mjs — 「字号必须落在角色阶上」的批量收敛器（2026-10-27 · 第 23 增量）
//
// ★★ 批量改造的三道闸（把「不可逆改写」的判据变成可证伪的等式）：
//   ① **声明总数不变**  —— 只改值，不增不减声明；
//   ② **改写后离阶 = 0** —— 重新用判据模块扫一遍（★ 同一个口径，不是另一份实现）；
//   ③ **非 font-size 内容逐字未变** —— 把改写前后的文本里所有 font-size 值区间
//      换成同一个占位符后比较 ⇒ **diff 只可能落在 font-size 上**。
//   ★ 闸③ 是最强的一条：它挡住「批量脚本顺手改了别的东西」。
//
// ★ 裁决来源（三份都可审计）：
//   · `scripts/design-type-scale-decisions.json` —— 语义平局档的裁决表 + 角色对齐表
//     + 人工图标豁免 + 死 CSS 清单；
//   · 就近阶（方向唯一时**唯一**，等距且 Δ≤0.5px 时取**下界** ⇒ 零裁量）；
//   · rem → px（同一阶的两份真值 ⇒ 收进 px；数值逐字不变）。
//
// 用法：
//   node --experimental-strip-types scripts/design-type-scale-migrate.mjs            # 只出清单（不落盘）
//   node --experimental-strip-types scripts/design-type-scale-migrate.mjs --apply    # 落盘 + 三道闸
//   node --experimental-strip-types scripts/design-type-scale-migrate.mjs --json     # 机器可读清单

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'typeScale.ts')
const DECISIONS = join(ROOT, 'scripts', 'design-type-scale-decisions.json')
const implUrl = pathToFileURL(IMPL).href

// ★ 父进程也要用判据模块（闸③ 要在**两份文本**上各算一遍区间 ⇒ 必须同源）
//   ⇒ 这里直接动态 import（父进程本身就带 --experimental-strip-types 跑）。
const { collectFontSizesFromText } = await import(implUrl)

const body = `
import { collectAllFontSizes, collectFontSizesFromText, collectIconExemptClasses, parseCanonicalScale, listStyleFiles, scanTypeScale } from ${JSON.stringify(implUrl)}
import { readFileSync } from 'node:fs'
const SRC = ${JSON.stringify(join(ROOT, 'src'))}
const SHEET = ${JSON.stringify(join(ROOT, 'src', 'style.css'))}
const steps = parseCanonicalScale(readFileSync(SHEET, 'utf8'))
const scalePx = [...new Set(steps.map((s) => s.px))].sort((a, b) => a - b)
const files = listStyleFiles(SRC)
const decls = collectAllFontSizes(files, SRC)
// ★★★ 图标豁免必须用**判据模块的同一份**（否则迁移脚本会改掉门放过的图标尺寸）
const iconExempt = [...collectIconExemptClasses(files, SRC)]
// ★ 判据的读数（**同一个口径**）：闸② 直接用它的 offScale / rem 数，不另写一份过滤
const manual = new Set(JSON.parse(readFileSync(${JSON.stringify(DECISIONS)}, 'utf8')).manualIconExemptions?.entries?.map((e) => e.class) ?? [])
const judged = scanTypeScale(files, SRC, scalePx, manual)
console.log(JSON.stringify({ scalePx, iconExempt, judgedFacts: judged.facts, decls: decls.map((d) => ({
  file: d.file, line: d.line, selector: d.selector, value: d.value,
  kind: d.classified.kind, px: d.classified.px, inMedia: d.inMedia,
  valueStart: d.valueStart, valueEnd: d.valueEnd,
})) }))
`

const r = spawnSync(
  process.execPath,
  ['--experimental-strip-types', '--no-warnings', '--input-type=module', '-e', body],
  { cwd: ROOT, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 },
)
if (r.status !== 0) {
  process.stderr.write(r.stderr || '')
  process.exit(r.status || 2)
}
const { scalePx, decls, iconExempt: iconExemptList, judgedFacts } = JSON.parse(r.stdout.trim())
const autoIcon = new Set(iconExemptList)

const decisions = JSON.parse(readFileSync(DECISIONS, 'utf8'))
const manualIcon = new Set((decisions.manualIconExemptions?.entries ?? []).map((e) => e.class))
const deadCss = new Set((decisions.deadCss?.entries ?? []).map((e) => e.class))
const tieDecisions = decisions.decisions ?? {}

// ── 规划 ──
const plan = []
const skipped = { manualIcon: 0, deadCss: 0, onScale: 0, dynamic: 0, token: 0 }

function lastClass(sel) {
  const m = sel.match(/\.([A-Za-z_][\w-]*)/g)
  return m && m.length ? m[m.length - 1].slice(1) : null
}

for (const d of decls) {
  const cls = lastClass(d.selector)
  if (d.kind === 'token') {
    skipped.token++
    continue
  }
  if (d.kind !== 'px' && d.kind !== 'rem') {
    skipped.dynamic++
    continue
  }
  if (cls && (manualIcon.has(cls) || autoIcon.has(cls))) {
    skipped.manualIcon++
    continue
  }
  if (cls && deadCss.has(cls)) {
    skipped.deadCss++
    continue
  }
  const px = d.px
  const onScale = scalePx.includes(px)
  let target = null
  let reason = ''

  // ①★ 媒体查询里的字号 = **响应式降档** ⇒ 方向与基准值相反。
  //   ⇒ 语义裁决表**不适用**（那张表是按基准规则写的）；
  //   ⇒ 取「阶上且 ≤ 原值的最大档」（保持降档阶梯）；若无（原值比阶底还小）⇒ 取阶底。
  if (d.inMedia) {
    if (onScale) {
      skipped.onScale++
      continue
    }
    const lower = scalePx.filter((s) => s <= px)
    target = lower.length ? lower[lower.length - 1] : scalePx[0]
    reason = '媒体查询降档'
  }

  // ② 角色对齐表（同一角色的所有取值 ⇒ 收成一个）
  //   ★ **完整选择器精确匹配**（不是「最后一个类名」）：`.mini-card__value small` 里
  //   font-size 作用在 `small` 元素上 ⇒ 它不属于「mini-card 数值」那个角色。
  // ③ 语义平局裁决表
  if (target === null) {
    const key = `${px}px|${d.selector}`
    if (tieDecisions[key] !== undefined) {
      target = tieDecisions[key]
      reason = '语义裁决'
    }
  }
  // ④ 方向唯一 ⇒ 就近阶（等距且 Δ≤0.5 ⇒ 取下界 ⇒ 零裁量）
  if (target === null && !onScale) {
    const sorted = [...scalePx].sort((a, b) => a - b)
    const dists = sorted.map((s) => Math.abs(s - px))
    const best = Math.min(...dists)
    target = sorted[dists.indexOf(best)]
    reason = '就近阶'
  }
  // ⑤ rem 写法 ⇒ 收进 px（数值不变）
  if (target === null && d.kind === 'rem') {
    target = px
    reason = 'rem→px'
  }
  if (target === null) {
    skipped.onScale++
    continue
  }
  // ★ 「无变化」的判据必须比**文本**，不能比数值 ——
  //   rem 写法（0.875rem）与 px 写法（14px）数值相同但**写法不同** ⇒ 仍要改。
  if (d.kind === 'px' && target === px) {
    skipped.onScale++
    continue
  }
  plan.push({
    file: d.file,
    line: d.line,
    selector: d.selector,
    from: d.value,
    to: `${target}px`,
    px,
    target,
    reason,
    inMedia: d.inMedia,
    valueStart: d.valueStart,
    valueEnd: d.valueEnd,
  })
}

// ── 输出清单 ──
if (process.argv.includes('--json')) {
  console.log(JSON.stringify({ plan, skipped, scalePx }, null, 2))
  process.exit(0)
}

const byFile = new Map()
for (const p of plan) {
  if (!byFile.has(p.file)) byFile.set(p.file, [])
  byFile.get(p.file).push(p)
}
console.log(`计划改写 ${plan.length} 处 / ${byFile.size} 个文件`)
console.log(
  `跳过：令牌写法 ${skipped.token} · 动态值 ${skipped.dynamic} · 人工图标豁免 ${skipped.manualIcon}` +
    ` · 死 CSS ${skipped.deadCss} · 已在阶上 ${skipped.onScale}`,
)
const byReason = {}
for (const p of plan) byReason[p.reason] = (byReason[p.reason] || 0) + 1
console.log('按理由：', byReason)
for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
  console.log(`\n${file}  (${list.length})`)
  for (const p of list) {
    console.log(`  L${p.line}  ${p.selector}  ${p.from} → ${p.to}  [${p.reason}]`)
  }
}

if (!process.argv.includes('--apply')) {
  console.log('\n（未加 --apply ⇒ 未落盘）')
  process.exit(0)
}

// ── 落盘 + 三道闸 ──
const SRC_DIR = join(ROOT, 'src')
const touched = new Map()
for (const p of plan) {
  if (!touched.has(p.file)) touched.set(p.file, [])
  touched.get(p.file).push(p)
}

const before = new Map()
const after = new Map()
for (const [file, list] of touched) {
  const abs = join(SRC_DIR, file)
  const text = readFileSync(abs, 'utf8')
  before.set(file, text)
  const sorted = [...list].sort((a, b) => b.valueStart - a.valueStart)
  let out = text
  for (const p of sorted) {
    const cur = out.slice(p.valueStart, p.valueEnd)
    if (cur !== p.from) {
      console.error(`❌ ${file}:${p.line} 区间不匹配：期望「${p.from}」，实际「${cur}」⇒ 中止，未写任何文件。`)
      process.exit(2)
    }
    out = out.slice(0, p.valueStart) + p.to + out.slice(p.valueEnd)
  }
  after.set(file, out)
}

// 闸①：声明总数不变（用同一个模块重扫 ⇒ 同一个口径）
// 闸③：非 font-size 内容逐字未变
//   ★★★ 这条判据的写法踩了**三个**坑，三个都记在这儿 ——
//   ① 「替换成固定长度占位符」比较 ⇒ 两侧区间长度不同 ⇒ 骨架长度天然不等 ⇒ 误判「变了」。
//   ② 「剔除区间」比较 ⇒ 剔除长度必须是**该文本里**的长度 ⇒ 用原文本长度去剔 after ⇒ 剔多了 ⇒ 误判。
//   ③ 即便两侧各用自己的长度剔除 ⇒ **区间坐标仍与文本不同源**（值改了长度，后面整段移位）⇒ 误判。
//   ⇒ 正解：**区间口径必须与被比较的文本同源** ——
//     用**同一个扫描器**在两份文本上各算一遍区间，各自删掉值，再比较剩下的内容。
function stripFontSizeValues(text, rel, isVue) {
  const decls = collectFontSizesFromText(text, rel, isVue)
  const sorted = [...decls].sort((a, b) => b.valueStart - a.valueStart)
  let out = text
  for (const d of sorted) out = out.slice(0, d.valueStart) + out.slice(d.valueEnd)
  return out
}
for (const [file, list] of touched) {
  const isVue = file.endsWith('.vue')
  const s1 = stripFontSizeValues(before.get(file), file, isVue)
  const s2 = stripFontSizeValues(after.get(file), file, isVue)
  if (s1 !== s2) {
    console.error(`❌ 闸③失败：${file} 的**非 font-size 内容**发生了变化 ⇒ 中止，未写任何文件。`)
    let i = 0
    while (i < s1.length && i < s2.length && s1[i] === s2[i]) i++
    console.error(`   第一处差异在偏移 ${i}：`)
    console.error(`   before: …${s1.slice(Math.max(0, i - 60), i + 60)}…`)
    console.error(`   after : …${s2.slice(Math.max(0, i - 60), i + 60)}…`)
    process.exit(2)
  }
}

// 落盘
for (const [file, text] of after) writeFileSync(join(SRC_DIR, file), text)

// 闸②：重新用判据模块扫 ⇒ 离阶必须是 0
const verify = spawnSync(
  process.execPath,
  ['--experimental-strip-types', '--no-warnings', '--input-type=module', '-e', body],
  { cwd: ROOT, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 },
)
if (verify.status !== 0) {
  console.error('❌ 闸②：重扫失败。')
  process.exit(2)
}
const v = JSON.parse(verify.stdout.trim())
// ★ 闸② 用**判据模块自己的读数**（含图标豁免 + 人工豁免）⇒ 不另写一份过滤
const stillOff = v.judgedFacts?.offScale ?? -1
const remLeft = v.decls.filter((d) => d.kind === 'rem').length
if (v.decls.length !== decls.length) {
  console.error(
    `❌ 闸①失败：font-size 声明总数 ${decls.length} → ${v.decls.length}（变了）⇒ 请检查后决定回滚。`,
  )
  process.exit(2)
}
if (stillOff !== 0 || remLeft !== 0) {
  console.error(
    `❌ 闸②失败：判据读数离阶 ${stillOff} 处、rem ${remLeft} 处 ⇒ 文件已改，请检查后决定回滚或补齐。`,
  )
  process.exit(2)
}
console.log(
  `\n✅ 三道闸全过：声明总数不变（${decls.length}）· 离阶 0 · rem 0 · 非 font-size 内容逐字未变`,
)
console.log(`   落盘 ${after.size} 个文件 / ${plan.length} 处改写`)
