/**
 * internalJargon.ts —— 「内部标识泄漏」判据（唯一实现）
 * 第 19 增量（impeccable `clarify.md`）
 *
 * ─────────────────────────────────────────────────────────────────────────────
 * ★ 判据的出处（impeccable `skill/reference/clarify.md` 原文）
 *
 *   "An actionable error answers: …"
 *   "**Do not expose internal codes as the primary message.**"
 *   "Audit the language: … **internal jargon or assumed knowledge** …"
 *
 *   —— round 16 的「错误出口门」只覆盖**错误出口**（哪里报错）。
 *      但同一句纪律在**成功 / 说明 / 操作指引**里同样成立，
 *      而那一片**至今无人管**（实测：`node_probe_state 已自动恢复`、
 *      `routing_overrides 表的每次变更…` 这类句子，用户在界面上从没过这些名字）。
 *
 * ─────────────────────────────────────────────────────────────────────────────
 * ★ 为什么这个形状可以跨 8 语种统一（这是本判据能成门的**前提**）
 *
 *   第 12 增量踩过一条死路：语言包侧的位置信号只能是**键路径**，
 *   而键路径分不出「错误陈述」与「计数摘要」⇒ 裸词表假阳性极高 ⇒ L1 只能列人工清单。
 *
 *   ★ 本判据**不用词表、不用键路径、不用「散文长度」**——
 *   它只问一个**跨语种一致**的问题：
 *
 *       「这句用户可见的话里，出现了一个【裸的 snake_case 标识符】吗？」
 *
 *   为什么跨语种一致：★ **snake_case 在任何语言的正常散文里几乎都不出现**
 *   （中文、日文、英文、德文、法文…都是）⇒ 它本身就是「机器内部名」的形状标记
 *   ⇒ 判据不需要知道这句话是什么语言、是不是散文、有几个词。
 *   ⇒ ★ 这条与第 12 增量的教训完全一致：**规则在语种间必须一致，否则门就不是门。**
 *
 * ─────────────────────────────────────────────────────────────────────────────
 * ★ 判据（4 条，全部可证伪）
 *
 *   J1 **形状**：句子里出现 snake_case 标识符（`foo_bar`）或点分小写路径（`a.b_c`）。
 *   J2 **不是整串**：该标识符**不是**整个值（整串即标识符 = 界面上的标签/列名 ⇒ 不判）。
 *   J3 **不是「用户对照的代码」**：该出现处被 `<code>…</code>` / 反引号 / 引号包裹 ⇒ 不判
 *      （用户在逐字对照代码或字段名 ⇒ 那正是他要找的东西）。
 *   J4 **豁免簿**：该标识符登记在豁免簿里 ⇒ 不判；**未登记 ⇒ 判红**（漏网即红）。
 *
 *   ★ 豁免簿按**标识符**登记（不是按键、不是按语种）⇒ ★★ **8 语种共用一张表**
 *   ⇒ 「同一个标识符在中文里豁免、在英文里却红」这种不一致**在结构上不可能发生**。
 *
 *   ★ 豁免的四个类别（每条必须写清理由 + **证人**：界面上真的原样展示过它的地方）：
 *     `payload-field`    用户要照抄 / 填入 / 搜索 / 匹配的字段名（载荷位）
 *     `ui-visible-term`  界面上原样展示过的产品术语（状态值 / 列名 / 错误码）
 *     `external-service` 外部服务名、文件路径、协议名
 *     `db-standard-term` 数据库 / 协议的标准术语（该领域的人本来就认识）
 *
 *   ★ 反向自证（J5）：`payload-field` / `ui-visible-term` 两类豁免必须附**证人**
 *   （界面上真的把它展示出来的那个文件）⇒ 门会核对该文件里**仍然存在**该标识符
 *   ⇒ ★★ **豁免不能靠「文件改了」继续挂着** ⇒ 豁免过期必须重新裁决。
 */

import { allLocaleValues, ALL_LOCALES } from './localeValueQuality.ts'

