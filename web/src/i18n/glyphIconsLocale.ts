// glyphIconsLocale.ts —— 「语言包侧 emoji 冒充图标」判据的**门侧实现**。
//
// ★★ 这道门补的是第 27 增量留下的**覆盖面盲区**：
//   源码侧的 `detectGlyphIcons`（`src/design/aiSmells.ts` 的 V3 判据）只扫
//   **界面层源码**里的 `<button>` / `<a>` / class 含图标语义词的元素
//   ⇒ 而 **8 个语言包的值**里的 emoji（`🧹 VACUUM` / `✅ 可达` / `⚠️ 警告` …）
//   **从来没有任何一道门看过** ⇒ 实测 8 语种合计 **518 处**（65 个键）。
//
// ★ 判据依据：DESIGN.md §6「图标」——「不许用 emoji / Unicode 字形冒充图标系统」
//   与 impeccable `craft-floor` 的 ban-glyph-icons 同源。
//
// ★ 纯函数核心在 `glyphIconCore.ts` —— ★★ **清理脚本与门共用同一份实现**
//   （第一版清理脚本自己写了一份判定 ⇒ 它比门宽 ⇒ 差点把协议互转的 `↔` 删掉）。
//
// ★ 为什么可以**零豁免**（不像 copy-clarity-locale 那样按位置豁免）：
//   本仓这 65 个键里的 emoji **全是冗余**——文字本身已经表达了语义
//   （`✅ 可达` 的「可达」、`📤 请求` 的「请求」都自带含义）⇒
//   去掉 emoji **不丢信息**（状态仍由「软填充 + 文字 + 文字颜色」表达，
//   见 DESIGN.md §3「状态不能只靠颜色区分：软填充 + 文字颜色 + 文字本身，三者同进」）。

import { allLocaleValues, ALL_LOCALES } from './localeValueQuality.ts'
import { glyphIconCandidates, glyphIconSlotViolation, stripGlyphIcons } from './glyphIconCore.ts'

export { glyphIconCandidates, glyphIconSlotViolation, stripGlyphIcons } from './glyphIconCore.ts'

export type LocaleGlyphRuleId = 'G1-emoji-as-icon'

export interface LocaleGlyphFinding {
  rule: LocaleGlyphRuleId
  locale: string
  key: string
  text: string
  glyphs: string[]
  reason: string
}

/** 扫描 8 个语种的全部语言包值。 */
export function detectLocaleGlyphIcons(
  localesDir?: string,
): LocaleGlyphFinding[] {
  const out: LocaleGlyphFinding[] = []
  const valuesByLocale = allLocaleValues(localesDir)
  for (const locale of ALL_LOCALES) {
    const values = valuesByLocale.get(locale)
    if (!values) continue
    for (const [key, text] of Object.entries(values)) {
      if (typeof text !== 'string' || !text) continue
      // ★ 口径：**图标位**（去掉字形后不剩文字）—— 与源码侧 V3 对齐
      //   （★ 「值里含 emoji」是更宽的口径 ⇒ 会把 518 处合法惯例判红 ⇒ 已收窄）
      const glyphs = glyphIconSlotViolation(text)
      if (!glyphs.length) continue
      out.push({
        rule: 'G1-emoji-as-icon',
        locale,
        key,
        text,
        glyphs,
        reason:
          '文案里用 emoji / Unicode 字形冒充图标（DESIGN.md §6：图标是画出来的，不许用 emoji / 字形冒充图标系统）',
      })
    }
  }
  return out
}

// ───────────────────────── 自证 ─────────────────────────

export interface LocaleGlyphSelftestCase {
  name: string
  text: string
  /** 期望命中的字形（空数组 = 期望不判红） */
  expect: string[]
}

/**
 * ★ 必须**双向**：会判红 + 不误判各半 —— 否则恒绿门与恒红门同样没用。
 *
 * ★★ 口径提醒：判据是「**图标位**」（去掉字形后不剩文字），与源码侧 V3 对齐。
 *   ★ 「字形 + 文本」（`✅ 可达`）在仓内是**文字标记**、是既有惯例 ⇒ 判绿。
 *   ★ 第一版把「含字形」当判据 ⇒ 会把真仓 518 处合法惯例全判红 ⇒ 本轮已收窄。
 */
export const LOCALE_GLYPH_SELFTEST: LocaleGlyphSelftestCase[] = [
  // ── 会判红（图标位：值整句就是一个字形）──
  { name: '纯字形值（图标位）', text: '🧹', expect: ['🧹'] },
  { name: '纯字形 + 尾随空格', text: '📤 ', expect: ['📤'] },
  { name: '纯字形（带变体选择符）', text: '⚠️', expect: ['⚠'] },
  // ── 不误判（★ 真仓实证的假阳性形态）──
  { name: '文字标记（仓内惯例判绿）', text: '✅ 可达', expect: [] },
  { name: '文字标记（带变体选择符）', text: '⚠️ 警告', expect: [] },
  { name: '字形 + 文字 + 占位符', text: '⏭️ 跳过 {n}', expect: [] },
  { name: '纯文字', text: '请求', expect: [] },
  { name: '流程箭头（协议互转）', text: '保持开启以支持 Anthropic ↔ OpenAI 协议转换。关闭后仅支持原生协议。', expect: [] },
  { name: '上下箭头', text: '命中率 ↑ 3%，环比 ↓ 1%', expect: [] },
  { name: '排印符号', text: '延迟 — 正常…', expect: [] },
  { name: '占位符与数字', text: '成功 {n}（42 MB，P99 < 500 ms）', expect: [] },
  { name: '纯 ASCII', text: 'OK', expect: [] },
]

export function runLocaleGlyphSelftest(): {
  passed: number
  total: number
  failed: string[]
} {
  const failed: string[] = []
  let passed = 0
  for (const c of LOCALE_GLYPH_SELFTEST) {
    const got = glyphIconSlotViolation(c.text)
    const ok =
      got.length === c.expect.length &&
      got.every((g, i) => g === c.expect[i])
    if (ok) passed++
    else {
      failed.push(
        `${c.name}：期望 [${c.expect.join('')}] 实得 [${got.join('')}]（值「${c.text}」）`,
      )
    }
  }
  return { passed, total: LOCALE_GLYPH_SELFTEST.length, failed }
}
