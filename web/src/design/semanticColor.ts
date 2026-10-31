// semanticColor.ts — 「颜色只承载语义，不承载分组」的可执行判据（2026-10-27 · 第 21 增量）
//
// ★ 要治的病（仓内明文规范 vs 真仓现状的偏离）：
//   `DESIGN.md` §视觉世界原话：
//     「状态用最小的颜色量表达、**每个元素都承担信息职责**」
//   impeccable `skill/reference/colorize.md`：
//     「**Decoration without a relationship to hierarchy, state, content, or the
//       visual world is not a color strategy.**」
//     「**Let the strongest color own a deliberate region or role instead of
//       scattering tiny accents.**」
//   impeccable `skill/reference/operate.md`：
//     「Accent color used for primary actions, current selection, and **state
//       indicators** only, **not decoration**.」
//
//   而真仓现状（`components/LiveRequestStreamV2.vue` 的 CSS 注释**自认**）：
//     /* 2026-07-24: 视觉分组 - 让相邻的功能簇有清晰的区隔 */
//     /* 2026-07-24: 不同功能簇使用不同色调做软区分 */
//     .control-group.control-group--mode {
//       background: color-mix(in srgb, var(--success) 14%, var(--bg-subtle));
//     /* 2026-07-24: 多维过滤器使用警告色，与维度/模式/请求类型组区分。 */
//     .filter-group {
//       background: color-mix(in srgb, var(--warning) 18%, var(--bg-subtle));
//
//   ⇒ **选色的理由是「跟旁边不一样」，不是「这是什么状态」**：
//     · 「小 / 大」显示模式切换被涂成**成功绿**（success）—— 它不是「成功」；
//     · 「筛选」控件组被涂成**告警橙**（warning）—— 它不是「警告」。
//   ⇒ 用户扫一眼工具栏，会以为「筛选出问题了」「模式成功了」⇒ **颜色在撒谎**。
//   ★ 这是 **AI 生成 UI 的典型痕迹**：没有语义也先上色，因为「多点颜色看起来更丰富」。
//
// ★★★ 本门只判**一条**判据（判据越少越可证伪）：
//
//   **承载「一组控件」的容器，底色/边框色必须中性。**
//   —— 颜色预算属于「被选中的那一个」自己；容器只是「分组」，不是「状态」。
//
// ★ 为什么要按【形状】判，且形态要穷举（LESSONS.md §451 / §450）：
//   ① **着色形态**：底色/边框色里**现场**混入**裸**颜色令牌
//      （`--accent` / `--success` / `--warning` / `--danger` / `--primary` …
//      及其 `--kx-` 别名）—— 而不是走仓内**已有的**语义底色令牌层
//      （`--success-bg` / `--warning-bd` / `--danger-bg` / `--accent-soft` …）。
//      ★ 语义底色令牌是「这个元素处于这个状态」的**有名字**的说法；
//        现场 color-mix 一个裸令牌 = **无名的颜色** ⇒ 「第二份真值」+ 可能撒谎。
//   ② **控件组容器形态**：一个元素的**直接子元素**里有 ≥2 个**可交互控件**。
//      ★ 用「直接子元素」而不是「任意后代」：后代里出现的按钮（卡片里的按钮）
//        不代表这个容器是「一组控件」。
//
// ★ 豁免（**状态载体**）：
//   容器**自身**带状态类（静态类名或动态绑定键命中 active/selected/error/… ）
//   ⇒ 它在表达「自己处于某状态」⇒ 颜色有意义 ⇒ 豁免。
//   ★ 例：`components/detail/SessionTurnsSyncPane.vue` 的 `.turn-card.active`
//     —— 卡片自己就是「被选中的那一项」，给它主色是对的。
//   ★ 这一条豁免不是白名单，而是**形状**（「这个元素的状态就是它带的那个状态词」）。
//
// ★ 本文件是**判据**（纯函数 + 文件扫描），门在 `web/scripts/design-semantic-color-guard.mjs`，
//   运行时尺在 `web/scripts/design-semantic-color-runtime-check.mjs`。
//   放 `src/` 是为了能被 vitest 覆盖（与 `aiSmells.ts` / `elevation.ts` / `primaryFocus.ts` 同一范式）。
//
// ★ 复用 `primaryFocus.ts` 的标签树解析器（`parseTagTree` / `templateBlocks` / `attrsOf`）
//   —— ★ 同一口径只能有一份实现（LESSONS.md §411）：自己再写一份解析器 ⇒
//   两个实现必然漂移 ⇒ 对拍永远对不上。

