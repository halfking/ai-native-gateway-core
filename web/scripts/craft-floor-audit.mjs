/**
 * craft-floor-audit.mjs — 视觉「AI 痕迹」静态审计（2026-10-24）
 *
 * 为什么要有这道门
 * ────────────────
 * 本仓有完整的**布局正确性**门禁（ui-audit / ui-sweep / responsive-audit /
 * device-fit-audit），但**没有一道门管「品味」**。结果就是：AI 生成界面的
 * 那些标志（彩色侧边条、中文界面里的 uppercase、渐变文字、硬偏移阴影、
 * 标题上方的 eyebrow、emoji 当图标、01/02 装饰序号）可以随时被写进来，
 * 而且没有任何东西会变红。
 *
 * 这道门把 impeccable 的 craft-floor 硬禁项变成可执行的判据。
 * 完整规则见仓库根目录 DESIGN.md §6 Components / §8 Do and Do Not。
 *
 * 覆盖的判据
 * ──────────
 *   A. 彩色侧边条 ≥2px        「A colored border-left or border-right above 1px
 *                              on cards, list items, callouts, or alerts.」
 *   B. 界面文案里的 uppercase   「中文没有大小写，uppercase 对中文是空操作」
 *   C. 渐变文字                「Gradient text. Emphasis comes from weight or size.」
 *   D. 硬偏移阴影              「box-shadow: 4px 4px 0 —— 那是 neobrutalism 的戏服」
 *   E. 标题上方的 eyebrow/kicker「A kicker or eyebrow above a heading. This one is
 *                              a ban, not a default: no brief earns it back.」
 *   F. emoji / Unicode 字形当图标「Unicode glyphs or emoji standing in for an
 *                              icon system.」
 *   G. 01/02 装饰序号          「Section numbers (01 / 02 / 03) unless the sequence
 *                              itself carries information」
 *
 * 用法
 * ────
 *   node scripts/craft-floor-audit.mjs             # 人读报告
 *   node scripts/craft-floor-audit.mjs --strict    # 有违规则 exit 1（CI 门）
 *   node scripts/craft-floor-audit.mjs --json out.json
 *   node scripts/craft-floor-audit.mjs --selftest  # ★ 判据自证（见下）
 *
 * ★ 判据必须能区分合格与不合格：改完请跑 --selftest，确认
 *   「植入违规 → 判红」与「干净样本 → 判绿」两个方向都成立。
 *
 * ★ 路径键统一归一成 POSIX 分隔符：node:path 的 relative() 在 Windows 返回
 *   反斜杠，而豁免表一律按 POSIX 书写 ⇒ 不归一会导致同一份代码在 Windows 上
 *   全量误报（web/scripts/ui-audit.mjs 踩过这个坑，见那里的注释）。
 */

import { readFileSync, readdirSync, statSync, writeFileSync, mkdirSync } from 'node:fs'
import { resolve, relative, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const WEB = resolve(HERE, '..')
const ROOTS = [
  { dir: resolve(WEB, 'src'), label: 'web/src' },
  { dir: resolve(WEB, '..', 'web-mobile', 'src'), label: 'web-mobile/src' },
]

/** 相对路径键统一成 POSIX 分隔符（Windows 教训，见文件头）。 */
const relPosix = (root, p) => relative(root, p).replace(/\\/g, '/')

function walk(dir, out = []) {
  if (!existsDir(dir)) return out
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (p.endsWith('.vue') || p.endsWith('.css')) out.push(p)
  }
  return out
}
function existsDir(p) {
  try { return statSync(p).isDirectory() } catch { return false }
}

/**
 * 剥掉注释再判。
 *
 * ★ 为什么必须剥：本仓的 CSS 里大量写着「原来这里是 `text-transform: uppercase`」
 *   这样的**说明性注释** —— 不剥的话，门会把自己写的整改说明判成违规，
 *   变成一条永远红的死门（假绿变假红，更难查）。
 */
export function stripComments(src) {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
    .replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '))
    .replace(/(^|[^:])\/\/[^\n]*/g, (m, p1) => p1 + ' '.repeat(Math.max(0, m.length - p1.length)))
}

// ─────────────────────────────────────────────────────────────
// 豁免表
// ─────────────────────────────────────────────────────────────

/**
 * ★ 侧边条**不再用路径白名单豁免**（2026-10-25）。
 *
 * 旧判据是「颜色像不像状态色」，判不准的地方只能靠白名单兜底 ——
 * 结果 `RequestOverviewPanel.vue` 的 `.qa-block` 被豁免了，而它的
 * `.qa-block--user { border-color: var(--kx-primary) }` 实际把这条 3px 竖线
 * **染了色** ⇒ 豁免掩盖的是真违规。
 *
 * 现在的判据是**形状**（同规则内「粗单边 + 圆角」），于是：
 *   · `.stt-children` 树形引导线 —— 2px，但**同规则没有圆角** ⇒ 天然不判红；
 *   · `.card__chevron` CSS 箭头 —— 2px，且**同规则没有圆角** ⇒ 天然不判红。
 * 几何豁免不再需要写进表里。
 */

