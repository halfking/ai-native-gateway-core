// glyphIconCore.ts —— 「emoji 冒充图标」的**纯函数核心**（无 IO、无依赖加载）。
//
// ★★ 为什么单独拆一个文件（★ 第 29 增量踩过的坑）：
//   判据与**清理器**必须是同一份实现 —— 第一版清理脚本自己写了一份判定，
//   结果它比门**宽**：把 `↔`（流程箭头，协议互转语义）也当成 emoji ⇒
//   差点把「Anthropic ↔ OpenAI 协议转换」里的箭头删掉 ⇒ **语义被破坏**。
//   ⇒ 同一个口径只能有一份实现 ⇒ 纯函数单独成模块，门（.ts）与
//     清理脚本（.mjs，Node 带 --experimental-strip-types）**都 import 它**。
//
// ★ 判据依据：DESIGN.md §6「图标」——「不许用 emoji / Unicode 字形冒充图标系统」
//   （与 impeccable craft-floor 的 ban-glyph-icons 同源）。
//
// ★ 排除项与源码侧**共用同一份集合**（不许另抄一份）：
//   `TYPOGRAPHIC_GLYPHS`（— – · … ▍ ⋯ 排印符号）——它们是标点不是图标；
//   `FLOW_ARROWS`（→ ← ↔ ↕ ⇄ … ↑ ↓ 流程箭头）——它们是连线示意不是图标。
//   ★ 真仓实证：`providerDetail.formatHint` =「…支持 Anthropic ↔ OpenAI 协议转换…」
//     里的 `↔` **必须**不判红 ⇒ 漏了这条排除就是假红。

import { isGlyphIconChar, GLYPH_ICON_TEST_RE } from '../design/aiSmells.ts'

/**
 * emoji / 图标位字形的候选集。
 *
 * ★ 口径说明（★ 逐条都有真仓证据）：
 *  - `Extended_Pictographic` ⇒ ✅❌🔍🧹🚫⏭ 等**具象图形**；
 *    ★ 实测精确化：`↔`(U+2194) **也是** Extended_Pictographic ⇒ 必须靠
 *      `FLOW_ARROWS` 排除（否则「Anthropic ↔ OpenAI 协议转换」会假红）；
 *      而 `★`(U+2605) / `×`(U+00D7) / `▸` / `▤` **不是** ⇒
 *      仓内那 172 处「★ 作危险标记前缀」天然不命中 ✔。
 *  - `Emoji_Presentation` ⇒ ⏳🟡 等**默认以 emoji 呈现**的符号；
 *  - U+FE0F（变体选择符）与 U+20E3（组合键帽）⇒ 它们是 emoji 的**修饰符**，
 *    本身不可见 ⇒ 命中 emoji 时要**一并报出来**（清理时必须一起删，
 *    否则会留下不可见残渣）。
 *
 * ★ 字符判定复用 `aiSmells.ts` 的 `isGlyphIconChar`（★ 唯一实现 ⇒ 不两份）。
 */
export const GLYPH_ICON_RE =
  /[\p{Extended_Pictographic}\p{Emoji_Presentation}\uFE0F\u20E3]/gu

export { GLYPH_ICON_TEST_RE }

/** 纯函数：从一段文案里拆出「疑似图标位」的字形。 */
export function glyphIconCandidates(text: string): string[] {
  const out: string[] = []
  for (const ch of text) {
    // ★ 排印符号 / 流程箭头 / 变体选择符的排除**已在 `isGlyphIconChar` 内**
    if (!isGlyphIconChar(ch)) continue
    out.push(ch)
  }
  return out
}

/**
 * ★★ **仓内既有口径**（与源码侧 `aiSmells.ts` 的 `glyphCandidates` 对齐）：
 *   **「图标位」= 去掉字形后不剩任何文字**。
 *
 * ★★★ 为什么口径必须是「图标位」而不是「值里含字形」——
 *   ★ 真仓实测（第 29 增量）：HEAD 原始状态下 8 语种共 **518 处**语言包值含 emoji，
 *     而其中**整句就是字形的有 0 处** ⇒ **518 处全是「字形 + 文本」**。
 *   ★ 而仓内 `aiSmells.ts` 的 V3 判据**只认图标位** ⇒ ★★ 源码侧那 **172 处**
 *     「文本以字形开头」（`★` 作危险标记前缀等）被明确记为
 *     **既有惯例、判绿**，不是待清的存量。
 *   ⇒ ⇒ 「值里含 emoji 就判红」是**比仓内口径宽**的判据 ⇒ 会把 518 处合法惯例
 *     全部判红 ⇒ ★★ **假红比假绿更费人** ⇒ 本函数把口径收紧回「图标位」。
 *
 * ★ 收紧后的实际收益：
 *   ① 真违规（`SettingsView.vue` 那种「icon 字段 = 一个 emoji」⇒ 渲染成一整块图标位）
 *      仍被抓得住；
 *   ② 语言包侧目前 **0 处**图标位 ⇒ 门变成**防回归闸**
 *     （★ 防止有人把 `icon: '📋'` 写进语言包 —— 那正是本轮真缺陷的语言包版）。
 */
export function glyphIconSlotViolation(text: string): string[] {
  const glyphs = glyphIconCandidates(text)
  if (!glyphs.length) return []
  const rest = text
    .replace(/[\p{Extended_Pictographic}\p{Emoji_Presentation}\uFE0F\u20E3]/gu, '')
    .replace(/\s/g, '')
  // ★ 去掉字形与空白后还剩文字 ⇒ 这是「文字标记」（字形 + 文字）⇒ 仓内惯例，判绿
  return rest.length === 0 ? glyphs : []
}

/** ★ 清理用：把一段文案里的 emoji（含修饰符）删掉，并收拾多余空格。 */
export function stripGlyphIcons(text: string): string {
  // ★★★ 逐字符过滤，**不用宽口径正则** ——
  //   ★ 第一版用 `/[\p{Extended_Pictographic}…]/gu` ⇒ ★ `↔`(U+2194) **也是**
  //     Extended_Pictographic ⇒ ⇒ 协议互转的箭头会被一起删掉（★ 语义被破坏）。
  //   ★ 是 vitest 用例 `stripGlyphIcons('Anthropic ↔ OpenAI {n}')` 抓出来的。
  //   ⇒ 清理的字符集必须与**判定**同源（`isGlyphIconChar` 已排除流程箭头/排印符号）。
  const out: string[] = []
  let skipNextSpace = false
  for (const ch of text) {
    if (isGlyphIconChar(ch)) {
      // ★ 删字形时**连它后面的一个空白一起删**（否则 HTML 内嵌文案会留下
      //   `">  内容"` 两个空格、渲染出来多一个空格）
      skipNextSpace = true
      continue
    }
    if ((ch === '\uFE0F' || ch === '\u20E3') && skipNextSpace) continue
    if (skipNextSpace && /\s/.test(ch)) {
      skipNextSpace = false
      continue
    }
    skipNextSpace = false
    out.push(ch)
  }
  let s = out.join('')
  // ★ 删掉 emoji 后可能留下「两个空格」或「空格 + 标点」的残渣 ⇒ 收拾一次
  s = s.replace(/[ \t]{2,}/g, ' ')
  s = s.replace(/^[\s]+/, '')
  s = s.replace(/[\s]+$/, '')
  return s
}
