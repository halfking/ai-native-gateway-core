// hardcodedCjk.ts — 硬编码中文计数器 + baseline 棘轮
//
// 目的：阻止 src/ 中新增硬编码中文。存量无法一次性清零，因此采用
// ratchet（棘轮）机制：scripts/i18n-cjk-baseline.json 记录当前计数，
// 计数只许降不许升。
//
// 计数口径（metric）：
//   - 文件：src/**/*.{vue,ts,tsx,js,mjs}
//   - 排除：src/locales/**（翻译源文件本身）、src/i18n/**（i18n 基建与自身测试）、
//           **/*.test.ts / **/*.spec.ts（测试夹具常含中文样本）
//   - 注释：只剥离**词法器认得的**注释（见下方「口径变更」）
//   - 计数：剩余文本里每段 /[\u4e00-\u9fff]+/g 的匹配次数（连续中文段记 1 次）
//
// ⚠️ 口径变更 2026-10-06（并修正了它自己上一版的实现）
//   旧口径只跳过「trim 后以 // * /* 开头」的**整行**，行尾注释里的中文照计。
//   实测（scripts/cjk-decompose.mjs）：旧口径 7030，其中大批是注释里的中文 ——
//   它们永远不会出现在屏幕上。
//   ⚠️ **第一版新口径也不对，已修**：它用 `ts.createScanner` 独立扫注释区间。
//     独立扫描器**不跟踪模板串的替换嵌套**：一旦源码里出现带 `${}` 的模板串，
//     它就不再产出任何注释 token（`skipTrivia` 两种取值都试过，均为 0）。
//     实测 `AuditLogView.vue` 的 <script>：真注释区间 33 条，扫描器只认出 14 条。
//     而 decompose 的 4 组自证（A/B/C/D1-D3）**全过** —— 它们探针里没有一个含
//     `${}` 的模板串。⇒ **自证覆盖不到的形状等于没验**：探针必须包含一个
//     「已证明会把量具打偏」的样本，见下方 D9。
//   现在改用**解析器**的注释区间 API（createSourceFile + getLeading/TrailingCommentRanges），
//   它就是 TS 自己收集注释走的路径。
//   ⚠️ **棘轮必须配套重铸**：口径放宽而 baseline 不动，等于一个量错目标的门。
//   ⚠️ **不要退回朴素正则切注释**：`/\*[\s\S]*?\*\//` 会把字符串字面量里的
//     `/*` 当注释起点（实测 21 个文件偏差、共 113 段）。朴素版与解析器版的差
//     **不是单向的**（有的文件朴素偏大、有的偏小）⇒ 两个错方向相反的量具互相
//     抵消时，比一个明显错的量具更危险。
//
// 消费方：
//   - scripts/i18n-cjk-count.mjs（CLI：npm run i18n:cjk:check）
//   - src/i18n/hardcoded_cjk.baseline.test.ts（pnpm test 强制执行）
//   - scripts/cjk-decompose.mjs（只读诊断；其自证 A 把本实现钉死在 CLI 输出上）

