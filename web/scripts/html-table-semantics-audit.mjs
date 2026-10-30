/**
 * html-table-semantics-audit.mjs — 「表格语义合规」门（2026-10-30 · r1050 候选 C）
 *
 * # 这道门补的是什么
 *
 * `web/src/views/RequestLogsView.vue` 的「会话缓存机制」说明表写成
 * `<table><tr>…` —— `<tr>` 直接挂在 `<table>` 下，**既无 `<thead>` 也无 `<tbody>`**。
 *
 * ★ 这类结构的后果不是「不好看」，而是**语义层被浏览器悄悄改写**：
 *   实测（jsdom，与浏览器同源的 HTML 解析器）：
 *     `<table><tr><td>x</td></tr></table>` 解析后 `table.children` = `[TBODY]`
 *   ⇒ 浏览器会**隐式插入**一个 `<tbody>`，把作者写的结构改掉。
 *   而 Vue 的**编译期模板**走的是另一条路（`innerHTML` 挂载），
 *   拿不到这个容错 ⇒ 同一份模板在编译期与运行期的 DOM 形状可以不一致。
 *   ⇒ 这也是 HTML lint 类工具（Vite / eslint-plugin-vue 的 `vue/valid-v-for` 族
 *     与浏览器的 `element-permitted-content` 检查）会报的原因：
 *     **`<tr>` 不是 `<table>` 的合法直接子节点**。
 *
 * ★★ 为什么不写成一个「一次性脚本」而是落成门：
 *   仓内纪律明写「临时 grep/脚本只能找线索，不能下判决」——
 *   一次性脚本没有回归保护，下一次有人写回 `<table><tr>` 时没有任何东西会红。
 *   ⇒ 本门与同族脚本（`ui-audit.mjs` / `compact-table-census.mjs` /
 *     `color-token-audit.mjs` / `deadcode-scan.mjs` …）同形：可复跑、有自证。
 *
 * # ★★★ 第二条判据：**「补了语义标签 ⇒ 全局表格样式开始生效」**（2026-10-30 r1050 轮追加）
 *
 * ★★ 本门把那张表修好（补 `<thead>`/`<tbody>`）之后，**出现了一个没人预料到的副作用**：
 *   全局 `src/style.css` 里有两条**元素选择器**规则：
 *     `thead th { font-size: var(--kx-text-2xs); font-weight: 600; color: var(--muted); … }`
 *     `tbody td { font-size: 13px; padding: 8px 12px; … }`
 *   ⇒ ★★ 那张表此前**因为缺 `<thead>`/`<tbody>` 而从未命中这两条规则**
 *   ⇒ ★★ 补上标签的**那一刻**，全局规则**开始管这张表**：
 *   ★ `tbody td { font-size: 13px }` 把这张 **11px 密排**的说明表撑成 13px 松排
 *   ★ （实测 jsdom 量测：无锁定规则时 `<td>` = **13px**，加锁定规则后 = **11px**）
 *   ⇒ ⇒ **补语义标签不是纯语法修复，它会让此前被绕过的样式规则开始生效。**
 *
 * ★★ **为什么这条要落进门，而不是留在审计档里**：
 *   ★ 若只改那一张表 ⇒ 下一个人给**别的**表补 `<tbody>` ⇒ 同样的字号劣化会重演
 *   ★ ⇒ **凡是「自身字号 ≠ 全局 `tbody td` 字号」的表**，补语义标签后必须显式锁定字号
 *   ⇒ 本门因此增加一条**可机械判定**的检查：
 *   ★★ 表格若（a）自身带 `font-size` 声明且（b）该值 ≠ 全局 `tbody td` 的 13px
 *   ★★ 且（c）它**已有 `<thead>`/`<tbody>`** ⇒ 它就必须有一条**覆盖全局字号的规则**
 *   ★★ （scoped 规则或行内 style 均可）⇒ 否则判红。
 *
 * ★★ **为什么只查「字号」这一项，不查全部排版属性**：
 *   ★ padding / border 在那张表上都是**行内 style**（行内优先 ⇒ 全局改不动）⇒ 不受影响；
 *   ★ font-weight / color / hover 底色是**改善**（表头终于有层级、数据格终于有规范）⇒ 不判；
 *   ★ ⇒ **只把「劣化」判红，不把「改善」判红** ⇒ 与本仓「窄而准」的判据纪律一致。
 *
 * # 判据
 *
 *   对 `src/` 下每个 `.vue` 文件的 **`<template>` 块**（不是整文件！）做一次
 *   深度配对扫描，判定是否存在「`<tr>` 的父节点是 `<table>`」。
 *
 * ★★ **为什么只判 `<tr>` 直挂 `<table>` 这一种，不做完整的 HTML 合法性判定**：
 *   ① 完整的 HTML 内容模型判定需要一张 per-element 的 permitted-content 表，
 *      自造一份就是「第二份真值」（仓内明令禁止）；
 *      ★ 而 HTML 规范本身是唯一事实源，但仓内没有这张表的权威副本。
 *   ② 本门只覆盖**本仓真实出现过的那一族**（有据可查：r1050 候选 C 的实测样本），
 *      判据窄但**每一处都会红**——比宽而漏好。
 *   ③ 其余形态（`<td>` 直挂 `<table>`、`<th>` 直挂 `<table>` 等）由浏览器容错，
 *      本仓实测未出现，且不属本轮范围。
 *
 * # ★★ 为什么必须先剥 `<script>`/`<style>` 再扫（掩码优先）
 *
 *   模板字符串里可能内嵌 HTML 片段（如 `h('table', …)` 或反引号里的 `<tr>`）。
 *   ⇒ 若对**整文件**做标签配对，`<script>` 内的字面量会污染栈 ⇒ 假阳性。
 *   ⇒ 本门只取 `<template>` 块（`vue/compiler-sfc` 的 `descriptor.template.content`），
 *     它天然不含 `<script>`/`<style>` 内容 ⇒ 栈不会被污染。
 *
 * # 用法
 *
 *   node scripts/html-table-semantics-audit.mjs             # 人读报告
 *   node scripts/html-table-semantics-audit.mjs --strict    # 有违规 exit 1（CI 门）
 *   node scripts/html-table-semantics-audit.mjs --selftest  # 自证（植入违规 → 判红）
 *
 * ★ 自证必须**两个方向**都成立：
 *   「植入违规 → 判红」证明判据有牙；
 *   「干净样本 → 判绿」证明判据不是恒红门。
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const { parse } = require('vue/compiler-sfc')

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')
const SRC = resolve(WEB, 'src')

// ─────────────────────────────────────────────────────────────
// 扫描面
// ─────────────────────────────────────────────────────────────

function walk(dir, out = []) {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (p.endsWith('.vue')) out.push(p)
  }
  return out
}

/**
 * 判据二的仓库级扫描：逐文件跑 `findUnlockedTableFontSize`。
 *
 * ★ 每个文件要读它自己的 `<style>` 块（scoped 规则就写在那里）⇒
 *   ★ **只看 template 块会漏掉「锁定规则在哪」这个必要输入**。
 */
