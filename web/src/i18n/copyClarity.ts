// copyClarity.ts — 「清晰文案」判据（impeccable clarify.md 三条）· 2026-10-27 第 11 增量
//
// ★ 为什么放在 src/i18n/ 而不是 scripts/：
//   判据需要 **Vue 感知的注释剥离**（`maskSource`），而它是 TypeScript。
//   仓内既有模式（i18n-cjk-count.mjs / i18n-audit.mjs）：
//   CLI 用 spawnSync + --experimental-strip-types 跑 .ts 实现。
//   把实现放在 src/i18n/ 还有第二个好处：**能被 vitest 覆盖**。
//
// ★ 三条判据（来自 impeccable skill/reference/clarify.md）：
//   R1 错误三问：文案落在「错误位置」时，整句必须给出**恢复动作**。
//      「什么失败 / 为什么 / 怎么恢复」—— 只说「失败了」等于没说。
//   R2 别用手势：出现手势词时必须**点名具体目标**
//      （引号内容 / 拉丁文本 / 箭头指向）—— 「点这里」是手，不是动作。
//   R3 通用空状态：整句等于通用空状态短语 ⇒ 没点名「缺的是什么」。
//
// ★ 作用域：**界面层**（src/App.vue + views/ + components/ + composables/ + hooks/）。
//   api/ types/ utils/ 不在视野内（那不是界面文案；语言包内容由 copy-quality-audit 管）。
//   ⇒ 已知未覆盖：api/admin.ts 的 TENANT_STATUS_LABELS、types/quality-api.ts 的
//     ERROR_MESSAGES 等**接口层**中文 —— 它们仍在人工清单里，只是门看不见。

import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, relative } from 'node:path'
import { maskSource, SRC_DIR } from './hardcodedCjk.ts'

export type RuleId = 'R1-error-recovery' | 'R2-gesture' | 'R3-generic-empty'

export interface ClarityFinding {
  file: string
  line: number
  col: number
  rule: RuleId
  reason: string
  text: string
}

/** 界面层目录（相对 src/）。 */
export const UI_DIRS = ['views', 'components', 'composables', 'hooks']
/** 界面层根文件。 */
export const UI_ROOT_FILES = ['App.vue']

/** 界面层文件清单。 */
export function uiFiles(): string[] {
  const out: string[] = []
  for (const f of UI_ROOT_FILES) {
    const p = join(SRC_DIR, f)
    try {
      readFileSync(p, 'utf8')
      out.push(p)
    } catch {
      /* 不存在就跳过 */
    }
  }
  for (const d of UI_DIRS) {
    const p = join(SRC_DIR, d)
    try {
      readdirSync(p)
    } catch {
      continue
    }
    const rec = (dir: string): void => {
      for (const e of readdirSync(dir, { withFileTypes: true })) {
        const q = join(dir, e.name)
        if (e.isDirectory()) {
          if (e.name.startsWith('.') || e.name === 'node_modules') continue
          rec(q)
        } else if (
          e.isFile() &&
          /\.(vue|ts|tsx)$/.test(e.name) &&
          !/\.(test|spec)\.[cm]?[jt]sx?$/.test(e.name)
        ) {
          out.push(q)
        }
      }
    }
    rec(p)
  }
  return out
}

// ───────────────────────── 词表 ─────────────────────────

/** R1：失败/错误类信号。 */
const ERR_SIGNAL =
  /(失败|错误|异常|不可用|无法|超时|未安装|缺失|被拒|已满|为空|中断|失效|未找到|不存在|不支持|未能|不成功)/

/**
 * R1：恢复动作信号（「怎么恢复」）。
 *
 * ★ ★ 为什么每一条后面都带 `(?!失败|错误|异常)`：
 *   第一版把「清理」「更新」「查看」「设置」直接当恢复动作词，
 *   而**这些词常常就是失败的主体**：
 *     `alert('清理失败: ' + …)`  ← 含「清理」⇒ 被判成「已有恢复动作」⇒ **漏报**
 *   ⇒ 恢复动作必须是「**动作**」，不能是「**失败的那个动作**」。
 *   ⇒ 判据：恢复词后面**不得**紧跟「失败/错误/异常」。
 */
