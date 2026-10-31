/**
 * 判据：状态过渡必须以指数减速到达（第 26 增量 · 2026-10-27）
 *
 * ★★★ 本门只判**一个**判据（判据越少越可证伪）：
 *
 *   **界面上的每一次状态变化，都必须以指数减速（exponential ease-out）到达。**
 *
 *   —— 「到位感」不是配色、不是圆角，是**动作怎么收尾**。
 *      默认的 `ease` 是「慢起步 → 匀速 → 慢收尾」，起手那一下是**软的**
 *      ⇒ 看起来像机器在「启动」；指数缓出是「起步即快、末尾指数减速」
 *      ⇒ 看起来像**人一把把它按到位**。
 *
 * ★ 判据来源（impeccable skill/reference/）：
 *   · `animate.md`「Timing and easing」：
 *       "Use natural deceleration such as `cubic-bezier(0.16, 1, 0.3, 1)`
 *        for confident arrivals; do not use bounce or elastic curves by reflex.
 *        Long feedback feels like latency."
 *       "Avoid casually animating layout-driving properties such as `width`,
 *        `height`, `top`, `left`, and margins; use FLIP, transforms, or grid
 *        techniques when appropriate."
 *       "Every web animation needs a `prefers-reduced-motion` path with an
 *        intentional alternative. ... Reduced motion means fewer and gentler
 *        animations, not disabling all motion."
 *       时长表：100–150ms 反馈 / 150–300ms 例行状态变化 / 300–500ms 浮层与视图过渡
 *               / 500–800ms 刻意编排的焦点入场
 *   · `craft-floor.md` Motion 段：
 *       "one authored moment, not scattered effects and not one identical
 *        entrance on every section. Exponential ease-out from an
 *        already-visible default. Reach past transform and opacity: blur,
 *        backdrop-filter, clip-path, mask, and shadow belong to the palette
 *        when they stay smooth."
 *
 * ★ 口径事实源：
 *   · 缓动的**唯一写法** = `var(--kx-motion-ease-out)`（令牌）
 *     或它的字面量 `cubic-bezier(0.16, 1, 0.3, 1)`（两者同一条曲线，
 *     ★ 照抄 `spacingScale.ts` 的「令牌写法 / 在阶上」两计一合法的范式）；
 *     唯一定义处 = `web/src/style.css` 的 `--kx-motion-ease-out`。
 *   · CSS 的 `ease-out` 关键字**不是**指数缓出：它是 `cubic-bezier(0, 0, 0.58, 1)`
 *     ⇒ y1 = 0 ⇒ **起步速度为零** ⇒ 「慢起手」而非「快起步」。
 *     ⇒ 本门判红（impeccable 明确点名的是 `cubic-bezier(0.16, 1, 0.3, 1)`）。
 *   · CSS 的 `ease` = `cubic-bezier(0.25, 0.1, 0.25, 1)` ⇒ y1 = 0.1 ⇒ 同样慢起手。
 *
 * ★ 七条判据（本门一次判完，缺一不可）：
 *
 *   M1 **减速曲线** —— 状态过渡（`transition`）与**一次性**入场动画（`animation`
 *      且非 `infinite`）的缓动必须是那条指数曲线。
 *      ★ 无限循环动画**不判**：它是呼吸节律（pulse / shimmer / spin），
 *        不是「到达」⇒ 没有「减速」可言。判它 = 把「呼吸」改成「到位」。
 *   M2 **具名属性** —— `transition` 不得过渡 `all`。
 *      ★ `all` 把 `width` / `height` / `top` / `left` 这类**布局驱动属性**
 *        一并拖进来 ⇒ 与 animate.md「Avoid casually animating layout-driving
 *        properties」直接冲突；且 `all` 保证包含**不 smooth** 的属性
 *        ⇒ 与 craft-floor「when they stay smooth」冲突。
 *   M3 **布局驱动属性** —— `transition` 不得过渡 `width` / `height` / `top` /
 *      `left` / `right` / `bottom` / `margin*` / `padding*`，
 *      **除非**在裁决簿登记（数据条把宽度当作**数据的编码**，
 *      那是唯一的诚实表达 ⇒ 登记并写明理由）。
 *   M4 **空间移动的 reduced-motion 路径** —— 文件内若有 `transition` 过渡了
 *      `transform`，该文件必须存在 `@media (prefers-reduced-motion: reduce)`
 *      块（animate.md:77「Every web animation needs a prefers-reduced-motion
 *      path」）。
 *      ★ 只判**过渡 transform**：匀速旋转指示器（`animation: rotate ... linear
 *        infinite`）是**状态反馈**，reduced-motion 下**必须保留**
 *        （animate.md:77「feedback that confirms an action should remain legible」）。
 *      ★ 只判**空间移动**：纯 `opacity` / `color` 的反馈不需要 reduced-motion
 *        路径（那正是「保留有意义的状态变化」）。
 *   M5 **时长上限** —— `transition` ≤ 500ms；一次性 `animation` ≤ 800ms。
 *      ★ 上限来自 animate.md 的时长表最上界；「Long feedback feels like latency」。
 *   M6 **入场登记** —— 一次性入场动画（引用的 keyframes 属于**入场类**
 *      = 起始帧含 `opacity: 0`）必须在裁决簿登记。
 *      ★ craft-floor「one authored moment, **not scattered effects**」
 *        ⇒ 入场动效必须**被 consciously 登记**，不能随手加一个淡入。
 *   M7 **无限循环的匀速** —— 无限循环 animation 若用 `linear`，
 *      必须是**匀速运动**（keyframes 只含 `rotate` / `translate` 一类
 *      匀速变换，不含 `opacity` 呼吸或 `scale` 脉动）⇒ 否则匀速会把
 *      「呼吸」拍成「机器」。
 *
 * ★ 不判的（单独计数，不算违规）：
 *   · `transition: none` / `animation: none` —— 没有动效。
 *   · `transition-delay` —— 延迟不是缓动。
 *   · `@media (prefers-reduced-motion: reduce)` 块内的一切 —— 那正是
 *     「关掉动效」的地方。
 *   · **媒体查询里的字号 / 间距降档**（第 24 增量的口径）—— 那是响应式，
 *     方向与基准值相反，不适用本门的动效判据。
 *   · `web-mobile/` —— 移动端是 Material 设计体系（缓动族是
 *     `cubic-bezier(0.2, 0, 0, 1)`），套用桌面曲线会破坏它的手感
 *     ⇒ 移动端需要**独立判据**，不是放宽桌面判据。
 *
 * ★ 本文件是**判据**（纯函数 + 文件扫描），门在 `web/scripts/design-motion-guard.mjs`。
 * ★ CSS 解析口径**复用** `spacingScale.ts` 的 `maskCssComments` /
 *   `styleBlocksOf` / `listStyleFiles`（★ 同一口径只能有一份实现）。
 */

