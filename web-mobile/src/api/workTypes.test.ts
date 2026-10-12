import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchWorkTypes,
  fetchWorkType,
  fetchWorkTypeStats,
  fetchL1TaskTypes,
  unwrapWorkTypes,
  unwrapWorkType,
  unwrapWorkTypeStats,
  unwrapL1TaskTypes,
  workTypesAreSortedBySortOrderThenKey,
  workTypeHasModelRoutes,
  workTypeModelRoutesKeyIsAbsent,
  l1TaskTypesHasAtLeastCanonicalEight,
  l1TaskTypesStartWithCanonicalInOrder,
  l1TaskTypeIsExtra,
  l1TaskTypeIsCanonical,
  l1TaskTypeCountsAreAllZero,
  l1TaskTypeCountsHaveSignal,
  workTypeStatCountIsDerivedConsistently,
  workTypeStatPrefersDirectWhenPresent,
  workTypeStatsShareL1Proxy,
  workTypeTopModelsAreNonAscending,
  workTypeTopModelsWithinLimit,
  modelRoutesAreOrderedByTierThenWeight,
  modelRouteTierIsKnown,
  workTypeErrorCodeIs,
  workTypeErrorBodyKeys,
  workTypesIncludeDisabledIsEffective,
  workTypePathWithTrailingSlashIsEquivalent,
  WORK_TYPES_PATH,
  WORK_TYPES_ERROR_CODES,
  WORK_TYPES_STATS_WINDOW_HOURS,
  WORK_TYPES_TOP_MODELS_LIMIT,
  WORK_TYPES_DEFAULT_PROFILES,
  WORK_TYPES_ROUTE_TIERS,
  WORK_TYPES_EXTRA_L1_ICON,
  WORK_TYPES_DEFAULT_PROFILE,
  WORK_TYPES_TASK_QUALITY_MIN,
  WORK_TYPES_TASK_QUALITY_MAX,
  WORK_TYPES_CANONICAL_L1_KEYS,
  WORK_TYPE_CONFIG_KEYS,
  WORK_TYPE_CONFIG_REQUIRED_KEYS,
  WORK_TYPE_CONFIG_OPTIONAL_KEYS,
  MODEL_ROUTE_KEYS,
  WORK_TYPE_STAT_ENTRY_KEYS,
  WORK_TYPE_STATS_KEYS,
  L1_TASK_TYPE_META_KEYS,
  WORK_TYPE_TOP_MODEL_KEYS,
  type WorkTypeConfig,
  type ModelRoute,
  type WorkTypeStatEntry,
  type WorkTypeStats,
  type L1TaskTypeMeta,
  type L1TaskTypesResponse,
} from './workTypes'

/**
 * 工作类型的契约测试（2026-10-08，第九十一批）。
 *
 * 后端：`admin/work_types.go:50-55`（`mux.HandleFunc` 在另一个文件里）
 * 由 `admin/handler.go:1433-1434` 用 **`h.superAdmin`** 挂载
 * + `deploy/sql/schemas/baseline/01-schema.sql:19408-19452`。
 *
 * 重点是源文件头写明的十九件事 (1)…(19)。带 ★ 的自校验判据都能被变异打掉。
 * ⚠️ 全部用例标题**字面量**写（不用 `it.each`）—— 变异 harness 靠标题取锚点。
 * ⚠️ 所有「逐个都要检查」的循环都刻意用**字面量数组**，不用被测常量。
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

// ── 夹具：逐字照抄 `admin/work_types.go:502-528` 与 `01-schema.sql` ──

function routeOf(over: Partial<ModelRoute> = {}): ModelRoute {
  return {
    id: 11,
    canonical_name: 'gpt-4o',
    weight: 1,
    min_score: 0,
    enabled: true,
    tier: 'primary',
    task_quality_score: 80,
    ...over,
  }
}

function wtOf(over: Partial<WorkTypeConfig> = {}): WorkTypeConfig {
  return {
    key: 'coding',
    label: '代码生成',
    category: '研发',
    l1_task_type: 'code',
    default_profile: 'smart',
    tags: ['code'],
    prompt_keywords: ['写代码'],
    enabled: true,
    sort_order: 1,
    updated_at: '2026-10-07T12:00:00Z',
    ...over,
  }
}

function statOf(over: Partial<WorkTypeStatEntry> = {}): WorkTypeStatEntry {
  return {
    key: 'coding',
    label: '代码生成',
    category: '研发',
    l1_task_type: 'code',
    count_24h: 40,
    count_direct: 40,
    count_l1_proxy: 120,
    ...over,
  }
}

function statsOf(over: Partial<WorkTypeStats> = {}): WorkTypeStats {
  return {
    window_hours: 24,
    by_work_type: { coding: statOf() },
    by_l1_task: { code: 120 },
    total_auto: 200,
    total_specified: 30,
    top_models: [
      { model: 'gpt-4o', count: 90 },
      { model: 'claude-sonnet', count: 40 },
    ],
    sync_meta: { source: 'acc', enabled_count: 8, route_count: 30, acc_configured: true },
    ...over,
  }
}

function l1Of(over: Partial<L1TaskTypeMeta> = {}): L1TaskTypeMeta {
  return { key: 'chat', label: '通用对话', icon: '💬', count: 3, ...over }
}

/** canonical 八项 + 一个 DB 新增项（后端追加在末尾）。 */
function l1ListOf(over: Partial<L1TaskTypesResponse> = {}): L1TaskTypesResponse {
  const canonical: L1TaskTypeMeta[] = [
    l1Of({ key: 'chat', label: '通用对话', icon: '💬', count: 3 }),
    l1Of({ key: 'reasoning', label: '逻辑推理', icon: '🧠', count: 1 }),
    l1Of({ key: 'code', label: '代码', icon: '💻', count: 5 }),
    l1Of({ key: 'agent', label: 'Agent', icon: '🤖', count: 0 }),
    l1Of({ key: 'creative', label: '创意', icon: '✍️', count: 0 }),
    l1Of({ key: 'long_context', label: '长文档', icon: '📚', count: 0 }),
    l1Of({ key: 'vision', label: '视觉', icon: '👁️', count: 0 }),
    l1Of({ key: 'function_call', label: '函数调用', icon: '🔧', count: 0 }),
    l1Of({ key: 'legacy_rag', label: 'legacy_rag', icon: WORK_TYPES_EXTRA_L1_ICON, count: 2 }),
  ]
  return { items: canonical, ...over }
}

