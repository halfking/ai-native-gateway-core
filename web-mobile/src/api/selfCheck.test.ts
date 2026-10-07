import { describe, it, expect, vi, beforeEach } from 'vitest'
import * as api from './selfCheck'
import type { ScProbeSystem, ScStatsSummary, ScModelInfo } from './selfCheck'

vi.mock('./client', () => ({ req: vi.fn(), ApiError: class extends Error {} }))

import { req } from './client'
const rq = vi.mocked(req)

function ok(payload: unknown): void {
  rq.mockResolvedValueOnce(payload)
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 夹具：逐字照抄 admin/self_check_handlers.go 的赋值
 * ═══════════════════════════════════════════════════════════════════════════ */

/** scRun（`:89-110`）健康无错的那一行。 */
function run(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 101,
    model_name: 'glm-5.2',
    started_at: '2026-10-07T09:00:00Z',
    duration_ms: 1200,
    status: 'success',
    rounds_total: 3,
    rounds_success: 3,
    had_tool_call: true,
    total_tokens: 812,
    avg_latency_ms: 400,
    upstream_tested: true,
    upstream_result: 'ok',
    upstream_latency_ms: 380,
    selection_strategy: 'most_used',
    attempted_models: ['glm-5.2'],
    ...over,
  }
}

/**
 * run 的九个条件键**全部**缺失（`:143-148` 全是 COALESCE(...,'') + omitempty）。
 *
 * ★ `model_name` 刻意用**非 cred 标签**：后端一旦看到 `cred-<正整数>` 就会回写
 *   `credential_id`（文件头第 (5) 条），所以「九键全缺」的载荷不可能带 cred 标签。
 */
function runBare(): Record<string, unknown> {
  return {
    id: 102,
    model_name: 'mimo-v2.5',
    started_at: '2026-10-07T09:05:00Z',
    duration_ms: 0,
    status: 'running',
    rounds_total: 0,
    rounds_success: 0,
    had_tool_call: false,
    total_tokens: 0,
    avg_latency_ms: 0,
    upstream_tested: false,
  }
}

/** round（`:251-266`）：14 键全在，无 omitempty。 */
function round(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 9001,
    round_index: 0,
    is_ping: true,
    is_tool_call: false,
    latency_ms: 120,
    prompt_tokens: 10,
    completion_tokens: 20,
    total_tokens: 30,
    success: true,
    http_code: 200,
    error_message: '',
    request_body: '',
    response_preview: '',
    created_at: '2026-10-07T09:00:05Z',
    ...over,
  }
}

/**
 * 已完成的 run：`completed_at` 在（`:94` 的 `*time.Time` + omitempty ⇒ 有则字符串）。
 * ★ 它同时是「字符串可选键」那一类的探针 —— 缺了它，
 *   把 `completed_at` 误列进数字可选键的变异就打不出差异。
 */
function runCompleted(over: Record<string, unknown> = {}): Record<string, unknown> {
  return run({
    status: 'success',
    completed_at: '2026-10-07T09:00:02Z',
    error_type: '',
    error_detail: '',
    ...over,
  })
}

/** probe_system（`:952-960`）真实在跑的样子。 */
function probeSystem(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    queue_ready: 3,
    queue_ready_unclaimable: 0,
    queue_running: 1,
    due_states: 4,
    queue_last_activity_at: '2026-10-07T09:29:00Z',
    last_probe_attempt_at: '2026-10-07T09:28:00Z',
    executing: true,
    healthy: true,
    ...over,
  }
}

function stats(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    range: '24h',
    summary: {
      total_runs: 12,
      success_runs: 8,
      partial_runs: 2,
      failed_runs: 1,
      success_rate: 8 / 12,
    },
    by_model: [
      {
        model_name: 'glm-5.2',
        total: 10,
        success: 7,
        partial: 2,
        failed: 1,
        success_rate: 0.7,
        avg_latency_ms: 412,
      },
    ],
    error_breakdown: [{ error_type: 'timeout', count: 3 }],
    trend: [{ timestamp: '2026-10-07T09:00:00Z', success_rate: 0.75, total: 4 }],
    probe_system: probeSystem(),
    ...over,
  }
}

