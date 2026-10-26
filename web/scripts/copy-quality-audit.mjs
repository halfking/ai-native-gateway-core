/**
 * copy-quality-audit.mjs — 文案「AI 腔」静态审计（2026-10-24）
 *
 * 为什么要有这道门
 * ────────────────
 * 本仓的 i18n 门禁管的是**结构**（键同构、占位符同构、值有没有翻）。
 * 没有任何一道门管**文案的腔调** —— 于是「无缝」「稳健」「赋能」「seamless」
 * 「robust」这类空洞形容词可以随时被写进 664 个语言包文件里，
 * 而且没有任何东西会变红。
 *
 * 这道门把 impeccable 的 STYLE.md 禁用词表变成可执行的判据。
 * 完整编辑方针见仓库根目录 DESIGN.md §7「Voice」。
 *
 * 覆盖的判据
 * ──────────
 *   A. 空洞形容词 / 营销腔     「seamless / robust / elevate / 无缝 / 稳健 / 赋能 …」
 *   B. 借来的工程师腔          「load-bearing / highest-leverage / reflex defaults …」
 *   C. 喉头清嗓与过渡          「in today's … / let's dive in / moreover / furthermore …」
 *   D. 破折号当标点            英文/西文里的 `—` 与 ` -- `
 *   E. 装饰性中点串            `A · B · C`（三段以上，像包装纸而不是句子）
 *   F. 否定转折当骨架          「不是 X，而是 Y」/「not just X, it's Y」
 *
 * 语种口径
 * ────────
 *   · 拉丁字母语种（en/de/es/fr/ar）查英文词表；
 *   · 中日韩（zh-CN / zh-TW / ja-JP）查中文词表；
 *   · **破折号只查拉丁字母语种** —— 中文的「——」与日文的「——」是正常破折号，
 *     不是英文那个「作者没决定两个分句什么关系」的信号。
 *   · 装饰性中点串只查**含拉丁字母**的串（`API · Gateway`）与中文串
 *     （`内核开源 · 企业级 · 私有部署`）—— 阿拉伯语用的是 `·` 还是 `،`
 *     由作者决定，不做猜测。
 *
 * 用法
 * ────
 *   node scripts/copy-quality-audit.mjs             # 人读报告
 *   node scripts/copy-quality-audit.mjs --strict    # 有违规则 exit 1（CI 门）
 *   node scripts/copy-quality-audit.mjs --json out.json
 *   node scripts/copy-quality-audit.mjs --selftest  # ★ 判据自证
 *
 * ★ 判据必须能区分合格与不合格：改完请跑 --selftest，确认
 *   「植入违规 → 判红」与「干净样本 → 判绿」两个方向都成立。
 */

