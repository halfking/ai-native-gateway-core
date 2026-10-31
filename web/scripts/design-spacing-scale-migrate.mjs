#!/usr/bin/env node
// design-spacing-scale-migrate.mjs — 「间距必须落在角色阶上」的批量收敛器（2026-10-27 · 第 24 增量）
//
// ★★ 批量改造的闸（把「不可逆改写」的判据变成可证伪的等式）：
//   ① **声明总数不变**  —— 只改值，不增不减声明；
//   ② **改写后离阶 = 0 且 rem = 0** —— 重新用判据模块扫一遍（★ 同一个口径，不是另一份实现）；
//   ③ **非间距内容逐字未变** —— 把改写前后的文本里所有间距**分量**区间
//      换成同一个占位符后比较 ⇒ **diff 只可能落在间距上**。
//   ④ **值语义守恒** —— ★ **独立复算**每一条的目标值：按它声称的理由重新算一遍，必须相等。
//   ⑤ **比值闸** —— 收缩/放大超过 2× 的改写**必须**有裁决簿里的显式条目，否则中止。
//   ⑥ **令牌完整性** —— 声明值里不能有**没被任何分量覆盖**的数值令牌（扫描器盲区 ⇒ 中止）。
//
// ★★ 第 ④⑤⑥ 道闸是**本轮补的** —— 它们的存在理由是一次真实事故：
//   `.35rem` 被数字正则截成 `35rem` ⇒ ① 小数点落在区间外 ② px 被算成 560（真值 5.6）
//   ⇒ 改写后留下 `.64px`（5.6px 变成 0.64px）⇒ 而 **① ② ③ 三道闸全过**：
//     闸③ 恰恰因为小数点被当成「非间距内容」而放行；闸② 因为 0.64px ≤ 3px 落在发丝豁免里。
//   ⇒ ★★ **闸③ 结构上挡不住「区间选错」**（它只保证区间之外逐字未变）；
//     ⇒ 必须补一道**独立于 px 换算**的**令牌完整性**闸（⑥），
//        再补一道**语义合理性**闸（⑤：比值）与**规则正确性**闸（④：独立复算）。
//
// ★ 裁决来源（三份都可审计）：
//   · `scripts/design-spacing-scale-decisions.json` —— 平局档的裁决表 + 死 CSS 清单 + 人工豁免；
//   · 就近阶（方向唯一时唯一；**平局取下界** ⇒ 运维台要密度，不要均匀的松）；
//   · rem → px（同一阶的两份真值 ⇒ 收进 px；数值不变）。
//
// 用法：
//   node --experimental-strip-types scripts/design-spacing-scale-migrate.mjs            # 只出清单（不落盘）
//   node --experimental-strip-types scripts/design-spacing-scale-migrate.mjs --apply    # 落盘 + 六道闸
//   node --experimental-strip-types scripts/design-spacing-scale-migrate.mjs --json     # 机器可读清单

import { spawnSync } from 'node:child_process'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const IMPL = join(ROOT, 'src', 'design', 'spacingScale.ts')
const DECISIONS = join(ROOT, 'scripts', 'design-spacing-scale-decisions.json')
const implUrl = pathToFileURL(IMPL).href

// ★ 父进程也要用判据模块（闸③ 要在**两份文本**上各算一遍区间 ⇒ 必须同源）
const { collectSpacingDeclsFromText, nearestSpaceSteps, HAIRLINE_MAX_PX, ROOT_FONT_PX } =
  await import(implUrl)

const body = `
import { scanSpacingScale, listStyleFiles, parseCanonicalSpaceScale, collectSpacingDeclsFromText } from ${JSON.stringify(implUrl)}
import { readFileSync } from 'node:fs'
const SRC = ${JSON.stringify(join(ROOT, 'src'))}
const SHEET = ${JSON.stringify(join(ROOT, 'src', 'style.css'))}
const DECISIONS = ${JSON.stringify(DECISIONS)}
const steps = parseCanonicalSpaceScale(readFileSync(SHEET, 'utf8'))
const scalePx = [...new Set(steps.map((s) => s.px))].sort((a, b) => a - b)
const files = listStyleFiles(SRC)
// ★★ 豁免必须**在重扫时也生效** —— 否则「派生对齐偏移」会被算成离阶 ⇒ 闸② 永远红。
//   ★ 这就是「同一口径只能有一份实现」的又一个面：豁免既要在规划时生效，也要在复核时生效。
const __dec = JSON.parse(readFileSync(DECISIONS, 'utf8'))
const __ex = (__dec.manualExemptions?.entries ?? []).map((e) => e.key)
const judged = scanSpacingScale(files, SRC, scalePx, { exemptions: __ex })
console.log(JSON.stringify({ scalePx, judgedFacts: judged.facts, findings: judged.findings }))
`

