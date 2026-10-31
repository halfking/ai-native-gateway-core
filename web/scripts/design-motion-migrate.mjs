#!/usr/bin/env node
// design-motion-migrate.mjs — 「状态过渡必须以指数减速到达」的批量收敛器（2026-10-27 · 第 26 增量）
//
// ★★ 批量改造的闸（把「不可逆改写」的判据变成可证伪的等式）：
//   ① **声明总数不变** —— 只改值，不增不减声明；
//   ② **改写后 M1 归零** —— 重新用判据模块扫一遍（★ 同一个口径，不是另一份实现）；
//   ③ **值语义守恒** —— 逐项比对 (属性 / 时长 / 延迟)：这三个必须**一字不变**，
//      只有缓动允许变（★ 守恒型闸结构上挡不住「改错了值」，所以要逐项独立复算）；
//   ④ **区间外逐字未变** —— 把改写区间换成同一个占位符后比较两份文本；
//   ⑤ **新缓动的形状** —— 每一条改写后的缓动都必须过 `classifyTiming`。
//
// ★ 改写落在**字符区间**上：先算出「值」的区间，再在**分项**里算出缓动 token 的区间，
//   只替换/插入那一个 token ⇒ **声明的属性列表、时长、延迟、换行格式全部原样保留**。
//
// 用法：
//   node --experimental-strip-types scripts/design-motion-migrate.mjs           # 只出清单（不落盘）
//   node --experimental-strip-types scripts/design-motion-migrate.mjs --apply   # 落盘 + 五道闸
//   node --experimental-strip-types scripts/design-motion-migrate.mjs --json    # 机器可读清单

import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  collectMotionDecls,
  scanMotionInFile,
  scanMotion,
  classifyTiming,
  parseCssRules,
  resolveSelector,
  maskCssComments,
  styleBlocksOf,
  MOTION_EASE_TOKEN,
  MOTION_EASE_LITERAL,
} from '../src/design/motion.ts'
import { readFileSync as _rf, writeFileSync as _wf } from 'node:fs'

// ★ Windows 修正（2026-10-28，同 responsive-audit.mjs）：URL.pathname 在
//   win32 上是 `/C:/...`，被 resolve/join 解析成「当前盘下的 C: 目录」。
const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const SRC = join(ROOT, 'src')
const DECISIONS_FILE = join(ROOT, 'scripts', 'design-motion-decisions.json')

const APPLY = process.argv.includes('--apply')
const JSON_OUT = process.argv.includes('--json')
const PHASE = (process.argv.find((a) => a.startsWith('--phase=')) || '--phase=m1').split('=')[1]

const TOKEN = MOTION_EASE_TOKEN

/**
 * 一个待改写的声明
 * @typedef {Object} Edit
 * @property {string} file
 * @property {number} line
 * @property {string} selector
 * @property {string} prop
 * @property {string} oldValue
 * @property {string} newValue
 * @property {number} valueStart 值区间起点（文件坐标）
 * @property {number} valueEnd   值区间终点（文件坐标）
 */

/**
 * 在一个声明里，把不合规的缓动改成那条曲线（返回新的值文本）
 * @param {any} decl
 * @returns {{value: string, changed: number} | null}
 */