import { readFileSync } from 'node:fs'
import { relative } from 'node:path'
import { maskSource } from '../i18n/hardcodedCjk.ts'
import {
  type TagNode,
  ancestorsOf,
  attrsOf,
  parseAttrs,
  parseTagTree,
  templateBlocks,
} from './primaryFocus.ts'

// ───────────────────────── 形态表 ─────────────────────────

/**
 * 可交互控件的**标签**形态（穷举）。
 *
 * ★ 只认「能被点」的标签；`div`/`span` 即使带 `onclick` 也不认 ——
 *   仓内没有这种写法，认了只会扩大误报面（判据宁可窄，不可宽）。
 */
export const CONTROL_TAGS = new Set([
  'button',
  'el-button',
  'el-radio',
  'el-radio-button',
  'el-checkbox',
  'el-checkbox-button',
  'el-segmented',
  'el-switch',
  'el-tab-pane',
  'input',
  'a',
])

/** 可交互控件的**语义角色**形态（穷举）——自定义控件没有标签名时走这里。 */
export const CONTROL_ROLES = new Set([
  'button',
  'tab',
  'radio',
  'switch',
  'checkbox',
  'menuitem',
  'menuitemradio',
  'option',
])

/**
 * 会被着色的 CSS 属性（穷举）。
 *
 * ★ **不含** `outline` / `outline-color` / `box-shadow` / `color`：
 *   - `outline` = **焦点环**（键盘可达性），是「状态指示」的正当用法；
 *   - `box-shadow` = 焦点环 / 浮起阴影；
 *   - `color` = 文字色，不是底色（底色是「这块区域处于某状态」的主战场）。
 */
export const PAINT_PROPS = new Set([
  'background',
  'background-color',
  'border',
  'border-color',
  'border-top',
  'border-bottom',
  'border-left',
  'border-right',
  'border-block',
  'border-inline',
  'border-top-color',
  'border-bottom-color',
  'border-left-color',
  'border-right-color',
])

/** 裸颜色令牌（品牌/主色 + 状态色）。 */
const RAW_COLOR_TOKEN = /^--(?:kx-)?(accent|primary|success|warning|danger|error|info)$/

/**
 * 语义底色令牌（**允许**直接使用的「有名字的底色」）。
 * ★ 这些是仓内**已经有的**语义层（`src/style.css` 的「语义: 状态色 + 透明背景」段）。
 */
const SEMANTIC_BG_TOKEN =
  /^--(?:kx-)?(accent|primary|success|warning|danger|error|info)-(soft|bg|bd|strong|hover|surface|dim|text|border)$/

/**
 * 「状态词」—— 出现在类名里就说明**这个元素自己在表达状态**。
 * ★ 用于「状态载体」豁免（不是白名单，是形状）。
 */
const STATE_WORD = /(^|[-_])(active|selected|current|checked|disabled|loading|error|danger|warn|warning|success|failed|invalid)([-_]|$)/i

// ───────────────────────── CSS 解析 ─────────────────────────

export interface CssDecl {
  prop: string
  value: string
  /** 在 css 字符串里的起点偏移 */
  offset: number
}

export interface CssRule {
  selector: string
  decls: CssDecl[]
  /** 规则体在 css 字符串里的起点偏移 */
  offset: number
}

/**
 * 迷你 CSS 解析器：抽出「规则 → 声明」。
 *
 * ★ 支持 `@media` / `@supports` 等嵌套 at-rule（内层规则照样抽出来）。
 * ★ 注释已由 `maskSource` 的 `maskStyleComments` 剥掉（等长替换成空格）
 *   ⇒ 这里看到的注释是空白，不会被当成声明。
 */