const r = spawnSync(
  process.execPath,
  ['--experimental-strip-types', '--no-warnings', '--input-type=module', '-e', body],
  { cwd: ROOT, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 },
)
if (r.status !== 0) {
  process.stderr.write(r.stderr || '')
  process.exit(2)
}
const { scalePx, findings, judgedFacts } = JSON.parse(r.stdout.trim())

const decisions = JSON.parse(readFileSync(DECISIONS, 'utf8'))
const tieDecisions = decisions.decisions ?? {}
const deadCss = new Set((decisions.deadCss?.entries ?? []).map((e) => e.class))
// ★ 人工豁免：**分量级**键 `<相对文件>|<选择器>|<属性>|<原始值>`（★ 不带行号 —— 行号会漂移）
const manualExempt = new Set((decisions.manualExemptions?.entries ?? []).map((e) => e.key))

/** 规则里出现过的 class（死 CSS 判据用）—— 取选择器里的**全部** class。 */
function classesOf(sel) {
  return (sel.match(/\.[A-Za-z_][\w-]*/g) || []).map((s) => s.slice(1))
}

/**
 * ★★ 独立的 px 换算（**不复用判据模块的 parseFloat 路径**）。
 *   判据模块是「整串直接 parseFloat」，这里是「先剥单位后 parseFloat」⇒ 两条路径。
 */
function pxOfRaw(raw) {
  const t = raw.trim()
  const m = t.match(/[a-z%]+$/i)
  const unit = m ? m[0].toLowerCase() : ''
  const n = parseFloat(t.slice(0, t.length - unit.length))
  if (!Number.isFinite(n)) return NaN
  if (unit === 'rem') return n * ROOT_FONT_PX
  if (unit === 'px' || unit === '') return n
  return NaN
}

/** 平局取下界的语义：两个等距档里取**较小**的那个（运维台要密度）。 */
function nearestWithTie(px, scale) {
  const { nearest, tie } = nearestSpaceSteps(px, scale)
  if (!Number.isFinite(nearest)) return { target: NaN, rule: '就近阶' }
  if (tie === null || tie === undefined) return { target: nearest, rule: '就近阶' }
  return { target: Math.min(nearest, tie), rule: '平局取下界' }
}

// ── 规划 ──
const plan = []
const skipped = { token: 0, dynamic: 0, zero: 0, hairline: 0, negative: 0, onScale: 0, deadCss: 0, manual: 0 }
// ★★ 覆盖账本：每个**违规分量**都必须要么进 plan，要么进 skipped 的某一桶
//   ⇒ 对不上就**响亮失败**（否则「没匹配上」会被静默吞掉 —— 第 23 增量的老坑：
//   rem 分量的 nearest 是 NaN ⇒ 被守卫跳过 ⇒ 198 处 rem 一处都不会改，而清单看着是好的）。
const coverage = { planned: 0, skipped: 0 }