/**
 * J1：snake_case 标识符（至少一个下划线）。
 *
 * ★★ **只认 snake_case，不认「点分小写路径」** —— 这是本轮实测出来的收窄：
 *   点分路径会把 `e.g.`（示例引导词）、`api.example.com`（URL 占位）、
 *   `mail.tm`（外部服务名）全部抓进来 ⇒ 实测 76 个标识符 / 726 条命中，
 *   其中绝大多数是假阳性 ⇒ **判据太宽 ⇒ 门会变成噪声源**。
 *   ★ 而点分路径里真正该管的那部分（`cmb.billing_mode`）**本来就含下划线段**
 *   ⇒ `billing_mode` 会被本正则抓到 ⇒ **点分规则是多余的**。
 *   ★★ 教训同第 12 增量：**判据宁可窄，不可宽** —— 窄了漏网可以靠豁免簿补，
 *   宽了假阳性会把门变成「没人看的东西」。
 */
export const JARGON_ID_RE = /\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b/g

/** 界面上的「引号」——CJK 直角引号、书名号、弯引号、书名号（各语种通用）。 */
const QUOTE_PAIRS: [string, string][] = [
  ['「', '」'],
  ['『', '』'],
  ['“', '”'],
  ['‘', '’'],
  ['«', '»'],
  ['„', '“'],
  ['‹', '›'],
]

/** J3：用户对照代码 / 字段名的包裹形态。 */
const CODE_WRAPPERS: [string, string][] = [
  ['<code>', '</code>'],
  ['<CODE>', '</CODE>'],
  ['`', '`'],
]

export type JargonExemptCategory =
  | 'payload-field'
  | 'ui-visible-term'
  | 'external-service'
  | 'db-standard-term'

export interface JargonExemption {
  identifier: string
  category: JargonExemptCategory
  reason: string
  /** 证人：界面上真的把它展示出来的文件（相对 web/）。ui-visible-term / payload-field 必填。 */
  witness?: string
}

export interface JargonFinding {
  locale: string
  key: string
  identifiers: string[]
  text: string
}

/**
 * ★★ 模板插值 `{...}` 内的标识符**不是文案**，是**与代码绑定的占位符名**
 * （★ `{session_load}` 对应组件里的 `session_load` 变量）
 * ⇒ 它必须逐字保留 ⇒ **判它红 = 要求人改一个占位符名 = 会直接改坏功能**。
 * ★ 与第 12 增量「『点名具体目标』只认引号内容 / 模板插值 / 箭头」同源：
 *   **模板插值是仓内已承认的「目标形态」**。
 */
function occurrenceIsInterpolation(text: string, start: number, end: number): boolean {
  const before = text.lastIndexOf('{', start - 1)
  const after = text.indexOf('}', end)
  return before !== -1 && after !== -1 && before < start && after >= end
}

/** 该次出现是否落在「用户对照的代码 / 引号内」形态。 */
function occurrenceIsQuoted(text: string, start: number, end: number): boolean {
  for (const [open, close] of CODE_WRAPPERS) {
    const before = text.lastIndexOf(open, start - 1)
    const after = text.indexOf(close, end)
    if (before !== -1 && after !== -1 && before < start && after >= end) return true
  }
  for (const [open, close] of QUOTE_PAIRS) {
    const before = text.lastIndexOf(open, start - 1)
    const after = text.indexOf(close, end)
    if (before !== -1 && after !== -1 && before < start && after >= end) return true
  }
  return false
}

/** 整串即该标识符（界面上的标签 / 列名）⇒ J2 不判。 */
function isWholeString(text: string, id: string): boolean {
  return text.trim() === id
}

/**
 * 判一条语言包值。
 * @param exempt 豁免标识符集合（来自登记簿）
 */
export function detectInternalJargon(
  key: string,
  text: string,
  locale: string,
  exempt: Set<string>,
): JargonFinding | null {
  if (!text) return null
  const hits = new Set<string>()
  for (const re of [JARGON_ID_RE]) {
    re.lastIndex = 0
    for (const m of text.matchAll(re)) {
      const id = m[0]
      if (isWholeString(text, id)) continue // J2
      if (occurrenceIsInterpolation(text, m.index, m.index + id.length)) continue // J3b 模板插值
      if (occurrenceIsQuoted(text, m.index, m.index + id.length)) continue // J3
      if (exempt.has(id)) continue // J4
      hits.add(id)
    }
  }
  if (!hits.size) return null
  return { locale, key, identifiers: [...hits], text }
}

/** 扫全语种。返回全部命中。 */
export function scanInternalJargon(
  exempt: Set<string>,
  locales: string[] = ALL_LOCALES,
): JargonFinding[] {
  const per = allLocaleValues()
  const out: JargonFinding[] = []
  for (const loc of locales) {
    const values = per.get(loc)
    if (!values) continue
    for (const [key, text] of Object.entries(values)) {
      const f = detectInternalJargon(key, text, loc, exempt)
      if (f) out.push(f)
    }
  }
  return out
}

