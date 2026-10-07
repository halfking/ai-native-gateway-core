import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchTaskTypeCorrectionStats,
  unwrapCorrectionStatsResponse,
  unwrapTaskTypeCorrection,
  buildCorrectionStatsPath,
  correctionStatsQueryIsValid,
  sinceMatchesRfc3339Utc,
  sinceEquals,
  suggestionKeysMatchStats,
  isConfidenceEscalated,
  confidenceEscalationFallbacks,
  suggestionMatchesConfidence075,
  escalatedTierAt,
  recentIsNonIncreasing,
  correctionStatsContainersAreNotNull,
  TASK_TYPE_CORRECTION_STATS_PATH,
  CORRECTION_STATS_ENVELOPE_KEYS,
  TASK_TYPE_CORRECTION_KEYS,
  TASK_TYPE_CORRECTION_NULLABLE_KEYS,
  CORRECTION_STATS_SUGGEST_CONFIDENCE,
  CORRECTION_STATS_SINCE_DAYS_MIN,
  CORRECTION_STATS_SINCE_DAYS_MAX,
  CORRECTION_STATS_RECENT_LIMIT_MIN,
  CORRECTION_STATS_RECENT_LIMIT_MAX,
  CORRECTION_STATS_DEFAULT_SINCE_DAYS,
  CORRECTION_STATS_DEFAULT_RECENT_LIMIT,
  type CorrectionStatsResponse,
} from './taskTypeCorrectionStats'
import type { CorrectionStat, TaskProfileSuggestion } from './taskProfile'

// ═══════════════════════════════════════════════════════════════════════════
// 夹具 —— 逐字抄自后端源码，**不得**用被测常量造
// ═══════════════════════════════════════════════════════════════════════════

/** registry.go:39 architecture（tier-a / [tier-b] / 0.70）。 */
const PROFILE_ARCHITECTURE = {
  task_type: 'architecture',
  preferred_tier: 'tier-a',
  fallback_tiers: ['tier-b'],
  min_confidence: 0.7,
}
/** registry.go:41 debugging（tier-a / [tier-b] / 0.65）—— 全表最低阈值。 */
const PROFILE_DEBUGGING = {
  task_type: 'debugging',
  preferred_tier: 'tier-a',
  fallback_tiers: ['tier-b'],
  min_confidence: 0.65,
}
/** registry.go:42 coding（tier-b / [tier-a, tier-c] / 0.75）—— 恰在 0.75 这条线上。 */
const PROFILE_CODING = {
  task_type: 'coding',
  preferred_tier: 'tier-b',
  fallback_tiers: ['tier-a', 'tier-c'],
  min_confidence: 0.75,
}
/** registry.go:45 devops（tier-c / [tier-b] / 0.80）。 */
const PROFILE_DEVOPS = {
  task_type: 'devops',
  preferred_tier: 'tier-c',
  fallback_tiers: ['tier-b'],
  min_confidence: 0.8,
}
/** registry.go:46 documentation（tier-c / **[]** / 0.85）—— 空 fallback 的那两个之一。 */
const PROFILE_DOCUMENTATION = {
  task_type: 'documentation',
  preferred_tier: 'tier-c',
  fallback_tiers: [],
  min_confidence: 0.85,
}
/** registry.go:53 chat（tier-b / [tier-a] / 0.80）—— V2 legacy。 */
const PROFILE_CHAT = {
  task_type: 'chat',
  preferred_tier: 'tier-b',
  fallback_tiers: ['tier-a'],
  min_confidence: 0.8,
}

/**
 * corrections.go:136-142 的 `GROUP BY` 聚合。
 * ★ 修正率**刻意不取 0.30**（升级阈值本身）—— 撞上常量时两种实现同解，判据就没牙。
 * ★ 阈值判别的真正区分格是「**样本够但率不够**」⇒ `STAT_BELOW_02`（total 10 / rate 0.20）。
 */
const STAT_BELOW_02 = { task_type: 'coding', total: 10, agrees: 8, corrected: 2, correction_rate: 2 / 10 }
const STAT_RATE_04 = { task_type: 'coding', total: 10, agrees: 6, corrected: 4, correction_rate: 4 / 10 }
const STAT_RATE_01 = { task_type: 'coding', total: 10, agrees: 9, corrected: 1, correction_rate: 1 / 10 }
const STAT_RATE_05 = { task_type: 'coding', total: 10, agrees: 5, corrected: 5, correction_rate: 5 / 10 }
/** `total = 4` 才是 `>= 5` 的区分格（`total = 5` 上两种实现同解）。 */
const STAT_TOTAL_4 = { task_type: 'coding', total: 4, agrees: 2, corrected: 2, correction_rate: 2 / 4 }
/** `total = 5` 且 rate 0.4 —— 刚好越过两个阈值。 */
const STAT_TOTAL_5 = { task_type: 'coding', total: 5, agrees: 3, corrected: 2, correction_rate: 2 / 5 }
/**
 * ★★ **唯一一份刻意压在阈值上的夹具**：`3 / 10 === 0.3` 与字面量 `0.3` 是同一个 double
 * ⇒ `>= 0.3` 成立、`> 0.3` 不成立 ⇒ 这是 `suggest.go:69` 那个 `>=` 的**唯一**区分格。
 * ★ 与「样本不要撞常量」不冲突：那条防的是**无意**撞上（两种实现恰好同解、白绿），
 *   这里要的是**有意**压在边界上 —— 没有它，`>=` 改成 `>` 完全测不出来。
 */
const STAT_EXACT_03 = { task_type: 'coding', total: 10, agrees: 7, corrected: 3, correction_rate: 3 / 10 }

/** corrections.go:28-39 + 724 建表：十个键全部有值的修正记录。 */
const CORRECTION_FULL = {
  id: 412,
  request_id: 'req-8f21c0ab',
  auto_task_type: 'coding',
  human_task_type: 'debugging',
  agrees: false,
  classifier_confidence: 0.93,
  profile: 'balanced',
  annotator: 'qa@local',
  reason: '实际是排错',
  created_at: '2026-10-08T09:12:33Z',
}
/** ★ (13) 两个可空键**都在 null**，而键本身仍在。 */
const CORRECTION_NULL_BOTH = {
  ...CORRECTION_FULL,
  classifier_confidence: null,
  profile: null,
}

/** corrections.go:178 `ORDER BY created_at DESC` ⇒ 非递增（并列真实可能）。 */
const CORRECTION_NEWER = { ...CORRECTION_FULL, id: 413, created_at: '2026-10-08T10:02:11Z' }
const CORRECTION_MIDDLE = { ...CORRECTION_FULL, id: 412, created_at: '2026-10-08T09:12:33Z' }
const CORRECTION_OLDER = { ...CORRECTION_FULL, id: 411, created_at: '2026-10-08T08:44:02Z' }

/** suggestion.go:85-92 的 6 键构造，`correction_stats` 省略（omitempty）。 */
function suggestion(over: Partial<TaskProfileSuggestion> = {}): TaskProfileSuggestion {
  return {
    task_type: 'coding',
    tier: 'tier-b',
    fallback_tiers: ['tier-a', 'tier-c'],
    min_confidence: 0.75,
    tier_source: 'registry',
    ...over,
  }
}

