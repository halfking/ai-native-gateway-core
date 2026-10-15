import { describe, it, expect } from 'vitest'
import { zhCN } from './zh-CN'
import { enUS } from './en-US'
import { MATRIX_ROWS, MATRIX_METRICS } from '@/api/autoRouteMatrix'
import { ANALYTICS_WINDOWS, TUNING_STATUSES, TUNING_CATEGORIES } from '@/api/autoRouteInsights'
import { ROUTING_OPT_METRICS_DIMS } from '@/api/routingOpt'
import { PENDING_AGE_BANDS } from '@/api/pendingResponses'
import { ANOMALY_TRIGGERS } from '@/api/requestAnomalies'
import { INJECTION_CATEGORIES } from '@/api/promptInjection'
import { ANALYTICS_CLIENT_ORDER_BYS } from '@/api/sessionAnalytics'
import {
  MAAS_RATE_DIMS,
  MAAS_EFFECTIVE_SOURCES,
  MAAS_PUBLIC_DIMS,
  MAAS_ORDER_TYPES,
  MAAS_ORDER_STATUSES,
  MAAS_PAYMENT_CHANNELS,
} from '@/api/maas'
import {
  COMPLIANCE_ISSUE_TYPES,
  COMPLIANCE_QUEUE_STATUSES,
  COMPLIANCE_THRESHOLD_FIELDS,
  COMPLIANCE_FEEDBACK_TYPES,
} from '@/api/outputCompliance'
// —— 第一百四十批新增：26 处拼接前缀的取值来源（见下方 DYNAMIC_KEYS ② 区）
import { APPROVAL_STATUSES } from '@/api/approvals'
import { INJECTION_SEVERITY_LEVELS } from '@/api/promptInjection'
import { JOURNEY_LIFECYCLE_STATES } from '@/api/requestJourney'
import { PROBE_TASK_STATUSES, PROBE_TASK_OUTCOMES } from '@/api/probeTriStateTasks'
import { SELF_CHECK_RUN_STATUSES } from '@/api/selfCheck'
import { REQUEST_DETAIL_BODY_STATUSES, REQUEST_DETAIL_BODY_STATUS_UNKNOWN } from '@/api/requestDetail'
import { sourceBlob, concatPrefixes, prefixHits, isRealPrefix, flattenDict, PREFIX_FORM_NAMES } from './keyScan'

/**
 * 动态 i18n 键的落地校验（2026-10-07）。
 *
 * verify-i18n-parity.mjs 的第 4 条判据能抽 `t('a.b.c')` 这种**字面量**键，
 * 但抽不出 `t('matrix.row.' + r)` 这种**拼接**键 —— 它会把这 7 处单列成
 * 「本门未覆盖，需人工确认」。
 *
 * ★ 「测不到」单列出来只是第一步；把它变成「测得到」才是收口。
 *   这个文件把每一处动态前缀与它在代码里的**取值来源**（allowlist 常量）
 *   拼起来，跑遍两侧词典 ⇒ 少一个键立刻红。
 *
 * ★ 同时做 en-US 侧的检查：缺 en 键时 i18n 回退成 zh（`t` 的实现），
 *   英文用户看到的是**中文**而不是裸键 —— 这是 §11.20 记过的
 *   「两侧插到不同词典」那一类，必须两侧都断。
 */

type Dict = Record<string, unknown>

function lookup(dict: Dict, path: string): string | undefined {
  let node: unknown = dict
  for (const seg of path.split('.')) {
    if (node && typeof node === 'object' && seg in (node as Record<string, unknown>)) {
      node = (node as Record<string, unknown>)[seg]
    } else {
      return undefined
    }
  }
  return typeof node === 'string' ? node : undefined
}

const zh = zhCN as unknown as Dict
const en = enUS as unknown as Dict

/**
 * 每处动态前缀 → 它在代码里真实会被拼上的后缀。
 * 后缀**必须**从代码实际用的常量取，不是手抄 —— 手抄的那份会漂。
 */