export function scanFontSize() {
  const files = walk(SRC).sort()
  const gSize = globalTbodyTdFontSize()
  const red = []
  const exempted = []
  for (const f of files) {
    const rel = relative(WEB, f).split('\\').join('/')
    const raw = readFileSync(f, 'utf8')
    const { descriptor } = parse(raw, { filename: f })
    if (!descriptor.template) continue
    const styles = descriptor.styles.map((s) => s.content).join('\n')
    const hits = findUnlockedTableFontSize(descriptor.template.content, styles, gSize)
    const part = partitionByExemption(hits, LEGACY_FONT_SIZE_EXEMPTIONS, rel)
    for (const h of part.red) red.push({ file: rel, ...h })
    for (const h of part.exempted) exempted.push({ file: rel, ...h })
  }
  return { files, fontFindings: red, exempted, globalSize: gSize }
}

/**
 * 返回「`<tr>` 的父节点是 `<table>`」的命中列表。
 *
 * ★ 栈式配对扫描：开标签入栈、闭标签出栈（`lastIndexOf` 找最近同名的那个）、
 *   自闭合标签不入栈。
 * ★ Vue 模板里的 `<tr v-for=…>` 属于「带属性的开标签」，不是自闭合 ⇒ 正常入栈。
 * ★ HTML 里 `<tr>`/`<td>`/`<th>` 的可选结束标签写法（省略 `</tr>`）在本仓未出现；
 *   若出现，`lastIndexOf` 的出栈语义仍能保持“最近的同名标签先闭合”的直觉。
 */
