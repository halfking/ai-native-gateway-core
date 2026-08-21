import { describe, expect, it } from 'vitest'
import {
  assignSpacedPriorities,
  cardWidthFromCapacity,
  credentialDisplayName,
  nodeCapacity,
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
  })
})
