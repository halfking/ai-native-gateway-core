// callSiteParams.ts — 「调用点参数 → 占位符名」的静态解析（2026-10-21 审计轮新增）
//
// 目的：补上 i18n 既有门合起来的**又一条盲区**（R54 §11.1-6 明确登记）：
//   「**所有语种一致、但都缺**调用点要的那个占位符」⇒ 跨语种平价门
//   （localePlaceholderParity.ts）抓不到（没有「不一致」可看）。
//   ⇒ 要关掉它，必须能把「调用点 `t(key, {…})` 传了哪些参数名」解析出来，
//     再去问「这些参数名有没有被任何语种的消息显示」。
//
// ⚠️ **为什么不能拿「参数名」当「作者意图」的充分证据**：
//   vue-i18n 会忽略没被消息使用的具名参数 ⇒ 「传了但没显示」本身不等于缺陷。
//   本模块只提供**事实**（传了什么），**判定**在 callSitePlaceholderCoverage.ts：
//   判据是「参数名在候选消息里**一个都没出现**」⇒ 该参数是**死的**
//   （既没被显示，也不可能被显示）⇒ 这才是可判的缺陷形态。
//
// ⚠️ **vue-i18n v9 的参数语义（实测自 node_modules/@intlify/core-base@9.14.5
//   的 parseTranslateArgs，2026-10-21 核对）**：
//   ```js
//   if (isNumber(arg2))      options.plural = arg2
//   else if (isString(arg2)) options.default = arg2      // ← 回退在这
//   else if (isPlainObject(arg2) && !isEmptyObject(arg2)) options.named = arg2
//   else if (isArray(arg2))  options.list = arg2
//   ```
//   ⇒ **`t(key, {…})` 的整个对象会变成 `options.named`**（= 具名插值表）
//   ⇒ 对象里**每一个属性都是插值名**，`default` / `list` 也不例外。
//   ⇒ ★ 推论：`t(key, { default: X })` **不提供回退**（`options.default` 仍是
//     undefined）⇒ v9 的回退写法是 `t(key, 'X')`（字符串第二参）或
//     `t(key, {}, { default: 'X' })`（第三参）。
//     本仓 `PromptInjectionConfigPanel.vue` 曾按 v8 直觉写 `{ default }`，
//     且注释还写明「vue-i18n v9: pass the fallback via the `default` option」
//     ⇒ **注释与行为都不成立**（见 callSitePlaceholderCoverage.test.ts 定点回归）。
//   ⇒ ★ 由此**不存在**「vue-i18n 选项名要从插值名里排除」这回事：
//     本模块**不排除**任何属性名。
//
// ── 实现为什么要自己写词法层（而不是一条正则）────────────────────
//   ① 注释里的 `t('key', { op })` 会被正则误当成真调用点
//      （R54 轮实测 `localePlaceholderParity.ts` 的注释里就有一处）⇒ 必须屏蔽注释。
//   ② 字符串字面量里的 `t(...)` 同理是伪调用点 ⇒ 必须区分「代码区」与「字符串区」。
//   ③ `.vue` 里 **145 处调用点在 `<template>` 内**（实测）⇒ 不能只扫 script；
//      而模板里的自由文本（`don't`）会让朴素的引号扫描把整段代码吃掉。
//   ④ 正则字面量里可以有引号（实测 `/[\\/:*?"<>|]/g`）⇒ 必须识别正则字面量。
//   ⇒ 故本模块做**按 SFC 分块**的掩码词法扫描：
//      script 块 / .ts 文件 ⇒ JS 掩码（注释·字符串·模板·正则）
//      template 块 ⇒ HTML 掩码（文本 / `{{ }}` 插值 / 属性值表达式）
//
// 消费方：
//   - src/i18n/callSitePlaceholderCoverage.ts（判定层）
//   - src/i18n/callSitePlaceholderCoverage.test.ts（门）
//   - src/i18n/localeKeyParity.ts（键存在性判定层，2026-10-21 R56 轮新增）
//   - scanner.ts 再导出本模块（=「接进 scanner」的落点）
//
// ⚠️ **本模块同时是 i18n 调用点的词法层（2026-10-21 R56 轮扩展）**：
//   `scanCallSiteParamsInSource` 只产出「**第二参数是对象字面量**」的调用点
//   （它是为「调用点传了哪些具名参数」服务的 ⇒ 第二参不是对象字面量时
//   **没有具名参数可解析** ⇒ 直接跳过）。
//   ⇒ ★ **但「这个键存不存在」与第二参形态无关** ⇒ R56 轮新增
//   `scanCallSiteKeysInSource`：**枚举全部调用点的键**（不限第二参形态），
//     供「跨语种键存在性平价」判定层使用。
//   ⇒ 动机（R56 实测）：R55 的缺陷键族 `sessions.config.promptInjectionCategory*`
//     来自 `t(\`sessions.config.${meta.i18nKey}\`, meta.zhFallback || safeRaw)`
//     —— 第二参**不是**对象字面量 ⇒ **R55 的扫描器压根扫不到这个调用点**
//     ⇒ R55 只能用「手写定点回归」兜住那一族，而**机制本身是瞎的**。
//   ⇒ 两个扫描器**共用同一套掩码**（`maskSource`）⇒ 不引入第二套词法层
//     （两套量具互相抵消比一个明显错的量具更危险）。