import { readFileSync, readdirSync, statSync, writeFileSync, mkdirSync } from 'node:fs'
import { resolve, relative, dirname, basename, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
// ★ CQA_WEB 允许把这次扫描**指到别的树**（A/B 对拍、worktree 副本）。
//   它只影响「读哪棵树」，不影响判据 ⇒ 但审计线索必须自陈扫的是哪棵，
//   否则「在哪棵树上绿过」这件事没法复核。
const WEB = process.env.CQA_WEB || resolve(HERE, '..')
const LOCALES = resolve(WEB, 'src/locales')
// ★ 仓根：移动端（web-mobile/）是**另一个包**，它的语言包布局也不同
//   （web-mobile/src/i18n/<locale>.ts 扁平文件，不是 src/locales/<locale>/*.ts）。
const REPO = resolve(WEB, '..')
const MOBILE_SRC = resolve(REPO, 'web-mobile/src')
const MOBILE_I18N = resolve(MOBILE_SRC, 'i18n')
/** 移动端语言包文件名 ⇒ 语种。 */
const LOCALE_FILE_RE = /^(zh-CN|zh-TW|en-US|ja-JP|de-DE|es-ES|fr-FR|ar-SA)\.ts$/

/** 相对路径键统一成 POSIX 分隔符（Windows 教训，见 ui-audit.mjs 的注释）。 */
const relPosix = (p) => relative(REPO, p).replace(/\\/g, '/')

// ─────────────────────────────────────────────────────────────
// 棘轮基线（2026-10-25 新增）
// ─────────────────────────────────────────────────────────────
// 为什么要有基线：这门刚扩到**移动端**（web-mobile/），实测那里有 126 处
// 存量破折号（DESIGN.md §7 第 8 条）。一次性手改 126 条产品文案风险高
// （语义漂移），而仓内对这类存量债务的既有做法就是**登记基线 + 只许降**
// （对照：ar-SA 有 1102 条「值=英文原文」登记在 localeValueBaseline.json）。
//
// 分桶：`<树>/<语种>`（语言包面）与 `<树>/source`（源码文本面）。
// ★ 必须按树分桶：web 的 en-US 与 web-mobile 的 en-US 是**两个地方**，
//   混在一个桶里 ⇒ web 侧新出现的违规会被移动端的存量淹没。
const BASELINE_FILE = resolve(WEB, 'scripts/copy-quality-baseline.json')

/** 文件属于哪棵树（web / web-mobile）。 */
function treeOf(absFile) {
  const rel = relPosix(absFile)
  if (rel.startsWith('web-mobile/')) return 'web-mobile'
  if (rel.startsWith('web/')) return 'web'
  return 'other'
}

/** 读基线（不存在或非法 ⇒ null）。 */
export function readQualityBaseline() {
  try {
    const b = JSON.parse(readFileSync(BASELINE_FILE, 'utf8'))
    if (!b || typeof b !== 'object' || typeof b.perGroup !== 'object') return null
    return b
  } catch {
    return null
  }
}

/**
 * 本次扫描涉及的**全部桶**（含 0 值桶）。
 *   语言包面：`<树>/<语种>`；源码面：`<树>/source`。
 */
function allGroups(localeEntries, srcFiles) {
  const set = new Set()
  for (const e of localeEntries) set.add(`${treeOf(e.file)}/${e.locale}`)
  for (const f of srcFiles) set.add(`${treeOf(f)}/source`)
  return [...set].sort()
}

/**
 * 写基线：**只许降不许升**。
 * 任何一个桶要涨 ⇒ 拒绝写入并报错（把「抬基线掩盖问题」这条堵死）。
 */
export function writeQualityBaseline(perGroup) {
  const old = readQualityBaseline()
  if (old) {
    const raised = []
    for (const [g, n] of Object.entries(perGroup)) {
      const prev = old.perGroup?.[g]
      if (typeof prev === 'number' && n > prev) raised.push(`${g}: ${prev} → ${n}`)
    }
    if (raised.length) {
      throw new Error(`棘轮只能降不许升，拒绝写入基线：\n  ${raised.join('\n  ')}`)
    }
  }
  const body = {
    metric:
      'findings per (tree, locale); web=src/locales/<locale>/**, web-mobile=src/i18n/<locale>.ts; ' +
      'source面单独成桶 <tree>/source；棘轮只许降不许升',
    updated: new Date().toISOString().slice(0, 10),
    perGroup,
  }
  mkdirSync(dirname(BASELINE_FILE), { recursive: true })
  writeFileSync(BASELINE_FILE, JSON.stringify(body, null, 2) + '\n')
  return body
}

// ─────────────────────────────────────────────────────────────
// 词表
// ─────────────────────────────────────────────────────────────

/** A. 空洞形容词 / 营销腔。 */
const EN_HOLLOW = [
  'seamless', 'seamlessly', 'robust', 'robustness', 'elevate', 'elevates', 'empower',
  'empowers', 'underscore', 'underscores', 'pivotal', 'tapestry', 'leverage',
  'highest-leverage', 'load-bearing', 'biggest unlock', 'holistic', 'cutting-edge',
  'state-of-the-art', 'unlock', 'streamline', 'seamlessly', 'effortlessly',
  'blazing fast', 'game-changing', 'revolutionary', 'world-class', 'best-in-class',
]

const ZH_HOLLOW = [
  '无缝', '稳健', '赋能', '一站式', '全方位', '极大提升', '有效降低', '护盾',
  '极致', '卓越', '领先一步', '全能', '深度赋能', '智能化改造', '助力',
  '打造闭环', '打通全链路', '沉淀能力', '拉满', '无感知', '最强', '旗舰级',
  '开箱即用', '轻量级', '高性能', '智能化',
]

/**
 * ★ 技术名词豁免：少数禁用词在本仓是**技术名词**而不是 AI 腔。
 *   例：输入提示 "letters, digits, underscore and dash only"
 *        —— 这里的 underscore 指下划线这个字符本身（命名规则），
 *        不是「强调某价值」那种修辞用法。
 *   判别：同一串里在列举字符类（letters / digits / hyphens / …）
 *        ⇒ 视为技术名词，放行。
 *   ★ 窄口径优先：豁免只会少抓，不会多抓；宁可漏一条也不要整片假红。
 */
const EN_TECHNICAL_CHAR_CONTEXT =
  /\b(?:letters?|digits?|hyphens?|dashes?|characters?|symbols?|alphanumerics?)\b/i
/** 词 → 豁免判据（命中即放行该词）。 */
const EN_WORD_EXEMPTIONS = {
  underscore: EN_TECHNICAL_CHAR_CONTEXT,
  underscores: EN_TECHNICAL_CHAR_CONTEXT,
}

/** B. 借来的工程师腔。 */
const EN_JARGON = [
  'reflex defaults', 'collapses into monoculture', 'data-driven',
]

/** C. 喉头清嗓 / 过渡 / 收尾。 */
const EN_THROAT = [
  "in today's", 'gone are the days', "whether you're", "let's dive in",
  'moreover', 'furthermore', 'in summary', 'in conclusion',
  'delve', 'delves', 'delved', 'delving',
]

/**
 * D. 破折号当标点（只查拉丁字母语种）。
 *
 * ★ 只在**两个词之间**才算破折号误用。
 *   本仓把 `—` 当「空值占位」用得极多（表格里的「无」），那是数据约定，
 *   不是标点 —— 早期版本用 `EM_DASH = /—|\s--\s/` 把这几百处全判红了。
 *   ⇒ 判据必须要求破折号**两侧都有文字**。
 *
 * ★★ 2026-10-25 补：左侧**紧邻字母**是判据的盲区。
 *   实测：左侧是 `)` / `"` / `}` / `**` 的形态（同一副「破折号当标点」骨架）
 *   全部漏判 ⇒ 移动端语言包里量到 **48 处**同样的 AI 腔，而桶读数却是 0
 *   ⇒ **「读数 0」不等于「干净」**。
 *
 * ★ 判据最终形态（三个条件同时成立才算违规）：
 *   ① 破折号**左侧紧邻**一个「字母 / 数字 / 右括号 / 右花括号 / 引号 / 星号」
 *      —— 不要求它与前面的词相邻（`it at 0) — do not read` 也要抓住）；
 *   ② 破折号**右侧是空格 + 一个以字母开头的词**；
 *   ③ ★ 但右侧首 token 形如 `P99` / `L50`（字母+数字）⇒ 判作**区间标签**，放过
 *      —— `P50 — P99` 是范围连接号，不是「两个分句」，不是 AI 腔。
 *
 * ★ 形态学：②③ 合起来把「右侧只有一个短 token」排除在外，
 *   于是「标签 + 指令」型（`Loading — retry.`）仍被抓，
 *   而「区间 + 续句」型（`P50 — P99 are shown`）被放过。
 *
 * ★ 判据细节会改变命中集合（LESSONS §404 通用式三的又一次印证）：
 *   这条判据前后量过 4 个版本（紧邻字母 / 放宽收尾符 / 数字收窄 / 本版），
 *   每改一次命中集合都变 ⇒ **改判据必须重新量全仓，并逐条复核新命中**。
 */
const EM_DASH = /[\p{L}\p{N}\]\)}`"'*]\s+—\s+(?!\p{L}\d+\b)["'*]?\p{L}\w*(?=[\s\-.,;:!?)"'`]|$)/u
const DOUBLE_HYPHEN = /\s--\s/