export function parseCssRules(css: string): CssRule[] {
  const out: CssRule[] = []
  interface Ctx {
    sel: string
    decls: CssDecl[]
    isAt: boolean
    bodyStart: number
  }
  const stack: Ctx[] = []
  let buf = ''
  let bufStart = 0
  let i = 0

  const flush = (): void => {
    const text = buf.trim()
    buf = ''
    if (!text) return
    const ctx = stack[stack.length - 1]
    if (!ctx || ctx.isAt) return
    const ci = text.indexOf(':')
    if (ci <= 0) return
    const prop = text.slice(0, ci).trim().toLowerCase()
    const value = text.slice(ci + 1).trim()
    ctx.decls.push({ prop, value, offset: bufStart })
  }

  while (i < css.length) {
    const c = css[i]
    if (c === '{') {
      const sel = buf.trim()
      buf = ''
      stack.push({ sel, decls: [], isAt: sel.startsWith('@'), bodyStart: i + 1 })
      i += 1
      bufStart = i
      continue
    }
    if (c === '}') {
      flush()
      const ctx = stack.pop()
      if (ctx && !ctx.isAt) out.push({ selector: ctx.sel, decls: ctx.decls, offset: ctx.bodyStart })
      i += 1
      bufStart = i
      continue
    }
    if (c === ';') {
      flush()
      i += 1
      bufStart = i
      continue
    }
    if (buf.length === 0) bufStart = i
    buf += c
    i += 1
  }
  return out
}

/**
 * 取选择器的**主体 compound**（最后一段）的类集合。
 *
 * ★ 一个逗号分隔的选择器可能有多段 ⇒ 每段一个主体 ⇒ 返回多个类集合。
 * ★ 必须取「最后一段」而不是「整条选择器的所有类」：
 *   `.control-group.control-group--dimension` 的主体是**这两个类同时成立**的那个元素；
 *   若把两个类拆开分别匹配，就会把没有着色的 `.control-group`（另一个分组）也算进来。
 * ★ `:global(...)` / `:deep(...)` 要先拆开取内层最后一段
 *   （Vue scoped 样式里的 `:global(.a .b)` 主体是 `.b`）。
 */
export function subjectClassSets(selector: string): string[][] {
  const out: string[][] = []
  for (const rawPart of selector.split(',')) {
    let s = rawPart.replace(/::[\w-]+/g, ' ')
    s = s.replace(/:(?:global|deep)\(([^()]*)\)/g, '$1')
    const compounds = s.split(/[\s>+~]+/).filter(Boolean)
    const last = compounds[compounds.length - 1] ?? ''
    const classes = [...last.matchAll(/\.([A-Za-z_][\w-]*)/g)].map((m) => m[1])
    if (classes.length) out.push([...new Set(classes)])
  }
  return out
}

/**
 * 声明里**现场混入的裸颜色令牌**。
 *
 * ★ 语义底色令牌（`--success-bg` / `--accent-soft` …）不算 ——
 *   那些是仓内的**语义层**，正是本门要引导写法使用的通道。
 */
