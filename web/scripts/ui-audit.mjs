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
const COMPONENTS = resolve(SRC, 'components')

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

/** 待人工确认项：判据只能提示、不能定罪的问题。--strict 不据此 exit 1。 */
const REVIEW_ITEMS = []

function checkOverlays(file, src) {
  const problems = []
  const rel = relative(SRC, file)

  // 1) 自研 CSS 弹层：容器级判定（宽度看容器，限高看所有相关规则）
  // 触发的类名形如 xxx-drawer / modal__panel。字符类必须含 `_`，
  // 否则 `modal__panel` 会在 `\b` 处失配（`_` 是 word char），整条判据静默失效
  // —— 自检里那条「限高无滚动必须判红」就是靠它兜住的。
  const seenKinds = new Set()
  for (const m of src.matchAll(/\b([a-z0-9_-]*(?:drawer|modal)[a-z0-9_-]*)\b/gi)) {
    const kind = /drawer/i.test(m[1]) ? 'drawer' : 'modal'
    if (seenKinds.has(kind)) continue
    seenKinds.add(kind)
    problems.push(...checkOverlayBlock(file, src, kind, REVIEW_ITEMS))
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
 * 自研弹层检查（组件级，不是逐类名级）。
 *
 * ★ 2026-10-03 修正过一次判据：原先要求每个 `.drawer__body` / `.modal__content`
 * 各自声明宽度，结果把 ChatParamsDrawer 判红 —— 但那里 `.drawer` 已经有
 * `width: min(360px,100vw)`，而 `.drawer__body` 是 flex 列的子项（`flex:1`），
 * 宽度本就由父容器决定。判据测的不是「弹层宽度不可控」这个真性质，
 * 而是「body 自己有没有写 width」。这是判据与被测性质不在同一处。
 *
 * 现在改成：只看**容器级**选择器（弹层根/面板），要求其中至少一个声明宽度；
 * 子部件（body/header/footer/content…）不参与宽度判定。子部件仍要满足
 * 「限高必须配滚动」——那条是内容完整性的真要求，与容器归属无关。
 */
// ★ 子部件后缀要同时认 `__` 与 `-` 两种分隔符。仓库里两种写法并存：
//   .drawer__body（UnifiedRequestSessionDrawer 那种）与
//   .drawer-body / .drawer-body-scroll（ChatParamsDrawer / ClientConfigDialog）。
// `panel` 故意不在列表里：__panel / -panel 是容器本身。
const OVERLAY_SUBPART_RE = /(?:__|-)(body|header|footer|content|hint|actions|title|meta|list|section|head|label|close|overlay|backdrop|toolbar|tabs|row|item|cell|text|icon|spacer|empty|error|loading)\b/

/**
 * 只有「以弹层本身命名」的类才算容器：`.drawer` / `.modal` /
 * `.app-drawer` / `.app-modal` / `.drawer__panel` / `.modal-panel`。
 *
 * 其余（`.drawer__body` 子部件、`.modal-danger` / `.drawer-sub` 修饰变体）
 * 都不算：修饰变体通常与一个已定宽的基类成对出现，单独判它「没定宽」是误报。
 * 这条判据宁可漏报也不误报 —— 误报会让人去改本来正确的代码。
 */
function isBaseOverlayContainer(cls, word) {
  const segs = cls.split(/(?:__|-)/).filter(Boolean)
  const wi = segs.findIndex((s) => s === word)
  if (wi < 0) return false
  const rest = segs.slice(wi + 1)
  if (rest.length === 0) return true // .drawer / .app-drawer
  return rest.length === 1 && (rest[0] === 'panel' || rest[0] === 'dialog')
}

function checkOverlayBlock(file, src, kind, review) {
  const problems = []
  const rel = relative(SRC, file)
  if (OVERLAY_EXEMPT.some((e) => rel === e.file)) return problems

  // ★ 只在 <style> 块内解析 CSS。早期版本扫全文，把 JavaScript 的
  // `if (!o) return ... {` 之类代码块当成 CSS 规则，于是
  // ModelOfferDetailDrawer 报出「弹窗容器（if (!o) return …）」这种垃圾。
  // 同时剥掉 CSS 注释，否则 AnnotationView 报出
  // 「容器（/* Modal internals */ .modal-sample-summary）」。
  const css = styleBlocks(src).join('\n').replace(/\/\*[\s\S]*?\*\//g, '')
  if (!css.trim()) return problems

  const word = kind === 'drawer' ? 'drawer' : 'modal'
  const classRe = new RegExp(`\\.([a-z0-9_-]*${word}[a-z0-9_-]*)`, 'gi')
  // 收集本组件里所有与该弹层相关的规则
  const rules = []
  const ruleRe = /([^{}]+)\{([^{}]*)\}/g
  let m
  while ((m = ruleRe.exec(css))) {
    const selector = m[1].trim()
    if (selector.startsWith('@')) continue
    const classes = [...selector.matchAll(classRe)].map((c) => c[1].toLowerCase())
    if (!classes.length) continue
    rules.push({ selector, body: m[2], line: css.slice(0, m.index).split('\n').length, classes })
  }
  if (!rules.length) return problems

  // 容器 = 以弹层本身命名、且不是子部件后缀的类
  //
  // ★ 误报家族③（2026-10-03 实测，AppNavDrawer.vue）：`.app-nav-drawer` 只是
  //   内层 `<nav>` 的类，模板根是 `<AppDrawer width="min(80vw,320px)">`（壳在
  //   另一个组件里，宽度是有声明的）。「名字里带 drawer」不等于「就是抽屉壳」。
  //   判别：容器类要么挂在模板根元素上，要么是 __panel/-panel 变体。
  //   偏保守 —— 可能漏报，但不会诱导人去改本来正确的代码。
  const rootClasses = new Set(templateRootClasses(src).map((c) => c.toLowerCase()))
  const containers = rules.filter((r) =>
    r.classes.some(
      (c) =>
        !OVERLAY_SUBPART_RE.test(c) &&
        isBaseOverlayContainer(c, word) &&
        (rootClasses.has(c) || /(?:__|-)(panel|dialog)$/i.test(c)),
    ),
  )
  if (containers.length) {
    const anyWidth = containers.some(
      (r) => /(?:^|;)\s*(?:width|min-width)\s*:/.test(r.body) || /(?:^|;)\s*max-width\s*:/.test(r.body),
    )
    if (!anyWidth) {
      const c = containers[0]
      problems.push({
        file,
        line: c.line,
        msg: `${word === 'drawer' ? '抽屉' : '弹窗'}容器（${c.selector}）未声明宽度，内容宽度不可控`,
      })
    }
  }

  // 限高必须可滚，但**不要求滚动写在同一条规则里**。
  //
  // 误报家族①（2026-10-03 实测）：弹窗壳限高 + 子项滚动，是最常见的写法 ——
  //   .modal-content { max-height: 90vh; display:flex; flex-direction:column }
  //   .modal-body    { flex: 1; overflow-y: auto }
  // （EmergencyDiagnosticModal.vue 真实形态）。原判据要求同规则内 overflow，
  //  把这种正确写法判成「内容会被裁切」—— 会诱导人去改本来没问题的代码。
  //
  // 误报家族②：子部件与壳同名族（.drawer__body vs .drawer）但壳在别的文件。
  //  AppNavDrawer.vue 的 .app-nav-drawer 只是内层 <nav>，壳是 AppDrawer 组件。
  //
  // 所以这里只保留「同规则滚动」与「同族 flex:1 子项滚动」两种豁免。
  // ── 内容可滚性：**降级为待人工确认项，不进硬门** ──
  //
  // 为什么降级（2026-10-03，三轮实证）：这条规则连续误报三次，每次都得回去读源码。
  //   ① 滚动写在同规则 → 判得对，但只覆盖一部分写法
  //   ② 滚动写在「同前缀兄弟」（.modal-content / .modal-body）→ 判得对
  //   ③ 滚动写在**任意命名的 flex:1 子项**（AttachmentManager.vue 的 .json-block
  //      { flex:1; overflow:auto }）→ 名字对不上，只能读源码才知道
  // 第③类在本仓是普遍写法（json-block / att-table-wrap / …），静态启发式要覆盖它
  // 就得把「同族」放宽成「同组件」，而那等于不再判别 —— 直接变成恒绿装饰。
  //
  // 判据只能证伪、不能证成时，留着它当门就是训练人忽略门。所以：
  //   硬门（--strict 会 exit 1）：能机械判定的那些
  //   待确认（只进报告）：限高但看不出滚动来源的，人点开看一眼即可
  const scrollableSelf = (r) => /overflow-y\s*:\s*(auto|scroll)|overflow\s*:\s*(auto|scroll)/.test(r.body)
  const flexScrollChildren = rules.filter(
    (r) =>
      /(?:^|;)\s*flex\s*:\s*1\b/.test(r.body) &&
      /(?:^|;)\s*overflow(-y)?\s*:\s*(auto|scroll)/.test(r.body),
  )
  // 壳与可滚子项是**同前缀兄弟**，不是后代：
  //   .modal-content / .modal-body
  //   .app-drawer__panel / .app-drawer__body
  // 所以按「去掉最后一段分隔符后的前缀」判同族，不能用 startsWith 前缀匹配
  // （那会把 modal-body 判成 modal-content 的子代，永远匹配不上）。
  const familyOf = (cls) => cls.replace(/(?:__|-)[a-z0-9_]+$/i, '')
  for (const r of rules) {
    const maxH = /(?:^|;)\s*max-height\s*:/.test(r.body)
    if (!maxH || scrollableSelf(r)) continue
    const fams = new Set(r.classes.map(familyOf))
    const sameFamily = flexScrollChildren.some((c) => c.classes.some((cc) => fams.has(familyOf(cc))))
    if (sameFamily) continue
    review.push({
      file,
      line: r.line,
      msg: `${r.selector} 限高但看不出滚动来源 —— 可能是子项滚动（命名对不上，需点开确认），也可能真会裁切`,
    })
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

  // ★ 判据回归护栏（2026-10-03）：这条判红过两次，都是判据错不是代码错。
  // ① 早期要求每个 `.drawer__body` 自己声明宽度 —— 真实形态取自
  //    components/chat/ChatParamsDrawer.vue：容器有宽度、body 是 flex:1 的子项
  //    （宽度由父级给），要求 body 写 width 测的不是真性质。
  // ② 早期不在 <style> 块内解析，把 JavaScript 的 `if (...) {` 当成 CSS 规则。
  const bodyInheritsWidth = `<template><div class="drawer"><div class="drawer__body"></div></div></template>
<style scoped>
.drawer { width: min(360px, 100vw); height: 100%; display: flex; flex-direction: column; }
.drawer__body { flex: 1; overflow-y: auto; padding: 12px 16px; }
</style>`
  // 连字符写法（.drawer-body-scroll）同样只继承容器宽度，不该被判红
  const hyphenBody = `<template><div class="drawer"><div class="drawer-body-scroll"></div></div></template>
<style scoped>
.drawer { width: min(720px, 92vw); height: 100%; display: flex; flex-direction: column; }
.drawer-body-scroll { flex: 1; overflow-y: auto; }
</style>`
  // script 里的 JavaScript 代码块不得被当成 CSS 规则/容器
  const jsBlockNotCss = `<template><div class="drawer"></div></template>
<script setup>
function open(o) {
  if (!o) return
  void refresh()
}
</script>
<style scoped>
.drawer { width: min(600px, 94vw); height: 100%; }
</style>`
  // 对照：容器真的没定宽 —— 这条必须仍判红，否则修复把鉴别力一起修掉了
  const containerUnsized = `<template><div class="drawer"><div class="drawer__body"></div></div></template>
<style scoped>
.drawer { height: 100%; }
.drawer__body { flex: 1; overflow-y: auto; }
</style>`
  // 限高在壳、滚动在 flex:1 子项 —— 真实形态取自 EmergencyDiagnosticModal.vue。
  // 原判据要求同规则内 overflow，把这种正确写法判红，属误报家族①。
  const shellLimitsBodyScrolls = `<template><div class="modal-content"><div class="modal-body"></div></div></template>
<style scoped>
.modal-content { display: flex; flex-direction: column; max-height: 90vh; }
.modal-body { flex: 1; overflow-y: auto; }
</style>`
  // 对照：限高且全族都没有任何滚动来源 —— 必须仍判红，否则豁免把牙齿一起修掉了
  const shellLimitsNoScrollAtAll = `<template><div class="modal-content"><div class="modal-body"></div></div></template>
<style scoped>
.modal-content { display: flex; flex-direction: column; max-height: 90vh; }
.modal-body { padding: 20px; }
</style>`

  // checkFullWidth / checkOverlays 的签名都是 (filePath, src)
  const f = (name, s, fn) => fn(join(VIEWS, name), s)
  const r = []
  r.push(['全屏：违规样本必须判红', f('bad.vue', badFull, checkFullWidth).length > 0])
  r.push(['全屏：合规样本必须判绿', f('good.vue', goodFull, checkFullWidth).length === 0])
  r.push(["弹层：限高无滚动（单规则形态）→ 必须判红", f("bad2.vue", badOverlay, checkOverlays).length > 0])
  r.push(['弹层：定宽+滚动必须判绿', f('good2.vue', goodOverlay, checkOverlays).length === 0])
  r.push(['折行：单元格 max-width:0 必须判红', f('bad3.vue', badZeroWidth, checkOverlays).length > 0])
  r.push(['折行：去掉 max-width:0 必须判绿', f('good3.vue', goodZeroWidth, checkOverlays).length === 0])
  r.push(['弹层：容器定宽、body 靠 flex 继承 → 判绿（判据回归护栏）', f('g5.vue', bodyInheritsWidth, checkOverlays).length === 0])
  r.push(['弹层：容器真的没定宽 → 仍判红（修复未把鉴别力一起修掉）', f('b5.vue', containerUnsized, checkOverlays).length > 0])
  r.push(['弹层：连字符写法 .drawer-body-scroll 继承容器宽度 → 判绿', f('g6.vue', hyphenBody, checkOverlays).length === 0])
  r.push(['弹层：<script> 里的 JS 代码块不得被当成 CSS 容器', f('g7.vue', jsBlockNotCss, checkOverlays).length === 0])
  r.push(['弹层：限高在壳、滚动在 flex:1 子项 → 判绿（误报家族①）', f('g8.vue', shellLimitsBodyScrolls, checkOverlays).length === 0])
  r.push(['弹层：限高且全族无滚动来源 → 进「待确认」桶（不再冒充硬门定罪）', (() => {
    REVIEW_ITEMS.length = 0
    f('b8.vue', shellLimitsNoScrollAtAll, checkOverlays)
    const flagged = REVIEW_ITEMS.length > 0
    REVIEW_ITEMS.length = 0
    return flagged
  })()])

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

// 2026-10-03：弹层大多住在 components/，不在 views/ —— 只扫 views 的话
// 契约 B 近乎空转（真身 RequestLogDrawer → UnifiedRequestSessionDrawer 都在
// components/detail/ 下）。组件层只跑契约 B：组件不是页面，不该有「根容器居中」
// 这条要求，那条只对 views 成立。
const componentFiles = walk(COMPONENTS).sort()
const componentOverlays = []
for (const f of componentFiles) {
  const src = readFileSync(f, 'utf8')
  componentOverlays.push(...checkOverlays(f, src))
}
overlays.push(...componentOverlays)

const inventory = buildListInventory(files)

const byFile = new Map()
for (const p of [...fullWidth.map((p) => ({ ...p, kind: 'A-全屏' })), ...overlays.map((p) => ({ ...p, kind: 'B-弹层' }))]) {
  if (!byFile.has(p.file)) byFile.set(p.file, [])
  byFile.get(p.file).push(p)
}

console.log(`扫描视图：${files.length} 个 · 组件：${componentFiles.length} 个`)
console.log(`契约 A 全屏违规：${fullWidth.length}`)
console.log(`契约 B 弹层违规：${overlays.length}（视图 ${overlays.length - componentOverlays.length} + 组件 ${componentOverlays.length}）`)
console.log(`待人工确认（不阻断构建）：${REVIEW_ITEMS.length}`)

if (byFile.size) {
  console.log('\n── 违规明细（硬门，--strict 会 exit 1）──')
  const sorted = [...byFile.entries()].sort((a, b) => a[0].localeCompare(b[0]))
  for (const [file, ps] of sorted) {
    console.log(`\n${relative(SRC, file)}`)
    for (const p of ps) console.log(`  [${p.kind}] L${p.line}  ${p.msg}`)
  }
}

if (REVIEW_ITEMS.length) {
  console.log('\n── 待人工确认（不阻断构建；判据只能提示、不能定罪）──')
  for (const r of REVIEW_ITEMS) {
    console.log(`  ${relative(SRC, r.file)} L${r.line}\n    ${r.msg}`)
  }
  console.log('  ↑ 这些要么是「子项滚动但命名对不上」，要么是真裁切。静态判据分不出来，点开看一眼。')
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