for (const f of findings) {
  const cls = classesOf(f.selector)
  if (cls.length && cls.every((c) => deadCss.has(c))) {
    skipped.deadCss++
    coverage.skipped += f.parts.length
    continue
  }
  for (const p of f.parts) {
    // ★ 人工豁免（**分量级**）：键里不带行号 ⇒ 行号漂移不会让豁免失效
    const exKey = `${f.file}|${f.selector}|${f.property}|${p.raw}`
    if (manualExempt.has(exKey)) {
      skipped.manual++
      coverage.skipped++
      continue
    }
    const isRem = /rem$/i.test(p.raw)
    const px = p.px
    let target = null
    let rule = ''
    let reason = ''

    // ① 语义裁决表（键 = `<原值 px>|<完整选择器>`）
    const dkey = `${px}px|${f.selector}`
    if (typeof tieDecisions[dkey] === 'number') {
      target = tieDecisions[dkey]
      rule = '语义裁决'
      reason = '语义裁决'
    }

    // ② 媒体查询里的间距 = **响应式降档** ⇒ 方向与基准值相反
    //    ⇒ 取「阶上且 ≤ 原值的最大档」（保持降档阶梯）；若无 ⇒ 取阶底。
    if (target === null && f.inMedia) {
      const lower = scalePx.filter((s) => s <= px)
      target = lower.length ? lower[lower.length - 1] : scalePx[0]
      rule = '媒体查询降档'
      reason = '媒体查询降档'
    }

    // ③ 就近阶；**平局取下界**（运维台要密度，不要均匀的松）
    if (target === null) {
      const n = nearestWithTie(px, scalePx)
      if (!Number.isFinite(n.target)) {
        console.error(
          `❌ ${f.file}:${f.line} ${f.property}: ${p.raw} ⇒ 换算不出 px（${px}）⇒ 中止，未写任何文件。`,
        )
        process.exit(2)
      }
      target = n.target
      rule = n.rule
      reason = n.rule
    }

    // ★★★ rem 的两种特例（★ 都是**写法债**，与阶无关）：
    //   · **发丝 rem**（换算后 ≤ 3px）：它是「光学微调」，不表达档位
    //     ⇒ **等值落地**（0.1rem ⇒ 1.6px，数值逐字不变）⇒ 绝不能落到 4px 档
    //     （实测踩过：badge 的 1.6px 内边距被撑成 4px，高了 2.5 倍）。
    //   · **在阶上的 rem**：数值已经合规 ⇒ 只换写法，不动数值。
    //   ★ 规则 id **保持内层**（不拼 'rem' 前缀）⇒ 闸④ 的独立复算可以按同一张表走。
    if (isRem) {
      if (px <= HAIRLINE_MAX_PX) {
        target = px
        rule = 'rem发丝等值'
        reason = 'rem→px（发丝等值）'
      } else if (scalePx.includes(px) && rule === '就近阶') {
        rule = 'rem在阶上'
        reason = 'rem→px（在阶上，数值不变）'
      } else {
        reason = `rem→px（${reason}）`
      }
    }

    // ★ 「无变化」的判据必须比**文本**，不能比数值 ——
    //   rem 写法（0.5rem）与 px 写法（8px）数值相同但**写法不同** ⇒ 仍要改。
    if (target === null || (!isRem && target === px)) {
      skipped.onScale++
      coverage.skipped++
      continue
    }
    coverage.planned++
    plan.push({
      file: f.file,
      line: f.line,
      selector: f.selector,
      property: f.property,
      from: p.raw,
      to: `${target}px`,
      px,
      target,
      rule,
      reason,
      inMedia: f.inMedia,
      valueStart: p.start,
      valueEnd: p.end,
    })
  }
}

// ★★ 覆盖闸：违规分量总数必须等于「已规划 + 已跳过」⇒ 否则有分量被静默吞掉
const totalParts = findings.reduce((s, f) => s + f.parts.length, 0)
if (coverage.planned + coverage.skipped !== totalParts) {
  console.error(
    `❌ 覆盖闸失败：违规分量 ${totalParts} 个，但只安排了 ${coverage.planned} 规划 + ${coverage.skipped} 跳过 = ${coverage.planned + coverage.skipped}` +
      ` ⇒ 有 ${totalParts - coverage.planned - coverage.skipped} 个分量被静默吞掉 ⇒ 中止，未写任何文件。`,
  )
  process.exit(2)
}

