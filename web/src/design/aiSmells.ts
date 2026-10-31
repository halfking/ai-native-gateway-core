// aiSmells.ts — 「AI 化视觉痕迹」的可执行判据（2026-10-27 · 第 17 增量）
//
// ★ 要治的病（不是推演，是 impeccable 规范里逐条点名的「AI 界面的标志」）：
//   impeccable `skill/reference/craft-floor.md` 的 Refuse 段 + 仓内 `DESIGN.md` §4/§5/§6/§8。
//   craft-floor 原话（codex 段）：
//     "Declare elevation once, border or shadow. A 1px border under a wide soft
//      shadow is the ghost card. Card radii stay at 12–16px; pills are for small
//      controls."
//   以及 Refuse 段的逐条禁令：渐变文字 / 彩色侧边条 / 硬偏移阴影 / 毛玻璃当装饰 /
//   emoji 字形当图标 / 800+ 字重 / 超紧字距 / uppercase / kicker / 01-02 序号。
//
// ★ 本文件是**判据**（纯函数），门在 `web/scripts/design-ai-smell-guard.mjs`。
//   放 `src/` 是为了能被 vitest 覆盖（与 `src/i18n/copyClarity.ts` 同一范式）。
//
// ★★★ **本门最值钱的一处判据设计：按【形状】判，不按【写法】判**
//   被禁的形状是「一条 ≥2px 的彩色侧边条」。
//   它有两种写法：`border-left: 3px solid <彩色>`  **和**
//   `box-shadow: inset 3px 0 0 <彩色>`（用内阴影伪装成侧边条）。
//   ⇒ 只扫 `border-left` 的第一版会漏掉后者（真仓实测 5 处里有 4 处是伪装形态）。
//   ⇒ 通用式：**凡是被禁的「视觉形状」，判据必须覆盖它的所有实现写法**，
//     否则门会给「用另一种写法绕过去」留后门。

import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { join, relative, extname, sep } from 'node:path'

export interface AiSmellFinding {
  rule: string
  file: string
  line: number
  selector: string
  text: string
  reason: string
}

export interface AiSmellOptions {
  /**
   * 扫描根目录（绝对路径，可多个）。
   *
   * ★ 第 27 增量：从「只扫 `web/src`」扩到「`web/src` + `web-mobile/src`」。
   *   移动端此前**没有任何品味门看守** ⇒ 同一个 craft-floor 硬禁在两个应用里
   *   口径不同（桌面判红、移动端没人判）⇒ ⇒ 「同一口径只能有一份实现」
   *   的第一层含义是**同一套判据要覆盖全部应用**。
   */
  rootDirs: string[]
  /**
   * 仓根（用于算相对路径）。
   *
   * ★ 第 27 增量：**路径口径统一到仓根**（`web/src/…` / `web-mobile/src/…`）。
   *   原因：两个应用的文件路径若都以 `src/` 开头 ⇒ 登记簿里两个应用的
   *   同名文件**无法区分**（`src/views/ModelsView.vue` 在两边都存在）
   *   ⇒ ⇒ 豁免会张冠李戴。
   */
  repoDir: string
  /** 豁免登记簿：{ file, rule, selector? } */
  registry?: Array<{ file: string; rule: string; selector?: string; reason: string }>
}

// ───────────────────────── 工具 ─────────────────────────

const SKIP_DIRS = new Set(['node_modules', 'dist', '.vite', 'coverage'])
const SCAN_EXT = new Set(['.vue', '.css'])

export function listScannableFiles(rootDir: string): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    let entries
    try {
      entries = readdirSync(dir, { withFileTypes: true })
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

interface CssBlock {
  selector: string
  body: string
  /** 块的**结束行**（1-based） */
  line: number
  /** 块的**起始行**（1-based）—— 第 18 增量新增：算「声明在哪一行」要用 */
  startLine: number
}

/**
 * 把 .vue 的 `<style>` 段与 .css 全文拆成「规则块」。
 *
 * ★ 为什么要块作用域（而不是行级正则）：
 *   幽灵卡片的判据是「**同一元素**既有描边又有阴影」——
 *   这两个声明经常写在**不同的行**（`border: …` 一行、`box-shadow: …` 另一行）
 *   ⇒ 行级扫描会把它们当成两件独立的事 ⇒ 整族漏判。
 */
export function extractCssBlocks(src: string, file: string): CssBlock[] {
  const lines = src.split('\n')
  const blocks: CssBlock[] = []
  const pushBuffer = (buf: string[], startLine: number) => {
    let depth = 0
    let cur: string[] = []
    // ★ 第 27 增量：**先掩注释再切块**（★ 注释里的声明不算声明；
    //   注释不再被逐行切碎 ⇒ 见 maskCssComments 的说明）。
    const masked = maskCssComments(buf.join('\n')).split('\n')
    for (let j = 0; j < masked.length; j++) {
      cur.push(masked[j])
      for (const ch of masked[j]) {
        if (ch === '{') depth++
        else if (ch === '}') depth--
      }
      // ★ 只有**含 `{` 的块**才是规则 ⇒ 注释 / 空行不单独成块
      //   （★ 旧版会把注释的每一行都当成一个块）。
      if (depth === 0 && cur.length && cur.join('').includes('{')) {
        const text = cur.join('\n')
        blocks.push({
          selector: text.split('{')[0].trim().split('\n').pop()!.trim(),
          body: text,
          line: startLine + j,
          startLine: startLine + j - cur.length + 1,
        })
        cur = []
      }
    }
  }
  let inStyle = false
  let buf: string[] = []
  let start = 0
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i]
    if (!inStyle && /<style[^>]*>/.test(l)) {
      inStyle = true
      buf = []
      start = i + 2
      continue
    }
    if (inStyle && /<\/style>/.test(l)) {
      inStyle = false
      pushBuffer(buf, start)
      continue
    }
    if (inStyle) buf.push(l)
  }
  if (file.endsWith('.css')) pushBuffer(lines, 1)
  return blocks
}

/**
 * ★★★ 把 CSS 注释**掩成等长空格**（★ 保留换行 ⇒ 行号与偏移都不变）。
 *
 * ★★★ 为什么必须掩而不是删（第 27 增量，★ 真仓实测抓到的既有缺陷）：
 *   `extractCssBlocks` 旧版按「括号深度归零」**逐行**切块 ⇒ ★ 多行 CSS 注释
 *   会被切成**碎片**（每行一个块）⇒ 碎片里没有完整的 `/* … *\/` ⇒
 *   事后 `stripCssComments` **剥不掉** ⇒ ⇒ 注释里写的声明被当成真声明。
 *   ★ 实测形态（`live-topology/ClientRequestPanel.vue`）：
 *     `/* … 原来左沿是 3px 粗边（`border-left-width: 3px`）… *\/`
 *     ⇒ 注释里明明在说「这条已经改掉了」，判据却把它当成一条真色条。
 *   ⇒ ⇒ **掩在切块之前**（注释不再参与深度计算 ⇒ 不会被切碎）
 *   ⇒ ⇒ 抬升门 `elevation.ts` 共用本提取器 ⇒ **一并受益**。
 */
export function maskCssComments(s: string): string {
  return s.replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
}