/**
 * uppercase 作用在**拉丁数据值**上的站点：徽标里显示的是 API 返回的英文
 * token（image / opened / failure_observed），那是数据不是界面文案，
 * 大写反而更易扫读。
 */
const UPPERCASE_DATA_VALUES = [
  'components/detail/MultimodalAttachmentsPanel.vue', // .kind
  'components/RouteIncidentDrawer.vue',                // .event-row__type
]

/**
 * emoji 当图标：正文里出现 emoji 字形就是违规。
 * 例外是**内容本身**是 emoji 的场景（示例库里给示例配的表情符号）。
 */
const EMOJI_ALLOWED = [
  'views/ExamplesView.vue',
]

// ─────────────────────────────────────────────────────────────
// 判据
// ─────────────────────────────────────────────────────────────

/**
 * emoji / 字形当图标。
 *
 * ★ 判据分两档，这是刻意的取舍，写在这里是为了让后来者看得见边界：
 *
 *  ① **真 emoji**（U+1F300–U+1FAFF、U+1F900–U+1F9FF）—— 无条件违规。
 *     「📥 表示下载」这种就是用 Unicode 字形顶替图标系统。
 *
 *  ② **功能图标字形**（☰ 拖拽手柄、➕ 新增、⚙ 设置……）—— 违规。
 *     它们在按钮/手柄上**扮演图标角色**，该用图标库或手写 SVG。
 *
 *  ③ **不算违规**：✓ / ✗ 这类**状态文字标记**。
 *     它们出现在「启用/禁用」开关按钮里，承载的是**状态本身**
 *     （哪个状态），不是一个通用图标；换成 SVG 反而会把语义变窄。
 *     本仓 29 处这类用法一律不判红 —— 判据只管「图标」，不管「文字标记」。
 */
const EMOJI_RE = /[\u{1F300}-\u{1FAFF}\u{1F900}-\u{1F9FF}\u{2705}\u{274C}\u{2B50}\u{26A1}\u{23F3}\u{23F1}\u{1F195}\u{1F18E}]/gu
const GLYPH_ICON_RE = /[☰➕➖⚙⌄▶⏸⏵⏭⏮✎✏]/gu
const GLYPH_ICON_SET = '☰➕➖⚙⌄▶⏸⏵⏭⏮✎✏'

/**
 * dingbat 字形（✕ ▾ ⚠）—— **只在图标位上**判红。
 *
 * ★ 为什么不能一刀切：`⚠️ 数据聚合视图未就绪（…）` 里的 ⚠ 是**这句话的一部分**，
 *   与仓库既有的「✓/✗ 状态文字标记不判红」是同一条线。
 *   一刀切会把 30 处正常告警文字判红 ⇒ 假红比没门更糟。
 * ★ 图标位的判据（与 web/scripts/sweep_emoji_icons.py 同一套）：
 *   ① 所在元素的 class 含 icon / caret / close
 *   ② 或它是 <button> 且字形是唯一内容（关闭按钮）
 *   ③ 或它有 aria-hidden 且字形是唯一内容（纯装饰图标位）
 */
const DINGBAT_ICON_RE = /[✕▾▴▸⚠]/gu
const DINGBAT_ICON_SET = '✕ ▾ ▴ ▸ ⚠'
/** 元素 class 命中即视为图标槽。 */
const ICON_SLOT_CLASS_RE = /(^|[\s_-])(icon|caret|close)([\s_-]|$)/

/**
 * A. 侧边条 / 圆角上的粗边。
 *
 * ★ 判据口径（2026-10-25 改版，对齐 impeccable 参考实现 `crates/core/src/checks/rules.rs`）：
 *
 *   原文只说「A **colored** `border-left` or `border-right` above 1px」。
 *   第一版按「颜色是不是语义状态色」去判，漏了一大片：
 *     - `border-left-width: 3px`（长写）根本没匹配到；
 *     - `border-top: 3px solid`（顶边）不在这条规则的字面里；
 *     - 颜色常常**不在同一条规则里**（`.x { border-color: var(--success) }` 另开一条），
 *       于是「是不是 colored」判不出来，只能靠路径白名单兜底 —— 那是掩盖，不是判据。
 *
 *   参考实现的判据是**形状**而不是**颜色**：
 *     ① 同一条 CSS 规则里，既有 ≥2px 的单边边框，又有非零 `border-radius`
 *        ⇒ 违规（`side-tab` / `border-accent-on-rounded`）；
 *     ② `::before`/`::after` 画的窄条（绝对定位 + 宽 ≤4px + 通高 + 有背景色）
 *        ⇒ 违规（它就是一条画出来的侧边条）。
 *
 *   这套口径的好处：
 *     - 不需要解析颜色（CSS 变量本来就解析不了）；
 *     - 不需要路径白名单 —— 树形引导线（`.stt-children`，2px 且**同规则无圆角**）
 *       与 CSS 箭头（`.card__chevron`，2px 且**同规则无圆角**）天然不判红；
 *     - 「颜色在别的规则里」这种最常见的漏标形态，判据直接覆盖。
 */
