/**
 * 判据：间距必须落在角色阶上（第 24 增量 · 2026-10-27）
 *
 * ★★★ 本门只判**一条**判据（判据越少越可证伪）：
 *
 *   **`margin` / `padding` / `gap` 的静态长度值必须落在已声明的间距阶上。**
 *
 *   —— 间距阶是「少数几个角色」，不是「所有测量值」；
 *      6px 与 8px 只差 2px 却各占一档 ⇒ 正是 impeccable `typeset.md` 的
 *      「adjacent sizes too close to carry different jobs」同族问题：
 *      **两个事实上的间距值统治版面 ⇒ 节奏全平 ⇒ 界面像机器拼的**。
 *
 * ★ 阶的事实源 = `web/src/style.css` 里 `--kx-space-*` 的**实际定义**，
 *   **运行时解析、绝不硬编码** ⇒ 改令牌 = 改判据（单一真值）。
 *
 * ★ 不判的（单独计数，不算违规）：
 *   · `auto` / `none` / `inherit` / `initial` / `unset` / `revert`
 *   · `%` / `em` / `vh` / `vw` / `calc()` / 其它 `var()` —— 相对父级或视口，静态不可定值；
 *   · `var(--kx-space-*)` / `var(--space-*)` —— **已经在消费令牌**，是阶的用法不是违反；
 *   · `0` / `0px` —— 没有间距；
 *   · **负值**（负 margin）—— 那是「把子元素拉回去」的**抵消**形状，不是节奏；
 *   · **≤ 3px** —— 发丝级（描边对齐 / 图标微调 / 光学校正）。
 *     ★ impeccable `layout.md`：「Make optical corrections only after inspecting the
 *       rendered result.」⇒ 光学微调是合法的，且它们**表达不了一个「档位」**。
 *     ★ 豁免计数必须**可见**（facts.hairline）—— 「豁免命中数为 0」是危险信号，不是「干净」。
 *
 * ★ `rem` **要判**：根字号是 16px（web 事实），rem 是可静态换算的
 *   ⇒ 而 rem 与 px 并存本身是「同一阶两份真值」⇒ 必须收进 px 口径。
 *   ★ `em` **不判**（em 是有意的相对语义，rem 是绝对值的相对写法）。
 *
 * ★ 本文件是**判据**（纯函数 + 文件扫描），门在 `web/scripts/design-spacing-scale-guard.mjs`。
 */

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'

// ───────────────────────── 阶 ─────────────────────────

export interface SpaceStep {
  token: string
  px: number
}

/**
 * 从 style.css 解析出**间距阶**（`--kx-space-*` 家族里定义了 px 长度的那些）。
 * ★ 只认正名单家族：`--kx-text-*` / `--radius*` 之类的 px 值不是间距。
 */
export function parseCanonicalSpaceScale(cssText: string): SpaceStep[] {
  const steps: SpaceStep[] = []
  for (const m of cssText.matchAll(/(--kx-space-[a-z0-9-]+)\s*:\s*(\d+(?:\.\d+)?)px\s*;/gi)) {
    const px = parseFloat(m[2])
    if (!steps.some((s) => s.token === m[1])) steps.push({ token: m[1], px })
  }
  return steps.sort((a, b) => a.px - b.px)
}

// ───────────────────────── 值分类 ─────────────────────────

export type SpaceKind =
  | 'onScale'     // 在阶上
  | 'offScale'    // 离阶
  | 'rem'         // rem 写法（同一阶的第二份真值）⇒ 判红
  | 'token'       // 消费令牌
  | 'zero'        // 0
  | 'dynamic'     // 静态不可定值
  | 'negative'    // 抵消形状（负 margin）
  | 'hairline'    // ≤3px 发丝/光学微调

export interface SpaceComponent {
  /** 该分量在**源码文本**里的起始偏移（批量改写必须落在字符区间上） */
  start: number
  end: number
  /** 分量的原始文本（如 `6px` / `0.875rem`） */
  raw: string
  /** 解析出的 px 值（rem 按 16px 根字号换算） */
  px: number
  kind: SpaceKind
}

export interface SpaceDecl {
  file: string
  selector: string
  property: string
  /** 声明的完整原始值 */
  raw: string
  line: number
  /** 是否位于 `@media` 块内 */
  inMedia: boolean
  components: SpaceComponent[]
  /** ★ 值里**没被任何分量覆盖**的残留（令牌完整性检查用） */
  residue: string
}

/** 根字号（web 事实：html 的 font-size 是 16px）—— rem 换算的事实源。 */
export const ROOT_FONT_PX = 16

/** 发丝/光学微调的上限（≤ 该值的 px 不判）。 */
export const HAIRLINE_MAX_PX = 3

const SPACING_PROPS = [
  'margin',
  'margin-top',
  'margin-right',
  'margin-bottom',
  'margin-left',
  'padding',
  'padding-top',
  'padding-right',
  'padding-bottom',
  'padding-left',
  'gap',
  'row-gap',
  'column-gap',
]

/** 一条声明里可能出现的多个值（shorthand）。 */
const PROP_RE = new RegExp(
  `(^|[{;\\s])(${SPACING_PROPS.join('|')})\\s*:\\s*([^;}"']+)`,
  'gi',
)