/* ═══════════════════════════════════════════════════════════════════════════
 * 常量对齐后端字面量 / 建表 CHECK
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('常量对齐后端字面量', () => {
  it('默认 limit 是 50', () => {
    // self_check_handlers.go:119
    expect(api.SELF_CHECK_RUNS_DEFAULT_LIMIT).toBe(50)
  })

  it('limit 上限是 500', () => {
    // :121
    expect(api.SELF_CHECK_RUNS_MAX_LIMIT).toBe(500)
  })

  it('窗口枚举四个且缺省 24h', () => {
    // :783-794
    expect([...api.SELF_CHECK_STATS_RANGES]).toEqual(['1h', '6h', '24h', '7d'])
    expect(api.SELF_CHECK_STATS_DEFAULT_RANGE).toBe('24h')
  })

  it('新探测模式缺省为 true', () => {
    // :764-766：env 为空 ⇒ true
    expect(api.SELF_CHECK_NEW_PROBE_MODE_DEFAULT).toBe(true)
  })

  it('触发模式两个字面量', () => {
    // :484 / :486
    expect(api.SELF_CHECK_TRIGGER_MODE_PROBE_QUEUE).toBe('probe_queue')
    expect(api.SELF_CHECK_TRIGGER_MODE_LEGACY_WORKER).toBe('legacy_worker')
  })

  it('两个 error_code 字面量', () => {
    // :493 / :496
    expect(api.SELF_CHECK_TRIGGER_ERR_NO_PROBE_PATH).toBe('self_check.trigger.no_probe_path')
    expect(api.SELF_CHECK_TRIGGER_ERR_WORKER_UNAVAILABLE).toBe('self_check.trigger.worker_unavailable')
  })

  it('stale 阈值是 15 分钟', () => {
    // :960
    expect(api.SELF_CHECK_PROBE_STALE_MS).toBe(900000)
  })

  it('★ 播种的默认精选模型逐字对齐 :290', () => {
    // defaultFeaturedModelsSC = `["minimax-m2.7","glm-5.2","mimo-v2.5","claude-sonnet-5","gpt-5.4","gpt-5.6-luna","deepseek-v4-pro"]`
    expect(JSON.stringify(api.SELF_CHECK_DEFAULT_FEATURED_MODELS)).toBe(
      '["minimax-m2.7","glm-5.2","mimo-v2.5","claude-sonnet-5","gpt-5.4","gpt-5.6-luna","deepseek-v4-pro"]',
    )
  })

  it('★ status 是五值枚举（含桌面漏掉的 retrying）', () => {
    // 建表 CHECK self_check_runs_status_check
    expect([...api.SELF_CHECK_RUN_STATUSES]).toEqual([
      'running', 'success', 'partial', 'failed', 'retrying',
    ])
  })

  it('★ error_type 是 31 值封闭枚举', () => {
    // 建表 CHECK self_check_runs_error_type_check（不含 http_% 模式项）
    expect(api.SELF_CHECK_ERROR_TYPES).toHaveLength(31)
    expect(api.SELF_CHECK_ERROR_TYPES[0]).toBe('none')
    expect(api.SELF_CHECK_ERROR_TYPES[30]).toBe('upstream_fail')
  })

  it('★ selection_strategy 是七值枚举', () => {
    // 建表 CHECK self_check_runs_selection_strategy_check
    expect([...api.SELF_CHECK_SELECTION_STRATEGIES]).toEqual([
      'most_used', 'random', 'featured', 'recent', 'common_7d', 'failed_model',
      'no_eligible_model',
    ])
  })

  it('★ run 恒在十一键、条件九键', () => {
    expect(api.SELF_CHECK_RUN_ALWAYS_KEYS).toHaveLength(11)
    expect(api.SELF_CHECK_RUN_OPTIONAL_KEYS).toHaveLength(9)
    const always = new Set<string>(api.SELF_CHECK_RUN_ALWAYS_KEYS)
    expect(api.SELF_CHECK_RUN_OPTIONAL_KEYS.filter((k) => always.has(k))).toEqual([])
  })

  it('★ 数字可选键只有 credential_id 与 upstream_latency_ms', () => {
    // ★ 写宽成「字符串或数字」会让 error_type: 500 蒙混过关
    expect([...api.SELF_CHECK_RUN_NUMERIC_OPTIONAL_KEYS]).toEqual([
      'credential_id', 'upstream_latency_ms',
    ])
  })

  it('★ 字符串可选键六个且与数字键无交集', () => {
    expect([...api.SELF_CHECK_RUN_STRING_OPTIONAL_KEYS]).toEqual([
      'completed_at', 'error_type', 'error_detail', 'upstream_result',
      'upstream_error', 'selection_strategy',
    ])
    const num = new Set<string>(api.SELF_CHECK_RUN_NUMERIC_OPTIONAL_KEYS)
    expect(api.SELF_CHECK_RUN_STRING_OPTIONAL_KEYS.filter((k) => num.has(k))).toEqual([])
  })

  it('round 十四键一个 omitempty 都没有', () => {
    expect(api.SELF_CHECK_ROUND_KEYS).toHaveLength(14)
  })

  it('stats 顶层六键、probe_system 八键', () => {
    expect([...api.SELF_CHECK_STATS_KEYS]).toEqual([
      'range', 'summary', 'by_model', 'error_breakdown', 'trend', 'probe_system',
    ])
    expect(api.SELF_CHECK_PROBE_SYSTEM_KEYS).toHaveLength(8)
  })

  it('★ models 的四恒在键里没有 partial', () => {
    // :993-999 —— 没有 partial 计数（stats 的 by_model 有）
    expect([...api.SELF_CHECK_MODEL_INFO_KEYS]).toEqual([
      'model_name', 'total', 'success', 'failed',
    ])
    expect(api.SELF_CHECK_MODEL_INFO_KEYS as readonly string[]).not.toContain('partial')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 解包
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('解包 · runs', () => {
  it('健康载荷原样通过', () => {
    const r = api.unwrapSelfCheckRuns({ items: [run()], total: 1 })
    expect(r.items).toHaveLength(1)
    expect(r.total).toBe(1)
  })

  it('★ 空数组不是 null', () => {
    // :161 `make([]scRun, 0)` + 「frontend reads .length」
    const r = api.unwrapSelfCheckRuns({ items: [], total: 0 })
    expect(r.items).toEqual([])
    expect(r.total).toBe(0)
  })

  it('★ 顶层缺键抛错', () => {
    expect(() => api.unwrapSelfCheckRuns({ items: [] })).toThrow(/缺 1 个键（total）/)
  })

  it('items 不是数组时抛错', () => {
    expect(() => api.unwrapSelfCheckRuns({ items: {}, total: 0 })).toThrow(/items 不是数组/)
  })

  it('total 不是数字时抛错', () => {
    expect(() => api.unwrapSelfCheckRuns({ items: [], total: '1' })).toThrow(/total 不是数字/)
  })

  it('响应形状不是裸对象时抛错', () => {
    expect(() => api.unwrapSelfCheckRuns(null)).toThrow(/期望裸对象，实得 null/)
    expect(() => api.unwrapSelfCheckRuns([])).toThrow(/期望裸对象，实得 array/)
    expect(() => api.unwrapSelfCheckRuns('x')).toThrow(/期望裸对象，实得 string/)
  })

  it('run 缺恒在键时抛错', () => {
    expect(() => api.unwrapSelfCheckRuns({ items: [{ id: 1 }], total: 1 })).toThrow(
      /缺 10 个键/,
    )
  })

  it('run 的 status 不是字符串时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [run({ status: 7 })], total: 1 }),
    ).toThrow(/items\[0\] 的 status 不是字符串/)
  })

  it('run 的 had_tool_call 不是布尔时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [run({ had_tool_call: 'yes' })], total: 1 }),
    ).toThrow(/items\[0\] 的 had_tool_call 不是布尔值/)
  })

  it('run 的 rounds_total 不是数字时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [run({ rounds_total: '3' })], total: 1 }),
    ).toThrow(/items\[0\] 的 rounds_total 不是数字/)
  })

  it('★ completed 的 run 带 completed_at 字符串时通过', () => {
    const r = api.unwrapSelfCheckRuns({ items: [runCompleted()], total: 1 })
    expect(r.items[0]!.completed_at).toBe('2026-10-07T09:00:02Z')
  })

  it('★★ completed_at 是数字时抛错（不能被当成数字可选键放过）', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [runCompleted({ completed_at: 7 })] , total: 1 }),
    ).toThrow(/items\[0\] 的 completed_at 不是字符串/)
  })

  it('★ error_type 是数字时抛错（字符串键不得放宽成 string|number）', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [run({ error_type: 500 })], total: 1 }),
    ).toThrow(/items\[0\] 的 error_type 不是字符串/)
  })

  it('★ 九个条件键全缺失时通过', () => {
    const r = api.unwrapSelfCheckRuns({ items: [runBare()], total: 1 })
    expect('credential_id' in r.items[0]!).toBe(false)
    expect('upstream_latency_ms' in r.items[0]!).toBe(false)
    expect('attempted_models' in r.items[0]!).toBe(false)
  })

  it('★ upstream_latency_ms 为 0 时是键缺失而不是 0', () => {
    // omitempty 打在 int 上 ⇒ 0 不落键
    const r = api.unwrapSelfCheckRuns({ items: [runBare()], total: 1 })
    expect(api.selfCheckUpstreamLatencyMsOrNull(r.items[0]!)).toBeNull()
  })

  it('★ 条件键在但类型错时抛错（键在 ≠ 不校验）', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [run({ credential_id: '7' })], total: 1 }),
    ).toThrow(/items\[0\] 的 credential_id 不是数字/)
  })

  it('attempted_models 不是数组时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [run({ attempted_models: '[]' })], total: 1 }),
    ).toThrow(/items\[0\] 的 attempted_models 不是数组/)
  })

  it('error_type 是数字时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [run({ error_type: 500 })], total: 1 }),
    ).toThrow(/items\[0\] 的 error_type 不是字符串/)
  })

  it('★ index 出现在错误消息里，可定位到具体条目', () => {
    expect(() =>
      api.unwrapSelfCheckRuns({ items: [run(), run({ status: 7 })], total: 2 }),
    ).toThrow(/items\[1\] 的 status 不是字符串/)
  })
})

describe('解包 · run 详情', () => {
  it('健康载荷原样通过', () => {
    const r = api.unwrapSelfCheckRunDetail({ run: run(), rounds: [round()] })
    expect(r.rounds).toHaveLength(1)
    expect(r.run.id).toBe(101)
  })

  it('★ rounds 空数组不是 null', () => {
    // :267 make([]round, 0)
    const r = api.unwrapSelfCheckRunDetail({ run: run(), rounds: [] })
    expect(r.rounds).toEqual([])
  })

  it('顶层缺键抛错', () => {
    expect(() => api.unwrapSelfCheckRunDetail({ run: run() })).toThrow(/缺 1 个键（rounds）/)
  })

  it('rounds 不是数组时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRunDetail({ run: run(), rounds: null }),
    ).toThrow(/rounds 不是数组/)
  })

  it('round 缺键抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRunDetail({ run: run(), rounds: [{ id: 1 }] }),
    ).toThrow(/rounds\[0\] 缺 13 个键/)
  })

  it('round 的 http_code 不是数字时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRunDetail({ run: run(), rounds: [round({ http_code: '200' })] }),
    ).toThrow(/rounds\[0\] 的 http_code 不是数字/)
  })

  it('round 的 error_message 不是字符串时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRunDetail({ run: run(), rounds: [round({ error_message: null })] }),
    ).toThrow(/rounds\[0\] 的 error_message 不是字符串/)
  })

  it('round 的 is_ping 不是布尔时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckRunDetail({ run: run(), rounds: [round({ is_ping: 1 })] }),
    ).toThrow(/rounds\[0\] 的 is_ping 不是布尔值/)
  })

  it('run 形状错时也报 run 详情而不是 rounds', () => {
    expect(() =>
      api.unwrapSelfCheckRunDetail({ run: { id: 1 }, rounds: [] }),
    ).toThrow(/self-check run 详情\.run 缺 10 个键/)
  })
})

describe('解包 · settings', () => {
  it('健康载荷原样通过', () => {
    const s = api.unwrapSelfCheckSettings({
      enabled: true,
      normal_interval_seconds: 3600,
      fault_interval_seconds: 900,
      model_source: 'both',
      max_models: 10,
      max_tokens_per_run: 4000,
      featured_model_ids: ['glm-5.2'],
      updated_at: '2026-10-07T08:00:00Z',
    })
    expect(s.featured_model_ids).toEqual(['glm-5.2'])
  })

  it('updated_by 在时通过', () => {
    const s = api.unwrapSelfCheckSettings({
      enabled: false,
      normal_interval_seconds: 0,
      fault_interval_seconds: 0,
      model_source: 'top10',
      max_models: 0,
      max_tokens_per_run: 0,
      featured_model_ids: [],
      updated_at: '2026-10-07T08:00:00Z',
      updated_by: 'admin',
    })
    expect(s.updated_by).toBe('admin')
  })

  it('★ 裸对象没有信封', () => {
    expect(() =>
      api.unwrapSelfCheckSettings({ success: true, data: {} }),
    ).toThrow(/缺 8 个键/)
  })

  it('enabled 不是布尔时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckSettings({
        enabled: 'yes',
        normal_interval_seconds: 0,
        fault_interval_seconds: 0,
        model_source: 'both',
        max_models: 0,
        max_tokens_per_run: 0,
        featured_model_ids: [],
        updated_at: 'x',
      }),
    ).toThrow(/enabled 不是布尔值/)
  })

  it('max_models 不是数字时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckSettings({
        enabled: true,
        normal_interval_seconds: 0,
        fault_interval_seconds: 0,
        model_source: 'both',
        max_models: '10',
        max_tokens_per_run: 0,
        featured_model_ids: [],
        updated_at: 'x',
      }),
    ).toThrow(/max_models 不是数字/)
  })

  it('featured_model_ids 不是数组时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckSettings({
        enabled: true,
        normal_interval_seconds: 0,
        fault_interval_seconds: 0,
        model_source: 'both',
        max_models: 1,
        max_tokens_per_run: 0,
        featured_model_ids: null,
        updated_at: 'x',
      }),
    ).toThrow(/featured_model_ids 不是数组/)
  })

  it('★ updated_by 为 null 时抛错（omitempty ⇒ 键在必为字符串）', () => {
    expect(() =>
      api.unwrapSelfCheckSettings({
        enabled: true,
        normal_interval_seconds: 0,
        fault_interval_seconds: 0,
        model_source: 'both',
        max_models: 1,
        max_tokens_per_run: 0,
        featured_model_ids: [],
        updated_at: 'x',
        updated_by: null,
      }),
    ).toThrow(/updated_by 不是字符串/)
  })
})

describe('解包 · 触发可用性', () => {
  it('probe_queue 模式通过', () => {
    const a = api.unwrapSelfCheckTriggerAvailability({
      available: true,
      new_probe_mode: true,
      mode: 'probe_queue',
    })
    expect(a.mode).toBe('probe_queue')
  })

  it('★ 两个条件键都在的不可用形态通过', () => {
    const a = api.unwrapSelfCheckTriggerAvailability({
      available: false,
      new_probe_mode: true,
      reason: '自检触发已迁移到节点探测队列',
      error_code: 'self_check.trigger.no_probe_path',
    })
    expect(a.error_code).toBe('self_check.trigger.no_probe_path')
  })

  it('★ 最小形态只有两个恒在键也通过', () => {
    const a = api.unwrapSelfCheckTriggerAvailability({ available: false, new_probe_mode: false })
    expect('mode' in a).toBe(false)
    expect('reason' in a).toBe(false)
  })

  it('available 不是布尔时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckTriggerAvailability({ available: 1, new_probe_mode: true }),
    ).toThrow(/available 不是布尔值/)
  })

  it('new_probe_mode 不是布尔时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckTriggerAvailability({ available: true, new_probe_mode: 'yes' }),
    ).toThrow(/new_probe_mode 不是布尔值/)
  })

  it('mode 在但不是字符串时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckTriggerAvailability({
        available: true,
        new_probe_mode: true,
        mode: 1,
      }),
    ).toThrow(/mode 不是字符串/)
  })

  it('error_code 在但不是字符串时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckTriggerAvailability({
        available: false,
        new_probe_mode: true,
        reason: 'x',
        error_code: 7,
      }),
    ).toThrow(/error_code 不是字符串/)
  })

  it('缺恒在键抛错', () => {
    expect(() => api.unwrapSelfCheckTriggerAvailability({ available: true })).toThrow(
      /缺 1 个键（new_probe_mode）/,
    )
  })
})

describe('解包 · stats', () => {
  it('健康载荷原样通过', () => {
    const s = api.unwrapSelfCheckStats(stats())
    expect(s.by_model).toHaveLength(1)
  })

  it('★ 顶层缺 probe_system 时抛错（桌面把它整个丢了）', () => {
    const s = stats()
    delete s.probe_system
    expect(() => api.unwrapSelfCheckStats(s)).toThrow(/缺 1 个键（probe_system）/)
  })

  it('range 不是字符串时抛错', () => {
    expect(() => api.unwrapSelfCheckStats(stats({ range: 24 }))).toThrow(
      /range 不是字符串/,
    )
  })

  it('summary 缺键时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckStats(
        stats({ summary: { total_runs: 1, success_runs: 1, partial_runs: 0, failed_runs: 0 } }),
      ),
    ).toThrow(/summary 缺 1 个键（success_rate）/)
  })

  it('summary 的 success_rate 不是数字时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckStats(
        stats({
          summary: {
            total_runs: 1, success_runs: 1, partial_runs: 0, failed_runs: 0,
            success_rate: '100%',
          },
        }),
      ),
    ).toThrow(/summary 的 success_rate 不是数字/)
  })

  it('by_model 不是数组时抛错', () => {
    expect(() => api.unwrapSelfCheckStats(stats({ by_model: {} }))).toThrow(
      /by_model 不是数组/,
    )
  })

  it('★ by_model 的元素缺 partial 时抛错（models 端点才没有 partial）', () => {
    expect(() =>
      api.unwrapSelfCheckStats(
        stats({
          by_model: [
            { model_name: 'm', total: 1, success: 1, failed: 0, success_rate: 1, avg_latency_ms: 1 },
          ],
        }),
      ),
    ).toThrow(/by_model\[0\] 缺 1 个键（partial）/)
  })

  it('by_model 的 avg_latency_ms 不是数字时抛错', () => {
    const s = stats()
    ;(s.by_model as Record<string, unknown>[])[0]!.avg_latency_ms = '412'
    expect(() => api.unwrapSelfCheckStats(s)).toThrow(/by_model\[0\] 的 avg_latency_ms 不是数字/)
  })

  it('error_breakdown 缺 count 时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckStats(stats({ error_breakdown: [{ error_type: 'timeout' }] })),
    ).toThrow(/error_breakdown\[0\] 缺 1 个键（count）/)
  })

  it('trend 缺 timestamp 时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckStats(stats({ trend: [{ success_rate: 1, total: 1 }] })),
    ).toThrow(/trend\[0\] 缺 1 个键（timestamp）/)
  })

  it('probe_system 缺 healthy 时抛错', () => {
    const ps = probeSystem()
    delete ps.healthy
    expect(() => api.unwrapSelfCheckStats(stats({ probe_system: ps }))).toThrow(
      /probe_system 缺 1 个键（healthy）/,
    )
  })

  it('★ probe_system 的两个时间键为 null 时通过', () => {
    const s = api.unwrapSelfCheckStats(
      stats({
        probe_system: probeSystem({
          queue_last_activity_at: null,
          last_probe_attempt_at: null,
        }),
      }),
    )
    expect(s.probe_system.queue_last_activity_at).toBeNull()
  })

  it('★ probe_system 的 healthy 是数字时抛错（布尔键不得放宽）', () => {
    expect(() =>
      api.unwrapSelfCheckStats(stats({ probe_system: probeSystem({ healthy: 1 }) })),
    ).toThrow(/healthy 不是布尔值/)
  })

  it('★ probe_system 的 executing 是数字时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckStats(stats({ probe_system: probeSystem({ executing: 0 }) })),
    ).toThrow(/executing 不是布尔值/)
  })

  it('★ probe_system 的时间键为数字时抛错（恒在键，不是键缺失）', () => {
    expect(() =>
      api.unwrapSelfCheckStats(
        stats({ probe_system: probeSystem({ queue_last_activity_at: 12345 }) }),
      ),
    ).toThrow(/queue_last_activity_at 不是字符串也不是 null/)
  })

  it('probe_system 的 queue_ready 不是数字时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckStats(stats({ probe_system: probeSystem({ queue_ready: '3' }) })),
    ).toThrow(/queue_ready 不是数字/)
  })

  it('probe_system 的 healthy 不是布尔时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckStats(stats({ probe_system: probeSystem({ healthy: 'yes' }) })),
    ).toThrow(/healthy 不是布尔值/)
  })

  it('probe_system 形状不对时点名 probe_system', () => {
    expect(() => api.unwrapSelfCheckStats(stats({ probe_system: 'none' }))).toThrow(
      /probe_system 响应形状不符/,
    )
  })
})

describe('解包 · models', () => {
  it('健康载荷原样通过', () => {
    const m = api.unwrapSelfCheckModels({
      models: [
        { model_name: 'glm-5.2', total: 5, success: 4, failed: 1, last_run: '2026-10-07T09:00:00Z' },
      ],
    })
    expect(m.models).toHaveLength(1)
  })

  it('★ 空数组不是 null', () => {
    expect(api.unwrapSelfCheckModels({ models: [] }).models).toEqual([])
  })

  it('★ last_run 缺失时通过（*time.Time + omitempty）', () => {
    const m = api.unwrapSelfCheckModels({
      models: [{ model_name: 'm', total: 0, success: 0, failed: 0 }],
    })
    expect('last_run' in m.models[0]!).toBe(false)
  })

  it('★ last_run 为 null 时抛错（缺失与 null 不是一回事）', () => {
    expect(() =>
      api.unwrapSelfCheckModels({
        models: [{ model_name: 'm', total: 0, success: 0, failed: 0, last_run: null }],
      }),
    ).toThrow(/last_run 不是字符串/)
  })

  it('缺 models 时抛错', () => {
    expect(() => api.unwrapSelfCheckModels({})).toThrow(/缺 1 个键（models）/)
  })

  it('models 不是数组时抛错', () => {
    expect(() => api.unwrapSelfCheckModels({ models: null })).toThrow(/models 不是数组/)
  })

  it('元素缺 failed 时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckModels({ models: [{ model_name: 'm', total: 1, success: 1 }] }),
    ).toThrow(/models\[0\] 缺 1 个键（failed）/)
  })

  it('元素的 total 不是数字时抛错', () => {
    expect(() =>
      api.unwrapSelfCheckModels({
        models: [{ model_name: 'm', total: '1', success: 1, failed: 0 }],
      }),
    ).toThrow(/models\[0\] 的 total 不是数字/)
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 语义判定
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('credential_id 是从 model_name 推导的', () => {
  it('cred-7 推出 7', () => {
    expect(api.selfCheckCredentialIdFromLabel('cred-7')).toBe(7)
  })

  it('★ cred-0 不推出（后端 id<=0 返回 nil）', () => {
    expect(api.selfCheckCredentialIdFromLabel('cred-0')).toBeNull()
  })

  it('★ cred--3 不推出（ParseInt 失败）', () => {
    expect(api.selfCheckCredentialIdFromLabel('cred--3')).toBeNull()
  })

  it('★ cred-7a 不推出（后缀不是纯数字）', () => {
    expect(api.selfCheckCredentialIdFromLabel('cred-7a')).toBeNull()
  })

  it('普通模型名不推出', () => {
    expect(api.selfCheckCredentialIdFromLabel('glm-5.2')).toBeNull()
  })

  it('★★ 无 cred 前缀但后缀是纯数字时也不推出', () => {
    // ★ 后端判的是**前缀**，不是「后缀像不像数字」。
    //   'gpt-0042'.slice('cred-'.length) === '0042' 是纯数字 ——
    //   只查后缀的实现会把它误认成凭据自检。
    expect(api.selfCheckCredentialIdFromLabel('gpt-0042')).toBeNull()
  })

  it('只有前缀没有数字不推出', () => {
    expect(api.selfCheckCredentialIdFromLabel('cred-')).toBeNull()
  })

  it('★★★ cred- 后面带前导空格时也不推出', () => {
    // ★ Go 的 strconv.ParseInt(" 7", 10, 64) 报错 ⇒ 返回 nil。
    //   而 JS 的 Number(" 7") === 7 —— 只查「非空」或「能否 Number()」的实现会误推。
    expect(api.selfCheckCredentialIdFromLabel('cred- 7')).toBeNull()
  })

  it('★ cred- 后面带十六进制时也不推出', () => {
    // ParseInt("0x10", 10, 64) 报错；Number("0x10") === 16
    expect(api.selfCheckCredentialIdFromLabel('cred-0x10')).toBeNull()
  })

  it('★ 推导值与后端一致时成立', () => {
    const r = api.unwrapSelfCheckRuns({
      items: [runBare(), run({ model_name: 'cred-7', credential_id: 7 })],
      total: 2,
    })
    expect(r.items.every(api.selfCheckCredentialIdMatches)).toBe(true)
  })

  it('★ 后端多写了 credential_id 时不成立', () => {
    const r = api.unwrapSelfCheckRuns({
      items: [run({ model_name: 'glm-5.2', credential_id: 7 })],
      total: 1,
    })
    expect(api.selfCheckCredentialIdMatches(r.items[0]!)).toBe(false)
  })

  it('★★★ cred 标签推出的 id 与后端回写的不一致时不成立', () => {
    // ★ 走的是「有推导值」的**另一半**分支；
    //   只有 derived===null 的用例时，恒真化那一半打不出差异。
    const r = api.unwrapSelfCheckRuns({
      items: [run({ model_name: 'cred-9', credential_id: 3 })],
      total: 1,
    })
    expect(api.selfCheckCredentialIdMatches(r.items[0]!)).toBe(false)
  })

  it('★ 非 cred 标签却带 credential_id 时不成立', () => {
    const r = api.unwrapSelfCheckRuns({
      items: [run({ model_name: 'cred-x', credential_id: 3 })],
      total: 1,
    })
    expect(api.selfCheckCredentialIdMatches(r.items[0]!)).toBe(false)
  })
})

describe('status 五值但统计只数三个', () => {
  const s = (o: Partial<ScStatsSummary>): ScStatsSummary => ({
    total_runs: 0, success_runs: 0, partial_runs: 0, failed_runs: 0, success_rate: 0, ...o,
  })

  it('三项之和等于 total 时没有在途 run', () => {
    expect(api.selfCheckHasInFlightRuns(s({ total_runs: 3, success_runs: 2, failed_runs: 1 }))).toBe(
      false,
    )
  })

  it('★ total 多出来时有 running/retrying 在途', () => {
    expect(
      api.selfCheckHasInFlightRuns(s({ total_runs: 5, success_runs: 2, partial_runs: 1, failed_runs: 1 })),
    ).toBe(true)
  })

  it('★ 三项之和恒不超过 total', () => {
    const x = s({ total_runs: 5, success_runs: 2, partial_runs: 1, failed_runs: 1 })
    expect(api.selfCheckCountedStatusTotal(x)).toBe(4)
  })

  it('★ total 为 0 时成功率无意义', () => {
    expect(api.selfCheckSuccessRateIsMeaningless(s({ total_runs: 0 }))).toBe(true)
  })

  it('★ 有 run 时成功率有意义（哪怕全失败）', () => {
    expect(
      api.selfCheckSuccessRateIsMeaningless(s({ total_runs: 3, success_runs: 0, failed_runs: 3 })),
    ).toBe(false)
  })

  it('★ 全失败与没跑过同值但判定不同', () => {
    const none = s({ total_runs: 0, success_rate: 0 })
    const allFailed = s({ total_runs: 3, success_runs: 0, failed_runs: 3, success_rate: 0 })
    expect(none.success_rate).toBe(allFailed.success_rate)
    expect(api.selfCheckSuccessRateIsMeaningless(none)).toBe(true)
    expect(api.selfCheckSuccessRateIsMeaningless(allFailed)).toBe(false)
  })
})

describe('range 回显的是请求值', () => {
  it('已知窗口被如实回显', () => {
    const st = api.unwrapSelfCheckStats(stats({ range: '7d' }))
    expect(api.selfCheckStatsRangeIsEffectiveRange(st)).toBe(true)
    expect(api.selfCheckStatsEffectiveRange(st)).toBe('7d')
  })

  it('★ 未知值被回显但实际窗口是 24h', () => {
    const st = api.unwrapSelfCheckStats(stats({ range: 'xyz' }))
    expect(st.range).toBe('xyz')
    expect(api.selfCheckStatsRangeIsEffectiveRange(st)).toBe(false)
    expect(api.selfCheckStatsEffectiveRange(st)).toBe('24h')
  })

  it('★ 空串也不是有效窗口', () => {
    const st = api.unwrapSelfCheckStats(stats({ range: '' }))
    expect(api.selfCheckStatsRangeIsEffectiveRange(st)).toBe(false)
  })
})

describe('probe_system 的健康判据', () => {
  const NOW = Date.parse('2026-10-07T09:30:00Z')
  const ps = (o: Partial<ScProbeSystem>): ScProbeSystem =>
    api.unwrapSelfCheckStats(stats({ probe_system: probeSystem(o) })).probe_system

  it('活动新鲜且无超期 ⇒ 健康', () => {
    expect(api.selfCheckProbeSystemHealthy(ps({}), NOW)).toBe(true)
  })

  it('★ 有超期未认领 ⇒ 不健康', () => {
    expect(api.selfCheckProbeSystemHealthy(ps({ queue_ready_unclaimable: 1 }), NOW)).toBe(false)
  })

  it('★ 正在跑 ⇒ 健康（哪怕活动很旧）', () => {
    expect(
      api.selfCheckProbeSystemHealthy(
        ps({ queue_running: 1, queue_last_activity_at: '2026-10-07T00:00:00Z' }),
        NOW,
      ),
    ).toBe(true)
  })

  it('★ 活动为 null ⇒ 健康（后端显式允许）', () => {
    // ★ `queue_running` 必须同时归零：判据是**短路链**，
    //   `queue_running > 0` 命中就 return，根本走不到 null 那支。
    expect(
      api.selfCheckProbeSystemHealthy(
        ps({ queue_running: 0, queue_last_activity_at: null }),
        NOW,
      ),
    ).toBe(true)
  })

  it('★ 活动超过 15 分钟且没在跑 ⇒ 不健康', () => {
    expect(
      api.selfCheckProbeSystemHealthy(
        ps({ queue_running: 0, queue_last_activity_at: '2026-10-07T09:00:00Z' }),
        NOW,
      ),
    ).toBe(false)
  })

  it('★ 恰好 15 分钟边界判为 stale（Go 是严格小于）', () => {
    // :960 `time.Since(*LastActivity) < 15*time.Minute`
    const at = new Date(NOW - api.SELF_CHECK_PROBE_STALE_MS).toISOString().replace('.000Z', 'Z')
    expect(api.selfCheckProbeSystemHealthy(ps({ queue_running: 0, queue_last_activity_at: at }), NOW)).toBe(
      false,
    )
  })

  it('★ 差一毫秒仍在阈值内', () => {
    const at = new Date(NOW - api.SELF_CHECK_PROBE_STALE_MS + 1).toISOString().replace('.000Z', 'Z')
    expect(api.selfCheckProbeSystemHealthy(ps({ queue_running: 0, queue_last_activity_at: at }), NOW)).toBe(
      true,
    )
  })

  it('★ 超期优先于正在跑', () => {
    expect(
      api.selfCheckProbeSystemHealthy(ps({ queue_running: 1, queue_ready_unclaimable: 2 }), NOW),
    ).toBe(false)
  })

  it('★ 时间戳解析不了 ⇒ 不健康（不能被坏值骗成绿灯）', () => {
    expect(
      api.selfCheckProbeSystemHealthy(
        ps({ queue_running: 0, queue_last_activity_at: 'not-a-time' }),
        NOW,
      ),
    ).toBe(false)
  })

  it('★ 两个查询都失败的全零形态被判成健康', () => {
    // ★★★ 这正是文件头第 (4) 条：查询失败 ⇒ 绿灯
    const zero = ps({
      queue_ready: 0,
      queue_ready_unclaimable: 0,
      queue_running: 0,
      due_states: 0,
      queue_last_activity_at: null,
      last_probe_attempt_at: null,
    })
    expect(api.selfCheckProbeSystemLooksUnqueried(zero)).toBe(true)
    expect(api.selfCheckProbeSystemHealthy(zero, NOW)).toBe(true)
  })

  it('★ 真实有数据时不被误判成「查不出来」', () => {
    expect(api.selfCheckProbeSystemLooksUnqueried(ps({}))).toBe(false)
  })

  it('executing 与后端是同一口径', () => {
    expect(api.selfCheckProbeSystemExecuting(ps({ queue_running: 2 }))).toBe(true)
    expect(api.selfCheckProbeSystemExecuting(ps({ queue_running: 0 }))).toBe(false)
  })
})

describe('降级区块的空数组二义', () => {
  it('★ 空数组默认只能说「看起来是空的」', () => {
    expect(api.selfCheckSectionLooksEmpty([])).toBe(true)
  })

  it('★ 已知区块缺席时才敢断言「确实没有」', () => {
    expect(api.selfCheckSectionLooksEmpty([], false)).toBe(true)
    expect(api.selfCheckSectionLooksEmpty([], true)).toBe(false)
  })

  it('有数据时两种说法都是 false', () => {
    const t = [{ timestamp: 'x', success_rate: 1, total: 1 }]
    expect(api.selfCheckSectionLooksEmpty(t)).toBe(false)
    expect(api.selfCheckSectionLooksEmpty(t, true)).toBe(false)
  })
})

describe('models 端点的口径', () => {
  it('★ 残差来自未计入的三态', () => {
    const m: ScModelInfo = { model_name: 'm', total: 5, success: 3, failed: 1 }
    expect(api.selfCheckModelCountsLeaveResidual(m)).toBe(1)
  })

  it('★ 残差为 0 时三项自洽', () => {
    const m: ScModelInfo = { model_name: 'm', total: 4, success: 3, failed: 1 }
    expect(api.selfCheckModelCountsLeaveResidual(m)).toBe(0)
  })

  it('★ models 是全时段，与 stats 的窗口口径不同', () => {
    expect(api.selfCheckModelsAreAllTime()).toBe(true)
  })
})

describe('upstream 结论', () => {
  it('★ 没测过', () => {
    expect(api.selfCheckUpstreamOutcome(api.unwrapSelfCheckRuns({ items: [runBare()], total: 1 }).items[0]!)).toBe(
      'not_tested',
    )
  })

  it('★ 测过但结果为空（键缺失与空串同义）', () => {
    expect(
      api.selfCheckUpstreamOutcome(
        api.unwrapSelfCheckRuns({ items: [run({ upstream_result: '' })], total: 1 }).items[0]!,
      ),
    ).toBe('result_empty')
  })

  it('★ 测过且有结果', () => {
    expect(
      api.selfCheckUpstreamOutcome(
        api.unwrapSelfCheckRuns({ items: [run({ upstream_result: 'ok' })], total: 1 }).items[0]!,
      ),
    ).toBe('result_present')
  })

  it('★ tested 为 false 时结果键即使存在也不算结论', () => {
    expect(
      api.selfCheckUpstreamOutcome(
        api.unwrapSelfCheckRuns({
          items: [run({ upstream_tested: false, upstream_result: 'ok' })],
          total: 1,
        }).items[0]!,
      ),
    ).toBe('not_tested')
  })
})

/* ═══════════════════════════════════════════════════════════════════════════
 * 取数
 * ═══════════════════════════════════════════════════════════════════════════ */

