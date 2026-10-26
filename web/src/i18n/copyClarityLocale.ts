// copyClarityLocale.ts — 语言包侧的「清晰文案」判据 · 2026-10-27 第 12 增量
//
// ★ 为什么单独一个文件（不并进 copyClarity.ts）：
//   源码侧判据扫的是**界面层源码**，语言包侧扫的是**语言包的值**。
//   两者的「位置信号」完全不同：
//     源码侧 = 语句上下文（赋值目标 / 报错调用 / 模板位置）
//     语言包侧 = **键路径**（`*.empty` 是空状态位、`common.terms.*` 是术语位）
//   ⇒ 语种词表又大（8 语种 × 3 类）⇒ 分文件更清楚，判据也更不容易互相污染。
//
// ★ 本轮只落地两条判据（**L2 / L3**），L1（错误三问）**不设硬闸**：
//   L1 的位置信号在语言包里是键路径，但键路径**不区分「错误陈述」与「计数摘要」**
//   （实测 `providerDetail.models.refreshSummaryFailed = "失败 {n}"` 是计数，不是错误陈述；
//    `dashboard.discovery.failuresTitle = "模型发现：最近 6h 测试失败"` 是统计标题）
//   ⇒ 裸词表判据假阳性率高 ⇒ 本轮把 L1 的候选列成**人工清单**（见审计档），下一轮治。
//   ⇒ ★★ 这是「门看不见的放人工清单」这条仓内纪律的应用（与第 9 增量对接口层中文同款）。
//
// ★ 跨语种一致的关键决定：**「点名目标」只认引号内容 / 模板插值 / 箭头**，
//   **不把裸拉丁词算作点名**。
//   ⇒ 否则拉丁语种天然不判红（实测：en-US 只报 3 处且 2 处是假阳性，fr-FR 报 0 处）
//   ⇒ 而中文里「点击「保存」生效」是合规的（引号）⇒ 规则在语种间不一致。
//   ⇒ 统一后各语种用同一把尺子：英文的 "Click to copy" 与中文的「点击复制」同样判红。
//
// ★ 语种词表的两处硬伤（已实测并处理）：
//   ① 拉丁语种的词表会误吃复合词：`Compress` 里的 `press`、`one-click` 里的 `click`
//      ⇒ 用 `(?<![\w-])…(?![\w-])` 的**连字符也算边界**的负向断言；
//   ② 日语的 `ワンクリック`（＝一键激活）是**复合词**，不是手势 ⇒ 用 `(?<!ワン)` 排除。

import { allLocaleValues, ALL_LOCALES, LOCALES_DIR } from './localeValueQuality.ts'

export type LocaleRuleId = 'L2-gesture' | 'L3-generic-empty'

export interface LocaleClarityFinding {
  locale: string
  key: string
  rule: LocaleRuleId
  reason: string
  text: string
}

/**
 * 各语种的「手势词」词表。
 *
 * ★ 命中后要检查**后面有没有点名具体目标**：
 *   引号内容（`「」『』""«»`）/ 模板插值（`${…}`）/ 箭头（`→` `>`）
 */