export function findTrDirectlyUnderTable(templateContent) {
  const re = /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)([^>]*?)(\/?)>/g
  const stack = []
  const hits = []
  let m
  while ((m = re.exec(templateContent)) !== null) {
    const [, closing, tag, , selfClosing] = m
    if (closing) {
      const i = stack.lastIndexOf(tag)
      if (i >= 0) stack.splice(i, 1)
      continue
    }
    if (selfClosing) continue
    const parent = stack[stack.length - 1]
    if (tag === 'tr' && parent === 'table') {
      const line = templateContent.slice(0, m.index).split('\n').length
      hits.push({ line, tag, parent })
    }
    stack.push(tag)
  }
  return hits
}

function scanAll() {
  const files = walk(SRC).sort()
  const findings = []
  for (const f of files) {
    const rel = relative(WEB, f).split('\\').join('/')
    const { descriptor } = parse(readFileSync(f, 'utf8'), { filename: f })
    if (!descriptor.template) continue
    for (const h of findTrDirectlyUnderTable(descriptor.template.content)) {
      findings.push({ file: rel, line: h.line })
    }
  }
  return { files, findings }
}

// ─────────────────────────────────────────────────────────────
// 判据二：补语义标签后，全局 `tbody td` 的字号会开始管这张表
// ⇒ 自身字号 ≠ 全局字号的表，必须显式锁定字号
// ─────────────────────────────────────────────────────────────

/**
 * 全局 `tbody td` 的 font-size 真值。
 *
 * ★★ **从 `src/style.css` 现读，不写死**：★ 写死就等于造了「第二份真值」，
 *   ★ 而全局字号一旦被改，这个门会**用过期真值去判活代码** ⇒ 假红或假绿。
 *   ⇒ 读不到 ⇒ 返回 null ⇒ 本判据**弃权**（不判红），并由自证记录该情形。
 */
export function globalTbodyTdFontSize() {
  let css
  try {
    css = readFileSync(resolve(WEB, 'src', 'style.css'), 'utf8')
  } catch {
    return null
  }
  const m = css.match(/tbody\s+td\s*\{[^}]*font-size\s*:\s*([^;]+);/i)
  return m ? m[1].trim() : null
}

/**
 * ★★★ 存量豁免清单（2026-10-30 r1050 轮登记）
 *
 * ★★ 这 3 张表**自始**就带 `<thead>`/`<tbody>` ⇒ 全局 `tbody td { font-size: 13px }`
 *   **一直在管它们的 `<td>`** ⇒ 它们表级声明的 `font-size:12px` **自始失效**
 *   ⇒ ★★ 界面现状一直是 13px ⇒ ★★ **把它们改成 12px 是未经裁决的视觉变更**
 *   ⇒ ★★ 删掉那条失效声明同样是一次（虽无害的）视觉语义整理
 *   ⇒ ⇒ 按仓内既有纪律（§585：「红本身按门内注释分诊：合法同形→人工调基线 /
 *   ★★ 真缺陷→修源码 / ★★ **存量→登记降级**」）⇒ ★★ **登记降级，不擅自改视觉**。
 *
 * ★★★ 但**豁免必须显式、且必须写清「它为什么是存量」** ⇒ ★★ 否则这张表就成了
 *   「任何人都能加一行进来的免死金牌」⇒ ★★ 本清单每条都带
 *   ★★ `since`（该表何时起就带语义标签）+ `why`（为何不改）。
 *   ★★ 若将来有人真把它修成 12px（补上锁定规则）⇒ 本门自然不再命中它 ⇒
 *   ★★ **清单条目可以且应该被「消解」**（⇒ 下游可据此定期清理本清单）。
 */
