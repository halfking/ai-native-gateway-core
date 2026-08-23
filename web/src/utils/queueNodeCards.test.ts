import { describe, expect, it } from 'vitest'
import {
  assignSpacedPriorities,
  cardWidthFromCapacity,
  compareRoutingCandidates,
  credentialDisplayName,
  mergeCardWindowEntries,
  nodeCapacity,
  orderNodesByRoutingCandidates,
  quotaAllowsPriority,
  sortRoutingCandidates,
} from './queueNodeCards'

describe('assignSpacedPriorities', () => {
  it('uses step 5 for small lists', () => {
    expect(assignSpacedPriorities(3)).toEqual([5, 10, 15])
  })

  it('compresses when count*5 exceeds 99', () => {
    const got = assignSpacedPriorities(25)
    expect(got).toHaveLength(25)
    expect(new Set(got).size).toBe(25)
    expect(Math.max(...got)).toBeLessThanOrEqual(99)
    expect(Math.min(...got)).toBeGreaterThanOrEqual(1)
  })

  it('fills [1,99] uniquely at max capacity', () => {
    const got = assignSpacedPriorities(99)
    expect(got).toHaveLength(99)
    expect(new Set(got).size).toBe(99)
  })

  it('rejects count above max', () => {
    expect(() => assignSpacedPriorities(100)).toThrow(/unique priorities/)
  })
})

describe('cardWidthFromCapacity', () => {
  it('maps relative capacity into [min,max]', () => {
    expect(cardWidthFromCapacity(1, 10, 120, 280)).toBe(136)
    expect(cardWidthFromCapacity(10, 10, 120, 280)).toBe(280)
    expect(cardWidthFromCapacity(0, 10, 120, 280)).toBe(136)
  })
})

describe('nodeCapacity / credentialDisplayName', () => {
  it('prefers effective concurrency then limit', () => {
    expect(nodeCapacity({ effective_concurrency: 8, concurrency_limit: 20 })).toBe(8)
    expect(nodeCapacity({ effective_concurrency: null, concurrency_limit: 4 })).toBe(4)
    expect(nodeCapacity(null)).toBe(1)
  })

  it('prefers credential label', () => {
    expect(credentialDisplayName({ credential_label: 'terra-prod' }, 'p', 9)).toBe('terra-prod')
    expect(credentialDisplayName({ credential_label: '  ' }, 'anthropic', 9)).toBe('anthropic · #9')
    expect(credentialDisplayName(null, 'anthropic', 9, 'sse-label')).toBe('sse-label')
  })
})

describe('compareRoutingCandidates / orderNodesByRoutingCandidates', () => {
  it('sorts by manual_priority ASC, not credential_id', () => {
    const candidates = sortRoutingCandidates([
      { credential_id: 10, manual_priority: 15 },
      { credential_id: 5, manual_priority: 5 },
      { credential_id: 7, manual_priority: 10 },
    ])
    expect(candidates.map(c => c.credential_id)).toEqual([5, 7, 10])
  })

  it('orders live nodes using resolve rank even when credential_id is inverted', () => {
    const byId = new Map([
      [10, { credential_id: 10, manual_priority: 15 }],
      [5, { credential_id: 5, manual_priority: 5 }],
    ])
    const ordered = orderNodesByRoutingCandidates(
      [{ credential_id: 10 }, { credential_id: 5 }],
      byId,
    )
    expect(ordered.map(n => n.credential_id)).toEqual([5, 10])
  })

  it('prefers priority flag when quota allows routing', () => {
    expect(compareRoutingCandidates(
      { credential_id: 1, manual_priority: 99, priority: true, quota_state: 'ok' },
      { credential_id: 2, manual_priority: 1, priority: false, quota_state: 'ok' },
    )).toBeLessThan(0)
  })
})
describe('quotaAllowsPriority', () => {
  it('allows ok and empty quota', () => {
    expect(quotaAllowsPriority(null)).toBe(true)
    expect(quotaAllowsPriority(undefined)).toBe(true)
    expect(quotaAllowsPriority('')).toBe(true)
    expect(quotaAllowsPriority('ok')).toBe(true)
    expect(quotaAllowsPriority('OK')).toBe(true)
  })

  it('blocks exhausted quota states', () => {
    expect(quotaAllowsPriority('balance_exhausted')).toBe(false)
    expect(quotaAllowsPriority('unknown')).toBe(false)
  })
})

describe('mergeCardWindowEntries', () => {
  it('replaces previous cells when the next payload omits entries', () => {
    const dest = new Map<string, number[]>([['1:m', [1, 2, 3]]])
    mergeCardWindowEntries(dest, '1:m', undefined)
    expect(dest.get('1:m')).toEqual([])
  })

  it('writes an empty window and truncates to the card limit', () => {
    const dest = new Map<string, number[]>()
    mergeCardWindowEntries(dest, '1:m', [])
    expect(dest.get('1:m')).toEqual([])
    mergeCardWindowEntries(dest, '1:m', [1, 2, 3, 4], 2)
    expect(dest.get('1:m')).toEqual([1, 2])
  })
})