/** 判定一个分量的原始文本属于哪一类（★ 不含位置信息，位置由调用方给）。 */
export function classifySpaceValue(raw: string, scalePx: number[]): Omit<SpaceComponent, 'start' | 'end'> {
  const t = raw.trim()
  if (t === '' ) return { raw: t, px: NaN, kind: 'dynamic' }
  if (/^var\(--(kx-)?space-/i.test(t)) return { raw: t, px: NaN, kind: 'token' }
  if (/^0(\.0+)?(px)?$/i.test(t)) return { raw: t, px: 0, kind: 'zero' }
  if (/^(auto|none|inherit|initial|unset|revert)$/i.test(t)) return { raw: t, px: NaN, kind: 'dynamic' }
  // ★ vh/vw 与 rem/em 同族坑：数字紧邻时没有词边界 ⇒ `\bvw\b` 匹配不到 `5vw`
  if (/%|(^|[^a-z])vh\b|(^|[^a-z])vw\b|calc\(|var\(/i.test(t)) return { raw: t, px: NaN, kind: 'dynamic' }
  // ★ rem：`(^|[^a-z])rem\b` —— 不能用 `\brem\b`：数字紧邻时没有词边界 ⇒ 匹配不到 `0.875rem`
  if (/(^|[^a-z])rem\b/i.test(t)) {
    const n = parseFloat(t)
    return { raw: t, px: Number.isFinite(n) ? n * ROOT_FONT_PX : NaN, kind: 'rem' }
  }
  // ★ em：同一类坑 —— `1em` 里 "1" 与 "e" 都是词字符 ⇒ `\bem\b` **匹配不到**
  if (/(^|[^a-z])em\b/i.test(t)) return { raw: t, px: NaN, kind: 'dynamic' }
  const px = parseFloat(t)
  if (!Number.isFinite(px)) return { raw: t, px: NaN, kind: 'dynamic' }
  if (px < 0) return { raw: t, px, kind: 'negative' }
  if (px <= HAIRLINE_MAX_PX) return { raw: t, px, kind: 'hairline' }
  if (scalePx.includes(px)) return { raw: t, px, kind: 'onScale' }
  return { raw: t, px, kind: 'offScale' }
}

/** 就近的阶（★ 返回**另一个等距的阶** ⇒ 平局档必须由人裁决，见裁决簿）。 */
export function nearestSpaceSteps(px: number, scalePx: number[]): { nearest: number; tie: number | null } {
  let nearest = scalePx[0]
  let best = Math.abs(px - nearest)
  let tie: number | null = null
  for (const s of scalePx) {
    const d = Math.abs(px - s)
    if (d < best) {
      best = d
      nearest = s
      tie = null
    } else if (d === best && tie === null && s !== nearest) {
      tie = s
    }
  }
  return { nearest, tie }
}

// ───────────────────────── 扫描 ─────────────────────────

/** 剥 CSS 注释（斜杠星 … 星斜杠）——**等长**替换为空白，行号不漂。 */
export function maskCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
}

/** 预计算每行起始偏移，便于把字符偏移换算成行号。 */
function lineStarts(text: string): number[] {
  const out = [0]
  for (let i = 0; i < text.length; i++) if (text[i] === '\n') out.push(i + 1)
  return out
}

function lineOf(starts: number[], offset: number): number {
  let lo = 0
  let hi = starts.length - 1
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1
    if (starts[mid] <= offset) lo = mid
    else hi = mid - 1
  }
  return lo + 1
}

/** 把一条声明的值文本切成若干分量，并给出每个分量的字符区间。 */
export function splitSpaceComponents(
  valueText: string,
  valueOffset: number,
  scalePx: number[],
): SpaceComponent[] {
  const out: SpaceComponent[] = []
  const t = valueText.trim()
  // ★ 整值就是「静态不可定值」的形状（calc / % / em / vh / vw / **env()**）⇒ **不再拆**
  //   （`8px calc(100% - 20px)` 整条判动态 —— 判据宁可窄，不可宽）
  // ★★ `env(...)` 是**运行时环境变量**（如 `env(safe-area-inset-bottom, 0px)`）
  //   ⇒ 静态不可定值 ⇒ 整条判动态（实测踩过：漏判 ⇒ 残留里出现 `env(...)` ⇒ 盲区告警）。
  if (/calc\(|env\(|%|(^|[^a-z])vh\b|(^|[^a-z])vw\b|(^|[^a-z])em\b/i.test(t)) {
    return [{ raw: t, px: NaN, kind: 'dynamic', start: valueOffset, end: valueOffset + t.length }]
  }
  // ★ 逐个 token：**var(...) 整体算一个**（令牌写法不是「数字分量」）
  //   —— 只按数字切会把 `var(--kx-space-2)` 整个丢掉 ⇒ 令牌声明会被误记成「动态」。
  //
  // ★★★ 数字字面量必须覆盖 CSS 允许的**全部**写法：**前导小数点**（`.35rem`）与前导符号。
  //   原写法 `-?\d+(?:\.\d+)?` **匹配不到前导小数点** ⇒ `.35rem` 被切成 `35rem`：
  //     ① 小数点落在区间之外 ⇒ 改写后留下 `.64px`（5.6px 变成 0.64px）；
  //     ② `parseFloat('35rem')` = 35 ⇒ px 被算成 **560**（真值 5.6）⇒ 就近阶判到 64px。
  //   ★ 这不是「漏计」而是「**算出错误的值**」⇒ 既有三道闸全都拦不住：
  //     闸③（区间外逐字未变）恰恰因为小数点被当成「非间距内容」而放行，
  //     闸②（离阶为 0）因为 0.64px ≤ 3px 落在发丝豁免里而放行。
  const re = /var\([^)]*\)|[-+]?(?:\d+(?:\.\d+)?|\.\d+)(?:px|rem|em|pt)?/gi
  let m: RegExpExecArray | null
  while ((m = re.exec(valueText)) !== null) {
    const raw = m[0]
    const start = valueOffset + m.index
    const end = start + raw.length
    const classified = classifySpaceValue(raw, scalePx)
    out.push({ ...classified, start, end })
  }
  // 夹在分量之间的**具名值**（auto / inherit …）——它们是「静态不可定值」
  if (out.length === 0) {
    out.push({ raw: t, px: NaN, kind: 'dynamic', start: valueOffset, end: valueOffset + t.length })
  }
  return out
}

/**
 * 声明值里**没有被任何分量覆盖**的残留文本。
 *
 * ★★★ 这是一道**独立于 px 换算逻辑**的「令牌完整性」检查：
 *   把值按扫描器的分量切开后，残留必须**只含具名值与分隔符**。
 *   任何**被漏掉的数值令牌**都会以「残留里有数字或小数点」的形态暴露出来。
 *
 * ★★ 为什么必须独立于 px 换算：
 *   `.35rem` 被截成 `35rem` 时，**算出来的 px 也是错的**（560 而不是 5.6），
 *   而任何「用算出来的 px 再校验一遍」的复核都会**跟着一起错** ⇒ 门绿。
 *   这道检查问的是「**文本**有没有被完整切分」⇒ 它不看 px ⇒ 它能独立抓出这类缺陷。
 */
export function valueResidue(
  valueText: string,
  components: SpaceComponent[],
  valueOffset: number,
): string {
  const ivs = components
    .map((c) => ({ start: c.start - valueOffset, end: c.end - valueOffset }))
    .filter((iv) => iv.start >= 0 && iv.end <= valueText.length && iv.end > iv.start)
    .sort((a, b) => b.start - a.start)
  let out = valueText
  for (const iv of ivs) {
    out = out.slice(0, iv.start) + ' '.repeat(iv.end - iv.start) + out.slice(iv.end)
  }
  return out
}

/**
 * 残留里若还有**任何未知 token** ⇒ 有数值令牌被漏掉了（扫描器有盲区）。
 * ★ 残留**只允许**「分隔符 + 具名值」：
 *   · 具名值：auto / none / inherit / initial / unset / revert / min-content / max-content / fit-content
 *   · 分隔符：空白 / 逗号 / 斜杠（`grid-gap: 8px / 12px` 的两值语法）
 * ★ 「未知单位」也算未知 token（实测：`10cm` 会被数字正则吃成 `10` ⇒ 残留 `cm` ⇒ 必须抓出来，
 *   否则 10cm 会被当成 10px 判阶）。
 */
const KNOWN_NAMED_VALUES =
  /^(?:auto|none|inherit|initial|unset|revert|min-content|max-content|fit-content)$/i

export function residueHasMissedNumber(residue: string): boolean {
  const r = residue.replace(/\/\*[\s\S]*?\*\//g, ' ').trim()
  if (!r) return false
  for (const tok of r.split(/[\s,/]+/).filter(Boolean)) {
    if (!KNOWN_NAMED_VALUES.test(tok)) return true
  }
  return false
}

/**
 * 扫描一段 CSS 文本里的间距声明。
 * ★ 必须能处理 **minified CSS**（一行多规则）与 **@media 嵌套** ——
 *   否则会把整条 minified 规则当成一个选择器 ⇒ 制造假阳性。
 */
export function scanCssSpacing(css: string, scalePx: number[], baseLine = 1): SpaceDecl[] {
  const out: SpaceDecl[] = []
  const starts = lineStarts(css)
  const declRe = new RegExp(`(^|[{;\\s])(${SPACING_PROPS.join('|')})\\s*:\\s*([^;}"']+)`, 'gi')

  const walk = (from: number, to: number, inMedia: boolean): void => {
    let i = from
    while (i < to) {
      const brace = css.indexOf('{', i)
      if (brace === -1 || brace >= to) break
      const selText = css.slice(i, brace).trim()
      let depth = 1
      let j = brace + 1
      while (j < to && depth > 0) {
        if (css[j] === '{') depth++
        else if (css[j] === '}') depth--
        j++
      }
      const bodyEnd = j - 1
      const body = css.slice(brace + 1, bodyEnd)
      if (selText.startsWith('@')) {
        const isMedia = /^@media/i.test(selText)
        walk(brace + 1, bodyEnd, inMedia || isMedia)
      } else if (body.includes('{')) {
        walk(brace + 1, bodyEnd, inMedia)
      } else {
        declRe.lastIndex = 0
        let m: RegExpExecArray | null
        while ((m = declRe.exec(body)) !== null) {
          // ★★ `!important` 是**优先级标记**，不是值的一部分 ⇒ 剥掉再解析。
          //   （它在值的**末尾** ⇒ 剥掉不影响前面各分量的偏移。）
          //   实测踩过：`padding: 32px 16px !important` ⇒ 残留里出现 `!important`
          //   ⇒ 被判成「扫描器盲区」；而它其实是**完全合法**的 CSS。
          const raw = m[3].replace(/\s*!important\s*$/i, '')
          const lead = raw.length - raw.trimStart().length
          const valOffset = brace + 1 + m.index + m[0].indexOf(raw) + lead
          const value = raw.trim()
          const components = splitSpaceComponents(value, valOffset, scalePx)
          out.push({
            file: '',
            selector: selText,
            property: m[2].toLowerCase(),
            raw: value,
            line: baseLine + lineOf(starts, brace + 1 + m.index) - 1,
            inMedia,
            components,
            residue: valueResidue(value, components, valOffset),
          })
        }
      }
      i = j
    }
  }
  walk(0, css.length, false)
  return out
}

/** 抽 `.vue` 的 `<style>` 块（返回文本 + 起始行）。 */
export function styleBlocksOf(vueText: string): Array<{ text: string; baseLine: number; start: number }> {
  const out: Array<{ text: string; baseLine: number; start: number }> = []
  const re = /<style[^>]*>([\s\S]*?)<\/style>/gi
  let m: RegExpExecArray | null
  while ((m = re.exec(vueText)) !== null) {
    out.push({
      text: m[1],
      baseLine: vueText.slice(0, m.index).split('\n').length,
      start: m.index + m[0].indexOf(m[1]),
    })
  }
  return out
}

/** 抽模板里的内联 `style="…"` 属性（★ 模板里的内联间距也是「上屏的间距」）。 */
export function scanInlineStyleSpacing(vueText: string, scalePx: number[]): SpaceDecl[] {
  const out: SpaceDecl[] = []
  const starts = lineStarts(vueText)
  // ★ 负向后顾：`:style="…"` 是 **Vue 绑定**（值是表达式），不是静态 style 属性
  //   ⇒ 用静态属性正则去匹它会切出**空值**的假声明（实测踩过）。
  const re = /(?<![:\w-])style\s*=\s*"([^"]*)"/gi
  let m: RegExpExecArray | null
  while ((m = re.exec(vueText)) !== null) {
    const attrBase = m.index + m[0].indexOf(m[1])
    const declRe = new RegExp(`(^|[{;\\s])(${SPACING_PROPS.join('|')})\\s*:\\s*([^;"']+)`, 'gi')
    let dm: RegExpExecArray | null
    while ((dm = declRe.exec(m[1])) !== null) {
      const raw = dm[3].replace(/\s*!important\s*$/i, '')
      if (!raw.trim()) continue
      const lead = raw.length - raw.trimStart().length
      const valOffset = attrBase + dm.index + dm[0].indexOf(raw) + lead
      const value = raw.trim()
      const components = splitSpaceComponents(value, valOffset, scalePx)
      out.push({
        file: '',
        selector: '(inline style)',
        property: dm[2].toLowerCase(),
        raw: value,
        line: lineOf(starts, attrBase + dm.index),
        inMedia: false,
        components,
        residue: valueResidue(value, components, valOffset),
      })
    }
  }
  // ★ Vue 对象语法：:style="{ gap: '10px' }" —— 同样是上屏的间距
  const objRe = new RegExp(
    `['"]?(${SPACING_PROPS.join('|')})['"]?\\s*:\\s*['"]([^'"]+)['"]`,
    'gi',
  )
  let om: RegExpExecArray | null
  while ((om = objRe.exec(vueText)) !== null) {
    const raw = om[2]
    const lead = raw.length - raw.trimStart().length
    const valStart = om.index + om[0].indexOf(raw) + lead
    const value = raw.trim()
    const components = splitSpaceComponents(value, valStart, scalePx)
    out.push({
      file: '',
      selector: '(inline style)',
      property: om[1].toLowerCase(),
      raw: value,
      line: lineOf(starts, om.index),
      inMedia: false,
      components,
      residue: valueResidue(value, components, valStart),
    })
  }
  return out
}

// ───────────────────────── 遍历 ─────────────────────────

export const SCAN_EXT = new Set(['.vue', '.css'])
export const SKIP_DIRS = new Set(['node_modules', 'dist', '.git', 'coverage', 'reports'])

/** 列出所有参与本门扫描的源文件（`.vue` + `.css`）。 */
export function listStyleFiles(rootDir: string): string[] {
  const out: string[] = []
  const walk = (dir: string): void => {
    let entries: Array<{ name: string; isDirectory(): boolean }>
    try {
      entries = readdirSync(dir, { withFileTypes: true }) as unknown as Array<{
        name: string
        isDirectory(): boolean
      }>
    } catch {
      return
    }
    for (const e of entries) {
      if (SKIP_DIRS.has(e.name)) continue
      const p = join(dir, e.name)
      if (e.isDirectory()) walk(p)
      else if (SCAN_EXT.has(extname(e.name))) out.push(p)
    }
  }
  walk(rootDir)
  return out.sort()
}

/** 扫描一个文件里的所有间距声明。 */
export function collectSpacingDecls(file: string, scalePx: number[]): SpaceDecl[] {
  const raw = readFileSync(file, 'utf8')
  const out = collectSpacingDeclsFromText(raw, file, scalePx)
  return out
}

/**
 * ★★ 在**一段文本**上收集间距声明（迁移器的闸③ 要在改写前后**各**跑一遍 ⇒ 必须同源）。
 * ★ `isVue` 决定走 `<style>` 块 + 内联样式，还是整份当作 CSS。
 */
export function collectSpacingDeclsFromText(raw: string, rel: string, scalePx: number[]): SpaceDecl[] {
  const out: SpaceDecl[] = []
  if (rel.endsWith('.css')) {
    const masked = maskCssComments(raw)
    out.push(...scanCssSpacing(masked, scalePx, 1))
  } else {
    for (const b of styleBlocksOf(raw)) {
      const masked = maskCssComments(b.text)
      const ds = scanCssSpacing(masked, scalePx, b.baseLine)
      // ★★★ 区间必须落在**文件**文本上，不是块内文本 ——
      //   `<style>` 块是从文件里切出来的 ⇒ 块内偏移要加上块的起点，否则批量改写会改错位置。
      //   （实测踩过：App.vue:563 区间不匹配 ⇒ 「期望 10px，实际 uthH」。）
      for (const d of ds) {
        for (const c of d.components) {
          c.start += b.start
          c.end += b.start
        }
      }
      out.push(...ds)
    }
    out.push(...scanInlineStyleSpacing(raw, scalePx))
  }
  for (const d of out) d.file = rel
  return out
}

// ───────────────────────── 汇总 ─────────────────────────

export interface SpacingFacts {
  totalDeclarations: number
  onScale: number
  offScale: number
  offScaleFiles: number
  offScaleComponents: number
  remSpacing: number
  tokenStyle: number
  dynamic: number
  zero: number
  hairline: number
  negative: number
  /**
   * ★★ **扫描器盲区数** —— 声明值里有**没被任何分量覆盖**的数值令牌。
   *   这不是「违规」，而是「**本门看不见那里**」⇒ 硬错误（不可棘轮化）。
   *   ★ 曾经的真实事故：`.35rem` 被数字正则截成 `35rem` ⇒ 小数点落在区间外
   *     ⇒ px 被算成 560（真值 5.6）⇒ 改写后留下 `.64px`
   *     ⇒ 而离阶读数是 0（0.64px ≤ 3px 属发丝豁免）⇒ **门绿**。
   */
  missedTokenDecls: number
  /**
   * ★ **人工豁免命中数** —— 裁决簿里登记、且本轮真的命中了的豁免条数。
   *   ★ 豁免必须**可见**：登记了 N 条但命中 0 条 ⇒ 裁决簿已与代码漂移 ⇒ 硬错误
   *   （「豁免命中数为 0」是危险信号，不是「干净」）。
   */
  exempted: number
}

export interface SpacingFinding {
  file: string
  line: number
  selector: string
  property: string
  value: string
  /** ★ `offScale` = 离阶 px；`rem` = rem 写法（同一阶的第二份真值）—— 两个**独立**指标 */
  kind: 'offScale' | 'rem'
  /** 离阶的分量（值 + 所在区间 + 就近的阶 + 平局） */
  parts: Array<{ raw: string; px: number; nearest: number; tie: number | null; start: number; end: number }>
  inMedia: boolean
}

export interface SpacingScanResult {
  findings: SpacingFinding[]
  facts: SpacingFacts
  decls: SpaceDecl[]
}

/**
 * 扫描一棵源码树，产出【离阶声明清单】与【读数】。
 * @param files 文件清单
 * @param rootDir 用于把绝对路径变成相对路径
 * @param scalePx 角色阶（px 集合）
 * @param opts.exemptions 人工豁免的**分量键**（`<相对文件>|<选择器>|<属性>|<原始值>`）
 *   —— ★ 键里**不带行号**：行号会随文件改动漂移 ⇒ 用行号当键的门是「行号错的门」，
 *     比没有门更坏（第 23 增量的教训）。
 */
export function scanSpacingScale(
  files: string[],
  rootDir: string,
  scalePx: number[],
  opts?: { exemptions?: string[] },
): SpacingScanResult {
  const findings: SpacingFinding[] = []
  const decls: SpaceDecl[] = []
  const exempt = new Set(opts?.exemptions ?? [])
  const facts: SpacingFacts = {
    totalDeclarations: 0,
    onScale: 0,
    offScale: 0,
    offScaleFiles: 0,
    offScaleComponents: 0,
    remSpacing: 0,
    tokenStyle: 0,
    dynamic: 0,
    zero: 0,
    hairline: 0,
    negative: 0,
    missedTokenDecls: 0,
    exempted: 0,
  }
  const offFiles = new Set<string>()

  for (const f of files) {
    const ds = collectSpacingDecls(f, scalePx)
    // ★★ `d.file` 必须是**相对路径**（与 findings 的 file 字段同一个口径）——
    //   否则人工豁免的键（相对路径）永远匹配不上（实测踩过：豁免命中 0 ⇒ 门报「豁免漂移」）。
    // ★ 同 motion.ts：Windows 反斜杠路径剥不掉前缀，用 relative + 统一正斜杠。
    const rel = relative(rootDir, f).replace(/\\/g, '/')
    for (const d of ds) d.file = rel
    for (const d of ds) {
      facts.totalDeclarations++
      // ★★ 令牌完整性：值里有**没被任何分量覆盖**的数值令牌
      //   ⇒ 本门对那条声明是**盲的** ⇒ 硬错误（不可棘轮化）。
      if (residueHasMissedNumber(d.residue)) facts.missedTokenDecls++
      let hasOff = false
      let hasRem = false
      const parts: SpacingFinding['parts'] = []
      for (const c of d.components) {
        // ★ 人工豁免：按**分量键**豁免（裁决簿里登记的「派生对齐偏移」这类）
        if (exempt.has(`${d.file}|${d.selector}|${d.property}|${c.raw}`)) {
          facts.exempted++
          continue
        }
        switch (c.kind) {
          case 'onScale':
            facts.onScale++
            break
          case 'offScale':
            hasOff = true
            facts.offScaleComponents++
            {
              const { nearest, tie } = nearestSpaceSteps(c.px, scalePx)
              parts.push({ raw: c.raw, px: c.px, nearest, tie, start: c.start, end: c.end })
            }
            break
          case 'rem':
            hasRem = true
            facts.remSpacing++
            break
          case 'token':
            facts.tokenStyle++
            break
          case 'zero':
            facts.zero++
            break
          case 'negative':
            facts.negative++
            break
          case 'hairline':
            facts.hairline++
            break
          default:
            facts.dynamic++
        }
      }
      if (hasRem) {
        // ★ rem 是**独立指标**（remSpacing），不并进 offScale ⇒ 棘轮能分开看
        offFiles.add(f)
        findings.push({
          file: f.replace(rootDir + '/', ''),
          line: d.line,
          selector: d.selector,
          property: d.property,
          value: d.raw,
          kind: 'rem',
          parts: d.components
            .filter((c) => c.kind === 'rem')
            .map((c) => ({ raw: c.raw, px: c.px, nearest: NaN, tie: null, start: c.start, end: c.end })),
          inMedia: d.inMedia,
        })
      }
      if (hasOff) {
        facts.offScale++
        offFiles.add(f)
        findings.push({
          file: f.replace(rootDir + '/', ''),
          line: d.line,
          selector: d.selector,
          property: d.property,
          value: d.raw,
          kind: 'offScale',
          parts,
          inMedia: d.inMedia,
        })
      }
    }
    decls.push(...ds)
  }
  facts.offScaleFiles = offFiles.size
  return { findings, facts, decls }
}

// ───────────────────────── 自证 ─────────────────────────

export interface SelftestCase {
  id: string
  name: string
  run: () => boolean
}

const SELFTEST_SCALE = [4, 8, 12, 16, 24, 32, 40, 48, 64]

export const SPACING_SELFTEST: SelftestCase[] = [
  {
    id: 'S1',
    name: '★ 阶的事实源从 style.css 解析出 9 档（不是硬编码）',
    run: () => {
      const css = `
        --kx-space-1: 4px;
        --kx-space-2: 8px;
        --kx-space-3: 12px;
        --kx-space-4: 16px;
        --kx-space-5: 24px;
        --kx-space-6: 32px;
        --kx-space-7: 40px;
        --kx-space-8: 48px;
        --kx-space-9: 64px;
      `
      const steps = parseCanonicalSpaceScale(css)
      return steps.length === 9 && steps[0].px === 4 && steps[8].px === 64
    },
  },
  {
    id: 'S2',
    name: '★ 非正名单家族的 px 值不算间距阶（--kx-text-md: 14px 不入阶）',
    run: () => {
      const steps = parseCanonicalSpaceScale('--kx-text-md: 14px; --kx-space-1: 4px;')
      return steps.length === 1 && steps[0].px === 4
    },
  },
  {
    id: 'S3',
    name: '★ 值分类正确：px 在阶上 / 离阶 / rem / 令牌 / 0 / 动态',
    run: () => {
      const s = SELFTEST_SCALE
      const k = (raw: string) => classifySpaceValue(raw, s).kind
      return (
        k('8px') === 'onScale' &&
        k('10px') === 'offScale' &&
        k('0.875rem') === 'rem' &&
        k('var(--kx-space-2)') === 'token' &&
        k('0') === 'zero' &&
        k('auto') === 'dynamic' &&
        k('50%') === 'dynamic' &&
        k('1em') === 'dynamic' &&
        k('calc(1rem + 2px)') === 'dynamic'
      )
    },
  },
  {
    id: 'S4',
    name: '★ rem 按 16px 根字号换算 ⇒ 可静态定值（要判）',
    run: () => {
      const c = classifySpaceValue('0.875rem', SELFTEST_SCALE)
      return c.kind === 'rem' && c.px === 14
    },
  },
  {
    id: 'S5',
    name: '★ em 不判（相对语义是有意的）',
    run: () => classifySpaceValue('1em', SELFTEST_SCALE).kind === 'dynamic',
  },
  {
    id: 'S6',
    name: '★ 负值是「抵消」形状 ⇒ 豁免（不是节奏）',
    run: () => classifySpaceValue('-20px', SELFTEST_SCALE).kind === 'negative',
  },
  {
    id: 'S7',
    name: '★ ≤3px 是发丝/光学微调 ⇒ 豁免（且豁免计数必须可见）',
    run: () => {
      const s = SELFTEST_SCALE
      return (
        classifySpaceValue('1px', s).kind === 'hairline' &&
        classifySpaceValue('3px', s).kind === 'hairline' &&
        classifySpaceValue('4px', s).kind === 'onScale'
      )
    },
  },
  {
    id: 'S8',
    name: '★ 平局档要标出「另一个等距的阶」（6px ↔ 4/8）',
    run: () => {
      const r = nearestSpaceSteps(6, SELFTEST_SCALE)
      return r.nearest === 4 && r.tie === 8
    },
  },
  {
    id: 'S9',
    name: '★ 方向唯一档不标平局（7px → 8，距离 1）',
    run: () => {
      const r = nearestSpaceSteps(7, SELFTEST_SCALE)
      return r.nearest === 8 && r.tie === null
    },
  },
  {
    id: 'S10',
    name: '★ CSS 注释里的间距不算数（遮罩）',
    run: () => {
      const css = '/* .x { gap: 7px } */\n.a { gap: 10px; }'
      const ds = scanCssSpacing(maskCssComments(css), SELFTEST_SCALE)
      return ds.length === 1 && ds[0].raw === '10px'
    },
  },
  {
    id: 'S11',
    name: '★ 遮罩后行号不漂（注释多行时仍指到声明所在行）',
    run: () => {
      const css = '/*\n * 多行注释\n */\n.a { gap: 10px; }'
      const ds = scanCssSpacing(maskCssComments(css), SELFTEST_SCALE)
      return ds.length === 1 && ds[0].line === 4
    },
  },
  {
    id: 'S12',
    name: '★ minified CSS：一行多规则 ⇒ 每个规则各自带自己的选择器（不塌成一条）',
    run: () => {
      const css = '.a{gap:10px}.b{padding:7px 10px}'
      const ds = scanCssSpacing(css, SELFTEST_SCALE)
      return ds.length === 2 && ds[0].selector === '.a' && ds[1].selector === '.b'
    },
  },
  {
    id: 'S13',
    name: '★ @media 嵌套：里面的规则仍带自己的选择器，且标记 inMedia',
    run: () => {
      const css = '@media (max-width: 480px) { .a { gap: 7px; } }'
      const ds = scanCssSpacing(css, SELFTEST_SCALE)
      return ds.length === 1 && ds[0].selector === '.a' && ds[0].inMedia === true
    },
  },
  {
    id: 'S14',
    name: '★ shorthand 多值：每个分量各自带区间（批量改写要落在字符区间上）',
    run: () => {
      const css = '.a { padding: 6px 10px; }'
      const ds = scanCssSpacing(css, SELFTEST_SCALE)
      const c = ds[0].components
      return c.length === 2 && c[0].raw === '6px' && c[1].raw === '10px' && c[1].start > c[0].start
    },
  },
  {
    id: 'S15',
    name: '★ 模板内联 style 的间距也要被扫到',
    run: () => {
      const vue = '<template><div style="gap: 10px"></div></template>'
      const ds = scanInlineStyleSpacing(vue, SELFTEST_SCALE)
      return ds.length === 1 && ds[0].property === 'gap'
    },
  },
  {
    id: 'S16',
    name: '★ Vue 对象语法 :style="{ gap: \'10px\' }" 也要被扫到',
    run: () => {
      const vue = '<template><div :style="{ gap: \'10px\' }"></div></template>'
      const ds = scanInlineStyleSpacing(vue, SELFTEST_SCALE)
      return ds.length === 1 && ds[0].raw === '10px'
    },
  },
  {
    id: 'S17',
    name: '★★ 对照：已在阶上的值绝不能被报成离阶（判据不是恒真）',
    run: () => {
      const res = scanSpacingScaleOn(['.a{gap:12px}.b{padding:0.875rem}.c{gap:var(--kx-space-2)}'], SELFTEST_SCALE)
      return res.findings.length === 1 // 只有 rem 那一条
    },
  },
  {
    id: 'S18',
    name: '★★ 对照：负值 / ≤3px / 0 / auto 都不报（豁免是活分支）',
    run: () => {
      const res = scanSpacingScaleOn(
        ['.a{margin:8px -20px}.b{padding:2px 4px}.c{margin:0}.d{gap:auto}'],
        SELFTEST_SCALE,
      )
      return res.findings.length === 0
    },
  },
  {
    id: 'S19',
    name: '★ 豁免计数必须可见（hairline / negative / zero 各自计数）',
    run: () => {
      const res = scanSpacingScaleOn(
        ['.a{padding:2px 4px}.b{margin:8px -20px}.c{margin:0}.d{gap:auto}.e{gap:8px}'],
        SELFTEST_SCALE,
      )
      return (
        res.facts.hairline === 1 &&
        res.facts.negative === 1 &&
        res.facts.zero === 1 &&
        res.facts.dynamic === 1 &&
        res.facts.onScale === 3
      )
    },
  },
  {
    id: 'S20',
    name: '★★ 判据对「离阶值改了结论就会变」敏感（改一处必须改结论）',
    run: () => {
      const before = scanSpacingScaleOn(['.a{gap:10px}'], SELFTEST_SCALE).findings.length
      const after = scanSpacingScaleOn(['.a{gap:12px}'], SELFTEST_SCALE).findings.length
      return before === 1 && after === 0
    },
  },
  {
    id: 'S21',
    name: '★★ .vue 的 <style> 块里的声明区间必须落在**文件**文本上（不是块内文本）',
    run: () => {
      const vue = [
        '<template>',
        '  <div class="a">x</div>',
        '</template>',
        '',
        '<style scoped>',
        '.a { gap: 10px; }',
        '</style>',
        '',
      ].join('\n')
      const ds = collectSpacingDeclsFromText(vue, 'fake.vue', SELFTEST_SCALE)
      if (ds.length !== 1) return false
      const c = ds[0].components[0]
      // ★ 区间取出来的**那几字**必须正好是「10px」
      return vue.slice(c.start, c.end) === '10px'
    },
  },
  {
    id: 'S22',
    name: '★ 同一文件里 <style> 块与内联样式的区间都在文件文本上',
    run: () => {
      const vue = [
        '<template>',
        '  <div style="gap: 10px"></div>',
        '</template>',
        '',
        '<style scoped>',
        '.a { padding: 6px 10px; }',
        '</style>',
        '',
      ].join('\n')
      const ds = collectSpacingDeclsFromText(vue, 'fake.vue', SELFTEST_SCALE)
      if (ds.length !== 2) return false
      return ds.every((d) => d.components.every((c) => {
        const t = vue.slice(c.start, c.end)
        return t === c.raw
      }))
    },
  },
  {
    id: 'S23',
    name: '★★ 前导小数点的数值必须**整体**匹配（`.35rem` 不是 `35rem`）',
    run: () => {
      const cs = splitSpaceComponents('.35rem', 0, SELFTEST_SCALE)
      if (cs.length !== 1) return false
      const c = cs[0]
      // ★ 三件事必须同时成立：原文完整 / 区间完整 / px 换算正确（5.6 而不是 560）
      return c.raw === '.35rem' && c.start === 0 && c.end === 6 && Math.abs(c.px - 5.6) < 1e-9
    },
  },
  {
    id: 'S24',
    name: '★★ 前导小数点的 px 也要整体匹配（`.5rem` = 8px，且仍判红为 rem）',
    run: () => {
      const cs = splitSpaceComponents('.5rem', 0, SELFTEST_SCALE)
      if (cs.length !== 1) return false
      const c = cs[0]
      // ★ kind 是 'rem'（写法债 ⇒ 判红），px 是 8（数值正确）
      return c.raw === '.5rem' && c.end === 5 && c.px === 8 && c.kind === 'rem'
    },
  },
  {
    id: 'S25',
    name: '★★ 令牌完整性：残留里不能有数字/小数点（漏掉的令牌必须暴露）',
    run: () => {
      // 正常：`.35rem` 被整体切分 ⇒ 残留只有空白
      const ok = scanCssSpacing('.a{gap:.35rem}', SELFTEST_SCALE, 1)
      if (ok.length !== 1) return false
      if (residueHasMissedNumber(ok[0].residue)) return false
      // ★ 反向：人为制造一个「被截断」的分量（漏掉前导小数点）⇒ 残留里出现小数点 ⇒ 必须被抓
      const truncated = [{ start: 1, end: 6, raw: '35rem', px: 560, kind: 'offScale' as const }]
      return residueHasMissedNumber(valueResidue('.35rem', truncated, 0))
    },
  },
  {
    id: 'S26',
    name: '★★ 具名值残留是合法的（`4px auto` 不算漏掉令牌）',
    run: () => {
      const ds = scanCssSpacing('.a{margin:4px auto}', SELFTEST_SCALE, 1)
      if (ds.length !== 1) return false
      return !residueHasMissedNumber(ds[0].residue)
    },
  },
  {
    id: 'S27',
    name: '★★ 发丝 rem **判红**（写法债），但换算后的 px 必须正确',
    run: () => {
      const c = classifySpaceValue('0.1rem', SELFTEST_SCALE)
      // ★ 判红（kind='rem'）且 px 正确（1.6）⇒ 迁移时按**等值 px** 落地，不落到 4px 档
      return c.kind === 'rem' && Math.abs(c.px - 1.6) < 1e-9
    },
  },
  {
    id: 'S28',
    name: '★★ 扫描器盲区计数必须为 0（正常 CSS 不该有残留数值）',
    run: () => {
      const res = scanSpacingScaleOn(
        ['.a{padding:4px 8px}.b{gap:.35rem}.c{margin:0 auto}.d{padding:var(--kx-space-2)}'],
        SELFTEST_SCALE,
      )
      return res.facts.missedTokenDecls === 0
    },
  },
]

/** 在一段 CSS 文本上跑判据（自证用）。 */
export function scanSpacingScaleOn(cssTexts: string[], scalePx: number[]): SpacingScanResult {
  const decls: SpaceDecl[] = []
  for (const t of cssTexts) decls.push(...scanCssSpacing(t, scalePx, 1))
  const findings: SpacingFinding[] = []
  const facts: SpacingFacts = {
    totalDeclarations: 0,
    onScale: 0,
    offScale: 0,
    offScaleFiles: 0,
    offScaleComponents: 0,
    remSpacing: 0,
    tokenStyle: 0,
    dynamic: 0,
    zero: 0,
    hairline: 0,
    negative: 0,
    missedTokenDecls: 0,
    exempted: 0,
  }
  for (const d of decls) {
    facts.totalDeclarations++
    if (residueHasMissedNumber(d.residue)) facts.missedTokenDecls++
    let hasOff = false
    let hasRem = false
    const parts: SpacingFinding['parts'] = []
    for (const c of d.components) {
      switch (c.kind) {
        case 'onScale': facts.onScale++; break
        case 'offScale':
          hasOff = true
          facts.offScaleComponents++
          {
            const { nearest, tie } = nearestSpaceSteps(c.px, scalePx)
            parts.push({ raw: c.raw, px: c.px, nearest, tie, start: c.start, end: c.end })
          }
          break
        case 'rem': hasRem = true; facts.remSpacing++; break
        case 'token': facts.tokenStyle++; break
        case 'zero': facts.zero++; break
        case 'negative': facts.negative++; break
        case 'hairline': facts.hairline++; break
        default: facts.dynamic++
      }
    }
    if (hasOff || hasRem) {
      if (hasOff) facts.offScale++
      findings.push({
        file: '(selftest)',
        line: d.line,
        selector: d.selector,
        property: d.property,
        value: d.raw,
        kind: hasRem ? 'rem' : 'offScale',
        parts: hasRem
          ? d.components
              .filter((c) => c.kind === 'rem')
              .map((c) => ({ raw: c.raw, px: c.px, nearest: NaN, tie: null, start: c.start, end: c.end }))
          : parts,
        inMedia: d.inMedia,
      })
    }
  }
  facts.offScaleFiles = findings.length ? 1 : 0
  return { findings, facts, decls }
}

export interface SelftestReport {
  passed: number
  total: number
  failed: string[]
}

export function runSpacingSelftest(): SelftestReport {
  const failed: string[] = []
  let passed = 0
  for (const c of SPACING_SELFTEST) {
    let ok = false
    try {
      ok = c.run()
    } catch {
      ok = false
    }
    if (ok) passed++
    else failed.push(`${c.id} ${c.name}`)
  }
  return { passed, total: SPACING_SELFTEST.length, failed }
}
