#!/usr/bin/env node
// verify-comment-boundaries.mjs — 「注释里藏着代码」门禁（UI 规范 17 §11.146）。
//
// ★★★★★★ 这道门堵的盲区是**别的门在结构上就看不见**的那一类：
//
// 实测样本（2026-10-14，第一百一十批）：`src/api/orphanLedger.spec.ts` 的
// 文件头块注释从第 5 行 `/**` 开启后，**中间二十多行丢了行首的 ` * ` 前缀**，
// 于是注释一直没闭合 —— 直到第 51 行那个本该独立成段的 `/** … */` 的
// `*/` 才把文件头注释关掉。**中间那两行是真的代码，被整段吞进了注释里：**
//
//     const ROOT = join(process.cwd(), 'src')
//     const API_DIR = join(ROOT, 'api')
//
// ★★★ 于是产生了本仓最隐蔽的一种读数：**每道门都绿，而那两行是死文本。**
//   - `vue-tsc` 绿：TypeScript 的 **TS2451（重复块级变量）恰恰看不见它** ——
//     注释已经把那两行从 program 里摘掉了，binder 眼里**根本不存在第二个 ROOT**。
//   - vitest 绿：esbuild 也不报错（那两行不是代码）。
//   - **孤儿棘轮 15 条断言全绿**：因为真正在跑的 `ROOT`/`API_DIR` 声明
//     还在文件后半段活着（本例第 104-105 行，是当初复制时留下的另一份）。
// ⇒ ★★★ 也就是说：**恰恰因为它是重复的，它才被伪装成无害**。
//   如果那两行不是重复的，这道门会在「一切正常」的读数下**静默失效** ——
//   而没有任何一道门会红。
//
// ⇒ 因此这道门与 TS2451 是**互补**而非重复：TS2451 管「同一份代码里声明两次」，
//   本门管「第二次声明被注释吞了」—— 后者对前者**永远不可见**。
//
// 判据：`src/**` 的 `.ts` / `.vue` 里，**块注释内部不得出现顶格的代码行**。
// 命中即 rc≠0，并逐行报出「文件:行号 + 该行的代码形态」。
//
// 口径与已知取舍（先写清楚，避免下一个人以为它比实际更严）：
//   - 只认**顶格**（列 0）开始的声明/语句形态。缩进在注释里的散文不算命中 ——
//     否则任何文档示例（` * const x = 1`）都会误响。
//   - 逐字符状态机处理 `//` 行注释、`/* */` 块注释、`'…'` / `"…"` / `` `…` `` 字符串
//     **以及正则字面量**，避免它们内部的形态被误当成注释或引号。
//   - 模板字符串按「遇到下一个反引号即闭合」处理，不解 `${}` 嵌套。
//     ⇒ 一个**未闭合**的反引号会让后续字符被当成字符串 ⇒ **漏报**（不误报）。
//     这是有意的取舍：宁可漏，不可把正常文件染红。
//
// ★★★★ 第一版**没有**处理正则字面量，代价是当场自曝了一次假阳性：
//   `orphanLedger.spec.ts:145` 的
//   `/from\s+['"][^'"]*api\/([A-Za-z0-9_]+)['"]/g`
//   里字符类 `['"]` 的那个 `"` 被状态机当成开引号 ⇒ 状态从此再没回过 `code`
//   ⇒ 后面 20 行正常的顶格函数全被判成「在注释里」。
// ★★★ 也就是说：**这道门一上来就报了 2 条命中，而正确的第一问是
//   「这条命中是真的吗」，不是「门在工作」** —— 两行 `function` 是再普通不过的
//   顶层代码。若不核对原文就照单全收，就会去「修」一个根本没坏的文件。
// --self-test：见 verify-comment-boundaries.selftest.mjs。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')

/** 顶格代码行：注释里出现这些形态 ⇒ 说明注释吞了代码。 */
const CODE_LINE_RE =
  /^(?:const|let|var|function|class|interface|type|enum|import|export|return|for|while|if|switch|async)\b/