/** E. 装饰性中点串：三段及以上。 */
const MIDDOT_RUN = /[^\s·]{2,}(?:\s*·\s*[^\s·]{2,}){2,}/
/**
 * ★ 行首的项目符号（`· ` 开头）不是装饰中点串 —— 那是列表。
 *   本仓的 dataLifecycle 提示文案就是这种形状：
 *     "…\n\n· 复制期间下载/写入不受影响\n· 完成后自动切换到新目录\n· 文件较多时可能耗时较久"
 *   不豁免的话会被整段判红（而它其实是正确的列表）。
 */
const BULLET_LEADER = /^[ \t]*·[ \t]+/gm

/** F. 否定转折当骨架。 */
const EN_NEGATION = /\bnot (?:just|only|merely)\b[^.!?]{0,60}\bbut\b/i
// ★ 中文侧不只「不是」，还有「不只是 / 不光是 / 不仅仅是」——
//   那是同一副骨架（先否定一个低配版本，再抬高），判据要一起覆盖。
//   ★ 结构：可选组是「不」与「是」之间的修饰前缀（仅仅/只/光/过），
//     不可把「是」写进可选组 —— 那样「不只是」永远匹配不上。
const ZH_NEGATION = /不(?:仅仅|只|光|过)?是[^，。！？]{1,24}[，,]\s*(?:而是|而在于)/

/** 拉丁字母语种（破折号/英文词表适用）。 */
const LATIN_LOCALES = new Set(['en-US', 'de-DE', 'es-ES', 'fr-FR', 'ar-SA'])
/** 中日韩（中文词表适用）。 */
const CJK_LOCALES = new Set(['zh-CN', 'zh-TW', 'ja-JP'])

// ─────────────────────────────────────────────────────────────
// 工具
// ─────────────────────────────────────────────────────────────

function walk(dir, out = []) {
  if (!existsDir(dir)) return out
  for (const e of readdirSync(dir)) {
    const p = join(dir, e)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (p.endsWith('.ts')) out.push(p)
  }
  return out
}
function existsDir(p) {
  try { return statSync(p).isDirectory() } catch { return false }
}

/**
 * 去掉注释（行注释 + 块注释），**逐字符对齐**（换行保留，其余填空格）。
 *
 * ★ 为什么必须做（实测到的假红来源）：
 *   语言包文件头部普遍会写「原稿是 `内核开源 · 企业级 · 私有部署` 这种三段式营销串」
 *   或者 `The previous draft was still the English marketing text: "Core Open Source · ..."`
 *   —— 这些是**注释里引用旧稿的说明文字**，不是给用户看的文案。
 *   提取器若不剥注释，就会把注释里的引号/反引号内容当成文案
 *   ⇒ 8 个语言包里凭空冒出「装饰中点串」假红。
 *   604/664 个语言包文件含 `//` ⇒ 这不是边角情况，是主干路径。
 *
 * 字符串/模板串里的 `//` 不是注释（如 "https://…"），所以要先判引号状态。
 */
export function stripComments(src) {
  const out = []
  let i = 0
  const n = src.length
  let quote = null
  while (i < n) {
    const c = src[i]
    if (quote) {
      out.push(c)
      if (c === '\\' && i + 1 < n) { out.push(src[i + 1]); i += 2; continue }
      if (c === quote) quote = null
      i++
      continue
    }
    if (c === '"' || c === "'" || c === '`') { quote = c; out.push(c); i++; continue }
    if (c === '/' && src[i + 1] === '/') {
      while (i < n && src[i] !== '\n') { out.push(' '); i++ }
      continue
    }
    if (c === '/' && src[i + 1] === '*') {
      out.push('  '); i += 2
      while (i < n && !(src[i] === '*' && src[i + 1] === '/')) {
        out.push(src[i] === '\n' ? '\n' : ' ')
        i++
      }
      if (i < n) { out.push('  '); i += 2 }
      continue
    }
    out.push(c)
    i++
  }
  return out.join('')
}

/**
 * 把字符串字面量的**源码形态**还原成**运行时的值**。
 *
 * ★ 为什么必须做（实测到的假红来源）：
 *   语言包里的多行提示写的是转义序列（`\n\n· 复制期间…`），
 *   源码里那两个 `\n` 是**两个字符**（0x5C 0x6E），不是换行
 *   ⇒ 于是「行首项目符号」那类判断的 `^` 锚点**永远匹配不到**
 *   ⇒ 合法的列表被当成装饰中点串整段判红。
 *   正确的口径是**判运行时的值**，不是判源码文本。
 */