describe('取数', () => {
  beforeEach(() => {
    rq.mockReset()
  })

  it('★ runs 无参数时不带问号', async () => {
    ok({ items: [], total: 0 })
    await api.fetchSelfCheckRuns()
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/runs')
  })

  it('★ runs 带三个参数时按 model/status/limit 拼', async () => {
    ok({ items: [], total: 0 })
    await api.fetchSelfCheckRuns({ model: 'glm-5.2', status: 'failed', limit: 20 })
    expect(rq.mock.calls[0]![1]).toBe(
      '/api/self-check/runs?model=glm-5.2&status=failed&limit=20',
    )
  })

  it('★ runs 空字符串参数不进 query', async () => {
    ok({ items: [], total: 0 })
    await api.fetchSelfCheckRuns({ model: '', status: '' })
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/runs')
  })

  it('★ runs limit 为 0 也要发出去（让后端回落成 50）', async () => {
    ok({ items: [], total: 0 })
    await api.fetchSelfCheckRuns({ limit: 0 })
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/runs?limit=0')
  })

  it('★ run 详情打 id 路径', async () => {
    ok({ run: run(), rounds: [] })
    await api.fetchSelfCheckRunDetail(101)
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/runs/101')
  })

  it('★ run 详情非数字 id 原样发出（后端回 400）', async () => {
    ok({ run: run(), rounds: [] })
    await api.fetchSelfCheckRunDetail('abc')
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/runs/abc')
  })

  it('★★ id 含斜杠时按路径段编码（不能拼出别的路由）', async () => {
    ok({ run: run(), rounds: [] })
    await api.fetchSelfCheckRunDetail('a/b')
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/runs/a%2Fb')
  })

  it('settings 打裸路径', async () => {
    ok({
      enabled: true,
      normal_interval_seconds: 0,
      fault_interval_seconds: 0,
      model_source: 'both',
      max_models: 1,
      max_tokens_per_run: 1,
      featured_model_ids: [],
      updated_at: 'x',
    })
    await api.fetchSelfCheckSettings()
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/settings')
  })

  it('availability 打裸路径', async () => {
    ok({ available: true, new_probe_mode: true, mode: 'probe_queue' })
    await api.fetchSelfCheckTriggerAvailability()
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/trigger/availability')
  })

  it('★ stats 不传 range 时不带问号（后端缺省 24h）', async () => {
    ok(stats())
    await api.fetchSelfCheckStats()
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/stats')
  })

  it('★ stats 传 range 时带上', async () => {
    ok(stats({ range: '7d' }))
    await api.fetchSelfCheckStats('7d')
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/stats?range=7d')
  })

  it('★ stats 空串 range 也要发（后端会落 24h 并回显空串）', async () => {
    ok(stats({ range: '' }))
    await api.fetchSelfCheckStats('')
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/stats?range=')
  })

  it('models 打裸路径', async () => {
    ok({ models: [] })
    await api.fetchSelfCheckModels()
    expect(rq.mock.calls[0]![1]).toBe('/api/self-check/models')
  })

  it('★ 每个端点都过解包器', async () => {
    rq.mockResolvedValueOnce({ items: [], total: 0 })
    await api.fetchSelfCheckRuns()
    expect(rq.mock.calls[0]![0]).toBe('GET')
  })

  it('★ fetch 会拒绝形状不符的载荷', async () => {
    ok({ items: 'nope', total: 0 })
    await expect(api.fetchSelfCheckRuns()).rejects.toThrow(/items 不是数组/)
  })

  it('★ run 详情会拒绝形状不符的载荷', async () => {
    ok({ run: run(), rounds: 'nope' })
    await expect(api.fetchSelfCheckRunDetail(101)).rejects.toThrow(/rounds 不是数组/)
  })

  it('★ run 详情会拒绝缺 run 的载荷', async () => {
    ok({ rounds: [] })
    await expect(api.fetchSelfCheckRunDetail(101)).rejects.toThrow(/缺 1 个键（run）/)
  })

  it('★ stats 会拒绝缺 probe_system 的载荷', async () => {
    const s = stats()
    delete s.probe_system
    ok(s)
    await expect(api.fetchSelfCheckStats()).rejects.toThrow(/缺 1 个键（probe_system）/)
  })

  it('★ settings 会拒绝信封形状', async () => {
    ok({ success: true, data: {} })
    await expect(api.fetchSelfCheckSettings()).rejects.toThrow(/缺 8 个键/)
  })

  it('★ models 会拒绝 null 数组', async () => {
    ok({ models: null })
    await expect(api.fetchSelfCheckModels()).rejects.toThrow(/models 不是数组/)
  })

  it('★ availability 会拒绝缺恒在键的载荷', async () => {
    ok({ available: true })
    await expect(api.fetchSelfCheckTriggerAvailability()).rejects.toThrow(
      /缺 1 个键（new_probe_mode）/,
    )
  })
})