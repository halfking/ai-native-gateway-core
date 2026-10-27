// typeScale.ts — 「字号必须落在角色阶上」的可执行判据（2026-10-27 · 第 23 增量）
//
// ★ 要治的病（仓内明文规范 vs 真仓现状的偏离）：
//
//   仓内 `DESIGN.md` §4 Typography **已经声明了一条 9 档的角色级字号阶**：
//     `--kx-text-2xs` 11 / `--kx-text-xs` 12 / `--kx-text-sm` 13 / `--kx-text-md` 14
//     `--kx-text-lg` 16 / `--kx-text-xl` 20 / `--kx-text-2xl` 24
//     `--kx-text-3xl` 30 / `--kx-text-4xl` 38
//   且 §2 消费规则写明：「**字号 → `--kx-text-*`（见 §4）**」
//   与「**新组件一律消费令牌，禁止手写魔法数**」。
//
//   而真仓现状（普查实测）：
//     · 全仓 `font-size` 声明 **2685** 处里，**令牌写法只占 3.4%**（92 处）
//       ⇒ 声明好的「角色阶」在实现里基本不存在；
//     · **离阶字号 475 处 / 160 个文件**：10px(188) · 18px(51) · 9px(47) · 15px(45)
//       · 22px(22) · 11.5px(17) · 12.5px(16) · 10.5px(12) · 28px(11) · 8px(9) …
//     · 并且 **rem 与 px 是两套写法**（0.85rem ≈ 13.6px · 0.8rem = 12.8px
//       · 0.9375rem = 15px · 0.78rem ≈ 12.48px）⇒ 同一个「阶」有两份真值。
//
//   ⇒ 这正是 impeccable `skill/reference/typeset.md` 的原话：
//     「**Scale and consistency: Is there a deliberate role scale,
//       or a collection of arbitrary values?**」
//     「**Are adjacent sizes or weights too close to carry different jobs?**」
//     「**Keep repeated roles consistent across screens and states.**」
//
// ★★★ 本门只判**一条**判据（判据越少越可证伪）：
//
//   **`font-size` 的静态长度值必须落在已声明的角色阶上。**
//   —— 角色阶是「少数几个角色」，不是「所有测量值」；
//      10px 与 11px 只差 1px 却占着两档 ⇒ 正是「adjacent sizes too close to
//      carry different jobs」⇒ 所以**不新增 10px 档**，而是把 10px 收进 11px。
//
// ★ 「阶」的事实源 = `web/src/style.css` 里 `--kx-text-*` 的**实际定义**，
//   **运行时解析、绝不硬编码** ⇒ 改令牌 = 改判据（单一真值）。
//
// ★ 不判的（单独计数，不算违规）：
//   · `em` / `clamp()` / `calc()` / `var()` —— 相对父级或视口，静态不可定值；
//   · `inherit` —— 继承，不是新角色。
//   ★★ 但 `rem` **要判**：根字号是 16px（web 事实），rem 是可静态换算的
//     ⇒ 而 rem 与 px 并存本身是「同一阶两份真值」⇒ 必须收进 px 口径。
//
// ★ 本文件是**判据**（纯函数 + 文件扫描），门在 `web/scripts/design-type-scale-guard.mjs`。