function rewriteDecl(decl) {
  const items = decl.prop.startsWith('transition')
    ? decl.transitionItems
    : decl.animationItems.map((it) => ({
        ...it,
        property: '',
        timingStart: it.timingStart,
        timingEnd: it.timingEnd,
        durationEnd: it.durationEnd,
      }))
  if (!items.length) return null

  // 只处理「该被管」的分项：transition 全管；animation 只管一次性（finite）
  const finiteOnly = decl.prop.startsWith('animation')
  /** @type {Array<{start: number, end: number, text: string}>} */
  const edits = []
  for (const it of items) {
    if (finiteOnly) {
      const infinite = (it.iterationCount ?? '').toLowerCase() === 'infinite'
      if (infinite) continue
      // ★ `animation: none` ⇒ 没有动效 ⇒ 不许插令牌（否则 `none var(...)` 是无效声明）
      if (!it.name) continue
    } else {
      // ★ `transition: none` ⇒ 没有动效 ⇒ 不许插令牌
      if (it.property === 'none') continue
    }
    const info = classifyTiming(it.timing)
    if (info.ok) continue
    // 绝对区间（相对**值**）
    const absStart = it.start + (it.timingStart >= 0 ? it.timingStart : it.durationEnd)
    const absEnd = it.timingStart >= 0 ? it.start + it.timingEnd : it.start + it.durationEnd
    if (it.timingStart >= 0) {
      edits.push({ start: absStart, end: absEnd, text: TOKEN })
    } else {
      // 未声明缓动 ⇒ 插在时长之后
      if (it.durationEnd < 0) continue
      edits.push({ start: absStart, end: absEnd, text: ` ${TOKEN}` })
    }
  }
  if (!edits.length) return null
  edits.sort((a, b) => a.start - b.start)
  // ★ 区间不得重叠（重叠 ⇒ 解析器口径有问题 ⇒ 响亮失败）
  for (let i = 1; i < edits.length; i++) {
    if (edits[i].start < edits[i - 1].end) {
      throw new Error(`区间重叠：${decl.file}:${decl.line} ${edits[i - 1].start}..${edits[i - 1].end} 与 ${edits[i].start}..${edits[i].end}`)
    }
  }
  let value = decl.value
  for (let i = edits.length - 1; i >= 0; i--) {
    const e = edits[i]
    value = value.slice(0, e.start) + e.text + value.slice(e.end)
  }
  return { value, changed: edits.length }
}

/** 值语义指纹：只有缓动允许变 @param {any} decl */
function semanticFingerprint(decl) {
  if (decl.prop.startsWith('transition')) {
    return JSON.stringify(
      decl.transitionItems.map((it) => [it.property, it.durationMs, it.delayMs]),
    )
  }
  return JSON.stringify(
    decl.animationItems.map((it) => [it.name, it.durationMs, it.delayMs, it.iterationCount]),
  )
}