// ══════════════════════════════════════════════════════════════════════════
// 清单端点（顶层裸数组）
// ══════════════════════════════════════════════════════════════════════════

describe('清单端点解包（顶层裸数组）', () => {
  it('★ 满配清单被放行', () => {
    expect(unwrapWorkTypes([wtOf()])).toHaveLength(1)
  })

  it('★ ★★ 空数组 ⇒ 放行（make([]workTypeConfig, 0)，空时是 [] 不是 null）', () => {
    expect(unwrapWorkTypes([])).toEqual([])
  })

  it('★ ★★ 顶层是对象 ⇒ 抛', () => {
    expect(() => unwrapWorkTypes({ items: [] } as never)).toThrow(/期望顶层裸数组，实得 object/)
  })

  it('★ ★ 顶层是 null ⇒ 抛', () => {
    expect(() => unwrapWorkTypes(null)).toThrow(/期望顶层裸数组，实得 null/)
  })

  // ★ 与上面的清单解包器**对称的一条**：同一个 unwrapWorkTypeConfig，
  //   但 where 标签不同（`工作类型` vs `工作类型清单[0]`）⇒ 报错必须能分辨是哪条路径。
  it('★ ★ 单项解包器用独立的 where 标签（不是「工作类型清单[0]」）', () => {
    expect(unwrapWorkType(wtOf())).toBeTruthy()
    expect(() => unwrapWorkType(del(wtOf() as never, 'tags') as never)).toThrow(/^工作类型 缺 1 个键/)
  })

  it('★ ★★ 十个恒在键逐个都要检查', () => {
    const keys = [
      'key', 'label', 'category', 'l1_task_type', 'default_profile',
      'tags', 'prompt_keywords', 'enabled', 'sort_order', 'updated_at',
    ]
    for (const k of keys) {
      expect(() => unwrapWorkTypes([del(wtOf() as never, k) as never])).toThrow(/工作类型清单\[0\] 缺 1 个键/)
    }
  })

  it('★ ★★ 六个字符串键逐个都要校验', () => {
    const keys = ['key', 'label', 'category', 'l1_task_type', 'default_profile', 'updated_at'] as const
    for (const k of keys) {
      expect(() => unwrapWorkTypes([{ ...wtOf(), [k]: 1 } as never])).toThrow(
        new RegExp(`工作类型清单\\[0\\] 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ enabled 不是布尔 ⇒ 抛', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), enabled: 'yes' } as never])).toThrow(
      /工作类型清单\[0\] 的 enabled 不是布尔/,
    )
  })

  it('★ ★ sort_order 不是数字 ⇒ 抛', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), sort_order: '1' } as never])).toThrow(
      /工作类型清单\[0\] 的 sort_order 不是数字/,
    )
  })

  it('★ ★★ tags 与 prompt_keywords 逐个都要是数组（NOT NULL 恒为数组）', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), tags: null } as never])).toThrow(/的 tags 不是数组/)
    expect(() => unwrapWorkTypes([{ ...wtOf(), prompt_keywords: null } as never])).toThrow(
      /的 prompt_keywords 不是数组/,
    )
  })

  it('★ tags 是空数组 ⇒ 放行（DEFAULT 是空数组而不是 null）', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), tags: [], prompt_keywords: [] }])).not.toThrow()
  })

  it('★ ★ 四个 omitempty 键全部缺席也是合法形状', () => {
    expect(() => unwrapWorkTypes([wtOf()])).not.toThrow()
  })

  it('★ ★ system_prompt 存在但不是字符串 ⇒ 抛', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), system_prompt: 1 } as never])).toThrow(/的 system_prompt 不是字符串/)
  })

  it('★ ★ acc_task_type 存在但不是字符串 ⇒ 抛', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), acc_task_type: 1 } as never])).toThrow(/的 acc_task_type 不是字符串/)
  })

  it('★ synced_from_acc_at 存在但不是字符串 ⇒ 抛', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), synced_from_acc_at: 1 } as never])).toThrow(
      /的 synced_from_acc_at 不是字符串/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (5) model_routes 三态 + (13)(14) 路由字段
// ══════════════════════════════════════════════════════════════════════════

describe('(5)(13)(14) model_routes 三态与路由字段', () => {
  it('★ ★★ model_routes 是 null ⇒ 放行（omitempty + 无路由 ⇒ 键消失，但 null 也合法）', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: null }])).not.toThrow()
  })

  it('★ ★★ model_routes 是数组 ⇒ 放行', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: [routeOf()] }])).not.toThrow()
  })

  it('★ ★ model_routes 既不是数组也不是 null ⇒ 抛', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: 'x' } as never])).toThrow(
      /的 model_routes 不是数组也不是 null/,
    )
  })

  it('★ ★★ 路由缺一个键 ⇒ 抛并点名下标', () => {
    const bad = del(routeOf() as never, 'tier')
    expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: [bad as never] }])).toThrow(
      /model_routes\[0\] 缺 1 个键（tier）/,
    )
  })

  it('★ ★★ 路由的六个恒在键逐个都要检查', () => {
    const keys = ['canonical_name', 'weight', 'min_score', 'enabled', 'tier', 'task_quality_score']
    for (const k of keys) {
      const bad = del(routeOf() as never, k)
      expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: [bad as never] }])).toThrow(
        /model_routes\[0\] 缺 1 个键/,
      )
    }
  })

  it('★ ★★ 路由的三个数字键逐个都要校验', () => {
    for (const k of ['weight', 'min_score', 'task_quality_score'] as const) {
      expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: [{ ...routeOf(), [k]: 'x' } as never] }])).toThrow(
        new RegExp(`model_routes\\[0\\] 的 ${k} 不是数字`),
      )
    }
  })

  it('★ ★★ 路由的两个字符串键逐个都要校验', () => {
    for (const k of ['canonical_name', 'tier'] as const) {
      expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: [{ ...routeOf(), [k]: 1 } as never] }])).toThrow(
        new RegExp(`model_routes\\[0\\] 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ ★★ id 缺席 ⇒ 放行（int + omitempty，id 为 0 时键消失）', () => {
    const { id, ...noId } = routeOf()
    void id
    expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: [noId as ModelRoute] }])).not.toThrow()
  })

  it('★ ★ id 存在但不是数字 ⇒ 抛', () => {
    expect(() => unwrapWorkTypes([{ ...wtOf(), model_routes: [{ ...routeOf(), id: 'x' } as never] }])).toThrow(
      /model_routes\[0\] 的 id 不是数字/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 统计端点解包
// ══════════════════════════════════════════════════════════════════════════

describe('统计端点解包', () => {
  it('★ 满配统计被放行（七键）', () => {
    expect(unwrapWorkTypeStats(statsOf())).toBeTruthy()
  })

  it('★ ★★ 七个顶层键逐个都要检查', () => {
    const keys = [
      'window_hours', 'by_work_type', 'by_l1_task', 'total_auto',
      'total_specified', 'top_models', 'sync_meta',
    ]
    for (const k of keys) {
      expect(() => unwrapWorkTypeStats(del(statsOf() as never, k))).toThrow(/工作类型统计 缺 1 个键/)
    }
  })

  it('★ ★★ 三个数字顶层键逐个都要校验', () => {
    expect(() => unwrapWorkTypeStats({ ...statsOf(), window_hours: '24' } as never)).toThrow(
      /window_hours 不是数字/,
    )
    expect(() => unwrapWorkTypeStats({ ...statsOf(), total_auto: '1' } as never)).toThrow(/total_auto 不是数字/)
    expect(() => unwrapWorkTypeStats({ ...statsOf(), total_specified: '1' } as never)).toThrow(
      /total_specified 不是数字/,
    )
  })

  it('★ by_work_type 不是对象 ⇒ 抛', () => {
    expect(() => unwrapWorkTypeStats(statsOf({ by_work_type: [] as never }))).toThrow(/by_work_type 不是对象/)
  })

  it('★ ★★ 统计项的七个键逐个都要检查', () => {
    const keys = ['key', 'label', 'category', 'l1_task_type', 'count_24h', 'count_direct', 'count_l1_proxy']
    for (const k of keys) {
      const bad = del(statOf() as never, k)
      expect(() => unwrapWorkTypeStats(statsOf({ by_work_type: { coding: bad as never } }))).toThrow(
        /by_work_type\[coding\] 缺 1 个键/,
      )
    }
  })

  it('★ ★★ 统计项的四个字符串键逐个都要校验', () => {
    for (const k of ['key', 'label', 'category', 'l1_task_type'] as const) {
      const bad = { ...statOf(), [k]: 1 } as never
      expect(() => unwrapWorkTypeStats(statsOf({ by_work_type: { coding: bad } }))).toThrow(
        new RegExp(`by_work_type\\[coding\\] 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ ★★ 统计项的三个计数逐个都要校验', () => {
    for (const k of ['count_24h', 'count_direct', 'count_l1_proxy'] as const) {
      const bad = { ...statOf(), [k]: 'x' } as never
      expect(() => unwrapWorkTypeStats(statsOf({ by_work_type: { coding: bad } }))).toThrow(
        new RegExp(`by_work_type\\[coding\\] 的 ${k} 不是数字`),
      )
    }
  })

  it('★ ★ by_l1_task 的值不是数字 ⇒ 抛', () => {
    expect(() => unwrapWorkTypeStats(statsOf({ by_l1_task: { code: '5' } as never }))).toThrow(
      /by_l1_task 的 code 不是数字/,
    )
  })

  it('★ top_models 不是数组 ⇒ 抛', () => {
    expect(() => unwrapWorkTypeStats(statsOf({ top_models: null as never }))).toThrow(/top_models 不是数组/)
  })

  it('★ ★ top_models 项缺一个键 ⇒ 抛', () => {
    const bad = del({ model: 'gpt-4o', count: 9 } as never, 'count')
    expect(() => unwrapWorkTypeStats(statsOf({ top_models: [bad as never] }))).toThrow(
      /top_models\[0\] 缺 1 个键（count）/,
    )
  })

  it('★ ★ top_models 的两个字段逐个都要校验', () => {
    expect(() => unwrapWorkTypeStats(statsOf({ top_models: [{ model: 1, count: 9 } as never] }))).toThrow(
      /top_models\[0\] 的 model 不是字符串/,
    )
    expect(() => unwrapWorkTypeStats(statsOf({ top_models: [{ model: 'a', count: '9' } as never] }))).toThrow(
      /top_models\[0\] 的 count 不是数字/,
    )
  })

  it('★ ★ sync_meta 不是对象 ⇒ 抛', () => {
    expect(() => unwrapWorkTypeStats(statsOf({ sync_meta: null as never }))).toThrow(/sync_meta 响应形状不符/)
  })

  it('★ ★★ sync_meta 的四个键逐个都要校验', () => {
    expect(() => unwrapWorkTypeStats(statsOf({ sync_meta: { ...statsOf().sync_meta, source: 1 } as never }))).toThrow(
      /sync_meta 的 source 不是字符串/,
    )
    expect(() =>
      unwrapWorkTypeStats(statsOf({ sync_meta: { ...statsOf().sync_meta, enabled_count: '1' } as never })),
    ).toThrow(/enabled_count 不是数字/)
    expect(() =>
      unwrapWorkTypeStats(statsOf({ sync_meta: { ...statsOf().sync_meta, route_count: '1' } as never })),
    ).toThrow(/route_count 不是数字/)
    expect(() =>
      unwrapWorkTypeStats(statsOf({ sync_meta: { ...statsOf().sync_meta, acc_configured: 1 } as never })),
    ).toThrow(/acc_configured 不是布尔/)
  })

  it('★ ★★ 数组冒充对象 ⇒ 抛「形状不符」而不是「缺键」', () => {
    expect(() => unwrapWorkTypeStats(statsOf({ sync_meta: [] as never }))).toThrow(
      /sync_meta 响应形状不符：期望裸对象，实得 array/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// L1 任务类型端点解包
// ══════════════════════════════════════════════════════════════════════════

describe('L1 任务类型端点解包', () => {
  it('★ 满配列表被放行（canonical 八项 + 一个新增项）', () => {
    expect(unwrapL1TaskTypes(l1ListOf())).toBeTruthy()
  })

  it('★ 顶层缺 items ⇒ 抛', () => {
    expect(() => unwrapL1TaskTypes({})).toThrow(/L1 任务类型 缺 1 个键（items）/)
  })

  it('★ items 不是数组 ⇒ 抛', () => {
    expect(() => unwrapL1TaskTypes({ items: null })).toThrow(/L1 任务类型 的 items 不是数组/)
  })

  it('★ ★★ 四个键逐个都要检查', () => {
    for (const k of ['key', 'label', 'icon', 'count']) {
      const bad = del(l1Of() as never, k)
      expect(() => unwrapL1TaskTypes({ items: [bad as never] })).toThrow(/items\[0\] 缺 1 个键/)
    }
  })

  it('★ ★★ 三个字符串键逐个都要校验', () => {
    for (const k of ['key', 'label', 'icon'] as const) {
      expect(() => unwrapL1TaskTypes({ items: [{ ...l1Of(), [k]: 1 } as never] })).toThrow(
        new RegExp(`items\\[0\\] 的 ${k} 不是字符串`),
      )
    }
  })

  it('★ count 不是数字 ⇒ 抛', () => {
    expect(() => unwrapL1TaskTypes({ items: [{ ...l1Of(), count: '3' } as never] })).toThrow(
      /items\[0\] 的 count 不是数字/,
    )
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (4)(5) 排序与三态
// ══════════════════════════════════════════════════════════════════════════

describe('(4)(5) 排序与 model_routes 三态', () => {
  it('★ ★★ 清单按 sort_order 再 key 严格升序 ⇒ 成立', () => {
    const rows = [wtOf({ key: 'a', sort_order: 1 }), wtOf({ key: 'b', sort_order: 1 }), wtOf({ key: 'c', sort_order: 2 })]
    expect(workTypesAreSortedBySortOrderThenKey(rows)).toBe(true)
  })

  it('★ ★ sort_order 出现回退 ⇒ 不成立', () => {
    const rows = [wtOf({ key: 'a', sort_order: 2 }), wtOf({ key: 'b', sort_order: 1 })]
    expect(workTypesAreSortedBySortOrderThenKey(rows)).toBe(false)
  })

  it('★ ★★ 同 sort_order 而 key 乱序 ⇒ 不成立（这是 tiebreak 那一格）', () => {
    const rows = [wtOf({ key: 'z', sort_order: 1 }), wtOf({ key: 'a', sort_order: 1 })]
    expect(workTypesAreSortedBySortOrderThenKey(rows)).toBe(false)
  })

  it('★ ★ 空清单 ⇒ 判为成立（没有行可违反）', () => {
    expect(workTypesAreSortedBySortOrderThenKey([])).toBe(true)
  })

  it('★ ★ sort_order 递增但 key 递减 ⇒ 仍成立（key 只在同 sort_order 内才比）', () => {
    const rows = [wtOf({ key: 'z', sort_order: 1 }), wtOf({ key: 'a', sort_order: 2 })]
    expect(workTypesAreSortedBySortOrderThenKey(rows)).toBe(true)
  })

  it('★ ★ model_routes 是空数组 ⇒ 判为无（不算有路由）', () => {
    expect(workTypeHasModelRoutes(wtOf({ model_routes: [] }))).toBe(false)
  })

  it('★ ★ 有路由 ⇒ 判为有', () => {
    expect(workTypeHasModelRoutes(wtOf({ model_routes: [routeOf()] }))).toBe(true)
  })

  it('★ ★ model_routes 是 null ⇒ 判为无（反向）', () => {
    expect(workTypeHasModelRoutes(wtOf({ model_routes: null }))).toBe(false)
  })

  it('★ ★★ 键不存在 ⇒ 判为键缺席（omitempty + 无路由）', () => {
    expect(workTypeModelRoutesKeyIsAbsent(wtOf())).toBe(true)
  })

  it('★ 键存在（哪怕是 null）⇒ 不判为缺席（反向）', () => {
    expect(workTypeModelRoutesKeyIsAbsent(wtOf({ model_routes: null }))).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (6)(7) L1 列表形状
// ══════════════════════════════════════════════════════════════════════════

describe('(6)(7) L1 列表形状与 count 二义', () => {
  it('★ ★★ 九项（含一个新增）⇒ 至少 canonical 八项成立', () => {
    expect(l1TaskTypesHasAtLeastCanonicalEight(l1ListOf().items)).toBe(true)
  })

  it('★ ★ 恰好八项 ⇒ 也成立（只有 canonical）', () => {
    expect(l1TaskTypesHasAtLeastCanonicalEight(l1ListOf().items.slice(0, 8))).toBe(true)
  })

  it('★ 七项 ⇒ 不成立（canonical 会被无条件 seed）', () => {
    expect(l1TaskTypesHasAtLeastCanonicalEight(l1ListOf().items.slice(0, 7))).toBe(false)
  })

  it('★ ★★ 前八项就是 canonical 且顺序固定 ⇒ 成立', () => {
    expect(l1TaskTypesStartWithCanonicalInOrder(l1ListOf().items)).toBe(true)
  })

  it('★ ★★ 前八项顺序被打乱 ⇒ 不成立', () => {
    const items = [...l1ListOf().items]
    const t = items[0]
    items[0] = items[1] as L1TaskTypeMeta
    items[1] = t as L1TaskTypeMeta
    expect(l1TaskTypesStartWithCanonicalInOrder(items)).toBe(false)
  })

  it('★ ★ 恰好八项 ⇒ 仍是 canonical 顺序（长度边界那一格）', () => {
    expect(l1TaskTypesStartWithCanonicalInOrder(l1ListOf().items.slice(0, 8))).toBe(true)
  })

  it('★ ★★ 新增项（icon 是菱形）⇒ 判为额外项', () => {
    expect(l1TaskTypeIsExtra(l1Of({ key: 'legacy_rag', label: 'legacy_rag', icon: WORK_TYPES_EXTRA_L1_ICON }))).toBe(
      true,
    )
  })

  it('★ ★ canonical 项 ⇒ 判为规范项（反向）', () => {
    expect(l1TaskTypeIsCanonical(l1Of())).toBe(true)
  })

  it('★ ★★ label 回落到 key 而 icon 不是菱形 ⇒ 不判为规范项', () => {
    expect(l1TaskTypeIsCanonical(l1Of({ key: 'legacy_rag', label: 'legacy_rag', icon: '💬' }))).toBe(false)
  })

  it('★ ★★ 全部 count 为 0 ⇒ 判为二义（没用到 / 查询失败 / db 为 nil）', () => {
    expect(l1TaskTypeCountsAreAllZero(l1ListOf().items.map((r) => ({ ...r, count: 0 })))).toBe(true)
  })

  it('★ ★ 有一项非 0 ⇒ 判为有信号（反向）', () => {
    expect(l1TaskTypeCountsHaveSignal(l1ListOf().items)).toBe(true)
  })

  it('★ ★ 全部为 0 时不该判为有信号', () => {
    const zero = l1ListOf().items.map((r) => ({ ...r, count: 0 }))
    expect(l1TaskTypeCountsHaveSignal(zero)).toBe(false)
  })

  // ★★ 这条是 `some(r => r.count !== 0)` 与 `some(r => r.count === 0)` 的**唯一区分格**：
  //   混合列表（既有 0 又有非 0）下两者都返回 true，只有「一个 0 都没有」时才分开。
  it('★ ★★ 一个 0 都没有 ⇒ 仍判为有信号（some 的真值分界那一格）', () => {
    const nonzero = l1ListOf().items.map((r, i) => ({ ...r, count: i + 1 }))
    expect(l1TaskTypeCountsHaveSignal(nonzero)).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (8) count_24h 是派生字段
// ══════════════════════════════════════════════════════════════════════════

describe('(8) count_24h 是派生字段', () => {
  it('★ ★★ 等于 count_direct ⇒ 互锁判据成立', () => {
    expect(workTypeStatCountIsDerivedConsistently(statOf({ count_24h: 40, count_direct: 40 }))).toBe(true)
  })

  it('★ ★ direct 为 0 而 count_24h 取 L1 代理 ⇒ 也成立', () => {
    expect(workTypeStatCountIsDerivedConsistently(statOf({ count_24h: 120, count_direct: 0, count_l1_proxy: 120 }))).toBe(
      true,
    )
  })

  it('★ ★★ 三个数字互不匹配 ⇒ 不成立', () => {
    expect(workTypeStatCountIsDerivedConsistently(statOf({ count_24h: 7, count_direct: 0, count_l1_proxy: 120 }))).toBe(
      false,
    )
  })

  it('★ ★★ direct 大于 0 时 count_24h 必须就是它', () => {
    expect(workTypeStatPrefersDirectWhenPresent(statOf({ count_24h: 40, count_direct: 40, count_l1_proxy: 120 }))).toBe(
      true,
    )
  })

  it('★ ★ direct 大于 0 而 count_24h 取了 L1 代理 ⇒ 不成立', () => {
    expect(workTypeStatPrefersDirectWhenPresent(statOf({ count_24h: 120, count_direct: 40, count_l1_proxy: 120 }))).toBe(
      false,
    )
  })

  it('★ ★★ direct 为 0 时本判据不发表意见（放行）', () => {
    expect(
      workTypeStatPrefersDirectWhenPresent(statOf({ count_24h: 120, count_direct: 0, count_l1_proxy: 120 })),
    ).toBe(true)
  })

  it('★ ★★★ 同一 L1 下两行共享同一个 count_l1_proxy ⇒ 判为重复计数', () => {
    const entries = [
      statOf({ key: 'coding', l1_task_type: 'code', count_l1_proxy: 120 }),
      statOf({ key: 'review', l1_task_type: 'code', count_l1_proxy: 120 }),
    ]
    expect(workTypeStatsShareL1Proxy(entries, 'code')).toBe(true)
  })

  it('★ ★ 同一 L1 下两行的代理值不同 ⇒ 不判为共享', () => {
    const entries = [
      statOf({ key: 'coding', l1_task_type: 'code', count_l1_proxy: 120 }),
      statOf({ key: 'review', l1_task_type: 'code', count_l1_proxy: 9 }),
    ]
    expect(workTypeStatsShareL1Proxy(entries, 'code')).toBe(false)
  })

  it('★ 只有一行时不该判为共享（反向）', () => {
    expect(workTypeStatsShareL1Proxy([statOf({ l1_task_type: 'code' })], 'code')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (11)(12)(13) top_models 与路由顺序
// ══════════════════════════════════════════════════════════════════════════

describe('(11)(12)(13) top_models 与路由顺序', () => {
  it('★ ★ top_models 非升序 ⇒ 成立', () => {
    expect(workTypeTopModelsAreNonAscending(statsOf())).toBe(true)
  })

  it('★ ★ top_models 出现升序 ⇒ 不成立', () => {
    const r = statsOf({ top_models: [{ model: 'a', count: 1 }, { model: 'b', count: 9 }] })
    expect(workTypeTopModelsAreNonAscending(r)).toBe(false)
  })

  it('★ ★ 同计数并列 ⇒ 仍判为成立（无 tiebreak，不做更严的断言）', () => {
    const r = statsOf({ top_models: [{ model: 'a', count: 9 }, { model: 'b', count: 9 }] })
    expect(workTypeTopModelsAreNonAscending(r)).toBe(true)
  })

  it('★ ★★ 空 top_models ⇒ 判为成立', () => {
    expect(workTypeTopModelsAreNonAscending(statsOf({ top_models: [] }))).toBe(true)
  })

  it('★ ★ 条数不超过硬上限 10 ⇒ 成立', () => {
    expect(workTypeTopModelsWithinLimit(statsOf())).toBe(true)
  })

  it('★ ★ 条数超过 10 ⇒ 不成立（后端不可能产出，但客户端要能发现）', () => {
    const many = Array.from({ length: 11 }, (_, i) => ({ model: `m${i}`, count: 1 }))
    expect(workTypeTopModelsWithinLimit(statsOf({ top_models: many }))).toBe(false)
  })

  it('★ ★★ 恰好 10 条 top_models ⇒ 成立（边界那一格）', () => {
    const ten = Array.from({ length: 10 }, (_, i) => ({ model: `m${i}`, count: 1 }))
    expect(workTypeTopModelsWithinLimit(statsOf({ top_models: ten }))).toBe(true)
  })

  // ★ ORDER BY: tier CASE, weight DESC, canonical_name
  it('★ ★★ tier 分组 + weight 降序 + 同权重 canonical_name 升序 ⇒ 成立', () => {
    const routes = [
      routeOf({ canonical_name: 'a', weight: 2, tier: 'primary' }),
      routeOf({ canonical_name: 'b', weight: 1, tier: 'primary' }),
      routeOf({ canonical_name: 'c', weight: 5, tier: 'fallback' }),
    ]
    expect(modelRoutesAreOrderedByTierThenWeight(routes)).toBe(true)
  })

  it('★ ★ tier 顺序反了 ⇒ 不成立', () => {
    const routes = [
      routeOf({ canonical_name: 'c', weight: 1, tier: 'fallback' }),
      routeOf({ canonical_name: 'a', weight: 9, tier: 'primary' }),
    ]
    expect(modelRoutesAreOrderedByTierThenWeight(routes)).toBe(false)
  })

  it('★ ★ 同 tier 下 weight 出现升序 ⇒ 不成立', () => {
    const routes = [
      routeOf({ canonical_name: 'a', weight: 1, tier: 'primary' }),
      routeOf({ canonical_name: 'b', weight: 9, tier: 'primary' }),
    ]
    expect(modelRoutesAreOrderedByTierThenWeight(routes)).toBe(false)
  })

  it('★ ★★ 同 tier 同 weight 而 canonical_name 乱序 ⇒ 不成立（第三层 tiebreak）', () => {
    const routes = [
      routeOf({ canonical_name: 'z', weight: 1, tier: 'primary' }),
      routeOf({ canonical_name: 'a', weight: 1, tier: 'primary' }),
    ]
    expect(modelRoutesAreOrderedByTierThenWeight(routes)).toBe(false)
  })

  it('★ 空路由数组 ⇒ 判为成立', () => {
    expect(modelRoutesAreOrderedByTierThenWeight([])).toBe(true)
  })

  it('★ ★★ 同 tier 同权重而 canonical_name 升序 ⇒ 成立', () => {
    const routes = [
      routeOf({ canonical_name: 'a', weight: 1, tier: 'primary' }),
      routeOf({ canonical_name: 'b', weight: 1, tier: 'primary' }),
    ]
    expect(modelRoutesAreOrderedByTierThenWeight(routes)).toBe(true)
  })

  it('★ ★★ 同 tier 且 weight 降序而 canonical_name 降序 ⇒ 仍成立（名字只在同权重时才比）', () => {
    const routes = [
      routeOf({ canonical_name: 'z', weight: 9, tier: 'primary' }),
      routeOf({ canonical_name: 'a', weight: 1, tier: 'primary' }),
    ]
    expect(modelRoutesAreOrderedByTierThenWeight(routes)).toBe(true)
  })

  it('★ ★ tier 未知值排在三者之后 ⇒ 成立（SQL 的 ELSE 3）', () => {
    const routes = [
      routeOf({ canonical_name: 'c', weight: 5, tier: 'fallback' }),
      routeOf({ canonical_name: 'd', weight: 9, tier: 'tertiary' }),
    ]
    expect(modelRoutesAreOrderedByTierThenWeight(routes)).toBe(true)
  })

  it('★ ★★ tier 三个取值都算已知', () => {
    expect(modelRouteTierIsKnown('primary')).toBe(true)
    expect(modelRouteTierIsKnown('secondary')).toBe(true)
    expect(modelRouteTierIsKnown('fallback')).toBe(true)
  })

  it('★ ★ 未知 tier ⇒ 不算已知（反向）', () => {
    expect(modelRouteTierIsKnown('tertiary')).toBe(false)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// (2)(16)(17) 错误码与路由形状
// ══════════════════════════════════════════════════════════════════════════

describe('(2)(16)(17) 错误码与路由形状', () => {
  it('★ ★★ 错误按 code 匹配 ⇒ 成立（message 是本地化文本，不能用）', () => {
    expect(workTypeErrorCodeIs('admin_work_type_not_found', WORK_TYPES_ERROR_CODES.workTypeNotFound)).toBe(true)
  })

  it('★ ★ code 不同 ⇒ 不成立（反向）', () => {
    expect(workTypeErrorCodeIs('admin_not_found', WORK_TYPES_ERROR_CODES.workTypeNotFound)).toBe(false)
  })

  it('★ ★ 错误体是第四种形状：message + code + type', () => {
    expect([...workTypeErrorBodyKeys()]).toEqual(['message', 'code', 'type'])
  })

  it('★ ★★ include_disabled 只认精确的 true', () => {
    expect(workTypesIncludeDisabledIsEffective('true')).toBe(true)
  })

  it('★ ★★ 其它取值全部静默当作 false', () => {
    expect(workTypesIncludeDisabledIsEffective('1')).toBe(false)
    expect(workTypesIncludeDisabledIsEffective('TRUE')).toBe(false)
    expect(workTypesIncludeDisabledIsEffective('yes')).toBe(false)
    expect(workTypesIncludeDisabledIsEffective(null)).toBe(false)
    expect(workTypesIncludeDisabledIsEffective(undefined)).toBe(false)
  })

  it('★ ★ 尾斜杠与不带等价（handleSub 做 Trim）', () => {
    expect(workTypePathWithTrailingSlashIsEquivalent('coding', true)).toBe(true)
    expect(workTypePathWithTrailingSlashIsEquivalent('coding', false)).toBe(true)
  })
})

// ══════════════════════════════════════════════════════════════════════════
// fetch 端到端
// ══════════════════════════════════════════════════════════════════════════

describe('fetch 端到端', () => {
  it('★ ★ 不给 includeDisabled 时不带查询串', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([wtOf()]))
    await fetchWorkTypes()
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain(WORK_TYPES_PATH)
    expect(u).not.toContain('?')
  })

  it('★ ★ includeDisabled 时拼出 include_disabled=true', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([wtOf()]))
    await fetchWorkTypes({ includeDisabled: true })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('include_disabled=true')
  })

  it('★ ★ 单项路径把 key 放在最后一段', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(wtOf()))
    await fetchWorkType({ key: 'coding' })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/work-types/coding')
  })

  it('★ ★★ 含斜杠的 key 被编码（否则会切错路径）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(wtOf()))
    await fetchWorkType({ key: 'a/b' })
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/work-types/a%2Fb')
  })

  it('★ ★ 统计端点不带查询串（窗口固定 24h）', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(statsOf()))
    await fetchWorkTypeStats()
    const u = String(fetchMock.mock.calls[0]![0])
    expect(u).toContain('/api/admin/work-types/stats')
    expect(u).not.toContain('?')
  })

  it('★ ★★ L1 端点挂在保留子路径上', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse(l1ListOf()))
    await fetchL1TaskTypes()
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/admin/work-types/l1-task-types')
  })

  it('★ ★★ 端到端：缺键 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([del(wtOf() as never, 'l1_task_type')]))
    await expect(fetchWorkTypes()).rejects.toThrow(/缺 1 个键/)
  })

  it('★ ★ 端到端：顶层裸数组被正确放行', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await expect(fetchWorkTypes()).resolves.toEqual([])
  })
})

// ══════════════════════════════════════════════════════════════════════════
// 常量取值
// ══════════════════════════════════════════════════════════════════════════

describe('常量取值', () => {
  it('★ ★★ 三个稳定错误码与后端一致', () => {
    expect(WORK_TYPES_ERROR_CODES.notFound).toBe('admin_not_found')
    expect(WORK_TYPES_ERROR_CODES.methodNotAllowed).toBe('admin_method_not_allowed')
    expect(WORK_TYPES_ERROR_CODES.workTypeNotFound).toBe('admin_work_type_not_found')
  })

  it('★ ★ 内部错误文案 === internal error (see server logs)', () => {
    expect(WORK_TYPES_ERROR_CODES.internal).toBe('internal error (see server logs)')
  })

  it('★ ★ 统计窗口恒 24 小时、top_models 上限 10', () => {
    expect(WORK_TYPES_STATS_WINDOW_HOURS).toBe(24)
    expect(WORK_TYPES_TOP_MODELS_LIMIT).toBe(10)
  })

  it('★ ★ default_profile 三值与表 CHECK 一致', () => {
    expect([...WORK_TYPES_DEFAULT_PROFILES]).toEqual(['smart', 'speed_first', 'cost_first'])
    expect(WORK_TYPES_DEFAULT_PROFILE).toBe('smart')
  })

  it('★ ★ tier 三值与表 CHECK 一致', () => {
    expect([...WORK_TYPES_ROUTE_TIERS]).toEqual(['primary', 'secondary', 'fallback'])
  })

  it('★ ★★ 新增 L1 的占位 icon 是菱形', () => {
    expect(WORK_TYPES_EXTRA_L1_ICON).toBe('◆')
  })

  it('★ ★★ task_quality_score 的域是 0–100（不是 0–1）', () => {
    expect(WORK_TYPES_TASK_QUALITY_MIN).toBe(0)
    expect(WORK_TYPES_TASK_QUALITY_MAX).toBe(100)
  })

  it('★ ★ canonical 八项顺序与后端一致', () => {
    expect([...WORK_TYPES_CANONICAL_L1_KEYS]).toEqual([
      'chat', 'reasoning', 'code', 'agent', 'creative', 'long_context', 'vision', 'function_call',
    ])
  })

  it('★ ★★ workTypeConfig 十四键：十个恒在 + 四个 omitempty', () => {
    expect(WORK_TYPE_CONFIG_KEYS.length).toBe(14)
    expect(WORK_TYPE_CONFIG_REQUIRED_KEYS.length).toBe(10)
    expect(WORK_TYPE_CONFIG_OPTIONAL_KEYS.length).toBe(4)
  })

  it('★ model_routes 属于 omitempty 那一组（三态的来源）', () => {
    expect([...WORK_TYPE_CONFIG_OPTIONAL_KEYS]).toContain('model_routes')
  })

  it('★ ★ 路由七键，且 id 是唯一 omitempty', () => {
    expect([...MODEL_ROUTE_KEYS]).toEqual([
      'id', 'canonical_name', 'weight', 'min_score', 'enabled', 'tier', 'task_quality_score',
    ])
  })

  it('★ ★ 统计项七键', () => {
    expect([...WORK_TYPE_STAT_ENTRY_KEYS]).toEqual([
      'key', 'label', 'category', 'l1_task_type', 'count_24h', 'count_direct', 'count_l1_proxy',
    ])
  })

  it('★ ★★ 统计顶层七键（含桌面类型没声明的 total_specified）', () => {
    expect([...WORK_TYPE_STATS_KEYS]).toEqual([
      'window_hours', 'by_work_type', 'by_l1_task', 'total_auto', 'total_specified', 'top_models', 'sync_meta',
    ])
  })

  it('★ L1 四键与 top_models 两键', () => {
    expect([...L1_TASK_TYPE_META_KEYS]).toEqual(['key', 'label', 'icon', 'count'])
    expect([...WORK_TYPE_TOP_MODEL_KEYS]).toEqual(['model', 'count'])
  })

  it('★ 路径前缀不带尾斜杠', () => {
    expect(WORK_TYPES_PATH).toBe('/api/admin/work-types')
  })
})