const LITERAL_ESCAPES =
  /\\(n|r|t|b|f|v|0|u\{[0-9a-fA-F]+\}|u[0-9a-fA-F]{4}|x[0-9a-fA-F]{2}|['"`\\])/g

export function decodeLiteral(raw) {
  return raw.replace(LITERAL_ESCAPES, (_m, e) => {
    switch (e) {
      case 'n': return '\n'
      case 'r': return '\r'
      case 't': return '\t'
      case 'b': return '\b'
      case 'f': return '\f'
      case 'v': return '\v'
      case '0': return '\0'
      case '\\': return '\\'
      case "'": return "'"
      case '"': return '"'
      case '`': return '`'
      default:
        if (e.startsWith('u{')) return String.fromCodePoint(parseInt(e.slice(2, -1), 16))
        if (e.startsWith('u')) return String.fromCharCode(parseInt(e.slice(1), 16))
        if (e.startsWith('x')) return String.fromCharCode(parseInt(e.slice(1), 16))
        return e
    }
  })
}

/** 取出文件里的所有字符串字面量（单引号 / 双引号 / 反引号，不含插值）。 */
export function extractStrings(src) {
  const out = []
  // ★ 先剥注释：注释里的引号内容不是文案。
  const clean = stripComments(src)
  const re = /(['"`])((?:\\.|(?!\1)[^\\])*)\1/g
  for (const m of clean.matchAll(re)) {
    // ★ 再还原转义：判据要看**运行时的值**，不是源码文本。
    out.push({ value: decodeLiteral(m[2]), index: m.index })
  }
  return out
}

const lineOf = (src, idx) => src.slice(0, idx).split('\n').length

// ─────────────────────────────────────────────────────────────
// 判据
// ─────────────────────────────────────────────────────────────

export function checkWords(locale, str) {
  const problems = []
  const lower = str.toLowerCase()
  if (LATIN_LOCALES.has(locale)) {
    for (const w of [...EN_HOLLOW, ...EN_JARGON, ...EN_THROAT]) {
      // ★ 技术名词豁免（如「underscore」指下划线这个字符本身）。
      const exempt = EN_WORD_EXEMPTIONS[w]
      if (exempt && exempt.test(str)) continue
      const re = new RegExp(`\\b${w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\b`, 'i')
      if (re.test(lower)) {
        problems.push({ kind: 'word', word: w, msg: `空洞词「${w}」—— STYLE.md 禁用词表` })
        break
      }
    }
  }
  if (CJK_LOCALES.has(locale)) {
    for (const w of ZH_HOLLOW) {
      if (str.includes(w)) {
        problems.push({ kind: 'word', word: w, msg: `空洞词「${w}」—— DESIGN.md §7 禁用词表` })
        break
      }
    }
  }
  return problems
}

export function checkEmDash(locale, str) {
  // ★ 只查拉丁字母语种：中文/日文的「——」是正常破折号。
  if (!LATIN_LOCALES.has(locale)) return []
  // ★ 两个判据都必须要求「两侧都有文字」：
  //   EM_DASH     —— 单个「—」，两侧各有一个字母（可夹空白）；
  //   DOUBLE_HYPHEN —— 「--」两侧各有一个空白（ASCII 双连字符当破折号）。
  //   否则本仓把「—」当表格空值占位的用法（几百处）会被整片判红。
  if (!EM_DASH.test(str) && !DOUBLE_HYPHEN.test(str)) return []
  return [{ kind: 'dash', msg: '破折号当标点（— / --）—— 那是「作者没决定两个分句什么关系」的信号' }]
}

export function checkMidDot(locale, str) {
  if (locale === 'ar-SA') return [] // 阿拉伯语的分隔符由作者定，不猜
  // ★ 行首项目符号先换成普通短横 —— 那是列表，不是装饰中点串。
  const probe = str.replace(BULLET_LEADER, '- ')
  if (!MIDDOT_RUN.test(probe)) return []
  return [{ kind: 'middot', msg: '装饰性中点串（A · B · C）—— 那是包装纸，不是句子；拆成正常句子或列表' }]
}

export function checkNegation(locale, str) {
  const hit = LATIN_LOCALES.has(locale) ? EN_NEGATION.test(str) : ZH_NEGATION.test(str)
  if (!hit) return []
  return [{ kind: 'negation', msg: '否定转折当骨架（不是 X，而是 Y）—— 比词汇表更深的 AI 痕迹，能改成直接正面主张就改' }]
}

const CHECKS = [
  ['空洞词', checkWords],
  ['破折号', checkEmDash],
  ['中点串', checkMidDot],
  ['否定转折', checkNegation],
]

// ─────────────────────────────────────────────────────────────
// 主流程
// ─────────────────────────────────────────────────────────────

export function audit(entries) {
  const findings = []
  for (const { file: f, locale } of entries) {
    const rel = relPosix(f)
    const group = `${treeOf(f)}/${locale}`
    const src = readFileSync(f, 'utf8')
    for (const s of extractStrings(src)) {
      for (const [, fn] of CHECKS) {
        for (const p of fn(locale, s.value)) {
          findings.push({
            file: rel, line: lineOf(src, s.index), locale, group,
            value: s.value.length > 90 ? s.value.slice(0, 90) + '…' : s.value,
            msg: p.msg,
          })
        }
      }
    }
  }
  return findings
}

/** 源码文本面（src/** 里的模板文本与字符串字面量）。 */
export function auditSource(files) {
  const findings = []
  for (const f of files) {
    const rel = relPosix(f)
    const group = `${treeOf(f)}/source`
    for (const hit of auditSourceText(rel, readFileSync(f, 'utf8'))) {
      findings.push({ ...hit, group })
    }
  }
  return findings
}

/**
 * 语言包文件清单：**两棵树**，语种归属方式不同 ⇒ 必须显式带上 locale，
 * 不能从路径里猜（web 是 src/locales/<locale>/*.ts；移动端是 src/i18n/<locale>.ts）。
 */
function collectLocaleEntries() {
  const entries = []
  // ① web 控制台：src/locales/<locale>/**.ts
  for (const f of walk(LOCALES)) {
    const locale = basename(dirname(f))
    entries.push({ file: f, locale })
  }
  // ② 移动端：src/i18n/<locale>.ts（扁平文件；index.ts / keyScan.ts / *.spec.ts 不算）
  if (existsDir(MOBILE_I18N)) {
    for (const name of readdirSync(MOBILE_I18N)) {
      const m = LOCALE_FILE_RE.exec(name)
      if (!m) continue
      entries.push({ file: join(MOBILE_I18N, name), locale: m[1] })
    }
  }
  return entries
}

// ─────────────────────────────────────────────────────────────
// ★ 源码文本面（2026-10-25 新增）
// ─────────────────────────────────────────────────────────────
// 此前这道门**只扫语言包**（src/locales/**），而界面上的字还有一大片
// 留在源码里（src/** 的模板文本与字符串字面量）——
// ⇒ 那些字**从未进入过** AI 腔的判据视野。
//
// 为什么不早扫：源码里的字面量形态更杂（标识符、CSS 值、类名、测试夹具…），
// 容易把判据打偏 ⇒ 宁可先量一遍。
// 实测（2026-10-25）：src 下 3084 条含中文的整串字面量里，只命中 **4 处** AI 腔
// ⇒ 这条门**可以**扩到全 UI 文本面而不爆红。
//
// ★ 口径与中文棘轮门对齐：扫 src/**/*.{vue,ts,tsx,js,mjs}，
//   排除 src/locales（语言包有自己的扫描面）、src/i18n（基建与自身测试）、
//   **/*.test.* / **/*.spec.*（测试夹具常含故意违规的样本）。
const SRC_DIR = resolve(WEB, 'src')

function collectSourceFiles() {
  const out = []
  const EXCLUDE_DIRS = new Set(['node_modules', 'dist', '.git', 'locales', 'i18n'])
  function rec(dir) {
    let entries
    try { entries = readdirSync(dir) } catch { return }
    for (const e of entries) {
      const p = join(dir, e)
      let st
      try { st = statSync(p) } catch { continue }
      if (st.isDirectory()) {
        if (EXCLUDE_DIRS.has(e) || e.startsWith('.')) continue
        rec(p)
      } else if (
        /\.(vue|ts|tsx|js|mjs)$/.test(e) &&
        !/\.test\.[cm]?[jt]s?$/.test(e) &&
        !/\.spec\.[cm]?[jt]s?$/.test(e)
      ) {
        out.push(p)
      }
    }
  }
  rec(SRC_DIR)
  // ★ 移动端也是网关 UI 的一部分（同一个产品的另一个客户端）⇒ 同样纳入源码面。
  //   它的语言包在 src/i18n/<locale>.ts，已被 EXCLUDE_DIRS 里的 'i18n' 排除
  //   ⇒ 不会与语言包面重复计。
  if (existsDir(MOBILE_SRC)) rec(MOBILE_SRC)
  return out
}

/**
 * 抽 .vue 模板里的**纯文本节点**（>…<）。
 * ★ 剥 HTML 注释：`.vue` 里 `<!-- … -->` 是注释，不是文案。
 * ★ 排除含插值的片段（`{{ … }}`）：那部分由字符串字面量那条面负责。
 */
export function extractTemplateText(src) {
  const out = []
  if (!src.includes('<')) return out
  let clean = stripComments(src)
  // HTML 注释（.vue 专有）—— 逐字符对齐，保持偏移不变。
  clean = clean.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '))
  const re = />([^<>{}]+)</g
  for (const m of clean.matchAll(re)) {
    const v = m[1].trim()
    if (!v) continue
    out.push({ value: v, index: m.index })
  }
  return out
}

/**
 * ★ 源码面**只管中文**：含中文 ⇒ zh-CN；不含中文 ⇒ **跳过**。
 *
 * 为什么跳过非中文（实测 2026-10-25）：把拉丁规则套到源码字面量上，
 * 15 条命中里绝大多数是**假阳性** ——
 *   · `row in summary.by_model` 是**代码表达式**不是文案；
 *   · `dashboardBoard inject missing — mount …` 是给开发看的 console 警告；
 *   · `${label} · ${health} · …` 是紧凑标签的**合法分隔符**。
 * ⇒ 判据在源码面必须**收窄到它真正能判的那一类**：
 *   源码面要管的是「**还没搬进语言包的中文文案**」，
 *   英文文案在仓里的既定做法就是进语言包（`src/locales/**`），那边已经有门了。
 */
export function localeOfContent(str) {
  return /[一-鿿]/.test(str) ? 'zh-CN' : null
}

/** 对一段源码文本跑全套判据，返回发现列表。 */
export function auditSourceText(rel, src) {
  const findings = []
  const surfaces = [
    ...extractStrings(src),
    ...extractTemplateText(src),
  ]
  for (const s of surfaces) {
    const locale = localeOfContent(s.value)
    if (!locale) continue
    for (const [, fn] of CHECKS) {
      for (const p of fn(locale, s.value)) {
        findings.push({
          file: rel,
          line: lineOf(src, s.index),
          locale,
          value: s.value.length > 90 ? s.value.slice(0, 90) + '…' : s.value,
          msg: p.msg,
        })
      }
    }
  }
  return findings
}

// ─────────────────────────────────────────────────────────────
// 自证
// ─────────────────────────────────────────────────────────────

const FIXTURES = [
  {
    name: 'A 英文禁用词 seamless → 必须判红',
    expect: 'red',
    bad: { locale: 'en-US', str: 'Seamless access to every model.' },
    good: { locale: 'en-US', str: 'OpenAI and Anthropic land in the same endpoint.' },
    fn: checkWords,
  },
  {
    name: 'A 中文禁用词 无缝 → 必须判红',
    expect: 'red',
    bad: { locale: 'zh-CN', str: '开源与商业模型无缝接入。' },
    good: { locale: 'zh-CN', str: '客户端照旧写，网关不用改。' },
    fn: checkWords,
  },
  {
    name: 'A 具体说法 → 必须判绿（不要误伤具体事实）',
    expect: 'green',
    bad: { locale: 'zh-CN', str: '40 MB 镜像，200 并发 P99 低于 500 ms。' },
    good: { locale: 'en-US', str: 'A 40 MB image at 200 concurrent, P99 under 500 ms.' },
    fn: checkWords,
  },
  {
    name: 'D 英文破折号 → 必须判红',
    expect: 'red',
    bad: { locale: 'en-US', str: 'Data stays on your side — and the bill stays readable.' },
    good: { locale: 'en-US', str: 'Data stays on your side, and the bill stays readable.' },
    fn: checkEmDash,
  },
  {
    // ★★ 左侧是**右括号**的形态：同一副「破折号当标点」骨架，但旧判据漏判
    //   （实测这类在移动端语言包里有 48 处，而桶读数是 0 ⇒ 假干净）。
    name: 'D 破折号左侧是右括号 → 必须判红（旧判据的盲区）',
    expect: 'red',
    bad: { locale: 'en-US', str: 'Result truncated (at most 500 bindings) — the totals below are incomplete.' },
    good: { locale: 'en-US', str: 'Result truncated (at most 500 bindings). The totals below are incomplete.' },
    fn: checkEmDash,
  },
  {
    name: 'D 破折号左侧是加粗收尾 → 必须判红（同一盲区的另一形态）',
    expect: 'red',
    bad: { locale: 'en-US', str: 'The image keys are **absent from the response entirely** — that is not "free".' },
    good: { locale: 'en-US', str: 'The image keys are **absent from the response entirely**. That is not "free".' },
    fn: checkEmDash,
  },
  {
    // 反向对照：破折号作**区间连接号**是合法用法（不是「两个分句」）。
    //   少了这一条，判据会把 `P50 — P99` 判红，逼人把合法写法改坏。
    name: 'D 破折号作区间连接号 → 必须判绿（P50 — P99 不是两个分句）',
    expect: 'green',
    bad: { locale: 'en-US', str: 'Quantiles P50 — P99' },
    good: { locale: 'en-US', str: 'Quantiles P50 to P99' },
    fn: checkEmDash,
  },
  {
    // 区间 + 续句：右侧首 token 仍是「字母+数字」的区间标签 ⇒ 同样放过。
    name: 'D 区间标签 + 续句 → 必须判绿（右侧首 token 是 P99 这类标签）',
    expect: 'green',
    bad: { locale: 'en-US', str: 'Quantiles P50 — P99 are shown' },
    good: { locale: 'en-US', str: 'Quantiles P50 to P99 are shown' },
    fn: checkEmDash,
  },
  {
    // 左侧是**数字**的形态：旧判据与「数字收窄」版都会漏判。
    name: 'D 破折号左侧是数字 → 必须判红（500 — over-limit …）',
    expect: 'red',
    bad: { locale: 'en-US', str: 'This endpoint caps buckets at 5000 — over-limit combinations are hidden.' },
    good: { locale: 'en-US', str: 'This endpoint caps buckets at 5000. Over-limit combinations are hidden.' },
    fn: checkEmDash,
  },
  {
    // 右侧**以引号开头**的形态（引号里是原样的字段值，不能改大小写）。
    name: 'D 破折号右侧以引号开头 → 必须判红',
    expect: 'red',
    bad: { locale: 'en-US', str: 'It could not be read as configured — "enabled" here does not mean it is actually enabled.' },
    good: { locale: 'en-US', str: 'It could not be read as configured: "enabled" here does not mean it is actually enabled.' },
    fn: checkEmDash,
  },
  {
    // 右侧是**单个词**的形态：「标签 + 指令」型短提示。
    name: 'D 右侧是单个词 → 必须判红（Loading — retry.）',
    expect: 'red',
    bad: { locale: 'en-US', str: 'Loading — retry.' },
    good: { locale: 'en-US', str: 'Loading. Retry.' },
    fn: checkEmDash,
  },
  {
    // 左句的收尾符与前面的词**不相邻**（`it at 0) — do not read`）：
    //   字符组里若含空格就会把 run 截断 ⇒ 这条形态会漏判。
    name: 'D 右括号与前面的词不相邻 → 必须判红（it at 0) — do not read）',
    expect: 'red',
    bad: { locale: 'en-US', str: 'Their margin rate is undefined (the backend leaves it at 0) — do not read that as a zero margin.' },
    good: { locale: 'en-US', str: 'Their margin rate is undefined (the backend leaves it at 0). Do not read that as a zero margin.' },
    fn: checkEmDash,
  },
  {
    name: 'D ASCII 双连字符 -- → 必须判红',
    expect: 'red',
    bad: { locale: 'en-US', str: 'The gateway routes calls -- and keeps the ledger.' },
    good: { locale: 'en-US', str: 'The gateway routes calls, and keeps the ledger.' },
    fn: checkEmDash,
  },
  {
    name: 'D「—」作表格空值占位 → 必须判绿（数据约定，不是标点）',
    expect: 'green',
    bad: { locale: 'en-US', str: '—' },
    good: { locale: 'zh-CN', str: '—' },
    fn: checkEmDash,
  },
  {
    name: 'D 中文破折号 → 必须判绿（中文的「——」是正常破折号）',
    expect: 'green',
    bad: { locale: 'zh-CN', str: '按租户、模型、任务类型自动选路——上游出问题就换。' },
    good: { locale: 'zh-CN', str: '按租户、模型、任务类型自动选路。' },
    fn: checkEmDash,
  },
  {
    name: 'E 三段中点串 → 必须判红',
    expect: 'red',
    bad: { locale: 'zh-CN', str: '内核开源 · 企业级 · 私有部署' },
    good: { locale: 'zh-CN', str: '内核开源，私有化部署。' },
    fn: checkMidDot,
  },
  {
    name: 'E 两段中点串 → 必须判绿（页脚那种品牌 + 副标是正常的）',
    expect: 'green',
    bad: { locale: 'zh-CN', str: 'AI-Native 组织核心网关 · Apache 2.0 开源内核' },
    good: { locale: 'en-US', str: 'Gateway · Apache 2.0 core' },
    fn: checkMidDot,
  },
  {
    name: 'F 否定转折 → 必须判红',
    expect: 'red',
    bad: { locale: 'en-US', str: 'Not just a proxy, but an agent gateway.' },
    good: { locale: 'en-US', str: 'It routes, meters and audits every call.' },
    fn: checkNegation,
  },
  {
    name: 'F 中文否定转折 → 必须判红',
    expect: 'red',
    bad: { locale: 'zh-CN', str: '它不只是一个代理，而是面向 Agent 的网关。' },
    good: { locale: 'zh-CN', str: '它负责路由、计量与审计每一次调用。' },
    fn: checkNegation,
  },
  // ── 以下三条锁的是**本轮修掉的三类假阳性**，改动判据时必须仍然成立 ──
  {
    name: '★ 注释里的引号内容不算文案 → 必须判绿（提取器）',
    expect: 'green',
    kind: 'extract',
    src:
      '// 原稿是 `内核开源 · 企业级 · 私有部署` 这种三段式营销串。\n' +
      'export default {\n  kicker: \'Apache 2.0 开源内核\',\n}\n',
    wantValues: ['Apache 2.0 开源内核'],
  },
  {
    name: '★ 注释里的双引号跨行内容不算文案 → 必须判绿（提取器）',
    expect: 'green',
    kind: 'extract',
    src:
      '// The previous draft was still the English marketing text: "Core Open Source · Enterprise-Grade · Private\n' +
      '// Deployment", adjectives standing in for facts.\n' +
      'export default {\n  kicker: \'Apache 2.0 core\',\n}\n',
    wantValues: ['Apache 2.0 core'],
  },
  {
    name: '★ 转义序列 \\n 要还原成真换行（提取器）',
    expect: 'green',
    kind: 'extract',
    src: "export default {\n  tip: '第一行。\\n\\n· 列表项一\\n· 列表项二',\n}\n",
    wantValues: ['第一行。\n\n· 列表项一\n· 列表项二'],
  },
  {
    name: 'E 行首「· 」项目符号列表 → 必须判绿（那是列表，不是包装纸）',
    expect: 'green',
    bad: {
      locale: 'zh-CN',
      str: '即将修改附件存储目录。\n\n· 复制期间下载/写入不受影响\n· 完成后自动切换到新目录\n· 文件较多时可能耗时较久',
    },
    good: { locale: 'zh-CN', str: '内核开源，私有化部署。' },
    fn: checkMidDot,
  },
  {
    name: 'A「underscore」作技术名词（下划线这个字符）→ 必须判绿',
    expect: 'green',
    bad: { locale: 'en-US', str: 'Lowercase letters, digits, underscore and dash only.' },
    good: { locale: 'en-US', str: 'Lowercase letters and digits only.' },
    fn: checkWords,
  },
  {
    name: 'A「underscore」作修辞动词（强调价值）→ 必须判红',
    expect: 'red',
    bad: { locale: 'en-US', str: 'The gateway underscores what your team already owns.' },
    good: { locale: 'en-US', str: 'The gateway keeps what your team already owns.' },
    fn: checkWords,
  },
  // ── 以下是**源码文本面**的鉴别力（2026-10-25 新增）────────────────
  // 这一面此前**根本没有门** ⇒ 判据本身有牙还不够，还要证明「这条面被采到了」。
  {
    name: '★ 源码面：模板文本里的三段中点串 → 必须判红',
    expect: 'red',
    kind: 'source',
    src: '<template>\n  <p>内核开源 · 企业级 · 私有部署</p>\n</template>\n',
  },
  {
    name: '★ 源码面：模板文本里的干净句子 → 必须判绿',
    expect: 'green',
    kind: 'source',
    src: '<template>\n  <p>内核开源，私有化部署。</p>\n</template>\n',
  },
  {
    name: '★ 源码面：字符串字面量里的空洞词 → 必须判红',
    expect: 'red',
    kind: 'source',
    src: "export const tag = '卓越的模型体验'\n",
  },
  {
    name: '★ 源码面：HTML 注释里的违规串 → 必须判绿（注释不是文案）',
    expect: 'green',
    kind: 'source',
    src: '<template>\n  <!-- 原稿是 内核开源 · 企业级 · 私有部署 这种三段式 -->\n  <p>内核开源，私有化部署。</p>\n</template>\n',
  },
  {
    name: '★ 源码面：注释里的违规串 → 必须判绿（注释不是文案）',
    expect: 'green',
    kind: 'source',
    src: "// 原稿是 `内核开源 · 企业级 · 私有部署` 这种三段式。\nexport const kicker = 'Apache 2.0 开源内核'\n",
  },
]

function selftest() {
  const results = []
  for (const fx of FIXTURES) {
    if (fx.kind === 'source') {
      // 源码面夹具：判据 + 采集面都要有鉴别力。
      const hits = auditSourceText('views/Fixture.vue', fx.src)
      const ok = fx.expect === 'red' ? hits.length > 0 : hits.length === 0
      results.push({
        name: fx.name, ok,
        detail: `源码面命中 ${hits.length} 条（期望 ${fx.expect === 'red' ? '>0' : '0'}）${hits.length ? '：' + hits.map(h => h.value).join(' / ') : ''}`,
      })
      continue
    }
    if (fx.kind === 'extract') {
      // 提取器夹具：注释里的内容不得进入「文案集合」。
      const got = extractStrings(fx.src).map((s) => s.value)
      const ok = got.length === fx.wantValues.length &&
        fx.wantValues.every((w) => got.includes(w))
      results.push({
        name: fx.name, ok,
        detail: `提取到 ${got.length} 条（期望 ${fx.wantValues.length} 条）：${JSON.stringify(got)}`,
      })
      continue
    }
    const badHits = fx.fn(fx.bad.locale, fx.bad.str)
    const goodHits = fx.fn(fx.good.locale, fx.good.str)
    const ok = fx.expect === 'red'
      ? badHits.length > 0 && goodHits.length === 0
      : badHits.length === 0 && goodHits.length === 0
    results.push({
      name: fx.name, ok,
      detail: `违规样本命中 ${badHits.length} 条 / 干净样本命中 ${goodHits.length} 条（期望 ${fx.expect === 'red' ? '红/绿' : '绿/绿'}）`,
    })
  }
  return results
}

function main() {
  const argv = process.argv.slice(2)
  if (argv.includes('--selftest')) {
    const results = selftest()
    let failed = 0
    for (const r of results) {
      console.log(`  ${r.ok ? '✓' : '✗'} ${r.name} —— ${r.detail}`)
      if (!r.ok) failed++
    }
    console.log(`\n${failed === 0 ? '✅' : '❌'} 判据自证：${results.length - failed}/${results.length} 通过`)
    process.exit(failed === 0 ? 0 : 1)
  }

  const localeEntries = collectLocaleEntries()
  const srcFiles = collectSourceFiles()
  const findings = [...audit(localeEntries), ...auditSource(srcFiles)]

  // ── 棘轮：按桶计数 vs 基线 ──────────────────────────────────
  const perGroup = {}
  for (const f of findings) perGroup[f.group] = (perGroup[f.group] || 0) + 1
  const groups = [...new Set([...Object.keys(perGroup), ...Object.keys(readQualityBaseline()?.perGroup || {})])].sort()

  if (argv.includes('--update-baseline')) {
    // 写出**显式的全量桶**（含 0 值桶）——
    // 否则「某个桶现在是干净的」这件事在基线里看不见，
    // 下次那里冒出违规时报的是「基线里没有这一项」，看不出是「本来干净」。
    const next = {}
    for (const g of allGroups(localeEntries, srcFiles)) next[g] = perGroup[g] || 0
    const body = writeQualityBaseline(next)
    console.log(`基线已更新（${body.updated}）：`)
    for (const [g, n] of Object.entries(body.perGroup)) console.log(`  ${g}: ${n}`)
    return
  }

  const baseline = readQualityBaseline()
  const over = []
  const under = []
  for (const g of groups) {
    const cur = perGroup[g] || 0
    const limit = baseline?.perGroup?.[g]
    if (typeof limit !== 'number') {
      over.push(`${g}: 基线里没有这一项（新增的桶必须先登记基线）`)
      continue
    }
    if (cur > limit) over.push(`${g}: ${cur} > baseline ${limit}（新增 ${cur - limit} 处）`)
    else if (cur < limit) under.push(`${g}: ${cur} < baseline ${limit}（可降棘轮 ${limit - cur}）`)
  }

  if (argv.includes('--json')) {
    const outArg = argv[argv.indexOf('--json') + 1]
    const target = outArg && !outArg.startsWith('--') ? outArg : resolve(WEB, 'reports/copy-quality-audit.json')
    mkdirSync(dirname(target), { recursive: true })
    writeFileSync(target, JSON.stringify({ count: findings.length, perGroup, findings }, null, 2))
    console.log(`JSON → ${target}（${findings.length} 条）`)
  } else {
    console.log('copy-quality 审计 —— 文案 AI 腔（STYLE.md / DESIGN.md §7）')
    console.log(`扫描根：${WEB}（仓根 ${REPO}）`)
    console.log(
      `扫描 ${localeEntries.length} 个语言包文件（web + 移动端） + ` +
        `${srcFiles.length} 个源码文件（src/** 的模板文本与字面量）\n`,
    )
    if (findings.length === 0) {
      console.log('✅ 未发现违规')
    } else {
      const byLocale = new Map()
      for (const f of findings) {
        if (!byLocale.has(f.locale)) byLocale.set(f.locale, [])
        byLocale.get(f.locale).push(f)
      }
      for (const [loc, list] of byLocale) {
        console.log(`■ ${loc}（${list.length} 处）`)
        for (const f of list.slice(0, 30)) {
          console.log(`  ${f.file}:${f.line}  ${f.msg}`)
          console.log(`      「${f.value}」`)
        }
        if (list.length > 30) console.log(`  …… 另有 ${list.length - 30} 处`)
        console.log()
      }
    }
    // 棘轮读数（口径：分桶计数，基线只许降）
    console.log('棘轮（按桶计数，只许降不许升）：')
    for (const g of groups) {
      const cur = perGroup[g] || 0
      const limit = baseline?.perGroup?.[g]
      const mark = typeof limit !== 'number' ? '❌ 无基线' : cur > limit ? '❌ 超基线' : cur < limit ? '✅ 可降' : '✅'
      console.log(`  ${g}: ${cur} / baseline ${typeof limit === 'number' ? limit : '—'}  ${mark}`)
    }
    if (under.length) {
      console.log(`✅ 有 ${under.length} 个桶低于基线，可降棘轮：node scripts/copy-quality-audit.mjs --update-baseline`)
    }
  }

  if (over.length) {
    console.error(`\n❌ copy-quality 棘轮被突破：\n  ${over.join('\n  ')}`)
    console.error('   修掉新冒出来的文案，或（只有真的清干净了才）降棘轮：node scripts/copy-quality-audit.mjs --update-baseline')
    process.exit(1)
  }
  if (argv.includes('--strict') && findings.length > 0) {
    console.error(`❌ copy-quality 审计发现 ${findings.length} 处违规（--strict：零容忍，不看基线）`)
    process.exit(1)
  }
}

if (process.argv[1] && process.argv[1].endsWith('copy-quality-audit.mjs')) {
  try {
    main()
  } catch (e) {
    // 棘轮拒绝（抬基线）这类错误要给出**可读**的结论，而不是裸栈。
    console.error(`❌ ${e instanceof Error ? e.message : String(e)}`)
    process.exit(1)
  }
}
