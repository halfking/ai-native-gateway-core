import { describe, expect, it } from 'vitest'
import type { LiveStreamLane, LiveStreamTile } from './liveStreamStore'
import {
  RECENT_STATUS_STRIP_LIMIT,
  recentIOForGroup,
} from './useModelRecentStatusStrip'

function tile(partial: Partial<LiveStreamTile> & { request_id: string }): LiveStreamTile {
  return {
    timestamp: '2026-08-27T00:00:00Z',
    model: 'gpt-4o',
    vendor: 'openai',
    provider: 'openai-official',
    status: 'success',
    ...partial,
  }
}

function lane(id: string, requests: LiveStreamTile[]): LiveStreamLane {
  return {
    id,
    name: id,
    dimension: 'model',
    requests,
    stats: { total: requests.length, success: 0, failure: 0 },
    isOthers: false,
  }
}

describe('recentIOForGroup（输入/输出双队列）', () => {
  it('returns empty IO when no lanes or no group keys', () => {
    expect(recentIOForGroup({ model: 'gpt-4o' }, [])).toMatchObject({ inflight: [], terminal: [], successRate: null })
    expect(recentIOForGroup({ model: 'gpt-4o' }, null)).toMatchObject({ inflight: [], terminal: [], successRate: null })
    expect(recentIOForGroup({ model: '  ' }, [lane('gpt-4o', [tile({ request_id: 'r1' })])])).toMatchObject({ inflight: [], terminal: [], successRate: null })
  })

  it('matches lane by id / name / alias / displayName (case-insensitive)', () => {
    const lanes = [
      lane('gpt-4o', [tile({ request_id: 'r1', timestamp: '2026-08-27T00:00:01Z' })]),
      lane('claude-sonnet', [tile({ request_id: 'r2', model: 'claude-sonnet', timestamp: '2026-08-27T00:00:02Z' })]),
    ]
    expect(recentIOForGroup({ model: 'GPT-4O' }, lanes).terminal.map(t => t.request_id)).toEqual(['r1'])
    expect(recentIOForGroup(
      { model: 'scope', displayName: 'Claude-Sonnet', aliases: [] },
      lanes,
    ).terminal.map(t => t.request_id)).toEqual(['r2'])
    expect(recentIOForGroup(
      { model: 'scope', aliases: ['claude-sonnet'] },
      lanes,
    ).terminal.map(t => t.request_id)).toEqual(['r2'])
  })

  it('merges multiple matching lanes keeping timestamp order across aliases', () => {
    const lanes = [
      lane('gpt-4o', [
        tile({ request_id: 'old', timestamp: '2026-08-27T00:00:01Z' }),
        tile({ request_id: 'mid', timestamp: '2026-08-27T00:00:03Z' }),
      ]),
      {
        ...lane('gpt-4o-2024', [
          tile({ request_id: 'new', model: 'gpt-4o-2024', timestamp: '2026-08-27T00:00:05Z' }),
        ]),
        name: 'gpt-4o',
      },
    ]
    const ids = recentIOForGroup(
      { model: 'gpt-4o', aliases: ['gpt-4o-2024'] },
      lanes,
    ).terminal.map(t => t.request_id)
    expect(ids).toEqual(['old', 'mid', 'new'])
  })

  it('keeps only the last RECENT_STATUS_STRIP_LIMIT tiles (limit applies before split)', () => {
    const requests = Array.from({ length: RECENT_STATUS_STRIP_LIMIT + 10 }, (_, i) =>
      tile({
        request_id: `r${i}`,
        timestamp: `2026-08-27T00:${String(Math.floor(i / 60)).padStart(2, '0')}:${String(i % 60).padStart(2, '0')}Z`,
      }),
    )
    const result = recentIOForGroup({ model: 'gpt-4o' }, [lane('gpt-4o', requests)])
    expect(result.terminal).toHaveLength(RECENT_STATUS_STRIP_LIMIT)
    expect(result.terminal[0].request_id).toBe('r10')
    expect(result.terminal[result.terminal.length - 1].request_id).toBe(`r${RECENT_STATUS_STRIP_LIMIT + 9}`)
  })

  it('splits tiles into inflight queue and client-terminal results', () => {
    const lanes = [
      lane('gpt-4o', [
        tile({ request_id: 'r-in', status: 'in_progress', timestamp: '2026-09-01T00:00:01Z' }),
        tile({ request_id: 'r-ok', status: 'success', timestamp: '2026-09-01T00:00:02Z' }),
        tile({ request_id: 'r-bad', status: 'failure', error_kind: 'upstream_5xx', timestamp: '2026-09-01T00:00:03Z' }),
        tile({ request_id: 'r-rate', status: 'rate_limited', timestamp: '2026-09-01T00:00:04Z' }),
        tile({ request_id: 'idle-model', status: 'idle', timestamp: '2026-09-01T00:00:05Z' }),
      ]),
    ]
    const io = recentIOForGroup({ model: 'gpt-4o' }, lanes)
    expect(io.inflight.map(t => t.request_id)).toEqual(['r-in'])
    expect(io.terminal.map(t => t.request_id)).toEqual(['r-ok', 'r-bad', 'r-rate'])
    expect(io.success).toBe(1)
    expect(io.failed).toBe(2)
    expect(io.successRate).toBeCloseTo(1 / 3)
  })

  it('keeps only the final outcome when a retried request transitions in place', () => {
    // 上游失败→网关重试成功：同一 request_id 的 tile 从 in_progress 更新为
    // success。拆分必须发生在去重之后，输出侧只保留最终 success。
    const lanes = [
      lane('gpt-4o', [
        tile({ request_id: 'r1', status: 'in_progress', timestamp: '2026-09-01T00:00:01Z' }),
        tile({ request_id: 'r1', status: 'success', timestamp: '2026-09-01T00:00:02Z' }),
      ]),
    ]
    const io = recentIOForGroup({ model: 'gpt-4o' }, lanes)
    expect(io.inflight).toHaveLength(0)
    expect(io.terminal.map(t => t.status)).toEqual(['success'])
    expect(io.successRate).toBe(1)
  })

  it('excludes synthetic probe tiles from both sides', () => {
    const lanes = [
      lane('gpt-4o', [
        tile({ request_id: 'p-in', status: 'in_progress', is_probe: true, timestamp: '2026-09-01T00:00:01Z' }),
        tile({ request_id: 'p-ok', status: 'success', is_probe: true, timestamp: '2026-09-01T00:00:02Z' }),
        tile({ request_id: 'r-ok', status: 'success', timestamp: '2026-09-01T00:00:03Z' }),
      ]),
    ]
    const io = recentIOForGroup({ model: 'gpt-4o' }, lanes)
    expect(io.inflight).toHaveLength(0)
    expect(io.terminal.map(t => t.request_id)).toEqual(['r-ok'])
  })

  it('returns null successRate (not 0%/100%) when no terminal requests exist', () => {
    const lanes = [
      lane('gpt-4o', [
        tile({ request_id: 'r-in', status: 'in_progress', timestamp: '2026-09-01T00:00:01Z' }),
      ]),
    ]
    const io = recentIOForGroup({ model: 'gpt-4o' }, lanes)
    expect(io.terminal).toHaveLength(0)
    expect(io.successRate).toBeNull()
    expect(io.failed).toBe(0)
  })
})