import { readFileSync } from 'node:fs'
import { relative } from 'node:path'
import {
  maskCssComments,
  styleBlocksOf,
  listStyleFiles,
} from './spacingScale.ts'

/**
 * ★ 把 CSS 口径工具**转出**给门与迁移脚本用 ——
 *   「同一口径只能有一份实现」：谁需要解析 CSS，都从这里拿同一套。
 */
export { maskCssComments, styleBlocksOf, listStyleFiles }

// ───────────────────────── 常量 ─────────────────────────

/** 指数减速曲线的唯一字面量（impeccable animate.md:61 点名的那个值） */
export const MOTION_EASE_LITERAL = 'cubic-bezier(0.16, 1, 0.3, 1)'
/** 指数减速曲线的唯一令牌写法 */
export const MOTION_EASE_TOKEN = 'var(--kx-motion-ease-out)'

/** transition 时长上限（animate.md 时长表：浮层 / 视图过渡 300–500ms） */
export const TRANSITION_MAX_MS = 500
/** 一次性入场动画时长上限（animate.md 时长表：焦点入场 500–800ms） */
export const ENTRANCE_MAX_MS = 800

/** 布局驱动属性（animate.md:71 点名的五个 + margin / padding） */
export const LAYOUT_DRIVEN_PROPS = new Set([
  'width', 'height', 'top', 'left', 'right', 'bottom',
  'margin', 'margin-top', 'margin-right', 'margin-bottom', 'margin-left',
  'margin-block', 'margin-inline', 'margin-block-start', 'margin-block-end',
  'margin-inline-start', 'margin-inline-end',
  'padding', 'padding-top', 'padding-right', 'padding-bottom', 'padding-left',
  'padding-block', 'padding-inline', 'padding-block-start', 'padding-block-end',
  'padding-inline-start', 'padding-inline-end',
])

/** 不可过渡 / 不该过渡的属性（`cursor` 不可动画；`font-weight` 过渡会抖） */
export const NOT_TRANSITIONABLE = new Set(['cursor', 'pointer-events', 'user-select', 'font-weight'])

// ───────────────────────── CSS 值切分 ─────────────────────────

/** 按顶层逗号切分（括号内逗号不切） */
export function splitTopLevel(v: string, sep = ','): string[] {
  const parts: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of v) {
    if (ch === '(') depth++
    else if (ch === ')') depth--
    if (ch === sep && depth === 0) {
      parts.push(cur)
      cur = ''
    } else cur += ch
  }
  parts.push(cur)
  return parts.map((s) => s.trim()).filter((s) => s.length > 0)
}

/** 按顶层空白切分（括号内空白不切） */
function splitTopSpace(v: string): string[] {
  const parts: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of v) {
    if (ch === '(') depth++
    else if (ch === ')') depth--
    if (/\s/.test(ch) && depth === 0) {
      if (cur) { parts.push(cur); cur = '' }
    } else cur += ch
  }
  if (cur) parts.push(cur)
  return parts
}

const TIME_RE = /^([+-]?\d*\.?\d+)(m?s)$/i

/** 解析时长（毫秒）；不是时长返回 null */
export function parseTimeMs(tok: string): number | null {
  const m = tok.match(TIME_RE)
  if (!m) return null
  const n = parseFloat(m[1])
  return m[2].toLowerCase() === 's' ? n * 1000 : n
}

export type TimingKind =
  | 'canonical'   // 那条指数曲线（令牌写法或字面量）
  | 'token'       // var(--kx-motion-ease-out)
  | 'literal'     // cubic-bezier(0.16, 1, 0.3, 1)
  | 'keyword'     // ease / ease-in / ease-out / ease-in-out / linear
  | 'steps'       // steps(...) / step-start / step-end
  | 'other'       // 其它 cubic-bezier(...)
  | 'none'        // 未声明（浏览器默认 = ease）

export interface TimingInfo {
  kind: TimingKind
  raw: string | null
  /** 是否合规（canonical） */
  ok: boolean
  /** 为什么不合规（人话） */
  reason: string
}