const DYNAMIC_KEYS: Array<[prefix: string, suffixes: readonly string[]]> = [
  ['funnel.window', ANALYTICS_WINDOWS],
  ['matrix.metricName.', MATRIX_METRICS.filter((m) => m !== '')],
  ['matrix.row.', MATRIX_ROWS.filter((r) => r !== '')],
  ['proposals.status.', TUNING_STATUSES.filter((s) => s !== '')],
  ['proposals.category.', TUNING_CATEGORIES.filter((c) => c !== '')],
  // ★ metrics 的四类行（后端 GROUPING SETS），键前缀 dim_
  ['routingOpt.dim_', ROUTING_OPT_METRICS_DIMS],
  // ★ 待处理响应的四个年龄段（边界写死在 api/pendingResponses.ts）
  ['pending.band_', PENDING_AGE_BANDS],
  // ★ 请求侧异常的三种类型（取值来自 ANOMALY_TRIGGERS 常量）
  ['anomalies.trigger_', ANOMALY_TRIGGERS],
  // ★ 输出合规的命中类别、复核状态、阈值字段
  ['compliance.issue_', COMPLIANCE_ISSUE_TYPES],
  ['compliance.qstatus_', COMPLIANCE_QUEUE_STATUSES],
  // ★ 复核结论的三个类型（取值来自 COMPLIANCE_FEEDBACK_TYPES 常量）
  ['compliance.ftype_', COMPLIANCE_FEEDBACK_TYPES],
  // 阈值键是 field.replace('_threshold','') ⇒ 后缀要现算，不手抄
  ['compliancePolicy.th_', COMPLIANCE_THRESHOLD_FIELDS.map((f) => f.replace('_threshold', ''))],
  // ★ 注入的 15 个攻击类别（取值来自 INJECTION_CATEGORIES 常量）
  ['injection.cat_', INJECTION_CATEGORIES],
  // ★ 会话分析的三个排序值（取值来自 ANALYTICS_CLIENT_ORDER_BYS 常量）
  ['sa.order_', ANALYTICS_CLIENT_ORDER_BYS],
  // ★ MaaS 积分价（superAdmin 档）的七维与三个生效来源
  ['maas.dim_', MAAS_RATE_DIMS.map((d) => d.key)],
  ['maas.src_', MAAS_EFFECTIVE_SOURCES],
  // ★★ MaaS 订单（superAdmin 档）的三个枚举 —— 后端 Go 侧各是一份枚举
  ['mo.type_', MAAS_ORDER_TYPES],
  ['mo.status_', MAAS_ORDER_STATUSES],
  ['mo.channel_', MAAS_PAYMENT_CHANNELS],
  // ★★★ MaaS 租户面（**admin 档**）**只有 4 维** ——
  //   登记它才能让「多画一维」这条判据在 i18n 侧也有一道网。
  ['mp.dim_', MAAS_PUBLIC_DIMS],

  // ══ ② 第一百四十批新增 26 处 ══════════════════════════════════════════
  // ★ 这 26 处在本批之前**从未登记** ⇒ 它们拼出来的键，词典里删一个都没人发现
  //   （`verify-i18n-parity.mjs` 第 4 条只抽**字面量**键，抽不到拼接出来的）。
  //   每条后面的注释写明**取值来源**，可从代码里对回去。
  ['approvalQueue.status.', APPROVAL_STATUSES], // api/approvals.ts:104
  ['autoRoute.accuracy.granularity.', ['5m', 'daily']], // autoRouteRead.ts:488 的返回联合
  ['autoRoute.audit.block.', ['task_distribution', 'profile_distribution', 'top_chosen_models']], // AutoRouteView.vue:279 内联
  ['autoRoute.outcome.', ['live', 'no_rows', 'stale', 'absent', 'query_failed', 'unknown']], // AutoRouteView.vue:162 词表 + 兜底
  ['cache.state.', ['healthy', 'healthy_confirmed', 'available', 'suspicious', 'probing', 'failing', 'broken_confirmed', 'unavailable', 'unknown']], // probeTimelineCache.ts:292 词表 + 兜底
  ['compressionView.band.', ['below', 'preliminary', 'forced']], // CompressionView.vue:206
  ['compressionView.granularity.', ['hour', '6h', 'day']], // compression.ts:287 seriesGranularityOf 三个分支
  ['dataFlow.segment.', ['hot_data', 'warm_data', 'cold_data', 'expired_data']], // dataLifecycleStats.ts:138 SEGMENT_KEYS（**不是** DATA_SEGMENT_KEYS —— 那个是度量名）
  // ★ 后端驱动的**开放集**（见 OPEN_PREFIXES）：只登记已知值。
  //   已知值本身是从词典里读出来的，不是从后端枚举抄的 —— 后端加一个状态，这儿不会红。
  ['heatmap.state_', ['healthy_confirmed', 'broken_confirmed', 'suspicious', 'probing', 'unknown', 'unprobed']],
  ['dh.state.', ['live', 'stale', 'unknown', 'unexpected-shape']], // DataHorizonView.vue:65 state computed
  ['dh.state.note.', ['live', 'stale', 'unknown', 'unexpected-shape']], // 同上
  ['injectionConfig.level_', INJECTION_SEVERITY_LEVELS], // promptInjection.ts:109
  // ★ `journey.` 的后缀**不是**生命周期原值：视图把 `in_flight` 映射成 `inFlight`
  ['journey.', JOURNEY_LIFECYCLE_STATES.map((s) => (s === 'in_flight' ? 'inFlight' : s))], // RequestJourneyView.vue:150
  ['login.siteEnv.', ['production', 'staging', 'internal']], // gatewaySites.ts:26 Site.env
  ['miq.err.', ['invalid-provider', 'invalid-canonical', 'credential-required', 'model-required']], // modelIQ.ts:644 的**四个分支**（不含 'unknown' —— 视图先判掉）
  ['pq.leg.', PROBE_TASK_STATUSES], // probeTriStateTasks.ts:89
  ['pq.outcomeDetail.', PROBE_TASK_OUTCOMES], // probeTriStateTasks.ts:93
  ['probeModel.health.', ['critical', 'warning', 'degraded', 'healthy', 'unknown']], // probeModelHealth.ts:152 词表 + 兜底
  ['providers.tab.', ['overview', 'credentials', 'models']], // ProviderDetailSheet.vue:145 内联 as const
  ['rd.b_', ['request_body', 'response_body', 'outbound_body']], // RequestDetailView.vue:316 内联 as const
  ['rd.bstatus_', [...REQUEST_DETAIL_BODY_STATUSES, REQUEST_DETAIL_BODY_STATUS_UNKNOWN]], // requestDetail.ts:93/97（**开放集**，见 OPEN_PREFIXES）
  ['sa.dim_', ['clients', 'tasks']], // SessionAnalyticsView.vue:56 视图内 type Dimension
  // ★★★ `sc.outcome.` 的联合里有 `'tested'`，但 selfCheckUpstreamOutcome **从不返回它**
  //   （selfCheck.ts:818 三个分支都没有）⇒ 词典里**有意不给**这个键，
  //   zh-CN.ts / en-US.ts 里都写了注释说明。这里登记的也只是实际分支。
  ['sc.outcome.', ['not_tested', 'result_empty', 'result_present']],
  ['sc.status.', SELF_CHECK_RUN_STATUSES], // SelfCheckView.vue:91 RUN_STATUS_FILTERS 的非空部分
  ['ue.field.', ['dollars_saved', 'compression_saved', 'total_saved']], // UsageEnhancedView.vue:139
  ['usage.dim.', ['model', 'provider', 'key', 'application', 'tenant']], // UsageView.vue:71 COST_DIMS
  // ── ② 区结束 ──────────────────────────────────────────────────────────
]