import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

/** 掩码取值：0=代码区，1=字符串/模板字面量，2=注释，3=HTML 文本 */
const CODE = 0
const STRING = 1
const COMMENT = 2
const TEXT = 3

export type CallSiteKeyKind =
  /** 单引号/双引号的字面量键（`t('a.b')`） */
  | 'literal'
  /** 模板字面量键（`t(\`a.${x}\`)`）——静态前后缀已知，动态段未知 */
  | 'template'
  /** 变量键（`t(key)`）——静态不可知 */
  | 'dynamic'

export interface CallSiteParamRef {
  /** 源文件绝对路径 */
  file: string
  /** 1-based 行号 */
  line: number
  /** 1-based 列号 */
  column: number
  /** 键：literal=字面量内容；template=模板原文；dynamic=变量名片段 */
  key: string
  keyKind: CallSiteKeyKind
  /** 模板键的静态前缀（如 `sessions.config.`）；literal/dynamic 为 '' */
  keyPrefix: string
  /** 模板键的静态后缀（如 `.tokenFormat`）；literal/dynamic 为 '' */
  keySuffix: string
  /** 第二个参数对象字面量的属性名 = vue-i18n v9 的具名插值名 */
  params: string[]
  /** 第二个参数含 spread / 计算属性 ⇒ 参数名集合无法静态确定 */
  unresolvedParams: boolean
  /** 完整调用片段（用于 in-editor 跳转） */
  snippet: string
}

// ─────────────────────────── 词法层 ───────────────────────────

/**
 * 跳过引号字符串（返回结束后的下标）。
 * @param allowNewline HTML 属性值**可以跨行**（`:range="a\n ? t(...)"`），
 *   而 JS 字符串字面量不能跨行 ⇒ 两种语义要分开。
 */
function skipQuoted(src: string, start: number, allowNewline = false): number {
  const quote = src[start]
  let i = start + 1
  while (i < src.length) {
    const c = src[i]
    if (c === '\\') {
      i += 2
      continue
    }
    if (c === quote) return i + 1
    if (c === '\n' && !allowNewline) return i + 1 // 未闭合字符串：就此打住，不吞掉后面的代码
    i++
  }
  return i
}

/** 跳过模板字面量；同时收集 `${}` 插值区间 */
function skipTemplate(src: string, start: number): { end: number; interps: Array<[number, number]> } {
  const interps: Array<[number, number]> = []
  let i = start + 1
  while (i < src.length) {
    const c = src[i]
    if (c === '\\') {
      i += 2
      continue
    }
    if (c === '`') return { end: i + 1, interps }
    if (c === '$' && src[i + 1] === '{') {
      const interpStart = i + 2
      // ⚠️ depth 初值必须是 1：开头的 `{` 已被上面消费掉，
      //    若从 0 起算，第一个 '}' 会把 depth 减成 -1 ⇒ 插值区间一路吞到文件尾。
      let depth = 1
      let j = interpStart
      while (j < src.length) {
        const d = src[j]
        if (d === "'" || d === '"') {
          j = skipQuoted(src, j)
          continue
        }
        if (d === '`') {
          j = skipTemplate(src, j).end
          continue
        }
        if (d === '{') depth++
        else if (d === '}') {
          depth--
          if (depth === 0) break
        }
        j++
      }
      interps.push([interpStart, j])
      i = j + 1
      continue
    }
    i++
  }
  return { end: i, interps }
}