/** 判定一个缓动 token 的形状 */
export function classifyTiming(raw: string | null | undefined): TimingInfo {
  if (raw === null || raw === undefined || raw === '') {
    return {
      kind: 'none',
      raw: null,
      ok: false,
      reason:
        '未声明缓动 ⇒ 浏览器默认 `ease`（cubic-bezier(0.25,0.1,0.25,1)，慢起手）' +
        '⇒ 状态变化没有「到位感」',
    }
  }
  const t = raw.trim()
  if (t === MOTION_EASE_TOKEN) {
    return { kind: 'token', raw: t, ok: true, reason: '令牌写法：指数减速' }
  }
  const norm = t.replace(/\s+/g, ' ').toLowerCase()
  if (norm === MOTION_EASE_LITERAL.replace(/\s+/g, ' ').toLowerCase()) {
    return { kind: 'literal', raw: t, ok: true, reason: '字面量写法：指数减速' }
  }
  if (/^(steps\(|step-start$|step-end$)/i.test(t)) {
    return { kind: 'steps', raw: t, ok: false, reason: `阶梯缓动 \`${t}\` 不是减速曲线` }
  }
  if (/^cubic-bezier\(/i.test(t)) {
    return {
      kind: 'other',
      raw: t,
      ok: false,
      reason:
        `\`${t}\` 不是本仓的减速曲线（唯一定义：${MOTION_EASE_TOKEN} / ${MOTION_EASE_LITERAL}）`,
    }
  }
  const kw = t.toLowerCase()
  const why: Record<string, string> = {
    ease:
      '`ease` = cubic-bezier(0.25,0.1,0.25,1) ⇒ y1=0.1 起步是软的 ⇒ 像机器在启动',
    'ease-in':
      '`ease-in` 起步最慢 ⇒ 与「到位感」相反',
    'ease-out':
      '`ease-out` = cubic-bezier(0,0,0.58,1) ⇒ y1=0 起步速度为零 ⇒ 慢起手，不是指数减速',
    'ease-in-out':
      '`ease-in-out` 两端都慢 ⇒ 状态变化被拖成「犹豫」',
    linear:
      '`linear` 匀速 ⇒ 没有减速 ⇒ 到位的动作必须减速',
  }
  if (why[kw]) {
    return { kind: 'keyword', raw: t, ok: false, reason: why[kw] }
  }
  return { kind: 'other', raw: t, ok: false, reason: `无法识别的缓动 \`${t}\`` }
}

// ───────────────────────── 声明解析 ─────────────────────────

export interface TransitionItem {
  /** 被过渡的属性（`all` / `opacity` / …） */
  property: string
  durationMs: number | null
  delayMs: number | null
  /** 原始缓动 token（未声明 ⇒ null） */
  timing: string | null
  /** 该分项的原始文本 */
  raw: string
  /** 该分项在**声明值**里的字符区间（批量改写必须落在区间上） */
  start: number
  end: number
  /** 缓动 token 在**分项内**的区间（-1 = 未声明） */
  timingStart: number
  timingEnd: number
  /** 时长 token 结束处的位置（-1 = 未声明）—— 补缓动时插在它后面 */
  durationEnd: number
}

export interface AnimationItem {
  /** keyframes 名 */
  name: string | null
  durationMs: number | null
  delayMs: number | null
  timing: string | null
  iterationCount: string | null
  raw: string
  start: number
  end: number
  /** 缓动 token 在**分项内**的区间（-1 = 未声明） */
  timingStart: number
  timingEnd: number
  /** 时长 token 结束处的位置（-1 = 未声明） */
  durationEnd: number
}

export interface MotionDecl {
  file: string
  line: number
  /** `transition` / `transition-property` / `animation` / … */
  prop: string
  value: string
  /** 所在选择器 */
  selector: string
  /** 是否在 `@media (prefers-reduced-motion: reduce)` 内 */
  inReducedMotion: boolean
  /** 声明在文件里的字符区间 */
  fileStart: number
  fileEnd: number
  /** **值**在文件里的起始偏移（批量改写必须落在值上，而不是整条声明上） */
  valueOffset: number
  transitionItems: TransitionItem[]
  animationItems: AnimationItem[]
}

/** 解析 transition 简写/长写的每一分项 */
export function parseTransitionItems(value: string, baseOffset = 0): TransitionItem[] {
  const out: TransitionItem[] = []
  let cursor = 0
  for (const part of splitTopLevel(value)) {
    const idx = value.indexOf(part, cursor)
    const start = baseOffset + (idx >= 0 ? idx : 0)
    cursor = (idx >= 0 ? idx : cursor) + part.length
    const toks = splitTopSpace(part)
    const times: number[] = []
    let timing: string | null = null
    let property = ''
    let iteration: string | null = null
    let timingStart = -1
    let timingEnd = -1
    let durationEnd = -1
    let cursorInPart = 0
    for (const tk of toks) {
      const tokStart = part.indexOf(tk, cursorInPart)
      cursorInPart = tokStart + tk.length
      const ms = parseTimeMs(tk)
      if (ms !== null) {
        times.push(ms)
        if (durationEnd < 0) durationEnd = tokStart + tk.length
        continue
      }
      const low = tk.toLowerCase()
      // ★ `!important` 既不是属性也不是缓动 —— 不认它 ⇒ 它会被当成 keyframes 名
      if (low === '!important') continue
      if (
        low === 'ease' || low === 'ease-in' || low === 'ease-out' ||
        low === 'ease-in-out' || low === 'linear' || low === 'step-start' ||
        low === 'step-end' || /^cubic-bezier\(/i.test(tk) || /^steps\(/i.test(tk) ||
        /^var\(--kx-motion-ease-out\)$/i.test(tk)
      ) {
        timing = tk
        timingStart = tokStart
        timingEnd = tokStart + tk.length
        continue
      }
      if (/^infinite$|^\d+$/.test(low)) { iteration = tk; continue }
      if (!property) property = tk
    }
    out.push({
      property: property || 'all',
      durationMs: times.length ? times[0] : null,
      delayMs: times.length > 1 ? times[1] : null,
      timing,
      raw: part,
      start,
      end: start + part.length,
      timingStart,
      timingEnd,
      durationEnd,
    })
  }
  return out
}

/** 解析 animation 简写的每一分项 */
export function parseAnimationItems(value: string, baseOffset = 0): AnimationItem[] {
  const out: AnimationItem[] = []
  let cursor = 0
  for (const part of splitTopLevel(value)) {
    const idx = value.indexOf(part, cursor)
    const start = baseOffset + (idx >= 0 ? idx : 0)
    cursor = (idx >= 0 ? idx : cursor) + part.length
    const toks = splitTopSpace(part)
    const times: number[] = []
    let timing: string | null = null
    let name: string | null = null
    let iteration: string | null = null
    let timingStart = -1
    let timingEnd = -1
    let durationEnd = -1
    let cursorInPart = 0
    for (const tk of toks) {
      const tokStart = part.indexOf(tk, cursorInPart)
      cursorInPart = tokStart + tk.length
      const ms = parseTimeMs(tk)
      if (ms !== null) {
        times.push(ms)
        if (durationEnd < 0) durationEnd = tokStart + tk.length
        continue
      }
      const low = tk.toLowerCase()
      if (
        low === 'ease' || low === 'ease-in' || low === 'ease-out' ||
        low === 'ease-in-out' || low === 'linear' || low === 'step-start' ||
        low === 'step-end' || /^cubic-bezier\(/i.test(tk) || /^steps\(/i.test(tk) ||
        /^var\(--kx-motion-ease-out\)$/i.test(tk)
      ) {
        timing = tk
        timingStart = tokStart
        timingEnd = tokStart + tk.length
        continue
      }
      if (low === 'infinite' || /^\d+$/.test(low)) { iteration = tk; continue }
      if (low === 'normal' || low === 'reverse' || low === 'alternate' || low === 'alternate-reverse') continue
      if (low === 'none') continue
      // ★ `!important` 不是 keyframes 名
      if (low === '!important') continue
      if (!name) name = tk
    }
    out.push({
      name,
      durationMs: times.length ? times[0] : null,
      delayMs: times.length > 1 ? times[1] : null,
      timing,
      iterationCount: iteration,
      raw: part,
      start,
      end: start + part.length,
      timingStart,
      timingEnd,
      durationEnd,
    })
  }
  return out
}

// ───────────────────────── CSS 扫描 ─────────────────────────

interface RawRule {
  selector: string
  parentSelector: string
  decls: Array<{ prop: string; value: string; off: number; valueOff: number }>
  inReducedMotion: boolean
}

const REDUCED_MOTION_RE = /prefers-reduced-motion\s*:\s*reduce/

/**
 * 扫描一段 CSS 文本，返回全部规则（含**嵌套解析**）。
 * ★ 嵌套（`&:hover { }`）必须解析：仓内有 4 个文件在用，
 *   不解析就是**整块失明**（`PromptInjectionSettingsView.vue` 正好是
 *   一个 `transition: all` 站点 ⇒ 不解析就会把它判成「没有状态变体」）。
 */
export function parseCssRules(css: string): RawRule[] {
  const rules: RawRule[] = []
  const stack: Array<{ selector: string; decls: Array<{ prop: string; value: string; off: number; valueOff: number }>; reduced: boolean }> = []
  let declStart = 0

  const pushDecl = (
    text: string,
    start: number,
    into: Array<{ prop: string; value: string; off: number; valueOff: number }>,
  ) => {
    const t = text.trim()
    const m = t.match(/^([a-z-]+)\s*:\s*([\s\S]+)$/i)
    if (!m) return
    const lead = text.length - text.trimStart().length
    // ★ 值在**原始文本**里的起点：属性名 + 冒号 + 空白之后
    const colonIdx = t.indexOf(':')
    // ★ 值在**原始文本**里的起点 = 冒号之后 + 跳过空白（★ 要的是偏移，不是剩余长度）
    const afterColon = colonIdx >= 0 ? t.slice(colonIdx + 1) : ''
    const wsLen = afterColon.length - afterColon.replace(/^\s+/, '').length
    const valueInT = colonIdx >= 0 ? colonIdx + 1 + wsLen : 0
    into.push({
      prop: m[1].toLowerCase(),
      value: m[2].trim(),
      off: start + lead,
      valueOff: start + lead + valueInT,
    })
  }

  for (let i = 0; i < css.length; i++) {
    const ch = css[i]
    if (ch === '{') {
      const header = css.slice(declStart, i).trim()
      const parent = stack.length ? stack[stack.length - 1] : null
      // ★ at-rule 自己的头里若写了 prefers-reduced-motion，标记要向下传递
      const reduced = REDUCED_MOTION_RE.test(header) || (parent ? parent.reduced : false)
      stack.push({ selector: header, decls: [], reduced })
      declStart = i + 1
    } else if (ch === '}') {
      const blk = stack.pop()
      if (!blk) { declStart = i + 1; continue }
      pushDecl(css.slice(declStart, i), declStart, blk.decls)
      declStart = i + 1
      const h = blk.selector
      if (!h || h.startsWith('@')) {
        // at-rule：prefers-reduced-motion 的「继承标记」向下传递
        continue
      }
      const parentSel = stack.length ? stack[stack.length - 1].selector : ''
      rules.push({
        selector: h,
        parentSelector: parentSel,
        decls: blk.decls,
        inReducedMotion: blk.reduced || REDUCED_MOTION_RE.test(h),
      })
    } else if (ch === ';') {
      const top = stack[stack.length - 1]
      if (top) pushDecl(css.slice(declStart, i), declStart, top.decls)
      declStart = i + 1
    }
  }
  return rules
}

/** 把 `&:hover` 这类嵌套头解析成完整选择器（`&` 替换成父选择器） */
export function resolveSelector(selector: string, parentSelector: string): string {
  const s = selector.trim()
  if (!s.includes('&')) return s
  const parent = parentSelector.trim() || '&'
  return s.replace(/&/g, parent)
}

export interface ScanInput {
  /** 相对路径（用于输出） */
  file: string
  /** 文件全文 */
  text: string
}

/** 扫描一个文件，返回它的全部 motion 声明 */
export function scanMotionInFile(input: ScanInput): MotionDecl[] {
  const { file, text } = input
  const starts: number[] = [0]
  for (let i = 0; i < text.length; i++) if (text[i] === '\n') starts.push(i + 1)
  const lineOf = (off: number): number => {
    let lo = 0, hi = starts.length - 1
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1
      if (starts[mid] <= off) lo = mid
      else hi = mid - 1
    }
    return lo + 1
  }

  const blocks = file.endsWith('.vue')
    ? styleBlocksOf(text)
    : [{ text, baseLine: 0, start: 0 }]

  const out: MotionDecl[] = []
  for (const blk of blocks) {
    const masked = maskCssComments(blk.text)
    const rules = parseCssRules(masked)
    for (const rule of rules) {
      const selector = resolveSelector(rule.selector, rule.parentSelector)
      for (const d of rule.decls) {
        if (!/^(transition|animation)/.test(d.prop)) continue
        if (d.prop === 'transition-delay' || d.prop === 'animation-fill-mode') continue
        const fileStart = blk.start + d.off
        const decl: MotionDecl = {
          file,
          line: lineOf(fileStart),
          prop: d.prop,
          value: d.value,
          selector,
          inReducedMotion: rule.inReducedMotion,
          fileStart,
          fileEnd: fileStart + d.value.length,
          valueOffset: blk.start + d.valueOff,
          transitionItems: [],
          animationItems: [],
        }
        if (d.prop === 'transition') {
          decl.transitionItems = parseTransitionItems(d.value)
        } else if (d.prop === 'transition-property') {
          decl.transitionItems = parseTransitionItems(d.value).map((it) => ({
            ...it,
            property: it.raw,
          }))
        } else if (d.prop === 'transition-timing-function') {
          decl.transitionItems = [{
            property: 'all', durationMs: null, delayMs: null,
            timing: d.value, raw: d.value, start: 0, end: d.value.length,
            timingStart: 0, timingEnd: d.value.length, durationEnd: -1,
          }]
        } else if (d.prop === 'animation') {
          decl.animationItems = parseAnimationItems(d.value)
        } else if (d.prop === 'animation-timing-function') {
          decl.animationItems = [{
            name: null, durationMs: null, delayMs: null,
            timing: d.value, iterationCount: null, raw: d.value, start: 0, end: d.value.length,
            timingStart: 0, timingEnd: d.value.length, durationEnd: -1,
          }]
        } else if (d.prop === 'animation-duration' || d.prop === 'animation-delay') {
          // 长写：只取时长，不参与缓动判据
          continue
        }
        out.push(decl)
      }
    }
  }
  return out
}

/** 列出仓库里所有要扫的样式文件 */
export function listMotionFiles(rootDir: string): string[] {
  return listStyleFiles(rootDir)
}

/** 读文件并扫描 */
export function collectMotionDecls(rootDir: string): MotionDecl[] {
  const out: MotionDecl[] = []
  for (const abs of listMotionFiles(rootDir)) {
    // ★ Windows 上 listStyleFiles 产出反斜杠绝对路径，`startsWith(rootDir + '/')`
    //   剥不掉前缀 ⇒ rel 变成整条绝对路径 ⇒ gate 里 join 出
    //   `src\C:\...\src\App.vue` 直接 ENOENT（main 上 Windows 本地跑不了本门）。
    //   与 elevation/aiSmells 等其余 6 处同款写法对齐：relative + 统一正斜杠。
    const rel = relative(rootDir, abs).replace(/\\/g, '/')
    out.push(...scanMotionInFile({ file: rel, text: readFileSync(abs, 'utf8') }))
  }
  return out
}

// ───────────────────────── 判据 ─────────────────────────

export type MotionRule = 'M1' | 'M2' | 'M3' | 'M4' | 'M5' | 'M6' | 'M7'

export interface MotionFinding {
  rule: MotionRule
  file: string
  line: number
  selector: string
  prop: string
  value: string
  /** 人话理由 */
  reason: string
  /** 修法提示 */
  fix: string
}

export interface MotionDecisions {
  /** M3 的登记豁免：数据条把宽度当数据编码 */
  layoutDriven?: Array<{ file: string; selector: string; property: string; reason: string }>
  /** M6 的入场登记册 */
  entrances?: Array<{ name: string; file: string; reason: string }>
}

export interface MotionScanResult {
  findings: MotionFinding[]
  facts: Record<string, number | string | unknown[]>
  decls: MotionDecl[]
}

const REDUCED_ONLY_RE = /prefers-reduced-motion\s*:\s*reduce/

/** 判据主体 */
export function scanMotion(
  decls: MotionDecl[],
  fileTexts: Map<string, string>,
  decisions: MotionDecisions = {},
): MotionScanResult {
  const findings: MotionFinding[] = []
  const layoutKeys = new Set(
    (decisions.layoutDriven ?? []).map((e) => `${e.file}|${e.selector}|${e.property}`),
  )
  /** ★ 登记项里**真正被命中**的那些（豁免漂移检查的分子） */
  const hitLayoutKeys = new Set<string>()
  const entranceNames = new Set((decisions.entrances ?? []).map((e) => e.name))

  // 收集 keyframes（判定「入场类」）
  const keyframeBodies = new Map<string, string[]>()
  for (const [file, text] of fileTexts) {
    const blocks = file.endsWith('.vue')
      ? styleBlocksOf(text)
      : [{ text, baseLine: 0, start: 0 }]
    for (const blk of blocks) {
      const masked = maskCssComments(blk.text)
      const re = /@keyframes\s+([\w-]+)\s*\{/g
      let m: RegExpExecArray | null
      while ((m = re.exec(masked))) {
        let depth = 0
        let j = m.index + m[0].length - 1
        for (; j < masked.length; j++) {
          if (masked[j] === '{') depth++
          else if (masked[j] === '}') { depth--; if (depth === 0) break }
        }
        const body = masked.slice(m.index + m[0].length, j)
        if (!keyframeBodies.has(m[1])) keyframeBodies.set(m[1], [])
        keyframeBodies.get(m[1])!.push(body)
      }
    }
  }
  /** 入场类 = 起始帧（from / 0%）含 opacity: 0 */
  const isEntranceKeyframe = (name: string): boolean => {
    const bodies = keyframeBodies.get(name)
    if (!bodies) return false
    return bodies.some((b) => {
      const m = b.match(/(?:^|\})\s*(?:from|0%)\s*\{([^}]*)\}/)
      if (!m) return false
      return /opacity\s*:\s*0(?!\d)/.test(m[1])
    })
  }

  // ★ M4 的判定对象是「(文件, 选择器)」对，不是「文件」——
  //   一个文件里可能有多个选择器过渡 transform，reduced-motion 块要**逐个**中和。
  const transformTargets = new Set<string>()
  /** ★ 选择器归一：逗号全串要**逐项**比较（声明侧是全串，reduced 侧是单项） */
  const normParts = (sel: string): string[] =>
    sel.split(',').map((x) => x.trim().replace(/\s+/g, ' ')).filter(Boolean)
  for (const d of decls) {
    if (d.prop !== 'transition') continue
    if (d.inReducedMotion) continue
    for (const it of d.transitionItems) {
      if (it.property !== 'transform') continue
      for (const part of normParts(d.selector)) transformTargets.add(`${d.file}|${part}`)
    }
  }
  // reduced-motion 块里「已经中和」的那些：transition: none / transition-property 不含 transform
  const neutralized = new Set<string>()
  for (const d of decls) {
    if (!d.inReducedMotion) continue
    if (d.prop !== 'transition' && d.prop !== 'transition-property') continue
    let excludesTransform = true
    if (d.prop === 'transition') {
      for (const it of d.transitionItems) {
        if (it.property === 'transform') excludesTransform = false
      }
      if (d.transitionItems.some((it) => it.property === 'none')) excludesTransform = true
    } else {
      for (const it of d.transitionItems) {
        if (it.property === 'transform') excludesTransform = false
      }
    }
    if (!excludesTransform) continue
    for (const part of normParts(d.selector)) {
      neutralized.add(`${d.file}|${part}`)
    }
  }

  let tokenStyle = 0
  let literalStyle = 0
  let defaultEase = 0
  let transitionTotal = 0
  let animationFinite = 0
  let animationInfinite = 0
  let allProps = 0
  let layoutDriven = 0
  let longDurations = 0
  let infiniteNonUniform = 0
  let entranceSites = 0
  let registeredEntrances = 0

  for (const d of decls) {
    if (d.inReducedMotion) continue

    if (d.prop === 'transition' || d.prop === 'transition-property' || d.prop === 'transition-timing-function') {
      transitionTotal++
      for (const it of d.transitionItems) {
        // ── M2：不得过渡 all
        if (it.property === 'all') {
          allProps++
          findings.push({
            rule: 'M2',
            file: d.file,
            line: d.line,
            selector: d.selector,
            prop: d.prop,
            value: d.value,
            reason:
              '`transition: all` 把 width / height / top / left 这类布局驱动属性一并拖进来' +
              '（animate.md:71「Avoid casually animating layout-driving properties」）' +
              '⇒ 且 `all` 保证包含**不 smooth** 的属性（craft-floor「when they stay smooth」）',
            fix: '换成具名属性列表：只写状态变体真正会改的那些属性',
          })
          // ★ **不 continue** —— `all` 这条的缓动同样要判：
          //   展开成具名属性之后，它的缓动仍必须落在那条曲线上。
        }
        // ── M3：布局驱动属性
        if (LAYOUT_DRIVEN_PROPS.has(it.property)) {
          const key = `${d.file}|${d.selector}|${it.property}`
          if (layoutKeys.has(key)) {
            hitLayoutKeys.add(key)
          } else {
            layoutDriven++
            findings.push({
              rule: 'M3',
              file: d.file,
              line: d.line,
              selector: d.selector,
              prop: d.prop,
              value: d.value,
              reason:
                `过渡布局驱动属性 \`${it.property}\` ⇒ 每帧都要重排` +
                '（animate.md:71 点名 width / height / top / left / margins）',
              fix: '改成 transform 表达；若这确实是数据条（宽度即数据），到裁决簿登记并写明理由',
            })
          }
        }
        // ★ `transition: none` ⇒ 没有动效 ⇒ 不判（M1/M5 都跳过）
        if (it.property === 'none') continue
        // ── M1：减速曲线
        const info = classifyTiming(it.timing)
        if (info.kind === 'token') tokenStyle++
        else if (info.kind === 'literal') literalStyle++
        else if (info.kind === 'none') defaultEase++
        if (!info.ok) {
          findings.push({
            rule: 'M1',
            file: d.file,
            line: d.line,
            selector: d.selector,
            prop: d.prop,
            value: d.value,
            reason: `缓动不合规：${info.reason}`,
            fix: `改成 ${MOTION_EASE_TOKEN}（唯一定义处：web/src/style.css）`,
          })
        }
        // ── M5：时长上限
        if (it.durationMs !== null && it.durationMs > TRANSITION_MAX_MS) {
          longDurations++
          findings.push({
            rule: 'M5',
            file: d.file,
            line: d.line,
            selector: d.selector,
            prop: d.prop,
            value: d.value,
            reason:
              `过渡 ${it.durationMs}ms > ${TRANSITION_MAX_MS}ms` +
              '（animate.md 时长表：浮层 / 视图过渡 300–500ms；「Long feedback feels like latency」）',
            fix: `收到 ${TRANSITION_MAX_MS}ms 以内`,
          })
        }
      }
      continue
    }

    if (d.prop === 'animation' || d.prop === 'animation-timing-function') {
      for (const it of d.animationItems) {
        // ★ `animation: none` ⇒ 没有动效 ⇒ 不判（没有 keyframes 名就是它）
        if (!it.name) continue
        const infinite = (it.iterationCount ?? '').toLowerCase() === 'infinite'
        if (infinite) {
          animationInfinite++
          // ── M7：无限循环的匀速必须真的是匀速运动
          const info = classifyTiming(it.timing)
          if (info.kind === 'keyword' && it.timing && it.timing.toLowerCase() === 'linear') {
            const bodies = it.name ? keyframeBodies.get(it.name) ?? [] : []
            const nonUniform = bodies.some((b) =>
              /opacity\s*:|scale\s*\(|scale[XYZ]\s*\(|ease|elastic|bounce/i.test(b),
            )
            if (nonUniform) {
              infiniteNonUniform++
              findings.push({
                rule: 'M7',
                file: d.file,
                line: d.line,
                selector: d.selector,
                prop: d.prop,
                value: d.value,
                reason:
                  `无限循环动画 \`${it.name}\` 用了 \`linear\`，但它的 keyframes 里含` +
                  '非匀速变换（opacity / scale）⇒ 匀速会把「呼吸」拍成「机器」',
                fix: '把该 keyframes 的缓动改成它自己的曲线，或去掉 linear',
              })
            }
          }
          continue
        }
        animationFinite++
        // ── M1：一次性动画的缓动
        const info = classifyTiming(it.timing)
        if (info.kind === 'token') tokenStyle++
        else if (info.kind === 'literal') literalStyle++
        else if (info.kind === 'none') defaultEase++
        if (!info.ok) {
          findings.push({
            rule: 'M1',
            file: d.file,
            line: d.line,
            selector: d.selector,
            prop: d.prop,
            value: d.value,
            reason: `一次性入场动画的缓动不合规：${info.reason}`,
            fix: `改成 ${MOTION_EASE_TOKEN}`,
          })
        }
        // ── M5：一次性动画时长上限
        if (it.durationMs !== null && it.durationMs > ENTRANCE_MAX_MS) {
          longDurations++
          findings.push({
            rule: 'M5',
            file: d.file,
            line: d.line,
            selector: d.selector,
            prop: d.prop,
            value: d.value,
            reason: `一次性动画 ${it.durationMs}ms > ${ENTRANCE_MAX_MS}ms（animate.md 时长表上限）`,
            fix: `收到 ${ENTRANCE_MAX_MS}ms 以内`,
          })
        }
        // ── M6：入场登记
        if (it.name && isEntranceKeyframe(it.name)) {
          entranceSites++
          if (entranceNames.has(it.name)) registeredEntrances++
          else {
            findings.push({
              rule: 'M6',
              file: d.file,
              line: d.line,
              selector: d.selector,
              prop: d.prop,
              value: d.value,
              reason:
                `入场动效 \`${it.name}\` 未登记` +
                '（craft-floor「one authored moment, **not scattered effects**」⇒ 入场必须被 consciously 登记）',
              fix: '到裁决簿 entrances 里登记它，并写明它为什么值得存在',
            })
          }
        }
      }
      continue
    }
  }

  // ── M4：空间移动必须有 reduced-motion 路径
  for (const target of transformTargets) {
    if (neutralized.has(target)) continue
    const sep = target.lastIndexOf('|')
    const file = target.slice(0, sep)
    const selector = target.slice(sep + 1)
    const first = decls.find(
      (d) => d.file === file && d.prop === 'transition' &&
        normParts(d.selector).includes(selector) &&
        d.transitionItems.some((it) => it.property === 'transform'),
    )
    findings.push({
      rule: 'M4',
      file,
      line: first?.line ?? 1,
      selector,
      prop: 'transition',
      value: first?.value ?? '',
      reason:
        `选择器 \`${selector}\` 过渡了 \`transform\`（空间移动），` +
        '但 `@media (prefers-reduced-motion: reduce)` 里没有把它中和掉' +
        '（animate.md:77「Every web animation needs a prefers-reduced-motion path」；' +
        '★ 光有块不够 —— 块里必须**真的关掉这条空间过渡**）',
      fix:
        '在 reduced-motion 块里给同一选择器写 `transition: none`（或 `transition-property` 不含 transform）；' +
        '颜色 / 透明度反馈要保留（animate.md:77「preserving opacity, color, and state transitions that carry meaning」）',
    })
  }

  // 裁决簿漂移检查（★ 豁免命中数必须与登记数一致）
  const declaredLayout = (decisions.layoutDriven ?? []).length
  const hitLayout = hitLayoutKeys.size
  if (declaredLayout !== hitLayout) {
    findings.push({
      rule: 'M3',
      file: '(裁决簿)',
      line: 0,
      selector: '',
      prop: 'layoutDriven',
      value: `登记 ${declaredLayout} 条 / 命中 ${hitLayout} 条`,
      reason:
        `裁决簿登记了 ${declaredLayout} 条布局驱动豁免，本轮命中 ${hitLayout} 条` +
        ` ⇒ ${declaredLayout - hitLayout} 条已失效（代码变了）或口径不一致`,
      fix: '更新裁决簿，使登记与命中逐条对应',
    })
  }

  const byRule = (r: MotionRule) => findings.filter((f) => f.rule === r).length

  return {
    findings,
    decls,
    facts: {
      totalDeclarations: decls.length,
      transitionTotal,
      animationFinite,
      animationInfinite,
      tokenStyle,
      literalStyle,
      defaultEase,
      allProps,
      layoutDriven,
      layoutExemptions: declaredLayout,
      longDurations,
      infiniteNonUniform,
      entranceSites,
      registeredEntrances,
      M1: byRule('M1'),
      M2: byRule('M2'),
      M3: byRule('M3'),
      M4: byRule('M4'),
      M5: byRule('M5'),
      M6: byRule('M6'),
      M7: byRule('M7'),
    },
  }
}

// ───────────────────────── 自证 ─────────────────────────

export interface MotionSelftestCase {
  name: string
  /** 虚拟文件集（key = 相对路径） */
  files: Record<string, string>
  decisions?: MotionDecisions
  /** 期望的**逐规则命中数**（★ 省略 = 期望 0 ⇒ 断言恒真的陷阱被堵死） */
  expect: Partial<Record<MotionRule, number>>
  /** 期望出现在某条发现理由里的片段 */
  expectReason?: string
  /** 期望行号（用于证明行号口径没漂） */
  expectLine?: number
}

const CURVE = 'var(--kx-motion-ease-out)'

export const MOTION_SELFTEST: MotionSelftestCase[] = [
  {
    name: 'S1 指数曲线（令牌写法）⇒ 全绿',
    files: { 'a.css': `.x { transition: opacity 0.2s ${CURVE}; }` },
    expect: {},
  },
  {
    name: 'S2 未声明缓动 ⇒ M1（浏览器默认 ease 是慢起手）',
    files: { 'a.css': `.x { transition: opacity 0.2s; }` },
    expect: { M1: 1 },
    expectReason: '浏览器默认',
  },
  {
    name: 'S3 显式 ease ⇒ M1',
    files: { 'a.css': `.x { transition: opacity 0.2s ease; }` },
    expect: { M1: 1 },
    expectReason: '起步是软的',
  },
  {
    name: 'S4 ease-out 关键字 ⇒ M1（y1=0 起步为零速，不是指数减速）',
    files: { 'a.css': `.x { transition: opacity 0.2s ease-out; }` },
    expect: { M1: 1 },
    expectReason: '起步速度为零',
  },
  {
    name: 'S5 别的 cubic-bezier ⇒ M1',
    files: { 'a.css': `.x { transition: opacity 0.2s cubic-bezier(0.22, 1, 0.36, 1); }` },
    expect: { M1: 1 },
    expectReason: '不是本仓的减速曲线',
  },
  {
    name: 'S6 规范字面量 ⇒ 全绿（照抄 spacingScale「在阶上 / 令牌写法」两计一合法）',
    files: { 'a.css': `.x { transition: opacity 0.2s cubic-bezier(0.16, 1, 0.3, 1); }` },
    expect: {},
  },
  {
    name: 'S7 transition: all ⇒ M2',
    files: { 'a.css': `.x { transition: all 0.2s ${CURVE}; }` },
    expect: { M2: 1 },
    expectReason: '布局驱动属性',
  },
  {
    name: 'S8 过渡布局驱动属性 ⇒ M3',
    files: { 'a.css': `.x { transition: width 0.3s ${CURVE}; }` },
    expect: { M3: 1 },
    expectReason: '重排',
  },
  {
    name: 'S9 布局驱动属性登记豁免后 ⇒ M3 归零（且裁决簿口径一致）',
    files: { 'a.css': `.x { transition: width 0.3s ${CURVE}; }` },
    decisions: {
      layoutDriven: [{ file: 'a.css', selector: '.x', property: 'width', reason: '数据条' }],
    },
    expect: {},
  },
  {
    name: 'S10 裁决簿登记了但代码已改（豁免失效）⇒ M3 报漂移',
    files: { 'a.css': `.x { transition: opacity 0.2s ${CURVE}; }` },
    decisions: {
      layoutDriven: [{ file: 'a.css', selector: '.x', property: 'width', reason: '数据条' }],
    },
    expect: { M3: 1 },
    expectReason: '已失效',
  },
  {
    name: 'S11 过渡 transform 但文件无 reduced-motion ⇒ M4',
    files: { 'a.css': `.x { transition: transform 0.2s ${CURVE}; }` },
    expect: { M4: 1 },
    expectReason: 'prefers-reduced-motion',
  },
  {
    name: 'S12 过渡 transform 且有 reduced-motion 块 ⇒ M4 归零',
    files: {
      'a.css':
        `.x { transition: transform 0.2s ${CURVE}; }\n` +
        `@media (prefers-reduced-motion: reduce) { .x { transition: none; } }`,
    },
    expect: {},
  },
  {
    name: 'S13 只过渡 opacity ⇒ 不需要 reduced-motion 路径（颜色反馈要保留）',
    files: { 'a.css': `.x { transition: opacity 0.2s ${CURVE}; }` },
    expect: {},
  },
  {
    name: 'S14 transition 超 500ms ⇒ M5',
    files: { 'a.css': `.x { transition: opacity 0.6s ${CURVE}; }` },
    expect: { M5: 1 },
    expectReason: 'Long feedback feels like latency',
  },
  {
    name: 'S15 一次性 animation 超 800ms ⇒ M5',
    files: { 'a.css': `.x { animation: fade 0.9s ${CURVE}; }` },
    expect: { M5: 1 },
    expectReason: '时长表上限',
  },
  {
    name: 'S16 入场 keyframes 未登记 ⇒ M6',
    files: {
      'a.css':
        `@keyframes fade { from { opacity: 0; } to { opacity: 1; } }\n` +
        `.x { animation: fade 0.3s ${CURVE}; }`,
    },
    expect: { M6: 1 },
    expectReason: 'not scattered effects',
  },
  {
    name: 'S17 入场 keyframes 已登记 ⇒ M6 归零',
    files: {
      'a.css':
        `@keyframes fade { from { opacity: 0; } to { opacity: 1; } }\n` +
        `.x { animation: fade 0.3s ${CURVE}; }`,
    },
    decisions: { entrances: [{ name: 'fade', file: 'a.css', reason: '浮层入场' }] },
    expect: {},
  },
  {
    name: 'S18 非入场 keyframes（无 opacity:0）⇒ 不判 M6',
    files: {
      'a.css':
        `@keyframes slide { from { transform: translateX(-8px); } to { transform: none; } }\n` +
        `.x { animation: slide 0.3s ${CURVE}; }`,
    },
    expect: {},
  },
  {
    name: 'S19 匀速旋转（keyframes 只含 rotate）⇒ 不判 M7',
    files: {
      'a.css':
        `@keyframes spin { to { transform: rotate(360deg); } }\n` +
        `.x { animation: spin 1s linear infinite; }`,
    },
    expect: {},
  },
  {
    name: 'S20 匀速但 keyframes 含 opacity ⇒ M7（匀速会把呼吸拍成机器）',
    files: {
      'a.css':
        `@keyframes pulse { 0% { opacity: 0.3; } 100% { opacity: 1; } }\n` +
        `.x { animation: pulse 1s linear infinite; }`,
    },
    expect: { M7: 1 },
    expectReason: '呼吸',
  },
  {
    name: 'S21 无限循环的 ease-in-out 呼吸 ⇒ 不判（呼吸节律不是「到达」）',
    files: {
      'a.css':
        `@keyframes pulse { 0% { opacity: 0.3; } 100% { opacity: 1; } }\n` +
        `.x { animation: pulse 1s ease-in-out infinite; }`,
    },
    expect: {},
  },
  {
    name: 'S22 reduced-motion 块内的一切 ⇒ 不判（那正是关动效的地方）',
    files: {
      'a.css':
        `@media (prefers-reduced-motion: reduce) {\n` +
        `  .x { transition: opacity 0.2s ease; animation: fade 0.9s ease; }\n` +
        `}`,
    },
    expect: {},
  },
  {
    name: 'S23 嵌套语法 `&:hover` 能被解析（不解析就是整块失明）',
    files: {
      'a.css':
        `.card {\n  transition: all 0.2s ${CURVE};\n  &:hover { background: red; }\n}`,
    },
    expect: { M2: 1 },
    expectLine: 2,
  },
  {
    name: 'S24 逗号多选择器 ⇒ 每个分项都判（不是只判第一个）',
    files: { 'a.css': `.a, .b { transition: all 0.2s ${CURVE}; }` },
    expect: { M2: 1 },
  },
  {
    name: 'S25 行号口径：第 3 行的声明报第 3 行',
    files: { 'a.css': `.x {\n  color: red;\n  transition: all 0.2s ${CURVE};\n}` },
    expect: { M2: 1 },
    expectLine: 3,
  },
  {
    name: 'S26 多分项 transition（两个分项都错 ⇒ 两条发现）',
    files: {
      'a.css':
        `.x { transition: opacity 0.2s ease, transform 0.3s ease; }\n` +
        `@media (prefers-reduced-motion: reduce) { .x { transition: none; } }`,
    },
    expect: { M1: 2 },
  },
  {
    name: 'S27 逐项计时：第二个分项也拿到减速曲线',
    files: {
      'a.css':
        `.x { transition: opacity 0.2s ${CURVE}, transform 0.3s cubic-bezier(0.16, 1, 0.3, 1); }\n` +
        `@media (prefers-reduced-motion: reduce) { .x { transition: none; } }`,
    },
    expect: {},
  },
  {
    name: 'S28 transition-property 长写同样受判（M2 具名 + M1 缓动）',
    files: { 'a.css': `.x { transition-property: all; transition-duration: 0.2s; }` },
    expect: { M2: 1, M1: 1 },
  },
]

export interface MotionSelftestReport {
  passed: number
  total: number
  failed: string[]
}

/** 跑自证 */
export function runMotionSelftest(): MotionSelftestReport {
  const failed: string[] = []
  let passed = 0
  for (const c of MOTION_SELFTEST) {
    const decls: MotionDecl[] = []
    const fileTexts = new Map<string, string>()
    for (const [file, text] of Object.entries(c.files)) {
      fileTexts.set(file, text)
      decls.push(...scanMotionInFile({ file, text }))
    }
    const res = scanMotion(decls, fileTexts, c.decisions ?? {})
    const counts: Record<string, number> = {}
    for (const f of res.findings) counts[f.rule] = (counts[f.rule] ?? 0) + 1
    const problems: string[] = []
    for (const rule of ['M1', 'M2', 'M3', 'M4', 'M5', 'M6', 'M7'] as MotionRule[]) {
      const want = c.expect[rule] ?? 0
      const got = counts[rule] ?? 0
      if (want !== got) problems.push(`${rule} 期望 ${want} 实得 ${got}`)
    }
    // 期望之外不应有别的规则命中（★ 防止「顺带命中」掩盖判据没生效）
    const extra = res.findings.filter(
      (f) => (c.expect[f.rule] ?? 0) === 0 && !problems.some((p) => p.startsWith(f.rule)),
    )
    if (extra.length && !problems.length) {
      problems.push(`多出 ${extra.length} 条未声明的发现（首条：${extra[0].rule} ${extra[0].reason}）`)
    }
    if (c.expectReason && !res.findings.some((f) => f.reason.includes(c.expectReason!))) {
      problems.push(`没有发现理由含「${c.expectReason}」`)
    }
    if (c.expectLine !== undefined) {
      const line = res.findings[0]?.line
      if (line !== c.expectLine) problems.push(`首条发现行号期望 ${c.expectLine} 实得 ${line}`)
    }
    if (problems.length) failed.push(`${c.name} —— ${problems.join('；')}`)
    else passed++
  }
  return { passed, total: MOTION_SELFTEST.length, failed }
}