/**
 * ★ 后缀来自**后端自由字符串**的前缀 —— 它们是**开放集**，
 *   登记只能覆盖「已知值」，不能证明「后端不会发别的」。
 *   列出来是为了让这道门的**边界**写在代码里，而不是让人以为它已经闭合。
 *   （本批实测：`heatmap.state_` 的 6 个已知值就是从词典里抄的，
 *     若后端新增一个状态 ⇒ 用户看到裸键，而**没有任何门会红**。）
 */
const OPEN_PREFIXES: readonly string[] = [
  'heatmap.state_', // CredentialHeatmapView.vue:123 t(`heatmap.state_${s}`)，s = m.node_status.state
  'rd.bstatus_', // requestDetail.ts:208 bodyStatusOf 直接回传后端原串
]

/**
 * ★ 生成键总数（本批钉死）。改了 DYNAMIC_KEYS 的后缀集就会动它 ⇒ ⑥-6 会红。
 * ⚠️ 红了不许直接改数字：先重量一遍，确认是真的增减。
 */
const GENERATED_KEY_TOTAL = 185 // 本批实测：46 个前缀 × 各自后缀数

describe('动态 i18n 键在两侧词典里都存在', () => {
  for (const [prefix, suffixes] of DYNAMIC_KEYS) {
    for (const suffix of suffixes) {
      const key = prefix + suffix
      it(`zh-CN: ${key}`, () => {
        const v = lookup(zh, key)
        expect(v, `zh-CN 缺 ${key}`).toBeTypeOf('string')
        // 值 === 键名 ⇒ 运行时显示裸键
        expect(v).not.toBe(key.split('.').pop())
      })
      it(`en-US: ${key}`, () => {
        const v = lookup(en, key)
        expect(v, `en-US 缺 ${key}（英文用户会看到中文回退）`).toBeTypeOf('string')
        expect(v).not.toBe(key.split('.').pop())
      })
    }
  }
})

