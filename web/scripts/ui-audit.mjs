/**
 * ui-audit.mjs — 逐页 UI 契约审计（2026-10-03）
 *
 * 三条契约，对应老板要求：
 *   A. 全屏：页面根容器不得有 max-width 收窄 / margin:0 auto 居中
 *   B. 弹窗：弹层只有两种形态（右侧抽屉 / 居中弹窗），且必须声明宽度、
 *      内容不得被裁切（限高必须配滚动）
 *   C. 列表：产出「列表 × 日期选择控件 × 模型选择控件 × 名称排序」清单
 *
 * 用法：
 *   node scripts/ui-audit.mjs              # 人读报告
 *   node scripts/ui-audit.mjs --strict     # A/B 有违规则 exit 1（CI 门）
 *   node scripts/ui-audit.mjs --json out.json
 *
 * ★ 判据必须能区分合格与不合格：改完请用 --selftest 跑一遍自带夹具，
 *   确认「植入违规 → 判红」与「干净样本 → 判绿」两个方向都成立。
 */

import { readFileSync, readdirSync, statSync, writeFileSync, mkdirSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')
const SRC = resolve(WEB, 'src')
const VIEWS = resolve(SRC, 'views')

// ─────────────────────────────────────────────────────────────
// 解析工具
// ─────────────────────────────────────────────────────────────

function walk(dir, out = []) {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (p.endsWith('.vue')) out.push(p)
  }
  return out
}

/** 取出 <style> 块（scoped / 非 scoped 都取，支持多个 style 块）。 */
function styleBlocks(src) {
  const blocks = []
  const re = /<style[^>]*>([\s\S]*?)<\/style>/g
  let m
  while ((m = re.exec(src))) blocks.push(m[1])
  return blocks
}

/**
 * 取模板里第一个真实元素（跳过注释与空白），取其 class 列表。
 * 用来确认「哪条 CSS 规则才是页面根容器」。
 */