const LEGACY_FONT_SIZE_EXEMPTIONS = [
  {
    file: 'src/views/AgentRegistryView.vue',
    own: '12px',
    since: '早于 2026-10-30（r1050 轮之前即已带 <thead>/<tbody>）',
    why: '存量：12px 声明自始被全局 tbody td 13px 覆盖；改它 = 未经裁决的视觉变更',
  },
  {
    file: 'src/views/RequestLogsView.vue',
    own: '12px',
    since: '早于 2026-10-30（r1050 轮之前即已带 <thead>/<tbody>）',
    why: '存量：request-log-table 的 12px 自始被全局 13px 覆盖；改它 = 未经裁决的视觉变更',
  },
  {
    file: 'src/views/provider-detail/DiagTab.vue',
    own: '12px',
    since: '早于 2026-10-30（r1050 轮之前即已带 <thead>/<tbody>）',
    why: '存量：12px 声明自始被全局 tbody td 13px 覆盖；改它 = 未经裁决的视觉变更',
  },
]

/**
 * 返回「表格自身字号 ≠ 全局 tbody td 字号、且已补语义标签、却没有锁定字号」的命中列表。
 *
 * ★ 判定条件（全部满足才命中）：
 *   ① 表自带 `font-size` 声明（行内 style）；
 *   ② 该值 ≠ 全局 `tbody td` 的字号；
 *   ③ 它**已有** `<thead>`/`<tbody>`（⇒ 全局规则真的管它了）；
 *   ④ 它**没有**任何覆盖字号的规则（scoped 样式块里的该表选择器，或单元格行内 font-size）。
 *
 * ★★ `exempt` 参数：存量豁免清单（按 `file` + `own` 匹配）。
 *   ★★ **豁免只放行「已登记的存量」⇒ 新增的同类劣化仍判红**（★ 这才是这道门的目的）。
 */