/** 供门用的分组视图：按标识符聚合（决定「豁免哪个标识符」的依据）。 */
export function groupByIdentifier(findings: JargonFinding[]): Map<string, JargonFinding[]> {
  const m = new Map<string, JargonFinding[]>()
  for (const f of findings) {
    for (const id of f.identifiers) {
      const list = m.get(id) ?? []
      list.push(f)
      m.set(id, list)
    }
  }
  return m
}

// ─────────────────────────────────────────────────────────────────────────────
// 自证
// ─────────────────────────────────────────────────────────────────────────────

export interface JargonSelftestCase {
  id: string
  expect: 'red' | 'green'
  desc: string
  text: string
  exempt?: string[]
}

export const JARGON_SELFTEST: JargonSelftestCase[] = [
  { id: 'T1', expect: 'red', desc: '散文里夹裸 snake_case 表名', text: '✓ 已清除 {n} 行失败的 node_probe_state，路由候选立即恢复' },
  { id: 'T2', expect: 'green', desc: '整串即标识符（界面上的标签）', text: 'canonical_name' },
  { id: 'T3', expect: 'green', desc: '<code> 包住 ⇒ 用户在对照代码', text: '压缩模式分为 <code>0 (off)</code> 与 <code>1 (auto_threshold)</code>' },
  { id: 'T4', expect: 'green', desc: '引号包住 ⇒ 用户在对照字段名', text: '请在下方输入「canonical_name」' },
  { id: 'T4b', expect: 'red', desc: '★ 引号豁免是【按出现处】判的：同句里未加引号的兄弟标识符仍判红', text: '请在下方输入「canonical_name」（必须匹配 models_canonical）' },
  { id: 'T5', expect: 'green', desc: '已登记豁免 ⇒ 不判', text: '缺少 provider_id，指纹槽位没能加载', exempt: ['provider_id'] },
  { id: 'T6', expect: 'red', desc: '未登记的标识符在英文里同样判红（语种一致）', text: 'The session_turns table has not caught up yet' },
  { id: 'T7', expect: 'green', desc: '普通英文散文不判（英文里几乎不出现 snake_case）', text: 'The gateway accepted the request but has no records yet' },
  { id: 'T8', expect: 'red', desc: '点分小写路径也判', text: '路由适配时，cmb.billing_mode 将自动从此派生' },
  { id: 'T9', expect: 'green', desc: '中文散文无标识符 ⇒ 不判', text: '路由候选已重新可用' },
  { id: 'T10', expect: 'green', desc: '反引号包住 ⇒ 不判', text: '写入 `node_probe_state` 表' },
  { id: 'T11', expect: 'red', desc: '同一句里两个标识符都要报出来', text: '会话在请求日志产生 gw_session_id 后自动聚合显示' },
  { id: 'T12', expect: 'red', desc: '豁免只对被登记的那个那个标识符生效（登记别的不会顺带豁免它）', text: '缺少 provider_id', exempt: ['other_id'] },
  { id: 'T13', expect: 'green', desc: '数字 / 大写缩写不算标识符（不误判）', text: '重试 3 次后返回 429' },
  { id: 'T14', expect: 'green', desc: '缩写型大写词不算（API Key / RPM）', text: '当前租户下还没有可用的 API Key' },
  { id: 'T15', expect: 'green', desc: '★ 模板插值内的标识符是占位符名，不是文案（改了会改坏功能）', text: '（会话负载 × {session_load}）+（错误次数 × {failure_penalty}）' },
  { id: 'T16', expect: 'red', desc: '★ 但插值【外面】的同名字段仍然判红', text: '路由适配时，session_load 将自动从此派生' },
]

export function runJargonSelftest(): { passed: number; total: number; failed: string[] } {
  const failed: string[] = []
  let passed = 0
  for (const c of JARGON_SELFTEST) {
    const exempt = new Set(c.exempt ?? [])
    const f = detectInternalJargon('selftest', c.text, 'zh-CN', exempt)
    const got = f ? 'red' : 'green'
    if (got === c.expect) passed++
    else failed.push(`${c.id}（${c.desc}）：期望 ${c.expect}，实际 ${got}`)
  }
  return { passed, total: JARGON_SELFTEST.length, failed }
}