describe('★★★ 第六组：拼接前缀的**覆盖**门（本批新增）', () => {
  const { blob } = sourceBlob()
  const KEY_LIST: string[] = flattenDict(zh)
  const REAL = [...concatPrefixes(blob)].filter((p) => isRealPrefix(p, KEY_LIST)).sort()
  const REGISTERED = new Set(DYNAMIC_KEYS.map(([p]) => p))

  it('⑥-1 ★★★★★ 源码里每一个拼接前缀都必须登记在 DYNAMIC_KEYS 里', () => {
    const missing = REAL.filter((p) => !REGISTERED.has(p))
    expect(
      missing,
      '这些前缀在代码里被拼接使用，但没登记 ⇒ 它们生成的键删掉了没有任何门会发现' +
        '（verify-i18n-parity 第 4 条只抽字面量键）：\n  ' +
        missing.join('\n  '),
    ).toEqual([])
  })

  it('⑥-2 ★★★★ 三种拼接形态各至少抓到 1 个前缀（删掉任一正则会先红）', () => {
    const hits = prefixHits(blob)
    console.log(`[⑥-2] 拼接形态命中：${JSON.stringify(PREFIX_FORM_NAMES.map((n, i) => [n, hits[i]]))}`)
    for (let i = 0; i < hits.length; i++) {
      expect(hits[i], `${PREFIX_FORM_NAMES[i]} 一个都没抓到 ⇒ 覆盖面退化了`).toBeGreaterThan(0)
    }
  })

  it('⑥-3 ★★★ 开放集前缀必须显式列在 OPEN_PREFIXES 里（不许悄悄当成闭合的）', () => {
    // ★ 后缀来自后端自由字符串的前缀，登记只能覆盖「已知值」。
    //   若有人把某个开放集前缀当成闭合的，删一条就不再有任何提示。
    expect([...OPEN_PREFIXES].sort()).toEqual(['heatmap.state_', 'rd.bstatus_'])
  })

  it('⑥-4 ★★★ DYNAMIC_KEYS 里不得有重复前缀', () => {
    const ps = DYNAMIC_KEYS.map(([p]) => p)
    expect(ps.filter((p, i) => ps.indexOf(p) !== i)).toEqual([])
  })

  it('⑥-5 ★★ 至少覆盖 45 处动态前缀（少于这个数说明下面的清单没跟上代码）', () => {
    expect(DYNAMIC_KEYS.length).toBeGreaterThanOrEqual(45)
  })

  it('⑥-6 ★★★★★ 生成的动态键**总数**钉死（后缀集被改窄必须看得见）', () => {
    // ★★★ 批 140 变异 K11 实测出的洞：**用例是从 DYNAMIC_KEYS 的后缀集生成的**，
    //   所以把某个后缀从清单里删掉 ⇒ 那条用例根本不存在 ⇒ 全绿。
    //   这与批 139 的 J11 同族：**一份数据同时决定「测什么」与「覆盖多少」时，
    //   它无法发现自己的收缩。**
    //   ⇒ 钉总数。红了不许直接改数字：先重量一遍，确认是真的增减
    //     （新增前缀 / 新增枚举值会顶高，删一条后缀会顶低）。
    const total = DYNAMIC_KEYS.reduce((n, [, ss]) => n + ss.length, 0)
    console.log(`[⑥-6] 动态前缀 ${DYNAMIC_KEYS.length} 个 · 生成键 ${total} 个`)
    expect(total).toBe(GENERATED_KEY_TOTAL)
  })
})

describe('★ 动态键不能是「字面量键集合的漏网」', () => {
  // 反向锁定：把每个动态键的真实值打出来给人看，
  // 顺带证明这些键**确实是不同的键**（不是全部塌成同一个）。
  it('各前缀下的键互不重复（防止清单写错导致漏测）', () => {
    const all = DYNAMIC_KEYS.flatMap(([p, ss]) => ss.map((s) => p + s))
    expect(new Set(all).size).toBe(all.length)
  })
})