// ── 闸④：值语义守恒（**独立复算**）──
//   每一条都按它**声称的理由**重新算一遍目标值，必须逐字相等。
//   ★ 复算走独立路径（pxOfRaw 剥单位后 parseFloat；闸⑤ 另用比值）。
function recompute(px, rule, selector) {
  switch (rule) {
    case '语义裁决': {
      const v = tieDecisions[`${px}px|${selector}`]
      return typeof v === 'number' ? v : NaN
    }
    case '媒体查询降档': {
      const lower = scalePx.filter((s) => s <= px)
      return lower.length ? lower[lower.length - 1] : scalePx[0]
    }
    case '就近阶': {
      const { nearest, tie } = nearestSpaceSteps(px, scalePx)
      return tie === null || tie === undefined ? nearest : NaN // 平局时「就近阶」不成立
    }
    case '平局取下界': {
      const { nearest, tie } = nearestSpaceSteps(px, scalePx)
      return tie === null || tie === undefined ? NaN : Math.min(nearest, tie)
    }
    case 'rem发丝等值': return px
    case 'rem在阶上': return px
    default: return NaN
  }
}
for (const p of plan) {
  const indep = pxOfRaw(p.from)
  if (!Number.isFinite(indep) || Math.abs(indep - p.px) > 1e-9) {
    console.error(
      `❌ 闸④失败：${p.file}:${p.line} ${p.property} 的「${p.from}」—— 判据算出 ${p.px}px，独立换算算出 ${indep}px ⇒ 两把尺不一致 ⇒ 中止，未写任何文件。`,
    )
    process.exit(2)
  }
  const want = recompute(p.px, p.rule, p.selector)
  if (!Number.isFinite(want) || Math.abs(want - p.target) > 1e-9) {
    console.error(
      `❌ 闸④失败：${p.file}:${p.line} ${p.property}: ${p.from} ⇒ 计划 ${p.target}px，但按规则「${p.rule}」独立复算得 ${want}px ⇒ 中止，未写任何文件。`,
    )
    process.exit(2)
  }
}

// ── 闸⑤：比值闸（收缩/放大 > 2× 必须有显式裁决）──
//   ★ 这道闸抓的是「值算错」：`.35rem` 被算成 560px 时，改成 64px 的比值是 0.114 ⇒ 立刻红。
const RATIO_LIMIT = 2
for (const p of plan) {
  if (!Number.isFinite(p.px) || p.px <= 0) continue
  const ratio = p.target / p.px
  if (ratio < 1 / RATIO_LIMIT || ratio > RATIO_LIMIT) {
    // 明确记录为「允许的大比例改写」（裁决簿里显式登记 ⇒ 才放行）
    const allowed = (decisions.allowedRatioChanges?.entries ?? []).some(
      (e) => e.key === `${p.file}|${p.selector}|${p.property}|${p.from}`,
    )
    if (!allowed) {
      console.error(
        `❌ 闸⑤失败：${p.file}:${p.line} ${p.property}: ${p.from}(${p.px}px) → ${p.target}px，比值 ${ratio.toFixed(3)} 超出 [1/${RATIO_LIMIT}, ${RATIO_LIMIT}]` +
          ` ⇒ 改写幅度不合常理（多半是「值算错了」）⇒ 中止，未写任何文件。`,
      )
      process.exit(2)
    }
  }
}

// ── 输出清单 ──
if (process.argv.includes('--json')) {
  // ★★ 用 `stdout.write` 的**回调**再退出 ⇒ 避免 `process.exit()` 在**管道**上截断大输出。
  //   （实测踩过：JSON 被截断在 61977 字节 ⇒ 下游 `JSON.parse` 报「Unterminated string」
  //    ⇒ 下游会误判成「门坏了」而不是「输出被截了」。）
  process.stdout.write(
    JSON.stringify({ plan, skipped, scalePx, judgedFacts }, null, 2) + '\n',
    () => process.exit(0),
  )
  // ★ 挂起：等 write 回调里 exit
  await new Promise(() => {})
}

const byFile = new Map()
for (const p of plan) {
  if (!byFile.has(p.file)) byFile.set(p.file, [])
  byFile.get(p.file).push(p)
}
console.log(`计划改写 ${plan.length} 处 / ${byFile.size} 个文件`)
console.log(`跳过：死 CSS ${skipped.deadCss} · 人工豁免 ${skipped.manual} · 已在阶上 ${skipped.onScale}`)
const byReason = {}
for (const p of plan) byReason[p.reason] = (byReason[p.reason] || 0) + 1
console.log('按理由：', byReason)
for (const [file, list] of [...byFile.entries()].sort((a, b) => b[1].length - a[1].length)) {
  console.log(`\n${file}  (${list.length})`)
  for (const p of list) {
    console.log(`  L${p.line}  ${p.selector}  ${p.property}: ${p.from} → ${p.to}  [${p.reason}]`)
  }
}