export function findUnlockedTableFontSize(templateContent, styleContent, globalSize, exempt = []) {
  if (!globalSize) return []
  const hits = []
  const re = /<table\b([^>]*)>/g
  let m
  while ((m = re.exec(templateContent)) !== null) {
    const attrs = m[1]
    const fsMatch = attrs.match(/font-size\s*:\s*([^;"]+)/i)
    if (!fsMatch) continue // ① 不自带字号 ⇒ 继承全局 ⇒ 无需锁定
    const own = fsMatch[1].trim()
    if (own === globalSize) continue // ② 与全局一致 ⇒ 无需锁定

    // ③ 找该 table 的闭合位置与内部，判断是否已有 thead/tbody
    const closeIdx = templateContent.indexOf('</table>', m.index)
    if (closeIdx < 0) continue
    const inner = templateContent.slice(m.index, closeIdx)
    if (!/<thead\b/i.test(inner) && !/<tbody\b/i.test(inner)) continue // ③ 未补语义标签 ⇒ 全局规则还不管它

    // ④ 是否已有覆盖字号的规则
    const classAttr = attrs.match(/class\s*=\s*"([^"]*)"/i)
    const cls = classAttr ? classAttr[1].trim().split(/\s+/)[0] : null
    let locked = false
    if (cls && styleContent) {
      // scoped 规则里出现该类名 + font-size ⇒ 已锁定
      const ruleRe = new RegExp(`\\.${cls}[^{]*\\{[^}]*font-size`, 'i')
      if (ruleRe.test(styleContent)) locked = true
    }
    if (!locked && /<t[dh][^>]*font-size\s*:/i.test(inner)) locked = true
    if (locked) continue

    const line = templateContent.slice(0, m.index).split('\n').length
    hits.push({ line, own, global: globalSize, cls })
  }
  return hits
}

/**
 * 把命中按「存量豁免」分流。
 *
 * ★★ 返回 `{ red, exempted }` 两组：
 *   ★★ `red` ⇒ 必须判红的（新增劣化）；
 *   ★★ `exempted` ⇒ 命中了判据但**已在存量清单里登记**（⇒ 不判红，**但必须打印出来**）
 *   ★★ ⇒ ★★ **豁免不许静默**：★ 静默豁免 = 「免死金牌」没人看得见 ⇒ ★★ 每次运行都列出来。
 */
export function partitionByExemption(findings, exemptions, file) {
  const red = []
  const exempted = []
  for (const h of findings) {
    const e = exemptions.find((x) => x.file === file && x.own === h.own)
    if (e) exempted.push({ ...h, since: e.since, why: e.why })
    else red.push(h)
  }
  return { red, exempted }
}

// ─────────────────────────────────────────────────────────────
// 自证（--selftest）
// ─────────────────────────────────────────────────────────────

const LEGAL = '<table><thead><tr><th>a</th></tr></thead><tbody><tr><td>x</td></tr></tbody></table>'
const DEFECT = '<table><tr><td>x</td></tr></table>'

function selftest() {
  let failed = 0
  const check = (name, ok, detail) => {
    console.log(`${ok ? '  ✔' : '  ✘'} ${name}${detail ? ` — ${detail}` : ''}`)
    if (!ok) failed++
  }
  console.log('自证：')

  check(
    '干净样本（table>thead/tbody>tr）判绿',
    findTrDirectlyUnderTable(LEGAL).length === 0,
    `命中 ${findTrDirectlyUnderTable(LEGAL).length}（期望 0）`,
  )
  check(
    '缺陷样本（table>tr）判红',
    findTrDirectlyUnderTable(DEFECT).length === 1,
    `命中 ${findTrDirectlyUnderTable(DEFECT).length}（期望 1）`,
  )
  check(
    '嵌套 table 里的 tr 不误报（父节点是内层 table ⇒ 命中）',
    findTrDirectlyUnderTable('<table><tbody><tr><td><table><tr><td>y</td></tr></table></td></tr></tbody></table>')
      .length === 1,
    '内层 table 的 tr 确为缺陷 ⇒ 命中 1（期望 1）',
  )
  check(
    'script 块里的字面量不被扫（真实通道：template 块不含 script 内容）',
    (() => {
      // 真实路径：整份 SFC → compiler-sfc 抽出 template 块 → 块内不含 <script> 内容
      const sfc = `<template><div>ok</div></template>\n<script setup lang="ts">\nconst s = '<table><tr><td>x</td></tr></table>'\n</script>\n`
      const { descriptor } = parse(sfc, { filename: 'probe.vue' })
      if (!descriptor.template) return false
      return findTrDirectlyUnderTable(descriptor.template.content).length === 0
    })(),
    '期望 0',
  )

  const { findings } = scanAll()
  check(
    '真实仓当前无命中（本门不是恒红门）',
    findings.length === 0,
    `命中 ${findings.length}（期望 0）`,
  )

  // ── 判据二的自证 ──
  const gSize = globalTbodyTdFontSize()
  check(
    '全局 tbody td 字号真值可从 style.css 现读（不写死）',
    gSize !== null && gSize !== '',
    `读到 ${gSize}`,
  )

  check(
    '判据二-负例：自带 11px + 已补 thead/tbody + 无锁定规则 ⇒ 判红',
    findUnlockedTableFontSize(
      '<table style="font-size:11px"><thead><tr><th>h</th></tr></thead><tbody><tr><td>d</td></tr></tbody></table>',
      '',
      gSize,
    ).length === 1,
    `命中 ${findUnlockedTableFontSize('<table style="font-size:11px"><thead><tr><th>h</th></tr></thead><tbody><tr><td>d</td></tr></tbody></table>', '', gSize).length}（期望 1）`,
  )
  check(
    '判据二-正例：scoped 规则已锁定字号 ⇒ 判绿',
    findUnlockedTableFontSize(
      '<table class="guide-table" style="font-size:11px"><thead><tr><th>h</th></tr></thead><tbody><tr><td>d</td></tr></tbody></table>',
      '.guide-table th, .guide-table td { font-size: 11px; }',
      gSize,
    ).length === 0,
    '期望 0',
  )
  check(
    '判据二-正例：单元格行内 font-size 已锁定 ⇒ 判绿',
    findUnlockedTableFontSize(
      '<table style="font-size:11px"><thead><tr><th>h</th></tr></thead><tbody><tr><td style="font-size:11px">d</td></tr></tbody></table>',
      '',
      gSize,
    ).length === 0,
    '期望 0',
  )
  check(
    '判据二-正例：尚未补语义标签 ⇒ 全局规则还不管它 ⇒ 不判红',
    findUnlockedTableFontSize(
      '<table style="font-size:11px"><tr><td>d</td></tr></table>',
      '',
      gSize,
    ).length === 0,
    '期望 0',
  )
  check(
    '判据二-正例：自身字号与全局一致 ⇒ 无需锁定 ⇒ 不判红',
    findUnlockedTableFontSize(
      `<table style="font-size:${gSize}"><thead><tr><th>h</th></tr></thead><tbody><tr><td>d</td></tr></tbody></table>`,
      '',
      gSize,
    ).length === 0,
    '期望 0',
  )
  check(
    '判据二-弃权：全局真值读不到 ⇒ 本判据不判红（不拿过期真值判活代码）',
    findUnlockedTableFontSize(
      '<table style="font-size:11px"><thead><tr><th>h</th></tr></thead><tbody><tr><td>d</td></tr></tbody></table>',
      '',
      null,
    ).length === 0,
    '期望 0',
  )

  const { fontFindings, exempted } = scanFontSize()
  check(
    '判据二在真实仓当前无「新增」命中（不是恒红门）',
    fontFindings.length === 0,
    `命中 ${fontFindings.length}（期望 0）`,
  )
  check(
    '存量豁免确实被命中（★ 否则豁免清单是死代码 ⇒ 门会失去「新增劣化」这道拦截）',
    exempted.length > 0,
    `豁免命中 ${exempted.length}（期望 >0）`,
  )
  check(
    '豁免分流有效：登记在案的 ⇒ 不判红；未登记的 ⇒ 判红',
    (() => {
      const one = [{ file: 'x.vue', line: 1, own: '12px' }]
      const registered = partitionByExemption(one, [{ file: 'x.vue', own: '12px' }], 'x.vue')
      const unregistered = partitionByExemption(one, [], 'x.vue')
      return registered.red.length === 0 && registered.exempted.length === 1 && unregistered.red.length === 1
    })(),
    '期望：登记⇒red 0 / 未登记⇒red 1',
  )

  if (failed > 0) {
    console.log(`\n自证失败 ${failed} 项`)
    process.exit(2)
  }
  console.log('\n自证全部通过')
}

// ─────────────────────────────────────────────────────────────
// 入口
// ─────────────────────────────────────────────────────────────

const argv = process.argv.slice(2)
if (argv.includes('--selftest')) {
  selftest()
} else {
  const { files, findings } = scanAll()
  const { fontFindings, exempted } = scanFontSize()
  console.log(`扫描 ${files.length} 个 .vue 文件（src/ 全量）`)

  const total = findings.length + fontFindings.length
  if (findings.length === 0) {
    console.log('✔ 表格语义合规：无「<tr> 直挂 <table>」')
  } else {
    console.log(`✘ 违规 ${findings.length} 处（「<tr>」不得是「<table>」的直接子节点）：`)
    for (const f of findings) console.log(`  ${f.file}:${f.line}`)
    console.log('\n修法：给表头行包 <thead>、数据行包 <tbody>。')
  }

  if (fontFindings.length === 0) {
    console.log('✔ 表格字号合规：无「补语义标签后被全局 tbody td 字号接管却未锁定」的表')
  } else {
    console.log(`✘ 违规 ${fontFindings.length} 处（表格自带字号 ≠ 全局 tbody td 字号，已补语义标签却未锁定）：`)
    for (const f of fontFindings) {
      console.log(`  ${f.file}:${f.line} — 自身 ${f.own} vs 全局 ${f.global}`)
    }
    console.log('\n修法：给该表加一条 scoped 规则把字号钉回自身值（如 .xxx th, .xxx td { font-size: … }）。')
  }

  if (exempted.length > 0) {
    console.log(`\nℹ 存量豁免 ${exempted.length} 处（命中判据但**已登记**，不判红；★ 豁免不静默 ⇒ 每次列出）：`)
    for (const f of exempted) {
      console.log(`  ${f.file}:${f.line} — 自身 ${f.own} vs 全局 ${f.global}`)
      console.log(`     存量时点：${f.since}`)
      console.log(`     豁免理由：${f.why}`)
    }
    console.log('  ★ 这些表自始被全局 tbody td 字号接管 ⇒ 表级 font-size 声明**自始失效**')
    console.log('  ★ ⇒ 改成声明值 = 未经裁决的视觉变更 ⇒ 按仓内纪律「存量→登记降级」保留现状')
  }

  if (argv.includes('--strict') && total > 0) process.exit(1)
}
