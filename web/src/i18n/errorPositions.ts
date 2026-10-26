// errorPositions.ts — 「错误位置」识别的**唯一一份实现**（2026-10-27，第 15 增量）
//
// ★ 为什么单独一个文件：
//   R1（错误三问，源码侧）判的是**字面量**；第 15 增量的 L1 判的是**语言包的值**。
//   两者的位置信号必须**完全一致**，否则会出现「源码侧说这是错误位、语言包侧说不是」
//   ⇒ 口径分成两份实现 = 迟早漂。
//   ⇒ 本文件把「什么位置算错误位」抽出来，R1 与 L1 都从这里取。
//
// ★ 位置信号（来自 impeccable skill/reference/clarify.md 的「An actionable error answers …」
//   + 仓内 R1 已有的四类位置）：
//   (a) 兜底形状：'X.message || …' / 'instanceof Error ? … : …'
//   (b) 错误槽位：赋值目标是错误文案变量（error/err/msg/message/fail 族）
//   (c) 报错调用：alert / notify* / ElMessage.error / *Err( / emit('error')
//   (d) throw / new Error(
//
// ★ 为什么**不**用「键路径」当位置信号：
//   第 12 增量试过 ⇒ 键路径分不出「错误陈述」与「计数摘要」
//   （`providerDetail.models.refreshSummaryFailed` = "失败 {n}" 是计数，不是错误陈述）
//   ⇒ 源码上下文分得出 ⇒ 本轮把位置信号搬到源码侧。

import { readFileSync } from 'node:fs'
import { relative } from 'node:path'
import { maskSource, SRC_DIR } from './hardcodedCjk.ts'

// ───────────────────────── 位置识别器 ─────────────────────────