if (!process.argv.includes('--apply')) {
  console.log('\n（未加 --apply ⇒ 未落盘）')
  process.exit(0)
}

// ── 落盘 + 六道闸 ──
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
  // ★ 从后往前改 ⇒ 前面区间的偏移不受影响
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

// 闸③：非间距内容逐字未变
//   ★★★ 闸③ 的正解：**区间口径必须与被比较的文本同源** ——
//     用**同一个扫描器**在两份文本上各算一遍区间，各自删掉值，再比较剩下的内容。
function stripSpacingValues(text, rel) {
  const decls = collectSpacingDeclsFromText(text, rel, scalePx)
  const ivs = []
  for (const d of decls) for (const c of d.components) ivs.push({ start: c.start, end: c.end })
  const sorted = [...ivs].sort((a, b) => b.start - a.start)
  let out = text
  for (const iv of sorted) out = out.slice(0, iv.start) + out.slice(iv.end)
  return out
}
for (const [file] of touched) {
  const s1 = stripSpacingValues(before.get(file), file)
  const s2 = stripSpacingValues(after.get(file), file)
  if (s1 !== s2) {
    console.error(`❌ 闸③失败：${file} 的**非间距内容**发生了变化 ⇒ 中止，未写任何文件。`)
    let i = 0
    while (i < s1.length && i < s2.length && s1[i] === s2[i]) i++
    console.error(`   第一处差异在偏移 ${i}：`)
    console.error(`   before: …${s1.slice(Math.max(0, i - 60), i + 60)}…`)
    console.error(`   after : …${s2.slice(Math.max(0, i - 60), i + 60)}…`)
    process.exit(2)
  }
}

// ── 闸⑥：令牌完整性（扫描器盲区 ⇒ 中止）──
//   ★ 把改写后的文本重新扫一遍：声明值里不能有**没被任何分量覆盖**的数值令牌。
//   ★ 这道闸**独立于 px 换算** ⇒ 它抓的是「区间选错」，不是「值算错」。
for (const [file, text] of after) {
  const decls = collectSpacingDeclsFromText(text, file, scalePx)
  for (const d of decls) {
    const residue = d.residue.replace(/\/\*[\s\S]*?\*\//g, ' ')
    if (/[0-9.]/.test(residue)) {
      console.error(
        `❌ 闸⑥失败：${file}:${d.line} ${d.property}: ${d.raw} —— 残留「${residue.trim()}」里有没被切分的数值令牌` +
          ` ⇒ 扫描器在这条上是盲的 ⇒ 中止，未写任何文件。`,
      )
      process.exit(2)
    }
  }
}

// 落盘
for (const [file, text] of after) writeFileSync(join(SRC_DIR, file), text)

// 闸②：重新用判据模块扫 ⇒ 离阶必须是 0 且 rem 必须是 0
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
const stillOff = v.judgedFacts?.offScale ?? -1
const remLeft = v.judgedFacts?.remSpacing ?? -1
const blindLeft = v.judgedFacts?.missedTokenDecls ?? -1
const declCountBefore = judgedFacts?.totalDeclarations ?? -1
const declCountAfter = v.judgedFacts?.totalDeclarations ?? -2
if (declCountBefore !== declCountAfter) {
  console.error(
    `❌ 闸①失败：间距声明总数 ${declCountBefore} → ${declCountAfter}（变了）⇒ 请检查后决定回滚。`,
  )
  process.exit(2)
}
if (stillOff !== 0 || remLeft !== 0 || blindLeft !== 0) {
  console.error(
    `❌ 闸②失败：判据读数离阶 ${stillOff} 处、rem ${remLeft} 处、扫描器盲区 ${blindLeft} 处 ⇒ 文件已改，请检查后决定回滚或补齐。`,
  )
  process.exit(2)
}
console.log(
  `\n✅ 六道闸全过：声明总数不变（${declCountAfter}）· 离阶 0 · rem 0 · 扫描器盲区 0 · 值语义守恒 · 比值合规 · 非间距内容逐字未变`,
)
console.log(`   落盘 ${after.size} 个文件 / ${plan.length} 处改写`)