if (PHASE === 'm1') {
const decls = collectMotionDecls(SRC)
/** @type {Map<string, string>} */
const fileTexts = new Map()
for (const d of decls) if (!fileTexts.has(d.file)) fileTexts.set(d.file, readFileSync(join(SRC, d.file), 'utf8'))

const judged = scanMotion(decls, fileTexts, { layoutDriven: [], entrances: [] })
const m1Files = new Set(judged.findings.filter((f) => f.rule === 'M1').map((f) => f.file))

/** @type {Edit[]} */
const edits = []
let changedDecls = 0
let changedTokens = 0
for (const d of decls) {
  if (d.inReducedMotion) continue
  if (!m1Files.has(d.file)) continue
  const r = rewriteDecl(d)
  if (!r) continue
  edits.push({
    file: d.file,
    line: d.line,
    selector: d.selector,
    prop: d.prop,
    oldValue: d.value,
    newValue: r.value,
    valueStart: d.valueOffset,
    valueEnd: d.valueOffset + d.value.length,
  })
  changedDecls++
  changedTokens += r.changed
}

// ── 逐文件落盘（从后往前改，避免偏移漂移） ──
/** @type {Map<string, Edit[]>} */
const byFile = new Map()
for (const e of edits) {
  if (!byFile.has(e.file)) byFile.set(e.file, [])
  byFile.get(e.file).push(e)
}

/** @type {string[]} */
const gateFailures = []
/** @type {string[]} */
const changedFiles = []

/** @type {Array<{abs: string, after: string}>} */
const pendingWrites = []

for (const [file, list] of byFile) {
  const abs = join(SRC, file)
  const before = readFileSync(abs, 'utf8')
  const failuresBefore = gateFailures.length
  list.sort((a, b) => b.valueStart - a.valueStart)
  let after = before
  for (const e of list) {
    const actual = after.slice(e.valueStart, e.valueEnd)
    if (actual !== e.oldValue) {
      gateFailures.push(
        `${file}:${e.line} 区间内容与预期不符（区间口径漂了）⇒ 期望 ${JSON.stringify(e.oldValue)} 实得 ${JSON.stringify(actual)}`,
      )
      continue
    }
    after = after.slice(0, e.valueStart) + e.newValue + after.slice(e.valueEnd)
  }
  if (after === before) continue

  // ── 闸①：声明总数不变
  const declsBefore = scanMotionInFile({ file, text: before }).length
  const declsAfter = scanMotionInFile({ file, text: after }).length
  if (declsBefore !== declsAfter) {
    gateFailures.push(`${file} 声明总数变了 ${declsBefore} → ${declsAfter}`)
  }
  // ── 闸③：值语义守恒（属性 / 时长 / 延迟一字不变）
  /** @type {Map<string, string>} */
  const semBefore = new Map()
  for (const d of scanMotionInFile({ file, text: before })) semBefore.set(`${d.prop}@${d.fileStart}`, semanticFingerprint(d))
  const afterDecls = scanMotionInFile({ file, text: after })
  for (const d of afterDecls) {
    const key = `${d.prop}@${d.fileStart}`
    const beforeFp = semBefore.get(key)
    if (beforeFp === undefined) continue
    const afterFp = semanticFingerprint(d)
    if (beforeFp !== afterFp) {
      gateFailures.push(`${file}:${d.line} 值语义变了：${beforeFp} → ${afterFp}（只有缓动允许变）`)
    }
  }
  // ── 闸⑤：新缓动的形状
  for (const d of afterDecls) {
    if (d.inReducedMotion) continue
    const items = d.prop.startsWith('transition')
      ? d.transitionItems.filter((it) => it.property !== 'none')
      : d.animationItems.filter(
          (it) => (it.iterationCount ?? '').toLowerCase() !== 'infinite' && it.name,
        )
    for (const it of items) {
      const info = classifyTiming(it.timing)
      if (!info.ok) {
        gateFailures.push(`${file}:${d.line} 改写后缓动仍不合规：${info.reason}`)
      }
    }
  }
  // ── 闸④：区间外逐字未变
  //   ★ 替换后**长度变了** ⇒ 后续区间的偏移会漂 ⇒ 必须按「逐条 edit 的 delta」跟踪偏移，
  //     否则闸④ 会**假红**（它自己就成了坏量具）。
  const fwd = list.slice().sort((a, b) => a.valueStart - b.valueStart)
  const deltas = fwd.map((e) => e.newValue.length - e.oldValue.length)
  //   ★★ 终点**不是**「原终点 + delta」，而是「起点 + 该文本里这一段的长度」：
  //     改后这一段的长度 = 新值长度。把它当成「原终点 + delta」⇒ 闸④ 会自己算错区间 ⇒ 假红。
  //   ★★★ 三个量各管各的：**起点**在改后文本里要带 delta（前面的 edit 变长了），
  //     在改前文本里**不带**；**长度**在改前用旧值长度、改后用新值长度。
  //     ★ 三者混用 ⇒ 闸④ 自己算错区间 ⇒ 假红（本轮真踩了两次）。
  const mask = (text, useShift, lenOf) => {
    let out = ''
    let cursor = 0
    let acc = 0
    for (let i = 0; i < fwd.length; i++) {
      const e = fwd[i]
      const s = e.valueStart + (useShift ? acc : 0)
      const en = s + lenOf(e)
      out += text.slice(cursor, s) + '<<MOTION>>'
      cursor = en
      acc += deltas[i]
    }
    out += text.slice(cursor)
    return out
  }
  const maskedBefore = mask(before, false, (e) => e.oldValue.length)
  const maskedAfter = mask(after, true, (e) => e.newValue.length)
  if (maskedBefore !== maskedAfter) {
    gateFailures.push(`${file} 区间之外的内容变了 ⇒ 改写越界`)
  }

  changedFiles.push(file)
  pendingWrites.push({ abs, after })
  if (gateFailures.length > failuresBefore) {
    // ★ 本文件有闸失败 ⇒ 本文件不落盘（但其余文件照常评估，最后统一决定写不写）
    pendingWrites.pop()
  }
}

// ★★ 写盘前先看**全部**闸门：一处失败 ⇒ 整个树一个字都不动
if (APPLY && gateFailures.length) {
  console.error('\n❌ 有闸失败 ⇒ **不写盘**（整个树保持原样）。')
} else if (APPLY) {
  for (const w of pendingWrites) writeFileSync(w.abs, w.after)
  console.log(`\n✅ 已落盘 ${pendingWrites.length} 个文件`)
}

// ── 闸②：改写后 M1 归零（用**同一口径**重扫整棵树） ──
if (APPLY && !gateFailures.length) {
  const afterDecls = collectMotionDecls(SRC)
  /** @type {Map<string, string>} */
  const afterTexts = new Map()
  for (const d of afterDecls) if (!afterTexts.has(d.file)) afterTexts.set(d.file, readFileSync(join(SRC, d.file), 'utf8'))
  const afterJudged = scanMotion(afterDecls, afterTexts, { layoutDriven: [], entrances: [] })
  if (afterJudged.facts.M1 !== 0) {
    gateFailures.push(`改写后 M1 仍有 ${afterJudged.facts.M1} 处 ⇒ 迁移没做完`)
  }
}

if (JSON_OUT) {
  console.log(JSON.stringify({
    changedDecls, changedTokens, changedFiles: changedFiles.length,
    gateFailures, edits,
  }, null, 1))
} else {
  console.log(`M1 迁移：${changedDecls} 条声明 / ${changedTokens} 个缓动 token / ${changedFiles.length} 个文件`)
  const byRuleFile = new Map()
  for (const e of edits) byRuleFile.set(e.file, (byRuleFile.get(e.file) ?? 0) + 1)
  for (const [f, n] of [...byRuleFile.entries()].sort((a, b) => b[1] - a[1])) {
    console.log(`  ${f}  x${n}`)
  }
  if (gateFailures.length) {
    console.error('\n❌ 闸失败：')
    for (const g of gateFailures) console.error('  ' + g)
  } else {
    console.log('\n✅ 五道闸全过')
  }
  if (!APPLY) console.log('\n（未加 --apply ⇒ 未落盘）')
}

if (gateFailures.length) process.exit(1)
if (!APPLY) process.exit(0)


}