export function checkSideStripes(file, src) {
  const problems = []
  const blocks = cssBlocks(src)

  // ① 伪元素画的窄条。
  //   ★ 必须**独立于圆角判断**：条画在卡片的边上，卡片的圆角写在**父规则**里
  //   （`.stat-card { border-radius: 10px }` + `.stat-card::before { … }`），
  //   而条自己的规则块里没有圆角 ⇒ 若跟着圆角走就会被整条漏掉。
  for (const blk of blocks) {
    const pseudo = /::(before|after)\b/.exec(blk.selector)
    if (pseudo && isStripePseudo(blk.decls)) {
      problems.push({
        file, line: lineOf(src, blk.index),
        msg: `::${pseudo[1]} 画出的窄色条（绝对定位 + 通高 + 有背景）—— 那就是一条画出来的侧边条；改用整块 1px 语义描边或状态点`,
      })
    }
  }

  // ② 粗单边 + **本规则**有圆角 + **同基类**有色。
  //   ★ 为什么圆角只看本规则、颜色要看同基类：
  //     · 圆角是「这个盒子长什么样」——同基类的不同变体可能长得完全不同
  //       （`.tab-btn` 是无边框的标签页，`.tab-btn.small` 是带 6px 圆角的小按钮）。
  //       按基类名把两个变体的圆角合并，就会把标签页误判成圆角卡片。
  //     · 颜色是「这个元素会不会被染色」——最常见的漏标形态恰恰是
  //       颜色写在 modifier 规则里：
  //         .cp__row { border: 1px solid transparent; border-left-width: 3px; border-radius: 7px; }
  //         .cp__row.is-success { border-color: var(--kx-success); }
  //       逐条读的话会因 transparent 放过去，而它实际会被染上状态色。
  //     ⇒ 圆角按规则读、颜色按基类组读。
  //   ★ 已知边界（宁可漏也不假红）：「基类规则有圆角、modifier 规则里有粗边」
  //     这一形态本门读不到（参考实现读的是同一元素的计算属性，能读到）。
  //     代价换来的是：不会把「标签页下划线」误判成「卡片色条」。
  const colors = colorsByBaseClass(blocks)
  for (const blk of blocks) {
    const radius = readRadius(blk.decls)
    if (radius === 'square') continue // 本规则无圆角 ⇒ 方盒子，不是卡片
    const g = colors.get(elementKey(blk.selector))
    if (!g || !g.colored) continue
    for (const e of edgesOf(blk)) {
      if (e.width < 2) continue
      problems.push({
        file, line: lineOf(src, e.index),
        msg: `${e.side} 边 ${e.width}px 粗边 + 同规则有圆角（${radius}）—— craft-floor 硬禁「卡片上 1px 以上的单边色条」；改用整块 1px 语义描边或状态点`,
      })
    }
  }
  return problems
}

/** 按基类名聚合「这个元素会不会被染色」。 */
function colorsByBaseClass(blocks) {
  const map = new Map()
  for (const blk of blocks) {
    if (/::(before|after)\b/.test(blk.selector)) continue
    const key = elementKey(blk.selector)
    let g = map.get(key)
    if (!g) { g = { colors: [] }; map.set(key, g) }
    for (const c of declColors(blk.decls)) g.colors.push(c)
  }
  for (const g of map.values()) {
    // 一个颜色都没声明 ⇒ 读不出 ⇒ 按有色算（判红是失败安全侧）
    g.colored = g.colors.length === 0
      ? true
      : g.colors.some((c) => !/^(transparent|none|initial|unset|inherit)$/i.test(c))
  }
  return map
}

/** 规则块里的单边边框（宽度 ≥1 的都收，判定留给调用方）。 */
function edgesOf(blk) {
  const out = []
  for (const m of blk.decls.matchAll(/border-(left|right|top|bottom|inline-start|inline-end)(-width)?\s*:\s*([^;]+)/g)) {
    const w = firstPx(m[3])
    if (w === null) continue
    out.push({ side: m[1], width: w, index: blk.index + m.index })
  }
  return out
}