export const LOCALE_GESTURE: Record<string, RegExp> = {
  // ★ 单字「点」也要算手势（实测 `liveTopology.panel.zoomHint` = 「点 ⤢ 新页查看详情」
  //   被漏报）⇒ 但必须**带边界**，否则会误伤「节点」「点击率」「点评」等含「点」的词
  //   ⇒ 用后顾 `(?=[\s:：→，,。])` 限定「点」后面必须是空白/标点。
  //   ★★ 「点击」还要排除**度量后缀**（率/数/量/次）——「点击率」是指标名，不是手势
  //     （实测仓内暂无此形态，但图表标题随时可能出现 ⇒ 判据先立好）。
  //   ★★ 单字「点」必须**前后都有边界**才算手势：
  //     只加后顾会误伤「**节点**。」「**节点**，」这类（实测 12 个键受影响：
  //     `requestJourneys.detail.node` = 「节点 {node}」、`proxy.error.protocolNotDialable`
  //     = 「…SOCKS5 节点，trojan…」…）⇒ 前顾 `(?<![一-鿿])` 把「节点」的「点」排除。
  'zh-CN': /点击(?!率|数|量|次)|點擊(?!率|數|量|次)|点这里|點這裡|点一下|双击|雙擊|(?<![一-鿿])点(?=[\s:：→，,。])/g,
  'zh-TW': /點擊(?!率|數|量|次)|點這裡|點一下|雙擊|(?<![一-鿿])點(?=[\s:：→，,。])/g,
  // ★ `ワンクリック`（＝一键激活）是复合词，不是手势 ⇒ 负向后顾排除。
  'ja-JP': /(?<!ワン)クリック|タップ|押して/g,
  // ★ 连字符也算词边界 ⇒ `one-click` / `click-through` 不命中；`Compress` 不命中。
  // ★★ `press` **不在词表里**：英文里 `press` 指**键盘**（"Press Enter to add"），
  //   不是指针手势 ⇒ 判它红是假阳性（实测 `workTypes.detail.intentRecogHint`
  //   的 "Press Enter or comma to add." 被误判）。
  //   ⇒ 指针手势只认 `click` / `tap`。
  'en-US': /(?<![\w-])(click|clicks|clicked|clicking|tap|taps|tapped|tapping)(?![\w-])/gi,
  'de-DE': /(?<![\w-])(klicken|klickst|klickt|anklicken|anklickst|tippen|tippt|antippen)(?![\w-])/gi,
  // ★ 拉丁语种一律加**词边界**（`(?<![\w-])…(?![\w-])`）：
  //   不加就会匹配更长词的前缀（实测 `toucher` 命中「touchera」⇒ 法语误报；
  //   `pulsar` 命中「pulsaron」同理）⇒ de/en 早已加，es/fr 本轮补上。
  'es-ES': /haz clic|haga clic|(?<![\w-])(pulsa|pulse|pulsar|pulsado)(?![\w-])/gi,
  'fr-FR': /(?<![\w-])(cliquez|clique|cliquer|touchez|toucher|tapotez|tapoter)(?![\w-])/gi,
  'ar-SA': /انقر على|انقر|اضغط على|اضغط/g,
}

/**
 * 各语种的「通用空状态短语」表。
 * ★ 整句**完全等于**表内短语才算命中（不是「包含」）—— 与源码侧 R3 同一口径。
 */
export const LOCALE_GENERIC_EMPTY: Record<string, Set<string>> = {
  'zh-CN': new Set(['暂无数据', '暂无', '无数据', '没有数据', '无记录', '没有记录', '暂无记录', '暂无信息', '暂无内容', '暂无结果', '无结果', '空空如也']),
  'zh-TW': new Set(['暫無資料', '暫無', '無資料', '沒有資料', '無記錄', '沒有記錄', '尚無紀錄', '暫無記錄', '暫無資訊', '暫無內容', '暫無結果', '無結果', '空空如也']),
  'ja-JP': new Set(['なし', 'データなし', '記録なし', '該当なし', 'ありません', '情報なし', 'データがありません', '記録がありません', 'レコードがありません', '結果なし']),
  'en-US': new Set(['No data', 'None', 'Nothing', 'No records', 'No results', 'Empty', 'No items', 'No data available', 'No data yet']),
  'de-DE': new Set(['Keine Daten', 'Keine Datensätze', 'Keine Ergebnisse', 'Keine Treffer', 'Nichts', 'Keine Einträge', 'Keine Daten verfügbar']),
  'es-ES': new Set(['Sin datos', 'Ninguno', 'Sin resultados', 'Nada', 'Sin registros', 'No hay datos disponibles']),
  'fr-FR': new Set(['Aucune donnée', 'Aucun résultat', 'Aucune correspondance', 'Rien', 'Aucun enregistrement', 'Aucune donnée disponible']),
  'ar-SA': new Set(['لا توجد بيانات', 'لا يوجد', 'لا نتائج', 'لا شيء', 'لا سجلات', 'لا بيانات', 'لا توجد سجلات']),
}

