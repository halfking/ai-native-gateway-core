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
  COMPLIANCE_ISSUE_TYPES,
  COMPLIANCE_QUEUE_STATUSES,
  COMPLIANCE_THRESHOLD_FIELDS,
  COMPLIANCE_FEEDBACK_TYPES,
} from '@/api/outputCompliance'

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
]

describe('动态 i18n 键在两侧词典里都存在', () => {
  it('★ 至少覆盖 14 处动态前缀（少于这个数说明下面的清单没跟上代码）', () => {
    expect(DYNAMIC_KEYS.length).toBeGreaterThanOrEqual(14)
  })

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

describe('★ 动态键不能是「字面量键集合的漏网」', () => {
  // 反向锁定：把每个动态键的真实值打出来给人看，
  // 顺带证明这些键**确实是不同的键**（不是全部塌成同一个）。
  it('各前缀下的键互不重复（防止清单写错导致漏测）', () => {
    const all = DYNAMIC_KEYS.flatMap(([p, ss]) => ss.map((s) => p + s))
    expect(new Set(all).size).toBe(all.length)
  })
})
