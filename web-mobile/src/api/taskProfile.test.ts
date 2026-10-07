import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchTaskProfile,
  unwrapTaskProfile,
  unwrapTaskProfileEntry,
  unwrapTaskProfileSuggestion,
  unwrapCorrectionStat,
  correctionStatSplitsSumToTotal,
  correctionRateMatchesStat,
  escalatedTier,
  correctionStatsTriggerEscalation,
  suggestionFollowsEscalationRule,
  taskTypesMatchProfiles,
  stringListIsAscending,
  TASK_PROFILE_PATH,
  TASK_PROFILE_ENVELOPE_KEYS,
  TASK_PROFILE_ENTRY_KEYS,
  TASK_PROFILE_ENTRY_OPTIONAL_KEYS,
  TASK_PROFILE_SUGGESTION_KEYS,
  TASK_PROFILE_SUGGESTION_OPTIONAL_KEYS,
  CORRECTION_STAT_KEYS,
  TASK_PROFILE_TIERS,
  TASK_PROFILE_CORRECTION_ESCALATION_RATE,
  TASK_PROFILE_CORRECTION_MIN_SAMPLES,
  TASK_PROFILE_ESCALATION_MIN_CONFIDENCE_BUMP,
  TASK_PROFILE_SCHEMA_VERSION,
  type CorrectionStat,
  type TaskProfileResponse,
  type TaskProfileEntry,
} from './taskProfile'

/**
 * 任务类型档案 + 人工修正反馈闭环的契约测试（2026-10-08，第九十八批）。
 *
 * 后端：`taskprofile/handler.go:128-136`（**第七种注册形态：方法内嵌路由模式 + 跨包**）
 * + `:141-180`（handleProfile）+ `:155-159`（profileView 匿名内嵌 ⇒ JSON 扁平）
 * + `taskprofile/suggest.go:45-105`（Suggest / escalateTier）+ `corrections.go:131-163`（Stats）
 * + `registry.go:25 :98-106 :128-136`，由 `admin/handler.go:1413` 用 `admin` 挂载。
 *
 * 重点是源文件头写明的十二件事 (1)…(12)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量。
 * ⚠️ 夹具逐字照抄 `registry.go:39-44` 的内置档案与 `suggest.go` 的升级逻辑。
 */

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

// ═══════════════════════════════════════════════════════════════════════════
// 夹具：逐字照抄 registry.go:39-44 与 suggest.go 的升级规则
// ═══════════════════════════════════════════════════════════════════════════

/**
 * ★ 内置档案「debugging」（照抄 `registry.go:41`）：
 * `PreferredTier: TierA, FallbackTiers: []string{TierB}, MinConfidence: 0.65`
 * ★ 它已在顶格 ⇒ 即便满足升级条件，`escalatedTier('tier-a')` 也是 `tier-a`（原地不动）。
 */
function debuggingEntryOf(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    task_type: 'debugging',
    description: 'Bug investigation, root cause analysis',
    preferred_tier: 'tier-a',
    fallback_tiers: ['tier-b'],
    min_confidence: 0.65,
    suggestion: {
      task_type: 'debugging',
      tier: 'tier-a',
      fallback_tiers: ['tier-b'],
      min_confidence: 0.65,
      tier_source: 'registry',
    },
    ...over,
  }
}

/** ★ 内置档案「coding」（照抄 `registry.go:42`）：tier-b ⇒ 升级后是 tier-a。 */
function codingEntryOf(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    task_type: 'coding',
    description: 'Feature implementation, API integration',
    preferred_tier: 'tier-b',
    fallback_tiers: ['tier-a', 'tier-c'],
    min_confidence: 0.75,
    suggestion: {
      task_type: 'coding',
      tier: 'tier-b',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.75,
      tier_source: 'registry',
    },
    ...over,
  }
}

/**
 * ★★ (11) 修正统计：照抄 `Stats` 的 SQL 语义 ——
 * `agrees` 与 `corrected` 是一对完备二分，所以 `agrees + corrected === total`，
 * 且 `correction_rate = corrected / total`。
 */
function statOf(taskType: string, total: number, agrees: number, corrected: number): Record<string, unknown> {
  return {
    task_type: taskType,
    total,
    agrees,
    corrected,
    correction_rate: corrected / total,
  }
}

/** ★ 完整的一份：键序照抄 `writeJSON(w, map[string]any{…})`（handler.go:174-179）。 */
function populated(): Record<string, unknown> {
  return {
    registry_version: '2026-10-08-overlay-3',
    schema_version: 1,
    // ★ (5) 升序：两个键都来自同一张 profiles 表并各自排序
    task_types: ['coding', 'debugging'],
    profiles: [codingEntryOf(), debuggingEntryOf()],
  }
}

/** ★ 空档案集：`make([]profileView, 0, 0)` ⇒ 是 `[]` 不是 `null`（见 (4)）。 */
function emptyProfiles(): Record<string, unknown> {
  return { registry_version: '', schema_version: 1, task_types: [], profiles: [] }
}