/**
 * 空状态位的键名形态（键路径的**最后一段**）。
 */
/** ★ 空状态位：**末段以空状态词开头**（`empty` / `emptyHint` / `emptyTitle` / `noData` / `noDataText` …）
 *  ★ 只认「开头」不认「整段相等」—— 否则 `emptyHint` / `emptyTitle` / `noResults` 这一族
 *  ★ （同样是空状态位，却带后缀）会整族落在门的视野之外（实测 47 个键）。
 */
export const EMPTY_SLOT_RE = /^(empty|noData|noRecord|noResult|noItems|nothing)/i

/**
 * 术语位：**术语表**里的条目不是空状态文案（「无数据」作为术语是合法词条）。
 * ★ 这是**位置规则**，不是白名单 ⇒ 零豁免。
 */
export const TERM_SLOT_RE = /(^|\.)(terms?|terminology)(\.|$)/i

/** 点名目标：引号内容 / 模板插值 / 箭头指向。 */
function namesTarget(after: string): boolean {
  // ★ 前导剥离**不能吃掉箭头**：`→` / `>` 是「指向」信号，不是标点。
  //   （第一版把 `→` 放进剥离字符类 ⇒ 「点击错误类型 → 下钻」被误判红 ⇒ 自证 L18 抓到。）
  const next = after.replace(/^[\s:：＞\-—、,，.。]+/, '')
  if (/^[「『“"'«»]/.test(next)) return true
  if (next.startsWith('${')) return true
  if (/^[→>]/.test(next)) return true
  return false
}

/** L2：手势词后面没点名具体目标。 */
export function ruleLocaleGesture(text: string, locale: string): LocaleClarityFinding | null {
  const re = LOCALE_GESTURE[locale]
  if (!re) return null
  re.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    const after = text.slice(m.index + m[0].length)
    if (!namesTarget(after)) {
      return {
        locale,
        key: '',
        rule: 'L2-gesture',
        reason: `手势词「${m[0]}」后面没点名具体目标`,
        text,
      }
    }
  }
  return null
}

/** L3：空状态位的值整句等于通用空状态短语。 */
export function ruleLocaleEmpty(key: string, text: string, locale: string): LocaleClarityFinding | null {
  const last = key.split('.').pop() ?? ''
  if (!EMPTY_SLOT_RE.test(last)) return null
  if (TERM_SLOT_RE.test(key)) return null
  const set = LOCALE_GENERIC_EMPTY[locale]
  if (!set) return null
  const t = text.replace(/\s+/g, ' ').trim()
  if (!set.has(t)) return null
  return {
    locale,
    key,
    rule: 'L3-generic-empty',
    reason: '通用空状态短语（没点名缺的是什么）',
    text,
  }
}

/**
 * 核心检测：扫全部 8 个语种的语言包值。
 *
 * ★ `allLocaleValues()` 返回的是 **Map**（不是普通对象）——
 *   第一版按普通对象取 `per[locale]` ⇒ 全部 undefined ⇒ **全仓 0 命中（假绿）**。
 *   ⇒ 这就是「0 命中有两种读法」的又一例：**扫到了没发现 vs 根本没扫到**。
 */
export function detectLocaleClarity(
  values?: Map<string, Record<string, string>> | Record<string, Record<string, string>>
): LocaleClarityFinding[] {
  const per: Map<string, Record<string, string>> =
    values instanceof Map ? values : new Map(Object.entries(values ?? {}))
  const table0 = per.size > 0 ? per : allLocaleValues()
  const out: LocaleClarityFinding[] = []
  for (const locale of ALL_LOCALES) {
    const table = table0.get(locale)
    if (!table) continue
    for (const [key, text] of Object.entries(table)) {
      const g = ruleLocaleGesture(text, locale)
      if (g) out.push({ ...g, key })
      const e = ruleLocaleEmpty(key, text, locale)
      if (e) out.push(e)
    }
  }
  return out
}

// ───────────────────────── 自证 ─────────────────────────
//
// ★ 必测的形态（每一条都对应一个**已实测会打偏量具**的样本）：
//   · 复合词里的手势词不是手势（`Compress` / `one-click` / `ワンクリック`）
//   · 点名了目标就合规（引号 / 模板插值 / 箭头）
//   · 术语位不是空状态位（`common.terms.noData` = 「无数据」是合法词条）
//   · 非空状态位的通用短语不是空状态（`routing.shared.noData` 在别的段里）
//   · 「包含」通用短语但整句不等 ⇒ 不命中

export interface LocaleSelftestCase {
  id: string
  desc: string
  locale: string
  key: string
  value: string
  expect: LocaleRuleId | null
}

export const LOCALE_SELFTEST: LocaleSelftestCase[] = [
  {
    id: 'L1',
    desc: '中文手势词后面没点名目标 → 必须判红（L2）',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '点击复制',
    expect: 'L2-gesture',
  },
  {
    id: 'L2',
    desc: '引号里点名了目标 → 必须判绿',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '点击「保存」生效',
    expect: null,
  },
  {
    id: 'L3',
    desc: '英文手势词同样判红（跨语种同一把尺子）',
    locale: 'en-US',
    key: 'x.hint',
    value: 'Click to copy',
    expect: 'L2-gesture',
  },
  {
    id: 'L4',
    desc: '★ 复合词里的手势词不是手势：`Compress` 里的 press → 必须判绿',
    locale: 'en-US',
    key: 'x.col',
    value: 'Compress',
    expect: null,
  },
  {
    id: 'L5',
    desc: '★ 连字符复合词 `one-click` → 必须判绿',
    locale: 'en-US',
    key: 'x.hint',
    value: 'One-click activation',
    expect: null,
  },
  {
    id: 'L6',
    desc: '★ 日语 `ワンクリック`（＝一键激活）不是手势 → 必须判绿',
    locale: 'ja-JP',
    key: 'x.hint',
    value: 'ワンクリック有効化',
    expect: null,
  },
  {
    id: 'L7',
    desc: '日语普通「クリック」→ 必须判红',
    locale: 'ja-JP',
    key: 'x.hint',
    value: 'クリックして表示',
    expect: 'L2-gesture',
  },
  {
    id: 'L8',
    desc: '★ 术语位（common.terms.noData = 无数据）是合法词条 → 必须判绿',
    locale: 'zh-CN',
    key: 'common.terms.noData',
    value: '无数据',
    expect: null,
  },
  {
    id: 'L9',
    desc: '空状态位 + 通用短语 → 必须判红（L3）',
    locale: 'zh-CN',
    key: 'dashboard.board.emptyNoResultProviders',
    value: '暂无数据',
    expect: 'L3-generic-empty',
  },
  {
    id: 'L10',
    desc: '★ 只是「包含」通用短语、整句不等 → 不判红',
    locale: 'zh-CN',
    key: 'x.empty',
    value: '暂无数据，请先配置再试',
    expect: null,
  },
  {
    id: 'L11',
    desc: '★ 非空状态位的通用短语 → 不判红（位置驱动，不是内容驱动）',
    locale: 'en-US',
    key: 'routing.shared.something',
    value: 'No data',
    expect: null,
  },
  {
    id: 'L12',
    desc: '德语手势词 anklicken（带前缀的完整形态）→ 必须判红',
    locale: 'de-DE',
    key: 'x.hint',
    value: 'Zeile anklicken',
    expect: 'L2-gesture',
  },
  {
    id: 'L13',
    desc: '德语 klicken 不应被 anklicken 的前缀规则误放行 → 仍判红',
    locale: 'de-DE',
    key: 'x.hint',
    value: 'Zum Kopieren klicken',
    expect: 'L2-gesture',
  },
  {
    id: 'L14',
    desc: '法语手势词 → 必须判红',
    locale: 'fr-FR',
    key: 'x.hint',
    value: 'Cliquez pour copier',
    expect: 'L2-gesture',
  },
  {
    id: 'L15',
    desc: '阿拉伯语手势词 → 必须判红',
    locale: 'ar-SA',
    key: 'x.hint',
    value: 'انقر للنسخ',
    expect: 'L2-gesture',
  },
  {
    id: 'L16',
    desc: '西班牙语手势词 → 必须判红',
    locale: 'es-ES',
    key: 'x.hint',
    value: 'Pulsa un tipo de error',
    expect: 'L2-gesture',
  },
  {
    id: 'L17',
    desc: '模板插值算点名目标 → 必须判绿',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '点击 ${name} 查看详情',
    expect: null,
  },
  {
    id: 'L18',
    desc: '★ 箭头**紧跟**手势词时算点名目标 → 必须判绿',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '点击 → 下钻',
    expect: null,
  },
  {
    id: 'L25',
    desc: '★ 法语词边界：`toucher` 不该命中「touchera」→ 必须判绿',
    locale: 'fr-FR',
    key: 'x.hint',
    value: "Désactivé manuellement, l'auto-probe ne le touchera pas.",
    expect: null,
  },
  {
    id: 'L26',
    desc: '★ 西语词边界：`pulsar` 不该命中「pulsaron」→ 必须判绿',
    locale: 'es-ES',
    key: 'x.hint',
    value: 'Los nodos pulsaron correctamente',
    expect: null,
  },
  {
    id: 'L24',
    desc: '★ 「节点」的「点」不是手势（只加后顾会误伤）→ 必须判绿',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '仅支持创建 HTTP/HTTPS 节点，trojan 需通过本地网桥暴露',
    expect: null,
  },
  {
    id: 'L22',
    desc: '★ 中文单字「点」也是手势（实测漏报）→ 必须判红',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '点 ⤢ 新页查看详情',
    expect: 'L2-gesture',
  },
  {
    id: 'L23',
    desc: '★ 含「点」的普通词不是手势（「节点」「点击率」）→ 必须判绿',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '按节点聚合，点击率最高的前 10 个',
    expect: null,
  },
  {
    id: 'L20',
    desc: '★ 键盘操作 `Press Enter` 不是指针手势 → 必须判绿',
    locale: 'en-US',
    key: 'x.hint',
    value: 'Type a keyword and press Enter or comma to add…',
    expect: null,
  },
  {
    id: 'L21',
    desc: '★ 指针手势 `tap` 仍然判红',
    locale: 'en-US',
    key: 'x.hint',
    value: 'Tap to expand',
    expect: 'L2-gesture',
  },
  {
    id: 'L19',
    desc: '★ 箭头在句中**不救场**（「点击错误类型 → 下钻」的手势词后没点名）→ 必须判红',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '点击错误类型 → 下钻',
    expect: 'L2-gesture',
  },
  {
    id: 'L27',
    desc: '★ 空状态位**带后缀**也算空状态位（`emptyHint` 末段以 empty 开头）→ 必须判红',
    locale: 'zh-CN',
    key: 'sessions.list.emptyHint',
    value: '暂无',
    expect: 'L3-generic-empty',
  },
  {
    id: 'L28',
    desc: '★ `noData` 前缀族同理（`noDataText`）→ 必须判红',
    locale: 'en-US',
    key: 'workTypes.overview.noDataText',
    value: 'No data',
    expect: 'L3-generic-empty',
  },
  {
    id: 'L29',
    desc: '★ 扩面后**非空状态位**仍不受 L3 约束（同样一句「暂无」放在普通提示位 → 判绿）',
    locale: 'zh-CN',
    key: 'x.hint',
    value: '暂无',
    expect: null,
  },
]

/** 自证执行器：逐条跑判据，比对期望。 */
export function runLocaleSelftest(): { passed: number; total: number; failed: string[] } {
  const failed: string[] = []
  let passed = 0
  for (const c of LOCALE_SELFTEST) {
    const g = ruleLocaleGesture(c.value, c.locale)
    const e = ruleLocaleEmpty(c.key, c.value, c.locale)
    const got = (g ?? e)?.rule ?? null
    if (got === c.expect) passed++
    else failed.push(`${c.id}（${c.desc}）期望 ${c.expect ?? '绿'} 实得 ${got ?? '绿'}`)
  }
  return { passed, total: LOCALE_SELFTEST.length, failed }
}