/** 去掉 CSS 注释（避免注释里的声明被当成真声明）。 */
export function stripCssComments(s: string): string {
  return s.replace(/\/\*[\s\S]*?\*\//g, '')
}

// ───────────────────────── 判据原语 ─────────────────────────

/** 中性令牌（描边、文字、遮罩、阴影底色…）—— 这些不是「彩色」。 */
const NEUTRAL_TOKEN = /^--(?:kx-)?(?:border|text|muted|overlay-[a-z-]+|shadow-color-[a-z-]+|surface|bg|bg-card|card|white|black)$/i

/**
 * 是不是「彩色」。
 *
 * ★★★ 口径改成「**不是中性**」而不是「命中状态色白名单」，因为：
 *   ① 真仓里彩色几乎都用 `color-mix(in srgb, var(--probe-cyan) 30%, transparent)` 表达
 *      ⇒ 第一版只认 `var(--primary|success|…)` / `#hex` / `rgb()` ⇒ **漏判一大片**；
 *   ② 白名单是枚举式的 ⇒ 换一种颜色写法就绕过去了。
 *   ⇒ 中性令牌是一张**封闭的**小表（描边/文字/遮罩/表面），其余一律算彩色。
 */
export function isColoredValue(v: string): boolean {
  const s = v.trim()
  if (!s) return false
  // ★ 只认「整个值就是 transparent/none」——
  //   `color-mix(in srgb, var(--success) 40%, transparent)` 里的 transparent 是**混合搭档**，
  //   不是「这个值是中性」⇒ 早退会把它误判成中性（自证 S13/S17a 抓到的）。
  if (/^\s*(none|transparent|currentcolor)\s*$/i.test(s)) return false
  // ★ 捕获时**保留 `--` 前缀** —— 中性表是按 `--kx-border` 这类全名写的
  const refs = [...s.matchAll(/var\((--[a-z0-9-]+)/gi)].map((m) => m[1])
  if (refs.length) {
    // 全部中性 ⇒ 中性；只要有一个非中性 ⇒ 彩色
    if (refs.every((r) => NEUTRAL_TOKEN.test(r))) return false
    return true
  }
  if (/#[0-9a-f]{3,8}\b/i.test(s)) return true
  if (/\b(rgb|rgba|hsl|hsla|oklch|color-mix)\(/i.test(s)) return true
  return false
}

/**
 * 按**顶层逗号**切分（括号内的逗号不切）。
 *
 * ★★★ 判据自身的坑（自证抓到的）：`value.split(/,(?![^(]*\))/)` 切不动
 *   `inset 0 1px 0 color-mix(in srgb, var(--kx-text) 4%, transparent)` ——
 *   `color-mix(...)` 里的逗号后面跟的是 ` var(--kx-text) 4%`，
 *   正则的 `[^(]*\)` 找不到 `)` ⇒ 那个逗号**被当成顶层逗号**切开了
 *   ⇒ 「纯内阴影」被误判成「有外阴影」⇒ 纯内阴影的表面被判成幽灵卡片。
 */
export function splitTopLevel(value: string): string[] {
  const out: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of value) {
    if (ch === '(') depth++
    else if (ch === ')') depth = Math.max(0, depth - 1)
    if (ch === ',' && depth === 0) {
      out.push(cur.trim())
      cur = ''
      continue
    }
    cur += ch
  }
  if (cur.trim()) out.push(cur.trim())
  return out
}

/** 阴影的模糊半径（px）。`0 20px 50px x` ⇒ 50。取不到 ⇒ null。 */
export function shadowBlur(value: string): number | null {
  const v = value.trim()
  const m = v.match(/(-?\d+(?:\.\d+)?)(?:px)?\s+(-?\d+(?:\.\d+)?)px\s+(-?\d+(?:\.\d+)?)px/)
  return m ? Number(m[3]) : null
}

/** 阴影是不是「纯内阴影」（内阴影不是抬升，不构成幽灵卡片）。 */
export function isInsetOnly(value: string): boolean {
  const parts = splitTopLevel(value)
  if (!parts.length) return false
  if (!parts.some((p) => /inset/.test(p))) return false
  return parts.every((p) => /inset/.test(p))
}

/**
 * 阴影值拆成「层」，并标出每层的偏移。
 *
 * ★★★ 第 18 增量补的共享原语（2026-10-27）：「什么算抬升」这个口径**只能有一份**。
 *   本函数与 `isElevationShadow` 是同一口径的两级粒度（全量 vs 判定），
 *   第 18 增量的浮起词汇门（`design/elevation.ts`）必须复用它，
 *   不许自己再写一份「哪一层算投影」的判断。
 *
 * ★ 坑（判据自身的坑，V7 盲区的同源）：
 *   CSS 允许**省略长度单位**（`0 0 0 1px` 的第 1、2 段就是无单位的 `0`），
 *   而旧的 V7 正则要求第 2 段必须带 `px` ⇒ `-1px 0 0 0 var(--accent)`
 *   （零模糊 + 有偏移 = craft-floor 明令的硬偏移阴影）**被判成不违规**。
 *   ⇒ 这里一律允许省略单位，靠「偏移是否非零」判定，而不是靠正则失配。
 */
export interface ShadowLayer {
  raw: string
  /** 水平偏移；null = 取不到（`var(--token)` / `none` 这类非字面量配方） */
  offsetX: number | null
  offsetY: number | null
}

export function shadowLayers(value: string): ShadowLayer[] {
  const out: ShadowLayer[] = []
  for (const part of splitTopLevel(value.trim())) {
    const raw = part.trim()
    if (!raw) continue
    if (/^none$/i.test(raw)) continue
    if (/inset/.test(raw)) continue
    const m = raw.match(/^(-?\d+(?:\.\d+)?)(?:px)?\s+(-?\d+(?:\.\d+)?)(?:px)?(?:\s|$)/)
    if (!m) {
      out.push({ raw, offsetX: null, offsetY: null })
      continue
    }
    out.push({ raw, offsetX: Number(m[1]), offsetY: Number(m[2]) })
  }
  return out
}

/**
 * 规范抬升 token 名单（**唯一的名单** —— 第 18 增量的浮起词汇门也 import 它）。
 *
 * ★★★ 为什么必须有这张名单（假绿的来源）：
 *   抬升配方的偏移写在 token 里（`var(--kx-shadow-md-left)` ⇒ `-12px 0 32px`），
 *   而 `shadowLayers` 只从**字面量**取偏移 ⇒ 取不到 ⇒ 「这个表面有没有抬升」判不出来
 *   ⇒ 幽灵卡片（描边 + 抬升）就**判不出来了**。
 *   ⇒ 规矩：**`--kx-shadow-*` 命名的 token 一律是抬升**。
 *   ⇒ 这不是「按写法判」而是「按名字约定的语义判」：命名即契约。
 */
export const ELEVATION_TOKENS = [
  '--kx-shadow-xs',
  '--kx-shadow-sm',
  '--kx-shadow-md',
  '--kx-shadow-md-left',
] as const

const ELEVATION_TOKEN_RE = new RegExp(`^var\\(\\s*(${ELEVATION_TOKENS.join('|')})\\s*\\)$`)

/** 这一层是不是「规范抬升 token」的引用。 */
export function isElevationToken(layer: string): boolean {
  return ELEVATION_TOKEN_RE.test(layer.trim())
}

/**
 * 这个阴影是不是「抬升」（真的投影）。
 *
 * ★ 判据：必须是**非内阴影**，且**带非零偏移**。
 *   · `0 0 0 1px <color>` 是「用阴影画的圆环」⇒ 不是抬升（是描边的替身）
 *   · `0 0 5px <color>` 是「零偏移光晕」⇒ craft-floor 明令是装饰，不是深度
 *   ⇒ 两者都不该让一个表面被判成「抬升声明了两次」。
 *   · `var(--kx-shadow-*)` 是**规范抬升**（偏移在 token 里，取不到字面量）
 *     ⇒ 第 18 增量实测：没有这一条，迁到 token 的抽屉会「变成非抬升」
 *       ⇒ 幽灵卡片门**假绿**。
 */
export function isElevationShadow(value: string): boolean {
  return shadowLayers(value).some(
    (l) =>
      isElevationToken(l.raw) ||
      (l.offsetX !== null && l.offsetX !== 0) ||
      (l.offsetY !== null && l.offsetY !== 0)
  )
}

/**
/**
 * 一个声明块里**所有** `box-shadow` 声明（按出现顺序）。
 *
 * ★★★ 第 18 增量补的共享原语（真仓变异臂 M3 抓到的盲区）：
 *   旧实现是 `body.match(/box-shadow\s*:\s*([^;]+)/)` ⇒ **只取第一条**，
 *   于是同块内的**第二条**声明（同块里既有规范 token、又有手写硬偏移）完全看不见。
 *   ⇒ V7（硬偏移）与第 18 增量的浮起词汇门**共用**这一个遍历器 ⇒ 口径只有一份。
 */
export interface BoxShadowDecl {
  value: string
  /** 该声明在块内的字符偏移（用于换算行号） */
  offset: number
}

export function boxShadowDeclarations(body: string): BoxShadowDecl[] {
  const s = stripCssComments(body)
  const out: BoxShadowDecl[] = []
  const re = /box-shadow\s*:/g
  let m: RegExpExecArray | null
  while ((m = re.exec(s))) {
    const semi = s.indexOf(';', m.index)
    const value = s.slice(m.index, semi < 0 ? s.length : semi).replace(/^box-shadow\s*:\s*/, '').trim()
    out.push({ value, offset: m.index })
  }
  return out
}

/**
 * 一个声明块里有没有「可见的**闭合**描边」。
 *
 * ★★★ 判据自身的坑（自证 S1 抓到的）：`border` 的匹配不能写成 `border(?:-[a-z]+)?`
 *   —— 那会把 **`border-radius`** / `border-color` / `border-style` 也当成描边
 *   ⇒ 于是「只有阴影 + 圆角」的浮起层被误判成幽灵卡片。
 *   ⇒ 正确口径：`border` 只认**整体**与**四边**（top/right/bottom/left）。
 *
 * ★★★ 第 18 增量补的边界（真仓实测 4 处假阳性）：
 *   **单边描边不算「表面描边」**。
 *   依据是 craft-floor 原话的形状 ——「A 1px border **under** a wide soft shadow
 *   is the ghost card」：幽灵卡片是**同一条边界**既画线又投影。
 *   而抽屉上的 `border-left`（左边框）+ 向左的投影（`--kx-shadow-md-left`）：
 *   描边在**内侧**、投影在**外侧** ⇒ 两者不重复 ⇒ 不是幽灵卡片。
 *   ⇒ 闭合 = 整体 `border`，或四条边齐全。
 */
function hasVisibleBorder(body: string): boolean {
  const s = stripCssComments(body)
  // ① 整体 border ⇒ 闭合
  const m = s.match(/(^|[\s;{])border\s*:\s*([^;]+)/i)
  if (m) {
    const v = m[2]
    if (!/^\s*none\b/i.test(v) && !/^\s*0\b/.test(v)) return true
  }
  // ② 四条边齐全 ⇒ 闭合
  const sides = ['top', 'right', 'bottom', 'left'] as const
  let all = true
  for (const side of sides) {
    const re = new RegExp(`(^|[\\s;{])border-${side}\\s*:\\s*([^;]+)`, 'i')
    const mm = s.match(re)
    if (!mm) { all = false; break }
    if (/^\s*(none|0)\b/i.test(mm[2])) { all = false; break }
  }
  if (all) return true
  // ③ 只有 border-width 时：宽度 > 0 也算可见（整体描边）
  const w = s.match(/(^|[\s;{])border-width\s*:\s*([^;]+)/i)
  if (w && /(\d+(?:\.\d+)?)px/.test(w[2])) {
    const px = Number(w[2].match(/(\d+(?:\.\d+)?)px/)![1])
    if (px > 0) return true
  }
  return false
}

/** 一个声明块里有没有「抬升阴影」（非内阴影 + 非零偏移）。 */
function hasElevationShadow(body: string): { value: string } | null {
  const s = stripCssComments(body)
  const m = s.match(/box-shadow\s*:\s*([^;]+)/)
  if (!m) return null
  const v = m[1].trim()
  if (!isElevationShadow(v)) return null
  return { value: v }
}

// ───────────────────────── V2 侧边条辅助 ─────────────────────────

/**
 * 规则选择器的「同族键」。
 *
 * ★ 用途：★ 「颜色写在 modifier 规则里」是彩色侧边条最常见的漏标形态 ——
 *   `.row { border: 1px solid transparent; border-left-width: 3px; }`
 *   `.row.is-ok { border-color: var(--kx-success); }`
 *   ⇒ 逐条读时第一条因为 `transparent` 放过去了 ⇒ ⇒ 必须按**同族**归并。
 */
function stripeFamilyKey(selector: string): string {
  const last = selector.split(/[\s>+~,]+/).filter(Boolean).pop() || selector
  const noPseudo = last.replace(/::?[a-zA-Z-]+(\([^)]*\))?/g, '')
  const cls = noPseudo.match(/\.[A-Za-z0-9_-]+/g)
  if (cls) return '.' + cls[0].slice(1).replace(/--.*$/, '')
  return noPseudo.replace(/[.#].*$/, '') || last
}

/** 一个规则块声明过的「带颜色的边框声明」（★ 宽度声明不算颜色）。 */
function declaredBorderColors(body: string): string[] {
  const out: string[] = []
  for (const m of body.matchAll(/(?:^|[;{])\s*border(?:-[a-z]+)*\s*:\s*([^;]+)/g)) {
    const v = m[1].trim()
    // ★ `border-left-width: 3px` 是宽度，不是颜色 ⇒ 不算「声明过颜色」
    //   （★ 否则「读不出 ⇒ 按有色算」这条失败安全侧就不会触发）。
    if (/^\d+(\.\d+)?px$/.test(v)) continue
    out.push(v)
  }
  return out
}

/**
 * 同族规则 → 「这个族会不会被染色」。
 *
 * ★ 失败安全侧：★ 一个颜色都没声明 ⇒ **读不出** ⇒ 按有色算（判红）。
 *   （★ 与孤儿门那份实现同口径：判红是失败安全侧。）
 */
function buildColoredBorderIndex(blocks: CssBlock[]): Map<string, boolean> {
  const groups = new Map<string, string[]>()
  for (const b of blocks) {
    if (/::(before|after)\b/.test(b.selector)) continue
    const key = stripeFamilyKey(b.selector)
    const list = groups.get(key) ?? []
    list.push(...declaredBorderColors(b.body))
    groups.set(key, list)
  }
  const out = new Map<string, boolean>()
  for (const [k, colors] of groups) {
    out.set(k, colors.length === 0 ? true : colors.some((c) => isColoredValue(c)))
  }
  return out
}

interface SideEdge {
  side: 'left' | 'right'
  width: number
  text: string
}

/** 取规则块里的左右边（★ 简写与长写都认 ⇒ 同形状的所有实现写法）。 */
function sideEdges(body: string): SideEdge[] {
  const out: SideEdge[] = []
  for (const m of body.matchAll(/border-(left|right)\s*:\s*([^;]+)/g)) {
    const w = m[2].match(/(\d+(?:\.\d+)?)px/)
    if (!w) continue
    out.push({ side: m[1] as 'left' | 'right', width: Number(w[1]), text: m[0].trim() })
  }
  for (const m of body.matchAll(/border-(left|right)-width\s*:\s*(\d+(?:\.\d+)?)px/g)) {
    out.push({ side: m[1] as 'left' | 'right', width: Number(m[2]), text: m[0].trim() })
  }
  // ★ 同一条边被写了两次（简写 + 长写）⇒ 取**更宽**的那次（判红是失败安全侧）
  const bySide = new Map<'left' | 'right', SideEdge>()
  for (const e of out) {
    const prev = bySide.get(e.side)
    if (!prev || e.width > prev.width) bySide.set(e.side, e)
  }
  return [...bySide.values()]
}

/** 取声明值里的第一个 px 数值（★ 仿孤儿门那份实现的 `firstPx`）。 */
function firstPx(value: string): number | null {
  const m = value.match(/(\d+(?:\.\d+)?)px/)
  return m ? Number(m[1]) : null
}

/**
 * 伪元素是不是「画出来的一条侧边条」。
 *
 * ★ 形状：绝对定位 + 通高 + 窄（≤4px）+ 有背景色 ⇒ ★ 那就是一条画出来的色条
 *   （★ 与 `border-left` 是**同一个视觉形状** ⇒ 同一判据）。
 */
function isStripePseudo(body: string): boolean {
  if (!/position\s*:\s*(absolute|fixed)/.test(body)) return false
  const tall =
    /height\s*:\s*100%/.test(body) ||
    (/top\s*:\s*0\s*;/.test(body) && /bottom\s*:\s*0\s*;/.test(body))
  if (!tall) return false
  const w = firstPx(body.match(/width\s*:\s*([^;]+)/)?.[1] ?? '')
  if (w === null || w > 4) return false
  const bg = body.match(/background(?:-color)?\s*:\s*([^;]+)/)?.[1] ?? ''
  if (!bg || /none|transparent/i.test(bg)) return false
  return true
}

/**
 * 取某条边的**颜色**（★ 找不到 ⇒ null ⇒ 交由同族规则决定）。
 *
 * ★ 优先级：① 该边自己的简写 ② 该边自己的 `*-color` ③ 通用 `border:` 简写。
 */
function sideColor(body: string, side: 'left' | 'right'): string | null {
  const strip = (v: string) =>
    v
      .trim()
      .replace(/^\s*\d+(\.\d+)?px\s*/, '')
      .replace(/\b(solid|dashed|dotted|double|groove|ridge|inset|outset|none|hidden)\b/gi, '')
      .trim()
  const own = body.match(new RegExp(`border-${side}\\s*:\\s*([^;]+)`))
  if (own) {
    const c = strip(own[1])
    if (c) return c
  }
  const lc = body.match(new RegExp(`border-${side}-color\\s*:\\s*([^;]+)`))
  if (lc) {
    const c = lc[1].trim()
    if (c) return c
  }
  const b = body.match(/(?:^|[;{])\s*border\s*:\s*([^;]+)/)
  if (b) {
    const c = strip(b[1])
    if (c) return c
  }
  return null
}

/**
 * V2：≥2px 的彩色侧边条（craft-floor：ban-side-stripe-borders）。
 *
 * ★★★ 第 27 增量：**补齐了已退役的孤儿门那份实现的写法覆盖**。
 *   孤儿门那份能认 5 种写法，本实现第 1 版只认 1 种（简写）⇒ ★ 退役它
 *   若不移植 ⇒ ★ **门会变弱**（三种写法直接绕过）⇒ ⇒ **退役 = 覆盖不许缩水**。
 *
 * ★ 写法（★ 同一个视觉形状的所有实现写法）：
 *   ① `border-left/right: 3px solid <色>`（简写）
 *   ② `border-left/right-width: 3px` + 颜色在 `border:` / `border-*-color:`（长写）
 *   ③ 颜色由**同基类的 modifier 规则**给出（最常见的漏标形态）
 *   ④ 伪元素画的窄色条（`::before` + 绝对定位 + ≤4px 宽 + 通高 + 有背景）
 *   ⑤ `box-shadow: inset 3px 0 0 <色>`（inset 阴影伪装）
 *
 * ★ 边的范围：**只判 left / right**（★ craft-floor 原话就是 border-left/right）
 *   ⇒ ★ 顶边/底边**不判**（孤儿门那份判 4 条边 ⇒ 比 craft-floor 更宽 ⇒ 不移植）。
 * ★ 颜色口径：沿用仓内的中性令牌表（`isColoredValue`）⇒ 中性描边不算色条。
 * ★ 一个规则块只报一条（★ 计数口径 = 元素数，与 V3 同一口径）。
 */
function detectSideStripe(
  body: string,
  sel: string,
  familyColored: boolean,
): { text: string; reason: string } | null {
  // ① / ② / ③：左右边（简写 + 长写）
  for (const e of sideEdges(body)) {
    if (e.width < 2) continue
    const own = sideColor(body, e.side)
    // ★ 本块的颜色若是**明确有色** ⇒ 有色；否则看同族（modifier 规则里染色）。
    const colored = (own !== null && isColoredValue(own)) || familyColored
    if (!colored) continue
    return {
      text: e.text.slice(0, 90),
      reason: '≥2px 彩色侧边条表达状态 ⇒ AI 痕迹（craft-floor：ban-side-stripe-borders）',
    }
  }
  // ④ 伪元素画的窄色条
  if (/::(before|after)\b/.test(sel) && isStripePseudo(body)) {
    const bg = body.match(/background(?:-color)?\s*:\s*([^;]+)/)?.[1]?.trim() ?? ''
    if (isColoredValue(bg)) {
      return {
        text: `${sel} 画的窄色条（${bg}）`,
        reason: '用伪元素画出来的彩色侧边条（形状与 border-left 相同 ⇒ 同一判据）',
      }
    }
  }
  // ⑤ inset 阴影伪装
  const ins = body.match(/box-shadow\s*:\s*inset\s+(\d+(?:\.\d+)?)px\s+0\s+0\s+(.+?)(?:;|$)/)
  if (ins && Number(ins[1]) >= 2 && isColoredValue(ins[2])) {
    return {
      text: `inset ${ins[1]}px 0 0 ${ins[2].trim()}`,
      reason: '用内阴影伪装的彩色侧边条（形状与 border-left 相同 ⇒ 同一判据）',
    }
  }
  return null
}

// ───────────────────────── 主判定 ─────────────────────────

export function detectAiSmells(opts: AiSmellOptions): AiSmellFinding[] {
  const { rootDirs, repoDir, registry = [] } = opts
  const findings: AiSmellFinding[] = []
  for (const rootDir of rootDirs) {
    const files = listScannableFiles(rootDir)
    for (const file of files) {
      // ★ 路径统一成仓根相对 + 正斜杠（★ Windows 上 `path.relative` 给的是反斜杠
      //   ⇒ 登记簿里的路径与扫描出来的路径对不上 ⇒ 豁免会静默失效）。
      const rel = relative(repoDir, file).split(sep).join('/')
      findings.push(...scanSource(readFileSync(file, 'utf8'), rel, file))
    }
  }
  return findings.filter((f) => !isExempt(f, registry))
}

/**
 * 扫一份源码 —— 判据的**唯一实现**。
 *
 * ★★★ 为什么必须把它抽出来（这是自证有效的前提）：
 *   第一版把自证写成「在自证里再抄一遍判据」⇒ 自证证明的是**那份抄件**对不对，
 *   不是**真判据**对不对 ⇒ 抄件与真实现漂移时，自证照样全绿 ⇒ **自证变成摆设**。
 *   ⇒ 自证与真扫描必须走**同一个函数**（★ 同一口径只能有一份实现）。
 */
export function scanSource(src: string, rel: string, file: string = rel): AiSmellFinding[] {
  const findings: AiSmellFinding[] = []
  {
    const blocks = extractCssBlocks(src, file)
    // ★ V2 需要「同族规则的边框颜色」跨规则索引（★ 形态 ③：颜色写在 modifier 规则里）
    const coloredByFamily = buildColoredBorderIndex(blocks)
    for (const b of blocks) {
      const body = stripCssComments(b.body)
      const sel = b.selector

      // ── V1 幽灵卡片：同一元素既有描边又有抬升阴影（抬升只声明一次）──
      if (hasVisibleBorder(body) && hasElevationShadow(body)) {
        const sh = hasElevationShadow(body)!
        findings.push({
          rule: 'V1-ghost-card',
          file: rel,
          line: b.line,
          selector: sel,
          text: sh.value.slice(0, 90),
          reason: '同一表面既声明描边又声明抬升阴影 ⇒ 抬升被声明了两次（craft-floor：ghost card）',
        })
      }

      // ── V2 彩色侧边条（★ 五种写法都算：见 detectSideStripe 的说明）──
      const v2 = detectSideStripe(body, sel, coloredByFamily.get(stripeFamilyKey(sel)) === true)
      if (v2) {
        findings.push({
          rule: 'V2-colored-side-stripe',
          file: rel,
          line: b.line,
          selector: sel,
          text: v2.text,
          reason: v2.reason,
        })
      }

      // ── V4 字重 ≥800 ──
      const fw = body.match(/font-weight\s*:\s*(\d+)/)
      if (fw && Number(fw[1]) >= 800) {
        findings.push({
          rule: 'V4-font-weight-800',
          file: rel,
          line: b.line,
          selector: sel,
          text: fw[0],
          reason: '字重 ≥800（DESIGN.md §4：只用 400/500/600/700）',
        })
      }

      // ── V5 uppercase ──
      if (/text-transform\s*:\s*uppercase/i.test(body)) {
        findings.push({
          rule: 'V5-uppercase-transform',
          file: rel,
          line: b.line,
          selector: sel,
          text: 'text-transform: uppercase',
          reason: '中文界面里 uppercase 无意义（DESIGN.md §4 规则 5）',
        })
      }

      // ── V6 渐变文字 ──
      if (/background-clip\s*:\s*text|-webkit-background-clip\s*:\s*text/i.test(body)) {
        findings.push({
          rule: 'V6-gradient-text',
          file: rel,
          line: b.line,
          selector: sel,
          text: 'background-clip: text',
          reason: '渐变文字（craft-floor：ban-gradient-text）',
        })
      }

      // ── V7 硬偏移阴影（零模糊 + 非零偏移）──
      // ★ 第 18 增量修正（真仓实测的盲区）：旧正则在第 2 段**要求 px 单位**，
      //   而 CSS 允许省略单位（`0` / `-1px 0 0 0 var(--accent)`）⇒
      //   零模糊 + 有偏移的硬偏移阴影被漏判（真仓 `HotPartitionManager.vue` 实测 1 处）。
      //   ⇒ 改为逐层判定，长度单位一律可省略。
      // ★ 第 18 增量再修（真仓变异臂 M3 抓到）：旧实现只取块内**第一条** box-shadow
      //   ⇒ 同块里的第二条声明完全看不见 ⇒ 改用共享遍历器 `boxShadowDeclarations`。
      for (const decl of boxShadowDeclarations(body)) {
        if (isInsetOnly(decl.value)) continue
        for (const layer of shadowLayers(decl.value)) {
          if (layer.offsetX === null || layer.offsetY === null) continue
          if (layer.offsetX === 0 && layer.offsetY === 0) continue
          const blurM = layer.raw.match(
            /^(-?\d+(?:\.\d+)?)(?:px)?\s+(-?\d+(?:\.\d+)?)(?:px)?\s+(-?\d+(?:\.\d+)?)(?:px)?(?:\s|$)/
          )
          if (!blurM) continue
          if (Number(blurM[3]) !== 0) continue
          findings.push({
            rule: 'V7-hard-offset-shadow',
            file: rel,
            line: b.line,
            selector: sel,
            text: layer.raw.slice(0, 90),
            reason: '零模糊硬偏移阴影（craft-floor：ban-hard-offset-shadow）',
          })
          break
        }
      }

      // ── V8 字距低于 −0.04em ──
      const ls = body.match(/letter-spacing\s*:\s*(-?\d*\.?\d+)em/)
      if (ls && Number(ls[1]) < -0.04) {
        findings.push({
          rule: 'V8-tracking-too-tight',
          file: rel,
          line: b.line,
          selector: sel,
          text: ls[0],
          reason: '字距低于 −0.04em（craft-floor codex：tracking 不得低于 −0.04em）',
        })
      }
      // ── V9 装饰性彩色光晕 ──
      // ★ 精确口径：**零偏移 + 有模糊半径 + 彩色**。
      //   `0 0 0 1px <color>`（blur=0）⇒ 是**用阴影画的描边**（圆环替身），不是光晕 ⇒ 不判
      //   `0 0 0 2px <color>`（焦点环）⇒ 同上 ⇒ 不判（craft-floor 反而要求 theming 焦点环）
      //   `0 0 5px color-mix(…)` ⇒ 真正的**彩色光晕** ⇒ 判（craft-floor：零偏移彩色光晕是装饰）
      const shAll = body.match(/box-shadow\s*:\s*([^;]+)/)
      if (shAll) {
        for (const part of splitTopLevel(shAll[1])) {
          if (/inset/.test(part)) continue
          const m = part.match(/^\s*(-?\d+(?:\.\d+)?)(?:px)?\s+(-?\d+(?:\.\d+)?)(?:px)?\s+(-?\d+(?:\.\d+)?)(?:px)?/)
          if (!m) continue
          const offX = Number(m[1])
          const offY = Number(m[2])
          const blur = Number(m[3])
          if (offX !== 0 || offY !== 0) continue
          if (blur <= 0) continue
          if (!isColoredValue(part)) continue
          findings.push({
            rule: 'V9-zero-offset-shadow',
            file: rel,
            line: b.line,
            selector: sel,
            text: part.slice(0, 90),
            reason: '装饰性彩色光晕（零偏移 + 模糊）—— craft-floor：阴影要有偏移与柔化，零偏移彩色光晕是装饰',
          })
        }
      }
    }
    findings.push(...detectGlyphIcons(src, rel))
    // ★ 第 29 增量：V3b —— 数据驱动的字形图标（`icon: '📋'` 这类）
    findings.push(...detectGlyphDataDriven(src, rel))
    // ★ 第 27 增量：从已退役的孤儿门移植进来的两条判据（判据唯一实现 = 本文件）
    findings.push(...detectKickers(src, rel))
    findings.push(...detectDecorativeNumbers(src, rel))
    // ★ 第 36 增量：V11b —— **手写字面量**的装饰序号（V11 只认 `padStart` 生成的那种）
    findings.push(...detectDecorativeNumberLiterals(src, rel))
  }
  return findings
}

export function isExempt(
  f: AiSmellFinding,
  registry: Array<{ file: string; rule: string; selector?: string; reason: string }>,
): boolean {
  return registry.some(
    (e) =>
      e.file === f.file &&
      (e.rule === '*' || e.rule === f.rule) &&
      (!e.selector || e.selector === f.selector),
  )
}

/**
 * V3：Unicode 字形 / emoji 冒充图标。
 *
 * ★ craft-floor 原话（Refuse 段）：
 *   "Unicode glyphs or emoji standing in for an icon system. Icons are drawn,
 *    from a real library or authored SVG, in one consistent stroke and weight."
 *   ⇒ 判据必须分清两件事：
 *      【位置】这个位置**期望放图标**吗？
 *      【形状】这个字形**长得像图标**吗（而不是正文占位符）？
 *
 * ★★★ 本判据是第 2 版。第 1 版（单行正则 + 只认 icon/glyph/symbol/emoji 四个词）
 *   有两个**已被真仓实测证实的盲区**：
 *   ① **写法覆盖面不足**：第 1 版用单行正则 `class="…"[^>]*>字形<`
 *      ⇒ class 与 `>` **跨行**就漏；`:class="…"` **动态绑定**就漏
 *      （真仓实测：`QueuePerspectivePanel` 的 `▸`（跨行）、`HotPartitionManager` 的 `↻`（动态绑定））。
 *   ② **位置信号太窄**：只认 icon/glyph/symbol/emoji 四个词
 *      ⇒ 真仓里 `btn-icon` / `check-mark` / `refresh-btn` / `tp-close` 全在门外。
 *   ⇒ 第 2 版：**写法全覆盖**（跨行 + class/:class/v-bind:class）+ **位置信号扩到「按钮/链接位或 class 含图标语义词」**。
 *
 * ★ 排除项（都是真仓里出现过的合法形态）：
 *   - CJK / 全角标点 ⇒ 是文字不是图标；
 *   - 纯排印字符（— – · … ▍）⇒ 正文占位符 / 分隔符，**不是图标位**；
 *   - 流程箭头（→ ↔ ↕ ⇄ …）在**非按钮位** ⇒ 是流程连线示意，不是图标系统。
 */
/**
 * 剥掉源码里的注释（JS 块注释 / HTML 注释 / JS 行注释）。
 *
 * ★ 移植自已退役的 `scripts/craft-floor-audit.mjs`（第 27 增量收口）：
 *   那道孤儿门守着同样的两条判据（kicker / 装饰序号）⇒ 孤儿门退役后，
 *   这段实现必须**搬进判据唯一实现**，不能留在孤儿门里（★ 同一口径只能有一份实现）。
 * ★ 替换成等长空格 ⇒ **行号与偏移不变**（报告不会指错位置）。
 */
export function stripSourceComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, (m) => m.replace(/[^\n]/g, ' '))
    .replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '))
    .replace(/(^|[^:])\/\/[^\n]*/g, (m, p1) => p1 + ' '.repeat(Math.max(0, m.length - p1.length)))
}

/**
 * V10：标题上方的 kicker / eyebrow。
 *
 * ★ craft-floor 原话（Refuse 段）：
 *   "A kicker or eyebrow above a heading. **This one is a ban, not a default:
 *    no brief earns it back.** The heading carries its own weight; delete the
 *    label and let the heading speak."
 *   ⇒ 这是**硬禁**（不是「默认不好」而是「brief 也不能赎回它」）。
 *
 * ★ 位置判据（★ 按【形状】判，不按【写法】判）：
 *   只认**紧邻标题上方**的那个小标签 ⇒ class 名里带 kicker/eyebrow 但不在标题上方的
 *   （比如一个普通徽标）**不判** ⇒ 判据宁可窄，不可宽。
 * ★ 与已退役的孤儿门相比**更窄**：孤儿门是「class 含 kicker/eyebrow 就判红」。
 */
export function detectKickers(src: string, rel: string): AiSmellFinding[] {
  const out: AiSmellFinding[] = []
  const els = scanElements(src)
  const HEADINGS = new Set(['h1', 'h2', 'h3', 'h4', 'h5', 'h6'])
  const KICKER_WORDS = new Set(['kicker', 'eyebrow', 'overline', 'pretitle', 'supertitle'])
  for (let i = 0; i < els.length; i++) {
    const e = els[i]
    const words = classWords(e.attrs)
    const hit = words.find((w) => KICKER_WORDS.has(w))
    if (!hit) continue
    // ★ 必须在**紧邻的下一个元素是标题**时才算「标题上方的小标签」
    const next = els[i + 1]
    if (!next || !HEADINGS.has(next.tag)) continue
    const rawFirst = rawClassFirst(e.attrs)
    out.push({
      rule: 'V10-kicker-above-heading',
      file: rel,
      line: lineOf(src, e.offset),
      selector: rawFirst ? `.${rawFirst}` : `<${e.tag}>`,
      text: e.content.trim().slice(0, 60),
      reason:
        `标题上方的 kicker/eyebrow（class 含 "${hit}"）—— craft-floor 硬禁：` +
        '「This one is a ban, not a default: no brief earns it back」；标题自己扛重量',
    })
  }
  return out
}

/**
 * V11：用代码把序号补零成两位（01 / 02 / 03）。
 *
 * ★ craft-floor 原话（Refuse 段）：
 *   "Section numbers (01 / 02 / 03) **unless the sequence itself carries
 *    information the reader needs**."
 *   ⇒ 判据只抓「**把一个序号补零**」这个动作 ⇒ 页面上写着 01 但那串序号
 *     本身就在传递信息（步骤 1/2/3）时**不判**（那是「序列携带信息」的合法形态）。
 *
 * ★ 与已退役的孤儿门同一口径（移植）：`padStart(2, '0')` + 序号语境
 *   （index/idx/i/count/step/no/num/order/seq）+ 排除日期取值语境
 *   （`getHours()` 之类补零是**时间**，不是装饰序号）。
 */
export function detectDecorativeNumbers(src: string, rel: string): AiSmellFinding[] {
  const out: AiSmellFinding[] = []
  const masked = stripSourceComments(src)
  const re = /padStart\s*\(\s*2\s*,\s*['"]0['"]\s*\)/g
  const ORDINAL =
    /String\s*\(\s*(?:[A-Za-z_$][\w$]*\s*\+\s*1|(?:index|idx|i|count|step|no|num|order|seq)\b[^)]*)\s*\)/
  const DATETIME = /get(Hours|Minutes|Seconds|Month|Date|Day|FullYear|Year)\s*\(/
  for (const m of masked.matchAll(re)) {
    const lineStart = src.lastIndexOf('\n', m.index) + 1
    const line = src.slice(lineStart, src.indexOf('\n', m.index))
    if (DATETIME.test(line)) continue
    const ctx = masked.slice(Math.max(0, m.index - 120), m.index)
    if (!ORDINAL.test(ctx)) continue
    out.push({
      rule: 'V11-section-number',
      file: rel,
      line: lineOf(src, m.index),
      selector: '',
      text: m[0],
      reason:
        "padStart(2, '0') 生成的 01/02 序号 —— craft-floor：序号只在序列本身携带信息时才用",
    })
  }
  return out
}

/**
 * ★★ V11b：**手写字面量**的装饰序号（第 36 增量）。
 *
 * ★★ 这条判据补的是 V11 的**盲区**：V11 只认 `padStart(2, '0')` **生成**的序号
 *   ⇒ 而数据数组里**手写**的 `'01'` / `'02'` 它完全看不见
 *   ⇒ ★ 与 V3b（第 29 增量补的「emoji 藏在数据数组里」）**完全同型**：
 *   「检测器只认一种形态 ⇒ 另一种形态整族漏网」。
 * ★ 真仓实证：`web/src/views/LandingView.vue` 的 8 个特性卡写死了
 *   `icon: '01'` … `icon: '08'` ⇒ ★ 模板压根不渲染 `item.icon`
 *   （`LandingFeature` 类型里也没有这个字段）⇒ 是**死数据**，但它是颗地雷：
 *   哪天有人把它接回模板，界面上就会冒出 01/02 装饰序号（craft-floor 明禁）。
 *
 * ★ 位置信号 = **属性名**（与 V3b 同款思路）：属性名含序号/图标位语义
 *   （icon / glyph / emoji / num / seq / idx / index / order / step / badge / marker / ordinal）
 *   ⇒ ★ 判据宁可窄，不可宽：普通文本里的 '01'（比如限额 '50'）**不判**。
 * ★ 值只认**零填充两位数**（`'01'` … `'09'`）—— 这正是 craft-floor 写的
 *   「Section numbers (01 / 02 / 03)」那个形状 ⇒ ⇒ 实测全仓假阳性 0 处
 *   （`step="60"` 那种属性值不是零填充 ⇒ 不命中）。
 */
export function detectDecorativeNumberLiterals(src: string, rel: string): AiSmellFinding[] {
  const out: AiSmellFinding[] = []
  const masked = stripSourceComments(src)
  // ★ 属性名先捕获（与 V3b 同构），值只认 '01'..'09'。
  const re = /\b([A-Za-z_$][\w$]*)\s*[:=]\s*(['"])(0[1-9])\2/g
  const NAME = /icon|glyph|emoji|num|seq|idx|index|order|step|badge|marker|ordinal/i
  let m: RegExpExecArray | null
  while ((m = re.exec(masked))) {
    const attr = m[1]
    if (!NAME.test(attr)) continue
    out.push({
      rule: 'V11b-section-number-literal',
      file: rel,
      line: lineOf(src, m.index),
      selector: attr,
      text: m[3],
      reason:
        "手写的 '01' 装饰序号 —— craft-floor：序号只在序列本身携带信息时才用（V11 只认 padStart 生成的那种）",
    })
  }
  return out
}

/**
 * V3：Unicode 字形 / emoji 冒充图标。
 *
 * ★★★ 第 27 增量：**一个元素只报一条**（★ 口径修正）。
 *   第一版是「一个字形一条」⇒ ★ 一个元素里有两个候选字形（`{{ a ? '▴' : '▾' }}`）
 *   就报两条 ⇒ ★ 计数口径与「要改几个地方」**对不上**（★ 计数类结论：
 *   口径必须和数字一起发布）⇒ ⇒ 现在按**元素**聚合，字形合并进 `text`。
 *   ★ 自证里钉住这一点（S-CF3 期望 1 条，不是 2 条）。
 */
/**
 * ★★ V3b：**数据驱动**的字形图标（第 29 增量）。
 *
 * ★★ 这条判据补的是 V3 的**盲区**：V3 扫的是「元素里的字面量」，
 *   而 emoji 藏在一个**数据数组**里时（`{ key: 'x', icon: '📋' }`），
 *   源码里的元素写的是 `{{ c.icon }}` ⇒ **V3 完全看不见它**。
 *   ★ 真仓实证：`web/src/views/SettingsView.vue` 的 11 个设置分类图标
 *     就是这么藏的（`icon: '📋'` / `'🗜'` / `'🚦'` …）⇒ 渲染成 16px 字形 ⇒
 *     **用户在界面上看到的是 emoji 冒充图标**（DESIGN.md §6 明令禁止）。
 *
 * ★ 位置信号：**属性名含 icon / glyph / emoji** ⇒ 值**恰好是一个字形**。
 *   ⇒ 判据宁可窄，不可宽：普通文本里的字形（文字标记）**不判**
 *   （★ 与仓库既有口径一致：`glyphCandidates` 只认「元素里只有这个字形」）。
 *
 * ★ 注释免疫：`stripSourceComments` 把注释内容掩成等长空格（行号/偏移不变）
 *   ⇒ 注释里写 `icon: '📋'` 不会造成假红（★ 本轮自己就写了这种注释 ⇒ 实测需要）。
 */
export function detectGlyphDataDriven(src: string, rel: string): AiSmellFinding[] {
  const out: AiSmellFinding[] = []
  const masked = stripSourceComments(src)
  // ★ 属性名先捕获，再在代码里判「是否含 icon / glyph / emoji」
  //   （★ 第一版把它写进正则 `(?:icon|glyph|emoji)` 且要求前缀至少一个字符
  //     ⇒ **单独的 `icon:` 匹配不上** ⇒ 判据没牙 ⇒ ★ 是自证 S21a 抓出来的）
  const re = /\b([A-Za-z_$][\w$]*)\s*[:=]\s*(['"])((?:\\.|(?!\2).)*)\2/g
  let m: RegExpExecArray | null
  while ((m = re.exec(masked))) {
    const attr = m[1]
    if (!/icon|glyph|emoji/i.test(attr)) continue
    const value = m[3]
    const chars = [...value]
    if (!chars.length) continue
    // ★ 值必须**恰好是一个字形**（⇒ 整块就是一个图标位）
    //   ★ 修饰符（U+FE0F 变体选择符 / U+20E3 组合键帽）不单独算字形，
    //     但它们紧跟字形时要**一起放行**（★ '⚠️' = U+26A0 + U+FE0F）
    const core = chars.filter((c) => c !== '\uFE0F' && c !== '\u20E3')
    if (!core.length) continue
    if (!core.every((c) => isGlyphIconChar(c))) continue
    out.push({
      rule: 'V3b-glyph-data-driven',
      file: rel,
      line: lineOf(src, m.index),
      selector: m[1],
      text: value,
      reason:
        '数据驱动的字形图标（DESIGN.md §6：图标是画出来的，不许用 emoji / Unicode 字形冒充图标系统）',
    })
  }
  return out
}

export function detectGlyphIcons(src: string, rel: string): AiSmellFinding[] {
  const out: AiSmellFinding[] = []

  for (const el of scanElements(src)) {
    // 位置信号：按钮/链接位，或 class 名含图标语义词
    const isButtonLike = el.tag === 'button' || el.tag === 'a'
    const roleHit = classWords(el.attrs).some((w) => ICON_ROLE_WORDS.has(w))
    if (!isButtonLike && !roleHit) continue

    // ★ 收集这个元素上的所有字形候选（★ 聚合口径：一个元素一条）。
    const hits: string[] = []
    for (const ch of glyphCandidates(el.content)) {
      // 文本而非图标：ASCII / CJK / 全角标点
      if (/[\x00-\x7F]/.test(ch)) continue
      if (/[\u4e00-\u9fff\u3000-\u303f\uff00-\uffef]/.test(ch)) continue
      // 纯排印字符 ⇒ 正文占位符 / 分隔符，不是图标位
      const chars = [...ch]
      if (chars.every((c) => TYPOGRAPHIC_GLYPHS.has(c))) continue
      // 流程箭头在非按钮位 ⇒ 流程连线示意，不是图标系统
      if (!isButtonLike && chars.every((c) => FLOW_ARROWS.has(c))) continue
      hits.push(ch)
    }
    if (!hits.length) continue

    // ★ selector 用**原始 class 的第一个类名**（如 `mp-chip-x`）——
    //   切词后的第一词（`mp`）定位不到 CSS 规则，报告里等于没用。
    const rawFirst = rawClassFirst(el.attrs)
    out.push({
      rule: 'V3-glyph-as-icon',
      file: rel,
      // ★ 行号指向**字形本身**，不是开标签行
      //   （开标签常跨多行：`…>×</button>` 的 `×` 在闭合行）⇒
      //   报告行号若指向开标签，按行号定位会找不到字形（真仓实测 20/78 条如此）。
      line: lineOf(src, el.contentStart + (el.content.length - el.content.trimStart().length)),
      selector: rawFirst ? `.${rawFirst}` : `<${el.tag}>`,
      text: hits.join(' '),
      reason: 'Unicode 字形 / emoji 冒充图标（craft-floor：ban-glyph-icons）',
    })
  }
  return out
}

/**
 * 取「可能是图标位字形」的候选串。
 *
 * ★ 两种形态（真仓实测都出现过）：
 *   ① 纯文本形态：`<span class="x-icon">📖</span>`
 *   ② **插值形态**：`<span class="x-glyph">{{ isDark ? '☾' : '☼' }}</span>`
 *      —— 只认 ① 的话，`ThemeToggle` 的月牙/太阳图标整族漏判
 *      （浏览器实测页面仍显示 `☾` 字形 ⇒ 门绿但界面没改）。
 *      ② 的字面量是**静态可知**的 ⇒ 应当纳入判据。
 */
export function glyphCandidates(inner: string): string[] {
  const t = inner.trim()
  if (!t) return []
  if (!t.includes('{{')) {
    return t.length > 4 ? [] : [t]
  }
  const out: string[] = []
  const re = /\{\{([\s\S]*?)\}\}/g
  let m: RegExpExecArray | null
  while ((m = re.exec(inner))) {
    for (const q of m[1].matchAll(/'([^']*)'|"([^"]*)"/g)) {
      const lit = (q[1] ?? q[2] ?? '').trim()
      if (lit && lit.length <= 4) out.push(lit)
    }
  }
  // ★★ 第 27 增量**刻意不采集「插值之外的裸文本」**（★ 试过 ⇒ 回退，记录理由）：
  //   ★ 形态 ③ 存在：按钮标签**后面挂一个字形**（形如 `{标签} ✕`）⇒ 采集它会让门多报 44 处。
  //   ★★ ★★ ⚠ **注释里不许写「取键调用 + 引号键名」的样例**（★ 本轮踩过 ⇒ ★ i18n 扫描器
  //   **不剥注释** ⇒ ⇒ 注释里那种样例会被当成**真键引用** ⇒ ⇒ 键名进可达集
  //   ⇒ ⇒ 8 个语种全缺 ⇒ ⇒ `i18n:key-parity:check` 变红（★ 第 11 增量记的
  //   「扫描器剥注释（幽灵引用 11 处 / 8 文件）」那笔的又一例）⇒ ⇒ 这里一律用 `{标签}` 代替。
  //   ★ 但**仓内自己的口径**是「字形 + 文本 = 文字标记，不是图标位」⇒ ★ **判绿**：
  //     · 孤儿门那条绿样本「dingbat 在带文字的按钮上做前后缀 ⇒ 必须判绿」；
  //     · 同族绿样本「dingbat 是句子的一部分 ⇒ 必须判绿」；
  //     · 实测全仓 **172 处**「文本以字形开头」（★ 多为 `★` 作危险标记前缀）
  //       ⇒ ★ 这是本仓的**既有惯例**，不是待清的存量。
  //   ⇒ ⇒ 判据只认「**图标位**（元素里只有这个字形）」⇒ ★ 口径窄、与仓内惯例一致。
  //   ★★ 代价（★ 如实记为局限）：★ 「标签旁边挂一个字形」这类冗余（★ 如关闭按钮上的 `✕`）
  //     **判据看不见** ⇒ ★ 量测 = **80 处 / 34 文件**（★ 多数是 `✕` `←` `↻` `→` `⚠️`）
  //     ⇒ ★ 列进下一轮工作线（★ 属「冗余装饰」，不是 craft-floor 硬禁）。
  return out
}

/** 取字面量 class 的第一个类名（不动态绑定） */
function rawClassFirst(attrs: string): string {
  const m = /(?:^|\s)class\s*=\s*"([^"]*)"/.exec(attrs)
  if (!m) return ''
  const first = m[1].trim().split(/\s+/)[0] ?? ''
  return first.replace(/[^\w-]/g, '')
}

/** 图标语义词：class 名按 `-` `_` 空格切词后**整词**匹配 */
const ICON_ROLE_WORDS = new Set([
  'icon', 'glyph', 'symbol', 'emoji', 'pictogram', 'mark', 'arrow', 'chevron',
  'caret', 'badge', 'avatar', 'logo', 'dot', 'star', 'indicator', 'check', 'tick',
  'refresh', 'reload', 'rotate', 'menu', 'search', 'setting', 'gear', 'link',
  'zoom', 'drag', 'handle', 'flag', 'collapse', 'expand', 'page', 'sort', 'prev',
  'next', 'open', 'fullscreen', 'warn', 'warning', 'info', 'error', 'remove',
  'delete', 'del', 'close', 'clear', 'lock', 'unlock', 'copy', 'download',
  'upload', 'share', 'pin', 'bookmark', 'eye', 'folder', 'document', 'list',
  'grid', 'table', 'chart', 'server', 'database', 'key', 'clock', 'timer',
  'calendar', 'tag', 'filter', 'branch', 'merge', 'split', 'swap', 'loop',
  'chip', 'plus', 'minus', 'edit', 'bell', 'user', 'home', 'file', 'image',
  'video', 'audio', 'x',
  // ★ 第 27 增量补 'ico'：★ 已退役的孤儿门 `craft-floor-audit.mjs` 的
  //   `ICON_SLOT_CLASS_RE` 含 `ico`，而本表**没有** ⇒ ★ `<span class="ico">📖</span>`
  //   这一族（真仓实测形态）在 ai-smells 门下是**漏判** ⇒ ★ 孤儿门退役前
  //   必须把它那份覆盖补进来（★ 退役 = 覆盖不许缩水）。
  'ico',
])

/** 纯排印字符：正文占位符 / 分隔符，不是图标位 */
/**
 * ★★ 字形字符判定的**唯一实现**（第 29 增量）。
 *
 * ★ 一个字符是不是「emoji / 图标位字形」：
 *  - `Extended_Pictographic` ⇒ ✅❌🔍🧹🚫⏭ 等**具象图形**；
 *  - `Emoji_Presentation` ⇒ ⏳🟡 等**默认以 emoji 呈现**的符号；
 *  - U+FE0F（变体选择符）/ U+20E3（组合键帽）⇒ 是 emoji 的**修饰符**，
 *    本身不可见、单独出现不算图标（紧跟 emoji 时随那个 emoji 一起算）。
 *
 * ★ 排除项（与「排印符号」「流程箭头」共用同一份集合）：
 *  `TYPOGRAPHIC_GLYPHS`（— – · … ▍ ⋯）是标点；`FLOW_ARROWS`（→ ← ↔ …）是连线示意。
 *  ★ 真仓实证：`providerDetail.formatHint` 里的 `↔`（协议互转）**必须**不算图标。
 *
 * ★ 为什么放这里（而不是放 i18n 侧）：源码侧与语言包侧**共用同一份字符判定**
 *   ⇒ 「同一口径只能有一份实现」；依赖方向是 i18n → design（单向）。
 */
export const GLYPH_ICON_TEST_RE =
  /[\p{Extended_Pictographic}\p{Emoji_Presentation}\uFE0F\u20E3]/u

export function isGlyphIconChar(ch: string): boolean {
  if (!GLYPH_ICON_TEST_RE.test(ch)) return false
  if (TYPOGRAPHIC_GLYPHS.has(ch)) return false
  if (FLOW_ARROWS.has(ch)) return false
  // ★ 变体选择符 / 组合键帽单独出现时不可见、也不是图标
  if (ch === '\uFE0F') return false
  if (ch === '\u20E3') return false
  return true
}

export const TYPOGRAPHIC_GLYPHS = new Set(['—', '–', '·', '…', '▍', '⋯'])

/** 流程 / 连接箭头：在流程示意位置是连线，不是图标系统 */
export const FLOW_ARROWS = new Set([
  '→', '←', '↔', '↕', '⇄', '⇅', '⇆', '↖', '↗', '↘', '↙',
  // ★ 第 27 增量补 `↑` `↓`：移动端 `RouteFunnelView` 的比率箭头 `↓` 是**流程连线示意**
  //   （class 叫 fn__rate-arrow ⇒ 位置信号命中 'arrow'）⇒ 补进流程箭头集合后，
  //   「非按钮位的流程箭头」不再被当成图标系统（按钮位仍然判红）。
  '↑', '↓',
])

interface ScannedElement {
  tag: string
  attrs: string
  content: string
  offset: number
  /** content 在 src 里的起始偏移（用于把行号指向字形本身） */
  contentStart: number
}

/**
 * 扫描模板里的元素（跨行安全）。
 * ★ 与第 1 版正则相比的两处关键修正：
 *   - `i` 只前进到**开标签之后**（不跳到闭合标签之后）⇒ `<template>` 不会把整个模板吞掉；
 *   - 闭标签按**同名**配对，inner 里含 `<` ⇒ 不是纯文本元素，直接跳过。
 */
export function scanElements(src: string): ScannedElement[] {
  const out: ScannedElement[] = []
  let i = 0
  while (i < src.length) {
    const lt = src.indexOf('<', i)
    if (lt < 0) break
    if (src.startsWith('<!--', lt)) {
      const e = src.indexOf('-->', lt)
      i = e < 0 ? lt + 4 : e + 3
      continue
    }
    const m = /^<([a-zA-Z][\w-]*)/.exec(src.slice(lt, lt + 40))
    if (!m) {
      i = lt + 1
      continue
    }
    const tag = m[1]
    // ★ 第 27 增量：★ `<script>` / `<style>` 块**不是模板文本** ⇒ 跳过整块。
    //   （★ 它们的正文里可能含字形（★ 例如 CSS 注释里写了 `▴` / `▾`）⇒
    //   ★ 不跳过的话，字形清点这类**量测**会把注释里的字形算进来。）
    if (tag === 'script' || tag === 'style') {
      const closeTag = src.indexOf(`</${tag}`, lt + m[0].length)
      i = closeTag < 0 ? src.length : closeTag + tag.length + 3
      continue
    }
    let j = lt + m[0].length
    let quote: string | null = null
    while (j < src.length) {
      const c = src[j]
      if (quote) {
        if (c === quote) quote = null
      } else if (c === '"' || c === "'") quote = c
      else if (c === '>') break
      j++
    }
    if (j >= src.length) {
      i = lt + 1
      continue
    }
    const attrs = src.slice(lt + m[0].length, j)
    const close = src.indexOf(`</${tag}`, j + 1)
    i = j + 1
    if (close < 0) continue
    const inner = src.slice(j + 1, close)
    // ★ 只排除**嵌套标签**（inner 含 `<` ⇒ 不是纯内容元素）；
    //   插值（`{{ … }}`）要保留 —— 字形可能藏在插值里的字面量中（见 glyphCandidates）。
    if (inner.includes('<')) continue
    out.push({ tag, attrs, content: inner, offset: lt, contentStart: j + 1 })
  }
  return out
}

/** 从属性串里提取 class 名候选（字面量 class + 动态绑定里的字符串字面量） */
export function classWords(attrs: string): string[] {
  const out: string[] = []
  const re = /(?:^|\s)(?::class|v-bind:class|class)\s*=\s*"([^"]*)"/g
  let m: RegExpExecArray | null
  while ((m = re.exec(attrs))) {
    const val = m[1]
    const quoted = [...val.matchAll(/['"]([^'"]*)['"]/g)].map((x) => x[1])
    const raw = quoted.length ? quoted : [val]
    for (const piece of raw) {
      for (const w of piece.split(/[\s_-]+/)) {
        if (w) out.push(w.toLowerCase())
      }
    }
  }
  return out
}

function lineOf(src: string, offset: number): number {
  let n = 1
  for (let i = 0; i < offset && i < src.length; i++) if (src[i] === '\n') n++
  return n
}

// ───────────────────────── 自证 ─────────────────────────

/**
 * 自证：每条判据至少 1 个正例 + 1 个反例；
 * ★ 反例必须包含**真仓里出现过的合法形态**（否则门会带着假阳性出厂）。
 */
export function runSelftest(): { passed: number; total: number; failed: string[] } {
  const cases: Array<{ id: string; desc: string; expect: number; actual: number }> = []
  const check = (id: string, desc: string, expect: number, actual: number) => {
    cases.push({ id, desc, expect, actual })
  }
  // ★ 自证与真扫描走**同一个实现**（scanSource）——
  //   判据只有一份；自证不是判据的复制品。
  const scan = (src: string): AiSmellFinding[] => scanSource(src, 'x.vue', 'x.vue')

  const rules = (src: string) => scan(src).map((f) => f.rule)

  // S1：干净样本 ⇒ 0 命中
  check('S1', '干净样本 ⇒ 0 命中', 0, scan(`
<style scoped>
.card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; }
.pop { background: var(--card); box-shadow: var(--kx-shadow-sm); border-radius: 12px; }
</style>`).length)

  // S2：幽灵卡片（描边 + 阴影，跨行也要命中 ⇒ 块作用域）
  check('S2a', '描边 + 阴影（同行）⇒ V1', 1, rules(`
<style scoped>
.card { border: 1px solid var(--border);
        box-shadow: 0 8px 24px var(--overlay-light); }
</style>`).filter((r) => r === 'V1-ghost-card').length)
  check('S2b', '描边 + 阴影（分行）⇒ V1（块作用域，不能漏）', 1, rules(`
<style scoped>
.card {
  border: 1px solid var(--border);
  box-shadow: 0 8px 24px var(--overlay-light);
}
</style>`).filter((r) => r === 'V1-ghost-card').length)

  // S3：纯内阴影不算抬升 ⇒ 不判幽灵卡片
  check('S3', '只有内阴影 ⇒ 不判 V1', 0, rules(`
<style scoped>
.card { border: 1px solid var(--border); box-shadow: inset 0 1px 0 rgba(0,0,0,.04); }
</style>`).filter((r) => r === 'V1-ghost-card').length)

  // S4：border: none 不算可见描边
  check('S4', 'border:none + 阴影 ⇒ 不判 V1', 0, rules(`
<style scoped>
.pop { border: none; box-shadow: 0 8px 24px var(--overlay-light); }
</style>`).filter((r) => r === 'V1-ghost-card').length)

  // S5：彩色侧边条 —— 两种写法都要命中（★ 形状判据）
  check('S5a', 'border-left 3px 彩色 ⇒ V2', 1, rules(`
<style scoped>
.item { border-left: 3px solid var(--kx-danger); }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  check('S5b', 'inset 3px 0 0 彩色（伪装形态）⇒ V2', 1, rules(`
<style scoped>
.item { box-shadow: inset 3px 0 0 var(--accent); }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)

  // S6：中性侧边条不判（真仓合法形态：树连线 / chevron）
  check('S6a', 'border-left 2px 中性（树连线）⇒ 不判 V2', 0, rules(`
<style scoped>
.tree { border-left: 2px solid var(--kx-border); }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  check('S6b', 'border-right 2px 中性（chevron）⇒ 不判 V2', 0, rules(`
<style scoped>
.chev { border-right: 2px solid var(--kx-muted); }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  check('S6c', 'border-left 1px 彩色 ⇒ 不判 V2（阈值是 2px）', 0, rules(`
<style scoped>
.item { border-left: 1px solid var(--kx-danger); }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)

  // S7：字形当图标
  check('S7a', 'emoji 在 icon 类里 ⇒ V3', 1, rules(`
<span class="guide-icon" aria-hidden="true">📖</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S7b', '几何符号在 icon 类里 ⇒ V3', 1, rules(`
<span class="gw__store-icon" aria-hidden="true">▤</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S7c', 'icon 类里的中文 ⇒ 不判 V3（那是文字不是图标）', 0, rules(`
<span class="nav-icon">供应商</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S7d', '非图标位（class 无图标语义词 + 非按钮）⇒ 不判 V3', 0, rules(`
<span class="pill-text">★</span>`).filter((r) => r === 'V3-glyph-as-icon').length)

  // ★ S20：V3 第 2 版修掉的两个盲区（真仓实测形态）
  //   ① 写法覆盖面：跨行 class / :class 动态绑定
  //   ② 位置信号：按钮位 / 排印占位符 / 流程箭头
  check('S20a', 'class 与 > 跨行 ⇒ 仍判 V3（第 1 版漏）', 1, rules(`
<span
  class="qp-model-group-caret"
  :class="{ 'is-open': open }"
>▸</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S20b', ':class 动态绑定 ⇒ 仍判 V3（第 1 版漏）', 1, rules(`
<span :class="['reload-icon', { spinning: loading }]">↻</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S20c', '按钮位（class 无图标语义词）⇒ 判 V3', 1, rules(`
<button type="button" class="btn btn-ghost btn-sm">×</button>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S20d', '排印占位符（— 在 muted 类里）⇒ 不判 V3（是正文占位不是图标）', 0, rules(`
<span class="text-muted">—</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S20e', '流程箭头在非按钮位 ⇒ 不判 V3（是流程连线示意）', 0, rules(`
<span class="gw__arrow" aria-hidden="true">→</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S20f', '流程箭头在按钮位 ⇒ 判 V3（返回按钮是真的图标位）', 1, rules(`
<button type="button" class="btn btn-sm">←</button>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S20g', '徽标里的 ★ ⇒ 判 V3（字形顶替图标系统）', 1, rules(`
<span class="badge">★</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S20h', '插值内容 ⇒ 不判 V3（不是字形）', 0, rules(`
<span class="stat-icon">{{ icon }}</span>`).filter((r) => r === 'V3-glyph-as-icon').length)

  // ★ S21：V3b —— 数据驱动的字形图标（第 29 增量）
  //   ★ 真仓形态：`{ key: 'x', icon: '📋' }` ⇒ 元素里写的是 `{{ c.icon }}`
  //   ⇒ V3（只扫元素字面量）**完全看不见** ⇒ 必须有独立判据。
  check('S21a', "icon 属性 = 一个 emoji ⇒ 判 V3b", 1, rules(`
const categories = [
  { key: '', label: t('settings.category.all'), icon: '📋' },
]`).filter((r) => r === 'V3b-glyph-data-driven').length)
  check('S21b', "icon 属性 = 一个几何/字形（⚠️ 带变体选择符）⇒ 判 V3b", 1, rules(`
const c = { key: 'compression', label: '压缩', icon: '⚠️' }`).filter((r) => r === 'V3b-glyph-data-driven').length)
  check('S21c', 'icon 属性 = 图标组件名 ⇒ 不判 V3b（那是真图标）', 0, rules(`
const categories = [
  { key: '', label: t('settings.category.all'), icon: Files },
]`).filter((r) => r === 'V3b-glyph-data-driven').length)
  check('S21d', '字形 + 文本（文字标记，仓内既有惯例）⇒ 不判 V3b', 0, rules(`
const badge = { label: '✅ 可达', tone: 'ok' }`).filter((r) => r === 'V3b-glyph-data-driven').length)
  check('S21e', '★ 注释里的 icon: 字形 ⇒ 不判 V3b（注释免疫）', 0, rules(`
// 旧写法：{ icon: '📋' } ⇒ 已改成图标组件
const categories = [
  { key: '', label: t('settings.category.all'), icon: Files },
]`).filter((r) => r === 'V3b-glyph-data-driven').length)
  check('S21f', '属性名不含 icon/glyph/emoji ⇒ 不判 V3b', 0, rules(`
const mark = { badge: '📋' }`).filter((r) => r === 'V3b-glyph-data-driven').length)

  // S8：字重 / uppercase / 渐变文字 / 硬偏移阴影 / 字距
  check('S8a', 'font-weight:800 ⇒ V4', 1, rules(`
<style scoped>
h2 { font-weight: 800; }
</style>`).filter((r) => r === 'V4-font-weight-800').length)
  check('S8b', 'font-weight:700 ⇒ 不判 V4', 0, rules(`
<style scoped>
h2 { font-weight: 700; }
</style>`).filter((r) => r === 'V4-font-weight-800').length)
  check('S8c', 'uppercase ⇒ V5', 1, rules(`
<style scoped>
.lbl { text-transform: uppercase; }
</style>`).filter((r) => r === 'V5-uppercase-transform').length)
  check('S8d', 'background-clip:text ⇒ V6', 1, rules(`
<style scoped>
.h1 { background-clip: text; }
</style>`).filter((r) => r === 'V6-gradient-text').length)
  check('S8e', '硬偏移阴影 4px 4px 0 ⇒ V7', 1, rules(`
<style scoped>
.btn { box-shadow: 4px 4px 0 #000; }
</style>`).filter((r) => r === 'V7-hard-offset-shadow').length)
  check('S8f', '软阴影 0 8px 24px ⇒ 不判 V7', 0, rules(`
<style scoped>
.btn { box-shadow: 0 8px 24px var(--overlay-light); }
</style>`).filter((r) => r === 'V7-hard-offset-shadow').length)
  // ★ 第 18 增量新增：长度单位可省略时，硬偏移阴影仍须命中（旧正则漏判这一形态）
  check('S8e2', '硬偏移 -1px 0 0 0（单位省略）⇒ V7', 1, rules(`
<style scoped>
.mark { box-shadow: -1px 0 0 0 var(--accent); }
</style>`).filter((r) => r === 'V7-hard-offset-shadow').length)
  check('S8f2', '零偏移圆环 0 0 0 1px ⇒ 不判 V7（是描边替身）', 0, rules(`
<style scoped>
.ring { box-shadow: 0 0 0 1px var(--kx-border); }
</style>`).filter((r) => r === 'V7-hard-offset-shadow').length)
  check('S8f3', '零偏移彩色光晕 0 0 5px ⇒ 判 V9、不判 V7', 0, rules(`
<style scoped>
.badge { box-shadow: 0 0 5px color-mix(in srgb, var(--probe-cyan) 30%, transparent); }
</style>`).filter((r) => r === 'V7-hard-offset-shadow').length)
  check('S8g', 'letter-spacing:-0.05em ⇒ V8', 1, rules(`
<style scoped>
.h1 { letter-spacing: -0.05em; }
</style>`).filter((r) => r === 'V8-tracking-too-tight').length)
  check('S8h', 'letter-spacing:-0.02em ⇒ 不判 V8', 0, rules(`
<style scoped>
.h1 { letter-spacing: -0.02em; }
</style>`).filter((r) => r === 'V8-tracking-too-tight').length)

  // S9：注释里的声明不算（真仓 style.css 有「2026-10-24 去掉 uppercase」这种注释）
  check('S9', '注释里的 uppercase ⇒ 不判 V5', 0, rules(`
<style scoped>
/* 去掉 text-transform: uppercase */
.lbl { color: red; }
</style>`).filter((r) => r === 'V5-uppercase-transform').length)

  // S11：内阴影里带**嵌套括号的逗号**（color-mix）⇒ 仍算纯内阴影 ⇒ 不判 V1
  //   ★ 这条是判据自身的坑：顶层逗号切分切不动 color-mix(in srgb, …) 里的逗号
  check('S11', 'inset + color-mix（嵌套逗号）⇒ 不判 V1', 0, rules(`
<style scoped>
.stat-card { border: 1px solid var(--border); box-shadow: inset 0 1px 0 color-mix(in srgb, var(--kx-text) 4%, transparent); }
</style>`).filter((r) => r === 'V1-ghost-card').length)

  // S12：零偏移圆环（用阴影画的描边）⇒ 不是抬升 ⇒ 不判 V1；也不是光晕 ⇒ 不判 V9
  check('S12a', '0 0 0 1px 圆环 + 描边 ⇒ 不判 V1', 0, rules(`
<style scoped>
.marker { border: 1px solid var(--border); box-shadow: 0 0 0 1px var(--kx-border); }
</style>`).filter((r) => r === 'V1-ghost-card').length)
  check('S12b', '零偏移圆环（blur=0）⇒ 不判 V9（那是描边替身，不是光晕）', 0, rules(`
<style scoped>
.marker { box-shadow: 0 0 0 1px var(--kx-border); }
</style>`).filter((r) => r === 'V9-zero-offset-shadow').length)

  // S13：零偏移彩色光晕 ⇒ 判 V9（craft-floor：零偏移彩色光晕是装饰）
  check('S13', '0 0 5px 彩色光晕 ⇒ 判 V9', 1, rules(`
<style scoped>
.badge { box-shadow: 0 0 5px color-mix(in srgb, var(--probe-cyan) 30%, transparent); }
</style>`).filter((r) => r === 'V9-zero-offset-shadow').length)

  // S15：焦点环（blur=0 的圆环）⇒ 不判 V9（craft-floor 反而要求 theming 焦点环）
  check('S15', '焦点环 0 0 0 2px ⇒ 不判 V9', 0, rules(`
<style scoped>
.input:focus { box-shadow: 0 0 0 2px var(--accent); }
</style>`).filter((r) => r === 'V9-zero-offset-shadow').length)

  // S16：中性光晕（混的是文字色）⇒ 不判 V9
  check('S16', '中性色光晕 ⇒ 不判 V9', 0, rules(`
<style scoped>
.badge { box-shadow: 0 0 5px color-mix(in srgb, var(--kx-text) 4%, transparent); }
</style>`).filter((r) => r === 'V9-zero-offset-shadow').length)

  // S17：isColoredValue 的口径（「不是中性」而不是「命中状态色白名单」）
  check('S17a', 'color-mix 里的状态色 ⇒ 彩色', 1, isColoredValue('color-mix(in srgb, var(--success) 40%, transparent)') ? 1 : 0)
  check('S17b', 'color-mix 里的文字色 ⇒ 中性', 0, isColoredValue('color-mix(in srgb, var(--kx-text) 4%, transparent)') ? 1 : 0)
  check('S17c', '描边令牌 ⇒ 中性', 0, isColoredValue('var(--kx-border)') ? 1 : 0)
  check('S17d', '遮罩令牌 ⇒ 中性', 0, isColoredValue('var(--overlay-light)') ? 1 : 0)
  check('S17e', '探针色（非中性）⇒ 彩色', 1, isColoredValue('var(--probe-cyan)') ? 1 : 0)
  check('S17f', '十六进制 ⇒ 彩色', 1, isColoredValue('#3b82f6') ? 1 : 0)

  // S14：真抬升（带偏移）+ 描边 ⇒ 判 V1
  check('S14', '0 8px 24px 抬升 + 描边 ⇒ 判 V1', 1, rules(`
<style scoped>
.tile { border: 1px solid var(--border); box-shadow: 0 8px 24px var(--overlay-light); }
</style>`).filter((r) => r === 'V1-ghost-card').length)

  // ★ 第 18 增量新增：规范 token 抬升 + 闭合描边 ⇒ 仍判 V1
  //   （旧口径取不到 token 里的偏移 ⇒ 这一整族是假绿）
  check('S14a', '规范 token 抬升 + 闭合描边 ⇒ 判 V1', 1, rules(`
<style scoped>
.tile { border: 1px solid var(--border); box-shadow: var(--kx-shadow-sm); }
</style>`).filter((r) => r === 'V1-ghost-card').length)
  check('S14b', '规范 token 抬升 + 单边描边（边界线）⇒ 不判 V1', 0, rules(`
<style scoped>
.drawer { border-left: 1px solid var(--border); box-shadow: var(--kx-shadow-md-left); }
</style>`).filter((r) => r === 'V1-ghost-card').length)
  check('S14c', '四边齐全 + 规范 token 抬升 ⇒ 判 V1', 1, rules(`
<style scoped>
.tile { border-top: 1px solid var(--border); border-right: 1px solid var(--border);
        border-bottom: 1px solid var(--border); border-left: 1px solid var(--border);
        box-shadow: var(--kx-shadow-md); }
</style>`).filter((r) => r === 'V1-ghost-card').length)

  // S10：豁免登记簿生效
  {
    const f: AiSmellFinding = {
      rule: 'V2-colored-side-stripe',
      file: 'src/a.vue',
      line: 1,
      selector: '.x',
      text: '',
      reason: '',
    }
    const reg = [{ file: 'src/a.vue', rule: 'V2-colored-side-stripe', reason: '测试' }]
    check('S10', '登记豁免生效', 1, isExempt(f, reg) ? 1 : 0)
  }

  // ── 第 27 增量：从已退役的孤儿门移植进来的两条判据 ──
  // S-V10 kicker：只在**紧邻标题上方**时判红（比孤儿门更窄）
  check('S-V10a', '标题上方的 kicker ⇒ V10', 1, rules(`
<template><p class="eyebrow">Section</p><h2>Title</h2></template>`).filter((r) => r === 'V10-kicker-above-heading').length)
  check('S-V10b', 'kicker 不在标题上方 ⇒ 不判 V10（判据宁可窄）', 0, rules(`
<template><p class="kicker">Badge</p><div>gap</div><h2>Title</h2></template>`).filter((r) => r === 'V10-kicker-above-heading').length)
  check('S-V10c', '只有标题 ⇒ 不判 V10', 0, rules(`
<template><h2>Title</h2></template>`).filter((r) => r === 'V10-kicker-above-heading').length)

  // S-V11 装饰序号：只抓「把序号补零」这个动作
  check('S-V11a', "padStart(2,'0') 生成序号 ⇒ V11", 1, rules(`
<template><span>{{ String(index + 1).padStart(2, '0') }}</span></template>`).filter((r) => r === 'V11-section-number').length)
  check('S-V11b', '不补零 ⇒ 不判 V11', 0, rules(`
<template><span>{{ String(index + 1) }}</span></template>`).filter((r) => r === 'V11-section-number').length)
  check('S-V11c', "时间补零 ⇒ 不判 V11（那是时间不是装饰序号）", 0, rules(`
<template><span>{{ getHours().padStart(2, '0') }}</span></template>`).filter((r) => r === 'V11-section-number').length)
  check('S-V11d', "注释里的 padStart ⇒ 不判 V11", 0, rules(`
<template><span>x</span></template>
<!-- 以前这里写过 String(index + 1).padStart(2, '0') -->`).filter((r) => r === 'V11-section-number').length)

  // S-V3b 流程箭头 `↓`：非按钮位是流程连线示意 ⇒ 不判；按钮位仍是图标位 ⇒ 判
  check('S-V3b1', '非按钮位的流程箭头 `↓` ⇒ 不判 V3（移动端漏斗比率箭头）', 0, rules(`
<template><span class="fn__rate-arrow">↓</span></template>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-V3b2', '按钮位的 `↓` ⇒ 仍判 V3（图标位）', 1, rules(`
<template><button class="icon-btn">↓</button></template>`).filter((r) => r === 'V3-glyph-as-icon').length)

  // ─────────────────────────────────────────────────────────────────────────
  // ★ 第 27 增量：**孤儿门的样本并入**（`scripts/craft-floor-audit.mjs` 退役）
  //
  // ★ 为什么要搬：★ 那道孤儿门守着 27 条自证样本，其中 8 条是「emoji / 字形」
  //   这一族的边界样本（红与绿各半）⇒ ★ 门退役 = **覆盖不许缩水**
  //   ⇒ ⇒ 样本必须搬进唯一实现的自证，否则「退役」就变成了「丢覆盖」。
  //
  // ★★ 本轮对拍抓到孤儿门**两处「实现 < 它自己的文档」**（★ 这是它不能当
  //   唯一实现的硬证据，不只是「更准」）：
  //   ① `checkEyebrow` 的文档写「紧跟在 <h1>/<h2>/<h3> 之前的，就是标题上方
  //      的小标签」，**代码却只有一条 class 正则** ⇒ ★ 从没查过相邻关系
  //      ⇒ ★ 本实现的 V10 实现了它声明的意图（更准，也更窄）。
  //   ② `isIconSlot` 的文档写「字形是唯一内容」，**代码用字面量字符串相等**
  //      ⇒ ★ 插值形态永远不成立 ⇒ `<button>{{ ok ? '✓' : '✗' }}</button>`
  //      被它判绿 ⇒ ★ 而那渲染出来就是一枚对勾 ⇒ **就是图标位**。
  // ─────────────────────────────────────────────────────────────────────────
  check('S-CF1', "孤儿门样本：class='ico' 的 emoji ⇒ V3（★ 孤儿门有、本实现原为漏判）", 1, rules(`
<span class="ico">📖</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF2', '孤儿门样本：拖拽手柄字形 ☰ ⇒ V3', 1, rules(`
<div class="rule-drag-handle">☰</div>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF3', "孤儿门样本：按钮位插值里的 ✓/✗ ⇒ V3（★ 裁决：孤儿门判绿是「字面量相等」的偶然）", 1, rules(`
<button>{{ ok ? '✓' : '✗' }}</button>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF4', '孤儿门样本：非图标位的插值 emoji ⇒ 不判 V3', 0, rules(`
<span>{{ ok ? '✅' : '❌' }}</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF5', '孤儿门样本：属性值里的 emoji ⇒ 不判 V3（脚本区与属性构成数据协议）', 0, rules(`
<div :class="{ error: msg.startsWith('❌') }">x</div>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF6', '孤儿门样本：class 含 icon 的 dingbat ⇒ V3', 1, rules(`
<span class="alert-icon">⚠</span>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF7', '孤儿门样本：关闭按钮的 ✕ ⇒ V3', 1, rules(`
<button class="btn btn-ghost" @click="close">✕</button>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF8', '孤儿门样本：dingbat 是句子的一部分 ⇒ 不判 V3', 0, rules(`
<p class="error">⚠️ 数据聚合视图未就绪（…），本列表不可作为结论。</p>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF9', '孤儿门样本：dingbat 在带文字的按钮上做前后缀 ⇒ 不判 V3', 0, rules(`
<button class="btn btn-ghost">✕ 关闭</button>`).filter((r) => r === 'V3-glyph-as-icon').length)
  check('S-CF10', '孤儿门样本：没有紧邻标题的 eyebrow ⇒ 不判 V10（★ V10 比孤儿门窄：它只查 class 名）', 0, rules(`
<p class="eyebrow">CAPABILITIES</p>`).filter((r) => r === 'V10-kicker-above-heading').length)
  check('S-CF11', '孤儿门样本：章节序号 padStart(2,\'0\') ⇒ V11', 1, rules(`
<template><span>{{ String(index + 1).padStart(2, '0') }}</span></template>`).filter((r) => r === 'V11-section-number').length)
  check('S-CF12', "孤儿门样本：时间补零 ⇒ 不判 V11", 0, rules(`
<template><span>{{ String(d.getHours()).padStart(2, '0') }}</span></template>`).filter((r) => r === 'V11-section-number').length)
  // ★ 计数口径钉死：★ 一个元素 = 一条（★ 不是「一个字形一条」）。
  //   移动端真仓形态：`{{ open ? '▴' : '▾' }}` 两个字形在**同一个元素**里
  //   ⇒ 第一版报 2 条 ⇒ 「违规 2 处」会让人以为要改两个地方。
  check('S-CF13', '一个元素里两个字形 ⇒ 只报一条（★ 计数口径 = 元素数）', 1, scan(`
<template><span class="login__site-caret">{{ open ? '▴' : '▾' }}</span></template>`)
    .filter((f) => f.rule === 'V3-glyph-as-icon').length)
  // ★ 聚合后的 text 字段要把两个字形都列出来（★ 报告要能指出「哪两个字形」）。
  check('S-CF14', '聚合后 text 列出全部字形', 1, scan(`
<template><span class="login__site-caret">{{ open ? '▴' : '▾' }}</span></template>`)
    .filter((f) => f.rule === 'V3-glyph-as-icon' && f.text === '▴ ▾').length)

  // ── 第 27 增量：V2 写法覆盖（★ 孤儿门那份能认 5 种，第 1 版只认 1 种）──
  check('S-CF15', '注释里的 border-left-width ⇒ 不判 V2（★ 注释不是声明）', 0, rules(`
<template></template>
<style scoped>
/* 行容器 —— 原来左沿是 3px 粗边（border-left-width: 3px），那是 craft-floor 硬禁的 */
.cp__row { border: 1px solid transparent; border-radius: 7px; }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  check('S-CF16', "长写 border-left-width + 颜色在 border: ⇒ V2（★ 同形状的第 2 种写法）", 1, rules(`
<template></template>
<style scoped>
.card { border: 1px solid var(--kx-danger); border-left-width: 3px; border-radius: 8px; }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  check('S-CF17', '颜色写在 modifier 规则里 ⇒ V2（★ 同形状的第 3 种写法）', 1, rules(`
<template></template>
<style scoped>
.row { border: 1px solid transparent; border-left-width: 3px; border-radius: 7px; }
.row.is-ok { border-color: var(--kx-success); }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  check('S-CF18', '伪元素画的窄色条 ⇒ V2（★ 同形状的第 4 种写法）', 1, rules(`
<template></template>
<style scoped>
.card::before { content: ""; position: absolute; top: 0; left: 0; width: 3px; height: 100%; background: var(--kx-danger); }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  check('S-CF19', '透明占位的粗左边 ⇒ 不判 V2（★ 那是布局占位，不是状态色条）', 0, rules(`
<template></template>
<style scoped>
.x { border: 1px solid transparent; border-left-width: 3px; border-radius: 8px; }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  check('S-CF20', '顶边粗边 ⇒ 不判 V2（★ craft-floor 原话只说 border-left / border-right）', 0, rules(`
<template></template>
<style scoped>
.node { border: 1px solid var(--border); border-top: 3px solid var(--kx-danger); border-radius: 7px; }
</style>`).filter((r) => r === 'V2-colored-side-stripe').length)
  // ★ 第 27 增量回归：`scanElements` 必须跳过 `<style>` / `<script>` 整块
  //   （★ 它们的正文不是模板文本 ⇒ 里面的字形（★ 例如 CSS 注释里写的 `▴`）
  //   不该被 V3 / V10 当成模板里的字形）。
  check('S-CF21', '<style> 块里的字形 ⇒ 不判 V3（★ 扫描作用域是模板文本）', 0, rules(`
<template><span class="ok-text">ok</span></template>
<style scoped>
/* 这里原来写的是 ▴ */
.probe { color: red; }
</style>`).filter((r) => r === 'V3-glyph-as-icon').length)

  // ── 第 36 增量：V11b —— **手写字面量**的装饰序号（★ V11 只认 padStart 生成的那种）──
  check('S-CF22', "数据数组里手写的 icon: '01' / '02' ⇒ V11b 两条（★ 按出现次数计，与 V3b 同款口径）", 2, rules(`
<script setup>
const features = computed(() => [
  { icon: '01', title: 'a' },
  { icon: '02', title: 'b' },
])
</script>`).filter((r) => r === 'V11b-section-number-literal').length)
  check('S-CF23', "非序号语义属性名（limit: '01'）⇒ 不判 V11b（★ 判据宁可窄不可宽）", 0, rules(`
<script setup>
const cfg = { limit: '01' }
</script>`).filter((r) => r === 'V11b-section-number-literal').length)
  check('S-CF24', "非零填充的 '60' ⇒ 不判 V11b（★ 只认 01..09 这个 craft-floor 形状）", 0, rules(`
<script setup>
const cfg = { icon: '60' }
</script>`).filter((r) => r === 'V11b-section-number-literal').length)
  check('S-CF25', "注释里的 icon: '01' ⇒ 不判 V11b（★ 注释不是声明）", 0, rules(`
<script setup>
// icon: '01' —— 这是注释
const cfg = { name: 'x' }
</script>`).filter((r) => r === 'V11b-section-number-literal').length)
  check('S-CF26', "padStart 生成的那种仍归 V11（★ V11b 不重复报）", 0, rules(`
<template><span>{{ String(index + 1).padStart(2, '0') }}</span></template>`).filter((r) => r === 'V11b-section-number-literal').length)
  check('S-CF27', 'V11b 的 selector 报的是属性名（★ 报告要能指出「哪个属性」）', 1, scan(`
<script setup>
const features = [{ icon: '01' }]
</script>`).filter((f) => f.rule === 'V11b-section-number-literal' && f.selector === 'icon').length)

  const failed: string[] = []
  let passed = 0
  for (const c of cases) {
    if (c.expect === c.actual) passed++
    else failed.push(`${c.id} ${c.desc}\n   期望 ${c.expect}，实得 ${c.actual}`)
  }
  return { passed, total: cases.length, failed }
}