/** ★★ (12) 未知任务类型：`Suggest` 自造档案时**没有 Description** ⇒ 空串。 */
function unknownTypeEntryOf(): Record<string, unknown> {
  return {
    task_type: 'never-seen-task',
    description: '',
    preferred_tier: 'tier-b',
    fallback_tiers: ['tier-a', 'tier-c'],
    min_confidence: 0.7,
    suggestion: {
      task_type: 'never-seen-task',
      tier: 'tier-b',
      fallback_tiers: ['tier-a', 'tier-c'],
      min_confidence: 0.7,
      tier_source: 'registry',
    },
  }
}

// ═══════════════════════════════════════════════════════════════════════════
// 常量与路径
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · 常量与路径', () => {
  it('路径是 /api/admin/task-profile（taskprofile/handler.go:129）', () => {
    expect(TASK_PROFILE_PATH).toBe('/api/admin/task-profile')
  })

  it('顶层是手写 map 的 4 个键（handler.go:174-179）', () => {
    expect(TASK_PROFILE_ENVELOPE_KEYS).toHaveLength(4)
    expect(TASK_PROFILE_ENVELOPE_KEYS).toEqual(['registry_version', 'schema_version', 'task_types', 'profiles'])
  })

  it('profile 是 6 个恒在键（★ 匿名内嵌 ⇒ 扁平，见 handler.go:155-159）', () => {
    expect(TASK_PROFILE_ENTRY_KEYS).toHaveLength(6)
    expect(TASK_PROFILE_ENTRY_KEYS).toEqual([
      'task_type',
      'description',
      'preferred_tier',
      'fallback_tiers',
      'min_confidence',
      'suggestion',
    ])
  })

  it('profile 级只有一个 omitempty 键', () => {
    expect(TASK_PROFILE_ENTRY_OPTIONAL_KEYS).toEqual(['correction_stats'])
  })

  it('suggestion 是 5 个恒在键（suggest.go:29-37）', () => {
    expect(TASK_PROFILE_SUGGESTION_KEYS).toHaveLength(5)
    expect(TASK_PROFILE_SUGGESTION_KEYS).toEqual(['task_type', 'tier', 'fallback_tiers', 'min_confidence', 'tier_source'])
  })

  it('suggestion 也带一个同名 correction_stats（suggest.go:40）', () => {
    expect(TASK_PROFILE_SUGGESTION_OPTIONAL_KEYS).toEqual(['correction_stats'])
  })

  it('correction_stats 是 5 个恒在键（types.go:43-49）', () => {
    expect(CORRECTION_STAT_KEYS).toHaveLength(5)
    expect(CORRECTION_STAT_KEYS).toEqual(['task_type', 'total', 'agrees', 'corrected', 'correction_rate'])
  })

  it('三档 tier（types.go:24-26）', () => {
    expect(TASK_PROFILE_TIERS).toEqual(['tier-a', 'tier-b', 'tier-c'])
  })

  it('升级阈值是 0.3 / 5 / +0.05（suggest.go:20 :22 :24）', () => {
    expect(TASK_PROFILE_CORRECTION_ESCALATION_RATE).toBe(0.3)
    expect(TASK_PROFILE_CORRECTION_MIN_SAMPLES).toBe(5)
    expect(TASK_PROFILE_ESCALATION_MIN_CONFIDENCE_BUMP).toBe(0.05)
  })

  it('schema_version 是编译期常量 1（registry.go:25）', () => {
    expect(TASK_PROFILE_SCHEMA_VERSION).toBe(1)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 顶层形状
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · 顶层形状', () => {
  it('顶层是数组时抛错', () => {
    expect(() => unwrapTaskProfile([])).toThrow(/响应形状不符：期望裸对象，实得 array/)
  })

  it('顶层是 null 时抛错', () => {
    expect(() => unwrapTaskProfile(null)).toThrow(/响应形状不符：期望裸对象，实得 null/)
  })

  it('顶层是字符串时抛错', () => {
    expect(() => unwrapTaskProfile('ok')).toThrow(/响应形状不符：期望裸对象，实得 string/)
  })

  it('顶层是数字 42 时抛错并报 number', () => {
    expect(() => unwrapTaskProfile(42)).toThrow(/响应形状不符：期望裸对象，实得 number/)
  })

  // ★ 下面两条是「falsy 但不是 null」那一格。
  it('顶层是 0 时报 number 而不是 null', () => {
    expect(() => unwrapTaskProfile(0)).toThrow(/响应形状不符：期望裸对象，实得 number/)
  })

  it('顶层是空串时报 string 而不是 null', () => {
    expect(() => unwrapTaskProfile('')).toThrow(/响应形状不符：期望裸对象，实得 string/)
  })

  it('完整的一份通过（handler.go:174-179）', () => {
    const r = unwrapTaskProfile(populated())
    expect(r.schema_version).toBe(1)
    expect(r.profiles).toHaveLength(2)
  })

  // ★ (4) 与 §11.133 那个「零行即 null」正好相反。
  it('★ 空档案集是 [] 不是 null（handler.go:160 的 make 强制非 nil）', () => {
    const r = unwrapTaskProfile(emptyProfiles())
    expect(r.profiles).toEqual([])
    expect(r.task_types).toEqual([])
  })

  it('★ 未知任务类型（description 为空串）时通过（suggest.go:48-57）', () => {
    const r = unwrapTaskProfile({
      registry_version: '',
      schema_version: 1,
      task_types: ['never-seen-task'],
      profiles: [unknownTypeEntryOf()],
    })
    expect((r.profiles[0] as TaskProfileEntry).description).toBe('')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// 顶层缺键与类型
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · 顶层缺键与类型', () => {
  it('缺 registry_version 时抛错并点名 registry_version', () => {
    expect(() => unwrapTaskProfile(del(populated(), 'registry_version'))).toThrow(
      /任务档案 缺 1 个键（registry_version）/,
    )
  })

  it('缺 schema_version 时抛错并点名 schema_version', () => {
    expect(() => unwrapTaskProfile(del(populated(), 'schema_version'))).toThrow(
      /任务档案 缺 1 个键（schema_version）/,
    )
  })

  it('缺 task_types 时抛错并点名 task_types', () => {
    expect(() => unwrapTaskProfile(del(populated(), 'task_types'))).toThrow(/任务档案 缺 1 个键（task_types）/)
  })

  it('缺 profiles 时抛错并点名 profiles', () => {
    expect(() => unwrapTaskProfile(del(populated(), 'profiles'))).toThrow(/任务档案 缺 1 个键（profiles）/)
  })

  it('顶层同时缺两个键时一次点名两个', () => {
    const bad = del(del(populated(), 'task_types'), 'profiles')
    expect(() => unwrapTaskProfile(bad)).toThrow(/任务档案 缺 2 个键（task_types, profiles）/)
  })

  it('registry_version 是数字时抛错并点名 registry_version', () => {
    expect(() => unwrapTaskProfile({ ...populated(), registry_version: 7 })).toThrow(
      /registry_version 不是字符串/,
    )
  })

  it('schema_version 是字符串时抛错并点名 schema_version', () => {
    expect(() => unwrapTaskProfile({ ...populated(), schema_version: '1' })).toThrow(/schema_version 不是数字/)
  })

  it('task_types 是对象时抛错并点名 task_types', () => {
    expect(() => unwrapTaskProfile({ ...populated(), task_types: { a: 1 } })).toThrow(/task_types 不是数组/)
  })

  it('★ task_types 是 0 时抛错并报 type（falsy 但不是 null 那一格）', () => {
    expect(() => unwrapTaskProfile({ ...populated(), task_types: 0 })).toThrow(/task_types 不是数组/)
  })

  it('task_types 的第 1 项是数字时抛错并点名第 1 项', () => {
    expect(() => unwrapTaskProfile({ ...populated(), task_types: ['coding', 5] })).toThrow(
      /task_types 第 1 项不是字符串/,
    )
  })

  it('profiles 是字符串时抛错并点名 profiles', () => {
    expect(() => unwrapTaskProfile({ ...populated(), profiles: 'x' })).toThrow(/profiles 不是数组/)
  })

  it('★ profiles 是 0 时抛错并报 type（falsy 但不是 null 那一格）', () => {
    expect(() => unwrapTaskProfile({ ...populated(), profiles: 0 })).toThrow(/profiles 不是数组/)
  })

  it('profiles 里的元素坏掉时错误消息带下标', () => {
    const bad = { ...populated(), profiles: [del(codingEntryOf(), 'task_type')] }
    expect(() => unwrapTaskProfile(bad)).toThrow(/profiles\[0\] 缺 1 个键（task_type）/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// profile 的 6 个恒在键
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · profile 的恒在键', () => {
  it('完整的 debugging 档案通过（registry.go:41）', () => {
    const e = unwrapTaskProfileEntry(debuggingEntryOf(), 'r')
    expect(e.preferred_tier).toBe('tier-a')
    expect(e.fallback_tiers).toEqual(['tier-b'])
  })

  it('缺 task_type 时抛错并点名 task_type', () => {
    expect(() => unwrapTaskProfileEntry(del(debuggingEntryOf(), 'task_type'), 'r')).toThrow(
      /r 缺 1 个键（task_type）/,
    )
  })

  it('缺 description 时抛错并点名 description', () => {
    expect(() => unwrapTaskProfileEntry(del(debuggingEntryOf(), 'description'), 'r')).toThrow(
      /r 缺 1 个键（description）/,
    )
  })

  it('缺 preferred_tier 时抛错并点名 preferred_tier', () => {
    expect(() => unwrapTaskProfileEntry(del(debuggingEntryOf(), 'preferred_tier'), 'r')).toThrow(
      /r 缺 1 个键（preferred_tier）/,
    )
  })

  it('profile 顶层缺 fallback_tiers 时抛错并点名', () => {
    expect(() => unwrapTaskProfileEntry(del(debuggingEntryOf(), 'fallback_tiers'), 'r')).toThrow(
      /r 缺 1 个键（fallback_tiers）/,
    )
  })

  it('profile 顶层缺 min_confidence 时抛错并点名', () => {
    expect(() => unwrapTaskProfileEntry(del(debuggingEntryOf(), 'min_confidence'), 'r')).toThrow(
      /r 缺 1 个键（min_confidence）/,
    )
  })

  it('缺 suggestion 时抛错并点名 suggestion', () => {
    expect(() => unwrapTaskProfileEntry(del(debuggingEntryOf(), 'suggestion'), 'r')).toThrow(
      /r 缺 1 个键（suggestion）/,
    )
  })

  it('★ correction_stats 缺席时通过（omitempty，见 handler.go:157）', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf(), 'r')).not.toThrow()
  })

  it('profile 顶层 task_type 是数字时抛错并点名', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ task_type: 3 }), 'r')).toThrow(/r 的 task_type 不是字符串/)
  })

  it('description 是 null 时抛错并点名 description', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ description: null }), 'r')).toThrow(
      /r 的 description 不是字符串/,
    )
  })

  it('preferred_tier 是数字时抛错并点名 preferred_tier', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ preferred_tier: 1 }), 'r')).toThrow(
      /r 的 preferred_tier 不是字符串/,
    )
  })

  it('★ preferred_tier 是空串时抛错（空串不是合法 tier）', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ preferred_tier: '' }), 'r')).not.toThrow()
  })

  it('fallback_tiers 是对象时抛错并点名 fallback_tiers', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ fallback_tiers: {} }), 'r')).toThrow(
      /r 的 fallback_tiers 不是数组/,
    )
  })

  it('fallback_tiers 的第 0 项是数字时抛错并点名第 0 项', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ fallback_tiers: [7] }), 'r')).toThrow(
      /r 的 fallback_tiers 第 0 项不是字符串/,
    )
  })

  it('min_confidence 是字符串时抛错并点名 min_confidence', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ min_confidence: '0.65' }), 'r')).toThrow(
      /r 的 min_confidence 不是数字/,
    )
  })

  it('suggestion 是字符串时抛错并点名 suggestion', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ suggestion: 'x' }), 'r')).toThrow(
      /r 的 suggestion 响应形状不符/,
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// suggestion 的 5 个恒在键
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · suggestion 的恒在键', () => {
  it('完整的 suggestion 通过（suggest.go:29-37）', () => {
    const s = unwrapTaskProfileSuggestion(debuggingEntryOf().suggestion, 's')
    expect(s.tier).toBe('tier-a')
    expect(s.tier_source).toBe('registry')
  })

  it('缺 task_type 时抛错并点名 task_type', () => {
    const bad = del(debuggingEntryOf().suggestion as Record<string, unknown>, 'task_type')
    expect(() => unwrapTaskProfileSuggestion(bad, 's')).toThrow(/s 缺 1 个键（task_type）/)
  })

  it('缺 tier 时抛错并点名 tier', () => {
    const bad = del(debuggingEntryOf().suggestion as Record<string, unknown>, 'tier')
    expect(() => unwrapTaskProfileSuggestion(bad, 's')).toThrow(/s 缺 1 个键（tier）/)
  })

  it('suggestion 内缺 fallback_tiers 时抛错并点名', () => {
    const bad = del(debuggingEntryOf().suggestion as Record<string, unknown>, 'fallback_tiers')
    expect(() => unwrapTaskProfileSuggestion(bad, 's')).toThrow(/s 缺 1 个键（fallback_tiers）/)
  })

  it('suggestion 内缺 min_confidence 时抛错并点名', () => {
    const bad = del(debuggingEntryOf().suggestion as Record<string, unknown>, 'min_confidence')
    expect(() => unwrapTaskProfileSuggestion(bad, 's')).toThrow(/s 缺 1 个键（min_confidence）/)
  })

  it('缺 tier_source 时抛错并点名 tier_source', () => {
    const bad = del(debuggingEntryOf().suggestion as Record<string, unknown>, 'tier_source')
    expect(() => unwrapTaskProfileSuggestion(bad, 's')).toThrow(/s 缺 1 个键（tier_source）/)
  })

  it('suggestion 内 task_type 是数字时抛错并点名', () => {
    expect(() => unwrapTaskProfileSuggestion({ ...(debuggingEntryOf().suggestion as object), task_type: 1 }, 's')).toThrow(
      /s 的 task_type 不是字符串/,
    )
  })

  it('tier 是对象时抛错并点名 tier', () => {
    expect(() => unwrapTaskProfileSuggestion({ ...(debuggingEntryOf().suggestion as object), tier: {} }, 's')).toThrow(
      /s 的 tier 不是字符串/,
    )
  })

  it('tier_source 是数字时抛错并点名 tier_source', () => {
    expect(
      () => unwrapTaskProfileSuggestion({ ...(debuggingEntryOf().suggestion as object), tier_source: 1 }, 's'),
    ).toThrow(/s 的 tier_source 不是字符串/)
  })

  it('fallback_tiers 是 null 时抛错并点名 fallback_tiers', () => {
    expect(
      () => unwrapTaskProfileSuggestion({ ...(debuggingEntryOf().suggestion as object), fallback_tiers: null }, 's'),
    ).toThrow(/s 的 fallback_tiers 不是数组/)
  })

  it('fallback_tiers 的第 1 项是 null 时抛错并点名第 1 项', () => {
    expect(
      () =>
        unwrapTaskProfileSuggestion(
          { ...(debuggingEntryOf().suggestion as object), fallback_tiers: ['tier-b', null] },
          's',
        ),
    ).toThrow(/s 的 fallback_tiers 第 1 项不是字符串/)
  })

  it('min_confidence 是布尔时抛错并点名 min_confidence', () => {
    expect(
      () => unwrapTaskProfileSuggestion({ ...(debuggingEntryOf().suggestion as object), min_confidence: false }, 's'),
    ).toThrow(/s 的 min_confidence 不是数字/)
  })

  it('缺席时：suggestion 级的 correction_stats 直接通过', () => {
    expect(() => unwrapTaskProfileSuggestion(debuggingEntryOf().suggestion, 's')).not.toThrow()
  })

  it('★ suggestion 级的 correction_stats 键不全时被抓住', () => {
    const bad = { ...(debuggingEntryOf().suggestion as object), correction_stats: { total: 3 } }
    expect(() => unwrapTaskProfileSuggestion(bad, 's')).toThrow(/s 的 correction_stats 缺 4 个键/)
  })

  it('★ profile 级与 suggestion 级的 correction_stats 错误消息层级不同', () => {
    const atProfile = { ...debuggingEntryOf(), correction_stats: { total: 3 } }
    expect(() => unwrapTaskProfileEntry(atProfile, 'r')).toThrow(/r 的 correction_stats 缺 4 个键/)
    const atSuggestion = {
      ...debuggingEntryOf(),
      suggestion: { ...(debuggingEntryOf().suggestion as object), correction_stats: { total: 3 } },
    }
    expect(() => unwrapTaskProfileEntry(atSuggestion, 'r')).toThrow(/r 的 suggestion 的 correction_stats 缺 4 个键/)
  })

  it('存在时：suggestion 级的 correction_stats 逐键校验', () => {
    const withStat = {
      ...(debuggingEntryOf().suggestion as object),
      correction_stats: statOf('debugging', 10, 7, 3),
    }
    const s = unwrapTaskProfileSuggestion(withStat, 's')
    expect(s.correction_stats?.total).toBe(10)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// correction_stats 的 5 个恒在键
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · correction_stats', () => {
  it('完整的一份通过（types.go:43-49）', () => {
    const s = unwrapCorrectionStat(statOf('coding', 10, 7, 3), 'c')
    expect(s.correction_rate).toBe(0.3)
  })

  it('缺 task_type 时抛错并点名 task_type', () => {
    expect(() => unwrapCorrectionStat(del(statOf('coding', 10, 7, 3), 'task_type'), 'c')).toThrow(
      /c 缺 1 个键（task_type）/,
    )
  })

  it('缺 correction_rate 时抛错并点名 correction_rate', () => {
    expect(() => unwrapCorrectionStat(del(statOf('coding', 10, 7, 3), 'correction_rate'), 'c')).toThrow(
      /c 缺 1 个键（correction_rate）/,
    )
  })

  it('correction_stat 的 task_type 是数字时抛错并点名', () => {
    // ★ `statOf` 的形参类型已钉住 ⇒ 「类型错」要写成改键值，不能改实参位置。
    expect(() => unwrapCorrectionStat({ ...statOf('coding', 10, 7, 3), task_type: 5 }, 'c')).toThrow(
      /c 的 task_type 不是字符串/,
    )
  })

  it('total 是字符串时抛错并点名 total', () => {
    expect(() => unwrapCorrectionStat({ ...statOf('coding', 10, 7, 3), total: '10' }, 'c')).toThrow(
      /c 的 total 不是数字/,
    )
  })

  it('agrees 是 null 时抛错并点名 agrees', () => {
    expect(() => unwrapCorrectionStat(statOf('coding', 10, null as never, 3), 'c')).toThrow(/c 的 agrees 不是数字/)
  })

  it('corrected 是布尔时抛错并点名 corrected', () => {
    expect(() => unwrapCorrectionStat(statOf('coding', 10, 7, true as never), 'c')).toThrow(/c 的 corrected 不是数字/)
  })

  it('correction_rate 是字符串时抛错并点名 correction_rate', () => {
    const bad = { ...statOf('coding', 10, 7, 3), correction_rate: '0.3' }
    expect(() => unwrapCorrectionStat(bad, 'c')).toThrow(/c 的 correction_rate 不是数字/)
  })

  it('correction_stat 同时缺两个键时一次点名两个', () => {
    const bad = del(del(statOf('coding', 10, 7, 3), 'agrees'), 'corrected')
    expect(() => unwrapCorrectionStat(bad, 'c')).toThrow(/c 缺 2 个键（agrees, corrected）/)
  })

  it('profile 级的 correction_stats 存在时逐键校验', () => {
    const e = unwrapTaskProfileEntry(debuggingEntryOf({ correction_stats: statOf('debugging', 10, 7, 3) }), 'r')
    expect(e.correction_stats?.corrected).toBe(3)
  })

  it('★ profile 级的 correction_stats 类型错时被抓住（消息与 suggestion 级不同）', () => {
    expect(() => unwrapTaskProfileEntry(debuggingEntryOf({ correction_stats: { task_type: 'x' } }), 'r')).toThrow(
      /r 的 correction_stats 缺 4 个键/,
    )
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// fetch
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · fetch', () => {
  it('发 GET 到 /api/admin/task-profile 且不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(populated()))
    await fetchTaskProfile()
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/api/admin/task-profile')
    expect(url).not.toContain('?')
    expect(init.method).toBe('GET')
  })

  it('★ 路径里不含方法名（★ 方法在 mux 的路由模式里，不在 URL 里）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(populated()))
    await fetchTaskProfile()
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('GET')
  })

  it('完整的一份被解出', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(populated()))
    const r = await fetchTaskProfile()
    expect(r.task_types).toEqual(['coding', 'debugging'])
    expect(r.profiles).toHaveLength(2)
  })

  it('响应形状不符时 Promise reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse('nope'))
    await expect(fetchTaskProfile()).rejects.toThrow(/响应形状不符/)
  })

  it('键缺时 Promise reject', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(del(populated(), 'profiles')))
    await expect(fetchTaskProfile()).rejects.toThrow(/缺 1 个键（profiles）/)
  })

  it('★ profile 元素坏掉时 Promise reject 且消息带下标', async () => {
    // ★ 删的是 **profile 顶层**的键（`preferred_tier`）；`tier` 住在 suggestion 里，删它无效。
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...populated(), profiles: [del(codingEntryOf(), 'preferred_tier') as never] }))
    await expect(fetchTaskProfile()).rejects.toThrow(/profiles\[0\] 缺 1 个键（preferred_tier）/)
  })

  it('★ suggestion 坏掉时 Promise reject 且消息带 suggestion 层级', async () => {
    const bad = codingEntryOf({ suggestion: { task_type: 'coding', tier: 'tier-b' } as never })
    fetchMock.mockResolvedValueOnce(jsonResponse({ ...populated(), profiles: [bad] }))
    await expect(fetchTaskProfile()).rejects.toThrow(/profiles\[0\] 的 suggestion 缺 3 个键/)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (11) 修正统计的自洽
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · 修正统计自洽 (11)', () => {
  it('agrees + corrected 等于 total（10 = 7 + 3）', () => {
    expect(correctionStatSplitsSumToTotal(unwrapCorrectionStat(statOf('coding', 10, 7, 3), 'c'))).toBe(true)
  })

  it('★ 全部纠正时 agrees 为 0 仍成立（10 = 0 + 10）', () => {
    expect(correctionStatSplitsSumToTotal(unwrapCorrectionStat(statOf('coding', 10, 0, 10), 'c'))).toBe(true)
  })

  it('★ 全部一致时 corrected 为 0 仍成立（5 = 5 + 0）', () => {
    expect(correctionStatSplitsSumToTotal(unwrapCorrectionStat(statOf('coding', 5, 5, 0), 'c'))).toBe(true)
  })

  it('★ 单条样本也成立（1 = 0 + 1）', () => {
    expect(correctionStatSplitsSumToTotal(unwrapCorrectionStat(statOf('coding', 1, 0, 1), 'c'))).toBe(true)
  })

  it('★ 和比 total 多 1 时判为假', () => {
    expect(correctionStatSplitsSumToTotal(unwrapCorrectionStat(statOf('coding', 10, 8, 3), 'c'))).toBe(false)
  })

  it('★ 和比 total 少 1 时判为假', () => {
    expect(correctionStatSplitsSumToTotal(unwrapCorrectionStat(statOf('coding', 10, 6, 3), 'c'))).toBe(false)
  })

  it('correction_rate 等于 corrected / total（3/10）', () => {
    expect(correctionRateMatchesStat(unwrapCorrectionStat(statOf('coding', 10, 7, 3), 'c'))).toBe(true)
  })

  it('★ correction_rate 为 0 时成立（0/10）', () => {
    expect(correctionRateMatchesStat(unwrapCorrectionStat(statOf('coding', 10, 10, 0), 'c'))).toBe(true)
  })

  it('★ correction_rate 为 1 时成立（10/10）', () => {
    expect(correctionRateMatchesStat(unwrapCorrectionStat(statOf('coding', 10, 0, 10), 'c'))).toBe(true)
  })

  it('★ 1/3 这种非整除的比率也成立', () => {
    expect(correctionRateMatchesStat(unwrapCorrectionStat(statOf('coding', 3, 2, 1), 'c'))).toBe(true)
  })

  it('★ rate 是 0.1（≠ 常量 0.3）但等于 1/10 时仍判为真', () => {
    // ★★★ 这条是「rate 判据退化成 === 0.3」那条变异的唯一区分点：
    //   我原来的样本是 3/10 = 0.3，**正好等于那个常量** ⇒ 两种实现同解。
    expect(correctionRateMatchesStat(unwrapCorrectionStat(statOf('coding', 10, 9, 1), 'c'))).toBe(true)
  })

  it('★ rate 是 0.5 时判为真（等于 5/10）', () => {
    expect(correctionRateMatchesStat(unwrapCorrectionStat(statOf('coding', 10, 5, 5), 'c'))).toBe(true)
  })

  it('★ rate 写成 0.33 而非精确值时判为假', () => {
    const s = { ...statOf('coding', 3, 2, 1), correction_rate: 0.33 }
    expect(correctionRateMatchesStat(unwrapCorrectionStat(s, 'c'))).toBe(false)
  })

  it('★ rate 被当成百分比（30 而不是 0.3）时判为假', () => {
    const s = { ...statOf('coding', 10, 7, 3), correction_rate: 30 }
    expect(correctionRateMatchesStat(unwrapCorrectionStat(s, 'c'))).toBe(false)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (10) tier 升级表
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · tier 升级表 (10)', () => {
  it('tier-c 升到 tier-b（suggest.go:98-99）', () => {
    expect(escalatedTier('tier-c')).toBe('tier-b')
  })

  it('tier-b 升到 tier-a（suggest.go:100-101）', () => {
    expect(escalatedTier('tier-b')).toBe('tier-a')
  })

  it('★ tier-a 原地不动（suggest.go:102-103 的 default 分支）', () => {
    expect(escalatedTier('tier-a')).toBe('tier-a')
  })

  it('★ 未知档位也落到 tier-a（default 分支，不是抛错）', () => {
    expect(escalatedTier('tier-z')).toBe('tier-a')
  })

  it('★ 空串也落到 tier-a', () => {
    expect(escalatedTier('')).toBe('tier-a')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (9) 升级规则与建议的一致性
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · 升级规则一致性 (9)', () => {
  it('★ 没有 correction_stats 时不触发升级', () => {
    expect(correctionStatsTriggerEscalation(undefined)).toBe(false)
  })

  it('★ 样本数不足 5（4 条、rate 1.0）时不触发（suggest.go:69 的 && 短路）', () => {
    expect(correctionStatsTriggerEscalation(unwrapCorrectionStat(statOf('coding', 4, 0, 4), 'c'))).toBe(false)
  })

  it('★ 样本数正好 5 且 rate 1.0 时触发（≥ 不是 >）', () => {
    expect(correctionStatsTriggerEscalation(unwrapCorrectionStat(statOf('coding', 5, 0, 5), 'c'))).toBe(true)
  })

  it('★ rate 正好 0.3 时触发（≥ 不是 >）', () => {
    expect(correctionStatsTriggerEscalation(unwrapCorrectionStat(statOf('coding', 10, 7, 3), 'c'))).toBe(true)
  })

  it('★ rate 略低于 0.3（3/11 ≈ 0.2727）时不触发', () => {
    expect(correctionStatsTriggerEscalation(unwrapCorrectionStat(statOf('coding', 11, 8, 3), 'c'))).toBe(false)
  })

  it('★ 无升级时 suggestion 与 preferred_tier 一致（tier-source=registry）', () => {
    const e = unwrapTaskProfileEntry(codingEntryOf(), 'r')
    expect(suggestionFollowsEscalationRule(e)).toBe(true)
  })

  it('★ 有升级时 tier 上调一级且 tier-source=correction_escalation', () => {
    const upgraded = {
      ...codingEntryOf(),
      correction_stats: statOf('coding', 10, 7, 3),
      suggestion: {
        task_type: 'coding',
        tier: 'tier-a',
        fallback_tiers: ['tier-a', 'tier-c'],
        min_confidence: 0.8,
        tier_source: 'correction_escalation',
        correction_stats: statOf('coding', 10, 7, 3),
      },
    }
    const e = unwrapTaskProfileEntry(upgraded, 'r')
    expect(e.preferred_tier).toBe('tier-b')
    expect(e.suggestion.tier).toBe('tier-a')
    expect(suggestionFollowsEscalationRule(e)).toBe(true)
  })

  it('★ ★ 顶格的 tier-a 即使触发升级也留在 tier-a（suggest.go:102-103）', () => {
    const topTier = {
      ...debuggingEntryOf(),
      correction_stats: statOf('debugging', 10, 7, 3),
      suggestion: {
        task_type: 'debugging',
        tier: 'tier-a',
        fallback_tiers: ['tier-b'],
        min_confidence: 0.7,
        tier_source: 'correction_escalation',
        correction_stats: statOf('debugging', 10, 7, 3),
      },
    }
    const e = unwrapTaskProfileEntry(topTier, 'r')
    expect(e.suggestion.tier).toBe(e.preferred_tier)
    expect(suggestionFollowsEscalationRule(e)).toBe(true)
  })

  it('★ 「tier 没变但 source 变了」仍判为自洽（两者不是一回事）', () => {
    const topTier = {
      ...debuggingEntryOf(),
      correction_stats: statOf('debugging', 10, 7, 3),
      suggestion: {
        task_type: 'debugging',
        tier: 'tier-a',
        fallback_tiers: ['tier-b'],
        min_confidence: 0.7,
        tier_source: 'correction_escalation',
      },
    }
    expect(suggestionFollowsEscalationRule(unwrapTaskProfileEntry(topTier, 'r'))).toBe(true)
  })

  it('★ 未触发升级却写了 correction_escalation 时判为不自洽', () => {
    const bad = {
      ...codingEntryOf(),
      suggestion: { ...(codingEntryOf().suggestion as object), tier_source: 'correction_escalation' },
    }
    expect(suggestionFollowsEscalationRule(unwrapTaskProfileEntry(bad, 'r'))).toBe(false)
  })

  it('★ 触发了升级却没上调 tier 时判为不自洽', () => {
    const bad = {
      ...codingEntryOf(),
      correction_stats: statOf('coding', 10, 7, 3),
      suggestion: { ...(codingEntryOf().suggestion as object), tier_source: 'correction_escalation' },
    }
    expect(suggestionFollowsEscalationRule(unwrapTaskProfileEntry(bad, 'r'))).toBe(false)
  })

  it('★ 未触发升级却把 tier 改了（凭空升级）时判为不自洽', () => {
    const bad = {
      ...codingEntryOf(),
      suggestion: { ...(codingEntryOf().suggestion as object), tier: 'tier-a' },
    }
    expect(suggestionFollowsEscalationRule(unwrapTaskProfileEntry(bad, 'r'))).toBe(false)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
// (5) 两个数组同集合且都升序
// ═══════════════════════════════════════════════════════════════════════════

describe('任务档案 · task_types 与 profiles 同集合 (5)', () => {
  it('完整的一份逐项相同', () => {
    expect(taskTypesMatchProfiles(unwrapTaskProfile(populated()))).toBe(true)
  })

  it('空档案集时也成立（两个都是空）', () => {
    expect(taskTypesMatchProfiles(unwrapTaskProfile(emptyProfiles()))).toBe(true)
  })

  it('★ 顺序被打乱时判为不同', () => {
    const bad = { ...populated(), task_types: ['debugging', 'coding'] }
    expect(taskTypesMatchProfiles(unwrapTaskProfile(bad))).toBe(false)
  })

  it('★ task_types 少一项时判为不同（长度不等）', () => {
    const bad = { ...populated(), task_types: ['coding'] }
    expect(taskTypesMatchProfiles(unwrapTaskProfile(bad))).toBe(false)
  })

  it('★ 首项相同但次项不同也判为不同（★ 只比首项的变异只有这一格抓得住）', () => {
    const bad = { ...populated(), task_types: ['coding', 'summary'] }
    expect(taskTypesMatchProfiles(unwrapTaskProfile(bad))).toBe(false)
  })

  it('★ task_types 多一项时判为不同', () => {
    const bad = { ...populated(), task_types: ['coding', 'debugging', 'summary'] }
    expect(taskTypesMatchProfiles(unwrapTaskProfile(bad))).toBe(false)
  })

  it('★ 升序的 task_types 判为真（sort.Strings 的字节序）', () => {
    expect(stringListIsAscending(['coding', 'debugging', 'summary'])).toBe(true)
  })

  it('★ 降序的 task_types 判为假', () => {
    expect(stringListIsAscending(['summary', 'debugging', 'coding'])).toBe(false)
  })

  it('★ 有重复项时判为假', () => {
    expect(stringListIsAscending(['coding', 'coding'])).toBe(false)
  })

  it('★ 空数组判为真（真空真，不是漏判）', () => {
    expect(stringListIsAscending([])).toBe(true)
  })

  it('★ 单项数组判为真', () => {
    expect(stringListIsAscending(['coding'])).toBe(true)
  })

  it('★ 大小写混排按字节序（Q 在 a 之前）', () => {
    expect(stringListIsAscending(['Q', 'a'])).toBe(true)
  })

  it('★ 大小写反序判为假', () => {
    expect(stringListIsAscending(['a', 'Q'])).toBe(false)
  })

  it('★ 含空串时升序仍判为真（空串最小）', () => {
    expect(stringListIsAscending(['', 'coding'])).toBe(true)
  })

  // ★ 类型级判据：键的类型必须与后端一致，写错会让 vue-tsc 报错。
  it('解包结果的键类型与后端一致（task_types 是可变 string[]）', () => {
    const r: TaskProfileResponse = unwrapTaskProfile(populated())
    const names: string[] = r.task_types
    const first: CorrectionStat | undefined = (r.profiles[0] as TaskProfileEntry).correction_stats
    expect(names).toHaveLength(2)
    expect(first).toBeUndefined()
  })
})