import { readFileSync, writeFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

const __filename = fileURLToPath(import.meta.url)
const __dirname = dirname(__filename)
const WEB_ROOT = join(__dirname, '..', '..')
export const SRC_DIR = join(WEB_ROOT, 'src')
export const BASELINE_FILE = join(WEB_ROOT, 'scripts', 'i18n-cjk-baseline.json')

const CJK_RE = /[一-鿿]+/g
const EXCLUDED_DIRS = new Set(['node_modules', 'dist', '.git', 'locales', 'i18n'])

export interface CjkFileCount {
  file: string
  count: number
}

export interface CjkCountResult {
  count: number
  files: CjkFileCount[]
}

/** Walk src/ collecting files that count toward the metric. */
export function collectFiles(dir: string = SRC_DIR): string[] {
  const out: string[] = []
  function rec(d: string): void {
    for (const e of readdirSync(d, { withFileTypes: true })) {
      const p = join(d, e.name)
      if (e.isDirectory()) {
        if (EXCLUDED_DIRS.has(e.name) || e.name.startsWith('.')) continue
        rec(p)
      } else if (
        e.isFile() &&
        /\.(vue|ts|tsx|js|mjs)$/.test(e.name) &&
        !/\.test\.[cm]?[jt]s?$/.test(e.name) &&
        !/\.spec\.[cm]?[jt]s?$/.test(e.name)
      ) {
        out.push(p)
      }
    }
  }
  rec(dir)
  return out
}

// ─────────────────────────── 注释剥离 ───────────────────────────
//
// 下面所有替换都**等长**（非换行字符一律替换成空格），因此偏移量在整条链路上不变，
// 可以在同一个字符串上叠加多道剥离。**这一点是自持的**：一旦某道改成不等长替换，
// 后面所有按偏移取区间的操作就会静默错位。
function blankKeepNewlines(s: string): string {
  return s.replace(/[^\n]/g, ' ')
}

function blankRange(src: string, start: number, end: number): string {
  return src.slice(0, start) + blankKeepNewlines(src.slice(start, end)) + src.slice(end)
}

/**
 * 取**真**注释区间：走 TypeScript **解析器**的注释 API
 * （createSourceFile + getLeadingCommentRanges），这正是 TS 自己收集注释的路径。
 *
 * 遍历方式：**逐 token 扫它的前导 trivia**。正确性论证：
 *   ① 完备 —— 任何两条 token 之间的注释，必然属于**后面那条 token 的前导 trivia**
 *      （否则它前面就没有 token 了），EOF 也单独兜一次 ⇒ 一条不漏。
 *   ② 无误报 —— 模板串/正则/字符串整体是**一个** token，其内部文本不属于任何
 *      token 的前导 trivia ⇒ 不可能被当成注释。
 *
 * ⚠️ **三种试过、两种是错的**（2026-10-06 实测）：
 *   A. `ts.createScanner` 独立扫描 —— 不跟踪模板串替换嵌套，遇到带 `${}` 的模板串
 *      后再也不产出注释 token（`skipTrivia` 两种都试过，都是 0 条）。
 *      实测 `AuditLogView.vue` 的 <script>：真注释区间 33 条，扫描器只认出 14 条。
 *   B. 只在**节点**上收（前导 + 行尾）—— 漏掉「块 / interface 尾部 `}` 之前的
 *      独立行注释」与「空 catch 块里的注释」：它们不属于任何节点。
 *      实测 `api/usage.ts` 尾部 5 行、`lib/shell/hyper/context.ts` 的空 catch 各漏。
 *   C. 节点 + NodeArray.pos —— 仍漏 B 的第一种（NodeArray.pos 是数组**起点**，
 *      扫不到数组**尾部**到 `}` 之间）。
 *   ⇒ D（本文所用）：`node.getChildren()` 全 token 走查。
 *   ⚠️ A 的症状极隐蔽：计数只会**偏大**，方向与正常一致、不报错、不破坏单调性，
 *      于是任何只验「数量级 / 单调性」的自证都会放它过去。探针见 D9 / D13 / D14。
 */
function scriptCommentRanges(text: string, base: number, kind: ts.ScriptKind): Array<[number, number]> {
  const sf = ts.createSourceFile('cjk-probe.ts', text, ts.ScriptTarget.Latest, true, kind)
  const ranges: Array<[number, number]> = []
  const stack: ts.Node[] = [sf]
  while (stack.length > 0) {
    const node = stack.pop()!
    const leading = ts.getLeadingCommentRanges(text, node.pos)
    if (leading) for (const r of leading) ranges.push([base + r.pos, base + r.end])
    // ⚠️ **行尾注释必须显式再收一次**：`getLeadingCommentRanges` 内部是
    //   `collecting = trailing || text[pos] === '/'` —— 只有当 pos **正好落在斜杠上**
    //   （或刚跨过一个换行）才开始收集。于是 `const a = 1 // 中文` 这种**同行行尾**注释
    //   会被静默漏掉（D5 就是这么红的）。`getTrailingCommentRanges` 专收同行行尾。
    const trailing = ts.getTrailingCommentRanges(text, node.end)
    if (trailing) for (const r of trailing) ranges.push([base + r.pos, base + r.end])
    for (const child of node.getChildren()) stack.push(child)
  }
  // 同一个注释可能被相邻两条 token 的前导各收一次 ⇒ 合并重叠区间。
  ranges.sort((a, b) => a[0] - b[0] || a[1] - b[1])
  const merged: Array<[number, number]> = []
  for (const r of ranges) {
    const last = merged[merged.length - 1]
    if (last && r[0] <= last[1]) last[1] = Math.max(last[1], r[1])
    else merged.push([r[0], r[1]])
  }
  return merged
}

function scriptKindOf(fileName: string): ts.ScriptKind {
  if (/\.tsx$/.test(fileName)) return ts.ScriptKind.TSX
  if (/\.(js|mjs|cjs)$/.test(fileName)) return ts.ScriptKind.JS
  return ts.ScriptKind.TS
}

/** 剥离 [start, end) 区间内的脚本注释（就地返回新的等长字符串）。 */
function maskScriptComments(src: string, start: number, end: number, kind: ts.ScriptKind): string {
  const ranges = scriptCommentRanges(src.slice(start, end), start, kind)
  let out = src
  for (const [a, b] of ranges.reverse()) out = blankRange(out, a, b)
  return out
}

/**
 * 找出 `<tag ...>` … `</tag>` 的**内容**区间。
 * 用 `\s[^>]*` 而不是 `[^>]*`：后者会把 `<scripting>` 也当成 `<script>`。
 */
function tagBlockRanges(src: string, tag: string): Array<[number, number]> {
  const re = new RegExp(`<${tag}(\\s[^>]*)?>`, 'gi')
  const ranges: Array<[number, number]> = []
  let m: RegExpExecArray | null
  while ((m = re.exec(src)) !== null) {
    const s = m.index + m[0].length
    const e = src.indexOf(`</${tag}>`, s)
    if (e === -1) break
    ranges.push([s, e])
    re.lastIndex = e
  }
  return ranges
}

const HTML_COMMENT_RE = /<!--[\s\S]*?-->/g
const CSS_COMMENT_RE = /\/\*[\s\S]*?\*\//g

/** 只在**非** script/style 的片段里剥 HTML 注释，避免吃掉脚本字符串里的 `<!--`。 */
function maskHtmlCommentsOutside(src: string, protectedRanges: Array<[number, number]>): string {
  const sorted = [...protectedRanges].sort((a, b) => a[0] - b[0])
  let out = ''
  let cur = 0
  for (const [s, e] of sorted) {
    if (s > cur) out += src.slice(cur, s).replace(HTML_COMMENT_RE, blankKeepNewlines)
    out += src.slice(s, e)
    cur = e
  }
  if (cur < src.length) out += src.slice(cur).replace(HTML_COMMENT_RE, blankKeepNewlines)
  return out
}

function maskStyleComments(src: string, start: number, end: number): string {
  return (
    src.slice(0, start) +
    src.slice(start, end).replace(CSS_COMMENT_RE, blankKeepNewlines) +
    src.slice(end)
  )
}

/** 抹掉一处源码里的全部注释（按文件类型分派），长度不变。 */
export function maskSource(absFile: string, src: string): string {
  const kind = scriptKindOf(absFile)
  if (!absFile.endsWith('.vue')) return maskScriptComments(src, 0, src.length, kind)

  // 区间一律在**原始** src 上算：下面每道剥离都等长，偏移不会漂。
  const scriptRanges = tagBlockRanges(src, 'script')
  const styleRanges = tagBlockRanges(src, 'style')

  let out = src
  for (const [s, e] of [...scriptRanges].reverse()) out = maskScriptComments(out, s, e, kind)
  for (const [s, e] of [...styleRanges].reverse()) out = maskStyleComments(out, s, e)
  out = maskHtmlCommentsOutside(out, [...scriptRanges, ...styleRanges])
  return out
}

/** Count CJK runs in one file's source, with comments stripped. */
export function countFile(absFile: string): number {
  const src = readFileSync(absFile, 'utf8')
  const matches = maskSource(absFile, src).match(CJK_RE)
  return matches ? matches.length : 0
}

/** Full count: per-file counts sorted desc by count. */
export function countAll(srcDir: string = SRC_DIR): CjkCountResult {
  const files = collectFiles(srcDir)
  const perFile: CjkFileCount[] = []
  let count = 0
  for (const f of files) {
    const c = countFile(f)
    if (c > 0) perFile.push({ file: f.slice(srcDir.length + 1), count: c })
    count += c
  }
  perFile.sort((a, b) => b.count - a.count)
  return { count, files: perFile }
}

// ─────────────────────────── 自证（不是门禁的装饰） ───────────────────────────
//
// ⚠️ 这组自证的**唯一**价值是「每次跑门禁都执行」。写成只被调用一次、或干脆
//   只把结果算出来却不断言，都等于没有 —— 一个不触发的检查不报任何错，它只是
//   让源码看起来可信。
//   极端案例见 scripts/cjk-decompose.mjs 头注释：它的「自证 A」曾**整段缺失**，
//   而文件头还写着「自证 A 之外还有 B/C/D」。
//
// 方向必须**双向**：
//   - 「会触发」—— 会显示给用户的中文必须仍被计到（防止为了让滤注释而把门拆了）
//   - 「不误触发」—— 注释里的中文必须被跳过（防止滤过头，把真违规也滤了）

export interface SelfCheckCase {
  id: string
  desc: string
  expected: number
  actual: number
  ok: boolean
}

/** 纯函数：跑全部自证用例并返回逐条结果（不抛错，交给调用方决定）。 */
export function selfCheckCounting(): SelfCheckCase[] {
  const cases: SelfCheckCase[] = []
  const vue = (body: string) =>
    ['<template>', body, '</template>', '<script setup lang="ts">', 'const a = 1', '</script>'].join('\n')

  const probe = (id: string, desc: string, file: string, src: string, expected: number) => {
    const actual = maskSource(file, src).match(CJK_RE)?.length ?? 0
    cases.push({ id, desc, expected, actual, ok: actual === expected })
  }

  probe('D1', '模板文本里的中文仍被计到（门没被拆）', 'p.vue', vue('<p>模型完整性</p>'), 1)
  probe('D2', 'HTML 注释里的中文被跳过', 'p.vue', vue('<!-- 模型完整性 -->'), 0)
  probe(
    'D3',
    '<script> 里的 // 与 /* */ 注释被跳过',
    'p.vue',
    '<template><p>x</p></template>\n<script setup>\n// 模型完整性\n/* 监控 面板 */\nconst a = 1\n</script>',
    0,
  )
  probe(
    'D4',
    '字符串字面量里的 /* 不是注释起点，其后中文仍被计到',
    'p.vue',
    '<template><p>x</p></template>\n<script setup>\nconst mask = "/* 模型完整性 */"\n</script>',
    1,
  )
  probe(
    'D5',
    '行尾注释里的中文被跳过（旧口径会多计这类）',
    'p.vue',
    '<template><p>x</p></template>\n<script setup>\nconst a = 1 // 模型完整性\n</script>',
    0,
  )
  probe(
    'D6',
    '<style> 里的 CSS 注释被跳过，CSS 正文里的中文仍被计到',
    'p.vue',
    ['<style scoped>', '/* 面板 样式 */', '.a::after { content: "监控"; }', '</style>'].join('\n'),
    1,
  )
  probe(
    'D7',
    '脚本字符串里的 <!-- 不是 HTML 注释，其后中文仍被计到',
    'p.vue',
    '<template><p>x</p></template>\n<script setup>\nconst s = "<!-- 模型完整性 -->"\n</script>',
    1,
  )
  probe('D8', '纯 .ts 文件的注释剥离同样生效', 'p.ts', '/** 模型完整性 */\nexport const a = 1\n', 0)
  // ⚠️ D9 是**回归探针**，2026-10-06 实测踩坑加的：
  //   旧实现用 `ts.createScanner` 独立扫，它一遇到带 `${}` 的模板串就再也不产出
  //   注释 token —— 探针里只要有一个 `${}`，旧实现立刻挂。本仓 `AuditLogView.vue`
  //   的 <script> 里就有 `` `${e.target_type} #${e.target_id ?? '?'}` ``。
  //   ⇒ 凡是「换掉注释识别实现」的改动，都必须让 D9 保持绿。
  probe(
    'D9',
    '插值模板串之后的注释仍被跳过（旧扫描器实现在此漏切）',
    'p.vue',
    '<template><p>x</p></template>\n<script setup>\nconst s = `${a} #${b ?? \'?\'}`\n// 模型完整性\nconst c = 1\n</script>',
    0,
  )
  // D10 是 D9 的**反方向**：模板串里出现的 `// 中文` 是字面文本（会被显示），
  // 必须仍然被计到，否则就是「滤过头」。
  probe(
    'D10',
    '模板串字面量里的 // 不是注释，其后中文仍被计到',
    'p.vue',
    '<template><p>x</p></template>\n<script setup>\nconst s = `${a} // 模型完整性`\n</script>',
    1,
  )
  probe(
    'D11',
    '嵌套模板串之后的注释仍被跳过',
    'p.vue',
    '<template><p>x</p></template>\n<script setup>\nconst s = `${a}${`${b}`}`\n/* 模型完整性 */\nconst c = 1\n</script>',
    0,
  )
  probe(
    'D12',
    '文件末尾（EOF 之后）的注释被跳过',
    'p.ts',
    'export const a = 1\n// 模型完整性\n',
    0,
  )
  // ⚠️ D13 / D14 是**回归探针**，2026-10-06 实测踩坑加的：
  //   「只在节点上收注释」的两种实现（节点前导+行尾 / 节点+NodeArray.pos）都会漏掉
  //   下面两种形状 —— 注释在**块 / interface 尾部 `}` 之前**或**空 catch 块内部**，
  //   不属于任何节点。实测 `api/usage.ts` 尾部 5 行、`lib/shell/hyper/context.ts`
  //   的空 catch 各漏一整段。
  probe(
    'D13',
    'interface 尾部 `}` 之前的独立行注释被跳过（不属于任何节点）',
    'p.ts',
    'interface A {\n  a: number\n  // 模型完整性\n  // 第二行注释\n}\nexport const x = 1\n',
    0,
  )
  probe(
    'D14',
    '空 catch 块内部的注释被跳过（块内没有任何语句节点）',
    'p.ts',
    'try {\n  f()\n} catch {\n  // 模型完整性\n}\n',
    0,
  )
  probe(
    'D15',
    '多行模板串里以 // 开头的行是字面文本，仍被计到（不会被当成注释滤掉）',
    'p.vue',
    '<template><p>x</p></template>\n<script setup>\nconst s = `第一行\n// 模型完整性\n第三行`\n</script>',
    3,
  )
  probe(
    'D16',
    '正则字面量里的 // 不是注释，其后中文仍被计到',
    'p.ts',
    'const re = /\\/\\/ 模型完整性/\nexport const a = 1\n',
    1,
  )

  return cases
}

/** 跑自证并**抛错**——测试与 CLI 都用它，保证失败一定被看见。 */
export function assertSelfCheckCounting(): SelfCheckCase[] {
  const cases = selfCheckCounting()
  const failed = cases.filter((c) => !c.ok)
  if (failed.length > 0) {
    const detail = cases
      .map((c) => `  ${c.ok ? 'PASS' : 'FAIL'} ${c.id} ${c.desc} — expected=${c.expected} actual=${c.actual}`)
      .join('\n')
    throw new Error(`hardcoded CJK counter self-check failed:\n${detail}`)
  }
  return cases
}

// ─────────────────────────── baseline ───────────────────────────

export interface Baseline {
  metric: string
  count: number
  updated: string
}

export function readBaseline(): Baseline | null {
  try {
    return JSON.parse(readFileSync(BASELINE_FILE, 'utf8')) as Baseline
  } catch {
    return null
  }
}

export function writeBaseline(count: number): Baseline {
  const baseline: Baseline = {
    metric:
      'CJK runs after stripping parser-detected comments; src/**/*.{vue,ts,tsx,js,mjs}; excludes src/locales, src/i18n, *.test/spec',
    count,
    updated: new Date().toISOString().slice(0, 10),
  }
  writeFileSync(BASELINE_FILE, JSON.stringify(baseline, null, 2) + '\n', 'utf8')
  return baseline
}
