import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchLiveStreamStats,
  liveStreamStatsNotWired,
  type LiveStreamStats,
} from './liveStream'
import { resetUnauthorizedForTests } from './client'

/**
 * liveStream（GET /api/admin/live-stream/stats）契约护栏（2026-10-29）。
 *
 * 判据钉住「响应形状」而不是「页面怎么画」：
 *  ① ★★★★★ 成功响应是**裸 map**（handleLiveStreamStats 直接 Encode(stats)）——
 *     没有 error 信封、没有 data 包裹 ⇒ 类型必须是顶层平铺的键。
 *  ② ★★★★★ 计数键恒在（Go 侧填 map 无 omitempty）⇒ 真实响应缺任何
 *     一个声明键都是契约漂移（不是可选字段）⇒ 用 keys 全等断言钉死键集。
 *  ③ ★★★ 503（hub 未装配）体是 `{"error": "..."}` ⇒ liveStreamStatsNotWired
 *     按 status 判，与「空列表」语义分开。
 *  ④ ★★ 本模块**只有 GET**：POST /trigger-snapshot 是 TEMPORARY DEBUG 端点，
 *     页面与模块都不得引用（防误用，静态断言）。
 */

const SAMPLE: LiveStreamStats = {
  active_clients: 0,
  active_scope_subscriptions: 0,
  total_connections: 0,
  total_disconnections: 0,
  auth_failures: 0,
  broadcast_count: 889,
  broadcast_drops: 0,
  cached_snapshot_baseline_present: 171,
  cached_snapshot_baseline_absent: 1,
  cached_snapshot_empty_skips: 0,
  cached_snapshot_evictions: 0,
  cached_snapshot_entries: 1,
  last_activity: '2026-10-29T10:00:00Z',
  seconds_since_activity: 3.2,
  lifecycle_actions_delivered: 0,
  lifecycle_action_scan_errors: 0,
  record_drops: 0,
}

/** Hub.Stats()（live_stream_sse.go:3229-3264）填充的键，逐一对拍 Go 源码。 */
const GO_STATS_KEYS = [
  'active_clients',
  'active_scope_subscriptions',
  'total_connections',
  'total_disconnections',
  'auth_failures',
  'broadcast_count',
  'broadcast_drops',
  'cached_snapshot_baseline_present',
  'cached_snapshot_baseline_absent',
  'cached_snapshot_empty_skips',
  'cached_snapshot_evictions',
  'cached_snapshot_entries',
  'last_activity',
  'seconds_since_activity',
  'lifecycle_actions_delivered',
  'lifecycle_action_scan_errors',
  'record_drops',
] as const

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

afterEach(() => {
  vi.unstubAllGlobals()
  resetUnauthorizedForTests()
})

describe('liveStream stats 契约', () => {
  it('①② 键集与 Go Hub.Stats() 逐键全等（缺键=契约漂移必红）', async () => {
    expect(Object.keys(SAMPLE).sort()).toEqual([...GO_STATS_KEYS].sort())
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(200, SAMPLE)))
    const got = await fetchLiveStreamStats()
    expect(got).toEqual(SAMPLE)
  })

  it('③ 503 → liveStreamStatsNotWired 为真（401/500/网络错为假）', async () => {
    expect(liveStreamStatsNotWired(Object.assign(new Error('x'), { status: 503 }))).toBe(true)
    expect(liveStreamStatsNotWired(Object.assign(new Error('x'), { status: 500 }))).toBe(false)
    expect(liveStreamStatsNotWired(Object.assign(new Error('x'), { status: 0 }))).toBe(false)
    expect(liveStreamStatsNotWired(null)).toBe(false)
  })

  it('③b 503 响应体是 {"error": ...}（裸错误串，不是信封）也能被解析路径抛出', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(503, { error: 'Live stream hub not initialized' })))
    await expect(fetchLiveStreamStats()).rejects.toMatchObject({ status: 503 })
  })

  it('④ 模块不得引用 trigger-snapshot（TEMPORARY DEBUG 端点，只 GET /stats）', async () => {
    const { readFileSync } = await import('node:fs')
    const { join } = await import('node:path')
    const src = readFileSync(join(process.cwd(), 'src/api/liveStream.ts'), 'utf8')
    expect(src).toContain("'/api/admin/live-stream/stats'")
    expect(src).not.toMatch(/req\w*\(\s*'(POST|PUT|DELETE)'/)
    expect(src).not.toContain("'/api/admin/live-stream'")
  })
})
