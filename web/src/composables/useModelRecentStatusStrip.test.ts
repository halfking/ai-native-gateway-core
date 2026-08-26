import { describe, expect, it } from 'vitest'
import type { LiveStreamLane, LiveStreamTile } from './liveStreamStore'
import {
  RECENT_STATUS_STRIP_LIMIT,
  recentTilesForGroup,
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

describe('recentTilesForGroup', () => {
  it('returns empty when no lanes or no group keys', () => {
    expect(recentTilesForGroup({ model: 'gpt-4o' }, [])).toEqual([])
    expect(recentTilesForGroup({ model: 'gpt-4o' }, null)).toEqual([])
    expect(recentTilesForGroup({ model: '  ' }, [lane('gpt-4o', [tile({ request_id: 'r1' })])])).toEqual([])
  })

  it('matches lane by id / name / alias / displayName (case-insensitive)', () => {
    const lanes = [
      lane('gpt-4o', [tile({ request_id: 'r1', timestamp: '2026-08-27T00:00:01Z' })]),
      lane('claude-sonnet', [tile({ request_id: 'r2', model: 'claude-sonnet', timestamp: '2026-08-27T00:00:02Z' })]),
    ]
    expect(recentTilesForGroup({ model: 'GPT-4O' }, lanes).map(t => t.request_id)).toEqual(['r1'])
    expect(recentTilesForGroup(
      { model: 'scope', displayName: 'Claude-Sonnet', aliases: [] },
      lanes,
    ).map(t => t.request_id)).toEqual(['r2'])
    expect(recentTilesForGroup(
      { model: 'scope', aliases: ['claude-sonnet'] },
      lanes,
    ).map(t => t.request_id)).toEqual(['r2'])
  })

  it('merges multiple matching lanes and sorts by timestamp ascending', () => {
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
    const ids = recentTilesForGroup(
      { model: 'gpt-4o', aliases: ['gpt-4o-2024'] },
      lanes,
    ).map(t => t.request_id)
    expect(ids).toEqual(['old', 'mid', 'new'])
  })

  it('keeps only the last RECENT_STATUS_STRIP_LIMIT tiles', () => {
    const requests = Array.from({ length: RECENT_STATUS_STRIP_LIMIT + 10 }, (_, i) =>
      tile({
        request_id: `r${i}`,
        timestamp: `2026-08-27T00:00:${String(i).padStart(2, '0')}Z`,
      }),
    )
    const result = recentTilesForGroup({ model: 'gpt-4o' }, [lane('gpt-4o', requests)])
    expect(result).toHaveLength(RECENT_STATUS_STRIP_LIMIT)
    expect(result[0].request_id).toBe('r10')
    expect(result[result.length - 1].request_id).toBe(`r${RECENT_STATUS_STRIP_LIMIT + 9}`)
  })

  it('dedupes by request_id keeping the last write', () => {
    const lanes = [
      lane('gpt-4o', [
        tile({ request_id: 'r1', status: 'in_progress', timestamp: '2026-08-27T00:00:01Z' }),
        tile({ request_id: 'r1', status: 'success', timestamp: '2026-08-27T00:00:02Z' }),
      ]),
    ]
    const result = recentTilesForGroup({ model: 'gpt-4o' }, lanes)
    expect(result).toHaveLength(1)
    expect(result[0].status).toBe('success')
  })
})