/** 错误**兜底形状** —— 字面量是「错误对象没有 message 时用这句」的替补。 */
export const FALLBACK_SHAPE = /\.(?:message|detail|reason|msg)\s*\|\||instanceof\s+Error\s*\?/
/** **报错调用** —— 实参直接进了一个报错通道。 */
export const ERROR_CALL =
  /(?:^|[^A-Za-z0-9])(?:alert|notify[A-Za-z0-9_]*|show[A-Za-z0-9_]*|ElMessage\.error|ElMessage\s*\.\s*error|[A-Za-z0-9_]*Err[A-Za-z0-9_]*)\s*\(/
/** emit 的第一个实参是 'error'。 */
export const ERROR_EMIT = /emit\(\s*['"]error['"]/
/** throw / new Error。 */
export const THROW_RE = /\bthrow\b|\bnew\s+[A-Za-z0-9_.]*Error\s*\(/
/** **错误槽位** —— 赋值目标本身就是一个「错误文案变量」。 */
export const ERROR_TARGET_ASSIGN =
  /([A-Za-z_$][\w$]*(?:\s*\.\s*[A-Za-z_$][\w$]*)*)\s*=(?!=)/g
/** 目标名里出现这些词 ⇒ 它是筛选项/状态码/字段名，不是错误文案变量。 */
export const NON_ERROR_TARGET = /(filter|label|option|type|status|mode|code|reason|tag|word|field|key)/i
/** 目标名**某一段**含这些词 ⇒ 是错误文案变量（逐段判，见 R1 的注释）。 */
export const ERROR_TARGET_WORD = /^(?:err|error|msg|message|fail)|(?:err|error|msg|message|fail)$/i
/**
 * **错误出口调用** —— `surfaceError(...)` 按定义就是错误位置。
 *
 * ★ 为什么要显式写一条（第 16 增量）：
 *   迁移前，错误位上的文案靠 `instanceof Error ?` 这个**兜底形状**被认出来。
 *   迁移后（`error.value = surfaceError(e, { key: 'x.y' })`）那个形状没了
 *   ⇒ 若只靠 `ERROR_TARGET_ASSIGN`，`const fallback = surfaceError(...)`
 *      这种中间变量就**不再是错误位** ⇒ 它的键会从 L1 的候选集里消失
 *      ⇒ **L1 门会静默变弱**（这是「门绿 ≠ 门有牙」的另一个形态：门没坏，覆盖面没了）。
 *   ⇒ 显式声明「出口调用即错误位」。
 */
export const ERROR_OUTLET_CALL = /\bsurfaceError\s*\(/

/**
 * 这一段上下文是不是「错误位置」。
 *
 * @param stmt  该字面量所在的那一行（语句）
 * @param before 字面量之前的上下文（跨行，回看 260 字符）
 */
export function isErrorPosition(stmt: string, before = ''): boolean {
  if (FALLBACK_SHAPE.test(stmt) || FALLBACK_SHAPE.test(before)) return true
  if (THROW_RE.test(stmt) || ERROR_EMIT.test(stmt) || ERROR_CALL.test(stmt)) return true
  // 错误出口调用：按定义是错误位（第 16 增量；见 ERROR_OUTLET_CALL 的注释）。
  if (ERROR_OUTLET_CALL.test(stmt)) return true
  // 取该行**最后一个**赋值（守卫 `if (…)` 不在行首时也能命中）。
  ERROR_TARGET_ASSIGN.lastIndex = 0
  let lhs: string | null = null
  let m: RegExpExecArray | null
  while ((m = ERROR_TARGET_ASSIGN.exec(stmt)) !== null) lhs = m[1]
  if (!lhs) return false
  lhs = lhs.replace(/\s+/g, '')
  if (NON_ERROR_TARGET.test(lhs)) return false
  // 逐段看：error.value / ctx.loadError.value / systemFeaturedError.value 都命中，
  // 而 errorKindFilter 被上面的 filter 排除。
  return lhs.split('.').some((seg) => ERROR_TARGET_WORD.test(seg))
}

// ───────────────────────── 上下文切片 ─────────────────────────

/** 字面量所在的那一行。 */
export function statementOf(masked: string, index: number): string {
  const start = masked.lastIndexOf('\n', index) + 1
  let end = masked.indexOf('\n', index)
  if (end < 0) end = masked.length
  return masked.slice(start, end)
}

/** 字面量**之前**的上下文（跨行，回看 260 字符）。 */
export function beforeOf(masked: string, index: number): string {
  return masked.slice(Math.max(0, index - 260), index)
}

// ───────────────────────── 取键 ─────────────────────────

/** 取键调用（与仓内既有的引用集口径一致：`t/tc/$t/te/tm/TE/i18n.t` + 字面量实参）。 */
const CALL_RE = /\b(?:t|tc|\$t|te|tm|TE|i18n\.t)\(\s*'([A-Za-z0-9_.]+)'/g

/**
 * **错误出口形态**：`surfaceError(<err>, { key: '<key>' })`（第 16 增量）。
 *
 * ★ 为什么必须单独认这一种形态：
 *   迁移后错误位上的文案**不再**由调用点直接写 `t('key')` ⇒ CALL_RE 看不见它
 *   ⇒ 若不补这一条，第 15 增量的 L1 门在迁移后会**候选集变空 ⇒ 什么也不判 ⇒ 假绿**。
 *   ⇒ 「门没坏，覆盖面没了」是比「门报错」更危险的一种坏。
 */
const OUTLET_KEY_RE = /\bsurfaceError\s*\(.{0,400}?\{\s*key\s*:\s*'([A-Za-z0-9_.]+)'/g

/**
 * 源码侧「错误位置」上、值由语言包承载的键。
 *
 * ★ 这就是 L1 **跨 `t()` 边界**的输入：
 *   R1 判 `error.value = '加载失败'`（字面量），判不到 `error.value = t('keys.loadFailed')`。
 *   本函数把后者取出来 ⇒ L1 可以去语言包里看那条值到底说了什么。
 *
 * @returns 键 → 调用点（`文件:行`）
 */
export function errorSlotLocaleKeys(files: string[]): Map<string, string[]> {
  const out = new Map<string, string[]>()
  const push = (key: string, site: string): void => {
    if (!out.has(key)) out.set(key, [])
    out.get(key)!.push(site)
  }
  for (const file of files) {
    const src = readFileSync(file, 'utf8')
    const masked = maskSource(file, src)
    const rel = relative(SRC_DIR, file)
    CALL_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = CALL_RE.exec(masked)) !== null) {
      const index = m.index
      const stmt = statementOf(masked, index)
      const before = beforeOf(masked, index)
      if (!isErrorPosition(stmt, before)) continue
      const line = masked.slice(0, index).split('\n').length
      push(m[1], `${rel}:${line}`)
    }
    // 错误出口形态（第 16 增量）：`surfaceError(e, { key: 'x.y' })`。
    for (const [key, site] of outletKeyCalls(masked, rel)) push(key, site)
  }
  return out
}

/**
 * **错误出口形态**上的语言包键 → 调用点。
 *
 * ★ 第 16 增量新增，供「错误出口门」复用（同一口径只能有一份实现）：
 *   `errorSlotLocaleKeys` 把 `t('key')` 与 `surfaceError(..., { key })` **合并**成
 *   「错误位候选集」（L1 门用）；
 *   本函数只取**出口形态**那部分（出口门用：它要逐个核对键在 8 语种是否存在）。
 */
export function outletKeyCalls(
  masked: string,
  rel: string
): Array<[string, string]> {
  const out: Array<[string, string]> = []
  OUTLET_KEY_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = OUTLET_KEY_RE.exec(masked)) !== null) {
    const index = m.index
    const stmt = statementOf(masked, index)
    const before = beforeOf(masked, index)
    if (!isErrorPosition(stmt, before)) continue
    const line = masked.slice(0, index).split('\n').length
    out.push([m[1], `${rel}:${line}`])
  }
  return out
}

/**
 * 源码里全部「错误出口形态」的键 → 调用点（**不**做位置过滤）。
 *
 * ★ 与 `outletKeyCalls` 的区别：出口门要核对的是**每一个**出口键在 8 语种是否存在，
 *   不是「它是否恰好在错误位上」⇒ 用这一份。
 */
export function allOutletKeys(files: string[]): Map<string, string[]> {
  const out = new Map<string, string[]>()
  for (const file of files) {
    const src = readFileSync(file, 'utf8')
    const masked = maskSource(file, src)
    const rel = relative(SRC_DIR, file)
    OUTLET_KEY_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = OUTLET_KEY_RE.exec(masked)) !== null) {
      const index = m.index
      const line = masked.slice(0, index).split('\n').length
      if (!out.has(m[1])) out.set(m[1], [])
      out.get(m[1])!.push(`${rel}:${line}`)
    }
  }
  return out
}

/**
 * 运行时在 `t('key')` **后面拼了别的东西**的调用点。
 *
 * ★ 用途：豁免项「运行时拼原因」的自证 ——
 *   `notifyError(res.error || \`${t('freePool.error.saveFailed')} (${res.status})\`)`
 *   这类值本身只是前缀（没点名主体、也没恢复动作），
 *   但**具体原因在运行时被拼上** ⇒ 它不是「裸失败短语」的病。
 *
 * ★ 判据 = 调用点后面 40 字符内出现**拼接形状**：
 *   `: ${…}` / `': '` / ` + ` / ` (${…}` / `' + '`
 */
export function composedLocaleKeyCalls(files: string[]): Map<string, string[]> {
  const out = new Map<string, string[]>()
  for (const file of files) {
    const src = readFileSync(file, 'utf8')
    const masked = maskSource(file, src)
    const rel = relative(SRC_DIR, file)
    CALL_RE.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = CALL_RE.exec(masked)) !== null) {
      const after = masked.slice(m.index + m[0].length, m.index + m[0].length + 40)
      if (!/[:：]\s*\$\{|['"]\s*[:：]\s*['"]|\s\+\s|\(\$\{|['"]\s*\+\s*['"]/.test(after)) continue
      const key = m[1]
      if (!out.has(key)) out.set(key, [])
      const line = masked.slice(0, m.index).split('\n').length
      out.get(key)!.push(`${rel}:${line}`)
    }
  }
  return out
}