/** 元素键：最后一个复合选择器的第一个类名（BEM 修饰符归到基类）。 */
function elementKey(selector) {
  const last = selector.split(/[\s>+~,]+/).filter(Boolean).pop() || selector
  const noPseudo = last.replace(/::?[a-zA-Z-]+(\([^)]*\))?/g, '')
  const cls = noPseudo.match(/\.[A-Za-z0-9_-]+/g)
  if (cls) return '.' + cls[0].slice(1).replace(/--.*$/, '')
  return noPseudo.replace(/[.#].*$/, '') || last
}

/** 取出声明块里出现过的边框颜色 token。 */
function declColors(decls) {
  const out = []
  for (const m of decls.matchAll(/border(?:-[a-z-]+)?\s*:\s*([^;]+)/g)) {
    for (const t of m[1].split(/\s+/)) {
      if (/^(transparent|none|initial|unset|inherit|currentcolor)$/i.test(t)) { out.push(t.toLowerCase()); continue }
      if (/^(var\(--[^)]*\)|#[0-9a-fA-F]{3,8}|rgba?\(|hsla?\(|color-mix\()/i.test(t)) out.push(t)
    }
  }
  return out
}

/**
 * 同一条规则里的圆角。
 *   'square' = 没有圆角，或圆角明确为 0（方盒子 ⇒ stripe 规则不适用）
 *   'rounded'= 圆角是非零字面量
 *   'unknown'= **声明了**圆角但读不出（var() / mixin / 插值）⇒ 按有圆角算
 *              （参考实现原文：「Wherever it cannot see the radius … the corners
 *                are unknown and the finding stays」—— 判红是它的失败安全侧）
 *
 * ★ 第一版把「**根本没声明圆角**」也算成 unknown ⇒ 17 处「方盒子上的 2px 分隔线」
 *   被整片判红。参考实现对这两种「读不出」的处置**正好相反**：
 *     - 作用域里**没有**圆角声明 ⇒ 「the initial square box and stays silent」；
 *     - 有圆角声明但读不出 ⇒ 保留 finding。
 *   ⇒ 必须把「没声明」与「读不出」分开。
 */
function readRadius(decls) {
  const m = /border-radius\s*:\s*([^;]+)/.exec(decls)
  if (!m) return 'square'
  const nums = (m[1].match(/\d+(?:\.\d+)?/g) || []).map(Number)
  if (nums.length === 0) return 'unknown'
  return nums.every((n) => n === 0) ? 'square' : 'rounded'
}

/** 取声明值里的第一个 px 数值。 */
function firstPx(value) {
  const m = /(\d+(?:\.\d+)?)px/.exec(value)
  return m ? parseFloat(m[1]) : null
}

/**
 * 伪元素是不是「画出来的侧边条」：
 *   绝对定位 + 通高（height:100% 或 top/bottom 同时为 0）+ 宽度 ≤4px + 有背景色。
 */
function isStripePseudo(decls) {
  if (!/position\s*:\s*(absolute|fixed)/.test(decls)) return false
  const tall = /height\s*:\s*100%/.test(decls) ||
    (/top\s*:\s*0[;\s]/.test(decls) && /bottom\s*:\s*0[;\s]/.test(decls))
  if (!tall) return false
  const w = firstPx((/width\s*:\s*([^;]+)/.exec(decls) || [])[1] || '')
  if (w === null || w > 4) return false
  const bg = (/background\s*:\s*([^;]+)/.exec(decls) || [])[1] || ''
  if (!bg || /none|transparent/i.test(bg)) return false
  return true
}

/**
 * 把样式表切成「规则块」。
 *   只取内层不含花括号的块（`@media { .x { … } }` 里拿到的是 `.x { … }`）。
 *   选择器取 `}` 之后那一段（丢掉 @media 前缀）。
 */
export function cssBlocks(src) {
  const out = []
  for (const m of src.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    out.push({
      selector: m[1].split('}').pop().trim(),
      decls: m[2],
      index: m.index + m[1].length + 1,
    })
  }
  return out
}

/** B. 界面文案里的 text-transform: uppercase。 */
export function checkUppercase(file, src) {
  const problems = []
  const rel = relPosix(rootOf(file), file)
  if (UPPERCASE_DATA_VALUES.some((g) => rel.includes(g))) return problems
  const re = /text-transform\s*:\s*uppercase\s*;/g
  for (const m of src.matchAll(re)) {
    problems.push({
      file, line: lineOf(src, m.index),
      msg: 'text-transform: uppercase —— 中文没有大小写，对中文是空操作；界面文案不要全大写',
    })
  }
  return problems
}

/** C. 渐变文字。 */
export function checkGradientText(file, src) {
  const problems = []
  // ★ 只认 background-clip: text 这一个签名。
  //   第一版写的是 `background\s*:\s*[^;]*?text\b` —— `[^;]*?` 会跨过 `>` 与 `}`
  //   一路吃到几百字符外的某个 "text" 字样，把
  //   `:style="{ background: getRiskLevelColor(...) }"` 这种普通内联背景判成渐变文字。
  //   ⇒ 判据越宽越假；**只匹配确切的签名**。
  const re = /(-webkit-)?background-clip\s*:\s*text\b/g
  for (const m of src.matchAll(re)) {
    problems.push({
      file, line: lineOf(src, m.index),
      msg: '渐变文字（background-clip: text）—— craft-floor 硬禁；强调靠字重与字号',
    })
  }
  return problems
}

/** D. 硬偏移阴影（零模糊的方块投影）。 */
export function checkHardShadow(file, src) {
  const problems = []
  const re = /box-shadow\s*:\s*(\d+)px\s+(\d+)px\s+0\b/g
  for (const m of src.matchAll(re)) {
    problems.push({
      file, line: lineOf(src, m.index),
      msg: `硬偏移阴影 box-shadow: ${m[1]}px ${m[2]}px 0 —— 那是 neobrutalism 的戏服，不是深度系统`,
    })
  }
  return problems
}

/** E. 标题上方的 eyebrow / kicker 元素。 */
export function checkEyebrow(file, src) {
  const problems = []
  // 形如  <p class="eyebrow">…</p>  /  <span class="...__eyebrow">…</span>
  // 紧跟在 <h1>/<h2>/<h3> 之前的，就是「标题上方的小标签」。
  const re = /<\s*(p|span|div)[^>]*class\s*=\s*["'][^"']*\b(eyebrow|kicker)\b[^"']*["'][^>]*>/g
  for (const m of src.matchAll(re)) {
    problems.push({
      file, line: lineOf(src, m.index),
      msg: `标题上方的 ${m[2]} 元素（class 含 "${m[2]}"）—— craft-floor 硬禁；标题自己扛重量`,
    })
  }
  return problems
}

/** F. emoji / Unicode 字形当图标。 */
export function checkEmojiIcons(file, src) {
  const problems = []
  const rel = relPosix(rootOf(file), file)
  if (EMOJI_ALLOWED.some((g) => rel.includes(g))) return problems
  // 只看模板里的文本节点（CSS/注释里的不算，已被 stripComments 剥掉）。
  // ★ 行号必须用**模板块在 src 里的偏移**来算：早期版本拿 tmpl 的 index
  //   直接去 src 里数行 ⇒ 行号指到脚本区去了，报告完全指错位置。
  const tStart = src.indexOf('<template>')
  const tEnd = src.indexOf('</template>', tStart)
  const region = tStart >= 0 && tEnd > tStart ? src.slice(tStart, tEnd) : src
  const offset = tStart >= 0 ? tStart : 0

  // ★ 插值与属性值里的字形**不是模板的文本节点**：
  //   · `{{ rd.success ? '✅' : '❌' }}` —— 那是 JS 表达式；
  //   · `:class="{ error: msg.startsWith('❌') }"` —— 那是属性值，
  //     而且往往与脚本区的字符串构成**数据协议**
  //     （`isError ? '❌ ' : '✓ '` 与 `startsWith('❌')` 是一对，
  //       改一边就断另一边）。
  //   ⇒ 这两处都不判红。
  const interpRanges = rangesOf(region, /\{\{[\s\S]*?\}\}/g)
  const tagRanges = rangesOf(region, /<[A-Za-z][^<>]*>/g)
  const inRanges = (pos) =>
    inRange(pos, interpRanges) || inRange(pos, tagRanges)

  for (const m of region.matchAll(EMOJI_RE)) {
    if (inRanges(m.index)) continue
    problems.push({
      file, line: lineOf(src, offset + m.index),
      msg: `emoji 字形「${m[0]}」出现在模板里 —— craft-floor 硬禁「Unicode 字形/emoji 顶替图标系统」；用图标库或手写 SVG`,
    })
  }
  for (const m of region.matchAll(GLYPH_ICON_RE)) {
    if (inRanges(m.index)) continue
    problems.push({
      file, line: lineOf(src, offset + m.index),
      msg: `功能图标字形「${m[0]}」出现在模板里 —— 扮演图标角色的字形该用图标库或手写 SVG（可用：${GLYPH_ICON_SET}）`,
    })
  }
  for (const m of region.matchAll(DINGBAT_ICON_RE)) {
    if (inRanges(m.index)) continue
    if (!isIconSlot(region, m.index, m[0], tagRanges)) continue
    problems.push({
      file, line: lineOf(src, offset + m.index),
      msg: `dingbat 字形「${m[0]}」占据了图标位 —— craft-floor 硬禁「Unicode 字形顶替图标系统」；用图标库（可用：${DINGBAT_ICON_SET}）`,
    })
  }
  return problems
}

/** 匹配区间 → [start, end, 文本] 列表。 */
function rangesOf(text, re) {
  const out = []
  for (const m of text.matchAll(re)) out.push([m.index, m.index + m[0].length, m[0]])
  return out
}

function inRange(pos, ranges) {
  for (const [a, b] of ranges) if (a <= pos && pos < b) return true
  return false
}

/**
 * 字形是不是处在一个「图标位」上（dingbat 专用）。
 *   ① 所在元素的 class 含 icon / caret / close
 *   ② 或它是 <button> 且字形是唯一内容
 *   ③ 或它有 aria-hidden 且字形是唯一内容
 */
function isIconSlot(region, pos, glyph, tagRanges) {
  let best = null
  for (const [a, b, text] of tagRanges) {
    if (b <= pos) best = { end: b, text }
    else break
  }
  if (!best) return false
  const m = /^<([A-Za-z][\w-]*)([\s\S]*)$/.exec(best.text)
  if (!m) return false
  const name = m[1]
  const attrs = m[2]
  const cm = /class\s*=\s*(["'])(.*?)\1/s.exec(attrs)
  if (cm && ICON_SLOT_CLASS_RE.test(cm[2])) return true
  const nxt = region.indexOf('<', best.end)
  const content = (nxt > best.end ? region.slice(best.end, nxt) : '').trim()
  const sole = content === glyph
  if (name === 'button' && sole) return true
  if (/aria-hidden/.test(attrs) && sole) return true
  return false
}

/**
 * G. 01/02 装饰序号。
 *
 * ★ 关键区分：craft-floor 禁的是**章节序号**（01 / 02 / 03 当装饰），
 *   不是**时间/日期补零**（`String(d.getHours()).padStart(2, '0')` 是把小时
 *   补成两位，那是正确写法）。
 *   第一版只认 `padStart(2, '0')` 这个语法，把 18 处时间格式化全判红了 ——
 *   **判据过宽 = 假红**。
 *   ⇒ 现在只判「被补零的是序号」：实参来自 index/i/count/step 这类序列计数，
 *     或者 `String(<x> + 1)` 这种「第几项」的形式。
 */
export function checkDecorativeNumbers(file, src) {
  const problems = []
  const re = /padStart\s*\(\s*2\s*,\s*['"]0['"]\s*\)/g
  // 序号语境：实参是 index/idx/i/count/step/no/num + 1，或 index/idx/i 本身
  const ORDINAL = /String\s*\(\s*(?:[A-Za-z_$][\w$]*\s*\+\s*1|(?:index|idx|i|count|step|no|num|order|seq)\b[^)]*)\s*\)/
  // 时间/日期语境：Date 取值器 —— 明确排除
  const DATETIME = /get(Hours|Minutes|Seconds|Month|Date|Day|FullYear|Year)\s*\(/
  for (const m of src.matchAll(re)) {
    // 往前看这一行的上文，判断实参是不是日期取值
    const lineStart = src.lastIndexOf('\n', m.index) + 1
    const line = src.slice(lineStart, src.indexOf('\n', m.index))
    if (DATETIME.test(line)) continue
    // 往前 120 字符里找实参
    const ctx = src.slice(Math.max(0, m.index - 120), m.index)
    if (!ORDINAL.test(ctx)) continue
    problems.push({
      file, line: lineOf(src, m.index),
      msg: "padStart(2, '0') 生成的 01/02 序号 —— craft-floor：序号只在序列本身携带信息时才用",
    })
  }
  return problems
}

const CHECKS = [
  ['彩色侧边条', checkSideStripes],
  ['界面文案 uppercase', checkUppercase],
  ['渐变文字', checkGradientText],
  ['硬偏移阴影', checkHardShadow],
  ['标题上方的 eyebrow/kicker', checkEyebrow],
  ['emoji 当图标', checkEmojiIcons],
  ['01/02 装饰序号', checkDecorativeNumbers],
]

// ─────────────────────────────────────────────────────────────
// 工具
// ─────────────────────────────────────────────────────────────

const ROOT_MAP = new Map()
function rootOf(file) {
  for (const { dir } of ROOTS) {
    if (file.startsWith(dir)) return dir
  }
  return WEB
}
function lineOf(src, idx) {
  return src.slice(0, idx).split('\n').length
}

// ─────────────────────────────────────────────────────────────
// 主流程
// ─────────────────────────────────────────────────────────────

export function audit(files) {
  const findings = []
  for (const f of files) {
    const raw = readFileSync(f, 'utf8')
    const src = stripComments(raw)
    for (const [, fn] of CHECKS) findings.push(...fn(f, src))
  }
  return findings
}

function collectFiles() {
  const out = []
  for (const { dir } of ROOTS) walk(dir, out)
  return out
}

// ─────────────────────────────────────────────────────────────
// 自证：判据必须有牙
// ─────────────────────────────────────────────────────────────

const FIXTURES = [
  {
    name: 'A 3px 侧边条 + 圆角 → 必须判红',
    expect: 'red',
    bad: '.card { border-left: 3px solid var(--kx-danger); border-radius: 12px; }',
    good: '.card { border: 1px solid var(--kx-danger); border-radius: 12px; }',
    fn: checkSideStripes,
  },
  {
    name: 'A 长写 border-left-width + 圆角 → 必须判红（第一版只认简写，漏了这一类）',
    expect: 'red',
    bad: '.card { border: 1px solid var(--kx-danger); border-left-width: 3px; border-radius: 8px; }',
    good: '.card { border: 1px solid var(--kx-danger); border-radius: 8px; }',
    fn: checkSideStripes,
  },
  {
    name: 'A 颜色写在 modifier 规则里 → 必须判红（最常见的漏标形态）',
    expect: 'red',
    bad: '.row { border: 1px solid transparent; border-left-width: 3px; border-radius: 7px; }\n.row.is-ok { border-color: var(--kx-success); }',
    good: '.row { border: 1px solid transparent; border-radius: 7px; }\n.row.is-ok { border-color: var(--kx-success); }',
    fn: checkSideStripes,
  },
  {
    name: 'A 顶边粗边 + 圆角 → 必须判红',
    expect: 'red',
    bad: '.node { border: 1px solid var(--border); border-top: 3px solid var(--muted); border-radius: 7px; }',
    good: '.node { border: 1px solid var(--border); border-radius: 7px; }',
    fn: checkSideStripes,
  },
  {
    name: 'A 伪元素画的窄色条 → 必须判红',
    expect: 'red',
    bad: '.card::before { content: ""; position: absolute; top: 0; left: 0; width: 3px; height: 100%; background: var(--muted); }',
    good: '.card { border: 1px solid var(--border); border-radius: 10px; }',
    fn: checkSideStripes,
  },
  {
    name: 'A 1px 侧边条 → 必须判绿（craft-floor 明确允许 1px）',
    expect: 'green',
    bad: '.card { border-left: 1px solid var(--kx-danger); border-radius: 12px; }',
    good: '.card { border: 1px solid var(--kx-danger); border-radius: 12px; }',
    fn: checkSideStripes,
  },
  {
    name: 'A 方盒子上的 3px 规则线（无圆角）→ 必须判绿（那是分隔线，不是卡片色条）',
    expect: 'green',
    bad: '.hr { border-left: 3px solid var(--kx-border); }',
    good: '.hr { border-left: 1px solid var(--kx-border); }',
    fn: checkSideStripes,
  },
  {
    name: 'A 树形引导线（2px 且同元素无圆角）→ 必须判绿',
    expect: 'green',
    bad: '.tree { margin-left: 14px; padding-left: 18px; border-left: 2px solid var(--kx-border); }',
    good: '.tree { margin-left: 14px; padding-left: 18px; }',
    fn: checkSideStripes,
  },
  {
    name: 'A 透明占位侧边条 → 必须判绿（布局占位，不是状态色条）',
    expect: 'green',
    bad: '.x { border: 1px solid transparent; border-left-width: 3px; border-radius: 8px; }',
    good: '.x { border: 1px solid transparent; border-radius: 8px; }',
    fn: checkSideStripes,
  },
  {
    name: 'A 标签页下划线（2px 透明占位 + active 改色，无圆角）→ 必须判绿',
    expect: 'green',
    bad: '.tab-btn { border: none; background: transparent; border-bottom: 2px solid transparent; }\n.tab-btn.active { border-bottom-color: var(--primary); }\n.tab-btn.small { border: 1px solid var(--border); border-radius: 6px; }',
    good: '.tab-btn { border: none; background: transparent; border-bottom: 2px solid transparent; }',
    fn: checkSideStripes,
  },
  {
    name: 'A 小圆点（绝对定位但不通高）→ 必须判绿（不是侧边条）',
    expect: 'green',
    bad: '.dot { position: absolute; top: 4px; left: 4px; width: 6px; height: 6px; border-radius: 50%; background: var(--success); }',
    good: '.dot { position: absolute; top: 4px; left: 4px; width: 6px; height: 6px; border-radius: 50%; background: var(--success); }',
    fn: checkSideStripes,
  },
  {
    name: 'B uppercase → 必须判红',
    expect: 'red',
    bad: '.x { text-transform: uppercase; }',
    good: '.x { letter-spacing: 0.01em; }',
    fn: checkUppercase,
  },
  {
    name: 'C 渐变文字 → 必须判红',
    expect: 'red',
    bad: '.x { background-clip: text; }',
    good: '.x { font-weight: 700; }',
    fn: checkGradientText,
  },
  {
    name: 'C 普通内联 background → 必须判绿（不能因为远处有个 text 就误报）',
    expect: 'green',
    bad: '<span :style="{ background: getRiskLevelColor(x) + \'22\', color: getRiskLevelColor(x) }">text-align</span>',
    good: '<span :style="{ background: getRiskLevelColor(x) }">ok</span>',
    fn: checkGradientText,
  },
  {
    name: 'D 硬偏移阴影 → 必须判红',
    expect: 'red',
    bad: '.x { box-shadow: 4px 4px 0 #000; }',
    good: '.x { box-shadow: 0 8px 24px rgba(0,0,0,.06); }',
    fn: checkHardShadow,
  },
  {
    name: 'E eyebrow 元素 → 必须判红',
    expect: 'red',
    bad: '<p class="eyebrow">CAPABILITIES</p>',
    good: '<h2>核心能力</h2>',
    fn: checkEyebrow,
  },
  {
    name: 'F 真 emoji → 必须判红',
    expect: 'red',
    bad: '<span class="ico">📥</span>',
    good: '<span class="ico"><Download /></span>',
    fn: checkEmojiIcons,
  },
  {
    name: 'F 功能图标字形（☰ 拖拽手柄）→ 必须判红',
    expect: 'red',
    bad: '<div class="rule-drag-handle">☰</div>',
    good: '<div class="rule-drag-handle"><Rank /></div>',
    fn: checkEmojiIcons,
  },
  {
    name: 'F 状态文字标记（✓/✗）→ 必须判绿（是文字标记，不是图标系统）',
    expect: 'green',
    bad: '<button>{{ ok ? \'✓\' : \'✗\' }}</button>',
    good: '<button>{{ ok ? \'启用\' : \'禁用\' }}</button>',
    fn: checkEmojiIcons,
  },
  {
    name: 'F emoji 在 {{ }} 表达式里 → 必须判绿（那是 JS，不是模板文本节点）',
    expect: 'green',
    bad: '<span>{{ ok ? \'✅\' : \'❌\' }}</span>',
    good: '<span>{{ ok ? \'A\' : \'B\' }}</span>',
    fn: checkEmojiIcons,
  },
  {
    name: 'F emoji 在属性值里 → 必须判绿（与脚本区字符串构成数据协议）',
    expect: 'green',
    bad: '<div :class="{ error: msg.startsWith(\'❌\') }">x</div>',
    good: '<div :class="{ error: msg.startsWith(\'X\') }">x</div>',
    fn: checkEmojiIcons,
  },
  {
    name: 'F dingbat 占据图标位（class 含 icon）→ 必须判红',
    expect: 'red',
    bad: '<span class="alert-icon">⚠</span>',
    good: '<span class="alert-icon"><Warning /></span>',
    fn: checkEmojiIcons,
  },
  {
    name: 'F dingbat 占据图标位（关闭按钮）→ 必须判红',
    expect: 'red',
    bad: '<button class="btn btn-ghost" @click="close">✕</button>',
    good: '<button class="btn btn-ghost" @click="close"><Close /></button>',
    fn: checkEmojiIcons,
  },
  {
    name: 'F dingbat 是句子的一部分 → 必须判绿（与 ✓/✗ 同一口径）',
    expect: 'green',
    bad: '<p class="error">⚠️ 数据聚合视图未就绪（…），本列表不可作为结论。</p>',
    good: '<p class="error">数据聚合视图未就绪（…），本列表不可作为结论。</p>',
    fn: checkEmojiIcons,
  },
  {
    name: 'F dingbat 在带文字的按钮上做前后缀 → 必须判绿',
    expect: 'green',
    bad: '<button class="btn btn-ghost">✕ 关闭</button>',
    good: '<button class="btn btn-ghost">关闭</button>',
    fn: checkEmojiIcons,
  },
  {
    name: 'G 章节序号 01/02 → 必须判红',
    expect: 'red',
    bad: "const n = String(index + 1).padStart(2, '0')",
    good: 'const n = String(index + 1)',
    fn: checkDecorativeNumbers,
  },
  {
    name: 'G 时间补零（小时）→ 必须判绿（不是装饰序号）',
    expect: 'green',
    bad: "const h = String(d.getHours()).padStart(2, '0')",
    good: "const h = String(d.getHours()).padStart(2, '0') + ':00'",
    fn: checkDecorativeNumbers,
  },
]

function selftest() {
  const results = []
  for (const fx of FIXTURES) {
    const badHits = fx.fn('selftest/fixture.vue', fx.bad)
    const goodHits = fx.fn('selftest/fixture.vue', fx.good)
    // red：违规样本必须命中，干净样本必须不命中
    // green：两个样本都不得命中（这条判据不许误报）
    const ok = fx.expect === 'red'
      ? badHits.length > 0 && goodHits.length === 0
      : badHits.length === 0 && goodHits.length === 0
    results.push({
      name: fx.name,
      ok,
      detail: `违规样本命中 ${badHits.length} 条 / 干净样本命中 ${goodHits.length} 条（期望 ${fx.expect === 'red' ? '红/绿' : '绿/绿'}）`,
    })
  }
  return results
}

function main() {
  const argv = process.argv.slice(2)
  if (argv.includes('--selftest')) {
    const results = selftest()
    let failed = 0
    for (const r of results) {
      console.log(`  ${r.ok ? '✓' : '✗'} ${r.name} —— ${r.detail}`)
      if (!r.ok) failed++
    }
    console.log(`\n${failed === 0 ? '✅' : '❌'} 判据自证：${results.length - failed}/${results.length} 通过`)
    process.exit(failed === 0 ? 0 : 1)
  }

  const files = collectFiles()
  const findings = audit(files)

  const byCheck = new Map()
  for (const f of findings) {
    const key = f.msg.split('——')[0].trim()
    if (!byCheck.has(key)) byCheck.set(key, [])
    byCheck.get(key).push(f)
  }

  if (argv.includes('--json')) {
    const outArg = argv[argv.indexOf('--json') + 1]
    const target = outArg && !outArg.startsWith('--') ? outArg : resolve(WEB, 'reports/craft-floor-audit.json')
    mkdirSync(dirname(target), { recursive: true })
    writeFileSync(target, JSON.stringify({ count: findings.length, findings }, null, 2))
    console.log(`JSON → ${target}（${findings.length} 条）`)
  } else {
    console.log('craft-floor 审计 —— impeccable 视觉硬禁项')
    console.log(`扫描 ${files.length} 个文件\n`)
    if (findings.length === 0) {
      console.log('✅ 未发现违规')
    } else {
      for (const [key, list] of byCheck) {
        console.log(`■ ${key}（${list.length} 处）`)
        for (const f of list.slice(0, 40)) {
          console.log(`  ${relative(WEB, f.file)}:${f.line}  ${f.msg}`)
        }
        if (list.length > 40) console.log(`  …… 另有 ${list.length - 40} 处`)
        console.log()
      }
    }
  }

  if (argv.includes('--strict') && findings.length > 0) {
    console.error(`❌ craft-floor 审计发现 ${findings.length} 处违规（--strict）`)
    process.exit(1)
  }
}

if (process.argv[1] && process.argv[1].endsWith('craft-floor-audit.mjs')) main()