// ═══════════════════════ 阶段 M2：transition:all → 具名属性 ═══════════════════════
if (PHASE === 'm2') {
  const decisions = JSON.parse(_rf(DECISIONS_FILE, 'utf8'))
  const entries = decisions.transitionAll?.entries ?? []
  const byFile = new Map()
  for (const e of entries) {
    if (!byFile.has(e.file)) byFile.set(e.file, [])
    byFile.get(e.file).push(e)
  }
  /** @type {string[]} */
  const failures = []
  /** @type {Array<{abs: string, after: string}>} */
  const writes = []
  let done = 0

  for (const [file, list] of byFile) {
    const abs = join(SRC, file)
    const before = _rf(abs, 'utf8')
    const decls = scanMotionInFile({ file, text: before })
    let after = before
    const edits = []
    for (const e of list) {
      const target = decls.find(
        (d) =>
          d.prop === 'transition' &&
          d.selector.replace(/\s+/g, ' ').trim() === e.selector.replace(/\s+/g, ' ').trim() &&
          d.transitionItems.some((it) => it.property === 'all'),
      )
      if (!target) {
        failures.push(`${file} 裁决簿条目 \`${e.key}\` 找不到对应的 \`transition: all\` 声明 ⇒ 裁决簿与代码漂移`)
        continue
      }
      const item = target.transitionItems.find((it) => it.property === 'all')
      // ★ 属性名之后的尾巴（时长 / 缓动 / 延迟）**原样保留**
      const m = /^(\S+)(\s[\s\S]*)?$/.exec(item.raw)
      const tail = m && m[2] ? m[2] : ''
      const newValue = e.properties.map((p) => p + tail).join(', ')
      edits.push({
        valueStart: target.valueOffset,
        valueEnd: target.valueOffset + target.value.length,
        oldValue: target.value,
        newValue,
      })
      done++
    }
    if (!edits.length) continue
    edits.sort((a, b) => b.valueStart - a.valueStart)
    for (const e of edits) {
      const actual = after.slice(e.valueStart, e.valueEnd)
      if (actual !== e.oldValue) {
        failures.push(`${file} 区间内容与预期不符 ⇒ 期望 ${JSON.stringify(e.oldValue)} 实得 ${JSON.stringify(actual)}`)
        continue
      }
      after = after.slice(0, e.valueStart) + e.newValue + after.slice(e.valueEnd)
    }
    // ★ 闸：改写后该文件**不得再有** transition: all
    const afterDecls = scanMotionInFile({ file, text: after })
    const stillAll = afterDecls.filter(
      (d) => d.prop === 'transition' && d.transitionItems.some((it) => it.property === 'all'),
    )
    if (stillAll.length) {
      failures.push(`${file} 改写后仍有 ${stillAll.length} 处 \`transition: all\``)
    }
    // ★ 闸：具名属性的**顺序与取值**必须与裁决簿逐条相等
    for (const e of list) {
      const t = afterDecls.find(
        (d) =>
          d.prop === 'transition' &&
          d.selector.replace(/\s+/g, ' ').trim() === e.selector.replace(/\s+/g, ' ').trim(),
      )
      if (!t) continue
      const got = t.transitionItems.map((it) => it.property)
      if (JSON.stringify(got) !== JSON.stringify(e.properties)) {
        failures.push(`${file} \`${e.selector}\` 的属性集与裁决簿不一致：实得 [${got}] 期望 [${e.properties}]`)
      }
    }
    if (after !== before) writes.push({ abs, after })
  }

  if (JSON_OUT) {
    console.log(JSON.stringify({ phase: 'm2', done, files: writes.length, failures }, null, 1))
  } else {
    console.log(`M2 迁移：${done} 条 \`transition: all\` → 具名属性 / ${writes.length} 个文件`)
    if (failures.length) {
      console.error('\n❌ 闸失败：')
      for (const f of failures) console.error('  ' + f)
    } else {
      console.log('✅ 闸全过')
    }
  }
  if (APPLY && !failures.length) {
    for (const w of writes) _wf(w.abs, w.after)
    console.log(`✅ 已落盘 ${writes.length} 个文件`)
  } else if (APPLY) {
    console.error('❌ 有闸失败 ⇒ **不写盘**')
  }
  process.exit(failures.length ? 1 : 0)
}