/**
 * handler.go:293-298 的四键信封。
 * ★ 默认这份 stat 是**未达阈值**的（total 10 但 rate 0.20），
 *   因此默认的 `suggestion()`（tier-b / registry / 0.75）与它**自洽** ——
 *   ★ 夹具必须自身可信，否则「解包通过」证明不了任何事。
 */
function envelope(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    since: '2026-09-08T09:12:33Z',
    stats: { coding: STAT_BELOW_02 },
    suggestions: { coding: suggestion() },
    recent: [CORRECTION_FULL],
    ...over,
  }
}

const fetchMock = vi.fn()
beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } })
}

/** 删键造「键缺」—— ★ 必须用 `delete`，`undefined` 会造出显式 undefined 键。 */
function del(obj: Record<string, unknown>, key: string): Record<string, unknown> {
  const c = { ...obj }
  delete c[key]
  return c
}

/** 改键的类型，**保持键齐全** —— 区分「键缺」与「类型错」两条分支。 */
function retype(obj: Record<string, unknown>, key: string, value: unknown): Record<string, unknown> {
  return { ...obj, [key]: value }
}

// ═══════════════════════════════════════════════════════════════════════════
// 常量与路径
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · 常量', () => {
  it('路径是 corrections/stats（handler.go:131）', () => {
    expect(TASK_TYPE_CORRECTION_STATS_PATH).toBe('/api/admin/task-profile/corrections/stats')
  })

  it('路径里不含方法名（★ 方法在 mux 路由模式里）', () => {
    expect(TASK_TYPE_CORRECTION_STATS_PATH).not.toContain('GET')
  })

  it('信封是 4 个键且顺序如源码', () => {
    expect(CORRECTION_STATS_ENVELOPE_KEYS).toEqual(['since', 'stats', 'suggestions', 'recent'])
  })

  it('信封键数是 4', () => {
    expect(CORRECTION_STATS_ENVELOPE_KEYS).toHaveLength(4)
  })

  it('suggestions 在信封里且恒在', () => {
    expect(CORRECTION_STATS_ENVELOPE_KEYS).toContain('suggestions')
  })

  it('修正记录是 10 个键（corrections.go:28-39）', () => {
    expect(TASK_TYPE_CORRECTION_KEYS).toHaveLength(10)
  })

  it('可空键是 classifier_confidence 与 profile', () => {
    expect(TASK_TYPE_CORRECTION_NULLABLE_KEYS).toEqual(['classifier_confidence', 'profile'])
  })

  it('可空键只有 2 个', () => {
    expect(TASK_TYPE_CORRECTION_NULLABLE_KEYS).toHaveLength(2)
  })

  it('可空键也是 10 键的子集', () => {
    for (const k of TASK_TYPE_CORRECTION_NULLABLE_KEYS) expect(TASK_TYPE_CORRECTION_KEYS).toContain(k)
  })

  it('建议置信度是 0.75（handler.go:290）', () => {
    expect(CORRECTION_STATS_SUGGEST_CONFIDENCE).toBe(0.75)
  })

  it('since_days 下界是 1（handler.go:258）', () => {
    expect(CORRECTION_STATS_SINCE_DAYS_MIN).toBe(1)
  })

  it('since_days 上界是 365（handler.go:258）', () => {
    expect(CORRECTION_STATS_SINCE_DAYS_MAX).toBe(365)
  })

  it('recent_limit 下界是 1（handler.go:267）', () => {
    expect(CORRECTION_STATS_RECENT_LIMIT_MIN).toBe(1)
  })

  it('recent_limit 上界是 500（handler.go:267）', () => {
    expect(CORRECTION_STATS_RECENT_LIMIT_MAX).toBe(500)
  })

  it('缺省 since_days 是 30（handler.go:254）', () => {
    expect(CORRECTION_STATS_DEFAULT_SINCE_DAYS).toBe(30)
  })

  it('缺省 recent_limit 是 100（handler.go:263）', () => {
    expect(CORRECTION_STATS_DEFAULT_RECENT_LIMIT).toBe(100)
  })

  it('缺省 recent_limit 落在合法区间内', () => {
    expect(CORRECTION_STATS_DEFAULT_RECENT_LIMIT).toBeGreaterThanOrEqual(CORRECTION_STATS_RECENT_LIMIT_MIN)
    expect(CORRECTION_STATS_DEFAULT_RECENT_LIMIT).toBeLessThanOrEqual(CORRECTION_STATS_RECENT_LIMIT_MAX)
  })

  it('缺省 since_days 落在合法区间内', () => {
    expect(CORRECTION_STATS_DEFAULT_SINCE_DAYS).toBeGreaterThanOrEqual(CORRECTION_STATS_SINCE_DAYS_MIN)
    expect(CORRECTION_STATS_DEFAULT_SINCE_DAYS).toBeLessThanOrEqual(CORRECTION_STATS_SINCE_DAYS_MAX)
  })
})

