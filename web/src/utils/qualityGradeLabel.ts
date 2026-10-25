/**
 * qualityGradeLabel.ts — 质量等级（S/A/B/C/D）的展示文案。
 *
 * ── 为什么不在 types/quality-api.ts 里放一张中文表 ──────────────
 * 原来那里有一张 `QUALITY_GRADE_LABELS`（S:'卓越' / A:'优秀' / …），
 * 三个问题：
 *   ① **空洞词**：「卓越」在 DESIGN.md §7 的禁用词表里（营销腔形容词）。
 *   ② **7 个语种显示中文**：它在源码里是硬编码中文 ⇒ 英文界面里
 *      徽章上写着「S 卓越」。
 *   ③ **两个真值源**：字面量表 + 语言包，改一处忘一处就会不一致。
 *
 * 现在：等级文案是**语言包里的词**（common.terms.qualityGrade.*），
 * 这里是**取词的地方**。字母（S/A/B/C/D）仍是稳定 code —— 逻辑比较用字母，
 * 展示用词，两者不混。
 *
 * ★ 键名写成**字面量**（而不是拼出来的）⇒ 静态扫描器能看见它们 ⇒
 *   「键在不在 8 个语种里都存在」这件事仍然有门在管。
 */

import { i18n } from '../i18n'

/**
 * 等级 → 展示文案。
 *
 * @param grade   后端给的质量等级（S/A/B/C/D）。
 * @param missing 等级**缺失**时的占位（各调用点约定不同：表格用空串、详情页用「—」）。
 * @returns 已知等级 ⇒ 语言包里的词；未知等级 ⇒ 回显原字母（字母是稳定 code，
 *          永远能显示）；等级缺失 ⇒ missing。
 */
export function qualityGradeLabel(grade: string | undefined | null, missing = ''): string {
  switch (grade) {
    case 'S':
      return i18n.global.t('common.terms.qualityGrade.s')
    case 'A':
      return i18n.global.t('common.terms.qualityGrade.a')
    case 'B':
      return i18n.global.t('common.terms.qualityGrade.b')
    case 'C':
      return i18n.global.t('common.terms.qualityGrade.c')
    case 'D':
      return i18n.global.t('common.terms.qualityGrade.d')
    default:
      return grade || missing
  }
}
