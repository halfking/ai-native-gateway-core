import { describe, expect, it } from 'vitest'
import { CREDENTIAL_LIFECYCLE_STATUSES } from './providers'

describe('credential lifecycle contract', () => {
  it('keeps the database-backed four-state lifecycle separate from other state domains', () => {
    expect(CREDENTIAL_LIFECYCLE_STATUSES).toEqual([
      'active',
      'disabled',
      'suspended',
      'retired',
    ])
    expect(CREDENTIAL_LIFECYCLE_STATUSES).not.toContain('deprecated')
    expect(CREDENTIAL_LIFECYCLE_STATUSES).not.toContain('test')
  })
})