/** 跳过正则字面量（返回结束后的下标）；字符类 `[...]` 内的引号不算结束 */
function skipRegex(src: string, start: number): number {
  let i = start + 1
  let inClass = false
  while (i < src.length) {
    const c = src[i]
    if (c === '\\') {
      i += 2
      continue
    }
    if (c === '\n') return i // 未闭合：就此打住
    if (inClass) {
      if (c === ']') inClass = false
    } else if (c === '[') {
      inClass = true
    } else if (c === '/') {
      i++
      // 跳过 flags
      while (i < src.length && /[a-z]/.test(src[i])) i++
      return i
    }
    i++
  }
  return i
}

/** `/` 到底是正则还是除号：看前一个「有效字符」 */
const REGEX_PRECEDING = new Set(['(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '^', '~', '<', '>'])
const REGEX_KEYWORDS = new Set([
  'return', 'typeof', 'instanceof', 'in', 'of', 'new', 'delete', 'void',
  'case', 'do', 'else', 'yield', 'await', 'throw',
])

/**
 * 给一段源码打掩码（JS 模式）。
 * @param from/to 区间（绝对下标）
 */
function markJs(src: string, mask: Uint8Array, from: number, to: number): void {
  let i = from
  let lastSig = ''
  let lastWord = ''
  while (i < to) {
    const c = src[i]
    if (c === '/' && src[i + 1] === '/') {
      while (i < to && src[i] !== '\n') {
        mask[i] = COMMENT
        i++
      }
      continue
    }
    if (c === '/' && src[i + 1] === '*') {
      mask[i] = COMMENT
      mask[i + 1] = COMMENT
      i += 2
      while (i < to && !(src[i] === '*' && src[i + 1] === '/')) {
        mask[i] = COMMENT
        i++
      }
      if (i < to) {
        mask[i] = COMMENT
        if (i + 1 < to) mask[i + 1] = COMMENT
        i += 2
      }
      continue
    }
    if (c === "'" || c === '"') {
      const end = Math.min(skipQuoted(src, i), to)
      for (let k = i; k < end; k++) mask[k] = STRING
      i = end
      lastSig = c
      lastWord = ''
      continue
    }
    if (c === '`') {
      const { end, interps } = skipTemplate(src, i)
      const limit = Math.min(end, to)
      for (let k = i; k < limit; k++) mask[k] = STRING
      for (const [a, b] of interps) markJs(src, mask, a, Math.min(b, to))
      i = end
      lastSig = '`'
      lastWord = ''
      continue
    }
    if (c === '/') {
      const isRegex =
        lastSig === '' ||
        REGEX_PRECEDING.has(lastSig) ||
        (REGEX_KEYWORDS.has(lastWord) && /[A-Za-z0-9_$]/.test(lastSig))
      if (isRegex) {
        const end = Math.min(skipRegex(src, i), to)
        for (let k = i; k < end; k++) mask[k] = STRING
        i = end
        lastSig = '/'
        lastWord = ''
        continue
      }
      mask[i] = CODE
      lastSig = '/'
      i++
      continue
    }
    mask[i] = CODE
    if (/[A-Za-z0-9_$]/.test(c)) {
      lastSig = c
      lastWord += c
    } else if (!/\s/.test(c)) {
      lastSig = c
      lastWord = ''
    }
    i++
  }
}

/**
 * 给一段模板块打掩码（HTML 模式）：
 *   · 自由文本（含 `don't` 这类撇号）⇒ TEXT
 *   · `{{ … }}` 插值 ⇒ 内部按 JS 掩码
 *   · 属性值（`:title="…"`）⇒ 内部按 JS 掩码
 */
function markTemplate(src: string, mask: Uint8Array, from: number, to: number): void {
  let i = from
  while (i < to) {
    const c = src[i]
    if (c === '<') {
      // 标签：扫到 '>' 为止。属性值里的引号要正确跳过；
      // ⚠️ 标签名/属性名是 TEXT，属性值**内部是表达式**（JS 掩码）⇒
      //    不能在最后把整段标签刷成 TEXT（会把属性值里的代码也刷掉）。
      let j = i + 1
      let segStart = i
      while (j < to) {
        const d = src[j]
        if (d === '"' || d === "'") {
          for (let k = segStart; k < j; k++) mask[k] = TEXT
          const attrEnd = skipQuoted(src, j, true) // HTML 属性值可跨行
          const valStart = j + 1
          const valEnd = attrEnd - 1
          markJs(src, mask, valStart, Math.min(valEnd, to))
          j = Math.min(attrEnd, to)
          segStart = j
          continue
        }
        if (d === '>') {
          j++
          break
        }
        j++
      }
      for (let k = segStart; k < Math.min(j, to); k++) mask[k] = TEXT
      i = j
      continue
    }
    if (c === '{' && src[i + 1] === '{') {
      const end = findInterpolationEnd(src, i + 2, to)
      markJs(src, mask, i + 2, end)
      i = end
      continue
    }
    mask[i] = TEXT
    i++
  }
}

/** 找 `{{ … }}` 的结束位置（尊重字符串与嵌套花括号） */
function findInterpolationEnd(src: string, start: number, to: number): number {
  let depth = 0
  let i = start
  while (i < to) {
    const c = src[i]
    if (c === "'" || c === '"') {
      i = skipQuoted(src, i)
      continue
    }
    if (c === '`') {
      i = skipTemplate(src, i).end
      continue
    }
    if (c === '{') depth++
    else if (c === '}') {
      if (depth === 0) {
        if (src[i + 1] === '}') return i + 2
        return i
      }
      depth--
    }
    i++
  }
  return to
}

/** 切分 .vue 的顶层块：template / script（style 与其它块忽略） */
export function splitSfcBlocks(src: string): Array<{ kind: 'template' | 'script'; start: number; end: number }> {
  const blocks: Array<{ kind: 'template' | 'script'; start: number; end: number }> = []
  let i = 0
  while (i < src.length) {
    const lt = src.indexOf('<', i)
    if (lt < 0) break
    const isTemplate = src.startsWith('<template', lt)
    const isScript = src.startsWith('<script', lt)
    if (!isTemplate && !isScript) {
      i = lt + 1
      continue
    }
    // 找块的结束标签（template 可嵌套）
    const tag = isTemplate ? 'template' : 'script'
    const closeTag = `</${tag}`
    let depth = 0
    let j = lt
    let end = -1
    while (j < src.length) {
      const openAt = src.indexOf(`<${tag}`, j)
      const closeAt = src.indexOf(closeTag, j)
      if (closeAt < 0) break
      if (openAt >= 0 && openAt < closeAt) {
        depth++
        j = openAt + tag.length + 1
        continue
      }
      depth--
      if (depth === 0) {
        end = src.indexOf('>', closeAt) + 1
        break
      }
      j = closeAt + closeTag.length
    }
    if (end < 0) break
    blocks.push({ kind: isTemplate ? 'template' : 'script', start: lt, end })
    i = end
  }
  return blocks
}

/** 给整个文件打掩码（按 SFC 分块选择 JS/HTML 模式） */
export function maskSource(src: string, isVue: boolean): Uint8Array {
  const mask = new Uint8Array(src.length).fill(TEXT)
  if (!isVue) {
    markJs(src, mask, 0, src.length)
    return mask
  }
  for (const block of splitSfcBlocks(src)) {
    if (block.kind === 'script') markJs(src, mask, block.start, block.end)
    else markTemplate(src, mask, block.start, block.end)
  }
  return mask
}

// ─────────────────────────── 调用点扫描 ───────────────────────────

/**
 * 匹配 `t(` / `tc(` / `$t(` / `i18n.t(` / `i18n.global.t(`。
 * ⚠️ 负向前瞻排除 `foo.t(`（对象方法调用），避免把 `api.t('x')` 当成 i18n 调用。
 */
const CALL_RE = /(?<![\w$.])(?:i18n\.(?:global\.)?)?(\$?t|tc)\s*\(/g

/** 读取一段引号字符串的内容（跳过转义） */
function readQuotedValue(src: string, start: number): { value: string; end: number } {
  const quote = src[start]
  let i = start + 1
  let out = ''
  while (i < src.length) {
    const c = src[i]
    if (c === '\\') {
      out += src[i + 1] ?? ''
      i += 2
      continue
    }
    if (c === quote) return { value: out, end: i + 1 }
    if (c === '\n') return { value: out, end: i }
    out += c
    i++
  }
  return { value: out, end: i }
}

/** 模板键的静态前后缀：把 `${…}` 换成占位标记后取首尾静态片段 */
function templateStaticParts(raw: string): { prefix: string; suffix: string } {
  const parts = raw.split(/\$\{[^}]*\}/)
  const prefix = parts[0] ?? ''
  const suffix = parts.length > 1 ? (parts[parts.length - 1] ?? '') : ''
  return { prefix, suffix }
}

/**
 * 读取一个平衡的花括号块（对象字面量），返回内部文本与结束位置。
 * 用掩码跳过字符串与注释 ⇒ 不会把字符串里的 '}' 当成块结束。
 */
function readBalancedObject(src: string, mask: Uint8Array, start: number): { body: string; end: number } | null {
  let depth = 0
  let i = start
  while (i < src.length) {
    const c = src[i]
    if (mask[i] === STRING) {
      if (c === "'" || c === '"') i = skipQuoted(src, i)
      else if (c === '`') i = skipTemplate(src, i).end
      else i++
      continue
    }
    if (c === '{') depth++
    else if (c === '}') {
      depth--
      if (depth === 0) return { body: src.slice(start + 1, i), end: i + 1 }
    }
    i++
  }
  return null
}

/** 按顶层逗号切分对象字面量的属性（跳过字符串/注释/嵌套括号） */
function splitTopLevelProps(src: string, mask: Uint8Array, bodyStart: number, bodyEnd: number): string[] {
  const parts: string[] = []
  let depth = 0
  let cur = ''
  let i = bodyStart
  while (i < bodyEnd) {
    const c = src[i]
    if (mask[i] === STRING) {
      let j = i
      if (c === "'" || c === '"') j = skipQuoted(src, i)
      else if (c === '`') j = skipTemplate(src, i).end
      else j = i + 1
      cur += src.slice(i, j)
      i = j
      continue
    }
    if (c === '(' || c === '[' || c === '{') depth++
    else if (c === ')' || c === ']' || c === '}') depth--
    else if (c === ',' && depth === 0) {
      parts.push(cur)
      cur = ''
      i++
      continue
    }
    cur += c
    i++
  }
  if (cur.trim()) parts.push(cur)
  return parts
}

/** 从对象字面量的一行属性里取出属性名；取不到 ⇒ null（spread / 计算属性） */
function propNameOf(prop: string): string | null {
  const p = prop.trim()
  if (!p) return null
  if (p.startsWith('...')) return null // spread：键集合未知
  if (p.startsWith('[')) return null // 计算属性：键未知
  const m = p.match(/^(?:(['"])([^'"]+)\1|([A-Za-z_$][\w$]*))\s*(?::|$)/)
  if (!m) return null
  return m[2] ?? m[3] ?? null
}

// ─────────────────────────── 对外 API ───────────────────────────

/**
 * 枚举一份源码里**全部** i18n 调用点的**键**（不限第二参数形态）。
 *
 * ⚠️ **为什么需要它**（2026-10-21 R56 轮实测）：
 *   `scanCallSiteParamsInSource` 只产出「第二参是对象字面量」的调用点 ⇒
 *   **模板键 + 非对象第二参**的调用点（61 处，实测）它一个都扫不到 ——
 *   包括 R55 缺陷自己的 `t(\`sessions.config.${meta.i18nKey}\`, meta.zhFallback || safeRaw)`。
 *   ⇒ 「这个键在各语种存不存在」这个问题**与第二参形态无关** ⇒ 必须有独立的键枚举。
 *
 * 与 `scanCallSiteParamsInSource` 的关系：**共用同一套掩码与调用点定位**
 * （`maskSource` + `CALL_RE` + `readQuotedValue` + `skipTemplate`）⇒
 * 两者的「代码区判定」必然一致 ⇒ 不会一个说「在代码区」另一个说「在字符串里」。
 */
export interface CallSiteKeyRef {
  /** 源文件绝对路径 */
  file: string
  /** 1-based 行号 */
  line: number
  /** 1-based 列号 */
  column: number
  /** 键：literal=字面量内容；template=模板原文；dynamic=变量名片段 */
  key: string
  keyKind: CallSiteKeyKind
  /** 模板键的静态前缀（如 `sessions.config.`）；literal/dynamic 为 '' */
  keyPrefix: string
  /** 模板键的静态后缀（如 `.tokenFormat`）；literal/dynamic 为 '' */
  keySuffix: string
  /** 第二参数形态（用于诊断「为何某个键族没被参数扫描器看到」） */
  secondArgKind: 'none' | 'object-literal' | 'string' | 'other'
  /** 完整调用片段 */
  snippet: string
}

/** 判定第二参数的形态（只用于诊断，不做参数解析）。 */
function classifySecondArg(src: string, mask: Uint8Array, i: number): {
  kind: CallSiteKeyRef['secondArgKind']
  next: number
} {
  if (i >= src.length) return { kind: 'none', next: i }
  if (src[i] === ',') {
    const j = skipTrivia(src, mask, i + 1)
    if (j >= src.length) return { kind: 'none', next: j }
    if (src[j] === '{') {
      const bal = readBalancedObject(src, mask, j)
      return { kind: 'object-literal', next: bal ? bal.end : j + 1 }
    }
    if (src[j] === "'" || src[j] === '"') {
      return { kind: 'string', next: skipQuoted(src, j) }
    }
    return { kind: 'other', next: j }
  }
  return { kind: 'none', next: i }
}

/** 扫描一份源码里**所有** i18n 调用点的键（不限第二参数形态）。 */
export function scanCallSiteKeysInSource(src: string, file: string): CallSiteKeyRef[] {
  const mask = maskSource(src, file.endsWith('.vue'))
  const out: CallSiteKeyRef[] = []
  CALL_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = CALL_RE.exec(src)) !== null) {
    if (mask[m.index] !== CODE) continue
    const openParen = m.index + m[0].length - 1
    let i = skipTrivia(src, mask, openParen + 1)
    if (i >= src.length) continue

    let key = ''
    let keyKind: CallSiteKeyKind = 'dynamic'
    let keyPrefix = ''
    let keySuffix = ''
    if (src[i] === "'" || src[i] === '"') {
      const r = readQuotedValue(src, i)
      key = r.value
      keyKind = 'literal'
      i = r.end
    } else if (src[i] === '`') {
      const { end } = skipTemplate(src, i)
      const raw = src.slice(i + 1, end - 1)
      key = raw
      keyKind = 'template'
      const parts = templateStaticParts(raw)
      keyPrefix = parts.prefix
      keySuffix = parts.suffix
      i = end
    } else {
      const vm2 = src.slice(i).match(/^[A-Za-z_$][\w$.]*/)
      key = vm2 ? vm2[0] : ''
      keyKind = 'dynamic'
      i += vm2 ? vm2[0].length : 0
    }

    const second = classifySecondArg(src, mask, i)
    const { line, column } = offsetToLineCol(src, m.index)
    out.push({
      file,
      line,
      column,
      key,
      keyKind,
      keyPrefix,
      keySuffix,
      secondArgKind: second.kind,
      snippet: src.slice(m.index, Math.min(second.next, src.length)).slice(0, 200),
    })
    // 推进到本次调用的第二参数末尾 ⇒ 避免把参数内部的 t(...) 当成新的调用点
    CALL_RE.lastIndex = Math.max(second.next, openParen + 1)
  }
  return out
}

/** 扫描单个文件的全部调用点键 */
export function scanCallSiteKeys(absFile: string): CallSiteKeyRef[] {
  return scanCallSiteKeysInSource(readFileSync(absFile, 'utf8'), absFile)
}

/** 扫描整个源码树的全部调用点键（与 `scanAllCallSiteParams` 同口径的文件枚举） */
export function scanAllCallSiteKeys(srcDir: string): {
  callSites: CallSiteKeyRef[]
  filesScanned: number
} {
  const files = listSourceFiles(srcDir)
  const callSites: CallSiteKeyRef[] = []
  for (const f of files) callSites.push(...scanCallSiteKeys(f))
  return { callSites, filesScanned: files.length }
}

/** 扫描一份源码里所有「第二个参数是对象字面量」的 t() 调用点 */
export function scanCallSiteParamsInSource(src: string, file: string): CallSiteParamRef[] {
  const mask = maskSource(src, file.endsWith('.vue'))
  const out: CallSiteParamRef[] = []
  CALL_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = CALL_RE.exec(src)) !== null) {
    // 调用名必须落在代码区（字符串/注释/HTML 文本里的同名文本不算）
    if (mask[m.index] !== CODE) continue
    const openParen = m.index + m[0].length - 1
    let i = skipTrivia(src, mask, openParen + 1)
    if (i >= src.length) continue

    // ── 第一个参数：键 ──
    let key = ''
    let keyKind: CallSiteKeyKind = 'dynamic'
    let keyPrefix = ''
    let keySuffix = ''
    if (src[i] === "'" || src[i] === '"') {
      const r = readQuotedValue(src, i)
      key = r.value
      keyKind = 'literal'
      i = r.end
    } else if (src[i] === '`') {
      const { end } = skipTemplate(src, i)
      const raw = src.slice(i + 1, end - 1)
      key = raw
      keyKind = 'template'
      const parts = templateStaticParts(raw)
      keyPrefix = parts.prefix
      keySuffix = parts.suffix
      i = end
    } else {
      // 变量键：静态不可知
      const vm2 = src.slice(i).match(/^[A-Za-z_$][\w$.]*/)
      key = vm2 ? vm2[0] : ''
      keyKind = 'dynamic'
      i += vm2 ? vm2[0].length : 0
    }

    // ── 第二个参数 ──
    i = skipTrivia(src, mask, i)
    if (src[i] !== ',') continue // 没有第二参数
    i = skipTrivia(src, mask, i + 1)
    if (src[i] !== '{') continue // 第二参数不是对象字面量（变量型参数本轮不分析）

    const bal = readBalancedObject(src, mask, i)
    if (!bal) continue
    const bodyStart = i + 1
    const bodyEnd = bal.end - 1
    const props = splitTopLevelProps(src, mask, bodyStart, bodyEnd)
    const params: string[] = []
    let unresolvedParams = false
    for (const prop of props) {
      const name = propNameOf(prop)
      if (name === null) {
        unresolvedParams = true
        continue
      }
      params.push(name)
    }

    const { line, column } = offsetToLineCol(src, m.index)
    out.push({
      file,
      line,
      column,
      key,
      keyKind,
      keyPrefix,
      keySuffix,
      params,
      unresolvedParams,
      snippet: src.slice(m.index, bal.end).slice(0, 200),
    })
    CALL_RE.lastIndex = bal.end
  }
  return out
}

/** 跳过空白与注释（不是字符串） */
function skipTrivia(src: string, mask: Uint8Array, i: number): number {
  let j = i
  while (j < src.length) {
    if (/\s/.test(src[j])) {
      j++
      continue
    }
    if (mask[j] === COMMENT) {
      j++
      continue
    }
    break
  }
  return j
}

/** 扫描单个文件 */
export function scanCallSiteParams(absFile: string): CallSiteParamRef[] {
  const src = readFileSync(absFile, 'utf8')
  return scanCallSiteParamsInSource(src, absFile)
}

/** 源码文件枚举（与 scanner.ts 的口径一致：跳过 node_modules/dist/.git/i18n/locales/测试文件） */
export function listSourceFiles(srcDir: string): string[] {
  const out: string[] = []
  const skip = new Set(['node_modules', 'dist', '.git', 'i18n', 'locales'])
  const rec = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      const p = join(dir, e.name)
      if (e.isDirectory()) {
        if (skip.has(e.name) || e.name.startsWith('.')) continue
        rec(p)
      } else if (
        e.isFile() &&
        /\.(vue|ts|tsx|js|mjs)$/.test(e.name) &&
        !/\.(test|spec)\.ts$/.test(e.name)
      ) {
        out.push(p)
      }
    }
  }
  rec(srcDir)
  return out
}

/** 扫描整个源码树 */
export function scanAllCallSiteParams(srcDir: string): {
  callSites: CallSiteParamRef[]
  filesScanned: number
} {
  const files = listSourceFiles(srcDir)
  const callSites: CallSiteParamRef[] = []
  for (const f of files) callSites.push(...scanCallSiteParams(f))
  return { callSites, filesScanned: files.length }
}

// ─────────────────────────── 工具 ───────────────────────────

export function offsetToLineCol(src: string, offset: number): { line: number; column: number } {
  let line = 1
  let lastNl = -1
  for (let i = 0; i < offset; i++) {
    if (src.charCodeAt(i) === 10) {
      line++
      lastNl = i
    }
  }
  return { line, column: offset - lastNl }
}