/** `/` 之前是这些字符时，它开的是**正则**而不是除法。 */
const REGEX_PRECEDER_RE = /[(,=:[!&|?{};+\-*%^~<>]$/

/** 这些关键字后面出现的 `/` 同样是正则（`return /x/`，`typeof /x/` …）。 */
const REGEX_KEYWORDS = new Set([
  'return', 'typeof', 'instanceof', 'in', 'of', 'new', 'delete', 'void', 'case', 'do', 'else',
  'yield', 'await', 'throw',
])

/**
 * 逐字符走一遍，把**块注释里的字符**换成空格、其余原样保留。
 * 返回的文本与原文逐行对齐 ⇒ 第 N 行就是原文第 N 行。
 */
export function maskComments(text) {
  const out = Array.from(text)
  // 'code' | 'block' | 'line' | 'sq' | 'dq' | 'tpl' | 'regex' | 'regexClass'
  let state = 'code'
  // 上一枚**有意义的** code 态字符，用来判断 `/` 是除法还是正则开头。
  let prevCode = ''
  const lastWord = () => (/[A-Za-z0-9_$]*$/.exec(prevCode)?.[0] ?? '')
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    const n = text[i + 1]
    if (state === 'code') {
      if (c === '/' && n === '/') {
        out[i] = ' '
        out[i + 1] = ' '
        i++
        state = 'line'
        continue
      }
      if (c === '/' && n === '*') {
        out[i] = ' '
        out[i + 1] = ' '
        i++
        state = 'block'
        continue
      }
      if (c === '/' && (prevCode === '' || REGEX_PRECEDER_RE.test(prevCode) || REGEX_KEYWORDS.has(lastWord()))) {
        state = 'regex'
        continue
      }
      if (c === "'") state = 'sq'
      else if (c === '"') state = 'dq'
      else if (c === '`') state = 'tpl'
      else if (c !== ' ' && c !== '\t' && c !== '\n' && c !== '\r') prevCode = c
      continue
    }
    if (state === 'block') {
      if (c === '*' && n === '/') {
        out[i] = ' '
        out[i + 1] = ' '
        i++
        state = 'code'
        continue
      }
      if (c !== '\n') out[i] = ' '
      continue
    }
    if (state === 'line') {
      if (c !== '\n') out[i] = ' '
      else state = 'code'
      continue
    }
    if (state === 'regex') {
      if (c === '\\') {
        i++
        continue
      }
      if (c === '[') state = 'regexClass'
      else if (c === '/') state = 'code' // 正则结束；紧随的 flag 仍按 code 走
      else if (c === '\n') state = 'code' // ★ 跨行/未闭合 ⇒ 宁可漏报，不可误报
      continue
    }
    if (state === 'regexClass') {
      if (c === '\\') {
        i++
        continue
      }
      if (c === ']') state = 'regex'
      else if (c === '\n') state = 'code'
      continue
    }
    // 字符串态：只找闭合引号；字符串内部的注释形态一律不解释。
    if (c === '\\') {
      i++
      continue
    }
    if (state === 'sq' && c === "'") state = 'code'
    else if (state === 'dq' && c === '"') state = 'code'
    else if (state === 'tpl' && c === '`') state = 'code'
    if (state !== 'code' && c !== '\n') out[i] = '\u0000' // 字符串字符标出来
  }
  return out.join('')
}

/**
 * 扫一段源码，返回「块注释里藏着代码」的命中行。
 * 返回 [{ line, text }]，行号从 1 起。
 */
export function scanSource(text) {
  const masked = maskComments(text)
  const rawLines = text.split('\n')
  const maskedLines = masked.split('\n')
  const hits = []
  for (let i = 0; i < rawLines.length; i++) {
    const raw = rawLines[i]
    const mk = maskedLines[i] ?? ''
    // ★ 判据只认**整行落在块注释内**且是顶格代码形态。
    //   块注释内的字符被置为空格 ⇒ mk 全是空白；字符串内的字符被置为 \u0000。
    const insideBlock = mk.length > 0 && /^[ \u0000]*$/.test(mk) && /\S/.test(raw)
    if (!insideBlock) continue
    const trimmed = raw.trimStart()
    // 顶格（列 0）——缩进的散文不算
    if (raw !== trimmed) continue
    if (!CODE_LINE_RE.test(trimmed)) continue
    hits.push({ line: i + 1, text: trimmed.slice(0, 100) })
  }
  return hits
}

function collectFiles(dir, out) {
  for (const name of readdirSync(dir)) {
    if (name === 'node_modules' || name === 'dist' || name.startsWith('.')) continue
    const p = join(dir, name)
    const st = statSync(p)
    if (st.isDirectory()) collectFiles(p, out)
    else if (name.endsWith('.ts') || name.endsWith('.vue')) out.push(p)
  }
  return out
}

function main() {
  const srcDir = join(ROOT, 'src')
  // ★ 「扫不到候选源文件」必须是失败而不是放行 —— 与 i18n 门同一条纪律：
  //   路径写错 / include 变了 ⇒ 扫到 0 个文件 ⇒ 若此时报 OK，
  //   那么「✓ 0 个文件，块注释里没有藏代码」会覆盖到一个**根本没量过**的地方，
  //   比没有门更坏（照 verify-i18n-parity.mjs 的既有口径）。
  let files = []
  try {
    files = collectFiles(srcDir, [])
  } catch (e) {
    console.error(`✗ 块注释边界门禁：扫不到 ${srcDir}（${e.message}）`)
    process.exit(1)
  }
  if (files.length === 0) {
    console.error(`✗ 块注释边界门禁：${srcDir} 下扫不到任何 .ts / .vue 源文件`)
    process.exit(1)
  }
  const findings = []
  for (const f of files) {
    const hits = scanSource(readFileSync(f, 'utf8'))
    for (const h of hits) findings.push({ file: f.replace(ROOT + '/', ''), ...h })
  }
  if (findings.length > 0) {
    console.error(`✗ 块注释边界门禁：${findings.length} 行的代码被吞进了块注释`)
    for (const f of findings) console.error(`  ${f.file}:${f.line}  ${f.text}`)
    console.error('')
    console.error('这是「文件头注释丢了行首 ` * ` 前缀 ⇒ 注释不闭合」的典型形态。')
    console.error('后果：那一行是死文本，编译器与运行时都看不见它；而**其它门也不会红**。')
    console.error('修法：给注释正文补回 ` * ` 前缀并显式闭合，删掉被吞掉的代码行。')
    process.exit(1)
  }
  console.log(`✓ 块注释边界：${files.length} 个文件，块注释里没有藏代码`)
}

main()