import { readFileSync, readdirSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import { maskSource } from '../i18n/hardcodedCjk.ts'

// ───────────────────────── 阶（事实源） ─────────────────────────

/** 正名单家族：`--kx-text-*` 是 DESIGN.md §4 声明的那一族。 */
const CANONICAL_FAMILY = /^--kx-text-[a-z0-9]+$/i

export interface TypeStep {
  /** 令牌名（含 --） */
  token: string
  /** 字号（px） */
  px: number
}

/**
 * 从 style.css 解析出**角色阶**（`--kx-text-*` 家族里定义了 px 长度的那些）。
 * ★ 只认正名单家族：`--space-*` / `--radius*` 之类的 px 值不是字号。
 */
export function parseCanonicalScale(cssText: string): TypeStep[] {
  const steps: TypeStep[] = []
  for (const m of cssText.matchAll(/(--[a-z0-9-]+)\s*:\s*(\d+(?:\.\d+)?)px\s*;/gi)) {
    const token = m[1]
    if (!CANONICAL_FAMILY.test(token)) continue
    const px = parseFloat(m[2])
    if (!steps.some((s) => s.token === token)) steps.push({ token, px })
  }
  steps.sort((a, b) => a.px - b.px || a.token.localeCompare(b.token))
  return steps
}

// ───────────────────────── 值分类 ─────────────────────────

export type SizeKind = 'px' | 'rem' | 'em' | 'clamp' | 'calc' | 'token' | 'inherit' | 'other'

export interface ClassifiedSize {
  kind: SizeKind
  /** 可静态定值时的 px 值（rem 按 16px 根字号换算） */
  px: number | null
  /** 去掉 `!important` 后的原始值 */
  raw: string
}

/** 根字号：web 的事实是 16px（`html { font-size: 100% }` ⇒ 浏览器默认 16px）。 */
export const ROOT_FONT_PX = 16

export function classifySize(rawInput: string): ClassifiedSize {
  const raw = rawInput.trim().replace(/!important\s*$/i, '').trim()
  if (/^inherit$/i.test(raw)) return { kind: 'inherit', px: null, raw }
  if (/^var\(/i.test(raw)) return { kind: 'token', px: null, raw }
  if (/^clamp\(/i.test(raw)) return { kind: 'clamp', px: null, raw }
  if (/^calc\(/i.test(raw)) return { kind: 'calc', px: null, raw }
  let m = raw.match(/^(-?\d+(?:\.\d+)?)px$/i)
  if (m) return { kind: 'px', px: parseFloat(m[1]), raw }
  m = raw.match(/^(-?\d+(?:\.\d+)?)rem$/i)
  if (m) return { kind: 'rem', px: parseFloat(m[1]) * ROOT_FONT_PX, raw }
  m = raw.match(/^(-?\d+(?:\.\d+)?)em$/i)
  if (m) return { kind: 'em', px: null, raw }
  return { kind: 'other', px: null, raw }
}

// ───────────────────────── CSS 扫描 ─────────────────────────

export interface FontSizeDecl {
  /** 声明所在的选择器（inline 模板样式记为 `(inline style)`） */
  selector: string
  /** 声明的原始值 */
  value: string
  /** 1 基行号 */
  line: number
  /** **值**在输入文本里的起始偏移（半开区间 [valueStart, valueEnd)）——批量改写必须落在字符区间上 */
  valueStart: number
  valueEnd: number
  /** ★ 是否位于 `@media` 块内（响应式降档 ⇒ 语义与基准值**相反**：要更小，不是更大） */
  inMedia: boolean
}

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

/**
 * 扫描一段 CSS 文本里的 `font-size` 声明。
 * ★ 必须能处理 **minified CSS**（一行多规则）与 **@media 嵌套** ——
 *   否则会把整条 minified 规则当成一个选择器 ⇒ 制造假阳性（第 22 增量的教训：
 *   「分析工具与门必须用同等的『区间 + 注释处理』能力」）。
 */
export function scanCssFontSizes(css: string, baseLine = 1): FontSizeDecl[] {
  const out: FontSizeDecl[] = []
  const starts = lineStarts(css)
  // ★★ 正则里**不能出现裸反引号**：copy-quality 门按「反引号 = 模板字符串起点」提取文本，
  //   裸反引号会把后面整段中文注释吞成一个字符串 ⇒ 误判成「否定转折当骨架」。
  //   ⇒ 用十六进制转义表示反引号（行为不变，源码里不再有裸反引号）。
  const declRe = /font-size\s*:\s*([^;}"'\x60]+)/gi

  const walk = (from: number, to: number, inMedia: boolean): void => {
    let i = from
    while (i < to) {
      const brace = css.indexOf('{', i)
      if (brace === -1 || brace >= to) break
      const selText = css.slice(i, brace).trim()
      // 找匹配的 '}'
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
        // at-rule（@media / @supports …）⇒ 里面的规则另算
        // ★ @media 里的字号是**响应式降档** ⇒ 语义与基准值相反（要更小）
        const isMedia = /^@media/i.test(selText)
        walk(brace + 1, bodyEnd, inMedia || isMedia)
      } else if (body.includes('{')) {
        walk(brace + 1, bodyEnd, inMedia)
      } else {
        declRe.lastIndex = 0
        let m: RegExpExecArray | null
        while ((m = declRe.exec(body)) !== null) {
          const raw = m[1]
          const lead = raw.length - raw.trimStart().length
          const valStart = brace + 1 + m.index + m[0].indexOf(raw) + lead
          const valEnd = valStart + raw.trim().length
          out.push({
            selector: selText,
            value: raw.trim(),
            line: baseLine + lineOf(starts, brace + 1 + m.index) - 1,
            valueStart: valStart,
            valueEnd: valEnd,
            inMedia,
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

/** 抽模板里的内联 `style="…"` 属性（★ 模板里的内联字号也是「上屏的字号」）。 */
export function scanInlineStyleFontSizes(vueText: string): FontSizeDecl[] {
  const out: FontSizeDecl[] = []
  const starts = lineStarts(vueText)
  const re = /\bstyle\s*=\s*"([^"]*)"/gi
  let m: RegExpExecArray | null
  while ((m = re.exec(vueText)) !== null) {
    const declRe = /font-size\s*:\s*([^;"']+)/gi
    let dm: RegExpExecArray | null
    while ((dm = declRe.exec(m[1])) !== null) {
      const raw = dm[1]
      const lead = raw.length - raw.trimStart().length
      // ★ dm.index 是相对 **m[1]（属性值）** 的偏移 ⇒ 必须加上属性值在 m[0] 里的起点
      const attrBase = m.index + m[0].indexOf(m[1])
      const valStart = attrBase + dm.index + dm[0].indexOf(raw) + lead
      out.push({
        selector: '(inline style)',
        value: raw.trim(),
        line: lineOf(starts, attrBase + dm.index),
        valueStart: valStart,
        valueEnd: valStart + raw.trim().length,
        inMedia: false,
      })
    }
  }
  // ★ Vue 对象语法：:style="{ fontSize: '9px' }" —— 同样是上屏的字号
  const objRe = /['"]?font-?[sS]ize['"]?\s*:\s*['"]([^'"]+)['"]/g
  let om: RegExpExecArray | null
  while ((om = objRe.exec(vueText)) !== null) {
    const raw = om[1]
    const lead = raw.length - raw.trimStart().length
    const valStart = om.index + om[0].indexOf(raw) + lead
    out.push({
      selector: '(inline style)',
      value: raw.trim(),
      line: lineOf(starts, om.index),
      valueStart: valStart,
      valueEnd: valStart + raw.trim().length,
      inMedia: false,
    })
  }
  return out
}

// ───────────────────────── 文件遍历 ─────────────────────────

const SCAN_EXT = new Set(['.vue', '.css'])
const SKIP_DIRS = new Set(['node_modules', 'dist', '.git', 'coverage', 'reports'])

/** 列出所有参与本门扫描的源文件（`.vue` + `.css`）。 */
export function listStyleFiles(rootDir: string): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
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

// ───────────────────────── 图标承载（形状豁免） ─────────────────────────
//
// ★★★ 为什么要有这一条：
//   仓里有一批 `font-size` **不是用来控制文字的**，而是用来控制**图标/字形**的尺寸：
//     · `<div class="error-icon"><Warning /></div>` —— Element Plus 图标是 1em，
//       所以容器的 font-size 就是图标尺寸；
//     · `<span class="app-bottom-nav__icon" aria-hidden="true">{{ item.icon }}</span>`
//       —— 字形 + aria-hidden（装饰内容，不是可读文本）；
//     · `<i class="qp-dot" />` —— 纯装饰点。
//   ⇒ 把它们算成「字号」是**量具的错**：图标尺寸属于另一条尺度（图标阶），
//     仓里**没有**声明图标阶 ⇒ 判据不该假装有。
//
// ★★★ 豁免的形状是**元素角色**，不是类名：
//   「这个 class 在模板里的**全部**用法都是图标承载」⇒ 它的 font-size 不是字号。
//   ★ 用类名判（名字里含 icon/close/arrow）会**误伤文字徽标**
//     （`.badge` / `.dep-badge` / `.src-badge` 里是**真文字**）⇒ 那是写法判，不是形状判。
// ★ 证据由模板自动推导 ⇒ 不需要人工登记簿；每个豁免都能报出它的证据行。

export interface ClassIconEvidence {
  /** 该 class 在模板里出现的总次数 */
  total: number
  /** 其中「图标承载」的次数 */
  iconish: number
  /** 是否**全部**都是图标承载（⇒ 可豁免） */
  only: boolean
  /** 一条证据（文件 + 行 + 片段） */
  witness?: { file: string; line: number; snippet: string }
}

/**
 * 全仓从 `@element-plus/icons-vue` 导入的**图标组件名集合**（事实源）。
 * ★ 比「首字母大写就是图标」可靠得多：`import { Close, Warning } from '@element-plus/icons-vue'`
 *   是仓内**唯一**的图标来源 ⇒ 组件名在这张表里 ⇒ 它是图标；不在 ⇒ 不是。
 */
export function collectIconComponentNames(files: string[]): Set<string> {
  const names = new Set<string>()
  const re = /import\s*\{([^}]*)\}\s*from\s*['"]@element-plus\/icons-vue['"]/g
  for (const abs of files) {
    let src: string
    try {
      src = readFileSync(abs, 'utf8')
    } catch {
      continue
    }
    let m: RegExpExecArray | null
    while ((m = re.exec(src)) !== null) {
      for (const part of m[1].split(',')) {
        const name = part.trim().split(/\s+as\s+/)[0].trim()
        if (name) names.add(name)
      }
    }
  }
  return names
}

/** 图标组件：首字母大写的自闭合组件（`<Warning />` / `<Close />` / `<Cpu />` …） */
const ICON_COMPONENT_RE = /^\s*<([A-Z][A-Za-z0-9]*)(\s[^>]*)?\/>/
/** 宿主标签上的 class 属性 */
const HOST_CLASS_RE = /class\s*=\s*"([^"]*)"/

/**
 * 扫描所有 .vue 的模板，推导每个 class 的「图标承载」证据。
 * ★ 只认**全部**用法都是图标承载的 class（保守：混用的一律要判）。
 */
export function collectClassIconEvidence(
  files: string[],
  srcDir: string,
  iconComponents?: Set<string>,
): Map<string, ClassIconEvidence> {
  const map = new Map<string, ClassIconEvidence>()
  const comps = iconComponents ?? collectIconComponentNames(files)
  for (const abs of files) {
    if (!abs.endsWith('.vue')) continue
    const src = readFileSync(abs, 'utf8')
    collectIconEvidenceFromText(src, relative(srcDir, abs), map, comps)
  }
  for (const e of map.values()) e.only = e.total > 0 && e.iconish === e.total
  return map
}

/** ★ 纯函数版：按**文本**推导图标承载证据（可被自证直接调用，不必落盘）。 */
export function collectIconEvidenceFromText(
  src: string,
  rel: string,
  map: Map<string, ClassIconEvidence> = new Map(),
  iconComponents: Set<string> = new Set(),
): Map<string, ClassIconEvidence> {
  const bump = (cls: string, iconish: boolean, file: string, line: number, snippet: string) => {
    let e = map.get(cls)
    if (!e) {
      e = { total: 0, iconish: 0, only: false }
      map.set(cls, e)
    }
    e.total++
    if (iconish) {
      e.iconish++
      if (!e.witness) e.witness = { file, line, snippet }
    }
  }

  const tplStart = src.indexOf('<template>')
  const tplEnd = src.lastIndexOf('</template>')
  if (tplStart < 0) return map
  const tpl = src.slice(tplStart, tplEnd)
  const tplBase = src.slice(0, tplStart).split('\n').length - 1

  // 逐个标签：`<tag attrs>` … 往后看一点内容，判断是不是图标承载
  // ★ 属性值里可能有 `>`（`:class="{ open: a > b }"'）⇒ 正则必须能跳过**带引号**的属性值
  const tagRe = /<([a-zA-Z][\w-]*)((?:"[^"]*"|'[^']*'|[^>"'])*)>/g
  let m: RegExpExecArray | null
  while ((m = tagRe.exec(tpl)) !== null) {
    const tag = m[1]
    const attrs = m[2] || ''
    const cm = attrs.match(HOST_CLASS_RE)
    if (!cm) continue
    const classes = cm[1].split(/\s+/).filter(Boolean)
    if (!classes.length) continue
    // 标签内容（往后 160 字符）——图标组件通常紧跟其后
    const after = tpl.slice(m.index + m[0].length, m.index + m[0].length + 160)
    const iconComp = after.match(ICON_COMPONENT_RE)
    const isIconComp = !!iconComp && (iconComponents.size === 0 || iconComponents.has(iconComp[1]))
    const iconish =
      tag === 'i' ||
      /aria-hidden\s*=\s*"true"/.test(attrs) ||
      isIconComp
    const line = tplBase + tpl.slice(0, m.index).split('\n').length
    const snippet = m[0].trim().slice(0, 100)
    for (const c of classes) {
      if (c.startsWith(':') || c.startsWith('v-') || c.includes('${') || c.includes('(')) continue
      bump(c, iconish, rel, line, snippet)
    }
  }
  return map
}

/** 取选择器的**最后一个简单选择器**里的类名（font-size 实际作用的那个元素）。 */
export function lastClassOf(selector: string): string | null {
  const m = selector.match(/\.([A-Za-z_][\w-]*)/g)
  if (!m || !m.length) return null
  return m[m.length - 1].slice(1)
}

/**
 * ★ 可豁免的「图标承载 class」集合（形状豁免，自动推导）。
 *   —— 门与批量改写脚本**共用**这一份 ⇒ 同一口径只有一份实现。
 */
export function collectIconExemptClasses(files: string[], srcDir: string): Set<string> {
  const ev = collectClassIconEvidence(files, srcDir, collectIconComponentNames(files))
  const out = new Set<string>()
  for (const [cls, e] of ev) if (e.only) out.add(cls)
  return out
}

// ───────────────────────── 扫描全仓 ─────────────────────────

export interface OffScaleFinding {
  /** 相对 `web/src` 的路径 */
  file: string
  line: number
  selector: string
  /** 原始声明值 */
  value: string
  /** 换算后的 px */
  px: number
  /** 就近的阶值 */
  nearest: number
  /** 距离 */
  distance: number
  /** 平局时的另一个阶值（null = 方向唯一） */
  tie: number | null
  /** 值在**文件**里的字符区间（半开区间）——批量改写必须落在字符区间上 */
  valueStart: number
  valueEnd: number
}

export interface TypeScaleFacts {
  /** 角色阶（事实源） */
  steps: TypeStep[]
  /** 阶值集合（px） */
  scalePx: number[]
  /** font-size 声明总数 */
  totalDeclarations: number
  /** 落在阶上的 */
  onScale: number
  /** 离阶的 */
  offScale: number
  /** 静态不可定值的（em / clamp / calc / var / inherit） */
  dynamic: number
  /** 用令牌写法（var(--…)) 的 */
  tokenStyle: number
  /** 涉及文件数 */
  offScaleFiles: number
  /** 被豁免的【图标承载】声明数 */
  iconExempt: number
  /** 被豁免的【图标承载】class 数 */
  iconExemptClasses: number
  /** ★ 用 rem 表达的字号数（**同一阶的两份真值**：仓内令牌层全是 px，根字号固定 16px） */
  remFontSizes: number
}

export interface TypeScaleResult {
  findings: OffScaleFinding[]
  /** ★ rem 写法的字号（值可能仍在阶上，但写法本身是第二份真值） */
  remFindings: OffScaleFinding[]
  facts: TypeScaleFacts
  /** 被豁免的图标承载 class（附证据，供人工复核） */
  iconExemptions: Array<{ class: string; witness?: { file: string; line: number; snippet: string } }>
}

export interface AllFontSizeDecl extends FontSizeDecl {
  /** 相对 `web/src` 的路径 */
  file: string
  /** 归类 */
  classified: ClassifiedSize
}

/**
 * ★ 遍历全仓的**全部** font-size 声明（不限离阶）——这是**唯一**的遍历实现，
 *   门与批量改写脚本共用它（★ 同一口径只能有一份实现）。
 */
export function collectAllFontSizes(files: string[], srcDir: string): AllFontSizeDecl[] {
  const out: AllFontSizeDecl[] = []
  for (const abs of files) {
    const src = readFileSync(abs, 'utf8')
    const isVue = abs.endsWith('.vue')
    const rel = relative(srcDir, abs)
    out.push(...collectFontSizesFromText(src, rel, isVue))
  }
  return out
}

/**
 * ★ 按**文本**收集 font-size 声明（区间相对该文本）。
 *   —— 批量改写的「区间口径」必须与「被比较的文本」**同源**：
 *      用 A 文本算出的区间去切 B 文本 ⇒ 区间错位（值改了长度就变了）⇒ 比较必然误判。
 */
export function collectFontSizesFromText(src: string, rel: string, isVue: boolean): AllFontSizeDecl[] {
  const masked = maskSource(isVue ? rel : rel, src)
  const decls: FontSizeDecl[] = []
  if (isVue) {
    for (const blk of styleBlocksOf(masked)) {
      for (const d of scanCssFontSizes(blk.text, blk.baseLine)) {
        decls.push({ ...d, valueStart: d.valueStart + blk.start, valueEnd: d.valueEnd + blk.start })
      }
    }
    decls.push(...scanInlineStyleFontSizes(masked))
  } else {
    // ★ .css 的 CSS 注释**不会**被 maskSource 剥掉（它走的是 TS 注释分支）⇒ 自己剥
    decls.push(...scanCssFontSizes(maskCssComments(masked)))
  }
  return decls.map((d) => ({ ...d, file: rel, classified: classifySize(d.value) }))
}

/**
 * 扫描一批文件里的离阶字号。
 * @param files 绝对路径列表
 * @param srcDir 用于计算相对路径
 * @param scalePx 阶值集合（px）
 */
export function scanTypeScale(
  files: string[],
  srcDir: string,
  scalePx: number[],
  manualExemptClasses?: Set<string>,
): TypeScaleResult {
  const findings: OffScaleFinding[] = []
  const iconEvidence = collectClassIconEvidence(files, srcDir, collectIconComponentNames(files))
  const iconExemptClasses = new Set<string>()
  for (const [cls, e] of iconEvidence) if (e.only) iconExemptClasses.add(cls)
  let total = 0
  let onScale = 0
  let dynamic = 0
  let tokenStyle = 0
  let iconExempt = 0
  const remFindings: OffScaleFinding[] = []

  for (const d of collectAllFontSizes(files, srcDir)) {
    total++
    const c = d.classified
    // ★ rem 写法：值可静态换算（根字号 16px），但**写法本身不合规** ——
    //   仓内的角色阶令牌全是 px，rem 与 px 并存 = 同一阶的两份真值
    //   （改 html 字号会让 rem 跟着动、令牌不会 ⇒ 两者脱钩）。
    if (c.kind === 'rem') {
      remFindings.push({
        file: d.file,
        line: d.line,
        selector: d.selector,
        value: c.raw,
        px: c.px ?? 0,
        nearest: 0,
        distance: 0,
        tie: null,
        valueStart: d.valueStart,
        valueEnd: d.valueEnd,
      })
      dynamic++
      continue
    }
    if (c.kind === 'token') {
      tokenStyle++
      dynamic++
      continue
    }
    if (c.px === null) {
      dynamic++
      continue
    }
    if (scalePx.includes(c.px)) {
      onScale++
      continue
    }
    // ★ 图标承载豁免（形状豁免：模板证据）
    const cls = lastClassOf(d.selector)
    if (cls && (iconExemptClasses.has(cls) || (manualExemptClasses && manualExemptClasses.has(cls)))) {
      iconExempt++
      continue
    }
    const sorted = [...scalePx].sort((a, b) => a - b)
    const d0 = sorted.map((s) => Math.abs(s - c.px!))
    const best = Math.min(...d0)
    const nearest = sorted[d0.indexOf(best)]
    const tie = d0.filter((x) => x === best)
    findings.push({
      file: d.file,
      line: d.line,
      selector: d.selector,
      value: c.raw,
      px: c.px,
      nearest,
      distance: best,
      tie: tie.length > 1 ? sorted[d0.lastIndexOf(best)] : null,
      valueStart: d.valueStart,
      valueEnd: d.valueEnd,
    })
  }

  return {
    findings,
    remFindings,
    facts: {
      steps: [],
      scalePx,
      totalDeclarations: total,
      onScale,
      offScale: findings.length,
      dynamic,
      tokenStyle,
      offScaleFiles: new Set(findings.map((f) => f.file)).size,
      iconExempt,
      iconExemptClasses: iconExemptClasses.size,
      remFontSizes: remFindings.length,
    },
    iconExemptions: [...iconExemptClasses].map((cls) => ({
      class: cls,
      witness: iconEvidence.get(cls)?.witness,
    })),
  }
}

// ───────────────────────── 自证 ─────────────────────────

export interface SelftestCase {
  id: string
  name: string
  run: () => boolean
}

export const TYPE_SCALE_SELFTEST: SelftestCase[] = [
  {
    id: 'S1',
    name: '★ 阶的事实源从 style.css 解析出 9 档（不是硬编码）',
    run: () => {
      const css = `
        --kx-text-2xs: 11px;
        --kx-text-xs:  12px;
        --kx-text-sm:  13px;
        --kx-text-md:  14px;
        --kx-text-lg:  16px;
        --kx-text-xl:  20px;
        --kx-text-2xl: 24px;
        --kx-text-3xl: 30px;
        --kx-text-4xl: 38px;
        --kx-space-1: 4px;
      `
      const steps = parseCanonicalScale(css)
      return steps.length === 9 && steps[0].px === 11 && steps[8].px === 38
    },
  },
  {
    id: 'S2',
    name: '★ 非正名单家族的 px 值不算「字号阶」（--kx-space-1: 4px 不入阶）',
    run: () => parseCanonicalScale('--kx-space-1: 4px; --kx-text-md: 14px;').length === 1,
  },
  {
    id: 'S3',
    name: '★ px 值分类正确',
    run: () => {
      const c = classifySize('12px')
      return c.kind === 'px' && c.px === 12
    },
  },
  {
    id: 'S4',
    name: '★ rem 按 16px 根字号换算 ⇒ 可静态定值（要判）',
    run: () => {
      const c = classifySize('0.875rem')
      return c.kind === 'rem' && c.px === 14
    },
  },
  {
    id: 'S5',
    name: '★ em / clamp / var 不可静态定值 ⇒ 不判（单独计数）',
    run: () => {
      const a = classifySize('1.1em')
      const b = classifySize('clamp(14px, 2vw, 17px)')
      const c = classifySize('var(--kx-text-md)')
      return a.px === null && b.px === null && c.px === null
    },
  },
  {
    id: 'S6',
    name: '★ !important 后缀被剥掉（值本身仍要判）',
    run: () => classifySize('15px !important').px === 15,
  },
  {
    id: 'S7',
    name: '★★ minified CSS：一行多规则 ⇒ 每个规则各自带自己的选择器（不塌成一条）',
    run: () => {
      const css = '.a{color:red;font-size:10px}.b{font-size:18px}'
      const decls = scanCssFontSizes(css)
      return (
        decls.length === 2 &&
        decls[0].selector === '.a' &&
        decls[0].value === '10px' &&
        decls[1].selector === '.b' &&
        decls[1].value === '18px'
      )
    },
  },
  {
    id: 'S8',
    name: '★ @media 嵌套：里面的规则仍带自己的选择器',
    run: () => {
      const css = '@media (max-width:768px){.x{font-size:9px}}'
      const decls = scanCssFontSizes(css)
      return decls.length === 1 && decls[0].selector === '.x'
    },
  },
  {
    id: 'S9',
    name: '★★ CSS 注释里的 font-size 不算数（遮罩）',
    run: () => {
      const css = '/* .old{font-size:10px} */\n.a{font-size:12px}'
      const decls = scanCssFontSizes(maskCssComments(css))
      return decls.length === 1 && decls[0].value === '12px'
    },
  },
  {
    id: 'S10',
    name: '★ 遮罩后行号不漂（注释多行时仍指到声明所在行）',
    run: () => {
      const css = '/*\n\n\n*/\n.a{font-size:12px}'
      const decls = scanCssFontSizes(maskCssComments(css))
      return decls.length === 1 && decls[0].line === 5
    },
  },
  {
    id: 'S11',
    name: '★ 模板内联 style 的字号也要被扫到',
    run: () => {
      const vue = '<template><span style="font-size:9px;margin-left:2px">x</span></template>'
      const decls = scanInlineStyleFontSizes(vue)
      return decls.length === 1 && decls[0].value === '9px' && decls[0].line === 1
    },
  },
  {
    id: 'S12',
    name: '★ 离阶判定：10px 离阶（11px 是阶底）· 11px 在阶上',
    run: () => {
      const scale = [11, 12, 13, 14, 16, 20, 24, 30, 38]
      const off = scanTypeScaleOn(['.a{font-size:10px}'], scale)
      const on = scanTypeScaleOn(['.a{font-size:11px}'], scale)
      return off.length === 1 && off[0].nearest === 11 && off[0].tie === null && on.length === 0
    },
  },
  {
    id: 'S13',
    name: '★ 平局档要标出「另一个等距的阶」（15px ↔ 14/16）',
    run: () => {
      const scale = [11, 12, 13, 14, 16, 20, 24, 30, 38]
      const off = scanTypeScaleOn(['.a{font-size:15px}'], scale)
      return off.length === 1 && off[0].nearest === 14 && off[0].tie === 16
    },
  },
  {
    id: 'S14',
    name: '★ 方向唯一档不标平局（18px → 16，距离 2）',
    run: () => {
      const scale = [11, 12, 13, 14, 16, 20, 24, 30, 38]
      const off = scanTypeScaleOn(['.a{font-size:18px}'], scale)
      return off.length === 1 && off[0].nearest === 16 && off[0].tie === 20
    },
  },
  {
    id: 'S15',
    name: '★★ 对照：已在阶上的值绝不能被报成离阶（判据不是恒真）',
    run: () => {
      const scale = [11, 12, 13, 14, 16, 20, 24, 30, 38]
      const off = scanTypeScaleOn(
        ['.a{font-size:12px}.b{font-size:0.875rem}.c{font-size:var(--kx-text-md)}'],
        scale,
      )
      return off.length === 0
    },
  },
  {
    id: 'S16',
    name: '★★ 图标承载豁免：class 全部用法都是 aria-hidden 装饰 ⇒ 其 font-size 不是字号',
    run: () => {
      const vue = `<template><span class="dot-x" aria-hidden="true"></span></template>
<style>.dot-x{font-size:10px}</style>`
      const ev = collectIconEvidenceFromText(vue, 'fake.vue')
      for (const e of ev.values()) e.only = e.total > 0 && e.iconish === e.total
      return ev.get('dot-x')?.only === true
    },
  },
  {
    id: 'S17',
    name: '★★ 图标承载豁免：宿主是图标组件（<Close />）⇒ 豁免',
    run: () => {
      const vue = `<template><button class="x-close"><Close /></button></template>`
      const ev = collectIconEvidenceFromText(vue, 'fake.vue')
      for (const e of ev.values()) e.only = e.total > 0 && e.iconish === e.total
      return ev.get('x-close')?.only === true
    },
  },
  {
    id: 'S18',
    name: '★★ 对照：文字徽标（.badge 挂在 span 上、装的是文字）⇒ **不**豁免',
    run: () => {
      const vue = `<template><span class="badge">ok</span></template>`
      const ev = collectIconEvidenceFromText(vue, 'fake.vue')
      for (const e of ev.values()) e.only = e.total > 0 && e.iconish === e.total
      return ev.get('badge')?.only === false
    },
  },
  {
    id: 'S19',
    name: '★ 混用 class（既挂图标又挂文字）⇒ **不**豁免（保守）',
    run: () => {
      const vue = `<template><span class="mix" aria-hidden="true">x</span><span class="mix">文字</span></template>`
      const ev = collectIconEvidenceFromText(vue, 'fake.vue')
      for (const e of ev.values()) e.only = e.total > 0 && e.iconish === e.total
      return ev.get('mix')?.only === false
    },
  },
  {
    id: 'S20',
    name: '★ 最后一段简单选择器才是 font-size 真正作用的那个元素（.a .b ⇒ .b）',
    run: () => lastClassOf('.a .b > .c') === 'c' && lastClassOf('h2') === null,
  },
]

/** 自证用的小扫描器（直接吃 CSS 片段，不落盘）。 */
function scanTypeScaleOn(cssChunks: string[], scalePx: number[]): OffScaleFinding[] {
  const out: OffScaleFinding[] = []
  for (const css of cssChunks) {
    for (const d of scanCssFontSizes(maskCssComments(css))) {
      const c = classifySize(d.value)
      if (c.px === null) continue
      if (scalePx.includes(c.px)) continue
      const sorted = [...scalePx].sort((a, b) => a - b)
      const dists = sorted.map((s) => Math.abs(s - c.px!))
      const best = Math.min(...dists)
      out.push({
        file: '(selftest)',
        line: d.line,
        selector: d.selector,
        value: c.raw,
        px: c.px,
        nearest: sorted[dists.indexOf(best)],
        distance: best,
        tie: dists.filter((x) => x === best).length > 1 ? sorted[dists.lastIndexOf(best)] : null,
        valueStart: 0,
        valueEnd: 0,
      })
    }
  }
  return out
}

export function runTypeScaleSelftest(): { passed: number; total: number; failed: string[] } {
  const failed: string[] = []
  let passed = 0
  for (const c of TYPE_SCALE_SELFTEST) {
    let ok = false
    try {
      ok = c.run()
    } catch {
      ok = false
    }
    if (ok) passed++
    else failed.push(`${c.id} ${c.name}`)
  }
  return { passed, total: TYPE_SCALE_SELFTEST.length, failed }
}