const RECOVER_SIGNAL =
  /(重试|重新|检查|请|前往|联系|刷新|调整|确认|稍后|启用|更新|查看|恢复|补充|指定|设置|改用|换|登录|填|解决|修复|排查|降级|清理|打开|选择|切换|返回|申请|取消|等待|重置|断开|重连|升级|回滚|重载|复制|粘贴|输入|填写|放宽|再次|允许|支持|改为)(?!失败|错误|异常)/

/** R1：标签/徽标语境 —— 那里的词是**状态名**，不是错误文案。 */
const LABEL_CONTEXT = /(label|cls|tooltip|defaultTooltip|badgeClass|badge)\s*[:=]/i

/** R1：日志语境 —— 日志不是界面文案。 */
const LOG_CONTEXT =
  /(console\.(log|error|warn|info|debug)|logger\.[A-Za-z]+|log\.(Printf|Println|Print|Fatalf))\s*\(/

/**
 * R1：错误**兜底形状** —— 字面量是「错误对象没有 message 时用这句」的替补。
 * ★ 这是最该抓的一种：它只在**什么都不知道**的时候出现 ⇒ 天然是最空洞的文案。
 *   `e instanceof Error ? e.message : '加载失败'` / `e.message || '加载失败'`
 */
const FALLBACK_SHAPE = /\.(?:message|detail|reason|msg)\s*\|\||instanceof\s+Error\s*\?/

/**
 * R1：错误**槽位** —— 赋值目标本身就是一个「错误文案变量」。
 *
 * ★ 为什么**不锚定行首**、且取**最后一个**赋值：
 *   真实代码里赋值前常带守卫：
 *     `if (!isAbort(error) && isCurrent()) ctx.loadError.value = '…'`
 *   锚定行首 ⇒ `if (...)` 让整行失配 ⇒ **漏报**（实测命中过）。
 *   取最后一个赋值 ⇒ 同��行里前面的比较（`x === 1`）不会被误取。
 */
const ERROR_TARGET_ASSIGN =
  /([A-Za-z_$][\w$]*(?:\s*\.\s*[A-Za-z_$][\w$]*)*)\s*=(?!=)/g
/** 目标名里出现这些词 ⇒ 它是筛选项/状态码/字段名，不是错误文案变量。 */
const NON_ERROR_TARGET = /(filter|label|option|type|status|mode|code|reason|tag|word|field|key)/i
/**
 * 目标名**某一段**含这些词 ⇒ 是错误文案变量。
 * ★ 为什么要**逐段**判而不是整串判：
 *   `ctx.loadError.value` —— 整串判会把 `.value` 也算进去（那不是变量名），
 *   而「只认最后一段」又会把 `error.value`（最常见的一种）漏掉
 *   ⇒ 正确口径：**逐段**看有没有一段本身是错误文案变量名。
 */
const ERROR_TARGET_WORD = /^(?:err|error|msg|message|fail)|(?:err|error|msg|message|fail)$/i

/** R1：**报错调用** —— 实参直接进了一个报错通道。 */
const ERROR_CALL =
  /(?:^|[^A-Za-z0-9])(?:alert|notify[A-Za-z0-9_]*|show[A-Za-z0-9_]*|ElMessage\.error|ElMessage\s*\.\s*error|[A-Za-z0-9_]*Err[A-Za-z0-9_]*)\s*\(/
/** R1：emit 的第一个实参是 'error'。 */
const ERROR_EMIT = /emit\(\s*['"]error['"]/
/** R1：throw / new Error。 */
const THROW_RE = /\bthrow\b|\bnew\s+[A-Za-z0-9_.]*Error\s*\(/

/** R2：手势词。 */
const GESTURE = /(点击|点这里|点一下|双击)/

/** R3：通用空状态短语（整句匹配，零白名单的「通用词黑名单」）。 */
export const GENERIC_EMPTY = new Set([
  '暂无数据',
  '暂无',
  '无数据',
  '没有数据',
  '(无数据)',
  '（无数据）',
  '暂无记录',
  '暂无信息',
  '暂无内容',
  '暂无结果',
  '无结果',
  '空空如也',
])

const CJK = /[一-鿿]/

// ───────────────────────── 提取 ─────────────────────────

/**
 * 取 `<template>` 块区间。
 *
 * ★ 必须**嵌套配对**，不能用 `indexOf('</template>')`：
 *   Vue 模板里大量使用 `<template v-if>` / `<template v-for>` 作为分组标签，
 *   它们有各自的 `</template>`。
 *   用「第一个 `</template>`」配对 ⇒ 顶层 `<template>` 的区间在**第一个嵌套
 *   template 结束处**就被截断 ⇒ 该文件**后面大半段模板从未被扫描**。
 *   实测：`src/views/RoutingDashboardView.vue` 顶层 `<template>` 在 974 行、
 *   第一个嵌套 `<template v-if>` 在 991 行 ⇒ 区间在 994 行就结束
 *   ⇒ 995~1683 行（含 1276 行的「索引暂无数据 — 点击 ↻ 刷新」）全部失明。
 *
 * ⇒ 改用**深度计数**：开标签 +1，闭标签 −1，深度归零时才是顶层区间的结束。
 */
function templateRanges(masked: string): Array<[number, number]> {
  const out: Array<[number, number]> = []
  const re = /<template(\s[^>]*)?>|<\/template>/g
  let m: RegExpExecArray | null
  let depth = 0
  let start = -1
  while ((m = re.exec(masked)) !== null) {
    if (m[0] !== '</template>') {
      if (depth === 0) start = m.index + m[0].length
      depth++
    } else {
      depth--
      if (depth === 0 && start >= 0) {
        out.push([start, m.index])
        start = -1
      }
    }
  }
  // 未闭合（理论上不该发生）⇒ 兜底到文件尾
  if (depth > 0 && start >= 0) out.push([start, masked.length])
  return out
}

/**
 * 取 `<script>` 块区间。
 *
 * ★ 同样必须**配对**，且 `.vue` 允许 **`<script setup>` + `<script>` 两个块**
 *   （`src/views/admin/UsageTrendExplorer.vue` 就是两个）。
 *   用「第一个 `</script>`」会把两个块合并成一个区间 ⇒
 *   第二个块里的字面量被当成第一个块的一部分（语句上下文判错）。
 * ⇒ 用深度计数逐个配对，每个 `<script…>` 各自成段。
 */
function scriptRanges(masked: string): Array<[number, number]> {
  const out: Array<[number, number]> = []
  const re = /<script(\s[^>]*)?>|<\/script>/g
  let m: RegExpExecArray | null
  let depth = 0
  let start = -1
  while ((m = re.exec(masked)) !== null) {
    if (m[0] !== '</script>') {
      if (depth === 0) start = m.index + m[0].length
      depth++
    } else {
      depth--
      if (depth === 0 && start >= 0) {
        out.push([start, m.index])
        start = -1
      }
    }
  }
  if (depth > 0 && start >= 0) out.push([start, masked.length])
  return out
}

/**
 * 取区间内的字符串字面量（单引号 / 双引号 / 模板串），保留偏移。
 *
 * ★ 关键：**必须按区间取**，不能扫全文件。
 *   Vue 模板里属性的双引号值**不是** JS 字符串：
 *     :title="triggerAvailability.available ? '' : (x.reason || '触发功能不可用')"
 *   全文件扫会把 17→96 整段当成一个 JS 字符串 ⇒
 *   ① 产出一个「表达式伪影」（把源码当文案判红）；
 *   ② 顺手吞掉里面真正的字面量 `'触发功能不可用'`。
 *   ⇒ 全文件扫描器在 .vue 上**既误报又漏报**。
 */
export function literalsIn(masked: string, start: number, end: number): Array<{ text: string; index: number; quote: string }> {
  const out: Array<{ text: string; index: number; quote: string }> = []
  let i = start
  while (i < end) {
    const c = masked[i]
    if (c === "'" || c === '"' || c === '`') {
      const quote = c
      let j = i + 1
      let buf = ''
      while (j < end) {
        const d = masked[j]
        if (d === '\\') {
          buf += masked[j] + (masked[j + 1] ?? '')
          j += 2
          continue
        }
        if (d === quote) break
        buf += d
        j++
      }
      out.push({ text: buf, index: i, quote })
      i = j + 1
      continue
    }
    i++
  }
  return out
}

/** 一条模板属性。 */
interface TemplateAttr {
  /** 属性名（原始，含 `:` / `v-bind:` 前缀）。 */
  name: string
  /** 属性值内容（已去掉外层引号）。 */
  value: string
  /** 属性值内容在源码中的起始偏移。 */
  valueStart: number
  /** true = 静态属性（值是纯文本）；false = 绑定属性（值是 JS 表达式）。 */
  isStatic: boolean
}

/**
 * 解析模板区间里的标签属性。
 *
 * ★ 为什么要区分静态 / 绑定：
 *   `empty-text="暂无发版模块"` —— 静态属性，值就是**纯文本**；
 *   `:title="'触发功能不可用'"` —— 绑定属性，值是 **JS 表达式**，
 *     里面的 `'…'` 才是字符串字面量。
 *   混为一谈 ⇒ 静态属性的中文被当成「一个巨大的字面量」，
 *   而绑定属性里的真字面量被漏掉。
 */
export function templateAttributes(masked: string, start: number, end: number): TemplateAttr[] {
  const out: TemplateAttr[] = []
  let i = start
  while (i < end) {
    if (masked[i] !== '<') {
      i++
      continue
    }
    let j = i + 1
    let q: string | null = null
    while (j < end) {
      const c = masked[j]
      if (q) {
        if (c === q) q = null
      } else if (c === '"' || c === "'") q = c
      else if (c === '>') break
      j++
    }
    const tagEnd = j
    // 解析标签内的属性
    let k = i + 1
    while (k < tagEnd) {
      const c = masked[k]
      if (/\s/.test(c)) {
        k++
        continue
      }
      if (c === '/' || c === '>') {
        k++
        continue
      }
      const nameStart = k
      while (k < tagEnd && !/[\s=/>]/.test(masked[k])) k++
      const name = masked.slice(nameStart, k)
      // 跳过空白找 '='
      let p = k
      while (p < tagEnd && /\s/.test(masked[p])) p++
      if (masked[p] !== '=') continue
      p++
      while (p < tagEnd && /\s/.test(masked[p])) p++
      const vc = masked[p]
      if (vc === '"' || vc === "'") {
        const vStart = p + 1
        let e = vStart
        while (e < tagEnd && masked[e] !== vc) e++
        out.push({
          name,
          value: masked.slice(vStart, e),
          valueStart: vStart,
          isStatic: !/^[:@#]|v-bind:|^v-bind\b|^v-on:/.test(name),
        })
        k = e + 1
        continue
      }
      // 无引号值
      const vStart = p
      while (p < tagEnd && !/[\s>]/.test(masked[p])) p++
      out.push({
        name,
        value: masked.slice(vStart, p),
        valueStart: vStart,
        isStatic: !/^[:@#]|v-bind:|^v-bind\b|^v-on:/.test(name),
      })
      k = p
    }
    i = tagEnd + 1
  }
  return out
}

/**
 * 取 **Vue 模板文本节点**（标签之间的纯文本）。
 *
 * ★ 为什么必须单独取：模板文本**不是字符串字面量**
 *   （`<p>点击查看诊断工作台</p>` 里根本没有引号）
 *   ⇒ 只抽字面量的提取器对模板文本**完全失明**
 *   ⇒ 手势词与通用空状态这两个判据会整片漏判。
 */
export function templateTextNodes(masked: string, start: number, end: number): Array<{ text: string; index: number }> {
  const out: Array<{ text: string; index: number }> = []
  let i = start
  while (i < end) {
    if (masked[i] === '<') {
      let j = i + 1
      let q: string | null = null
      while (j < end) {
        const c = masked[j]
        if (q) {
          if (c === q) q = null
        } else if (c === '"' || c === "'") q = c
        else if (c === '>') break
        j++
      }
      i = j + 1
      continue
    }
    let j = masked.indexOf('<', i)
    if (j < 0 || j > end) j = end
    const raw = masked.slice(i, j)
    // {{ ... }} 是表达式，不是文案（里面的字面量由绑定属性负责）
    const text = raw.replace(/\{\{[\s\S]*?\}\}/g, ' ').trim()
    if (text && CJK.test(text)) out.push({ text, index: i })
    i = j
  }
  return out
}

/** 某偏移所在「语句」文本（行级）。 */
function statementOf(masked: string, index: number): string {
  const start = masked.lastIndexOf('\n', index) + 1
  let end = masked.indexOf('\n', index)
  if (end < 0) end = masked.length
  return masked.slice(start, end)
}

/**
 * 字面量**之前**的上下文（跨行，回看 260 字符）。
 *
 * ★ 为什么不能只看行：错误兜底常写成多行三元
 *   ```
 *   error.value = e instanceof Error
 *     ? e.message
 *     : '加载失败'        ← 兜底在上一行之外
 *   ```
 *   只看本行 ⇒ 字面量所在行是 `: '加载失败'`，看不到 `.message` ⇒ 兜底形状漏判。
 */
function beforeOf(masked: string, index: number): string {
  return masked.slice(Math.max(0, index - 260), index)
}

// ───────────────────────── 规则 ─────────────────────────

/**
 * R1：失败类文案必须给出恢复动作。
 *
 * ★ 判据是**位置驱动**的，不是内容驱动的 ——
 *   第一版只按内容判（出现「失败/错误/异常」就算），实测把三类**不是错误文案**的东西
 *   整片判红了：
 *     ① 状态词映射   `if (s === 'failed') return '失败'`
 *     ② 枚举标签     `ORIGIN_LABEL = { error: '错误触发' }`
 *     ③ 校验提示     `notifyNameMappingsErr('原始名称和标准名称都不能为空')`（调用名带 Err）
 *   ⇒ 「文案里有失败两个字」**不等于**「这是错误文案」。
 *   现在只在这三种**错误位置**上判：
 *     (a) 兜底形状：'X.message || …' / 'instanceof Error ? … : …'
 *     (b) 错误槽位：赋值目标是错误文案变量（error/err/msg/message/fail 结尾族）
 *     (c) 报错调用：alert / notify* / ElMessage.error / *Err( / emit('error')
 *     (d) throw / new Error(
 */
export function ruleError(text: string, stmt: string, before: string): ClarityFinding | null {
  if (!CJK.test(text)) return null
  if (!ERR_SIGNAL.test(text)) return null
  if (RECOVER_SIGNAL.test(text)) return null
  if (LOG_CONTEXT.test(stmt)) return null
  if (LABEL_CONTEXT.test(stmt)) return null

  const inErrorPosition =
    FALLBACK_SHAPE.test(before) ||
    FALLBACK_SHAPE.test(stmt) ||
    THROW_RE.test(stmt) ||
    ERROR_EMIT.test(stmt) ||
    ERROR_CALL.test(stmt) ||
    (() => {
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
    })()
  if (!inErrorPosition) return null
  return { rule: 'R1-error-recovery', reason: '失败类文案没说「怎么办」（缺恢复动作）', text, line: 0, col: 0, file: '' }
}

/** R2：手势词必须点名具体目标。 */
export function ruleGesture(text: string): ClarityFinding | null {
  if (!CJK.test(text)) return null
  const m = GESTURE.exec(text)
  if (!m) return null
  const after = text.slice(m.index + m[0].length)
  const next = after.replace(/^[\s:：→>＞\-—]+/, '')
  // 点名目标：引号内容 / 拉丁字母数字 / 模板插值（动态目标）/ 箭头指向
  if (/^[「『“"']/.test(next)) return null
  if (/^[A-Za-z0-9]/.test(next)) return null
  if (next.startsWith('${')) return null
  if (/^→/.test(after)) return null
  return { rule: 'R2-gesture', reason: `手势词「${m[0]}」后面没点名具体目标`, text, line: 0, col: 0, file: '' }
}

/** R3：通用空状态短语。 */
export function ruleEmpty(text: string): ClarityFinding | null {
  if (!CJK.test(text)) return null
  const t = text.replace(/\s+/g, ' ').trim()
  if (!GENERIC_EMPTY.has(t)) return null
  return { rule: 'R3-generic-empty', reason: '通用空状态短语（没点名缺的是什么）', text, line: 0, col: 0, file: '' }
}

/**
 * 核心检测。
 * @param files 可注入（自证用），默认扫界面层。
 */
export function detectClarity(files?: string[]): ClarityFinding[] {
  const list = files ?? uiFiles()
  const findings: ClarityFinding[] = []
  const seen = new Set<string>()
  for (const file of list) {
    const src = readFileSync(file, 'utf8')
    const masked = maskSource(file, src)
    const rel = relative(SRC_DIR, file)
    const push = (r: ClarityFinding | null, index: number): void => {
      if (!r) return
      const upto = masked.slice(0, index)
      const line = upto.split('\n').length
      const col = index - upto.lastIndexOf('\n')
      const dedupeKey = `${rel}|${line}|${col}|${r.rule}|${r.text}`
      if (seen.has(dedupeKey)) return
      seen.add(dedupeKey)
      findings.push({ ...r, file: rel, line, col })
    }
    /** 脚本里的字面量（含模板串）⇒ 三条规则全判。 */
    const handleLiteral = (lit: { text: string; index: number; quote: string }): void => {
      const stmt = statementOf(masked, lit.index)
      const before = beforeOf(masked, lit.index)
      if (lit.quote === '`') {
        const bare = lit.text.replace(/\$\{[\s\S]*?\}/g, '')
        push(ruleError(bare, stmt, before), lit.index)
        push(ruleGesture(bare), lit.index)
        push(ruleEmpty(bare), lit.index)
        return
      }
      push(ruleError(lit.text, stmt, before), lit.index)
      push(ruleGesture(lit.text), lit.index)
      push(ruleEmpty(lit.text), lit.index)
    }

    if (file.endsWith('.vue')) {
      // ── script 块：JS 字面量 ──
      for (const [s, e] of scriptRanges(masked)) {
        for (const lit of literalsIn(masked, s, e)) handleLiteral(lit)
      }
      // ── template 块：文本节点 + 属性 ──
      for (const [s, e] of templateRanges(masked)) {
        for (const node of templateTextNodes(masked, s, e)) {
          push(ruleGesture(node.text), node.index)
          push(ruleEmpty(node.text), node.index)
        }
        for (const attr of templateAttributes(masked, s, e)) {
          if (attr.isStatic) {
            // 静态属性 = 纯文本（不是 JS 表达式）
            push(ruleGesture(attr.value), attr.valueStart)
            push(ruleEmpty(attr.value), attr.valueStart)
            continue
          }
          // 绑定属性 = JS 表达式 ⇒ 里面的字符串字面量按脚本规则判
          for (const lit of literalsIn(masked, attr.valueStart, attr.valueStart + attr.value.length)) {
            handleLiteral(lit)
          }
        }
      }
      continue
    }

    // ── .ts / .tsx：整文件都是代码 ──
    for (const lit of literalsIn(masked, 0, masked.length)) handleLiteral(lit)
  }
  return findings
}

// ───────────────────────── 自证 ─────────────────────────
//
// ★ 自证必须覆盖「已证明会把量具打偏」的样本：
//   · 日志里的中文不是界面文案（否则 console.error('加载失败') 被判红）
//   · 标签/徽标里的「探测失败」是状态名，不是错误文案
//   · 手势词后面点名了目标时不算违规（「点击「保存」生效」是合法写法）
//   · **模板文本节点不是字符串字面量**（只抽字面量的提取器对模板完全失明）

export interface SelftestCase {
  id: string
  desc: string
  files: Record<string, string>
  expect: Partial<Record<RuleId, number>>
}

export const SELFTEST: SelftestCase[] = [
  {
    id: 'S1',
    desc: '错误位置的裸失败文案 → 必须判红（R1）',
    files: {
      'a.vue': `<script setup>\nconst error = ref('')\nerror.value = e instanceof Error ? e.message : '加载失败'\n</script>`,
    },
    expect: { 'R1-error-recovery': 1 },
  },
  {
    id: 'S2',
    desc: '错误文案已含恢复动作 → 必须判绿',
    files: {
      'b.vue': `<script setup>\nconst error = ref('')\nerror.value = e instanceof Error ? e.message : '加载失败，请重试'\n</script>`,
    },
    expect: { 'R1-error-recovery': 0 },
  },
  {
    id: 'S3',
    desc: 'console.error 里的中文 → 必须判绿（日志不是文案）',
    files: {
      'c.vue': `<script setup>\ntry { await f() } catch (e) { console.error('加载失败', e) }\n</script>`,
    },
    expect: { 'R1-error-recovery': 0 },
  },
  {
    id: 'S4',
    desc: '标签/徽标里的「探测失败」→ 必须判绿（状态名不是错误文案）',
    files: {
      'd.vue': `<script setup>\nconst MAP = { probe_broken: { label: '探测失败', cls: 'badge-red' } }\n</script>`,
    },
    expect: { 'R1-error-recovery': 0 },
  },
  {
    id: 'S5',
    desc: '手势词没点名目标 → 必须判红（R2）',
    files: { 'e.vue': `<template>\n<p>点击查看诊断工作台</p>\n</template>` },
    expect: { 'R2-gesture': 1 },
  },
  {
    id: 'S6',
    desc: '手势词点了名（引号）→ 必须判绿',
    files: { 'f.vue': `<template>\n<p>点击「保存特色模型」生效</p>\n</template>` },
    expect: { 'R2-gesture': 0 },
  },
  {
    id: 'S7',
    desc: '手势词点了名（拉丁文本）→ 必须判绿',
    files: { 'g.vue': `<template>\n<p>点击 Verify 验证连通性</p>\n</template>` },
    expect: { 'R2-gesture': 0 },
  },
  {
    id: 'S8',
    desc: '通用空状态整句 → 必须判红（R3）',
    files: { 'h.vue': `<template>\n<p>(无数据)</p>\n</template>` },
    expect: { 'R3-generic-empty': 1 },
  },
  {
    id: 'S9',
    desc: '点名了缺什么的空状态 → 必须判绿',
    files: { 'i.vue': `<template>\n<p>这次探针还没有关联节点</p>\n</template>` },
    expect: { 'R3-generic-empty': 0 },
  },
  {
    id: 'S10',
    desc: '注释里的违规文案 → 必须判绿（注释不是文案）',
    files: {
      'j.vue': `<script setup>\n// error.value = '加载失败'\n/* alert('操作失败') */\nconst ok = true\n</script>`,
    },
    expect: { 'R1-error-recovery': 0, 'R2-gesture': 0, 'R3-generic-empty': 0 },
  },
  {
    id: 'S11',
    desc: '模板文本节点不是字符串字面量 → 仍必须判红（R2/R3）',
    files: {
      'k.vue': `<template>\n<div>\n  <p>点击查看详情</p>\n  <span>暂无数据</span>\n</div>\n</template>`,
    },
    expect: { 'R2-gesture': 1, 'R3-generic-empty': 1 },
  },
  {
    id: 'S12',
    desc: '模板**绑定属性**里的中文字面量 → 仍必须判红（R2）',
    files: { 'l.vue': `<template>\n<div :title="'点击查看详情'" />\n</template>` },
    expect: { 'R2-gesture': 1 },
  },
  {
    id: 'S13',
    desc: 'Vue 属性双引号值**不是** JS 字符串 ⇒ 不得产出「表达式伪影」，且真字面量不得被吞',
    files: {
      'm.vue': `<template>\n<div :title="a.available ? '' : (a.reason || '触发功能不可用')" />\n</template>`,
    },
    expect: { 'R1-error-recovery': 1 },
  },
  {
    id: 'S14',
    desc: '失败动作名**不是**恢复动作（`清理失败` 含「清理」但没有恢复路径）→ 必须判红',
    files: {
      'n.vue': `<script setup>\nalert('清理失败: ' + (e.message || 'x'))\n</script>`,
    },
    expect: { 'R1-error-recovery': 1 },
  },
  {
    id: 'S15',
    desc: '带点的赋值目标（ctx.loadError.value）也是错误槽位 → 必须判红',
    files: {
      'o.vue': `<script setup>\nctx.loadError.value = '节点核心状态加载失败，仍展示实时状态。'\n</script>`,
    },
    expect: { 'R1-error-recovery': 1 },
  },
  {
    id: 'S16',
    desc: '筛选项变量（errorKindFilter）不是错误槽位 → 必须判绿',
    files: {
      'p.vue': `<script setup>\nerrorKindFilter.value = '按错误类型筛选'\n</script>`,
    },
    expect: { 'R1-error-recovery': 0 },
  },
  {
    id: 'S17',
    desc: '★ 嵌套 `<template v-if>` 之后的内容仍必须被扫到（配对不得提前截断）',
    files: {
      'q.vue': `<template>\n<div>\n  <template v-if="x">\n    <p>内层</p>\n  </template>\n  <span>暂无数据</span>\n</div>\n</template>`,
    },
    expect: { 'R3-generic-empty': 1 },
  },
  {
    id: 'S18',
    desc: '★ 嵌套 template **之前**的内容同样必须被扫到（区间起点不得错位）',
    files: {
      'r.vue': `<template>\n<p>暂无数据</p>\n<template v-if="x">\n  <span>内层</span>\n</template>\n</template>`,
    },
    expect: { 'R3-generic-empty': 1 },
  },
  {
    id: 'S19',
    desc: '★ `<script setup>` + `<script>` **两个脚本块**都要被扫到（不得合并成一段）',
    files: {
      's.vue': `<template>\n<div />\n</template>\n<script setup lang="ts">\nconst msg = '暂无数据'\n</script>\n<script lang="ts">\nconst other = '暂无数据'\nexport default { name: 'x' }\n</script>`,
    },
    expect: { 'R3-generic-empty': 2 },
  },
]

/**
 * 自证执行器：把夹具写到系统临时目录，跑判据，比对期望。
 * 夹具**不进仓**（仓内 .tmp-* 会被 git 当未跟踪文件）。
 */
export function runSelftest(): { passed: number; total: number; failed: string[] } {
  const tmp = join(tmpdir(), 'copy-clarity-selftest')
  const failed: string[] = []
  let passed = 0
  for (const c of SELFTEST) {
    const written: string[] = []
    for (const [name, content] of Object.entries(c.files)) {
      const p = join(tmp, name)
      mkdirSync(dirname(p), { recursive: true })
      writeFileSync(p, content)
      written.push(p)
    }
    const got = detectClarity(written)
    const problems: string[] = []
    for (const [rule, want] of Object.entries(c.expect)) {
      const have = got.filter((g) => g.rule === rule).length
      if (have !== want) problems.push(`${rule}=${have}(期望${want})`)
    }
    for (const g of got) {
      if (!(g.rule in c.expect)) problems.push(`越界命中 ${g.rule}`)
    }
    if (problems.length) failed.push(`${c.id} ${c.desc} —— ${problems.join(' ')}`)
    else passed++
    for (const p of written) {
      try {
        rmSync(p)
      } catch {
        /* ignore */
      }
    }
  }
  try {
    rmSync(tmp, { recursive: true, force: true })
  } catch {
    /* ignore */
  }
  return { passed, total: SELFTEST.length, failed }
}