// ═══════════════════════ 阶段 M4：空间移动的 reduced-motion 路径 ═══════════════════════
if (PHASE === 'm4') {
  const decls = collectMotionDecls(SRC)
  const files = [...new Set(decls.map((d) => d.file))]
  const fileTexts = new Map(files.map((f) => [f, _rf(join(SRC, f), 'utf8')]))
  const judged = scanMotion(decls, fileTexts, { layoutDriven: [], entrances: [] })
  const m4 = judged.findings.filter((f) => f.rule === 'M4')
  /** @type {Map<string, Array<{selector: string, keep: string[], variant: string|null}>>} */
  const plan = new Map()

  for (const f of m4) {
    // ★ 口径必须与判据一致：两侧都按**逗号分项**比较（声明侧是全串，finding 侧是单项）
    const parts = (sel) => sel.split(',').map((x) => x.trim().replace(/\s+/g, ' ')).filter(Boolean)
    const target = decls.find(
      (d) => d.file === f.file && d.prop === 'transition' &&
        parts(d.selector).includes(f.selector) &&
        d.transitionItems.some((it) => it.property === 'transform'),
    )
    if (!target) continue
    const keep = target.transitionItems.filter((it) => it.property !== 'transform').map((it) => it.property)
    // ★ 装饰性判定：同一选择器的**伪类变体**里若也写了 transform ⇒ 那是 hover 抬升 ⇒ 取消位移本身
    let variant = null
    const text = fileTexts.get(f.file)
    const blocks = f.file.endsWith('.vue')
      ? styleBlocksOf(text)
      : [{ text, baseLine: 0, start: 0 }]
    for (const blk of blocks) {
      const masked = maskCssComments(blk.text)
      for (const r of parseCssRules(masked)) {
        for (const part of resolveSelector(r.selector, r.parentSelector).split(',')) {
          const sel = part.trim()
          if (!sel.startsWith(f.selector)) continue
          if (!/:(hover|active|focus|focus-visible)/.test(sel)) continue
          if (!r.decls.some((d) => d.prop === 'transform')) continue
          variant = sel
        }
      }
    }
    if (!plan.has(f.file)) plan.set(f.file, [])
    plan.get(f.file).push({ selector: f.selector, keep, variant })
  }

  /** @type {string[]} */
  const failures = []
  /** @type {Array<{abs: string, after: string}>} */
  const writes = []
  for (const [file, items] of plan) {
    const abs = join(SRC, file)
    const before = _rf(abs, 'utf8')
    const text = fileTexts.get(file)
    const blocks = file.endsWith('.vue') ? styleBlocksOf(text) : [{ text, baseLine: 0, start: 0 }]
    const last = blocks[blocks.length - 1]
    const insertAt = file.endsWith('.vue') ? last.start + last.text.length : before.length
    const lines = []
    lines.push('')
    lines.push('/* 2026-10-27 第 26 增量：空间移动的 reduced-motion 路径（impeccable animate.md:77）。')
    lines.push('   颜色 / 透明度反馈保留（那是「有意义的状态变化」），只把**空间移动**停下来。 */')
    lines.push('@media (prefers-reduced-motion: reduce) {')
    // ★ 多选择器要拆成多行、逐行缩进（否则第二行顶格，块会很难读）
    const fmt = (sel) =>
      sel
        .split(',')
        .map((x) => x.trim().replace(/\s+/g, ' '))
        .join(',\n  ')
    for (const it of items) {
      if (it.keep.length) {
        lines.push(`  ${fmt(it.selector)} {`)
        lines.push(`    transition-property: ${it.keep.join(', ')};`)
        lines.push('  }')
      } else {
        lines.push(`  ${fmt(it.selector)} { transition: none; }`)
      }
      if (it.variant) {
        lines.push(`  ${fmt(it.variant)} { transform: none; }`)
      }
    }
    lines.push('}')
    const block = lines.join('\n') + '\n'
    const after = before.slice(0, insertAt) + block + before.slice(insertAt)
    if (after !== before) writes.push({ abs, after })
  }

  // ★★ 闸：**全树复扫**（同一口径），M4 必须归零 ——
  //   ★ 只查「计划内的文件」是**假绿**：多选择器站点会被整份丢弃而门却看不见。
  if (APPLY) {
    // ★★★ 声明与文本必须来自**同一份**内容：
    //   声明从磁盘读、文本用待落盘的版本 ⇒ 两边对不上 ⇒ 闸自己算错 ⇒ 假红。
    const afterTexts = new Map()
    for (const d of decls) {
      if (!afterTexts.has(d.file)) afterTexts.set(d.file, _rf(join(SRC, d.file), 'utf8'))
    }
    for (const w of writes) afterTexts.set(w.abs.slice(SRC.length + 1), w.after)
    const afterDecls = []
    for (const [file, text] of afterTexts) afterDecls.push(...scanMotionInFile({ file, text }))
    const afterJudged = scanMotion(afterDecls, afterTexts, { layoutDriven: [], entrances: [] })
    if (afterJudged.facts.M4 !== 0) {
      failures.push(`全树复扫后 M4 仍有 ${afterJudged.facts.M4} 处 ⇒ 迁移没做完`)
    }
  }

  if (JSON_OUT) {
    console.log(JSON.stringify({ phase: 'm4', files: writes.length, failures }, null, 1))
  } else {
    console.log(`M4 迁移：${plan.size} 个文件 / ${writes.length} 个落盘`)
    for (const [file, items] of plan) {
      console.log(`  ${file}`)
      for (const it of items) {
        console.log(`    ${it.selector}  保留=[${it.keep.join(',')}]${it.variant ? '  取消位移=' + it.variant : ''}`)
      }
    }
    if (failures.length) {
      console.error('\n❌ 闸失败：')
      for (const f of failures) console.error('  ' + f)
    } else {
      console.log('✅ 闸全过')
    }
  }
  if (APPLY && !failures.length) {
    for (const w of writes) _wf(w.abs, w.after)
    console.log(`✅ 已落盘 ${writes.length} 个文件`)
  } else if (APPLY) {
    console.error('❌ 有闸失败 ⇒ **不写盘**')
  }
  process.exit(failures.length ? 1 : 0)
}