describe('修正统计 · buildCorrectionStatsPath', () => {
  it('★ 无参数时不给查询串（桌面默认也不给）', () => {
    expect(buildCorrectionStatsPath()).toBe(TASK_TYPE_CORRECTION_STATS_PATH)
  })

  it('★ 显式空对象同样无查询串', () => {
    expect(buildCorrectionStatsPath({})).toBe(TASK_TYPE_CORRECTION_STATS_PATH)
  })

  it('只给 since_days 时拼一个参数', () => {
    expect(buildCorrectionStatsPath({ sinceDays: 30 })).toBe(`${TASK_TYPE_CORRECTION_STATS_PATH}?since_days=30`)
  })

  it('★ 给 recent_limit 时拼另一个参数', () => {
    expect(buildCorrectionStatsPath({ recentLimit: 10 })).toBe(`${TASK_TYPE_CORRECTION_STATS_PATH}?recent_limit=10`)
  })

  it('★ 两个都给时用 & 连接', () => {
    expect(buildCorrectionStatsPath({ sinceDays: 7, recentLimit: 20 })).toBe(
      `${TASK_TYPE_CORRECTION_STATS_PATH}?since_days=7&recent_limit=20`,
    )
  })

  it('★ sinceDays 取下界 1', () => {
    expect(buildCorrectionStatsPath({ sinceDays: 1 })).toContain('since_days=1')
  })

  it('sinceDays 取上界 365', () => {
    expect(buildCorrectionStatsPath({ sinceDays: 365 })).toContain('since_days=365')
  })

  it('recentLimit 取下界 1', () => {
    expect(buildCorrectionStatsPath({ recentLimit: 1 })).toContain('recent_limit=1')
  })

  it('recentLimit 取上界 500', () => {
    expect(buildCorrectionStatsPath({ recentLimit: 500 })).toContain('recent_limit=500')
  })

  it('★ sinceDays 为 0 也会照拼（合法性由后端判）', () => {
    expect(buildCorrectionStatsPath({ sinceDays: 0 })).toContain('since_days=0')
  })

  it('recentLimit 超过上界也照拼（不夹取）', () => {
    expect(buildCorrectionStatsPath({ recentLimit: 501 })).toContain('recent_limit=501')
  })

  it('★ 查询串里不含方法名', () => {
    expect(buildCorrectionStatsPath({ sinceDays: 3, recentLimit: 4 })).not.toContain('GET')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (5)(6) 查询参数合法性
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · 查询参数合法性', () => {
  it('★ 不传参数时合法', () => {
    expect(correctionStatsQueryIsValid()).toBe(true)
  })

  it('空对象也合法', () => {
    expect(correctionStatsQueryIsValid({})).toBe(true)
  })

  it('sinceDays 取下界 1 合法', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: 1 })).toBe(true)
  })

  it('sinceDays 取上界 365 合法', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: 365 })).toBe(true)
  })

  it('★ sinceDays=0 越下界（handler 的 days <= 0 分支）', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: 0 })).toBe(false)
  })

  it('★ sinceDays=366 越上界（days > 365 分支）', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: 366 })).toBe(false)
  })

  it('★ sinceDays=1.5 非整数（err != nil 分支）', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: 1.5 })).toBe(false)
  })

  it('sinceDays 为负数也越界', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: -7 })).toBe(false)
  })

  it('recentLimit 取下界 1 合法', () => {
    expect(correctionStatsQueryIsValid({ recentLimit: 1 })).toBe(true)
  })

  it('recentLimit 取上界 500 合法', () => {
    expect(correctionStatsQueryIsValid({ recentLimit: 500 })).toBe(true)
  })

  it('★ recentLimit=0 越下界（n <= 0 分支）', () => {
    expect(correctionStatsQueryIsValid({ recentLimit: 0 })).toBe(false)
  })

  it('★ recentLimit=501 越上界（n > 500 分支）', () => {
    expect(correctionStatsQueryIsValid({ recentLimit: 501 })).toBe(false)
  })

  it('★ recentLimit 非整数被拒', () => {
    expect(correctionStatsQueryIsValid({ recentLimit: 2.5 })).toBe(false)
  })

  it('★ 两个参数都合法时整体合法', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: 30, recentLimit: 100 })).toBe(true)
  })

  it('★ 只要一个非法就整体非法（since 越界）', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: 366, recentLimit: 100 })).toBe(false)
  })

  it('★ 只要一个非法就整体非法（limit 越界）', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: 30, recentLimit: 501 })).toBe(false)
  })

  it('★ Infinity 被 Number.isInteger 挡掉', () => {
    expect(correctionStatsQueryIsValid({ sinceDays: Number.POSITIVE_INFINITY })).toBe(false)
  })

  it('★ NaN 被 Number.isInteger 挡掉', () => {
    expect(correctionStatsQueryIsValid({ recentLimit: Number.NaN })).toBe(false)
  })

  it('默认缺省组合本身合法', () => {
    expect(
      correctionStatsQueryIsValid({
        sinceDays: CORRECTION_STATS_DEFAULT_SINCE_DAYS,
        recentLimit: CORRECTION_STATS_DEFAULT_RECENT_LIMIT,
      }),
    ).toBe(true)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (8)(9) since
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · since 格式', () => {
  it('★ 标准 UTC 秒级时间戳通过', () => {
    expect(sinceMatchesRfc3339Utc('2026-09-08T09:12:33Z')).toBe(true)
  })

  it('★ 缺省 30 天算出的形状也通过', () => {
    expect(sinceMatchesRfc3339Utc('2026-10-08T09:12:33Z')).toBe(true)
  })

  it('★ 小数秒被拒（Format(RFC3339) 不含小数）', () => {
    expect(sinceMatchesRfc3339Utc('2026-09-08T09:12:33.123Z')).toBe(false)
  })

  it('★ 非零时区偏移被拒（.UTC() 之后必是 Z）', () => {
    expect(sinceMatchesRfc3339Utc('2026-09-08T09:12:33+08:00')).toBe(false)
  })

  it('★ 空格分隔被拒（RFC3339 是 T）', () => {
    expect(sinceMatchesRfc3339Utc('2026-09-08 09:12:33Z')).toBe(false)
  })

  it('★ 缺 Z 后缀被拒', () => {
    expect(sinceMatchesRfc3339Utc('2026-09-08T09:12:33')).toBe(false)
  })

  it('★ 缺秒被拒', () => {
    expect(sinceMatchesRfc3339Utc('2026-09-08T09:12Z')).toBe(false)
  })

  // ★★★ **起锚 `^` 的唯一区分格**：只测「少一段」是抓不住去锚的 ——
  //   少的那段整体都不匹配，去锚也一样不匹配 ⇒ 必须有一份「**前面有多余字符**」的样本。
  it('★ 前缀有多余字符时被拒', () => {
    expect(sinceMatchesRfc3339Utc('前缀2026-09-08T09:12:33Z')).toBe(false)
  })

  it('★ 后缀有多余字符时被拒', () => {
    expect(sinceMatchesRfc3339Utc('2026-09-08T09:12:33Z尾巴')).toBe(false)
  })

  it('空串被拒', () => {
    expect(sinceMatchesRfc3339Utc('')).toBe(false)
  })

  it('★ 普通英文串被拒', () => {
    expect(sinceMatchesRfc3339Utc('2026-09-08')).toBe(false)
  })

  it('两串相同时 sinceEquals 为真', () => {
    expect(sinceEquals('2026-09-08T09:12:33Z', '2026-09-08T09:12:33Z')).toBe(true)
  })

  it('★ 跨秒则不相等（两次 time.Now 落在不同时刻）', () => {
    expect(sinceEquals('2026-09-08T09:12:33Z', '2026-09-08T09:12:34Z')).toBe(false)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 解包：顶层
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · 顶层解包', () => {
  it('★ 一份完整信封被解出', () => {
    const r = unwrapCorrectionStatsResponse(envelope(), 'r')
    expect(Object.keys(r)).toHaveLength(4)
    expect(r.since).toBe('2026-09-08T09:12:33Z')
  })

  it('响应是字符串时抛形状不符', () => {
    expect(() => unwrapCorrectionStatsResponse('nope', 'r')).toThrow(/r 响应形状不符：期望裸对象，实得 string/)
  })

  it('响应是 null 时抛形状不符', () => {
    expect(() => unwrapCorrectionStatsResponse(null, 'r')).toThrow(/实得 null/)
  })

  it('响应是数组时抛形状不符', () => {
    expect(() => unwrapCorrectionStatsResponse([], 'r')).toThrow(/实得 array/)
  })

  it('★ 缺 since 时报缺 1 个键', () => {
    const { since: _s, ...rest } = envelope()
    expect(() => unwrapCorrectionStatsResponse(rest, 'r')).toThrow(/r 缺 1 个键（since）/)
  })

  it('★ 缺 stats 时报缺 1 个键', () => {
    const { stats: _s, ...rest } = envelope()
    expect(() => unwrapCorrectionStatsResponse(rest, 'r')).toThrow(/r 缺 1 个键（stats）/)
  })

  it('★ 缺 suggestions 时报缺 1 个键（桌面误标成可选）', () => {
    const { suggestions: _s, ...rest } = envelope()
    expect(() => unwrapCorrectionStatsResponse(rest, 'r')).toThrow(/r 缺 1 个键（suggestions）/)
  })

  it('★ 缺 recent 时报缺 1 个键', () => {
    const { recent: _s, ...rest } = envelope()
    expect(() => unwrapCorrectionStatsResponse(rest, 'r')).toThrow(/r 缺 1 个键（recent）/)
  })

  it('★ 缺两个键时报缺 2 个键', () => {
    const e = del(envelope(), 'since')
    expect(() => unwrapCorrectionStatsResponse(del(e, 'recent'), 'r')).toThrow(/r 缺 2 个键（since, recent）/)
  })

  it('★ 缺三个键时报缺 3 个键', () => {
    let e = del(envelope(), 'since')
    e = del(e, 'stats')
    e = del(e, 'suggestions')
    expect(() => unwrapCorrectionStatsResponse(e, 'r')).toThrow(/r 缺 3 个键（since, stats, suggestions）/)
  })

  it('★ since 不是字符串时被抓住', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'since', 12345), 'r')).toThrow(/r 的 since 不是字符串/)
  })

  it('since 为 null 时被抓住', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'since', null), 'r')).toThrow(/r 的 since 不是字符串/)
  })

  it('★ stats 是数组时抛不是对象', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'stats', []), 'r')).toThrow(/r 的 stats 不是对象/)
  })

  it('★ stats 是 null 时抛不是对象', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'stats', null), 'r')).toThrow(/r 的 stats 不是对象/)
  })

  it('stats 是数字时抛不是对象', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'stats', 7), 'r')).toThrow(/r 的 stats 不是对象/)
  })

  it('★ suggestions 是数组时抛不是对象', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'suggestions', []), 'r')).toThrow(
      /r 的 suggestions 不是对象/,
    )
  })

  it('★ suggestions 是 null 时抛不是对象', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'suggestions', null), 'r')).toThrow(
      /r 的 suggestions 不是对象/,
    )
  })

  it('★ suggestions 是字符串时抛不是对象', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'suggestions', 'x'), 'r')).toThrow(
      /r 的 suggestions 不是对象/,
    )
  })

  it('★ recent 不是数组时被抓住', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'recent', {}), 'r')).toThrow(/r 的 recent 不是数组/)
  })

  it('recent 是 null 时被抓住', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'recent', null), 'r')).toThrow(/r 的 recent 不是数组/)
  })

  it('recent 是字符串时被抓住', () => {
    expect(() => unwrapCorrectionStatsResponse(retype(envelope(), 'recent', 'x'), 'r')).toThrow(/r 的 recent 不是数组/)
  })

  it('★ 空 stats 对象 {} 通过（make 出来的非 nil）', () => {
    const r = unwrapCorrectionStatsResponse(envelope({ stats: {} }), 'r')
    expect(Object.keys(r.stats)).toHaveLength(0)
  })

  it('★ 空 suggestions 对象 {} 通过', () => {
    const r = unwrapCorrectionStatsResponse(envelope({ suggestions: {} }), 'r')
    expect(Object.keys(r.suggestions)).toHaveLength(0)
  })

  it('★ 空 recent 数组 [] 通过', () => {
    const r = unwrapCorrectionStatsResponse(envelope({ recent: [] }), 'r')
    expect(r.recent).toHaveLength(0)
  })

  it('★ stats 键齐全只错类型时走类型分支', () => {
    const bad = { ...STAT_RATE_04, correction_rate: '0.4' }
    expect(() => unwrapCorrectionStatsResponse(envelope({ stats: { coding: bad } }), 'r')).toThrow(
      /r 的 stats\[coding\] 的 correction_rate 不是数字/,
    )
  })

  it('★ stats 值缺键时走键缺分支（消息与类型错不同）', () => {
    const { correction_rate: _c, ...bad } = STAT_RATE_04
    expect(() => unwrapCorrectionStatsResponse(envelope({ stats: { coding: bad } }), 'r')).toThrow(
      /r 的 stats\[coding\] 缺 1 个键（correction_rate）/,
    )
  })

  it('★ suggestions 键齐全只错类型时走类型分支', () => {
    const bad = { ...suggestion(), min_confidence: '0.75' }
    expect(() => unwrapCorrectionStatsResponse(envelope({ suggestions: { coding: bad } }), 'r')).toThrow(
      /r 的 suggestions\[coding\] 的 min_confidence 不是数字/,
    )
  })

  it('★ suggestions 值缺键时走键缺分支', () => {
    const { tier_source: _t, ...bad } = suggestion()
    expect(() => unwrapCorrectionStatsResponse(envelope({ suggestions: { coding: bad } }), 'r')).toThrow(
      /r 的 suggestions\[coding\] 缺 1 个键（tier_source）/,
    )
  })

  it('★ 多类型 stats 逐项都校验（第二项也报错）', () => {
    const bad = { ...STAT_RATE_04, total: '10' }
    const e = envelope({ stats: { coding: STAT_RATE_04, debugging: bad } })
    expect(() => unwrapCorrectionStatsResponse(e, 'r')).toThrow(/r 的 stats\[debugging\] 的 total 不是数字/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 解包：recent 逐项（10 键）
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · 修正记录解包', () => {
  it('★ 完整一份被解出', () => {
    const c = unwrapTaskTypeCorrection(CORRECTION_FULL, 'c')
    expect(c.id).toBe(412)
    expect(c.request_id).toBe('req-8f21c0ab')
  })

  it('★ 两个可空键都为 null 时通过', () => {
    const c = unwrapTaskTypeCorrection(CORRECTION_NULL_BOTH, 'c')
    expect(c.classifier_confidence).toBeNull()
    expect(c.profile).toBeNull()
  })

  it('响应是字符串时抛形状不符', () => {
    expect(() => unwrapTaskTypeCorrection('x', 'c')).toThrow(/c 响应形状不符：期望裸对象，实得 string/)
  })

  it('★ 修正记录是数组时抛', () => {
    expect(() => unwrapTaskTypeCorrection([], 'c')).toThrow(/实得 array/)
  })

  it('★ 修正记录是 null 时抛', () => {
    expect(() => unwrapTaskTypeCorrection(null, 'c')).toThrow(/实得 null/)
  })

  it('★ 缺 id 时报缺 1 个键', () => {
    expect(() => unwrapTaskTypeCorrection(del(CORRECTION_FULL as never, 'id') as never, 'c')).toThrow(
      /c 缺 1 个键（id）/,
    )
  })

  it('★ 缺 classifier_confidence 时报缺键（键恒在）', () => {
    expect(() => unwrapTaskTypeCorrection(del(CORRECTION_FULL as never, 'classifier_confidence') as never, 'c')).toThrow(
      /c 缺 1 个键（classifier_confidence）/,
    )
  })

  it('★ 缺 profile 时报缺键', () => {
    expect(() => unwrapTaskTypeCorrection(del(CORRECTION_FULL as never, 'profile') as never, 'c')).toThrow(
      /c 缺 1 个键（profile）/,
    )
  })

  it('★ 修正记录缺两个键时', () => {
    let c = del(CORRECTION_FULL as never, 'agrees') as never
    c = del(c as never, 'annotator') as never
    expect(() => unwrapTaskTypeCorrection(c as never, 'c')).toThrow(/c 缺 2 个键（agrees, annotator）/)
  })

  it('★ 修正记录缺三个键时', () => {
    let c = del(CORRECTION_FULL as never, 'reason') as never
    c = del(c as never, 'created_at') as never
    c = del(c as never, 'human_task_type') as never
    expect(() => unwrapTaskTypeCorrection(c as never, 'c')).toThrow(/c 缺 3 个键（human_task_type, reason, created_at）/)
  })

  // ★ 下面四条各删**一个**键：变异里「从 10 键常量里删掉某个键」会直接让其中一条变红，
  //   而类型校验用例抓不到（字符串循环与键常量是两条独立的分支）。
  it('★ 修正记录缺 agrees 时', () => {
    expect(() => unwrapTaskTypeCorrection(del(CORRECTION_FULL as never, 'agrees') as never, 'c')).toThrow(
      /c 缺 1 个键（agrees）/,
    )
  })

  it('★ 修正记录缺 created_at 时', () => {
    expect(() => unwrapTaskTypeCorrection(del(CORRECTION_FULL as never, 'created_at') as never, 'c')).toThrow(
      /c 缺 1 个键（created_at）/,
    )
  })

  it('★ 修正记录缺 request_id 时', () => {
    expect(() => unwrapTaskTypeCorrection(del(CORRECTION_FULL as never, 'request_id') as never, 'c')).toThrow(
      /c 缺 1 个键（request_id）/,
    )
  })

  it('★ 修正记录缺 reason 时', () => {
    expect(() => unwrapTaskTypeCorrection(del(CORRECTION_FULL as never, 'reason') as never, 'c')).toThrow(
      /c 缺 1 个键（reason）/,
    )
  })

  it('★ 缺全部 10 个键时报缺 10 个键', () => {
    const empty: Record<string, unknown> = {}
    expect(() => unwrapTaskTypeCorrection(empty, 'c')).toThrow(/c 缺 10 个键/)
  })

  it('★ id 不是数字时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'id', '412') as never, 'c')).toThrow(
      /c 的 id 不是数字/,
    )
  })

  it('id 为 null 时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'id', null) as never, 'c')).toThrow(
      /c 的 id 不是数字/,
    )
  })

  it('★ request_id 不是字符串时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'request_id', 9) as never, 'c')).toThrow(
      /c 的 request_id 不是字符串/,
    )
  })

  it('★ auto_task_type 不是字符串时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'auto_task_type', 1) as never, 'c')).toThrow(
      /c 的 auto_task_type 不是字符串/,
    )
  })

  it('★ human_task_type 不是字符串时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'human_task_type', {}) as never, 'c')).toThrow(
      /c 的 human_task_type 不是字符串/,
    )
  })

  it('★ agrees 是字符串时被抓住（不是布尔）', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'agrees', 'false') as never, 'c')).toThrow(
      /c 的 agrees 不是布尔/,
    )
  })

  it('agrees 为数字时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'agrees', 0) as never, 'c')).toThrow(
      /c 的 agrees 不是布尔/,
    )
  })

  it('★ agrees 为 false 是合法布尔（不被当成缺）', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'agrees', false) as never, 'c')).not.toThrow()
  })

  it('★ annotator 不是字符串时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'annotator', null) as never, 'c')).toThrow(
      /c 的 annotator 不是字符串/,
    )
  })

  it('★ reason 不是字符串时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'reason', 3) as never, 'c')).toThrow(
      /c 的 reason 不是字符串/,
    )
  })

  it('★ created_at 不是字符串时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'created_at', 1757318400) as never, 'c')).toThrow(
      /c 的 created_at 不是字符串/,
    )
  })

  it('★ 置信度是字符串时被抓住', () => {
    expect(() =>
      unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'classifier_confidence', '0.93') as never, 'c'),
    ).toThrow(/c 的 classifier_confidence 不是数字也不是 null/)
  })

  it('★ 置信度是布尔时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'classifier_confidence', true) as never, 'c')).toThrow(
      /c 的 classifier_confidence 不是数字也不是 null/,
    )
  })

  it('★ 置信度为 0 是合法数字（不被当成假值）', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'classifier_confidence', 0) as never, 'c')).not.toThrow()
  })

  it('★ profile 是数字时被抓住', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'profile', 7) as never, 'c')).toThrow(
      /c 的 profile 不是字符串也不是 null/,
    )
  })

  it('★ profile 为空串是合法字符串', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'profile', '') as never, 'c')).not.toThrow()
  })

  it('★ 空串 profile 与 null profile 消息不同（两条分支）', () => {
    expect(() => unwrapTaskTypeCorrection(retype(CORRECTION_FULL as never, 'profile', 0) as never, 'c')).toThrow(
      /不是字符串也不是 null/,
    )
  })

  it('★ recent[1] 的错误带下标', () => {
    const e = envelope({ recent: [CORRECTION_FULL, retype(CORRECTION_OLDER as never, 'id', 'x') as never] })
    expect(() => unwrapCorrectionStatsResponse(e, 'r')).toThrow(/r 的 recent\[1\] 的 id 不是数字/)
  })

  it('★ recent[0] 的错误也带下标', () => {
    const e = envelope({ recent: [retype(CORRECTION_FULL as never, 'id', null) as never] })
    expect(() => unwrapCorrectionStatsResponse(e, 'r')).toThrow(/r 的 recent\[0\] 的 id 不是数字/)
  })

  it('recent 的空数组不会被索引', () => {
    expect(() => unwrapCorrectionStatsResponse(envelope({ recent: [] }), 'r')).not.toThrow()
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (12) 同集合
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · 同集合判据', () => {
  function resp(s: Record<string, unknown>, g: Record<string, unknown>): CorrectionStatsResponse {
    return { since: '2026-09-08T09:12:33Z', stats: s, suggestions: g, recent: [] } as unknown as CorrectionStatsResponse
  }

  it('★ 单键且键名相同时为真', () => {
    expect(suggestionKeysMatchStats(resp({ coding: STAT_RATE_04 }, { coding: suggestion() }))).toBe(true)
  })

  it('★ 两边都空时为真', () => {
    expect(suggestionKeysMatchStats(resp({}, {}))).toBe(true)
  })

  it('★ ★区分格：键数相同但键名不同 ⇒ 假', () => {
    expect(suggestionKeysMatchStats(resp({ coding: STAT_RATE_04 }, { debugging: suggestion() }))).toBe(false)
  })

  it('★ suggestions 多一个键时为假', () => {
    expect(suggestionKeysMatchStats(resp({ coding: STAT_RATE_04 }, { coding: suggestion(), debugging: suggestion() }))).toBe(
      false,
    )
  })

  it('★ suggestions 少一个键时为假', () => {
    expect(suggestionKeysMatchStats(resp({ coding: STAT_RATE_04, debugging: STAT_RATE_01 }, { coding: suggestion() }))).toBe(
      false,
    )
  })

  it('★ 两个键顺序不同但集合相同 ⇒ 仍为真（map 无序）', () => {
    const s = { coding: STAT_RATE_04, debugging: STAT_RATE_01 }
    const g = { debugging: suggestion(), coding: suggestion() }
    expect(suggestionKeysMatchStats(resp(s, g))).toBe(true)
  })

  it('★ 键名相同但只有 stats 侧有值也算同集合（值不看，只看键）', () => {
    expect(suggestionKeysMatchStats(resp({ coding: STAT_RATE_04 }, { coding: undefined as never }))).toBe(true)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (1)(2)(3) 置信度升级三件套
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · 置信度升级判定', () => {
  it('★ 0.75 < 0.80 成立（devops 的 base 阈值）', () => {
    expect(isConfidenceEscalated(0.75, 0.8)).toBe(true)
  })

  it('★ 0.75 < 0.85 成立（documentation 的 base 阈值）', () => {
    expect(isConfidenceEscalated(0.75, 0.85)).toBe(true)
  })

  it('★ ★边界：0.75 < 0.75 不成立（< 不是 <=）', () => {
    expect(isConfidenceEscalated(0.75, 0.75)).toBe(false)
  })

  it('★ 0.75 < (0.70+0.05) 不成立（浮点上正好等于 0.75）', () => {
    expect(isConfidenceEscalated(0.75, 0.7 + 0.05)).toBe(false)
  })

  it('★ 0.75 < (0.75+0.05) 成立（coding 升级后跨过这条线）', () => {
    expect(isConfidenceEscalated(0.75, 0.75 + 0.05)).toBe(true)
  })

  it('★ 0.75 < 0.70 不成立', () => {
    expect(isConfidenceEscalated(0.75, 0.7)).toBe(false)
  })

  it('★ 0.75 < (0.80+0.05) 成立（长尾值也照样越线）', () => {
    expect(isConfidenceEscalated(0.75, 0.8 + 0.05)).toBe(true)
  })

  it('★ 0.75 < (0.65+0.05) 不成立', () => {
    expect(isConfidenceEscalated(0.75, 0.65 + 0.05)).toBe(false)
  })

  it('★ ★边界：confidence 远大于 minConf 时不成立', () => {
    expect(isConfidenceEscalated(2, 0.75)).toBe(false)
  })

  it('★ 空 fallback 被补成 [tier-b]（documentation）', () => {
    expect(confidenceEscalationFallbacks([])).toEqual(['tier-b'])
  })

  it('★ 非空 fallback 原样返回（devops）', () => {
    expect(confidenceEscalationFallbacks(['tier-b'])).toEqual(['tier-b'])
  })

  it('★ 非空 fallback 返回的是副本不是同一引用', () => {
    const src = ['tier-a', 'tier-c']
    expect(confidenceEscalationFallbacks(src)).not.toBe(src)
  })

  it('★ 单项 fallback 保持不变', () => {
    expect(confidenceEscalationFallbacks(['tier-a'])).toEqual(['tier-a'])
  })

  it('★ 两个 fallback 保持顺序', () => {
    expect(confidenceEscalationFallbacks(['tier-c', 'tier-a'])).toEqual(['tier-c', 'tier-a'])
  })
})

describe('修正统计 · escalateTier 镜像', () => {
  it('★ tier-c 升到 tier-b', () => {
    expect(escalatedTierAt('tier-c')).toBe('tier-b')
  })

  it('★ tier-b 升到 tier-a', () => {
    expect(escalatedTierAt('tier-b')).toBe('tier-a')
  })

  it('★ tier-a 原地不动（区分格不是 tier-a）', () => {
    expect(escalatedTierAt('tier-a')).toBe('tier-a')
  })

  it('★ 未知档位落到 tier-a（default 分支）', () => {
    expect(escalatedTierAt('tier-z')).toBe('tier-a')
  })

  it('★ 空串也落到 tier-a', () => {
    expect(escalatedTierAt('')).toBe('tier-a')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (1)(2)(3) suggestionMatchesConfidence075
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · 建议自洽（confidence = 0.75）', () => {
  /** coding（tier-b / [tier-a,tier-c] / 0.75）+ 达阈值的修正 → 升级后 0.80 → 置信度再覆盖。 */
  const CODING_ESCALATED = suggestion({
    tier: 'tier-a',
    fallback_tiers: ['tier-a', 'tier-c'],
    min_confidence: 0.75 + 0.05,
    tier_source: 'confidence_escalation',
  })

  it('★ ★★ coding + 达阈值修正：source 被置信度覆盖但 min_confidence 留痕', () => {
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_5, CODING_ESCALATED)).toBe(true)
  })

  it('★ ★★ 把 source 写回 correction_escalation 就不成立', () => {
    const wrong = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'correction_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_5, wrong)).toBe(false)
  })

  it('★ ★★ 把 min_confidence 写成未升级的 0.75 就不成立', () => {
    const wrong = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_5, wrong)).toBe(false)
  })

  it('★ coding + 无修正：0.75 < 0.75 不成立 ⇒ 保持 registry', () => {
    const s = suggestion({ tier: 'tier-b', min_confidence: 0.75, tier_source: 'registry' })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, undefined, s)).toBe(true)
  })

  it('★ ★区分格：样本够(10) 但率 0.20 未达 0.30 ⇒ 不升级', () => {
    const s = suggestion({ tier: 'tier-b', min_confidence: 0.75, tier_source: 'registry' })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_BELOW_02, s)).toBe(true)
  })

  it('★ ★★同一 total=10 只把率换成 0.40 ⇒ 升级（真区分格）', () => {
    const up = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_BELOW_02, up)).toBe(false)
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_RATE_04, up)).toBe(true)
  })

  it('★ ★区分格：率 0.10 也未达阈值 ⇒ 不升级', () => {
    const s = suggestion({ tier: 'tier-b', min_confidence: 0.75, tier_source: 'registry' })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_RATE_01, s)).toBe(true)
  })

  it('★ ★区分格：率 0.50 且 total=10 ⇒ 升级', () => {
    const up = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_RATE_05, up)).toBe(true)
  })

  it('★ ★★修正率恰为 0.30 时仍升级（★ >= 的边界格）', () => {
    const up = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_EXACT_03, up)).toBe(true)
  })

  it('★ ★★率 0.20 不升级、率 0.30 升级（★ 边界两侧同一条对照）', () => {
    const up = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    const down = suggestion({ tier: 'tier-b', min_confidence: 0.75, tier_source: 'registry' })
    // ★ 夹具自证：3/10 必须与字面量 0.3 是同一个 double，否则这一整组边界断言失去意义。
    expect(STAT_EXACT_03.correction_rate).toBe(0.3)
    expect(STAT_BELOW_02.correction_rate).not.toBe(0.3)
    // ★ 两侧对调：同一份 stat，两份互斥的建议，各自只对一侧成立。
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_BELOW_02, down)).toBe(true)
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_BELOW_02, up)).toBe(false)
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_EXACT_03, down)).toBe(false)
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_EXACT_03, up)).toBe(true)
  })

  it('★ ★区分格：coding + total=4（未达 5）⇒ 不升级', () => {
    const s = suggestion({ tier: 'tier-b', min_confidence: 0.75, tier_source: 'registry' })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_4, s)).toBe(true)
  })

  it('★ ★区分格：total=5 且 rate 0.4 ⇒ 升级（两个阈值都刚过）', () => {
    const s = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_5, s)).toBe(true)
  })

  it('★ ★区分格：total=4 即使率 0.50 也不升级（样本数不够）', () => {
    const s = suggestion({ tier: 'tier-b', min_confidence: 0.75, tier_source: 'registry' })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_4, s)).toBe(true)
  })

  it('★ ★★ architecture（0.70）+ 无修正：0.75 > 0.70 ⇒ 不命中置信度升级', () => {
    const s = suggestion({
      task_type: 'architecture',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.7,
      tier_source: 'registry',
    })
    expect(suggestionMatchesConfidence075(PROFILE_ARCHITECTURE, undefined, s)).toBe(true)
  })

  it('★ ★★ architecture + 达阈值修正：升到 0.75，仍不命中（< 不是 <=）', () => {
    const s = suggestion({
      task_type: 'architecture',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.7 + 0.05,
      tier_source: 'correction_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_ARCHITECTURE, STAT_TOTAL_5, s)).toBe(true)
  })

  it('★ architecture 升级后若 source 误写 confidence_escalation 就不成立', () => {
    const s = suggestion({
      task_type: 'architecture',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.7 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_ARCHITECTURE, STAT_TOTAL_5, s)).toBe(false)
  })

  it('★ ★★★ documentation（0.85 + 空 fallback）+ 无修正：命中且补 [tier-b]', () => {
    const s = suggestion({
      task_type: 'documentation',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.85,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_DOCUMENTATION, undefined, s)).toBe(true)
  })

  it('★ ★★★ documentation 若 fallback 没被补成 [tier-b] 就不成立', () => {
    const s = suggestion({
      task_type: 'documentation',
      tier: 'tier-a',
      fallback_tiers: [],
      min_confidence: 0.85,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_DOCUMENTATION, undefined, s)).toBe(false)
  })

  it('★ ★★★ devops（0.80 / tier-c / [tier-b]）+ 无修正：升到 tier-a', () => {
    const s = suggestion({
      task_type: 'devops',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.8,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_DEVOPS, undefined, s)).toBe(true)
  })

  it('★ ★★★ devops + 达阈值修正：min_confidence 带长尾 0.8500000000000001', () => {
    const s = suggestion({
      task_type: 'devops',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.8 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_DEVOPS, STAT_TOTAL_5, s)).toBe(true)
  })

  it('★ ★★ 长尾值写成四舍五入的 0.85 就不成立（浮点陷阱）', () => {
    const s = suggestion({
      task_type: 'devops',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.85,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_DEVOPS, STAT_TOTAL_5, s)).toBe(false)
  })

  it('★ debugging（0.65，全表最低）+ 达阈值修正 ⇒ 升到 0.7000000000000001，仍不命中', () => {
    const s = suggestion({
      task_type: 'debugging',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.65 + 0.05,
      tier_source: 'correction_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_DEBUGGING, STAT_TOTAL_5, s)).toBe(true)
  })

  it('★ debugging 误写 0.7 就不成立（长尾 ≠ 字面量）', () => {
    const s = suggestion({
      task_type: 'debugging',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.7,
      tier_source: 'correction_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_DEBUGGING, STAT_TOTAL_5, s)).toBe(false)
  })

  it('★ chat（tier-b / [tier-a] / 0.80）+ 达阈值修正：升档后又回到 tier-a', () => {
    const s = suggestion({
      task_type: 'chat',
      tier: 'tier-a',
      fallback_tiers: ['tier-a'],
      min_confidence: 0.8 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CHAT, STAT_TOTAL_5, s)).toBe(true)
  })

  it('★ ★区分格：fallback 顺序不同就不成立', () => {
    const s = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-c', 'tier-a'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_5, s)).toBe(false)
  })

  it('★ fallback 多一项就不成立', () => {
    const s = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a', 'tier-c', 'tier-b'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_5, s)).toBe(false)
  })

  // ★★★ 这一条是上面那条的**镜像格**：actual 比 expected **短**时 `.every` 只遍历 actual 的下标，
  //   短的那几项全中 ⇒ `.every` 单独抓不住 ⇒ **长度比对那条不可删**。
  //   （只测「多一项」会让长度比对看起来冗余 —— 变异 #82 就是这么白绿的。）
  it('★ fallback 少一项就不成立', () => {
    const s = suggestion({
      tier: 'tier-a',
      fallback_tiers: ['tier-a'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_5, s)).toBe(false)
  })

  it('★ tier 写成 registry 档就不成立', () => {
    const s = suggestion({
      tier: 'tier-b',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75 + 0.05,
      tier_source: 'confidence_escalation',
    })
    expect(suggestionMatchesConfidence075(PROFILE_CODING, STAT_TOTAL_5, s)).toBe(false)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// recent 排序与容器
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · recent 排序', () => {
  function withRecent(recent: unknown[]): CorrectionStatsResponse {
    return { since: '2026-09-08T09:12:33Z', stats: {}, suggestions: {}, recent } as unknown as CorrectionStatsResponse
  }

  it('★ 严格降序时为真', () => {
    expect(recentIsNonIncreasing(withRecent([CORRECTION_NEWER, CORRECTION_MIDDLE, CORRECTION_OLDER]))).toBe(true)
  })

  it('★ 单项时为真', () => {
    expect(recentIsNonIncreasing(withRecent([CORRECTION_FULL]))).toBe(true)
  })

  it('★ 空数组时为真（不索引越界）', () => {
    expect(recentIsNonIncreasing(withRecent([]))).toBe(true)
  })

  it('★ ★并列时仍为真（非递增不是严格递减）', () => {
    const a = { ...CORRECTION_FULL, id: 1 }
    const b = { ...CORRECTION_FULL, id: 2 }
    expect(recentIsNonIncreasing(withRecent([a, b]))).toBe(true)
  })

  it('★ 升序时为假', () => {
    expect(recentIsNonIncreasing(withRecent([CORRECTION_OLDER, CORRECTION_NEWER]))).toBe(false)
  })

  it('★ 中间一项逆序时为假', () => {
    expect(recentIsNonIncreasing(withRecent([CORRECTION_NEWER, CORRECTION_OLDER, CORRECTION_MIDDLE]))).toBe(false)
  })
})

describe('修正统计 · 容器非 nil', () => {
  function resp(s: unknown, g: unknown, r: unknown): CorrectionStatsResponse {
    return { since: '2026-09-08T09:12:33Z', stats: s, suggestions: g, recent: r } as unknown as CorrectionStatsResponse
  }

  it('★ 三个容器都是对象/数组时为真', () => {
    expect(correctionStatsContainersAreNotNull(resp({}, {}, []))).toBe(true)
  })

  it('★ stats 为 null 时为假', () => {
    expect(correctionStatsContainersAreNotNull(resp(null, {}, []))).toBe(false)
  })

  it('★ suggestions 为 null 时为假', () => {
    expect(correctionStatsContainersAreNotNull(resp({}, null, []))).toBe(false)
  })

  it('★ recent 为 null 时为假', () => {
    expect(correctionStatsContainersAreNotNull(resp({}, {}, null))).toBe(false)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// fetch
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · fetch', () => {
  it('★ 不给参数时 URL 无查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope()))
    await fetchTaskTypeCorrectionStats()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain(TASK_TYPE_CORRECTION_STATS_PATH)
    expect(url).not.toContain('?')
    expect(init.method).toBe('GET')
  })

  it('★ 给 sinceDays 时 URL 带 since_days', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope()))
    await fetchTaskTypeCorrectionStats(undefined, { sinceDays: 30 })
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('since_days=30')
  })

  it('★ ★给 recentLimit 时桌面原来从不给，现在给了也能带上', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope()))
    await fetchTaskTypeCorrectionStats(undefined, { recentLimit: 20 })
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('recent_limit=20')
  })

  it('★ 两个参数都带', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope()))
    await fetchTaskTypeCorrectionStats(undefined, { sinceDays: 7, recentLimit: 10 })
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('since_days=7&recent_limit=10')
  })

  it('★ URL 里不含方法名', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope()))
    await fetchTaskTypeCorrectionStats()
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('GET')
  })

  it('★ 完整一份被解出', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope()))
    const r = await fetchTaskTypeCorrectionStats()
    expect(Object.keys(r.suggestions)).toEqual(['coding'])
    expect(r.recent).toHaveLength(1)
  })

  it('★ 两个可空键都在时也能解出', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope({ recent: [CORRECTION_NULL_BOTH] })))
    const r = await fetchTaskTypeCorrectionStats()
    expect(r.recent[0]?.classifier_confidence).toBeNull()
  })

  it('★ 形状不符时 Promise reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse('nope'))
    await expect(fetchTaskTypeCorrectionStats()).rejects.toThrow(/响应形状不符/)
  })

  it('★ 缺 suggestions 时 reject', async () => {
    const { suggestions: _s, ...rest } = envelope()
    fetchMock.mockResolvedValueOnce(jsonResponse(rest))
    await expect(fetchTaskTypeCorrectionStats()).rejects.toThrow(/缺 1 个键（suggestions）/)
  })

  it('★ recent 项缺键时 reject 且带下标', async () => {
    const { profile: _p, ...bad } = CORRECTION_FULL
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope({ recent: [bad] })))
    await expect(fetchTaskTypeCorrectionStats()).rejects.toThrow(/recent\[0\] 缺 1 个键（profile）/)
  })

  it('★ ★零行响应解出的是 {} / {} / [] 不是 null', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope({ stats: {}, suggestions: {}, recent: [] })))
    const r = await fetchTaskTypeCorrectionStats()
    expect(r.stats).toEqual({})
    expect(r.suggestions).toEqual({})
    expect(r.recent).toEqual([])
    expect(correctionStatsContainersAreNotNull(r)).toBe(true)
  })

  it('★ 空响应解出后 since 格式判据成立', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(envelope({ stats: {}, suggestions: {}, recent: [] })))
    const r = await fetchTaskTypeCorrectionStats()
    expect(sinceMatchesRfc3339Utc(r.since)).toBe(true)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 夹具自身的形状自检（防止夹具造错）