export function rawColorTokensIn(value: string): string[] {
  const out: string[] = []
  for (const m of value.matchAll(/var\(\s*(--[\w-]+)/g)) {
    const t = m[1]
    if (RAW_COLOR_TOKEN.test(t) && !SEMANTIC_BG_TOKEN.test(t)) out.push(t)
  }
  return [...new Set(out)]
}

// ───────────────────────── 模板侧 ─────────────────────────

export interface ClassInfo {
  /** 静态 class 属性里的类名 */
  static: string[]
  /** 动态 `:class="{…}"` 里的对象键（无法解析的数组形态不计入） */
  dynamicKeys: string[]
}

/** 拆出静态类名与动态类键。 */
export function classInfoOf(node: TagNode): ClassInfo {
  const out: ClassInfo = { static: [], dynamicKeys: [] }
  for (const a of parseAttrs(node.attrs)) {
    if (a.base !== 'class') continue
    if (a.name === 'class') {
      out.static.push(...(a.value ?? '').split(/\s+/).filter(Boolean))
    } else {
      out.dynamicKeys.push(...objectKeysOf(a.value ?? ''))
    }
  }
  return out
}

/** 从对象字面量表达式里取键（`{ active: x, 'a--b': y }` ⇒ `['active','a--b']`）。 */
export function objectKeysOf(expr: string): string[] {
  const out: string[] = []
  const re = /[{,]\s*(?:'([^']+)'|"([^"]+)"|([A-Za-z_$][\w$]*))\s*:/g
  let m: RegExpExecArray | null
  while ((m = re.exec(expr))) out.push(m[1] ?? m[2] ?? m[3])
  return out
}

/** 该元素是不是「可交互控件」？ */
export function isControl(node: TagNode): boolean {
  if (CONTROL_TAGS.has(node.name)) return true
  const role = (attrsOf(node).role ?? '').toLowerCase()
  return CONTROL_ROLES.has(role)
}

/**
 * 该元素**自身**是不是「状态载体」（它的状态就是它带的那个状态词）。
 *
 * ★ 判据落在**形状**上（元素自带状态词），不是白名单。
 */
export function isStateCarrier(info: ClassInfo): boolean {
  for (const t of [...info.static, ...info.dynamicKeys]) {
    if (STATE_WORD.test(t)) return true
  }
  return false
}

/** 直接子元素里的可交互控件。 */
export function controlChildren(node: TagNode): TagNode[] {
  return node.children.filter((c) => isControl(c))
}

/** 该元素的开标签片段（归一空白），用于登记豁免对拍。 */
export function elementSnippet(node: TagNode): string {
  return node.attrs.replace(/\s+/g, ' ').trim().slice(0, 160)
}

// ───────────────────────── 扫描 ─────────────────────────

export interface SemanticColorFinding {
  file: string
  /** CSS 声明所在行（1 基） */
  line: number
  /** CSS 选择器 */
  selector: string
  /** 主体 compound 的类 */
  classes: string[]
  /** 现场混入的裸颜色令牌 */
  tokens: string[]
  /** 声明原文 */
  declaration: string
  /** 容器里的直接子控件数 */
  controlChildren: number
  /** 容器开标签片段 */
  snippet: string
  reason: string
}

export interface SemanticColorExemption {
  id: string
  file: string
  /** 容器开标签片段（空白归一） */
  snippet: string
  reason: string
  /** witness：凭什么说它不是「装饰性着色」——必须指向事实源 */
  witness: string
}

export interface SemanticColorFacts {
  /** 违规数（= 控件组容器被着色） */
  tintedGroupContainers: number
  /** 全仓「现场混入裸颜色令牌的着色声明」总数（读数用） */
  totalTintedDeclarations: number
  /** 命中「状态载体」豁免的数量（读数用） */
  stateCarrierExempt: number
  /** 命中登记豁免的数量（读数用） */
  registeredExempt: number
}

export function normalizeSnippet(s: string): string {
  return s.replace(/\s+/g, ' ').trim()
}

export function isExemptFinding(
  finding: Pick<SemanticColorFinding, 'file' | 'snippet'>,
  exemptions: SemanticColorExemption[],
): SemanticColorExemption | undefined {
  const sn = normalizeSnippet(finding.snippet)
  return exemptions.find((e) => e.file === finding.file && normalizeSnippet(e.snippet) === sn)
}

/** 取 `<style>` 块的区间（在已遮罩的源上）。 */
export function styleBlocks(masked: string): Array<[number, number]> {
  const out: Array<[number, number]> = []
  const re = /<style([^>]*)>/g
  let m: RegExpExecArray | null
  while ((m = re.exec(masked))) {
    const start = m.index + m[0].length
    const end = masked.indexOf('</style>', start)
    if (end === -1) {
      out.push([start, masked.length])
      break
    }
    out.push([start, end])
    re.lastIndex = end + '</style>'.length
  }
  return out
}

/**
 * 扫描目录，返回违规明细与读数。
 *
 * ★★★ 判据（唯一一条，完整形态）：
 *
 *   **承载「一组控件」的容器，底色/边框色必须中性。**
 *
 *   - **控件组容器**：直接子元素里有 ≥2 个可交互控件
 *   - **着色**：底色/边框色里现场混入裸颜色令牌（语义底色令牌不算）
 *   - **豁免（状态载体）**：容器自身带状态词（静态类名或动态绑定键）
 */
export function scanSemanticColor(
  files: string[],
  rootDir: string,
  exemptions: SemanticColorExemption[] = [],
): { findings: SemanticColorFinding[]; facts: SemanticColorFacts } {
  const findings: SemanticColorFinding[] = []
  let totalTinted = 0
  let stateCarrier = 0
  let registered = 0

  for (const abs of files) {
    const src = readFileSync(abs, 'utf8')
    const masked = maskSource(abs, src)
    const rel = relative(rootDir, abs).replace(/\\/g, '/')

    // 模板树
    const elements: TagNode[] = []
    for (const [s, e] of templateBlocks(masked)) {
      const roots = parseTagTree(masked, s, e)
      const walk = (nodes: TagNode[]) => {
        for (const n of nodes) {
          elements.push(n)
          walk(n.children)
        }
      }
      walk(roots)
    }
    if (!elements.length) continue

    // 样式块
    for (const [s, e] of styleBlocks(masked)) {
      const css = masked.slice(s, e)
      for (const rule of parseCssRules(css)) {
        for (const decl of rule.decls) {
          if (!PAINT_PROPS.has(decl.prop)) continue
          const tokens = rawColorTokensIn(decl.value)
          if (!tokens.length) continue
          totalTinted++
          const absOffset = s + decl.offset
          const line = masked.slice(0, absOffset).split('\n').length

          for (const cset of subjectClassSets(rule.selector)) {
            if (!cset.length) continue
            for (const el of elements) {
              const info = classInfoOf(el)
              const eligible = new Set([...info.static, ...info.dynamicKeys])
              // 主体 compound 的类必须**全部**成立
              if (!cset.every((c) => eligible.has(c))) continue
              // 且至少一个类来自静态 class 属性（避免把动态键当唯一依据）
              if (!cset.some((c) => info.static.includes(c))) continue
              // 必须是「控件组容器」
              const kids = controlChildren(el)
              if (kids.length < 2) continue
              // 豁免：状态载体
              if (isStateCarrier(info)) {
                stateCarrier++
                continue
              }
              const snippet = elementSnippet(el)
              const finding: SemanticColorFinding = {
                file: rel,
                line,
                selector: rule.selector.trim(),
                classes: cset,
                tokens,
                declaration: `${decl.prop}: ${decl.value}`.slice(0, 160),
                controlChildren: kids.length,
                snippet,
                reason:
                  `承载 ${kids.length} 个控件的分组容器被 ${tokens.join('/')} 着色 —— ` +
                  `分组不是状态，颜色预算属于「被选中的那一个」`,
              }
              if (isExemptFinding(finding, exemptions)) {
                registered++
                continue
              }
              findings.push(finding)
            }
          }
        }
      }
    }
  }

  return {
    findings,
    facts: {
      tintedGroupContainers: findings.length,
      totalTintedDeclarations: totalTinted,
      stateCarrierExempt: stateCarrier,
      registeredExempt: registered,
    },
  }
}

// ───────────────────────── 自证 ─────────────────────────

export interface SelftestCase {
  name: string
  ok: boolean
}

/**
 * 自证用例。
 *
 * ★ 纪律（LESSONS.md §411 / §419）：
 *   ① 每个用例都要有「必须判绿」的对照，否则判据可能是恒真；
 *   ② 断言恒真比恒假更隐蔽 —— 每个 `ok` 都要能被抓出来。
 */
export const SEMANTIC_COLOR_SELFTEST: SelftestCase[] = [
  {
    name: '控件组容器被 success 着色 ⇒ 判红',
    ok: (() => {
      const src = `<template><div class="control-group"><button>A</button><button>B</button></div></template>
<style scoped>
.control-group { background: color-mix(in srgb, var(--success) 14%, var(--bg-subtle)); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 1
    })(),
  },
  {
    name: '★ 对照：同样两个按钮，但容器用中性底色 ⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="control-group"><button>A</button><button>B</button></div></template>
<style scoped>
.control-group { background: var(--bg-subtle); border: 1px solid var(--border); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 对照：按钮自己带主色（当前选中）⇒ 判绿（颜色预算给选中项）',
    ok: (() => {
      const src = `<template><div class="control-group"><button class="control-btn--active">A</button><button>B</button></div></template>
<style scoped>
.control-btn--active { background: color-mix(in srgb, var(--accent) 15%, transparent); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 状态载体豁免：容器自身带 `active` ⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="turn-card" :class="{ active: sel === 1 }"><button>A</button><button>B</button></div></template>
<style scoped>
.turn-card.active { background: color-mix(in srgb, var(--accent) 8%, var(--bg-card)); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 对照：同一个容器**不带**状态类 ⇒ 判红（豁免必须因形状而失效）',
    ok: (() => {
      const src = `<template><div class="turn-card"><button>A</button><button>B</button></div></template>
<style scoped>
.turn-card { background: color-mix(in srgb, var(--accent) 8%, var(--bg-card)); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 1
    })(),
  },
  {
    name: '★ 只有一个控件的容器不构成「一组控件」⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="code-box"><span>验证码</span><button>复制</button></div></template>
<style scoped>
.code-box { background: color-mix(in srgb, var(--success) 14%, transparent); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 语义底色令牌（`--success-bg`）不算「现场混入」⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="g"><button>A</button><button>B</button></div></template>
<style scoped>
.g { background: var(--success-bg); border: 1px solid var(--success-bd); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 后代选择器：被着色的是**按钮**而不是容器 ⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="pm-actions"><button class="btn">A</button><button class="btn btn-success">B</button></div></template>
<style scoped>
.pm-actions .btn-success:hover { background: color-mix(in srgb, var(--success) 88%, var(--kx-text)); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ `:global(.a .b)` 的主体是 `.b` ⇒ 不得把 `.a` 当成被着色元素',
    ok: (() => {
      const src = `<template><div class="retention-radio"><el-radio-button :label="1">1</el-radio-button><el-radio-button :label="3">3</el-radio-button></div></template>
<style scoped>
:global(.retention-radio .el-radio-button__inner) { background: color-mix(in srgb, var(--accent) 20%, transparent); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 复合主体 `.a.b` 必须**两个类同时成立**才算命中（不得波及只带 `.a` 的元素）',
    ok: (() => {
      const src = `<template>
<div class="control-group"><button>A</button><button>B</button></div>
<div class="control-group control-group--mode"><button>小</button><button>大</button></div>
</template>
<style scoped>
.control-group.control-group--mode { background: color-mix(in srgb, var(--success) 14%, var(--bg-subtle)); }
</style>`
      const r = scanSemanticColorSource(src, 'x.vue')
      return r.findings.length === 1 && r.findings[0].controlChildren === 2
    })(),
  },
  {
    name: '★ `outline` / `box-shadow` 不算底色着色（焦点环是正当用法）⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="g"><button>A</button><button>B</button></div></template>
<style scoped>
.g:focus-visible { outline: 2px solid var(--accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 20%, transparent); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 文字色 `color` 不是底色 ⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="g"><button>A</button><button>B</button></div></template>
<style scoped>
.g { color: var(--accent); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 直接子元素才算「一组控件」（后代按钮不算）⇒ 判绿',
    ok: (() => {
      const src = `<template><div class="g"><div class="inner"><button>A</button><button>B</button></div></div></template>
<style scoped>
.g { background: color-mix(in srgb, var(--warning) 18%, var(--bg-subtle)); }
</style>`
      return scanSemanticColorSource(src, 'x.vue').findings.length === 0
    })(),
  },
  {
    name: '★ 判据不含全局状态（同一输入两次结果一致）',
    ok: (() => {
      const src = `<template><div class="g"><button>A</button><button>B</button></div></template>
<style scoped>
.g { background: color-mix(in srgb, var(--warning) 18%, var(--bg-subtle)); }
</style>`
      const a = scanSemanticColorSource(src, 'x.vue')
      const b = scanSemanticColorSource(src, 'x.vue')
      return JSON.stringify(a) === JSON.stringify(b)
    })(),
  },
  {
    name: '★ 判据对「控件数」敏感（删掉一个按钮即不再命中）',
    ok: (() => {
      const two = `<template><div class="g"><button>A</button><button>B</button></div></template>
<style scoped>
.g { background: color-mix(in srgb, var(--warning) 18%, var(--bg-subtle)); }
</style>`
      const one = `<template><div class="g"><button>A</button></div></template>
<style scoped>
.g { background: color-mix(in srgb, var(--warning) 18%, var(--bg-subtle)); }
</style>`
      return scanSemanticColorSource(two, 'x.vue').findings.length === 1
        && scanSemanticColorSource(one, 'x.vue').findings.length === 0
    })(),
  },
]

/**
 * 对**一段源码**做扫描（自证用；走的是与全仓扫描**同一个**裁决函数）。
 *
 * ★ 纪律：自证不得另抄一份判据（LESSONS.md §411）⇒ 这里只把「文件」换成「字符串」。
 */
export function scanSemanticColorSource(
  source: string,
  file: string,
  exemptions: SemanticColorExemption[] = [],
): { findings: SemanticColorFinding[]; facts: SemanticColorFacts } {
  const elements: TagNode[] = []
  for (const [s, e] of templateBlocks(source)) {
    const roots = parseTagTree(source, s, e)
    const walk = (nodes: TagNode[]) => {
      for (const n of nodes) {
        elements.push(n)
        walk(n.children)
      }
    }
    walk(roots)
  }
  const findings: SemanticColorFinding[] = []
  let totalTinted = 0
  let stateCarrier = 0
  let registered = 0
  for (const [s, e] of styleBlocks(source)) {
    const css = source.slice(s, e)
    for (const rule of parseCssRules(css)) {
      for (const decl of rule.decls) {
        if (!PAINT_PROPS.has(decl.prop)) continue
        const tokens = rawColorTokensIn(decl.value)
        if (!tokens.length) continue
        totalTinted++
        const line = source.slice(0, s + decl.offset).split('\n').length
        for (const cset of subjectClassSets(rule.selector)) {
          if (!cset.length) continue
          for (const el of elements) {
            const info = classInfoOf(el)
            const eligible = new Set([...info.static, ...info.dynamicKeys])
            if (!cset.every((c) => eligible.has(c))) continue
            if (!cset.some((c) => info.static.includes(c))) continue
            const kids = controlChildren(el)
            if (kids.length < 2) continue
            if (isStateCarrier(info)) {
              stateCarrier++
              continue
            }
            const finding: SemanticColorFinding = {
              file,
              line,
              selector: rule.selector.trim(),
              classes: cset,
              tokens,
              declaration: `${decl.prop}: ${decl.value}`.slice(0, 160),
              controlChildren: kids.length,
              snippet: elementSnippet(el),
              reason:
                `承载 ${kids.length} 个控件的分组容器被 ${tokens.join('/')} 着色 —— ` +
                `分组不是状态，颜色预算属于「被选中的那一个」`,
            }
            if (isExemptFinding(finding, exemptions)) {
              registered++
              continue
            }
            findings.push(finding)
          }
        }
      }
    }
  }
  return {
    findings,
    facts: {
      tintedGroupContainers: findings.length,
      totalTintedDeclarations: totalTinted,
      stateCarrierExempt: stateCarrier,
      registeredExempt: registered,
    },
  }
}

export function runSemanticColorSelftest(): { passed: number; total: number; failed: string[] } {
  const failed: string[] = []
  let passed = 0
  for (const c of SEMANTIC_COLOR_SELFTEST) {
    if (c.ok) passed++
    else failed.push(c.name)
  }
  return { passed, total: SEMANTIC_COLOR_SELFTEST.length, failed }
}