function templateRootClasses(src) {
  const t = src.match(/<template>([\s\S]*)<\/template>/)
  if (!t) return []
  // 去掉 HTML 注释，避免把注释掉的元素当成根
  const body = t[1].replace(/<!--[\s\S]*?-->/g, '')
  // 第一个非空行开头的标签
  const m = body.match(/<([a-zA-Z][\w-]*)((?:[^>"']|"[^"]*"|'[^']*')*)>/)
  if (!m) return []
  const cls = m[2].match(/\bclass="([^"]*)"/)
  if (!cls) return []
  return cls[1].split(/\s+/).filter(Boolean)
}

/** 解析 CSS 文本成 [{ selector, body, line }]，行号相对 style 块 + 偏移。 */
function parseRules(css, lineOffset) {
  const rules = []
  const re = /([^{}]+)\{([^{}]*)\}/g
  let m
  while ((m = re.exec(css))) {
    const selector = m[1].trim()
    if (selector.startsWith('@')) continue // media/keyframes 不作为根规则
    const before = css.slice(0, m.index)
    const line = lineOffset + before.split('\n').length
    rules.push({ selector, body: m[2], line })
  }
  return rules
}

function styleLineOffsets(src) {
  // 每个 <style> 块内容起始处的 1-based 行号
  const out = []
  const re = /<style[^>]*>/g
  let m
  while ((m = re.exec(src))) {
    out.push(src.slice(0, m.index).split('\n').length) // 标签所在行；内容从下一行开始
  }
  return out
}

/** 选择器是否命中给定 class（只处理简单 class 选择器，够用于根容器判定）。 */
function selectorTargetsClass(selector, cls) {
  return selector
    .split(',')
    .some((s) => s.trim().split(/\s+/).some((part) => part === `.${cls}` || part.endsWith(`.${cls}`)))
}

// ─────────────────────────────────────────────────────────────
// 契约 A：全屏（无居中模式）
// ─────────────────────────────────────────────────────────────

// 允许的 max-width 取值：不限 / 相对宽度 / fillViewport 页面自带控制
const FULLWIDTH_OK = new Set(['none', '100%', 'unset', 'initial', 'auto'])

function checkFullWidth(file, src) {
  const problems = []
  // file 是路径字符串（相对 SRC 的），问题项里原样带出
  const rootClasses = templateRootClasses(src)
  if (!rootClasses.length) return problems

  const blocks = styleBlocks(src)
  const offsets = styleLineOffsets(src)
  blocks.forEach((css, bi) => {
    for (const rule of parseRules(css, (offsets[bi] ?? 0) + 1)) {
      const hitsRoot = rootClasses.some((c) => selectorTargetsClass(rule.selector, c))
      if (!hitsRoot) continue
      const mw = rule.body.match(/(?:^|;)\s*max-width\s*:\s*([^;]+)/)
      if (mw) {
        const val = mw[1].trim()
        const base = val.split(/\s+/)[0]
        if (!FULLWIDTH_OK.has(base) && !/^(1[0-9]{3}|[0-9]{3,4})px$/.test(base)) {
          if (!/px$/.test(base)) {
            problems.push({ file, line: rule.line, msg: `根容器 ${rule.selector} 的 max-width=${val} 会收窄页面（全屏要求 none/100%）` })
          }
        } else if (/px$/.test(base)) {
          problems.push({ file, line: rule.line, msg: `根容器 ${rule.selector} 的 max-width=${val} 是居中模式（全屏要求 none/100%）` })
        }
      }
      const mar = rule.body.match(/(?:^|;)\s*margin\s*:\s*([^;]+)/)
      if (mar && /(^|\s)0\s+auto(\s|$)/.test(mar[1]) && /auto/.test(mar[1])) {
        // 只有「无 max-width 时的 auto margin」是惰性的；这里仍报，因为意图是居中
        const hasMaxWidth = Boolean(mw)
        if (hasMaxWidth) {
          problems.push({ file, line: rule.line, msg: `根容器 ${rule.selector} 同时有 max-width + margin:0 auto —— 典型居中模式` })
        }
      }
    }
  })
  return problems
}

// ─────────────────────────────────────────────────────────────
// 契约 B：弹层形态 + 内容完整
// ─────────────────────────────────────────────────────────────

const OVERLAY_SELECTORS = [
  { kind: 'drawer', re: /el-drawer|\bdrawer\b/, label: '右侧抽屉' },
  { kind: 'dialog', re: /el-dialog|\bmodal\b|\bdialog\b/, label: '居中弹窗' },
]

/**
 * 允许豁免的弹层：这些是全屏/内嵌面板，不是「弹窗」形态问题。
 * 必须逐条写理由，不允许按文件名整体豁免。
 */
const OVERLAY_EXEMPT = [
  { file: 'views/ops/VibeCodingView.vue', reason: '全屏控制台终端视图，无弹层容器' },
]

function checkOverlays(file, src) {
  const problems = []
  const rel = relative(SRC, file)

  // 1) 自研 CSS 弹层：.modal__panel / .drawer__panel 等，逐一检查是否声明宽度
  const modalPanelRe = /\b(modal|drawer)__(?:panel|body|content|content-body)\b/g
  for (const m of src.matchAll(modalPanelRe)) {
    const kind = m[1]
    const problems2 = checkOverlayBlock(file, src, m[0], kind)
    problems.push(...problems2)
  }

  // 2) Element Plus：<el-dialog> / <el-drawer> 标签必须声明 width / size
  for (const m of src.matchAll(/<el-(dialog|drawer)\b([^>]*)>/g)) {
    const kind = m[1]
    const attrs = m[2]
    const hasWidth = /\bwidth\s*=/.test(attrs) || /\bsize\s*=/.test(attrs)
    if (!hasWidth) {
      const line = src.slice(0, m.index).split('\n').length
      problems.push({ file, line, msg: `<el-${kind}> 未声明 width/size，内容宽度不可控（弹层必须显式定宽）` })
    }
    // 居中弹窗必须有 max-height + 滚动，否则长内容被裁切
    if (kind === 'dialog' && !/max-height/.test(attrs)) {
      // 只有当页内没有对该 dialog 的 max-height CSS 时才算问题
      const clsGuess = (attrs.match(/\bclass="([^"]*)"/) || [, ''])[1]
      const hasCssMaxHeight = clsGuess
        .split(/\s+/).filter(Boolean).some((c) => {
          const re = new RegExp(`\\.${c.replace(/[-]/g, '\\-')}[^{]*\\{[^}]*max-height`, 'm')
          return re.test(src)
        })
      if (!hasCssMaxHeight) {
        const line = src.slice(0, m.index).split('\n').length
        problems.push({ file, line, msg: `<el-dialog> 缺 max-height + 滚动，长内容会被裁切（内容必须显示完整）` })
      }
    }
  }

  // 3) 契约 B 延伸 —— 「逐字折行」bug 的机械特征。
  //    table-layout:fixed 下给 td/th 加 `max-width: 0` 会把每列可用宽度归零，
  //    浏览器只能按字符断行：17 列的供应商表曾整体塌成竖排文字
  //    （"H/E/A/D/E/R P/R/O/F/I/L/E"、URL 断成 "h t t p s : /"）。
  //    这是「不必要的折行 + 看不全」的直接成因，且特征唯一，可机械判定。
  const ZERO_W_RE = /(?:^|[},])\s*(table[^{}]*|td[^{}]*|th[^{}]*)\{([^}]*max-width\s*:\s*0\s*[;}])/g
  for (const m of src.matchAll(ZERO_W_RE)) {
    const line = src.slice(0, m.index).split('\n').length
    problems.push({
      file,
      line,
      msg: `表格单元格规则含 max-width: 0（选择器「${m[1].trim()}」）→ fixed 布局下列宽归零，文字会被逐字符折断`,
    })
  }

  return problems.filter((p) => !OVERLAY_EXEMPT.some((e) => rel === e.file))
}

/**
 * 自研弹层块级检查：解析与该类名相邻的规则，
 * 断言①声明宽度 ②限高时必须配 overflow-y:auto/scroll。
 */
function checkOverlayBlock(file, src, clsName, kind) {
  const problems = []
  const re = new RegExp(`\\.${clsName}\\b[^{]*\\{([^}]*)\\}`, 'g')
  let m
  while ((m = re.exec(src))) {
    const body = m[1]
    const line = src.slice(0, m.index).split('\n').length
    const hasWidth = /(?:^|;)\s*(?:width|min-width|max-width)\s*:/.test(body)
    const hasMinWidth = /min-width\s*:/.test(body)
    if (!hasWidth && !hasMinWidth) {
      problems.push({ file, line, msg: `.${clsName}（${kind === 'drawer' ? '抽屉' : '弹窗'}）未声明宽度，内容宽度不可控` })
    }
    const maxH = /(?:^|;)\s*max-height\s*:/.test(body)
    const scrollable = /overflow-y\s*:\s*(auto|scroll)/.test(body)
    if (maxH && !scrollable) {
      problems.push({ file, line, msg: `.${clsName} 有 max-height 但没有 overflow-y:auto/scroll —— 内容会被裁切` })
    }
  }
  return problems
}

// ─────────────────────────────────────────────────────────────
// 契约 C：列表 × (日期控件 / 模型控件 / 名称排序) 清单
// ─────────────────────────────────────────────────────────────

const DATE_FILTER_RE = /el-date-picker|type="daterange"|type='daterange'|DateRange|dateRange|date-range|date_range/
const MODEL_FILTER_RE = /<ModelPicker|ModelPicker\.vue|<model-picker|modelFilter|model-filter|modelFilterKey/
const NAME_SORT_RE = /sortBy[^\n]*name|sort_by[^\n]*name|localeCompare\(|collation\.|\bsortName\b|nameAsc|byName|sortByName/
const TABLE_RE = /<el-table|<table\b|class="[^"]*\btable\b/

function isNameBasedList(file, rel) {
  // 供应商 / 租户 / 用户 / 密钥 / 模型 / Agent / 模块 —— 有「名称」实体的列表
  return /Provider|Tenant|User|Key|Model|Agent|Module|MaasAccount/i.test(rel) && TABLE_RE.test(readFileSync(file, 'utf8'))
}

function buildListInventory(files) {
  const rows = []
  for (const file of files) {
    const src = readFileSync(file, 'utf8')
    if (!TABLE_RE.test(src)) continue
    const rel = relative(SRC, file)
    rows.push({
      file: rel,
      hasDate: DATE_FILTER_RE.test(src),
      hasModel: MODEL_FILTER_RE.test(src),
      nameBased: isNameBasedList(file, rel),
      nameSorted: NAME_SORT_RE.test(src),
    })
  }
  return rows
}

// ─────────────────────────────────────────────────────────────
// 自检：判据必须有鉴别力
// ─────────────────────────────────────────────────────────────

function selftest() {
  const badFull = `<template><div class="x-view"></div></template>
<style scoped>
.x-view { max-width: 1200px; margin: 0 auto; }
</style>`
  const goodFull = `<template><div class="x-view"></div></template>
<style scoped>
.x-view { max-width: none; }
</style>`
  const badOverlay = `<template><div class="modal__panel"></div></template>
<style scoped>
.modal__panel { max-height: 400px; }
</style>`
  const goodOverlay = `<template><div class="modal__panel"></div></template>
<style scoped>
.modal__panel { width: 720px; max-height: 400px; overflow-y: auto; }
</style>`
  // 逐字折行 bug 的真实样本（2026-10-03 供应商表实测形态）
  const badZeroWidth = `<template><table><tr><td>x</td></tr></table></template>
<style scoped>
table { table-layout: fixed; width: 100%; }
table td, table th { word-break: break-word; max-width: 0; }
</style>`
  const goodZeroWidth = `<template><table><tr><td>x</td></tr></table></template>
<style scoped>
table { table-layout: fixed; width: 100%; }
table td, table th { word-break: break-word; }
</style>`

  // checkFullWidth / checkOverlays 的签名都是 (filePath, src)
  const f = (name, s, fn) => fn(join(VIEWS, name), s)
  const r = []
  r.push(['全屏：违规样本必须判红', f('bad.vue', badFull, checkFullWidth).length > 0])
  r.push(['全屏：合规样本必须判绿', f('good.vue', goodFull, checkFullWidth).length === 0])
  r.push(['弹层：限高无滚动必须判红', f('bad2.vue', badOverlay, checkOverlays).length > 0])
  r.push(['弹层：定宽+滚动必须判绿', f('good2.vue', goodOverlay, checkOverlays).length === 0])
  r.push(['折行：单元格 max-width:0 必须判红', f('bad3.vue', badZeroWidth, checkOverlays).length > 0])
  r.push(['折行：去掉 max-width:0 必须判绿', f('good3.vue', goodZeroWidth, checkOverlays).length === 0])

  console.log('── 判据自检 ──')
  let ok = true
  for (const [label, pass] of r) {
    console.log(`  ${pass ? '✅' : '❌'} ${label}`)
    if (!pass) ok = false
  }
  if (!ok) { console.error('\n判据无鉴别力，门不可信，先修判据。'); process.exit(1) }
  console.log('  → 判据具备鉴别力\n')
}

// ─────────────────────────────────────────────────────────────
// 主流程
// ─────────────────────────────────────────────────────────────

const args = process.argv.slice(2)
if (args.includes('--selftest')) { selftest(); process.exit(0) }

const files = walk(VIEWS).sort()
const fullWidth = []
const overlays = []
for (const f of files) {
  const src = readFileSync(f, 'utf8')
  fullWidth.push(...checkFullWidth(f, src))
  overlays.push(...checkOverlays(f, src))
}
const inventory = buildListInventory(files)

const byFile = new Map()
for (const p of [...fullWidth.map((p) => ({ ...p, kind: 'A-全屏' })), ...overlays.map((p) => ({ ...p, kind: 'B-弹层' }))]) {
  if (!byFile.has(p.file)) byFile.set(p.file, [])
  byFile.get(p.file).push(p)
}

console.log(`扫描视图：${files.length} 个`)
console.log(`契约 A 全屏违规：${fullWidth.length}`)
console.log(`契约 B 弹层违规：${overlays.length}`)

if (byFile.size) {
  console.log('\n── 违规明细 ──')
  const sorted = [...byFile.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  for (const [file, ps] of sorted) {
    console.log(`\n${relative(SRC, file)}`)
    for (const p of ps) console.log(`  [${p.kind}] L${p.line}  ${p.msg}`)
  }
}

console.log(`\n── 契约 C 列表清单（共 ${inventory.length} 个列表）──`)
console.log('  有日期控件 / 有模型控件 / 名称列表 / 已按名称排序')
for (const r of inventory) {
  console.log(
    `  ${r.hasDate ? '日期✓' : '日期✗'} ${r.hasModel ? '模型✓' : '模型✗'} ${r.nameBased ? '名称列表' : '        '} ${r.nameSorted ? '已排序✓' : '未排序 '}  ${r.file}`,
  )
}
const needDate = inventory.filter((r) => !r.hasDate).length
const needModel = inventory.filter((r) => !r.hasModel).length
const needNameSort = inventory.filter((r) => r.nameBased && !r.nameSorted)
console.log(`\n汇总：缺日期控件 ${needDate} / 缺模型控件 ${needModel} / 名称列表未排序 ${needNameSort.length}`)
for (const r of needNameSort) console.log(`  名称列表未排序：${r.file}`)

if (args.includes('--json')) {
  const outPath = args[args.indexOf('--json') + 1] || 'reports/ui-audit.json'
  const abs = resolve(WEB, outPath)
  mkdirSync(dirname(abs), { recursive: true })
  writeFileSync(abs, JSON.stringify({ fullWidth, overlays, inventory }, null, 2))
  console.log(`\nJSON 报告：${outPath}`)
}

if (args.includes('--strict') && (fullWidth.length || overlays.length)) {
  console.error(`\n✗ UI 契约门未通过（A:${fullWidth.length} B:${overlays.length}），exit 1`)
  process.exit(1)
}
if (!args.includes('--strict')) {
  console.log('\n（加 --strict 可作为 CI 阻断门；加 --selftest 验证判据鉴别力）')
}