// ═══════════════════════════════════════════════════════════════════════════

describe('修正统计 · 夹具自检', () => {
  it('★ 完整修正记录的键数是 10', () => {
    expect(Object.keys(CORRECTION_FULL)).toHaveLength(10)
  })

  it('★ 完整修正记录的键与常量一致', () => {
    expect(Object.keys(CORRECTION_FULL)).toEqual([...TASK_TYPE_CORRECTION_KEYS])
  })

  it('★ 可空夹具的键数仍是 10', () => {
    expect(Object.keys(CORRECTION_NULL_BOTH)).toHaveLength(10)
  })

  it('★ 四键信封的键与常量一致', () => {
    expect(Object.keys(envelope())).toEqual([...CORRECTION_STATS_ENVELOPE_KEYS])
  })

  it('★ agrees+corrected === total 对每份统计都成立', () => {
    const stats: CorrectionStat[] = [STAT_BELOW_02, STAT_RATE_04, STAT_RATE_01, STAT_RATE_05, STAT_TOTAL_4, STAT_TOTAL_5]
    for (const s of stats) expect(s.agrees + s.corrected).toBe(s.total)
  })

  it('★ correction_rate === corrected/total 对每份统计都成立', () => {
    const stats: CorrectionStat[] = [STAT_BELOW_02, STAT_RATE_04, STAT_RATE_01, STAT_RATE_05, STAT_TOTAL_4, STAT_TOTAL_5]
    for (const s of stats) expect(s.correction_rate).toBe(s.corrected / s.total)
  })

  it('★ ★恰有一份统计压在 0.30 阈值上（★ 那是 >= 的边界夹具）', () => {
    const all: CorrectionStat[] = [
      STAT_BELOW_02,
      STAT_RATE_04,
      STAT_RATE_01,
      STAT_RATE_05,
      STAT_TOTAL_4,
      STAT_TOTAL_5,
      STAT_EXACT_03,
    ]
    expect(all.filter((s) => s.correction_rate === 0.3)).toEqual([STAT_EXACT_03])
  })

  it('★ 其余统计都不压在 0.30 上（★ 避免无意撞常量）', () => {
    const stats: CorrectionStat[] = [STAT_BELOW_02, STAT_RATE_04, STAT_RATE_01, STAT_RATE_05, STAT_TOTAL_4, STAT_TOTAL_5]
    for (const s of stats) expect(s.correction_rate).not.toBe(0.3)
  })

  it('★ 夹具里的统计 total 都是正数（Total ≥ 1 恒成立）', () => {
    const stats: CorrectionStat[] = [STAT_BELOW_02, STAT_RATE_04, STAT_RATE_01, STAT_RATE_05, STAT_TOTAL_4, STAT_TOTAL_5]
    for (const s of stats) expect(s.total).toBeGreaterThan(0)
  })

  it('★ 建议夹具的 5 个恒在键齐全', () => {
    expect(Object.keys(suggestion())).toEqual(['task_type', 'tier', 'fallback_tiers', 'min_confidence', 'tier_source'])
  })

  it('★ 六份注册表档案的 min_confidence 都取自源码', () => {
    const all = [
      PROFILE_ARCHITECTURE,
      PROFILE_DEBUGGING,
      PROFILE_CODING,
      PROFILE_DEVOPS,
      PROFILE_DOCUMENTATION,
      PROFILE_CHAT,
    ]
    expect(all.map((p) => p.min_confidence)).toEqual([0.7, 0.65, 0.75, 0.8, 0.85, 0.8])
  })

  it('★ 只有 documentation 的 fallback 是空的', () => {
    expect(PROFILE_DOCUMENTATION.fallback_tiers).toHaveLength(0)
    expect(PROFILE_DEVOPS.fallback_tiers).toHaveLength(1)
  })
})